package engine

import (
	"testing"

	"github.com/xalgord/xalgorix/v4/internal/tools"
)

func TestParseModeDefaultsToLegacyAndRejectsTypos(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want Mode
	}{
		{"", ModeLegacy},
		{" legacy ", ModeLegacy},
		{" EINO ", ModeEino},
	} {
		got, err := ParseMode(tc.raw)
		if err != nil || got != tc.want {
			t.Fatalf("ParseMode(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
		}
	}
	if _, err := ParseMode("agentic"); err == nil {
		t.Fatal("unknown mode must fail rather than silently fall back")
	}
}

func TestNewDefaultsToLegacyWithoutDependencies(t *testing.T) {
	eng, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if eng.Mode != ModeLegacy || eng.ChatModel != nil || len(eng.Tools) != 0 {
		t.Fatalf("legacy engine = %+v", eng)
	}
	if _, err := New(Config{Mode: ModeEino}); err == nil {
		t.Fatal("eino without dependencies must fail")
	}
	client := &fakeChatClient{}
	eng, err = New(Config{Mode: ModeEino, Client: client, Registry: tools.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	if eng.Mode != ModeEino || eng.ChatModel == nil {
		t.Fatalf("eino engine = %+v", eng)
	}
}
