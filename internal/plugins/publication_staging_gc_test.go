package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fetch killed between stagePlugin's MkdirTemp and its deferred removal
// strands a marked .staging- container under publishing/<marketplace>/<plugin>,
// including its .git. Gc must reclaim only containers that carry the marker
// this code writes and are old enough that no fetch in flight could still own
// them, and it must not report them as clones it deliberately removed.
func TestGcReclaimsAbandonedPublicationStaging(t *testing.T) {
	m := NewManager(t.TempDir())
	parent := m.publishingDir("acme", "widget")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	aged := func(name string, marker func(string) error) string {
		dir := filepath.Join(parent, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if marker != nil {
			if err := marker(dir); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	regularMarker := func(dir string) error {
		return os.WriteFile(filepath.Join(dir, stagingMarker), nil, 0o600)
	}

	abandoned := aged(".staging-old", regularMarker)
	young := filepath.Join(parent, ".staging-young")
	if err := os.MkdirAll(young, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := regularMarker(young); err != nil {
		t.Fatal(err)
	}
	unmarked := aged(".staging-nomarker", nil)
	linkMarker := aged(".staging-linkmarker", func(dir string) error {
		return os.Symlink("elsewhere", filepath.Join(dir, stagingMarker))
	})

	removed, err := m.Gc(context.Background())
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("abandoned publication staging survived Gc (stat err = %v)", err)
	}
	for name, dir := range map[string]string{
		"young":      young,
		"unmarked":   unmarked,
		"linkMarker": linkMarker,
	} {
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s publication container was reclaimed: %v", name, err)
		}
	}
	for _, path := range removed {
		if strings.HasPrefix(path, m.publishingDir("", "")) || strings.Contains(path, string(filepath.Separator)+publishingDirName+string(filepath.Separator)) {
			t.Errorf("Gc reported a publication staging path as removed: %s", path)
		}
	}
}
