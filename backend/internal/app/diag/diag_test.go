package diag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	authSvc "quiccpos/main/internal/app/auth"
	sseBroker "quiccpos/main/internal/infra/sse"

	"github.com/google/uuid"
)

func reportJSON(t *testing.T, over map[string]any) []byte {
	t.Helper()
	r := map[string]any{
		"v": 1, "id": uuid.NewString(), "created_at": "2026-10-20T12:00:00Z",
		"kind": "guide", "scenario": "pi_unreachable", "outcome": "fixed",
		"headline": "Pi was unplugged", "cause": "pi_power", "fixed_by": "power_cycle",
		"steps":  []any{map[string]any{"id": "power_cycle", "answer": "yes"}},
		"tablet": map[string]any{"name": "Counter", "app_version": "1.3.0", "android_sdk": 34},
		"pi":     map[string]any{"version": "2.0.1"},
		"log":    []any{"a", "b"},
	}
	for k, v := range over {
		if v == nil {
			delete(r, k)
		} else {
			r[k] = v
		}
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustParse(t *testing.T, over map[string]any) *Report {
	t.Helper()
	r, err := ParseReport(reportJSON(t, over))
	if err != nil {
		t.Fatalf("ParseReport: %v", err)
	}
	return r
}

// --- parsing + caps ------------------------------------------------------------

func TestParseReportCapsFields(t *testing.T) {
	logLines := make([]any, 350)
	for i := range logLines {
		logLines[i] = fmt.Sprintf("line %d %s", i, strings.Repeat("x", 600))
	}
	r := mustParse(t, map[string]any{
		"headline": strings.Repeat("é", 200),
		"note":     strings.Repeat("n", 900),
		"kind":     "some_future_kind_" + strings.Repeat("k", 60),
		"log":      logLines,
	})
	if n := len([]rune(r.Headline)); n != maxHeadline {
		t.Fatalf("headline %d runes, want %d", n, maxHeadline)
	}
	if len(r.Note) != maxNote {
		t.Fatalf("note %d, want %d", len(r.Note), maxNote)
	}
	if !strings.HasPrefix(r.Kind, "some_future_kind_") || len(r.Kind) != maxEnum {
		t.Fatalf("unknown kind should be kept, capped at %d: %q", maxEnum, r.Kind)
	}
	var doc struct {
		Headline string   `json:"headline"`
		Log      []string `json:"log"`
		Tablet   struct {
			AndroidSDK json.Number `json:"android_sdk"`
		} `json:"tablet"`
	}
	b, _ := r.JSON()
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Headline != r.Headline {
		t.Fatal("stored doc not capped")
	}
	if len(doc.Log) != maxLogLines || !strings.HasPrefix(doc.Log[0], "line 50 ") {
		t.Fatalf("log should keep the newest %d lines, got %d starting %q", maxLogLines, len(doc.Log), doc.Log[0][:10])
	}
	if len(doc.Log[0]) != maxLogLine {
		t.Fatalf("log line %d chars, want %d", len(doc.Log[0]), maxLogLine)
	}
	if doc.Tablet.AndroidSDK.String() != "34" {
		t.Fatalf("numbers must round-trip exactly, got %q", doc.Tablet.AndroidSDK)
	}
}

func TestParseReportRejects(t *testing.T) {
	cases := map[string][]byte{
		"not json":   []byte("nope"),
		"array":      []byte("[1]"),
		"v2":         reportJSON(t, map[string]any{"v": 2}),
		"no v":       reportJSON(t, map[string]any{"v": nil}),
		"bad id":     reportJSON(t, map[string]any{"id": "abc"}),
		"no kind":    reportJSON(t, map[string]any{"kind": nil}),
		"no outcome": reportJSON(t, map[string]any{"outcome": ""}),
	}
	for name, raw := range cases {
		if _, err := ParseReport(raw); !errors.Is(err, ErrBadReport) {
			t.Errorf("%s: err = %v, want ErrBadReport", name, err)
		}
	}
	big := reportJSON(t, map[string]any{"note": strings.Repeat("x", MaxBody)})
	if _, err := ParseReport(big); !errors.Is(err, ErrTooLarge) {
		t.Errorf("oversize: err = %v, want ErrTooLarge", err)
	}
}

func TestParseReportDefaultsAndNullScenario(t *testing.T) {
	r := mustParse(t, map[string]any{"created_at": "garbage", "scenario": nil})
	if r.CreatedAt.IsZero() || time.Since(r.CreatedAt) > time.Minute {
		t.Fatalf("bad created_at should default to now, got %v", r.CreatedAt)
	}
	if r.Scenario != "" || r.AppVersion != "1.3.0" || r.AgentVersion != "2.0.1" || r.TabletName != "Counter" || r.Steps != 1 {
		t.Fatalf("summary fields wrong: %+v", r)
	}
}

func TestMergePiOverridesVersion(t *testing.T) {
	r := mustParse(t, nil)
	r.MergePi(map[string]any{"version": "2.0.2", "health": map[string]any{"sd_errors": 0}})
	if r.AgentVersion != "2.0.2" {
		t.Fatalf("agent version = %q", r.AgentVersion)
	}
	b, _ := r.JSON()
	if !strings.Contains(string(b), `"health":{"sd_errors":0}`) {
		t.Fatalf("pi.health not merged: %s", b)
	}
}

// --- paging --------------------------------------------------------------------

func TestShouldPage(t *testing.T) {
	cases := []struct {
		kind, scenario, outcome string
		want                    bool
	}{
		{"guide", "pi_unreachable", "needs_part", true},
		{"guide", "printer", "needs_owner", true},
		{"guide", "printer", "not_fixed", true},
		{"guide", "printer", "fixed", false},
		{"guide", "printer", "abandoned", false},
		{"full_check", "", "ok", false},
		{"manual", "", "info", true},
		{"setup", "setup", "abandoned", true},
		{"setup", "setup", "ok", false},
		{"guide", "no_orders", "info", true},
		{"full_check", "no_orders", "ok", false},
		{"guide", "pi_unreachable", "info", false},
	}
	for _, c := range cases {
		r := &Report{Kind: c.kind, Scenario: c.scenario, Outcome: c.outcome}
		if got := ShouldPage(r); got != c.want {
			t.Errorf("ShouldPage(%s/%s/%s) = %v, want %v", c.kind, c.scenario, c.outcome, got, c.want)
		}
	}
}

func TestFormatPage(t *testing.T) {
	id := uuid.MustParse("1a2b3c4d-0000-4000-8000-000000000000")
	r := &Report{ID: id, Kind: "guide", Outcome: "needs_part", Headline: "The Pi's SD card failed",
		Cause: "sd_card", FixedBy: "sd_swap", AppVersion: "1.3.0", AgentVersion: "2.0.1", Note: "spare used"}
	title, msg := FormatPage("Lombardi's", r, ViaDirect, "Counter")
	if title != "Lombardi's: The Pi's SD card failed" {
		t.Fatalf("title = %q", title)
	}
	want := strings.Join([]string{
		"Cause: the Pi's SD card",
		"Fixed by: swapping the SD card",
		"Tablet: Counter · Quibase 1.3.0 · Pi 2.0.1 · sent via tablet",
		"Note: spare used",
		"Details: /app/admin/diagnostics?id=1a2b3c4d",
	}, "\n")
	if msg != want {
		t.Fatalf("message:\n%s\nwant:\n%s", msg, want)
	}

	r2 := &Report{ID: id, Kind: "guide", Outcome: "needs_owner", Steps: 3, Cause: "weird_new_cause"}
	title, msg = FormatPage("Lombardi's", r2, ViaPi, "")
	if title != "Lombardi's: needs owner" {
		t.Fatalf("title fallback = %q", title)
	}
	for _, line := range []string{"Cause: weird new cause", "Not fixed after 3 steps", "Tablet: unnamed · sent via Pi"} {
		if !strings.Contains(msg, line) {
			t.Fatalf("message missing %q:\n%s", line, msg)
		}
	}
	if strings.Contains(msg, "Note:") {
		t.Fatal("no note line expected")
	}
}

func TestStorePagesOwnerOnceThenThrottles(t *testing.T) {
	clock := time.Date(2026, 10, 20, 18, 0, 0, 0, time.UTC)
	s, db, pager := testService(&clock)
	scope := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	ctx := context.Background()

	store := func(over map[string]any) {
		t.Helper()
		if _, dup, err := s.Store(ctx, scope, mustParse(t, over), ViaDirect, "tab-1", ""); err != nil || dup {
			t.Fatalf("store: dup=%v err=%v", dup, err)
		}
	}
	store(map[string]any{"outcome": "fixed"}) // no page
	if len(pager.sent) != 0 {
		t.Fatal("a fixed guide must not page")
	}
	store(map[string]any{"outcome": "needs_part"})
	if len(pager.sent) != 1 || !strings.HasPrefix(pager.sent[0].title, "Lombardi's: ") {
		t.Fatalf("expected one owner page, got %+v", pager.sent)
	}
	if len(db.paged) != 1 {
		t.Fatal("paged_at not stamped")
	}
	clock = clock.Add(10 * time.Minute)
	store(map[string]any{"outcome": "needs_part"}) // same key inside 30 min
	store(map[string]any{"outcome": "needs_owner"})
	if len(pager.sent) != 2 {
		t.Fatalf("throttle per (scenario, outcome): got %d pages, want 2", len(pager.sent))
	}
	clock = clock.Add(31 * time.Minute)
	store(map[string]any{"outcome": "needs_part"})
	if len(pager.sent) != 3 {
		t.Fatalf("throttle should lapse after 30 min: got %d pages", len(pager.sent))
	}
}

func TestStoreDedupesOnClientID(t *testing.T) {
	clock := time.Now()
	s, _, _ := testService(&clock)
	scope := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	raw := reportJSON(t, nil)
	r1, _ := ParseReport(raw)
	r2, _ := ParseReport(raw)
	id1, dup1, err1 := s.Store(context.Background(), scope, r1, ViaPi, "tab", "Counter")
	id2, dup2, err2 := s.Store(context.Background(), scope, r2, ViaDirect, "tab", "Counter")
	if err1 != nil || err2 != nil || dup1 || !dup2 || id1 != id2 {
		t.Fatalf("dedupe: (%v %v %v) (%v %v %v)", id1, dup1, err1, id2, dup2, err2)
	}
}

// --- limits --------------------------------------------------------------------

func TestLimits(t *testing.T) {
	clock := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	l := newLimits()
	l.setClock(func() time.Time { return clock })
	loc := uuid.New()

	for i := 0; i < perTabletMax; i++ {
		if w := l.take(loc, "a", "guide"); w != 0 {
			t.Fatalf("report %d limited (%v)", i, w)
		}
	}
	if w := l.take(loc, "a", "guide"); w <= 0 || w > time.Hour {
		t.Fatalf("31st report in an hour: wait %v, want (0, 1h]", w)
	}
	if w := l.take(loc, "b", "guide"); w != 0 {
		t.Fatal("another tablet should not be limited by tablet a")
	}

	// manual: 1 per 10 min per tablet, and a refused call books nothing.
	if w := l.take(loc, "c", "manual"); w != 0 {
		t.Fatal("first manual refused")
	}
	if w := l.take(loc, "c", "manual"); w <= 0 || w > manualWindow {
		t.Fatalf("second manual within 10 min: wait %v", w)
	}
	if w := l.take(loc, "c", "guide"); w != 0 {
		t.Fatal("a guide report should not be held by the manual limit")
	}
	clock = clock.Add(manualWindow)
	if w := l.take(loc, "c", "manual"); w != 0 {
		t.Fatal("manual should be allowed again after 10 min")
	}

	// per location: 120 per day across tablets.
	loc2 := uuid.New()
	for i := 0; i < perLocationMax; i++ {
		clock = clock.Add(time.Second)
		if w := l.take(loc2, fmt.Sprintf("t%d", i), "guide"); w != 0 {
			t.Fatalf("location report %d limited", i)
		}
	}
	if w := l.take(loc2, "fresh", "guide"); w <= 0 {
		t.Fatal("121st report of the day should be limited")
	}
}

func TestStoreRateLimitedError(t *testing.T) {
	clock := time.Now()
	s, _, _ := testService(&clock)
	scope := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	if _, _, err := s.Store(context.Background(), scope, mustParse(t, map[string]any{"kind": "manual", "outcome": "info"}), ViaDirect, "t", ""); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Store(context.Background(), scope, mustParse(t, map[string]any{"kind": "manual", "outcome": "info"}), ViaDirect, "t", "")
	var rl *RateLimitError
	if !errors.As(err, &rl) || !errors.Is(err, ErrRateLimited) || RetrySeconds(rl.RetryAfter) < 1 {
		t.Fatalf("want RateLimitError, got %v", err)
	}
}

// --- tokens --------------------------------------------------------------------

func TestTokenFormatAndHash(t *testing.T) {
	clock := time.Now()
	s, db, _ := testService(&clock)
	scope := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	_, tok, err := s.Mint(context.Background(), scope, "tab-1", "Counter")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok, TokenPrefix) || len(tok) != len(TokenPrefix)+43 {
		t.Fatalf("token %q: want qrep_ + 43 base64url chars", tok)
	}
	if strings.ContainsAny(tok, "+/=") {
		t.Fatal("token must be base64url without padding")
	}
	if db.tokens[0].hash != authSvc.HashKey(tok) || strings.Contains(db.tokens[0].hash, tok) {
		t.Fatal("only the sha256 hash may be stored")
	}
	if _, _, err := s.Mint(context.Background(), scope, "  ", "x"); !errors.Is(err, ErrBadTablet) {
		t.Fatalf("empty tablet_id: %v", err)
	}
}

func TestTokenRotationPerTablet(t *testing.T) {
	clock := time.Now()
	s, _, _ := testService(&clock)
	ctx := context.Background()
	scope := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	_, old, _ := s.Mint(ctx, scope, "tab-1", "Counter")
	_, other, _ := s.Mint(ctx, scope, "tab-2", "Bar")
	_, fresh, _ := s.Mint(ctx, scope, "tab-1", "Counter")

	if _, err := s.Authenticate(ctx, old); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("re-mint must revoke the tablet's old token: %v", err)
	}
	for _, tok := range []string{other, fresh} {
		if a, err := s.Authenticate(ctx, tok); err != nil || a.Scope.LocationID != scope.LocationID {
			t.Fatalf("token should be valid: %v", err)
		}
	}
	if err := s.Revoke(ctx, scope, "tab-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(ctx, other); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("revoked token still valid")
	}
	if _, err := s.Authenticate(ctx, "qopr_not-a-report-key"); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("foreign token prefix accepted")
	}
}

