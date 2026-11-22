// Command toolshim executes filesystem and code-search tool operations inside a
// project container.
//
// The platform already decided that the path is allowed (sandbox.Policy) before
// dispatching; the shim re-checks containment inside the container as defence in
// depth and refuses anything outside the mounted scan root. It is a pure
// JSON-in/JSON-out process so the platform's tools can reuse it over the runtime's
// exec channel without linking the container filesystem into the Go process.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type request struct {
	Op         string `json:"op"`
	Root       string `json:"root"`
	Path       string `json:"path"`
	ViewRange  string `json:"view_range,omitempty"`
	FileText   string `json:"file_text,omitempty"`
	OldStr     string `json:"old_str,omitempty"`
	NewStr     string `json:"new_str,omitempty"`
	InsertLine string `json:"insert_line,omitempty"`
	Pattern    string `json:"pattern,omitempty"`
	Include    string `json:"include,omitempty"`
	Query      string `json:"query,omitempty"`
	Sinks      string `json:"sinks,omitempty"`
	Glob       string `json:"glob,omitempty"`
	Max        string `json:"max,omitempty"`
}

type response struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

func main() {
	reqPath := ""
	for i, arg := range os.Args[1:] {
		if arg == "--request" && i+2 <= len(os.Args)-1 {
			reqPath = os.Args[i+2]
		}
	}
	var reader *bufio.Reader
	if reqPath != "" {
		file, err := os.Open(reqPath)
		if err != nil {
			write(response{Error: fmt.Sprintf("open request file: %v", err)})
			return
		}
		defer func() { _ = file.Close() }()
		reader = bufio.NewReader(file)
	} else {
		reader = bufio.NewReader(os.Stdin)
	}
	var req request
	if err := json.NewDecoder(reader).Decode(&req); err != nil {
		write(response{Error: fmt.Sprintf("decode request: %v", err)})
		return
	}
	out, err := dispatch(req)
	if err != nil {
		write(response{Error: err.Error()})
		return
	}
	write(response{Output: out})
}

func write(r response) {
	_ = json.NewEncoder(os.Stdout).Encode(r)
}

func dispatch(req request) (string, error) {
	if req.Op != "code_search" {
		if err := contain(req.Root, req.Path); err != nil {
			return "", err
		}
	}
	switch req.Op {
	case "view":
		return view(req.Path, req.ViewRange)
	case "create":
		return create(req.Path, req.FileText)
	case "str_replace":
		return replace(req.Path, req.OldStr, req.NewStr)
	case "insert":
		return insert(req.Path, req.NewStr, req.InsertLine)
	case "list":
		return list(req.Path)
	case "search":
		return search(req.Path, req.Pattern, req.Include)
	case "code_search":
		return codeSearch(req)
	default:
		return "", fmt.Errorf("unknown op %q", req.Op)
	}
}

// contain rejects paths that escape the mounted scan root.
func contain(root, path string) error {
	if root == "" {
		return nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %s is outside the scan root %s", absPath, absRoot)
	}
	return nil
}

func view(path, viewRange string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	start, end := 1, len(lines)
	if viewRange != "" {
		parts := strings.SplitN(viewRange, "-", 2)
		if len(parts) != 2 {
			return "", fmt.Errorf("invalid view_range %q (expected start-end)", viewRange)
		}
		if parsed, convErr := strconv.Atoi(parts[0]); convErr == nil {
			start = parsed
		}
		if parsed, convErr := strconv.Atoi(parts[1]); convErr == nil {
			end = parsed
		}
	}
	if start < 1 {
		start = 1
	}
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	for i := start; i <= end && i-1 < len(lines); i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i, lines[i-1])
	}
	return b.String(), nil
}

