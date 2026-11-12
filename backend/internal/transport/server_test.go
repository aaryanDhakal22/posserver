package transport

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v5"
	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	appMiddleware "quiccpos/main/internal/transport/middleware"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newTestEcho(t *testing.T) (*echo.Echo, *syncBuf) {
	t.Helper()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider())
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	buf := &syncBuf{}
	lg := zerolog.New(buf).Level(zerolog.DebugLevel)
	e := echo.New()
	AddDefaultMiddlewares(e, "test", &lg)
	return e, buf
}

func TestPanicIsLoggedWithStackAndTrace(t *testing.T) {
	e, buf := newTestEcho(t)
	e.GET("/boom", func(c *echo.Context) error { panic("kaboom") })
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if rec.Code != 500 {
		t.Fatalf("status %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "kaboom") || strings.Contains(rec.Body.String(), "goroutine") {
		t.Fatalf("the panic leaked to the client: %s", rec.Body.String())
	}
	out := buf.String()
	var panicLine string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "panic in HTTP handler") {
			panicLine = l
		}
	}
	if panicLine == "" {
		t.Fatalf("no panic line: %s", out)
	}
	for _, want := range []string{`"level":"error"`, `"panic":"kaboom"`, `"stack":"goroutine`, `"route":"/boom"`} {
		if !strings.Contains(panicLine, want) {
			t.Errorf("panic line lacks %s: %s", want, panicLine)
		}
	}
	if !regexp.MustCompile(`"trace_id":"[0-9a-f]{32}"`).MatchString(panicLine) {
		t.Errorf("panic line has no trace_id: %s", panicLine)
	}
	if !strings.Contains(out, `"message":"request completed"`) || !strings.Contains(out, `"status":500`) {
		t.Errorf("request line missing the 500: %s", out)
	}
}

func TestRequestLoggerLogsErrorText(t *testing.T) {
	e, buf := newTestEcho(t)
	e.GET("/err", func(c *echo.Context) error { return echo.NewHTTPError(http.StatusBadGateway, "upstream down") })
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/err", nil))
	if !strings.Contains(buf.String(), "upstream down") {
		t.Fatalf("5xx error text not logged: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"level":"debug","request_id"`) && !strings.Contains(buf.String(), `"message":"request started"`) {
		t.Fatalf("no request started line: %s", buf.String())
	}
	for _, l := range strings.Split(buf.String(), "\n") {
		if strings.Contains(l, "request started") && !strings.Contains(l, `"level":"debug"`) {
			t.Fatalf("request started must be debug: %s", l)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	e, _ := newTestEcho(t)
	read := func(c *echo.Context) error {
		n, err := io.Copy(io.Discard, c.Request().Body)
		if err != nil {
			return err
		}
		return c.JSON(200, map[string]int64{"read": n})
	}
	e.POST("/store/v1/me/staff", read)
	e.POST("/admin/v1/app-releases", read)
	e.POST("/store/v1/agent/rpc", read)

	big := bytes.Repeat([]byte("x"), 5<<20)
	for _, tc := range []struct {
		path string
		size int
		want int
	}{
		{"/store/v1/me/staff", 5 << 20, http.StatusRequestEntityTooLarge},
		{"/store/v1/me/staff", 1 << 19, 200},
		{"/admin/v1/app-releases", 5 << 20, 200}, // the APK route caps itself
		{"/store/v1/agent/rpc", 5 << 20, 200},    // 8 MB for the Pi's batches
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(big[:tc.size]))
		req.Header.Set("Content-Type", "application/json")
		e.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s %d bytes: status %d, want %d", tc.path, tc.size, rec.Code, tc.want)
		}
	}
	// A chunked body (no Content-Length) is cut while reading.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/store/v1/me/staff", io.MultiReader(bytes.NewReader(big)))
	req.ContentLength = -1
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked 5 MB: status %d, want 413", rec.Code)
	}
}

// Every BodyLimits route must still exist in routes.go (a renamed route would
// silently fall back to the 1 MB default).
func TestBodyLimitRoutesExist(t *testing.T) {
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	for k, v := range map[string]string{`apiPrefix+"`: `"/api/v1`, `adminPrefix+"`: `"/admin/v1`, `storePrefix+"`: `"/store/v1`, `channelPrefix+"`: `"/channel/v1`} {
		s = strings.ReplaceAll(s, k, v)
	}
	for key := range BodyLimits {
		method, path, _ := strings.Cut(key, " ")
		if !strings.Contains(s, `def("`+method+`", "`+path+`"`) {
			t.Errorf("BodyLimits names %q, which routes.go no longer has", key)
		}
	}
}

func TestShutdownEndsStreams(t *testing.T) {
	e, _ := newTestEcho(t)
	started := make(chan struct{})
	e.GET("/api/v1/events/orders", func(c *echo.Context) error {
		w := c.Response()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		close(started)
		<-c.Request().Context().Done()
		return nil
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: e}
	configureServer(srv)
	if srv.IdleTimeout != 120*time.Second {
		t.Fatalf("IdleTimeout = %v", srv.IdleTimeout)
	}
	go func() { _ = srv.Serve(ln) }()
	resp, err := http.Get("http://" + ln.Addr().String() + "/api/v1/events/orders")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	t0 := time.Now()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown didn't drain with a stream open: %v", err)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("Shutdown took %v", d)
	}
	// The global drain is closed now: give later tests in this package a fresh one.
	streams = appMiddleware.NewDrain()
}

func TestIsStream(t *testing.T) {
	for _, r := range []string{"/api/v1/events/orders", "/channel/v1/events"} {
		if !appMiddleware.IsStream(r) {
			t.Errorf("%s should be a stream", r)
		}
	}
	for _, r := range []string{"/channel/v1/orders", "/store/v1/me/eventsx"} {
		if appMiddleware.IsStream(r) {
			t.Errorf("%s is not a stream", r)
		}
	}
}
