package terminal

// Engine-side observation capture.
//
// The information model is fed by the model calling add_hypothesis_evidence,
// which is a *voluntary* act: a scan that ends on its budget, loses the LLM
// connection, or simply reports a finding directly never writes that record,
// and the information space stays empty while the finding list is not.
//
// This file closes that hole for the highest-value case: the moment a command's
// own output proves that a sensitive artifact is being served (a 200 on .env /
// backup.sql / .git/config ...) or leaks credential material, the engine
// records the observation itself. It is deliberately narrow:
//
//   - only patterns with a low false-positive rate are matched;
//   - secret VALUES never reach the journal: every match is masked before the
//     excerpt is built (the journal and its PG replica must not become a
//     credential store);
//   - at most maxObservationsPerCommand items per command, so a directory
//     bust cannot flood the cold path;
//   - failures are logged, never propagated: observation capture must not turn
//     a successful command into a failed tool call.

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/infoitem"
	"github.com/xalgord/xalgorix/v4/internal/scanctx"
)

const (
	// maxObservationScanBytes bounds how much of a command's output is
	// inspected. Terminal output is already capped upstream; this keeps the
	// regex pass cheap on very large dumps.
	maxObservationScanBytes = 256 << 10
	// maxObservationsPerCommand bounds journal writes per tool call.
	maxObservationsPerCommand = 3
)

// sensitivePathPatterns match "this sensitive artifact was actually served",
// not merely "the word .env appeared": a status code or a body marker must be
// on the same line.
var sensitivePathPatterns = []struct {
	label string
	re    *regexp.Regexp
}{
	{"env_file", regexp.MustCompile(`(?i)\b(?:200|HTTP/[0-9.]+ 200)\b[^\n]{0,160}?/?\.env(?:\.(?:bak|old|save|prod|local))?\b`)},
	{"backup_file", regexp.MustCompile(`(?i)\b(?:200|HTTP/[0-9.]+ 200)\b[^\n]{0,160}?/?(?:backup|dump|db|database)[\w.-]*\.(?:sql|zip|tar|gz|bak)\b`)},
	{"vcs_metadata", regexp.MustCompile(`(?i)\b(?:200|HTTP/[0-9.]+ 200)\b[^\n]{0,160}?/\.(?:git/config|git/HEAD|svn/entries|hg/hgrc)\b`)},
	{"private_key_file", regexp.MustCompile(`(?i)\b(?:200|HTTP/[0-9.]+ 200)\b[^\n]{0,160}?/?(?:id_rsa|id_dsa|id_ecdsa|id_ed25519|\.ssh/[\w.-]+|\.htpasswd|\.npmrc|\.pypirc|credentials\.json)\b`)},
	{"php_config", regexp.MustCompile(`(?i)\b(?:200|HTTP/[0-9.]+ 200)\b[^\n]{0,160}?/?(?:config|settings|wp-config)\.php(?:\.(?:bak|old|save|txt))?\b`)},
}

// credentialPatterns match secret material inside a command's output. Each
// match is masked before it is written anywhere.
var credentialPatterns = []struct {
	label string
	re    *regexp.Regexp
}{
	{"aws_access_key_id", regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{"private_key_block", regexp.MustCompile(`-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----`)},
	{"sql_connection_string", regexp.MustCompile(`(?i)\b(?:mysql|postgres|postgresql|mongodb(?:\+srv)?|redis)://[^\s:@/]+:[^\s@/]{3,}@[^\s]+`)},
}

// credentialAssignment matches KEY=VALUE lines whose KEY looks secret-bearing.
var credentialAssignment = regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:PASSWORD|PASSWD|SECRET|TOKEN|API_KEY|ACCESS_KEY|PRIVATE_KEY|CLIENT_SECRET)[A-Z0-9_]*)\s*[:=]\s*("[^"]*"|'[^']*'|[^\s"']{3,})`)

