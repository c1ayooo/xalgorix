package trafficproxy

// Observed-traffic sink (REDESIGN §4, R2b decisions 7-8 + 2026-11-22 raw
// amendment): every proxied request/response becomes an observed InfoItem in
// the owning Run's domain journal, and attributed exchanges additionally
// persist their RAW wire packets into the per-scan traffic archive — off the
// request path. The proxy handler only performs a non-blocking enqueue into a
// bounded queue drained by a single writer goroutine; overflow is dropped and
// counted (never allowed to slow the proxy or the scan).
//
// Red lines implemented here (2026-11-22 operator decision amends the two
// marked ones):
//   - [AMENDED] the compact observation summary still records only header
//     NAMES and masked query values — the 4 KiB audit line rides the journal
//     and the PG replica — BUT attributed exchanges now also persist the
//     full raw packets (header VALUES, bodies, unmasked URL) into
//     <ScanDir>/traffic/ (scanctx.TrafficArchive). The operator explicitly
//     chose raw fidelity for findings display; the archive lives inside the
//     scan isolation boundary;
//   - the observation summary itself stays masked as before;
//   - system-level traffic (no run token) produces no InfoItem and no raw
//     record at all (nothing to attribute the packet to);
//   - high-risk verdicts come from internal/approval.AssessHTTP and are
//     recorded ONLY as observation metadata — the proxy never creates an
//     approval row and never takes over scope decisions.

import (
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/c1ayooo/xalgorix/v4/internal/approval"
	"github.com/c1ayooo/xalgorix/v4/internal/infoitem"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// observationQueueSize bounds the pending-observation queue. Overflow drops
// instead of blocking the proxy request path.
const observationQueueSize = 1024

// rawCaptureLimit bounds how much of a request/response body is captured into
// the per-scan raw archive (per side) and doubles as the high-risk assessment
// preview bound: one body buffer serves both consumers. Unredacted per the
// 2026-11-22 operator decision; beyond the limit the remainder streams
// through untouched and the exchange is flagged truncated.
const rawCaptureLimit = 128 << 10

// TrafficObservation is one proxied request/response summary. The summary
// fields are pre-redacted (no header values, masked query values); Raw, when
// non-nil, carries the unredacted wire packets destined for the per-scan
// traffic archive.
type TrafficObservation struct {
	At                  time.Time
	InstanceID          string // owning Run ("" for system-level traffic)
	ScanCtxID           string // owning scan context ("" for system-level traffic)
	Method              string
	URL                 string // summary form: userinfo stripped, sensitive query values masked
	Status              int
	Upstream            string // upstream proxy (masked) or "direct"
	ReqHeaderNames      []string
	RespHeaderNames     []string
	HighRisk            string // approval.AssessHTTP reason when the request pends as high-risk
	Intercepted         bool   // TLS was intercepted (CONNECT MITM) rather than blind-forwarded
	Failed              bool   // upstream request failed (502 returned to the client)
	TargetTLSUnverified bool   // target certificate failed verification; retried without it
	UpgradePassthrough  bool   // protocol-upgrade request relayed as raw bytes (no inner split)
	// Raw is the unredacted exchange for the per-scan traffic archive. It is
	// built only for attributed, non-upgrade requests; the sink persists it
	// and stamps the archive sequence into the InfoItem context.
	Raw *scanctx.TrafficExchange
}

// Sink consumes one observation. Called from the single writer goroutine.
type Sink func(TrafficObservation)

// BufferedSink decouples the proxy request path from the journal write. It
// never blocks: a full queue drops the observation and counts it. Enqueue is
// serialized with Close on mu, so the queue channel is never sent-to after
// close and no observation is lost mid-handoff.
type BufferedSink struct {
	sink     Sink
	ch       chan TrafficObservation
	done     chan struct{}
	closeOne sync.Once
	mu       sync.Mutex
	closed   bool
	drops    atomic.Int64
}

// NewBufferedSink starts a single writer goroutine draining a bounded queue.
func NewBufferedSink(sink Sink, queue int) *BufferedSink {
	if sink == nil {
		sink = func(TrafficObservation) {}
	}
	if queue <= 0 {
		queue = observationQueueSize
	}
	b := &BufferedSink{
		sink: sink,
		ch:   make(chan TrafficObservation, queue),
		done: make(chan struct{}),
	}
	go b.run()
	return b
}

func (b *BufferedSink) run() {
	for obs := range b.ch {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[trafficproxy] observation sink panicked: %v", r)
				}
			}()
			b.sink(obs)
		}()
	}
	close(b.done)
}

