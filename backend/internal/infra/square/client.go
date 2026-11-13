// Package square is a small client for the parts of Square's API main uses
// (weborder stage 2): OAuth (a store connects its Square account), payments
// (authorize at checkout, raise / complete / cancel later, refund), customers
// and cards (a card saved for a late tip) and locations. Card data never
// reaches main: the storefront's Web Payments SDK turns the card into a
// one-time token, and main only keeps Square's ids.
//
// The base URL is Square's (production / sandbox) or the in-process fake
// (package squarefake) on dev and in tests.
package square

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Version is the Square-Version header main is written against.
const Version = "2026-09-16"

const (
	ProductionURL = "https://connect.squareup.com"
	SandboxURL    = "https://connect.squareupsandbox.com"
)

// Payment statuses.
const (
	StatusApproved  = "APPROVED"
	StatusCompleted = "COMPLETED"
	StatusCanceled  = "CANCELED"
	StatusFailed    = "FAILED"
	StatusPending   = "PENDING"
)

// Payment capabilities (what UpdatePayment may still change).
const (
	CapTipUp    = "EDIT_TIP_AMOUNT_UP"
	CapAmountUp = "EDIT_AMOUNT_UP"
)

// ScopeInPerson lets the counter terminal's Square Reader (the Mobile Payments
// SDK, authorized with the store's token: agent contract §2.10r) take payments.
const ScopeInPerson = "PAYMENTS_WRITE_IN_PERSON"

// Scopes main asks a store for. A store that connected before ScopeInPerson
// was added reconnects once (Store › Settings › Payments says so).
var Scopes = []string{"PAYMENTS_WRITE", "PAYMENTS_READ", ScopeInPerson, "MERCHANT_PROFILE_READ", "CUSTOMERS_WRITE", "CUSTOMERS_READ", "DISPUTES_READ"}

// Client talks to one Square environment for one app.
type Client struct {
	BaseURL   string
	AppID     string
	AppSecret string
	HTTP      *http.Client
}

func New(baseURL, appID, appSecret string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), AppID: appID, AppSecret: appSecret, HTTP: hc}
}

// Money is an amount in the smallest unit (cents).
type Money struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

func USD(cents int64) *Money { return &Money{Amount: cents, Currency: "USD"} }

// Payment is the slice of Square's Payment main reads.
type Payment struct {
	ID            string   `json:"id"`
	Status        string   `json:"status"`
	LocationID    string   `json:"location_id,omitempty"`
	CustomerID    string   `json:"customer_id,omitempty"`
	ReferenceID   string   `json:"reference_id,omitempty"`
	AmountMoney   *Money   `json:"amount_money,omitempty"`
	TipMoney      *Money   `json:"tip_money,omitempty"`
	TotalMoney    *Money   `json:"total_money,omitempty"`
	RefundedMoney *Money   `json:"refunded_money,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty"`
	VersionToken  string   `json:"version_token,omitempty"`
	DelayedUntil  string   `json:"delayed_until,omitempty"`
	ReceiptNumber string   `json:"receipt_number,omitempty"`
	CardDetails   *struct {
		Status         string `json:"status"`
		AuthResultCode string `json:"auth_result_code"`
		EntryMethod    string `json:"entry_method"` // KEYED | EMV | CONTACTLESS | SWIPED | ON_FILE
		Card           struct {
			Last4     string `json:"last_4"`
			CardBrand string `json:"card_brand"`
		} `json:"card"`
	} `json:"card_details,omitempty"`
}

