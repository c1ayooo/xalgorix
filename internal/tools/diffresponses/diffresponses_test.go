package diffresponses

import (
	"strings"
	"testing"
)

func TestDiffBodiesReportsAddedRemovedChanged(t *testing.T) {
	a := `{"id":1,"role":"user","email":"a@example.com","nested":{"plan":"free"},"ts":"2026-01-01T00:00:00Z"}`
	b := `{"id":2,"role":"admin","nested":{"plan":"pro"},"leaked":"ssn","ts":"2026-01-02T00:00:00Z"}`
	changes, ok := diffBodies(a, b)
	if !ok {
		t.Fatal("expected structured JSON diff")
	}
	joined := ""
	for _, c := range changes {
		joined += c.Kind + " " + c.Path + "\n"
	}
	for _, want := range []string{"changed $.id", "changed $.role", "changed $.nested.plan", "added $.leaked", "removed $.email"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in diff:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "ts") {
		t.Fatalf("noise key ts must be filtered:\n%s", joined)
	}
}

func TestDiffBodiesFiltersNoiseValues(t *testing.T) {
	a := `{"trace":"abc","data":{"user_id":1001}}`
	b := `{"trace":"def","data":{"user_id":1002}}`
	changes, _ := diffBodies(a, b)
	if len(changes) != 1 || changes[0].Path != "$.data.user_id" {
		t.Fatalf("expected only the user_id change, got %+v", changes)
	}
}

func TestDiffBodiesNonJSONFallsBack(t *testing.T) {
	if _, ok := diffBodies("<html>a</html>", "<html>b</html>"); ok {
		t.Fatal("non-JSON bodies must not be reported as structured")
	}
}

func TestParseMessageSplitsResponse(t *testing.T) {
	msg := parseMessage("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nX-Trace: abc\r\n\r\n{\"a\":1}")
	if msg.StatusLine != "HTTP/1.1 200 OK" {
		t.Fatalf("status line = %q", msg.StatusLine)
	}
	if msg.Headers["x-trace"] != "abc" {
		t.Fatalf("header parse failed: %+v", msg.Headers)
	}
	if msg.Body != `{"a":1}` {
		t.Fatalf("body = %q", msg.Body)
	}
}

func TestDiffHeadersSkipsNoise(t *testing.T) {
	a := map[string]string{"date": "Mon", "x-trace": "1", "server": "nginx", "x-new": "1"}
	b := map[string]string{"date": "Tue", "x-trace": "2", "server": "apache"}
	changes := diffHeaders(a, b)
	var paths []string
	for _, c := range changes {
		paths = append(paths, c.Path)
	}
	if strings.Join(paths, ",") != "server,x-new" {
		t.Fatalf("expected only server (changed) and x-new (removed), got %+v", changes)
	}
}
