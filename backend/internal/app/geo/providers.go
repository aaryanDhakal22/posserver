package geo

import (
	"context"
	"errors"
	"os"
	"strings"
)

// Config selects the providers (env GEO_* / GOOGLE_MAPS_API_KEY).
type Config struct {
	OSRMURL      string // GEO_OSRM_URL; "" = routing off (unless Google)
	Contact      string // GEO_CONTACT, for Nominatim's User-Agent
	GoogleKey    string // GOOGLE_MAPS_API_KEY; set = Google for everything
	CensusURL    string // GEO_CENSUS_URL override (tests)
	NominatimURL string // GEO_NOMINATIM_URL override (tests)
	GoogleURL    string // override (tests)
}

// ConfigFromEnv reads the GEO_* variables.
func ConfigFromEnv() Config {
	return Config{
		OSRMURL:      strings.TrimSpace(os.Getenv("GEO_OSRM_URL")),
		Contact:      strings.TrimSpace(os.Getenv("GEO_CONTACT")),
		GoogleKey:    strings.TrimSpace(os.Getenv("GOOGLE_MAPS_API_KEY")),
		CensusURL:    strings.TrimSpace(os.Getenv("GEO_CENSUS_URL")),
		NominatimURL: strings.TrimSpace(os.Getenv("GEO_NOMINATIM_URL")),
	}
}

// Providers is the active geocoder + router.
type Providers struct {
	Geocoder
	Router
	name    string
	routing bool
}

// Name is "google" or "census+nominatim/osrm" (…/none when routing is off).
func (p *Providers) Name() string { return p.name }

// Routing reports whether a router is configured.
func (p *Providers) Routing() bool { return p.routing }

// Build picks the providers: Google when a key is set, else Census →
// Nominatim for geocoding and OSRM (when GEO_OSRM_URL is set) for routing.
func Build(c Config) *Providers {
	if c.GoogleKey != "" {
		g := &Google{BaseURL: or(c.GoogleURL, DefaultGoogleURL), Key: c.GoogleKey}
		return &Providers{Geocoder: g, Router: g, name: "google", routing: true}
	}
	gc := Chain{
		&Census{BaseURL: or(c.CensusURL, DefaultCensusURL)},
		&Nominatim{BaseURL: or(c.NominatimURL, DefaultNominatimURL), Contact: c.Contact},
	}
	if c.OSRMURL != "" {
		return &Providers{Geocoder: gc, Router: &OSRM{BaseURL: c.OSRMURL}, name: "census+nominatim/osrm", routing: true}
	}
	return &Providers{Geocoder: gc, Router: noRouter{}, name: "census+nominatim/none", routing: false}
}

func or(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

// Chain asks each geocoder in turn until one matches. The result is
// ErrNotFound only when every geocoder answered "no match"; if any failed
// (network, 5xx) and none matched, that failure is returned so the order is
// retried.
type Chain []Geocoder

// Geocode — see Geocoder.
func (c Chain) Geocode(ctx context.Context, a Address, near *Point) (GeocodeResult, error) {
	var failure error
	for _, g := range c {
		r, err := g.Geocode(ctx, a, near)
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, ErrNotFound) {
			failure = errors.Join(failure, err)
		}
		if ctx.Err() != nil {
			return GeocodeResult{}, ctx.Err()
		}
	}
	if failure != nil {
		return GeocodeResult{}, failure
	}
	return GeocodeResult{}, ErrNotFound
}

// GeocodeIn asks each geocoder in turn for a inside box (see AreaGeocoder),
// with Geocode's error rule: ErrNotFound only when every one answered "no
// match".
func (c Chain) GeocodeIn(ctx context.Context, a Address, box BBox) (GeocodeResult, error) {
	var failure error
	for _, g := range c {
		r, err := geocodeIn(ctx, g, a, box)
		if err == nil {
			return r, nil
		}
		if !errors.Is(err, ErrNotFound) {
			failure = errors.Join(failure, err)
		}
		if ctx.Err() != nil {
			return GeocodeResult{}, ctx.Err()
		}
	}
	if failure != nil {
		return GeocodeResult{}, failure
	}
	return GeocodeResult{}, ErrNotFound
}
