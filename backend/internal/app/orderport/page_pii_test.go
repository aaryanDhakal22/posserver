package orderport

import (
	"context"
	"strings"
	"testing"

	channelDom "quiccpos/main/internal/domain/channel"
	"quiccpos/main/internal/domain/notify"

	"github.com/google/uuid"
)

// The owner's "order NOT placed" page names the order, never the customer
// (HARDENING-PLAN L-4).
func TestNotPlacedPageHasNoCustomerName(t *testing.T) {
	rec := &notify.Recorder{}
	s := &Service{}
	s.SetNotifier(rec)
	ch := channelDom.Channel{ID: uuid.New(), LocationID: uuid.New(), Name: "Website"}
	var req Request
	req.ClientOrderID = "web-123"
	req.Customer.Name = "Zelda Quartermaine"
	s.notNotify(context.Background(), ch, req, "the card was declined", false)
	got := rec.For(ch.LocationID)
	if len(got) != 1 {
		t.Fatalf("notices = %d, want 1", len(got))
	}
	text := got[0].Title + " " + got[0].Message
	if strings.Contains(text, "Zelda") || strings.Contains(text, "Quartermaine") {
		t.Fatalf("page names the customer: %s", text)
	}
	if !strings.Contains(text, "web-123") {
		t.Fatalf("page lost the order key: %s", text)
	}
}
