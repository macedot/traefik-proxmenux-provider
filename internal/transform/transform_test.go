package transform

import (
	"strings"
	"testing"
	"time"

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

func watch(name, via string, port int) proxmenux.Watch {
	return proxmenux.Watch{
		ID: name, Name: name, InstalledVia: via,
		Ports: []proxmenux.Port{{Port: port, Scheme: "http", WebPath: "/"}},
	}
}

func guest(vmid int, name, ip, status string, watches ...proxmenux.Watch) proxmenux.Guest {
	return proxmenux.Guest{
		VMID: vmid, Name: name, IP: ip, Status: status,
		Tags: "oci;proxmenux", AppWatches: watches,
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"AdGuard Home":      "adguard-home",
		"  Jellystat!!  ":   "jellystat",
		"Over seas__Errand": "over-seas-errand",
		"---weird---":       "weird",
		"Ünïcode Thing":     "n-code-thing",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 80) + "-z"
	if got := Slugify(long); len(got) != 63 || strings.HasSuffix(got, "-") {
		t.Errorf("Slugify(long) len=%d suffix=%q; want 63 chars, no trailing dash", len(got), got)
	}
}

// openPort starts a TCP listener on a random local port for probe tests.
func openPort(t *testing.T) (host string, port int, cleanup func()) {
	t.Helper()
	ln, err := listenLocal()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return "127.0.0.1", ln.Addr().(*tcpAddr).Port, func() { ln.Close() }
}

func TestTransformHappyPath(t *testing.T) {
	host, port, stop := openPort(t)
	defer stop()

	cfg := base()
	g := guest(100, "adguard", host, "running", watch("AdGuard Home", "oci_image", port))
	g.IP = host
	res := Transform([]proxmenux.Guest{g}, cfg)

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
	if r.VMID != 100 || r.GuestName != "adguard" {
		t.Errorf("vmid/guest = %d/%s", r.VMID, r.GuestName)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("skipped = %+v", res.Skipped)
	}
}

func TestTransformSkips(t *testing.T) {
	cfg := base()
	guests := []proxmenux.Guest{
		guest(113, "plex", "10.0.0.9", "stopped", watch("Plex", "oci_image", 32400)),                         // not running
		guest(114, "noip", "", "running", watch("NoIP", "oci_image", 80)),                                    // no IP
		guest(115, "nolabel", "10.0.0.10", "running", watch("Sidecar", "user_sidecar", 80)),                  // not oci
		guest(116, "noports", "10.0.0.11", "running", proxmenux.Watch{Name: "X", InstalledVia: "oci_image"}), // no ports
		guest(117, "dead", "127.0.0.1", "running", watch("Dead App", "oci_image", 1)),                        // probe fails
	}
	res := Transform(guests, cfg)
	if len(res.Routes) != 0 {
		t.Errorf("routes = %d, want 0", len(res.Routes))
	}
	if len(res.Skipped) != 5 {
		t.Fatalf("skipped = %d, want 5: %+v", len(res.Skipped), res.Skipped)
	}
	reasons := map[int]string{}
	for _, s := range res.Skipped {
		reasons[s.VMID] = s.Reason
	}
	for vmid, frag := range map[int]string{
		113: "not running", 114: "no IP", 115: "not an OCI-managed",
		116: "no catalog ports", 117: "probe failed",
	} {
		if !strings.Contains(reasons[vmid], frag) {
			t.Errorf("vmid %d reason = %q, want substring %q", vmid, reasons[vmid], frag)
		}
	}
}

func TestTransformCollisionDedupe(t *testing.T) {
	cfg := base()
	cfg.Routing.Probe = false
	g1 := guest(100, "a1", "127.0.0.1", "running", watch("Same App", "oci_image", 80))
	g2 := guest(200, "a2", "127.0.0.1", "running", watch("Same App", "oci_image", 80))
	res := Transform([]proxmenux.Guest{g1, g2}, cfg)
	if len(res.Routes) != 2 {
		t.Fatalf("routes = %d, want 2", len(res.Routes))
	}
	if res.Routes[0].Slug != "same-app" || res.Routes[1].Slug != "same-app-200" {
		t.Errorf("slugs = %q, %q", res.Routes[0].Slug, res.Routes[1].Slug)
	}
}

func TestTransformVMIDSuffix(t *testing.T) {
	cfg := base()
	cfg.Routing.Probe = false
	cfg.Domain.IncludeVMIDSuffix = true
	g := guest(100, "a", "127.0.0.1", "running", watch("App", "oci_image", 80))
	res := Transform([]proxmenux.Guest{g}, cfg)
	if res.Routes[0].Slug != "app-100" {
		t.Errorf("slug = %q, want app-100", res.Routes[0].Slug)
	}
}

func TestTransformAllowDeny(t *testing.T) {
	cfg := base()
	cfg.Routing.Probe = false
	cfg.Expose.Deny = []string{"secret-app"}
	g := guest(100, "a", "127.0.0.1", "running",
		watch("Secret App", "oci_image", 80),
		watch("Public App", "oci_image", 80))
	res := Transform([]proxmenux.Guest{g}, cfg)
	if len(res.Routes) != 1 || res.Routes[0].Slug != "public-app" {
		t.Fatalf("routes = %+v", res.Routes)
	}

	cfg2 := base()
	cfg2.Routing.Probe = false
	cfg2.Expose.Allow = []string{"public-app"}
	res2 := Transform([]proxmenux.Guest{g}, cfg2)
	if len(res2.Routes) != 1 || res2.Routes[0].Slug != "public-app" {
		t.Fatalf("allow-list routes = %+v", res2.Routes)
	}
}

func TestTransformReservedHostname(t *testing.T) {
	cfg := base()
	cfg.Dashboard.ViaTraefik = true
	cfg.Dashboard.Hostname = "provider"
	g := guest(100, "a", "127.0.0.1", "running", watch("Provider", "oci_image", 8081))
	res := Transform([]proxmenux.Guest{g}, cfg)
	if len(res.Routes) != 0 {
		t.Fatalf("reserved hostname published: %+v", res.Routes)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Reason, "reserved") {
		t.Fatalf("skip reason = %+v", res.Skipped)
	}
}

func TestTransformSlugOverride(t *testing.T) {
	cfg := base()
	cfg.Routing.Probe = false
	cfg.Domain.SlugOverrides = map[string]string{"AdGuard Home": "dns"}
	g := guest(100, "adguard", "127.0.0.1", "running", watch("AdGuard Home", "oci_image", 80))
	res := Transform([]proxmenux.Guest{g}, cfg)
	if res.Routes[0].Slug != "dns" {
		t.Errorf("slug = %q, want dns", res.Routes[0].Slug)
	}
}

func TestTransformHostnameSource(t *testing.T) {
	cfg := base()
	cfg.Routing.Probe = false
	cfg.Domain.SlugSource = "hostname"
	g := guest(100, "adguard", "127.0.0.1", "running", watch("AdGuard Home", "oci_image", 80))
	res := Transform([]proxmenux.Guest{g}, cfg)
	if res.Routes[0].Slug != "adguard" {
		t.Errorf("slug = %q, want adguard (hostname source)", res.Routes[0].Slug)
	}
}

func TestTransformProbeDisabled(t *testing.T) {
	cfg := base()
	cfg.Routing.Probe = false
	cfg.Routing.ProbeTimeout = config.Duration(100 * time.Millisecond)
	g := guest(117, "dead", "127.0.0.1", "running", watch("Dead App", "oci_image", 1))
	res := Transform([]proxmenux.Guest{g}, cfg)
	if len(res.Routes) != 1 {
		t.Fatalf("probe disabled should publish: %+v", res.Skipped)
	}
}
