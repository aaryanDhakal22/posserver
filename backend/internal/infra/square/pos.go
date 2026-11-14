package square

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Square's Point of Sale API ("Send to phone", agent contract §2.10z): a
// charge made in the Square app answers a transaction id, which is the
// Orders API's order id; the order's tenders carry the payment id and the
// card. A charge whose answer was lost is found among the location's
// payments by its note / amount.

// CardDetailsLite is a tender's / payment's card.
type CardDetailsLite struct {
	EntryMethod string `json:"entry_method"`
	Card        struct {
		Last4     string `json:"last_4"`
		CardBrand string `json:"card_brand"`
	} `json:"card"`
}

// PosTender is one tender of a Point of Sale order.
type PosTender struct {
	ID          string           `json:"id"`
	PaymentID   string           `json:"payment_id"`
	Type        string           `json:"type"`
	AmountMoney *Money           `json:"amount_money,omitempty"`
	TipMoney    *Money           `json:"tip_money,omitempty"`
	CardDetails *CardDetailsLite `json:"card_details,omitempty"`
}

// PosOrder is the slice of an Order the lookup reads.
type PosOrder struct {
	ID            string      `json:"id"`
	LocationID    string      `json:"location_id"`
	State         string      `json:"state"`
	Tenders       []PosTender `json:"tenders"`
	TotalTipMoney *Money      `json:"total_tip_money,omitempty"`
	CreatedAt     string      `json:"created_at"`
}

// ListedPayment is a payment as ListPayments answers it.
type ListedPayment struct {
	ID            string           `json:"id"`
	Status        string           `json:"status"`
	LocationID    string           `json:"location_id"`
	OrderID       string           `json:"order_id"`
	Note          string           `json:"note"`
	CreatedAt     string           `json:"created_at"`
	ReceiptNumber string           `json:"receipt_number"`
	SourceType    string           `json:"source_type"`
	AmountMoney   *Money           `json:"amount_money,omitempty"`
	TipMoney      *Money           `json:"tip_money,omitempty"`
	TotalMoney    *Money           `json:"total_money,omitempty"`
	RefundedMoney *Money           `json:"refunded_money,omitempty"`
	CardDetails   *CardDetailsLite `json:"card_details,omitempty"`
}

// GetOrder is RetrieveOrder (GET /v2/orders/{id}).
func (c *Client) GetOrder(ctx context.Context, token, id string) (*PosOrder, error) {
	var out struct {
		Order *PosOrder `json:"order"`
	}
	if err := c.do(ctx, http.MethodGet, "/v2/orders/"+url.PathEscape(id), bearer(token), nil, &out); err != nil {
		return nil, err
	}
	return out.Order, nil
}

// ListPayments is ListPayments over [begin, end] for a location, newest
// first (one page of up to 100).
func (c *Client) ListPayments(ctx context.Context, token, location string, begin, end time.Time) ([]ListedPayment, error) {
	q := url.Values{}
	q.Set("begin_time", begin.UTC().Format(time.RFC3339))
	q.Set("end_time", end.UTC().Format(time.RFC3339))
	q.Set("sort_order", "DESC")
	q.Set("limit", strconv.Itoa(100))
	if location != "" {
		q.Set("location_id", location)
	}
	var out struct {
		Payments []ListedPayment `json:"payments"`
	}
	if err := c.do(ctx, http.MethodGet, "/v2/payments?"+q.Encode(), bearer(token), nil, &out); err != nil {
		return nil, err
	}
	return out.Payments, nil
}

// ListCardPayments is one page of ListPayments for a location over [begin,
// end] with the card's last 4 (Square's last_4 filter), newest first; the
// cursor continues it ("" = the first page; the answer's "" = no more). The
// general card refund's fallback (agent contract §2.10x).
func (c *Client) ListCardPayments(ctx context.Context, token, location, last4 string, begin, end time.Time, cursor string) ([]ListedPayment, string, error) {
	q := url.Values{}
	q.Set("begin_time", begin.UTC().Format(time.RFC3339))
	q.Set("end_time", end.UTC().Format(time.RFC3339))
	q.Set("sort_order", "DESC")
	q.Set("limit", strconv.Itoa(100))
	q.Set("last_4", last4)
	if location != "" {
		q.Set("location_id", location)
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	var out struct {
		Payments []ListedPayment `json:"payments"`
		Cursor   string          `json:"cursor"`
	}
	if err := c.do(ctx, http.MethodGet, "/v2/payments?"+q.Encode(), bearer(token), nil, &out); err != nil {
		return nil, "", err
	}
	return out.Payments, out.Cursor, nil
}

// FakePOSCharge makes a Point of Sale transaction in the fake Square (dev:
// SQUARE_ENV=fake only; Square itself has no such route).
func (c *Client) FakePOSCharge(ctx context.Context, token, location string, totalCents int64, note string) (tx, clientTx string, err error) {
	var out struct {
		TransactionID       string `json:"transaction_id"`
		ClientTransactionID string `json:"client_transaction_id"`
	}
	body := map[string]any{"total_cents": totalCents, "note": note, "location_id": location}
	if err := c.do(ctx, http.MethodPost, "/_fake/pos/charge", bearer(token), body, &out); err != nil {
		return "", "", err
	}
	return out.TransactionID, out.ClientTransactionID, nil
}
