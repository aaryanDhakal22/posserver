package online

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"quiccpos/main/internal/domain/order"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// testTenantID / testLocationID are arbitrary stable UUIDs threaded through
// every Cast call in the suite. The aggregate requires both — the values
// themselves don't matter for casting correctness.
var (
	testTenantID   = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	testLocationID = uuid.MustParse("22222222-2222-2222-2222-222222222222")
)

// samplePayload mirrors tests/data.py["sample_coupon_order"] — a minimal,
// representative vendor JSON. Kept inline so the Go test doesn't need to
// reach into Python land.
const samplePayload = `{
  "tVer": "1.0.0",
  "order_id": 61124111,
  "store_id": 3282,
  "store_name": "Lombardi's Pizza",
  "service_type": "takeout",
  "submitted_date": "2026-03-07T01:11:00-0500",
  "print_date": "2026-03-07T01:11:00-0500",
  "deferred_date": "",
  "misc_charges": [
    {"misc_charge_name":"delivery_charge","misc_charge_desc":"Delivery Fee","misc_charge_amount":4},
    {"misc_charge_name":"other_charge","misc_charge_desc":"Service Charge","misc_charge_amount":0.5}
  ],
  "tip": 3.02,
  "taxes": [{"tax_name":"Sales Tax","tax_amount":1.81}],
  "is_tax_exempt": false,
  "order_total": 35.05,
  "balance_owing": 0,
  "notes": "",
  "customer": {
    "first_name":"ERIK","last_name":"DAVIS","company":"","phone":"4432077156","ext":"","email":"ERIK_TAILS11@YAHOO.COM"
  },
  "payments": [{"type":"JCB","amount":35.05,"card_number":"xxxxxxxxxxxxx303","card_holder":"ERIK DAIVS","auth_code":"052482","transaction_id":"","token":""}],
  "items": [
    {"name":"Regular Tomato Cheese Pizza","size_id":2,"size_name":"12 Medium","quantity":1,"price":9.99,"plu":"","who":"","group_id":"","notes":"",
     "modifiers":[{"side":"whole","name":"Add Pineapple","quantity":1,"plu":"","price":1.5,"action":"add"}]},
    {"name":"Regular Tomato Cheese Pizza","size_id":3,"size_name":"14 Large","quantity":1,"price":11.99,"plu":"","who":"","group_id":"","notes":"",
     "modifiers":[{"side":"whole","name":"Add Double Cheese","quantity":1,"plu":"","price":1.75,"action":"add"}]},
    {"name":"Brownies","size_id":1,"size_name":"Serving","quantity":1,"price":4.49,"plu":"","who":"","group_id":"","notes":"","modifiers":[]}
  ]
}`

// stubResolver stands in for tenancy.ResolveStore in caster tests: it returns a
// fixed (tenant, location) for any store, or a set error. If seen is non-nil it
// records the (store_id, store_name) the caster asked about.
type stubResolver struct {
	tenantID   uuid.UUID
	locationID uuid.UUID
	err        error
	seen       *resolveCall
}

type resolveCall struct {
	storeID   int64
	storeName string
}

func (s stubResolver) ResolveStore(_ context.Context, storeID int64, storeName string) (order.TenantID, order.LocationID, string, error) {
	if s.seen != nil {
		*s.seen = resolveCall{storeID: storeID, storeName: storeName}
	}
	if s.err != nil {
		return order.TenantID{}, order.LocationID{}, "name_fallback", s.err
	}
	return order.TenantID(s.tenantID), order.LocationID(s.locationID), "store_id", nil
}

func newTestCaster(t *testing.T) *Caster {
	t.Helper()
	return New(zerolog.Nop(), stubResolver{tenantID: testTenantID, locationID: testLocationID}, nil)
}

// TestCast_UnresolvedStoreErrors verifies an unresolvable store_name is a hard
// cast failure (→ DLQ), not a silent placeholder fallback.
func TestCast_UnresolvedStoreErrors(t *testing.T) {
	c := New(zerolog.Nop(), stubResolver{err: errors.New("no match")}, nil)
	if _, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(samplePayload)}); err == nil {
		t.Fatal("Cast should fail when the store cannot be resolved")
	}
}

