package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// delegateSnapshotWithPacket builds a delegateSnapshot carrying a terminal
// packet with metadata, exercising the packet-projection branches of
// projectStableActivityDelegate.
func delegateSnapshotWithPacket(id, owner, child, task string, packet *delegatestore.TerminalPacket, outcome *delegatestore.Outcome) delegateSnapshot {
	row := stableActivitySnapshot(id, owner, child, task)
	row.latestPacket = packet
	row.lastOutcome = outcome
	return row
}

func TestActivityUsageFromCumulative(t *testing.T) {
	t.Parallel()
	if got := activityUsageFromCumulative(nil); got != nil {
		t.Fatalf("nil usage = %+v, want nil", got)
	}
	zero := schema.CumulativeUsage{}
	if got := activityUsageFromCumulative(&zero); got != nil {
		t.Fatalf("zero usage = %+v, want nil", got)
	}
	usage := &schema.CumulativeUsage{InputTokens: 100, OutputTokens: 50, CacheReadTokens: 20, TotalTokens: 170}
	got := activityUsageFromCumulative(usage)
	if got == nil || got.InputTokens != 100 || got.OutputTokens != 50 || got.CacheReadTokens != 20 || got.TotalTokens != 170 {
		t.Fatalf("usage = %+v, want %+v", got, usage)
	}
	if &got.InputTokens == &usage.InputTokens {
		t.Fatal("returned pointer aliases input struct")
	}
}

func TestCloneInt64(t *testing.T) {
	t.Parallel()
	if got := cloneInt64(nil); got != nil {
		t.Fatalf("cloneInt64(nil) = %v, want nil", got)
	}
	v := int64(42)
	got := cloneInt64(&v)
	if got == nil || *got != 42 || got == &v {
		t.Fatalf("cloneInt64 = %v, want distinct copy of 42", got)
	}
}

func TestActivityOwnedRecords(t *testing.T) {
	t.Parallel()
	// Empty sessionID or no records returns records as-is.
	if got := activityOwnedRecords("", nil); len(got) != 0 {
		t.Fatalf("empty sessionID with nil records = %v", got)
	}
	recs := []*jobstore.JobRecord{
		{JobID: "a", OwnerSessionID: "root"},
		{JobID: "b", OwnerSessionID: "other"},
		{JobID: "c", OwnerSessionID: ""}, // empty owner = owned by all
		nil,                              // nil record is skipped
	}
	got := activityOwnedRecords("root", recs)
	if len(got) != 2 {
		t.Fatalf("got %d records, want 2 (root-owned + empty-owner), ids=%v", len(got), activityRecordIDs(got))
	}
	if got[0].JobID != "a" || got[1].JobID != "c" {
		t.Fatalf("ids = %v, want [a c]", activityRecordIDs(got))
	}
}

func TestActivityDelegateOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		outcome string
		want    string
	}{
		{string(delegatestore.OutcomeFailed), "failure"},
		{string(delegatestore.OutcomeExhausted), "failure"},
		{string(delegatestore.OutcomeCompleted), "success"},
		{string(delegatestore.OutcomeCancelled), "neutral"},
		{string(delegatestore.OutcomeStopped), "neutral"},
		{"unknown", ""},
	} {
		if got := activityDelegateOutcome(tc.outcome); got != tc.want {
			t.Errorf("activityDelegateOutcome(%q) = %q, want %q", tc.outcome, got, tc.want)
		}
	}
}

func TestActivityBranchComplete(t *testing.T) {
	t.Parallel()
	if !activityBranchComplete(appwire.JobActivityBranchState{}) {
		t.Error("empty branch should be complete")
	}
	if activityBranchComplete(appwire.JobActivityBranchState{Error: "boom"}) {
		t.Error("branch with error should not be complete")
	}
	if activityBranchComplete(appwire.JobActivityBranchState{Truncated: true}) {
		t.Error("truncated branch should not be complete")
	}
	if activityBranchComplete(appwire.JobActivityBranchState{Continuation: "abc"}) {
		t.Error("branch with continuation should not be complete")
	}
}

func TestActivitySessionLabel(t *testing.T) {
	t.Parallel()
	// Display name from SessionMeta takes priority.
	meta := schema.SessionMeta{ID: "sid", Name: "My Session"}
	if got := activitySessionLabel(meta); got != "My Session" {
		t.Fatalf("label = %q, want display name", got)
	}
	// Falls back to ID when no display name.
	meta = schema.SessionMeta{ID: "sid"}
	if got := activitySessionLabel(meta); got != "sid" {
		t.Fatalf("label = %q, want id", got)
	}
	// Falls back to ID when display name is whitespace.
	meta = schema.SessionMeta{ID: "sid", Name: "  "}
	if got := activitySessionLabel(meta); got != "sid" {
		t.Fatalf("label = %q, want id for whitespace name", got)
	}
}

func TestSameActivityStateDir(t *testing.T) {
	t.Parallel()
	if !sameActivityStateDir("", "") {
		t.Error("empty == empty should match")
	}
	if sameActivityStateDir("", "/a") {
		t.Error("empty vs non-empty should not match")
	}
	if !sameActivityStateDir("/a/b", "/a/b") {
		t.Error("same path should match")
	}
}

