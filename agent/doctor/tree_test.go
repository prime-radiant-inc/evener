package doctor

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
)

const childSID = "01TESTCHILDSESSIONXXXXXXXXX"
const obsSID = "01TESTOBSERVERSESSIONXXXXXX"

// treeFixture builds: root (hash1) --delegate--> child (hash2, cross-bucket),
// with root observed by an observer session (hash1).
func treeFixture(t *testing.T) (base, rootSID string) {
	t.Helper()
	base = t.TempDir()
	rootBucket := stateHomeBucket(base, hash1)
	childBucket := stateHomeBucket(base, hash2)
	rootSID = sidA

	writeSession(t, rootBucket, rootSID)
	writeSession(t, childBucket, childSID) // child lives in a DIFFERENT bucket
	writeSession(t, rootBucket, obsSID)    // observer session

	// Root's jobs: a delegate to the child + a job_started so status is running.
	rootJobs := filepath.Join(rootBucket, "sessions", rootSID, "jobs.jsonl")
	writeJobsEvents(t, rootJobs, []jobstore.Event{
		{Kind: jobstore.EventDelegateCreated, DelegateID: "del1", Delegate: &jobstore.DelegateEvent{
			ChildSessionID: childSID,
			TranscriptRef:  "proj:" + hash2 + ":" + childSID,
			AgentType:      "explorer",
			Generation:     "g1",
		}},
		{Kind: jobstore.EventJobStarted, JobID: "j1", DelegateID: "del1"},
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
		t.Errorf("child status = %q, want %q (EventJobStarted → DelegateRunning)", c.Status, "running")
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
	childJobs := filepath.Join(childBucket, "sessions", childSID, "jobs.jsonl")
	writeJobsEvents(t, childJobs, []jobstore.Event{
		{Kind: jobstore.EventDelegateCreated, DelegateID: "del2", Delegate: &jobstore.DelegateEvent{
			ChildSessionID: "grandchild-placeholder",
			AgentType:      "leaf",
		}},
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
