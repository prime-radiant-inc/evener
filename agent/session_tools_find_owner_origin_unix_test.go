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

// TestFind_ProjectName_SiblingOriginDoesNotResolveTheCheckoutForANameOnlyRow is
// the bucket-wide half of the rule above, and it is a COST assertion, so it is
// written against the work rather than the answer.
//
// A deleted session-owned lane with no usable owner or parent has no project ID
// to verify, so the bucket owes it only a NAME. A sibling that records a saved
// origin supplies that name from metadata. Resolving that sibling's recorded
// checkout first is work nobody asked for, and one historical checkout on a
// stalled mount would hold the whole read-only search.
//
// The two orders produce the SAME label, so a label assertion cannot tell them
// apart — an earlier version of this test passed against the unfixed code for
// exactly that reason. What differs is whether the sibling's path is resolved at
// all, and that is visible in the evidence the snapshot holds: the name-only form
// records no project ID, because deriving one is the step that walks the
// checkout. A live checkout is used so a walk would positively SUCCEED and leave
// a project ID behind, which is what makes its absence evidence of skipped work.
func TestFind_ProjectName_SiblingOriginDoesNotResolveTheCheckoutForANameOnlyRow(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	repo := newNamedCheckout(t, "bucket-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The sibling records an origin AND a checkout that resolves, so reading the
	// record yields a project ID and skipping the record yields none.
	historical := newNamedCheckout(t, "historical-checkout")
	sibling := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	sibling.EnvInfo.GitOriginURL = "git@github.com:bucket/bucket-repo.git"
	sibling.EnvInfo.WorkingDir = historical
	if err := schema.SaveSessionMeta(bucket, sibling); err != nil {
		t.Fatal(err)
	}

	metas, err := schema.ListSessionMetas(bucket)
	if err != nil {
		t.Fatal(err)
	}
	resolver := newProjectNameResolver(map[string][]schema.SessionMeta{bucket: metas})

	// An ownerless lane in this bucket: <bucket>/worktrees/<lane>. Its path
	// records no project ID, and it is gone, so it supplies no candidate of its
	// own and nothing about it can be verified.
	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "worktrees", "dlg_unowned")

	if got, want := resolver.projectName(row, bucket), "bucket-repo"; got != want {
		t.Fatalf("projectName(name-only row) = %q, want the sibling's origin %q", got, want)
	}

	evidence, ok := resolver.siblingEvidence[siblingEvidenceKey{bucketDir: bucket, needProjectID: false}]
	if !ok {
		t.Fatalf("the name-only row did not build the bucket's sibling evidence")
	}
	if len(evidence) != 1 || evidence[0].name != "bucket-repo" {
		t.Fatalf("sibling evidence = %#v, want the one origin-derived candidate", evidence)
	}
	if evidence[0].projectID != "" {
		t.Fatalf("sibling evidence for a name-only row carries project ID %q: the checkout was resolved for a row that needed no ID, so a historical checkout on a stalled mount would hold this search", evidence[0].projectID)
	}
}

