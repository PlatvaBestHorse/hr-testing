package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Nginx overwrites X-Forwarded-For with exactly one address. Never accept
// arbitrary forwarding chains or headers from direct/untrusted clients.
func (a *App) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || peer.Zone() != "" {
		return "unknown"
	}
	peer = peer.Unmap()
	trusted := false
	for _, prefix := range a.TrustedProxies {
		if prefix.Contains(peer) {
			trusted = true
			break
		}
	}
	if trusted {
		values := r.Header.Values("X-Forwarded-For")
		if len(values) == 1 {
			ip, err := netip.ParseAddr(strings.TrimSpace(values[0]))
			if err == nil && ip.Zone() == "" {
				return ip.Unmap().String()
			}
		}
	}
	return peer.String()
}