func TestProjectStableActivityDelegate_TerminalPacketMetadata(t *testing.T) {
	t.Parallel()
	// A delegate with a terminal packet carrying valid metadata exercises the
	// usage/worktree extraction paths.
	resumable := false
	valid := true
	metadata, _ := json.Marshal(delegateTerminalPacketMetadata{
		CumulativeUsage: &schema.CumulativeUsage{InputTokens: 200, OutputTokens: 30, TotalTokens: 230},
		Worktree:        &delegateTerminalWorktreeReport{Path: "/wt", Branch: "br", HeadSHA: "abc", Ahead: 2, Dirty: true},
	})
	packet := &delegatestore.TerminalPacket{
		Kind:                   "result",
		Message:                json.RawMessage(`{}`),
		StructuredResult:       json.RawMessage(`{"ok":true}`),
		StructuredResultValid:  &valid,
		StructuredResultReason: "validated",
		Warnings:               []string{"w1"},
		Metadata:               metadata,
	}
	outcome := &delegatestore.Outcome{
		Status:           delegatestore.OutcomeCompleted,
		Reason:           "done",
		EndedAt:          time.Unix(100, 0).UTC(),
		ExhaustionBudget: delegatestore.ExhaustionBudgetTurns,
		ExhaustionLimit:  5,
		Resumable:        &resumable,
	}
	row := delegateSnapshotWithPacket("dlg_1", "root", "child", "inspect", packet, outcome)
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": row},
		Children:        map[string]*activitySessionSnapshot{"child": {SessionID: "child", Ref: "local:child"}},
	}
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil, 0)
	if delegate.Outcome != "completed" || delegate.Reason != "done" || !delegate.Terminal {
		t.Fatalf("delegate outcome = %q reason=%q terminal=%v", delegate.Outcome, delegate.Reason, delegate.Terminal)
	}
	if delegate.RunEndedAt != "1970-01-01T00:01:40Z" {
		t.Errorf("RunEndedAt = %q", delegate.RunEndedAt)
	}
	if delegate.ExhaustionBudget != "max_turns" || delegate.ExhaustionLimit != 5 {
		t.Errorf("exhaustion = %q/%d", delegate.ExhaustionBudget, delegate.ExhaustionLimit)
	}
	if delegate.ExhaustionResumable == nil || *delegate.ExhaustionResumable != false {
		t.Errorf("ExhaustionResumable = %v", delegate.ExhaustionResumable)
	}
	if delegate.PacketKind != "result" {
		t.Errorf("PacketKind = %q", delegate.PacketKind)
	}
	if delegate.StructuredValid == nil || !*delegate.StructuredValid {
		t.Errorf("StructuredValid = %v", delegate.StructuredValid)
	}
	if delegate.StructuredReason != "validated" {
		t.Errorf("StructuredReason = %q", delegate.StructuredReason)
	}
	if len(delegate.Warnings) != 1 || delegate.Warnings[0] != "w1" {
		t.Errorf("Warnings = %v", delegate.Warnings)
	}
	if delegate.Usage == nil || delegate.Usage.InputTokens != 200 || delegate.Usage.TotalTokens != 230 {
		t.Errorf("Usage = %+v", delegate.Usage)
	}
	if delegate.Worktree == nil || delegate.Worktree.Path != "/wt" || delegate.Worktree.Branch != "br" || delegate.Worktree.Ahead != 2 || !delegate.Worktree.Dirty {
		t.Errorf("Worktree = %+v", delegate.Worktree)
	}
}

func TestProjectStableActivityDelegate_InvalidMetadata(t *testing.T) {
	t.Parallel()
	packet := &delegatestore.TerminalPacket{
		Kind:     "result",
		Metadata: json.RawMessage(`{not valid json`),
	}
	row := delegateSnapshotWithPacket("dlg_1", "root", "child", "inspect", packet, nil)
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": row},
	}
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil, 0)
	if delegate.Branch.Error == "" || !strings.Contains(delegate.Branch.Error, "metadata is invalid") {
		t.Fatalf("expected invalid-metadata branch error, got %q", delegate.Branch.Error)
	}
	if len(delegate.Diagnostics) != 1 || delegate.Diagnostics[0] != "delegate terminal metadata is invalid" {
		t.Errorf("Diagnostics = %v", delegate.Diagnostics)
	}
}

func TestProjectStableActivityDelegate_ChildMismatchErrors(t *testing.T) {
	t.Parallel()
	row := stableActivitySnapshot("dlg_1", "root", "child", "inspect")
	// Child loaded with wrong SessionID/Ref.
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": row},
		Children:        map[string]*activitySessionSnapshot{"child": {SessionID: "wrong", Ref: "local:wrong"}},
	}
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil, 0)
	if delegate.Branch.Error == "" || !strings.Contains(delegate.Branch.Error, "child link does not match") {
		t.Fatalf("expected mismatch error, got %q", delegate.Branch.Error)
	}
}

