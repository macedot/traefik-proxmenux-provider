package discover

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}

	client := &http.Client{}
	ctx := context.Background()

	if !probe(ctx, client, host, p, "/ok") {
		t.Fatal("200 endpoint must probe true")
	}
	if probe(ctx, client, host, p, "/missing") {
		t.Fatal("404 endpoint must probe false")
	}
	if probe(ctx, client, "127.0.0.1", 1, "/ok") {
		t.Fatal("closed port must probe false")
	}
}

func TestIPLessAndHostOf(t *testing.T) {
	a, b := "http://10.0.0.2:8008", "http://10.0.0.10:8008"
	if !ipLess(a, b) {
		t.Fatal("10.0.0.2 must sort before 10.0.0.10")
	}
	if ipLess(b, a) {
		t.Fatal("comparison must be antisymmetric here")
	}
	h, err := hostOf(a)
	if err != nil || h != "10.0.0.2" {
		t.Fatalf("hostOf = %q, %v", h, err)
	}
	h2, err := hostOf("https://host.example:443")
	if err != nil || h2 != "host.example" {
		t.Fatalf("hostOf https = %q, %v", h2, err)
	}
}