// TestFind_ProjectName_NameOnlyRowStopsAtTheFirstOrigin pins the cost the
// previous scan paid: an ORIGINLESS sibling older than the origin-bearing one
// was still resolved before the origin answered.
//
// A pruned session-owned lane needs only a project name, and a sibling's saved
// origin settles that from metadata alone. Resolving the originless sibling's
// recorded path is filesystem work the answer does not need, and on an
// unavailable network mount it blocks the whole read-only search. The scan must
// therefore read the metadata for a usable origin before resolving any sibling's
// path.
//
// The originless sibling's checkout is LIVE, so a resolution positively succeeds
// and is visible: a name-only snapshot that touches it carries the project ID
// that resolving it produces. Its absence is the evidence that no path was
// resolved, which a label assertion cannot show because both orders label the
// row the same way.
func TestFind_ProjectName_NameOnlyRowStopsAtTheFirstOrigin(t *testing.T) {
	stateHome := pinStateHome(t)

	repo := newNamedCheckout(t, "bucket-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The originless sibling is written FIRST, so a scan that walks metadata in
	// order reaches it before the origin that would have answered.
	originless := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	originless.EnvInfo.WorkingDir = newNamedCheckout(t, "originless-checkout")
	if err := schema.SaveSessionMeta(bucket, originless); err != nil {
		t.Fatal(err)
	}
	withOrigin := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	withOrigin.EnvInfo.GitOriginURL = "git@github.com:bucket/bucket-repo.git"
	withOrigin.EnvInfo.WorkingDir = filepath.Join(t.TempDir(), "gone-checkout")
	if err := schema.SaveSessionMeta(bucket, withOrigin); err != nil {
		t.Fatal(err)
	}

	metas, err := schema.ListSessionMetas(bucket)
	if err != nil {
		t.Fatal(err)
	}
	resolver := newProjectNameResolver(map[string][]schema.SessionMeta{bucket: metas})

	row := schema.SessionMeta{ID: identifier.MustNewSessionID()}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_name_only")
	if got, want := resolver.projectName(row, bucket), "bucket-repo"; got != want {
		t.Fatalf("projectName(name-only row beside an originless sibling) = %q, want %q", got, want)
	}

	evidence, ok := resolver.siblingEvidence[siblingEvidenceKey{bucketDir: bucket, needProjectID: false}]
	if !ok {
		t.Fatalf("the name-only row did not build the bucket's sibling evidence")
	}
	for _, cand := range evidence {
		if cand.projectID != "" {
			t.Fatalf("name-only sibling evidence = %#v carries project ID %q: the originless sibling's checkout was resolved even though a saved origin had already answered, which is the walk an unavailable mount stalls", evidence, cand.projectID)
		}
	}
}

// TestFind_ProjectName_VerifiedParentOriginDoesNotResolveTheParentPaths is the
// PARENT half of the same rule, and it is a cost assertion for the same reason:
// the label is identical either way, so only the work can tell the orders apart.
//
// A pruned session-owned lane has no project ID to verify, so a verified
// parent's saved origin settles its label from metadata alone. The parent's own
// record is read BEFORE that origin in the unfixed code, and reading it resolves
// every recorded path — a filesystem call on a checkout that may sit on an
// unavailable mount. An older parent on such a mount would hold the whole
// read-only search even though the answer was already in local metadata.
//
// The parent's recorded checkout is LIVE, so a resolution positively succeeds
// and is visible in pathResolutions. The count is the evidence: a lookup that
// read the parent's record cannot have zero.
func TestFind_ProjectName_VerifiedParentOriginDoesNotResolveTheParentPaths(t *testing.T) {
	stateHome := pinStateHome(t)

	repo := newNamedCheckout(t, "bucket-repo")
	project := identifier.ProjectFromCanonicalPath(identifier.ResolveClean(repo))
	bucket, err := RuntimeDirForProjectWithStateHome(project, stateHome)
	if err != nil {
		t.Fatalf("runtime layout helper: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bucket, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir bucket: %v", err)
	}

	// The parent is LIVE on disk and carries an origin, so reading its record is
	// work that succeeds and leaves a trace, while its origin answers on its own.
	parentID := identifier.MustNewSessionID()
	parent := schema.SessionMeta{ID: parentID}
	parent.EnvInfo.GitOriginURL = "git@github.com:bucket/bucket-repo.git"
	parent.EnvInfo.WorkingDir = newNamedCheckout(t, "parent-live-checkout")
	if err := schema.SaveSessionMeta(bucket, parent); err != nil {
		t.Fatal(err)
	}

	metas, err := schema.ListSessionMetas(bucket)
	if err != nil {
		t.Fatal(err)
	}
	resolver := newProjectNameResolver(map[string][]schema.SessionMeta{bucket: metas})

	// A session-owned lane whose own directory is gone: no project ID anywhere in
	// its path, so the parent's origin is admissible as it stands.
	row := schema.SessionMeta{ID: identifier.MustNewSessionID(), ParentSessionID: parentID}
	row.EnvInfo.WorkingDir = filepath.Join(bucket, "sessions", identifier.MustNewSessionID(), "worktrees", "dlg_pruned")

	if got, want := resolver.projectName(row, bucket), "bucket-repo"; got != want {
		t.Fatalf("projectName(pruned session lane beside a parent that saved an origin) = %q, want %q", got, want)
	}
	if resolver.pathResolutions != 0 {
		t.Fatalf("the lookup resolved %d recorded path(s) before answering from the verified parent's saved origin; reading the parent's record walks a checkout an unavailable mount stalls, and the origin needed no walk at all", resolver.pathResolutions)
	}
}
