package orchestrator

import (
	"strings"
	"testing"
)

// A task instruction is human prose. When the operator writes an API manual as
// "# 一、总览 / ## 5.4 提交 Flag" instead of "## Control-plane", the manual must
// still stay out of the execution lane — otherwise every container worker is
// handed the platform's auth headers and endpoint list, which is exactly what
// lane routing exists to prevent.
const chineseManualInstruction = `# 跑分纪律（严格遵守）
1. 拿到 flag 立即 submit。
2. 用满并发，不要串行逐题。
# 并行委派
三个 worker 同时推进。

# TSecBench API 接入文档
> 本文档面向调用评测接口的开发者。

# 一、总览
## 1. 认证方式
所有 /openapi/v1/challenges/* 接口通过 BENCHMARK_TOKEN 头鉴权。

## 2. 接口列表与调用流程
GET /openapi/v1/challenges 列出题目
POST /openapi/v1/challenges/start 启动容器
POST /openapi/v1/challenges/submit 提交 flag

## 3. 注意事项
start/close 涉及资源分配，可重试。

# 二、接口详情
## 5.4 提交 Flag
POST /openapi/v1/challenges/submit
请求体：{"unique_code":"a-05","flag":"flag{x}"}

# 三、最佳实践
建议先 close 再 start 重置环境。
`

func TestSplitControlPlaneManualKeepsConstraintsOutOfTheManual(t *testing.T) {
	tb := ParseTaskBrief("", chineseManualInstruction)

	if strings.Contains(tb.Rules, "/openapi/v1/challenges") {
		t.Fatalf("shared rules still carry the API manual:\n%s", tb.Rules)
	}
	if !strings.Contains(tb.Rules, "跑分纪律") {
		t.Fatal("shared rules lost the discipline chapter")
	}
	if !strings.Contains(tb.Rules, "并行委派") {
		t.Fatal("shared rules lost the delegation chapter")
	}

	// The manual, including its sub-chapters and trailing best-practice block,
	// belongs to the control-plane lane.
	if !strings.Contains(tb.ControlPlane, "/openapi/v1/challenges/submit") {
		t.Fatalf("control-plane lost the submit endpoint:\n%s", tb.ControlPlane)
	}
	if !strings.Contains(tb.ControlPlane, "最佳实践") {
		t.Fatal("manual block stopped before the trailing best-practice chapter")
	}
	if strings.Contains(tb.ControlPlane, "跑分纪律") {
		t.Fatal("constraint chapters leaked into the manual block")
	}
}

func TestPromptForHidesManualFromExecutionLane(t *testing.T) {
	tb := ParseTaskBrief("", chineseManualInstruction)

	exec := tb.PromptFor(TaskLaneExecution)
	if strings.Contains(exec, "/openapi/v1/challenges") {
		t.Fatalf("execution lane received the API manual:\n%s", exec)
	}
	if !strings.Contains(exec, "跑分纪律") {
		t.Fatal("execution lane lost the constraints")
	}

	cp := tb.PromptFor(TaskLaneControlPlane)
	if !strings.Contains(cp, "/openapi/v1/challenges/submit") {
		t.Fatal("control-plane lane lost the manual it drives")
	}
	if !strings.Contains(cp, "跑分纪律") {
		t.Fatal("control-plane lane must still see the operator constraints")
	}
}

func TestSplitControlPlaneManualLeavesPlainInstructionsAlone(t *testing.T) {
	plain := `# 要求
只测 in-scope 资产，禁止打 127.0.0.1。
# 交付
每个漏洞写清复现步骤。`
	tb := ParseTaskBrief("", plain)
	if tb.ControlPlane != "" {
		t.Fatalf("plain instruction produced a manual block: %q", tb.ControlPlane)
	}
	if !strings.Contains(tb.Rules, "禁止打") {
		t.Fatal("plain instruction was mangled")
	}
}

func TestSplitControlPlaneManualStopsAtNextConstraintChapter(t *testing.T) {
	// The manual is followed by a constraint chapter at the same level: the
	// split must end there instead of swallowing the rest of the instruction.
	instr := `# API 接入文档
POST /openapi/v1/challenges/submit
# 收尾要求
打完必须 close 所有容器。`
	tb := ParseTaskBrief("", instr)
	if strings.Contains(tb.Rules, "/openapi/v1/challenges") {
		t.Fatal("manual stayed in the shared block")
	}
	if !strings.Contains(tb.Rules, "收尾要求") {
		t.Fatalf("constraint chapter after the manual was swallowed:\n%s", tb.Rules)
	}
	if strings.Contains(tb.ControlPlane, "收尾要求") {
		t.Fatal("constraint chapter landed in the manual")
	}
}

func TestSplitControlPlaneManualIgnoresHeadingsInsideCodeFences(t *testing.T) {
	// A shell comment inside a fenced block is content, not a chapter.
	instr := "# 要求\n```bash\n# API 接入文档\ncurl http://x\n```\n保持只读。"
	tb := ParseTaskBrief("", instr)
	if tb.ControlPlane != "" {
		t.Fatalf("a fenced comment was treated as a manual heading: %q", tb.ControlPlane)
	}
	if !strings.Contains(tb.Rules, "保持只读") {
		t.Fatal("body after the fence was lost")
	}
}

func TestStructuredBriefStillRoutesWhenManualIsInRules(t *testing.T) {
	// Explicit headings still win, but a manual pasted into ## Rules must not
	// reach execution lanes either.
	instr := `## Rules
禁止越权。
# API 接入文档
POST /openapi/v1/challenges/submit`
	tb := ParseTaskBrief("", instr)
	if tb.structured != true {
		t.Fatal("explicit ## Rules should mark the brief structured")
	}
	if strings.Contains(tb.PromptFor(TaskLaneExecution), "/openapi/v1/challenges") {
		t.Fatal("manual inside ## Rules reached the execution lane")
	}
	if !strings.Contains(tb.ControlPlane, "/openapi/v1/challenges/submit") {
		t.Fatal("manual was dropped instead of routed to control-plane")
	}
}