func TestProjectStableActivityDelegate_ChildUnavailable(t *testing.T) {
	t.Parallel()
	row := stableActivitySnapshot("dlg_1", "root", "child", "inspect")
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": row},
		// No Children map entry for "child".
	}
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil, 0)
	if delegate.Branch.Error == "" || !strings.Contains(delegate.Branch.Error, "unavailable") {
		t.Fatalf("expected unavailable error, got %q", delegate.Branch.Error)
	}
}

func TestProjectStableActivityDelegate_DepthTruncated(t *testing.T) {
	t.Parallel()
	row := stableActivitySnapshot("dlg_1", "root", "child", "inspect")
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": row},
		Children:        map[string]*activitySessionSnapshot{"child": {SessionID: "child", Ref: "local:child"}},
	}
	budget := newBoundedActivityBudget("root", time.Unix(1, 0).UTC(), 0)
	// maxDepth is 32; set depth to 32 so the child is truncated. The path must
	// not contain the delegate id yet: appendActivityPath adds row.id inside
	// the projection, and decodeActivityContinuation rejects duplicate hops.
	delegate := projectStableActivityDelegate(snap, row, budget, 32, nil, 0)
	if !delegate.Branch.Truncated || delegate.Branch.Continuation == "" {
		t.Fatalf("expected truncation, got %+v", delegate.Branch)
	}
	// The continuation should be decodable for the right root.
	cont, err := decodeActivityContinuation(delegate.Branch.Continuation, "root")
	if err != nil {
		t.Fatalf("continuation decode: %v", err)
	}
	if cont.SessionID != "child" {
		t.Errorf("continuation session = %q, want child", cont.SessionID)
	}
}

func TestProjectStableActivityDelegate_MalformedChildLink(t *testing.T) {
	t.Parallel()
	// ChildSessionID present but TranscriptRef mismatch -> activityChildSessionForStable errors.
	row := delegateSnapshot{
		id:        "dlg_1",
		lifecycle: delegateLifecycleIdle,
		phase:     delegatestore.PhaseIdle,
		descriptor: delegatestore.Descriptor{
			ChildSessionID: "child",
			TranscriptRef:  "local:other",
			OwnerSessionID: "root",
			Task:           "inspect",
		},
	}
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": row},
	}
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil, 0)
	if delegate.Branch.Error == "" {
		t.Fatal("expected branch error for malformed child link")
	}
}

func TestProjectStableActivityDelegate_ErrorFromParentSnapshot(t *testing.T) {
	t.Parallel()
	row := stableActivitySnapshot("dlg_1", "root", "child", "inspect")
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": row},
		Errors:          map[string]error{"child": errActivityHistoryUnavailable},
	}
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil, 0)
	if delegate.Branch.Error != errActivityHistoryUnavailable.Error() {
		t.Fatalf("branch error = %q, want %q", delegate.Branch.Error, errActivityHistoryUnavailable.Error())
	}
}

func TestProjectStableActivityDelegate_OutcomeWithoutEndedAt(t *testing.T) {
	t.Parallel()
	outcome := &delegatestore.Outcome{Status: delegatestore.OutcomeFailed, Reason: "boom"}
	row := delegateSnapshotWithPacket("dlg_1", "root", "child", "inspect", nil, outcome)
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root",
		StableDelegates: map[string]delegateSnapshot{"dlg_1": row},
		Children:        map[string]*activitySessionSnapshot{"child": {SessionID: "child", Ref: "local:child"}},
	}
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil, 0)
	if delegate.RunEndedAt != "" {
		t.Errorf("RunEndedAt = %q, want empty for zero EndedAt", delegate.RunEndedAt)
	}
	if delegate.Outcome != "failed" || !delegate.Terminal {
		t.Errorf("outcome = %q terminal=%v", delegate.Outcome, delegate.Terminal)
	}
}

func TestActivityConsumeWorkUnit(t *testing.T) {
	t.Parallel()
	// Unbounded budget always allows.
	budget := newActivityBudget()
	if !activityConsumeWorkUnit(budget, 1) {
		t.Error("unbounded budget should allow work")
	}
	// Bounded budget at limit refuses.
	bounded := newBoundedActivityBudget("root", time.Unix(1, 0).UTC(), 0)
	bounded.usedWork = bounded.maxWorkUnits
	if activityConsumeWorkUnit(bounded, 1) {
		t.Error("budget at limit should refuse")
	}
	// Negative units clamp to 0.
	bounded2 := newBoundedActivityBudget("root", time.Unix(1, 0).UTC(), 0)
	if !activityConsumeWorkUnit(bounded2, -5) {
		t.Error("negative units should clamp and succeed")
	}
}

func TestAppendActivityPath(t *testing.T) {
	t.Parallel()
	got := appendActivityPath([]string{"a", "b"}, "dlg_1")
	if !reflect.DeepEqual(got, []string{"a", "b", "dlg_1"}) {
		t.Fatalf("path = %v", got)
	}
	// Empty delegateID doesn't append.
	got = appendActivityPath([]string{"a"}, "")
	if !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("path = %v, want [a]", got)
	}
	// Nil path + empty id = empty slice.
	got = appendActivityPath(nil, "")
	if len(got) != 0 {
		t.Fatalf("path = %v, want empty", got)
	}
}