// TestCast_PassesStoreIDAndName verifies the caster hands the resolver the
// vendor's numeric store_id (the primary routing key) and the store_name
// (fallback), and stamps the resolved scope on the order.
func TestCast_PassesStoreIDAndName(t *testing.T) {
	var seen resolveCall
	c := New(zerolog.Nop(), stubResolver{tenantID: testTenantID, locationID: testLocationID, seen: &seen}, nil)
	o, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(samplePayload)})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	if seen.storeID != 3282 || seen.storeName != "Lombardi's Pizza" {
		t.Fatalf("resolver got (%d, %q), want (3282, \"Lombardi's Pizza\")", seen.storeID, seen.storeName)
	}
	if uuid.UUID(o.TenantID()) != testTenantID || uuid.UUID(o.LocationID()) != testLocationID {
		t.Fatalf("order scope = %v/%v, want %v/%v", o.TenantID(), o.LocationID(), testTenantID, testLocationID)
	}
}

// ---------------------------------------------------------------------------
// Identity & happy path
// ---------------------------------------------------------------------------

func TestCaster_SourceIsOnline(t *testing.T) {
	if got := newTestCaster(t).Source(); got != order.SourceOnline {
		t.Errorf("Source() = %q, want %q", got, order.SourceOnline)
	}
}

func TestCast_HappyPath(t *testing.T) {
	c := newTestCaster(t)

	agg, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(samplePayload), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err != nil {
		t.Fatalf("Cast returned error: %v", err)
	}

	if agg.Source() != order.SourceOnline {
		t.Errorf("Source = %q, want %q", agg.Source(), order.SourceOnline)
	}
	if agg.SourceEventID() != "61124111" {
		t.Errorf("SourceEventID = %q, want %q", agg.SourceEventID(), "61124111")
	}
	if agg.Status() != order.StatusReceived {
		t.Errorf("Status = %q, want %q", agg.Status(), order.StatusReceived)
	}
	if agg.Currency() != "USD" {
		t.Errorf("Currency = %q, want USD", agg.Currency())
	}
	if got := len(agg.Items()); got != 3 {
		t.Errorf("Items count = %d, want 3", got)
	}

	m := agg.Money()
	if m.Total != 3505 {
		t.Errorf("Total = %d cents, want 3505", m.Total)
	}
	if m.Tax != 181 {
		t.Errorf("Tax = %d cents, want 181", m.Tax)
	}
	if m.Tip != 302 {
		t.Errorf("Tip = %d cents, want 302", m.Tip)
	}
	// Takeout: the vendor's $4 delivery charge isn't in order_total, so only
	// the $0.50 other charge counts and the subtotal is the items' $29.72.
	if got := len(m.Fees); got != 1 {
		t.Errorf("Fees count = %d, want 1", got)
	}
	if m.Subtotal != 2972 {
		t.Errorf("Subtotal = %d cents, want 2972", m.Subtotal)
	}
	// Reconciliation is validated inside order.NewOrder; if the err above
	// were non-nil we'd never reach here.

	if got := len(agg.RawPayload()); got == 0 {
		t.Error("RawPayload is empty; should preserve source bytes")
	}

	md := agg.SourceMetadata()
	if md == nil || md["store_id"] == nil {
		t.Error("SourceMetadata missing store_id")
	}
}

// ---------------------------------------------------------------------------
// Time-zone parsing matrix — the vendor sends multiple formats; all must
// produce the correct UTC instant. The aggregate's PlacedAt() always returns
// UTC, so equality is defined in UTC.
// ---------------------------------------------------------------------------

func TestParseVendorTime_AllAcceptedFormats(t *testing.T) {
	// All variants below describe the same instant: 2026-03-07T06:11:00Z.
	cases := []struct {
		name string
		raw  string
	}{
		{"vendor no-colon EST", "2026-03-07T01:11:00-0500"},
		{"vendor no-colon UTC", "2026-03-07T06:11:00+0000"},
		{"vendor no-colon Tokyo", "2026-03-07T15:11:00+0900"},
		{"vendor no-colon India", "2026-03-07T11:41:00+0530"},
		{"vendor no-colon EDT", "2026-03-07T02:11:00-0400"},
		{"vendor with millis", "2026-03-07T01:11:00.000-0500"},
		{"vendor with micros", "2026-03-07T01:11:00.000000-0500"},
		{"RFC3339 colon offset", "2026-03-07T01:11:00-05:00"},
		{"RFC3339 Z marker", "2026-03-07T06:11:00Z"},
		{"RFC3339Nano with Z", "2026-03-07T06:11:00.123456789Z"},
		{"space separator with colon TZ", "2026-03-07 06:11:00+00:00"},
	}

	want := time.Date(2026, 3, 7, 6, 11, 0, 0, time.UTC)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseVendorTime(tc.raw)
			if err != nil {
				t.Fatalf("parse %q failed: %v", tc.raw, err)
			}
			if !got.UTC().Truncate(time.Second).Equal(want) {
				t.Errorf("parse %q → %v; want %v", tc.raw, got.UTC(), want)
			}
		})
	}
}

