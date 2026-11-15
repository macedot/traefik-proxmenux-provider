package transform

import (
	"net"
	"strings"
	"testing"

	"github.com/macedot/traefik-proxmenux-provider/internal/config"
	"github.com/macedot/traefik-proxmenux-provider/internal/proxmenux"
)

func base() *config.Config {
	cfg, err := config.Parse([]byte("proxmenux:\n  api_url: http://h:8008\ndomain:\n  base: home.example.com\n"))
	if err != nil {
		panic(err)
	}
	return cfg
}

func watch(name string, port int) proxmenux.Watch {
	return proxmenux.Watch{
		ID: name, Name: name, InstalledVia: "oci_image",
		Ports: []proxmenux.Port{{Port: port, Scheme: "http", WebPath: "/"}},
	}
}

func guest(vmid int, name, gtype, ip, status string, watches ...proxmenux.Watch) proxmenux.Guest {
	return proxmenux.Guest{
		VMID: vmid, Name: name, Type: gtype, IP: ip, Status: status,
		Tags: "oci;proxmenux", AppWatches: watches,
	}
}

func openPort(t *testing.T) (port int, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln.Addr().(*net.TCPAddr).Port, func() { ln.Close() }
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"AdGuard Home":       "adguard-home",
		"  Jellystat!!  ":    "jellystat",
		"Over seas__Errand":  "over-seas-errand",
		"---weird---":        "weird",
		"Ünïcode Thing":      "n-code-thing",
		"reclip-dashboard-1": "reclip-dashboard-1",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 80) + "-z"
	if got := Slugify(long); len(got) != 63 || strings.HasSuffix(got, "-") {
		t.Errorf("Slugify(long) len=%d; want 63 chars, no trailing dash", len(got))
	}
}

func TestTransformHappyPath(t *testing.T) {
	port, stop := openPort(t)
	defer stop()

	cfg := base()
	cfg.ProxMenux.LANResolved = "127.0.0.0/24"
	g := guest(100, "adguard", "lxc", "127.0.0.1", "running", watch("AdGuard Home", port))
	res := Transform([]proxmenux.Guest{g}, nil, cfg)

	if len(res.Routes) != 1 {
		t.Fatalf("routes = %d (%+v), want 1", len(res.Routes), res.Skipped)
	}
	r := res.Routes[0]
	if r.Slug != "adguard-home" || r.Hostname != "adguard-home.home.example.com" {
		t.Errorf("slug/hostname = %s / %s", r.Slug, r.Hostname)
	}
	if r.Target != "http://127.0.0.1:"+itoa(port) {
		t.Errorf("target = %s", r.Target)
	}
	if r.Stale || !r.Served || r.Kind != "app" {
		t.Errorf("flags wrong: %+v", r)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("skipped = %+v", res.Skipped)
	}
}

func TestVmsAreSkippedSilently(t *testing.T) {
	cfg := base()
	cfg.Routing.Probe = false
	g := guest(400, "algo", "qemu", "127.0.0.1", "running", watch("ALGO", 80))
	res := Transform([]proxmenux.Guest{g}, nil, cfg)
	if len(res.Routes) != 0 || len(res.Skipped) != 0 {
		t.Fatalf("vm guest must be ignored: %+v %+v", res.Routes, res.Skipped)
	}
}

func TestStaleIsTaggedNotDropped(t *testing.T) {
	cfg := base()
	cfg.ProxMenux.LANResolved = "127.0.0.0/24"
	g := guest(117, "dead", "lxc", "127.0.0.1", "running", watch("Dead App", 1))
	res := Transform([]proxmenux.Guest{g}, nil, cfg)
	if len(res.Routes) != 1 {
		t.Fatalf("stale route must stay visible: %+v", res.Skipped)
	}
	if !res.Routes[0].Stale || res.Routes[0].Served {
		t.Errorf("stale flags wrong: %+v", res.Routes[0])
	}
	if len(res.Skipped) != 0 {
		t.Errorf("stale must not be a skip: %+v", res.Skipped)
	}
}

func TestProbeDisabledServesEverything(t *testing.T) {
	cfg := base()
	cfg.ProxMenux.LANResolved = "127.0.0.0/24"
	cfg.Routing.Probe = false
	g := guest(117, "dead", "lxc", "127.0.0.1", "running", watch("Dead App", 1))
	res := Transform([]proxmenux.Guest{g}, nil, cfg)
	if len(res.Routes) != 1 || res.Routes[0].Stale || !res.Routes[0].Served {
		t.Fatalf("probe off: %+v", res.Routes)
	}
}

