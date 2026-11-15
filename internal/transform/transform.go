// Package transform turns monitor-API guests into provider routes,
// replicating ProxDnx's reporting semantics exactly:
//   - running LXC guests only
//   - ports = app_watches + docker/native discovery candidates
//   - IP picked subnet-first, then first non-excluded IPv4
//   - probe TAGS staleness (stale routes stay visible, just not served)
package transform

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/macedot/traefik-proxmenux-provider/internal/config"
	"github.com/macedot/traefik-proxmenux-provider/internal/proxmenux"
)

// Route is one app/infra route.
type Route struct {
	Key        string `json:"key,omitempty"` // override key; infra uses "infra:<slug>"
	Slug       string `json:"slug"`
	Hostname   string `json:"hostname"`
	Domain     string `json:"domain"` // the domain part of the URL (per-app pick)
	AppName    string `json:"app_name"`
	Kind       string `json:"kind"` // app | infra
	VMID       int    `json:"vmid"`
	GuestName  string `json:"guest_name"`
	IP         string `json:"ip"`
	Target     string `json:"target"`
	Scheme     string `json:"scheme"`
	Port       int    `json:"port"`
	WebPath    string `json:"web_path"`
	HealthPath string `json:"health_path"`
	Stale      bool   `json:"stale"`    // TCP probe failed: shown, not served
	Disabled   bool   `json:"disabled"` // toggled off on the dashboard: shown, not served
	Insecure   bool   `json:"insecure"` // https backend with self-signed cert (PVE UI)
	Served     bool   `json:"served"`   // published to the generated Traefik config
}

// Skipped records why a port/watch was not turned into a route.
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

// ServicePort is one discoverable service port of a guest (watch port or
// discovery candidate).
type ServicePort struct {
	Name       string
	Port       int
	Scheme     string
	WebPath    string
	HealthPath string
	Source     string // app_watch | docker | native (dedupe/debug)
}

var slugNoise = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify converts a service name to a DNS-safe label.
func Slugify(name string) string {
	s := slugNoise.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	return s
}

// ReservedHostnames returns hostnames (lowercase, full label) that app
// routes must not claim: dashboards and the static infra names.
func ReservedHostnames(cfg *config.Config) map[string]bool {
	res := map[string]bool{}
	if cfg.Dashboard.Enabled && cfg.Dashboard.ViaTraefik {
		res[strings.ToLower(cfg.Dashboard.Hostname)] = true
	}
	if cfg.Expose.TraefikDashboard.Enabled {
		res[strings.ToLower(cfg.Expose.TraefikDashboard.Hostname)] = true
	}
	if cfg.Infra.Enabled {
		res["menux"] = true
		res["proxmenux"] = true
	}
	return res
}

