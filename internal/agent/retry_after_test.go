package agent

import (
	"testing"
	"time"
)

func TestRetryAfterDelay(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want time.Duration
		ok   bool
	}{
		{"header seconds", "HTTP/1.1 429 Too Many Requests\nRetry-After: 120", 120 * time.Second, true},
		{"json field", `{"error":"rate limited","retry_after":30}`, 30 * time.Second, true},
		{"zero", "retry-after: 0", 0, true},
		{"absent", "HTTP/1.1 429 Too Many Requests", 0, false},
		{"garbage", "retry-after: soon", 0, false},
	}
	for _, tc := range cases {
		got, ok := retryAfterDelay(tc.raw)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%s: got (%s,%v) want (%s,%v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
	// HTTP-date form must be accepted and never negative.
	if d, ok := retryAfterDelay("Retry-After: Wed, 21 Oct 2015 07:28:00 GMT"); !ok || d < 0 {
		t.Errorf("date form: got (%s,%v)", d, ok)
	}
}
