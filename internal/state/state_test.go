package state

import (
	"sync"
	"testing"
	"time"

	"github.com/macedot/traefik-proxmenux-provider/internal/transform"
)

func TestHolderLifecycle(t *testing.T) {
	h := New("v1")
	got := h.Get()
	if got.Version != "v1" {
		t.Fatalf("version = %q", got.Version)
	}
	exposed, skipped := got.Counts()
	if exposed != 0 || skipped != 0 {
		t.Fatalf("empty counts = %d/%d", exposed, skipped)
	}

	got.Domain = "example.com"
	got.TLSMode = "internal"
	got.Entrypoint = "websecure"
	got.AuthMode = "open"
	got.GeneratedAt = time.Unix(1700000000, 0)
	got.NextSyncAt = got.GeneratedAt.Add(30 * time.Second)
	got.EditEnabled = true
	got.Domains = []string{"example.com"}
	got.DomainsOff = []string{"off.example.com"}
	got.InfraOff = true
	got.LastError = ""
	got.Routes = []transform.Route{{Slug: "a", Served: true}, {Slug: "b", Served: false}}
	got.Skipped = []transform.Skipped{{VMID: 1, Reason: "nope"}}
	h.Update(got)

	after := h.Get()
	exposed, skipped = after.Counts()
	if exposed != 2 || skipped != 1 {
		t.Fatalf("counts = %d/%d, want 2/1", exposed, skipped)
	}
	if !after.InfraOff || len(after.DomainsOff) != 1 {
		t.Fatalf("snapshot fields not carried: %+v", after)
	}

	bumped := h.Bump()
	if bumped.SyncCount != after.SyncCount+1 {
		t.Fatalf("Bump = %d, want %d", bumped.SyncCount, after.SyncCount+1)
	}
	if h.Get().SyncCount != after.SyncCount {
		t.Fatal("Bump mutated the stored snapshot")
	}
}

func TestHolderConcurrentAccess(t *testing.T) {
	h := New("t")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s := h.Bump()
				s.SyncCount = uint64(i*1000 + j)
				h.Update(s)
				_ = h.Get().SyncCount
			}
		}(i)
	}
	wg.Wait()
}
