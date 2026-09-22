package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// TestChildRegistryKeepsDelegateWithAllowance verifies seam 3 (spec §1): the
// registry strip at child init is gated on delegationAllowance, not depth.
//
// Positive case: a child with delegationAllowance > 0 retains delegate and
// job_watch in its registry (today fails: strip runs on depth > 0).
//
// Negative case: a child with delegationAllowance == 0 still has delegate and
// job_watch stripped (preserves today's leaf behaviour).
func TestChildRegistryKeepsDelegateWithAllowance(t *testing.T) {
	t.Parallel()
	t.Run("allowance>0 retains delegate and job_watch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})

		cfg := SessionConfig{
			NoProjectPrompts: true,
			StateDir:         dir,
		}
		cfg.spawn.depth = 1
		cfg.spawn.parentSessionID = "parent-session"
		cfg.spawn.delegationAllowance = 1
		cfg.testOnly.metaFS = afero.NewMemMapFs()

		child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer child.Close()

		for _, toolName := range []string{"delegate", "job_watch"} {
			if child.reg.Get(toolName) == nil {
				t.Errorf("child with delegationAllowance=1: registry is missing %q (strip should not have run)", toolName)
			}
		}
	})

	t.Run("allowance==0 strips delegate but keeps job_watch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		c := llm.NewClient()
		c.Register(&fakeAdapter{name: "openai"})

		cfg := SessionConfig{
			NoProjectPrompts: true,
			StateDir:         dir,
		}
		cfg.spawn.depth = 1
		cfg.spawn.parentSessionID = "parent-session"
		cfg.spawn.delegationAllowance = 0 // leaf child — delegation strip must still run
		cfg.testOnly.metaFS = afero.NewMemMapFs()

		child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
		if err != nil {
			t.Fatalf("NewSession: %v", err)
		}
		defer child.Close()

		if child.reg.Get("delegate") != nil {
			t.Error("child with delegationAllowance=0: registry still contains delegate (strip should have run)")
		}
		if child.reg.Get("job_watch") == nil {
			t.Error("child with delegationAllowance=0: registry is missing job_watch — a session that can run jobs must be able to watch its own jobs")
		}
	})
}

// resolvedPath is EvalSymlinks over a fixture path that must exist, for
// building expectations that must match what the session records:
// canonicalStateDir resolves the state dir to its physical path, and on macOS
// t.TempDir() is reached through /var or /tmp symlinks, so a raw fixture path
// and the canonical spelling are two names for one file there, and only the
// resolved spelling matches. On Linux the two spellings are identical and
// this is a no-op, which is why CI never sees it (skillFixtureRoot documents
// the same trap for skill discovery).
func resolvedPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", path, err)
	}
	return resolved
}

// TestNewSessionCanonicalizesRelativeStateDir pins the path contract of a
// relative --state-dir: attachment paths recorded into transcripts name an
// absolute location, and the model's file tools resolve paths against their
// own working directory, so the session must anchor a relative state dir to
// the process working directory once, at construction — writes and announced
// paths then resolve identically no matter who reads them back. No
// t.Parallel: chdir is process-global.
func TestNewSessionCanonicalizesRelativeStateDir(t *testing.T) {
	work := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore Chdir: %v", err)
		}
	})

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: "state"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	want := filepath.Join(resolvedPath(t, work), "state")
	if sess.stateDir != want {
		t.Fatalf("session stateDir=%q, want %q (a relative StateDir must resolve against the process working directory at construction)", sess.stateDir, want)
	}
}

