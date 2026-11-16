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

func TestCustomEntries(t *testing.T) {
	st := Open(filepath.Join(t.TempDir(), "overrides.json"))
	e := CustomEntry{ID: "c1", Name: "NAS", Slug: "nas", Domain: "", IP: "192.0.2.10", Port: 5000, Scheme: "http"}
	if err := st.AddCustom(e); err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(st.ListCustom()) != 1 {
		t.Fatalf("list = %d, want 1", len(st.ListCustom()))
	}
	// duplicate hostname rejected
	dup := e
	dup.ID = "c2"
	if err := st.AddCustom(dup); err == nil {
		t.Fatal("duplicate hostname must be rejected")
	}
	// rename + domain pick
	if err := st.SetCustomSlug("c1", "storage"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := st.SetCustomDomain("c1", "alt.example.com"); err != nil {
		t.Fatalf("domain: %v", err)
	}
	got := st.ListCustom()[0]
	if got.Slug != "storage" || got.Domain != "alt.example.com" {
		t.Fatalf("entry = %+v", got)
	}
	// unknown id
	if err := st.SetCustomSlug("nope", "x"); err == nil {
		t.Fatal("unknown id must error")
	}
	// remove cleans toggle/domain state too
	_ = st.SetDisabled("custom:c1", true)
	removed, err := st.RemoveCustom("c1")
	if err != nil || !removed {
		t.Fatalf("remove: %v %v", removed, err)
	}
	f, _ := st.Load()
	if len(f.Custom) != 0 || f.Disabled["custom:c1"] {
		t.Fatalf("state not cleaned: %+v", f)
	}
	if removed2, _ := st.RemoveCustom("c1"); removed2 {
		t.Fatal("second remove must report false")
	}
}
