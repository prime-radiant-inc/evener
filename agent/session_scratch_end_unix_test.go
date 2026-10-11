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

// TestOneShotRootCloseKeepsItsScratch: a one-shot run's exit is an ordinary
// end. Its scratch is kept, like any session's, until the hub archives or
// deletes the session.
func TestOneShotRootCloseKeepsItsScratch(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	root, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir, TurnEndsProcess: true})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	scratch := mintRootScratch(t, root)
	root.Close()
	assertScratchSettledAtEnd(t, "one-shot root", scratch)
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

// TestSessionEndHookRunsBeforeScratchIsEnded: SessionEnd hooks and MCP servers
// run with TMPDIR inside the scratch, so close ends it last.
func TestSessionEndHookRunsBeforeScratchIsEnded(t *testing.T) {
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
	assertScratchSettledAtEnd(t, "restored root", scratch)
}

// TestDelegateRestoreLeavesTheHandedEnvironmentUnnamed: a delegate restore can
// be handed its parent's own environment (a shared delegate), so naming that
// environment after the delegate would put the parent's next scratch in a tree
// no archive of the root ever removes. Only a root restore names it.
func TestDelegateRestoreLeavesTheHandedEnvironmentUnnamed(t *testing.T) {
	meta := artifactRestoreMeta(t)
	meta.IsSubagent = true
	cfg := artifactRestoreConfig(t, t.TempDir())
	cfg.spawn.parentSessionID = artifactRestoreMeta(t).ID
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if sess, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), env, meta, cfg); err == nil {
		t.Cleanup(sess.Close)
	}
	if root, session := env.ScratchIdentity(); root != "" {
		t.Errorf("a delegate restore named the environment it was handed (%s/%s)", root, session)
	}
}

// TestMetaRecordsTheScratchTempDir: the hub removes an archived session's tree
// from its own temp dir, which a daemon started with another TMPDIR does not
// use. The meta records the daemon's temp dir so the hub looks there too.
func TestMetaRecordsTheScratchTempDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	root := newSession(t, withoutGitSnapshot())
	if got := root.Meta().ScratchTempDir; got != os.TempDir() {
		t.Errorf("meta ScratchTempDir = %q, want this process's temp dir %q", got, os.TempDir())
	}
}

// A relative TMPDIR means nothing to the hub, which runs in another working
// directory, so the meta records it resolved against the daemon's.
func TestMetaRecordsARelativeTempDirAsAbsolute(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.Mkdir(filepath.Join(cwd, "reltmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", "reltmp")
	root := newSession(t, withoutGitSnapshot())
	if got, want := root.Meta().ScratchTempDir, filepath.Join(cwd, "reltmp"); got != want {
		t.Errorf("meta ScratchTempDir = %q, want the absolute %q", got, want)
	}
}

// TestDelegateResumedAsRootReopensItsScratchInItsRootsTree: `serve --resume
// <delegate>` restores a delegate as a top-level session. Its scratch is still
// its directory in its root's tree, so it reopens what it had and goes when the
// root is archived, instead of opening a tree of its own nobody archives.
func TestDelegateResumedAsRootReopensItsScratchInItsRootsTree(t *testing.T) {
	meta := artifactRestoreMeta(t)
	rootID := artifactRestoreMeta(t).ID
	meta.IsSubagent = true
	meta.JobTreeRootSessionID = rootID
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if sess, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), env, meta, artifactRestoreConfig(t, t.TempDir())); err == nil {
		t.Cleanup(sess.Close)
	}
	if root, session := env.ScratchIdentity(); root != rootID || session != meta.ID {
		t.Errorf("delegate resumed as a root named its scratch %s/%s, want %s/%s", root, session, rootID, meta.ID)
	}
}

// TestDelegateResumedAsRootKeepsItsTreeAcrossResumes: a delegate resumed as a
// root re-roots its job clock at its own ID, so the job tree root no longer
// names the spawning root. Its meta records the scratch tree root itself, and
// a second resume from that meta reopens the same directory.
func TestDelegateResumedAsRootKeepsItsTreeAcrossResumes(t *testing.T) {
	meta := artifactRestoreMeta(t)
	rootID := artifactRestoreMeta(t).ID
	meta.IsSubagent = true
	meta.JobTreeRootSessionID = rootID
	first, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, artifactRestoreConfig(t, t.TempDir()))
	if err != nil {
		t.Fatalf("first restore: %v", err)
	}
	saved := first.Meta()
	first.Close()
	if saved.ScratchTreeRootID != rootID {
		t.Fatalf("meta after the first resume records scratch tree root %q, want %q", saved.ScratchTreeRootID, rootID)
	}

	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if second, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), env, saved, artifactRestoreConfig(t, t.TempDir())); err == nil {
		t.Cleanup(second.Close)
	}
	if root, _ := env.ScratchIdentity(); root != rootID {
		t.Errorf("second resume named its scratch tree %q, want %q", root, rootID)
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
