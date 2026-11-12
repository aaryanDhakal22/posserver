package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

func readyCall(t *testing.T, h *ReadinessHandler) (int, readinessResponse, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := h.Check(echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil), rec)); err != nil {
		t.Fatal(err)
	}
	var body readinessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return rec.Code, body, rec.Body.String()
}

// The probe is open to anyone: a flood of calls is one Postgres ping and one
// AWS call per cache window, and a failure says which check, never why.
func TestReadinessCachedAndGeneric(t *testing.T) {
	now := time.Date(2026, 11, 12, 20, 0, 0, 0, time.UTC)
	probes := 0
	var dbErr, sqsErr error
	h := NewReadinessHandler(nil, nil, "", 5*time.Second, zerolog.Nop())
	h.now = func() time.Time { return now }
	h.probe = func(context.Context) (error, error) { probes++; return dbErr, sqsErr }

	for i := 0; i < 50; i++ {
		if code, body, _ := readyCall(t, h); code != http.StatusOK || !body.Ready || !body.DB.OK || !body.SQS.OK {
			t.Fatalf("call %d: %d %+v", i, code, body)
		}
	}
	if probes != 1 {
		t.Fatalf("50 calls inside the window ran %d probes, want 1", probes)
	}

	// SQS goes away with an error full of detail.
	sqsErr = errors.New("operation error SQS: GetQueueAttributes, https response error StatusCode: 403, RequestID: 1f2e, api error AccessDenied: User: arn:aws:iam::123456789012:user/quicc is not authorized on arn:aws:sqs:us-east-1:123456789012:prod-queue")
	if code, _, _ := readyCall(t, h); code != http.StatusOK {
		t.Fatalf("inside the window the cached answer stands, got %d", code)
	}
	now = now.Add(5 * time.Second)
	code, body, raw := readyCall(t, h)
	if code != http.StatusServiceUnavailable || body.Ready || !body.DB.OK || body.SQS.OK || body.SQS.Err != "unavailable" {
		t.Fatalf("after the window: %d %+v", code, body)
	}
	for _, leak := range []string{"arn:", "123456789012", "AccessDenied", "RequestID", "prod-queue"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("the body leaks %q: %s", leak, raw)
		}
	}
	if probes != 2 {
		t.Fatalf("probes = %d, want 2", probes)
	}
	// A failure is cached too (no hammering a struggling dependency), then recovers.
	readyCall(t, h)
	if probes != 2 {
		t.Fatalf("a failing answer was not cached: %d probes", probes)
	}
	sqsErr = nil
	now = now.Add(6 * time.Second)
	if code, body, _ := readyCall(t, h); code != http.StatusOK || !body.Ready {
		t.Fatalf("recovered: %d %+v", code, body)
	}

	// Cache off: every call checks.
	h2 := NewReadinessHandler(nil, nil, "", 0, zerolog.Nop())
	n := 0
	h2.probe = func(context.Context) (error, error) {
		n++
		return errors.New("pg: connection refused 10.0.0.5:5432"), nil
	}
	for i := 0; i < 3; i++ {
		if code, body, raw := readyCall(t, h2); code != http.StatusServiceUnavailable || body.DB.Err != "unavailable" || strings.Contains(raw, "10.0.0.5") {
			t.Fatalf("%d %s", code, raw)
		}
	}
	if n != 3 {
		t.Fatalf("cache 0 ran %d probes, want 3", n)
	}
}
