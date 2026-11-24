package engine

// Tool-surface compatibility layer. Models habitually emit short tool names
// ("bash", "http", "browser") that differ from the registered canonical name,
// and they occasionally sent scalars where the registry contract is a string.
// Both are corrected here, before the tool node dispatches, without touching
// the registry, the guard order, or the approval protocol.
//
// Aliases are only registered for canonical tools that actually exist in the
// supplied registry snapshot: Eino rejects an alias that collides with any
// registered tool name, and an alias for a tool that is not in this lane must
// still reach UnknownToolsHandler as a correctable result.

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/cloudwego/eino/compose"
)

// defaultToolAliases is the built-in stable alias table: canonical tool name →
// model-emitted aliases. It is deliberately tiny; add an alias only when the
// short name is unambiguous across the whole tool surface.
func defaultToolAliases() map[string][]string {
	return map[string][]string{
		"terminal_execute": {"terminal", "bash"},
		"http_request":     {"http"},
		"browser_action":   {"browser"},
		"python_action":    {"python"},
	}
}

// buildToolAliases turns an alias table into the Eino ToolsNodeConfig form and
// a reverse alias→canonical map for the guard/observer middleware (Eino passes
// the ALIAS name to tool middlewares, so the guard would otherwise see "bash"
// and skip every name-scoped policy). Aliases are dropped when:
//   - their canonical tool is not registered in this lane,
//   - the alias is itself a registered tool name (Eino would reject the table),
//   - the alias was already claimed by another canonical tool,
//   - the alias is empty or equals its canonical name.
//
// override == nil keeps the built-in table; a non-nil (even empty) map replaces
// it, so a caller can disable aliasing entirely.
func buildToolAliases(available []string, override map[string][]string) (map[string]compose.ToolAliasConfig, map[string]string) {
	table := defaultToolAliases()
	if override != nil {
		table = override
	}
	known := make(map[string]struct{}, len(available))
	for _, name := range available {
		known[name] = struct{}{}
	}

	canonicals := make([]string, 0, len(table))
	for canonical := range table {
		canonicals = append(canonicals, canonical)
	}
	sort.Strings(canonicals)

	var configs map[string]compose.ToolAliasConfig
	reverse := make(map[string]string)
	for _, canonical := range canonicals {
		if _, ok := known[canonical]; !ok {
			continue
		}
		var accepted []string
		seen := make(map[string]struct{})
		for _, alias := range table[canonical] {
			alias = strings.TrimSpace(alias)
			if alias == "" || alias == canonical {
				continue
			}
			if _, isTool := known[alias]; isTool {
				continue
			}
			if _, claimed := reverse[alias]; claimed {
				continue
			}
			if _, dup := seen[alias]; dup {
				continue
			}
			seen[alias] = struct{}{}
			accepted = append(accepted, alias)
			reverse[alias] = canonical
		}
		if len(accepted) == 0 {
			continue
		}
		if configs == nil {
			configs = make(map[string]compose.ToolAliasConfig)
		}
		configs[canonical] = compose.ToolAliasConfig{NameAliases: accepted}
	}
	if len(reverse) == 0 {
		reverse = nil
	}
	return configs, reverse
}

// canonicalToolName maps a model-emitted alias to its registered tool name.
// Unknown names pass through unchanged so UnknownToolsHandler keeps seeing the
// name the model actually used.
func canonicalToolName(name string, aliasToCanonical map[string]string) string {
	if canonical, ok := aliasToCanonical[name]; ok {
		return canonical
	}
	return name
}

// whitespaceSensitiveArgs are argument names whose value is byte payload or
// source text: trimming them would change what the tool does (an indented
// python line becomes a SyntaxError, an old_str with trailing spaces no longer
// matches the file, a typed browser value loses leading spaces). They are still
// passed through the registry's string contract untouched.
var whitespaceSensitiveArgs = map[string]struct{}{
	"old_str":   {}, // str_replace_editor: exact match text
	"new_str":   {}, // str_replace_editor: exact replacement
	"file_text": {}, // str_replace_editor: full file body
	"code":      {}, // python_action / browser_action execute_js
	"body":      {}, // http_request / send_request payload
	"text":      {}, // browser_action typed text, select value, cookie value
	"value":     {}, // add_note: operator-facing content
}

// normalizeToolArguments is the ToolsNodeConfig.ToolArgumentsHandler. It trims
// string scalars and renders JSON number/bool scalars as strings, which is the
// registry's argument contract (map[string]string). It never renames or drops
// a field, never rewrites structured values or whitespace-sensitive payloads,
// and returns the input byte-for-byte when nothing needed normalizing.
// Malformed JSON is passed through so the tool adapter's existing error path
// reports it, rather than failing the whole tool node.
func normalizeToolArguments(_ context.Context, _ string, arguments string) (string, error) {
	raw := strings.TrimSpace(arguments)
	if raw == "" || raw == "null" {
		return arguments, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return arguments, nil
	}
	var normalized map[string]json.RawMessage
	changed := false
	for name, value := range values {
		if _, sensitive := whitespaceSensitiveArgs[name]; sensitive {
			continue
		}
		next, valueChanged := normalizeScalarArgument(value)
		if !valueChanged {
			continue
		}
		if !changed {
			normalized = make(map[string]json.RawMessage, len(values))
			for k, v := range values {
				normalized[k] = v
			}
			changed = true
		}
		normalized[name] = next
	}
	if !changed {
		return arguments, nil
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return arguments, nil
	}
	return string(encoded), nil
}

// normalizeScalarArgument normalizes one top-level JSON value. Only string,
// number and bool scalars are eligible; null, objects, arrays and any other
// JSON form are returned unchanged.
func normalizeScalarArgument(raw json.RawMessage) (json.RawMessage, bool) {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		trimmed := strings.TrimSpace(str)
		if trimmed == str {
			return raw, false
		}
		encoded, err := json.Marshal(trimmed)
		if err != nil {
			return raw, false
		}
		return encoded, true
	}

	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return raw, false
	}
	switch v := value.(type) {
	case json.Number:
		return marshalScalarString(v.String())
	case bool:
		if v {
			return marshalScalarString("true")
		}
		return marshalScalarString("false")
	default:
		return raw, false
	}
}

func marshalScalarString(value string) (json.RawMessage, bool) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	return encoded, true
}
