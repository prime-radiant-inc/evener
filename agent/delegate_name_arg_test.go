package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/worktree"
)

// The delegate tool's optional `name` argument names the git branch of a
// worktree-isolated delegate's lane, so `git branch` and merges read
// mnemonically while the lane directory, sidecar, and every addressing surface
// stay keyed to the delegate id (the delegate-lane branch-names design). The
// decode test is pure over the args map; the spawn tests run on the real-git
// harness because a lane cut on a branch that differs from its directory is a
// `worktree add -b` registry effect, and the rollback must really delete it.

func TestDecodeDelegateArgs_Name(t *testing.T) {
	// Absent name stays empty: the lane branch defaults to the delegate id.
	absent, err := decodeDelegateArgs(map[string]any{"prompt": "p"})
	if err != nil {
		t.Fatal(err)
	}
	if absent.Name != "" {
		t.Fatalf("absent name decoded as %q, want empty", absent.Name)
	}

	named, err := decodeDelegateArgs(map[string]any{"prompt": "p", "isolation": "worktree", "name": "parser-rename"})
	if err != nil {
		t.Fatal(err)
	}
	if named.Name != "parser-rename" {
		t.Fatalf("name decoded as %q, want parser-rename", named.Name)
	}

	// A slash is legal in a branch name and must survive decode untouched;
	// consumers resolve it from the sidecar record, never by reconstruction.
	slashed, err := decodeDelegateArgs(map[string]any{"prompt": "p", "isolation": "worktree", "name": "feat/parser"})
	if err != nil {
		t.Fatal(err)
	}
	if slashed.Name != "feat/parser" {
		t.Fatalf("slashed name decoded as %q, want feat/parser", slashed.Name)
	}

	// A name without worktree isolation has nothing to name.
	if _, err := decodeDelegateArgs(map[string]any{"prompt": "p", "name": "parser-rename"}); err == nil || !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("name without isolation: err = %v, want invalid_request", err)
	}

	// The same alphabet manage_worktree names use, checked before any capacity
	// is reserved.
	if _, err := decodeDelegateArgs(map[string]any{"prompt": "p", "isolation": "worktree", "name": "bad name!"}); err == nil || !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("invalid name: err = %v, want invalid_request", err)
	}
}

// spawnIsolationLaneForTest drives the fresh-create plumbing up to and including
// prepareIsolation, reusing reserveWorktreeIsolatedDelegateArgs for the
// select→ceiling→describe→ReserveCreate sequence exactly as production runs it.
// The caller owns the returned isolation and must run its cleanup.
func spawnIsolationLaneForTest(t *testing.T, r *wtRepo, args delegateArgs) (*delegateStartReservation, delegateIsolation) {
	t.Helper()
	runtime, reservation, project := reserveWorktreeIsolatedDelegateArgs(t, r.s, args)
	if reservation.descriptor.Isolation != "worktree" || reservation.descriptor.WorktreeBranch != args.Name {
		t.Fatalf("descriptor isolation/branch = %q/%q, want worktree/%q", reservation.descriptor.Isolation, reservation.descriptor.WorktreeBranch, args.Name)
	}
	isolation, err := runtime.prepareIsolation(context.Background(), reservation, project, nil)
	if err != nil {
		t.Fatalf("prepareIsolation: %v", err)
	}
	return reservation, isolation
}

// REAL git: the lane is really cut on the named branch (`worktree add -b`),
// really checks it out, and the rollback at the end must really delete that
// branch and never reach for the delegate id.
func TestDelegateIsolation_NamedLaneBranch(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	args := delegateArgs{Task: "named lane unit", DelegationAllowance: new(0), Isolation: "worktree", Name: "parser-rename"}
	reservation, isolation := spawnIsolationLaneForTest(t, r, args)

	lanePath := isolation.worktreePath
	if filepath.Base(lanePath) != reservation.delegateID {
		t.Fatalf("lane directory = %q, want it keyed to the delegate id %q", filepath.Base(lanePath), reservation.delegateID)
	}
	if !branchExistsInRepo(t, r.mainRoot, "parser-rename") {
		t.Fatal("lane was not cut on the named branch parser-rename")
	}
	e := r.porcelainEntry(t, lanePath)
	if e.Branch != "refs/heads/parser-rename" {
		t.Fatalf("lane's checked-out branch = %q, want refs/heads/parser-rename", e.Branch)
	}
	sc, err := worktree.ReadSidecar(r.metaDir(t, r.mainRoot), reservation.delegateID)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if sc.Name != reservation.delegateID || sc.Branch != "parser-rename" {
		t.Fatalf("sidecar name/branch = %q/%q, want %s/parser-rename", sc.Name, sc.Branch, reservation.delegateID)
	}
	if isolation.laneBranch != "parser-rename" {
		t.Fatalf("isolation laneBranch = %q, want parser-rename", isolation.laneBranch)
	}

	// The rollback the spawn path owes must delete the mnemonic branch.
	isolation.cleanup(r.s, reservation.delegateID)
	if branchExistsInRepo(t, r.mainRoot, "parser-rename") {
		t.Fatal("rollback left the named branch behind")
	}
	if branchExistsInRepo(t, r.mainRoot, reservation.delegateID) {
		t.Fatal("rollback left a branch named after the delegate id")
	}
	if _, err := worktree.ReadSidecar(r.metaDir(t, r.mainRoot), reservation.delegateID); !os.IsNotExist(err) {
		t.Fatalf("rollback left the sidecar behind: %v", err)
	}
	if laneWorktreePresent(lanePath) {
		t.Fatal("rollback left the lane directory behind")
	}
}

// REAL git: the default lane branch is the delegate id, pinning today's behavior
// for every spawn that sends no name.
func TestDelegateIsolation_AbsentNameDefaultsToDelegateID(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	args := delegateArgs{Task: "default lane unit", DelegationAllowance: new(0), Isolation: "worktree"}
	reservation, isolation := spawnIsolationLaneForTest(t, r, args)
	defer isolation.cleanup(r.s, reservation.delegateID)

	if isolation.laneBranch != reservation.delegateID {
		t.Fatalf("isolation laneBranch = %q, want the delegate id %q", isolation.laneBranch, reservation.delegateID)
	}
	e := r.porcelainEntry(t, isolation.worktreePath)
	if e.Branch != "refs/heads/"+reservation.delegateID {
		t.Fatalf("default lane branch = %q, want refs/heads/%s", e.Branch, reservation.delegateID)
	}
}