// Can reports whether the payment still allows an update.
func (p *Payment) Can(capability string) bool {
	for _, c := range p.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// Last4 / Brand of the card (empty when unknown).
func (p *Payment) Last4() string {
	if p.CardDetails == nil {
		return ""
	}
	return p.CardDetails.Card.Last4
}

func (p *Payment) Brand() string {
	if p.CardDetails == nil {
		return ""
	}
	return p.CardDetails.Card.CardBrand
}

// Entry is how the card was read (Square's entry_method; "" when unknown).
func (p *Payment) Entry() string {
	if p.CardDetails == nil {
		return ""
	}
	return p.CardDetails.EntryMethod
}

func (p *Payment) AuthCode() string {
	if p.CardDetails == nil {
		return ""
	}
	return p.CardDetails.AuthResultCode
}

// RefundAmount is what can still be refunded (total − refunded; 0 when unknown).
func (p *Payment) RefundAmount() int64 {
	total := int64(0)
	if p.TotalMoney != nil {
		total = p.TotalMoney.Amount
	} else if p.AmountMoney != nil {
		total = p.AmountMoney.Amount
		if p.TipMoney != nil {
			total += p.TipMoney.Amount
		}
	}
	if p.RefundedMoney != nil {
		total -= p.RefundedMoney.Amount
	}
	return total
}

// Refund is the slice of Square's PaymentRefund main reads.
type Refund struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	PaymentID   string `json:"payment_id"`
	AmountMoney *Money `json:"amount_money"`
}

// Location is one of the seller's Square locations.
type Location struct {
	ID           string   `json:"id"`
	MerchantID   string   `json:"merchant_id,omitempty"`
	Name         string   `json:"name"`
	Status       string   `json:"status"`
	Currency     string   `json:"currency"`
	Capabilities []string `json:"capabilities"`
}

// TakesCards reports whether payments can go to the location.
func (l Location) TakesCards() bool {
	if l.Status != "ACTIVE" || (l.Currency != "" && l.Currency != "USD") {
		return false
	}
	for _, c := range l.Capabilities {
		if c == "CREDIT_CARD_PROCESSING" {
			return true
		}
	}
	return false
}

// Token is an OAuth token answer.
type Token struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    string `json:"expires_at"`
	MerchantID   string `json:"merchant_id"`
}

// Expires is ExpiresAt parsed (zero when missing).
func (t Token) Expires() time.Time {
	at, _ := time.Parse(time.RFC3339, t.ExpiresAt)
	return at
}

// ErrorItem is one of Square's errors.
type ErrorItem struct {
	Category string `json:"category"`
	Code     string `json:"code"`
	Detail   string `json:"detail"`
	Field    string `json:"field,omitempty"`
}

// APIError is a non-2xx answer.
type APIError struct {
	Status int
	Errors []ErrorItem
}

func (e *APIError) Error() string {
	if len(e.Errors) == 0 {
		return fmt.Sprintf("square: HTTP %d", e.Status)
	}
	return fmt.Sprintf("square: HTTP %d %s: %s", e.Status, e.Errors[0].Code, e.Errors[0].Detail)
}

// Code is the first error's code.
func (e *APIError) Code() string {
	if len(e.Errors) == 0 {
		return ""
	}
	return e.Errors[0].Code
}

// CodeOf is err's Square error code ("" when it isn't a Square answer).
func CodeOf(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Code()
	}
	return ""
}

// StatusOf is err's HTTP status (0 when it isn't a Square answer).
func StatusOf(err error) int {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status
	}
	return 0
}

// Declined reports whether err is the card being refused (show the buyer).
func Declined(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	if ae.Status == http.StatusPaymentRequired {
		return true
	}
	for _, e := range ae.Errors {
		if e.Category == "PAYMENT_METHOD_ERROR" {
			return true
		}
	}
	switch ae.Code() {
	case "GENERIC_DECLINE", "CARD_DECLINED", "CVV_FAILURE", "ADDRESS_VERIFICATION_FAILURE", "INSUFFICIENT_FUNDS",
		"INVALID_EXPIRATION", "CARD_EXPIRED", "CARD_NOT_SUPPORTED", "INVALID_CARD", "INVALID_CARD_DATA",
		"CARD_TOKEN_USED", "CARD_TOKEN_EXPIRED", "TRANSACTION_LIMIT", "PAN_FAILURE", "EXPIRATION_FAILURE",
		"CARD_DECLINED_CALL_ISSUER":
		return true
	}
	return false
}

