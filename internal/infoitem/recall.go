package infoitem

// recall.go — deterministic lexical recall for prior work.
//
// This is the concrete "similarity consumer" the charter asks for before any
// pgvector work: substring/token overlap with a stable ordering, no model, no
// embeddings, no external dependency. Callers use it to surface a handful of
// L0 index entries (IDs + short summaries); detail is always fetched
// explicitly by ID.

import (
	"sort"
	"strings"
	"unicode"
)

// DefaultRecallLimit bounds one recall response.
const DefaultRecallLimit = 5

// Doc is the L0 document shape recall ranks. Summary must already be bounded.
type Doc struct {
	ID        string
	Kind      string
	Title     string
	Summary   string
	HasDetail bool
}

// Hit is one ranked document.
type Hit struct {
	Doc
	Score float64
}

// Rank scores docs against query and returns the top limit with a deterministic
// (score desc, ID asc) order. Non-positive scores are dropped.
func Rank(query string, docs []Doc, limit int) []Hit {
	if limit <= 0 {
		limit = DefaultRecallLimit
	}
	q := tokenSet(query)
	if len(q) == 0 {
		return nil
	}
	hits := make([]Hit, 0, len(docs))
	for _, doc := range docs {
		score := scoreTokens(q, query, doc.Title, doc.Summary)
		if score <= 0 {
			continue
		}
		hits = append(hits, Hit{Doc: doc, Score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].ID < hits[j].ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	if len(hits) == 0 {
		return nil
	}
	return hits
}

// Score is the single-document form (used by tests and callers that rank one).
func Score(query, title, summary string) float64 {
	q := tokenSet(query)
	if len(q) == 0 {
		return 0
	}
	return scoreTokens(q, query, title, summary)
}

func scoreTokens(q map[string]bool, rawQuery, title, summary string) float64 {
	titleTokens := tokenSet(title)
	summaryTokens := tokenSet(summary)
	titleHits := 0
	for tok := range q {
		if titleTokens[tok] {
			titleHits++
		}
	}
	summaryHits := 0
	for tok := range q {
		if summaryTokens[tok] && !titleTokens[tok] {
			summaryHits++
		}
	}
	score := 2*float64(titleHits)/float64(len(q)) + float64(summaryHits)/float64(len(q))
	trimmed := strings.ToLower(strings.TrimSpace(rawQuery))
	if trimmed != "" && strings.Contains(strings.ToLower(title), trimmed) {
		score += 1
	}
	return score
}

// tokenSet splits text into lowercase tokens. ASCII words stay whole; CJK
// runes become individual tokens so Chinese summaries still match.
func tokenSet(text string) map[string]bool {
	out := map[string]bool{}
	var word strings.Builder
	flush := func() {
		if word.Len() == 0 {
			return
		}
		out[strings.ToLower(word.String())] = true
		word.Reset()
	}
	for _, r := range text {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'):
			word.WriteRune(r)
		case r >= unicode.MaxASCII && unicode.IsLetter(r):
			flush()
			out[strings.ToLower(string(r))] = true
		default:
			flush()
		}
	}
	flush()
	return out
}
