package engine

// HTTP-free Eino ChatModel adapter. It forwards to the existing internal/llm
// client so retries, KeyRotator, provider protocols and token accounting stay
// in one place. This package is not wired into the scan loop yet.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/xalgord/xalgorix/v4/internal/llm"
)

// ChatClient is the narrow seam the adapter needs. *llm.Client satisfies it.
type ChatClient interface {
	ChatWithUsageToolCalls(messages []llm.Message, tools []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error)
}

var _ ChatClient = (*llm.Client)(nil)
var _ model.ToolCallingChatModel = (*ChatModel)(nil)

// ChatModel adapts one ChatClient to Eino's tool-calling model interface.
// WithTools returns a copy, so a base model is safe to share across requests.
type ChatModel struct {
	client ChatClient
	tools  []llm.ToolDefinition
}

// NewChatModel builds an unbound adapter.
func NewChatModel(client ChatClient) *ChatModel {
	return &ChatModel{client: client}
}

// WithTools implements model.ToolCallingChatModel without mutating the base.
func (m *ChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	defs, err := toolDefinitionsFromInfo(tools)
	if err != nil {
		return nil, err
	}
	return &ChatModel{client: m.client, tools: defs}, nil
}

// Generate blocks on the legacy non-streaming client and maps the response
// back to Eino schema messages.
func (m *ChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if m == nil || m.client == nil {
		return nil, errors.New("engine: chat client is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	messages, err := toLLMMessages(input)
	if err != nil {
		return nil, err
	}
	content, calls, usage, err := m.client.ChatWithUsageToolCalls(messages, m.tools)
	if err != nil {
		return nil, err
	}
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: content,
	}
	for i, call := range calls {
		args := strings.TrimSpace(call.RawArguments)
		if args == "" {
			encoded, marshalErr := json.Marshal(call.Args)
			if marshalErr != nil {
				return nil, fmt.Errorf("engine: encode tool call %d args: %w", i, marshalErr)
			}
			args = string(encoded)
			if len(call.Args) == 0 {
				args = "{}"
			}
		}
		callType := "function"
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
			ID:   call.ID,
			Type: callType,
			Function: schema.FunctionCall{
				Name:      call.Name,
				Arguments: args,
			},
		})
	}
	finishReason := "stop"
	if len(msg.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}
	msg.ResponseMeta = &schema.ResponseMeta{FinishReason: finishReason}
	if usage != nil {
		msg.ResponseMeta.Usage = &schema.TokenUsage{
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			TotalTokens:      usage.TotalTokens,
			PromptTokenDetails: schema.PromptTokenDetails{
				CachedTokens: usage.CachedTokens,
			},
		}
	}
	return msg, nil
}

// Stream is deliberately disabled in the skeleton. The legacy production loop
// uses the non-streaming client; streaming needs a separate parity review.
func (m *ChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("engine: streaming is not enabled in the Eino skeleton")
}

func toLLMMessages(input []*schema.Message) ([]llm.Message, error) {
	out := make([]llm.Message, 0, len(input))
	for i, msg := range input {
		if msg == nil {
			return nil, fmt.Errorf("engine: message %d is nil", i)
		}
		role := string(msg.Role)
		if role == "" {
			return nil, fmt.Errorf("engine: message %d has no role", i)
		}
		converted := llm.Message{
			Role:       role,
			Content:    msg.Content,
			ToolCallID: msg.ToolCallID,
		}
		for _, call := range msg.ToolCalls {
			converted.ToolCalls = append(converted.ToolCalls, llm.MessageToolCall{
				ID:   call.ID,
				Type: call.Type,
				Function: llm.MessageToolFunction{
					Name:      call.Function.Name,
					Arguments: call.Function.Arguments,
				},
			})
		}
		out = append(out, converted)
	}
	return out, nil
}

// toolDefinitionsFromInfo converts the Eino-side bindings into the provider
// neutral definitions internal/llm already sends on the native channel.
func toolDefinitionsFromInfo(infos []*schema.ToolInfo) ([]llm.ToolDefinition, error) {
	if len(infos) == 0 {
		return nil, nil
	}
	out := make([]llm.ToolDefinition, 0, len(infos))
	for _, info := range infos {
		if info == nil {
			continue
		}
		def := llm.ToolDefinition{Name: info.Name, Description: info.Desc}
		if info.ParamsOneOf != nil {
			js, err := info.ParamsOneOf.ToJSONSchema()
			if err != nil {
				return nil, fmt.Errorf("engine: convert tool %q schema: %w", info.Name, err)
			}
			payload, err := json.Marshal(js)
			if err != nil {
				return nil, fmt.Errorf("engine: encode tool %q schema: %w", info.Name, err)
			}
			var doc struct {
				Properties map[string]struct {
					Description string `json:"description"`
				} `json:"properties"`
				Required []string `json:"required"`
			}
			if err := json.Unmarshal(payload, &doc); err != nil {
				return nil, fmt.Errorf("engine: decode tool %q schema: %w", info.Name, err)
			}
			required := make(map[string]struct{}, len(doc.Required))
			for _, name := range doc.Required {
				required[name] = struct{}{}
			}
			for name, prop := range doc.Properties {
				_, isRequired := required[name]
				def.Parameters = append(def.Parameters, llm.ToolParameter{
					Name:        name,
					Description: prop.Description,
					Required:    isRequired,
				})
			}
			// JSON object iteration is unordered; keep the request schema
			// deterministic across calls.
			sort.Slice(def.Parameters, func(i, j int) bool {
				return def.Parameters[i].Name < def.Parameters[j].Name
			})
		}
		out = append(out, def)
	}
	return out, nil
}
