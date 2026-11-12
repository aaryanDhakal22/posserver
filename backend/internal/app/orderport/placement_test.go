package orderport

import (
	"net/http"
	"testing"
	"time"

	"quiccpos/main/internal/domain/customer"
	"quiccpos/main/internal/infra/database/repositories"

	"github.com/google/uuid"
)

func TestBodyHashIgnoresTokensButNotTheOrder(t *testing.T) {
	var a Request
	a.ClientOrderID, a.Type, a.Notes = "k1", "pickup", "no onions"
	a.Payment.Method, a.Payment.Token = "card", "cnon:one"
	b := a
	b.Payment.Token, b.Payment.VerificationToken = "cnon:two", "verf:x" // a retry after 3-D Secure
	if bodyHash(a) != bodyHash(b) {
		t.Fatal("the card tokens changed the hash")
	}
	c := a
	c.Notes = "extra onions"
	if bodyHash(a) == bodyHash(c) {
		t.Fatal("a different order hashed the same")
	}
	d := a
	d.Account = &customer.Customer{ID: uuid.New()}
	if bodyHash(a) == bodyHash(d) {
		t.Fatal("the signed-in customer isn't part of the hash")
	}
}

func TestErrorOfAStoredRefusal(t *testing.T) {
	e := refuse(http.StatusConflict, "store_closed", "The store is closed — order for later.")
	p := repositories.Placement{State: repositories.PlacementRefused, Result: resultOf(e)}
	got := errorOf(p)
	if got.Status != e.Status || got.Code != e.Code || got.Message != e.Message {
		t.Fatalf("got %+v", got)
	}
	if errorOf(repositories.Placement{State: repositories.PlacementExpired}).Code != "expired" {
		t.Fatal("an expired placement without a result")
	}
	if errorOf(repositories.Placement{State: repositories.PlacementVoided}).Code != "voided" {
		t.Fatal("a voided placement without a result")
	}
}

func TestDueCheckIsHalfAnHourBefore(t *testing.T) {
	if dueCheck(nil) != nil {
		t.Fatal("ASAP orders have no due check")
	}
	at := time.Date(2026, 11, 10, 18, 0, 0, 0, time.UTC)
	if got := dueCheck(&at); !got.Equal(at.Add(-30 * time.Minute)) {
		t.Fatalf("got %v", got)
	}
}