// observeSensitiveOutput inspects one command's output and records up to
// maxObservationsPerCommand observed InfoItems. It never returns an error to
// the caller: a tool call must not fail because observation capture did.
func observeSensitiveOutput(contextID, command, output string) {
	if strings.TrimSpace(output) == "" || contextID == "" {
		return
	}
	sc := scanctx.Get(contextID)
	if sc == nil || sc.Ledger == nil {
		return
	}
	head := output
	if len(head) > maxObservationScanBytes {
		head = head[:maxObservationScanBytes]
	}
	written := 0
	emit := func(label, subject, excerpt, purpose string) {
		if written >= maxObservationsPerCommand {
			return
		}
		subject = maskObservationLine(subject)
		item := buildObservationItem(label, purpose, command, subject, excerpt)
		if err := sc.Ledger.AppendToolObservation(item, fmt.Sprintf("%s: %s", label, subject)); err != nil {
			log.Printf("[terminal] observation capture failed (%s): %v", label, err)
			return
		}
		written++
	}
	for _, p := range sensitivePathPatterns {
		line := p.re.FindString(head)
		if line == "" {
			continue
		}
		emit(p.label, strings.TrimSpace(line), strings.TrimSpace(line), "sensitive_artifact_served")
	}
	for _, p := range credentialPatterns {
		if m := p.re.FindString(head); m != "" {
			emit(p.label, m, maskCredential(m), "credential_material_observed")
		}
	}
	if m := credentialAssignment.FindStringSubmatch(head); len(m) == 3 {
		emit("credential_assignment", m[0], m[1]+"=***", "credential_material_observed")
	}
}

func buildObservationItem(label, purpose, command, subjectLine, excerpt string) *infoitem.Item {
	id, err := infoitem.NewID()
	if err != nil {
		id = fmt.Sprintf("obs-%d-%s", time.Now().UnixNano(), label)
	}
	now := time.Now().UTC()
	cmd := truncateText(command, 300)
	return &infoitem.Item{
		ID:               id,
		Nature:           infoitem.NatureObserved,
		CoordinateStatus: infoitem.CoordinatesComplete,
		Coordinates: infoitem.Coordinates{
			Content: infoitem.Content{
				Text:      truncateText(excerpt, 1200),
				MediaType: "text/plain",
			},
			Source: infoitem.Source{
				Kind: "engine",
				Name: "terminal_execute",
			},
			ObservedAt: now,
			Subject: infoitem.Subject{
				Kind:       "observation",
				Identifier: truncateText(subjectLine, 200),
			},
			Purpose: purpose,
			Context: map[string]string{
				"pattern": label,
				"command": cmd,
			},
		},
		CreatedAt: now,
	}
}

// maskCredential keeps a recognisable prefix/suffix while removing the secret
// itself. Anything unrecognised is fully masked.
func maskCredential(s string) string {
	switch {
	case strings.HasPrefix(s, "AKIA") && len(s) >= 12:
		return s[:4] + "****************" + s[len(s)-4:]
	case strings.Contains(s, "PRIVATE KEY-----"):
		return "-----BEGIN PRIVATE KEY----- (redacted)"
	case strings.Contains(s, "://"):
		if at := strings.LastIndex(s, "@"); at > 0 {
			scheme := ""
			if i := strings.Index(s, "://"); i > 0 {
				scheme = s[:i+3]
			}
			return scheme + "***:***" + s[at:]
		}
	}
	if len(s) > 8 {
		return s[:2] + "***" + s[len(s)-2:]
	}
	return "***"
}

func firstNonEmptyLine(output, fallback string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			return truncateText(strings.TrimSpace(line), 200)
		}
	}
	return truncateText(fallback, 200)
}

// maskObservationLine strips credential material from a single line before it
// is used as an InfoItem subject or journal summary. The excerpt path masks
// per-match already; this closes the other two sinks (subject_identifier and
// the journal summary) so no raw secret can reach the journal or its PG copy.
func maskObservationLine(line string) string {
	out := line
	for _, p := range credentialPatterns {
		out = p.re.ReplaceAllStringFunc(out, maskCredential)
	}
	out = credentialAssignment.ReplaceAllString(out, "$1=***")
	return out
}

func truncateText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}
