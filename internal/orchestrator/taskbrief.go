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
		tb.Rules = strings.TrimSpace(instruction)
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
	return tb
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
