package squarepay

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"quiccpos/main/internal/domain/tenancy"
	"quiccpos/main/internal/infra/database/repositories"
	"quiccpos/main/internal/infra/square"
	"quiccpos/main/internal/infra/square/squarefake"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// The OAuth finish shared by main's own callback and the storefront's relay
// (POST /square/oauth/forward): the signed state decides, tokens are sealed.

type upserts struct {
	Repo // the rest is never called here
	got  []repositories.SquareConnection
}

func (u *upserts) Upsert(_ context.Context, c repositories.SquareConnection) (repositories.SquareConnection, error) {
	u.got = append(u.got, c)
	return c, nil
}

type oneLoc struct{ loc tenancy.Location }

func (o oneLoc) GetLocation(_ context.Context, id uuid.UUID) (*tenancy.Location, error) {
	if id != o.loc.ID {
		return nil, errors.New("no such location")
	}
	return &o.loc, nil
}

const fakeRedirect = "https://order.example.test/square/oauth/callback"

func oauthRig(t *testing.T) (*Service, *upserts, tenancy.Location, *bytes.Buffer, string) {
	t.Helper()
	fake := squarefake.New("sq0idp-fake", "sq0csp-fake")
	mux := http.NewServeMux()
	mux.Handle("/", fake.Handler("/sq", fakeRedirect))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	base := srv.URL + "/sq"
	loc := tenancy.Location{ID: uuid.New(), TenantID: uuid.New()}
	repo := &upserts{}
	logs := &bytes.Buffer{}
	cfg := Config{Env: "fake", AppID: "sq0idp-fake", AppSecret: "sq0csp-fake", BaseURL: base, RedirectURL: fakeRedirect,
		AppURL: "http://main.lan:1323/", TokenKey: testKey("oauth")}
	s, err := NewService(cfg, square.New(base, cfg.AppID, cfg.AppSecret, nil), repo, oneLoc{loc}, zerolog.New(logs))
	if err != nil {
		t.Fatal(err)
	}
	return s, repo, loc, logs, base
}

// authorize plays the owner on Square's page: the code + state Square sends
// to the redirect URL (not followed).
func authorize(t *testing.T, s *Service, loc uuid.UUID) (code, state string) {
	t.Helper()
	u, err := s.ConnectURL(loc)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := c.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	to, err := url.Parse(res.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(to.String(), fakeRedirect) {
		t.Fatalf("Square sent the owner to %q, want the registered redirect %q", to, fakeRedirect)
	}
	return to.Query().Get("code"), to.Query().Get("state")
}

func TestFinishConnectsAndSealsTokens(t *testing.T) {
	s, repo, loc, logs, _ := oauthRig(t)
	code, state := authorize(t, s, loc.ID)
	lid, result, err := s.Finish(context.Background(), code, state, "")
	if err != nil || result != ResultConnected || lid != loc.ID {
		t.Fatalf("Finish = %v, %q, %v", lid, result, err)
	}
	if got := s.ReturnURL(result); got != "http://main.lan:1323/app/store/payments?square=connected" {
		t.Fatalf("ReturnURL = %q", got)
	}
	if len(repo.got) != 1 {
		t.Fatalf("%d connections stored", len(repo.got))
	}
	c := repo.got[0]
	if c.LocationID != loc.ID || c.TenantID != loc.TenantID || c.SquareLocationID == "" || c.MerchantID == "" {
		t.Fatalf("stored %+v", c)
	}
	for name, enc := range map[string][]byte{"access": c.AccessEnc, "refresh": c.RefreshEnc} {
		plain, err := s.open(enc)
		if err != nil || plain == "" {
			t.Fatalf("%s token doesn't open: %v", name, err)
		}
		if bytes.Contains(enc, []byte(plain)) {
			t.Fatalf("%s token stored in the clear", name)
		}
	}
	if strings.Contains(logs.String(), code) {
		t.Fatal("the OAuth code was logged")
	}
	// Square's codes are single use: a replay exchanges nothing.
	if _, result, err := s.Finish(context.Background(), code, state, ""); err == nil || result != ResultError || len(repo.got) != 1 {
		t.Fatalf("replayed code: %q, %v, %d stored", result, err, len(repo.got))
	}
}

func TestFinishRefusesBadStates(t *testing.T) {
	s, repo, loc, logs, _ := oauthRig(t)
	code, state := authorize(t, s, loc.ID)
	parts := strings.Split(state, ".")
	other := uuid.New().String() + "." + parts[1] + "." + parts[2] // another store, same signature
	for name, st := range map[string]string{
		"empty": "", "garbage": "nope", "another store": other,
		"longer expiry": parts[0] + "." + parts[1] + "9." + parts[2],
		"bad signature": parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2])),
	} {
		if _, result, err := s.Finish(context.Background(), code, st, ""); !errors.Is(err, ErrBadState) || result != ResultExpired {
			t.Errorf("%s: %q, %v", name, result, err)
		}
	}
	// A real state, but older than 15 minutes.
	s.now = func() time.Time { return time.Now().Add(16 * time.Minute) }
	if _, result, err := s.Finish(context.Background(), code, state, ""); !errors.Is(err, ErrBadState) || result != ResultExpired {
		t.Errorf("expired: %q, %v", result, err)
	}
	s.now = time.Now
	// The owner said no on Square's page.
	if _, result, err := s.Finish(context.Background(), "", state, "access_denied"); !errors.Is(err, ErrDenied) || result != ResultDenied {
		t.Errorf("denied: %q, %v", result, err)
	}
	if len(repo.got) != 0 {
		t.Fatalf("a refused state stored %d connections", len(repo.got))
	}
	// Nothing was exchanged: the code still works with its own state.
	if _, result, err := s.Finish(context.Background(), code, state, ""); err != nil || result != ResultConnected {
		t.Fatalf("the untouched code: %q, %v", result, err)
	}
	if strings.Contains(logs.String(), code) {
		t.Fatal("the OAuth code was logged")
	}
}

func TestFinishWhenSquareIsOff(t *testing.T) {
	s, err := NewService(Config{AppURL: ""}, nil, nil, nil, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if _, result, err := s.Finish(context.Background(), "c", "s", ""); !errors.Is(err, ErrDisabled) || result != ResultError {
		t.Fatalf("off: %q, %v", result, err)
	}
	if got := s.ReturnURL(ResultError); got != "/app/store/payments?square=error" {
		t.Fatalf("relative ReturnURL = %q", got)
	}
}

func TestFakeReturnsToMainsOwnOrigin(t *testing.T) {
	// The fake runs inside main: with no SQUARE_APP_URL the owner goes back to
	// main's own origin (a gate stack's port, not always 1323), absolute so the
	// storefront's relay can send the browser there too.
	for base, want := range map[string]string{
		"":                                     "http://localhost:1323",
		"http://localhost:21423/__fake/square": "http://localhost:21423",
	} {
		t.Setenv("SQUARE_ENV", "fake")
		t.Setenv("SQUARE_BASE_URL", base)
		t.Setenv("SQUARE_APP_URL", "")
		cfg, _ := FromEnv("dev", zerolog.Nop())
		if cfg.AppURL != want {
			t.Errorf("base %q: AppURL = %q, want %q", base, cfg.AppURL, want)
		}
	}
	t.Setenv("SQUARE_APP_URL", "http://192.168.1.221:1323")
	if cfg, _ := FromEnv("dev", zerolog.Nop()); cfg.AppURL != "http://192.168.1.221:1323" {
		t.Errorf("SQUARE_APP_URL ignored: %q", cfg.AppURL)
	}
}