// Transform filters guests and service ports into routes (ProxDnx
// semantics). Stale and disabled routes are flagged (visible on the
// dashboard), never silently dropped. disabled is keyed by vmid string;
// routeDomains maps vmid → the domain the app URL uses ("" = default).
func Transform(guests []proxmenux.Guest, extras map[int][]ServicePort, disabled map[string]bool, routeDomains map[string]string, cfg *config.Config) *Result {
	res := &Result{}
	reserved := ReservedHostnames(cfg)

	allow := toSet(cfg.Expose.Allow)
	deny := toSet(cfg.Expose.Deny)

	sorted := append([]proxmenux.Guest(nil), guests...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].VMID < sorted[j].VMID })

	type candidate struct {
		route Route
	}
	var candidates []candidate
	seenHost := map[string]bool{}
	seenIPPort := map[string]bool{}

	for _, g := range sorted {
		if g.Status != "running" {
			for _, w := range g.AppWatches {
				res.Skipped = append(res.Skipped, Skipped{VMID: g.VMID, GuestName: g.Name, AppName: w.Name, Reason: "guest not running"})
			}
			continue
		}
		// ProxDnx parity: LXC guests only (VMs are not reported).
		if g.Type != "" && g.Type != "lxc" {
			continue
		}
		gip := pickIP(g, cfg)
		if gip == "" {
			continue
		}

		ports := mergePorts(g, extras[g.VMID])
		if len(ports) == 0 {
			for _, w := range g.AppWatches {
				res.Skipped = append(res.Skipped, Skipped{VMID: g.VMID, GuestName: g.Name, AppName: w.Name, Reason: "no discoverable ports"})
			}
			continue
		}

		for _, sp := range ports {
			skip := func(reason string) {
				res.Skipped = append(res.Skipped, Skipped{VMID: g.VMID, GuestName: g.Name, AppName: sp.Name, Reason: reason})
			}

			// ProxDnx parity: one record per unique ip:port.
			if seenIPPort[gip+":"+strconv.Itoa(sp.Port)] {
				continue
			}

			slug := resolveSlug(sp.Name, g, cfg)
			if slug == "" {
				continue
			}
			// Hostname is slug + the app's domain (per-vmid override or default).
			appDomain := routeDomains[strconv.Itoa(g.VMID)]
			if appDomain == "" {
				appDomain = cfg.Domain.Base
			}
			hostname := slug
			full := hostname + "." + appDomain
			if seenHost[full] {
				hostname = fmt.Sprintf("%s-%d", slug, g.VMID)
				full = hostname + "." + appDomain
			}
			if seenHost[full] {
				skip("slug collision")
				continue
			}
			seenHost[full] = true

			// Reserved labels apply on the primary domain, where the infra
			// entries and dashboards live.
			if reserved[strings.ToLower(slug)] && appDomain == cfg.Domain.Base {
				skip("hostname reserved")
				continue
			}

			if deny[strings.ToLower(slug)] {
				skip("denied by expose.deny")
				continue
			}
			if len(allow) > 0 && !allow[strings.ToLower(slug)] {
				skip("not in expose.allow")
				continue
			}

			route := Route{
				Slug:      hostname,
				Hostname:  full,
				Domain:    appDomain,
				AppName:   sp.Name,
				Kind:      "app",
				VMID:      g.VMID,
				GuestName: g.Name,
				IP:        gip,
				Scheme:    sp.Scheme,
				Port:      sp.Port,
				WebPath:   sp.WebPath,
				Target:    fmt.Sprintf("%s://%s:%d", sp.Scheme, gip, sp.Port),
			}
			seenIPPort[gip+":"+strconv.Itoa(sp.Port)] = true

			// Per-app on/off toggle: card stays visible, route not served.
			// Explicit per-app state wins; absent key = config default
			// (routing.default_enabled). Map values: true = off, false = on.
			var isDisabled bool
			if v, explicit := disabled[strconv.Itoa(g.VMID)]; explicit {
				isDisabled = v
			} else {
				isDisabled = !cfg.Routing.DefaultEnabled
			}
			if isDisabled {
				route.Disabled = true
				route.Served = false
				res.Routes = append(res.Routes, route)
				continue
			}

			candidates = append(candidates, candidate{route: route})
		}
	}

	// Probe tags staleness (parallel; never drops).
	var routes []*Route
	for i := range candidates {
		routes = append(routes, &candidates[i].route)
	}
	probeCandidates(routes, cfg)

	for _, r := range routes {
		r.Served = !r.Stale
		res.Routes = append(res.Routes, *r)
	}
	sort.Slice(res.Routes, func(i, j int) bool { return res.Routes[i].Hostname < res.Routes[j].Hostname })
	return res
}

