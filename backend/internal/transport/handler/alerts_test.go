package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/pushover"
	appMiddleware "quiccpos/main/internal/transport/middleware"
	"quiccpos/main/internal/transport/session"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

type fakeLocs struct{ cfg tenancy.Config }

func (f fakeLocs) GetLocation(_ context.Context, id uuid.UUID) (*tenancy.Location, error) {
	return &tenancy.Location{ID: id, Config: f.cfg}, nil
}

type fakeSender struct {
	enabled bool
	err     error
	sent    [][]string
	msgs    []string
}

func (f *fakeSender) Enabled() bool { return f.enabled }
func (f *fakeSender) SendDevices(_ context.Context, devices []string, _, message, _ string) error {
	f.sent = append(f.sent, devices)
	f.msgs = append(f.msgs, message)
	return f.err
}

func runAlertTest(t *testing.T, h *AlertsHandler, lid uuid.UUID) (int, string) {
	t.Helper()
	signer, err := session.New("test-secret-at-least-16-bytes-long", false)
	if err != nil {
		t.Fatal(err)
	}
	issued := httptest.NewRecorder()
	signer.Issue(issued, nil, uuid.New(), lid)
	req := httptest.NewRequest(http.MethodPost, "/store/v1/me/alerts/test", nil)
	req.AddCookie(issued.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	if err := appMiddleware.StoreSessionAuth(signer)(h.MeTest)(echo.New().NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	code, _ := body["code"].(string)
	return rec.Code, code
}

func TestAlertTestAnswers(t *testing.T) {
	saved := tenancy.Config{tenancy.OwnerDevicesKey: "OwnerPhone,Tablet2"}
	for name, tc := range map[string]struct {
		cfg    tenancy.Config
		sender *fakeSender
		status int
		code   string
	}{
		"no devices":        {tenancy.Config{}, &fakeSender{enabled: true}, 400, "no_devices"},
		"empty devices":     {tenancy.Config{tenancy.OwnerDevicesKey: ""}, &fakeSender{enabled: true}, 400, "no_devices"},
		"not configured":    {saved, &fakeSender{}, 503, "not_configured"},
		"sent":              {saved, &fakeSender{enabled: true}, 200, ""},
		"device not found":  {saved, &fakeSender{enabled: true, err: &pushover.RejectedError{Status: 400, Device: "invalid"}}, 400, "device_not_found"},
		"bad user key":      {saved, &fakeSender{enabled: true, err: &pushover.RejectedError{Status: 400, Errors: []string{"user key is invalid"}}}, 502, "pushover_rejected"},
		"pushover down":     {saved, &fakeSender{enabled: true, err: &pushover.RejectedError{Status: 503}}, 502, "pushover_unreachable"},
		"network error":     {saved, &fakeSender{enabled: true, err: errors.New("dial tcp: timeout")}, 502, "pushover_unreachable"},
		"lost token midway": {saved, &fakeSender{enabled: true, err: pushover.ErrNotConfigured}, 503, "not_configured"},
	} {
		h := NewAlertsHandler(fakeLocs{tc.cfg}, tc.sender, zerolog.Nop())
		status, code := runAlertTest(t, h, uuid.New())
		if status != tc.status || code != tc.code {
			t.Errorf("%s: got %d %q, want %d %q", name, status, code, tc.status, tc.code)
		}
	}

	s := &fakeSender{enabled: true}
	h := NewAlertsHandler(fakeLocs{saved}, s, zerolog.Nop())
	lid := uuid.New()
	if status, _ := runAlertTest(t, h, lid); status != 200 {
		t.Fatalf("first test: %d", status)
	}
	if len(s.sent) != 1 || len(s.sent[0]) != 2 || s.sent[0][0] != "OwnerPhone" || s.msgs[0] != TestAlertMessage {
		t.Fatalf("sent %v %v", s.sent, s.msgs)
	}
	if status, code := runAlertTest(t, h, lid); status != 429 || code != "too_soon" {
		t.Fatalf("second test within 10 s: %d %q, want 429 too_soon", status, code)
	}
	if status, _ := runAlertTest(t, h, uuid.New()); status != 200 {
		t.Fatalf("another location is not limited: %d", status)
	}
	if len(s.sent) != 2 {
		t.Fatalf("the limited call must not send: %d sends", len(s.sent))
	}
}

func TestLocationErrCodesBadDevice(t *testing.T) {
	if got := locationErr(&tenancy.BadDeviceError{Name: "x y"}); got["code"] != "bad_device" {
		t.Fatalf("got %v", got)
	}
	if got := locationErr(errors.New("name is required")); got["code"] != "" || got["error"] != "name is required" {
		t.Fatalf("got %v", got)
	}
}
