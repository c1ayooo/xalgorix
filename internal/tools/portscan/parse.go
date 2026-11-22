package portscan

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// maxJSONLine bounds a single result line. gogo emits one compact object per
// line; anything larger is a runaway/corrupt line and is skipped rather than
// buffered forever.
const maxJSONLine = 4 << 20 // 4 MiB

// framework is the normalised shape of a detected technology.
type framework struct {
	Name    string
	Version string
}

// serviceRecord is one deduplicated host:port observation.
type serviceRecord struct {
	Host       string
	Port       string
	Protocol   string
	Status     string
	Title      string
	Product    string
	OS         string
	URI        string
	Frameworks []framework
}

// scanSummary is the streaming aggregate of a gogo run. It never holds the raw
// JSON lines: each line is decoded, normalised into serviceRecord, deduplicated
// by host|port|protocol, and discarded.
type scanSummary struct {
	Records    []serviceRecord
	Hosts      []string
	Lines      int // non-empty JSON lines read
	Malformed  int // lines that did not decode as a JSON object
	Ports      int // deduplicated host:port observations
	Frameworks int
}

// parseJSONL consumes gogo's `-o jl` output.
func parseJSONL(r io.Reader) (*scanSummary, error) {
	sum := &scanSummary{}
	br := bufio.NewReaderSize(r, 64<<10)
	seen := make(map[string]struct{})
	hostSet := make(map[string]struct{})
	fwSet := make(map[string]struct{})

	for {
		line, readErr := br.ReadBytes('\n')
		if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
			if len(trimmed) > maxJSONLine {
				sum.Malformed++
			} else if rec, ok := decodeRecord(trimmed); ok {
				sum.Lines++
				key := rec.Host + "|" + rec.Port + "|" + rec.Protocol
				if _, dup := seen[key]; !dup {
					seen[key] = struct{}{}
					sum.Records = append(sum.Records, rec)
					sum.Ports++
					if rec.Host != "" {
						hostSet[rec.Host] = struct{}{}
					}
					for _, fw := range rec.Frameworks {
						id := fw.Name + "@" + fw.Version
						if _, dup := fwSet[id]; !dup && (fw.Name != "" || fw.Version != "") {
							fwSet[id] = struct{}{}
							sum.Frameworks++
						}
					}
				}
			} else {
				sum.Malformed++
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return sum, readErr
		}
	}

	for host := range hostSet {
		sum.Hosts = append(sum.Hosts, host)
	}
	sort.Slice(sum.Hosts, func(i, j int) bool { return hostLess(sum.Hosts[i], sum.Hosts[j]) })
	sort.SliceStable(sum.Records, func(i, j int) bool {
		a, b := sum.Records[i], sum.Records[j]
		if a.Host != b.Host {
			return hostLess(a.Host, b.Host)
		}
		return portLess(a.Port, b.Port)
	})
	return sum, nil
}

// decodeRecord normalises one gogo JSON object. The field names below are the
// ones gogo emits today; every lookup is tolerant because the scanner has
// changed key casing/spelling across versions and a strict decoder would turn
// a version bump into a silent "zero findings" run.
func decodeRecord(line string) (serviceRecord, bool) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil || obj == nil {
		return serviceRecord{}, false
	}

	rec := serviceRecord{
		Host:     stringField(obj, "ip", "host", "target", "hostname"),
		Port:     numberField(obj, "port"),
		Protocol: stringField(obj, "protocol", "proto", "scheme", "type"),
		Status:   numberField(obj, "status"),
		Title:    stringField(obj, "title"),
		Product:  stringField(obj, "server", "product", "midware", "banner"),
		OS:       stringField(obj, "os", "osversion"),
		URI:      stringField(obj, "uri", "url", "path"),
	}
	rec.Frameworks = frameworksField(obj)
	return rec, true
}

