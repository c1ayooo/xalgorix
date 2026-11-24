package engine

// HTTP-free Eino ChatModel adapter. It is the only model seam of the scan
// runtime: provider retries, protocol fallback, recovery nudges and token
// accounting all pass through here before the ADK sees a response.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/llm"
)

// ChatClient is the narrow seam the adapter needs. *llm.Client satisfies it.
type ChatClient interface {
	ChatWithUsageToolCalls(messages []llm.Message, tools []llm.ToolDefinition) (string, []llm.ToolCall, *llm.TokenUsage, error)
}

var _ ChatClient = (*llm.Client)(nil)

// ChatMetaClient is the optional capability for capturing the model's raw
// reasoning text alongside the completion (*llm.Client satisfies it). Probed
// at call time, so test fakes implementing only ChatClient keep working.
type ChatMetaClient interface {
	ChatWithUsageToolCallsMeta(messages []llm.Message, tools []llm.ToolDefinition) (content string, reasoning string, calls []llm.ToolCall, usage *llm.TokenUsage, err error)
}

var _ model.ToolCallingChatModel = (*ChatModel)(nil)

// ChatModel adapts one ChatClient to Eino's tool-calling model interface.
// WithTools returns a copy, so a base model is safe to share across requests.
type ChatModel struct {
	client ChatClient
	tools  []llm.ToolDefinition
	// onReasoning, when set, receives the accepted response's raw reasoning
	// text (reasoning models only; empty for plain completions).
	onReasoning func(string)
	stopCheck   func() bool
	recovery    ModelRecovery
	compactor   func([]*schema.Message) []*schema.Message
	// nativeTools is true only when the resolved endpoint understands the
	// OpenAI `tools`/`tool_calls` wire format. Otherwise the adapter keeps the
	// provider-neutral XML dialect as a protocol fallback, still returning
	// schema.ToolCalls to the ADK. The ADK never sees an unsupported channel.
	nativeTools bool
}

// NewChatModel builds an unbound adapter.
func NewChatModel(client ChatClient) *ChatModel {
	return &ChatModel{client: client, nativeTools: true}
}

// WithModelRecovery installs the scan-owned retry/recovery policy.
func (m *ChatModel) WithModelRecovery(recovery ModelRecovery) *ChatModel {
	cp := *m
	cp.recovery = recovery
	return &cp
}

// WithCompactor installs the scan-owned bounded-context policy.
func (m *ChatModel) WithCompactor(fn func([]*schema.Message) []*schema.Message) *ChatModel {
	cp := *m
	cp.compactor = fn
	return &cp
}

// WithNativeTools selects the wire protocol. false enables the XML fallback
// adapter for Anthropic/Gemini/Responses endpoints.
func (m *ChatModel) WithNativeTools(native bool) *ChatModel {
	cp := *m
	cp.nativeTools = native
	return &cp
}

// WithStopCheck installs a pre-request stop probe. The runtime wires it to the
// scan's completion flag so no further model call is issued after finish.
// WithOnReasoning installs a fire-and-forget sink for the model's raw
// reasoning text (one call per accepted provider response, before the turn's
// message is surfaced). Nil-safe: without it reasoning is simply dropped.
// The callback must not gate or retry — it is observability only.
func (m *ChatModel) WithOnReasoning(fn func(string)) *ChatModel {
	m.onReasoning = fn
	return m
}

func (m *ChatModel) WithStopCheck(fn func() bool) *ChatModel {
	cp := *m
	cp.stopCheck = fn
	return &cp
}

// WithTools implements model.ToolCallingChatModel without mutating the base.
func (m *ChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	defs, err := toolDefinitionsFromInfo(tools)
	if err != nil {
		return nil, err
	}
	cp := *m
	cp.tools = defs
	return &cp, nil
}

