// Package pricing prices a cart (main 3.0 step 4): a channel's menu (its
// view build), the bank facts the view hides (each item's category and tax
// rate), the store's pricing settings as this channel sees them, and the
// channel's deals → priced lines, discounts, fees, tax, tip and total.
//
// Pure and deterministic, integer cents throughout. The same JSON cases
// (testdata/cases) run against this engine and its TypeScript copy
// (frontend/src/lib/pricing/engine.ts); keep the two in step.
package pricing

import "quiccpos/main/internal/domain/menuview"

// Context is everything a quote prices against.
type Context struct {
	Menu     Menu                 `json:"menu"`
	Items    map[string]ItemFacts `json:"items"` // bank item id → facts
	Settings Settings             `json:"settings"`
	Deals    []Deal               `json:"deals"` // this channel's deals (inactive ones are skipped)
	Timezone string               `json:"timezone"`
	// Types: which order types the store takes now (store status).
	Types Types `json:"types"`
}

// Menu is the part of a channel's menu a quote reads.
type Menu struct {
	Categories     []menuview.BuildCategory `json:"categories"`
	ModifierGroups []menuview.BuildGroup    `json:"modifier_groups"`
}

// ItemFacts are a bank item's facts views don't show.
type ItemFacts struct {
	CategoryID string `json:"category_id"`
	TaxBps     int    `json:"tax_bps"` // 0 = the store rate
}

type Types struct {
	Pickup   bool `json:"pickup"`
	Delivery bool `json:"delivery"`
}

// Settings are the pricing settings a channel sees (store defaults with the
// channel kind's overrides applied).
type Settings struct {
	TaxBps int `json:"tax_bps"` // the store rate: items without their own, and the service fee
	// The service (online) fee: a flat part plus a percent of the food after
	// discounts. Taxed at the store rate.
	ServiceFeeCents int64 `json:"service_fee_cents"`
	ServiceFeeBps   int   `json:"service_fee_bps"`
	// Delivery: the fee (untaxed), free from an amount (nil = never), and the
	// minimum food total after discounts.
	DeliveryFeeCents      int64   `json:"delivery_fee_cents"`
	FreeDeliveryOverCents *int64  `json:"free_delivery_over_cents"`
	DeliveryMinCents      int64   `json:"delivery_min_cents"`
	Tips                  Tips    `json:"tips"`
	CardFee               CardFee `json:"card_fee"`
	// Stacking: best (bundles + the one best automatic deal or code), stack
	// (bundles + every automatic deal + a code), single (one deal per order:
	// a bundle line rules out automatic deals and codes).
	Stacking string `json:"stacking"`
}

type Tips struct {
	Enabled    bool  `json:"enabled"`
	Presets    []int `json:"presets"`     // percents
	DefaultPct int   `json:"default_pct"` // used when the cart names no tip
}

// Card fee modes.
const (
	CardFeeNone         = "none"
	CardFeeSurcharge    = "surcharge"
	CardFeeCashDiscount = "cash_discount"
)

type CardFee struct {
	Mode string `json:"mode"`
	Bps  int    `json:"bps"`
}

// Stacking modes.
const (
	StackBest   = "best"
	StackAll    = "stack"
	StackSingle = "single"
)

// Deal kinds.
const (
	DealBundle   = "bundle"    // pick items for slots at a set price
	DealMix      = "mix"       // any qty+ eligible units at each_cents each
	DealNth      = "nth"       // every nth eligible unit (priciest first) pct off
	DealCode     = "code"      // a promo code: pct or amount off the food
	DealFreeItem = "free_item" // one eligible unit free once the food reaches min_subtotal_cents
)

// Deal is one of the store's deals.
type Deal struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Active      bool      `json:"active"`
	Fulfil      string    `json:"fulfil"` // pickup | delivery | "" (both)
	Schedule    *Schedule `json:"schedule"`
	// bundle
	PriceCents int64  `json:"price_cents,omitempty"`
	Slots      []Slot `json:"slots,omitempty"`
	// mix, nth, free_item
	Eligible  *Eligible `json:"eligible,omitempty"`
	Qty       int       `json:"qty,omitempty"`
	EachCents int64     `json:"each_cents,omitempty"`
	N         int       `json:"n,omitempty"`
	// nth, code
	Pct int `json:"pct,omitempty"`
	// code
	Code           string `json:"code,omitempty"`
	AmountCents    int64  `json:"amount_cents,omitempty"`
	FirstOrderOnly bool   `json:"first_order_only,omitempty"`
	// code, free_item
	MinSubtotalCents int64 `json:"min_subtotal_cents,omitempty"`
}

