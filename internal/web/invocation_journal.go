package web

// invocation_journal.go — cross-scan invocation records for the integration
// modules (MCP tool calls / skill loads / sub-agent delegations).
//
// Posture (charter §5): the per-scan audit AUTHORITY is the domain journal
// (skill_load / agent_delegate / mcp_call records, scan_id+seq); events.jsonl
// is telemetry and is never an audit source. This file implements the
// cross-scan RETRIEVAL projection the integration pages serve: one JSONL file
// per module under <dataDir>/_invocations/, appended best-effort by
// processEvent from engine events. A missing or failed record never affects
// a scan (RecordApprovalRuleHit precedent — the definition row is the
// authority, bookkeeping is a projection).

import (
	"bufio"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/xalgord/xalgorix/v4/internal/agent"
)

// Invocation modules.
const (
	InvocationModuleMCP    = "mcp"
	InvocationModuleSkills = "skills"
	InvocationModuleAgents = "agents"
)

// Invocation record statuses.
const (
	InvocationStatusOK    = "ok"
	InvocationStatusError = "error"
)

// invocationMaxFileBytes caps one journal file before rotation. Rotation is a
// single rename to "<file>.old" (previous .old is overwritten) — bounded
// disk, no unbounded history, no retention machinery.
const invocationMaxFileBytes = 8 << 20 // 8 MiB

// invocationArgsDigestBytes bounds the echoed arguments blob per record.
const invocationArgsDigestBytes = 512

