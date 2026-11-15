// Package dashboard serves the provider status UI: generated URLs, probe
// state, skip reasons, sync times, plus gated edit endpoints (rename /
// domain pick, per-app toggle) that require dashboard.edit and persist
// only to overrides.json.
//
// Exposure model: plain LAN bind by default; Traefik-routed only with a
// CIDR allowlist (generate refuses to publish the route otherwise — the
// edit API must never be public).
package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/macedot/traefik-proxmenux-provider/internal/overrides"
	"github.com/macedot/traefik-proxmenux-provider/internal/state"
	"github.com/macedot/traefik-proxmenux-provider/internal/transform"
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

// NewMux builds the handler set: read-only views plus (when o.Edit) the
// single mutation endpoint — slug overrides, persisted locally.
func NewMux(h *state.Holder, o Options) *http.ServeMux {
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
	mux.HandleFunc("/api/slug", func(w http.ResponseWriter, r *http.Request) {
		writeJSON := func(code int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		if r.Method != http.MethodPost && r.Method != http.MethodDelete {
			w.Header().Set("Allow", "POST, DELETE")
			writeJSON(http.StatusMethodNotAllowed, map[string]string{"error": "POST or DELETE only"})
			return
		}
		if !o.Edit || o.Store == nil {
			writeJSON(http.StatusForbidden, map[string]string{"error": "editing is disabled (dashboard.edit: false)"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			writeJSON(http.StatusBadRequest, map[string]string{"error": "read body"})
			return
		}
		var req struct {
			VMID   int    `json:"vmid"`
			Key    string `json:"key"` // infra entries ("infra:<slug>"); vmid otherwise
			Slug   string `json:"slug"`
			Domain string `json:"domain"` // optional: pick from cfg domains; empty = default
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(http.StatusBadRequest, map[string]string{"error": "bad json"})
			return
		}
		ident := req.Key
		if ident == "" && req.VMID > 0 {
			ident = strconv.Itoa(req.VMID)
		}
		if ident == "" || len(ident) > 64 || strings.ContainsAny(ident, "/\\ 	\n") {
			writeJSON(http.StatusBadRequest, map[string]string{"error": "vmid or key required"})
			return
		}
		if req.Domain != "" {
			okDomain := false
			for _, d := range o.Domains {
				if d == req.Domain {
					okDomain = true
					break
				}
			}
			if !okDomain {
				writeJSON(http.StatusBadRequest, map[string]string{"error": "domain not in the configured list"})
				return
			}
		}
		switch r.Method {
		case http.MethodPost:
			slug := transform.Slugify(req.Slug)
			if slug == "" {
				writeJSON(http.StatusBadRequest, map[string]string{"error": "slug must contain alphanumeric characters"})
				return
			}
			if err := o.Store.SetSlug(ident, slug); err != nil {
				writeJSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			domainSet := ""
			if req.Domain != "" {
				if err := o.Store.SetDomain(ident, req.Domain); err != nil {
					writeJSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
					return
				}
				domainSet = req.Domain
			} else {
				_, _ = o.Store.ClearDomain(ident) // empty = back to the default domain
			}
			signalResync(o)
			writeJSON(http.StatusOK, map[string]any{"ok": true, "id": ident, "slug": slug, "domain": domainSet})
		case http.MethodDelete:
			existed, err := o.Store.ClearSlug(ident)
			if err != nil {
				writeJSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			_, _ = o.Store.ClearDomain(ident)
			signalResync(o)
			writeJSON(http.StatusOK, map[string]any{"ok": true, "id": ident, "cleared": existed})
		}
	})
	mux.HandleFunc("/api/enabled", func(w http.ResponseWriter, r *http.Request) {
		writeJSON := func(code int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeJSON(http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		if !o.Edit || o.Store == nil {
			writeJSON(http.StatusForbidden, map[string]string{"error": "editing is disabled (dashboard.edit: false)"})
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil {
			writeJSON(http.StatusBadRequest, map[string]string{"error": "read body"})
			return
		}
		var req struct {
			VMID     int    `json:"vmid"`
			Key      string `json:"key"` // infra entries ("infra:<slug>"); vmid otherwise
			Disabled bool   `json:"disabled"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSON(http.StatusBadRequest, map[string]string{"error": "bad json"})
			return
		}
		key := req.Key
		if key == "" && req.VMID > 0 {
			key = strconv.Itoa(req.VMID)
		}
		if key == "" || len(key) > 64 || strings.ContainsAny(key, "/\\ 	\n") {
			writeJSON(http.StatusBadRequest, map[string]string{"error": "vmid or key required"})
			return
		}
		if err := o.Store.SetDisabled(key, req.Disabled); err != nil {
			writeJSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		signalResync(o)
		writeJSON(http.StatusOK, map[string]any{"ok": true, "key": key, "disabled": req.Disabled})
	})
	return mux
}

func signalResync(o Options) {
	if o.Resync == nil {
		return
	}
	select {
	case o.Resync <- struct{}{}:
	default: // a resync is already pending
	}
}

// Options wires the dashboard to the provider runtime.
type Options struct {
	Edit    bool             // allow slug edits (POST/DELETE /api/slug)
	Store   *overrides.Store // overrides persistence (required when Edit)
	Resync  chan struct{}    // signalled after a successful edit
	Domains []string         // pickable URL domains (Base first)
}

// Serve runs the dashboard until ctx is cancelled.
func Serve(ctx context.Context, listen string, h *state.Holder, o Options, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              listen,
		Handler:           NewMux(h, o),
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
