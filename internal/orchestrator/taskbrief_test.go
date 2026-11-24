package orchestrator

import (
	"strings"
	"testing"
)

const structuredTaskInstruction = `preamble shared rule
## Goal
Capture the flag
## Rules
Stay in scope; at most 3 containers.
## Control-plane
Call https://bench.example.com/api/v1 only.
## Execution
Exploit the container target.
## Analysis
Read notes and summarize.
`

func TestParseTaskBriefRoutesLanes(t *testing.T) {
	brief := ParseTaskBrief("task goal text", structuredTaskInstruction)
	if brief.Goal != "task goal text" {
		t.Fatalf("goal = %q, want the task goal to win", brief.Goal)
	}
	if !strings.Contains(brief.Rules, "preamble shared rule") || !strings.Contains(brief.Rules, "Stay in scope") {
		t.Fatalf("rules lost preamble/section: %q", brief.Rules)
	}
	if !strings.Contains(brief.Execution, "Exploit the container target.") {
		t.Fatalf("execution section = %q", brief.Execution)
	}

	exec := brief.PromptFor(TaskLaneExecution)
	for _, want := range []string{"task goal text", "preamble shared rule", "Stay in scope", "Exploit the container target."} {
		if !strings.Contains(exec, want) {
			t.Fatalf("execution prompt missing %q:\n%s", want, exec)
		}
	}
	for _, forbidden := range []string{"Call https://bench.example.com/api/v1 only.", "Read notes and summarize."} {
		if strings.Contains(exec, forbidden) {
			t.Fatalf("execution prompt leaked another lane's section %q:\n%s", forbidden, exec)
		}
	}

	analysis := brief.PromptFor(TaskLaneAnalysis)
	if !strings.Contains(analysis, "Read notes and summarize.") {
		t.Fatalf("analysis prompt missing its section:\n%s", analysis)
	}
	for _, forbidden := range []string{"Call https://bench.example.com/api/v1 only.", "Exploit the container target."} {
		if strings.Contains(analysis, forbidden) {
			t.Fatalf("analysis prompt leaked %q:\n%s", forbidden, analysis)
		}
	}

	control := brief.PromptFor(TaskLaneControlPlane)
	if !strings.Contains(control, "Call https://bench.example.com/api/v1 only.") {
		t.Fatalf("control-plane prompt missing its section:\n%s", control)
	}
	for _, forbidden := range []string{"Exploit the container target.", "Read notes and summarize."} {
		if strings.Contains(control, forbidden) {
			t.Fatalf("control-plane prompt leaked %q:\n%s", forbidden, control)
		}
	}
}

func TestParseTaskBriefFallsBackToGoalSection(t *testing.T) {
	brief := ParseTaskBrief("", structuredTaskInstruction)
	if brief.Goal != "Capture the flag" {
		t.Fatalf("goal = %q, want the ## Goal section", brief.Goal)
	}
	if prompt := brief.PromptFor(TaskLaneExecution); !strings.Contains(prompt, "Capture the flag") {
		t.Fatalf("execution prompt missing goal section:\n%s", prompt)
	}
}

func TestParseTaskBriefChineseHeadings(t *testing.T) {
	brief := ParseTaskBrief("", `## 目标
拿到 flag
## 规则
不得越界
## 控制面
调用平台 API
## 执行
攻击容器
## 分析
读笔记
`)
	if brief.Goal != "拿到 flag" || brief.Rules != "不得越界" {
		t.Fatalf("goal/rules = %q / %q", brief.Goal, brief.Rules)
	}
	exec := brief.PromptFor(TaskLaneExecution)
	if !strings.Contains(exec, "攻击容器") || strings.Contains(exec, "调用平台 API") || strings.Contains(exec, "读笔记") {
		t.Fatalf("execution prompt lane routing wrong:\n%s", exec)
	}
	analysis := brief.PromptFor(TaskLaneAnalysis)
	if !strings.Contains(analysis, "读笔记") || strings.Contains(analysis, "攻击容器") {
		t.Fatalf("analysis prompt lane routing wrong:\n%s", analysis)
	}
	control := brief.PromptFor(TaskLaneControlPlane)
	if !strings.Contains(control, "调用平台 API") || strings.Contains(control, "攻击容器") {
		t.Fatalf("control-plane prompt lane routing wrong:\n%s", control)
	}
}

func TestParseTaskBriefIgnoresFencedHeadings(t *testing.T) {
	instruction := "## Rules\nuse notes\n" +
		"```\n## Execution\nthis is an example, not a section\n```\n" +
		"## Analysis\nreal analysis\n"
	brief := ParseTaskBrief("", instruction)
	if brief.Execution != "" {
		t.Fatalf("fenced heading created an Execution section: %q", brief.Execution)
	}
	if brief.Analysis != "real analysis" {
		t.Fatalf("analysis section = %q", brief.Analysis)
	}
	if !strings.Contains(brief.Rules, "this is an example, not a section") {
		t.Fatalf("fenced content should stay inside Rules: %q", brief.Rules)
	}
}

func TestParseTaskBriefNoSectionsKeepsWholeInstruction(t *testing.T) {
	const plain = "plain instruction without any recognized headings"
	brief := ParseTaskBrief("goal text", plain)
	if brief.Rules != plain {
		t.Fatalf("rules = %q, want the whole instruction", brief.Rules)
	}
	for _, lane := range []string{TaskLaneControlPlane, TaskLaneExecution, TaskLaneAnalysis} {
		prompt := brief.PromptFor(lane)
		if !strings.Contains(prompt, "goal text") || !strings.Contains(prompt, plain) {
			t.Fatalf("lane %q fallback prompt = %q", lane, prompt)
		}
	}
}
