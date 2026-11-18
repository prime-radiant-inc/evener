//go:build unix

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
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

// TestSharedEnvDelegateMetaRecordsTheScratchOwner: a delegate handed its
// parent's environment writes into that parent's scratch directory, so its
// meta must record the session the environment names as the directory's owner —
// the hub serves the delegate's scratch through that record.
func TestSharedEnvDelegateMetaRecordsTheScratchOwner(t *testing.T) {
	delegate := artifactRestoreMeta(t)
	delegate.IsSubagent = true
	rootID := artifactRestoreMeta(t).ID
	parent := artifactRestoreMeta(t).ID
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	env.SetScratchIdentity(rootID, parent)
	cfg := artifactRestoreConfig(t, t.TempDir())
	// The live spawn carrier is what makes this the IN-PROCESS restore: with a
	// parent session named, the restore leaves the handed environment's identity
	// alone instead of naming it from the meta.
	cfg.spawn.parentSessionID = parent
	sess, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), env, delegate, cfg)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	t.Cleanup(sess.Close)
	if got := sess.Meta().ScratchDirSessionID; got != parent {
		t.Errorf("shared-env delegate meta records scratch dir session %q, want the environment owner %q", got, parent)
	}
	// The in-process path hands the child the parent's own environment, so the
	// restore must leave that environment's identity exactly as the parent named
	// it: adopting the recorded owner belongs to a bare restore, not this one.
	if root, owner := env.ScratchIdentity(); root != rootID || owner != parent {
		t.Errorf("in-process delegate restore named the handed environment %s/%s, want the parent's own %s/%s", root, owner, rootID, parent)
	}
}

// TestBareRestoreNamesTheRecordedScratchOwner: a restore with no spawn carrier
// has only the meta to reconstruct a scratch it shared with its parent, so it
// must name the environment after the RECORDED owner — the session whose
// directory in the tree the scratch occupies — and not after the session itself:
// naming it after the session mints a second, empty directory beside the one its
// transcript names, and the first autosave rewrites the recorded owner as
// unowned.
//
// This is the restore constructor's contract, NOT a CLI capability: a bare
// `serve --resume` or `run --resume` of a delegate is refused before any restore
// (refuseBareDelegateResume in cmd/evener/resume_guard.go), and the daemon's own
// delegate restore always carries a live spawn parent. Every path that does reach
// this branch therefore arrives through the constructor, so the assertion is made
// at the seam all of them share.
func TestBareRestoreNamesTheRecordedScratchOwner(t *testing.T) {
	rootID := artifactRestoreMeta(t).ID
	ownerID := artifactRestoreMeta(t).ID
	delegate := artifactRestoreMeta(t)
	delegate.IsSubagent = true
	delegate.ScratchTreeRootID = rootID
	delegate.ScratchDirSessionID = ownerID
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	sess, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), env, delegate, artifactRestoreConfig(t, t.TempDir()))
	if err != nil {
		t.Fatalf("bare restore: %v", err)
	}
	t.Cleanup(sess.Close)
	if root, owner := env.ScratchIdentity(); root != rootID || owner != ownerID {
		t.Fatalf("bare restore named its scratch %s/%s, want the recorded %s/%s", root, owner, rootID, ownerID)
	}
	// A named scratch is kept at close and TestMain collects it; never remove a
	// directory reached through filepath.Dir, which is the shared temp root when
	// allocation fell back to a disposable scratch.
	scratch := mintRootScratch(t, sess)
	if got := sandbox.SessionScratchOwnerOf(scratch); got != ownerID {
		t.Errorf("restored delegate mints the directory of %q, want the recorded owner %q", got, ownerID)
	}
	if got := sandbox.SessionScratchTreeRootOf(scratch); got != rootID {
		t.Errorf("restored delegate mints in tree %q, want the recorded tree %q", got, rootID)
	}
	if got := sess.Meta().ScratchDirSessionID; got != ownerID {
		t.Errorf("meta after the bare restore records scratch dir session %q, want the recorded owner %q", got, ownerID)
	}
}

