package platform

// FileStore implementation of the integrations entities (MCP servers / user
// skills / agent profiles). Same snapshot posture as the rest of the store:
// one full platform.json rewrite per mutation behind the shared RWMutex —
// these are low-frequency operator definitions, not per-call records, so the
// snapshot cost is negligible. High-frequency invocation records deliberately
// do NOT live here (web-layer JSONL journals).

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// snapshotAdditions note: the three slices ride on fileSnapshot directly (see
// filestore.go) — added there because the snapshot struct owns persistence
// shape.

// CreateMcpServer implements Repository.CreateMcpServer.
func (s *FileStore) CreateMcpServer(_ context.Context, in McpServerInput) (*McpServer, error) {
	if err := validateMcpServer(in.Name, in.Transport, in.Command, in.URL); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	name := strings.TrimSpace(in.Name)
	for _, m := range s.data.McpServers {
		if m.Name == name {
			return nil, fmt.Errorf("%w: MCP server name %q already exists", ErrConflict, name)
		}
	}
	now := time.Now().UTC()
	server := normalizeMcpServer(McpServer{
		ID:              uuid.NewString(),
		Name:            name,
		Transport:       in.Transport,
		Command:         strings.TrimSpace(in.Command),
		Args:            in.Args,
		Env:             in.Env,
		URL:             strings.TrimSpace(in.URL),
		Headers:         in.Headers,
		Enabled:         in.Enabled,
		RequireApproval: in.RequireApproval,
		TimeoutSeconds:  in.TimeoutSeconds,
		CreatedBy:       in.CreatedBy,
		CreatedAt:       now,
	})
	s.data.McpServers = append(s.data.McpServers, server)
	if err := s.save(); err != nil {
		return nil, err
	}
	returned := server
	return &returned, nil
}

// ListMcpServers implements Repository.ListMcpServers (newest first).
func (s *FileStore) ListMcpServers(_ context.Context) ([]McpServer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]McpServer(nil), s.data.McpServers...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	for i := range out {
		out[i] = normalizeMcpServer(out[i])
	}
	return out, nil
}

// GetMcpServer implements Repository.GetMcpServer.
func (s *FileStore) GetMcpServer(_ context.Context, id string) (*McpServer, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, m := range s.data.McpServers {
		if m.ID == id {
			returned := normalizeMcpServer(m)
			return &returned, nil
		}
	}
	return nil, ErrNotFound
}

// UpdateMcpServer implements Repository.UpdateMcpServer.
func (s *FileStore) UpdateMcpServer(_ context.Context, id string, upd McpServerUpdate) (*McpServer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.McpServers {
		m := &s.data.McpServers[i]
		if m.ID != id {
			continue
		}
		if upd.Name != nil {
			name := strings.TrimSpace(*upd.Name)
			if name == "" {
				return nil, fmt.Errorf("%w: MCP server name is required", ErrValidation)
			}
			for j := range s.data.McpServers {
				if j != i && s.data.McpServers[j].Name == name {
					return nil, fmt.Errorf("%w: MCP server name %q already exists", ErrConflict, name)
				}
			}
			m.Name = name
		}
		if upd.Command != nil {
			m.Command = strings.TrimSpace(*upd.Command)
		}
		if upd.Args != nil {
			m.Args = upd.Args
		}
		if upd.Env != nil {
			m.Env = upd.Env
		}
		if upd.URL != nil {
			m.URL = strings.TrimSpace(*upd.URL)
		}
		if upd.Headers != nil {
			m.Headers = upd.Headers
		}
		if upd.Enabled != nil {
			m.Enabled = *upd.Enabled
		}
		if upd.RequireApproval != nil {
			m.RequireApproval = *upd.RequireApproval
		}
		if upd.TimeoutSeconds != nil {
			m.TimeoutSeconds = *upd.TimeoutSeconds
		}
		if upd.ToolCount != nil {
			m.ToolCount = *upd.ToolCount
		}
		if upd.LastHealthyAt != nil {
			t := upd.LastHealthyAt.UTC()
			m.LastHealthyAt = &t
		}
		if upd.LastError != nil {
			m.LastError = *upd.LastError
		}
		if err := validateMcpServer(m.Name, m.Transport, m.Command, m.URL); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		m.UpdatedAt = &now
		*m = normalizeMcpServer(*m)
		if err := s.save(); err != nil {
			return nil, err
		}
		returned := *m
		return &returned, nil
	}
	return nil, ErrNotFound
}

