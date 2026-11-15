// Package proxmenux is a read-only client for the ProxMenux monitor API.
//
// Security posture: optional Bearer token (read from a 0600 file, re-read
// every poll so it can rotate without restart), fixed base URL, redirects
// rejected (no credential/URL hop), response size capped, per-request
// timeout.
package proxmenux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/macedot/traefik-proxmenux-provider/internal/config"
)

// Guest is one entry of GET /api/vms.
type Guest struct {
	VMID       int     `json:"vmid"`
	Name       string  `json:"name"`
	IP         string  `json:"ip"`
	Type       string  `json:"type"`
	Status     string  `json:"status"`
	Tags       string  `json:"tags"`
	AppWatches []Watch `json:"app_watches"`
}

// Watch is a monitored app on a guest. OCI-managed apps are synthetic
// read-only watches maintained by ProxMenux from the OCI catalog
// (InstalledVia == "oci_image"); their ports are catalog-known.
type Watch struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	HelperSlug   string  `json:"helper_slug"`
	HealthPath   *string `json:"health_path"`
	Ports        []Port  `json:"ports"`
	InstalledVia string  `json:"installed_via"`
}

// Port is a catalog-known service port of an app.
type Port struct {
	Port     int    `json:"port"`
	Scheme   string `json:"scheme"`
	WebPath  string `json:"web_path"`
	Category string `json:"category"`
}

// Client queries the monitor API.
type Client struct {
	base     string
	tokenRef config.ProxMenux
	http     *http.Client
	log      *slog.Logger
}

func New(pc config.ProxMenux, log *slog.Logger) *Client {
	return &Client{
		base:     pc.APIURL,
		tokenRef: pc,
		http: &http.Client{
			Timeout: pc.Timeout.Std(),
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return errors.New("proxmenux client: redirects are not followed")
			},
		},
		log: log,
	}
}

func (c *Client) readToken() (string, error) {
	if c.tokenRef.TokenFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(c.tokenRef.TokenFile)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// ListGuests fetches and decodes GET /api/vms.
func (c *Client) ListGuests(ctx context.Context) ([]Guest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/vms", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "traefik-proxmenux-provider")
	token, err := c.readToken()
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s/api/vms: %w", c.base, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GET %s/api/vms: unexpected status %d: %s",
			c.base, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(c.tokenRef.MaxResponseBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if int64(len(body)) > int64(c.tokenRef.MaxResponseBytes) {
		return nil, fmt.Errorf("response exceeds max_response_bytes (%d)", c.tokenRef.MaxResponseBytes)
	}

	var guests []Guest
	if err := json.Unmarshal(body, &guests); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return guests, nil
}

// HasTag reports whether the guest's tag string contains tag.
func (g Guest) HasTag(tag string) bool {
	for _, t := range strings.Split(g.Tags, ";") {
		if strings.TrimSpace(t) == tag {
			return true
		}
	}
	return false
}

// CheckAPI performs a lightweight availability probe of the monitor API.
func CheckAPI(ctx context.Context, baseURL string, timeout time.Duration) error {
	base := strings.TrimRight(baseURL, "/")
	cli := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return errors.New("redirects are not followed")
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/health/status", nil)
	if err != nil {
		return err
	}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