func stringField(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			if s := scalarString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func numberField(obj map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := obj[k]; ok {
			if s := scalarString(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		// encoding/json decodes every number as float64; ports and statuses are
		// integers, so format without a trailing ".0".
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}

// frameworksField accepts the two shapes gogo has used: an array of objects
// ({"name","version"}) and an array of plain strings.
func frameworksField(obj map[string]any) []framework {
	raw, ok := obj["frameworks"]
	if !ok {
		raw, ok = obj["framework"]
	}
	if !ok {
		return nil
	}

	switch t := raw.(type) {
	case []any:
		out := make([]framework, 0, len(t))
		for _, item := range t {
			switch v := item.(type) {
			case map[string]any:
				out = append(out, framework{
					Name:    stringField(v, "name", "product"),
					Version: stringField(v, "version", "ver"),
				})
			case string:
				if s := strings.TrimSpace(v); s != "" {
					out = append(out, framework{Name: s})
				}
			}
		}
		return out
	case string:
		if s := strings.TrimSpace(t); s != "" {
			return []framework{{Name: s}}
		}
	}
	return nil
}

// renderSummary builds the model-facing table. Rows are capped so a /16 sweep
// cannot blow the context window; the full result stays in the archive file.
func renderSummary(sum *scanSummary, target, ports, archive string, maxRows int) string {
	if maxRows <= 0 {
		maxRows = 80
	}

	var b strings.Builder
	fmt.Fprintf(&b, "port_scan target=%s ports=%s | hosts=%d open_ports=%d frameworks=%d",
		target, ports, len(sum.Hosts), sum.Ports, sum.Frameworks)
	if sum.Malformed > 0 {
		fmt.Fprintf(&b, " malformed_lines=%d", sum.Malformed)
	}
	b.WriteString("\n")

	if len(sum.Records) == 0 {
		b.WriteString("No open ports or services were identified for this target/port set.\n")
		if archive != "" {
			fmt.Fprintf(&b, "Raw JSONL: %s\n", archive)
		}
		return b.String()
	}

	b.WriteString("HOST | PORT | PROTO | STATUS | SERVICE/TITLE | FRAMEWORKS\n")
	limit := len(sum.Records)
	if limit > maxRows {
		limit = maxRows
	}
	for _, rec := range sum.Records[:limit] {
		fmt.Fprintf(&b, "%s | %s | %s | %s | %s | %s\n",
			cell(rec.Host, 40),
			cell(rec.Port, 8),
			cell(rec.Protocol, 6),
			cell(rec.Status, 6),
			cell(serviceLabel(rec), 48),
			cell(frameworkLabel(rec.Frameworks), 40),
		)
	}
	if len(sum.Records) > limit {
		fmt.Fprintf(&b, "... [%d more host:port rows omitted — see the archive file]\n", len(sum.Records)-limit)
	}
	if archive != "" {
		fmt.Fprintf(&b, "Raw JSONL (%d lines): %s\n", sum.Lines, archive)
	}
	return b.String()
}

func serviceLabel(rec serviceRecord) string {
	parts := make([]string, 0, 3)
	for _, p := range []string{rec.Product, rec.Title, rec.URI} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if rec.OS != "" {
		parts = append(parts, "os="+rec.OS)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " / ")
}

func frameworkLabel(fws []framework) string {
	if len(fws) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(fws))
	for _, fw := range fws {
		switch {
		case fw.Name != "" && fw.Version != "":
			parts = append(parts, fw.Name+" "+fw.Version)
		case fw.Name != "":
			parts = append(parts, fw.Name)
		case fw.Version != "":
			parts = append(parts, fw.Version)
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

// cell truncates a column to n runes so one long banner cannot break the table.
func cell(s string, n int) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " ")
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 1 {
		return string(runes[:n])
	}
	return string(runes[:n-1]) + "~"
}

// hostLess compares hosts numerically when both are IPv4, lexically otherwise.
func hostLess(a, b string) bool {
	ai, aok := ipv4ToUint(a)
	bi, bok := ipv4ToUint(b)
	if aok && bok {
		return ai < bi
	}
	return a < b
}

func portLess(a, b string) bool {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	if aerr == nil && berr == nil {
		return ai < bi
	}
	return a < b
}

func ipv4ToUint(s string) (uint32, bool) {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return 0, false
	}
	var out uint32
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return 0, false
		}
		out = out<<8 | uint32(n)
	}
	return out, true
}