// DeleteMcpServer implements Repository.DeleteMcpServer.
func (s *FileStore) DeleteMcpServer(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.data.McpServers {
		if s.data.McpServers[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	s.data.McpServers = append(s.data.McpServers[:idx], s.data.McpServers[idx+1:]...)
	return s.save()
}

// CreateSkill implements Repository.CreateSkill.
func (s *FileStore) CreateSkill(_ context.Context, in SkillInput) (*Skill, error) {
	// Normalize before validating: "Testing-OAuth-…" is the same skill as its
	// lowercase form, so the canonical name is what must pass the kebab gate.
	name := strings.ToLower(strings.TrimSpace(in.Name))
	if err := validateSkill(name, in.Description, in.Body); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.data.Skills {
		if k.Name == name {
			return nil, fmt.Errorf("%w: skill name %q already exists", ErrConflict, name)
		}
	}
	now := time.Now().UTC()
	skill := normalizeSkill(Skill{
		ID:          uuid.NewString(),
		Name:        name,
		Category:    SkillCategoryUser,
		Description: strings.TrimSpace(in.Description),
		Tags:        in.Tags,
		Intent:      strings.TrimSpace(in.Intent),
		Body:        in.Body,
		Enabled:     in.Enabled,
		CreatedBy:   in.CreatedBy,
		CreatedAt:   now,
	})
	s.data.Skills = append(s.data.Skills, skill)
	if err := s.save(); err != nil {
		return nil, err
	}
	returned := skill
	return &returned, nil
}

// ListSkills implements Repository.ListSkills (newest first).
func (s *FileStore) ListSkills(_ context.Context) ([]Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Skill(nil), s.data.Skills...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	for i := range out {
		out[i] = normalizeSkill(out[i])
	}
	return out, nil
}

// GetSkill implements Repository.GetSkill.
func (s *FileStore) GetSkill(_ context.Context, id string) (*Skill, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, k := range s.data.Skills {
		if k.ID == id {
			returned := normalizeSkill(k)
			return &returned, nil
		}
	}
	return nil, ErrNotFound
}

// UpdateSkill implements Repository.UpdateSkill.
func (s *FileStore) UpdateSkill(_ context.Context, id string, upd SkillUpdate) (*Skill, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Skills {
		k := &s.data.Skills[i]
		if k.ID != id {
			continue
		}
		if upd.Name != nil {
			name := strings.ToLower(strings.TrimSpace(*upd.Name))
			for j := range s.data.Skills {
				if j != i && s.data.Skills[j].Name == name {
					return nil, fmt.Errorf("%w: skill name %q already exists", ErrConflict, name)
				}
			}
			k.Name = name
		}
		if upd.Description != nil {
			k.Description = strings.TrimSpace(*upd.Description)
		}
		if upd.Tags != nil {
			k.Tags = upd.Tags
		}
		if upd.Intent != nil {
			k.Intent = strings.TrimSpace(*upd.Intent)
		}
		if upd.Body != nil {
			k.Body = *upd.Body
		}
		if upd.Enabled != nil {
			k.Enabled = *upd.Enabled
		}
		if err := validateSkill(k.Name, k.Description, k.Body); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		k.UpdatedAt = &now
		*k = normalizeSkill(*k)
		if err := s.save(); err != nil {
			return nil, err
		}
		returned := *k
		return &returned, nil
	}
	return nil, ErrNotFound
}

// DeleteSkill implements Repository.DeleteSkill.
func (s *FileStore) DeleteSkill(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.data.Skills {
		if s.data.Skills[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	s.data.Skills = append(s.data.Skills[:idx], s.data.Skills[idx+1:]...)
	return s.save()
}

// CreateAgentProfile implements Repository.CreateAgentProfile.
func (s *FileStore) CreateAgentProfile(_ context.Context, in AgentProfileInput) (*AgentProfile, error) {
	if err := validateAgentProfile(in.Role, in.Focus, in.EvidenceContract, in.StoppingRule); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	role := strings.TrimSpace(in.Role)
	for _, p := range s.data.AgentProfiles {
		if p.Role == role {
			return nil, fmt.Errorf("%w: agent role %q already exists", ErrConflict, role)
		}
	}
	now := time.Now().UTC()
	profile := normalizeAgentProfile(AgentProfile{
		ID:               uuid.NewString(),
		Role:             role,
		Focus:            strings.TrimSpace(in.Focus),
		VulnClasses:      in.VulnClasses,
		EvidenceContract: strings.TrimSpace(in.EvidenceContract),
		StoppingRule:     strings.TrimSpace(in.StoppingRule),
		ToolScopeHidden:  in.ToolScopeHidden,
		Enabled:          in.Enabled,
		CreatedBy:        in.CreatedBy,
		CreatedAt:        now,
	})
	s.data.AgentProfiles = append(s.data.AgentProfiles, profile)
	if err := s.save(); err != nil {
		return nil, err
	}
	returned := profile
	return &returned, nil
}

// ListAgentProfiles implements Repository.ListAgentProfiles (newest first).
func (s *FileStore) ListAgentProfiles(_ context.Context) ([]AgentProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]AgentProfile(nil), s.data.AgentProfiles...)
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	for i := range out {
		out[i] = normalizeAgentProfile(out[i])
	}
	return out, nil
}

// GetAgentProfile implements Repository.GetAgentProfile.
func (s *FileStore) GetAgentProfile(_ context.Context, id string) (*AgentProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.data.AgentProfiles {
		if p.ID == id {
			returned := normalizeAgentProfile(p)
			return &returned, nil
		}
	}
	return nil, ErrNotFound
}

// UpdateAgentProfile implements Repository.UpdateAgentProfile.
func (s *FileStore) UpdateAgentProfile(_ context.Context, id string, upd AgentProfileUpdate) (*AgentProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.AgentProfiles {
		p := &s.data.AgentProfiles[i]
		if p.ID != id {
			continue
		}
		if upd.Role != nil {
			role := strings.TrimSpace(*upd.Role)
			for j := range s.data.AgentProfiles {
				if j != i && s.data.AgentProfiles[j].Role == role {
					return nil, fmt.Errorf("%w: agent role %q already exists", ErrConflict, role)
				}
			}
			p.Role = role
		}
		if upd.Focus != nil {
			p.Focus = strings.TrimSpace(*upd.Focus)
		}
		if upd.VulnClasses != nil {
			p.VulnClasses = upd.VulnClasses
		}
		if upd.EvidenceContract != nil {
			p.EvidenceContract = strings.TrimSpace(*upd.EvidenceContract)
		}
		if upd.StoppingRule != nil {
			p.StoppingRule = strings.TrimSpace(*upd.StoppingRule)
		}
		if upd.ToolScopeHidden != nil {
			p.ToolScopeHidden = upd.ToolScopeHidden
		}
		if upd.Enabled != nil {
			p.Enabled = *upd.Enabled
		}
		if err := validateAgentProfile(p.Role, p.Focus, p.EvidenceContract, p.StoppingRule); err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		p.UpdatedAt = &now
		*p = normalizeAgentProfile(*p)
		if err := s.save(); err != nil {
			return nil, err
		}
		returned := *p
		return &returned, nil
	}
	return nil, ErrNotFound
}

// DeleteAgentProfile implements Repository.DeleteAgentProfile.
func (s *FileStore) DeleteAgentProfile(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.data.AgentProfiles {
		if s.data.AgentProfiles[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrNotFound
	}
	s.data.AgentProfiles = append(s.data.AgentProfiles[:idx], s.data.AgentProfiles[idx+1:]...)
	return s.save()
}