func TestAppendActivityBranchError(t *testing.T) {
	t.Parallel()
	var branch appwire.JobActivityBranchState
	appendActivityBranchError(&branch, "first")
	if branch.Error != "first" {
		t.Fatalf("error = %q", branch.Error)
	}
	appendActivityBranchError(&branch, "second")
	if branch.Error != "first; second" {
		t.Fatalf("error = %q, want joined", branch.Error)
	}
	// Empty message is a no-op.
	appendActivityBranchError(&branch, "  ")
	if branch.Error != "first; second" {
		t.Fatalf("error = %q, should be unchanged", branch.Error)
	}
	// First message empty is a no-op.
	var b2 appwire.JobActivityBranchState
	appendActivityBranchError(&b2, "  ")
	if b2.Error != "" {
		t.Fatalf("error = %q, want empty", b2.Error)
	}
}

func TestRecomputeActivitySession(t *testing.T) {
	t.Parallel()
	// nil is a no-op.
	recomputeActivitySession(nil)
	// A session with a delegate child recomputes recursively.
	child := &appwire.JobActivitySession{
		SessionID: "child",
		Entries:   []appwire.JobActivityEntry{{Kind: "shell", Job: new(appwire.JobActivityJob{JobID: "j1", Status: "running"})}},
	}
	root := &appwire.JobActivitySession{
		SessionID: "root",
		Entries:   []appwire.JobActivityEntry{{Kind: "delegate", Delegate: &appwire.JobActivityDelegate{Type: "delegate", Child: child}}},
	}
	recomputeActivitySession(root)
	// The delegate (Type="delegate", non-terminal) counts as 1 active work
	// unit, plus the child's 1 active shell job = 2 active total.
	if root.Counts.Active != 2 {
		t.Fatalf("root counts = %+v, want Active=2 (delegate + child)", root.Counts)
	}
	if child.Counts.Active != 1 {
		t.Fatalf("child counts = %+v, want Active=1", child.Counts)
	}
}

func TestTrimActivityTreeToFit(t *testing.T) {
	t.Parallel()
	// A small tree fits within activityMaxEncodedBytes and is returned as-is.
	tree := appwire.JobActivityTree{
		Revision: 1,
		Root: appwire.JobActivitySession{
			SessionID: "root", Ref: "local:root",
			Entries: []appwire.JobActivityEntry{{Kind: "shell", Job: new(appwire.JobActivityJob{JobID: "j1"})}},
		},
	}
	got, err := trimActivityTreeToFit(tree, "root", 0, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Root.Entries) != 1 {
		t.Fatalf("entries = %d, want 1 (tree fits)", len(got.Root.Entries))
	}
	if got.Root.Branch.Truncated {
		t.Error("small tree should not be truncated")
	}
}

