// egress.go — Engagement-wide egress budget (R4).
//
// One Budget lives on the ScanContext, so the root agent and every worker
// share it: per-origin rate, global/per-host concurrency, total request
// budget, pause and stop are all counted once per engagement. Tools acquire a
// ticket before sending and report the response status on release; a 429 pauses
// the origin for the target's Retry-After, repeated 5xx/transport failures pause
// it briefly, and an exhausted/stopped budget refuses further traffic.
//
// The Budget is a policy component, not a state authority: it never edits
// hypotheses, intents, findings or the graph. State changes are audited through
// the ledger journal by the owning ScanContext.

package scanctx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// EgressPolicy is the per-scan egress configuration.
type EgressPolicy struct {
	GlobalRPS          float64
	Burst              int
	PerHostRPS         float64
	MaxConcurrency     int
	PerHostConcurrency int
	TotalBudget        int64 // 0 = unlimited
	Source             string
}

// Enabled reports whether any limit is configured.
func (p EgressPolicy) Enabled() bool {
	return p.GlobalRPS > 0 || p.PerHostRPS > 0 || p.MaxConcurrency > 0 || p.TotalBudget > 0
}

// Egress errors are soft: callers surface them as a tool Output, never as a
// Result.Error (the circuit breaker must not trip on a policy pause).
var (
	ErrEgressStopped         = errors.New("egress stopped")
	ErrEgressBudgetExhausted = errors.New("egress request budget exhausted")
	ErrEgressPaused          = errors.New("egress paused")
	ErrEgressThrottled       = errors.New("egress throttled")
)

// EgressSnapshot is the read-only accounting view.
type EgressSnapshot struct {
	Requests      int64
	Active        int
	Paused        bool
	PauseReason   string
	PauseUntil    time.Time
	Stopped       bool
	StopReason    string
	Budget        int64
	BudgetUsed    int64
	PerHost       map[string]int
	StatusClasses map[string]int64
}

type hostBudget struct {
	limiter        *rate.Limiter
	active         int
	consecutive5xx int
	pausedUntil    time.Time
	pauseReason    string
	sem            chan struct{}
}

// EgressBudget is the shared ticket dispenser for one scan.
type EgressBudget struct {
	mu     sync.Mutex
	policy EgressPolicy
	global *rate.Limiter
	hosts  map[string]*hostBudget

	used          int64
	active        int
	statusClasses map[string]int64
	pausedUntil   time.Time
	pauseReason   string
	stopped       bool
	stopReason    string

	globalSem chan struct{}
	audit     func(operation, summary string)
}

// NewEgressBudget builds a budget. audit may be nil.
func NewEgressBudget(policy EgressPolicy, audit func(operation, summary string)) *EgressBudget {
	if policy.Burst < 1 {
		policy.Burst = 1
	}
	b := &EgressBudget{policy: policy, hosts: map[string]*hostBudget{}, audit: audit, statusClasses: map[string]int64{}}
	if policy.GlobalRPS > 0 {
		b.global = rate.NewLimiter(rate.Limit(policy.GlobalRPS), policy.Burst)
	}
	if policy.MaxConcurrency > 0 {
		b.globalSem = make(chan struct{}, policy.MaxConcurrency)
	}
	return b
}

// Policy returns the effective policy.
func (b *EgressBudget) Policy() EgressPolicy {
	if b == nil {
		return EgressPolicy{}
	}
	return b.policy
}

// Ticket is one acquired unit of egress capacity.
type Ticket struct {
	b       *EgressBudget
	host    string
	hostSem chan struct{}
	once    sync.Once
}

// EgressHost extracts a normalized host key from a URL, host:port or host.
func EgressHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			return strings.ToLower(u.Hostname())
		}
	}
	if host, _, err := net.SplitHostPort(raw); err == nil && host != "" {
		return strings.ToLower(host)
	}
	return strings.ToLower(raw)
}