// Temporary reports whether retrying with the same idempotency key is safe
// and may work (Square's TEMPORARY_ERROR / 5xx / a network error).
func Temporary(err error) bool {
	if err == nil {
		return false
	}
	var ae *APIError
	if !errors.As(err, &ae) {
		return true // network / timeout: we don't know, the same key is safe
	}
	return ae.Status >= 500 || ae.Code() == "TEMPORARY_ERROR" || ae.Status == http.StatusTooManyRequests
}

func (c *Client) do(ctx context.Context, method, path, bearer string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Square-Version", Version)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("square %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("square %s %s: %w", method, path, err)
	}
	if resp.StatusCode/100 != 2 {
		ae := &APIError{Status: resp.StatusCode}
		var eb struct {
			Errors []ErrorItem `json:"errors"`
			// OAuth answers errors as {type, message}.
			Type    string `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &eb) == nil {
			ae.Errors = eb.Errors
			if len(ae.Errors) == 0 && eb.Type != "" {
				ae.Errors = []ErrorItem{{Category: "AUTHENTICATION_ERROR", Code: strings.ToUpper(eb.Type), Detail: eb.Message}}
			}
		}
		return ae
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func bearer(token string) string { return "Bearer " + token }

// --- OAuth -------------------------------------------------------------------------

// AuthorizeURL is where a store owner is sent to connect their Square account.
// redirect is the callback registered for the app (sent so the fake knows
// where to go back; Square checks it against the registered one).
func (c *Client) AuthorizeURL(state, redirect string, production bool) string {
	q := url.Values{}
	q.Set("client_id", c.AppID)
	q.Set("scope", strings.Join(Scopes, " "))
	q.Set("state", state)
	if redirect != "" {
		q.Set("redirect_uri", redirect)
	}
	if production {
		q.Set("session", "false")
	}
	return c.BaseURL + "/oauth2/authorize?" + q.Encode()
}

// ExchangeCode turns the callback's code into tokens (5 minutes, single use).
func (c *Client) ExchangeCode(ctx context.Context, code, redirect string) (Token, error) {
	body := map[string]string{"client_id": c.AppID, "client_secret": c.AppSecret, "grant_type": "authorization_code", "code": code}
	if redirect != "" {
		body["redirect_uri"] = redirect
	}
	var t Token
	err := c.do(ctx, http.MethodPost, "/oauth2/token", "", body, &t)
	return t, err
}

// Refresh gets a new access token (code-flow refresh tokens don't expire;
// Square answers a refresh token, kept when it changes).
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	var t Token
	err := c.do(ctx, http.MethodPost, "/oauth2/token", "", map[string]string{"client_id": c.AppID, "client_secret": c.AppSecret,
		"grant_type": "refresh_token", "refresh_token": refreshToken}, &t)
	if err == nil && t.RefreshToken == "" {
		t.RefreshToken = refreshToken
	}
	return t, err
}

// TokenInfo is RetrieveTokenStatus's answer: what an access token may do.
type TokenInfo struct {
	Scopes     []string `json:"scopes"`
	ExpiresAt  string   `json:"expires_at"`
	ClientID   string   `json:"client_id"`
	MerchantID string   `json:"merchant_id"`
}

// Has reports whether the token was granted scope.
func (t TokenInfo) Has(scope string) bool {
	for _, s := range t.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// TokenStatus reads an access token's scopes and expiry (POST /oauth2/token/status).
func (c *Client) TokenStatus(ctx context.Context, accessToken string) (TokenInfo, error) {
	var t TokenInfo
	err := c.do(ctx, http.MethodPost, "/oauth2/token/status", bearer(accessToken), map[string]any{}, &t)
	return t, err
}

// Revoke ends main's access to the seller's account.
func (c *Client) Revoke(ctx context.Context, accessToken string) error {
	return c.do(ctx, http.MethodPost, "/oauth2/revoke", "Client "+c.AppSecret,
		map[string]string{"client_id": c.AppID, "access_token": accessToken}, nil)
}

// --- locations ---------------------------------------------------------------------

func (c *Client) Locations(ctx context.Context, token string) ([]Location, error) {
	var out struct {
		Locations []Location `json:"locations"`
	}
	err := c.do(ctx, http.MethodGet, "/v2/locations", bearer(token), nil, &out)
	return out.Locations, err
}

// --- payments ----------------------------------------------------------------------

// CreatePayment is a CreatePayment request (Autocomplete false = a hold).
type CreatePayment struct {
	IdempotencyKey    string `json:"idempotency_key"`
	SourceID          string `json:"source_id"`
	AmountMoney       *Money `json:"amount_money"`
	TipMoney          *Money `json:"tip_money,omitempty"`
	Autocomplete      bool   `json:"autocomplete"`
	LocationID        string `json:"location_id,omitempty"`
	CustomerID        string `json:"customer_id,omitempty"`
	ReferenceID       string `json:"reference_id,omitempty"`
	Note              string `json:"note,omitempty"`
	BuyerEmailAddress string `json:"buyer_email_address,omitempty"`
	VerificationToken string `json:"verification_token,omitempty"`
	// CustomerDetails {customer_initiated: false, seller_keyed_in: true} = a
	// card staff typed in (Mail Order / Telephone Order: the counter terminal's
	// manual entry, agent contract §2.10r).
	CustomerDetails *CustomerDetails `json:"customer_details,omitempty"`
}

// CustomerDetails says who started a payment and how the card was given.
type CustomerDetails struct {
	CustomerInitiated bool `json:"customer_initiated"`
	SellerKeyedIn     bool `json:"seller_keyed_in"`
}

func (c *Client) CreatePayment(ctx context.Context, token string, req CreatePayment) (*Payment, error) {
	var out struct {
		Payment *Payment `json:"payment"`
	}
	if err := c.do(ctx, http.MethodPost, "/v2/payments", bearer(token), req, &out); err != nil {
		return nil, err
	}
	return out.Payment, nil
}

func (c *Client) GetPayment(ctx context.Context, token, id string) (*Payment, error) {
	var out struct {
		Payment *Payment `json:"payment"`
	}
	if err := c.do(ctx, http.MethodGet, "/v2/payments/"+url.PathEscape(id), bearer(token), nil, &out); err != nil {
		return nil, err
	}
	return out.Payment, nil
}

// UpdatePayment changes a hold's amount and/or tip (only while APPROVED and
// only as payment.capabilities allow; else AMOUNT_TOO_HIGH).
func (c *Client) UpdatePayment(ctx context.Context, token, id, idempotencyKey string, amount, tip *Money) (*Payment, error) {
	p := map[string]any{}
	if amount != nil {
		p["amount_money"] = amount
	}
	if tip != nil {
		p["tip_money"] = tip
	}
	var out struct {
		Payment *Payment `json:"payment"`
	}
	err := c.do(ctx, http.MethodPut, "/v2/payments/"+url.PathEscape(id), bearer(token),
		map[string]any{"idempotency_key": idempotencyKey, "payment": p}, &out)
	if err != nil {
		return nil, err
	}
	return out.Payment, nil
}

// CompletePayment captures a hold.
func (c *Client) CompletePayment(ctx context.Context, token, id string) (*Payment, error) {
	var out struct {
		Payment *Payment `json:"payment"`
	}
	if err := c.do(ctx, http.MethodPost, "/v2/payments/"+url.PathEscape(id)+"/complete", bearer(token), map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Payment, nil
}

// CancelPayment voids a hold.
func (c *Client) CancelPayment(ctx context.Context, token, id string) (*Payment, error) {
	var out struct {
		Payment *Payment `json:"payment"`
	}
	if err := c.do(ctx, http.MethodPost, "/v2/payments/"+url.PathEscape(id)+"/cancel", bearer(token), map[string]any{}, &out); err != nil {
		return nil, err
	}
	return out.Payment, nil
}

// CancelByIdempotencyKey voids whatever a CreatePayment with that key made
// (succeeds even when it made nothing).
func (c *Client) CancelByIdempotencyKey(ctx context.Context, token, key string) error {
	return c.do(ctx, http.MethodPost, "/v2/payments/cancel", bearer(token), map[string]string{"idempotency_key": key}, nil)
}

// RefundPayment refunds (part of) a completed payment.
func (c *Client) RefundPayment(ctx context.Context, token, idempotencyKey, paymentID string, amount *Money, reason string) (*Refund, error) {
	var out struct {
		Refund *Refund `json:"refund"`
	}
	body := map[string]any{"idempotency_key": idempotencyKey, "payment_id": paymentID, "amount_money": amount}
	if reason != "" {
		body["reason"] = reason
	}
	if err := c.do(ctx, http.MethodPost, "/v2/refunds", bearer(token), body, &out); err != nil {
		return nil, err
	}
	return out.Refund, nil
}

// --- customers + cards (a card saved for a late tip) -------------------------------

// Customer is the slice of Square's Customer main writes.
type Customer struct {
	ID           string `json:"id,omitempty"`
	GivenName    string `json:"given_name,omitempty"`
	PhoneNumber  string `json:"phone_number,omitempty"`
	EmailAddress string `json:"email_address,omitempty"`
	ReferenceID  string `json:"reference_id,omitempty"`
	Note         string `json:"note,omitempty"`
}

func (c *Client) CreateCustomer(ctx context.Context, token, idempotencyKey string, cu Customer) (*Customer, error) {
	body := map[string]any{"idempotency_key": idempotencyKey, "given_name": cu.GivenName, "reference_id": cu.ReferenceID, "note": cu.Note}
	if cu.PhoneNumber != "" {
		body["phone_number"] = cu.PhoneNumber
	}
	if cu.EmailAddress != "" {
		body["email_address"] = cu.EmailAddress
	}
	var out struct {
		Customer *Customer `json:"customer"`
	}
	if err := c.do(ctx, http.MethodPost, "/v2/customers", bearer(token), body, &out); err != nil {
		return nil, err
	}
	return out.Customer, nil
}

// Card is a card on file.
type Card struct {
	ID         string `json:"id"`
	CustomerID string `json:"customer_id"`
	Last4      string `json:"last_4"`
	CardBrand  string `json:"card_brand"`
	ExpMonth   int    `json:"exp_month"`
	ExpYear    int    `json:"exp_year"`
	Enabled    bool   `json:"enabled"`
}

// CreateCard saves the card behind sourceID (a payment id or a token) for a
// customer; verification is verifyBuyer's token for a STORE intent, if any.
func (c *Client) CreateCard(ctx context.Context, token, idempotencyKey, sourceID, customerID, verification string) (*Card, error) {
	var out struct {
		Card *Card `json:"card"`
	}
	body := map[string]any{"idempotency_key": idempotencyKey, "source_id": sourceID, "card": map[string]string{"customer_id": customerID}}
	if verification != "" {
		body["verification_token"] = verification
	}
	err := c.do(ctx, http.MethodPost, "/v2/cards", bearer(token), body, &out)
	if err != nil {
		return nil, err
	}
	return out.Card, nil
}

// DisableCard turns a saved card off (it can't be charged again).
func (c *Client) DisableCard(ctx context.Context, token, cardID string) error {
	return c.do(ctx, http.MethodPost, "/v2/cards/"+url.PathEscape(cardID)+"/disable", bearer(token), map[string]any{}, nil)
}
