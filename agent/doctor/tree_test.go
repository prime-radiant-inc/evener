package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
)

const childSID = "02wMz5Txv2enqVTitaig6F"
const obsSID = "02wMz5Txv47YP64RR3B9YJ"
const grandchildSID = "02wMz5Txv1C3Hut0M8GCeB"

// treeFixture builds: root (hash1) --delegate--> child (hash2, cross-bucket),
// with root observed by an observer session (hash1). Each descriptorMutate
// hook, when given, adjusts the delegate's created descriptor before it is
// journaled (the name-label test uses one to give the delegate a Name).
func treeFixture(t *testing.T, descriptorMutate ...func(*delegatestore.Descriptor)) (base, rootSID string) {
	t.Helper()
	base = t.TempDir()
	rootBucket := stateHomeBucket(base, hash1)
	childBucket := stateHomeBucket(base, hash2)
	rootSID = sidA

	writeSession(t, rootBucket, rootSID)
	writeSession(t, childBucket, childSID) // child lives in a DIFFERENT bucket
	writeSession(t, rootBucket, obsSID)    // observer session

	// Root's stable delegate journal contains a running delegate to the child.
	rootDelegates := filepath.Join(rootBucket, "sessions", rootSID, "delegates.jsonl")
	descriptor := delegatestore.Descriptor{
		ChildSessionID: childSID, TranscriptRef: "proj:" + hash2 + ":" + childSID,
		OwnerSessionID: rootSID, VisibleSessionID: rootSID, Task: "inspect child",
		AgentType: "explorer", ToolNameCeiling: []string{"communicate"}, Resumable: true,
	}
	for _, mutate := range descriptorMutate {
		mutate(&descriptor)
	}
	writeDelegateEvents(t, rootDelegates, []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "del1", Created: &delegatestore.DelegateCreated{Descriptor: descriptor}},
		{Kind: delegatestore.EventDelegateRunStarted, DelegateID: "del1", RunStarted: &delegatestore.RunStarted{
			Generation: 1, Trigger: delegatestore.TriggerInitial, StartedAt: time.Unix(1, 0).UTC(),
		}},
	})

	// Stamp the root's meta with the observer link.
	meta := schema.SessionMeta{ID: rootSID, ObservedBy: []string{obsSID}}
	if err := schema.SaveSessionMeta(rootBucket, meta); err != nil {
		t.Fatal(err)
	}
	return base, rootSID
}

