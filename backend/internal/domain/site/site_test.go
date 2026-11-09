package site

import "testing"

func TestNormalizeHost(t *testing.T) {
	cases := map[string]string{
		"Lombardis.QuiccPOS.com":         "lombardis.quiccpos.com",
		"https://order.example.com/menu": "order.example.com",
		"order.example.com:8443":         "order.example.com",
		"order.example.com.":             "order.example.com",
		"localhost":                      "",
		"bad_host.com":                   "",
		"":                               "",
		"-x.example.com":                 "",
		"192.168.1.1":                    "",
	}
	for in, want := range cases {
		if got := NormalizeHost(in); got != want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOnPlatform(t *testing.T) {
	if !OnPlatform("lombardis.quiccpos.com", "quiccpos.com") || OnPlatform("evilquiccpos.com", "quiccpos.com") ||
		OnPlatform("order.example.com", "quiccpos.com") {
		t.Fatal("OnPlatform")
	}
}
