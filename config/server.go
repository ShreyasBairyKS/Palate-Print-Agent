package config

import (
	"net/netip"
	"net/url"
	"strings"
)

// DefaultServer is Palate's production server. Setup fills it in, so a
// restaurant never has to type an address.
const DefaultServer = "https://plexus.palatepower.com/api/print-agent"

// APIPath is the path Palate's servers serve the print-agent API under.
const APIPath = "/api/print-agent"

// NormalizeServer tidies an address a person typed, so the usual slips work:
//
//   - spaces around it and a trailing slash are dropped;
//   - "plexus.palatepower.com" gets https:// in front;
//   - a bare website ("https://plexus.palatepower.com/") gets /api/print-agent,
//     where Palate's servers answer the agent (a loopback address is left as
//     it is: the test server answers at its root);
//   - a /v1 on the end is dropped, because the agent adds it itself.
//
// Anything it can't make sense of is returned trimmed, for CheckServer to
// explain.
func NormalizeServer(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return strings.TrimRight(s, "/")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	p := strings.TrimRight(u.Path, "/")
	p = strings.TrimSuffix(p, "/v1")
	p = strings.TrimRight(p, "/")
	if p == "" && !isLoopbackHost(u.Hostname()) {
		p = APIPath
	}
	u.Path, u.RawPath = p, ""
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

func isLoopbackHost(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	return err == nil && ip.IsLoopback()
}
