// Package httptrust limits forwarded transport headers to configured peers.
package httptrust

import (
	"net"
	"net/http"
)

func TrustedPeer(remote string, proxies []string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, proxy := range proxies {
		if trusted := net.ParseIP(proxy); trusted != nil {
			if trusted.Equal(ip) {
				return true
			}
		} else if _, network, err := net.ParseCIDR(proxy); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}
func HTTPS(r *http.Request, proxies []string) bool {
	return r.TLS != nil || (TrustedPeer(r.RemoteAddr, proxies) && len(r.Header.Values("X-Forwarded-Proto")) == 1 && r.Header.Get("X-Forwarded-Proto") == "https")
}
