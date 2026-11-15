// Package discover finds the ProxMenux monitor API on the local LAN by
// probing TCP:8008 and checking GET /api/health/status. Used by install.sh
// so the user doesn't have to know the PVE host IP (plug-and-play).
package discover

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Candidates returns base URLs ("http://IP:8008") of hosts on the local
// subnets that answer the monitor-API health endpoint.
func Candidates(ctx context.Context, port int, path string, timeout time.Duration) ([]string, error) {
	hosts, err := localSubnetHosts()
	if err != nil {
		return nil, err
	}

	var mu sync.Mutex
	var found []string
	sem := make(chan struct{}, 64)
	var wg sync.WaitGroup

	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	for _, h := range hosts {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			if probe(ctx, client, host, port, path) {
				mu.Lock()
				found = append(found, "http://"+host+":"+strconv.Itoa(port))
				mu.Unlock()
			}
		}(h)
	}
	wg.Wait()

	sort.Slice(found, func(i, j int) bool {
		return ipLess(found[i], found[j])
	})
	return found, nil
}

func probe(ctx context.Context, client *http.Client, host string, port int, path string) bool {
	// Fast TCP check first.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 400*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()

	url := fmt.Sprintf("http://%s:%d%s", host, port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// localSubnetHosts lists IPv4 hosts of the /24 containing each non-loopback
// local address (bounded to 256 hosts per interface).
func localSubnetHosts() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
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
			// Only scan /24 around self, and skip own address later is fine.
			base := []byte{ip4[0], ip4[1], ip4[2], 0}
			for i := 1; i <= 254; i++ {
				h := net.IP(append([]byte(nil), base...)).String()
				h = strings.TrimSuffix(h, ".0") + "." + strconv.Itoa(i)
				h = fmt.Sprintf("%d.%d.%d.%d", base[0], base[1], base[2], i)
				if !seen[h] {
					seen[h] = true
					out = append(out, h)
				}
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no IPv4 interfaces found to scan")
	}
	return out, nil
}

func ipLess(a, b string) bool {
	ah, _ := hostOf(a)
	bh, _ := hostOf(b)
	return strings.Compare(ah, bh) < 0
}

func hostOf(url string) (string, error) {
	u := strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")
	return strings.Split(u, ":")[0], nil
}
