package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/macedot/traefik-proxmenux-provider/internal/state"
	"github.com/macedot/traefik-proxmenux-provider/internal/transform"
)

func TestHandlers(t *testing.T) {
	h := state.New("test-v1")
	snap := h.Bump()
	snap.Domain = "home.example.com"
	snap.TLSMode = "internal"
	snap.Source = "oci"
	snap.Entrypoint = "websecure"
	snap.GeneratedAt = time.Now()
	snap.Routes = []transform.Route{{
		Slug: "adguard-home", Hostname: "adguard-home.home.example.com",
		AppName: "AdGuard Home", VMID: 100, GuestName: "adguard",
		Target: "http://192.0.2.2:3000",
	}}
	snap.Skipped = []transform.Skipped{{VMID: 117, AppName: "Dead App", Reason: "probe failed"}}
	h.Update(snap)

	srv := httptest.NewServer(NewMux(h))
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
	go func() { done <- Serve(ctx, "127.0.0.1:0", h, discardLogger()) }()
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
