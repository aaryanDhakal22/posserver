package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
)

func req(peer string, h map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = peer
	for k, v := range h {
		r.Header.Set(k, v)
	}
	return r
}

func TestExtractor(t *testing.T) {
	x, err := FromEnv("", "")
	if err != nil {
		t.Fatal(err)
	}
	const traefik = "172.18.0.5:41234" // the proxy on main's Docker network
	cases := []struct {
		name string
		peer string
		h    map[string]string
		want string
	}{
		{"direct caller, no headers", "203.0.113.7:5000", nil, "203.0.113.7"},
		{"direct caller can't name an address (XFF)", "203.0.113.7:5000", map[string]string{"X-Forwarded-For": "1.2.3.4"}, "203.0.113.7"},
		{"direct caller can't name an address (CF)", "203.0.113.7:5000", map[string]string{"CF-Connecting-IP": "1.2.3.4"}, "203.0.113.7"},
		{"direct caller can't name an address (X-Real-IP)", "203.0.113.7:5000", map[string]string{"X-Real-IP": "1.2.3.4"}, "203.0.113.7"},
		{"behind Traefik: the client Traefik saw", traefik, map[string]string{"X-Forwarded-For": "198.51.100.9"}, "198.51.100.9"},
		{"behind Traefik: a client-sent XFF entry is not believed", traefik, map[string]string{"X-Forwarded-For": "6.6.6.6, 198.51.100.9"}, "198.51.100.9"},
		{"around Cloudflare: CF-Connecting-IP from a non-Cloudflare client ignored", traefik,
			map[string]string{"X-Forwarded-For": "198.51.100.9", "CF-Connecting-IP": "6.6.6.6"}, "198.51.100.9"},
		{"through Cloudflare: Traefik saw a Cloudflare edge", traefik,
			map[string]string{"X-Forwarded-For": "162.158.10.20", "CF-Connecting-IP": "198.51.100.77"}, "198.51.100.77"},
		{"through Cloudflare, IPv6", traefik,
			map[string]string{"X-Forwarded-For": "2606:4700::1234", "CF-Connecting-IP": "2001:db8::7"}, "2001:db8::7"},
		{"cloudflared tunnel on the box: every hop trusted", traefik,
			map[string]string{"X-Forwarded-For": "172.18.0.9", "CF-Connecting-IP": "198.51.100.78"}, "198.51.100.78"},
		{"a junk CF-Connecting-IP is not an address", traefik,
			map[string]string{"X-Forwarded-For": "162.158.10.20", "CF-Connecting-IP": "not-an-ip"}, "162.158.10.20"},
		{"local caller without headers (dev, tests)", "127.0.0.1:5000", nil, "127.0.0.1"},
		{"unparsable XFF: the peer", traefik, map[string]string{"X-Forwarded-For": "garbage"}, "172.18.0.5"},
	}
	for _, c := range cases {
		if got := x(req(c.peer, c.h)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestExtractorConfigured(t *testing.T) {
	// Only 10.9.0.0/24 is a proxy; Cloudflare not believed at all.
	x, err := FromEnv("10.9.0.0/24", "none")
	if err != nil {
		t.Fatal(err)
	}
	if got := x(req("172.18.0.5:1", map[string]string{"X-Forwarded-For": "1.2.3.4"})); got != "172.18.0.5" {
		t.Errorf("a private address outside TRUSTED_PROXY_CIDRS was believed: %q", got)
	}
	if got := x(req("10.9.0.2:1", map[string]string{"X-Forwarded-For": "1.2.3.4"})); got != "1.2.3.4" {
		t.Errorf("trusted proxy's XFF: %q", got)
	}
	if got := x(req("10.9.0.2:1", map[string]string{"X-Forwarded-For": "162.158.10.20", "CF-Connecting-IP": "9.9.9.9"})); got != "162.158.10.20" {
		t.Errorf("CLOUDFLARE_CIDRS=none still believed CF-Connecting-IP: %q", got)
	}
	// "none": no proxy is trusted, the peer is always the client.
	x, _ = FromEnv("none", "")
	if got := x(req("127.0.0.1:1", map[string]string{"X-Forwarded-For": "1.2.3.4"})); got != "127.0.0.1" {
		t.Errorf("TRUSTED_PROXY_CIDRS=none: %q", got)
	}
}

func TestParseCIDRs(t *testing.T) {
	n, err := ParseCIDRs(" 10.0.0.1, 2001:db8::1 192.168.0.0/16 ", "")
	if err != nil || len(n) != 3 || n[0].String() != "10.0.0.1/32" || n[1].String() != "2001:db8::1/128" {
		t.Fatalf("got %v, %v", n, err)
	}
	if _, err := ParseCIDRs("10.0.0.0/33", ""); err == nil {
		t.Fatal("bad CIDR accepted")
	}
	if _, err := FromEnv("nope", ""); err == nil {
		t.Fatal("bad TRUSTED_PROXY_CIDRS accepted")
	}
	for _, def := range []string{DefaultTrustedProxies, DefaultCloudflare} {
		if _, err := ParseCIDRs("", def); err != nil {
			t.Fatalf("default list: %v", err)
		}
	}
}

// RealIP goes through the extractor once it is set on Echo.
func TestRealIPUsesExtractor(t *testing.T) {
	e := echo.New()
	e.IPExtractor, _ = FromEnv("", "")
	c := e.NewContext(req("172.18.0.5:1", map[string]string{"X-Forwarded-For": "198.51.100.9"}), httptest.NewRecorder())
	if got := c.RealIP(); got != "198.51.100.9" {
		t.Fatalf("RealIP = %q", got)
	}
}