// mergePorts = app_watches ports + per-guest discovery candidates,
// deduped by name|port (ProxDnx parity).
func mergePorts(g proxmenux.Guest, extras []ServicePort) []ServicePort {
	seen := map[string]bool{}
	var out []ServicePort
	add := func(sp ServicePort) {
		if sp.Port == 0 {
			return
		}
		k := sp.Name + "|" + strconv.Itoa(sp.Port)
		if seen[k] {
			return
		}
		seen[k] = true
		// Scheme allowlist: only http/https may reach the generated config
		// or the dashboard (a crafted scheme could become an executable
		// URL in the UI). Empty defaults to http; anything else is dropped.
		switch sp.Scheme {
		case "":
			sp.Scheme = "http"
		case "http", "https":
		default:
			return
		}
		// Path hygiene: must be a sane URL path or empty.
		if sp.WebPath != "" && !sanePath(sp.WebPath) {
			sp.WebPath = ""
		}
		if sp.HealthPath != "" && !sanePath(sp.HealthPath) {
			sp.HealthPath = ""
		}
		out = append(out, sp)
	}
	for _, w := range g.AppWatches {
		name := w.Name
		if name == "" {
			name = w.HelperSlug
		}
		hp := ""
		if w.HealthPath != nil {
			hp = *w.HealthPath
		}
		for _, p := range w.Ports {
			add(ServicePort{Name: name, Port: p.Port, Scheme: portScheme(p.Scheme), WebPath: p.WebPath, HealthPath: hp, Source: "app_watch"})
		}
	}
	for _, sp := range extras {
		add(sp)
	}
	return out
}

// BuildInfraRoutes builds the static infrastructure routes on the provider
// domain, mirroring ProxDnx's static entries: <host> (PVE UI :8006) and the
// ProxMenux UI on <host>.menux / menux / proxmenux (:8008).
func BuildInfraRoutes(cfg *config.Config, host, apiIP string) []Route {
	if !cfg.Infra.Enabled || host == "" || apiIP == "" {
		return nil
	}
	port := cfg.ProxMenux.MonitorPort()
	base := "http://" + apiIP + ":" + strconv.Itoa(port)
	menux := []Route{
		{Slug: host + "-menux", Hostname: host + ".menux." + cfg.Domain.Base, AppName: "ProxMenux", Kind: "infra", GuestName: host, IP: apiIP, Scheme: "http", Port: port, Target: base},
		{Slug: "menux", Hostname: "menux." + cfg.Domain.Base, AppName: "ProxMenux", Kind: "infra", GuestName: host, IP: apiIP, Scheme: "http", Port: port, Target: base},
		{Slug: "proxmenux", Hostname: "proxmenux." + cfg.Domain.Base, AppName: "ProxMenux", Kind: "infra", GuestName: host, IP: apiIP, Scheme: "http", Port: port, Target: base},
	}
	pve := Route{Slug: host, Hostname: host + "." + cfg.Domain.Base, AppName: "Proxmox VE", Kind: "infra", GuestName: host, IP: apiIP, Scheme: "https", Port: 8006, Insecure: true,
		Target: "https://" + apiIP + ":8006"}
	out := append([]Route{pve}, menux...)
	for i := range out {
		out[i].Served = true
		out[i].Key = "infra:" + out[i].Slug
	}
	return out
}

// ApplyToggle applies the per-route on/off state to infrastructure
// routes (apps are handled inside Transform). Explicit state wins;
// absent keys follow cfg.Routing.DefaultEnabled.
func ApplyToggle(routes []Route, disabled map[string]bool, cfg *config.Config) {
	for i := range routes {
		r := &routes[i]
		if r.Kind != "infra" || r.Key == "" {
			continue
		}
		var off bool
		if v, ok := disabled[r.Key]; ok {
			off = v
		} else {
			off = !cfg.Routing.DefaultEnabled
		}
		r.Disabled = off
		r.Served = !off && !r.Stale
	}
}

// ProbeRoute probes a route target. http targets get a TCP dial; https
// targets get a real TLS handshake with certificate verification. An
// https target whose certificate cannot be verified for the dial address
// (self-signed appliance certs addressed by IP) is flagged Insecure and
// still served — generate attaches the shared pmp-insecure transport. A
// target that does not answer at all is stale and not served.
func ProbeRoute(r *Route, timeout time.Duration) {
	addr := net.JoinHostPort(r.IP, strconv.Itoa(r.Port))
	dialer := &net.Dialer{Timeout: timeout}
	if r.Scheme != "https" {
		conn, err := dialer.Dial("tcp", addr)
		if err != nil {
			r.Stale = true
			r.Served = false
			return
		}
		_ = conn.Close()
		r.Stale = false
		r.Insecure = false
		r.Served = true
		return
	}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		MinVersion: tls.VersionTLS12, // verification left enabled on purpose
	})
	if err != nil {
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			// answers TLS but the certificate cannot be validated for this
			// address (self-signed, unknown CA, no IP SAN)
			r.Insecure = true
			r.Stale = false
			r.Served = true
			return
		}
		r.Stale = true
		r.Served = false
		return
	}
	_ = conn.Close()
	r.Stale = false
	r.Insecure = false
	r.Served = true
}

