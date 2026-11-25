package orchestrator

// taskbrief.go — deterministic lane routing for task instructions.
//
// A task carries two human-authored prompts: Goal (what success means) and
// Instruction (how/constraints/deliverables). The Instruction may be written
// with fixed Markdown sections so each worker lane only sees the slice that
// matches its tool surface:
//
//	## Goal
//	## Rules            (shared: scope, credentials, time, prohibitions)
//	## Control-plane    (control_plane_api lane only)
//	## Execution        (container/web/host execution lanes only)
//	## Analysis         (analysis lane only)
//
// The split is deterministic (no model call), so a control-plane playbook can
// never leak into an execution/analysis prompt — the run5 failure where an
// analysis lane received the platform manual and started challenges itself.
// A plain instruction without recognized sections keeps its old meaning: the
// whole text is the shared rules block every lane receives.

import (
	"fmt"
	"strings"
)

// Task lanes. They line up with the pool's tool surfaces
// (surfaceForIntent): orchestrator / worker / analysis.
const (
	TaskLaneControlPlane = "control-plane"
	TaskLaneExecution    = "execution"
	TaskLaneAnalysis     = "analysis"
)

// TaskBrief is the deterministic split of a task's Goal + Instruction by lane.
type TaskBrief struct {
	// Goal is the task Goal when set, otherwise the content of a `## Goal`
	// section. It is read-only context for every lane.
	Goal string
	// Rules is the shared block: the instruction preamble before the first
	// recognized heading plus any `## Rules` section.
	Rules string
	// ControlPlane is only rendered for the control_plane_api lane.
	ControlPlane string
	// Execution is only rendered for container/web/host execution lanes.
	Execution string
	// Analysis is only rendered for the analysis lane.
	Analysis string

	// structured is true when the instruction carried at least one recognized
	// heading. When false, Rules holds the whole instruction verbatim and the
	// rendering matches the pre-lane-routing behavior.
	structured bool
}

// briefSection identifies one recognized instruction section.
type briefSection int

const (
	secNone briefSection = iota
	secGoal
	secRules
	secControlPlane
	secExecution
	secAnalysis
)

// ParseTaskBrief splits an instruction into lane-routed sections. A fenced code
// block never produces a section boundary, so documentation examples that
// happen to contain "## Execution" stay inside the surrounding section.
func ParseTaskBrief(goal, instruction string) TaskBrief {
	tb := TaskBrief{Goal: strings.TrimSpace(goal)}
	instruction = strings.ReplaceAll(instruction, "\r\n", "\n")
	instruction = strings.ReplaceAll(instruction, "\r", "\n")
	if strings.TrimSpace(instruction) == "" {
		return tb
	}

	var preamble, goalSec, rulesSec, cpSec, execSec, anaSec strings.Builder
	cur := &preamble
	structured := false
	inFence := false
	fence := ""

	for _, raw := range strings.Split(instruction, "\n") {
		trimmed := strings.TrimSpace(raw)
		if inFence {
			cur.WriteString(raw)
			cur.WriteString("\n")
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
			}
			continue
		}
		if marker, ok := fenceMarkerOf(trimmed); ok {
			inFence = true
			fence = marker
			cur.WriteString(raw)
			cur.WriteString("\n")
			continue
		}
		if sec, ok := taskBriefHeading(trimmed); ok {
			structured = true
			switch sec {
			case secGoal:
				cur = &goalSec
			case secRules:
				cur = &rulesSec
			case secControlPlane:
				cur = &cpSec
			case secExecution:
				cur = &execSec
			case secAnalysis:
				cur = &anaSec
			}
			continue
		}
		cur.WriteString(raw)
		cur.WriteString("\n")
	}

	if !structured {
		// Backward compatibility: no recognized headings, so the whole
		// instruction is the shared rules text for every lane.
		//
		// The one thing that must never be shared: an API reference manual.
		// Operators write "## 5.4 提交 Flag"-style chapters, not "## Rules",
		// so without this split every execution worker is handed the platform
		// manual — the leak the lane routing exists to prevent.
		tb.Rules, tb.ControlPlane = splitControlPlaneManual(strings.TrimSpace(instruction))
		return tb
	}

	tb.structured = true
	if tb.Goal == "" {
		tb.Goal = strings.TrimSpace(goalSec.String())
	}
	tb.Rules = joinBriefSections(preamble.String(), rulesSec.String())
	tb.ControlPlane = strings.TrimSpace(cpSec.String())
	tb.Execution = strings.TrimSpace(execSec.String())
	tb.Analysis = strings.TrimSpace(anaSec.String())

	// A structured brief can still carry the manual inside the shared block
	// (operators mix formats). Peel it out for the same reason as above.
	rules, extra := splitControlPlaneManual(tb.Rules)
	tb.Rules = rules
	tb.ControlPlane = joinBriefSections(tb.ControlPlane, extra)
	return tb
}