// InvocationRecord is one integration invocation as served by the
// /invocations endpoints (docs/API_CONTRACT.md §integration-invocations).
type InvocationRecord struct {
	ID         string `json:"id"`
	Module     string `json:"module"`
	TS         string `json:"ts"` // RFC3339 UTC
	InstanceID string `json:"instance_id,omitempty"`
	ScanID     string `json:"scan_id,omitempty"`
	// AgentID is the delegated agent that performed the call ("" = root).
	AgentID string `json:"agent_id,omitempty"`
	// Name is the vocabulary of the module: the MCP bridge tool name
	// (mcp_<server>_<tool>), the canonical skill name, or the specialist
	// role (delegate_result for the completion record of a delegation).
	Name string `json:"name"`
	// Server is the MCP server segment (mcp module only).
	Server  string `json:"server,omitempty"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
	Args    string `json:"args,omitempty"` // bounded digest of the call arguments
	Outcome string `json:"outcome,omitempty"`
}

// invocationJournal is one module's JSONL append log. Appends serialize on
// the mutex; processEvent is single-goroutine per scan but concurrent scans
// share the file.
type invocationJournal struct {
	mu   sync.Mutex
	path string
}

// invocationJournals is the process-wide journal set keyed by module. The
// files live under <dataDir>/_invocations/. Created at Server startup; a nil
// entry (dataDir unknown) disables recording.
var (
	invocationJournalsMu sync.RWMutex
	invocationJournals   = map[string]*invocationJournal{}
)

var invocationModules = []string{InvocationModuleMCP, InvocationModuleSkills, InvocationModuleAgents}

// initInvocationJournals wires the journal set to the server's data dir.
func (s *Server) initInvocationJournals() {
	invocationJournalsMu.Lock()
	defer invocationJournalsMu.Unlock()
	base := filepath.Join(s.dataDir, "_invocations")
	if err := os.MkdirAll(base, 0o700); err != nil {
		// No directory → no cross-scan records; scans are unaffected.
		log.Printf("[invocations] journal dir unavailable: %v", err)
		return
	}
	for _, module := range invocationModules {
		invocationJournals[module] = &invocationJournal{path: filepath.Join(base, module+".jsonl")}
	}
}

// recordInvocation appends one record best-effort. Derivation failures are
// swallowed, never propagated — a scan never fails because bookkeeping did.
func recordInvocation(rec InvocationRecord) {
	invocationJournalsMu.RLock()
	j := invocationJournals[rec.Module]
	invocationJournalsMu.RUnlock()
	if j == nil {
		return
	}
	j.append(rec)
}

func (j *invocationJournal) append(rec InvocationRecord) {
	if rec.ID == "" {
		rec.ID = uuid.NewString()
	}
	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format(time.RFC3339)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.rotateIfNeededLocked(int64(len(data) + 1))
	f, err := os.OpenFile(j.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return
	}
	// events.jsonl fsyncs every append; these records are a retrieval
	// projection of the already-durable domain journal, so the cheaper
	// write-only path is honest here. A crash loses at most recent records.
}

// rotateIfNeededLocked moves a full journal to "<file>.old" before appending.
// Callers hold j.mu.
func (j *invocationJournal) rotateIfNeededLocked(incoming int64) {
	info, err := os.Stat(j.path)
	if err != nil || info.Size()+incoming <= invocationMaxFileBytes {
		return
	}
	_ = os.Remove(j.path + ".old")
	if err := os.Rename(j.path, j.path+".old"); err != nil {
		log.Printf("[invocations] rotate %s failed: %v", j.path, err)
	}
}

// listInvocations returns the newest-first page of a module's records after
// applying the optional filters (instanceID / name / status; empty = any).
// A missing file is an empty list, not an error.
func listInvocations(module string, page, size int, instanceID, name, status string) ([]InvocationRecord, int, error) {
	invocationJournalsMu.RLock()
	j := invocationJournals[module]
	invocationJournalsMu.RUnlock()
	if j == nil {
		return []InvocationRecord{}, 0, nil
	}
	// .old (pre-rotation) is read before the live file so the newest-first
	// reversal over the concatenation stays chronological.
	paths := []string{j.path + ".old", j.path}
	all := []InvocationRecord{}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue // missing file / rotation gap — honest empty read
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(strings.TrimSpace(string(line))) == 0 {
				continue
			}
			var rec InvocationRecord
			if err := json.Unmarshal(line, &rec); err != nil {
				// A torn final line from a crash is skipped, not fatal —
				// same partial-append posture as the event journal.
				continue
			}
			if instanceID != "" && rec.InstanceID != instanceID {
				continue
			}
			if name != "" && !invocationNameMatches(rec.Name, name) {
				continue
			}
			if status != "" && rec.Status != status {
				continue
			}
			all = append(all, rec)
		}
		_ = f.Close()
	}
	// Newest first (append order is chronological).
	for i, rn := 0, len(all)/2; i < rn; i++ {
		all[i], all[len(all)-1-i] = all[len(all)-1-i], all[i]
	}
	total := len(all)
	if page < 1 {
		page = 1
	}
	if size < 1 {
		size = 50
	}
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	return all[start:end], total, nil
}

func invocationNameMatches(recordName, filter string) bool {
	lower := strings.ToLower(recordName)
	return lower == strings.ToLower(filter) || strings.Contains(lower, strings.ToLower(filter))
}

// deriveInvocationRecords turns one engine event into zero or more
// integration invocation records. Called from processEvent (single-goroutine
// per scan): read_skill completions → skills; mcp_* tool completions → mcp;
// spawn/create results and delegated-agent finish events → agents.
func deriveInvocationRecords(evt agent.Event, sess *scanSession) {
	if sess == nil {
		return
	}
	errMsg := ""
	output := ""
	success := true
	if evt.Type == "tool_result" {
		errMsg = evt.ToolResult.Error
		output = evt.ToolResult.Output
		success = errMsg == ""
	}
	base := func(module, name string) InvocationRecord {
		return InvocationRecord{
			Module:     module,
			TS:         time.Now().UTC().Format(time.RFC3339),
			InstanceID: sess.instanceID,
			ScanID:     sess.id,
			AgentID:    evt.AgentID,
			Name:       name,
			Args:       digestToolArgs(evt.ToolArgs),
		}
	}
	switch {
	// Skill loads: every read_skill completion (first load or reload).
	case evt.Type == "tool_result" && evt.ToolName == "read_skill":
		name := strings.TrimSpace(evt.ToolArgs["skill_name"])
		if name == "" {
			name = strings.TrimSpace(evt.ToolArgs["name"])
		}
		if name == "" {
			return
		}
		rec := base(InvocationModuleSkills, name)
		if success {
			rec.Status = InvocationStatusOK
		} else {
			rec.Status, rec.Error = InvocationStatusError, truncString(errMsg, 300)
		}
		recordInvocation(rec)

	// MCP tool calls (bridge naming mcp_<server>_<tool>, internal/tools/mcp).
	case evt.Type == "tool_result" && strings.HasPrefix(evt.ToolName, "mcp_"):
		rec := base(InvocationModuleMCP, evt.ToolName)
		rec.Server = mcpServerFromToolName(evt.ToolName)
		if success {
			rec.Status = InvocationStatusOK
		} else {
			rec.Status, rec.Error = InvocationStatusError, truncString(errMsg, 300)
		}
		recordInvocation(rec)

	// Delegation spawn: the graph tool's result carries the agent identity.
	case evt.Type == "tool_result" && (evt.ToolName == "spawn_agent" || evt.ToolName == "create_agent"):
		agentID, _ := evt.ToolResult.Metadata["agent_id"].(string)
		rec := base(InvocationModuleAgents, strings.TrimSpace(evt.ToolArgs["name"]))
		rec.AgentID = agentID
		if success {
			rec.Status = InvocationStatusOK
			rec.Outcome = truncString(output, 300)
		} else {
			rec.Status, rec.Error = InvocationStatusError, truncString(errMsg, 300)
		}
		recordInvocation(rec)
	}

	// Delegation completion: a finished event from a delegated agent id
	// (sub_/sync_ prefixes from the graph's nextIDLocked).
	if evt.Type == "finished" && (strings.HasPrefix(evt.AgentID, "sub_") || strings.HasPrefix(evt.AgentID, "sync_")) {
		rec := base(InvocationModuleAgents, "delegate_result")
		rec.Args = ""
		if evt.Aborted {
			reason := evt.AbortReason
			if strings.TrimSpace(reason) == "" {
				reason = "delegated agent aborted"
			}
			rec.Status, rec.Error = InvocationStatusError, truncString(reason, 300)
		} else {
			rec.Status = InvocationStatusOK
			rec.Outcome = truncString(evt.Content, 300)
		}
		recordInvocation(rec)
	}
}

// digestToolArgs renders the arguments as a bounded JSON digest. Secret-ish
// keys are masked before serialization (defense in depth: engine events are
// already secret-redacted upstream).
func digestToolArgs(args map[string]string) string {
	if len(args) == 0 {
		return ""
	}
	filtered := map[string]string{}
	for k, v := range args {
		if containsSensitiveKey(k) {
			filtered[k] = "***"
			continue
		}
		filtered[k] = truncString(v, 120)
	}
	data, err := json.Marshal(filtered)
	if err != nil {
		return ""
	}
	return truncString(string(data), invocationArgsDigestBytes)
}

// containsSensitiveKey reports whether an argument key looks like a secret
// field.
func containsSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "token") || strings.Contains(k, "secret") ||
		strings.Contains(k, "password") || strings.Contains(k, "api_key") ||
		strings.Contains(k, "apikey") || strings.Contains(k, "authorization")
}

// mcpServerFromToolName extracts the server segment from the
// mcp_<server>_<tool> bridge naming.
func mcpServerFromToolName(toolName string) string {
	parts := strings.SplitN(strings.TrimPrefix(toolName, "mcp_"), "_", 2)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func truncString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
