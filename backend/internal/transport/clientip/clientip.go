// Package clientip decides whose address a request comes from (security
// review M3). main sits behind Traefik (and, in prod, Cloudflare in front of
// Traefik), so the TCP peer is the proxy for every caller: without this every
// rate limit, audit row and log line saw one address.
//
// A forwarding header is believed only when the request arrives from a
// trusted proxy (TRUSTED_PROXY_CIDRS; default: loopback + the private
// ranges, since Traefik shares main's box / Docker network):
//
//   - X-Forwarded-For is walked from the right; the first address that is
//     not a trusted proxy is the client (Echo's ExtractIPFromXFFHeader).
//   - CF-Connecting-IP wins over that, but only when the hop X-Forwarded-For
//     ends on is Cloudflare (CLOUDFLARE_CIDRS; default: Cloudflare's
//     published ranges) or itself trusted (a cloudflared tunnel on the box):
//     somebody who reaches Traefik around Cloudflare can't name an address.
//
// A caller that is not a trusted proxy is its own TCP peer, whatever headers
// it sends.
package clientip

import (
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/labstack/echo/v5"
)

// DefaultTrustedProxies: loopback and the private ranges (RFC 1918, RFC 4193).
const DefaultTrustedProxies = "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"

// DefaultCloudflare is https://www.cloudflare.com/ips/ (read 2026-11-12).
const DefaultCloudflare = "173.245.48.0/20,103.21.244.0/22,103.22.200.0/22,103.31.4.0/22,141.101.64.0/18," +
	"108.162.192.0/18,190.93.240.0/20,188.114.96.0/20,197.234.240.0/22,198.41.128.0/17,162.158.0.0/15," +
	"104.16.0.0/13,104.24.0.0/14,172.64.0.0/13,131.0.72.0/22," +
	"2400:cb00::/32,2606:4700::/32,2803:f800::/32,2405:b500::/32,2405:8100::/32,2a06:98c0::/29,2c0f:f248::/32"

// HeaderCFConnectingIP is the client address Cloudflare puts on every request it proxies.
const HeaderCFConnectingIP = "CF-Connecting-IP"

// ParseCIDRs reads a comma / space separated list of CIDRs (a bare address is
// its /32 or /128). Empty = def; "none" = nothing.
func ParseCIDRs(list, def string) ([]*net.IPNet, error) {
	list = strings.TrimSpace(list)
	if list == "" {
		list = def
	}
	if strings.EqualFold(list, "none") {
		return nil, nil
	}
	var out []*net.IPNet
	for _, f := range strings.FieldsFunc(list, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		if !strings.Contains(f, "/") {
			ip := net.ParseIP(f)
			if ip == nil {
				return nil, fmt.Errorf("clientip: %q is not an address or CIDR", f)
			}
			if ip.To4() != nil {
				f += "/32"
			} else {
				f += "/128"
			}
		}
		_, n, err := net.ParseCIDR(f)
		if err != nil {
			return nil, fmt.Errorf("clientip: %q is not an address or CIDR", f)
		}
		out = append(out, n)
	}
	return out, nil
}

func contains(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Extractor is the echo.IPExtractor for the given trusted proxies and
// Cloudflare ranges (see the package comment).
func Extractor(trusted, cloudflare []*net.IPNet) echo.IPExtractor {
	opts := []echo.TrustOption{echo.TrustLoopback(false), echo.TrustLinkLocal(false), echo.TrustPrivateNet(false)}
	for _, n := range trusted {
		opts = append(opts, echo.TrustIPRange(n))
	}
	direct := echo.ExtractIPDirect()
	xff := echo.ExtractIPFromXFFHeader(opts...)
	return func(r *http.Request) string {
		peer := direct(r)
		peerIP := net.ParseIP(peer)
		if peerIP == nil || !contains(trusted, peerIP) {
			return peer // not one of our proxies: its headers say nothing
		}
		hop := xff(r)
		hopIP := net.ParseIP(hop)
		if cf := net.ParseIP(strings.TrimSpace(r.Header.Get(HeaderCFConnectingIP))); cf != nil &&
			hopIP != nil && (contains(cloudflare, hopIP) || contains(trusted, hopIP)) {
			return cf.String()
		}
		return hop
	}
}

// FromEnv builds the extractor from TRUSTED_PROXY_CIDRS and CLOUDFLARE_CIDRS
// (each: empty = its default, "none" = nothing).
func FromEnv(trustedProxies, cloudflare string) (echo.IPExtractor, error) {
	t, err := ParseCIDRs(trustedProxies, DefaultTrustedProxies)
	if err != nil {
		return nil, fmt.Errorf("TRUSTED_PROXY_CIDRS: %w", err)
	}
	cf, err := ParseCIDRs(cloudflare, DefaultCloudflare)
	if err != nil {
		return nil, fmt.Errorf("CLOUDFLARE_CIDRS: %w", err)
	}
	return Extractor(t, cf), nil
}
