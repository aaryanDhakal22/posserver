package observability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/rs/zerolog"
)

func TestPprofOffByDefault(t *testing.T) {
	a, err := StartPprof(context.Background(), "", zerolog.Nop())
	if a != nil || err != nil {
		t.Fatalf("empty PPROF_ADDR started pprof: %v %v", a, err)
	}
}

func TestPprofRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", ":0", "192.168.1.5:0", "[::]:0", "example.com:0"} {
		if _, err := StartPprof(context.Background(), addr, zerolog.Nop()); !errors.Is(err, ErrPprofNotLoopback) {
			t.Errorf("%q: err = %v, want ErrPprofNotLoopback", addr, err)
		}
	}
}

func TestPprofServesOnLoopback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, err := StartPprof(ctx, "127.0.0.1:0", zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + a.String() + "/debug/pprof/goroutine?debug=1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || len(body) == 0 {
		t.Fatalf("status %d, %d bytes", resp.StatusCode, len(body))
	}
}

func TestSQSUpGauge(t *testing.T) {
	SetSQSUp(false)
	if SQSUp() {
		t.Fatal("sqs.up should be 0")
	}
	SetSQSUp(true)
	if !SQSUp() {
		t.Fatal("sqs.up should be 1")
	}
}
