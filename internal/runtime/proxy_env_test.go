package runtime

import (
	"strings"
	"testing"
)

func TestAppendGatewayProxyEnvReplacesHostProxy(t *testing.T) {
	env := []string{
		"HOME=/scan/home",
		"HTTP_PROXY=http://127.0.0.1:54001",
		"https_proxy=http://127.0.0.1:54001",
		"NO_PROXY=*",
		"FOO=bar",
	}
	got := appendGatewayProxyEnv(env, "http://172.30.8.1:8081")
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "127.0.0.1:54001") || strings.Contains(joined, "NO_PROXY=*") {
		t.Fatalf("host proxy environment survived: %v", got)
	}
	for _, want := range []string{
		"HOME=/scan/home",
		"FOO=bar",
		"HTTP_PROXY=http://172.30.8.1:8081",
		"HTTPS_PROXY=http://172.30.8.1:8081",
		"http_proxy=http://172.30.8.1:8081",
		"NO_PROXY=127.0.0.1,localhost,::1",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
}

func TestCapabilitiesAlwaysCarryDACOverride(t *testing.T) {
	cases := []struct {
		profile   string
		net       string
		wantRaw   bool
		wantAdmin bool
	}{
		{ProfileCore, NetProfileProxyOnly, false, false},
		{ProfileFull, NetProfileProxyOnly, true, true},
		{ProfileCore, NetProfileRaw, true, false},
		{ProfileFull, NetProfileRaw, true, true},
	}
	for _, tc := range cases {
		caps := capabilitiesForProfile(tc.profile, tc.net)
		joined := strings.Join(caps, ",")
		for _, want := range []string{"CAP_DAC_OVERRIDE", "CAP_DAC_READ_SEARCH"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("%s/%s missing %s: %v", tc.profile, tc.net, want, caps)
			}
		}
		if strings.Contains(joined, "NET_RAW") != tc.wantRaw {
			t.Fatalf("%s/%s NET_RAW = %v, want %v: %v", tc.profile, tc.net, !tc.wantRaw, tc.wantRaw, caps)
		}
		if strings.Contains(joined, "NET_ADMIN") != tc.wantAdmin {
			t.Fatalf("%s/%s NET_ADMIN = %v, want %v: %v", tc.profile, tc.net, !tc.wantAdmin, tc.wantAdmin, caps)
		}
	}
}
