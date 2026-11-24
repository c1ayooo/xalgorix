package skills

// catalog.go — read-only projection of the EMBEDDED skill corpus for the
// operator console (docs/API_CONTRACT.md §integration-skills). The engine's
// tool surface (read_skill/list_skills/search_skills) is the authority for
// what a scan can load; this file only exposes the same immutable catalog so
// the UI can list and read it without a scan. Built-in entries are never
// writable — operator-authored skills live in the platform store.

import (
	"io/fs"
	"sort"
	"strings"
	"sync"
)

// CatalogEntry is one built-in skill listing row.
type CatalogEntry struct {
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Intent      string   `json:"intent,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// CatalogSkill is one built-in skill with its full markdown body (frontmatter
// stripped — the structured fields above carry that metadata).
type CatalogSkill struct {
	CatalogEntry
	Body string `json:"body"`
}

var (
	catalogOnce    sync.Once
	catalogEntries []CatalogEntry
)

// Catalog returns the embedded corpus index sorted by category, then name.
// The corpus is immutable, so the index is built once per process.
func Catalog() []CatalogEntry {
	catalogOnce.Do(func() {
		catalogEntries = buildCatalog(embeddedSkillsDataFS)
	})
	out := make([]CatalogEntry, len(catalogEntries))
	for i, e := range catalogEntries {
		// Copy the tags slice so callers cannot mutate the cache.
		if e.Tags != nil {
			e.Tags = append([]string(nil), e.Tags...)
		}
		out[i] = e
	}
	return out
}

func buildCatalog(fsys fs.FS) []CatalogEntry {
	idx := buildSkillIndex(fsys)
	out := make([]CatalogEntry, 0, len(idx))
	for _, m := range idx {
		out = append(out, CatalogEntry{
			Name:        m.name,
			Category:    m.category,
			Description: m.description,
			Intent:      m.intent,
			Tags:        m.tags,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Category != out[j].Category {
			return out[i].Category < out[j].Category
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ReadCatalogSkill returns one built-in skill by exact name, optionally
// narrowed to a category. Unknown names return ok=false (no fuzzy resolution:
// this is a viewer, not the agent's lookup path).
func ReadCatalogSkill(name, category string) (CatalogSkill, bool) {
	name = sanitizeSlug(strings.TrimSpace(name))
	category = sanitizeSlug(strings.TrimSpace(category))
	if name == "" {
		return CatalogSkill{}, false
	}
	for _, e := range Catalog() {
		if e.Name != name || (category != "" && e.Category != category) {
			continue
		}
		data, err := fs.ReadFile(embeddedSkillsDataFS, e.Category+"/"+e.Name+"/SKILL.md")
		if err != nil {
			return CatalogSkill{}, false
		}
		return CatalogSkill{CatalogEntry: e, Body: stripSkillFrontmatter(string(data))}, true
	}
	return CatalogSkill{}, false
}

// stripSkillFrontmatter removes a leading YAML frontmatter block (and the
// blank line after it) so the rendered body starts at the first heading.
func stripSkillFrontmatter(content string) string {
	if !strings.HasPrefix(content, "---") {
		return content
	}
	rest := content[3:]
	if !strings.HasPrefix(rest, "\n") && !strings.HasPrefix(rest, "\r\n") {
		return content
	}
	lines := strings.Split(rest, "\n")
	for i, raw := range lines {
		if strings.TrimSpace(raw) == "---" {
			body := strings.Join(lines[i+1:], "\n")
			return strings.TrimLeft(body, "\r\n")
		}
	}
	return content
}
