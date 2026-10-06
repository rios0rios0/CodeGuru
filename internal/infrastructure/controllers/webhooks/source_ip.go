package webhooks

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Re-exported indirectly: source_ip.go and dispatcher.go share the
// netip.Prefix type via the dispatcher's pre-parsed slice.

// clientIP returns the original client IP for r, preferring proxy-set headers
// over the connection peer because the pod typically sits behind a CDN
// (Cloudflare) and an in-cluster ingress controller. Header precedence,
// strongest signal first:
//
//  1. CF-Connecting-IP — set by Cloudflare and reset on every hop, so
//     spoofing requires bypassing Cloudflare entirely.
//  2. X-Real-IP — typically set by a single trusted proxy in front of the pod.
//  3. X-Forwarded-For — comma-separated chain; the leftmost entry is the
//     original client. Trustworthy only when the upstream chain is also
//     trusted; combine with a network-level allowlist if the threat model
//     requires defending against forged XFF.
//  4. r.RemoteAddr — the TCP peer, used as a last resort. In a typical
//     ingress chain this is the load balancer or the ingress controller, not
//     the original client.
func clientIP(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip")); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		return v
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		// First entry is the original client; subsequent entries are
		// proxy hops appended by each forwarder.
		first, _, _ := strings.Cut(v, ",")
		if first = strings.TrimSpace(first); first != "" {
			return first
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// sourceIPAllowed returns true when ip is within any of the prefixes, or when
// the list is empty (which means "no allowlist configured, allow all").
// Prefixes are pre-parsed at dispatcher construction (see parseAllowedCIDRs)
// so this hot-path check has no per-request parsing cost.
func sourceIPAllowed(ip string, prefixes []netip.Prefix) bool {
	if len(prefixes) == 0 {
		return true
	}
	addr, parseErr := netip.ParseAddr(ip)
	if parseErr != nil {
		return false
	}
	for _, prefix := range prefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// enforceSourceIPAllowlist runs the source-IP allowlist check on r and writes
// a 403 response when the request's source IP is outside the configured
// CIDRs. Returns true when the request should proceed to the next handler
// stage, false when the response has already been written.
//
// label is included in the warning log so operators can tell ADO and GitHub
// rejections apart in the same stream.
func (d *Dispatcher) enforceSourceIPAllowlist(w http.ResponseWriter, r *http.Request, label string) bool {
	ip := clientIP(r)
	if sourceIPAllowed(ip, d.allowedSourcePrefixes) {
		return true
	}
	logWarnf("%s webhook rejected: source IP %s not in allowlist", label, ip)
	writeError(w, http.StatusForbidden, "source IP not allowed")
	return false
}
