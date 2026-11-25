package web

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/intelsubmit"
	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

// The observer is the only place platform prose becomes a receipt, so the
// parsing rules are pinned here: a wrong state silently turns a scored
// challenge into a "still pending" debt, or worse, hides a rejection.

func TestPlatformActionClassifiesAndExtractsCode(t *testing.T) {
	cases := []struct {
		name       string
		args       map[string]string
		wantAction string
		wantCode   string
	}{
		{
			name: "submit with json body",
			args: map[string]string{
				"url":  "https://bench.example/openapi/v1/challenges/submit",
				"body": `{"unique_code":"a-05","flag":"flag{x}"}`,
			},
			wantAction: "submit",
			wantCode:   "a-05",
		},
		{
			name: "start with query code",
			args: map[string]string{
				"url": "https://bench.example/openapi/v1/challenges/start?unique_code=a-07",
			},
			wantAction: "start",
			wantCode:   "a-07",
		},
		{
			name: "close via curl command",
			args: map[string]string{
				"command": `curl -X POST "https://bench.example/openapi/v1/challenges/close?unique_code=c-07"`,
			},
			wantAction: "close",
			wantCode:   "c-07",
		},
		{
			name: "challenge list is not a lifecycle call",
			args: map[string]string{
				"url": "https://bench.example/openapi/v1/challenges",
			},
			wantAction: "",
		},
		{
			name: "ordinary target traffic is ignored",
			args: map[string]string{
				"url": "http://10.0.177.16/api.php?action=export",
			},
			wantAction: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, code := platformAction(tc.args)
			if action != tc.wantAction {
				t.Fatalf("action = %q, want %q", action, tc.wantAction)
			}
			if code != tc.wantCode {
				t.Fatalf("code = %q, want %q", code, tc.wantCode)
			}
		})
	}
}

func TestPlatformArgsOfIgnoresNonPlatformCalls(t *testing.T) {
	if got := platformArgsOf(map[string]string{"url": "http://10.0.0.1/"}); got != nil {
		t.Fatalf("ordinary traffic must not be observed, got %v", got)
	}
	if got := platformArgsOf(map[string]string{"url": "https://b/openapi/v1/challenges/start"}); got == nil {
		t.Fatal("platform call must be observed")
	}
	if got := platformArgsOf(nil); got != nil {
		t.Fatalf("empty args must be ignored, got %v", got)
	}
}

func TestLastJSONObjectTakesTheResponseBody(t *testing.T) {
	output := "Tool 'http_request' result:\n" +
		"HTTP/1.1 200 OK\nRequest-Time: 754ms\n\n" +
		"--- Response Headers ---\nContent-Type: application/json\n\n" +
		"--- Response Body (text) ---\n" +
		`{"correct":true,"awarded":300,"cumulative_score":300,"correct_flag_count":1,"total_flag_count":1,"matched_flag_index":0}`
	obj, ok := lastJSONObject(output)
	if !ok {
		t.Fatal("expected a decodable object")
	}
	if got := jsonIntField(obj, "awarded"); got != 300 {
		t.Fatalf("awarded = %d, want 300", got)
	}
	if correct, _ := obj["correct"].(bool); !correct {
		t.Fatal("correct must parse as true")
	}
}

func TestLastJSONObjectPrefersTheLaterObject(t *testing.T) {
	// Request echo followed by the real response: the last object wins.
	s := `{"request":"POST /challenges/submit"} ... {"correct":false,"awarded":0}`
	obj, ok := lastJSONObject(s)
	if !ok {
		t.Fatal("expected a decodable object")
	}
	if correct, _ := obj["correct"].(bool); correct {
		t.Fatal("the response must win over the request echo")
	}
}

func TestReceiptStateMapsPlatformVerdicts(t *testing.T) {
	cases := []struct {
		name    string
		resp    map[string]any
		want    intelsubmit.State
		awarded int
	}{
		{
			name:    "correct scores",
			resp:    map[string]any{"correct": true, "awarded": float64(100)},
			want:    intelsubmit.StateSubmitted,
			awarded: 100,
		},
		{
			name: "wrong flag invalidates",
			resp: map[string]any{"correct": false, "awarded": float64(0)},
			want: intelsubmit.StateInvalid,
		},
		{
			name: "duplicate is its own state",
			resp: map[string]any{"code": "duplicate", "message": "already submitted"},
			want: intelsubmit.StateDuplicate,
		},
		{
			name: "unparseable response changes nothing",
			resp: map[string]any{"message": "rate limited"},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, awarded := receiptState(tc.resp)
			if state != tc.want {
				t.Fatalf("state = %q, want %q", state, tc.want)
			}
			if awarded != tc.awarded {
				t.Fatalf("awarded = %d, want %d", awarded, tc.awarded)
			}
		})
	}
}

