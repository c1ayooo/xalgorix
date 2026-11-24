package skills

// catalog_test.go pins the read-only console projection of the embedded
// corpus: the index is sorted, carries frontmatter tags, and ReadCatalogSkill
// returns a body with the frontmatter stripped.

import (
	"strings"
	"testing"
)

func TestCatalogIsSortedAndNonEmpty(t *testing.T) {
	entries := Catalog()
	if len(entries) < 100 {
		t.Fatalf("catalog has %d entries, want the embedded corpus (100+)", len(entries))
	}
	for i := 1; i < len(entries); i++ {
		prev, cur := entries[i-1], entries[i]
		if prev.Category > cur.Category ||
			(prev.Category == cur.Category && prev.Name >= cur.Name) {
			t.Fatalf("catalog not sorted at %d: %+v then %+v", i, prev, cur)
		}
	}
	for _, e := range entries {
		if e.Name == "" || e.Category == "" {
			t.Fatalf("entry missing identity: %+v", e)
		}
	}
}

func TestCatalogEntriesCarryTagsAndDescription(t *testing.T) {
	entries := Catalog()
	var withTags int
	for _, e := range entries {
		if e.Description == "" {
			t.Fatalf("entry %s/%s has no description", e.Category, e.Name)
		}
		if len(e.Tags) > 0 {
			withTags++
		}
	}
	if withTags == 0 {
		t.Fatal("no catalog entry exposed tags; frontmatter tag parsing regressed")
	}
}

func TestCatalogIsACopy(t *testing.T) {
	first := Catalog()
	if len(first) == 0 {
		t.Fatal("empty catalog")
	}
	first[0].Name = "mutated"
	if Catalog()[0].Name == "mutated" {
		t.Fatal("Catalog() returned the cached slice instead of a copy")
	}
}

func TestReadCatalogSkillStripsFrontmatter(t *testing.T) {
	var target CatalogEntry
	for _, e := range Catalog() {
		if len(e.Tags) > 0 {
			target = e
			break
		}
	}
	skill, ok := ReadCatalogSkill(target.Name, target.Category)
	if !ok {
		t.Fatalf("ReadCatalogSkill(%q, %q) not found", target.Name, target.Category)
	}
	if strings.HasPrefix(strings.TrimSpace(skill.Body), "---") {
		t.Fatalf("body still starts with frontmatter: %q", firstLine(skill.Body))
	}
	if strings.Contains(skill.Body, "name: "+target.Name) {
		t.Fatalf("frontmatter leaked into body for %s", target.Name)
	}
	if len(skill.Tags) != len(target.Tags) {
		t.Fatalf("detail tags %v != list tags %v", skill.Tags, target.Tags)
	}
	if _, ok := ReadCatalogSkill("no-such-skill-name", ""); ok {
		t.Fatal("unknown skill resolved")
	}
	if _, ok := ReadCatalogSkill("../etc/passwd", ""); ok {
		t.Fatal("path traversal name resolved")
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
