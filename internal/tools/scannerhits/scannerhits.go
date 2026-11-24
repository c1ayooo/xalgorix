// Package scannerhits turns a scanner hit into a reviewable, deterministic
// conclusion. It deliberately never claims a vulnerability is real: it flags
// hits that lack enough evidence to stand on their own, and it surfaces the
// same checklist the model must satisfy before reporting.
//
// Two read-only tools live here:
//   - assess_nuclei_hit: verdict.Assess over one nuclei result (template
//     matcher shape + response evidence).
//   - parse_poc_template: pocparse.Parse over a nuclei template, so the model
//     can read severity/CVE/CWE/matchers without a lossy YAML re-serialization.
package scannerhits

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/pocparse"
	"github.com/c1ayooo/xalgorix/v4/internal/sandbox"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
	"github.com/c1ayooo/xalgorix/v4/internal/verdict"
)

// maxTemplateBytes bounds a template read from disk. Real nuclei templates are
// a few KB; 1 MiB leaves room for bundled multi-request templates without
// letting a path mistake pull a huge file into the conversation.
const maxTemplateBytes = 1 << 20

// maxResponseBytes is the excerpt of a hit response that satisfies the
// "was a body preserved" check. The verdict only looks at emptiness and
// short-payload heuristics, so a bounded excerpt is enough.
const maxResponseBytes = 64 << 10

