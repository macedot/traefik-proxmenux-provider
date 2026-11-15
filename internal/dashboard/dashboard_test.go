package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/macedot/traefik-proxmenux-provider/internal/overrides"
	"github.com/macedot/traefik-proxmenux-provider/internal/state"
	"github.com/macedot/traefik-proxmenux-provider/internal/transform"
)

func TestHandlers(t *testing.T) {
	h := state.New("test-v1")
	snap := h.Bump()
	snap.Domain = "home.example.com"
	snap.TLSMode = "internal"
	snap.Entrypoint = "websecure"
	snap.AuthMode = "open"
	snap.EditEnabled = false
	snap.GeneratedAt = time.Now()
	snap.Routes = []transform.Route{{
		Slug: "adguard-home", Hostname: "adguard-home.home.example.com",
		AppName: "AdGuard Home", VMID: 100, GuestName: "adguard",
		Target: "http://192.0.2.2:3000",
	}}
	snap.Skipped = []transform.Skipped{{VMID: 117, AppName: "Dead App", Reason: "probe failed"}}
	h.Update(snap)

	srv := httptest.NewServer(NewMux(h, Options{}))
	defer srv.Close()
	cli := srv.Client()

	// /api/routes reflects the snapshot.
	resp, err := cli.Get(srv.URL + "/api/routes")
	if err != nil {
		t.Fatalf("GET /api/routes: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	var got state.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != "test-v1" || got.Domain != "home.example.com" {
		t.Errorf("snapshot = %+v", got)
	}
	if len(got.Routes) != 1 || got.Routes[0].Hostname != "adguard-home.home.example.com" {
		t.Errorf("routes = %+v", got.Routes)
	}
	if len(got.Skipped) != 1 || got.Skipped[0].Reason == "" {
		t.Errorf("skipped = %+v", got.Skipped)
	}

	// /healthz
	resp2, err := cli.Get(srv.URL + "/healthz")
	if err != nil || resp2.StatusCode != 200 {
		t.Fatalf("healthz: %v %v", err, resp2)
	}
	resp2.Body.Close()

	// / serves the embedded UI with no-store.
	resp3, err := cli.Get(srv.URL + "/")
	if err != nil || resp3.StatusCode != 200 {
		t.Fatalf("index: %v %v", err, resp3)
	}
	defer resp3.Body.Close()
	buf := make([]byte, 64)
	n, _ := resp3.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), "<!doctype html>") {
		t.Error("index not HTML")
	}
	if cc := resp3.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("cache-control = %q", cc)
	}

	// Unknown path / non-GET → not allowed (read-only by construction).
	resp4, _ := cli.Post(srv.URL+"/api/routes", "application/json", strings.NewReader("{}"))
	resp4.Body.Close()
	if resp4.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("POST /api/routes = %d, want 405 (read-only)", resp4.StatusCode)
	}
}

func TestServeShutdown(t *testing.T) {
	h := state.New("t")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, "127.0.0.1:0", h, Options{}, discardLogger()) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not shut down")
	}
}

func TestSlugEditEndpoint(t *testing.T) {
	h := state.New("t")
	dir := t.TempDir()
	store := overrides.Open(filepath.Join(dir, "overrides.json"))
	resync := make(chan struct{}, 1)
	srv := httptest.NewServer(NewMux(h, Options{Edit: true, Store: store, Resync: resync}))
	defer srv.Close()
	cli := srv.Client()

	// POST persists + signals resync.
	resp, err := cli.Post(srv.URL+"/api/slug", "application/json",
		strings.NewReader(`{"vmid":100,"slug":"My DNS!"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"slug":"my-dns"`) {
		t.Fatalf("POST status=%d body=%s", resp.StatusCode, body)
	}
	f, err := store.Load()
	if err != nil || f.SlugOverrides["100"] != "my-dns" {
		t.Fatalf("overrides file = %+v err=%v", f, err)
	}
	select {
	case <-resync:
	default:
		t.Error("resync not signalled")
	}

	// DELETE clears it.
	req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/slug",
		strings.NewReader(`{"vmid":100}`))
	resp2, err := cli.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("DELETE status=%d", resp2.StatusCode)
	}
	f2, _ := store.Load()
	if len(f2.SlugOverrides) != 0 {
		t.Fatalf("override not cleared: %+v", f2.SlugOverrides)
	}
}

