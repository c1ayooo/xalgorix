package platform

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newIntegrationsFileStore(t *testing.T) *FileStore {
	t.Helper()
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestFileStoreMcpServerLifecycle pins the MCP server CRUD contract:
// validation, name uniqueness, PATCH semantics (nil = unchanged), refresh
// projections, snapshot survival, delete.
func TestFileStoreMcpServerLifecycle(t *testing.T) {
	store := newIntegrationsFileStore(t)
	ctx := context.Background()

	server, err := store.CreateMcpServer(ctx, McpServerInput{
		Name:            "recon-tools",
		Transport:       McpTransportStdio,
		Command:         "npx",
		Args:            []string{"-y", "@example/recon-mcp"},
		Env:             map[string]string{"API_TOKEN": "sekret"},
		Enabled:         true,
		RequireApproval: true,
		CreatedBy:       "operator:admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if server.TimeoutSeconds != DefaultMcpTimeoutSeconds {
		t.Fatalf("default timeout = %d, want %d", server.TimeoutSeconds, DefaultMcpTimeoutSeconds)
	}
	if server.ID == "" || server.CreatedAt.IsZero() {
		t.Fatalf("identity/timestamps not persisted: %+v", server)
	}

	// Duplicate name is ErrConflict.
	if _, err := store.CreateMcpServer(ctx, McpServerInput{
		Name: "recon-tools", Transport: McpTransportStdio, Command: "x",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name = %v, want ErrConflict", err)
	}

	// PATCH semantics: nil fields unchanged, empty map is an explicit clear.
	enabled := false
	toolCount := 7
	healthy := time.Now().UTC()
	lastErr := ""
	updated, err := store.UpdateMcpServer(ctx, server.ID, McpServerUpdate{
		Enabled:       &enabled,
		ToolCount:     &toolCount,
		LastHealthyAt: &healthy,
		LastError:     &lastErr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || updated.ToolCount != 7 || updated.LastHealthyAt == nil {
		t.Fatalf("refresh projection not stamped: %+v", updated)
	}
	if updated.Command != "npx" || len(updated.Args) != 2 || updated.Env["API_TOKEN"] != "sekret" {
		t.Fatalf("PATCH clobbered unchanged fields: %+v", updated)
	}
	if updated.UpdatedAt == nil {
		t.Fatalf("updated_at not stamped: %+v", updated)
	}

	// Validation gate: transport/command/url coherence.
	bad := []McpServerInput{
		{Name: "", Transport: McpTransportStdio, Command: "x"},
		{Name: "no-command", Transport: McpTransportStdio},
		{Name: "no-url", Transport: McpTransportHTTP},
		{Name: "bad-transport", Transport: "carrier-pigeon", Command: "x"},
	}
	for i, in := range bad {
		if _, err := store.CreateMcpServer(ctx, in); !errors.Is(err, ErrValidation) {
			t.Errorf("case %d: err = %v, want ErrValidation", i, err)
		}
	}

	// Snapshot round-trip.
	dir := store.path[:len(store.path)-len("/_platform/platform.json")]
	reopened, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	survived, err := reopened.GetMcpServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if survived.Name != "recon-tools" || survived.Enabled || survived.ToolCount != 7 ||
		survived.Env["API_TOKEN"] != "sekret" {
		t.Fatalf("snapshot round-trip lost server state: %+v", survived)
	}

	// Delete: second delete is ErrNotFound.
	if err := reopened.DeleteMcpServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DeleteMcpServer(ctx, server.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete = %v, want ErrNotFound", err)
	}
}

// TestFileStoreSkillLifecycle pins the user-skill CRUD contract.
func TestFileStoreSkillLifecycle(t *testing.T) {
	store := newIntegrationsFileStore(t)
	ctx := context.Background()

	skill, err := store.CreateSkill(ctx, SkillInput{
		Name:        "testing-oauth-token-theft",
		Description: "Prove OAuth token theft with a live code-exchange check",
		Tags:        []string{"oauth", "web-application-security"},
		Intent:      "Confirm token exfiltration, not mere reflection",
		Body:        "# Testing OAuth Token Theft\n\n1. Steal…",
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if skill.Category != SkillCategoryUser {
		t.Fatalf("category = %q, want %q", skill.Category, SkillCategoryUser)
	}

	if _, err := store.CreateSkill(ctx, SkillInput{
		Name: "Testing-OAuth-Token-Theft", Description: "dup", Body: "x",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("case-insensitive duplicate name = %v, want ErrConflict", err)
	}

	bad := []SkillInput{
		{Name: "Not_Kebab", Description: "d", Body: "b"},
		{Name: "-leading", Description: "d", Body: "b"},
		{Name: "double--hyphen", Description: "d", Body: "b"},
		{Name: "ok-name", Description: "", Body: "b"},
		{Name: "ok-name-2", Description: "d", Body: "  "},
	}
	for i, in := range bad {
		if _, err := store.CreateSkill(ctx, in); !errors.Is(err, ErrValidation) {
			t.Errorf("case %d: err = %v, want ErrValidation", i, err)
		}
	}

	enabled := false
	updated, err := store.UpdateSkill(ctx, skill.ID, SkillUpdate{Enabled: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled {
		t.Fatalf("PATCH enabled = true, want false")
	}

	dir := store.path[:len(store.path)-len("/_platform/platform.json")]
	reopened, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	survived, err := reopened.GetSkill(ctx, skill.ID)
	if err != nil {
		t.Fatal(err)
	}
	if survived.Body == "" || survived.Enabled || len(survived.Tags) != 2 {
		t.Fatalf("snapshot round-trip lost skill state: %+v", survived)
	}
	if err := reopened.DeleteSkill(ctx, skill.ID); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DeleteSkill(ctx, skill.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete = %v, want ErrNotFound", err)
	}
}

// TestFileStoreAgentProfileLifecycle pins the delegation-profile CRUD
// contract (role uniqueness, contract triple validation, snapshot survival).
func TestFileStoreAgentProfileLifecycle(t *testing.T) {
	store := newIntegrationsFileStore(t)
	ctx := context.Background()

	profile, err := store.CreateAgentProfile(ctx, AgentProfileInput{
		Role:             "api-fuzzing",
		Focus:            "REST/GraphQL surface fuzzing",
		VulnClasses:      []string{"api-auth", "mass-assignment"},
		EvidenceContract: "a proven differential or a rejected hypothesis with its control",
		StoppingRule:     "stop when the assigned endpoint × class matrix is exhausted",
		ToolScopeHidden:  []string{"browser_action"},
		Enabled:          true,
		CreatedBy:        "operator:admin",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.CreateAgentProfile(ctx, AgentProfileInput{
		Role: "api-fuzzing", Focus: "dup", EvidenceContract: "e", StoppingRule: "s",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate role = %v, want ErrConflict", err)
	}

	bad := []AgentProfileInput{
		{Role: "Bad_Role", Focus: "f", EvidenceContract: "e", StoppingRule: "s"},
		{Role: "no-focus", EvidenceContract: "e", StoppingRule: "s"},
		{Role: "no-contract", Focus: "f", StoppingRule: "s"},
		{Role: "no-stop", Focus: "f", EvidenceContract: "e"},
	}
	for i, in := range bad {
		if _, err := store.CreateAgentProfile(ctx, in); !errors.Is(err, ErrValidation) {
			t.Errorf("case %d: err = %v, want ErrValidation", i, err)
		}
	}

	enabled := false
	updated, err := store.UpdateAgentProfile(ctx, profile.ID, AgentProfileUpdate{
		Enabled: &enabled, VulnClasses: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Enabled || len(updated.VulnClasses) != 0 {
		t.Fatalf("PATCH not applied: %+v", updated)
	}

	dir := store.path[:len(store.path)-len("/_platform/platform.json")]
	reopened, err := NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	survived, err := reopened.GetAgentProfile(ctx, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if survived.Role != "api-fuzzing" || survived.EvidenceContract == "" || survived.Enabled {
		t.Fatalf("snapshot round-trip lost profile state: %+v", survived)
	}
	if err := reopened.DeleteAgentProfile(ctx, profile.ID); err != nil {
		t.Fatal(err)
	}
	if err := reopened.DeleteAgentProfile(ctx, profile.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double delete = %v, want ErrNotFound", err)
	}
}
