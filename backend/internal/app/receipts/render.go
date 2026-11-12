package receipts

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Kinds of receipt.
const (
	KindPlaced = "placed"
	KindFinal  = "final"
)

// Line is one item on a receipt.
type Line struct {
	Name       string
	Size       string
	Qty        int
	TotalCents int64
	Mods       []string
	Notes      string
}

// Amount is a labelled money row (fees, discounts).
type Amount struct {
	Label string
	Cents int64
}

// Data is everything a receipt shows.
type Data struct {
	Kind        string
	StoreName   string
	StorePhone  string
	StoreAddr   string
	OrderRef    string
	PlacedAt    time.Time
	Type        string // pickup | delivery
	PromisedAt  *time.Time
	DeliverTo   string
	Customer    string
	Lines       []Line
	Subtotal    int64
	Discounts   []Amount
	Fees        []Amount
	Tax         int64
	Tip         int64
	Total       int64
	PaymentNote string // how it is / was paid
	TrackURL    string // "" = none
	Cancelled   bool
	Notes       string
}

func money(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s$%d.%02d", sign, c/100, c%100)
}

// Subject is the email's subject line.
func Subject(d Data) string {
	switch {
	case d.Cancelled:
		return fmt.Sprintf("Your %s order %s was cancelled", d.StoreName, d.OrderRef)
	case d.Kind == KindFinal:
		return fmt.Sprintf("Your final receipt from %s (order %s)", d.StoreName, d.OrderRef)
	}
	return fmt.Sprintf("Your %s order %s", d.StoreName, d.OrderRef)
}

func when(d Data) string {
	what := "Pickup"
	if d.Type == "delivery" {
		what = "Delivery"
	}
	if d.PromisedAt != nil {
		return what + " at " + d.PromisedAt.Format("Mon Jan 2, 3:04 PM")
	}
	return what + ", as soon as possible"
}

// Text is the plain-text body.
func Text(d Data) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	switch {
	case d.Cancelled:
		w("Your order %s from %s was cancelled.", d.OrderRef, d.StoreName)
	case d.Kind == KindFinal:
		w("Here is the final receipt for your order %s from %s.", d.OrderRef, d.StoreName)
	default:
		w("Thanks for your order, %s! %s has it.", firstName(d.Customer), d.StoreName)
	}
	w("")
	w("Order %s — placed %s", d.OrderRef, d.PlacedAt.Format("Mon Jan 2, 3:04 PM"))
	if !d.Cancelled {
		w("%s", when(d))
		if d.DeliverTo != "" {
			w("Deliver to: %s", d.DeliverTo)
		}
	}
	w("")
	for _, l := range d.Lines {
		name := l.Name
		if l.Size != "" {
			name = l.Size + " " + name
		}
		w("%d × %s  %s", l.Qty, name, money(l.TotalCents))
		for _, m := range l.Mods {
			w("    %s", m)
		}
		if l.Notes != "" {
			w("    Note: %s", l.Notes)
		}
	}
	w("")
	w("Subtotal  %s", money(d.Subtotal))
	for _, a := range d.Discounts {
		w("%s  -%s", a.Label, money(a.Cents))
	}
	for _, a := range d.Fees {
		w("%s  %s", a.Label, money(a.Cents))
	}
	w("Tax  %s", money(d.Tax))
	if d.Tip > 0 {
		w("Tip  %s", money(d.Tip))
	}
	w("Total  %s", money(d.Total))
	w("")
	w("%s", d.PaymentNote)
	if d.TrackURL != "" && !d.Cancelled {
		w("")
		w("Follow your order: %s", d.TrackURL)
	}
	w("")
	w("%s", d.StoreName)
	if d.StoreAddr != "" {
		w("%s", d.StoreAddr)
	}
	if d.StorePhone != "" {
		w("%s", d.StorePhone)
	}
	return b.String()
}

func firstName(n string) string {
	if f := strings.Fields(n); len(f) > 0 {
		return f[0]
	}
	return "there"
}

