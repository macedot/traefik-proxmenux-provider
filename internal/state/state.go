// Package state holds the last sync snapshot shared with the dashboard.
package state

import (
	"sync/atomic"
	"time"

	"github.com/macedot/traefik-proxmenux-provider/internal/transform"
)

// Snapshot is what the dashboard shows. Contains no secrets.
type Snapshot struct {
	Version     string              `json:"version"`
	Domain      string              `json:"domain"`
	TLSMode     string              `json:"tls_mode"`
	Entrypoint  string              `json:"entrypoint"`
	AuthMode    string              `json:"auth_mode"` // open | token | unknown
	GeneratedAt time.Time           `json:"generated_at"`
	NextSyncAt  time.Time           `json:"next_sync_at"`
	SyncCount   uint64              `json:"sync_count"`
	LastError   string              `json:"last_error,omitempty"`
	EditEnabled bool                `json:"edit_enabled"`
	Routes      []transform.Route   `json:"routes"`
	Skipped     []transform.Skipped `json:"skipped"`
}

func (s *Snapshot) Counts() (exposed, skipped int) {
	return len(s.Routes), len(s.Skipped)
}

// Holder is a concurrency-safe single-snapshot holder.
type Holder struct {
	v   atomic.Pointer[Snapshot]
	ver string
}

func New(version string) *Holder {
	h := &Holder{ver: version}
	h.v.Store(&Snapshot{Version: version})
	return h
}

// Update stores a new snapshot derived from the last one (preserving
// SyncCount continuity is done by the caller bumping SyncCount).
func (h *Holder) Update(s *Snapshot) {
	s.Version = h.ver
	h.v.Store(s)
}

func (h *Holder) Get() *Snapshot {
	return h.v.Load()
}

// Bump returns the current snapshot for mutation before Update.
func (h *Holder) Bump() *Snapshot {
	cur := h.Get()
	cp := *cur
	cp.SyncCount = cur.SyncCount + 1
	return &cp
}
