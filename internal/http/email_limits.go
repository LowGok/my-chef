package httpapi

import (
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

func emailClientAddress(r *http.Request, trusted []netip.Addr) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || peer.Zone() != "" {
		return "unknown"
	}
	peer = peer.Unmap()
	if slices.Contains(trusted, peer) {
		values := r.Header.Values("X-Forwarded-For")
		if len(values) == 1 {
			client, err := netip.ParseAddr(strings.TrimSpace(values[0]))
			client = client.Unmap()
			if err == nil && client.Zone() == "" && !client.IsUnspecified() && !client.IsMulticast() {
				return client.String()
			}
		}
	}
	return peer.String()
}
