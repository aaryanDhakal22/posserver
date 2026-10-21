package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"quiccpos/main/internal/transport/session"

	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
)

// runStoreSession issues a cookie with issue, then runs StoreSessionAuth over a
// request carrying it and returns what the next handler saw.
func runStoreSession(t *testing.T, issue func(s *session.Signer, w http.ResponseWriter, tid, lid uuid.UUID)) (by string, ran bool) {
	t.Helper()
	s, err := session.New("test-secret-at-least-16-bytes-long", false)
	if err != nil {
		t.Fatal(err)
	}
	tid, lid := uuid.New(), uuid.New()
	issued := httptest.NewRecorder()
	issue(s, issued, tid, lid)

	req := httptest.NewRequest(http.MethodGet, "/store/v1/session", nil)
	req.AddCookie(issued.Result().Cookies()[0])
	c := echo.New().NewContext(req, httptest.NewRecorder())
	err = StoreSessionAuth(s)(func(c *echo.Context) error {
		ran = true
		ctx := c.Request().Context()
		if sc, ok := StoreScopeFromContext(ctx); !ok || uuid.UUID(sc.TenantID) != tid || uuid.UUID(sc.LocationID) != lid {
			t.Fatalf("scope = %+v ok=%v, want %v/%v", sc, ok, tid, lid)
		}
		by = StoreImpersonatorFromContext(ctx)
		return nil
	})(c)
	if err != nil {
		t.Fatal(err)
	}
	return by, ran
}

func TestStoreSessionAuthImpersonator(t *testing.T) {
	by, ran := runStoreSession(t, func(s *session.Signer, w http.ResponseWriter, tid, lid uuid.UUID) {
		if err := s.IssueAs(w, nil, tid, lid, "admin"); err != nil {
			t.Fatal(err)
		}
	})
	if !ran || by != "admin" {
		t.Fatalf("impersonated session: ran=%v by=%q", ran, by)
	}

	by, ran = runStoreSession(t, func(s *session.Signer, w http.ResponseWriter, tid, lid uuid.UUID) {
		s.Issue(w, nil, tid, lid)
	})
	if !ran || by != "" {
		t.Fatalf("manager session: ran=%v by=%q", ran, by)
	}
}
