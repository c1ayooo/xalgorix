// Package honeypot exposes the honeydetect signature engine as a read-only
// agent tool. The score is deterministic: it is a static substring match over
// the service view the model already observed, so a hit is a signal to reason
// about, never a verdict on its own.
package honeypot

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/honeydetect"
	"github.com/c1ayooo/xalgorix/v4/internal/tools"
)

// maxBodyBytes bounds the response-body excerpt one call may carry. The
// signature engine only does substring matching, so a prefix is enough.
const maxBodyBytes = 256 << 10

// Register adds detect_honeypot to the registry. signaturesPath is the
// operator-provisioned signature file (empty = use the embedded library).
func Register(r *tools.Registry, signaturesPath string) {
	r.Register(&tools.Tool{
		Name: "detect_honeypot",
		Description: "Score a service against the static honeypot signature library (Cowrie, OpenCanary, Kippo, " +
			"cloud sandboxes, …). Returns a 0-1 confidence score plus the matched signature names and sources. " +
			"Pass the banner/title/body/certificate fields you actually observed. A high score means " +
			"'verify before exploiting': re-check the service, look for implausibly perfect findings, and prefer " +
			"a different target surface — it is a signal, not proof that the target is a decoy.",
		Parameters: []tools.Parameter{
			{Name: "port", Description: "Service port (optional; 0 = unknown)", Required: false},
			{Name: "banner", Description: "Protocol banner / HTTP Server header", Required: false},
			{Name: "title", Description: "HTTP <title>", Required: false},
			{Name: "body", Description: "HTTP response body or excerpt", Required: false},
			{Name: "cert_subject", Description: "TLS certificate subject", Required: false},
			{Name: "cert_issuer", Description: "TLS certificate issuer", Required: false},
		},
		Execute: func(args map[string]string) (tools.Result, error) {
			return detect(args, signaturesPath)
		},
	})
}

func detect(args map[string]string, signaturesPath string) (tools.Result, error) {
	port := 0
	if raw := strings.TrimSpace(args["port"]); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 65535 {
			return tools.Result{Error: "port must be an integer between 0 and 65535"}, nil
		}
		port = n
	}
	body := args["body"]
	if len(body) > maxBodyBytes {
		body = body[:maxBodyBytes]
	}
	view := honeydetect.ServiceView{
		Port:        port,
		Banner:      args["banner"],
		Title:       args["title"],
		Body:        body,
		CertSubject: args["cert_subject"],
		CertIssuer:  args["cert_issuer"],
	}
	if view.Banner == "" && view.Title == "" && view.Body == "" && view.CertSubject == "" && view.CertIssuer == "" && view.Port == 0 {
		return tools.Result{Error: "detect_honeypot needs at least one observed field (port/banner/title/body/cert_*)"}, nil
	}
	engine := honeydetect.NewEngine(signaturesPath)
	score, evidence := engine.Evaluate(view)

	out := map[string]any{
		"score":    score,
		"evidence": evidence,
	}
	payload, _ := json.MarshalIndent(out, "", "  ")
	text := "honeypot score: " + strconv.FormatFloat(score, 'f', 2, 64) + " (0 = no signature matched, 1 = certain)\n"
	if score >= 0.6 {
		text += "interpretation: strong decoy signal — re-verify the service and look for implausibly perfect findings before reporting anything.\n"
	} else if score > 0 {
		text += "interpretation: weak signal — one obscure signature matched; treat as a caution flag, not proof.\n"
	} else {
		text += "interpretation: no static signature matched. This does not prove the target is real; absence of a signature is not proof of absence.\n"
	}
	return tools.Result{
		Output:   text + string(payload),
		Metadata: map[string]any{"honeypot_score": score, "honeypot_evidence": evidence},
	}, nil
}
