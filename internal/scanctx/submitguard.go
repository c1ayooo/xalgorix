package scanctx

// submitguard.go — per-scan platform submission discipline ledger
// (BENCHMARK_ROUND5_ANALYSIS §7 P0-2).
//
// Round 5 sent 449 non-flags out of 515 submit calls: probe strings, invented
// challenge ids and shell loops. That rule used to live only in the planner
// prompt, so it was not enforced. The engine now decides in code, and it needs
// three facts that must be shared by every lane of one scan:
//
//   - what this run has actually OBSERVED (tool output text), so a flag cannot
//     be submitted from model memory (charter §3.2 observed/inferred, §4.4 no
//     recollection bypass);
//   - which challenge codes the platform itself listed (/challenges), so a
//     submit to an invented code is refusable;
//   - which submissions already happened, so a completed challenge is not
//     re-submitted and a shell loop cannot hammer the endpoint.
//
// This is per-scan scratch state routed through ScanContext (AGENTS.md
// invariant 1). It is never a state authority, never persisted, and it never
// decides whether a vulnerability exists — that stays with the report gate and
// the Verifier.

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// submitGuardObservedLimit bounds the retained tail of observed tool
	// output. Provenance only needs recent output; a pathological target must
	// not grow this without limit.
	submitGuardObservedLimit = 1 << 20 // 1 MiB
	// submitGuardCatalogLimit bounds the per-scan challenge catalog.
	submitGuardCatalogLimit = 4096
	// submitGuardBudgetWindow / submitGuardBudgetMax bound submit attempts per
	// scan. Round 5 sent 195 submits in 9 minutes from one intent; a real run
	// needs a handful per hour.
	submitGuardBudgetWindow = 2 * time.Minute
	submitGuardBudgetMax    = 5
	// submitGuardCooldown is the minimum gap between two attempts for the same
	// challenge code.
	submitGuardCooldown = 20 * time.Second
)

var (
	// catalogJSONRE matches the platform's /challenges payload.
	catalogJSONRE = regexp.MustCompile(`(?i)"unique_code"\s*:\s*"([A-Za-z0-9][A-Za-z0-9._:-]{1,79})"`)
	// catalogFormRE matches a form/query rendering of the same field.
	catalogFormRE = regexp.MustCompile(`(?i)unique_code["']?\s*[=:]\s*["']?([A-Za-z0-9][A-Za-z0-9._:-]{1,79})`)
)

// looksLikeChallengeCatalog gates catalog learning: only an actual challenge
// LISTING teaches the code set. A tool output that merely echoes a code the
// model typed (`unique_code=c-25`) must not become a catalog entry, otherwise
// the check could always be satisfied by the very value it is meant to reject
// (round 5 submitted to invented codes such as a-19..a-25 and "web").
func looksLikeChallengeCatalog(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, `"challenges"`) ||
		strings.Contains(lower, "/challenges") ||
		strings.Contains(lower, "challenge_list")
}

// SubmissionVerdict is the engine's decision on one submit attempt.
type SubmissionVerdict struct {
	Allowed bool
	Reason  string
}

type submissionRecord struct {
	last      time.Time
	attempts  int
	completed bool
}

// SubmitGuard is the per-scan submission discipline ledger. The zero value is
// usable; NewSubmitGuard is the constructor the scan context uses.
type SubmitGuard struct {
	mu       sync.Mutex
	observed []byte
	catalog  map[string]bool
	records  map[string]*submissionRecord
	recent   []time.Time
	budget   int
	window   time.Duration
	cooldown time.Duration
}

// NewSubmitGuard returns an empty ledger with the default bounds.
func NewSubmitGuard() *SubmitGuard {
	return &SubmitGuard{
		catalog:  make(map[string]bool),
		records:  make(map[string]*submissionRecord),
		budget:   submitGuardBudgetMax,
		window:   submitGuardBudgetWindow,
		cooldown: submitGuardCooldown,
	}
}

