// Package health serves the bot's loopback /healthz endpoint, which the
// deploy and Instance Status read over SSM. It imports only the standard
// library and exposes no secrets, IDs or user text.
package health

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"
)

// Report is the JSON body of /healthz.
type Report struct {
	Status           string   `json:"status"` // ok, starting, unhealthy or draining
	Version          string   `json:"version"`
	UptimeSeconds    int64    `json:"uptime_seconds"`
	DiscordConnected bool     `json:"discord_connected"`
	OpenAIDegraded   bool     `json:"openai_degraded"`
	Reasons          []string `json:"reasons"`
}

// Sources are the live signals behind a report. A nil func counts as false.
type Sources struct {
	Version          string
	Start            time.Time
	Ready            func() bool
	DiscordConnected func() bool
	Degraded         func() bool
	Draining         func() bool
}

func get(f func() bool) bool { return f != nil && f() }

// Evaluate computes the report. OpenAI degraded mode is reported but never
// changes the status: a rollback can't fix it.
func Evaluate(s Sources, now time.Time) Report {
	r := Report{
		Version:          s.Version,
		UptimeSeconds:    int64(now.Sub(s.Start).Seconds()),
		DiscordConnected: get(s.DiscordConnected),
		OpenAIDegraded:   get(s.Degraded),
		Reasons:          []string{},
	}
	switch {
	case get(s.Draining):
		r.Status = "draining"
		r.Reasons = append(r.Reasons, "draining")
	case !get(s.Ready):
		r.Status = "starting"
		r.Reasons = append(r.Reasons, "starting")
	case !r.DiscordConnected:
		r.Status = "unhealthy"
		r.Reasons = append(r.Reasons, "discord gateway not connected")
	default:
		r.Status = "ok"
	}
	return r
}

// Handler serves GET/HEAD /healthz: 200 when ok, 503 otherwise.
func Handler(s Sources) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r := Evaluate(s, time.Now())
		body, err := json.Marshal(r)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Status == "ok" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		if req.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	})
	return mux
}

// Serve binds addr synchronously (so a bind failure is returned), then serves
// in the background until ctx is cancelled.
func Serve(ctx context.Context, addr string, h http.Handler) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
		MaxHeaderBytes:    4096,
	}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	return nil
}
