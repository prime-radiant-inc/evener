//go:build unix

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestRootCloseRemovesItsScratch: a root's own scratch does not outlive close.
func TestRootCloseRemovesItsScratch(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	local, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("root env = %T, want a local environment", root.currentEnv())
	}
	if _, err := local.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatal(err)
	}
	scratch := local.SessionScratchDir()
	if scratch == "" {
		t.Fatal("no scratch minted")
	}
	root.Close()
	if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
		t.Fatalf("root close left its scratch %s: %v", scratch, err)
	}
}

// TestRootCloseSweepsOldScratch: a long-running daemon reclaims old scratch as
// root sessions end, without waiting for a restart.
func TestRootCloseSweepsOldScratch(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	old := filepath.Join(base, "evener-sandbox-424242")
	if err := os.Mkdir(old, 0o700); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	root := newQueuePersistTestSession(t, t.TempDir())
	root.Close()
	// The sweep runs off the close path, so wait for it rather than expect it
	// to have finished when Close returns.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Lstat(old); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("root close did not sweep %s within 10s", old)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRootCloseDoesNotWaitForTheSweep: the sweep can take a long time on a big
// temp base, so close starts it and returns.
func TestRootCloseDoesNotWaitForTheSweep(t *testing.T) {
	base := t.TempDir()
	t.Setenv("TMPDIR", base)
	release := make(chan struct{})
	started := make(chan struct{})
	t.Cleanup(func() { close(release) })
	root := newQueuePersistTestSession(t, t.TempDir())
	root.cfg.testOnly.scratchSweep = func(string) error {
		close(started)
		<-release
		return nil
	}
	done := make(chan struct{})
	go func() { root.Close(); close(done) }()
	select {
	case <-done:
	// TRIPWIRE: close returns in well under a second here; the sweep is held
	// open forever, so a close that waits for it never returns at all, and this
	// bound only turns that hang into a failure.
	case <-time.After(10 * time.Second):
		t.Fatal("root close waited for the scratch sweep")
	}
	select {
	case <-started:
	// TRIPWIRE: the sweep goroutine starts within milliseconds of close; this
	// bound only turns a close that never starts it into a failure, not a hang.
	case <-time.After(10 * time.Second):
		t.Fatal("root close never started the scratch sweep")
	}
}

// sessionEndProbePluginDir writes a plugin whose SessionEnd hook touches marker
// only if the directory named in pathFile still exists when the hook runs.
// Modeled on newResumeHookPluginDir (session_resume_hooks_test.go).
func sessionEndProbePluginDir(t *testing.T, pathFile, marker string) string {
	t.Helper()
	pluginDir := filepath.Join(t.TempDir(), "session-end-probe")
	metaDir := filepath.Join(pluginDir, ".claude-plugin")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("test -d \"$(cat %q)\" && touch %q", pathFile, marker)
	manifest := fmt.Sprintf(`{
  "name": "session-end-probe",
  "version": "0.0.1",
  "hooks": {
    "SessionEnd": [
      {"hooks": [{"type": "command", "command": %q, "timeout": 5}]}
    ]
  }
}`, command)
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return pluginDir
}

// TestSessionEndHookRunsBeforeScratchIsRemoved: SessionEnd hooks and MCP servers
// run with TMPDIR inside the scratch, so close removes it last.
func TestSessionEndHookRunsBeforeScratchIsRemoved(t *testing.T) {
	work := t.TempDir()
	pathFile := filepath.Join(work, "scratch-path")
	marker := filepath.Join(work, "hook-saw-scratch")
	meta := schema.SessionMeta{
		ID:        "01KSESSIONENDPROBE000000000",
		ProfileID: "test",
		Model:     "gpt-5.2",
		Config:    schema.ConfigSnapshot{PluginDirs: []string{sessionEndProbePluginDir(t, pathFile, marker)}},
		TurnCount: 1,
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	sess, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), env, meta,
		RestoreSessionConfig{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	local, ok := sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("session env = %T, want a local environment", sess.currentEnv())
	}
	if _, err := local.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatal(err)
	}
	scratch := local.SessionScratchDir()
	if scratch == "" {
		t.Fatal("no scratch minted")
	}
	if err := os.WriteFile(pathFile, []byte(scratch), 0o600); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the SessionEnd hook did not see the scratch %s still present: %v", scratch, err)
	}
	if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
		t.Fatalf("close left the scratch %s: %v", scratch, err)
	}
}

// TestResumeAfterCloseGetsAFreshScratch: a session restored after its scratch was
// removed runs with a new one.
func TestResumeAfterCloseGetsAFreshScratch(t *testing.T) {
	dir := t.TempDir()
	root1 := newQueuePersistTestSession(t, dir)
	env1, ok := root1.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("root env = %T, want a local environment", root1.currentEnv())
	}
	if _, err := env1.ExecCommand(context.Background(), "true", 5000, dir, nil); err != nil {
		t.Fatal(err)
	}
	old := env1.SessionScratchDir()
	meta := root1.Meta()
	root1.Close()
	if _, err := os.Lstat(old); !os.IsNotExist(err) {
		t.Fatalf("close left the scratch %s: %v", old, err)
	}

	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	root2, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	defer root2.Close()
	env2, ok := root2.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("restored env = %T, want a local environment", root2.currentEnv())
	}
	if _, err := env2.ExecCommand(context.Background(), "true", 5000, dir, nil); err != nil {
		t.Fatalf("restored session cannot run a command: %v", err)
	}
	fresh := env2.SessionScratchDir()
	if fresh == "" || filepath.Clean(fresh) == filepath.Clean(old) {
		t.Fatalf("restored scratch = %q, want a fresh directory, not %q", fresh, old)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("restored scratch %s missing: %v", fresh, err)
	}
}