func TestParseVendorTime_RejectsEmpty(t *testing.T) {
	if _, err := parseVendorTime(""); err == nil {
		t.Error("parseVendorTime(\"\") should error")
	}
	if _, err := parseVendorTime("   "); err == nil {
		t.Error("parseVendorTime whitespace should error")
	}
}

func TestParseVendorTime_RejectsGarbage(t *testing.T) {
	for _, bad := range []string{"yesterday", "2026/03/07 01:11", "61124111", "Mar 7, 2026"} {
		if _, err := parseVendorTime(bad); err == nil {
			t.Errorf("parseVendorTime(%q) should error", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// PlacedAt is normalized to UTC on the aggregate regardless of the wire offset.
// ---------------------------------------------------------------------------

func TestCast_PlacedAtIsUTC(t *testing.T) {
	c := newTestCaster(t)
	// 01:11 EST == 06:11 UTC
	agg, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(samplePayload), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	want := time.Date(2026, 3, 7, 6, 11, 0, 0, time.UTC)
	if !agg.PlacedAt().Equal(want) {
		t.Errorf("PlacedAt() = %v; want %v", agg.PlacedAt(), want)
	}
	if agg.PlacedAt().Location() != time.UTC {
		t.Errorf("PlacedAt() Location = %v; want UTC", agg.PlacedAt().Location())
	}
}

func TestCast_DeferredDate_ParsedWhenPresent(t *testing.T) {
	c := newTestCaster(t)
	payload := strings.Replace(samplePayload, `"deferred_date": ""`, `"deferred_date": "2026-03-07T18:00:00-0500"`, 1)
	agg, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(payload), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	if agg.Fulfillment().PromisedAt == nil {
		t.Fatal("PromisedAt should be set when deferred_date present")
	}
	want := time.Date(2026, 3, 7, 23, 0, 0, 0, time.UTC)
	if !agg.Fulfillment().PromisedAt.Equal(want) {
		t.Errorf("PromisedAt = %v; want %v", *agg.Fulfillment().PromisedAt, want)
	}
}

func TestCast_DeferredDate_UnparseableIsLogged(t *testing.T) {
	c := newTestCaster(t)
	payload := strings.Replace(samplePayload, `"deferred_date": ""`, `"deferred_date": "tomorrow at six"`, 1)
	agg, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(payload), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	if agg.Fulfillment().PromisedAt != nil {
		t.Errorf("PromisedAt should be nil when deferred_date is garbage; got %v", agg.Fulfillment().PromisedAt)
	}
}

// ---------------------------------------------------------------------------
// Service type → fulfillment mapping
// ---------------------------------------------------------------------------

func TestCast_FulfillmentTypes(t *testing.T) {
	cases := []struct {
		raw  string
		want order.FulfillmentType
	}{
		{"takeout", order.FulfillmentPickup},
		{"pickup", order.FulfillmentPickup},
		{"PICKUP", order.FulfillmentPickup},
		{"delivery", order.FulfillmentDelivery},
		{"DELIVERY", order.FulfillmentDelivery},
		{"dine_in", order.FulfillmentDineIn},
		{"DineIn", order.FulfillmentDineIn},
		{"unknown_garbage", order.FulfillmentPickup}, // safe default
		{"", order.FulfillmentPickup},
	}
	c := newTestCaster(t)
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			payload := strings.Replace(samplePayload, `"service_type": "takeout"`, fmt.Sprintf(`"service_type": %q`, tc.raw), 1)
			agg, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(payload), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
			if err != nil {
				t.Fatalf("Cast: %v", err)
			}
			if got := agg.Fulfillment().Type; got != tc.want {
				t.Errorf("service_type=%q → Fulfillment.Type=%q; want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCast_DeliveryAddressMaps(t *testing.T) {
	c := newTestCaster(t)
	payload := strings.Replace(samplePayload,
		`"service_type": "takeout"`,
		`"service_type":"delivery","delivery_address":{"street":"123 Main","cross_streets":"5th & Vine","suite":"4B","buz":"","city":"NYC","state":"NY","zip":"10001"}`,
		1)
	agg, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(payload), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	f := agg.Fulfillment()
	if f.Type != order.FulfillmentDelivery {
		t.Fatalf("Fulfillment.Type = %q, want delivery", f.Type)
	}
	if f.Address == nil {
		t.Fatal("Fulfillment.Address is nil for delivery order")
	}
	if f.Address.Line1 != "123 Main" {
		t.Errorf("Address.Line1 = %q, want '123 Main'", f.Address.Line1)
	}
	if f.Address.Line2 != "4B" {
		t.Errorf("Address.Line2 = %q, want '4B'", f.Address.Line2)
	}
	if f.Address.Notes != "5th & Vine" {
		t.Errorf("Address.Notes = %q, want '5th & Vine'", f.Address.Notes)
	}
}

func TestCast_DeliveryAddressIgnoredForPickup(t *testing.T) {
	c := newTestCaster(t)
	// service_type stays "takeout" but a delivery_address is also present —
	// the address must be dropped (you don't deliver a pickup).
	payload := strings.Replace(samplePayload,
		`"customer":`,
		`"delivery_address":{"street":"123 Main","city":"NYC","state":"NY","zip":"10001","cross_streets":"","suite":"","buz":""},"customer":`,
		1)
	agg, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(payload), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	if agg.Fulfillment().Address != nil {
		t.Errorf("Address should be nil for pickup; got %+v", agg.Fulfillment().Address)
	}
}

// ---------------------------------------------------------------------------
// Money / discount / fee mapping
// ---------------------------------------------------------------------------

func TestCast_CouponBecomesDiscount(t *testing.T) {
	c := newTestCaster(t)
	// Inject a coupon and bump the order_total downward so reconcile passes.
	payload := strings.Replace(samplePayload,
		`"items":`,
		`"coupons":[{"serial":"SUMMER10","plu":"","name":"Summer 10% off","value":3.50,"group_id":""}],"items":`,
		1)
	payload = strings.Replace(payload,
		`"order_total": 35.05`,
		`"order_total": 31.55`,
		1)
	agg, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(payload), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	m := agg.Money()
	if got := len(m.Discounts); got != 1 {
		t.Fatalf("Discounts count = %d, want 1", got)
	}
	if m.Discounts[0].Code != "SUMMER10" {
		t.Errorf("Discount.Code = %q, want SUMMER10", m.Discounts[0].Code)
	}
	if m.Discounts[0].Amount != 350 {
		t.Errorf("Discount.Amount = %d cents, want 350", m.Discounts[0].Amount)
	}
}

func TestCast_FeeTypesNormalized(t *testing.T) {
	c := newTestCaster(t)
	// As a delivery (the delivery charge counts, and order_total includes it).
	raw := strings.Replace(samplePayload, `"service_type": "takeout"`, `"service_type": "delivery"`, 1)
	raw = strings.Replace(raw, `"order_total": 35.05`, `"order_total": 39.05`, 1)
	agg, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(raw), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	gotTypes := make([]string, 0, len(agg.Money().Fees))
	for _, f := range agg.Money().Fees {
		gotTypes = append(gotTypes, f.Type)
	}
	want := []string{"delivery", "other"}
	if len(gotTypes) != len(want) {
		t.Fatalf("Fees len = %d, want %d (got types %v)", len(gotTypes), len(want), gotTypes)
	}
	for i := range want {
		if gotTypes[i] != want[i] {
			t.Errorf("Fees[%d].Type = %q, want %q", i, gotTypes[i], want[i])
		}
	}
}

// dollarsToCents tests document the rounding policy. Inputs with exact
// float representations are predictable; inputs that don't (1.005, 1.015)
// drift by 1 cent due to IEEE-754 representation, which is OK — the caster
// reconciliation tolerates 1c discrepancies.
func TestDollarsToCents_Rounding(t *testing.T) {
	cases := []struct {
		in   float64
		want order.Money
	}{
		{0, 0},
		{1.00, 100},
		{0.25, 25},
		{0.5, 50},
		{0.1 + 0.2, 30}, // classic float-drift case — still rounds to 30
		{-0.5, 0},       // negatives clamp to 0
		{-100, 0},       // big negative also clamps
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%.4f", tc.in), func(t *testing.T) {
			if got := dollarsToCents(tc.in); got != tc.want {
				t.Errorf("dollarsToCents(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Items tree + modifier handling
// ---------------------------------------------------------------------------

func TestCast_ModifiersBecomeChildren(t *testing.T) {
	c := newTestCaster(t)
	agg, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(samplePayload), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	items := agg.Items()
	if len(items) != 3 {
		t.Fatalf("expected 3 top-level items, got %d", len(items))
	}
	if got := len(items[0].Children); got != 1 {
		t.Errorf("items[0].Children = %d, want 1", got)
	}
	if items[0].Children[0].ParentID == nil || *items[0].Children[0].ParentID != items[0].ID {
		t.Errorf("items[0].Children[0].ParentID should point at items[0].ID")
	}
}

func TestCast_QuantityFloorIsOne(t *testing.T) {
	c := newTestCaster(t)
	// Force quantity 0 on first item; reconcile must still pass after total
	// is bumped to compensate. Easier: just check the floor on the aggregate
	// doesn't reject — caster sets qty=1 when input is <1, but the price
	// already reflects the original. The key invariant is "no zero-qty rows".
	payload := strings.Replace(samplePayload, `"quantity":1,"price":9.99`, `"quantity":0,"price":9.99`, 1)
	agg, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(payload), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	if got := agg.Items()[0].Quantity; got != 1 {
		t.Errorf("zero qty should be floored to 1, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// Failure modes return the right sentinel error so the consumer can route
// to the DLQ vs. retry.
// ---------------------------------------------------------------------------

func TestCast_RejectsMalformedJSON(t *testing.T) {
	c := newTestCaster(t)
	_, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(`{not json`), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err == nil {
		t.Fatal("Cast accepted malformed JSON")
	}
	if !errors.Is(err, ErrPayloadInvalid) {
		t.Errorf("err = %v; want wrapping ErrPayloadInvalid", err)
	}
}

func TestCast_RejectsMissingOrderID(t *testing.T) {
	c := newTestCaster(t)
	bad := strings.Replace(samplePayload, `"order_id": 61124111,`, ``, 1)
	_, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(bad), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err == nil {
		t.Fatal("Cast accepted payload with missing order_id")
	}
	if !errors.Is(err, ErrPayloadIncomplete) {
		t.Errorf("err = %v; want wrapping ErrPayloadIncomplete", err)
	}
}

func TestCast_RejectsBadSubmittedDate(t *testing.T) {
	c := newTestCaster(t)
	bad := strings.Replace(samplePayload, `"submitted_date": "2026-03-07T01:11:00-0500"`, `"submitted_date": "yesterday"`, 1)
	_, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(bad), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err == nil {
		t.Fatal("Cast accepted payload with garbage submitted_date")
	}
	if !errors.Is(err, ErrTimestampInvalid) {
		t.Errorf("err = %v; want wrapping ErrTimestampInvalid", err)
	}
}

func TestCast_RejectsEmptyItems(t *testing.T) {
	c := newTestCaster(t)
	bad := strings.Replace(samplePayload, `"items": [`, `"items_x": [`, 1)
	bad = strings.Replace(bad, `"items_x":`, `"items": [],"items_x":`, 1)
	_, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(bad), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err == nil {
		t.Fatal("Cast accepted payload with no items")
	}
	if !errors.Is(err, order.ErrEmptyItems) {
		t.Errorf("err = %v; want wrapping order.ErrEmptyItems", err)
	}
}

// ---------------------------------------------------------------------------
// SourceMetadata captures vendor-only fields — the aggregate doesn't promote
// them but they're queryable.
// ---------------------------------------------------------------------------

func TestCast_SourceMetadataPopulated(t *testing.T) {
	c := newTestCaster(t)
	agg, err := c.Cast(context.Background(), order.CastInput{Raw: []byte(samplePayload), TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	md := agg.SourceMetadata()
	mustHave := []string{"tver", "store_id", "store_name", "is_tax_exempt", "balance_owing"}
	for _, k := range mustHave {
		if _, ok := md[k]; !ok {
			t.Errorf("SourceMetadata missing key %q", k)
		}
	}
	// Vendor payments are summarised in metadata, not promoted to first-class.
	if _, ok := md["payments"]; !ok {
		t.Error("SourceMetadata missing payments summary")
	}
}

func TestCast_RawPayloadPreserved(t *testing.T) {
	c := newTestCaster(t)
	raw := []byte(samplePayload)
	agg, err := c.Cast(context.Background(), order.CastInput{Raw: raw, TenantID: order.TenantID(testTenantID), LocationID: order.LocationID(testLocationID)})
	if err != nil {
		t.Fatalf("Cast: %v", err)
	}
	if string(agg.RawPayload()) != samplePayload {
		t.Error("RawPayload was mutated; should be byte-identical to input")
	}
	// Mutating the input slice afterward must not affect the stored payload.
	raw[0] = 'X'
	if got := agg.RawPayload(); string(got) != samplePayload {
		t.Error("RawPayload aliases the input slice; aggregate should defensively copy")
	}
}

// The real "sample_delayed_order" (tests/data.py): a takeout order whose
// misc_charges list a $4 delivery charge that order_total leaves out, paid
// cash (payments null, balance_owing = total).
func TestBuildMoney_TakeoutIgnoresTheVendorsDeliveryCharge(t *testing.T) {
	p := Payload{
		ServiceType: "takeout",
		OrderTotal:  6.88,
		Taxes:       []Tax{{TaxName: "Sales Tax", TaxAmount: 0.39}},
		MiscCharges: []MiscCharge{
			{MiscChargeName: "delivery_charge", MiscChargeDesc: "Delivery Fee", MiscChargeAmount: 4},
			{MiscChargeName: "other_charge", MiscChargeDesc: "Service Fee", MiscChargeAmount: 0.5},
		},
	}
	m := buildMoney(p)
	if m.Subtotal != 599 {
		t.Errorf("subtotal = %d, want 599 (the items)", m.Subtotal)
	}
	if len(m.Fees) != 1 || m.Fees[0].Type != "other" || m.Fees[0].Amount != 50 {
		t.Errorf("fees = %+v, want only the $0.50 service fee", m.Fees)
	}

	p.ServiceType = "delivery"
	p.OrderTotal = 10.88
	m = buildMoney(p)
	if m.Subtotal != 599 || len(m.Fees) != 2 {
		t.Errorf("delivery: subtotal %d fees %+v, want 599 and both fees", m.Subtotal, m.Fees)
	}
}

func TestBuildPayments_BalanceOwingIsCash(t *testing.T) {
	// Cash order: no payments, everything owing.
	ps := buildPayments(nil, 6.88)
	if len(ps) != 1 || ps[0].Processor != "cash" || ps[0].Amount != 688 || ps[0].Status != order.PaymentPending {
		t.Fatalf("cash order → %+v, want one pending cash payment of 688", ps)
	}
	// Part-paid: card now, the rest in cash.
	ps = buildPayments([]Payment{{Type: "Visa", Amount: 15, CardNumber: "xxxx4242"}}, 5.5)
	if len(ps) != 2 || ps[0].Processor != "card" || ps[1].Processor != "cash" || ps[1].Amount != 550 {
		t.Fatalf("part-paid → %+v, want card + 550 cash", ps)
	}
	// A cash payment already covering the balance isn't doubled.
	ps = buildPayments([]Payment{{Type: "cash", Amount: 31.09}}, 31.09)
	if len(ps) != 1 || ps[0].Amount != 3109 {
		t.Fatalf("explicit cash → %+v, want it alone", ps)
	}
	// Prepaid (card / third party), nothing owing.
	if ps = buildPayments([]Payment{{Type: "third_party", Amount: 16.25}}, 0); len(ps) != 1 || ps[0].Processor != "other" {
		t.Fatalf("prepaid → %+v", ps)
	}
	if ps = buildPayments(nil, 0); ps != nil {
		t.Fatalf("nothing paid or owing → %+v, want nil", ps)
	}
}
