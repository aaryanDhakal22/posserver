// Package pushover sends main's own operational pages (a restaurant's Pi went
// offline during opening hours, a Pi was replaced and restored). It reuses the
// agent's Pushover app + recipients (AGENT_SECRET_PUSHOVER_*), so the owner and
// the boss get one consistent stream of floor problems. With no app token or
// recipients configured every Send is a no-op.
package pushover

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const endpoint = "https://api.pushover.net/1/messages.json"

type Client struct {
	token      string
	recipients []string
	http       *http.Client
	endpoint   string
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

// Enabled reports whether Send would page anyone.
func (c *Client) Enabled() bool { return c != nil && c.token != "" && len(c.recipients) > 0 }

// Send pages every recipient; the first error is returned (the rest still go).
func (c *Client) Send(ctx context.Context, title, message, sound string) error {
	if !c.Enabled() {
		return nil
	}
	var firstErr error
	for _, user := range c.recipients {
		form := url.Values{"token": {c.token}, "user": {user}, "message": {message}}
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
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK && firstErr == nil {
			firstErr = fmt.Errorf("pushover answered %d", resp.StatusCode)
		}
	}
	return firstErr
}
