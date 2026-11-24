package scannerhits

import (
	"strings"
	"testing"
)

const statusOnlyTemplate = `id: weak-check
info:
  name: Weak check
  severity: medium
  tags: exposure
http:
  - matchers:
      - type: status
        status:
          - 200
`

const wordTemplate = `id: strong-check
info:
  name: Strong check
  severity: high
  tags: injection
http:
  - matchers:
      - type: word
        words:
          - "root:x:0:0"
`

func TestAssessStatusOnlyHitNeedsReview(t *testing.T) {
	res, err := assessNucleiHit("", map[string]string{
		"template_id": "weak-check",
		"name":        "Weak check",
		"type":        "http",
		"status":      "200",
		"response":    "<html>home page</html>",
		"template":    statusOnlyTemplate,
	})
	if err != nil {
		t.Fatalf("assess: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected Result.Error: %s", res.Error)
	}
	if got := res.Metadata["scanner_verdict"]; got != "needs_review" {
		t.Fatalf("status-only hit should be needs_review, got %v\n%s", got, res.Output)
	}
	if !strings.Contains(res.Output, "HTTP 状态码") {
		t.Fatalf("output should explain the status-only weakness:\n%s", res.Output)
	}
}

func TestAssessMissingBodyNeedsReview(t *testing.T) {
	res, _ := assessNucleiHit("", map[string]string{
		"template_id": "strong-check",
		"type":        "http",
		"status":      "200",
		"extracted":   "root:x:0:0",
		"template":    wordTemplate,
	})
	if got := res.Metadata["scanner_verdict"]; got != "needs_review" {
		t.Fatalf("a hit with no preserved body should be needs_review, got %v\n%s", got, res.Output)
	}
}

func TestAssessStrongHitWithBodyIsConfirmedShape(t *testing.T) {
	res, _ := assessNucleiHit("", map[string]string{
		"template_id": "strong-check",
		"name":        "Strong check",
		"type":        "http",
		"status":      "200",
		"response":    "root:x:0:0:root:/root:/bin/bash",
		"extracted":   "root:x:0:0",
		"template":    wordTemplate,
	})
	if got := res.Metadata["scanner_verdict"]; got != "confirmed" {
		t.Fatalf("a word-matched hit with body evidence should be confirmed, got %v\n%s", got, res.Output)
	}
	if !strings.Contains(res.Output, "still not a reported vulnerability") {
		t.Fatalf("confirmed must not read as a reportable finding:\n%s", res.Output)
	}
}

func TestAssessRequiresIdentity(t *testing.T) {
	res, _ := assessNucleiHit("", map[string]string{"status": "200"})
	if res.Error == "" {
		t.Fatal("expected a Result.Error when no template id/name/matched_at is supplied")
	}
}

func TestParseTemplateNormalizesFields(t *testing.T) {
	res, err := parseTemplate("", map[string]string{"content": wordTemplate})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected Result.Error: %s", res.Error)
	}
	for _, want := range []string{`"id": "strong-check"`, `"severity": "high"`, `"protocol": "http"`} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("parsed template missing %s:\n%s", want, res.Output)
		}
	}
}

func TestParseTemplateRejectsBrokenYAML(t *testing.T) {
	res, _ := parseTemplate("", map[string]string{"content": "info: [unclosed"})
	if res.Error == "" {
		t.Fatal("expected a Result.Error for malformed YAML")
	}
}
