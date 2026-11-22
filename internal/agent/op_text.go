package agent

import (
	"fmt"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

// opText returns the operator-facing variant of a fixed engine broadcast in
// the configured output language. This localizes the OUTPUT half only: system
// prompts and LLM-facing directives (re-test nudges, verification windows)
// intentionally stay English — translating those alters loop behavior. Fixed
// status broadcasts the operator reads in the transcript/event stream
// localize per config.Language (default zh-CN, see config.DefaultLanguage).
func opText(lang, en, zh string) string {
	if config.NormalizeLanguage(lang) == "zh-CN" {
		return zh
	}
	return en
}

// op is the receiver-bound form of opText (reads the agent's configured
// language; a nil cfg means the default output language).
func (a *Agent) op(en, zh string) string {
	lang := ""
	if a.cfg != nil {
		lang = a.cfg.Language
	}
	return opText(lang, en, zh)
}

// opf is the formatted form of op: the chosen template is Sprintf'd with args.
func (a *Agent) opf(en, zh string, args ...any) string {
	lang := ""
	if a.cfg != nil {
		lang = a.cfg.Language
	}
	return fmt.Sprintf(opText(lang, en, zh), args...)
}
