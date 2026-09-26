package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/worktree"
)

// The delegate tool's optional `name` argument is accepted for every delegate.
// With isolation:"worktree" it names the git branch of the lane, so `git
// branch` and merges read mnemonically; without isolation it is a display-only
// label. Either way the lane directory, sidecar, and every addressing surface
// stay keyed to the delegate id. The decode test is pure over the args map; the
// spawn tests run on the real-git harness because a lane cut on a branch that
// differs from its directory is a `worktree add -b` registry effect, and the
// rollback must really delete it.

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

	// A name without worktree isolation is accepted as a display-only label.
	labeled, err := decodeDelegateArgs(map[string]any{"prompt": "p", "name": "parser-rename"})
	if err != nil {
		t.Fatalf("name without isolation: %v", err)
	}
	if labeled.Name != "parser-rename" {
		t.Fatalf("label decoded as %q, want parser-rename", labeled.Name)
	}

	// The same alphabet manage_worktree names use, checked before any capacity
	// is reserved — for every name, worktree or not.
	if _, err := decodeDelegateArgs(map[string]any{"prompt": "p", "isolation": "worktree", "name": "bad name!"}); err == nil || !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("invalid name: err = %v, want invalid_request", err)
	}
	if _, err := decodeDelegateArgs(map[string]any{"prompt": "p", "name": "bad name!"}); err == nil || !strings.Contains(err.Error(), "invalid_request") {
		t.Fatalf("invalid label: err = %v, want invalid_request", err)
	}

	// The isolation value is normalized at decode, so a padded value flows
	// to every downstream consumer in the same shape as a clean one (create
	// and describe trim it; decode must not be the one place that doesn't).
	padded, err := decodeDelegateArgs(map[string]any{"prompt": "p", "isolation": "worktree ", "name": "parser-rename"})
	if err != nil {
		t.Fatalf("padded isolation with a name: %v", err)
	}
	if padded.Isolation != "worktree" || padded.Name != "parser-rename" {
		t.Fatalf("padded isolation decoded as %q/%q, want worktree/parser-rename", padded.Isolation, padded.Name)
	}
}

// describeForTest runs the select→describe sequence the spawn path uses and
// returns the descriptor describe built for args.
func describeForTest(t *testing.T, r *wtRepo, args delegateArgs) delegatestore.Descriptor {
	t.Helper()
	selection, err := r.s.selectSubagentModel(context.Background(), args.Model, args.AgentType)
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	descriptor, _, err := (delegateRuntime{owner: r.s}).describe(context.Background(), args, args.Task, args.Isolation, nil, selection, nil)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	return descriptor
}

// describe carries a non-worktree name as a display-only label: the
// descriptor records it as Name, and nothing is cut, so WorktreeBranch stays
// empty.
func TestDescribeDelegate_NameLabelsNonWorktreeDelegate(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	args := delegateArgs{Task: "labeled unit", Name: "research-label", DelegationAllowance: new(0)}
	descriptor := describeForTest(t, r, args)
	if descriptor.Name != "research-label" {
		t.Fatalf("descriptor name = %q, want research-label", descriptor.Name)
	}
	if descriptor.WorktreeBranch != "" {
		t.Fatalf("non-worktree descriptor branch = %q, want empty (a label cuts no branch)", descriptor.WorktreeBranch)
	}
}

// With worktree isolation the name still names the lane's branch while also
// labeling the delegate: both descriptor fields carry it.
func TestDescribeDelegate_WorktreeNameStillNamesBranch(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	args := delegateArgs{Task: "named lane unit", Name: "parser-rename", Isolation: "worktree", DelegationAllowance: new(0)}
	descriptor := describeForTest(t, r, args)
	if descriptor.Name != "parser-rename" || descriptor.WorktreeBranch != "parser-rename" {
		t.Fatalf("descriptor name/branch = %q/%q, want parser-rename on both", descriptor.Name, descriptor.WorktreeBranch)
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

func TestDescribeDelegate_InvalidNameRejectedAtStoreSeam(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"bad name!", "has space"} {
		args := delegateArgs{Task: "bad label unit", Name: bad, DelegationAllowance: new(0)}
		_, _, err := (delegateRuntime{owner: newWorktreeRepo(t).s}).describe(context.Background(), args, args.Task, args.Isolation, nil, subagentModelSelection{}, nil)
		if err == nil {
			t.Fatalf("describe with invalid name %q succeeded, want rejection", bad)
		}
		if !strings.Contains(err.Error(), "invalid_request") {
			t.Fatalf("invalid name %q error = %v, want invalid_request", bad, err)
		}
	}
}
