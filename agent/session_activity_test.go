package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestSessionActivityRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot())
	for _, ref := range []string{"", ":::bad", "local:unrelated", "remote:host:session"} {
		_, err := s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: ref})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Errorf("ref %q: error=%v, want structured invalid params", ref, err)
		}
	}
	ref := encodeRef("", s.ID())
	if _, err := s.ListActivityJobs(t.Context(), appwire.SessionActivityListParams{Ref: ref, Limit: -1}); err == nil {
		t.Fatal("negative limit accepted")
	}
	if _, err := s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: ref, Scope: "unknown"}); err == nil {
		t.Fatal("unknown scope accepted")
	}
}

func TestSessionActivityDelegateIdentityWithoutRuntime(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot())
	seedStableToolRunningDelegate(t, s, "dlg_retained", "", time.Unix(100, 0).UTC())
	response, err := s.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{Ref: encodeRef("", s.ID())})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Delegates) != 1 {
		t.Fatalf("delegates=%d, want 1", len(response.Delegates))
	}
	row := response.Delegates[0]
	if row.DelegateID != "dlg_retained" || row.OwnerRef != encodeRef("", s.ID()) || row.ChildRef == "" {
		t.Fatalf("stable identity lost: %+v", row)
	}
	if !response.Page.Complete {
		t.Fatalf("authoritative controller page incomplete: %+v", response.Page)
	}
}

func TestSessionActivityCanceledFoldResumesWithoutDuplicateCreation(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "canceledfold"
	savePastActivityMeta(t, stateDir, id, "Root")
	writeActivityJobLogFast(t, stateDir, id, 31)
	index, err := acquireSessionActivityIndex(t.Context(), stateDir+"\x00"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	index.release()
	ctx := &sessionActivityCancelDuringFold{Context: t.Context(), index: index, owner: id}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", id)}
	if _, err = LoadSessionActivityJobs(ctx, stateDir, id, params); !errors.Is(err, context.Canceled) {
		t.Fatalf("fold cancellation=%v", err)
	}
	response, err := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Jobs) != 31 || !response.Page.Complete {
		t.Fatalf("resume lost accepted events: rows=%d page=%+v", len(response.Jobs), response.Page)
	}
}

type sessionActivityCancelDuringFold struct {
	context.Context
	index     *sessionActivityIndex
	owner     string
	delegates bool
}

func (ctx *sessionActivityCancelDuringFold) Err() error {
	if ctx.delegates && len(ctx.index.delegates) >= 5 {
		return context.Canceled
	}
	if source := ctx.index.jobs[ctx.owner]; source != nil && len(source.Jobs) >= 5 {
		return context.Canceled
	}
	return ctx.Context.Err()
}

func TestSessionActivity451JobsPreserveMembershipAndCurrentStatus(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "pagingjobs"
	savePastActivityMeta(t, stateDir, id, "Root")
	path := writeActivityJobLogFast(t, stateDir, id, 451)
	params := appwire.SessionActivityListParams{Ref: encodeRef("", id), Limit: 73}
	first, err := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Jobs) != 73 || first.Page.Complete {
		t.Fatalf("first rows=%d page=%+v", len(first.Jobs), first.Page)
	}
	store, err := jobstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Unix(1_700_000_000, 0).UTC()
	if err = store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_new", Type: jobstore.JobShell, Background: true, OwnerSessionID: id, TS: at, StartedAt: &at}); err != nil {
		t.Fatal(err)
	}
	if err = store.Append(jobstore.Event{Kind: jobstore.EventJobFinished, JobID: "job_000000", Status: jobstore.StatusFailed, TerminalGen: "failed", TS: at}); err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool)
	for _, row := range first.Jobs {
		seen[row.JobID] = true
	}
	params.Cursor = first.Page.NextCursor
	updated := false
	for attempts := 0; params.Cursor != "" && attempts < 20; attempts++ {
		page, readErr := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, row := range page.Jobs {
			if seen[row.JobID] {
				t.Fatalf("duplicate %s", row.JobID)
			}
			seen[row.JobID] = true
			if row.JobID == "job_000000" {
				updated = row.Status == "failed"
			}
		}
		params.Cursor = page.Page.NextCursor
	}
	if len(seen) != 451 || seen["job_new"] || !updated {
		t.Fatalf("membership=%d new=%v updated=%v", len(seen), seen["job_new"], updated)
	}
	params.Cursor = ""
	fresh, err := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Jobs) == 0 || fresh.Jobs[0].JobID != "job_new" {
		t.Fatalf("refresh did not include new member: %+v", fresh)
	}
}

