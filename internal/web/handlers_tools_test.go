package web

// Built-in tool directory surface (GET /api/tools) + its invocation records.
// The catalog is a static read-only projection, so the contract under test is
// the envelope, the filter semantics, the method guard and name uniqueness.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestToolCatalogHandler(t *testing.T) {
	s := &Server{}

	rec := httptest.NewRecorder()
	s.handleToolCatalog(rec, httptest.NewRequest(http.MethodGet, "/api/tools", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	var body toolCatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tools) == 0 || len(body.Categories) == 0 {
		t.Fatalf("empty catalog: %d tools / %d categories", len(body.Tools), len(body.Categories))
	}
	seen := make(map[string]bool, len(body.Tools))
	foundTerminal := false
	for _, entry := range body.Tools {
		if entry.Name == "" || entry.Category == "" || entry.Description == "" {
			t.Fatalf("incomplete catalog entry: %+v", entry)
		}
		if seen[entry.Name] {
			t.Fatalf("duplicate tool name %q", entry.Name)
		}
		seen[entry.Name] = true
		if entry.Name == "terminal_execute" {
			foundTerminal = true
			if entry.Approval != "command" {
				t.Fatalf("terminal_execute approval = %q, want command", entry.Approval)
			}
		}
	}
	if !foundTerminal {
		t.Fatal("catalog missing terminal_execute")
	}
}

func TestToolCatalogFilters(t *testing.T) {
	s := &Server{}

	rec := httptest.NewRecorder()
	s.handleToolCatalog(rec, httptest.NewRequest(http.MethodGet, "/api/tools?category=ledger", nil))
	var body toolCatalogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tools) == 0 {
		t.Fatal("category=ledger returned no tools")
	}
	for _, entry := range body.Tools {
		if entry.Category != "ledger" {
			t.Fatalf("category filter leaked %q", entry.Name)
		}
	}

	rec = httptest.NewRecorder()
	s.handleToolCatalog(rec, httptest.NewRequest(http.MethodGet, "/api/tools?q=SQLI", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tools) != 1 || body.Tools[0].Name != "verify_sqli" {
		t.Fatalf("q=SQLI = %+v", body.Tools)
	}

	rec = httptest.NewRecorder()
	s.handleToolCatalog(rec, httptest.NewRequest(http.MethodGet, "/api/tools?q=no-such-tool-xyz", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Tools) != 0 {
		t.Fatalf("unmatched query returned %d tools", len(body.Tools))
	}

	rec = httptest.NewRecorder()
	s.handleToolCatalog(rec, httptest.NewRequest(http.MethodPost, "/api/tools", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d, want 405", rec.Code)
	}
}

func TestToolInvocationsEmptyIsHonest(t *testing.T) {
	withInvocationTestJournals(t)
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleToolInvocations(rec, httptest.NewRequest(http.MethodGet, "/api/tools/invocations", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"total":0`) {
		t.Fatalf("empty tools module = %d %s", rec.Code, rec.Body.String())
	}
}