func create(path, text string) (string, error) {
	if _, statErr := os.Stat(path); statErr == nil {
		return "", fmt.Errorf("file already exists: %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("create parent directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	return fmt.Sprintf("File created: %s", path), nil
}

func replace(path, oldStr, newStr string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read file: %w", err)
	}
	content := string(data)
	if oldStr == "" {
		return "", fmt.Errorf("old_str is required")
	}
	count := strings.Count(content, oldStr)
	switch count {
	case 0:
		return "", fmt.Errorf("old_str not found in %s", path)
	case 1:
	default:
		return "", fmt.Errorf("old_str is not unique in %s (%d occurrences)", path, count)
	}
	updated := strings.Replace(content, oldStr, newStr, 1)
	if err := os.WriteFile(path, []byte(updated), 0o600); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	return fmt.Sprintf("Replaced 1 occurrence in %s", path), nil
}

func insert(path, text, insertLine string) (string, error) {
	line, err := strconv.Atoi(strings.TrimSpace(insertLine))
	if err != nil {
		return "", fmt.Errorf("invalid insert_line %q", insertLine)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	if line < 0 || line > len(lines) {
		return "", fmt.Errorf("insert_line %d out of range (0..%d)", line, len(lines))
	}
	inserted := make([]string, 0, len(lines)+1)
	inserted = append(inserted, lines[:line]...)
	inserted = append(inserted, text)
	inserted = append(inserted, lines[line:]...)
	if err := os.WriteFile(path, []byte(strings.Join(inserted, "\n")), 0o600); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}
	return fmt.Sprintf("Inserted content after line %d in %s", line, path), nil
}

func list(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("cannot read directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var b strings.Builder
	for _, entry := range entries {
		suffix := ""
		if entry.IsDir() {
			suffix = "/"
		}
		fmt.Fprintf(&b, "%s%s\n", entry.Name(), suffix)
	}
	return b.String(), nil
}

func search(dir, pattern, include string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid pattern: %w", err)
	}
	if include != "" {
		if _, matchErr := filepath.Match(include, "x"); matchErr != nil {
			return "", fmt.Errorf("invalid include glob: %w", matchErr)
		}
	}
	var b strings.Builder
	matches := 0
	err = filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if matches >= 200 {
			return filepath.SkipAll
		}
		if include != "" {
			if ok, matchErr := filepath.Match(include, info.Name()); matchErr != nil || !ok {
				return nil
			}
		}
		if info.Size() > 2<<20 {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				fmt.Fprintf(&b, "%s:%d:%s\n", path, i+1, line)
				matches++
				if matches >= 200 {
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if matches == 0 {
		return "No matches found.", nil
	}
	if matches >= 200 {
		b.WriteString("[truncated at 200 matches]\n")
	}
	return b.String(), nil
}

// codeSearch uses ripgrep, which the toolbox image always provides.
func codeSearch(req request) (string, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" && req.Sinks != "" {
		query = sinkPattern(req.Sinks)
	}
	if query == "" {
		return "", fmt.Errorf("provide 'query' or 'sinks'")
	}
	max := 60
	if parsed, err := strconv.Atoi(strings.TrimSpace(req.Max)); err == nil && parsed > 0 {
		max = parsed
	}
	if max > 200 {
		max = 200
	}
	dir := req.Root
	if req.Path != "" {
		dir = filepath.Join(req.Root, req.Path)
	}
	args := []string{"-n", "--no-heading", "--color", "never", "-m", strconv.Itoa(max), "-e", query}
	if req.Glob != "" {
		args = append(args, "-g", req.Glob)
	}
	args = append(args, dir)
	cmd := exec.Command("rg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "No matches found.", nil
		}
		return "", fmt.Errorf("ripgrep: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// sinkPattern mirrors the platform's curated sink catalogue closely enough for
// container execution; the platform-side catalogue remains authoritative.
func sinkPattern(sinks string) string {
	switch strings.ToLower(strings.TrimSpace(sinks)) {
	case "rce", "cmdi":
		return `(exec|system|popen|spawn|Runtime\.getRuntime|ProcessBuilder|eval|child_process)`
	case "sqli":
		return `(SELECT|INSERT|UPDATE|DELETE).*(FROM|INTO|WHERE)|query\(|execute\(`
	case "ssrf":
		return `(http\.Get|requests\.(get|post)|urllib|fetch|axios\.(get|post)|RestTemplate)`
	case "fileio":
		return `(open\(|readFile|writeFile|os\.ReadFile|FileInputStream|include\()`
	case "template":
		return `(\{\{|render_template|Template\(|Thymeleaf|freemarker)`
	case "secrets":
		return `(AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----|api[_-]?key\s*=|secret\s*=)`
	case "auth", "redirect", "crypto", "deserialization":
		return `(deserialize|unserialize|pickle\.loads|ObjectInputStream|redirect|jwt|md5|sha1)`
	default:
		return ""
	}
}