func TestSessionActivityWatchReceiverOwnership(t *testing.T) {
	t.Parallel()
	fixture := newStableWatchRuntimeBase(t, nil)
	fixture.source.events = make(chan events.SessionEvent, 64)
	fixture.sourceJM.retirementOwner = fixture.source
	args := watchArgs{Source: "dlg_source", Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, ReceiverSessionID: fixture.root.ID(), ReceiverNotify: func(jobNotification) {}}
	result, err := fixture.sourceJM.configureWatch(args)
	if err != nil {
		t.Fatal(err)
	}
	requireSessionActivityInvalidation(t, fixture.source.events, fixture.root.ID(), fixture.root.ID(), appwire.SessionActivityResourceWatches)
	params := appwire.SessionActivityListParams{Ref: encodeRef("", fixture.root.ID())}
	page, err := fixture.root.ListActivityWatches(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Watches) != 1 || page.Watches[0].Watch.ID != result.WatchID || page.Watches[0].State != appwire.SessionWatchStateArmed || page.Watches[0].ReceiverRef != params.Ref || page.Watches[0].OwnerRef != params.Ref || page.Watches[0].SourceRef != encodeRef("", fixture.source.ID()) {
		t.Fatalf("receiver row=%+v", page)
	}
	childPage, err := fixture.root.ListActivityWatches(t.Context(), appwire.SessionActivityListParams{Ref: encodeRef("", fixture.source.ID())})
	if err != nil {
		t.Fatal(err)
	}
	if len(childPage.Watches) != 0 || !childPage.Page.Complete {
		t.Fatalf("source inherited receiver watch: %+v", childPage)
	}
	onSessionEventKD(fixture.sourceJM, events.EventCommunicate, events.CommunicateData{Message: "delivery"})
	requireSessionActivityInvalidation(t, fixture.source.events, fixture.root.ID(), fixture.root.ID(), appwire.SessionActivityResourceWatches)
	page, err = fixture.root.ListActivityWatches(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if page.Watches[0].Watch.Deliveries != 1 {
		t.Fatalf("delivery count=%+v", page)
	}
	if _, err = fixture.sourceJM.clearReceiverWatchByID(result.WatchID, fixture.root.ID(), ""); err != nil {
		t.Fatal(err)
	}
	requireSessionActivityInvalidation(t, fixture.source.events, fixture.root.ID(), fixture.root.ID(), appwire.SessionActivityResourceWatches)
	page, err = fixture.root.ListActivityWatches(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Watches) != 1 || page.Watches[0].State != appwire.SessionWatchStateEnded || page.Watches[0].Watch.EndReason == "" || page.Watches[0].Watch.Deliveries != 1 {
		t.Fatalf("end lost: %+v", page)
	}
}

func requireSessionActivityInvalidation(t *testing.T, stream <-chan events.SessionEvent, target, owner string, resource appwire.SessionActivityResource) {
	t.Helper()
	for {
		select {
		case event := <-stream:
			if payload, ok := event.Data.(events.SessionActivityChangedData); ok && payload.ThreadID == target && payload.SessionID == owner && slices.Contains(payload.Resources, resource) {
				if payload.Ref != encodeRef("", target) || event.SessionID == "" {
					t.Fatalf("invalid bridge routing: %+v", event)
				}
				return
			}
		default:
			t.Fatalf("missing %s invalidation target=%s owner=%s", resource, target, owner)
		}
	}
}

func TestSessionActivityColdProgressAndWarmReadCost(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "coldprogress"
	savePastActivityMeta(t, stateDir, id, "Root")
	writeActivityJobLogFast(t, stateDir, id, 2001)
	params := appwire.SessionActivityListParams{Ref: encodeRef("", id), Limit: 200}
	summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, appwire.SessionActivityReadParams{Ref: params.Ref})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Jobs.Known {
		t.Fatal("cold badge pretended to know history")
	}
	index, err := acquireSessionActivityIndex(t.Context(), stateDir+"\x00"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.jobs) != 0 || index.delegateCursor.Journal.Offset != 0 {
		t.Fatal("summary scanned activity journals")
	}
	index.release()
	first, err := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Jobs) != 0 || first.Page.Complete || first.Page.NextCursor == "" {
		t.Fatalf("cold prefix published as current outcome: %+v", first)
	}
	if count := len(index.jobs[id].Jobs); count != 2000 {
		t.Fatalf("cold work count=%d", count)
	}
	params.Cursor = first.Page.NextCursor
	second, err := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Jobs) != 200 || second.Page.NextCursor == params.Cursor {
		t.Fatalf("continuation failed to progress: %+v", second.Page)
	}
	offset := index.jobs[id].Cursor.Journal.Offset
	scanCalls := index.scanCalls
	params.Cursor = second.Page.NextCursor
	if _, err = LoadSessionActivityJobs(t.Context(), stateDir, id, params); err != nil {
		t.Fatal(err)
	}
	if index.jobs[id].Cursor.Journal.Offset != offset || index.scanCalls != scanCalls {
		t.Fatal("warm page rescanned journal records")
	}
}