func TestTrimActivityTreeToFit_TrimsExcessEntries(t *testing.T) {
	t.Parallel()
	// Build a tree whose encoded size exceeds activityMaxEncodedBytes (4MB).
	// We do this by creating many entries with large descriptions.
	big := strings.Repeat("x", 10000)
	entries := make([]appwire.JobActivityEntry, 0, 600)
	for range 600 {
		entries = append(entries, appwire.JobActivityEntry{
			Kind: "shell",
			Job:  new(appwire.JobActivityJob{JobID: "j", Description: big, Status: "completed", Terminal: true}),
		})
	}
	tree := appwire.JobActivityTree{
		Revision: 1,
		Root: appwire.JobActivitySession{
			SessionID: "root", Ref: "local:root",
			Entries: entries,
		},
	}
	got, err := trimActivityTreeToFit(tree, "root", 0, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Root.Branch.Truncated {
		t.Fatal("expected tree to be truncated after trimming")
	}
	if got.Root.Branch.Continuation == "" {
		t.Fatal("expected continuation token after trimming")
	}
	if len(got.Root.Entries) >= 600 {
		t.Fatalf("expected fewer entries after trimming, got %d", len(got.Root.Entries))
	}
}

func TestTrimActivityTrailingEntry_EmptyReturnsFalse(t *testing.T) {
	t.Parallel()
	session := &appwire.JobActivitySession{SessionID: "root"}
	if trimActivityTrailingEntry(session, "root", nil, 0, nil, 0) {
		t.Error("empty entries should return false")
	}
	if trimActivityTrailingEntry(nil, "root", nil, 0, nil, 0) {
		t.Error("nil session should return false")
	}
}

func TestTrimActivityTrailingEntry_DelegateChildRecurses(t *testing.T) {
	t.Parallel()
	child := &appwire.JobActivitySession{
		SessionID: "child",
		Entries: []appwire.JobActivityEntry{
			{Kind: "shell", Job: new(appwire.JobActivityJob{JobID: "j1"})},
		},
	}
	session := &appwire.JobActivitySession{
		SessionID: "root",
		Entries: []appwire.JobActivityEntry{
			{Kind: "delegate", Delegate: &appwire.JobActivityDelegate{DelegateID: "dlg_1", Child: child}},
		},
	}
	if !trimActivityTrailingEntry(session, "root", nil, 0, nil, 0) {
		t.Fatal("expected trailing entry to be trimmed")
	}
	// The recursive call trims the child's entry and returns true; the
	// root's delegate entry is NOT removed (the recursion already made
	// progress). The root is marked truncated.
	if len(session.Entries) != 1 {
		t.Fatalf("entries = %d, want 1 (delegate entry kept, child trimmed)", len(session.Entries))
	}
	// The root's branch is not marked truncated — only the child's is,
	// since the recursion made progress inside the child and returned true.
	if len(child.Entries) != 0 {
		t.Fatalf("child entries = %d, want 0 (child's trailing entry trimmed)", len(child.Entries))
	}
	if !child.Branch.Truncated {
		t.Error("child should be truncated")
	}
}

// TestTrimActivityTrailingEntry_EmbedsEpochsInContinuation covers roborev's
// r6 finding on #807's review: trimActivityTrailingEntry minted a
// continuation with JobsEpoch/DelegatesEpoch always omitted (silently
// zero) instead of the trimmed session's own epochs from the projection
// snapshot, weakening the staleness check a resumed continuation relies
// on. The per-session jobsEpochs map lets a nested session (whose own
// JobsEpoch differs from its ancestors') get ITS OWN epoch embedded, not
// an ancestor's.
func TestTrimActivityTrailingEntry_EmbedsEpochsInContinuation(t *testing.T) {
	t.Parallel()
	child := &appwire.JobActivitySession{
		SessionID: "child",
		Entries: []appwire.JobActivityEntry{
			{Kind: "shell", Job: new(appwire.JobActivityJob{JobID: "j1"})},
		},
	}
	session := &appwire.JobActivitySession{
		SessionID: "root",
		Entries: []appwire.JobActivityEntry{
			{Kind: "delegate", Delegate: &appwire.JobActivityDelegate{DelegateID: "dlg_1", Child: child}},
		},
	}
	jobsEpochs := map[string]uint64{"root": 7, "child": 42}
	if !trimActivityTrailingEntry(session, "root", nil, 9, jobsEpochs, 17) {
		t.Fatal("expected trailing entry to be trimmed")
	}
	if child.Branch.Continuation == "" {
		t.Fatal("expected a continuation token on the trimmed child")
	}
	cont, err := decodeActivityContinuation(child.Branch.Continuation, "root")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cont.JobsEpoch != 42 {
		t.Fatalf("JobsEpoch = %d, want 42 (the CHILD's own epoch, not the root's 7)", cont.JobsEpoch)
	}
	if cont.DelegatesEpoch != 9 {
		t.Fatalf("DelegatesEpoch = %d, want 9 (the shared root delegates epoch)", cont.DelegatesEpoch)
	}
	if cont.Revision != 17 {
		t.Fatalf("Revision = %d, want 17 (the root's live-clock revision at mint time)", cont.Revision)
	}
}

// TestMarkActivityDelegateTruncated_EmbedsEpochsInContinuation covers
// roborev's r6 finding on #807's review: markActivityDelegateTruncated
// minted a continuation with JobsEpoch/DelegatesEpoch always omitted
// (silently zero) instead of the truncated delegate's own JobsEpoch and
// the shared root's DelegatesEpoch. Revision (budget.revision) covers the
// companion "live pagination lacks cross-request revision guard" finding.
func TestMarkActivityDelegateTruncated_EmbedsEpochsInContinuation(t *testing.T) {
	t.Parallel()
	delegate := &appwire.JobActivityDelegate{DelegateID: "dlg_1"}
	budget := newBoundedActivityBudget("root", time.Unix(1, 0).UTC(), 17)
	markActivityDelegateTruncated(delegate, budget, "child", []string{"dlg_1"}, 42, 9)
	if delegate.Branch.Continuation == "" {
		t.Fatal("expected a continuation token")
	}
	cont, err := decodeActivityContinuation(delegate.Branch.Continuation, "root")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cont.JobsEpoch != 42 {
		t.Fatalf("JobsEpoch = %d, want 42", cont.JobsEpoch)
	}
	if cont.DelegatesEpoch != 9 {
		t.Fatalf("DelegatesEpoch = %d, want 9", cont.DelegatesEpoch)
	}
	if cont.Revision != 17 {
		t.Fatalf("Revision = %d, want 17 (the budget's live-clock revision at mint time)", cont.Revision)
	}
}

// TestMarkActivitySessionTruncated_EmbedsRevisionInContinuation covers the
// same "live pagination lacks cross-request revision guard" finding for
// markActivitySessionTruncated's own continuation (its JobsEpoch/
// DelegatesEpoch threading predates this round and is covered elsewhere).
func TestMarkActivitySessionTruncated_EmbedsRevisionInContinuation(t *testing.T) {
	t.Parallel()
	session := &appwire.JobActivitySession{SessionID: "root"}
	budget := newBoundedActivityBudget("root", time.Unix(1, 0).UTC(), 17)
	markActivitySessionTruncated(session, budget, "root", nil, 3, 42, 9)
	if session.Branch.Continuation == "" {
		t.Fatal("expected a continuation token")
	}
	cont, err := decodeActivityContinuation(session.Branch.Continuation, "root")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cont.Revision != 17 {
		t.Fatalf("Revision = %d, want 17 (the budget's live-clock revision at mint time)", cont.Revision)
	}
}

// TestCollectActivityJobsEpochs covers the helper trimActivityTrailingEntry
// relies on to look up a session's own JobsEpoch after projection has
// already flattened the internal snapshot tree to its wire shape (which
// carries no epoch information of its own): each session in the tree,
// nested arbitrarily deep, must map to its OWN JobsEpoch, not an
// ancestor's.
func TestCollectActivityJobsEpochs(t *testing.T) {
	t.Parallel()
	grandchild := &activitySessionSnapshot{SessionID: "grandchild", JobsEpoch: 100}
	child := &activitySessionSnapshot{
		SessionID: "child", JobsEpoch: 42,
		Children: map[string]*activitySessionSnapshot{"grandchild": grandchild},
	}
	root := activitySessionSnapshot{
		SessionID: "root", JobsEpoch: 7,
		Children: map[string]*activitySessionSnapshot{"child": child},
	}
	got := collectActivityJobsEpochs(root)
	want := map[string]uint64{"root": 7, "child": 42, "grandchild": 100}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("collectActivityJobsEpochs = %+v, want %+v", got, want)
	}
}

