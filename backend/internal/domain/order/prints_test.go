package order

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCheckPrintJob(t *testing.T) {
	at := time.Date(2026, 11, 13, 17, 1, 0, 0, time.UTC)
	mode, got, err := CheckPrintJob(" accept ", []PrintTicket{
		{Kind: "station", Printer: " Pizza ", Result: "printed", PrintedOn: "Pizza", Attempts: 1},
		{Kind: "receipt", Printer: "Front", Result: "rerouted", PrintedOn: "Online", By: "auto", Attempts: 2, Error: strings.Repeat("x", 400)},
	}, at)
	if err != nil || mode != "accept" || len(got) != 2 || got[0].Printer != "Pizza" || !got[0].At.Equal(at) || len([]rune(got[1].Error)) != 300 {
		t.Fatalf("mode=%q got=%+v err=%v", mode, got, err)
	}
	for name, tk := range map[string][]PrintTicket{
		"no tickets":         {},
		"no kind":            {{Result: "printed"}},
		"unknown result":     {{Kind: "station", Result: "maybe"}},
		"rerouted nowhere":   {{Kind: "station", Result: "rerouted"}},
		"too many attempts":  {{Kind: "station", Result: "failed", Attempts: 101}},
		"printer name > 120": {{Kind: "station", Result: "failed", Printer: strings.Repeat("p", 121)}},
	} {
		if _, _, err := CheckPrintJob("accept", tk, at); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, _, err := CheckPrintJob("", []PrintTicket{{Kind: "station", Result: "printed"}}, at); err == nil {
		t.Error("no mode: accepted")
	}
}

func TestCardBrands(t *testing.T) {
	for in, want := range map[string]string{"": "", "VISA": "visa", "Mastercard": "mastercard", "AMERICAN_EXPRESS": "amex",
		"discover": "discover", "JCB": "other"} {
		if got := NormCardBrand(in); got != want {
			t.Errorf("NormCardBrand(%q) = %q, want %q", in, got, want)
		}
	}
	if b := cardBrandOf("cash", "visa"); b != "" {
		t.Errorf("cash has a brand: %q", b)
	}
	p := CounterPayment{ID: "0193b1c0-0000-7000-8000-000000000001", Method: "square_app", AmountCents: 100, Brand: "VISA"}
	cp, err := p.Check()
	if err != nil || cp.Brand != "visa" {
		t.Fatalf("checked brand %q err %v", cp.Brand, err)
	}
}

func TestTakenByNames(t *testing.T) {
	var s CounterSnapshot
	if err := json.Unmarshal([]byte(`{"device_name":" Counter 1 ","source_metadata":{"terminal":{"device_name":"x","staff_name":"Carlos M."}}}`), &s); err != nil {
		t.Fatal(err)
	}
	if d, n := s.terminalName("device_name", s.DeviceName), s.terminalName("staff_name", s.StaffName); d != "Counter 1" || n != "Carlos M." {
		t.Fatalf("device %q staff %q", d, n)
	}
	s.DeviceName, s.SourceMeta = strings.Repeat("d", 121), nil
	if d := s.terminalName("device_name", s.DeviceName); d != "" {
		t.Fatalf("too long kept: %q", d)
	}
}
