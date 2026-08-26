package agent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil)
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
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil)
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
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil)
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
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil)
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
	budget := newBoundedActivityBudget("root", time.Unix(1, 0).UTC())
	// maxDepth is 32; set depth to 32 so the child is truncated. The path must
	// not contain the delegate id yet: appendActivityPath adds row.id inside
	// the projection, and decodeActivityContinuation rejects duplicate hops.
	delegate := projectStableActivityDelegate(snap, row, budget, 32, nil)
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
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil)
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
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil)
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
	delegate := projectStableActivityDelegate(snap, row, newActivityBudget(), 0, nil)
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
	bounded := newBoundedActivityBudget("root", time.Unix(1, 0).UTC())
	bounded.usedWork = bounded.maxWorkUnits
	if activityConsumeWorkUnit(bounded, 1) {
		t.Error("budget at limit should refuse")
	}
	// Negative units clamp to 0.
	bounded2 := newBoundedActivityBudget("root", time.Unix(1, 0).UTC())
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
	got, err := trimActivityTreeToFit(tree, "root")
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
	got, err := trimActivityTreeToFit(tree, "root")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Root.Branch.Truncated {
		t.Fatal("expected tree to be truncated after trimming")
	}
	if got.Root.Branch.Continuation == "" {
		t.Fatal("expected continuation token after trimming")
	}
	cont, err := decodeActivityContinuation(got.Root.Branch.Continuation, "root")
	if err != nil {
		t.Fatalf("decode continuation: %v", err)
	}
	if cont.Revision != tree.Revision {
		t.Fatalf("continuation revision = %d, want %d", cont.Revision, tree.Revision)
	}
	if len(got.Root.Entries) >= 600 {
		t.Fatalf("expected fewer entries after trimming, got %d", len(got.Root.Entries))
	}
}