// splitControlPlaneManual divides an instruction into the part every lane
// needs (constraints, procedure, prohibitions) and the API reference manual
// that only the control-plane lane can act on.
//
// Two deterministic signals, no model call:
//   - a chapter heading names the API surface ("TSecBench API 接入文档",
//     "5.4 提交 Flag", "认证方式", "错误码");
//   - a chapter body is dense in endpoints and HTTP methods.
//
// Once a manual chapter is found the block runs forward until a chapter whose
// heading names a constraint ("纪律/约束/禁止/要求/协议..."), so sub-chapters and
// trailing "最佳实践" stay with the manual instead of needing their own match.
func splitControlPlaneManual(text string) (string, string) {
	if strings.TrimSpace(text) == "" {
		return text, ""
	}
	chapters, lines := splitBriefChapters(text)
	if len(chapters) == 0 {
		return text, ""
	}
	first := -1
	for i, c := range chapters {
		if manualChapterTitle(c.title) || manualChapterBody(lines[c.head:c.end]) {
			first = i
			break
		}
	}
	if first < 0 {
		return text, ""
	}
	last := len(chapters)
	for i := first + 1; i < len(chapters); i++ {
		// Only a heading at the manual's own level (or higher) can end the
		// block. A deeper heading is a manual sub-chapter — "## 3. 注意事项"
		// inside an API reference is still reference material.
		if chapters[i].level > chapters[first].level {
			continue
		}
		if constraintChapterTitle(chapters[i].title) {
			last = i
			break
		}
	}
	from, to := chapters[first].head, chapters[last-1].end
	body := strings.Join(append(append([]string{}, lines[:from]...), lines[to:]...), "\n")
	manual := strings.Join(lines[from:to], "\n")
	return strings.TrimSpace(body), strings.TrimSpace(manual)
}

// briefChapter is one heading-delimited block, in line coordinates.
type briefChapter struct {
	head, end int
	title     string
	level     int
}

// splitBriefChapters indexes heading-delimited blocks. A `#` inside a fenced
// code block is content, never a chapter boundary.
func splitBriefChapters(text string) ([]briefChapter, []string) {
	lines := strings.Split(text, "\n")
	var chapters []briefChapter
	inFence := false
	fence := ""
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if inFence {
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
			}
			continue
		}
		if marker, ok := fenceMarkerOf(trimmed); ok {
			inFence = true
			fence = marker
			continue
		}
		level, title, ok := headingParts(trimmed)
		if !ok {
			continue
		}
		chapters = append(chapters, briefChapter{head: i, title: title, level: level})
	}
	for i := range chapters {
		if i+1 < len(chapters) {
			chapters[i].end = chapters[i+1].head
		} else {
			chapters[i].end = len(lines)
		}
	}
	return chapters, lines
}

// headingParts splits an ATX heading into level and title.
func headingParts(line string) (int, string, bool) {
	if !strings.HasPrefix(line, "#") {
		return 0, "", false
	}
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i == 0 || i > 6 || i >= len(line) || (line[i] != ' ' && line[i] != '	') {
		return 0, "", false
	}
	title := strings.TrimSpace(line[i:])
	return i, strings.TrimSpace(strings.TrimRight(title, "#")), true
}

