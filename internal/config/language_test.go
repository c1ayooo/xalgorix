package config

import (
	"strings"
	"testing"
)

func TestNormalizeLanguage(t *testing.T) {
	cases := map[string]string{
		"":                   "zh-CN", // default output language
		"en":                 "en",
		"EN":                 "en",
		"en-US":              "en",
		"english":            "en",
		"zh":                 "zh-CN",
		"zh-CN":              "zh-CN",
		"ZH-CN":              "zh-CN",
		"zh_cn":              "zh-CN",
		"zh-Hans":            "zh-CN",
		"Simplified Chinese": "zh-CN",
		"fr":                 "zh-CN", // unsupported falls back to the default
	}
	for in, want := range cases {
		if got := NormalizeLanguage(in); got != want {
			t.Errorf("NormalizeLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOutputLanguageDirective(t *testing.T) {
	if d := OutputLanguageDirective("en"); d != "" {
		t.Errorf("English directive should be empty, got %q", d)
	}
	// 默认输出语言也必须带指令：模型的自身默认是英文，不指令就会答英文。
	d := OutputLanguageDirective("")
	if d == "" {
		t.Fatal("default (zh-CN) directive should be non-empty")
	}
	if !strings.Contains(d, "简体中文") {
		t.Errorf("default directive should name the language, got %q", d)
	}
	d = OutputLanguageDirective("zh-CN")
	if d == "" {
		t.Fatal("zh-CN directive should be non-empty")
	}
	if !strings.Contains(d, "简体中文") {
		t.Errorf("zh-CN directive should name the language, got %q", d)
	}
	// Unsupported codes fall back to the default language (zh-CN, with directive).
	if d := OutputLanguageDirective("fr"); d == "" {
		t.Error("unsupported language falls back to the zh-CN default, which carries a directive")
	}
}

func TestLanguageDisplayName(t *testing.T) {
	if got := LanguageDisplayName("en"); got != "English" {
		t.Errorf("LanguageDisplayName(en) = %q", got)
	}
	if got := LanguageDisplayName("zh-CN"); !strings.Contains(got, "Chinese") {
		t.Errorf("LanguageDisplayName(zh-CN) = %q", got)
	}
}
