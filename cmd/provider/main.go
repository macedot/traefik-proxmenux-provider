// traefik-proxmenux-provider: one HTTPS route per ProxMenux app.
//
// Polls the ProxMenux monitor API, generates Traefik file-provider config,
// writes it atomically to conf.d, and serves a read-only dashboard.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/macedot/traefik-proxmenux-provider/internal/config"
	"github.com/macedot/traefik-proxmenux-provider/internal/dashboard"
	"github.com/macedot/traefik-proxmenux-provider/internal/discover"
	"github.com/macedot/traefik-proxmenux-provider/internal/generate"
	"github.com/macedot/traefik-proxmenux-provider/internal/overrides"
	"github.com/macedot/traefik-proxmenux-provider/internal/proxmenux"
	"github.com/macedot/traefik-proxmenux-provider/internal/state"
	"github.com/macedot/traefik-proxmenux-provider/internal/transform"
)

var version = "dev"

func main() {
	cfgPath := flag.String("config", "/etc/traefik-proxmenux/config.yaml", "path to config.yaml")
	once := flag.Bool("once", false, "run one sync and exit")
	dryRun := flag.Bool("dry-run", false, "print generated YAML instead of writing it")
	check := flag.Bool("check", false, "validate config and exit")
	discoverAPI := flag.Bool("discover-api", false, "probe the local subnet for the ProxMenux monitor API and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *discoverAPI {
		runDiscovery()
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal(err)
	}
	if *check {
		fmt.Println("config OK:", *cfgPath)
		return
	}

	logger := newLogger(cfg.Provider.LogLevel)
	holder := state.New(version)
	client := proxmenux.New(cfg.ProxMenux, logger)
	store := overrides.Open(cfg.ProxMenux.OverridesPath)
	if err := resolveLAN(cfg); err != nil {
		logger.Warn("could not auto-detect LAN subnet; add proxmenux.lan_cidr to config", "err", err)
	}
	// Pristine config slug overrides — the merge target must never be the
	// map the previous sync mutated, or cleared overrides would stick.
	baseSlugOverrides := make(map[string]string, len(cfg.Domain.SlugOverrides))
	for k, v := range cfg.Domain.SlugOverrides {
		baseSlugOverrides[k] = v
	}
	resyncC := make(chan struct{}, 1)

	// Auth contract: detect the monitor's posture once, re-detect on auth
	// changes. Credentials are sent only in AuthToken mode; if the monitor
	// requires auth and we can't provide them, routes are NOT updated.
	authMode := proxmenux.AuthUnknown

	sync := func(ctx context.Context) error {
		snap := holder.Bump()
		snap.AuthMode = string(authMode)

		if authMode == proxmenux.AuthUnknown {
			m, derr := client.Detect(ctx)
			if derr != nil {
				snap.LastError = "monitor auth check failed: " + derr.Error()
				snap.NextSyncAt = time.Now().Add(cfg.Provider.PollInterval.Std())
				holder.Update(snap)
				return derr
			}
			authMode = m
			snap.AuthMode = string(m)
			if m == proxmenux.AuthToken {
				client.SendToken()
				logger.Info("monitor auth mode", "mode", "token (Bearer enabled)")
			} else {
				if cfg.ProxMenux.TokenFile != "" {
					logger.Warn("monitor API is auth-free; configured token will NOT be sent")
				}
				logger.Info("monitor auth mode", "mode", "open (no authentication)")
			}
		}

		guests, err := client.ListGuests(ctx)
		if err != nil {
			if errors.Is(err, proxmenux.ErrAuthRequired) {
				authMode = proxmenux.AuthUnknown // re-detect next cycle (token may appear)
				snap.LastError = "monitor requires authentication — configure token_file; routes NOT updated"
			} else {
				snap.LastError = err.Error()
			}
			snap.NextSyncAt = time.Now().Add(cfg.Provider.PollInterval.Std())
			holder.Update(snap)
			return err
		}

		// ProxDnx parity: docker/native discovery ports per guest.
		extras := map[int][]transform.ServicePort{}
		var dmu sync.Mutex
		var dwg sync.WaitGroup
		dsem := make(chan struct{}, 8)
		for i := range guests {
			g := guests[i]
			if g.Status != "running" || (g.Type != "" && g.Type != "lxc") {
				continue
			}
			dwg.Add(1)
			go func(g proxmenux.Guest) {
				defer dwg.Done()
				dsem <- struct{}{}
				defer func() { <-dsem }()
				ports := client.DiscoveryPorts(ctx, g.VMID)
				if len(ports) == 0 {
					return
				}
				sps := make([]transform.ServicePort, 0, len(ports))
				for _, d := range ports {
					sps = append(sps, transform.ServicePort{Name: d.Name, Port: d.Port, Scheme: "http", WebPath: "/", Source: d.Source})
				}
				dmu.Lock()
				extras[g.VMID] = sps
				dmu.Unlock()
			}(g)
		}
		dwg.Wait()

		// Dashboard edits (overrides file) win over config slug_overrides.
		disabled := map[string]bool{}
		routeDomains := map[string]string{}
		domainOff := map[string]bool{}
		var infraOff bool
		if ov, oerr := store.Load(); oerr != nil {
			logger.Warn("overrides load failed", "err", oerr)
		} else {
			merged := make(map[string]string, len(baseSlugOverrides)+len(ov.SlugOverrides))
			for k, v := range baseSlugOverrides {
				merged[k] = v
			}
			for k, v := range ov.SlugOverrides {
				merged[k] = v
			}
			cfg.Domain.SlugOverrides = merged
			for k, v := range ov.Domains {
				if v != "" {
					routeDomains[k] = v
				}
			}
			for k, v := range ov.Disabled {
				disabled[k] = v // explicit state (true=off, false=on); absent = config default
			}
			for k, v := range ov.DomainsOff {
				if v {
					domainOff[k] = true
				}
			}
			infraOff = ov.InfraOff
		}
		result := transform.Transform(guests, extras, disabled, routeDomains, domainOff, cfg)

		// Static infra entries (ProxDnx parity): <host> (PVE UI) and the
		// ProxMenux UI names, derived from the monitor hostname + api host.
		if cfg.Infra.Enabled {
			if host, herr := client.SystemHostname(ctx); herr == nil {
				if u, perr := url.Parse(cfg.ProxMenux.APIURL); perr == nil {
					infra := transform.BuildInfraRoutes(cfg, host, u.Hostname())
					if cfg.Routing.Probe {
						for i := range infra {
							transform.ProbeRoute(&infra[i], cfg.Routing.ProbeTimeout.Std())
						}
					}
					// Edits (rename/domain) then toggle, both keyed by the
					// stable infra:<default-slug> identity.
					transform.ApplyInfraEdits(infra, cfg.Domain.SlugOverrides, routeDomains)
					transform.ApplyToggle(infra, disabled, domainOff, infraOff, cfg)
					result.Routes = append(infra, result.Routes...)
				}
			} else {
				logger.Warn("infra entries skipped", "err", herr)
			}
		}

		doc, err := generate.Render(result.Routes, cfg)
		if err != nil {
			snap.LastError = err.Error()
			holder.Update(snap)
			return err
		}

		if *dryRun {
			fmt.Println(string(doc))
		} else {
			mode, mErr := cfg.Output.OutputMode()
			if mErr != nil {
				return mErr
			}
			changed, wErr := generate.WriteAtomic(cfg.Output.File, mode, cfg.Output.Backup, doc)
			if wErr != nil {
				snap.LastError = wErr.Error()
				holder.Update(snap)
				return wErr
			}
			logger.Info("sync complete",
				"routes", len(result.Routes),
				"skipped", len(result.Skipped),
				"output", cfg.Output.File,
				"changed", changed,
			)
		}

		snap.Domain = cfg.Domain.Base
		snap.TLSMode = cfg.TLS.Mode
		snap.Entrypoint = cfg.Routing.Entrypoint
		snap.AuthMode = string(authMode)
		snap.EditEnabled = cfg.Dashboard.Edit
		snap.Domains = cfg.Domain.All()
		snap.DomainsOff = domainOffKeys(domainOff)
		snap.InfraOff = infraOff
		snap.GeneratedAt = time.Now()
		snap.NextSyncAt = time.Now().Add(cfg.Provider.PollInterval.Std())
		snap.Routes = result.Routes
		snap.Skipped = result.Skipped
		snap.LastError = ""
		holder.Update(snap)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cfg.Dashboard.Enabled && !*dryRun {
		go func() {
			opts := dashboard.Options{Edit: cfg.Dashboard.Edit, Store: store, Resync: resyncC, Domains: cfg.Domain.All()}
			if err := dashboard.Serve(ctx, cfg.Dashboard.Listen, holder, opts, logger); err != nil {
				logger.Error("dashboard stopped", "err", err)
			}
		}()
	}

	if err := sync(ctx); err != nil {
		logger.Error("initial sync failed", "err", err)
		if *once {
			os.Exit(1)
		}
	}
	if *once || *dryRun {
		return
	}

	ticker := time.NewTicker(cfg.Provider.PollInterval.Std())
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			logger.Info("shutting down")
			return
		case <-resyncC:
			if err := sync(ctx); err != nil {
				logger.Error("resync failed", "err", err)
			}
		case <-ticker.C:
			if err := sync(ctx); err != nil {
				logger.Error("sync failed", "err", err)
			}
		}
	}
}

// resolveLAN fills ProxMenux.LANResolved: the explicit lan_cidr when set,
// otherwise the provider's own /24 (guests usually report their VPN IP
// first — target IPs must be picked from the reachable subnet).
// NOTE: interface enumeration needs AF_NETLINK — keep it in the unit's
// RestrictAddressFamilies or this silently degrades.
func resolveLAN(cfg *config.Config) error {
	if v := strings.TrimSpace(cfg.ProxMenux.LANCIDR); v != "" {
		cfg.ProxMenux.LANResolved = v
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			cfg.ProxMenux.LANResolved = ip4.Mask(net.CIDRMask(24, 32)).String() + "/24"
			return nil
		}
	}
	return fmt.Errorf("no non-loopback IPv4 interface found")
}

func runDiscovery() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	found, err := discover.Candidates(ctx, 8008, "/api/health/status", 2*time.Second)
	if err != nil {
		fatal(err)
	}
	if len(found) == 0 {
		fmt.Println("no ProxMenux monitor API found on the local subnet(s)")
		os.Exit(1)
	}
	for _, f := range found {
		fmt.Println(f)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// domainOffKeys returns the sorted list of switched-off domains (stable for the UI).
func domainOffKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
