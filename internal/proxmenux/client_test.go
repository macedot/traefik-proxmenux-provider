package proxmenux

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/macedot/traefik-proxmenux-provider/internal/config"
)

func testCfg(pc func(*config.ProxMenux)) config.ProxMenux {
	pc2 := config.ProxMenux{
		APIURL:           "http://placeholder",
		Timeout:          config.Duration(2 * time.Second),
		MaxResponseBytes: 64 * 1024,
		Source:           "oci",
	}
	if pc != nil {
		pc(&pc2)
	}
	return pc2
}

const fixtureJSON = `[
  {"vmid":100,"name":"adguard","ip":"192.0.2.2","type":"lxc","status":"running",
   "tags":"adblock;oci;proxmenux",
   "app_watches":[{"id":"f26e","name":"AdGuard Home","helper_slug":"image-adguard-home",
     "health_path":null,"installed_via":"oci_image",
     "ports":[{"port":3000,"scheme":"http","web_path":"/","category":"DNS"}]}]},
  {"vmid":113,"name":"plex","ip":"192.0.2.70","type":"lxc","status":"stopped",
   "tags":"media","app_watches":[]}
]`

func TestListGuestsParsesFixture(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/vms" {
			t.Errorf("path = %q, want /api/vms", r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		w.Write([]byte(fixtureJSON))
	}))
	defer srv.Close()

	c := New(testCfg(func(p *config.ProxMenux) { p.APIURL = srv.URL }), slog.Default())
	guests, err := c.ListGuests(context.Background())
	if err != nil {
		t.Fatalf("ListGuests error: %v", err)
	}
	if len(guests) != 2 {
		t.Fatalf("guests = %d, want 2", len(guests))
	}
	g := guests[0]
	if g.VMID != 100 || g.Name != "adguard" || !g.HasTag("oci") || g.HasTag("nope") {
		t.Errorf("guest 0 fields wrong: %+v", g)
	}
	if len(g.AppWatches) != 1 || g.AppWatches[0].Name != "AdGuard Home" ||
		len(g.AppWatches[0].Ports) != 1 || g.AppWatches[0].Ports[0].Port != 3000 {
		t.Errorf("watch fields wrong: %+v", g.AppWatches)
	}
}

func TestListGuestsSendsBearerToken(t *testing.T) {
	dir := t.TempDir()
	tokFile := filepath.Join(dir, "token")
	os.WriteFile(tokFile, []byte("  secret-token  \n"), 0o600)

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := New(testCfg(func(p *config.ProxMenux) {
		p.APIURL = srv.URL
		p.TokenFile = tokFile
	}), slog.Default())
	c.SendToken() // only after Detect proves the monitor requires a token
	if _, err := c.ListGuests(context.Background()); err != nil {
		t.Fatalf("ListGuests error: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want Bearer secret-token", gotAuth)
	}
}

func TestNoCredentialsInOpenMode(t *testing.T) {
	// If-and-only-if: a configured token must NOT be sent while the monitor
	// is auth-free (before SendToken is proven justified).
	dir := t.TempDir()
	tokFile := filepath.Join(dir, "token")
	os.WriteFile(tokFile, []byte("secret-token"), 0o600)

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := New(testCfg(func(p *config.ProxMenux) {
		p.APIURL = srv.URL
		p.TokenFile = tokFile
	}), slog.Default())

	mode, err := c.Detect(context.Background())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if mode != AuthOpen {
		t.Fatalf("mode = %q, want open", mode)
	}
	if _, err := c.ListGuests(context.Background()); err != nil {
		t.Fatalf("ListGuests: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (no credentials to an auth-free monitor)", gotAuth)
	}
}

