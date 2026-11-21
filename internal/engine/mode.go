package engine

import (
	"fmt"
	"strings"
)

// Mode selects the scan-loop implementation. Eino is the default production
// engine (config.go); legacy is the documented rollback via
// XALGORIX_ENGINE=legacy. ParseMode's empty-value default exists for callers
// that construct an engine Config directly without going through config.go.
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
