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
