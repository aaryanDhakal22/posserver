package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"quiccpos/main/internal/domain/site"
	sseTransport "quiccpos/main/internal/transport/sse"

	"github.com/google/uuid"
)

// store_config's receipt_logo: left out with no logo or an SVG one (the
// frame stays byte-identical for older agents), {id, etag, content_type}
// for a PNG.
func TestStoreConfigReceiptLogo(t *testing.T) {
	marshal := func(img *site.Image) string {
		b, err := json.Marshal(sseTransport.StoreContext{ReceiptLogo: receiptLogoContext(img)})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := marshal(nil); strings.Contains(got, "receipt_logo") {
		t.Fatalf("no logo: %s", got)
	}
	svg := &site.Image{ID: uuid.New(), Kind: "logo", ContentType: "image/svg+xml", Bytes: []byte("<svg/>")}
	if got := marshal(svg); strings.Contains(got, "receipt_logo") {
		t.Fatalf("svg logo: %s", got)
	}
	png := &site.Image{ID: uuid.New(), Kind: "logo", ContentType: "image/png", Bytes: []byte("\x89PNG\r\n\x1a\nxx")}
	var out struct {
		ReceiptLogo map[string]string `json:"receipt_logo"`
	}
	if err := json.Unmarshal([]byte(marshal(png)), &out); err != nil {
		t.Fatal(err)
	}
	l := out.ReceiptLogo
	if l["id"] != png.ID.String() || l["content_type"] != "image/png" || !strings.HasPrefix(l["etag"], `"l`) || len(l) != 3 {
		t.Fatalf("png logo: %v", l)
	}
}

func TestEtagMatches(t *testing.T) {
	const e = `"labc"`
	for h, want := range map[string]bool{`"labc"`: true, `labc`: true, `W/"labc"`: true, `"x", "labc"`: true, `*`: true, `"lother"`: false, ``: false} {
		if etagMatches(h, e) != want {
			t.Errorf("etagMatches(%q) = %v", h, !want)
		}
	}
}
