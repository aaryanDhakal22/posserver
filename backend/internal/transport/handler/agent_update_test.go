package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"quiccpos/main/internal/app/printerstatus"
	sseBroker "quiccpos/main/internal/infra/sse"
	"quiccpos/main/internal/observability"
	"quiccpos/main/internal/rpc"
	appMiddleware "quiccpos/main/internal/transport/middleware"
	"quiccpos/main/internal/transport/session"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
)

func TestAgentUpdateValidation(t *testing.T) {
	h := NewAgentUpdateHandler(nil, nil, zerolog.Nop())
	tid, lid := uuid.NewString(), uuid.NewString()
	for name, tc := range map[string]struct {
		body string
		run  func(*AgentUpdateHandler, *echo.Context) error
	}{
		"bad uuids":          {`{"tenant_id":"x","location_id":"y"}`, (*AgentUpdateHandler).Update},
		"unknown requester":  {`{"tenant_id":"` + tid + `","location_id":"` + lid + `","requested_by":"pi-web"}`, (*AgentUpdateHandler).Update},
		"rollback bad uuids": {`{"tenant_id":"","location_id":""}`, (*AgentUpdateHandler).Rollback},
		"not json":           {`{`, (*AgentUpdateHandler).Update},
	} {
		req := httptest.NewRequest(http.MethodPost, "/admin/v1/agent/update", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		if err := tc.run(h, echo.New().NewContext(req, rec)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", name, rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/v1/agent-updates?location_id=nope", nil)
	rec := httptest.NewRecorder()
	if err := h.List(echo.New().NewContext(req, rec)); err != nil || rec.Code != http.StatusBadRequest {
		t.Fatalf("list with a bad location_id: status %d err %v, want 400", rec.Code, err)
	}
}

type offlinePub struct{}

func (offlinePub) PublishCommand(context.Context, sseBroker.Scope, []byte) int { return 0 }
func (offlinePub) SubscribersFor(sseBroker.Scope) int                          { return 0 }

// The live roster passes the agent's per-printer MAC and its found printers
// through (served from the cache when the agent is offline).
func TestMeGetLiveFoundPassthrough(t *testing.T) {
	meters, _ := observability.NewMeters()
	cache := printerstatus.NewCache()
	h := NewPrinterStatusHandler(cache, rpc.NewRequester(offlinePub{}, meters, zerolog.Nop()), meters, zerolog.Nop())

	tid, lid := uuid.New(), uuid.New()
	report := `{"reported_at":"` + time.Now().UTC().Format(time.RFC3339) + `","agent_version":"2.0.6",
		"printers":[{"name":"Pizza","ip":"10.0.0.9","mac":"aa:bb:cc:dd:ee:01","up":true}],
		"found":[{"ip":"10.0.0.77","mac":"aa:bb:cc:dd:ee:77"}]}`
	if _, err := cache.Handle(context.Background(), sseBroker.Scope{TenantID: tid, LocationID: lid}, json.RawMessage(report)); err != nil {
		t.Fatal(err)
	}

	body := runLive(t, h, tid, lid)
	if body["agent_online"] != false {
		t.Fatalf("agent_online = %v, want false", body["agent_online"])
	}
	printers := body["printers"].([]any)
	if p := printers[0].(map[string]any); p["mac"] != "aa:bb:cc:dd:ee:01" {
		t.Fatalf("printer = %v, want its mac", p)
	}
	found := body["found"].([]any)
	if len(found) != 1 || found[0].(map[string]any)["ip"] != "10.0.0.77" {
		t.Fatalf("found = %v", found)
	}

	// No report yet (or an agent before 2.0.6): found is an empty list.
	if body := runLive(t, h, uuid.New(), uuid.New()); len(body["found"].([]any)) != 0 {
		t.Fatalf("found without a report = %v, want []", body["found"])
	}
}

func runLive(t *testing.T, h *PrinterStatusHandler, tid, lid uuid.UUID) map[string]any {
	t.Helper()
	signer, err := session.New("test-secret-at-least-16-bytes-long", false)
	if err != nil {
		t.Fatal(err)
	}
	issued := httptest.NewRecorder()
	signer.Issue(issued, nil, tid, lid)
	req := httptest.NewRequest(http.MethodGet, "/store/v1/me/printers/live", nil)
	req.AddCookie(issued.Result().Cookies()[0])
	rec := httptest.NewRecorder()
	if err := appMiddleware.StoreSessionAuth(signer)(h.MeGetLive)(echo.New().NewContext(req, rec)); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}
