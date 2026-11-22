package engine

// Registry-backed Eino tools. Every invocation goes through
// tools.Registry.Execute, so the existing six-ring security chain, circuit
// breaker and (once R2 is complete) approval gate stay the single execution
// path. This adapter never calls a tool's Execute closure directly.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	einoTool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"

	"github.com/xalgord/xalgorix/v4/internal/tools"
)

var toolTimeoutGrace = 30 * time.Second

var _ einoTool.InvokableTool = (*RegistryTool)(nil)

// RegistryTool exposes one registered Xalgorix tool to Eino.
type RegistryTool struct {
	registry    *tools.Registry
	name        string
	hardTimeout func(string) time.Duration
	onTimeout   func(string)
}

// RegistryTools returns a deterministic snapshot of every registered tool.
// The returned adapters read metadata from the registry lazily.
func RegistryTools(reg *tools.Registry) ([]einoTool.InvokableTool, error) {
	return registryTools(reg, nil, nil)
}

func registryTools(reg *tools.Registry, hardTimeout func(string) time.Duration, onTimeout func(string)) ([]einoTool.InvokableTool, error) {
	if reg == nil {
		return nil, errors.New("engine: registry is nil")
	}
	names := reg.List()
	sort.Strings(names)
	out := make([]einoTool.InvokableTool, 0, len(names))
	for _, name := range names {
		out = append(out, &RegistryTool{registry: reg, name: name, hardTimeout: hardTimeout, onTimeout: onTimeout})
	}
	return out, nil
}

// Info implements tool.BaseTool.
func (t *RegistryTool) Info(context.Context) (*schema.ToolInfo, error) {
	tool, ok := t.registry.Get(t.name)
	if !ok {
		return nil, fmt.Errorf("engine: tool %q is no longer registered", t.name)
	}
	info := &schema.ToolInfo{Name: tool.Name, Desc: tool.Description}
	if len(tool.Parameters) > 0 {
		params := make(map[string]*schema.ParameterInfo, len(tool.Parameters))
		for _, param := range tool.Parameters {
			params[param.Name] = &schema.ParameterInfo{
				Type:     schema.String,
				Desc:     param.Description,
				Required: param.Required,
			}
		}
		info.ParamsOneOf = schema.NewParamsOneOfByParams(params)
	}
	return info, nil
}

// InvokableRun decodes Eino's JSON argument string into the registry's
// string-valued argument map and executes through Registry.ExecuteContext.
// A configured hard ceiling force-returns even for tools that ignore ctx.
func (t *RegistryTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...einoTool.Option) (string, error) {
	args, err := decodeRegistryArgs(argumentsInJSON)
	if err != nil {
		return "", fmt.Errorf("engine: tool %q arguments: %w", t.name, err)
	}
	if t.hardTimeout == nil {
		result, runErr := t.registry.ExecuteContext(ctx, t.name, args)
		recordToolExecutionResult(ctx, result)
		return registryToolOutput(t.name, result, runErr)
	}
	limit := t.hardTimeout(t.name)
	if limit <= 0 {
		result, runErr := t.registry.ExecuteContext(ctx, t.name, args)
		recordToolExecutionResult(ctx, result)
		return registryToolOutput(t.name, result, runErr)
	}
	toolCtx, cancel := context.WithTimeout(ctx, limit+toolTimeoutGrace)
	defer cancel()

	type outcome struct {
		result tools.Result
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- outcome{err: fmt.Errorf("tool panicked: %v\n%s", recovered, debug.Stack())}
			}
		}()
		result, runErr := t.registry.ExecuteContext(toolCtx, t.name, args)
		done <- outcome{result: result, err: runErr}
	}()

	select {
	case out := <-done:
		recordToolExecutionResult(ctx, out.result)
		return registryToolOutput(t.name, out.result, out.err)
	case <-toolCtx.Done():
		if errors.Is(toolCtx.Err(), context.DeadlineExceeded) {
			if t.onTimeout != nil {
				t.onTimeout(t.name)
			}
			return "", fmt.Errorf("engine: tool %q exceeded hard timeout %s", t.name, limit)
		}
		return "", fmt.Errorf("engine: tool %q canceled: %w", t.name, toolCtx.Err())
	}
}

func registryToolOutput(name string, result tools.Result, err error) (string, error) {
	if err != nil {
		return "", fmt.Errorf("engine: tool %q: %w", name, err)
	}
	if !result.Success || strings.TrimSpace(result.Error) != "" {
		reason := strings.TrimSpace(result.Error)
		if reason == "" {
			reason = "execution failed without an error message"
		}
		return "", fmt.Errorf("engine: tool %q failed: %s", name, reason)
	}
	return result.Output, nil
}

func decodeRegistryArgs(argumentsInJSON string) (map[string]string, error) {
	raw := strings.TrimSpace(argumentsInJSON)
	if raw == "" || raw == "null" {
		return map[string]string{}, nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("expected a JSON object: %w", err)
	}
	out := make(map[string]string, len(values))
	for name, value := range values {
		decoded, err := registryArgumentString(value)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", name, err)
		}
		if decoded == "" && string(value) == "null" {
			continue
		}
		out[name] = decoded
	}
	return out, nil
}

// registryArgumentString preserves the registry's string contract: strings
// stay verbatim, scalars use their canonical text, and structured values keep
// their JSON form for tools that parse nested input themselves.
func registryArgumentString(raw json.RawMessage) (string, error) {
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return "", err
	}
	switch v := value.(type) {
	case nil:
		return "", nil
	case json.Number:
		return v.String(), nil
	case bool:
		return strconv.FormatBool(v), nil
	default:
		encoded, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}
