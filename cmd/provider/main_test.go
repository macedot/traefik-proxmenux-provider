package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
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
	// multi-NIC guests: with no default gateway observable, the host's own
	// network (the monitor API address) is used
	old := routeFile
	routeFile = filepath.Join(t.TempDir(), "route") // no default route
	defer func() { routeFile = old }()

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

func TestDefaultGateway(t *testing.T) {
	// real /proc/net/route format: gateway hex is little-endian
	f := filepath.Join(t.TempDir(), "route")
	os.WriteFile(f, []byte("Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n"+
		"eth0\t00000000\t630200C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"+
		"eth0\t0A04020A\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n"), 0o600)
	got, ok := defaultGateway(f)
	if !ok || got != "192.0.2.99" {
		t.Fatalf("defaultGateway = %q, %v; want 192.0.2.99", got, ok)
	}

	// no default route → not ok
	f2 := filepath.Join(t.TempDir(), "route2")
	os.WriteFile(f2, []byte("Iface\tDestination\tGateway\tFlags\neth0\t0A04020A\t00000000\t0001\n"), 0o600)
	if _, ok := defaultGateway(f2); ok {
		t.Fatal("no default route must return not-ok")
	}
}

func TestResolveLANBridgeGateway(t *testing.T) {
	// the CT's default gateway IS the host's address on the configured
	// adapter: the resolved subnet must be the gateway's /24
	old := routeFile
	routeFile = filepath.Join(t.TempDir(), "route")
	defer func() { routeFile = old }()
	os.WriteFile(routeFile, []byte("Iface\tDestination\tGateway\tFlags\neth0\t00000000\t630200C0\t0003\n"), 0o600)

	cfg := config.Defaults()
	cfg.ProxMenux.APIURL = "http://192.0.2.99:8008" // host-IP fallback must NOT win
	cfg.ProxMenux.LANResolved = "203.0.113.0/24"
	if err := resolveLAN(cfg); err != nil {
		t.Fatalf("resolveLAN: %v", err)
	}
	if cfg.ProxMenux.BridgeResolved != "192.0.2.99" {
		t.Fatalf("BridgeResolved = %q", cfg.ProxMenux.BridgeResolved)
	}
	if cfg.ProxMenux.LANResolved != "192.0.2.0/24" {
		t.Fatalf("LANResolved = %q, want gateway subnet 192.0.2.0/24", cfg.ProxMenux.LANResolved)
	}
}

func TestResolveLANBridgeInvalidName(t *testing.T) {
	_, err := config.Parse([]byte("proxmenux:\n  api_url: http://h:8008\ndomain:\n  base: example.com\n  bridge_check: 1\nproxmenux_bad: 1\n"))
	_ = err
	// bridge name validation lives in Validate: craft config with bad name
	c := config.Defaults()
	c.ProxMenux.Bridge = "bad name with spaces"
	if err := c.Validate(); err == nil {
		t.Fatal("invalid bridge name must fail validation")
	}
}
