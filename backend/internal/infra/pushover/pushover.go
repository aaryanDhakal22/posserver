// Package pushover sends main's own operational pages (a restaurant's Pi went
// offline during opening hours, a Pi was replaced and restored). It reuses the
// agent's Pushover app + recipients (AGENT_SECRET_PUSHOVER_*), so the owner and
// the boss get one consistent stream of floor problems. With no app token or
// recipients configured every Send is a no-op. SendDevices targets device
// names on the first recipient's account (the store owner's alert test).
package pushover

import (
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

const endpoint = "https://api.pushover.net/1/messages.json"

// ErrNotConfigured: no app token or recipient key, nothing can be sent.
var ErrNotConfigured = errors.New("pushover is not configured")

// RejectedError is a non-200 answer from Pushover. A 4xx carries Pushover's
// own `errors` text (a bad device name, a bad user key).
type RejectedError struct {
	Status int
	Errors []string
	// Device is Pushover's `device` field ("invalid" for an unknown name).
	Device string
}

func (e *RejectedError) Error() string { return fmt.Sprintf("pushover answered %d", e.Status) }

// DeviceNotFound reports whether Pushover refused the device name(s): they
// are not devices of that user.
func (e *RejectedError) DeviceNotFound() bool {
	if e.Status < 400 || e.Status >= 500 {
		return false
	}
	if e.Device != "" {
		return true
	}
	for _, m := range e.Errors {
		if strings.Contains(strings.ToLower(m), "device") {
			return true
		}
	}
	return false
}

type Client struct {
	token      string
	recipients []string
	// devices scopes the first recipient (the user key) to the admin's
	// phones (AGENT_SECRET_PUSHOVER_ADMIN_DEVICE): drivers and the store
	// owner share that account. Empty = every device, as before 2.2.3.
	devices  []string
	http     *http.Client
	endpoint string
}

// New builds a client for app `token` paging every non-empty recipient key.
func New(token string, recipients ...string) *Client {
	var rs []string
	for _, r := range recipients {
		if r = strings.TrimSpace(r); r != "" {
			rs = append(rs, r)
		}
	}
	return &Client{
		token:      strings.TrimSpace(token),
		recipients: rs,
		http:       &http.Client{Timeout: 10 * time.Second},
		endpoint:   endpoint,
	}
}

// SetEndpoint points the client at another messages URL (PUSHOVER_API_URL: a
// test fake); "" keeps the real one.
func (c *Client) SetEndpoint(u string) {
	if u = strings.TrimSpace(u); u != "" {
		c.endpoint = u
	}
}

// SetDevices scopes the user key's pages to these devices (see devices).
func (c *Client) SetDevices(devices []string) { c.devices = devices }

// Enabled reports whether Send would page anyone.
func (c *Client) Enabled() bool { return c != nil && c.token != "" && len(c.recipients) > 0 }

// Send pages every recipient; the first error is returned (the rest still go).
func (c *Client) Send(ctx context.Context, title, message, sound string) error {
	return c.SendAlso(ctx, nil, title, message, sound)
}

// SendAlso is Send with more devices on the user key (a store owner's
// phones for a floor page) — only when the user key is scoped at all.
func (c *Client) SendAlso(ctx context.Context, extra []string, title, message, sound string) error {
	if !c.Enabled() {
		return nil
	}
	device := ""
	if len(c.devices) > 0 {
		seen := map[string]bool{}
		var ds []string
		for _, d := range append(append([]string(nil), c.devices...), extra...) {
			if !seen[d] {
				seen[d] = true
				ds = append(ds, d)
			}
		}
		device = strings.Join(ds, ",")
	}
	var firstErr error
	for i, user := range c.recipients {
		d := device
		if i > 0 {
			d = "" // the boss key is its own account
		}
		if err := c.post(ctx, user, d, title, message, sound); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// SendDevices sends one message to the named devices (Pushover's comma list)
// of the first recipient's account. ErrNotConfigured without a token or key;
// a *RejectedError for a non-200 answer; anything else is a network error.
func (c *Client) SendDevices(ctx context.Context, devices []string, title, message, sound string) error {
	if !c.Enabled() {
		return ErrNotConfigured
	}
	return c.post(ctx, c.recipients[0], strings.Join(devices, ","), title, message, sound)
}

func (c *Client) post(ctx context.Context, user, device, title, message, sound string) error {
	form := url.Values{"token": {c.token}, "user": {user}, "message": {message}}
	if device != "" {
		form.Set("device", device)
	}
	if title != "" {
		form.Set("title", title)
	}
	if sound != "" {
		form.Set("sound", sound)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	rej := &RejectedError{Status: resp.StatusCode}
	var body struct {
		Device string   `json:"device"`
		Errors []string `json:"errors"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body) == nil {
		rej.Device, rej.Errors = body.Device, body.Errors
	}
	return rej
}