func TestTokenMaxActivePerLocation(t *testing.T) {
	clock := time.Now()
	s, _, _ := testService(&clock)
	ctx := context.Background()
	scope := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	other := sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}
	_, otherTok, _ := s.Mint(ctx, other, "tab-0", "")

	var toks []string
	for i := 0; i < MaxActiveTokens+2; i++ {
		_, tok, err := s.Mint(ctx, scope, fmt.Sprintf("tab-%d", i), "")
		if err != nil {
			t.Fatal(err)
		}
		toks = append(toks, tok)
	}
	for i, tok := range toks {
		_, err := s.Authenticate(ctx, tok)
		if wantValid := i >= 2; (err == nil) != wantValid {
			t.Fatalf("token %d valid=%v, want %v (oldest two revoked past %d)", i, err == nil, wantValid, MaxActiveTokens)
		}
	}
	if _, err := s.Authenticate(ctx, otherTok); err != nil {
		t.Fatal("another location's token must not count toward the cap")
	}
}

func TestAuthenticateTouchesAtMostOncePerMinute(t *testing.T) {
	clock := time.Now()
	s, db, _ := testService(&clock)
	ctx := context.Background()
	_, tok, _ := s.Mint(ctx, sseBroker.Scope{TenantID: uuid.New(), LocationID: uuid.New()}, "tab", "")
	for i := 0; i < 5; i++ {
		if _, err := s.Authenticate(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
	if db.touches != 1 {
		t.Fatalf("touches = %d, want 1 within a minute", db.touches)
	}
	clock = clock.Add(61 * time.Second)
	_, _ = s.Authenticate(ctx, tok)
	if db.touches != 2 {
		t.Fatalf("touches = %d, want 2 after a minute", db.touches)
	}
}
