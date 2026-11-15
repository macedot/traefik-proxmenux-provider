// Package transform turns monitor-API guests into provider routes:
// slug rules, collision dedupe, allow/deny, reserved hostnames, and the
// TCP reachability probe that keeps dead or drifted watches unpublished.
package transform

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/macedot/traefik-proxmenux-provider/internal/config"
	"github.com/macedot/traefik-proxmenux-provider/internal/proxmenux"
)

// Route is one publishable app route.
type Route struct {
	Slug       string `json:"slug"`
	Hostname   string `json:"hostname"` // "adguard-home.home.example.com"
	AppName    string `json:"app_name"` // "AdGuard Home" (display)
	VMID       int    `json:"vmid"`
	GuestName  string `json:"guest_name"`
	IP         string `json:"ip"`
	Target     string `json:"target"` // "http://192.0.2.2:3000"
	Scheme     string `json:"scheme"`
	Port       int    `json:"port"`
	WebPath    string `json:"web_path"`
	HealthPath string `json:"health_path"` // "" = none
}

// Skipped records why an app watch was not published (shown on the dashboard).
type Skipped struct {
	VMID      int    `json:"vmid"`
	GuestName string `json:"guest_name"`
	AppName   string `json:"app_name"`
	Slug      string `json:"slug"`
	Reason    string `json:"reason"`
}

// Result is the outcome of one transform pass.
type Result struct {
	Routes  []Route
	Skipped []Skipped
}

var slugNoise = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify converts an app name to a DNS-safe label.
func Slugify(name string) string {
	s := slugNoise.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	return s
}

// ReservedHostnames returns hostnames (lowercase, full label) that app
// routes must not claim because they belong to dashboards.
func ReservedHostnames(cfg *config.Config) map[string]bool {
	res := map[string]bool{}
	if cfg.Dashboard.Enabled && cfg.Dashboard.ViaTraefik {
		res[strings.ToLower(cfg.Dashboard.Hostname)] = true
	}
	if cfg.Expose.TraefikDashboard.Enabled {
		res[strings.ToLower(cfg.Expose.TraefikDashboard.Hostname)] = true
	}
	return res
}

