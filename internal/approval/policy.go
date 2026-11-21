package approval

// Policy — approval configuration + the read-only reconnaissance allowlist.
//
// The allowlist is the milestone acceptance hard gate (REDESIGN §9 R2
// acceptance: "无审批放行类目单" pinned by tests): every category below has
// one positive and one negative example in approval_test.go. Default entries
// are deliberately narrow — passive recon and read-only network queries; the
// operator extends them via XALGORIX_APPROVAL_ALLOWLIST (comma-separated,
// appended to the built-in list; an entry matches a command segment whose
// program basename equals the entry, case-insensitive).
//
// Honest scope statement (charter review HIGH-2): this is a BEST-EFFORT
// passive-recon classifier, not a shell interpreter. It cannot prove a
// command is safe — it can only recognize the narrow shapes above. Anything
// it cannot classify cleanly goes to approval (over-blocking is the correct
// failure mode: an unnecessary approval costs seconds; a missed write costs
// the authorization boundary). Known blind spots drive the deny rules below:
// command substitution, redirects, text tools with write/execute escapes
// (awk/sed/sort are deliberately NOT on the default list), inline loader/path
// shadowing, and nmap output flags that touch the filesystem.
//
// Matching semantics: a command is split into segments on shell control
// operators (;, &&, ||, |, newline, backtick). Every segment must pass for
// the whole command to pass — "nmap -sV x && rm -rf /" therefore requires
// approval.

import (
	"regexp"
	"strings"
	"time"
)

// Mode toggles the whole pipeline. Default off (D5 decision recorded in
// REDESIGN §5 and the R2a report): enforce is an operator choice, not a
// factory default — a headless scheduled scan would otherwise stall on every
// non-allowlisted command until the operator answers.
type Mode string

const (
	ModeOff     Mode = "off"
	ModeEnforce Mode = "enforce"
)

// Policy is the effective approval configuration (config-backed).
type Policy struct {
	Mode       Mode
	Timeout    time.Duration
	Allowlist  []string // operator additions, appended to the built-ins
	MaxPending int
}

// builtInAllowPrograms are unconditionally allowed read-only recon programs.
// Segment matching uses the program basename, so /usr/bin/subfinder passes.
var builtInAllowPrograms = map[string]struct{}{
	"subfinder": {},
	"httpx":     {},
	"dnsx":      {},
	"amass":     {},
	"whois":     {},
	"dig":       {},
	"host":      {},
	"nslookup":  {},
	"whatweb":   {},
}

// textFilterPrograms are read-only text processors that recon pipelines feed
// results into ("dig example.com | grep NXDOMAIN"). They consume stdin/args
// and write stdout only.
//
// awk/sed/sort are deliberately ABSENT (charter review HIGH-2): all three
// carry write/execute escapes (awk system(), sed 'e'/w-files/-i, sort -o
// file) that a best-effort classifier cannot reliably rule out, so they go
// to approval by default; an operator who accepts the risk can append them
// via XALGORIX_APPROVAL_ALLOWLIST.
var textFilterPrograms = map[string]struct{}{
	"grep": {}, "egrep": {}, "fgrep": {}, "zgrep": {},
	"cut": {}, "tr": {}, "uniq": {},
	"head": {}, "tail": {}, "wc": {}, "cat": {}, "jq": {},
	"strings": {}, "xxd": {}, "base64": {}, "echo": {}, "printf": {},
	"nl": {}, "column": {}, "fold": {}, "fmt": {},
}

// dangerousInlineEnvVars are inline environment assignments that let a
// decoy binary masquerade as an allowlisted program (PATH=), inject code
// into a child process (LD_PRELOAD=), or reroute interpreters
// (PYTHONPATH=/PYTHONHOME=). A segment prefixing any of these (even before
// an otherwise-allowlisted program) leaves the allowlist.
var dangerousInlineEnvVars = map[string]struct{}{
	"path":            {},
	"ld_preload":      {},
	"ld_library_path": {},
	"pythonpath":      {},
	"pythonhome":      {},
}

// commandSubstitutionRe matches $(...) substitution and process substitution
// <(...); the backtick form is split into segments by segmentSplitter.
var commandSubstitutionRe = regexp.MustCompile(`\$\(|<\(`)

