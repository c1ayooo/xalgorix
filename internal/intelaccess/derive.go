// derive.go — the deterministic projection of one intelligence item into a
// reachability row. Both the conclusion hook (all items of a scan) and the
// operator triage endpoint (one item, before any conclusion) call this, so a
// row derived from either path is byte-identical.

package intelaccess

import (
	"fmt"
	"strings"

	"github.com/c1ayooo/xalgorix/v4/internal/tools/intelligence"
)

// Derived is the projection of one intel item: identity, kind, subject and
// canonical targets. No content is carried here (invariant 6).
type Derived struct {
	Fingerprint string
	Kind        string
	Subject     string
	Targets     []string
}

// DeriveItem projects one item. ok=false means the item carries no
// authorization object: sensitive_data is a disclosure rather than a door, and
// an unknown kind is left out instead of guessed.
func DeriveItem(item intelligence.Item) (Derived, bool) {
	fp := strings.TrimSpace(item.Fingerprint)
	if fp == "" {
		return Derived{}, false
	}
	var kind string
	switch strings.ToLower(strings.TrimSpace(item.Kind)) {
	case intelligence.KindCredential:
		kind = KindCredential
	case intelligence.KindLead:
		kind = KindPath
	default:
		return Derived{}, false
	}
	d := Derived{Fingerprint: fp, Kind: kind}
	if target, ok := NormalizeTarget(item.Endpoint); ok {
		d.Targets = []string{target}
		d.Subject = hostOf(target)
	}
	return d, true
}

// deriveAll projects every item, keeping deterministic order.
func deriveAll(items []intelligence.Item) []Derived {
	out := make([]Derived, 0, len(items))
	for _, item := range items {
		if d, ok := DeriveItem(item); ok {
			out = append(out, d)
		}
	}
	return out
}

// normalizeAll canonicalizes operator-supplied grants, rejecting the whole set
// when any entry does not resolve (an operator typo must not silently become a
// narrower or wider grant).
func normalizeAll(targets []string) ([]string, error) {
	out := make([]string, 0, len(targets))
	seen := make(map[string]bool, len(targets))
	for _, raw := range targets {
		target, ok := NormalizeTarget(raw)
		if !ok {
			return nil, &InvalidTargetError{Raw: raw}
		}
		if seen[target] {
			continue
		}
		seen[target] = true
		out = append(out, target)
	}
	return out, nil
}

// InvalidTargetError reports one grant that could not be canonicalized.
type InvalidTargetError struct {
	Raw string
}

func (e *InvalidTargetError) Error() string {
	return fmt.Sprintf("invalid target %q (expected host:port or CIDR)", e.Raw)
}