// Transform filters guests and app watches into publishable routes.
// It is pure except for the optional TCP probe.
func Transform(guests []proxmenux.Guest, cfg *config.Config) *Result {
	res := &Result{}
	reserved := ReservedHostnames(cfg)

	allow := toSet(cfg.Expose.Allow)
	deny := toSet(cfg.Expose.Deny)
	source := cfg.ProxMenux.Source

	sorted := append([]proxmenux.Guest(nil), guests...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].VMID < sorted[j].VMID })

	type candidate struct {
		route Route
	}
	var candidates []candidate
	seenSlug := map[string]bool{}

	for _, g := range sorted {
		if g.Status != "running" {
			for _, w := range g.AppWatches {
				res.Skipped = append(res.Skipped, Skipped{VMID: g.VMID, GuestName: g.Name, AppName: w.Name, Reason: "guest not running"})
			}
			continue
		}
		if strings.TrimSpace(g.IP) == "" && len(g.IPs) == 0 {
			for _, w := range g.AppWatches {
				res.Skipped = append(res.Skipped, Skipped{VMID: g.VMID, GuestName: g.Name, AppName: w.Name, Reason: "no IP reported by monitor"})
			}
			continue
		}
		for _, w := range g.AppWatches {
			skip := func(reason string) {
				res.Skipped = append(res.Skipped, Skipped{VMID: g.VMID, GuestName: g.Name, AppName: w.Name, Reason: reason})
			}

			if source == "oci" && w.InstalledVia != "oci_image" {
				skip("not an OCI-managed app (source=oci)")
				continue
			}
			if len(w.Ports) == 0 {
				skip("no catalog ports")
				continue
			}

			slug := resolveSlug(w, g, cfg)
			if slug == "" {
				slug = fmt.Sprintf("app-%d", g.VMID)
			}

			// Dedupe: on collision append the vmid (unique per guest).
			if seenSlug[slug] {
				slug = fmt.Sprintf("%s-%d", slug, g.VMID)
			}
			if seenSlug[slug] {
				skip("slug collision")
				continue
			}
			if cfg.Domain.IncludeVMIDSuffix {
				slug = fmt.Sprintf("%s-%d", slug, g.VMID)
			}
			seenSlug[slug] = true

			// Exposure control by slug (deny wins over allow).
			if deny[strings.ToLower(slug)] {
				skip("denied by expose.deny")
				continue
			}
			if len(allow) > 0 && !allow[strings.ToLower(slug)] {
				skip("not in expose.allow")
				continue
			}

			if reserved[strings.ToLower(slug)] {
				skip("hostname reserved for a dashboard")
				continue
			}

			p := w.Ports[0]
			gip := pickIP(g, cfg)
			candidates = append(candidates, candidate{route: Route{
				Slug:       slug,
				Hostname:   slug + "." + cfg.Domain.Base,
				AppName:    w.Name,
				VMID:       g.VMID,
				GuestName:  g.Name,
				IP:         gip,
				Scheme:     portScheme(p),
				Port:       p.Port,
				WebPath:    p.WebPath,
				HealthPath: healthPath(w),
				Target:     fmt.Sprintf("%s://%s:%d", portScheme(p), gip, p.Port),
			}})
		}
	}

	// Probe before publish.
	for _, c := range candidates {
		r := c.route
		if cfg.Routing.Probe {
			if err := probe(r.IP, r.Port, cfg.Routing.ProbeTimeout.Std()); err != nil {
				res.Skipped = append(res.Skipped, Skipped{
					VMID: r.VMID, GuestName: r.GuestName, AppName: r.AppName, Slug: r.Slug,
					Reason: "probe failed (" + err.Error() + ")",
				})
				continue
			}
		}
		res.Routes = append(res.Routes, r)
	}
	return res
}

// pickIP chooses the best target IP for a guest: prefer an address inside
// the LAN subnet (guests behind gluetun/VPN report the VPN IP first, which
// is unreachable from the Traefik CT). Falls back to the first reported IP.
func pickIP(g proxmenux.Guest, cfg *config.Config) string {
	seen := map[string]bool{}
	var cands []string
	for _, c := range append([]string{g.IP}, g.IPs...) {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return ""
	}
	if cidr := strings.TrimSpace(cfg.ProxMenux.LANResolved); cidr != "" {
		if _, ipnet, err := net.ParseCIDR(cidr); err == nil {
			for _, c := range cands {
				if ip := net.ParseIP(c); ip != nil && ipnet.Contains(ip) {
					return c
				}
			}
		}
	}
	return cands[0]
}

func resolveSlug(w proxmenux.Watch, g proxmenux.Guest, cfg *config.Config) string {
	if s, ok := cfg.Domain.SlugOverrides[strconv.Itoa(g.VMID)]; ok {
		return Slugify(s)
	}
	if s, ok := cfg.Domain.SlugOverrides[w.Name]; ok {
		return Slugify(s)
	}
	if s, ok := cfg.Domain.SlugOverrides[g.Name]; ok {
		return Slugify(s)
	}
	if cfg.Domain.SlugSource == "hostname" {
		return Slugify(g.Name)
	}
	return Slugify(w.Name)
}

func portScheme(p proxmenux.Port) string {
	if strings.EqualFold(p.Scheme, "https") {
		return "https"
	}
	return "http"
}

func healthPath(w proxmenux.Watch) string {
	if w.HealthPath != nil && *w.HealthPath != "" {
		return *w.HealthPath
	}
	return ""
}

func probe(host string, port int, timeout time.Duration) error {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

func toSet(list []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range list {
		m[strings.ToLower(strings.TrimSpace(s))] = true
	}
	return m
}
