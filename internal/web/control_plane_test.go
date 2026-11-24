package web

import "testing"

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
