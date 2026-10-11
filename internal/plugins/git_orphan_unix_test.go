//go:build unix

package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/execsupport/orphanpipe/orphanpipetest"
)

// git()'s WaitDelay already bounds a cancelled git. Its other half is a git
// that succeeded while something it ran left a process on its output: a
// post-checkout hook from the user's global core.hooksPath that backgrounds a
// job runs during every clone, and git hands hooks its stderr. The clone
// completed, so the WaitDelay's ErrWaitDelay must not fail the install.
func TestGitCloneSurvivesAHookThatOrphansAPipeHolder(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src")
	sha := makeGitRepo(t, src, "plugin.txt", "hello")
	h := orphanpipetest.New(t)
	h.WriteScript(t, "post-checkout", h.Spawn()+"\n")
	// Environment-supplied config is how git takes a setting the way a user's
	// global config would, without touching the real one.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
	t.Setenv("GIT_CONFIG_VALUE_0", h.Dir())

	dst := filepath.Join(t.TempDir(), "dst")
	done := make(chan error, 1)
	go func() { done <- gitClone(context.Background(), src, dst, "", sha) }()
	if err := orphanpipetest.Await(t, h, done); err != nil {
		t.Fatalf("a clone that completed was reported as: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dst, "plugin.txt")); err != nil || string(b) != "hello" {
		t.Fatalf("cloned file = %q, err %v", b, err)
	}
}