// Generate issues provider requests until the recovery policy accepts a
// response, requests a retry, or aborts. Recovery callbacks are deliberately
// synchronous: the scan's verifier timing invariant depends on one model turn
// being active at a time inside the serialized tool node.
func (m *ChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m == nil || m.client == nil {
		return nil, errors.New("engine: chat client is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.stopCheck != nil && m.stopCheck() {
		return nil, &ErrTurnStopped{Reason: "scan complete"}
	}

	tools := m.tools
	if len(opts) > 0 {
		common := model.GetCommonOptions(&model.Options{}, opts...)
		if len(common.Tools) > 0 {
			bound, bindErr := toolDefinitionsFromInfo(common.Tools)
			if bindErr != nil {
				return nil, bindErr
			}
			tools = bound
		}
	}

	working := cloneSchemaMessages(input)
	if m.compactor != nil {
		working = m.compactor(working)
	}
	// A provider request is retried in place; this is not a new ADK turn, so
	// budgets and ledger work are not consumed by transport-level failures.
	for attempt := 0; ; attempt++ {
		if attempt > 256 {
			return nil, errors.New("engine: model recovery did not converge")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if m.stopCheck != nil && m.stopCheck() {
			return nil, &ErrTurnStopped{Reason: "scan complete"}
		}

		if m.recovery != nil {
			_ = m.recovery.BeforeModel(ctx, attempt)
		}

		messages, err := m.providerMessages(working)
		if err != nil {
			return nil, err
		}
		wireTools := tools
		if !m.nativeTools {
			wireTools = nil
		}
		var (
			content   string
			calls     []llm.ToolCall
			usage     *llm.TokenUsage
			reasoning string
		)
		if meta, ok := m.client.(ChatMetaClient); ok {
			content, reasoning, calls, usage, err = meta.ChatWithUsageToolCallsMeta(messages, wireTools)
		} else {
			content, calls, usage, err = m.client.ChatWithUsageToolCalls(messages, wireTools)
		}
		if err != nil {
			if m.recovery == nil {
				return nil, err
			}
			decision := m.recovery.OnModelError(ctx, err)
			if decision.Fatal != nil {
				return nil, decision.Fatal
			}
			if !decision.Retry {
				return nil, err
			}
			switch {
			case decision.ResetContext:
				working = resetSchemaMessages(working, decision.Nudge)
			case decision.CompactContext:
				working = compactForOverflow(working)
			default:
				working = appendSchemaNudge(working, decision.Nudge)
			}
			continue
		}

		// Native calls take precedence. The XML parser is a compatibility
		// fallback for providers that only carry text, and for models that emit
		// the XML dialect despite the tools array.
		if len(calls) == 0 {
			if parsed := llm.ParseToolCalls(content); len(parsed) > 0 {
				calls = llm.EnsureToolCallIDs(parsed, fmt.Sprintf("xmlcall_%d", attempt))
				content = strings.TrimSpace(llm.CleanContent(content))
			}
		}
		if m.recovery != nil {
			decision := m.recovery.OnModelResponse(ctx, messages, content, calls, usage)
			if decision.Fatal != nil {
				return nil, decision.Fatal
			}
			if decision.Retry || decision.ResetContext {
				if decision.ResetContext {
					working = resetSchemaMessages(working, decision.Nudge)
				} else {
					working = appendSchemaNudge(working, decision.Nudge)
				}
				continue
			}
		}
		// Reasoning surfaces only after the recovery policy accepted the
		// response — failed attempts never emit a thinking row.
		if reasoning != "" && m.onReasoning != nil {
			m.onReasoning(reasoning)
		}
		return responseMessage(content, calls, usage), nil
	}
}

func (m *ChatModel) providerMessages(input []*schema.Message) ([]llm.Message, error) {
	messages, err := toLLMMessages(input)
	if err != nil {
		return nil, err
	}
	if m.nativeTools {
		return llm.NormalizeNativeToolHistory(messages), nil
	}
	return toXMLProviderMessages(messages), nil
}

// ChatStreamClient is the optional streaming capability the adapter needs.
// *llm.Client satisfies it. Probed at call time like ChatMetaClient, so test
// fakes implementing only ChatClient keep working (and simply cannot stream).
type ChatStreamClient interface {
	ChatStreamWithTools(messages []llm.Message, tools []llm.ToolDefinition) <-chan llm.StreamChunk
}

var _ ChatStreamClient = (*llm.Client)(nil)

// Stream implements model.ToolCallingChatModel by consuming the client's SSE
// stream. Text is surfaced incrementally; tool-call fragments are accumulated
// by index and delivered complete on the final frame, so the concatenated
// stream matches the message Generate would have returned.
//
// The scan's recovery policy applies on this path too: BeforeModel runs per
// attempt, and a provider error or an empty/reasoning-only response is handed
// to OnModelError / OnModelResponse exactly as Generate does. The one
// streaming-specific rule is that a retry is only possible while nothing has
// been surfaced to the consumer: a replay after a delta reached the reader
// would duplicate text, so such a response is accepted (or its provider error
// surfaced) instead of retried.
//
// Cancelling ctx closes the reader with ctx.Err() and drains the producer
// channel so the client goroutine cannot block on an unread send.
func (m *ChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m == nil || m.client == nil {
		return nil, errors.New("engine: chat client is nil")
	}
	streamer, ok := m.client.(ChatStreamClient)
	if !ok {
		// Streaming is an optimization, not a capability requirement: a client
		// without SSE (test fakes, a gateway that only speaks non-streaming)
		// degrades to one complete message, which the ADK consumes identically.
		msg, genErr := m.Generate(ctx, input, opts...)
		if genErr != nil {
			return nil, genErr
		}
		return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.stopCheck != nil && m.stopCheck() {
		return nil, &ErrTurnStopped{Reason: "scan complete"}
	}

	tools := m.tools
	if len(opts) > 0 {
		common := model.GetCommonOptions(&model.Options{}, opts...)
		if len(common.Tools) > 0 {
			bound, bindErr := toolDefinitionsFromInfo(common.Tools)
			if bindErr != nil {
				return nil, bindErr
			}
			tools = bound
		}
	}
	working := cloneSchemaMessages(input)
	if m.compactor != nil {
		working = m.compactor(working)
	}
	wireTools := tools
	if !m.nativeTools {
		wireTools = nil
	}

	reader, writer := schema.Pipe[*schema.Message](16)
	go func() {
		defer writer.Close()
		m.streamWithRecovery(ctx, writer, working, wireTools, streamer, !m.nativeTools)
	}()
	return reader, nil
}

// streamWithRecovery runs the streaming provider request through the same
// policy loop Generate uses. Attempts that surfaced nothing to the consumer
// (empty responses, errors before the first delta) retry with the recovery
// decision applied to the working context; once a delta is out, a retry is
// refused because the reader cannot unsee it.
func (m *ChatModel) streamWithRecovery(
	ctx context.Context,
	writer *schema.StreamWriter[*schema.Message],
	working []*schema.Message,
	wireTools []llm.ToolDefinition,
	streamer ChatStreamClient,
	bufferContent bool,
) {
	for attempt := 0; ; attempt++ {
		if attempt > 256 {
			writer.Send(nil, errors.New("engine: model recovery did not converge"))
			return
		}
		if err := ctx.Err(); err != nil {
			writer.Send(nil, err)
			return
		}
		if m.stopCheck != nil && m.stopCheck() {
			writer.Send(nil, &ErrTurnStopped{Reason: "scan complete"})
			return
		}
		if m.recovery != nil {
			_ = m.recovery.BeforeModel(ctx, attempt)
		}
		messages, err := m.providerMessages(working)
		if err != nil {
			writer.Send(nil, err)
			return
		}
		chunks := streamer.ChatStreamWithTools(messages, wireTools)
		res := m.forwardStreamAttempt(ctx, chunks, writer, bufferContent)
		if res.consumerClosed {
			return
		}
		if res.err != nil {
			if m.recovery == nil {
				writer.Send(nil, res.err)
				return
			}
			decision := m.recovery.OnModelError(ctx, res.err)
			if decision.Fatal != nil {
				writer.Send(nil, decision.Fatal)
				return
			}
			if !decision.Retry || res.emitted {
				writer.Send(nil, res.err)
				return
			}
			switch {
			case decision.ResetContext:
				working = resetSchemaMessages(working, decision.Nudge)
			case decision.CompactContext:
				working = compactForOverflow(working)
			default:
				working = appendSchemaNudge(working, decision.Nudge)
			}
			continue
		}
		if m.recovery != nil {
			decision := m.recovery.OnModelResponse(ctx, messages, res.content, res.calls, res.usage)
			if decision.Fatal != nil {
				writer.Send(nil, decision.Fatal)
				return
			}
			// An empty or reasoning-only turn surfaced nothing, so the same
			// retry Generate performs is safe here.
			if (decision.Retry || decision.ResetContext) && !res.emitted {
				if decision.ResetContext {
					working = resetSchemaMessages(working, decision.Nudge)
				} else {
					working = appendSchemaNudge(working, decision.Nudge)
				}
				continue
			}
		}
		// Reasoning surfaces only after the recovery policy accepted the
		// response, mirroring Generate: failed or retried attempts never emit a
		// thinking row.
		if res.reasoning != "" && m.onReasoning != nil {
			m.onReasoning(res.reasoning)
		}
		if !writer.Send(responseMessage(res.finalContent, res.calls, res.usage), nil) {
			go drainStreamChunks(chunks)
		}
		return
	}
}

// streamAttempt is one provider stream reduced to what the recovery policy and
// the final frame need. content is the text Generate would have returned;
// finalContent is the text for the final frame (empty when deltas already
// carried it, so the reader never receives the same text twice).
type streamAttempt struct {
	content        string
	finalContent   string
	reasoning      string
	calls          []llm.ToolCall
	usage          *llm.TokenUsage
	emitted        bool
	consumerClosed bool
	err            error
}

// forwardStreamAttempt relays one client stream into the Eino pipe and returns
// the reduced response. It sends deltas but never the final frame: the caller
// sends that only after the recovery policy accepts the turn.
//
// bufferContent is true on the XML fallback: there tool calls live inside the
// text, so the deltas are held until the end and the final frame carries the
// cleaned content plus parsed calls (mirroring Generate). On the native
// channel text streams immediately and the final frame only completes the
// tool calls and usage.
func (m *ChatModel) forwardStreamAttempt(
	ctx context.Context,
	chunks <-chan llm.StreamChunk,
	writer *schema.StreamWriter[*schema.Message],
	bufferContent bool,
) streamAttempt {
	var (
		content   strings.Builder
		reasoning strings.Builder
		builders  = make(map[int]*streamCallBuilder)
		order     []int
		usage     *llm.TokenUsage
		emitted   bool
	)

	finalize := func() streamAttempt {
		raw := content.String()
		recoveryContent := raw
		finalContent := ""
		if bufferContent {
			finalContent = strings.TrimSpace(raw)
			recoveryContent = finalContent
		}
		calls := assembleStreamCalls(builders, order)
		// Native calls take precedence; the XML parser is the same
		// compatibility fallback Generate applies on the non-streaming path.
		if len(calls) == 0 {
			if parsed := llm.ParseToolCalls(raw); len(parsed) > 0 {
				calls = llm.EnsureToolCallIDs(parsed, "xmlstream")
				if bufferContent {
					recoveryContent = strings.TrimSpace(llm.CleanContent(raw))
					finalContent = recoveryContent
				}
			}
		}
		return streamAttempt{
			content:      recoveryContent,
			finalContent: finalContent,
			reasoning:    reasoning.String(),
			calls:        calls,
			usage:        usage,
			emitted:      emitted,
		}
	}

	for {
		select {
		case <-ctx.Done():
			writer.Send(nil, ctx.Err())
			go drainStreamChunks(chunks)
			return streamAttempt{err: ctx.Err(), emitted: emitted, consumerClosed: true}
		case chunk, ok := <-chunks:
			if !ok {
				return finalize()
			}
			if chunk.Err != nil {
				// The provider failed mid-stream. The caller decides whether the
				// policy may retry (only when nothing was surfaced). Drain so a
				// producer goroutine cannot block on an unread send.
				go drainStreamChunks(chunks)
				return streamAttempt{err: chunk.Err, emitted: emitted}
			}
			if chunk.Reasoning != "" {
				reasoning.WriteString(chunk.Reasoning)
			}
			if chunk.Content != "" {
				content.WriteString(chunk.Content)
				if !bufferContent {
					if writer.Send(&schema.Message{Role: schema.Assistant, Content: chunk.Content}, nil) {
						go drainStreamChunks(chunks)
						return streamAttempt{consumerClosed: true, emitted: true}
					}
					emitted = true
				}
			}
			for _, fragment := range chunk.ToolCalls {
				builder := builders[fragment.Index]
				if builder == nil {
					builder = &streamCallBuilder{}
					builders[fragment.Index] = builder
					order = append(order, fragment.Index)
				}
				builder.apply(fragment)
			}
			if chunk.Usage != nil {
				usage = chunk.Usage
			}
			if chunk.Done {
				return finalize()
			}
		}
	}
}

// streamCallBuilder accumulates the fragments of one tool call.
type streamCallBuilder struct {
	id   string
	name string
	args strings.Builder
}

func (b *streamCallBuilder) apply(fragment llm.StreamToolCall) {
	if fragment.ID != "" {
		b.id = fragment.ID
	}
	if fragment.Name != "" {
		b.name = fragment.Name
	}
	if fragment.Arguments != "" {
		b.args.WriteString(fragment.Arguments)
	}
}

// assembleStreamCalls turns the index-keyed fragments into complete calls in
// the order the provider opened them.
func assembleStreamCalls(builders map[int]*streamCallBuilder, order []int) []llm.ToolCall {
	if len(order) == 0 {
		return nil
	}
	sort.Ints(order)
	out := make([]llm.ToolCall, 0, len(order))
	for _, index := range order {
		builder := builders[index]
		if builder == nil || (builder.name == "" && builder.args.Len() == 0) {
			continue
		}
		out = append(out, llm.ToolCall{
			ID:           builder.id,
			Name:         builder.name,
			RawArguments: strings.TrimSpace(builder.args.String()),
		})
	}
	return out
}

// drainStreamChunks unblocks the producing goroutine after the consumer has
// stopped reading (ctx cancelled or reader closed).
func drainStreamChunks(chunks <-chan llm.StreamChunk) {
	for range chunks {
	}
}

func responseMessage(content string, calls []llm.ToolCall, usage *llm.TokenUsage) *schema.Message {
	msg := &schema.Message{Role: schema.Assistant, Content: content}
	for i, call := range calls {
		args := strings.TrimSpace(call.RawArguments)
		if args == "" {
			encoded, marshalErr := json.Marshal(call.Args)
			if marshalErr != nil {
				args = "{}"
			} else {
				args = string(encoded)
			}
			if len(call.Args) == 0 {
				args = "{}"
			}
		}
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
			ID:   call.ID,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      call.Name,
				Arguments: args,
			},
		})
		_ = i
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
	return msg
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

// toXMLProviderMessages renders native tool calls/results into the text
// dialect understood by Anthropic, Gemini and Responses endpoints. The ADK
// still receives native schema messages on both sides of this adapter.
func toXMLProviderMessages(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "assistant":
			content := strings.TrimSpace(msg.Content)
			for _, call := range msg.ToolCalls {
				content += "\n" + xmlToolCall(call)
			}
			out = append(out, llm.Message{Role: "assistant", Content: strings.TrimSpace(content)})
		case "tool":
			label := msg.ToolCallID
			if label == "" {
				label = "result"
			}
			out = append(out, llm.Message{
				Role:    "user",
				Content: "[TOOL RESULT " + label + "]\n" + msg.Content,
			})
		default:
			out = append(out, msg)
		}
	}
	return out
}

