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
	writeJobLogFast(t, stateDir, id, 31)
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
	index *sessionActivityIndex
	owner string
}

func (ctx *sessionActivityCancelDuringFold) Err() error {
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
	path := writeJobLogFast(t, stateDir, id, 451)
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
	if err = store.Append(jobstore.Event{Kind: jobstore.EventJobStarted, JobID: "job_new", Type: jobstore.JobShell, OwnerSessionID: id, TS: at, StartedAt: &at}); err != nil {
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
	if len(page.Watches) != 1 || page.Watches[0].Watch.ID != result.WatchID || page.Watches[0].State != appwire.SessionWatchStateArmed || page.Watches[0].ReceiverRef != params.Ref {
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
	writeJobLogFast(t, stateDir, id, 2001)
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
	path := writeJobLogFast(t, stateDir, id, 3)
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
		batch[i] = jobstore.Event{Kind: jobstore.EventJobStarted, JobID: fmt.Sprintf("job_large_%04d", i), Type: jobstore.JobShell, OwnerSessionID: id, TS: at, StartedAt: &at, Command: prose, Description: prose, Task: prose}
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
		{Kind: jobstore.EventJobStarted, JobID: "job_failed", Type: jobstore.JobShell, OwnerSessionID: id, TS: at, StartedAt: &at},
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

func TestSessionActivityRealDelegateTree(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 2, ForceRealIO: true, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}}), withSteps(
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{ID: "spawn-grandchild", Name: "delegate", Arguments: json.RawMessage(`{"prompt":"grandchild activity task"}`), Type: "function"})
		},
		func(llm.Request) llm.Response { return finalResponse("grandchild done") },
		func(llm.Request) llm.Response { return finalResponse("parent done") },
	))
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
	case <-time.After(10 * time.Second):
		t.Fatal("scripted grandchild did not finish")
	}
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
	claim := claimSettledIdleSubtree(t, c, parent.DelegateID, parentRuntime, grandchildRuntime)
	if err = c.AbortRuntimeReclamation(claim); err != nil {
		t.Fatal(err)
	}
	if !parentRuntime.releaseIdleRuntimeAfterFinalize() {
		t.Fatal("real idle subtree release refused")
	}
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
		event := jobstore.Event{Kind: jobstore.EventJobStarted, JobID: fmt.Sprintf("job_budget_%03d", i), Type: jobstore.JobShell, OwnerSessionID: descriptor.ChildSessionID, TS: at, StartedAt: &at, Command: strings.Repeat("x", (1<<20)+17)}
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
}
