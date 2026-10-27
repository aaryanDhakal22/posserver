package pushover

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// fake records each form posted and answers with status + body.
func fake(t *testing.T, status int, body string) (*httptest.Server, func() []url.Values) {
	t.Helper()
	var mu sync.Mutex
	var got []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		got = append(got, r.PostForm)
		mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []url.Values { mu.Lock(); defer mu.Unlock(); return got }
}

func TestSendPagesEveryRecipientWithoutDevice(t *testing.T) {
	srv, forms := fake(t, 200, `{"status":1}`)
	c := New("app", "owner", "boss")
	c.SetEndpoint(srv.URL)
	if err := c.Send(context.Background(), "T", "M", "pushover"); err != nil {
		t.Fatal(err)
	}
	fs := forms()
	if len(fs) != 2 || fs[0].Get("user") != "owner" || fs[1].Get("user") != "boss" {
		t.Fatalf("got %v", fs)
	}
	for _, f := range fs {
		if f.Has("device") {
			t.Fatalf("Send must not target a device: %v", f)
		}
	}
}

func TestSendDevicesTargetsTheFirstAccount(t *testing.T) {
	srv, forms := fake(t, 200, `{"status":1}`)
	c := New("app", "owner", "boss")
	c.SetEndpoint(srv.URL)
	if err := c.SendDevices(context.Background(), []string{"a", "b"}, "", "hello", ""); err != nil {
		t.Fatal(err)
	}
	fs := forms()
	if len(fs) != 1 || fs[0].Get("user") != "owner" || fs[0].Get("device") != "a,b" || fs[0].Get("message") != "hello" {
		t.Fatalf("got %v", fs)
	}
	if fs[0].Has("title") || fs[0].Has("sound") {
		t.Fatalf("empty title/sound must not be sent: %v", fs[0])
	}
}

func TestSendDevicesErrors(t *testing.T) {
	if err := New("", "owner").SendDevices(context.Background(), []string{"a"}, "", "m", ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no token: want ErrNotConfigured, got %v", err)
	}
	if err := New("app").SendDevices(context.Background(), []string{"a"}, "", "m", ""); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("no user key: want ErrNotConfigured, got %v", err)
	}

	srv, _ := fake(t, 400, `{"device":"invalid","errors":["device name is not valid for user"],"status":0}`)
	c := New("app", "owner")
	c.SetEndpoint(srv.URL)
	err := c.SendDevices(context.Background(), []string{"nope"}, "", "m", "")
	var rej *RejectedError
	if !errors.As(err, &rej) || !rej.DeviceNotFound() || rej.Status != 400 {
		t.Fatalf("want a device-not-found rejection, got %#v", err)
	}

	srv2, _ := fake(t, 400, `{"user":"invalid","errors":["user identifier is not a valid user, group, or subscribed user key"],"status":0}`)
	c.SetEndpoint(srv2.URL)
	err = c.SendDevices(context.Background(), []string{"a"}, "", "m", "")
	if !errors.As(err, &rej) || rej.DeviceNotFound() {
		t.Fatalf("a bad user key is not a missing device: %#v", err)
	}

	srv3, _ := fake(t, 500, `oops`)
	c.SetEndpoint(srv3.URL)
	err = c.SendDevices(context.Background(), []string{"a"}, "", "m", "")
	if !errors.As(err, &rej) || rej.Status != 500 || rej.DeviceNotFound() {
		t.Fatalf("want a plain 500 rejection, got %#v", err)
	}
}
