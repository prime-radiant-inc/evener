package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDelegateArtifactsDir_LivesUnderDelegationState(t *testing.T) {
	t.Parallel()
	const stateDir = "state-root"
	got, err := delegateArtifactsDir(stateDir, "sess_child")
	if err != nil {
		t.Fatalf("delegateArtifactsDir: %v", err)
	}
	want := filepath.Join(stateDir, sessionsSubdir, "sess_child", delegateArtifactsSubdir)
	if got != want {
		t.Fatalf("delegateArtifactsDir = %q, want %q", got, want)
	}

	if _, err := delegateArtifactsDir("", "sess_child"); err == nil {
		t.Fatal("empty state dir accepted")
	}
	if _, err := delegateArtifactsDir(stateDir, "../escape"); err == nil {
		t.Fatal("invalid session id accepted")
	}
}

func TestAdvertiseArtifactsDir_OnlyWhenPresent(t *testing.T) {
	t.Parallel()
	if got := advertiseArtifactsDir(""); got != "" {
		t.Fatalf("advertiseArtifactsDir(\"\") = %q, want empty", got)
	}
	if got := advertiseArtifactsDir(filepath.Join(t.TempDir(), "missing")); got != "" {
		t.Fatalf("advertiseArtifactsDir(missing) = %q, want empty", got)
	}
	dir := t.TempDir()
	if got := advertiseArtifactsDir(dir); got != dir {
		t.Fatalf("advertiseArtifactsDir(existing) = %q, want %q", got, dir)
	}
	file := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if got := advertiseArtifactsDir(file); got != "" {
		t.Fatalf("advertiseArtifactsDir(regular file) = %q, want empty", got)
	}
}

// TestEnsureDelegateArtifactsDir_RefusesNonDirectoryCollisions pins that a
// planted regular file or symlink at either the session dir or the artifacts
// leaf is refused and left untouched, rather than followed or deleted.
func TestEnsureDelegateArtifactsDir_RefusesNonDirectoryCollisions(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessions := filepath.Join(stateDir, sessionsSubdir)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	// A regular file where the session dir belongs is refused and kept.
	sessionFile := filepath.Join(sessions, "sess_file")
	if err := os.WriteFile(sessionFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	if _, err := ensureDelegateArtifactsDir(stateDir, "sess_file"); err == nil {
		t.Fatal("regular file at session path accepted")
	}
	if _, err := os.Lstat(sessionFile); err != nil {
		t.Fatalf("session file removed by cleanup: %v", err)
	}

	// A regular file where the artifacts leaf belongs is refused and kept.
	child := "sess_leaf"
	if err := os.MkdirAll(filepath.Join(sessions, child), 0o700); err != nil {
		t.Fatalf("mkdir child: %v", err)
	}
	leafFile := filepath.Join(sessions, child, delegateArtifactsSubdir)
	if err := os.WriteFile(leafFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write leaf file: %v", err)
	}
	if _, err := ensureDelegateArtifactsDir(stateDir, child); err == nil {
		t.Fatal("regular file at artifacts leaf accepted")
	}
	if _, err := os.Lstat(leafFile); err != nil {
		t.Fatalf("leaf file removed by cleanup: %v", err)
	}

	// A symlink where the artifacts leaf belongs is refused, and both the link
	// and its target are preserved.
	linkChild := "sess_leaf_link"
	if err := os.MkdirAll(filepath.Join(sessions, linkChild), 0o700); err != nil {
		t.Fatalf("mkdir link child: %v", err)
	}
	leafTarget := t.TempDir()
	leafLink := filepath.Join(sessions, linkChild, delegateArtifactsSubdir)
	if err := os.Symlink(leafTarget, leafLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ensureDelegateArtifactsDir(stateDir, linkChild); err == nil {
		t.Fatal("symlinked artifacts leaf accepted")
	}
	if _, err := os.Lstat(leafLink); err != nil {
		t.Fatalf("leaf symlink removed by cleanup: %v", err)
	}
	if _, err := os.Lstat(leafTarget); err != nil {
		t.Fatalf("leaf symlink target removed by cleanup: %v", err)
	}

	// A symlinked session dir is refused and kept (skipped where symlinks need
	// privilege).
	link := filepath.Join(sessions, "sess_link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ensureDelegateArtifactsDir(stateDir, "sess_link"); err == nil {
		t.Fatal("symlinked session dir accepted")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("symlink removed by cleanup: %v", err)
	}
}

// TestCreateDelegate_CreationResultNamesArtifactsDir pins the issue's core
// contract: every delegation gets its own durable artifacts directory at
// creation, the delegate tool's creation result names it, a report written
// there is readable, and the existing session-artifact disposal removes it.
func TestCreateDelegate_CreationResultNamesArtifactsDir(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)

	out, err := stableDelegateCreateTool(context.Background(), root, map[string]any{
		"prompt":               "write your report into your artifacts directory",
		"delegation_allowance": 0,
	}, 30000)
	if err != nil {
		t.Fatalf("stableDelegateCreateTool: %v", err)
	}
	var result stableDelegateCreateResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal create result: %v\n%s", err, out)
	}
	if result.ChildSessionID == "" {
		t.Fatalf("create result has no child session id: %s", out)
	}
	want, err := delegateArtifactsDir(root.stateDir, result.ChildSessionID)
	if err != nil {
		t.Fatalf("delegateArtifactsDir: %v", err)
	}
	if result.ArtifactsDir != want {
		t.Fatalf("creation result artifacts_dir = %q, want %q", result.ArtifactsDir, want)
	}
	// It must sit under the delegation's own state, not a shared scratch base.
	if !strings.HasPrefix(result.ArtifactsDir, filepath.Join(root.stateDir, sessionsSubdir, result.ChildSessionID)+string(filepath.Separator)) {
		t.Fatalf("artifacts dir %q is not under the delegation's session state", result.ArtifactsDir)
	}
	if info, err := os.Stat(result.ArtifactsDir); err != nil || !info.IsDir() {
		t.Fatalf("artifacts dir was not created: err=%v", err)
	}

	// A report the delegate writes into its own dir is readable by the
	// controller, then absent after the delegation's state is disposed.
	report := filepath.Join(result.ArtifactsDir, "report.md")
	if err := os.WriteFile(report, []byte("findings"), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if got, err := os.ReadFile(report); err != nil || string(got) != "findings" {
		t.Fatalf("read report = %q err=%v, want findings", got, err)
	}

	if err := RemoveSessionArtifacts(root.stateDir, result.ChildSessionID); err != nil {
		t.Fatalf("RemoveSessionArtifacts: %v", err)
	}
	if _, err := os.Stat(result.ArtifactsDir); !os.IsNotExist(err) {
		t.Fatalf("artifacts dir survived session-artifact disposal: err=%v", err)
	}
}

// TestRemoveDelegateArtifacts_SymlinkedSessionDirRemovesLinkNotTarget pins that
// a symlink planted at the session dir is removed as a link, never followed
// into a target directory's artifacts tree.
func TestRemoveDelegateArtifacts_SymlinkedSessionDirRemovesLinkNotTarget(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	sessions := filepath.Join(stateDir, sessionsSubdir)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(target, delegateArtifactsSubdir), 0o700); err != nil {
		t.Fatalf("mkdir target artifacts: %v", err)
	}
	keep := filepath.Join(target, delegateArtifactsSubdir, "keep.md")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatalf("write target artifact: %v", err)
	}
	link := filepath.Join(sessions, "sess_link_rm")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if err := removeDelegateArtifacts(stateDir, "sess_link_rm"); err != nil {
		t.Fatalf("removeDelegateArtifacts: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("session symlink not removed: err=%v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("target artifacts deleted through the symlink: %v", err)
	}
}

