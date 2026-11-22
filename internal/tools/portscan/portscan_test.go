package portscan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseJSONL_NormalisesAndDeduplicates(t *testing.T) {
	const payload = `{"ip":"10.0.0.5","port":22,"protocol":"tcp","server":"OpenSSH 8.9"}
{"ip":"10.0.0.5","port":"22","protocol":"tcp","server":"OpenSSH 8.9"}
{"ip":"10.0.0.5","port":80,"protocol":"http","status":200,"title":"Admin","frameworks":[{"name":"nginx","version":"1.24"}]}
{"host":"10.0.0.9","port":443,"protocol":"https","frameworks":["tomcat"]}
`
	sum, err := parseJSONL(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("parseJSONL: %v", err)
	}
	if sum.Ports != 3 {
		t.Fatalf("expected 3 deduplicated host:port rows, got %d", sum.Ports)
	}
	if len(sum.Hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %v", sum.Hosts)
	}
	if sum.Frameworks != 2 {
		t.Fatalf("expected 2 frameworks, got %d", sum.Frameworks)
	}
	if got := sum.Records[0].Host; got != "10.0.0.5" {
		t.Fatalf("first record host = %q, want 10.0.0.5", got)
	}
	if got := sum.Records[0].Port; got != "22" {
		t.Fatalf("first record port = %q, want 22 (numeric port must format without .0)", got)
	}
	if got := sum.Records[0].Product; got != "OpenSSH 8.9" {
		t.Fatalf("server field = %q", got)
	}
	if got := sum.Records[1].Port; got != "80" {
		t.Fatalf("second record port = %q, want 80 (records sort by port)", got)
	}
}

func TestParseJSONL_SkipsMalformedLines(t *testing.T) {
	const payload = `not json
{"ip":"10.0.0.5","port":80}
[]

`
	sum, err := parseJSONL(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("parseJSONL: %v", err)
	}
	if sum.Ports != 1 {
		t.Fatalf("expected 1 valid row, got %d", sum.Ports)
	}
	if sum.Malformed != 2 {
		t.Fatalf("expected 2 malformed lines (garbage + array), got %d", sum.Malformed)
	}
}

func TestParseJSONL_EmptyInputIsValidNegative(t *testing.T) {
	sum, err := parseJSONL(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseJSONL: %v", err)
	}
	if sum.Ports != 0 || len(sum.Records) != 0 {
		t.Fatalf("expected no findings, got %+v", sum)
	}
}

func TestRenderSummary_TruncatesRowsAndReportsArchive(t *testing.T) {
	recs := make([]serviceRecord, 0, 120)
	for i := 0; i < 120; i++ {
		recs = append(recs, serviceRecord{Host: "10.0.0.5", Port: "80", Protocol: "tcp"})
	}
	sum := &scanSummary{Records: recs, Hosts: []string{"10.0.0.5"}, Ports: 120, Lines: 120}

	out := renderSummary(sum, "10.0.0.5", "top2", "/tmp/archive.jsonl", 10)
	if !strings.Contains(out, "hosts=1 open_ports=120") {
		t.Fatalf("header missing counters: %s", out)
	}
	if !strings.Contains(out, "110 more host:port rows omitted") {
		t.Fatalf("row cap not enforced: %s", out)
	}
	if !strings.Contains(out, "/tmp/archive.jsonl") {
		t.Fatalf("archive path missing: %s", out)
	}
	if strings.Count(out, "\n") > 20 {
		t.Fatalf("summary too long: %d lines", strings.Count(out, "\n"))
	}
}

func TestRenderSummary_NoFindingsIsNotAnError(t *testing.T) {
	sum := &scanSummary{}
	out := renderSummary(sum, "10.0.0.5", "top2", "", maxSummaryRows)
	if !strings.Contains(out, "No open ports") {
		t.Fatalf("valid-negative wording missing: %s", out)
	}
	if !strings.Contains(out, "hosts=0 open_ports=0") {
		t.Fatalf("counters missing: %s", out)
	}
}

