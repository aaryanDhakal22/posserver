package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/transport/session"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
	err = StoreSessionAuth(s, nil)(func(c *echo.Context) error {
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
		if err := s.IssueAs(w, nil, tid, lid, "admin", 0); err != nil {
			t.Fatal(err)
		}
	})
	if !ran || by != "admin" {
		t.Fatalf("impersonated session: ran=%v by=%q", ran, by)
	}

	by, ran = runStoreSession(t, func(s *session.Signer, w http.ResponseWriter, tid, lid uuid.UUID) {
		s.Issue(w, nil, tid, lid, 0)
	})
	if !ran || by != "" {
		t.Fatalf("manager session: ran=%v by=%q", ran, by)
	}
}

// M8: the store's side of a session is read on every request. A password
// rotation (the store's epoch moves on), a retired store or a deleted one
// ends the manager's session: 401, cookie cleared, the handler never runs.
func TestStoreSessionAuthEpochAndRetired(t *testing.T) {
	s, err := session.New("test-secret-at-least-16-bytes-long", false)
	if err != nil {
		t.Fatal(err)
	}
	tid, lid := uuid.New(), uuid.New()
	cookie := func(by string, epoch int64) *http.Cookie {
		w := httptest.NewRecorder()
		if by != "" {
			if err := s.IssueAs(w, nil, tid, lid, by, epoch); err != nil {
				t.Fatal(err)
			}
		} else {
			s.Issue(w, nil, tid, lid, epoch)
		}
		return w.Result().Cookies()[0]
	}
	run := func(ck *http.Cookie, st tenancy.SessionState, stErr error) (code int, ran, cleared bool) {
		req := httptest.NewRequest(http.MethodPost, "/store/v1/me/printers", nil)
		req.AddCookie(ck)
		rec := httptest.NewRecorder()
		state := func(_ context.Context, id uuid.UUID) (tenancy.SessionState, error) {
			if id != tid {
				t.Fatalf("state asked for %v, want the cookie's tenant", id)
			}
			return st, stErr
		}
		if err := StoreSessionAuth(s, state)(func(c *echo.Context) error {
			ran = true
			return c.NoContent(http.StatusNoContent)
		})(echo.New().NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name == session.CookieName && c.MaxAge < 0 {
				cleared = true
			}
		}
		return rec.Code, ran, cleared
	}

	// Same epoch: in. A cookie from before epochs (0) on a never-rotated store: in.
	if code, ran, _ := run(cookie("", 2), tenancy.SessionState{Epoch: 2}, nil); !ran || code != http.StatusNoContent {
		t.Fatalf("current epoch: code=%d ran=%v", code, ran)
	}
	if code, ran, _ := run(cookie("", 0), tenancy.SessionState{Epoch: 0}, nil); !ran || code != http.StatusNoContent {
		t.Fatalf("epoch 0: code=%d ran=%v", code, ran)
	}
	// The password was rotated after the cookie was issued.
	if code, ran, cleared := run(cookie("", 2), tenancy.SessionState{Epoch: 3}, nil); ran || code != http.StatusUnauthorized || !cleared {
		t.Fatalf("rotated password: code=%d ran=%v cleared=%v, want 401 + cleared cookie", code, ran, cleared)
	}
	if code, ran, _ := run(cookie("", 0), tenancy.SessionState{Epoch: 1}, nil); ran || code != http.StatusUnauthorized {
		t.Fatalf("an old-form cookie after a rotation: code=%d ran=%v", code, ran)
	}
	if code, ran, _ := run(cookie("admin", 2), tenancy.SessionState{Epoch: 3}, nil); ran || code != http.StatusUnauthorized {
		t.Fatalf("an impersonated session after a rotation: code=%d ran=%v", code, ran)
	}
	// A retired store: the manager is out, the admin's open-as-store still reads it.
	if code, ran, _ := run(cookie("", 1), tenancy.SessionState{Epoch: 1, Retired: true}, nil); ran || code != http.StatusUnauthorized {
		t.Fatalf("retired store, manager: code=%d ran=%v", code, ran)
	}
	if code, ran, _ := run(cookie("admin", 1), tenancy.SessionState{Epoch: 1, Retired: true}, nil); !ran || code != http.StatusNoContent {
		t.Fatalf("retired store, admin open-as-store: code=%d ran=%v", code, ran)
	}
	// The store is gone; a database error fails closed without logging out.
	if code, ran, cleared := run(cookie("", 0), tenancy.SessionState{}, pgx.ErrNoRows); ran || code != http.StatusUnauthorized || !cleared {
		t.Fatalf("deleted store: code=%d ran=%v cleared=%v", code, ran, cleared)
	}
	if code, ran, cleared := run(cookie("", 0), tenancy.SessionState{}, errors.New("db down")); ran || code != http.StatusServiceUnavailable || cleared {
		t.Fatalf("lookup error: code=%d ran=%v cleared=%v, want 503 and the cookie kept", code, ran, cleared)
	}
}
