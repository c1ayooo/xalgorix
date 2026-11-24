package agent

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/eino/schema"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
	"github.com/c1ayooo/xalgorix/v4/internal/llm"
	"github.com/c1ayooo/xalgorix/v4/internal/scanctx"
)

// IG6a: the default Eino path stores tool results as role="tool" and native
// tool calls in Message.ToolCalls. Before this fix the digest's tool/finding
// sections were silently empty on that path.
func TestCompactMessagesNativeToolCallsAndToolRole(t *testing.T) {
	msgs := []llm.Message{
		{Role: "assistant", ToolCalls: []llm.MessageToolCall{
			{Function: llm.MessageToolFunction{Name: "terminal_execute"}},
		}},
		{Role: "tool", Content: "Tool 'terminal_execute' result:\nsql injection vulnerability found at /api/users?id=1"},
		{Role: "assistant", ToolCalls: []llm.MessageToolCall{
			{Function: llm.MessageToolFunction{Name: "http_request"}},
		}},
		{Role: "tool", Content: "Tool 'http_request' result:\n200 OK https://example.test/admin"},
	}

	digest := compactMessages(msgs)

	for _, want := range []string{
		"### Tool Calls (counts",
		"- terminal_execute (×1)",
		"- http_request (×1)",
		"Tool result messages in the dropped window: 2",
		"### URLs Mentioned",
		"https://example.test/admin",
		"### Candidate Finding Mentions (UNVERIFIED",
		"sql injection vulnerability found at /api/users?id=1",
	} {
		if !strings.Contains(digest, want) {
			t.Fatalf("digest missing %q:\n%s", want, digest)
		}
	}
}

// IG6a: the digest used to iterate maps, so the same input produced different
// bytes across runs (and broke prompt-prefix caching). Output must be stable.
func TestCompactMessagesDeterministic(t *testing.T) {
	msgs := []llm.Message{
		{Role: "assistant", ToolCalls: []llm.MessageToolCall{
			{Function: llm.MessageToolFunction{Name: "zeta"}},
			{Function: llm.MessageToolFunction{Name: "alpha"}},
			{Function: llm.MessageToolFunction{Name: "zeta"}},
		}},
		{Role: "tool", Content: "Tool 'zeta' result:\nxss candidate at https://b.test/x and https://a.test/y"},
		{Role: "tool", Content: "Tool 'alpha' result:\nError: timeout https://c.test/z"},
	}

	first := compactMessages(msgs)
	for i := 0; i < 20; i++ {
		if got := compactMessages(msgs); got != first {
			t.Fatalf("digest not deterministic:\n--- first ---\n%s\n--- run %d ---\n%s", first, i, got)
		}
	}

	// Stronger counts must sort before weak ones, ties broken by name.
	if zeta := strings.Index(first, "- zeta (×2)"); zeta < 0 {
		t.Fatalf("zeta count missing:\n%s", first)
	}
	if alpha := strings.Index(first, "- alpha (×1)"); alpha < 0 || alpha < strings.Index(first, "- zeta (×2)") {
		t.Fatalf("tool counts not sorted by count desc:\n%s", first)
	}
}

// CORE §5: the digest must not present guessed mentions as accepted findings.
func TestCompactMessagesAuthorityWording(t *testing.T) {
	digest := compactMessages([]llm.Message{
		{Role: "tool", Content: "Tool 'scan' result:\npossible xss injection reflected on /search"},
	})

	for _, forbidden := range []string{"### Key Findings", "### Tools Executed", "confirmed findings"} {
		if strings.Contains(digest, forbidden) {
			t.Fatalf("digest claims authority it does not have (%q):\n%s", forbidden, digest)
		}
	}
	if !strings.Contains(digest, "NOT authoritative") {
		t.Fatalf("digest missing authority scope note:\n%s", digest)
	}
	if !strings.Contains(digest, "the report gate decides acceptance") {
		t.Fatalf("digest missing report-gate qualifier:\n%s", digest)
	}
}

func TestCompactMessagesEmpty(t *testing.T) {
	digest := compactMessages(nil)
	if !strings.Contains(digest, "No structured data") {
		t.Fatalf("empty digest = %q", digest)
	}
}

func compactionFixture(n int) []*schema.Message {
	out := make([]*schema.Message, 0, n)
	out = append(out, schema.SystemMessage("system"))
	for i := 1; i < n; i++ {
		out = append(out, schema.UserMessage(fmt.Sprintf("message %d", i)))
	}
	return out
}

