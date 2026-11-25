package web

// intel_submit_observer.go — turn observed platform API responses into
// submission receipts (migration 0038).
//
// Round 7 of the benchmark finished with 19 findings, several of them verified
// critical, and 1000/23000 on the platform: workers proved challenges but the
// control-plane lane never picked the results up, and the next session had no
// way to see which challenges were already spent. The receipts written here
// answer exactly that question, keyed by the platform's unique_code at task
// level so the answer survives the per-scan partition.
//
// Authority note: this is an audit projection driven by responses the engine
// actually observed. It never infers a state from model prose, and awarded
// points only ever come from the platform's own `awarded` field.

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/c1ayooo/xalgorix/v4/internal/agent"
	"github.com/c1ayooo/xalgorix/v4/internal/intelsubmit"
)

// platformCallArgs remembers the arguments of one in-flight platform call so
// the matching tool_result can be attributed. tool_result events carry no
// arguments, and the submit response does not echo the challenge code.
var platformCallArgs sync.Map // callKey -> map[string]string

func intelCallKey(sess *scanSession, evt agent.Event) string {
	sessID := ""
	if sess != nil {
		sessID = sess.id
	}
	return sessID + "|" + evt.AgentID + "|" + evt.ToolName
}

// observeIntelSubmit is called from processEvent for tool_call and tool_result
// events only. It is a no-op unless the submission store is wired.
func (s *Server) observeIntelSubmit(evt agent.Event, sess *scanSession) {
	store := intelsubmit.StoreOrNil()
	if store == nil || sess == nil {
		return
	}
	taskID := strings.TrimSpace(sess.objectiveTaskID)
	if taskID == "" {
		return
	}
	key := intelCallKey(sess, evt)

	if evt.Type == "tool_call" {
		if args := platformArgsOf(evt.ToolArgs); args != nil {
			platformCallArgs.Store(key, args)
		}
		return
	}

	cached, ok := platformCallArgs.LoadAndDelete(key)
	if !ok {
		return
	}
	args, _ := cached.(map[string]string)
	action, code := platformAction(args)
	if action == "" {
		return
	}

	output := evt.ToolResult.Output
	if evt.ToolResult.Error != "" {
		output += " " + evt.ToolResult.Error
	}
	resp, hasResp := lastJSONObject(output)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	projectID := strings.TrimSpace(sess.objectiveProjectID)

	switch action {
	case "start":
		if hasResp {
			if v := jsonStringField(resp, "unique_code"); v != "" {
				code = v
			}
		}
		endpoint := firstContainerAddr(resp)
		if code == "" || endpoint == "" {
			return
		}
		_, _ = store.Upsert(ctx, intelsubmit.UpsertInput{
			ProjectID:  projectID,
			TaskID:     taskID,
			UniqueCode: code,
			Endpoint:   endpoint,
			State:      intelsubmit.StatePending,
			Response:   "container started at " + endpoint,
		})

	case "submit":
		if hasResp {
			if v := jsonStringField(resp, "unique_code"); v != "" {
				code = v
			}
		}
		if code == "" {
			return
		}
		state, awarded := receiptState(resp)
		if state == "" {
			return
		}
		_, _ = store.Upsert(ctx, intelsubmit.UpsertInput{
			ProjectID:  projectID,
			TaskID:     taskID,
			UniqueCode: code,
			State:      state,
			Awarded:    awarded,
			Response:   truncateForReceipt(output),
			Attempted:  true,
		})

	case "close":
		if hasResp {
			if v := jsonStringField(resp, "unique_code"); v != "" {
				code = v
			}
			if closed, ok := resp["closed"].(bool); ok && !closed {
				return
			}
		}
		if code == "" {
			return
		}
		_, _ = store.InvalidateByCode(ctx, taskID, code,
			"container closed: the flag it held is no longer live")
	}
}

// platformArgsOf returns the flattened argument map when it looks like a
// platform API call, nil otherwise. Cheap: the scan's ordinary traffic must
// not pay for this observer.
func platformArgsOf(args map[string]string) map[string]string {
	if len(args) == 0 {
		return nil
	}
	for _, k := range []string{"url", "command", "body"} {
		if strings.Contains(args[k], "/openapi/v1/challenges") {
			return args
		}
	}
	return nil
}

// platformAction classifies one platform call and extracts the challenge code
// from the request when the response will not echo it.
func platformAction(args map[string]string) (action, code string) {
	joined := args["url"] + " " + args["command"] + " " + args["body"]
	if !strings.Contains(joined, "/openapi/v1/challenges") {
		return "", ""
	}
	switch {
	case strings.Contains(joined, "/challenges/submit"):
		action = "submit"
	case strings.Contains(joined, "/challenges/start"):
		action = "start"
	case strings.Contains(joined, "/challenges/close"):
		action = "close"
	default:
		return "", ""
	}
	code = codeFromRequest(args["body"])
	if code == "" {
		code = codeFromQuery(args["url"] + " " + args["command"])
	}
	return action, code
}

// codeFromRequest pulls unique_code out of a JSON request body.
func codeFromRequest(body string) string {
	if body == "" {
		return ""
	}
	if obj, ok := lastJSONObject(body); ok {
		return jsonStringField(obj, "unique_code")
	}
	return ""
}