func TestEncodeActivityContinuation(t *testing.T) {
	t.Parallel()
	// A valid continuation round-trips.
	cont := activityContinuation{Version: activityContinuationV1, RootID: "root", SessionID: "root", Path: []string{"dlg_1"}}
	encoded := encodeActivityContinuation(cont)
	if encoded == "" {
		t.Fatal("encoded continuation is empty")
	}
	decoded, err := decodeActivityContinuation(encoded, "root")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(decoded.Path, cont.Path) || decoded.SessionID != cont.SessionID {
		t.Fatalf("round-trip mismatch: %+v", decoded)
	}
}

func TestDecodeActivityContinuation_Malformed(t *testing.T) {
	t.Parallel()
	// Empty token.
	if _, err := decodeActivityContinuation("", "root"); err == nil {
		t.Fatal("empty token should error")
	}
	// Whitespace-only token.
	if _, err := decodeActivityContinuation("   ", "root"); err == nil {
		t.Fatal("whitespace token should error")
	}
	// Missing root or session.
	bad := encodeActivityContinuation(activityContinuation{Version: 1, RootID: "", SessionID: ""})
	if _, err := decodeActivityContinuation(bad, ""); err == nil {
		t.Fatal("missing root/session should error")
	}
	// Invalid root token.
	badRoot := encodeActivityContinuation(activityContinuation{Version: 1, RootID: "bad root!", SessionID: "root"})
	if _, err := decodeActivityContinuation(badRoot, ""); err == nil {
		t.Fatal("invalid root token should error")
	}
	// Invalid session token.
	badSession := encodeActivityContinuation(activityContinuation{Version: 1, RootID: "root", SessionID: "bad session!"})
	if _, err := decodeActivityContinuation(badSession, ""); err == nil {
		t.Fatal("invalid session token should error")
	}
	// Invalid path hop.
	badHop := encodeActivityContinuation(activityContinuation{Version: 1, RootID: "root", SessionID: "root", Path: []string{"bad hop!"}})
	if _, err := decodeActivityContinuation(badHop, "root"); err == nil {
		t.Fatal("invalid path hop should error")
	}
}

func TestProjectActivitySession_CycleDetected(t *testing.T) {
	t.Parallel()
	// Two sessions that point at each other via children create a cycle that
	// the budget's visiting set must catch.
	a := &activitySessionSnapshot{SessionID: "a", Ref: "local:a"}
	b := &activitySessionSnapshot{SessionID: "b", Ref: "local:b"}
	a.Children = map[string]*activitySessionSnapshot{"b": b}
	b.Children = map[string]*activitySessionSnapshot{"a": a}
	// Both have delegates so the projection recurses into children.
	a.StableDelegates = map[string]delegateSnapshot{"dlg_a": stableActivitySnapshot("dlg_a", "a", "b", "task")}
	b.StableDelegates = map[string]delegateSnapshot{"dlg_b": stableActivitySnapshot("dlg_b", "b", "a", "task")}
	got := projectActivitySession(*a, newActivityBudget())
	// The cycle should produce a branch error on the second visit.
	// At least the top-level session should have projected.
	if got.SessionID != "a" {
		t.Fatalf("session = %q, want a", got.SessionID)
	}
}

func TestProjectActivitySession_UnsupportedJobType(t *testing.T) {
	t.Parallel()
	snap := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root",
		Jobs: []*jobstore.JobRecord{{JobID: "j1", Type: jobstore.JobType("unknown"), OwnerSessionID: "root", Status: jobstore.StatusRunning}},
	}
	got := projectActivitySession(snap, newActivityBudget())
	if len(got.Entries) != 0 {
		t.Fatalf("entries = %d, want 0 for unsupported type", len(got.Entries))
	}
	if got.Branch.Error == "" || !strings.Contains(got.Branch.Error, "unsupported type") {
		t.Fatalf("branch error = %q, want unsupported type", got.Branch.Error)
	}
}

