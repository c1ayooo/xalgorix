package web

// keypool.go: a scoped LLM key-pool API for machine-to-machine clients.
//
// The settings surface (/api/settings/*) is operator-only BY DESIGN: it
// exposes every environment variable, so machine API tokens are blocked from
// it (see auth_session.go's machineTokenRouteDenylist). But an automation
// control plane (the SaaS admin panel) legitimately needs to manage exactly
// one knob: the LLM API key pool that outbound requests rotate across to
// spread provider rate limits.
//
// /api/pool exposes that single setting with a purpose-built, masked read
// model. Machine tokens are allowed here (it is not on the denylist and it
// returns no secret material), while /api/settings/* stays operator-only -
// an automation client can rotate keys without ever being able to read any
// other environment value.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/llm"
)

// maxKeyPoolSize bounds the pool the same way an operator would be bounded
// by practicality; the env-file value must stay a single parseable line.
const (
	maxKeyPoolSize   = 64
	maxKeyPoolKeyLen = 1024
)

// keyPoolKeyView is one key's masked status row. The health fields are the
// process-wide breaker state (llm keyhealth): why traffic moved off this key
// and until when. No secret material is ever included.
type keyPoolKeyView struct {
	ID        string `json:"id"`
	Masked    string `json:"masked"`
	State     string `json:"state"` // ok | cooling
	OpenUntil string `json:"open_until,omitempty"`
	Trips     int    `json:"trips,omitempty"`
	LastError string `json:"last_error,omitempty"`
	LastAt    string `json:"last_at,omitempty"`
}

type keyPoolResponse struct {
	Keys   int      `json:"keys"`
	Masked []string `json:"masked"`
	// Strategy is the rotation mode the clients run with: round_robin (default),
	// sticky (overflow failover) or off (fewer than two keys).
	Strategy string           `json:"strategy"`
	Entries  []keyPoolKeyView `json:"entries"`
	// RestartNeeded mirrors applyEnvironmentUpdates' verdict. The pool and
	// the primary key apply to new scans immediately, so this is normally
	// false; it is surfaced for completeness.
	RestartNeeded bool `json:"restart_needed,omitempty"`
}

// currentKeyPool renders the live pool (runtime config) with every key
// masked, plus each key's circuit-breaker state. The response deliberately
// contains no secret material.
func (s *Server) currentKeyPool() keyPoolResponse {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	pool := make([]string, 0, len(s.cfg.APIKeys)+1)
	seen := make(map[string]bool, len(s.cfg.APIKeys)+1)
	for _, k := range append([]string{s.cfg.APIKey}, s.cfg.APIKeys...) {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		pool = append(pool, k)
	}
	masked := maskedKeyPoolList(pool)
	if masked == nil {
		masked = []string{}
	}
	strategy := "off"
	if len(pool) > 1 {
		strategy = "round_robin"
		if strings.EqualFold(strings.TrimSpace(s.cfg.KeyPoolStrategy), "sticky") {
			strategy = "sticky"
		}
	}
	health := llm.KeyPoolHealthSnapshot()
	entries := make([]keyPoolKeyView, 0, len(pool))
	for i, k := range pool {
		fp := llm.KeyFingerprint(k)
		st := health[fp]
		state := "ok"
		if st.Open() {
			state = "cooling"
		}
		view := keyPoolKeyView{
			ID:        fp,
			Masked:    masked[i],
			State:     state,
			Trips:     st.Trips,
			LastError: st.LastError,
		}
		if !st.OpenUntil.IsZero() {
			view.OpenUntil = st.OpenUntil.UTC().Format(time.RFC3339)
		}
		if !st.LastAt.IsZero() {
			view.LastAt = st.LastAt.UTC().Format(time.RFC3339)
		}
		entries = append(entries, view)
	}
	return keyPoolResponse{Keys: len(pool), Masked: masked, Strategy: strategy, Entries: entries}
}

// cleanKeyPoolInput trims, drops empties, dedupes preserving order, and
// enforces the same single-line constraints the env writer applies: commas
// separate pool entries in the stored value, so a key containing one would
// corrupt the pool.
func cleanKeyPoolInput(keys []string) ([]string, error) {
	seen := make(map[string]bool, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if strings.ContainsAny(k, "\r\n,") {
			return nil, fmt.Errorf("API keys cannot contain commas or newlines")
		}
		if len(k) > maxKeyPoolKeyLen {
			return nil, fmt.Errorf("API key exceeds the maximum length of %d", maxKeyPoolKeyLen)
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("at least one API key is required")
	}
	if len(out) > maxKeyPoolSize {
		return nil, fmt.Errorf("too many keys (max %d)", maxKeyPoolSize)
	}
	return out, nil
}

// handleKeyPool serves GET /api/pool (masked pool view) and POST /api/pool
// (replace the pool; the first key also becomes the primary LLM API key,
// mirroring the operator dashboard's semantics). Both the dashboard session
// and machine API tokens authorize it.
func (s *Server) handleKeyPool(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSONStatus(w, http.StatusOK, s.currentKeyPool())
		return
	case http.MethodPost:
		var body struct {
			Keys []string `json:"keys"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{
				"error": "invalid JSON body",
			})
			return
		}
		clean, err := cleanKeyPoolInput(body.Keys)
		if err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		restart, err := s.applyEnvironmentUpdates(map[string]string{
			"XALGORIX_API_KEYS": strings.Join(clean, ","),
			"XALGORIX_API_KEY":  clean[0],
		})
		if err != nil {
			writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		resp := s.currentKeyPool()
		resp.RestartNeeded = restart
		writeJSONStatus(w, http.StatusOK, resp)
		return
	default:
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{
			"error": "method not allowed",
		})
	}
}

// handleKeyPoolReset serves POST /api/pool/reset: clear one key's breaker
// ({"id":"<fingerprint>"}) or every key's (empty body / empty id). This is the
// operator's "try this key again now" action after topping up an account.
func (s *Server) handleKeyPoolReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	// An empty body means "reset everything"; malformed JSON is still a 400.
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
			return
		}
	}
	llm.ResetKeyPoolHealth(strings.TrimSpace(body.ID))
	writeJSONStatus(w, http.StatusOK, s.currentKeyPool())
}
