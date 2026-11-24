package honeypot

import (
	"strings"
	"testing"
)

func TestDetectMatchesEmbeddedSignature(t *testing.T) {
	res, err := detect(map[string]string{
		"port":   "22",
		"banner": "SSH-2.0-OpenSSH_6.0p1 Debian-4+deb7u2",
	}, "")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected Result.Error: %s", res.Error)
	}
	score, _ := res.Metadata["honeypot_score"].(float64)
	if score < 0.9 {
		t.Fatalf("expected a high score for the Cowrie default banner, got %.2f\n%s", score, res.Output)
	}
	if !strings.Contains(res.Output, "cowrie_default_ssh_banner") {
		t.Fatalf("output missing the matched signature name:\n%s", res.Output)
	}
}

func TestDetectNoMatchIsZeroScore(t *testing.T) {
	res, err := detect(map[string]string{"banner": "SSH-2.0-OpenSSH_9.6 Ubuntu-3"}, "")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	score, _ := res.Metadata["honeypot_score"].(float64)
	if score != 0 {
		t.Fatalf("unexpected match score %.2f for a stock banner:\n%s", score, res.Output)
	}
	if !strings.Contains(res.Output, "absence of a signature is not proof of absence") {
		t.Fatalf("zero-score result must not read as 'target is real' proof:\n%s", res.Output)
	}
}

func TestDetectRequiresAnObservedField(t *testing.T) {
	res, err := detect(map[string]string{}, "")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected a Result.Error when no observation is supplied")
	}
}

func TestDetectRejectsBadPort(t *testing.T) {
	res, _ := detect(map[string]string{"port": "70000", "banner": "x"}, "")
	if res.Error == "" {
		t.Fatal("expected a Result.Error for an out-of-range port")
	}
}
