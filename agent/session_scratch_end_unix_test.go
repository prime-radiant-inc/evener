//go:build unix

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// mintRootScratch runs a command on sess's environment so it mints its
// scratch, writes a file and a cache dir there, and returns the scratch path.
func mintRootScratch(t *testing.T, sess *Session) string {
	t.Helper()
	local, ok := sess.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("env = %T, want a local environment", sess.currentEnv())
	}
	if _, err := local.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatal(err)
	}
	scratch := local.SessionScratchDir()
	if scratch == "" {
		t.Fatal("no scratch minted")
	}
	if err := os.WriteFile(filepath.Join(scratch, "notes.md"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(scratch, "gocache", "ab"), 0o700); err != nil {
		t.Fatal(err)
	}
	return scratch
}

// TestRootCloseKeepsItsScratchAndPrunesCaches: a daemon session's scratch
// outlives close — it goes when the hub archives or deletes the session — and
// only its regenerable caches are pruned.
func TestRootCloseKeepsItsScratchAndPrunesCaches(t *testing.T) {
	root := newQueuePersistTestSession(t, t.TempDir())
	scratch := mintRootScratch(t, root)
	if want := "evener-scratch-" + root.ID(); filepath.Base(filepath.Dir(scratch)) != want {
		t.Fatalf("root scratch %s is not in its tree %s", scratch, want)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(scratch)) })
	root.Close()
	if _, err := os.Stat(filepath.Join(scratch, "notes.md")); err != nil {
		t.Fatalf("root close removed its scratch's file: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(scratch, "gocache")); !os.IsNotExist(err) {
		t.Fatalf("root close kept the cache: %v", err)
	}
}

// TestOneShotRootCloseRemovesItsTree: a one-shot run has no archive to wait
// for, so its root removes the whole scratch tree when it exits.
func TestOneShotRootCloseRemovesItsTree(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	root, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, TurnEndsProcess: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	scratch := mintRootScratch(t, root)
	tree := filepath.Dir(scratch)
	root.Close()
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Fatalf("a one-shot root left its scratch tree %s: %v", tree, err)
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
// run with TMPDIR inside the scratch, so a one-shot run's close removes it last.
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
		RestoreSessionConfig{StateDir: t.TempDir(), TurnEndsProcess: true})
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

// TestResumeReopensTheSameScratch: a session's scratch outlives close, and the
// restored session reopens the same directory, files and all.
func TestResumeReopensTheSameScratch(t *testing.T) {
	dir := t.TempDir()
	root1 := newQueuePersistTestSession(t, dir)
	scratch := mintRootScratch(t, root1)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(scratch)) })
	meta := root1.Meta()
	root1.Close()

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
	if got := env2.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratch) {
		t.Fatalf("restored scratch = %q, want the same %q", got, scratch)
	}
	if _, err := os.Stat(filepath.Join(scratch, "notes.md")); err != nil {
		t.Fatalf("the restored session's scratch lost its file: %v", err)
	}
}