func TestProjectActivityJob_NilRecord(t *testing.T) {
	t.Parallel()
	job := projectActivityJob(nil, "local:root")
	if job.JobID != "" {
		t.Fatalf("nil record job = %+v, want zero", job)
	}
}

func TestProjectActivityJob_DescriptionFallback(t *testing.T) {
	t.Parallel()
	// Description falls back to Command, then Task.
	exit := 1
	rec := &jobstore.JobRecord{
		JobID: "j1", Type: jobstore.JobShell, OwnerSessionID: "root",
		Status: jobstore.StatusCompleted, Command: "make test",
		StartedAt: time.Unix(100, 0).UTC(), ExitCode: &exit,
	}
	job := projectActivityJob(rec, "local:root")
	if job.Description != "make test" {
		t.Fatalf("description = %q, want command fallback", job.Description)
	}
	if job.ExitCode == nil || *job.ExitCode != 1 {
		t.Fatalf("exit code = %v", job.ExitCode)
	}
	// Task fallback when no description or command.
	rec2 := &jobstore.JobRecord{JobID: "j2", Task: "build", StartedAt: time.Unix(100, 0).UTC()}
	job2 := projectActivityJob(rec2, "local:root")
	if job2.Description != "build" {
		t.Fatalf("description = %q, want task fallback", job2.Description)
	}
}

func TestCloneActivityRecord(t *testing.T) {
	t.Parallel()
	if cloneActivityRecord(nil) != nil {
		t.Fatal("nil record should return nil")
	}
	ended := time.Unix(10, 0)
	exit := 0
	last := time.Unix(20, 0)
	rec := &jobstore.JobRecord{
		JobID: "j1", StartedAt: time.Unix(5, 0),
		EndedAt: &ended, ExitCode: &exit, LastActivity: &last,
	}
	clone := cloneActivityRecord(rec)
	if clone.JobID != "j1" {
		t.Fatalf("clone = %+v", clone)
	}
	// Verify the pointers are distinct copies.
	clone.EndedAt = nil
	if rec.EndedAt == nil {
		t.Fatal("mutating clone affected original EndedAt")
	}
	clone.ExitCode = nil
	if rec.ExitCode == nil {
		t.Fatal("mutating clone affected original ExitCode")
	}
	clone.LastActivity = nil
	if rec.LastActivity == nil {
		t.Fatal("mutating clone affected original LastActivity")
	}
}

func TestMergeActivityRecords_NilAndEmpty(t *testing.T) {
	t.Parallel()
	// Nil durable + nil live = empty.
	got := mergeActivityRecords(nil, nil)
	if len(got) != 0 {
		t.Fatalf("got %d, want 0", len(got))
	}
	// Records with empty JobID are skipped.
	durable := []*jobstore.JobRecord{{JobID: "", Status: jobstore.StatusRunning}}
	live := map[string]*jobstore.JobRecord{"": {JobID: ""}}
	got = mergeActivityRecords(durable, live)
	if len(got) != 0 {
		t.Fatalf("got %d, want 0 (empty IDs skipped)", len(got))
	}
}

func TestMergeActivityRecords_LiveOnlyInsertion(t *testing.T) {
	t.Parallel()
	durable := []*jobstore.JobRecord{
		{JobID: "job_a", Status: jobstore.StatusCompleted, StartedAt: time.Unix(2, 0)},
	}
	live := map[string]*jobstore.JobRecord{
		"job_b": {JobID: "job_b", Status: jobstore.StatusRunning, StartedAt: time.Unix(1, 0)},
		"job_c": {JobID: "job_c", Status: jobstore.StatusRunning, StartedAt: time.Unix(3, 0)},
	}
	got := mergeActivityRecords(durable, live)
	if ids := activityRecordIDs(got); !reflect.DeepEqual(ids, []string{"job_b", "job_a", "job_c"}) {
		t.Fatalf("ids = %v, want [job_b job_a job_c]", ids)
	}
}

func TestSnapshotPersistedRevision(t *testing.T) {
	t.Parallel()
	// nil snapshot returns 0.
	if got := activitySnapshotPersistedRevision(nil, "root"); got != 0 {
		t.Fatalf("nil snapshot = %d, want 0", got)
	}
	// Empty snapshot with empty root falls back to session ID.
	snap := &activitySessionSnapshot{SessionID: "root", Revision: 5}
	if got := activitySnapshotPersistedRevision(snap, ""); got != 5 {
		t.Fatalf("revision = %d, want 5", got)
	}
	// Matching root returns max revision across nodes and delegates.
	child := &activitySessionSnapshot{SessionID: "child", Revision: 10}
	snap = &activitySessionSnapshot{
		SessionID: "root", RootID: "root", Revision: 5,
		StableDelegates: map[string]delegateSnapshot{
			"dlg_1": {id: "dlg_1", revision: 8},
		},
		Children: map[string]*activitySessionSnapshot{"child": child},
	}
	if got := activitySnapshotPersistedRevision(snap, "root"); got != 10 {
		t.Fatalf("revision = %d, want 10", got)
	}
	// Non-matching root node returns delegate revision only.
	snap = &activitySessionSnapshot{
		SessionID: "root", RootID: "other", Revision: 100,
		StableDelegates: map[string]delegateSnapshot{
			"dlg_1": {id: "dlg_1", revision: 3},
		},
	}
	if got := activitySnapshotPersistedRevision(snap, "root"); got != 3 {
		t.Fatalf("revision = %d, want 3 (delegate only, node root mismatch)", got)
	}
}