// inlineCodeExecutionRe matches C-family runtime escapes (awk system(),
// python eval/os.system inside a -c string, perl system) — defense in depth
// for operator-appended text tools that would otherwise bypass the
// awk/sed/sort removal (HIGH-2).
var inlineCodeExecutionRe = regexp.MustCompile(`(?i)system\s*\(|\beval\s*\(|os\.system\s*\(|popen\s*\(`)

// redirectTokenRe matches shell redirection operators, whether standalone
// (">", ">>", "<") or prefix-attached to their target ("2>/dev/null",
// ">file", "<input", ">&2", "&>>log"). Quoted redirection-looking arguments
// ('<div', "<file") start with a quote character and are NOT matched — an
// over-block here just routes the command to approval, which is the
// sanctioned failure mode.
var redirectTokenRe = regexp.MustCompile(`^\d*[<>]|^&>>?`)

// nmapOutputFlags are nmap flags that write files (-oN/-oG/-oX/-oA/-oS
// plus --append-output which amends files). Writing scan output to an
// operator-chosen path is a filesystem side effect, not read-only recon.
var nmapOutputFlags = []string{"-on", "-og", "-ox", "-oa", "-os", "--append-output"}

// readMethodTokens are the HTTP methods a curl invocation may use to stay on
// the allowlist.
var readMethodTokens = map[string]struct{}{
	"get":     {},
	"head":    {},
	"options": {},
}

// curlDataFlags force a write (request body / upload) and disqualify curl.
// Short flags match case-sensitively: -T uploads, -d sends a body, -F posts a
// form; their lowercase siblings are unrelated read-only flags.
var curlDataFlags = map[string]struct{}{
	"-d": {}, "--data": {}, "--data-raw": {}, "--data-binary": {},
	"--data-urlencode": {}, "-F": {}, "--form": {}, "--form-string": {},
	"-T": {}, "--upload-file": {}, "--json": {},
}

// safeNmapScriptCategories are NSE categories allowed with --script; any
// other value (e.g. vuln, exploit, intrusive, brute) disqualifies nmap.
var safeNmapScriptCategories = map[string]struct{}{
	"safe":      {},
	"default":   {},
	"discovery": {},
	"auth":      {},
}

// segmentSplitter splits a command line on shell control operators.
var segmentSplitter = regexp.MustCompile(`(?:&&|\|\||;|\||\n|` + "`" + `)`)

// authFieldRe matches auth-bearing field assignments in request bodies
// ("password=x", '"session": "abc"', "token ="). Value-only mentions
// ("query=password policy") do NOT match — a search string that happens to
// contain the word stays on the audit trail instead of the approval queue.
var authFieldRe = regexp.MustCompile(`(password|passwd|token|session|cookie|secret|credential|apikey|api_key)\s*"?\s*[=:]\s*"?`)

// allowCommand reports whether the whole command line qualifies for the
// read-only recon allowlist, returning the matching rule name for the audit
// interpretation. Best-effort by design: any segment carrying a shape the
// classifier cannot vouch for (substitution, redirection, loader/path
// shadowing, unknown program) sends the whole command to approval.
func (p *Policy) allowCommand(command string) (string, bool) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "", false
	}
	allowAll := map[string]struct{}{}
	for prog := range builtInAllowPrograms {
		allowAll[prog] = struct{}{}
	}
	for _, entry := range p.Allowlist {
		allowAll[strings.ToLower(strings.TrimSpace(entry))] = struct{}{}
	}
	for _, segment := range segmentSplitter.Split(trimmed, -1) {
		seg := strings.TrimSpace(segment)
		if seg == "" {
			continue
		}
		if reason := rejectSegmentShape(seg); reason != "" {
			return reason, false
		}
		program, rest := splitProgram(seg)
		program = strings.ToLower(program)
		switch program {
		case "nmap":
			if nmapReadOnly(rest) {
				continue
			}
			return "nmap with non-read-only flags", false
		case "curl":
			if curlReadOnly(rest) {
				continue
			}
			return "curl with write flags", false
		default:
			if _, ok := allowAll[program]; ok {
				continue
			}
			if _, ok := textFilterPrograms[program]; ok {
				continue
			}
			return "program outside read-only allowlist: " + program, false
		}
	}
	return "read-only recon allowlist", true
}

