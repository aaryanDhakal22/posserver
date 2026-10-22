package geo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultCensusURL is the US Census Bureau one-line address geocoder.
const DefaultCensusURL = "https://geocoding.geo.census.gov/geocoder/locations/onelineaddress"

// Census geocodes with the free US Census geocoder (TIGER address ranges).
// A match whose house number equals the input's is "exact", anything else
// (a range end, a street match) "approx".
type Census struct {
	BaseURL string
	Client  *http.Client
}

type censusResponse struct {
	Result struct {
		AddressMatches []struct {
			MatchedAddress string `json:"matchedAddress"`
			Coordinates    struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			} `json:"coordinates"`
		} `json:"addressMatches"`
	} `json:"result"`
}

// Geocode — see Geocoder.
func (c *Census) Geocode(ctx context.Context, a Address, _ *Point) (GeocodeResult, error) {
	q := url.Values{}
	q.Set("address", a.Line())
	q.Set("benchmark", "Public_AR_Current")
	q.Set("format", "json")
	var body censusResponse
	if err := getJSON(ctx, c.Client, c.BaseURL+"?"+q.Encode(), nil, &body); err != nil {
		return GeocodeResult{}, fmt.Errorf("census: %w", err)
	}
	return parseCensus(body, a)
}

func parseCensus(body censusResponse, a Address) (GeocodeResult, error) {
	if len(body.Result.AddressMatches) == 0 {
		return GeocodeResult{}, ErrNotFound
	}
	m := body.Result.AddressMatches[0]
	if m.Coordinates.X == 0 && m.Coordinates.Y == 0 {
		return GeocodeResult{}, ErrNotFound
	}
	q := QualityApprox
	in := HouseNumber(a.Line1)
	if in == "" {
		in = HouseNumber(a.Oneline)
	}
	if in != "" && HouseNumber(m.MatchedAddress) == in {
		q = QualityExact
	}
	return GeocodeResult{Point: Point{Lat: m.Coordinates.Y, Lng: m.Coordinates.X}, Quality: q, Source: "census"}, nil
}

// --- shared HTTP ----------------------------------------------------------------

// httpStatusError is a non-2xx answer from a provider.
type httpStatusError struct{ Code int }

func (e httpStatusError) Error() string { return fmt.Sprintf("http %d", e.Code) }

var defaultClient = &http.Client{Timeout: 10 * time.Second}

// getJSON GETs u and decodes the JSON body into out. A non-2xx status is an
// httpStatusError (OSRM answers 400 with a JSON body for "NoRoute" etc., which
// callers that care read through getJSONAny).
func getJSON(ctx context.Context, client *http.Client, u string, hdr http.Header, out any) error {
	status, err := getJSONAny(ctx, client, u, hdr, out)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return httpStatusError{Code: status}
	}
	return nil
}

// getJSONAny decodes the body whatever the status (when it is JSON) and
// returns the status.
func getJSONAny(ctx context.Context, client *http.Client, u string, hdr http.Header, out any) (int, error) {
	if client == nil {
		client = defaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			return resp.StatusCode, httpStatusError{Code: resp.StatusCode}
		}
		return resp.StatusCode, fmt.Errorf("decode: %w", err)
	}
	return resp.StatusCode, nil
}

func trimSlash(u string) string { return strings.TrimRight(u, "/") }