// Acquire waits for a rate token and a concurrency slot, or returns a soft
// policy error. host may be empty for tools that cannot attribute a single
// origin (a shell command fanning out); they still consume the global budget.
func (b *EgressBudget) Acquire(ctx context.Context, host string) (*Ticket, error) {
	if b == nil || !b.policy.Enabled() {
		return &Ticket{}, nil
	}
	host = EgressHost(host)
	if ctx == nil {
		ctx = context.Background()
	}
	if err := b.checkPolicy(host); err != nil {
		return nil, err
	}
	// Rate first, so a waiting request does not hold a concurrency slot.
	if b.global != nil {
		if err := b.global.Wait(ctx); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEgressThrottled, err)
		}
	}
	hb := b.hostState(host)
	if hb != nil && hb.limiter != nil {
		if err := hb.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrEgressThrottled, err)
		}
	}
	// Pause may have been set while waiting for a token.
	if err := b.checkPolicy(host); err != nil {
		return nil, err
	}
	t := &Ticket{b: b, host: host}
	if b.globalSem != nil {
		select {
		case b.globalSem <- struct{}{}:
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %v", ErrEgressThrottled, ctx.Err())
		}
	}
	if hb != nil && hb.perHostSem() != nil {
		select {
		case hb.perHostSem() <- struct{}{}:
			t.hostSem = hb.perHostSem()
		case <-ctx.Done():
			if b.globalSem != nil {
				<-b.globalSem
			}
			return nil, fmt.Errorf("%w: %v", ErrEgressThrottled, ctx.Err())
		}
	}
	b.mu.Lock()
	if b.policy.TotalBudget > 0 && b.used >= b.policy.TotalBudget {
		b.mu.Unlock()
		t.releaseSlots()
		return nil, ErrEgressBudgetExhausted
	}
	b.active++
	b.used++
	if hb := b.hosts[host]; hb != nil {
		hb.active++
	}
	b.mu.Unlock()
	return t, nil
}

// Done releases the slot and reports the response status. retryAfter is the
// target's Retry-After when known; status <= 0 means no HTTP status (terminal).
func (t *Ticket) Done(status int, retryAfter time.Duration) {
	if t == nil || t.b == nil {
		return
	}
	t.once.Do(func() {
		b := t.b
		b.mu.Lock()
		if b.active > 0 {
			b.active--
		}
		hb := b.hosts[t.host]
		if hb != nil && hb.active > 0 {
			hb.active--
		}
		var pause time.Duration
		var reason string
		switch {
		case status == 429:
			if retryAfter <= 0 {
				retryAfter = time.Minute
			}
			if retryAfter > 30*time.Minute {
				retryAfter = 30 * time.Minute
			}
			pause, reason = retryAfter, "target rate-limited (HTTP 429)"
			if hb != nil {
				hb.consecutive5xx = 0
			}
		case status >= 500:
			if hb != nil {
				hb.consecutive5xx++
				if hb.consecutive5xx >= 3 {
					pause, reason = 30*time.Second, fmt.Sprintf("target returned %d three times", status)
					hb.consecutive5xx = 0
				}
			}
		case status >= 200 && status < 400:
			if hb != nil {
				hb.consecutive5xx = 0
			}
		}
		if status > 0 {
			class := "2xx"
			switch {
			case status >= 500:
				class = "5xx"
			case status >= 400:
				class = "4xx"
			case status >= 300:
				class = "3xx"
			}
			b.statusClasses[class]++
		}
		if pause > 0 && hb != nil {
			until := time.Now().Add(pause)
			if until.After(hb.pausedUntil) {
				hb.pausedUntil = until
				hb.pauseReason = reason
			}
		}
		b.mu.Unlock()
		t.releaseSlots()
		if pause > 0 && b.audit != nil {
			b.audit("egress_pause", fmt.Sprintf("host=%s reason=%s pause=%s", t.host, reason, pause))
		}
	})
}

func (t *Ticket) releaseSlots() {
	b := t.b
	if b.globalSem != nil {
		select {
		case <-b.globalSem:
		default:
		}
	}
	if t.hostSem != nil {
		select {
		case <-t.hostSem:
		default:
		}
	}
}

func (hb *hostBudget) perHostSem() chan struct{} {
	return hb.sem
}