// rejectSegmentShape refuses command shapes the classifier cannot vouch for
// regardless of the program involved (HIGH-2 deny rules). Empty reason = no
// objection.
func rejectSegmentShape(segment string) string {
	// Command / process substitution: the substituted text is an arbitrary
	// second command the classifier never sees ($(curl evil|sh), <(cmd)).
	if commandSubstitutionRe.MatchString(segment) {
		return "command substitution is not classifiable"
	}
	// Runtime escapes (awk BEGIN{system(...)}, python -c os.system(...)):
	// refused even in operator-appended programs.
	if inlineCodeExecutionRe.MatchString(segment) {
		return "inline code execution escape (system/eval/popen)"
	}
	// Redirection (write to file / read from device): any token carrying a
	// redirection operator defeats "stdout-only" promises.
	for _, token := range strings.Fields(segment) {
		if redirectTokenRe.MatchString(token) {
			return "shell redirection is not read-only"
		}
	}
	// Loader/interpreter shadowing: PATH=/LD_PRELOAD=… before the program
	// name makes the allowlisted basename untrustworthy.
	for _, token := range strings.Fields(segment) {
		if idx := strings.IndexByte(token, '='); idx > 0 && !strings.HasPrefix(token, "-") {
			varName := strings.ToLower(token[:idx])
			if _, dangerous := dangerousInlineEnvVars[varName]; dangerous {
				return "inline environment assignment shadows the execution environment: " + token[:idx]
			}
		}
	}
	return ""
}

// splitProgram peels the first token off a command segment, stripping leading
// env assignments and path prefixes (/usr/bin/nmap → nmap).
func splitProgram(segment string) (string, string) {
	fields := strings.Fields(segment)
	for len(fields) > 0 && strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "-") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return "", ""
	}
	program := fields[0]
	if idx := strings.LastIndexByte(program, '/'); idx >= 0 {
		program = program[idx+1:]
	}
	rest := strings.TrimSpace(strings.TrimPrefix(segment, fields[0]))
	return program, rest
}

// nmapReadOnly allows plain scanning flags (-sV/-sn/-A/…) and refuses NSE
// scripts outside the safe categories plus filesystem-writing output flags
// (-oN/-oG/-oX/-oA/-oS, --append-output). The decision to allow -sV is D5's
// explicit example ("nmap -sV 类"); writing scan output to an operator-chosen
// path is a side effect, not read-only recon.
func nmapReadOnly(rest string) bool {
	fields := strings.Fields(rest)
	for i := 0; i < len(fields); i++ {
		f := strings.ToLower(fields[i])
		for _, out := range nmapOutputFlags {
			if f == out || strings.HasPrefix(f, out+"=") {
				return false
			}
		}
		if f == "--script" || strings.HasPrefix(f, "--script=") {
			value := ""
			if f == "--script" && i+1 < len(fields) {
				value = fields[i+1]
			} else if strings.HasPrefix(f, "--script=") {
				value = strings.TrimPrefix(f, "--script=")
			}
			if value != "" && !safeNmapScripts(value) {
				return false
			}
		}
	}
	return true
}

// safeNmapScripts validates a comma-separated --script value: every element
// must be a safe category name (script filenames stay refused — a named
// script's behavior is not knowable at classification time).
func safeNmapScripts(value string) bool {
	for _, item := range strings.Split(value, ",") {
		if _, ok := safeNmapScriptCategories[strings.TrimSpace(strings.ToLower(item))]; !ok {
			return false
		}
	}
	return true
}

// curlReadOnly allows curl only for read methods without body/upload flags.
// Short flags are matched case-sensitively (curl: -X = method, -x = proxy,
// -d = data, -D = dump-header) — lowercasing them would confuse write and
// read variants.
func curlReadOnly(rest string) bool {
	fields := strings.Fields(rest)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if _, blocked := curlDataFlags[f]; blocked {
			return false
		}
		method := ""
		switch {
		case f == "-X" || f == "--request":
			if i+1 < len(fields) {
				method = fields[i+1]
			}
		case strings.HasPrefix(f, "--request="):
			method = strings.TrimPrefix(f, "--request=")
		case strings.HasPrefix(f, "-X") && len(f) > 2:
			method = f[2:]
		}
		if method != "" {
			if _, ok := readMethodTokens[strings.ToLower(method)]; !ok {
				return false
			}
		}
	}
	return true
}
