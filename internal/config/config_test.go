package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	// Defaults apply to optional fields even when only the two required
	// keys are present.
	cfg, err := Parse(baseCfg())
	if err != nil {
		t.Fatalf("Parse(base) returned error: %v", err)
	}
	if cfg.Provider.PollInterval.Std() != 30*time.Second {
		t.Errorf("poll_interval default = %s, want 30s", cfg.Provider.PollInterval.Std())
	}
	if cfg.Dashboard.Enabled != true {
		t.Error("dashboard must default to enabled")
	}
	if cfg.Dashboard.Listen != ":8082" {
		t.Errorf("dashboard.listen default = %q, want :8082", cfg.Dashboard.Listen)
	}
	if cfg.TLS.Mode != "internal" {
		t.Errorf("tls.mode default = %q, want internal", cfg.TLS.Mode)
	}
	if cfg.ProxMenux.Source != "watches" {
		t.Errorf("proxmenux.source default = %q, want watches", cfg.ProxMenux.Source)
	}
	if cfg.Routing.Probe != true {
		t.Error("routing.probe must default to true")
	}
}

func baseCfg() []byte {
	return []byte("proxmenux:\n  api_url: http://192.0.2.99:8008\ndomain:\n  base: home.example.com\n")
}

func TestParseValid(t *testing.T) {
	cfg, err := Parse(baseCfg())
	if err != nil {
		t.Fatalf("Parse(valid) error: %v", err)
	}
	if cfg.ProxMenux.APIURL != "http://192.0.2.99:8008" {
		t.Errorf("api_url = %q", cfg.ProxMenux.APIURL)
	}
}

func TestParseTrailingSlashTrimmed(t *testing.T) {
	b := []byte("proxmenux:\n  api_url: http://host:8008/\ndomain:\n  base: home.example.com\n")
	cfg, err := Parse(b)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if cfg.ProxMenux.APIURL != "http://host:8008" {
		t.Errorf("api_url = %q, want trailing slash trimmed", cfg.ProxMenux.APIURL)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := []struct {
		name    string
		yamlStr string
		wantSub string
	}{
		{"missing domain", "proxmenux:\n  api_url: http://h:8008\n", "domain.base"},
		{"missing api_url", "domain:\n  base: home.example.com\n", "api_url"},
		{"bad scheme", "proxmenux:\n  api_url: ftp://h:8008\ndomain:\n  base: home.example.com\n", "api_url"},
		{"low poll", "provider:\n  poll_interval: \"5s\"\n" + string(baseCfg()), "poll_interval"},
		{"bad level", "provider:\n  log_level: verbose\n" + string(baseCfg()), "log_level"},
		{"bad source", "proxmenux:\n  api_url: http://h:8008\n  source: none\ndomain:\n  base: home.example.com\n", "source"},
		{"single-label domain", "proxmenux:\n  api_url: http://h:8008\ndomain:\n  base: localhost\n", "domain.base"},
		{"per-app no email", "proxmenux:\n  api_url: http://h:8008\ndomain:\n  base: home.example.com\ntls:\n  mode: per-app\n", "tls.email"},
		{"wildcard no dns", "proxmenux:\n  api_url: http://h:8008\ndomain:\n  base: home.example.com\ntls:\n  mode: wildcard\n  email: a@b.c\n", "dns_provider"},
		{"unknown key", "banana: yes\n" + string(baseCfg()), "parse config"},
		{"bad duration", "provider:\n  poll_interval: 30\n" + string(baseCfg()), "duration"},
		{"bad octal", string(baseCfg()) + "output:\n  mode: 999\n", "output.mode"},
		{"bad listen", string(baseCfg()) + "dashboard:\n  listen: \":99999\"\n", "dashboard.listen"},
		{"bad cidr", string(baseCfg()) + "dashboard:\n  via_traefik: true\n  allow_cidrs: [\"10.0.0.0/99\"]\n", "allow_cidrs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yamlStr))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestValidatePerAppOK(t *testing.T) {
	b := []byte("proxmenux:\n  api_url: http://h:8008\ndomain:\n  base: home.example.com\ntls:\n  mode: per-app\n  email: a@b.c\n  cert_resolver: letsencrypt\n")
	if _, err := Parse(b); err != nil {
		t.Fatalf("per-app with email+resolver should validate, got: %v", err)
	}
}

func TestOutputMode(t *testing.T) {
	cfg, err := Parse(baseCfg())
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	m, err := cfg.Output.OutputMode()
	if err != nil {
		t.Fatalf("OutputMode error: %v", err)
	}
	if m.String() != "-rw-r--r--" {
		t.Errorf("mode = %s, want -rw-r--r--", m.String())
	}
}