func xmlToolCall(call llm.MessageToolCall) string {
	args := call.Function.Arguments
	if strings.TrimSpace(args) == "" {
		args = "{}"
	}
	var values map[string]any
	_ = json.Unmarshal([]byte(args), &values)
	var sb strings.Builder
	sb.WriteString("<function=")
	sb.WriteString(call.Function.Name)
	sb.WriteString(">")
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		sb.WriteString("<parameter=")
		sb.WriteString(key)
		sb.WriteString(">")
		sb.WriteString(xmlValue(values[key]))
		sb.WriteString("</parameter>")
	}
	sb.WriteString("</function>")
	return sb.String()
}

func xmlValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(encoded)
}

func cloneSchemaMessages(input []*schema.Message) []*schema.Message {
	out := make([]*schema.Message, 0, len(input))
	for _, msg := range input {
		if msg == nil {
			continue
		}
		cp := *msg
		if msg.ToolCalls != nil {
			cp.ToolCalls = append([]schema.ToolCall(nil), msg.ToolCalls...)
		}
		out = append(out, &cp)
	}
	return out
}

func appendSchemaNudge(input []*schema.Message, nudge string) []*schema.Message {
	nudge = strings.TrimSpace(nudge)
	if nudge == "" {
		return input
	}
	if len(input) > 0 && input[len(input)-1] != nil &&
		input[len(input)-1].Role == schema.User && strings.TrimSpace(input[len(input)-1].Content) == nudge {
		return input
	}
	return append(input, schema.UserMessage(nudge))
}

