package geo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A provider timeout must not carry the customer's address or our key in
// its text (it is logged and stored in order_geo.last_error) — L-4.
func TestProviderErrorsHaveNoAddressOrKey(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer slow.Close()
	client := &http.Client{Timeout: 50 * time.Millisecond}
	a := Address{Line1: "931 Taylor Ave", City: "Towson", State: "MD", Zip: "21286"}

	g := &Google{BaseURL: slow.URL, Key: "AIzaSECRETKEY", Client: client}
	_, gErr := g.Geocode(context.Background(), a, nil)
	c := &Census{BaseURL: slow.URL, Client: client}
	_, cErr := c.Geocode(context.Background(), a, nil)
	o := &OSRM{BaseURL: slow.URL, Client: client}
	_, oErr := o.Route(context.Background(), Point{Lat: 39.4, Lng: -76.6}, Point{Lat: 39.41, Lng: -76.61})

	for name, err := range map[string]error{"google": gErr, "census": cErr, "osrm": oErr} {
		if err == nil {
			t.Fatalf("%s: expected a timeout", name)
		}
		msg := err.Error()
		for _, bad := range []string{"address=", "key=", "AIzaSECRETKEY", "Taylor", "-76.6", "39.4"} {
			if strings.Contains(msg, bad) {
				t.Errorf("%s error leaks %q: %s", name, bad, msg)
			}
		}
		if !strings.Contains(msg, "127.0.0.1") {
			t.Errorf("%s error lost the host: %s", name, msg)
		}
	}
}

func TestScrubURL(t *testing.T) {
	got := scrubURL("https://u:p@maps.googleapis.com/maps/api/geocode/json?address=1+Main&key=K#x")
	if got != "https://maps.googleapis.com/maps/api/geocode/json" {
		t.Fatalf("scrubURL = %s", got)
	}
	if got := scrubURL("http://osrm:5000/route/v1/driving/-76.6,39.4;-76.61,39.41?overview=false"); got != "http://osrm:5000/route/v1/driving/_" {
		t.Fatalf("osrm scrub = %s", got)
	}
}