// HTML is the HTML body: one plain column, inline styles (mail clients).
func HTML(d Data) string {
	e := html.EscapeString
	var b strings.Builder
	w := func(s string) { b.WriteString(s) }
	row := func(l, r string, bold bool) {
		st := ""
		if bold {
			st = "font-weight:700;"
		}
		w(`<tr><td style="padding:2px 0;` + st + `">` + l + `</td><td style="padding:2px 0;text-align:right;` + st + `">` + r + `</td></tr>`)
	}
	w(`<!doctype html><html><body style="margin:0;background:#f4f4f5;font-family:-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#18181b">`)
	w(`<div style="max-width:520px;margin:0 auto;padding:24px 16px"><div style="background:#fff;border-radius:12px;padding:24px">`)
	w(`<h1 style="font-size:20px;margin:0 0 4px">` + e(d.StoreName) + `</h1>`)
	switch {
	case d.Cancelled:
		w(`<p style="margin:0 0 16px">Your order <b>` + e(d.OrderRef) + `</b> was cancelled.</p>`)
	case d.Kind == KindFinal:
		w(`<p style="margin:0 0 16px">Your final receipt for order <b>` + e(d.OrderRef) + `</b>.</p>`)
	default:
		w(`<p style="margin:0 0 16px">Thanks for your order, ` + e(firstName(d.Customer)) + `! Order <b>` + e(d.OrderRef) + `</b>.</p>`)
	}
	if !d.Cancelled {
		w(`<p style="margin:0 0 4px;font-weight:600">` + e(when(d)) + `</p>`)
		if d.DeliverTo != "" {
			w(`<p style="margin:0 0 4px">` + e(d.DeliverTo) + `</p>`)
		}
	}
	w(`<p style="margin:0 0 16px;color:#71717a;font-size:13px">Placed ` + e(d.PlacedAt.Format("Mon Jan 2, 3:04 PM")) + `</p>`)
	w(`<table style="width:100%;border-collapse:collapse;font-size:14px">`)
	for _, l := range d.Lines {
		name := l.Name
		if l.Size != "" {
			name = l.Size + " " + name
		}
		row(fmt.Sprintf("%d × %s", l.Qty, e(name)), money(l.TotalCents), false)
		for _, m := range l.Mods {
			w(`<tr><td colspan="2" style="padding:0 0 2px 16px;color:#71717a;font-size:13px">` + e(m) + `</td></tr>`)
		}
		if l.Notes != "" {
			w(`<tr><td colspan="2" style="padding:0 0 2px 16px;color:#71717a;font-size:13px">Note: ` + e(l.Notes) + `</td></tr>`)
		}
	}
	w(`<tr><td colspan="2" style="border-top:1px solid #e4e4e7;padding-top:8px"></td></tr>`)
	row("Subtotal", money(d.Subtotal), false)
	for _, a := range d.Discounts {
		row(e(a.Label), "-"+money(a.Cents), false)
	}
	for _, a := range d.Fees {
		row(e(a.Label), money(a.Cents), false)
	}
	row("Tax", money(d.Tax), false)
	if d.Tip > 0 {
		row("Tip", money(d.Tip), false)
	}
	row("Total", money(d.Total), true)
	w(`</table>`)
	w(`<p style="margin:16px 0 0;font-size:14px">` + e(d.PaymentNote) + `</p>`)
	if d.TrackURL != "" && !d.Cancelled {
		w(`<p style="margin:20px 0 0"><a href="` + e(d.TrackURL) + `" style="display:inline-block;background:#18181b;color:#fff;text-decoration:none;padding:10px 16px;border-radius:8px;font-weight:600">Follow your order</a></p>`)
	}
	w(`</div><p style="text-align:center;color:#71717a;font-size:12px;margin:16px 0 0">` + e(d.StoreName))
	if d.StoreAddr != "" {
		w(` · ` + e(d.StoreAddr))
	}
	if d.StorePhone != "" {
		w(` · ` + e(d.StorePhone))
	}
	w(`</p></div></body></html>`)
	return b.String()
}