// TestBareRestoreNamesFromTheRecordNotTheCallersCopy: the meta a caller passes
// can predate the durable record — a daemon that recorded an owner after the
// caller read the file. The restore must name the environment from the record it
// reloads under session ownership, not from that stale copy, or it reopens the
// wrong directory and clears the owner it was supposed to honor. The reload is
// reached through RestoreSessionConfig's own seam: a StateDir plus
// AcquireSessionOwnership reloads the meta after ownership is taken.
func TestBareRestoreNamesFromTheRecordNotTheCallersCopy(t *testing.T) {
	stateDir := t.TempDir()
	rootID := artifactRestoreMeta(t).ID
	ownerID := artifactRestoreMeta(t).ID
	delegate := artifactRestoreMeta(t)
	delegate.IsSubagent = true
	// The caller's copy knows the tree but not the owner the record later gained.
	stale := delegate
	stale.ScratchTreeRootID = rootID
	recorded := delegate
	recorded.ScratchTreeRootID = rootID
	recorded.ScratchDirSessionID = ownerID
	if err := schema.SaveSessionMeta(stateDir, recorded); err != nil {
		t.Fatalf("save the durable record: %v", err)
	}
	cfg := artifactRestoreConfig(t, stateDir)
	cfg.AcquireSessionOwnership = func(string) error { return nil }
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	sess, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), env, stale, cfg)
	if err != nil {
		t.Fatalf("bare restore: %v", err)
	}
	t.Cleanup(sess.Close)
	if root, owner := env.ScratchIdentity(); root != rootID || owner != ownerID {
		t.Errorf("bare restore named its scratch %s/%s from the caller's copy, want the recorded %s/%s", root, owner, rootID, ownerID)
	}
	// The name must decide the DIRECTORY, not only the identity the test read
	// back: a named scratch keeps it at close and TestMain collects it, so no
	// cleanup removes a path reached through filepath.Dir.
	scratch := mintRootScratch(t, sess)
	if got := sandbox.SessionScratchOwnerOf(scratch); got != ownerID {
		t.Errorf("restored session mints the directory of %q, want the recorded owner %q", got, ownerID)
	}
	if got := sandbox.SessionScratchTreeRootOf(scratch); got != rootID {
		t.Errorf("restored session mints in tree %q, want the recorded tree %q", got, rootID)
	}
}

// TestSelfOwnedScratchMetaRecordsNoScratchOwner: a delegate resumed as the root
// of its tree names its environment after itself, and every session whose
// environment names it after itself owns its scratch; the meta records no
// owner beyond the session.
func TestSelfOwnedScratchMetaRecordsNoScratchOwner(t *testing.T) {
	meta := artifactRestoreMeta(t)
	rootID := artifactRestoreMeta(t).ID
	meta.IsSubagent = true
	meta.JobTreeRootSessionID = rootID
	sess, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, artifactRestoreConfig(t, t.TempDir()))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	t.Cleanup(sess.Close)
	if got := sess.Meta().ScratchDirSessionID; got != "" {
		t.Errorf("a session that owns its scratch records scratch dir session %q, want none", got)
	}
}

// TestMetaRecordsTheScratchTempDir: the hub removes an archived session's tree
// from its own temp dir, which a daemon started with another TMPDIR does not
// use. The meta records the daemon's temp dir so the hub looks there too.
func TestMetaRecordsTheScratchTempDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	root := newSession(t)
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
	root := newSession(t)
	if got, want := root.Meta().ScratchTempDir, filepath.Join(cwd, "reltmp"); got != want {
		t.Errorf("meta ScratchTempDir = %q, want the absolute %q", got, want)
	}
}

// TestMetaRecordsTheBaseItsScratchWasAllocatedIn: allocation refuses a temp
// base inside the session's workspace and lands the scratch in the user cache
// dir instead, so the meta records the base the scratch was actually
// allocated in. Recording the refused temp dir would send the hub to a
// retained old tree there and never to the session's current scratch.
func TestMetaRecordsTheBaseItsScratchWasAllocatedIn(t *testing.T) {
	// Not parallel: the redirects below steer this process's scratch bases —
	// HOME as well as XDG_CACHE_HOME, because macOS's user cache dir follows
	// HOME, not XDG — for the duration of the test.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cache, 0o700); err != nil {
		t.Fatal(err)
	}
	ws := t.TempDir()
	inside := filepath.Join(ws, "tmp")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", inside)
	root := newSession(t, withDir(ws))
	scratch := mintRootScratch(t, root)
	want := filepath.Dir(filepath.Dir(scratch))
	if got := root.Meta().ScratchTempDir; got != want {
		t.Errorf("meta ScratchTempDir = %q, want the allocated scratch base %q", got, want)
	}
}

