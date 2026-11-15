// Package config loads and validates the provider configuration.
// Unknown YAML keys are rejected (typo protection); everything except
// proxmenux.api_url and domain.base has a safe default.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration is a time.Duration that (un)marshals from YAML strings like "30s".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return fmt.Errorf("duration must be a quoted string like \"30s\": %w", err)
	}
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

type Provider struct {
	PollInterval Duration `yaml:"poll_interval"`
	LogLevel     string   `yaml:"log_level"`
}

type ProxMenux struct {
	APIURL           string   `yaml:"api_url"`
	TokenFile        string   `yaml:"token_file"`
	Timeout          Duration `yaml:"timeout"`
	MaxResponseBytes int      `yaml:"max_response_bytes"`
	LANCIDR          string   `yaml:"lan_cidr"` // prefer guest IPs in this subnet (default: provider's /24)
	LANResolved      string   `yaml:"-"`        // runtime-resolved CIDR used by transform
	OverridesPath    string   `yaml:"-"`        // runtime: <config dir>/overrides.json
}

// MonitorPort is the monitor API port derived from api_url.
func (p ProxMenux) MonitorPort() int {
	u, err := url.Parse(p.APIURL)
	if err != nil {
		return 8008
	}
	if s := u.Port(); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 65536 {
			return n
		}
	}
	if u.Scheme == "https" {
		return 443
	}
	return 80
}

type Domain struct {
	Base              string            `yaml:"base"`
	Alternates        []string          `yaml:"alternates"` // pickable in the dashboard edit dialog; Base is the default
	SlugSource        string            `yaml:"slug_source"`
	SlugOverrides     map[string]string `yaml:"slug_overrides"`
	IncludeVMIDSuffix bool              `yaml:"include_vmid_suffix"`
}

