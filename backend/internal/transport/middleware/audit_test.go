package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"

	"quiccpos/main/internal/domain/order"
	"quiccpos/main/internal/infra/database/auditconn"
)

type memWriter struct {
	mu      sync.Mutex
	entries []auditconn.Entry
}

func (m *memWriter) WriteApp(_ context.Context, e auditconn.Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, e)
	return nil
}

func TestAuditActorKinds(t *testing.T) {
	tid, lid := uuid.New(), uuid.New()
	base := withRequestInfo(context.Background(), "rq-1", "10.0.0.9")
	if _, ok := AuditActor(base); ok {
		t.Fatal("no caller = system")
	}
	if a, ok := AuditActor(withAdmin(base)); !ok || a.Kind != "admin" || a.RequestID != "rq-1" || a.IP != "10.0.0.9" {
		t.Fatalf("admin: %+v", a)
	}
	mgr := context.WithValue(base, storeScopeKey, order.Scope{TenantID: order.TenantID(tid), LocationID: order.LocationID(lid)})
	if a, ok := AuditActor(mgr); !ok || a.Kind != "manager" || a.ID != tid.String() || a.LocationID != lid.String() {
		t.Fatalf("manager: %+v", a)
	}
	imp := context.WithValue(mgr, storeImpersonatorKey, "admin")
	if a, _ := AuditActor(imp); a.Kind != "admin" || a.ImpersonatedBy != "admin" || a.LocationID != lid.String() {
		t.Fatalf("impersonated: %+v", a)
	}
}

// TestAuditWritesOnlyWrites: a POST through the guarded route leaves one row
// (route, id, answer, actor); a GET none.
func TestAuditWritesOnlyWrites(t *testing.T) {
	w := &memWriter{}
	e := echo.New()
	h := func(c *echo.Context) error { return c.JSON(http.StatusConflict, map[string]string{"error": "taken"}) }
	mark := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			c.SetRequest(c.Request().WithContext(withAdmin(c.Request().Context())))
			return next(c)
		}
	}
	mw := Chain(mark, AuditWrites(w))
	e.POST("/admin/v1/tenants/:id/store-id", h, mw)
	e.GET("/admin/v1/tenants/:id", h, mw)
	for _, m := range []string{http.MethodPost, http.MethodGet} {
		path := "/admin/v1/tenants/abc/store-id"
		if m == http.MethodGet {
			path = "/admin/v1/tenants/abc"
		}
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(m, path, nil))
	}
	if len(w.entries) != 1 {
		t.Fatalf("want 1 row, got %+v", w.entries)
	}
	got := w.entries[0]
	if got.Method != "POST" || got.Route != "/admin/v1/tenants/:id/store-id" || got.EntityID != "abc" || got.Status != 409 || got.Actor.Kind != "admin" {
		t.Fatalf("row: %+v", got)
	}
}
