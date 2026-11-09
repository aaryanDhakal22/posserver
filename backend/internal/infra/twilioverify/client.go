// Package twilioverify sends and checks sign-in codes through Twilio Verify
// (weborder stage 2): a text, or a call that reads the code. Twilio picks the
// sending number (no 10DLC registration of our own).
package twilioverify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is Twilio Verify's API.
const DefaultBaseURL = "https://verify.twilio.com"

type Client struct {
	BaseURL    string
	AccountSID string
	AuthToken  string
	ServiceSID string
	HTTP       *http.Client
}

func New(baseURL, accountSID, authToken, serviceSID string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), AccountSID: accountSID, AuthToken: authToken, ServiceSID: serviceSID,
		HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// Error is a Twilio refusal.
type Error struct {
	Status  int
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("twilio verify: HTTP %d (%d) %s", e.Status, e.Code, e.Message)
}

func (c *Client) post(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.AccountSID, c.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		e := &Error{Status: resp.StatusCode}
		_ = json.Unmarshal(raw, e)
		return e
	}
	return json.Unmarshal(raw, out)
}

// Send starts a verification: channel "sms" or "call" to an E.164 number.
func (c *Client) Send(ctx context.Context, to, channel string) error {
	var out struct {
		Status string `json:"status"`
	}
	return c.post(ctx, "/v2/Services/"+url.PathEscape(c.ServiceSID)+"/Verifications",
		url.Values{"To": {to}, "Channel": {channel}}, &out)
}

// Check reports whether code is the one sent to the number. A wrong code is
// (false, nil); a verification that expired or ran out of tries is 404.
func (c *Client) Check(ctx context.Context, to, code string) (bool, error) {
	var out struct {
		Status string `json:"status"`
		Valid  bool   `json:"valid"`
	}
	err := c.post(ctx, "/v2/Services/"+url.PathEscape(c.ServiceSID)+"/VerificationCheck",
		url.Values{"To": {to}, "Code": {code}}, &out)
	if e, ok := err.(*Error); ok && e.Status == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return out.Status == "approved", nil
}
