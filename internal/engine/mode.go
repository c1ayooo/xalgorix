package engine

import (
	"fmt"
	"strings"
)

// Mode selects the scan-loop implementation. Legacy remains the only wired
// production path in this milestone; Eino is a compiled-but-unused skeleton
// until the R4 parity gate passes.
type Mode string

const (
	ModeLegacy Mode = "legacy"
	ModeEino   Mode = "eino"
)

// ParseMode accepts the REDESIGN §6 switch values. An empty value preserves
// the current behavior (legacy); unknown values fail instead of silently
// falling back, so a typo cannot change engines unnoticed.
func ParseMode(raw string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ModeLegacy):
		return ModeLegacy, nil
	case string(ModeEino):
		return ModeEino, nil
	default:
		return "", fmt.Errorf("engine: unsupported mode %q (want legacy or eino)", raw)
	}
}