// Schedule limits a deal to days ("mon".."sun") and a time of day (HH:MM, store time).
type Schedule struct {
	Days  []string `json:"days"`
	Start string   `json:"start"`
	End   string   `json:"end"`
}

// Eligible names the bank items, bank categories and sizes a deal applies to
// (an item matches by its id or its category; sizes, when given, narrow it).
type Eligible struct {
	Items      []string `json:"items"`
	Categories []string `json:"categories"`
	Sizes      []string `json:"sizes"`
}

// Slot is one pick of a bundle.
type Slot struct {
	Label           string   `json:"label"`
	Eligible        Eligible `json:"eligible"`
	IncludedOptions int      `json:"included_options"` // the priciest N options are in the deal price
}

// Cart is what a channel asks to price.
type Cart struct {
	Type       string `json:"type"`    // pickup | delivery
	Payment    string `json:"payment"` // card | cash
	Lines      []Line `json:"lines"`
	Code       string `json:"code"`
	TipCents   *int64 `json:"tip_cents"`
	TipPct     *int   `json:"tip_pct"`
	FirstOrder *bool  `json:"first_order"`
}

// Selection is one configured menu item.
type Selection struct {
	ItemID   string `json:"item_id"`   // a menu item's id
	ChoiceID string `json:"choice_id"` // a combined item: the bank item picked
	SizeID   string `json:"size_id"`
	Options  []Pick `json:"options"`
}

// Pick is one chosen modifier option.
type Pick struct {
	GroupID  string `json:"group_id"`
	OptionID string `json:"option_id"`
	Portion  string `json:"portion"` // whole (default) | left | right
}

// Line is a cart line: an item (Selection) or a bundle deal (DealID + Picks).
type Line struct {
	Selection
	Qty    int         `json:"qty"`
	Note   string      `json:"note"`
	DealID string      `json:"deal_id"`
	Picks  []Selection `json:"picks"`
}

// Quote is a priced cart.
type Quote struct {
	Lines            []QuoteLine `json:"lines"`
	SubtotalCents    int64       `json:"subtotal_cents"`
	Discounts        []Discount  `json:"discounts"`
	DeliveryFeeCents int64       `json:"delivery_fee_cents"`
	ServiceFeeCents  int64       `json:"service_fee_cents"`
	TaxCents         int64       `json:"tax_cents"`
	TipCents         int64       `json:"tip_cents"`
	CardFeeCents     int64       `json:"card_fee_cents"`
	TotalCents       int64       `json:"total_cents"`
	SavedCents       int64       `json:"saved_cents"`
	Errors           []Issue     `json:"errors"`
	Warnings         []string    `json:"warnings"`
	Nudges           []string    `json:"nudges"`
}

type QuoteLine struct {
	Index        int      `json:"index"`
	Name         string   `json:"name"`
	Mods         []string `json:"mods"`
	Qty          int      `json:"qty"`
	UnitCents    int64    `json:"unit_cents"`
	TotalCents   int64    `json:"total_cents"`
	RegularCents *int64   `json:"regular_cents"` // a bundle: what its picks cost on their own
	DealID       string   `json:"deal_id"`
}

type Discount struct {
	Type        string `json:"type"` // deal | code | cash
	DealID      string `json:"deal_id"`
	Label       string `json:"label"`
	AmountCents int64  `json:"amount_cents"`
}

// Issue is something that stops the cart from being ordered as it is.
type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Line    *int   `json:"line"` // the cart line's index, when it is about one
}

// Issue codes.
const (
	IssueEmpty       = "empty"
	IssueUnknownItem = "unknown_item"
	IssueUnknownSize = "unknown_size"
	IssueUnknownOpt  = "unknown_option"
	IssueOptionRules = "option_rules"
	IssueSoldOut     = "sold_out"
	IssueDealEnded   = "deal_ended"
	IssueDealNotNow  = "deal_unavailable"
	IssueDealSlot    = "deal_slot"
	IssueCodeInvalid = "code_invalid"
	IssueCodeMinimum = "code_minimum"
	IssueCodeFirst   = "code_first_order"
	IssueCodeAlone   = "code_not_combinable"
	IssueTypeOff     = "type_off"
	IssueBadType     = "bad_type"
	IssueDeliveryMin = "delivery_minimum"
)
