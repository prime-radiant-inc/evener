//go:build linux

package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCopyTreeRefusesLinkRetargetedBetweenCheckAndOpen puts the retarget in the
// exact window between reading the source link and opening its target. The
// link still names safe.txt, but safe.txt has become a link into .git/config;
// the opened descriptor resolves there, so the copy must refuse rather than
// publish those bytes under the companion file's name.
func TestCopyTreeRefusesLinkRetargetedBetweenCheckAndOpen(t *testing.T) {
	const sentinel = "nonsecret-retargeted-git-sentinel"
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, ".git", "config"), sentinel)
	safe := filepath.Join(src, "safe.txt")
	writeTestFile(t, safe, "safe")
	link := filepath.Join(src, "companion.txt")
	if err := os.Symlink("safe.txt", link); err != nil {
		t.Fatal(err)
	}

	previous := pluginSourceBeforeLinkOpen
	pluginSourceBeforeLinkOpen = func(path string) {
		if path != link {
			return
		}
		if err := os.Remove(safe); err != nil {
			t.Fatalf("retarget target: %v", err)
		}
		if err := os.Symlink(".git/config", safe); err != nil {
			t.Fatalf("retarget target: %v", err)
		}
	}
	t.Cleanup(func() { pluginSourceBeforeLinkOpen = previous })

	dst := filepath.Join(t.TempDir(), "published")
	if err := copyTree(src, dst); err == nil {
		t.Fatal("copyTree accepted a source link retargeted to .git/config")
	}
	for _, name := range []string{"companion.txt", "safe.txt"} {
		data, err := os.ReadFile(filepath.Join(dst, name))
		if err == nil && strings.Contains(string(data), sentinel) {
			t.Fatalf("%s published Git metadata after the retarget", name)
		}
	}
}