func TestSkipReasons(t *testing.T) {
	cfg := base()
	guests := []proxmenux.Guest{
		guest(113, "plex", "lxc", "10.0.0.9", "stopped", watch("Plex", 32400)),
		guest(116, "noports", "lxc", "10.0.0.11", "running", proxmenux.Watch{Name: "X", InstalledVia: "oci_image"}),
	}
	res := Transform(guests, nil, cfg)
	if len(res.Routes) != 0 {
		t.Fatalf("routes = %d, want 0", len(res.Routes))
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("skipped = %d, want 2: %+v", len(res.Skipped), res.Skipped)
	}
	reasons := map[int]string{}
	for _, s := range res.Skipped {
		reasons[s.VMID] = s.Reason
	}
	for vmid, frag := range map[int]string{
		113: "not running", 116: "no discoverable ports",
	} {
		if !strings.Contains(reasons[vmid], frag) {
			t.Errorf("vmid %d reason = %q, want substring %q", vmid, reasons[vmid], frag)
		}
	}
}

func TestCollisionDedupe(t *testing.T) {
	cfg := base()
	cfg.Routing.Probe = false
	g1 := guest(100, "a1", "lxc", "10.0.0.1", "running", watch("Same App", 80))
	g2 := guest(200, "a2", "lxc", "10.0.0.2", "running", watch("Same App", 80))
	res := Transform([]proxmenux.Guest{g1, g2}, nil, cfg)
	if len(res.Routes) != 2 {
		t.Fatalf("routes = %d, want 2", len(res.Routes))
	}
	slugs := map[string]bool{}
	for _, r := range res.Routes {
		slugs[r.Slug] = true
	}
	if !slugs["same-app"] || !slugs["same-app-200"] {
		t.Errorf("slugs = %v, want same-app + same-app-200", slugs)
	}
}

func TestDiscoveryExtrasAndIPPortDedupe(t *testing.T) {
	cfg := base()
	cfg.Routing.Probe = false
	// Guest with no app watch, discovered via docker discovery (ProxDnx
	// reports such guests — e.g. dockhand).
	extras := map[int][]ServicePort{
		193: {{Name: "dockhand", Port: 3000, Scheme: "http", Source: "docker"}},
	}
	g := guest(193, "docker", "lxc", "10.0.0.193", "running")
	res := Transform([]proxmenux.Guest{g}, extras, cfg)
	if len(res.Routes) != 1 || res.Routes[0].Slug != "dockhand" {
		t.Fatalf("discovery route wrong: %+v", res.Routes)
	}

	// Watch port + duplicate discovery entry for the same ip:port → one route.
	g2 := guest(100, "adguard", "lxc", "10.0.0.100", "running", watch("AdGuard Home", 8080))
	extras[100] = []ServicePort{{Name: "AdGuard Home", Port: 8080, Scheme: "http", Source: "docker"}}
	res2 := Transform([]proxmenux.Guest{g2}, extras, cfg)
	if len(res2.Routes) != 1 {
		t.Fatalf("ip:port dedupe failed: %+v", res2.Routes)
	}
}

func TestAllowDenyAndReserved(t *testing.T) {
	cfg := base()
	cfg.Routing.Probe = false
	cfg.Expose.Deny = []string{"secret-app"}
	g := guest(100, "a", "lxc", "10.0.0.1", "running",
		watch("Secret App", 80), watch("Public App", 80))
	res := Transform([]proxmenux.Guest{g}, nil, cfg)
	if len(res.Routes) != 1 || res.Routes[0].Slug != "public-app" {
		t.Fatalf("deny routes = %+v", res.Routes)
	}

	cfg2 := base()
	cfg2.Routing.Probe = false
	cfg2.Expose.Allow = []string{"public-app"}
	res2 := Transform([]proxmenux.Guest{g}, nil, cfg2)
	if len(res2.Routes) != 1 || res2.Routes[0].Slug != "public-app" {
		t.Fatalf("allow routes = %+v", res2.Routes)
	}

	cfg3 := base()
	cfg3.Routing.Probe = false
	cfg3.Dashboard.ViaTraefik = true
	cfg3.Dashboard.Hostname = "provider"
	g3 := guest(100, "a", "lxc", "10.0.0.1", "running", watch("Provider", 8081))
	res3 := Transform([]proxmenux.Guest{g3}, nil, cfg3)
	if len(res3.Routes) != 0 || !strings.Contains(res3.Skipped[0].Reason, "reserved") {
		t.Fatalf("reserved hostname: %+v %+v", res3.Routes, res3.Skipped)
	}
}

