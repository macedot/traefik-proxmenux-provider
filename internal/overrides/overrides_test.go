package overrides

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overrides.json")
	s := Open(path)

	// Missing file = empty.
	f, err := s.Load()
	if err != nil || len(f.SlugOverrides) != 0 {
		t.Fatalf("initial load: %+v err=%v", f, err)
	}

	if err := s.SetSlug("100", "dns"); err != nil {
		t.Fatalf("SetSlug: %v", err)
	}
	if err := s.SetSlug("106", "prowlarr"); err != nil {
		t.Fatalf("SetSlug2: %v", err)
	}
	f, err = s.Load()
	if err != nil || f.SlugOverrides["100"] != "dns" || f.SlugOverrides["106"] != "prowlarr" {
		t.Fatalf("after set: %+v err=%v", f, err)
	}

	// Persisted on disk with restrictive perms.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %s, want 0600", fi.Mode().Perm())
	}

	existed, err := s.ClearSlug("100")
	if err != nil || !existed {
		t.Fatalf("ClearSlug: existed=%v err=%v", existed, err)
	}
	existed, err = s.ClearSlug("100")
	if err != nil || existed {
		t.Fatalf("second ClearSlug: existed=%v err=%v", existed, err)
	}
	f, _ = s.Load()
	if len(f.SlugOverrides) != 1 {
		t.Fatalf("after clear: %+v", f.SlugOverrides)
	}
}

func TestStoreLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "overrides.json")
	s := Open(path)

	// setters on a fresh store
	if err := s.SetSlug("100", "dns"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDomain("100", "b.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDomainOff("b.example.com", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetInfraOff(true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDisabled("100", false); err != nil {
		t.Fatal(err)
	}

	f, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if f.SlugOverrides["100"] != "dns" || f.Domains["100"] != "b.example.com" {
		t.Fatalf("slug/domain not persisted: %+v", f)
	}
	if !f.DomainsOff["b.example.com"] || !f.InfraOff || f.Disabled["100"] {
		t.Fatalf("switches not persisted: %+v", f)
	}

	// persistence across reopen
	s2 := Open(path)
	f2, err := s2.Load()
	if err != nil {
		t.Fatal(err)
	}
	if f2.InfraOff != true || f2.SlugOverrides["100"] != "dns" {
		t.Fatalf("reopen lost data: %+v", f2)
	}

	// clears
	if _, err := s2.ClearSlug("100"); err != nil {
		t.Fatal(err)
	}
	if _, err := s2.ClearDomain("100"); err != nil {
		t.Fatal(err)
	}
	f3, _ := s2.Load()
	if _, ok := f3.SlugOverrides["100"]; ok {
		t.Fatal("ClearSlug did not remove")
	}
	if _, ok := f3.Domains["100"]; ok {
		t.Fatal("ClearDomain did not remove")
	}
}

func TestStoreCorruptFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "overrides.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// corrupt file: Load must surface the error AND return initialized maps
	// (the caller logs the warning and keeps running)
	f, err := Open(p).Load()
	if err == nil {
		t.Fatal("corrupt file must report an error")
	}
	if f.SlugOverrides == nil || f.Domains == nil || f.DomainsOff == nil || f.Disabled == nil {
		t.Fatalf("maps must be initialized even on error: %+v", f)
	}
}
