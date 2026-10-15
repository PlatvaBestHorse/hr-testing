package httpapi

import (
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestClientIPTrustBoundary(t *testing.T) {
	app := &App{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.30.254.10/32"), netip.MustParsePrefix("::1/128")}}
	for _, tc := range []struct {
		name, peer string
		forwarded  []string
		want       string
	}{
		{"trusted single hop", "172.30.254.10:1234", []string{"198.51.100.9"}, "198.51.100.9"},
		{"direct spoof ignored", "198.51.100.8:1234", []string{"198.51.100.9"}, "198.51.100.8"},
		{"other container ignored", "172.30.254.11:1234", []string{"198.51.100.9"}, "172.30.254.11"},
		{"forwarding chain rejected", "172.30.254.10:1234", []string{"198.51.100.9, 198.51.100.8"}, "172.30.254.10"},
		{"duplicate headers rejected", "172.30.254.10:1234", []string{"198.51.100.9", "198.51.100.8"}, "172.30.254.10"},
		{"port rejected", "172.30.254.10:1234", []string{"198.51.100.9:2345"}, "172.30.254.10"},
		{"ipv6 canonical", "[::1]:1234", []string{"2001:db8:0:0::1"}, "2001:db8::1"},
		{"mapped ipv4 canonical", "[::ffff:172.30.254.10]:1234", []string{"::ffff:198.51.100.9"}, "198.51.100.9"},
		{"zone rejected", "[::1]:1234", []string{"fe80::1%eth0"}, "::1"},
		{"missing header", "172.30.254.10:1234", nil, "172.30.254.10"},
		{"invalid peer ignores header", "invalid", []string{"198.51.100.9"}, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.peer
			for _, v := range tc.forwarded {
				r.Header.Add("X-Forwarded-For", v)
			}
			r.Header.Set("X-Real-IP", "203.0.113.88")
			r.Header.Set("Forwarded", "for=203.0.113.88")
			if got := app.clientIP(r); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.30.254.10:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.9")
	if (&App{}).clientIP(r) != "172.30.254.10" {
		t.Fatal("forwarded headers trusted by default")
	}
}