func resetSchemaMessages(input []*schema.Message, nudge string) []*schema.Message {
	out := make([]*schema.Message, 0, 2)
	for _, msg := range input {
		if msg != nil && msg.Role == schema.System {
			cp := *msg
			out = append(out, &cp)
		}
	}
	if strings.TrimSpace(nudge) != "" {
		out = append(out, schema.UserMessage(nudge))
	}
	if len(out) == 0 {
		out = append(out, schema.UserMessage("Continue the security assessment from the saved scan state."))
	}
	return out
}

// compactForOverflow is the last-resort local shrink used when a provider
// rejects the prompt as too large. The scan-owned compactor normally runs
// before the request; this fallback guarantees progress if that heuristic
// underestimated the provider's tokenizer.
func compactForOverflow(input []*schema.Message) []*schema.Message {
	if len(input) <= 8 {
		return input
	}
	const keepRecent = 12
	const maxContent = 8000
	out := make([]*schema.Message, 0, keepRecent+2)
	for _, msg := range input[:1] {
		if msg != nil && msg.Role == schema.System {
			out = append(out, msg)
		}
	}
	out = append(out, schema.UserMessage("[CONTEXT COMPACTED] Older turns were removed after the provider rejected the prompt size. Continue from the current plan and saved notes; do not finish solely because of compaction."))
	start := len(input) - keepRecent
	if start < 1 {
		start = 1
	}
	for _, msg := range input[start:] {
		if msg == nil {
			continue
		}
		cp := *msg
		if len(cp.Content) > maxContent {
			cp.Content = cp.Content[:maxContent] + "\n\n[OUTPUT TRUNCATED AFTER CONTEXT OVERFLOW]"
		}
		out = append(out, &cp)
	}
	return out
}

// toolDefinitionsFromInfo converts the Eino-side bindings into the provider
// neutral definitions internal/llm sends on the native channel.
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
			js, err := info.ToJSONSchema()
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
			sort.Slice(def.Parameters, func(i, j int) bool {
				return def.Parameters[i].Name < def.Parameters[j].Name
			})
		}
		out = append(out, def)
	}
	return out, nil
}
