//go:build unix

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenJobOutputFileRefusesSymlinkedBucketRoot proves the projects-dir root
// keeps the project bucket a no-followed walked component: a bucket directory
// swapped for a symlink after the locate is refused at open, not followed.
// Rooting the walk at the bucket itself would open the symlink as the walk
// root and read through it.
//
// Unix-only: the refusal is the descriptor walk's openat(O_NOFOLLOW) on the
// first walked component. The portable fallback has no openat and only refuses
// a symlinked root, so it follows a symlinked bucket and this invariant does not
// hold there — hence the build tag rather than a runtime skip.
func TestOpenJobOutputFileRefusesSymlinkedBucketRoot(t *testing.T) {
	t.Parallel()
	// The honest bucket lives under a foreign state home; the layout bucket
	// name is a symlink to it.
	attackerBucket := localJobProjectBucket(t, t.TempDir(), localJobCurrentProject)
	outputPath := filepath.Join(attackerBucket, "sessions", "owner", "jobs", "output.log")
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		t.Fatalf("create attacker output dir: %v", err)
	}
	if err := os.WriteFile(outputPath, []byte("must not read\n"), 0o600); err != nil {
		t.Fatalf("write attacker output: %v", err)
	}

	stateHome := t.TempDir()
	layoutBucket := filepath.Join(stateHome, "evener", "projects", localJobCurrentProject)
	if err := os.MkdirAll(filepath.Dir(layoutBucket), 0o700); err != nil {
		t.Fatalf("create projects dir: %v", err)
	}
	if err := os.Symlink(attackerBucket, layoutBucket); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	symlinkedOutput := filepath.Join(layoutBucket, "sessions", "owner", "jobs", "output.log")

	f, err := openJobOutputFile(symlinkedOutput)
	if f != nil {
		_ = f.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("openJobOutputFile error = %v, want symlinked bucket refusal", err)
	}
}
