// Package dashboard serves the read-only status UI: every generated URL
// per container, probe results, skip reasons, sync times.
//
// Security by construction: no mutation endpoints, no secrets in the
// snapshot, plain LAN bind by default (or Traefik-routed with a CIDR
// allowlist via dashboard.via_traefik).
package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/macedot/traefik-proxmenux-provider/internal/state"
)

//go:embed static/index.html
var indexHTML embed.FS

// readOnly enforces the GET/HEAD-only contract on every handler.
func readOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "read-only endpoint", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

// NewMux builds the read-only handler set.
func NewMux(h *state.Holder) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", readOnly(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		b, _ := indexHTML.ReadFile("static/index.html")
		_, _ = w.Write(b)
	}))
	mux.HandleFunc("/api/routes", readOnly(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(h.Get())
	}))
	mux.HandleFunc("/healthz", readOnly(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	return mux
}

// Serve runs the dashboard until ctx is cancelled.
func Serve(ctx context.Context, listen string, h *state.Holder, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              listen,
		Handler:           NewMux(h),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	done := make(chan error, 1)
	go func() {
		log.Info("dashboard listening", "addr", listen)
		done <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return nil
	case err := <-done:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