func TestTree_DelegateEdgeCrossBucket(t *testing.T) {
	base, rootSID := treeFixture(t)
	root, err := Tree(base, rootSID, TreeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if root.SessionID != rootSID {
		t.Fatalf("root SID = %q, want %q", root.SessionID, rootSID)
	}
	if len(root.Children) != 1 {
		t.Fatalf("root should have 1 delegate child, got %d", len(root.Children))
	}
	c := root.Children[0]
	if c.SessionID != childSID || c.Edge != "delegate" {
		t.Errorf("child = %+v, want delegate edge to %s", c, childSID)
	}
	if c.AgentType != "explorer" {
		t.Errorf("child AgentType = %q, want explorer", c.AgentType)
	}
	if c.TranscriptRef != "proj:"+hash2+":"+childSID {
		t.Errorf("child ref = %q, want cross-bucket proj ref", c.TranscriptRef)
	}
	if c.Status != "running" {
		t.Errorf("child status = %q, want %q", c.Status, "running")
	}
}

// In doctor.Tree, a delegate child in the same project bucket must emit the
// canonical proj:<bucket>:<id> ref matching the root node's ref, even when
// delegates.jsonl recorded a local:<id> descriptor (issue #6179).
func TestTree_SameBucketChildRefMatchesRootProjScheme(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	rootSID := sidA
	childSID := "02wMz5Txv2enqVTitaig6F"

	writeSession(t, bucket, rootSID)
	writeSession(t, bucket, childSID) // child lives in the SAME bucket as root

	rootDelegates := filepath.Join(bucket, "sessions", rootSID, "delegates.jsonl")
	writeDelegateEvents(t, rootDelegates, []delegatestore.Event{
		{
			Kind:       delegatestore.EventDelegateCreated,
			DelegateID: "del1",
			Created: &delegatestore.DelegateCreated{
				Descriptor: delegatestore.Descriptor{
					ChildSessionID:   childSID,
					TranscriptRef:    "local:" + childSID,
					OwnerSessionID:   rootSID,
					VisibleSessionID: rootSID,
					Task:             "inspect child",
					AgentType:        "explorer",
					ToolNameCeiling:  []string{"communicate"},
					Resumable:        true,
				},
			},
		},
		{
			Kind:       delegatestore.EventDelegateRunStarted,
			DelegateID: "del1",
			RunStarted: &delegatestore.RunStarted{
				Generation: 1,
				Trigger:    delegatestore.TriggerInitial,
				StartedAt:  time.Unix(1, 0).UTC(),
			},
		},
	})

	root, err := Tree(base, rootSID, TreeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	wantRootRef := "proj:" + hash1 + ":" + rootSID
	if root.TranscriptRef != wantRootRef {
		t.Fatalf("root.TranscriptRef = %q, want %q", root.TranscriptRef, wantRootRef)
	}
	if len(root.Children) != 1 {
		t.Fatalf("root should have 1 child, got %d", len(root.Children))
	}
	child := root.Children[0]
	wantChildRef := "proj:" + hash1 + ":" + childSID
	if child.TranscriptRef != wantChildRef {
		t.Errorf("child.TranscriptRef = %q, want canonical %q", child.TranscriptRef, wantChildRef)
	}
}

func TestTree_ObserversOptIn(t *testing.T) {
	base, rootSID := treeFixture(t)

	without, _ := Tree(base, rootSID, TreeOpts{Observers: false})
	for _, c := range without.Children {
		if c.Edge == "observer" {
			t.Error("observer edge should not appear without Observers opt-in")
		}
	}

	with, _ := Tree(base, rootSID, TreeOpts{Observers: true})
	var sawObserver bool
	for _, c := range with.Children {
		if c.Edge == "observer" && c.SessionID == obsSID {
			sawObserver = true
		}
	}
	if !sawObserver {
		t.Errorf("observer edge to %s missing with Observers opt-in: %+v", obsSID, with.Children)
	}
}

func TestTree_DepthLimit(t *testing.T) {
	base, rootSID := treeFixture(t)

	// Give childSID a delegate child of its own so depthLimitNote has a real
	// grandchild to report and the depth-1 suppression is observable in the note.
	childBucket := stateHomeBucket(base, hash2)
	childDelegates := filepath.Join(childBucket, "sessions", childSID, "delegates.jsonl")
	writeSession(t, childBucket, grandchildSID)
	writeDelegateEvents(t, childDelegates, []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "del2", Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID: grandchildSID, TranscriptRef: "proj:" + hash2 + ":" + grandchildSID,
			OwnerSessionID: childSID, VisibleSessionID: childSID, Task: "inspect grandchild",
			AgentType: "leaf", ToolNameCeiling: []string{"communicate"}, Resumable: true,
		}}},
	})

	root, _ := Tree(base, rootSID, TreeOpts{Depth: 1})
	if len(root.Children) != 1 {
		t.Fatalf("depth 1 should still list immediate children, got %d", len(root.Children))
	}
	child := root.Children[0]
	if len(child.Children) != 0 {
		t.Errorf("depth 1 should not expand grandchildren, got %d", len(child.Children))
	}
	if !strings.Contains(child.Note, "depth limit") {
		t.Errorf("depth-limit note should indicate suppression, got %q", child.Note)
	}
}