func TestActivityFilterSnapshotToDelegate(t *testing.T) {
	t.Parallel()
	base := activitySessionSnapshot{
		SessionID: "root", Ref: "local:root", RootID: "root", Revision: 7,
		Jobs:            []*jobstore.JobRecord{{JobID: "j1", OwnerSessionID: "root"}},
		StableDelegates: map[string]delegateSnapshot{"dlg_1": stableActivitySnapshot("dlg_1", "root", "child", "task")},
		Diagnostics:     []string{"diag1"},
	}
	child := &activitySessionSnapshot{SessionID: "child", Ref: "local:child"}
	filtered := activityFilterSnapshotToDelegate(base, "dlg_1", child)
	if len(filtered.StableDelegates) != 1 || filtered.StableDelegates["dlg_1"].descriptor.ChildSessionID != "child" {
		t.Fatalf("filtered stable delegates = %+v", filtered.StableDelegates)
	}
	if len(filtered.Jobs) != 0 {
		t.Fatalf("filtered jobs = %v, want empty (filtered to delegate only)", filtered.Jobs)
	}
	if len(filtered.Children) != 1 || filtered.Children["child"] != child {
		t.Fatalf("filtered children = %+v, want the one child", filtered.Children)
	}
	if len(filtered.Diagnostics) != 1 || filtered.Diagnostics[0] != "diag1" {
		t.Fatalf("diagnostics = %v", filtered.Diagnostics)
	}
	if filtered.SessionID != "root" || filtered.Revision != 7 {
		t.Fatalf("filtered identity lost: %+v", filtered)
	}
	// Missing delegateID results in no stable delegates or children.
	filtered2 := activityFilterSnapshotToDelegate(base, "dlg_missing", child)
	if len(filtered2.StableDelegates) != 0 || len(filtered2.Children) != 0 {
		t.Fatalf("missing delegate should produce empty maps")
	}
}

// TestJobActivityTree_LiveContinuationRejectedAfterRevisionChanges is the
// end-to-end red test for roborev's r6 finding "live pagination lacks
// cross-request revision guard": a live root's JobsEpoch/DelegatesEpoch are
// always 0 (loadLiveActivityBase reads neither fold cache), so the epoch
// check in loadActivitySnapshotForParamsWithCache provides no protection
// at all for a live continuation -- 0 == 0 always passes, even across a
// real mutation. This mints a valid live continuation (Path nil, matching
// markActivitySessionTruncated's own shape for the root's own list),
// forces a real mutation on the live tree (bumping jobActivityClock the
// same way an actual job-started/job-finished event would), and asserts
// resuming with the now-stale continuation is rejected -- proving the new
// Revision check (jobs_activity.go's loadActivitySnapshotForParamsWithCache)
// actually fires, not just that continuations carry the field.
func TestJobActivityTree_LiveContinuationRejectedAfterRevisionChanges(t *testing.T) {
	stateDir := t.TempDir()
	s := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir}),
		withoutGitSnapshot(),
	)
	if s.jobActivityClock == nil {
		t.Fatal("expected a top-level live session to have a jobActivityClock")
	}
	cont := activityContinuation{
		Version:   activityContinuationV1,
		RootID:    s.ID(),
		SessionID: s.ID(),
		Revision:  activityCurrentRootRevision(s.jobActivityClock),
	}
	token := encodeActivityContinuation(cont)
	if token == "" {
		t.Fatal("expected a non-empty encoded continuation")
	}

	// A real mutation on the live tree (the shape a job-started/finished
	// event actually produces) bumps the clock's revision, making the
	// continuation minted above stale.
	s.jobActivityClock.nextRevision()

	if _, err := s.JobActivityTree(appwire.JobsListParams{Continuation: token}); err == nil {
		t.Fatal("expected the stale live continuation to be rejected")
	} else if !strings.Contains(err.Error(), "live session changed") {
		t.Fatalf("error = %v, want a live-session-changed staleness error", err)
	}

	// Sanity check the positive case: a continuation minted against the
	// CURRENT revision is accepted.
	fresh := activityContinuation{
		Version:   activityContinuationV1,
		RootID:    s.ID(),
		SessionID: s.ID(),
		Revision:  activityCurrentRootRevision(s.jobActivityClock),
	}
	if _, err := s.JobActivityTree(appwire.JobsListParams{Continuation: encodeActivityContinuation(fresh)}); err != nil {
		t.Fatalf("continuation minted against the current revision was rejected: %v", err)
	}
}
