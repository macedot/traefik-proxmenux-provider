// Package proxmenux is a read-only client for the ProxMenux monitor API.
//
// Auth contract (if-and-only-if): the client detects whether the monitor
// requires authentication. A Bearer token is sent *only* when the monitor
// actually demands one; when the monitor is auth-free no credentials are
// sent at all. Callers must Detect() first; SendToken stays false until
// detection proves the monitor requires a token and accepts ours.
//
// Security posture: fixed base URL, redirects rejected, response size
// capped, per-request timeout, token read from a 0600 file at poll time
// (rotates without restart).
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

// ErrAuthRequired is returned when the monitor demands authentication and
// none (or none valid) was provided. Callers must not publish routes in
// this state.
var ErrAuthRequired = errors.New("monitor API requires authentication")

// AuthMode is the detected authentication posture of the monitor API.
type AuthMode string

const (
	AuthUnknown AuthMode = "unknown"
	AuthOpen    AuthMode = "open"  // monitor accepts unauthenticated reads
	AuthToken   AuthMode = "token" // monitor requires (and accepted) our Bearer token
)

// Guest is one entry of GET /api/vms.
type Guest struct {
	VMID       int      `json:"vmid"`
	Name       string   `json:"name"`
	IP         string   `json:"ip"`
	IPs        []string `json:"ips"` // all guest IPs (VPN + LAN) — pick by subnet
	Type       string   `json:"type"`
	Status     string   `json:"status"`
	Tags       string   `json:"tags"`
	AppWatches []Watch  `json:"app_watches"`
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
	base      string
	tokenRef  config.ProxMenux
	sendToken bool // set only after Detect() proves the monitor requires a token
	http      *http.Client
	log       *slog.Logger
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

// SendToken is called by the caller after a successful Detect(AuthToken);
// credentials are never attached before that.
func (c *Client) SendToken() { c.sendToken = true }

func (c *Client) readToken() (string, error) {
	if c.tokenRef.TokenFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(c.tokenRef.TokenFile) // #nosec G304 -- admin-controlled config path
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// fetch performs GET /api/vms. withToken controls the Authorization header;
// the token is only ever sent when the caller proved the monitor wants one.
func (c *Client) fetch(ctx context.Context, withToken bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/vms", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "traefik-proxmenux-provider")

	if withToken {
		token, err := c.readToken()
		if err != nil {
			return nil, err
		}
		if token == "" {
			return nil, fmt.Errorf("token file %q is empty", c.tokenRef.TokenFile)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s/api/vms: %w", c.base, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		// fallthrough to body read below
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w (HTTP %d)", ErrAuthRequired, resp.StatusCode)
	default:
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
	return body, nil
}

// Detect probes the monitor's authentication posture:
//   - unauthenticated /api/vms returns 200     → AuthOpen
//   - 401/403 and our configured token works   → AuthToken (then call SendToken)
//   - 401/403 with no/invalid token            → error wrapping ErrAuthRequired
func (c *Client) Detect(ctx context.Context) (AuthMode, error) {
	if _, err := c.fetch(ctx, false); err == nil {
		return AuthOpen, nil
	} else if !errors.Is(err, ErrAuthRequired) {
		return "", err // network / server problem — not an auth verdict
	}

	// Monitor demands authentication.
	if c.tokenRef.TokenFile == "" {
		return "", fmt.Errorf("%w: monitor has auth enabled and no token_file is configured", ErrAuthRequired)
	}
	if _, err := c.fetch(ctx, true); err != nil {
		if errors.Is(err, ErrAuthRequired) {
			return "", fmt.Errorf("%w: configured token was rejected by the monitor", ErrAuthRequired)
		}
		return "", err
	}
	return AuthToken, nil
}

// ListGuests fetches and decodes GET /api/vms. SendToken() must have been
// called (after Detect) for credentials to be attached.
func (c *Client) ListGuests(ctx context.Context) ([]Guest, error) {
	body, err := c.fetch(ctx, c.sendToken)
	if err != nil {
		return nil, err
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