// TestTree_DisposedDelegate covers delegate-lane disposal spec §P1 doctor
// visibility: a delegate whose lane was disposed renders with the "disposed"
// note so it reads as non-resumable in the tree.
func TestTree_DisposedDelegate(t *testing.T) {
	base, rootSID := treeFixture(t)

	// Append a mid-life resumability closure for the fixture's delegate.
	rootBucket := stateHomeBucket(base, hash1)
	rootDelegates := filepath.Join(rootBucket, "sessions", rootSID, "delegates.jsonl")
	writeDelegateEvents(t, rootDelegates, []delegatestore.Event{
		{Kind: delegatestore.EventDelegateResumabilityClosed, DelegateID: "del1", ResumabilityClosed: &delegatestore.ResumabilityClosed{Reason: "disposed"}},
	})

	root, err := Tree(base, rootSID, TreeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 1 {
		t.Fatalf("root should have 1 delegate child, got %d", len(root.Children))
	}
	child := root.Children[0]
	if !strings.Contains(child.Note, "disposed") {
		t.Errorf("disposed delegate child note = %q, want it to mention disposed", child.Note)
	}
	if !strings.Contains(RenderTree(root), "disposed") {
		t.Errorf("render should surface disposed marker:\n%s", RenderTree(root))
	}
}

func TestTree_Render(t *testing.T) {
	base, rootSID := treeFixture(t)
	root, _ := Tree(base, rootSID, TreeOpts{Observers: true})
	out := RenderTree(root)
	if !strings.Contains(out, childSID) || !strings.Contains(out, "explorer") {
		t.Errorf("render missing delegate child:\n%s", out)
	}
	if !strings.Contains(out, "observer "+obsSID) {
		t.Errorf("render missing observer edge:\n%s", out)
	}
	if !strings.Contains(out, "proj:"+hash2) {
		t.Errorf("render missing cross-bucket ref:\n%s", out)
	}
}

// A named delegate's tree node carries the label (display-only; the node stays
// addressed by session id), and an unnamed delegate's node has no label slot.
func TestTree_DelegateNameLabel(t *testing.T) {
	base, rootSID := treeFixture(t, func(d *delegatestore.Descriptor) { d.Name = "tree-label" })
	root, err := Tree(base, rootSID, TreeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 1 {
		t.Fatalf("root should have 1 delegate child, got %d", len(root.Children))
	}
	if root.Children[0].Name != "tree-label" {
		t.Errorf("named child Name = %q, want tree-label", root.Children[0].Name)
	}
	if rendered := RenderTree(root); !strings.Contains(rendered, "tree-label") {
		t.Errorf("render missing the delegate label:\n%s", rendered)
	}

	// The unmutated fixture's delegate has no name: its node projects the
	// zero label, which renders as absent.
	fixtureBase, fixtureRoot := treeFixture(t)
	fixtureRootTree, _ := Tree(fixtureBase, fixtureRoot, TreeOpts{})
	if len(fixtureRootTree.Children) != 1 || fixtureRootTree.Children[0].Name != "" {
		t.Errorf("unnamed child = %#v, want an empty label", fixtureRootTree.Children)
	}
}

// TestWithDelegateReason covers all branches of withDelegateReason.
func TestWithDelegateReason(t *testing.T) {
	if got := withDelegateReason("", ""); got != "" {
		t.Errorf("withDelegateReason(empty, empty) = %q, want empty", got)
	}
	if got := withDelegateReason("note", ""); got != "note" {
		t.Errorf("withDelegateReason(note, empty) = %q, want note", got)
	}
	if got := withDelegateReason("", "disposed"); got != "disposed" {
		t.Errorf("withDelegateReason(empty, disposed) = %q, want disposed", got)
	}
	if got := withDelegateReason("note", "disposed"); got != "disposed; note" {
		t.Errorf("withDelegateReason(note, disposed) = %q, want 'disposed; note'", got)
	}
}

// A node whose jobs log cannot be read carries a note saying so instead of
// failing the tree.
func TestTree_JobsUnreadable(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)
	if err := writeDirAt(filepath.Join(bucket, "sessions", sidA, "jobs.jsonl")); err != nil {
		t.Fatal(err)
	}
	root, err := Tree(base, sidA, TreeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if root.Note == "" || len(root.Children) != 0 {
		t.Errorf("root = %+v, want a note and no children", root)
	}
}

// A delegate whose child transcript is missing is listed with a note rather
// than being dropped.
func TestTree_DelegateChildTranscriptMissing(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSession(t, bucket, sidA)
	missing := "01MISSINGCHILDSESSIONXXXXXX"
	writeDelegateEvents(t, filepath.Join(bucket, "sessions", sidA, "delegates.jsonl"), []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "d1", Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID: missing, TranscriptRef: "local:" + missing,
			OwnerSessionID: sidA, VisibleSessionID: sidA, Task: "inspect missing child",
			AgentType: "ghost", ToolNameCeiling: []string{"communicate"}, Resumable: true,
		}}},
	})
	root, err := Tree(base, sidA, TreeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 1 || root.Children[0].SessionID != missing || root.Children[0].Note == "" {
		t.Errorf("children = %+v, want the missing child listed with a note", root.Children)
	}
	if root.Children[0].TranscriptRef != "" {
		t.Errorf("missing child TranscriptRef = %q, want empty", root.Children[0].TranscriptRef)
	}
}

// writeDirAt removes any file at path and creates a directory there, so a
// subsequent read of that path fails.
func writeDirAt(path string) error {
	_ = os.Remove(path)
	return os.MkdirAll(path, 0o755)
}