// TestWorktreeDelegateMetaRecordsTheScratchOwner: a worktree-isolated delegate
// of a sandboxed parent re-roots its parent's kernel wrapper, which still
// carries the scratch minted for the parent — the delegate's commands write
// into that directory even though the clone's own mint identity names the
// child. Its meta must record the parent, the session that owns the directory,
// or the hub searches a directory the delegate never writes into.
func TestWorktreeDelegateMetaRecordsTheScratchOwner(t *testing.T) {
	delegate := artifactRestoreMeta(t)
	delegate.IsSubagent = true
	rootID := artifactRestoreMeta(t).ID
	parent := artifactRestoreMeta(t).ID
	cwd := t.TempDir()
	lane := t.TempDir()
	net := true
	rp, err := sandbox.Resolve(
		sandbox.SandboxPolicy{Mode: sandbox.ModeWorkspaceWrite, Network: &net},
		sandbox.HostFacts{OS: "linux", Home: t.TempDir(), BwrapPath: "/fixture/bwrap", BwrapCapable: true},
		cwd)
	if err != nil {
		t.Fatalf("resolve the parent's policy: %v", err)
	}
	env := execenv.NewLocalExecutionEnvironment(cwd)
	env.SetScratchIdentity(rootID, parent)
	if err := env.EnableSandbox(&rp); err != nil {
		t.Fatalf("enable the parent's sandbox: %v", err)
	}
	t.Cleanup(env.DisposeSandboxScratch)
	child := env.WithWorkingDirectory(lane)
	child.SetScratchIdentity(rootID, delegate.ID)
	if got, want := child.SessionScratchDir(), env.SessionScratchDir(); got == "" || got != want {
		t.Fatalf("the re-rooted clone writes into %q, want its parent's wrapper scratch %q", got, want)
	}
	sess, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), child, delegate, artifactRestoreConfig(t, t.TempDir()))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	t.Cleanup(sess.Close)
	if got := sess.Meta().ScratchDirSessionID; got != parent {
		t.Errorf("worktree delegate meta records scratch dir session %q, want the wrapper scratch's owner %q", got, parent)
	}
}

