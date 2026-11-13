package pushover

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
)

// The package's other tests are about targeting, against a fake Pushover:
// they run with the tests-only hatch. The guard tests below set APP_ENV and
// PUSHOVER_NONPROD themselves (t.Setenv).
func TestMain(m *testing.M) {
	os.Setenv(NonProdEnvVar, "1")
	os.Exit(m.Run())
}

func guarded(t *testing.T, appEnv, nonProd string) (*Client, *atomic.Int32, *bytes.Buffer) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("APP_ENV", appEnv)
	t.Setenv(NonProdEnvVar, nonProd)
	var logs bytes.Buffer
	SetLogger(zerolog.New(&logs))
	t.Cleanup(func() { suppressLog.Store(nil) })
	c := New("app", "user", "boss")
	c.SetEndpoint(srv.URL)
	c.SetDevices([]string{"AdminPhone"})
	return c, &calls, &logs
}

// sendEverything goes through every public send path once: 2 + 2 + 1 + 2 + 1 = 8 posts.
func sendEverything(t *testing.T, c *Client) {
	t.Helper()
	ctx := context.Background()
	for _, err := range []error{
		c.Send(ctx, "Pi offline", "Lombardi's Pi is offline", "siren"),
		c.SendAlso(ctx, []string{"OwnerPhone"}, "Card hold failed", "Order 1102", "siren"),
		c.SendAdmin(ctx, "Pi replaced", "restored", ""),
		c.SendOwner(ctx, []string{"OwnerPhone"}, "Your Pi is offline", "Check the cable", ""),
		c.SendDevices(ctx, []string{"OwnerPhone"}, "", "Test alert", ""),
	} {
		if err != nil {
			t.Fatalf("send: %v", err)
		}
	}
}

// The owner 2026-11-13: staging (and dev, and an unset APP_ENV) never sends a
// Pushover, even with the keys configured; it logs what it dropped.
func TestGuardNonProdSendsNothing(t *testing.T) {
	for _, env := range []string{"staging", "dev", "", "Prod", "production"} {
		t.Run("env="+env, func(t *testing.T) {
			c, calls, logs := guarded(t, env, "")
			if c.Sends() {
				t.Fatal("Sends() true outside prod")
			}
			if !c.Enabled() {
				t.Fatal("Enabled() must still say configured (store_config's owner_offline_alert_by_main)")
			}
			sendEverything(t, c)
			if n := calls.Load(); n != 0 {
				t.Fatalf("APP_ENV=%q made %d Pushover calls", env, n)
			}
			if got := strings.Count(logs.String(), "pushover suppressed (APP_ENV="); got != 8 {
				t.Fatalf("%d suppressed lines, want 8: %s", got, logs.String())
			}
			if env == "staging" && !strings.Contains(logs.String(), "pushover suppressed (APP_ENV=staging): Pi offline") {
				t.Fatalf("no line naming the title: %s", logs.String())
			}
			if !strings.Contains(logs.String(), `"level":"info"`) {
				t.Fatalf("suppressed lines are not info: %s", logs.String())
			}
		})
	}
}

func TestGuardProdSends(t *testing.T) {
	c, calls, logs := guarded(t, "prod", "")
	if !c.Sends() {
		t.Fatal("Sends() false on prod")
	}
	sendEverything(t, c)
	if n := calls.Load(); n != 8 {
		t.Fatalf("prod made %d Pushover calls, want 8", n)
	}
	if strings.Contains(logs.String(), "suppressed") {
		t.Fatalf("prod logged a suppression: %s", logs.String())
	}
}

func TestGuardNonProdEscapeHatch(t *testing.T) {
	for _, env := range []string{"staging", "dev"} {
		c, calls, _ := guarded(t, env, "1")
		sendEverything(t, c)
		if n := calls.Load(); n != 8 {
			t.Fatalf("APP_ENV=%s PUSHOVER_NONPROD=1 made %d calls, want 8", env, n)
		}
	}
	c, calls, _ := guarded(t, "staging", "yes") // only "1" opens it
	sendEverything(t, c)
	if n := calls.Load(); n != 0 {
		t.Fatalf("PUSHOVER_NONPROD=yes made %d calls", n)
	}
}