// Register adds the scanner-hit tools to the registry.
func Register(r *tools.Registry) {
	r.Register(&tools.Tool{
		Name: "assess_nuclei_hit",
		Description: "Deterministically triage one scanner/nuclei hit before reporting it. Returns the vulnerability " +
			"class, a verdict (confirmed | needs_review), the reasons, and the class-specific review checklist. " +
			"Status-only templates, 5xx responses, missing response bodies and extracted-value-less hits are " +
			"flagged needs_review: a scanner 'found' is not evidence. Pass the template YAML (or template_path) so " +
			"the matcher shape can be judged, plus the response text you actually saw.",
		Parameters: []tools.Parameter{
			{Name: "template_id", Description: "nuclei template id", Required: false},
			{Name: "name", Description: "template/result name", Required: false},
			{Name: "tags", Description: "comma-separated template tags", Required: false},
			{Name: "type", Description: "result type (http, dns, tcp, …); empty is treated as http", Required: false},
			{Name: "status", Description: "HTTP status code of the hit", Required: false},
			{Name: "response", Description: "response body or excerpt actually observed", Required: false},
			{Name: "extracted", Description: "comma-separated extracted matcher values", Required: false},
			{Name: "matched_at", Description: "matched-at URL", Required: false},
			{Name: "template", Description: "raw nuclei template YAML (for matcher-shape detection)", Required: false},
			{Name: "template_path", Description: "path to the template file (read via the path policy)", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return assessNucleiHit(r.GetScanContextID(), args)
		},
	})

	r.Register(&tools.Tool{
		Name: "parse_poc_template",
		Description: "Parse a nuclei template into normalized fields (id, name, severity, protocol, CVE/CWE, CVSS, " +
			"tags, references, digest) while keeping the raw YAML intact. Read-only: nothing is imported or executed.",
		Parameters: []tools.Parameter{
			{Name: "content", Description: "raw template YAML", Required: false},
			{Name: "path", Description: "path to a template file (read via the path policy)", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return parseTemplate(r.GetScanContextID(), args)
		},
	})
}

func splitCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func assessNucleiHit(contextID string, args map[string]string) (tools.Result, error) {
	template := args["template"]
	if template == "" {
		content, err := readTemplateFile(contextID, args["template_path"])
		if err != nil {
			return tools.Result{Error: err.Error()}, nil
		}
		template = content
	}
	status := 0
	if raw := strings.TrimSpace(args["status"]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return tools.Result{Error: "status must be an integer HTTP status code"}, nil
		}
		status = n
	}
	response := args["response"]
	if len(response) > maxResponseBytes {
		response = response[:maxResponseBytes]
	}
	obs := verdict.Observation{
		TemplateID: strings.TrimSpace(args["template_id"]),
		Name:       strings.TrimSpace(args["name"]),
		Tags:       splitCSV(args["tags"]),
		Type:       strings.TrimSpace(args["type"]),
		StatusCode: status,
		Response:   response,
		Extracted:  splitCSV(args["extracted"]),
		MatchedAt:  strings.TrimSpace(args["matched_at"]),
		Matcher:    verdict.ParseMatcherProfile(template),
	}
	if obs.TemplateID == "" && obs.Name == "" && obs.MatchedAt == "" {
		return tools.Result{Error: "assess_nuclei_hit needs template_id, name or matched_at to identify the hit"}, nil
	}
	decision := verdict.Assess(obs)

	text := fmt.Sprintf("verdict: %s\nclass: %s\n", decision.Verdict, decision.Class)
	if decision.NeedsManualReview() {
		text += "interpretation: this hit is not evidence yet — perform the checks below and re-test before reporting.\n"
	} else {
		text += "interpretation: the hit carries self-evident proof shape, but a scanner match is still not a reported vulnerability; keep the request/response and report only with reproduced impact.\n"
	}
	if len(decision.Reasons) > 0 {
		text += "reasons:\n"
		for _, reason := range decision.Reasons {
			text += "  - " + reason + "\n"
		}
	}
	if len(decision.Checks) > 0 {
		text += "checks:\n"
		for _, check := range decision.Checks {
			text += "  - " + check + "\n"
		}
	}
	payload, _ := json.MarshalIndent(decision, "", "  ")
	return tools.Result{
		Output: text + string(payload),
		Metadata: map[string]any{
			"scanner_verdict": decision.Verdict,
			"scanner_class":   string(decision.Class),
		},
	}, nil
}

func parseTemplate(contextID string, args map[string]string) (tools.Result, error) {
	content := args["content"]
	if strings.TrimSpace(content) == "" {
		read, err := readTemplateFile(contextID, args["path"])
		if err != nil {
			return tools.Result{Error: err.Error()}, nil
		}
		content = read
	}
	fallback := strings.TrimSpace(args["template_id"])
	tpl, err := pocparse.Parse([]byte(content), fallback)
	if err != nil {
		return tools.Result{Error: "template parse failed: " + err.Error()}, nil
	}
	view := map[string]any{
		"id":          tpl.TemplateID,
		"name":        tpl.Name,
		"severity":    tpl.Severity,
		"protocol":    tpl.Protocol,
		"cve":         tpl.CVEID,
		"cwe":         tpl.CWEID,
		"cvss":        tpl.CVSSScore,
		"tags":        tpl.Tags,
		"author":      tpl.Author,
		"description": tpl.Description,
		"references":  tpl.References,
		"digest":      tpl.Digest,
		"content_len": len(tpl.Content),
	}
	payload, _ := json.MarshalIndent(view, "", "  ")
	return tools.Result{Output: string(payload)}, nil
}

// readTemplateFile resolves and reads a template path through the shared path
// policy. Empty input is not an error (the caller reports a missing-source
// error with its own wording).
func readTemplateFile(contextID, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	sc := scanctx.Get(contextID)
	if sc == nil {
		return "", fmt.Errorf("no active scan context: pass the template content instead of a path")
	}
	canonical, err := sandbox.Default().CheckRead(sc, "parse_poc_template", raw)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("read template: %w", err)
	}
	if info.Size() > maxTemplateBytes {
		return "", fmt.Errorf("template is %d bytes, limit is %d", info.Size(), maxTemplateBytes)
	}
	data, err := os.ReadFile(canonical)
	if err != nil {
		return "", fmt.Errorf("read template: %w", err)
	}
	return string(data), nil
}
