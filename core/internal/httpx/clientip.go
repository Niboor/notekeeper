package httpx

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP returns the caller's address. X-Forwarded-For is honoured only when the direct peer
// is one of the trusted proxies (the ingress); otherwise a client could put any address there
// and dodge per-address throttling (docs/design/03-auth.md section 3).
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if !inPrefixes(trusted, peer) {
		return peer.String()
	}
	// Walk the chain from the right: the first address that is not a trusted proxy is the client.
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			continue
		}
		if !inPrefixes(trusted, a) {
			return a.String()
		}
	}
	return peer.String()
}

func inPrefixes(prefixes []netip.Prefix, a netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// ParsePrefixes parses a comma-separated list of CIDR ranges or single addresses.
func ParsePrefixes(list []string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, s := range list {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
			continue
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return nil, err
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
