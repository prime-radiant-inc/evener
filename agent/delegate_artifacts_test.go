package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDelegateArtifactsDir_LivesUnderDelegationState(t *testing.T) {
	t.Parallel()
	got, err := delegateArtifactsDir("/state", "sess_child")
	if err != nil {
		t.Fatalf("delegateArtifactsDir: %v", err)
	}
	want := filepath.Join("/state", sessionsSubdir, "sess_child", delegateArtifactsSubdir)
	if got != want {
		t.Fatalf("delegateArtifactsDir = %q, want %q", got, want)
	}

	if _, err := delegateArtifactsDir("", "sess_child"); err == nil {
		t.Fatal("empty state dir accepted")
	}
	if _, err := delegateArtifactsDir("/state", "../escape"); err == nil {
		t.Fatal("invalid session id accepted")
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