// TestRemoveDelegateArtifacts_SymlinkedSessionsDirRefused pins the outermost
// guard: a symlink planted at the shared sessions dir is refused before any
// traversal, leaving both the link and the target's artifacts untouched.
func TestRemoveDelegateArtifacts_SymlinkedSessionsDirRefused(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	target := t.TempDir()
	child := "sess_nested"
	if err := os.MkdirAll(filepath.Join(target, child, delegateArtifactsSubdir), 0o700); err != nil {
		t.Fatalf("mkdir target tree: %v", err)
	}
	keep := filepath.Join(target, child, delegateArtifactsSubdir, "keep.md")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatalf("write target artifact: %v", err)
	}
	sessions := filepath.Join(stateDir, sessionsSubdir)
	if err := os.Symlink(target, sessions); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if err := removeDelegateArtifacts(stateDir, child); err == nil {
		t.Fatal("symlinked sessions dir accepted")
	}
	if _, err := os.Lstat(sessions); err != nil {
		t.Fatalf("sessions symlink removed: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("target artifact deleted through the sessions symlink: %v", err)
	}
}

// TestRemoveDelegateArtifacts_EmptyStateDirIsNoOp pins that disposing a child in
// a session with no durable state dir (a supported non-durable mode) reports no
// cleanup failure, since no artifacts directory was ever created.
func TestRemoveDelegateArtifacts_EmptyStateDirIsNoOp(t *testing.T) {
	t.Parallel()
	if err := removeDelegateArtifacts("", "sess_none"); err != nil {
		t.Fatalf("removeDelegateArtifacts with empty state dir = %v, want nil", err)
	}
	if err := removeDelegateArtifacts("   ", "sess_none"); err != nil {
		t.Fatalf("removeDelegateArtifacts with blank state dir = %v, want nil", err)
	}
}

