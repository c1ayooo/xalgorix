package trafficproxy

import (
	"net"
	"testing"
)

func TestProxyURLForListener(t *testing.T) {
	loopback := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8080}
	unspecified := &net.TCPAddr{IP: net.IPv4zero, Port: 8080}
	cases := []struct {
		name      string
		addr      net.Addr
		advertise string
		want      string
	}{
		{"loopback default", loopback, "", "http://127.0.0.1:8080"},
		{"unspecified without advertise stays dialable", unspecified, "", "http://127.0.0.1:8080"},
		{"advertise host only inherits the port", unspecified, "host.docker.internal", "http://host.docker.internal:8080"},
		{"advertise host:port wins", unspecified, "host.docker.internal:9090", "http://host.docker.internal:9090"},
		{"advertise URL is accepted", unspecified, "http://proxy.example:8081", "http://proxy.example:8081"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := proxyURLForListener(tc.addr, tc.advertise); got != tc.want {
				t.Fatalf("proxyURLForListener(%v, %q) = %q, want %q", tc.addr, tc.advertise, got, tc.want)
			}
		})
	}
}