// All returns every usable domain, Base first (the default).
func (d Domain) All() []string {
	out := []string{d.Base}
	seen := map[string]bool{d.Base: true}
	for _, a := range d.Alternates {
		a = strings.TrimSpace(a)
		if a != "" && !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

type TLS struct {
	Mode               string `yaml:"mode"`
	Email              string `yaml:"email"`
	CertResolver       string `yaml:"cert_resolver"`
	DNSProvider        string `yaml:"dns_provider"`
	DNSCredentialsFile string `yaml:"dns_credentials_file"`
}

type Routing struct {
	Entrypoint   string   `yaml:"entrypoint"`
	Middlewares  []string `yaml:"middlewares"`
	Probe        bool     `yaml:"probe"`
	ProbeTimeout Duration `yaml:"probe_timeout"`
	HealthCheck  bool     `yaml:"health_check"`
}

type TraefikDashboard struct {
	Enabled    bool     `yaml:"enabled"`
	Hostname   string   `yaml:"hostname"`
	AllowCIDRs []string `yaml:"allow_cidrs"`
}

type Expose struct {
	Allow            []string         `yaml:"allow"`
	Deny             []string         `yaml:"deny"`
	TraefikDashboard TraefikDashboard `yaml:"dashboard"`
}

type Dashboard struct {
	Enabled    bool     `yaml:"enabled"`
	Listen     string   `yaml:"listen"`
	ViaTraefik bool     `yaml:"via_traefik"`
	Hostname   string   `yaml:"hostname"`
	AllowCIDRs []string `yaml:"allow_cidrs"`
	Edit       bool     `yaml:"edit"` // double-click slug edits on the dashboard
}

type Output struct {
	File   string `yaml:"file"`
	Mode   string `yaml:"mode"`
	Backup bool   `yaml:"backup"`
}

// Infra controls the static <host>/<menux> entries (ProxDnx parity).
type Infra struct {
	Enabled bool `yaml:"enabled"`
}

type Config struct {
	Provider  Provider  `yaml:"provider"`
	ProxMenux ProxMenux `yaml:"proxmenux"`
	Domain    Domain    `yaml:"domain"`
	TLS       TLS       `yaml:"tls"`
	Routing   Routing   `yaml:"routing"`
	Expose    Expose    `yaml:"expose"`
	Dashboard Dashboard `yaml:"dashboard"`
	Infra     Infra     `yaml:"infra"`
	Output    Output    `yaml:"output"`
}

// Defaults returns the built-in configuration. Dashboard is ON by default.
func Defaults() *Config {
	return &Config{
		Provider:  Provider{PollInterval: Duration(30 * time.Second), LogLevel: "info"},
		ProxMenux: ProxMenux{Timeout: Duration(10 * time.Second), MaxResponseBytes: 5 * 1024 * 1024},
		Domain:    Domain{SlugSource: "watch"},
		TLS:       TLS{Mode: "internal", CertResolver: "letsencrypt"},
		Routing:   Routing{Entrypoint: "websecure", Probe: true, ProbeTimeout: Duration(1200 * time.Millisecond)},
		Infra:     Infra{Enabled: true},
		Dashboard: Dashboard{Enabled: true, Listen: ":8082", Hostname: "provider", Edit: true},
		Output:    Output{File: "/etc/traefik/conf.d/proxmenux-generated.yaml", Mode: "0644", Backup: true},
	}
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- path comes from the admin-controlled config/flag
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg, err := Parse(b)
	if err != nil {
		return nil, err
	}
	cfg.ProxMenux.OverridesPath = filepath.Join(filepath.Dir(path), "overrides.json")
	return cfg, nil
}

func Parse(b []byte) (*Config, error) {
	cfg := Defaults()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// OutputMode parses Output.Mode ("0644") into an os.FileMode.
func (o Output) OutputMode() (os.FileMode, error) {
	m, err := strconvParseOctal(o.Mode)
	if err != nil {
		return 0, fmt.Errorf("output.mode: %w", err)
	}
	return m, nil
}

func (c *Config) Validate() error {
	var errs []string
	add := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	// provider
	switch c.Provider.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		add("provider.log_level must be debug|info|warn|error (got %q)", c.Provider.LogLevel)
	}
	if c.Provider.PollInterval.Std() < 10*time.Second {
		add("provider.poll_interval must be >= 10s (got %s)", c.Provider.PollInterval.Std())
	}

	// proxmenux
	c.ProxMenux.APIURL = strings.TrimRight(strings.TrimSpace(c.ProxMenux.APIURL), "/")
	if !strings.HasPrefix(c.ProxMenux.APIURL, "http://") && !strings.HasPrefix(c.ProxMenux.APIURL, "https://") {
		add("proxmenux.api_url must start with http:// or https:// (got %q)", c.ProxMenux.APIURL)
	}
	if c.ProxMenux.Timeout.Std() <= 0 {
		add("proxmenux.timeout must be > 0")
	}
	if c.ProxMenux.MaxResponseBytes < 4096 {
		add("proxmenux.max_response_bytes must be >= 4096")
	}
	if c.ProxMenux.LANCIDR != "" {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(c.ProxMenux.LANCIDR)); err != nil {
			add("proxmenux.lan_cidr: invalid CIDR %q", c.ProxMenux.LANCIDR)
		}
	}

	// domain
	if err := validateHostname(c.Domain.Base); err != nil {
		add("domain.base: %v", err)
	}
	for _, a := range c.Domain.Alternates {
		if err := validateHostname(strings.TrimSpace(a)); err != nil {
			add("domain.alternates: %v", err)
		}
	}
	switch c.Domain.SlugSource {
	case "watch", "hostname":
	default:
		add("domain.slug_source must be watch|hostname (got %q)", c.Domain.SlugSource)
	}

	// tls
	switch c.TLS.Mode {
	case "internal":
	case "per-app":
		if c.TLS.Email == "" {
			add("tls.email is required when tls.mode=per-app")
		}
	case "wildcard":
		if c.TLS.Email == "" {
			add("tls.email is required when tls.mode=wildcard")
		}
		if c.TLS.DNSProvider == "" {
			add("tls.dns_provider is required when tls.mode=wildcard")
		}
		if c.TLS.DNSCredentialsFile == "" {
			add("tls.dns_credentials_file is required when tls.mode=wildcard")
		}
	default:
		add("tls.mode must be internal|per-app|wildcard (got %q)", c.TLS.Mode)
	}
	if c.TLS.Mode != "internal" && c.TLS.CertResolver == "" {
		add("tls.cert_resolver must be set when tls.mode != internal")
	}

	// routing
	if c.Routing.ProbeTimeout.Std() <= 0 {
		add("routing.probe_timeout must be > 0")
	}

	// expose
	if c.Expose.TraefikDashboard.Enabled {
		if err := validateLabel(c.Expose.TraefikDashboard.Hostname); err != nil {
			add("expose.dashboard.hostname: %v", err)
		}
		if err := validateCIDRs(c.Expose.TraefikDashboard.AllowCIDRs); err != nil {
			add("expose.dashboard.allow_cidrs: %v", err)
		}
	}

	// dashboard
	if c.Dashboard.Enabled {
		if err := validateListen(c.Dashboard.Listen); err != nil {
			add("dashboard.listen: %v", err)
		}
		if c.Dashboard.ViaTraefik {
			if err := validateLabel(c.Dashboard.Hostname); err != nil {
				add("dashboard.hostname: %v", err)
			}
			if err := validateCIDRs(c.Dashboard.AllowCIDRs); err != nil {
				add("dashboard.allow_cidrs: %v", err)
			}
		}
	}

	// output
	if c.Output.File == "" {
		add("output.file must not be empty")
	}
	if _, err := c.Output.OutputMode(); err != nil {
		add("%v", err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid config:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

func validateHostname(h string) error {
	if h == "" {
		return fmt.Errorf("must not be empty")
	}
	labels := strings.Split(strings.TrimSuffix(h, "."), ".")
	if len(labels) < 2 {
		return fmt.Errorf("%q must be a fully-qualified name (at least two labels)", h)
	}
	for _, l := range labels {
		if len(l) == 0 || len(l) > 63 {
			return fmt.Errorf("label %q in %q must be 1-63 characters", l, h)
		}
		for _, r := range l {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return fmt.Errorf("label %q contains invalid character %q", l, string(r))
			}
		}
		if strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
			return fmt.Errorf("label %q must not start or end with '-'", l)
		}
	}
	return nil
}

func validateLabel(l string) error {
	if l == "" {
		return fmt.Errorf("must not be empty")
	}
	if len(l) > 63 {
		return fmt.Errorf("%q must be at most 63 characters", l)
	}
	for _, r := range l {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return fmt.Errorf("%q contains invalid character %q", l, string(r))
		}
	}
	if strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
		return fmt.Errorf("%q must not start or end with '-'", l)
	}
	return nil
}

func validateCIDRs(cidrs []string) error {
	for _, c := range cidrs {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(c)); err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", c, err)
		}
	}
	return nil
}

func validateListen(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("must be host:port or :port (got %q)", addr)
	}
	var p int
	if _, err := fmt.Sscanf(port, "%d", &p); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("invalid port %q", port)
	}
	return nil
}
