//go:build linux || darwin

package execenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
}