// probeCandidates marks stale routes in parallel (ProxDnx-sized:
// 1200ms default, 16 workers).
func probeCandidates(routes []*Route, cfg *config.Config) {
	if !cfg.Routing.Probe {
		return
	}
	timeout := cfg.Routing.ProbeTimeout.Std()
	sem := make(chan struct{}, 16)
	var wg sync.WaitGroup
	for _, r := range routes {
		wg.Add(1)
		go func(r *Route) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			ProbeRoute(r, timeout)
		}(r)
	}
	wg.Wait()
}

// sanePath: URL path must start with "/" and contain no whitespace or
// control characters (defense against crafted monitor data).
func sanePath(p string) bool {
	if !strings.HasPrefix(p, "/") {
		return false
	}
	for _, r := range p {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// pickIP mirrors ProxDnx: IPv4 from ips[] (plus ip), subnet-preferred, then
// the first IPv4 that is not loopback / CGNAT / docker / link-local.
func pickIP(g proxmenux.Guest, cfg *config.Config) string {
	seen := map[string]bool{}
	var v4 []string
	for _, c := range append([]string{g.IP}, g.IPs...) {
		c = strings.TrimSpace(c)
		if c == "" || strings.Contains(c, ":") || seen[c] {
			continue
		}
		seen[c] = true
		v4 = append(v4, c)
	}
	if len(v4) == 0 {
		return ""
	}
	if cidr := strings.TrimSpace(cfg.ProxMenux.LANResolved); cidr != "" {
		if _, ipnet, err := net.ParseCIDR(cidr); err == nil {
			for _, c := range v4 {
				if ip := net.ParseIP(c); ip != nil && ipnet.Contains(ip) {
					return c
				}
			}
		}
	}
	for _, c := range v4 {
		if !excludedIP(c) {
			return c
		}
	}
	return ""
}

var fallbackExclude = []*net.IPNet{
	mustCIDR("100.64.0.0/10"),  // CGNAT / tailscale
	mustCIDR("172.16.0.0/12"),  // docker bridges
	mustCIDR("169.254.0.0/16"), // link-local
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic("bad builtin CIDR " + s)
	}
	return n
}

func excludedIP(ip string) bool {
	p := net.ParseIP(ip)
	if p == nil || p.IsLoopback() || !p.IsGlobalUnicast() {
		return true
	}
	for _, n := range fallbackExclude {
		if n.Contains(p) {
			return true
		}
	}
	return false
}

// resolveSlug: explicit override (config + dashboard) wins, then the
// service name — ProxDnx slugs from the service name, not the hostname.
func resolveSlug(serviceName string, g proxmenux.Guest, cfg *config.Config) string {
	if s, ok := cfg.Domain.SlugOverrides[strconv.Itoa(g.VMID)]; ok {
		return Slugify(s)
	}
	if s, ok := cfg.Domain.SlugOverrides[serviceName]; ok {
		return Slugify(s)
	}
	if s, ok := cfg.Domain.SlugOverrides[g.Name]; ok {
		return Slugify(s)
	}
	if cfg.Domain.SlugSource == "hostname" {
		return Slugify(g.Name)
	}
	return Slugify(serviceName)
}

func portScheme(scheme string) string {
	if strings.EqualFold(scheme, "https") {
		return "https"
	}
	return "http"
}

func toSet(list []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range list {
		m[strings.ToLower(strings.TrimSpace(s))] = true
	}
	return m
}