func TestFirstContainerAddr(t *testing.T) {
	obj, ok := lastJSONObject(`{"unique_code":"a-05","container_addr":["10.0.177.16:80"]}`)
	if !ok {
		t.Fatal("expected object")
	}
	if got := firstContainerAddr(obj); got != "10.0.177.16:80" {
		t.Fatalf("endpoint = %q", got)
	}
	if got := firstContainerAddr(map[string]any{"container_addr": []any{}}); got != "" {
		t.Fatalf("empty list must yield empty endpoint, got %q", got)
	}
}

func TestPendingDigestIsEmptyWithoutStore(t *testing.T) {
	// No PG wired (legacy CLI): the gate must not invent a debt.
	intelsubmit.SetStore(nil)
	if got := pendingSubmissionDigest("task-1"); got != "" {
		t.Fatalf("digest = %q, want empty", got)
	}
}

func TestAttachReceiptsJoinsByEndpointThenByCode(t *testing.T) {
	receipts := []intelsubmit.Submit{
		{UniqueCode: "a-05", Endpoint: "10.0.177.16:80", State: intelsubmit.StatePending},
		{UniqueCode: "a-07", Endpoint: "10.0.177.18:80", State: intelsubmit.StateSubmitted, Awarded: 300},
	}
	byEndpointItem := &taskIntelItem{Item: intelligence.Item{
		Title:    "a-05 导出注入",
		Endpoint: "http://10.0.177.16:80/api.php?action=export",
	}}
	byCodeItem := &taskIntelItem{Item: intelligence.Item{
		Title: "a-07 XXE 任意文件读取",
	}}
	unrelated := &taskIntelItem{Item: intelligence.Item{
		Title:    "c-03 无回执",
		Endpoint: "http://10.0.177.30:22",
	}}
	items := []*taskIntelItem{byEndpointItem, byCodeItem, unrelated}
	attachReceipts(items, receipts)

	if byEndpointItem.Submit == nil || byEndpointItem.Submit.UniqueCode != "a-05" {
		t.Fatalf("endpoint join failed: %+v", byEndpointItem.Submit)
	}
	if byCodeItem.Submit == nil || byCodeItem.Submit.State != intelsubmit.StateSubmitted {
		t.Fatalf("code fallback failed: %+v", byCodeItem.Submit)
	}
	if byCodeItem.Submit.Awarded != 300 {
		t.Fatalf("awarded not carried: %d", byCodeItem.Submit.Awarded)
	}
	if unrelated.Submit != nil {
		t.Fatalf("unrelated item got a receipt: %+v", unrelated.Submit)
	}

	// A scored receipt wins for a slot that also has a stale invalid one.
	slot := []intelsubmit.Submit{
		{UniqueCode: "a-09", Endpoint: "10.0.177.16:80", State: intelsubmit.StateInvalid},
		{UniqueCode: "a-09", Endpoint: "10.0.177.16:80", State: intelsubmit.StateSubmitted, Awarded: 300},
	}
	item := &taskIntelItem{Item: intelligence.Item{Endpoint: "10.0.177.16:80"}}
	attachReceipts([]*taskIntelItem{item}, slot)
	if item.Submit == nil || item.Submit.State != intelsubmit.StateSubmitted {
		t.Fatalf("submitted must win over invalid: %+v", item.Submit)
	}

	// No receipts: nothing is decorated, so the panel shows no badge at all.
	bare := &taskIntelItem{Item: intelligence.Item{Endpoint: "10.0.177.16:80"}}
	attachReceipts([]*taskIntelItem{bare}, nil)
	if bare.Submit != nil {
		t.Fatalf("empty receipts decorated an item: %+v", bare.Submit)
	}
}

func TestNormalizeEndpointJoinsPlatformAndIntelShapes(t *testing.T) {
	// The receipt carries the platform's bare host:port; the recorded intel
	// usually carries a full URL ending in a path. Both must land on one key,
	// otherwise every result looks unsubmitted.
	cases := map[string]string{
		"10.0.177.16:80":                         "10.0.177.16:80",
		"http://10.0.177.16:80":                  "10.0.177.16:80",
		"http://10.0.177.16:80/api.php?action=x": "10.0.177.16:80",
		"https://10.0.177.18:8443/admin":         "10.0.177.18:8443",
		"  http://10.0.177.17:8080/  ":           "10.0.177.17:8080",
		"":                                       "",
		// The shapes actually recorded by the model: a bare host with a path,
		// and a port plus a trailing annotation. Both must land on host:port
		// or the receipt never joins.
		"10.0.161.32/api.php?endpoint=check": "10.0.161.32:80",
		"10.0.161.33:80/ (POST username)":    "10.0.161.33:80",
		"https://app.example/admin":          "app.example:443",
		"app.example/login":                  "app.example:80",
	}
	for in, want := range cases {
		if got := normalizeEndpoint(in); got != want {
			t.Fatalf("normalizeEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncateForReceipt(t *testing.T) {
	long := make([]byte, 900)
	for i := range long {
		long[i] = 'x'
	}
	got := truncateForReceipt(string(long))
	if len(got) > 420 {
		t.Fatalf("receipt excerpt too long: %d", len(got))
	}
}
