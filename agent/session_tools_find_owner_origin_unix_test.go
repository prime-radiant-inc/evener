//go:build unix

package agent

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

// TestFind_ProjectName_OwnerOriginAnswersWithoutReadingTheRecord covers a deleted
// session-owned lane whose owner records a saved origin AND a checkout that is
// still on disk. The origin names the project and needs nothing from the
// filesystem, so it must answer without the owner's record being read: reading it
// walks the checkout, and a checkout on a stalled network mount would then hold
// the whole read-only search.
//
// The two paths are made to disagree so the test can tell them apart. The lane
// path records the checkout's own project ID, so the checkout — if it is read —
// is a candidate that positively names the lane's project and is preferred over
// an origin that records no project at all. The owner's origin names a different
// repository. An implementation that reads the record answers with the checkout's
// directory name, an implementation that returns the origin answers with the
// origin's repository, and the finding says the origin is what should answer.
func TestFind_ProjectName_OwnerOriginAnswersWithoutReadingTheRecord(t *testing.T) {
	stateDir := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	// The owner's checkout is live, so reading the record means walking it.
	checkout := newNamedCheckout(t, "checkout-name")

	ownerID := identifier.MustNewSessionID()
	owner := schema.SessionMeta{ID: ownerID}
	owner.EnvInfo.GitOriginURL = "git@github.com:owner/owner-origin-repo.git"
	owner.EnvInfo.WorkingDir = checkout
	if err := schema.SaveSessionMeta(stateDir, owner); err != nil {
		t.Fatal(err)
	}

	// The session-owned layout is the one that RECORDS an owner; a managed lane
	// carries its project in its path and consults no owner at all.
	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = filepath.Join(stateDir, "sessions", ownerID, "worktrees", "dlg_owner_origin")

	if got, want := projectName(row, stateDir), "owner-origin-repo"; got != want {
		t.Fatalf("projectName(deleted lane whose owner records an origin and a live checkout) = %q, want the owner's origin %q without reading the checkout", got, want)
	}
}

// TestFind_ProjectName_OwnerOriginDoesNotWalkItsCheckout is the same rule where
// the checkout cannot be resolved at all. The directory that used to enclose it
// holds a FIFO where .git was, a shape the structural walk refuses rather than
// opens, so a resolver that reads the record reaches a refusal and one that
// answers the origin first does not. This is a regression guard on the refusal's
// handling, not a proof of the ordering above.
func TestFind_ProjectName_OwnerOriginDoesNotWalkItsCheckout(t *testing.T) {
	stateDir := canonicalStateDir(t.TempDir())
	if err := os.MkdirAll(filepath.Join(stateDir, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}

	enclosing := t.TempDir()
	gitEntry := filepath.Join(enclosing, ".git")
	if err := syscall.Mkfifo(gitEntry, 0o644); err != nil {
		t.Fatalf("mkfifo %s: %v", gitEntry, err)
	}

	ownerID := identifier.MustNewSessionID()
	owner := schema.SessionMeta{ID: ownerID}
	owner.EnvInfo.GitOriginURL = "git@github.com:owner/owner-origin-repo.git"
	owner.EnvInfo.WorkingDir = filepath.Join(enclosing, "gone-owner-checkout")
	if err := schema.SaveSessionMeta(stateDir, owner); err != nil {
		t.Fatal(err)
	}

	row := schema.SessionMeta{}
	row.EnvInfo.WorkingDir = filepath.Join(stateDir, "sessions", ownerID, "worktrees", "dlg_owner_origin")

	// The path above is inside a state dir, and a FIFO there must never be opened
	// by a label lookup: an unbounded open would block this test forever.
	if got, want := projectName(row, stateDir), "owner-origin-repo"; got != want {
		t.Fatalf("projectName(deleted session-owned lane whose owner records an origin above a refused .git entry) = %q, want the owner's origin %q", got, want)
	}
}