// Enqueue hands one observation to the writer goroutine. It never blocks;
// the return value reports whether the observation was accepted.
func (b *BufferedSink) Enqueue(obs TrafficObservation) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false // sink closed; late observations are dropped
	}
	select {
	case b.ch <- obs:
		return true
	default:
		n := b.drops.Add(1)
		if n == 1 || n%100 == 0 {
			log.Printf("[trafficproxy] observation queue overflow: %d observations dropped so far", n)
		}
		return false
	}
}

// Drops reports the number of observations dropped by queue overflow.
func (b *BufferedSink) Drops() int64 {
	return b.drops.Load()
}

// Close stops the sink after draining everything already enqueued. The
// closed flag under mu guarantees no Enqueue is in flight when the channel
// closes.
func (b *BufferedSink) Close() {
	b.closeOne.Do(func() {
		b.mu.Lock()
		b.closed = true
		close(b.ch)
		b.mu.Unlock()
	})
	<-b.done
}

// observeTraffic is the production sink: attributed observations become
// observed InfoItems in the owning Run's domain journal, and a carried raw
// exchange is persisted into the scan's traffic archive first (the InfoItem
// context then references the archive sequence). Without a DSN the journal is
// still written (hot-path JSON authority unchanged); the PostgreSQL
// projection simply does not happen.
func observeTraffic(obs TrafficObservation) {
	if strings.TrimSpace(obs.ScanCtxID) == "" {
		return // system-level traffic: observable, but never an InfoItem
	}
	sc := scanctx.Get(obs.ScanCtxID)
	if sc == nil || sc.Ledger == nil {
		// The scan context is gone (finished/teared down) — drop silently:
		// late-arriving observations for a dead Run carry no decision value.
		return
	}
	if obs.Raw != nil {
		if seq, err := sc.Traffic.Append(obs.Raw); err != nil {
			// A lost raw packet must not lose the audit line: log and continue.
			log.Printf("[trafficproxy] raw exchange for %s not archived: %v", obs.ScanCtxID, err)
		} else {
			obs.Raw.Seq = seq
		}
	}
	if err := sc.Ledger.AppendTrafficObservation(buildTrafficInfoItem(obs), trafficSummary(obs)); err != nil {
		log.Printf("[trafficproxy] traffic observation for %s not journaled: %v", obs.ScanCtxID, err)
	}
}

// trafficSummary renders the bounded human-readable summary stored alongside
// the InfoItem (and in the journal record's Summary field).
func trafficSummary(obs TrafficObservation) string {
	var b strings.Builder
	b.WriteString(obs.Method)
	b.WriteString(" ")
	b.WriteString(obs.URL)
	if obs.Status != 0 {
		b.WriteString(" -> ")
		b.WriteString(statusLine(obs.Status))
	} else if obs.Failed {
		b.WriteString(" -> failed")
	}
	if obs.Upstream != "" {
		b.WriteString(" via ")
		b.WriteString(obs.Upstream)
	}
	if obs.Intercepted {
		b.WriteString(" (tls-intercepted)")
	}
	if obs.UpgradePassthrough {
		b.WriteString(" (upgrade-passthrough)")
	}
	if obs.HighRisk != "" {
		b.WriteString(" high-risk: ")
		b.WriteString(obs.HighRisk)
	}
	// The cap lives here so the InfoItem text AND the journal record summary
	// are bounded by the same limit.
	return truncateObservationText(b.String())
}

// maxObservationTextBytes caps the InfoItem summary text (4 KiB) so a
// pathological URL or reason cannot bloat the journal record.
const maxObservationTextBytes = 4 << 10

// truncateObservationText caps a summary at maxObservationTextBytes without
// splitting a multi-byte UTF-8 rune.
func truncateObservationText(s string) string {
	if len(s) <= maxObservationTextBytes {
		return s
	}
	cut := maxObservationTextBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "… (truncated)"
}