// codeFromQuery pulls unique_code=… out of a query string or curl URL.
func codeFromQuery(s string) string {
	const marker = "unique_code="
	idx := strings.Index(s, marker)
	if idx < 0 {
		return ""
	}
	rest := s[idx+len(marker):]
	end := strings.IndexAny(rest, "&\"' \n\r\t)")
	if end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// receiptState maps one submit response onto a receipt state.
func receiptState(resp map[string]any) (intelsubmit.State, int) {
	if resp == nil {
		return "", 0
	}
	if code := jsonStringField(resp, "code"); code == "duplicate" {
		return intelsubmit.StateDuplicate, 0
	}
	if correct, ok := resp["correct"].(bool); ok {
		if correct {
			return intelsubmit.StateSubmitted, jsonIntField(resp, "awarded")
		}
		return intelsubmit.StateInvalid, 0
	}
	return "", 0
}

func jsonStringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func jsonIntField(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(v))
		return n
	}
	return 0
}

// firstContainerAddr reads container_addr[0] from a start response.
func firstContainerAddr(m map[string]any) string {
	if m == nil {
		return ""
	}
	raw, ok := m["container_addr"]
	if !ok {
		return ""
	}
	list, ok := raw.([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	if s, ok := list[0].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// lastJSONObject returns the last decodable JSON object in s. Tool output
// wraps the platform body in prose ("Tool 'http_request' result: … ---
// Response Body (text) ---"), so scanning for the last complete object is more
// robust than parsing the transport format.
func lastJSONObject(s string) (map[string]any, bool) {
	var found map[string]any
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		var obj map[string]any
		if err := dec.Decode(&obj); err == nil && len(obj) > 0 {
			found = obj
			// Jump past this object to keep scanning for a later one.
			consumed := int(dec.InputOffset())
			if consumed <= 0 {
				continue
			}
			i += consumed - 1
		}
	}
	return found, found != nil
}

// pendingSubmissionDigest summarises unsubmitted results for one task, or ""
// when nothing is pending. Callers use it to make a drained frontier honest:
// "no more work to plan" is not the same as "nothing left to score".
func pendingSubmissionDigest(taskID string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return intelsubmit.PendingDigest(ctx, taskID, 20)
}

// attachReceipts decorates recorded intel with its scoring receipt: did this
// result actually become points?
//
// Endpoint is the authoritative join (it comes from the platform's start
// response, so it is a fact), normalised on both sides because intel items
// often carry a full URL. The challenge code named inside the item is the
// fallback for results recorded before the endpoint was captured.
func attachReceipts(items []*taskIntelItem, receipts []intelsubmit.Submit) {
	if len(receipts) == 0 {
		return
	}
	byEndpoint := make(map[string]*intelsubmit.Submit, len(receipts))
	for i := range receipts {
		r := &receipts[i]
		ep := normalizeEndpoint(r.Endpoint)
		if ep == "" {
			continue
		}
		// A scored receipt wins over an invalidated one for the same slot.
		if prev, ok := byEndpoint[ep]; !ok ||
			(prev.State != intelsubmit.StateSubmitted && r.State == intelsubmit.StateSubmitted) {
			byEndpoint[ep] = r
		}
	}
	for _, ti := range items {
		if ep := normalizeEndpoint(ti.Endpoint); ep != "" {
			if r, ok := byEndpoint[ep]; ok {
				ti.Submit = r
				continue
			}
		}
		for i := range receipts {
			r := &receipts[i]
			if r.UniqueCode == "" {
				continue
			}
			if strings.Contains(ti.Title, r.UniqueCode) ||
				strings.Contains(ti.Content, r.UniqueCode) ||
				strings.Contains(ti.Endpoint, r.UniqueCode) {
				ti.Submit = r
				break
			}
		}
	}
}

// normalizeEndpoint reduces a target to host:port so receipts (written from
// the platform's "10.0.177.16:80") join recorded intel (often written as
// "http://10.0.177.16:80/api.php"). Without this the endpoint join silently
// never matches and every result looks unsubmitted.
//
// The port is filled in when the item omits it: intel items commonly carry a
// bare host ("10.0.161.32/api.php?action=login") while the receipt always has
// the platform's explicit port. Without the default the two never meet. A bare
// host defaults to 80 because challenge containers are plain HTTP on high
// ports; https is only inferred when the item spells the scheme out.
func normalizeEndpoint(s string) string {
	s = strings.TrimSpace(s)
	scheme := ""
	switch {
	case strings.HasPrefix(s, "http://"):
		scheme = "http"
		s = s[len("http://"):]
	case strings.HasPrefix(s, "https://"):
		scheme = "https"
		s = s[len("https://"):]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return ""
	}
	if !strings.Contains(s, ":") {
		if scheme == "https" {
			return s + ":443"
		}
		return s + ":80"
	}
	return s
}

// truncateForReceipt keeps a short excerpt of the platform response so the
// panel can explain a rejection without storing a whole body.
func truncateForReceipt(s string) string {
	const limit = 400
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