func (b *EgressBudget) hostState(host string) *hostBudget {
	if host == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	hb, ok := b.hosts[host]
	if !ok {
		hb = &hostBudget{consecutive5xx: 0}
		if b.policy.PerHostRPS > 0 {
			hb.limiter = rate.NewLimiter(rate.Limit(b.policy.PerHostRPS), b.policy.Burst)
		}
		if b.policy.PerHostConcurrency > 0 {
			hb.sem = make(chan struct{}, b.policy.PerHostConcurrency)
		}
		b.hosts[host] = hb
	}
	return hb
}

func (b *EgressBudget) checkPolicy(host string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return fmt.Errorf("%w: %s", ErrEgressStopped, b.stopReason)
	}
	if b.policy.TotalBudget > 0 && b.used >= b.policy.TotalBudget {
		return ErrEgressBudgetExhausted
	}
	now := time.Now()
	if b.pausedUntil.After(now) {
		return fmt.Errorf("%w until %s: %s", ErrEgressPaused, b.pausedUntil.Format(time.RFC3339), b.pauseReason)
	}
	if hb := b.hosts[host]; hb != nil && hb.pausedUntil.After(now) {
		return fmt.Errorf("%w for %s until %s: %s", ErrEgressPaused, host, hb.pausedUntil.Format(time.RFC3339), hb.pauseReason)
	}
	return nil
}

// Pause stops all egress until d elapses (target asked for a break).
func (b *EgressBudget) Pause(d time.Duration, reason string) {
	if b == nil || d <= 0 {
		return
	}
	b.mu.Lock()
	until := time.Now().Add(d)
	if until.After(b.pausedUntil) {
		b.pausedUntil = until
		b.pauseReason = reason
	}
	b.mu.Unlock()
	if b.audit != nil {
		b.audit("egress_pause", fmt.Sprintf("global reason=%s pause=%s", reason, d))
	}
}

// PauseHost stops egress to one origin until d elapses.
func (b *EgressBudget) PauseHost(host string, d time.Duration, reason string) {
	if b == nil || d <= 0 {
		return
	}
	host = EgressHost(host)
	if host == "" {
		b.Pause(d, reason)
		return
	}
	b.mu.Lock()
	hb := b.hosts[host]
	if hb == nil {
		hb = &hostBudget{}
		b.hosts[host] = hb
	}
	until := time.Now().Add(d)
	if until.After(hb.pausedUntil) {
		hb.pausedUntil = until
		hb.pauseReason = reason
	}
	b.mu.Unlock()
	if b.audit != nil {
		b.audit("egress_pause", fmt.Sprintf("host=%s reason=%s pause=%s", host, reason, d))
	}
}

// Stop ends all further egress for the run (operator/authorization stop).
func (b *EgressBudget) Stop(reason string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	b.stopped = true
	b.stopReason = reason
	b.mu.Unlock()
	if b.audit != nil {
		b.audit("egress_stop", "reason="+reason)
	}
}

// Snapshot is the read-only accounting projection.
func (b *EgressBudget) Snapshot() EgressSnapshot {
	if b == nil {
		return EgressSnapshot{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	snap := EgressSnapshot{
		Requests: b.used, Active: b.active,
		Paused: b.pausedUntil.After(time.Now()), PauseReason: b.pauseReason, PauseUntil: b.pausedUntil,
		Stopped: b.stopped, StopReason: b.stopReason,
		Budget: b.policy.TotalBudget, BudgetUsed: b.used,
		PerHost:       map[string]int{},
		StatusClasses: map[string]int64{},
	}
	for host, hb := range b.hosts {
		snap.PerHost[host] = hb.active
	}
	for k, v := range b.statusClasses {
		snap.StatusClasses[k] = v
	}
	return snap
}

// EgressSoftMessage renders a policy refusal for a tool's Output field. Tools
// must not set Result.Error for this: a target's pause is not a broken tool.
func EgressSoftMessage(err error) string {
	if err == nil {
		return ""
	}
	return "[EGRESS POLICY] " + err.Error()
}

// ParseRetryAfter accepts delta-seconds or an HTTP-date.
func ParseRetryAfter(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, err := http.ParseTime(raw); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
