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
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/macedot/traefik-proxmenux-provider/internal/config"
	"github.com/macedot/traefik-proxmenux-provider/internal/dashboard"
	"github.com/macedot/traefik-proxmenux-provider/internal/discover"
	"github.com/macedot/traefik-proxmenux-provider/internal/generate"
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
		result := transform.Transform(guests, cfg)
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
		snap.Source = cfg.ProxMenux.Source
		snap.TLSMode = cfg.TLS.Mode
		snap.Entrypoint = cfg.Routing.Entrypoint
		snap.AuthMode = string(authMode)
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
			if err := dashboard.Serve(ctx, cfg.Dashboard.Listen, holder, logger); err != nil {
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
		case <-ticker.C:
			if err := sync(ctx); err != nil {
				logger.Error("sync failed", "err", err)
			}
		}
	}
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