func TestSlugEditDisabled(t *testing.T) {
	h := state.New("t")
	srv := httptest.NewServer(NewMux(h, Options{}))
	defer srv.Close()
	resp, err := srv.Client().Post(srv.URL+"/api/slug", "application/json",
		strings.NewReader(`{"vmid":100,"slug":"x"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 when editing disabled", resp.StatusCode)
	}
}

func TestSlugEditValidation(t *testing.T) {
	h := state.New("t")
	store := overrides.Open(filepath.Join(t.TempDir(), "overrides.json"))
	resync := make(chan struct{}, 1)
	srv := httptest.NewServer(NewMux(h, Options{Edit: true, Store: store, Resync: resync}))
	defer srv.Close()

	for _, body := range []string{`{"vmid":100,"slug":"!!!"}`, `{"slug":"x"}`, `not json`} {
		resp, err := srv.Client().Post(srv.URL+"/api/slug", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST %q: %v", body, err)
		}
		resp.Body.Close()
		if resp.StatusCode != 400 {
			t.Fatalf("POST %q status = %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestEnabledEndpoint(t *testing.T) {
	h := state.New("t")
	store := overrides.Open(filepath.Join(t.TempDir(), "overrides.json"))
	resync := make(chan struct{}, 1)
	srv := httptest.NewServer(NewMux(h, Options{Edit: true, Store: store, Resync: resync}))
	defer srv.Close()
	cli := srv.Client()

	resp, err := cli.Post(srv.URL+"/api/enabled", "application/json",
		strings.NewReader(`{"vmid":100,"disabled":true}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	f, _ := store.Load()
	if !f.Disabled["100"] {
		t.Fatalf("disabled flag not persisted: %+v", f)
	}
	select {
	case <-resync:
	default:
		t.Error("resync not signalled")
	}

	// Re-enable stores an explicit ON (false); absent keys fall back to
	// the config default (routing.default_enabled).
	resp2, _ := cli.Post(srv.URL+"/api/enabled", "application/json",
		strings.NewReader(`{"vmid":100,"disabled":false}`))
	resp2.Body.Close()
	f2, _ := store.Load()
	if f2.Disabled["100"] {
		t.Fatalf("expected explicit enabled (false), got: %+v", f2.Disabled)
	}
}

func TestDomainInfraSwitches(t *testing.T) {
	h := state.New("t")
	store := overrides.Open(filepath.Join(t.TempDir(), "overrides.json"))
	resync := make(chan struct{}, 2)
	srv := httptest.NewServer(NewMux(h, Options{Edit: true, Store: store, Resync: resync, Domains: []string{"a.example.com", "b.example.com"}}))
	defer srv.Close()

	// domain switch requires a configured domain
	resp, _ := srv.Client().Post(srv.URL+"/api/domain", "application/json",
		strings.NewReader(`{"domain":"nope.example.com","disabled":true}`))
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("unknown domain must 400, got %d", resp.StatusCode)
	}
	resp, _ = srv.Client().Post(srv.URL+"/api/domain", "application/json",
		strings.NewReader(`{"domain":"a.example.com","disabled":true}`))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("domain off: %d", resp.StatusCode)
	}
	resp, _ = srv.Client().Post(srv.URL+"/api/infra", "application/json",
		strings.NewReader(`{"disabled":true}`))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("infra off: %d", resp.StatusCode)
	}
	f, _ := store.Load()
	if !f.DomainsOff["a.example.com"] || !f.InfraOff {
		t.Fatalf("switches not persisted: %+v", f)
	}
	select {
	case <-resync:
	default:
		t.Error("resync not signalled")
	}
}
