package web

// integration_inputs.go — scan-launch assembly of the operator-authored
// integration inputs (user skills / agent profiles / MCP servers) from the
// platform store into agent options.
//
// Posture (charter §6.6): best-effort. A nil store, a store error, or a
// deadline miss degrades to "no operator content for this scan" — scanning
// itself is never blocked or slowed beyond the bounded deadline.

import (
	"context"
	"log"
	"time"

	"github.com/xalgord/xalgorix/v4/internal/agent"
	"github.com/xalgord/xalgorix/v4/internal/scopeguard"
	mcptool "github.com/xalgord/xalgorix/v4/internal/tools/mcp"
	skillstool "github.com/xalgord/xalgorix/v4/internal/tools/skills"
)

// integrationInputTimeout bounds each platform-store read on the scan-launch
// path (approvalStoreCallTimeout posture).
var integrationInputTimeout = 3 * time.Second

// scanUserSkills fetches the enabled operator-authored skills for a scan.
// Disabled skills stay manageable in the UI but never reach the engine.
func (s *Server) scanUserSkills() []skillstool.UserSkill {
	store := s.currentPlatformStore()
	if store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), integrationInputTimeout)
	defer cancel()
	skills, err := store.ListSkills(ctx)
	if err != nil {
		log.Printf("[integrations] user skills unavailable for scan: %v", err)
		return nil
	}
	out := make([]skillstool.UserSkill, 0, len(skills))
	for _, k := range skills {
		if !k.Enabled {
			continue
		}
		out = append(out, skillstool.UserSkill{
			Name:        k.Name,
			Description: k.Description,
			Tags:        k.Tags,
			Intent:      k.Intent,
			Body:        k.Body,
		})
	}
	return out
}

// scanAgentProfiles fetches the enabled operator-authored delegation
// profiles for a scan (merged behind the builtin specialists by the agent).
func (s *Server) scanAgentProfiles() []agent.SpecialistProfile {
	store := s.currentPlatformStore()
	if store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), integrationInputTimeout)
	defer cancel()
	profiles, err := store.ListAgentProfiles(ctx)
	if err != nil {
		log.Printf("[integrations] agent profiles unavailable for scan: %v", err)
		return nil
	}
	out := make([]agent.SpecialistProfile, 0, len(profiles))
	for _, p := range profiles {
		if !p.Enabled {
			continue
		}
		out = append(out, agent.SpecialistProfile{
			Role:             p.Role,
			Focus:            p.Focus,
			VulnClasses:      p.VulnClasses,
			EvidenceContract: p.EvidenceContract,
			StoppingRule:     p.StoppingRule,
			ToolScopeHidden:  p.ToolScopeHidden,
		})
	}
	return out
}

// scanMcpServers fetches the enabled MCP server specs for a scan. The scope
// config matches the scan's own guard config so an HTTP endpoint that points
// at the operator machine is refused at connect time (defense in depth: the
// CRUD handler already rejects it at write time).
func (s *Server) scanMcpServers() []mcptool.ServerSpec {
	store := s.currentPlatformStore()
	if store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), integrationInputTimeout)
	defer cancel()
	servers, err := store.ListMcpServers(ctx)
	if err != nil {
		log.Printf("[integrations] mcp servers unavailable for scan: %v", err)
		return nil
	}
	cfg := s.configSnapshot()
	scope := scopeguard.Config{AllowLocalTargets: cfg != nil && cfg.AllowLocalTargets}
	if cfg != nil {
		scope.BindAddr = cfg.BindAddr
		scope.Port = s.port
	}
	out := make([]mcptool.ServerSpec, 0, len(servers))
	for _, m := range servers {
		if !m.Enabled {
			continue
		}
		out = append(out, mcptool.ServerSpec{
			Name:           m.Name,
			Transport:      m.Transport,
			Command:        m.Command,
			Args:           m.Args,
			Env:            m.Env,
			URL:            m.URL,
			Headers:        m.Headers,
			TimeoutSeconds: m.TimeoutSeconds,
			Scope:          scope,
		})
	}
	return out
}
