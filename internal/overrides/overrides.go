// Package overrides persists dashboard edits (slug overrides keyed by VMID)
// to a small JSON file. Runtime edits win over config.yaml slug_overrides.
package overrides

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type File struct {
	SlugOverrides map[string]string `json:"slug_overrides"`
	Domains       map[string]string `json:"domains"`     // vmid/infra-key -> URL domain (per-app pick)
	Disabled      map[string]bool   `json:"disabled"`    // vmid/infra-key -> explicit state
	DomainsOff    map[string]bool   `json:"domains_off"` // domain -> global off (multiplier)
	InfraOff      bool              `json:"infra_off"`   // global infra switch
	Custom        []CustomEntry     `json:"custom"`      // manually defined entries
}

// CustomEntry is a user-defined route created from the dashboard's + button.
// ID is the stable identity ("custom:<id>" keys everywhere); Slug is current
// (renames update it); Domain "" = config default.
type CustomEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Slug   string `json:"slug"`
	Domain string `json:"domain"`
	IP     string `json:"ip"`
	Port   int    `json:"port"`
	Scheme string `json:"scheme"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func Open(path string) *Store {
	return &Store{path: path}
}

func (s *Store) Load() (File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) loadLocked() (File, error) {
	var f File
	b, err := os.ReadFile(s.path) // #nosec G304 -- admin-controlled config path
	if err != nil {
		if os.IsNotExist(err) {
			return File{
				SlugOverrides: map[string]string{},
				Domains:       map[string]string{},
				DomainsOff:    map[string]bool{},
				Disabled:      map[string]bool{},
			}, nil
		}
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return File{
			SlugOverrides: map[string]string{},
			Domains:       map[string]string{},
			DomainsOff:    map[string]bool{},
			Disabled:      map[string]bool{},
		}, fmt.Errorf("parse overrides: %w", err)
	}
	if f.SlugOverrides == nil {
		f.SlugOverrides = map[string]string{}
	}
	if f.Domains == nil {
		f.Domains = map[string]string{}
	}
	if f.DomainsOff == nil {
		f.DomainsOff = map[string]bool{}
	}
	if f.Disabled == nil {
		f.Disabled = map[string]bool{}
	}
	return f, nil
}

// SetDomain stores the URL domain pick for a vmid (validated by the caller).
func (s *Store) SetDomain(vmid, domain string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked()
	if err != nil {
		return err
	}
	f.Domains[vmid] = domain
	return s.saveLocked(f)
}

// ClearDomain removes a domain pick; returns whether one existed.
func (s *Store) ClearDomain(vmid string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	if _, ok := f.Domains[vmid]; !ok {
		return false, nil
	}
	delete(f.Domains, vmid)
	return true, s.saveLocked(f)
}

// SetDomainOff stores the global on/off switch for a whole domain
// (multiplier over per-app state). off=false removes the entry.
func (s *Store) SetDomainOff(domain string, off bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked()
	if err != nil {
		return err
	}
	if off {
		f.DomainsOff[domain] = true
	} else {
		delete(f.DomainsOff, domain)
	}
	return s.saveLocked(f)
}

// SetInfraOff stores the global infrastructure switch.
func (s *Store) SetInfraOff(off bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked()
	if err != nil {
		return err
	}
	f.InfraOff = off
	return s.saveLocked(f)
}

// SetDisabled stores the explicit per-app toggle state (true = off,
// false = on). Absent key = config default (routing.default_enabled).
func (s *Store) SetDisabled(vmid string, disabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked()
	if err != nil {
		return err
	}
	f.Disabled[vmid] = disabled
	return s.saveLocked(f)
}

// SetSlug stores a slug override for a vmid (already validated by the caller).
func (s *Store) SetSlug(vmid, slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked()
	if err != nil {
		return err
	}
	f.SlugOverrides[vmid] = slug
	return s.saveLocked(f)
}

// ClearSlug removes an override; returns whether one existed.
func (s *Store) ClearSlug(vmid string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	if _, ok := f.SlugOverrides[vmid]; !ok {
		return false, nil
	}
	delete(f.SlugOverrides, vmid)
	return true, s.saveLocked(f)
}

func (s *Store) saveLocked(f File) error {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".overrides-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

// ListCustom returns the manually defined entries (empty when none).
func (s *Store) ListCustom() []CustomEntry {
	f, err := s.Load()
	if err != nil {
		return nil
	}
	return f.Custom
}

// AddCustom appends a manually defined entry; the ID must be unique and the
// slug+domain combination must not collide with an existing entry.
func (s *Store) AddCustom(e CustomEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked()
	if err != nil {
		return err
	}
	host := e.Slug + "." + e.Domain
	for _, c := range f.Custom {
		if c.ID == e.ID {
			return fmt.Errorf("custom id %s already exists", e.ID)
		}
		if c.Slug+"."+c.Domain == host {
			return fmt.Errorf("hostname %s already exists", host)
		}
	}
	f.Custom = append(f.Custom, e)
	return s.saveLocked(f)
}

// RemoveCustom deletes an entry and its toggle/domain state; reports whether
// an entry was removed.
func (s *Store) RemoveCustom(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	found := false
	out := f.Custom[:0]
	for _, c := range f.Custom {
		if c.ID == id {
			found = true
			continue
		}
		out = append(out, c)
	}
	if !found {
		return false, nil
	}
	f.Custom = out
	key := "custom:" + id
	delete(f.Disabled, key)
	delete(f.Domains, key)
	delete(f.SlugOverrides, key)
	return true, s.saveLocked(f)
}

// SetCustomSlug renames a manually defined entry (caller slugifies).
func (s *Store) SetCustomSlug(id, slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i := range f.Custom {
		if f.Custom[i].ID == id {
			f.Custom[i].Slug = slug
			return s.saveLocked(f)
		}
	}
	return fmt.Errorf("custom entry %s not found", id)
}

// SetCustomDomain stores the URL domain pick for an entry; empty = default.
func (s *Store) SetCustomDomain(id, domain string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i := range f.Custom {
		if f.Custom[i].ID == id {
			f.Custom[i].Domain = domain
			return s.saveLocked(f)
		}
	}
	return fmt.Errorf("custom entry %s not found", id)
}
