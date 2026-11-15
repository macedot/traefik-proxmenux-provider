package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/macedot/traefik-proxmenux-provider/internal/config"
)

func TestDomainOffKeys(t *testing.T) {
	m := map[string]bool{"b.example": true, "a.example": true, "kept.example": false}
	got := domainOffKeys(m)
	if len(got) != 2 || got[0] != "a.example" || got[1] != "b.example" {
		t.Fatalf("domainOffKeys = %v, want sorted [a.example b.example]", got)
	}
	if got := domainOffKeys(nil); len(got) != 0 {
		t.Fatalf("nil map = %v", got)
	}
}

func TestNewLogger(t *testing.T) {
	ctx := context.Background()
	for _, level := range []string{"debug", "info", "warn", "error", "bogus"} {
		if l := newLogger(level); l == nil {
			t.Fatalf("newLogger(%q) = nil", level)
		}
	}
	if l := newLogger("debug"); !l.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("debug logger must be enabled at debug level")
	}
	if l := newLogger("info"); l.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("info logger must not enable debug level")
	}
}

func TestResolveLANExplicit(t *testing.T) {
	cfg := config.Defaults()
	cfg.ProxMenux.LANCIDR = "10.99.0.0/16"
	if err := resolveLAN(cfg); err != nil {
		t.Fatalf("resolveLAN: %v", err)
	}
	if cfg.ProxMenux.LANResolved != "10.99.0.0/16" {
		t.Fatalf("LANResolved = %q", cfg.ProxMenux.LANResolved)
	}
}

func TestResolveLANHostNetwork(t *testing.T) {
	// multi-NIC guests: the host's own network (the monitor API address)
	// wins over the provider CT's interfaces
	cfg := config.Defaults()
	cfg.ProxMenux.APIURL = "http://192.0.2.99:8008"
	cfg.ProxMenux.LANResolved = "203.0.113.0/24" // stale value must be replaced
	if err := resolveLAN(cfg); err != nil {
		t.Fatalf("resolveLAN: %v", err)
	}
	if cfg.ProxMenux.LANResolved != "192.0.2.0/24" {
		t.Fatalf("LANResolved = %q, want host subnet 192.0.2.0/24", cfg.ProxMenux.LANResolved)
	}

	// hostname (unparseable IP) falls back without error
	cfg2 := config.Defaults()
	cfg2.ProxMenux.APIURL = "http://monitor.example.com:8008"
	cfg2.ProxMenux.LANResolved = "203.0.113.0/24"
	if err := resolveLAN(cfg2); err != nil {
		t.Fatalf("resolveLAN fallback: %v", err)
	}
}
