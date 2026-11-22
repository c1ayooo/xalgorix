package platform

// Store (PostgreSQL) implementation of the integrations entities (MCP
// servers / user skills / agent profiles), backed by 0008_integrations.sql.
// Same posture as the rest of the platform tables: UUID PKs, JSONB for the
// list/map fields, unique constraints carrying the duplicate-name/role
// ErrConflict semantics.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ── MCP servers ──────────────────────────────────────────────────────────────

const mcpServerColumns = `id, name, transport, command, args, env, url, headers,
	enabled, require_approval, timeout_seconds, tool_count,
	last_healthy_at, last_error, created_by, created_at, updated_at`

func scanMcpServerRow(row pgx.Row) (*McpServer, error) {
	var (
		m          McpServer
		argsJSON   []byte
		envJSON    []byte
		headerJSON []byte
	)
	if err := row.Scan(&m.ID, &m.Name, &m.Transport, &m.Command, &argsJSON, &envJSON,
		&m.URL, &headerJSON, &m.Enabled, &m.RequireApproval, &m.TimeoutSeconds,
		&m.ToolCount, &m.LastHealthyAt, &m.LastError, &m.CreatedBy, &m.CreatedAt, &m.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("platform/postgres: scan mcp server: %w", err)
	}
	if err := unmarshalJSONB(argsJSON, &m.Args); err != nil {
		return nil, err
	}
	if err := unmarshalJSONB(envJSON, &m.Env); err != nil {
		return nil, err
	}
	if err := unmarshalJSONB(headerJSON, &m.Headers); err != nil {
		return nil, err
	}
	m = normalizeMcpServer(m)
	m.CreatedAt = m.CreatedAt.UTC()
	if m.UpdatedAt != nil {
		t := m.UpdatedAt.UTC()
		m.UpdatedAt = &t
	}
	if m.LastHealthyAt != nil {
		t := m.LastHealthyAt.UTC()
		m.LastHealthyAt = &t
	}
	return &m, nil
}

