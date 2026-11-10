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

// TestCopyTreeRefusesSymlinkedGitNamespace covers the lexical half of the
// check: .git itself is a symlink to a directory outside the source, so the
// resolved config path has no .git component. The link's lexical target still
// names it, and those bytes must not be published as an ordinary file.
func TestCopyTreeRefusesSymlinkedGitNamespace(t *testing.T) {
	const sentinel = "nonsecret-credential-file-sentinel"
	src := t.TempDir()
	admin := t.TempDir()
	writeTestFile(t, filepath.Join(admin, "config"), "[core]\n# "+sentinel+"\n")
	if err := os.Symlink(admin, filepath.Join(src, ".git")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(src, "README.md"), "content")
	if err := os.Symlink(".git/config", filepath.Join(src, "companion.txt")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "published")
	if err := copyTree(src, dst); err == nil {
		t.Fatal("copyTree accepted a link whose lexical .git resolves outside the source")
	}
	data, err := os.ReadFile(filepath.Join(dst, "companion.txt"))
	if err == nil && strings.Contains(string(data), sentinel) {
		t.Fatal("companion.txt published the credential file's bytes")
	}
}

// TestCopyTreeOpensLinkPathWithKernelDotDotSemantics pins that the copy opens
// the link path rather than a lexically joined target: Join collapses ".."
// before the kernel resolves the intermediate alias symlink, which can name a
// different file.
func TestCopyTreeOpensLinkPathWithKernelDotDotSemantics(t *testing.T) {
	base := t.TempDir()
	plugin := filepath.Join(base, "plugin")
	if err := os.MkdirAll(plugin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "real", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(base, "guide.md"), "wrong")
	writeTestFile(t, filepath.Join(base, "real", "guide.md"), "right")
	if err := os.Symlink("real/sub", filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../alias/../guide.md", filepath.Join(plugin, "doc")); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(t.TempDir(), "published")
	if err := copyTree(plugin, dst); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dst, "doc"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "right" {
		t.Fatalf("doc = %q; want the file the kernel resolves through the alias", data)
	}
}
