package chains

import (
	"strings"
	"testing"
)

func TestDisciplineMentionsContract(t *testing.T) {
	d := Discipline()
	for _, want := range []string{"反问纪律", "report_vulnerability", "add_note", "蜜罐"} {
		if !strings.Contains(d, want) {
			t.Fatalf("discipline block missing %q", want)
		}
	}
	if strings.Contains(d, "record_fact") || strings.Contains(d, "report_finding") {
		t.Fatal("discipline block still names 4zreco-only tools")
	}
}

func TestSkeletonRoutesCanonicalClasses(t *testing.T) {
	cases := []struct {
		class string
		want  string
	}{
		{"sqli", "注入类"},
		{"nosqli", "注入类"},
		{"ssti", "注入类"},
		{"xss", "XSS"},
		{"dom-xss", "XSS"},
		{"rce", "RCE"},
		{"deserialization", "RCE"},
		{"path_traversal", "文件操作"},
		{"ssrf", "SSRF"},
		{"xxe", "XXE"},
		{"idor", "认证/越权"},
		{"auth-bypass", "认证/越权"},
		{"file-upload", "文件上传"},
		{"business-logic", "业务逻辑"},
		{"race-conditions", "业务逻辑"},
		{"parameter_mining", "信息收集"},
	}
	for _, c := range cases {
		got := Skeleton(c.class)
		if got == "" {
			t.Fatalf("class %q returned no skeleton", c.class)
		}
		if !strings.Contains(got, c.want) {
			t.Fatalf("class %q routed to wrong skeleton (want marker %q):\n%s", c.class, c.want, got)
		}
		if strings.Contains(got, "record_fact") || strings.Contains(got, "scan_with_nuclei") {
			t.Fatalf("class %q skeleton names a 4zreco-only tool", c.class)
		}
	}
}

func TestSkeletonUnknownClassReturnsEmpty(t *testing.T) {
	if got := Skeleton("some-unknown-class"); got != "" {
		t.Fatalf("unknown class should return empty so callers fall back, got:\n%s", got)
	}
	if got := Skeleton(""); got != "" {
		t.Fatalf("empty class should return empty, got:\n%s", got)
	}
}

func TestSkeletonForIntentFallsBackToKeywords(t *testing.T) {
	cases := map[string]string{
		"测试一下这个接口的 SQL 注入": "注入",
		"试试命令执行":           "RCE",
		"越权访问其他用户订单":       "认证/越权",
		"枚举目录和备份文件":        "信息收集",
		"给这个登录框测一下跨站脚本":    "XSS",
	}
	for intent, want := range cases {
		got := SkeletonForIntent(intent)
		if !strings.Contains(got, want) {
			t.Fatalf("intent %q routed to wrong skeleton (want marker %q):\n%s", intent, want, got)
		}
	}
	if got := SkeletonForIntent("随便看看"); !strings.Contains(got, "通用") {
		t.Fatalf("unmatched intent should fall back to the generic chain, got:\n%s", got)
	}
}