func TestValidateTarget(t *testing.T) {
	for _, bad := range []string{"-sV", "10.0.0.5; rm -rf /", "a b", "http://x/$(id)"} {
		if err := validateTarget(bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
	for _, good := range []string{"10.0.0.5", "10.0.0.0/24", "app.example.com", "fd00::1"} {
		if err := validateTarget(good); err != nil {
			t.Fatalf("expected %q to be accepted, got %v", good, err)
		}
	}
}

func TestValidatePorts(t *testing.T) {
	for _, bad := range []string{"-p", "80;id", "80 443"} {
		if err := validatePorts(bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
	for _, good := range []string{"top2", "common", "all", "80,443,8000-8100"} {
		if err := validatePorts(good); err != nil {
			t.Fatalf("expected %q to be accepted, got %v", good, err)
		}
	}
}

func TestLocateGogo_PinnedPathIsUsed(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "gogo")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(gogoPathEnvVar, bin)

	got, err := locateGogo()
	if err != nil {
		t.Fatalf("locateGogo: %v", err)
	}
	if got != bin {
		t.Fatalf("locateGogo = %q, want %q", got, bin)
	}
}

func TestLocateGogo_InvalidPinnedPathIsNotSilentlyIgnored(t *testing.T) {
	t.Setenv(gogoPathEnvVar, filepath.Join(t.TempDir(), "does-not-exist"))

	if _, err := locateGogo(); err == nil || !strings.Contains(err.Error(), gogoPathEnvVar) {
		t.Fatalf("expected an explicit %s error, got %v", gogoPathEnvVar, err)
	}
}

func TestExecute_MissingBinaryIsSoftFailure(t *testing.T) {
	t.Setenv(gogoPathEnvVar, filepath.Join(t.TempDir(), "nope"))

	res, err := Execute(context.Background(), "", map[string]string{"target": "10.0.0.5"})
	if err != nil {
		t.Fatalf("soft failure must not return an error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("soft failure must not set Result.Error (circuit breaker): %q", res.Error)
	}
	if !strings.Contains(res.Output, "gogo binary not found") {
		t.Fatalf("missing install hint: %s", res.Output)
	}
}

func TestExecute_RejectsBadArgumentsWithoutRunning(t *testing.T) {
	cases := []map[string]string{
		{},
		{"target": "-sV"},
		{"target": "10.0.0.5", "ports": "80;id"},
		{"target": "10.0.0.5", "mode": "turbo"},
		{"target": "10.0.0.5", "timeout": "soon"},
	}
	for _, args := range cases {
		res, err := Execute(context.Background(), "", args)
		if err != nil {
			t.Fatalf("args %v: unexpected error %v", args, err)
		}
		if res.Error == "" {
			t.Fatalf("args %v: expected a validation error, got output %q", args, res.Output)
		}
	}
}

// fakeGogo writes a POSIX shell stub that ignores every flag except -f and
// emits a fixed JSONL payload there, so the full Execute path (binary
// resolution, child launch, parsing, archiving) can be exercised without the
// real scanner.
func fakeGogo(t *testing.T, payload string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "gogo")
	script := "#!/bin/sh\n" +
		"out=\"\"\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in\n" +
		"    -f) out=\"$2\"; shift 2 ;;\n" +
		"    *) shift ;;\n" +
		"  esac\n" +
		"done\n" +
		"if [ -z \"$out\" ]; then echo 'no -f flag' >&2; exit 2; fi\n" +
		"cat > \"$out\" <<'EOF'\n" + payload + "EOF\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestExecute_FakeGogoProducesStructuredResult(t *testing.T) {
	bin := fakeGogo(t, `{"ip":"10.0.0.5","port":22,"protocol":"tcp","server":"OpenSSH"}
{"ip":"10.0.0.5","port":80,"protocol":"http","status":200,"title":"App","frameworks":[{"name":"nginx","version":"1.24"}]}
`)
	t.Setenv(gogoPathEnvVar, bin)

	res, err := Execute(context.Background(), "", map[string]string{
		"target": "10.0.0.5",
		"ports":  "top2",
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected Result.Error: %q (output %s)", res.Error, res.Output)
	}
	if !strings.Contains(res.Output, "hosts=1 open_ports=2") {
		t.Fatalf("summary counters wrong: %s", res.Output)
	}
	if !strings.Contains(res.Output, "nginx 1.24") {
		t.Fatalf("framework not rendered: %s", res.Output)
	}

	md := res.Metadata
	if md == nil {
		t.Fatal("metadata missing")
	}
	if got, _ := md["open_ports"].(int); got != 2 {
		t.Fatalf("metadata open_ports = %v, want 2", md["open_ports"])
	}
	archive, _ := md["archive"].(string)
	if archive == "" {
		t.Fatal("metadata archive path missing")
	}
	if _, statErr := os.Stat(archive); statErr != nil {
		t.Fatalf("archive file not written: %v", statErr)
	}
}

func TestExecute_FakeGogoEmptyResultIsValidNegative(t *testing.T) {
	t.Setenv(gogoPathEnvVar, fakeGogo(t, ""))

	res, err := Execute(context.Background(), "", map[string]string{"target": "10.0.0.5"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("empty scan must be a valid negative, got error %q", res.Error)
	}
	if !strings.Contains(res.Output, "No open ports") {
		t.Fatalf("expected valid-negative wording, got: %s", res.Output)
	}
}

func TestBuildArgs_OnlyUsesConfirmedFlags(t *testing.T) {
	args := buildArgs(scanOptions{Target: "10.0.0.5", Ports: "top2"}, "/tmp/out.jsonl")
	joined := strings.Join(args, " ")
	for _, want := range []string{"-i 10.0.0.5", "-p top2", "-o jl", "-f /tmp/out.jsonl", "--no-color"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	if strings.Contains(joined, "-m ") {
		t.Fatalf("default mode must not emit -m: %v", args)
	}

	deep := buildArgs(scanOptions{Target: "t", Ports: "all", Mode: "sc"}, "/tmp/o")
	if !strings.Contains(strings.Join(deep, " "), "-m sc") {
		t.Fatalf("explicit mode not forwarded: %v", deep)
	}
}
