// Package mail sends email (weborder stage 2: customer receipts). SES goes
// through its v2 HTTP API signed with SigV4 (the AWS core module main already
// has — no extra SDK); Fake keeps messages in memory for dev and tests.
package mail

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// Message is one email.
type Message struct {
	FromName, FromAddress string
	ReplyTo               string // "" = none
	To                    string
	Subject               string
	Text, HTML            string
}

// Sender delivers email; it returns the provider's message id.
type Sender interface {
	Send(ctx context.Context, m Message) (string, error)
}

// From is the From header: "Name" <address>, the name RFC 2047-encoded when needed.
func (m Message) From() string {
	name := strings.NewReplacer(`"`, "", "\r", "", "\n", "").Replace(m.FromName)
	if name == "" {
		return m.FromAddress
	}
	return mime.QEncoding.Encode("utf-8", name) + " <" + m.FromAddress + ">"
}

// --- SES (v2 SendEmail over HTTPS, SigV4) -------------------------------------------------

type SES struct {
	Region string
	Creds  aws.CredentialsProvider
	HTTP   *http.Client
	signer *v4.Signer
	// Endpoint overrides https://email.<region>.amazonaws.com (tests).
	Endpoint string
}

func NewSES(region string, creds aws.CredentialsProvider) *SES {
	return &SES{Region: region, Creds: creds, HTTP: &http.Client{Timeout: 20 * time.Second}, signer: v4.NewSigner()}
}

// SendError is SES refusing a message (Permanent = retrying won't help).
type SendError struct {
	Status    int
	Type      string
	Message   string
	Permanent bool
}

func (e *SendError) Error() string {
	return fmt.Sprintf("ses: HTTP %d %s: %s", e.Status, e.Type, e.Message)
}

func (s *SES) Send(ctx context.Context, m Message) (string, error) {
	content := map[string]any{"Subject": map[string]string{"Data": m.Subject, "Charset": "UTF-8"},
		"Body": map[string]any{"Text": map[string]string{"Data": m.Text, "Charset": "UTF-8"}}}
	if m.HTML != "" {
		content["Body"].(map[string]any)["Html"] = map[string]string{"Data": m.HTML, "Charset": "UTF-8"}
	}
	body := map[string]any{"FromEmailAddress": m.From(), "Destination": map[string]any{"ToAddresses": []string{m.To}},
		"Content": map[string]any{"Simple": content}}
	if m.ReplyTo != "" {
		body["ReplyToAddresses"] = []string{m.ReplyTo}
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = "https://email." + s.Region + ".amazonaws.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(endpoint, "/")+"/v2/email/outbound-emails", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	creds, err := s.Creds.Retrieve(ctx)
	if err != nil {
		return "", fmt.Errorf("ses credentials: %w", err)
	}
	sum := sha256.Sum256(raw)
	if err := s.signer.SignHTTP(ctx, creds, req, hex.EncodeToString(sum[:]), "ses", s.Region, time.Now()); err != nil {
		return "", err
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(out, &e)
		typ := resp.Header.Get("X-Amzn-ErrorType")
		if i := strings.IndexByte(typ, ':'); i >= 0 {
			typ = typ[:i]
		}
		perm := resp.StatusCode == http.StatusBadRequest && typ != "TooManyRequestsException" && typ != "LimitExceededException"
		return "", &SendError{Status: resp.StatusCode, Type: typ, Message: e.Message, Permanent: perm}
	}
	var ok struct {
		MessageID string `json:"MessageId"`
	}
	_ = json.Unmarshal(out, &ok)
	return ok.MessageID, nil
}

// --- Fake ---------------------------------------------------------------------------------

// Fake keeps sent messages in memory (dev, tests); to "fail@…" fails.
type Fake struct {
	mu   sync.Mutex
	sent []Sent
}

// Sent is a message the fake took.
type Sent struct {
	Message
	ID string    `json:"id"`
	At time.Time `json:"at"`
}

func NewFake() *Fake { return &Fake{} }

func (f *Fake) Send(_ context.Context, m Message) (string, error) {
	if strings.HasPrefix(m.To, "fail@") {
		return "", &SendError{Status: 400, Type: "MessageRejected", Message: "fake: rejected", Permanent: true}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	id := fmt.Sprintf("fake-%d", len(f.sent)+1)
	f.sent = append(f.sent, Sent{Message: m, ID: id, At: time.Now()})
	return id, nil
}

// To lists what was sent to an address, oldest first.
func (f *Fake) To(addr string) []Sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Sent
	for _, s := range f.sent {
		if strings.EqualFold(s.To, addr) {
			out = append(out, s)
		}
	}
	return out
}
