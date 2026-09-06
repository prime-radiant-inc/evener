//go:build linux || darwin

package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// TestLoadIgnoreSetSkipsMaskedSubtree: loadIgnoreSet, driven with the same
// secureDirFS and masking skip predicate sandboxFS.glob/grepNative wire it
// with, never descends into (and so never reads a .gitignore from) a masked
// subtree — proving I3: prior to this fix, loadIgnoreSet had no way to skip
// masked paths at all, so a sandboxed session's glob/grep would read
// .gitignore files inside a policy-masked directory (e.g. ~/.ssh, an AWS
// credentials dir). A non-masked .gitignore elsewhere under the same base
// must still be picked up, so this also proves the fix doesn't over-skip.
func TestLoadIgnoreSetSkipsMaskedSubtree(t *testing.T) {
	t.Parallel()
	env, home, _ := sandboxedEnvWithDenylist(t, sandbox.ModeReadOnly, filepath.Join("~", "vault"))
	vault := filepath.Join(home, "vault")
	if err := os.MkdirAll(vault, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, ".gitignore"), []byte("*.secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sfs := env.sandbox()
	if sfs == nil {
		t.Fatal("expected a sandboxed environment")
	}
	// sandbox() hands back a held layer; the ignore-set work below runs on it,
	// so the release goes at the end of the operation, not here.
	defer sfs.release()
	baseFd, canonical, err := sfs.openReadBaseFd("glob", home)
	if err != nil {
		t.Fatalf("openReadBaseFd: %v", err)
	}
	defer func() { _ = unix.Close(baseFd) }()

	fsys := &secureDirFS{baseFd: baseFd, basePath: canonical, fs: sfs}
	set, err := loadIgnoreSet(fsys, func(relPath string) bool {
		return sfs.underMasked(filepath.Join(canonical, relPath))
	}, &globBudget{})
	if err != nil {
		t.Fatal(err)
	}

	for _, d := range set.dirs {
		if d.rel == "vault" || strings.HasPrefix(d.rel, "vault/") {
			t.Errorf("loadIgnoreSet read a .gitignore inside the masked vault subtree: rel=%q", d.rel)
		}
	}
	found := false
	for _, d := range set.dirs {
		if d.rel == "." {
			found = true
		}
	}
	if !found {
		t.Error("loadIgnoreSet should still load the non-masked base .gitignore")
	}
}