// TestNewSessionResolvesSymlinkedStateDirToPhysicalPath: a state dir reached
// through a symlink keeps that component in an Abs-only resolution, and the
// sandbox's file tools refuse symlinked ancestors on some hosts (macOS's
// /tmp, /var), which would turn an announced attachment path into a
// deterministic refusal. The anchored state dir must be the physical path.
func TestNewSessionResolvesSymlinkedStateDirToPhysicalPath(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	physical := filepath.Join(work, "real-state")
	if err := os.Mkdir(physical, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	link := filepath.Join(work, "link-state")
	if err := os.Symlink(physical, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{StateDir: link})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	if sess.stateDir != resolvedPath(t, physical) {
		t.Fatalf("session stateDir=%q, want the physical path %q (a symlinked StateDir must resolve at construction)", sess.stateDir, physical)
	}
}

// TestCanonicalStateDirResolvesSymlinkedAncestorOfMissingPath pins the
// not-yet-created half of the same contract: EvalSymlinks cannot resolve a
// path whose final components do not exist (a fresh --state-dir on first
// launch), and an Abs-only fallback would record the symlinked form — on the
// hosts whose file tools refuse symlinked ancestors, every attachment path
// announced from that session would be a deterministic read refusal. The
// deepest existing ancestor must resolve, with the missing tail joined back
// unchanged.
func TestCanonicalStateDirResolvesSymlinkedAncestorOfMissingPath(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	physical := filepath.Join(work, "real-state")
	if err := os.Mkdir(physical, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	link := filepath.Join(work, "link-state")
	if err := os.Symlink(physical, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	fresh := filepath.Join(link, "never", "created")
	want := filepath.Join(resolvedPath(t, physical), "never", "created")
	if got := canonicalStateDir(fresh); got != want {
		t.Fatalf("canonicalStateDir(%q) = %q, want %q (a symlinked ancestor must resolve even when the state dir does not exist yet)", fresh, got, want)
	}
	// The same branch without any symlink must keep the anchored absolute form.
	plain := filepath.Join(work, "plain-missing", "state")
	// On hosts whose temp root is itself a symlink (macOS /var →
	// /private/var), the anchored form resolves through it, so the want
	// must be built from the resolved work dir, not the lexical one.
	wantPlain := filepath.Join(resolvedPath(t, work), "plain-missing", "state")
	if got := canonicalStateDir(plain); got != wantPlain {
		t.Fatalf("canonicalStateDir(%q) = %q, want %q (the anchored absolute form)", plain, got, wantPlain)
	}
}

// TestCanonicalStateDirResolvesDotDotThroughSymlink pins the
// kernel-equivalent half of the same contract: filepath.Abs cleans `..`
// lexically, which is wrong across a symlink. `link/../state` addresses the
// physical parent of link's target, exactly as the kernel resolves it, not
// link's lexical parent. A state dir built that way must canonicalize to the
// physical path, so attachment paths announced from the session point where
// the kernel will actually look.
func TestCanonicalStateDirResolvesDotDotThroughSymlink(t *testing.T) {
	t.Parallel()
	work := t.TempDir()
	target := filepath.Join(work, "target", "sub")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	link := filepath.Join(work, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	// Raw string, not filepath.Join: Join would Clean the ".." away before
	// canonicalStateDir ever sees it, and the CLI delivers a raw
	// --state-dir value with its dots intact.
	sep := string(filepath.Separator)
	through := link + sep + ".." + sep + "state"
	want := filepath.Join(resolvedPath(t, filepath.Dir(target)), "state")
	if got := canonicalStateDir(through); got != want {
		t.Fatalf("canonicalStateDir(%q) = %q, want %q (.. must pop the symlink target's physical parent, not the lexical one)", through, got, want)
	}
	// Without a symlink in play the lexical and physical answers agree; the
	// plain form must keep resolving to the anchored path.
	plainDots := work + sep + "a" + sep + ".." + sep + "b"
	wantPlainDots := filepath.Join(resolvedPath(t, work), "b")
	if got := canonicalStateDir(plainDots); got != wantPlainDots {
		t.Fatalf("canonicalStateDir(%q) = %q, want %q (no-symlink .. keeps the anchored form)", plainDots, got, wantPlainDots)
	}
}

// TestCanonicalStateDirAnchorsRelativeStateDirThroughSymlinkedCwd pins the
// relative-input half of the same contract under a symlinked working
// directory: a shell that cd'd through a symlink hands the process a lexical
// PWD, which os.Getwd prefers whenever it matches ".". Anchoring a relative
// state dir onto that lexical form — or resolving it before anchoring — must
// not preserve the symlink: the session records the physical path its
// attachment reads will actually address. No t.Parallel: chdir and PWD are
// process-global.
func TestCanonicalStateDirAnchorsRelativeStateDirThroughSymlinkedCwd(t *testing.T) {
	work := t.TempDir()
	realWork := filepath.Join(work, "real-work")
	if err := os.MkdirAll(realWork, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	link := filepath.Join(work, "link-work")
	if err := os.Symlink(realWork, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	state := filepath.Join(realWork, "state")
	if err := os.Mkdir(state, 0o700); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(link); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Setenv("PWD", link)
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore Chdir: %v", err)
		}
	})

	if got := canonicalStateDir("state"); got != resolvedPath(t, state) {
		t.Fatalf("canonicalStateDir(%q) = %q, want %q (an existing relative state dir under a symlinked cwd must resolve to the physical path)", "state", got, state)
	}
	fresh := filepath.Join("fresh", "inner")
	wantFresh := filepath.Join(resolvedPath(t, realWork), "fresh", "inner")
	if got := canonicalStateDir(fresh); got != wantFresh {
		t.Fatalf("canonicalStateDir(%q) = %q, want %q (a missing relative state dir under a symlinked cwd must resolve to the physical path)", fresh, got, wantFresh)
	}
}

// TestRestoreSessionCanonicalizesRelativeStateDir pins the restore-side half
// of the same contract: `evener serve --resume` threads a --state-dir
// through RestoreSessionFromMetaWithConfig, and a restored session must carry
// the same absolute anchor the original session recorded, so state paths
// written before the restart and reads after it agree. The setup uses an
// absolute state dir, so only the restore path exercises the relative form.
// No t.Parallel: chdir is process-global.
func TestRestoreSessionCanonicalizesRelativeStateDir(t *testing.T) {
	work := t.TempDir()
	stateDir := filepath.Join(work, "state")
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(work), SessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	id := sess.ID()
	sess.Close()

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatalf("restore Chdir: %v", err)
		}
	})

	meta, err := schema.LoadSessionMeta("state", id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(work), meta, RestoreSessionConfig{StateDir: "state"})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if restored.stateDir != resolvedPath(t, stateDir) {
		t.Fatalf("restored stateDir=%q, want %q (a relative restore StateDir must resolve against the process working directory at construction)", restored.stateDir, stateDir)
	}
}