// Observe appends one tool output to the observed tail and learns any
// challenge catalog entries it carries. Empty input is a no-op.
func (g *SubmitGuard) Observe(output string) {
	if g == nil || strings.TrimSpace(output) == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observed = append(g.observed, output...)
	g.observed = append(g.observed, '\n')
	if len(g.observed) > submitGuardObservedLimit {
		g.observed = g.observed[len(g.observed)-submitGuardObservedLimit:]
	}
	if g.catalog == nil {
		g.catalog = make(map[string]bool)
	}
	if len(g.catalog) < submitGuardCatalogLimit && looksLikeChallengeCatalog(output) {
		for _, m := range catalogJSONRE.FindAllStringSubmatch(output, -1) {
			g.catalog[m[1]] = true
		}
		for _, m := range catalogFormRE.FindAllStringSubmatch(output, -1) {
			g.catalog[m[1]] = true
		}
	}
}

// Observed reports whether needle appears in the retained observed output.
// It is the provenance check: a flag that no tool ever printed was not
// observed, whatever the model believes.
func (g *SubmitGuard) Observed(needle string) bool {
	if g == nil {
		return false
	}
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return strings.Contains(string(g.observed), needle)
}

// CatalogContains reports whether the platform listed code in this scan.
func (g *SubmitGuard) CatalogContains(code string) bool {
	if g == nil {
		return false
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.catalog[code]
}

// CatalogSize reports how many challenge codes this scan has learned.
func (g *SubmitGuard) CatalogSize() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.catalog)
}

// Reserve decides one submit attempt and records it when allowed. Blocked
// attempts do not consume budget: a rejected call must not use up the window
// that a real submission needs.
func (g *SubmitGuard) Reserve(code string, now time.Time) SubmissionVerdict {
	if g == nil {
		return SubmissionVerdict{Allowed: false, Reason: "submission guard unavailable"}
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return SubmissionVerdict{Allowed: false, Reason: "submit without a unique_code"}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if now.IsZero() {
		now = time.Now()
	}
	if g.records == nil {
		g.records = make(map[string]*submissionRecord)
	}
	if rec := g.records[code]; rec != nil {
		if rec.completed {
			return SubmissionVerdict{Allowed: false, Reason: "challenge " + code + " was already submitted successfully; do not submit it again"}
		}
		cooldown := g.cooldown
		if cooldown <= 0 {
			cooldown = submitGuardCooldown
		}
		if !rec.last.IsZero() && now.Sub(rec.last) < cooldown {
			return SubmissionVerdict{
				Allowed: false,
				Reason:  "challenge " + code + " was submitted " + now.Sub(rec.last).Round(time.Second).String() + " ago; wait before retrying",
			}
		}
	}
	window := g.window
	if window <= 0 {
		window = submitGuardBudgetWindow
	}
	kept := g.recent[:0]
	for _, at := range g.recent {
		if now.Sub(at) < window {
			kept = append(kept, at)
		}
	}
	g.recent = kept
	max := g.budget
	if max <= 0 {
		max = submitGuardBudgetMax
	}
	if len(g.recent) >= max {
		return SubmissionVerdict{
			Allowed: false,
			Reason:  "submit rate limit: " + strconv.Itoa(len(g.recent)) + " attempts in the last " + window.String() + " (max " + strconv.Itoa(max) + ")",
		}
	}
	rec := g.records[code]
	if rec == nil {
		rec = &submissionRecord{}
		g.records[code] = rec
	}
	rec.last = now
	rec.attempts++
	g.recent = append(g.recent, now)
	return SubmissionVerdict{Allowed: true}
}

// MarkCompleted records that the platform accepted code.
func (g *SubmitGuard) MarkCompleted(code string) {
	if g == nil {
		return
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.records == nil {
		g.records = make(map[string]*submissionRecord)
	}
	rec := g.records[code]
	if rec == nil {
		rec = &submissionRecord{}
		g.records[code] = rec
	}
	rec.completed = true
}

// Completed reports whether code was accepted earlier in this scan.
func (g *SubmitGuard) Completed(code string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	rec := g.records[strings.TrimSpace(code)]
	return rec != nil && rec.completed
}

// Attempts reports how many times code was submitted (allowed attempts only).
func (g *SubmitGuard) Attempts(code string) int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if rec := g.records[strings.TrimSpace(code)]; rec != nil {
		return rec.attempts
	}
	return 0
}

// ObservedBytes reports the retained provenance tail size, for tests and the
// audit view.
func (g *SubmitGuard) ObservedBytes() int {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.observed)
}