func statusLine(status int) string {
	line := strconv.Itoa(status)
	if txt := http.StatusText(status); txt != "" {
		line += " " + txt
	}
	return line
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

// buildTrafficInfoItem fills the seven InfoItem coordinates without any LLM:
// subject = endpoint (method + URL), source = trafficproxy (upstream + Run),
// context = phase "traffic" (+ high-risk reason when assessed).
func buildTrafficInfoItem(obs TrafficObservation) *infoitem.Item {
	id, err := infoitem.NewID()
	if err != nil {
		// A random-UUID failure is not worth dropping the item over; fall
		// back to a timestamped id (still globally unique enough for a local
		// journal line, and the journal assigns the durable seq anyway).
		id = "traffic-" + itoa(obs.At.UnixNano())
	}
	context := map[string]string{
		"phase": "traffic",
	}
	if obs.Upstream != "" {
		context["upstream"] = obs.Upstream
	}
	if obs.Intercepted {
		context["tls_intercepted"] = "true"
	}
	if obs.TargetTLSUnverified {
		context["tls_target_unverified"] = "true"
	}
	if obs.UpgradePassthrough {
		context["upgrade_passthrough"] = "true"
	}
	if obs.HighRisk != "" {
		context["high_risk_reason"] = obs.HighRisk
	}
	if len(obs.ReqHeaderNames) > 0 {
		context["request_headers"] = strings.Join(obs.ReqHeaderNames, ",")
	}
	if len(obs.RespHeaderNames) > 0 {
		context["response_headers"] = strings.Join(obs.RespHeaderNames, ",")
	}
	if obs.Raw != nil && obs.Raw.Seq > 0 {
		context["traffic_seq"] = strconv.FormatInt(obs.Raw.Seq, 10)
		context["raw_archive"] = scanctx.TrafficExchangeFile(obs.Raw.Seq)
	}
	if obs.Raw != nil && obs.Raw.Channel != "" {
		context["channel"] = obs.Raw.Channel
	}
	return &infoitem.Item{
		ID:               id,
		Nature:           infoitem.NatureObserved,
		CoordinateStatus: infoitem.CoordinatesComplete,
		Coordinates: infoitem.Coordinates{
			Content: infoitem.Content{
				Text:      trafficSummary(obs), // trafficSummary already caps at maxObservationTextBytes
				MediaType: "text/plain",
			},
			Source: infoitem.Source{
				Kind:    "trafficproxy",
				Name:    obs.Upstream,
				AgentID: obs.InstanceID,
			},
			ObservedAt: obs.At,
			Subject: infoitem.Subject{
				Kind:       "endpoint",
				Identifier: obs.Method + " " + obs.URL,
			},
			Purpose: "traffic_observation",
			Context: context,
		},
		CreatedAt: time.Now().UTC(),
	}
}

// redactURL strips userinfo and masks the values of credential-bearing query
// parameters. Keys are compared lowercased; over-masking is preferred over
// any chance of a credential surviving into the journal.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User != nil {
		u.User = nil
	}
	q := u.Query()
	changed := false
	for key, values := range q {
		if !sensitiveParamKey(key) {
			continue
		}
		for i := range values {
			if values[i] != "" {
				values[i] = "****"
			}
		}
		q[key] = values
		changed = true
	}
	if changed {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// sensitiveParamKeys are query-parameter name fragments whose values are
// masked before an observation is recorded.
var sensitiveParamKeys = []string{
	"password", "passwd", "pwd", "token", "secret", "apikey", "api_key",
	"key", "auth", "session", "credential", "signature", "sig", "jwt",
	"sessid", "sid", "phpsessid",
}

func sensitiveParamKey(key string) bool {
	k := strings.ToLower(key)
	for _, frag := range sensitiveParamKeys {
		if strings.Contains(k, frag) {
			return true
		}
	}
	return false
}

// assessHighRisk applies the shared approval HTTP classifier. The verdict is
// observation metadata only — the proxy never creates an approval row and
// never blocks scope-legal traffic (REDESIGN §4: the proxy does not take
// over scope).
func assessHighRisk(method, rawURL string, bodyPreview string) string {
	verdict := approval.AssessHTTP(method, rawURL, nil, bodyPreview)
	if verdict.Decision == approval.Require {
		return verdict.Reason
	}
	return ""
}

// headerNames returns the order-preserving header names of a header map.
// Values are deliberately NOT returned.
func headerNames(h http.Header) []string {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	return names
}
