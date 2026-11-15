package proxmenux

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/macedot/traefik-proxmenux-provider/internal/config"
)

func newTestClient(t *testing.T, srvURL string, tokenFile string) *Client {
	pc := config.ProxMenux{APIURL: srvURL, TokenFile: tokenFile, Timeout: config.Duration(5 * time.Second), MaxResponseBytes: 1 << 20}
	return New(pc, nil)
}

func writeToken(t *testing.T, token string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiscoveryPorts(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		if r.URL.Path == "/api/vms/7/apps/docker-discovery" {
			_ = json.NewEncoder(w).Encode(map[string]any{"candidates": []map[string]any{
				{"container_name": "gluetun", "default_ports": []int{8888}},
				{"container_name": "gluetun", "default_ports": []int{8888}}, // dup, must dedupe
				{"container_name": "", "default_ports": []int{9999}},        // no name, dropped
			}})
			return
		}
		if r.URL.Path == "/api/vms/7/apps/native-discovery" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "")
	got := c.DiscoveryPorts(context.Background(), 7)
	if len(got) != 1 || got[0].Name != "gluetun" || got[0].Port != 8888 || got[0].Source != "docker" {
		t.Fatalf("DiscoveryPorts = %+v", got)
	}
	if len(seen) != 2 {
		t.Fatalf("endpoints queried: %v", seen)
	}
}

func TestSystemHostname(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/system" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"hostname": "pve-node"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "")
	h, err := c.SystemHostname(context.Background())
	if err != nil || h != "pve-node" {
		t.Fatalf("SystemHostname = %q, %v", h, err)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{})
	}))
	defer bad.Close()
	if _, err = newTestClient(t, bad.URL, "").SystemHostname(context.Background()); err == nil {
		t.Fatal("empty hostname must error")
	}
}

func TestReadTokenTrimsAndRequiresContent(t *testing.T) {
	tf := writeToken(t, "  secret-token  \n")
	c := newTestClient(t, "http://127.0.0.1:1", tf)
	got, err := c.readToken()
	if err != nil || got != "secret-token" {
		t.Fatalf("readToken = %q, %v", got, err)
	}

	empty := filepath.Join(t.TempDir(), "empty")
	_ = os.WriteFile(empty, []byte("   "), 0o600)
	c2 := newTestClient(t, "http://127.0.0.1:1", empty)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c3 := newTestClient(t, srv.URL, empty)
	if _, err := c3.getJSON(context.Background(), "/api/vms", true); err == nil || err.Error() == "" {
		t.Fatal("empty token file must error")
	}
	_ = c2
}

func TestResponseSizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 64; i++ {
			_, _ = w.Write(make([]byte, 1024))
		}
	}))
	defer srv.Close()

	pc := config.ProxMenux{APIURL: srv.URL, Timeout: config.Duration(5 * time.Second), MaxResponseBytes: 1024}
	c := New(pc, nil)
	if _, err := c.ListGuests(context.Background()); err == nil {
		t.Fatal("oversized response must error")
	}
}

func TestRedirectRejected(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/vms", http.StatusFound)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL, "")
	if _, err := c.ListGuests(context.Background()); err == nil {
		t.Fatal("redirects must be rejected")
	}
}

func TestDetectMatrix(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		token   string
		want    AuthMode
		wantErr bool
	}{
		{"open", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }, "", AuthOpen, false},
		{"auth-token-ok", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer good" {
				w.WriteHeader(200)
				return
			}
			w.WriteHeader(401)
		}, "good", AuthToken, false},
		{"auth-no-token", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }, "", "", true},
		{"auth-bad-token", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }, "wrong", "", true},
		{"server-error", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }, "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			var tf string
			if tc.token != "" {
				tf = writeToken(t, tc.token)
			}
			c := newTestClient(t, srv.URL, tf)
			mode, err := c.Detect(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("Detect err = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && mode != tc.want {
				t.Fatalf("mode = %q, want %q", mode, tc.want)
			}
		})
	}
}