func TestDetectPaths(t *testing.T) {
	t.Run("auth required, no token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(401)
		}))
		defer srv.Close()
		c := New(testCfg(func(p *config.ProxMenux) { p.APIURL = srv.URL }), slog.Default())
		_, err := c.Detect(context.Background())
		if !errors.Is(err, ErrAuthRequired) {
			t.Fatalf("want ErrAuthRequired, got %v", err)
		}
	})

	t.Run("auth required, token accepted", func(t *testing.T) {
		dir := t.TempDir()
		tokFile := filepath.Join(dir, "token")
		os.WriteFile(tokFile, []byte("good"), 0o600)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer good" {
				w.Write([]byte("[]"))
				return
			}
			w.WriteHeader(401)
		}))
		defer srv.Close()
		c := New(testCfg(func(p *config.ProxMenux) {
			p.APIURL = srv.URL
			p.TokenFile = tokFile
		}), slog.Default())
		mode, err := c.Detect(context.Background())
		if err != nil || mode != AuthToken {
			t.Fatalf("mode = %q err = %v, want token", mode, err)
		}
	})

	t.Run("auth required, token rejected", func(t *testing.T) {
		dir := t.TempDir()
		tokFile := filepath.Join(dir, "token")
		os.WriteFile(tokFile, []byte("bad"), 0o600)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(403)
		}))
		defer srv.Close()
		c := New(testCfg(func(p *config.ProxMenux) {
			p.APIURL = srv.URL
			p.TokenFile = tokFile
		}), slog.Default())
		_, err := c.Detect(context.Background())
		if !errors.Is(err, ErrAuthRequired) || !strings.Contains(err.Error(), "rejected") {
			t.Fatalf("want rejected-token ErrAuthRequired, got %v", err)
		}
	})
}

func TestListGuests401MappedToErrAuthRequired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	c := New(testCfg(func(p *config.ProxMenux) { p.APIURL = srv.URL }), slog.Default())
	_, err := c.ListGuests(context.Background())
	if !errors.Is(err, ErrAuthRequired) {
		t.Fatalf("want ErrAuthRequired, got %v", err)
	}
}

func TestListGuestsErrorPaths(t *testing.T) {
	t.Run("http 500", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
		}))
		defer srv.Close()
		c := New(testCfg(func(p *config.ProxMenux) { p.APIURL = srv.URL }), slog.Default())
		if _, err := c.ListGuests(context.Background()); err == nil || !strings.Contains(err.Error(), "500") {
			t.Fatalf("want 500 error, got %v", err)
		}
	})

	t.Run("size cap", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(strings.Repeat("a", 70000)))
		}))
		defer srv.Close()
		c := New(testCfg(func(p *config.ProxMenux) { p.APIURL = srv.URL }), slog.Default())
		if _, err := c.ListGuests(context.Background()); err == nil || !strings.Contains(err.Error(), "max_response_bytes") {
			t.Fatalf("want size-cap error, got %v", err)
		}
	})

	t.Run("redirect rejected", func(t *testing.T) {
		dst := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("[]"))
		}))
		defer dst.Close()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, dst.URL+"/api/vms", http.StatusFound)
		}))
		defer srv.Close()
		c := New(testCfg(func(p *config.ProxMenux) { p.APIURL = srv.URL }), slog.Default())
		if _, err := c.ListGuests(context.Background()); err == nil {
			t.Fatal("want redirect error, got nil")
		}
	})

	t.Run("bad json", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("not json"))
		}))
		defer srv.Close()
		c := New(testCfg(func(p *config.ProxMenux) { p.APIURL = srv.URL }), slog.Default())
		if _, err := c.ListGuests(context.Background()); err == nil || !strings.Contains(err.Error(), "decode") {
			t.Fatalf("want decode error, got %v", err)
		}
	})
}

func TestCheckAPI(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health/status" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer ok.Close()
	if err := CheckAPI(context.Background(), ok.URL, time.Second); err != nil {
		t.Fatalf("CheckAPI(ok) error: %v", err)
	}
	if err := CheckAPI(context.Background(), "http://127.0.0.1:1", 300*time.Millisecond); err == nil {
		t.Fatal("CheckAPI(dead) should fail")
	}
}