// manualChapterTitle reports a heading that introduces API reference material.
func manualChapterTitle(title string) bool {
	t := normalizeBriefHeading(title)
	if strings.Contains(t, "api") {
		for _, k := range []string{"文档", "接入", "手册", "参考", "说明", "规范", "详情"} {
			if strings.Contains(t, k) {
				return true
			}
		}
	}
	if strings.Contains(t, "接口") {
		for _, k := range []string{"详情", "列表", "说明", "文档", "定义", "汇总"} {
			if strings.Contains(t, k) {
				return true
			}
		}
	}
	for _, k := range []string{"认证方式", "错误码", "请求示例", "响应格式", "返回格式", "调用流程"} {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

// manualChapterBody reports a chapter that reads like reference material even
// without a telling title: several endpoints plus several HTTP methods.
func manualChapterBody(lines []string) bool {
	s := strings.Join(lines, "\n")
	endpoints := strings.Count(s, "/openapi/") + strings.Count(s, "/api/v1/")
	if endpoints < 2 {
		return false
	}
	methods := 0
	for _, m := range []string{"GET ", "POST ", "PUT ", "DELETE ", "PATCH "} {
		methods += strings.Count(s, m)
	}
	return methods >= 2
}

// constraintChapterTitle reports a heading that ends a manual block: the rest
// of the instruction is about how to work, not about the API surface.
func constraintChapterTitle(title string) bool {
	t := normalizeBriefHeading(title)
	for _, k := range []string{
		"纪律", "约束", "要求", "禁止", "规则", "协议", "必须",
		"注意", "上报", "委派", "收尾", "时限", "停止", "交付",
	} {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

// PromptFor renders the brief for one lane: Goal, the shared Rules, then the
// lane's own section if the instruction provided one. A control-plane prompt
// never carries the Execution/Analysis sections, and execution/analysis
// prompts never carry the Control-plane section.
func (tb *TaskBrief) PromptFor(lane string) string {
	if tb == nil {
		return ""
	}
	var b strings.Builder
	appendSection := func(heading, body string) {
		body = strings.TrimSpace(body)
		if body == "" {
			return
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		if tb.structured {
			fmt.Fprintf(&b, "## %s\n%s\n", heading, body)
		} else if heading == "Control-plane" {
			// Unstructured brief: the shared block is rendered verbatim, but
			// the API manual needs its own label so the lane can tell the
			// operator's constraints apart from the reference it drives.
			fmt.Fprintf(&b, "## Control-plane API reference\n%s\n", body)
		} else {
			b.WriteString(body)
			b.WriteString("\n")
		}
	}
	appendSection("Goal", tb.Goal)
	appendSection("Rules", tb.Rules)
	switch laneSection(lane) {
	case secControlPlane:
		appendSection("Control-plane", tb.ControlPlane)
	case secExecution:
		appendSection("Execution", tb.Execution)
	case secAnalysis:
		appendSection("Analysis", tb.Analysis)
	}
	return strings.TrimSpace(b.String())
}

// laneSection maps a lane name (and a few aliases) to its brief section.
func laneSection(lane string) briefSection {
	switch normalizeBriefHeading(lane) {
	case "controlplane", "control", "orchestrator", "controlplaneapi":
		return secControlPlane
	case "execution", "execute", "worker":
		return secExecution
	case "analysis", "analyst", "task":
		return secAnalysis
	}
	return secNone
}

// taskBriefHeading recognizes one section heading line. It accepts `#` through
// `######`, the English section names and their Chinese equivalents.
func taskBriefHeading(line string) (briefSection, bool) {
	if !strings.HasPrefix(line, "#") {
		return secNone, false
	}
	i := 0
	for i < len(line) && line[i] == '#' {
		i++
	}
	if i == 0 || i > 6 || i >= len(line) {
		return secNone, false
	}
	if line[i] != ' ' && line[i] != '\t' {
		return secNone, false
	}
	title := strings.TrimSpace(line[i:])
	title = strings.TrimSpace(strings.TrimRight(title, "#"))
	switch normalizeBriefHeading(title) {
	case "goal", "目标":
		return secGoal, true
	case "rules", "规则":
		return secRules, true
	case "controlplane", "控制面":
		return secControlPlane, true
	case "execution", "执行":
		return secExecution, true
	case "analysis", "分析":
		return secAnalysis, true
	}
	return secNone, false
}

// normalizeBriefHeading lower-cases a heading and removes separators so
// "Control-plane", "Control Plane" and "control_plane" are the same key.
func normalizeBriefHeading(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer("-", "", "_", "", " ", "", "\t", "").Replace(s)
}

// fenceMarkerOf reports the fence marker a line opens (``` or ~~~).
func fenceMarkerOf(trimmed string) (string, bool) {
	switch {
	case strings.HasPrefix(trimmed, "```"):
		return "```", true
	case strings.HasPrefix(trimmed, "~~~"):
		return "~~~", true
	}
	return "", false
}

func joinBriefSections(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}
