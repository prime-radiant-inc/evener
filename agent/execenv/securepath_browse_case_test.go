//go:build linux || darwin

package execenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"primeradiant.com/evener/agent/sandbox"
)

// TestBrowseJudgesDescendantsAgainstTheOpenedDirectorysSpelling pins the mask
// judgement for everything reached through a browse base. A caller can name a
// directory in another letter case than the filesystem stores (a
// case-insensitive APFS or Linux volume), and descendants built from the
// caller's spelling would match none of a mask listed in the stored spelling.
// The base is opened through the policy, so the kernel's own path for that fd
// is the spelling every descendant must be judged in. The test skips on a
// case-sensitive volume, where no spelling can differ.
func TestBrowseJudgesDescendantsAgainstTheOpenedDirectorysSpelling(t *testing.T) {
	t.Parallel()
	parent := realTempDir(t)
	storedConfig := filepath.Join(parent, "casehome", ".config", "evener")
	if err := os.MkdirAll(storedConfig, 0o755); err != nil {
		t.Fatal(err)
	}
	const secret = "EVENER-HIDDEN-SECRET"
	if err := os.WriteFile(filepath.Join(storedConfig, "secret.txt"), []byte(secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spelled := filepath.Join(parent, "CaseHome", ".Config")
	if _, err := os.Stat(spelled); err != nil {
		t.Skip("this volume is case-sensitive")
	}

	rp := &sandbox.ResolvedPolicy{
		Mode:        sandbox.ModeReadOnly,
		FileTool:    sandbox.AccessScope{Read: sandbox.ReadAnywhere},
		MaskedPaths: []string{storedConfig},
	}
	s := newSandboxFS(rp, "")
	t.Cleanup(s.close)

	entries, err := s.listDir("list_dir", spelled, 2, false)
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	for _, e := range entries {
		if e.Name == "evener" {
			t.Errorf("a masked subtree was listed through a case-variant parent spelling: %+v", entries)
		}
	}

	matches, _, err := s.glob(context.Background(), "glob", spelled, "**/secret.txt", false, newGlobBudget("glob"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("a masked file was returned through a case-variant base: %v", matches)
	}

	out, err := s.grepNative(context.Background(), secret, spelled, "", false, 10, "content", 0, nil)
	if err != nil {
		t.Fatalf("grepNative: %v", err)
	}
	if strings.Contains(out, secret) {
		t.Errorf("a masked subtree was searched through a case-variant base:\n%s", out)
	}

	// A base inside the mask, named in another case, is still the masked
	// directory: the open rechecks the fd's canonical path.
	maskedBase := filepath.Join(parent, "CaseHome", ".Config", "EVENER")
	if _, err := s.listDir("list_dir", maskedBase, 1, false); err == nil {
		t.Errorf("a browse base inside the mask must be denied, however it is spelled")
	}
}

// TestBrowseBasePathUsesTheOpenedDirectorysKernelPath pins the spelling seam
// itself on every platform: the path handed to the walk is the directory the
// descriptor refers to, not the spelled path used to open it. It runs on a
// case-sensitive volume too, where the difference is a symlinked ancestor.
func TestBrowseBasePathUsesTheOpenedDirectorysKernelPath(t *testing.T) {
	t.Parallel()
	base := realTempDir(t)
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	fd, err := unix.Open(link, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatalf("open %s: %v", link, err)
	}
	defer func() { _ = unix.Close(fd) }()
	if got := browseBasePath(fd, link); got != target {
		t.Errorf("browseBasePath(%q) = %q, want the opened directory %q", link, got, target)
	}
}