func TestTrimActivityTrailingEntry_EmptyReturnsFalse(t *testing.T) {
	t.Parallel()
	session := &appwire.JobActivitySession{SessionID: "root"}
	if trimActivityTrailingEntry(session, "root", nil) {
		t.Error("empty entries should return false")
	}
	if trimActivityTrailingEntry(nil, "root", nil) {
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
	if !trimActivityTrailingEntry(session, "root", nil) {
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

func TestEncodeActivityContinuation(t *testing.T) {
	t.Parallel()
	// A valid continuation round-trips.
	cont := activityContinuation{Version: activityContinuationV2, RootID: "root", SessionID: "root", Revision: 7, Source: activitySourceHistorical, Generation: activityServingGeneration, Path: []string{"dlg_1"}}
	encoded := encodeActivityContinuation(cont)
	if encoded == "" {
		t.Fatal("encoded continuation is empty")
	}
	decoded, err := decodeActivityContinuation(encoded, "root")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(decoded.Path, cont.Path) || decoded.SessionID != cont.SessionID || decoded.Revision != cont.Revision {
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

func TestValidateActivityContinuationRevisionRequiresRestart(t *testing.T) {
	t.Parallel()
	cont := activityContinuation{Version: activityContinuationV1, RootID: "root", SessionID: "root", Revision: 1}
	err := validateActivityContinuationRevision(cont, 2)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("revision mismatch error = %T %v, want conflict", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorStaleContinuation || data.Cause != "restartFromRoot" {
		t.Fatalf("revision mismatch data = %#v, want structured stale restart", wire.Data)
	}
	if !strings.Contains(wire.Message, "restart") {
		t.Fatalf("revision mismatch message = %q, want restart guidance", wire.Message)
	}
}

func TestValidateActivityContinuationIdentityRejectsSourceHandoff(t *testing.T) {
	t.Parallel()
	cont := activityContinuation{Version: activityContinuationV2, Source: activitySourceHistorical, Generation: "g1"}
	err := validateActivityContinuationIdentity(cont, activityContinuationIdentity{Source: activitySourceLive, Generation: "g1"})
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("handoff error = %T %v, want WireError", err, err)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorStaleContinuation || data.Cause != "restartFromRoot" || data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Fatalf("handoff data = %#v, want structured stale restart", wire.Data)
	}
}

func TestValidateActivityContinuationIdentityRejectsServingRestartAtSameRevision(t *testing.T) {
	t.Parallel()
	cont := activityContinuation{Version: activityContinuationV2, Revision: 7, Source: activitySourceLive, Generation: "old-serving-generation"}
	err := validateActivityContinuationIdentity(cont, activityContinuationIdentity{Source: activitySourceLive, Generation: "new-serving-generation"})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
		t.Fatalf("restart error = %T %v, want typed conflict", err, err)
	}
}

func TestActivityContinuationV2AuthenticatesIdentityAndRejectsLegacy(t *testing.T) {
	t.Parallel()
	cont := activityContinuation{
		Version: activityContinuationV2, RootID: "root", SessionID: "root",
		Revision: 0, Source: activitySourceHistorical, Generation: "generation-1",
	}
	token := encodeActivityContinuation(cont)
	decoded, err := decodeActivityContinuation(token, "root")
	if err != nil || decoded.Source != cont.Source || decoded.Generation != cont.Generation {
		t.Fatalf("v2 round trip = %+v, err=%v", decoded, err)
	}
	outerRaw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	var outer activityContinuationEnvelope
	if err := json.Unmarshal(outerRaw, &outer); err != nil {
		t.Fatal(err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(outer.Payload)
	if err != nil {
		t.Fatal(err)
	}
	payload = []byte(strings.Replace(string(payload), `"revision":0`, `"revision":1`, 1))
	outer.Payload = base64.RawURLEncoding.EncodeToString(payload)
	outerRaw, err = json.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeActivityContinuation(base64.RawURLEncoding.EncodeToString(outerRaw), "root"); err == nil {
		t.Fatal("semantic valid-JSON cursor tamper accepted")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 1
	if _, err := decodeActivityContinuation(base64.RawURLEncoding.EncodeToString(raw), "root"); err == nil {
		t.Fatal("tampered continuation accepted")
	} else {
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
			t.Fatalf("tampered continuation error = %T %v, want typed conflict", err, err)
		}
	}
	legacy := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"root":"root","session":"root","path":[]}`))
	if _, err := decodeActivityContinuation(legacy, "root"); err == nil {
		t.Fatal("legacy v1 continuation accepted")
	} else {
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeConflict {
			t.Fatalf("legacy continuation error = %T %v, want typed conflict", err, err)
		}
	}
	for _, raw := range []string{
		`{"v":2,"root":"root","session":"root","source":"historical","generation":"g","path":[]}`,
		`{"v":2,"root":"root","session":"root","revision":null,"source":"historical","generation":"g","path":[]}`,
	} {
		if _, err := decodeActivityContinuation(base64.RawURLEncoding.EncodeToString([]byte(raw)), "root"); err == nil {
			t.Fatalf("unsigned omitted/null revision continuation accepted: %s", raw)
		}
	}
	signed := func(payload string) string {
		mac := hmac.New(sha256.New, activityCursorSecret)
		_, _ = mac.Write([]byte(payload))
		envelope, err := json.Marshal(activityContinuationEnvelope{
			Version:   activityContinuationV2,
			Payload:   base64.RawURLEncoding.EncodeToString([]byte(payload)),
			Signature: hex.EncodeToString(mac.Sum(nil)),
		})
		if err != nil {
			t.Fatal(err)
		}
		return base64.RawURLEncoding.EncodeToString(envelope)
	}
	for _, payload := range []string{
		`{"v":2,"root":"root","session":"root","source":"historical","generation":"g","path":[]}`,
		`{"v":2,"root":"root","session":"root","revision":null,"source":"historical","generation":"g","path":[]}`,
		`{"v":2,"root":"root","session":"root","revision":0,"source":"historical","generation":"g","path":[],"extra":1}`,
		`{"v":3,"root":"root","session":"root","revision":0,"source":"historical","generation":"g","path":[]}`,
		`{"v":2,"root":"root","root":"root","session":"root","revision":0,"source":"historical","generation":"g","path":[]}`,
	} {
		if _, err := decodeActivityContinuation(signed(payload), "root"); err == nil {
			t.Fatalf("authenticated malformed cursor accepted: %s", payload)
		}
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
