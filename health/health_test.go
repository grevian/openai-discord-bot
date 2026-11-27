package health

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func yes() bool { return true }
func no() bool  { return false }

func src() Sources {
	return Sources{Version: "abc", Start: time.Now(), Ready: yes, DiscordConnected: yes, Degraded: no, Draining: no}
}

func TestEvaluate(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name     string
		mod      func(*Sources)
		status   string
		degraded bool
	}{
		{"ok", func(*Sources) {}, "ok", false},
		{"not ready", func(s *Sources) { s.Ready = no }, "starting", false},
		{"discord down", func(s *Sources) { s.DiscordConnected = no }, "unhealthy", false},
		{"draining wins", func(s *Sources) { s.Draining = yes; s.Ready = no; s.DiscordConnected = no }, "draining", false},
		{"degraded still ok", func(s *Sources) { s.Degraded = yes }, "ok", true},
		{"nil funcs", func(s *Sources) { *s = Sources{Version: "abc", Start: now} }, "starting", false},
	}
	for _, c := range cases {
		s := src()
		c.mod(&s)
		r := Evaluate(s, now)
		if r.Status != c.status || r.OpenAIDegraded != c.degraded {
			t.Errorf("%s: got %+v", c.name, r)
		}
		if (r.Status == "ok") != (len(r.Reasons) == 0) {
			t.Errorf("%s: reasons %v do not match status", c.name, r.Reasons)
		}
	}
}

func TestHandler(t *testing.T) {
	h := Handler(src())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"version":"abc"`) || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers: %v", rec.Header())
	}

	s := src()
	s.DiscordConnected = no
	rec = httptest.NewRecorder()
	Handler(s).ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), `"status":"unhealthy"`) {
		t.Errorf("unhealthy: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/healthz", nil))
	if rec.Code != 405 {
		t.Errorf("POST: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/other", nil))
	if rec.Code != 404 {
		t.Errorf("/other: %d", rec.Code)
	}
}

func TestServe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Find a free port, then release it for Serve.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	if err := Serve(ctx, addr, Handler(src())); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"status":"ok"`) {
		t.Fatalf("got %d %s", resp.StatusCode, b)
	}
	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			break
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatal("server still accepting after ctx cancel")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestServeBindFailure(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if err := Serve(context.Background(), l.Addr().String(), Handler(src())); err == nil {
		t.Fatal("expected a bind error")
	}
}