// IG6a: provider-reported prompt tokens are the primary trigger. The byte
// budget here is deliberately huge, so only the token signal can fire.
func TestCompactUsesPromptTokens(t *testing.T) {
	a := &Agent{
		cfg: &config.Config{
			ContextCompactTokens: -1, // auto: window-relative trigger, byte ceiling above the fixture
			LLMContextWindow:     1000,
			ContextCompactRatio:  0.75,
		},
		scanCtx: scanctx.Default(),
	}
	a.lastPromptTokens.Store(800) // >= 750

	input := compactionFixture(44) // > keepRecent+1, < maxMessagesBeforePrune
	out := (&einoObserver{agent: a}).CompactMessages(input)

	if len(out) == len(input) {
		t.Fatal("token trigger did not compact")
	}
	if len(out) != 42 {
		t.Fatalf("compacted length = %d, want 42 (system + digest + 40 recent)", len(out))
	}
	if !strings.Contains(out[1].Content, "CONTEXT PRUNED") {
		t.Fatalf("second message is not the digest: %q", out[1].Content)
	}
	if a.compactionCount != 1 {
		t.Fatalf("compactionCount = %d, want 1", a.compactionCount)
	}
}

func TestCompactBelowTokenThreshold(t *testing.T) {
	a := &Agent{
		cfg: &config.Config{
			ContextCompactTokens: -1,
			LLMContextWindow:     1000,
			ContextCompactRatio:  0.75,
		},
		scanCtx: scanctx.Default(),
	}
	a.lastPromptTokens.Store(700) // < 750

	input := compactionFixture(44)
	out := (&einoObserver{agent: a}).CompactMessages(input)
	if len(out) != len(input) {
		t.Fatalf("compacted below the token threshold: %d -> %d", len(input), len(out))
	}
	if a.compactionCount != 0 {
		t.Fatalf("compactionCount = %d, want 0", a.compactionCount)
	}
}

// An explicit XALGORIX_CONTEXT_COMPACT_TOKENS is an absolute budget: it must
// fire even when the model window is much larger.
func TestCompactUsesExplicitTokenBudget(t *testing.T) {
	a := &Agent{
		cfg: &config.Config{
			ContextCompactTokens: 1000,
			LLMContextWindow:     1_000_000, // window ratio would never fire
			ContextCompactRatio:  0.75,
		},
		scanCtx: scanctx.Default(),
	}
	a.lastPromptTokens.Store(1200)

	input := compactionFixture(42) // small bytes: only the explicit budget can fire
	threshold := a.compactThresholdBytes()
	size := 0
	for _, msg := range input {
		size += len(msg.Content) + perMessageOverheadBytes
	}
	if size > threshold {
		t.Fatalf("fixture hits the byte trigger (%d > %d); not isolating the budget", size, threshold)
	}
	out := (&einoObserver{agent: a}).CompactMessages(input)
	if a.compactionCount != 1 {
		t.Fatalf("explicit token budget did not compact (count=%d)", a.compactionCount)
	}
	if !strings.Contains(out[1].Content, "CONTEXT PRUNED") {
		t.Fatalf("second message is not the compaction digest: %q", out[1].Content)
	}
}

// Without provider usage, the fallback estimator must count CJK closer to one
// token per rune; the raw 4-bytes/token byte ceiling would undercount and
// compact too late.
func TestCompactFallsBackToCJKEstimator(t *testing.T) {
	input := make([]*schema.Message, 0, 42)
	input = append(input, schema.SystemMessage("system"))
	for i := 1; i < 42; i++ {
		input = append(input, schema.UserMessage(strings.Repeat("扫", 30)))
	}
	size := 0
	for _, msg := range input {
		size += len(msg.Content) + perMessageOverheadBytes
	}
	est := estimateSchemaMessagesTokens(input)
	// Pick a window whose 75% threshold sits just below the CJK estimate, so
	// the byte ceiling alone would not fire.
	a := &Agent{
		cfg: &config.Config{
			ContextCompactTokens: -1,
			LLMContextWindow:     int(float64(est)/0.75) - 4,
			ContextCompactRatio:  0.75,
		},
		scanCtx: scanctx.Default(),
	}
	if a.promptTokensNearLimit() {
		t.Fatal("fixture unexpectedly has provider usage near the limit")
	}
	if size > a.compactThresholdBytes() {
		t.Fatalf("fixture hits the byte trigger (%d > %d); not isolating the estimator", size, a.compactThresholdBytes())
	}
	if !a.estimatedTokensNearLimit(input) {
		t.Fatalf("CJK estimator %d did not reach the window threshold", est)
	}
	(&einoObserver{agent: a}).CompactMessages(input)
	if a.compactionCount != 1 {
		t.Fatalf("CJK estimator fallback did not compact (count=%d)", a.compactionCount)
	}
}

// A provider that reports PromptTokens must not be overridden by a byte
// estimate that undercounts CJK text.
func TestEstimateTextTokensCountsCJK(t *testing.T) {
	text := "扫描目标 8080 端口，测试 SQL 注入"
	cjk := estimateTextTokens(text)
	ascii := estimateTextTokens(strings.Repeat("a", utf8.RuneCountInString(text)))
	if cjk <= ascii {
		t.Fatalf("CJK estimate %d should exceed same-rune-count ASCII estimate %d", cjk, ascii)
	}
}
