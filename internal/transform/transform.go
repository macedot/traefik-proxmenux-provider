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
	Slug       string // "adguard-home" (unique after dedupe)
	Hostname   string // "adguard-home.home.example.com"
	AppName    string // "AdGuard Home" (display)
	VMID       int
	GuestName  string // LXC/VM hostname
	IP         string
	Target     string // "http://192.0.2.2:3000"
	Scheme     string // backend scheme
	Port       int
	WebPath    string
	HealthPath string // "" = none
}

// Skipped records why an app watch was not published (shown on the dashboard).
type Skipped struct {
	VMID      int
	GuestName string
	AppName   string
	Slug      string
	Reason    string
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
		if strings.TrimSpace(g.IP) == "" {
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
			candidates = append(candidates, candidate{route: Route{
				Slug:       slug,
				Hostname:   slug + "." + cfg.Domain.Base,
				AppName:    w.Name,
				VMID:       g.VMID,
				GuestName:  g.Name,
				IP:         strings.TrimSpace(g.IP),
				Scheme:     portScheme(p),
				Port:       p.Port,
				WebPath:    p.WebPath,
				HealthPath: healthPath(w),
				Target:     fmt.Sprintf("%s://%s:%d", portScheme(p), strings.TrimSpace(g.IP), p.Port),
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
