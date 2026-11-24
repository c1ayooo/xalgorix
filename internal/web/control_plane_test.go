package web

import (
	"testing"

	"github.com/c1ayooo/xalgorix/v4/internal/config"
)

func TestAllTargetsControlPlaneHosts(t *testing.T) {
	cp := []string{"tsecbench.zc.tencent.com", "10.0.100.58"}
	cases := []struct {
		targets []string
		want    bool
	}{
		{[]string{"tsecbench.zc.tencent.com"}, true},
		{[]string{"https://tsecbench.zc.tencent.com/openapi/v1/challenges"}, true},
		{[]string{"10.0.100.58"}, true},
		{[]string{"10.0.1.5:8080"}, false},
		{[]string{"tsecbench.zc.tencent.com", "10.0.1.5"}, false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := allTargetsControlPlaneHosts(tc.targets, cp); got != tc.want {
			t.Fatalf("%v -> %v, want %v", tc.targets, got, tc.want)
		}
	}
}

func TestScopeControlPlaneHosts(t *testing.T) {
	cp := []string{"tsecbench.zc.tencent.com", "10.0.100.58"}
	cases := []struct {
		name     string
		declared []string
		want     []string
	}{
		{
			name: "benchmark dispatch names both hosts",
			declared: []string{
				"tsecbench.zc.tencent.com",
				"BENCHMARK_BASE_URL: https://tsecbench.zc.tencent.com\nGET http://10.0.100.58",
			},
			want: cp,
		},
		{
			name:     "unrelated project names neither host",
			declared: []string{"59.110.104.110", "全端口扫描 + Web 枚举"},
			want:     nil,
		},
		{
			name:     "url target and subdomain still name the host",
			declared: []string{"https://api.tsecbench.zc.tencent.com:8443/openapi"},
			want:     []string{"tsecbench.zc.tencent.com"},
		},
		{
			name:     "lookalike IP is not the control-plane IP",
			declared: []string{"110.0.100.58", "10.0.100.580"},
			want:     nil,
		},
		{
			name:     "host as a prefix of a longer host does not match",
			declared: []string{"tsecbench.zc.tencent.com.evil.example"},
			want:     nil,
		},
		{
			name:     "no declared text",
			declared: nil,
			want:     nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scopeControlPlaneHosts(cp, tc.declared...)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
	if got := scopeControlPlaneHosts(nil, "tsecbench.zc.tencent.com"); got != nil {
		t.Fatalf("empty global list must stay empty, got %v", got)
	}
}

func TestScopedScanConfigDoesNotMutateInput(t *testing.T) {
	cfg := &config.Config{ControlPlaneHosts: []string{"tsecbench.zc.tencent.com", "10.0.100.58"}}
	req := ScanRequest{Targets: []string{"59.110.104.110"}, Instruction: "recon only"}

	scoped := scopedScanConfig(cfg, req)
	if scoped == cfg {
		t.Fatal("expected a copy, got the same config pointer")
	}
	if len(scoped.ControlPlaneHosts) != 0 {
		t.Fatalf("unrelated dispatch kept control-plane hosts: %v", scoped.ControlPlaneHosts)
	}
	if len(cfg.ControlPlaneHosts) != 2 {
		t.Fatalf("input config was mutated: %v", cfg.ControlPlaneHosts)
	}

	named := scopedScanConfig(cfg, ScanRequest{Instruction: "use https://tsecbench.zc.tencent.com and http://10.0.100.58"})
	if len(named.ControlPlaneHosts) != 2 {
		t.Fatalf("benchmark dispatch lost its hosts: %v", named.ControlPlaneHosts)
	}

	if got := scopedScanConfig(nil, req); got != nil {
		t.Fatalf("nil config must stay nil, got %+v", got)
	}
	if got := scopedScanConfig(&config.Config{}, req); got == nil || len(got.ControlPlaneHosts) != 0 {
		t.Fatalf("empty config must pass through, got %+v", got)
	}
}
