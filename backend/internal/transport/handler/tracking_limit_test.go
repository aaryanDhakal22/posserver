package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	channelDom "quiccpos/main/internal/domain/channel"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

// The public tracking limit: 120 per IP and minute unless TRACK_PUBLIC_PER_MIN (dev / the gate
// only) says otherwise — staging and prod never set it.
func TestTrackPublicPerMinDefaultAndEnv(t *testing.T) {
	for _, c := range []struct {
		env  string
		want int
	}{{"", 120}, {"100000", 100000}, {" 500 ", 500}, {"0", 120}, {"-5", 120}, {"lots", 120}} {
		t.Run(fmt.Sprintf("%q", c.env), func(t *testing.T) {
			t.Setenv("TRACK_PUBLIC_PER_MIN", c.env)
			if got := TrackPublicPerMin(); got != c.want {
				t.Fatalf("TRACK_PUBLIC_PER_MIN=%q: got %d, want %d", c.env, got, c.want)
			}
		})
	}
}

func TestTrackingLimiterAtDefault(t *testing.T) {
	h := NewTrackingHandler(nil, 0, zerolog.Nop())
	for i := 0; i < TrackPublicPerMinDefault; i++ {
		if !h.public.Allow("203.0.113.7") {
			t.Fatalf("lookup %d refused, want %d allowed a minute", i+1, TrackPublicPerMinDefault)
		}
		h.public.Add("203.0.113.7")
	}
	if h.public.Allow("203.0.113.7") {
		t.Fatalf("lookup %d allowed, want 429 past the default", TrackPublicPerMinDefault+1)
	}
	if !h.public.Allow("198.51.100.9") {
		t.Fatal("another IP is refused: the limit must be per IP")
	}
}

// A fake AuthenticateSite: platform key qpk_good on host shop.test is a store's website; anything else
// is refused (as main refuses an unknown or revoked key).
func fakeSiteAuth(_ context.Context, key, host string) (*channelDom.Channel, error) {
	if key == "qpk_good" && host == "shop.test" {
		return &channelDom.Channel{Kind: channelDom.KindWeb}, nil
	}
	return nil, errors.New("invalid or revoked channel key")
}

func trackReq(h *TrackingHandler, key, host, customerIP string) *echo.Context {
	r := httptest.NewRequest(http.MethodGet, "/track/v1/tok", nil)
	r.RemoteAddr = "192.0.2.10:5555" // the storefront's own address
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	if host != "" {
		r.Header.Set("X-Site-Host", host)
	}
	if customerIP != "" {
		r.Header.Set("X-Customer-IP", customerIP)
	}
	return echo.New().NewContext(r, httptest.NewRecorder())
}

func TestTrackCallerTrustsTheCustomerIPOnlyFromAVerifiedStorefront(t *testing.T) {
	h := NewTrackingHandler(nil, 0, zerolog.Nop())
	h.SetSiteAuth(fakeSiteAuth)
	for _, c := range []struct {
		name, key, host, cust, wantIP string
		wantSite                      bool
	}{
		{"no key: the caller", "", "", "10.1.1.1", "192.0.2.10", false},
		{"verified storefront: the customer", "qpk_good", "shop.test", "10.1.1.1", "cust|10.1.1.1", true},
		{"bad key: the header is ignored", "qpk_forged", "shop.test", "10.1.1.1", "192.0.2.10", false},
		{"good key, other host: ignored", "qpk_good", "elsewhere.test", "10.1.1.1", "192.0.2.10", false},
		{"a channel key isn't a storefront", "qch_x", "shop.test", "10.1.1.1", "192.0.2.10", false},
		{"verified, not an IP: the caller", "qpk_good", "shop.test", "nonsense", "192.0.2.10", true},
		{"verified, no customer IP: the caller", "qpk_good", "shop.test", "", "192.0.2.10", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ip, site := h.trackCaller(trackReq(h, c.key, c.host, c.cust))
			if ip != c.wantIP || (site != "") != c.wantSite {
				t.Fatalf("got (%q, site %q), want (%q, site set %v)", ip, site, c.wantIP, c.wantSite)
			}
		})
	}
	h.SetSiteAuth(nil) // no channel service wired: never believed
	if ip, site := h.trackCaller(trackReq(h, "qpk_good", "shop.test", "10.1.1.1")); ip != "192.0.2.10" || site != "" {
		t.Fatalf("without site auth: got (%q, %q)", ip, site)
	}
}

// Two customers of one storefront: one over its limit is refused, the other still gets through.
func TestTrackingLimitPerCustomerThroughTheStorefront(t *testing.T) {
	h := NewTrackingHandler(nil, 0, zerolog.Nop())
	h.SetSiteAuth(fakeSiteAuth)
	for i := 0; i < TrackPublicPerMinDefault; i++ {
		if !h.admit(trackReq(h, "qpk_good", "shop.test", "10.1.1.1")) {
			t.Fatalf("customer A refused at lookup %d", i+1)
		}
	}
	if h.admit(trackReq(h, "qpk_good", "shop.test", "10.1.1.1")) {
		t.Fatal("customer A over the limit was let through")
	}
	if !h.admit(trackReq(h, "qpk_good", "shop.test", "10.2.2.2")) {
		t.Fatal("customer B refused because of customer A (one shared limit)")
	}
	// a forged header without the key counts against the caller's own address
	for i := 0; i < TrackPublicPerMinDefault; i++ {
		h.admit(trackReq(h, "", "", "10.3.3."+fmt.Sprint(i%250)))
	}
	if h.admit(trackReq(h, "", "", "10.4.4.4")) {
		t.Fatal("rotating X-Customer-IP without a key escaped the per-IP limit")
	}
}

// The storefront ceiling: perMin × TrackSitePerMinFactor lookups a minute across all its customers.
func TestTrackingCeilingPerStorefront(t *testing.T) {
	h := NewTrackingHandler(nil, 2, zerolog.Nop()) // 2 per customer → 100 per storefront
	h.SetSiteAuth(fakeSiteAuth)
	n := 0
	for i := 0; i < 200; i++ {
		if h.admit(trackReq(h, "qpk_good", "shop.test", fmt.Sprintf("10.9.%d.%d", i/250, i%250+1))) {
			n++
		}
	}
	if want := 2 * TrackSitePerMinFactor; n != want {
		t.Fatalf("one storefront got %d lookups through, want the ceiling %d", n, want)
	}
}