// TestEnsureDelegateArtifactsDir_RefusesSymlinkedSessionsDir pins the create
// path's outermost guard: a symlink planted at the shared sessions dir is
// refused before any traversal, leaving both the link and the target's
// artifacts tree untouched.
func TestEnsureDelegateArtifactsDir_RefusesSymlinkedSessionsDir(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	target := t.TempDir()
	child := "sess_nested_create"
	if err := os.MkdirAll(filepath.Join(target, child, delegateArtifactsSubdir), 0o700); err != nil {
		t.Fatalf("mkdir target tree: %v", err)
	}
	keep := filepath.Join(target, child, delegateArtifactsSubdir, "keep.md")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatalf("write target artifact: %v", err)
	}
	sessions := filepath.Join(stateDir, sessionsSubdir)
	if err := os.Symlink(target, sessions); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	if _, err := ensureDelegateArtifactsDir(stateDir, child); err == nil {
		t.Fatal("symlinked sessions dir accepted")
	}
	if _, err := os.Lstat(sessions); err != nil {
		t.Fatalf("sessions symlink removed: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("target artifact deleted through the sessions symlink: %v", err)
	}
}

// TestDisposeUnadoptedSubagentSession_RemovesArtifactsDir pins that a delegate
// disposed before adoption takes its artifacts with it, via the existing
// unadopted-child disposal path.
func TestDisposeUnadoptedSubagentSession_RemovesArtifactsDir(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)
	_, _, _, prepared := prepareCommittedUnadoptedDelegate(t, root, "disposed before adoption")

	childID := prepared.sub.sess.id
	dir, err := ensureDelegateArtifactsDir(root.stateDir, childID)
	if err != nil {
		t.Fatalf("ensureDelegateArtifactsDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte("findings"), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}

	disposeUnadoptedSubagentSession(prepared.sub.sess)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("artifacts dir survived unadopted-child disposal: err=%v", err)
	}
}

// TestMarshalStableDelegateCreateResult_KeepsArtifactsDirWithinBound pins the
// bounded-result contract: artifacts_dir is the delegation's routing path, so
// the marshaler drops every supplementary field before it and keeps the path
// whenever the core can still fit.
func TestMarshalStableDelegateCreateResult_KeepsArtifactsDirWithinBound(t *testing.T) {
	t.Parallel()
	in := stableDelegateCreateResult{
		DelegateID:     "dlg_bounded",
		ChildSessionID: "sess_child",
		Type:           "delegate",
		Status:         "failed",
		TranscriptRef:  "local:sess_child",
		ArtifactsDir:   "/state/sessions/sess_child/artifacts",
		StartError:     strings.Repeat("oversized post-commit diagnostic ", 200),
		Warnings:       []string{strings.Repeat("w", 600)},
		Model:          "openai/gpt-5.2",
	}
	out, err := marshalStableDelegateCreateResult(in, jobToolResultMinJSONChars)
	if err != nil {
		t.Fatalf("marshalStableDelegateCreateResult: %v", err)
	}
	if got := jsonCharLen([]byte(out)); got > jobToolResultMinJSONChars {
		t.Fatalf("bounded result length = %d, want <= %d", got, jobToolResultMinJSONChars)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		t.Fatalf("decode bounded result %q: %v", out, err)
	}
	if _, ok := fields["artifacts_dir"]; !ok {
		t.Fatalf("bounded result dropped artifacts_dir: %s", out)
	}
	if _, ok := fields["error"]; ok {
		t.Fatalf("bounded result retained oversized error diagnostic: %s", out)
	}
}

// TestCreateDelegate_ConstructFailureRemovesArtifactsDir pins the leak fix: a
// start that dies during construction (before a child run exists to dispose)
// must take the just-created artifacts directory with it.
func TestCreateDelegate_ConstructFailureRemovesArtifactsDir(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point == "new_session" {
			return errors.New("injected construct failure")
		}
		return nil
	}
	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "fail during construction",
		DelegationAllowance: new(0),
	})
	if result.Err == nil {
		t.Fatalf("createDelegate err = nil, want construct failure")
	}
	if result.ChildSessionID == "" {
		t.Fatalf("construct failure returned no child session id: %#v", result)
	}
	if result.ArtifactsDir != "" {
		t.Fatalf("construct failure advertised a removed artifacts dir: %q", result.ArtifactsDir)
	}
	dir, err := delegateArtifactsDir(root.stateDir, result.ChildSessionID)
	if err != nil {
		t.Fatalf("delegateArtifactsDir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("artifacts dir leaked after construct failure: err=%v", err)
	}
	sessionDir, err := delegateSessionDir(root.stateDir, result.ChildSessionID)
	if err != nil {
		t.Fatalf("delegateSessionDir: %v", err)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("empty session dir residue after construct failure: err=%v", err)
	}
}