// TestChildOfAResumedDelegateRecordsTheScratchsTreeRoot: `serve --resume
// <delegate>` restores a delegate as the root of its children — a child
// spawned after the resume inherits the resumed delegate's id as its
// delegate-tree root — but the environment the child shares still writes into
// the ORIGINAL root's tree. The meta must record the tree the scratch actually
// occupies, or the hub composes one nobody wrote into and refuses the child's
// scratch documents.
func TestChildOfAResumedDelegateRecordsTheScratchsTreeRoot(t *testing.T) {
	stateDir := t.TempDir()
	rootID := artifactRestoreMeta(t).ID
	resumed := artifactRestoreMeta(t)
	resumed.IsSubagent = true
	resumed.ScratchTreeRootID = rootID
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	parent, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), env, resumed, artifactRestoreConfig(t, stateDir))
	if err != nil {
		t.Fatalf("restore the resumed delegate: %v", err)
	}
	t.Cleanup(parent.Close)
	mintRootScratch(t, parent)
	if _, session := env.ScratchIdentity(); session != resumed.ID {
		t.Fatalf("the resumed delegate's environment names %q, want the delegate %q", session, resumed.ID)
	}
	child := artifactRestoreMeta(t)
	child.IsSubagent = true
	cfg := artifactRestoreConfig(t, stateDir)
	cfg.spawn = spawnConfig{
		delegateController:    parent.delegateController,
		delegateRootSessionID: parent.delegateRootSessionID,
		parentSessionID:       parent.ID(),
	}
	sess, err := RestoreSessionFromMetaWithConfig(newArtifactTestClient(), NewOpenAIProfile("gpt-5.2"), env, child, cfg)
	if err != nil {
		t.Fatalf("restore the child: %v", err)
	}
	t.Cleanup(sess.Close)
	if got := sess.Meta().ScratchTreeRootID; got != rootID {
		t.Errorf("child of a resumed delegate records scratch tree %q, want the original root's tree %q", got, rootID)
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
	dir, workspace := t.TempDir(), t.TempDir()
	scratchBase := t.TempDir()
	t.Setenv("TMPDIR", scratchBase)
	adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response { return agenttest.FinalResponse("done") }}
	launchEnv := execenv.NewLocalExecutionEnvironment(workspace)
	cfg := SessionConfig{StateDir: dir}
	if err := PickFreshSessionID(launchEnv, &cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := launchEnv.ExecCommand(t.Context(), "true", 5000, workspace, nil); err != nil {
		t.Fatal(err)
	}
	root1 := newSession(t, withEnv(launchEnv), withConfig(cfg), withAdapter(adapter))
	scratch := mintRootScratch(t, root1)
	sendOneUserInput(t, root1, "start")
	prompt := adapter.Requests()[0].Messages[0].Text()
	if !strings.Contains(prompt, scratch) {
		t.Fatal("launch request did not name the installed scratch")
	}
	meta := root1.Meta()
	root1.Close()

	root2, err := RestoreSessionFromMetaWithConfig(root1.client, root1.profile,
		execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{StateDir: dir, testOnly: testConfig{skipGitSnapshot: true}})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	defer root2.Close()
	env2, ok := root2.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("restored env = %T, want a local environment", root2.currentEnv())
	}
	if got := env2.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratch) {
		t.Fatalf("scratch before the first restored command = %q, want %q", got, scratch)
	}
	if _, err := os.Stat(filepath.Join(scratch, "notes.md")); err != nil {
		t.Fatalf("the restored session's scratch lost its file: %v", err)
	}
	sendOneUserInput(t, root2, "continue")
	reqs := adapter.Requests()
	if updates := requestScratchUpdates(reqs[len(reqs)-1]); len(updates) != 0 {
		t.Fatalf("unchanged scratch was repeated: %v", updates)
	}
	root2.Close()

	header, entries, _, err := readTranscript(t.Context(), schema.SessionTranscriptPath(dir, meta.ID), dir)
	if err != nil {
		t.Fatal(err)
	}
	// Neither scratch allocation base may live inside the workspace.
	t.Setenv("TMPDIR", workspace)
	t.Setenv("HOME", workspace)
	t.Setenv("XDG_CACHE_HOME", workspace)
	reopen := func(t *testing.T, state string, previous *Session) *Session {
		t.Helper()
		checkpoint, err := schema.LoadSessionMeta(state, previous.ID())
		if err != nil {
			t.Fatal(err)
		}
		env := execenv.NewLocalExecutionEnvironment(workspace)
		resumed, err := RestoreSessionFromMetaWithConfig(previous.client, previous.profile, env, checkpoint, RestoreSessionConfig{StateDir: state, testOnly: testConfig{skipGitSnapshot: true}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(resumed.Close)
		return resumed
	}
	checkRecovery := func(t *testing.T, state string, before *Session, wantPath, wantPrompt string) {
		t.Helper()
		t.Setenv("TMPDIR", scratchBase)
		resumed := reopen(t, state, before)
		sendOneUserInput(t, resumed, "use the restored file")
		requests := adapter.Requests()
		req := requests[len(requests)-1]
		path := installedScratchDir(resumed.currentEnv())
		if path != wantPath {
			t.Errorf("scratch at the first resumed request = %q, want %q", path, wantPath)
		} else if b, err := os.ReadFile(filepath.Join(path, "notes.md")); err != nil || string(b) != "keep" {
			t.Errorf("reopened scratch file = %q, error %v", b, err)
		}
		updates := requestScratchUpdates(req)
		if len(updates) < 2 || updates[len(updates)-2] != "" || updates[len(updates)-1] != wantPath {
			t.Errorf("scratch updates after the directory recovered = %q, want a clear followed by %q", updates, wantPath)
		}
		if req.Messages[0].Text() != wantPrompt {
			t.Error("recovering scratch changed the saved system prompt")
		}
		resumed.Close()
	}
	for _, version := range []string{"recorded", "unrecorded"} {
		t.Run("unavailable_"+version, func(t *testing.T) {
			state := t.TempDir()
			saved := root2.Meta()
			if version == "unrecorded" {
				saved.PromptLaunch.ScratchDir = nil
			}
			writer, err := transcript.NewWriter(schema.SessionTranscriptPath(state, meta.ID), header)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if err := writer.Append(entry.Turn); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := schema.SaveSessionMeta(state, saved); err != nil {
				t.Fatal(err)
			}
			for round := range 2 {
				checkpoint, err := schema.LoadSessionMeta(state, meta.ID)
				if err != nil {
					t.Fatal(err)
				}
				launchScratch := checkpoint.PromptLaunch.ScratchDir
				if (launchScratch == nil) != (version == "unrecorded") || (launchScratch != nil && *launchScratch != scratch) {
					t.Fatalf("checkpoint %d changed the %s launch scratch path", round+1, version)
				}
				env := execenv.NewLocalExecutionEnvironment(workspace)
				restored, err := RestoreSessionFromMetaWithConfig(root1.client, root1.profile, env, checkpoint, RestoreSessionConfig{StateDir: state, testOnly: testConfig{skipGitSnapshot: true}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(restored.Close)
				if _, err := env.ExecCommand(t.Context(), "true", 5000, workspace, nil); err != nil || env.SessionScratchDir() != "" {
					t.Fatalf("unavailable scratch = %q, command error = %v", env.SessionScratchDir(), err)
				}
				sendOneUserInput(t, restored, "resume without scratch")
				requests := adapter.Requests()
				req := requests[len(requests)-1]
				if req.Messages[0].Text() != prompt {
					t.Fatal("resume changed the startup prompt")
				}
				if updates := requestScratchUpdates(req); !slices.Equal(updates, []string{""}) {
					t.Errorf("request %d scratch updates = %#v, want one explicit clear", round+1, updates)
				}
				restored.Close()
			}
			checkRecovery(t, state, root2, scratch, prompt)
		})
	}
	t.Run("unavailable_at_launch", func(t *testing.T) {
		state := t.TempDir()
		live := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: state}), withAdapter(adapter))
		for round := range 2 {
			checkpoint, err := schema.LoadSessionMeta(state, live.ID())
			if err != nil {
				t.Fatal(err)
			}
			if path := checkpoint.PromptLaunch.ScratchDir; path == nil || *path != "" {
				t.Fatalf("checkpoint %d lost the known empty launch scratch path", round+1)
			}
			if round > 0 {
				t.Setenv("TMPDIR", scratchBase)
				live, err = RestoreSessionFromMetaWithConfig(live.client, live.profile, execenv.NewLocalExecutionEnvironment(workspace), checkpoint, RestoreSessionConfig{StateDir: state, testOnly: testConfig{skipGitSnapshot: true}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(live.Close)
			}
			sendOneUserInput(t, live, "without scratch")
			requests := adapter.Requests()
			if updates := requestScratchUpdates(requests[len(requests)-1]); len(updates) != 0 {
				t.Errorf("request %d repeated a known empty scratch path: %#v", round+1, updates)
			}
			if round == 0 {
				live.Close()
			}
		}
		// The first command can provision scratch long after an empty launch.
		lateScratch := mintRootScratch(t, live)
		sendOneUserInput(t, live, "keep the scratch file")
		requests := adapter.Requests()
		latePrompt := requests[len(requests)-1].Messages[0].Text()
		if updates := requestScratchUpdates(requests[len(requests)-1]); !slices.Equal(updates, []string{lateScratch}) {
			t.Fatalf("scratch advertised after launch = %q, want %q", updates, lateScratch)
		}
		live.Close()
		t.Setenv("TMPDIR", workspace)
		missing := reopen(t, state, live)
		sendOneUserInput(t, missing, "continue while unavailable")
		missing.Close()
		checkRecovery(t, state, missing, lateScratch, latePrompt)
	})
}