func TestSessionActivityCursorsBindResourceScopeAndIncarnation(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "cursoridentity"
	savePastActivityMeta(t, stateDir, id, "Root")
	path := writeActivityJobLogFast(t, stateDir, id, 3)
	params := appwire.SessionActivityListParams{Ref: encodeRef("", id), Limit: 1}
	page, err := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
	if err != nil {
		t.Fatal(err)
	}
	params.Cursor = page.Page.NextCursor
	_, err = LoadSessionActivityWatches(t.Context(), stateDir, id, params)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("cross-resource=%v", err)
	}
	other := params
	other.Scope = appwire.SessionActivityScopeSubtree
	if _, err = LoadSessionActivityJobs(t.Context(), stateDir, id, other); err == nil {
		t.Fatal("cross-scope cursor accepted")
	}
	replacement := path + ".replacement"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(replacement, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	_, err = LoadSessionActivityJobs(t.Context(), stateDir, id, params)
	if !errors.As(err, &wire) || wire.Data == nil {
		t.Fatalf("replacement did not stale cursor: %v", err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadSessionActivityJobs(t.Context(), stateDir, id, params); err == nil {
		t.Fatal("deleted source accepted old cursor")
	}
}

func TestSessionActivityLargeProseKeepsEveryJobReachable(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "largeprose"
	savePastActivityMeta(t, stateDir, id, "Root")
	dir := jobsDir(stateDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.Open(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	prose := strings.Repeat("界", 7000)
	at := time.Unix(100, 0).UTC()
	batch := make([]jobstore.Event, 451)
	for i := range batch {
		batch[i] = jobstore.Event{Kind: jobstore.EventJobStarted, JobID: fmt.Sprintf("job_large_%04d", i), Type: jobstore.JobShell, Background: true, OwnerSessionID: id, TS: at, StartedAt: &at, Command: prose, Description: prose, Task: prose}
	}
	if err = store.AppendBatch(batch); err != nil {
		t.Fatal(err)
	}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", id), Limit: 200}
	seen := make(map[string]bool)
	for range 100 {
		page, readErr := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
		if readErr != nil {
			t.Fatal(readErr)
		}
		raw, marshalErr := json.Marshal(page)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if len(raw) > sessionActivityPageBytes {
			t.Fatalf("response bytes=%d", len(raw))
		}
		for _, row := range page.Jobs {
			if seen[row.JobID] {
				t.Fatalf("duplicate %s", row.JobID)
			}
			seen[row.JobID] = true
		}
		if page.Page.Complete {
			break
		}
		if page.Page.NextCursor == "" || page.Page.NextCursor == params.Cursor {
			t.Fatal("byte-reduced page stalled")
		}
		params.Cursor = page.Page.NextCursor
	}
	if len(seen) != 451 {
		t.Fatalf("reachable=%d", len(seen))
	}
}

func TestSessionActivitySummaryFailedShellAndUnknownRetainedWatch(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "summaryfacts"
	savePastActivityMeta(t, stateDir, id, "Root")
	dir := jobsDir(stateDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.Open(filepath.Join(dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	at := time.Unix(100, 0).UTC()
	exit := 7
	if err = store.AppendBatch([]jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: "job_failed", Type: jobstore.JobShell, Background: true, OwnerSessionID: id, TS: at, StartedAt: &at},
		{Kind: jobstore.EventJobFinished, JobID: "job_failed", Status: jobstore.StatusCommandExitedNonzero, ExitCode: &exit, TerminalGen: "failure", TS: at},
		{Kind: jobstore.EventWatchRegistered, WatchID: "watch_unknown", TS: at, Watch: &jobstore.WatchEvent{Generation: "g", OwnerSessionID: id, VisibleSessionID: id, Target: "job_failed", ConfigHash: "hash", Config: &jobstore.WatchConfigSnapshot{Target: "job_failed", Events: []string{"job.notification"}}}},
	}); err != nil {
		t.Fatal(err)
	}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", id)}
	if _, err = LoadSessionActivityJobs(t.Context(), stateDir, id, params); err != nil {
		t.Fatal(err)
	}
	watches, err := LoadSessionActivityWatches(t.Context(), stateDir, id, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(watches.Watches) != 1 || watches.Watches[0].State != appwire.SessionWatchStateUnknown {
		t.Fatalf("retained registration falsely armed: %+v", watches)
	}
	summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, appwire.SessionActivityReadParams{Ref: params.Ref})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Jobs.Known || summary.Jobs.Failed != 1 || summary.Jobs.Completed != 0 {
		t.Errorf("failed shell count=%+v", summary.Jobs)
	}
	if summary.Watches.Known {
		t.Errorf("unknown registration claimed known armed count: %+v", summary.Watches)
	}
	if err = store.Append(jobstore.Event{Kind: jobstore.EventWatchCleared, WatchID: "watch_unknown", TS: at, Watch: &jobstore.WatchEvent{Generation: "g", EndReason: "cleared"}}); err != nil {
		t.Fatal(err)
	}
	if _, err = LoadSessionActivityWatches(t.Context(), stateDir, id, params); err != nil {
		t.Fatal(err)
	}
	summary, err = LoadSessionActivitySummary(t.Context(), stateDir, id, appwire.SessionActivityReadParams{Ref: params.Ref})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Watches.Known || summary.Watches.Completed != 1 || summary.Watches.Active != 0 {
		t.Fatalf("proven end count=%+v", summary.Watches)
	}
}

// realDelegateTree is a root session whose one delegate (parent) spawned one
// delegate of its own (grandchild), both run to completion: three sessions
// deep, built by the real delegate runtime. descendantEvents, when set, sees
// every event the tree's subagent sessions emit, as the daemon's AppWire
// bridge does.
type realDelegateTree struct {
	s                   *Session
	c                   *delegateTreeController
	parent              delegateResult
	parentRuntime       *Session
	grandchildID        string
	grandchildSessionID string
	grandchildRuntime   *Session
}

func newRealDelegateTree(t *testing.T, descendantEvents func(events.SessionEvent)) realDelegateTree {
	t.Helper()
	workspace := t.TempDir()
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 2, ForceRealIO: true, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}}), withSteps(
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{ID: "spawn-grandchild", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"grandchild activity task"}`), Type: "function"})
		},
		func(llm.Request) llm.Response { return finalResponse("grandchild done") },
		func(llm.Request) llm.Response { return finalResponse("parent done") },
	))
	if descendantEvents != nil {
		s.SetDescendantEventFunc(descendantEvents)
	}
	one := 1
	parent := s.createDelegate(t.Context(), delegateArgs{Task: "spawn one grandchild", DelegationAllowance: &one})
	if parent.Err != nil {
		t.Fatal(parent.Err)
	}
	sub := s.subagents.get(parent.ChildSessionID)
	if sub == nil {
		t.Fatal("parent runtime not created")
	}
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	select {
	case <-done:
	// TRIPWIRE: scripted local delegates finish in milliseconds; ten seconds only detects a stuck completion signal.
	case <-time.After(10 * time.Second):
		t.Fatal("scripted parent did not finish")
	}
	c := s.delegateController
	c.mu.Lock()
	parentRuntime := c.live[parent.DelegateID].runtime
	var grandchildID, grandchildSessionID string
	for id, aggregate := range c.durable {
		if aggregate.Descriptor.ParentDelegateID == parent.DelegateID {
			grandchildID = id
			grandchildSessionID = aggregate.Descriptor.ChildSessionID
		}
	}
	grandchildRuntime := c.live[grandchildID].runtime
	c.mu.Unlock()
	if grandchildID == "" || parentRuntime == nil || grandchildRuntime == nil {
		t.Fatal("script did not create a real two-level delegate graph")
	}
	childSub := parentRuntime.subagents.get(grandchildSessionID)
	childSub.mu.Lock()
	childDone := childSub.done
	childSub.mu.Unlock()
	select {
	case <-childDone:
	// TRIPWIRE: scripted local delegates finish in milliseconds; ten seconds only detects a stuck completion signal.
	case <-time.After(10 * time.Second):
		t.Fatal("scripted grandchild did not finish")
	}
	return realDelegateTree{s: s, c: c, parent: parent, parentRuntime: parentRuntime, grandchildID: grandchildID, grandchildSessionID: grandchildSessionID, grandchildRuntime: grandchildRuntime}
}

// releaseMiddle releases the settled middle subagent's runtime, and its
// grandchild's with it, through the real idle-release path.
func (tree realDelegateTree) releaseMiddle(t *testing.T) {
	t.Helper()
	claim := claimSettledIdleSubtree(t, tree.c, tree.parent.DelegateID, tree.parentRuntime, tree.grandchildRuntime)
	if err := tree.c.AbortRuntimeReclamation(claim); err != nil {
		t.Fatal(err)
	}
	if !tree.parentRuntime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("real idle subtree release refused")
	}
}

func TestSessionActivityRealDelegateTree(t *testing.T) {
	t.Parallel()
	fixture := newRealDelegateTree(t, nil)
	s, c, parent := fixture.s, fixture.c, fixture.parent
	grandchildID, grandchildSessionID := fixture.grandchildID, fixture.grandchildSessionID
	params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID())}
	direct, err := s.ListActivityDelegates(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(direct.Delegates) != 1 || direct.Delegates[0].DelegateID != parent.DelegateID {
		t.Fatalf("root direct=%+v", direct)
	}
	params.Scope = appwire.SessionActivityScopeSubtree
	tree, err := s.ListActivityDelegates(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Delegates) != 2 || !tree.Page.Complete {
		t.Fatalf("root subtree=%+v", tree)
	}
	child, err := s.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{Ref: encodeRef("", parent.ChildSessionID)})
	if err != nil {
		t.Fatal(err)
	}
	if len(child.Delegates) != 1 || child.Delegates[0].DelegateID != grandchildID || child.Context.ParentRef != encodeRef("", s.ID()) || !child.Context.AncestryKnown {
		t.Fatalf("child identity=%+v", child)
	}
	leaf, err := s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: encodeRef("", grandchildSessionID)})
	if err != nil {
		t.Fatal(err)
	}
	if leaf.Context.DelegateID != grandchildID || len(leaf.Context.Ancestors) != 2 || leaf.Context.Ancestors[1].SessionID != parent.ChildSessionID {
		t.Fatalf("grandchild context=%+v", leaf.Context)
	}
	// The physical root bridge publishes each authoritative subscription target
	// while retaining the affected child as the logical owner.
	c.rootRuntime.emitSessionActivityChanged(grandchildSessionID, appwire.SessionActivityResourceJobs)
	for _, target := range []string{grandchildSessionID, parent.ChildSessionID, s.ID()} {
		requireSessionActivityInvalidation(t, s.events, target, grandchildSessionID, appwire.SessionActivityResourceJobs)
	}
	fixture.releaseMiddle(t)
	retained, err := s.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{Ref: encodeRef("", parent.ChildSessionID)})
	if err != nil {
		t.Fatal(err)
	}
	if retained.Context.Availability != "retained" || len(retained.Delegates) != 1 || retained.Delegates[0].DelegateID != grandchildID {
		t.Fatalf("missing runtime erased stable graph: %+v", retained)
	}
}

func TestSessionActivityMultiJournalRawInputBudget(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, sandboxProber: bwrapCapableProber(workspace)}}))
	c := s.delegateController
	at := time.Unix(100, 0).UTC()
	const sources = 9
	created := make([]delegatestore.Event, 0, sources)
	for i := range sources {
		id := fmt.Sprintf("dlg_budget_%03d", i)
		descriptor := stableToolDescriptor(s, id, "")
		created = append(created, delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: id, TS: at, Created: &delegatestore.DelegateCreated{Descriptor: descriptor}})
		savePastActivityMetaWithTreeRevision(t, s.stateDir, descriptor.ChildSessionID, "Child", s.ID(), 0)
		dir := jobsDir(s.stateDir, descriptor.ChildSessionID)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		store, err := jobstore.Open(filepath.Join(dir, "jobs.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		event := jobstore.Event{Kind: jobstore.EventJobStarted, JobID: fmt.Sprintf("job_budget_%03d", i), Type: jobstore.JobShell, Background: true, OwnerSessionID: descriptor.ChildSessionID, TS: at, StartedAt: &at, Command: strings.Repeat("x", (1<<20)+17)}
		if err = store.Append(event); err != nil {
			t.Fatal(err)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	c.mu.Lock()
	_, err := c.appendLocked(created...)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Scope: appwire.SessionActivityScopeSubtree}
	index, err := acquireSessionActivityIndex(t.Context(), s.stateDir+"\x00"+s.ID(), c)
	if err != nil {
		t.Fatal(err)
	}
	index.release()
	var seen int
	for range 12 {
		before := index.rawBytes
		page, readErr := s.ListActivityJobs(t.Context(), params)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if readBytes := index.rawBytes - before; readBytes > 4<<20 {
			t.Fatalf("shared actual raw input=%d", readBytes)
		}
		seen += len(page.Jobs)
		if page.Page.Complete {
			break
		}
		if page.Page.NextCursor == "" || page.Page.NextCursor == params.Cursor {
			t.Fatal("multi-source byte budget did not advance")
		}
		params.Cursor = page.Page.NextCursor
	}
	if seen != sources {
		t.Fatalf("reachable multi-source jobs=%d", seen)
	}
}

func TestSessionActivitySummaryUsesDelegateTerminalClassifier(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot())
	c := s.delegateController
	at := time.Unix(100, 0).UTC()
	finish := delegateRunFinishedEvent(delegateLease{delegateID: "dlg_exhausted", generation: 1}, delegatestore.OutcomeExhausted, delegatestore.DispositionTerminalError, "turn_budget_exhausted", at, delegateDeliveryID("dlg_exhausted", 1), &delegatestore.TerminalPacket{Kind: delegatestore.PacketTerminalError, Message: json.RawMessage(`"exhausted"`)})
	resumable := false
	finish.RunFinished.Outcome.ExhaustionBudget = delegatestore.ExhaustionBudgetTurns
	finish.RunFinished.Outcome.ExhaustionLimit = 1
	finish.RunFinished.Outcome.Resumable = &resumable
	c.mu.Lock()
	_, err := c.appendLocked(
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_pending", Created: &delegatestore.DelegateCreated{Descriptor: stableToolDescriptor(s, "dlg_pending", "")}},
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: "dlg_exhausted", Created: &delegatestore.DelegateCreated{Descriptor: stableToolDescriptor(s, "dlg_exhausted", "")}},
		delegateControllerRunStartedEvent("dlg_exhausted", 1, delegatestore.TriggerInitial, at),
		finish,
	)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: encodeRef("", s.ID())})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Delegates.Known || got.Delegates.Total != 2 || got.Delegates.Active != 1 || got.Delegates.Failed != 1 || got.Delegates.Completed != 0 {
		t.Fatalf("terminal classifier drift: %+v", got.Delegates)
	}
	page, err := s.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{Ref: encodeRef("", s.ID())})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range page.Delegates {
		if row.DelegateID == "dlg_pending" && (row.Terminal || row.Outcome != "") {
			t.Fatalf("created-only row claimed terminal: %+v", row)
		}
		if row.DelegateID == "dlg_exhausted" && (!row.Terminal || activityDelegateOutcome(row.Outcome) != "failure") {
			t.Fatalf("exhausted row disagrees with count: %+v", row)
		}
	}
}

func TestSessionActivityRetainedAncestryMakesBoundedProgress(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootID := "ancestryroot"
	savePastActivityMeta(t, stateDir, rootID, "Root")
	dir := jobsDir(stateDir, rootID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := delegatestore.Open(filepath.Join(dir, "delegates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	batch := make([]delegatestore.Event, 2001)
	for i := range batch {
		id := fmt.Sprintf("dlg_ancestry_%04d", i)
		batch[i] = delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: id, TS: time.Unix(int64(i), 0).UTC(), Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{OwnerSessionID: rootID, VisibleSessionID: rootID, ChildSessionID: "child-" + id, TranscriptRef: "local:child-" + id, ResolvedModel: "gpt-5.2", AgentType: "general", Task: "retained task", Resumable: true, ToolNameCeiling: []string{"communicate"}}}}
	}
	if _, _, err = store.AppendBatch(make(delegatestore.State), batch); err != nil {
		t.Fatal(err)
	}
	childID := batch[2000].Created.Descriptor.ChildSessionID
	savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Child", rootID, 0)
	params := appwire.SessionActivityReadParams{Ref: encodeRef("", childID)}
	first, err := LoadSessionActivitySummary(t.Context(), stateDir, childID, params)
	if err != nil {
		t.Fatal(err)
	}
	if first.Context.AncestryKnown || first.Context.ParentRef != "" || len(first.Context.Ancestors) != 0 || first.Delegates.Known || first.Context.RootRef != encodeRef("", rootID) {
		t.Fatalf("pending ancestry implied a root/known collection: %+v", first)
	}
	second, err := LoadSessionActivitySummary(t.Context(), stateDir, childID, params)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Context.AncestryKnown || second.Context.ParentRef != encodeRef("", rootID) || second.Context.DelegateID != batch[2000].DelegateID || second.Context.Epoch != first.Context.Epoch {
		t.Fatalf("bounded ancestry did not converge: %+v", second)
	}
	page, err := LoadSessionActivityDelegates(t.Context(), stateDir, rootID, appwire.SessionActivityListParams{Ref: encodeRef("", rootID), Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Delegates) != 1 || page.Delegates[0].ChildRef != params.Ref {
		t.Fatalf("retained owner mismatch: %+v", page)
	}
}

func TestSessionActivityCorruptRetainedSourceIsUnavailable(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootID := "corruptroot"
	savePastActivityMeta(t, stateDir, rootID, "Root")
	path := filepath.Join(jobsDir(stateDir, rootID), "delegates.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{invalid}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	page, err := LoadSessionActivityDelegates(t.Context(), stateDir, rootID, appwire.SessionActivityListParams{Ref: encodeRef("", rootID)})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable || page.Page.Complete {
		t.Fatalf("corruption claimed empty success: page=%+v error=%v", page, err)
	}
	childID := "missinglineage"
	savePastActivityMetaWithTreeRevision(t, stateDir, childID, "Child", rootID, 0)
	if _, err = LoadSessionActivitySummary(t.Context(), stateDir, childID, appwire.SessionActivityReadParams{Ref: encodeRef("", childID)}); !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("unavailable ancestry=%v", err)
	}
}

func TestSessionActivityCacheLossAndCursorIdentity(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	id := "cacheloss"
	savePastActivityMeta(t, stateDir, id, "Root")
	writeActivityJobLogFast(t, stateDir, id, 3)
	params := appwire.SessionActivityListParams{Ref: encodeRef("", id), Limit: 1}
	first, err := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
	if err != nil {
		t.Fatal(err)
	}
	params.Cursor = first.Page.NextCursor
	otherID := "othercursorowner"
	savePastActivityMeta(t, stateDir, otherID, "Other")
	other := params
	other.Ref = encodeRef("", otherID)
	_, err = LoadSessionActivityJobs(t.Context(), stateDir, otherID, other)
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("cross-session cursor=%v", err)
	}
	malformed := params
	malformed.Cursor = "not-an-authenticated-cursor"
	if _, err = LoadSessionActivityJobs(t.Context(), stateDir, id, malformed); !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("malformed cursor=%v", err)
	}
	// Discard only this fixture's disposable entry, as the bounded LRU does.
	key := stateDir + "\x00" + id
	sessionActivityIndexes.Lock()
	if cached := sessionActivityIndexes.entries[key]; cached != nil {
		delete(sessionActivityIndexes.entries, key)
		sessionActivityIndexes.order.Remove(cached.element)
	}
	sessionActivityIndexes.Unlock()
	_, err = LoadSessionActivityJobs(t.Context(), stateDir, id, params)
	if !errors.As(err, &wire) || wire.Data == nil || wire.Data.(appwire.ErrorData).EvenerErrorInfo != appwire.ErrorSessionActivityCursorStale {
		t.Fatalf("cache-loss cursor=%v", err)
	}
	params.Cursor = ""
	fresh, err := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Jobs) != 1 || fresh.Context.Epoch == first.Context.Epoch {
		t.Fatalf("cache-loss refresh=%+v", fresh)
	}
}

func TestSessionActivityDurableWatchClearInvalidatesReceiver(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name     string
		rejected bool
		own      bool
	}{{name: "receiver"}, {name: "rejected", rejected: true}, {name: "empty-receiver", own: true}} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			fixture := newStableWatchRuntimeBase(t, nil)
			fixture.source.events = make(chan events.SessionEvent, 64)
			fixture.sourceJM.retirementOwner = fixture.source
			target := fixture.root.ID()
			args := watchArgs{Source: "dlg_source", Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, ReceiverSessionID: target, ReceiverNotify: func(jobNotification) {}}
			if scenario.own {
				target = fixture.source.ID()
				args.ReceiverSessionID = ""
				args.ReceiverNotify = nil
			}
			result, err := fixture.sourceJM.configureWatch(args)
			if err != nil {
				t.Fatal(err)
			}
			requireSessionActivityInvalidation(t, fixture.source.events, target, target, appwire.SessionActivityResourceWatches)
			fixture.sourceJM.mu.Lock()
			for key, cfg := range fixture.sourceJM.watches {
				if cfg.id == result.WatchID {
					closeWatchConfig(cfg)
					delete(fixture.sourceJM.watches, key)
				}
			}
			fixture.sourceJM.mu.Unlock()
			if scenario.rejected {
				appendEvents := fixture.sourceJM.appendEvents
				t.Cleanup(func() { fixture.sourceJM.appendEvents = appendEvents })
				fixture.sourceJM.appendEvents = func([]jobstore.Event) error { return os.ErrPermission }
			}
			_, err = fixture.sourceJM.clearWatchByID(result.WatchID)
			if !scenario.rejected {
				if err != nil {
					t.Fatal(err)
				}
				requireSessionActivityInvalidation(t, fixture.source.events, target, target, appwire.SessionActivityResourceWatches)
				return
			}
			if !errors.Is(err, os.ErrPermission) {
				t.Fatalf("rejected clear=%v", err)
			}
			select {
			case event := <-fixture.source.events:
				t.Fatalf("rejected clear emitted mutation: %+v", event)
			default:
			}
			watches, err := fixture.sourceJM.store.LoadWatches()
			if err != nil || watches[result.WatchID] == nil || !watches[result.WatchID].Active {
				t.Fatalf("rejected clear changed authority: watches=%+v err=%v", watches, err)
			}
		})
	}
}

func TestSessionActivityDeepContextCannotExceedResponseBudget(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot())
	c := s.delegateController
	batch := make([]delegatestore.Event, 400)
	parentID := ""
	for i := range batch {
		id := fmt.Sprintf("dlg_deep_%04d", i)
		descriptor := stableToolDescriptor(s, id, parentID)
		descriptor.Description = strings.Repeat("界", 200)
		batch[i] = delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: id, Created: &delegatestore.DelegateCreated{Descriptor: descriptor}}
		parentID = id
	}
	c.mu.Lock()
	_, err := c.appendLocked(batch...)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", batch[len(batch)-1].Created.Descriptor.ChildSessionID)}
	queries := map[string]func() (any, error){
		"summary": func() (any, error) {
			return s.ActivitySummary(t.Context(), appwire.SessionActivityReadParams{Ref: params.Ref})
		},
		"delegates": func() (any, error) { return s.ListActivityDelegates(t.Context(), params) },
		"jobs":      func() (any, error) { return s.ListActivityJobs(t.Context(), params) },
		"watches":   func() (any, error) { return s.ListActivityWatches(t.Context(), params) },
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			page, readErr := query()
			raw, marshalErr := json.Marshal(page)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var wire appwire.WireError
			if !errors.As(readErr, &wire) || wire.Code != appwire.CodeUnavailable || len(raw) > sessionActivityPageBytes {
				t.Fatalf("oversized context: bytes=%d err=%v", len(raw), readErr)
			}
		})
	}
}

func TestSessionActivityCanceledDelegateFoldResumesWithoutReplay(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootID := "canceleddelegates"
	savePastActivityMeta(t, stateDir, rootID, "Root")
	descriptors := make([]delegatestore.Descriptor, 31)
	for i := range descriptors {
		descriptors[i] = pastStableDescriptor(rootID, fmt.Sprintf("childcancel_%04d", i), "retained task")
	}
	writePastStableDelegates(t, stateDir, rootID, descriptors...)
	index, err := acquireSessionActivityIndex(t.Context(), stateDir+"\x00"+rootID, nil)
	if err != nil {
		t.Fatal(err)
	}
	index.release()
	ctx := &sessionActivityCancelDuringFold{Context: t.Context(), index: index, delegates: true}
	params := appwire.SessionActivityListParams{Ref: encodeRef("", rootID)}
	if _, err = LoadSessionActivityDelegates(ctx, stateDir, rootID, params); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled delegate fold=%v", err)
	}
	got, err := LoadSessionActivityDelegates(t.Context(), stateDir, rootID, params)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Delegates) != 31 || !got.Page.Complete {
		t.Fatalf("resumed delegate fold lost events: %+v", got)
	}
	seen := make(map[string]bool)
	for _, row := range got.Delegates {
		if seen[row.DelegateID] {
			t.Fatalf("replayed Creation %s", row.DelegateID)
		}
		seen[row.DelegateID] = true
	}
}

func TestSessionActivityLiveDelegateAdmissionMembership(t *testing.T) {
	t.Parallel()
	for _, scope := range []appwire.SessionActivityScope{appwire.SessionActivityScopeSession, appwire.SessionActivityScopeSubtree} {
		for _, laterAt := range []int64{150, 200} {
			t.Run(fmt.Sprintf("%s/%d", scope, laterAt), func(t *testing.T) {
				s := newSession(t, withoutGitSnapshot())
				c := s.delegateController
				create := func(id, parent string, at int64) {
					t.Helper()
					descriptor := stableToolDescriptor(s, id, parent)
					c.mu.Lock()
					_, err := c.appendLocked(delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: id, TS: time.Unix(at, 0).UTC(), Created: &delegatestore.DelegateCreated{Descriptor: descriptor}})
					c.mu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
				}
				create("dlg_initial_300", "", 300)
				create("dlg_initial_200", "", 200)
				parent := ""
				if scope == appwire.SessionActivityScopeSubtree {
					parent = "dlg_initial_300"
				}
				create("dlg_initial_100", parent, 100)
				// The tied-clock cases also prove the membership fence is restored
				// from accepted journal events when the controller reopens.
				if laterAt == 200 {
					restored, err := openDelegateTreeController(delegateTreeControllerConfig{store: c.store, rootRuntime: s, rootSessionID: s.ID(), stateDir: c.stateDir})
					if err != nil {
						t.Fatal(err)
					}
					c = restored
					s.delegateController = c
				}
				params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Scope: scope, Limit: 1}
				first, err := s.ListActivityDelegates(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				if len(first.Delegates) != 1 || first.Delegates[0].DelegateID != "dlg_initial_300" || first.Page.NextCursor == "" {
					t.Fatalf("invalid first page: %+v", first)
				}
				create("dlg_a_later", parent, laterAt)
				c.mu.Lock()
				_, err = c.appendLocked(delegateControllerRunStartedEvent("dlg_initial_200", 1, delegatestore.TriggerInitial, time.Unix(400, 0).UTC()))
				c.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				params.Cursor = first.Page.NextCursor
				params.Limit = 200
				second, err := s.ListActivityDelegates(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				if len(second.Delegates) != 2 || !second.Page.Complete {
					t.Fatalf("later creation changed membership: %+v", second)
				}
				for _, row := range second.Delegates {
					if row.DelegateID == "dlg_a_later" {
						t.Fatalf("later admission leaked: %+v", second)
					}
					if row.DelegateID == "dlg_initial_200" && row.Phase != "running" {
						t.Fatalf("status update hidden by membership fence: %+v", row)
					}
				}
				params.Cursor = ""
				fresh, err := s.ListActivityDelegates(t.Context(), params)
				if err != nil {
					t.Fatal(err)
				}
				if len(fresh.Delegates) != 4 || !fresh.Page.Complete {
					t.Fatalf("fresh walk omitted later admission: %+v", fresh)
				}
			})
		}
	}
}

func TestSessionActivityRecoverableSourceAccess(t *testing.T) {
	t.Parallel()
	for _, resource := range []string{"metadata", "delegates", "jobs"} {
		for _, warm := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/warm=%t", resource, warm), func(t *testing.T) {
				stateDir := t.TempDir()
				id := "accessrecovery"
				savePastActivityMeta(t, stateDir, id, "Root")
				params := appwire.SessionActivityListParams{Ref: encodeRef("", id), Limit: 1}
				path := filepath.Join(stateDir, "sessions", id+".meta.json")
				query := func() (int, error) {
					summary, err := LoadSessionActivitySummary(t.Context(), stateDir, id, appwire.SessionActivityReadParams{Ref: params.Ref})
					return len(summary.Context.SessionID), err
				}
				switch resource {
				case "delegates":
					writePastStableDelegates(t, stateDir, id, pastStableDescriptor(id, "accesschild1", "one"), pastStableDescriptor(id, "accesschild2", "two"))
					path = filepath.Join(jobsDir(stateDir, id), "delegates.jsonl")
					query = func() (int, error) {
						page, err := LoadSessionActivityDelegates(t.Context(), stateDir, id, params)
						if err == nil && params.Cursor == "" {
							params.Cursor = page.Page.NextCursor
						}
						return len(page.Delegates), err
					}
				case "jobs":
					writeActivityJobLogFast(t, stateDir, id, 2)
					path = filepath.Join(jobsDir(stateDir, id), "jobs.jsonl")
					query = func() (int, error) {
						page, err := LoadSessionActivityJobs(t.Context(), stateDir, id, params)
						if err == nil && params.Cursor == "" {
							params.Cursor = page.Page.NextCursor
						}
						return len(page.Jobs), err
					}
				}
				if warm {
					if _, err := query(); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Rename(path, path+".held"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				_, err := query()
				var wire appwire.WireError
				if !errors.As(err, &wire) {
					t.Fatalf("source failure not typed: %v", err)
				}
				data, ok := wire.Data.(appwire.ErrorData)
				if wire.Code != appwire.CodeUnavailable || !ok || data.EvenerErrorInfo != appwire.ErrorActionUnavailable || data.RetryDisposition != appwire.RetryDispositionAutomatic {
					t.Fatalf("recoverable access parked: %+v", wire)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+".held", path); err != nil {
					t.Fatal(err)
				}
				if rows, err := query(); err != nil || rows == 0 {
					t.Fatalf("restored source cannot resume same walk: rows=%d err=%v", rows, err)
				}
			})
		}
	}
}

func TestSessionActivityPermanentSourceFailuresStayDistinct(t *testing.T) {
	t.Parallel()
	for _, resource := range []string{"metadata", "delegates", "jobs"} {
		t.Run(resource, func(t *testing.T) {
			dir := t.TempDir()
			id := "permanentsource"
			savePastActivityMeta(t, dir, id, "Root")
			path := filepath.Join(dir, "sessions", id+".meta.json")
			query := func() error {
				_, err := LoadSessionActivitySummary(t.Context(), dir, id, appwire.SessionActivityReadParams{Ref: encodeRef("", id)})
				return err
			}
			switch resource {
			case "delegates":
				path = filepath.Join(jobsDir(dir, id), "delegates.jsonl")
				query = func() error {
					_, err := LoadSessionActivityDelegates(t.Context(), dir, id, appwire.SessionActivityListParams{Ref: encodeRef("", id)})
					return err
				}
			case "jobs":
				path = filepath.Join(jobsDir(dir, id), "jobs.jsonl")
				query = func() error {
					_, err := LoadSessionActivityJobs(t.Context(), dir, id, appwire.SessionActivityListParams{Ref: encodeRef("", id)})
					return err
				}
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{invalid}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			err := query()
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
				t.Fatalf("corrupt source=%v", err)
			}
			if data, ok := wire.Data.(appwire.ErrorData); !ok || data.RetryDisposition == appwire.RetryDispositionAutomatic {
				t.Fatalf("corrupt data treated as access failure: %+v", wire)
			}
		})
	}
	dir := t.TempDir()
	id := "absentmetadata"
	_, err := LoadSessionActivitySummary(t.Context(), dir, id, appwire.SessionActivityReadParams{Ref: encodeRef("", id)})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.ResourceNotFound("").Code {
		t.Fatalf("missing metadata=%v", err)
	}
}

func TestSessionActivityTailAccessPreservesProbePairing(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "journal")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	index := &sessionActivityIndex{}
	read := &sessionActivityRead{index: index}
	source := sessionActivitySource{Offset: 4, Tail: []byte("old!")}
	err := read.captureTail(path, &source, 8)
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("tail access=%v", err)
	}
	if data := wire.Data.(appwire.ErrorData); data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Fatalf("tail access parked: %+v", wire)
	}
	if source.Offset != 4 || string(source.Tail) != "old!" {
		t.Fatalf("old tail paired with new offset: %+v", source)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("restored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := read.captureTail(path, &source, 8); err != nil {
		t.Fatal(err)
	}
	if string(source.Tail) != "restored" || index.rawBytes != 8 {
		t.Fatalf("restored tail=%+v bytes=%d", source, index.rawBytes)
	}
}

func TestSessionActivityRejectedTailCapturePublicRecovery(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"jobs", "delegates"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			id := "tailstaging"
			savePastActivityMeta(t, dir, id, "Root")
			params := appwire.SessionActivityListParams{Ref: encodeRef("", id)}
			path := filepath.Join(jobsDir(dir, id), kind+".jsonl")
			query := func() (int, error) {
				page, err := LoadSessionActivityJobs(t.Context(), dir, id, params)
				return len(page.Jobs), err
			}
			if kind == "jobs" {
				writeActivityJobLogFast(t, dir, id, 2)
			} else {
				writePastStableDelegates(t, dir, id, pastStableDescriptor(id, "tailchild1", "one"), pastStableDescriptor(id, "tailchild2", "two"))
				query = func() (int, error) {
					page, err := LoadSessionActivityDelegates(t.Context(), dir, id, params)
					return len(page.Delegates), err
				}
			}
			if rows, err := query(); err != nil || rows != 2 {
				t.Fatalf("initial read rows=%d err=%v", rows, err)
			}
			read, err := retainedActivityRead(t.Context(), dir, id, appwire.SessionActivityReadParams{Ref: params.Ref})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if read.index != nil {
					read.index.release()
				}
			}()
			at := time.Unix(1_600_000_000, 0).UTC()
			var source *sessionActivitySource
			var accept func() error
			var candidateOffset int64
			if kind == "jobs" {
				index := read.index.jobs[id]
				store, err := jobstore.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_later", Type: jobstore.JobShell, Background: true, OwnerSessionID: id, TS: at, StartedAt: &at}); err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				cursor := index.Cursor
				pending, complete, err := jobstore.ReadPage(t.Context(), path, &cursor, 4<<20, 2000)
				if err != nil {
					t.Fatal(err)
				}
				source = &index.Source
				candidateOffset = cursor.Journal.Offset
				accept = func() error { return read.acceptJobPage(path, index, cursor, pending, complete) }
			} else {
				store, err := delegatestore.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				event := delegateControllerRunStartedEvent("dlg_tailchild1", 1, delegatestore.TriggerInitial, at)
				event.TS = at
				if _, _, err := store.AppendBatch(read.index.delegates, []delegatestore.Event{event}); err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				cursor := read.index.delegateCursor
				pending, complete, err := delegatestore.ReadPage(t.Context(), path, &cursor, 4<<20, 2000)
				if err != nil {
					t.Fatal(err)
				}
				source = &read.index.delegateSource
				candidateOffset = cursor.Journal.Offset
				accept = func() error { return read.acceptDelegatePage(cursor, pending, complete) }
			}
			originalOffset := source.Offset
			originalTail := string(source.Tail)
			if candidateOffset <= originalOffset || originalTail == "" {
				t.Fatal("invalid scanner candidate")
			}
			if err := os.Rename(path, path+".held"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			captureErr := accept()
			var wire appwire.WireError
			if !errors.As(captureErr, &wire) || wire.Data.(appwire.ErrorData).RetryDisposition != appwire.RetryDispositionAutomatic {
				t.Fatalf("capture error=%v", captureErr)
			}
			if source.Offset != originalOffset || string(source.Tail) != originalTail {
				t.Fatalf("rejected source pairing changed: %+v", source)
			}
			if kind == "jobs" {
				index := read.index.jobs[id]
				if index.Cursor.Journal.Offset != originalOffset || len(index.Pending) != 0 {
					t.Fatal("rejected job scanner state committed")
				}
			} else if read.index.delegateCursor.Journal.Offset != originalOffset || len(read.index.delegatePending) != 0 {
				t.Fatal("rejected delegate scanner state committed")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path+".held", path); err != nil {
				t.Fatal(err)
			}
			read.index.release()
			read.index = nil
			wantRows := 3
			if kind == "delegates" {
				wantRows = 2
			}
			if rows, err := query(); err != nil || rows != wantRows {
				t.Fatalf("public recovery rows=%d err=%v", rows, err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rewrite := strings.ReplaceAll(string(raw), "12:26:40Z", "12:26:41Z")
			if rewrite == string(raw) || len(rewrite) != len(raw) {
				t.Fatal("invalid changed tail")
			}
			if err := os.WriteFile(path, []byte(rewrite), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = query()
			if !errors.As(err, &wire) || wire.Data.(appwire.ErrorData).EvenerErrorInfo != appwire.ErrorSessionActivityCursorStale {
				t.Fatalf("changed tail accepted after recovery: %v", err)
			}
		})
	}
}

func TestSessionActivityPausedAppendRemainsRecoverable(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"jobs", "delegates"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			id := "pausedappend"
			savePastActivityMeta(t, dir, id, "Root")
			params := appwire.SessionActivityListParams{Ref: encodeRef("", id)}
			path := filepath.Join(jobsDir(dir, id), kind+".jsonl")
			var raw []byte
			at := time.Unix(1_600_000_000, 0).UTC()
			query := func() (int, string, appwire.SessionActivityPage, error) {
				page, err := LoadSessionActivityJobs(t.Context(), dir, id, params)
				status := ""
				if len(page.Jobs) > 0 {
					status = page.Jobs[0].Status
				}
				return len(page.Jobs), status, page.Page, err
			}
			if kind == "jobs" {
				writeActivityJobLogFast(t, dir, id, 1)
				raw, _ = json.Marshal(jobstore.Event{Kind: jobstore.EventJobFinished, Seq: 2, JobID: "job_000000", Status: jobstore.StatusCommandExitedNonzero, TerminalGen: "failure", TS: at})
			} else {
				writePastStableDelegates(t, dir, id, pastStableDescriptor(id, "childpaused", "one"))
				event := delegateControllerRunStartedEvent("dlg_paused", 1, delegatestore.TriggerInitial, at)
				event.Seq = 2
				event.TS = at
				raw, _ = json.Marshal(struct {
					Events []delegatestore.Event `json:"events"`
				}{[]delegatestore.Event{event}})
				query = func() (int, string, appwire.SessionActivityPage, error) {
					page, err := LoadSessionActivityDelegates(t.Context(), dir, id, params)
					phase := ""
					if len(page.Delegates) > 0 {
						phase = page.Delegates[0].Phase
					}
					return len(page.Delegates), phase, page.Page, err
				}
			}
			raw = append(raw, '\n')
			if rows, _, _, err := query(); err != nil || rows != 1 {
				t.Fatalf("initial admission rows=%d err=%v", rows, err)
			}
			appendBytes := func(data []byte) {
				t.Helper()
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write(data); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			half := len(raw) / 2
			appendBytes(raw[:half])
			rows, _, progress, err := query()
			if err != nil || rows != 0 || progress.Complete || progress.NextCursor == "" {
				t.Fatalf("incomplete prefix published: rows=%d page=%+v err=%v", rows, progress, err)
			}
			params.Cursor = progress.NextCursor
			_, _, _, err = query()
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable || wire.Data.(appwire.ErrorData).RetryDisposition != appwire.RetryDispositionAutomatic {
				t.Fatalf("paused append parked: %v", err)
			}
			appendBytes(raw[half:])
			rows, status, finished, err := query()
			want := string(jobstore.StatusCommandExitedNonzero)
			if kind == "delegates" {
				want = "running"
			}
			if err != nil || rows != 1 || status != want || !finished.Complete {
				t.Fatalf("completed append cannot resume existing admission: rows=%d status=%s page=%+v err=%v", rows, status, finished, err)
			}
		})
	}
}
