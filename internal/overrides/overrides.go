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
	Domains       map[string]string `json:"domains"`  // vmid -> URL domain (per-app pick)
	Disabled      map[string]bool   `json:"disabled"` // vmid -> app toggle off
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
			return File{SlugOverrides: map[string]string{}, Disabled: map[string]bool{}}, nil
		}
		return f, err
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return File{SlugOverrides: map[string]string{}, Disabled: map[string]bool{}}, fmt.Errorf("parse overrides: %w", err)
	}
	if f.SlugOverrides == nil {
		f.SlugOverrides = map[string]string{}
	}
	if f.Domains == nil {
		f.Domains = map[string]string{}
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