// TestLeafDelegateWatchesItsOwnJobsOnly: a session that can run jobs can watch
// them, at any depth — job_watch is not delegation, and stripping it left a
// leaf delegate unable to wait on its own background work. What stays closed
// is everything the root-only strip was actually protecting, which each watch
// SOURCE enforces itself: `parent` requires delegate(watch_parent=true), and a
// concrete job id must be owned by the watching session.
func TestLeafDelegateWatchesItsOwnJobsOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})

	cfg := SessionConfig{
		NoProjectPrompts: true,
		StateDir:         dir,
	}
	cfg.spawn.depth = 1
	cfg.spawn.parentSessionID = "parent-session"
	cfg.spawn.delegationAllowance = 0 // leaf: no watch_parent grant
	cfg.testOnly.metaFS = afero.NewMemMapFs()

	child, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer child.Close()

	shellRes := child.reg.ExecuteCall(context.Background(), child.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","mode":"background"}`),
	})
	if shellRes.IsError {
		t.Fatalf("leaf shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil || shellOut.JobID == "" {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	t.Cleanup(func() {
		_, _ = child.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, child.jobManager, shellOut.JobID)
	})

	ownRes := child.reg.ExecuteCall(context.Background(), child.env, llm.ToolCallData{
		ID:        "watch-own",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","source":"` + shellOut.JobID + `","progress_interval_ms":120000}`),
	})
	if ownRes.IsError {
		t.Fatalf("leaf delegate must be able to watch its own job, got error: %s", ownRes.Output)
	}

	parentRes := child.reg.ExecuteCall(context.Background(), child.env, llm.ToolCallData{
		ID:        "watch-parent",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","source":"parent","events":["communicate"]}`),
	})
	if !parentRes.IsError {
		t.Fatalf("leaf delegate without watch_parent must not watch its parent, got: %s", parentRes.Output)
	}
}