// CreateMcpServer implements Repository.CreateMcpServer.
func (s *Store) CreateMcpServer(ctx context.Context, in McpServerInput) (*McpServer, error) {
	if err := validateMcpServer(in.Name, in.Transport, in.Command, in.URL); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	if _, err := s.exec(ctx, `
		INSERT INTO mcp_servers
			(id, name, transport, command, args, env, url, headers,
			 enabled, require_approval, timeout_seconds, created_by, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		id, strings.TrimSpace(in.Name), in.Transport, strings.TrimSpace(in.Command),
		mustJSONB(orEmptySlice(in.Args)), mustJSONB(orEmptyMap(in.Env)),
		strings.TrimSpace(in.URL), mustJSONB(orEmptyMap(in.Headers)),
		in.Enabled, in.RequireApproval, normalizeMcpTimeout(in.TimeoutSeconds),
		in.CreatedBy, time.Now().UTC()); err != nil {
		return nil, mapConstraintErr(err, "create mcp server")
	}
	return s.getMcpServer(ctx, id)
}

// ListMcpServers implements Repository.ListMcpServers (newest first).
func (s *Store) ListMcpServers(ctx context.Context) ([]McpServer, error) {
	rows, err := s.query(ctx,
		`SELECT `+mcpServerColumns+` FROM mcp_servers ORDER BY created_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list mcp servers: %w", err)
	}
	defer rows.Close()
	out := []McpServer{}
	for rows.Next() {
		m, err := scanMcpServerRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// GetMcpServer implements Repository.GetMcpServer.
func (s *Store) GetMcpServer(ctx context.Context, id string) (*McpServer, error) {
	return s.getMcpServer(ctx, id)
}

func (s *Store) getMcpServer(ctx context.Context, id string) (*McpServer, error) {
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	return scanMcpServerRow(s.queryRow(ctx,
		`SELECT `+mcpServerColumns+` FROM mcp_servers WHERE id = $1`, parsed))
}

// UpdateMcpServer implements Repository.UpdateMcpServer.
func (s *Store) UpdateMcpServer(ctx context.Context, id string, upd McpServerUpdate) (*McpServer, error) {
	parsedID, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	current, err := s.getMcpServer(ctx, id)
	if err != nil {
		return nil, err
	}
	if upd.Name != nil {
		name := strings.TrimSpace(*upd.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: MCP server name is required", ErrValidation)
		}
		current.Name = name
	}
	if upd.Command != nil {
		current.Command = strings.TrimSpace(*upd.Command)
	}
	if upd.Args != nil {
		current.Args = upd.Args
	}
	if upd.Env != nil {
		current.Env = upd.Env
	}
	if upd.URL != nil {
		current.URL = strings.TrimSpace(*upd.URL)
	}
	if upd.Headers != nil {
		current.Headers = upd.Headers
	}
	if upd.Enabled != nil {
		current.Enabled = *upd.Enabled
	}
	if upd.RequireApproval != nil {
		current.RequireApproval = *upd.RequireApproval
	}
	if upd.TimeoutSeconds != nil {
		current.TimeoutSeconds = *upd.TimeoutSeconds
	}
	if upd.ToolCount != nil {
		current.ToolCount = *upd.ToolCount
	}
	if upd.LastHealthyAt != nil {
		t := upd.LastHealthyAt.UTC()
		current.LastHealthyAt = &t
	}
	if upd.LastError != nil {
		current.LastError = *upd.LastError
	}
	if err := validateMcpServer(current.Name, current.Transport, current.Command, current.URL); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	*current = normalizeMcpServer(*current)
	current.UpdatedAt = &now
	if _, err := s.exec(ctx, `
		UPDATE mcp_servers SET name=$2, transport=$3, command=$4, args=$5, env=$6,
			url=$7, headers=$8, enabled=$9, require_approval=$10, timeout_seconds=$11,
			tool_count=$12, last_healthy_at=$13, last_error=$14, updated_at=$15
		WHERE id=$1`,
		parsedID, current.Name, current.Transport, current.Command,
		mustJSONB(orEmptySlice(current.Args)), mustJSONB(orEmptyMap(current.Env)),
		current.URL, mustJSONB(orEmptyMap(current.Headers)),
		current.Enabled, current.RequireApproval, current.TimeoutSeconds,
		current.ToolCount, current.LastHealthyAt, current.LastError, now); err != nil {
		return nil, mapConstraintErr(err, "update mcp server")
	}
	return current, nil
}

// DeleteMcpServer implements Repository.DeleteMcpServer.
func (s *Store) DeleteMcpServer(ctx context.Context, id string) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return err
	}
	tag, err := s.exec(ctx, `DELETE FROM mcp_servers WHERE id = $1`, parsed)
	if err != nil {
		return fmt.Errorf("platform/postgres: delete mcp server: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── user skills ──────────────────────────────────────────────────────────────

const skillColumns = `id, name, category, description, tags, intent, body,
	enabled, created_by, created_at, updated_at`

func scanSkillRow(row pgx.Row) (*Skill, error) {
	var (
		k        Skill
		tagsJSON []byte
	)
	if err := row.Scan(&k.ID, &k.Name, &k.Category, &k.Description, &tagsJSON,
		&k.Intent, &k.Body, &k.Enabled, &k.CreatedBy, &k.CreatedAt, &k.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("platform/postgres: scan skill: %w", err)
	}
	if err := unmarshalJSONB(tagsJSON, &k.Tags); err != nil {
		return nil, err
	}
	k = normalizeSkill(k)
	k.CreatedAt = k.CreatedAt.UTC()
	if k.UpdatedAt != nil {
		t := k.UpdatedAt.UTC()
		k.UpdatedAt = &t
	}
	return &k, nil
}

// CreateSkill implements Repository.CreateSkill.
func (s *Store) CreateSkill(ctx context.Context, in SkillInput) (*Skill, error) {
	// Normalize before validating (canonical lowercase name, see FileStore).
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if err := validateSkill(name, in.Description, in.Body); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	if _, err := s.exec(ctx, `
		INSERT INTO user_skills
			(id, name, category, description, tags, intent, body, enabled, created_by, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		id, name, SkillCategoryUser,
		strings.TrimSpace(in.Description), mustJSONB(orEmptySlice(in.Tags)),
		strings.TrimSpace(in.Intent), in.Body, in.Enabled, in.CreatedBy, time.Now().UTC()); err != nil {
		return nil, mapConstraintErr(err, "create skill")
	}
	return s.getSkill(ctx, id)
}

// ListSkills implements Repository.ListSkills (newest first).
func (s *Store) ListSkills(ctx context.Context) ([]Skill, error) {
	rows, err := s.query(ctx,
		`SELECT `+skillColumns+` FROM user_skills ORDER BY created_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list skills: %w", err)
	}
	defer rows.Close()
	out := []Skill{}
	for rows.Next() {
		k, err := scanSkillRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

// GetSkill implements Repository.GetSkill.
func (s *Store) GetSkill(ctx context.Context, id string) (*Skill, error) {
	return s.getSkill(ctx, id)
}

func (s *Store) getSkill(ctx context.Context, id string) (*Skill, error) {
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	return scanSkillRow(s.queryRow(ctx,
		`SELECT `+skillColumns+` FROM user_skills WHERE id = $1`, parsed))
}

// UpdateSkill implements Repository.UpdateSkill.
func (s *Store) UpdateSkill(ctx context.Context, id string, upd SkillUpdate) (*Skill, error) {
	parsedID, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	current, err := s.getSkill(ctx, id)
	if err != nil {
		return nil, err
	}
	if upd.Name != nil {
		current.Name = strings.ToLower(strings.TrimSpace(*upd.Name))
	}
	if upd.Description != nil {
		current.Description = strings.TrimSpace(*upd.Description)
	}
	if upd.Tags != nil {
		current.Tags = upd.Tags
	}
	if upd.Intent != nil {
		current.Intent = strings.TrimSpace(*upd.Intent)
	}
	if upd.Body != nil {
		current.Body = *upd.Body
	}
	if upd.Enabled != nil {
		current.Enabled = *upd.Enabled
	}
	if err := validateSkill(current.Name, current.Description, current.Body); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	*current = normalizeSkill(*current)
	current.UpdatedAt = &now
	if _, err := s.exec(ctx, `
		UPDATE user_skills SET name=$2, description=$3, tags=$4, intent=$5,
			body=$6, enabled=$7, updated_at=$8
		WHERE id=$1`,
		parsedID, current.Name, current.Description, mustJSONB(orEmptySlice(current.Tags)),
		current.Intent, current.Body, current.Enabled, now); err != nil {
		return nil, mapConstraintErr(err, "update skill")
	}
	return current, nil
}

// DeleteSkill implements Repository.DeleteSkill.
func (s *Store) DeleteSkill(ctx context.Context, id string) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return err
	}
	tag, err := s.exec(ctx, `DELETE FROM user_skills WHERE id = $1`, parsed)
	if err != nil {
		return fmt.Errorf("platform/postgres: delete skill: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── agent profiles ───────────────────────────────────────────────────────────

const agentProfileColumns = `id, role, focus, vuln_classes, evidence_contract,
	stopping_rule, tool_scope_hidden, enabled, created_by, created_at, updated_at`

func scanAgentProfileRow(row pgx.Row) (*AgentProfile, error) {
	var (
		p         AgentProfile
		clsJSON   []byte
		scopeJSON []byte
	)
	if err := row.Scan(&p.ID, &p.Role, &p.Focus, &clsJSON, &p.EvidenceContract,
		&p.StoppingRule, &scopeJSON, &p.Enabled, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("platform/postgres: scan agent profile: %w", err)
	}
	if err := unmarshalJSONB(clsJSON, &p.VulnClasses); err != nil {
		return nil, err
	}
	if err := unmarshalJSONB(scopeJSON, &p.ToolScopeHidden); err != nil {
		return nil, err
	}
	p = normalizeAgentProfile(p)
	p.CreatedAt = p.CreatedAt.UTC()
	if p.UpdatedAt != nil {
		t := p.UpdatedAt.UTC()
		p.UpdatedAt = &t
	}
	return &p, nil
}

// CreateAgentProfile implements Repository.CreateAgentProfile.
func (s *Store) CreateAgentProfile(ctx context.Context, in AgentProfileInput) (*AgentProfile, error) {
	if err := validateAgentProfile(in.Role, in.Focus, in.EvidenceContract, in.StoppingRule); err != nil {
		return nil, err
	}
	id := uuid.NewString()
	if _, err := s.exec(ctx, `
		INSERT INTO agent_profiles
			(id, role, focus, vuln_classes, evidence_contract, stopping_rule,
			 tool_scope_hidden, enabled, created_by, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		id, strings.TrimSpace(in.Role), strings.TrimSpace(in.Focus),
		mustJSONB(orEmptySlice(in.VulnClasses)), strings.TrimSpace(in.EvidenceContract),
		strings.TrimSpace(in.StoppingRule), mustJSONB(orEmptySlice(in.ToolScopeHidden)),
		in.Enabled, in.CreatedBy, time.Now().UTC()); err != nil {
		return nil, mapConstraintErr(err, "create agent profile")
	}
	return s.getAgentProfile(ctx, id)
}

// ListAgentProfiles implements Repository.ListAgentProfiles (newest first).
func (s *Store) ListAgentProfiles(ctx context.Context) ([]AgentProfile, error) {
	rows, err := s.query(ctx,
		`SELECT `+agentProfileColumns+` FROM agent_profiles ORDER BY created_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("platform/postgres: list agent profiles: %w", err)
	}
	defer rows.Close()
	out := []AgentProfile{}
	for rows.Next() {
		p, err := scanAgentProfileRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// GetAgentProfile implements Repository.GetAgentProfile.
func (s *Store) GetAgentProfile(ctx context.Context, id string) (*AgentProfile, error) {
	return s.getAgentProfile(ctx, id)
}

func (s *Store) getAgentProfile(ctx context.Context, id string) (*AgentProfile, error) {
	parsed, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	return scanAgentProfileRow(s.queryRow(ctx,
		`SELECT `+agentProfileColumns+` FROM agent_profiles WHERE id = $1`, parsed))
}

// UpdateAgentProfile implements Repository.UpdateAgentProfile.
func (s *Store) UpdateAgentProfile(ctx context.Context, id string, upd AgentProfileUpdate) (*AgentProfile, error) {
	parsedID, err := parseUUID(id)
	if err != nil {
		return nil, err
	}
	current, err := s.getAgentProfile(ctx, id)
	if err != nil {
		return nil, err
	}
	if upd.Role != nil {
		current.Role = strings.TrimSpace(*upd.Role)
	}
	if upd.Focus != nil {
		current.Focus = strings.TrimSpace(*upd.Focus)
	}
	if upd.VulnClasses != nil {
		current.VulnClasses = upd.VulnClasses
	}
	if upd.EvidenceContract != nil {
		current.EvidenceContract = strings.TrimSpace(*upd.EvidenceContract)
	}
	if upd.StoppingRule != nil {
		current.StoppingRule = strings.TrimSpace(*upd.StoppingRule)
	}
	if upd.ToolScopeHidden != nil {
		current.ToolScopeHidden = upd.ToolScopeHidden
	}
	if upd.Enabled != nil {
		current.Enabled = *upd.Enabled
	}
	if err := validateAgentProfile(current.Role, current.Focus, current.EvidenceContract, current.StoppingRule); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	*current = normalizeAgentProfile(*current)
	current.UpdatedAt = &now
	if _, err := s.exec(ctx, `
		UPDATE agent_profiles SET role=$2, focus=$3, vuln_classes=$4,
			evidence_contract=$5, stopping_rule=$6, tool_scope_hidden=$7,
			enabled=$8, updated_at=$9
		WHERE id=$1`,
		parsedID, current.Role, current.Focus, mustJSONB(orEmptySlice(current.VulnClasses)),
		current.EvidenceContract, current.StoppingRule, mustJSONB(orEmptySlice(current.ToolScopeHidden)),
		current.Enabled, now); err != nil {
		return nil, mapConstraintErr(err, "update agent profile")
	}
	return current, nil
}

// DeleteAgentProfile implements Repository.DeleteAgentProfile.
func (s *Store) DeleteAgentProfile(ctx context.Context, id string) error {
	parsed, err := parseUUID(id)
	if err != nil {
		return err
	}
	tag, err := s.exec(ctx, `DELETE FROM agent_profiles WHERE id = $1`, parsed)
	if err != nil {
		return fmt.Errorf("platform/postgres: delete agent profile: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ── small shared helpers ─────────────────────────────────────────────────────

// unmarshalJSONB decodes a JSONB column into out; NULL/empty decodes to the
// zero value untouched (callers normalize afterwards).
func unmarshalJSONB(data []byte, out any) error {
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("platform/postgres: decode jsonb: %w", err)
	}
	return nil
}

// mustJSONB encodes v or panics: every input here comes from typed structs
// that marshalJSON already validated on the write path.
func mustJSONB(v any) []byte {
	data, err := marshalJSON(v)
	if err != nil {
		panic(fmt.Sprintf("platform/postgres: marshal jsonb: %v", err))
	}
	return data
}

// orEmptySlice / orEmptyMap keep nil slices/maps out of the JSONB columns so
// file snapshots and PG rows round-trip identically.
func orEmptySlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// normalizeMcpTimeout clamps the per-call timeout to the shared default.
func normalizeMcpTimeout(seconds int) int {
	if seconds <= 0 {
		return DefaultMcpTimeoutSeconds
	}
	return seconds
}