func TestSlugOverrideAndHostnameSource(t *testing.T) {
	cfg := base()
	cfg.Routing.Probe = false
	cfg.Domain.SlugOverrides = map[string]string{"AdGuard Home": "dns"}
	g := guest(100, "adguard", "lxc", "10.0.0.100", "running", watch("AdGuard Home", 80))
	res := Transform([]proxmenux.Guest{g}, nil, cfg)
	if res.Routes[0].Slug != "dns" {
		t.Errorf("override slug = %q, want dns", res.Routes[0].Slug)
	}

	cfg2 := base()
	cfg2.Routing.Probe = false
	cfg2.Domain.SlugSource = "hostname"
	g2 := guest(100, "adguard", "lxc", "10.0.0.100", "running", watch("AdGuard Home", 80))
	res2 := Transform([]proxmenux.Guest{g2}, nil, cfg2)
	if res2.Routes[0].Slug != "adguard" {
		t.Errorf("hostname-source slug = %q, want adguard", res2.Routes[0].Slug)
	}
}

func TestPickIPPrefersLanSubnet(t *testing.T) {
	port, stop := openPort(t)
	defer stop()

	cfg := base()
	cfg.ProxMenux.LANResolved = "127.0.0.0/24"
	// Arr-style guest: monitor reports the VPN IP first, LAN IP second.
	g := guest(109, "qbittorrent", "lxc", "198.51.100.33", "running", watch("Qbittorrent", port))
	g.IPs = []string{"127.0.0.1", "198.51.100.33"}
	res := Transform([]proxmenux.Guest{g}, nil, cfg)
	if len(res.Routes) != 1 {
		t.Fatalf("routes = %d (%+v), want 1", len(res.Routes), res.Skipped)
	}
	if res.Routes[0].IP != "127.0.0.1" || res.Routes[0].Target != "http://127.0.0.1:"+itoa(port) {
		t.Errorf("ip/target = %s / %s, want LAN IP picked", res.Routes[0].IP, res.Routes[0].Target)
	}
}

func TestPickIPFallsBackToUsable(t *testing.T) {
	cfg := base()
	cfg.Routing.Probe = false
	g := guest(115, "byparr", "lxc", "198.51.100.39", "running", watch("FlareSolverr", 8191))
	g.IPs = []string{"198.51.100.39"}
	cfg.ProxMenux.LANResolved = "127.0.0.0/24"
	res := Transform([]proxmenux.Guest{g}, nil, cfg)
	if len(res.Routes) != 1 || res.Routes[0].IP != "198.51.100.39" {
		t.Fatalf("fallback pick wrong: %+v", res.Routes)
	}
}

func TestSidecarsAreReported(t *testing.T) {
	cfg := base() // no source filter: sidecars are reported like ProxDnx
	cfg.Routing.Probe = false
	g := guest(111, "sonarr", "lxc", "10.0.0.150", "running", watch("Sonarr", 8989))
	res := Transform([]proxmenux.Guest{g}, nil, cfg)
	if len(res.Routes) != 1 || res.Routes[0].Slug != "sonarr" {
		t.Fatalf("sidecar not reported: %+v (%+v)", res.Routes, res.Skipped)
	}
}

func TestBuildInfraRoutes(t *testing.T) {
	cfg := base()
	infra := BuildInfraRoutes(cfg, "pve-host", "192.0.2.99")
	if len(infra) != 4 {
		t.Fatalf("infra routes = %d, want 4", len(infra))
	}
	want := map[string]string{
		"pve-host.home.example.com":       "https://192.0.2.99:8006",
		"pve-host.menux.home.example.com": "http://192.0.2.99:8008",
		"menux.home.example.com":       "http://192.0.2.99:8008",
		"proxmenux.home.example.com":   "http://192.0.2.99:8008",
	}
	for _, r := range infra {
		if want[r.Hostname] != r.Target {
			t.Errorf("%s -> %s, want %s", r.Hostname, r.Target, want[r.Hostname])
		}
		if r.Kind != "infra" || !r.Served {
			t.Errorf("infra flags wrong: %+v", r)
		}
	}
	if !infra[0].Insecure {
		t.Errorf("PVE route must be insecure-skip-verify (self-signed)")
	}

	if BuildInfraRoutes(cfg, "", "1.2.3.4") != nil {
		t.Error("empty host must yield nil")
	}
	off := *cfg
	off.Infra.Enabled = false
	if BuildInfraRoutes(&off, "pve-host", "1.2.3.4") != nil {
		t.Error("infra disabled must yield nil")
	}
}
