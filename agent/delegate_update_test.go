package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/internal/delegatestore"
	toolpkg "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// isDelegateUpdateAttentionID reports whether attentionID names a delegate
// update's delivery rather than a generation's report.
func isDelegateUpdateAttentionID(attentionID string) bool {
	deliveryID, ok := strings.CutPrefix(attentionID, delegateAttentionID(""))
	return ok && delegatestore.IsUpdateDeliveryID(deliveryID)
}

func waitForDelegateTestCustody(t *testing.T, s *Session, payload string) string {
	t.Helper()
	var id string
	waitForCondition(t, 30*time.Second, "exact canonical custody of "+payload, func() bool {
		data, err := readTranscriptFull(s.TranscriptPath(), "")
		if err != nil {
			t.Fatal(err)
		}
		accepted := 0
		for _, entry := range data.Entries {
			if entry.Turn.AttentionID != "" && strings.Contains(entry.Turn.Message.Text(), payload) {
				id = entry.Turn.AttentionID
				accepted++
			}
		}
		if accepted == 0 {
			return false
		}
		if accepted != 1 {
			t.Fatalf("payload %s accepted %d times", payload, accepted)
		}
		for _, entry := range data.Entries {
			if slices.Contains(entry.Turn.ObservedNotificationIDs, id) || (entry.Turn.AttentionResolution != nil && entry.Turn.AttentionResolution.AttentionID == id) {
				t.Fatalf("notification %s observed before designated successful response", id)
			}
		}
		return true
	})
	return id
}

func assertDelegateTestObservation(t *testing.T, receiver, source *Session, id string) {
	t.Helper()
	waitForCondition(t, 30*time.Second, "one consumed transition for "+id, func() bool {
		fold, err := readDelegateAttentionFold(receiver.TranscriptPath(), receiver.ID())
		return err == nil && fold.resolutions[id] == delegateAttentionConsumed
	})
	incidentNotificationFacts(t, receiver.stateDir, receiver.ID(), id)
	data, err := readTranscriptFull(receiver.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range data.Entries {
		if resolution := entry.Turn.AttentionResolution; resolution != nil && resolution.AttentionID == id && resolution.ResumeGeneration != 0 {
			t.Fatalf("input=%s consumed by unexpected successor generation=%d", id, resolution.ResumeGeneration)
		}
	}
	if source != nil {
		events, err := source.delegateController.store.Load()
		if err != nil {
			t.Fatal(err)
		}
		acked := 0
		for _, event := range events {
			if receipt := event.DeliveryAcknowledged; receipt != nil && receipt.DeliveryID == strings.TrimPrefix(id, "delegate:") {
				acked++
			}
		}
		if acked != 1 {
			t.Fatalf("source delivery %s custody receipts=%d, want1", id, acked)
		}
	}
}

type updateStep = func(context.Context, llm.Request) (llm.Response, error)

func updateReply(response llm.Response) updateStep {
	return func(context.Context, llm.Request) (llm.Response, error) { return response, nil }
}

func updateGate(gate <-chan struct{}, next updateStep) updateStep {
	return func(ctx context.Context, req llm.Request) (llm.Response, error) {
		select {
		case <-gate:
			return next(ctx, req)
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
	}
}

func updateWait(call, child, payload string) llm.Response {
	return toolCallResponse(communicateCallArgs(call, map[string]any{
		"message": payload, "end_turn": true, "end_reason": "waiting_on_work", "waiting_on": child,
	}))
}

// Every finite request and response is retained before TestMain removes roots.
// In particular, script exhaustion is an error, never an implicit done loop.
func finiteUpdateAdapter(t *testing.T, markers ...string) *messagingAdapter {
	t.Helper()
	a := newMessagingAdapter(markers...)
	a.onUnexpected = func(detail string) { t.Error(detail) }
	a.finiteScript("root")
	return a
}

func traceUpdateFixture(t *testing.T, root *Session, a *messagingAdapter) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		a.mu.Lock()
		type requestTrace struct {
			SessionID  string
			Request    int
			ResponseID string
			Payloads   []llm.ContentPart
		}
		trace := map[string][]requestTrace{}
		for key, requests := range a.requests {
			for i, req := range requests {
				record := requestTrace{SessionID: req.SessionID, Request: i + 1}
				if i < len(a.answered[key]) {
					record.ResponseID = a.answered[key][i].ID
				}
				for _, msg := range req.Messages {
					for _, part := range msg.Content {
						if part.ToolCall != nil || part.ToolResult != nil || strings.Contains(part.Text, "<delegate-notification") {
							record.Payloads = append(record.Payloads, part)
						}
					}
				}
				trace[key] = append(trace[key], record)
			}
		}
		raw, err := json.Marshal(trace)
		a.mu.Unlock()
		t.Logf("finite provider trace err=%v: %s", err, raw)
		var dump func(*Session)
		dump = func(sess *Session) {
			data, err := os.ReadFile(sess.TranscriptPath())
			t.Logf("raw transcript session=%s err=%v:\n%s", sess.ID(), err, data)
			for _, child := range sess.subagents.sessions() {
				dump(child)
			}
		}
		dump(root)
	})
}

func TestDelegateUpdateFiniteScriptIsOptIn(t *testing.T) {
	t.Parallel()
	req := llm.Request{SessionID: "fixture-root"}
	a := newMessagingAdapter()
	a.rootID = req.SessionID
	a.script("root", func(llm.Request) llm.Response {
		response := finalResponse("SCRIPTED")
		response.ID = "default-scripted"
		return response
	})
	first, err := a.Complete(context.Background(), req)
	if err != nil || first.ID != "default-scripted" {
		t.Fatalf("default scripted response=%+v err=%v", first, err)
	}
	for range 2 {
		response, err := a.Complete(context.Background(), req)
		if err != nil || len(response.ToolCalls()) != 1 || response.ToolCalls()[0].Name != "communicate" {
			t.Fatalf("default fallback=%+v err=%v", response, err)
		}
	}
	finite := newMessagingAdapter()
	finite.rootID = req.SessionID
	gate := make(chan struct{})
	finite.finiteScript("root", updateGate(gate, updateReply(finalResponse("unreachable"))))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := finite.Complete(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("finite canceled error=%v", err)
	}
	unexpected := 0
	finite.onUnexpected = func(string) { unexpected++ }
	if _, err := finite.Complete(context.Background(), req); !errors.Is(err, context.Canceled) {
		t.Fatalf("finite exhaustion error=%v", err)
	}
	if unexpected != 1 || len(finite.requestsFor("root")) != 2 || len(finite.answered["root"]) != 0 {
		t.Fatalf("unexpected=%d requests=%d answered=%d", unexpected, len(finite.requestsFor("root")), len(finite.answered["root"]))
	}
}

func updateChildID(req llm.Request) string {
	return deferredHeadDelegateID.FindString(lastToolResultText(req.Messages))
}

func assertUpdateSource(t *testing.T, root *Session, worker, payload, id string) {
	t.Helper()
	var ids []string
	for _, event := range publicationEvents(t, root) {
		if update := event.UpdatePosted; event.DelegateID == worker && update != nil && update.Message == payload {
			if update.Generation != 1 {
				t.Fatalf("update source generation=%d want=1", update.Generation)
			}
			ids = append(ids, delegateAttentionID(delegatestore.UpdateDeliveryID(worker, event.Seq)))
		}
	}
	if !slices.Equal(ids, []string{id}) {
		t.Fatalf("producer=%s payload=%s posted identities=%v accepted=%s", worker, payload, ids, id)
	}
}

func assertUpdateEmptyWakes(t *testing.T, owner *Session) {
	t.Helper()
	for range 2 {
		if err := owner.requestNotificationWake(); err != nil {
			t.Fatal(err)
		}
	}
}

func finishUpdateRoot(ctx context.Context, t *testing.T, root *Session, a *messagingAdapter, payloads ...string) {
	t.Helper()
	a.finiteScript("root", updateReply(finalResponse("ROOT_SETTLED")))
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	assertUpdateRequests(t, a, "root", root, nil, nil, payloads)
	data, err := readTranscriptFull(root.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range data.Entries {
		if id := entry.Turn.AttentionID; id != "" {
			assertDelegateTestObservation(t, root, root, id)
		}
	}
}

// Wait for the completed activation, not provider arrival or SessionIdle alone.
// The real yielded binding releases capacity while retaining generation one.
func awaitUpdateYield(t *testing.T, root, owner *Session, call, child string) {
	t.Helper()
	sub, c := root.getSub(owner.ID()), root.delegateController
	waitForCondition(t, 30*time.Second, "safe yielded activation "+call, func() bool {
		checkpoint, err := readWaitCheckpoint(owner.TranscriptPath(), owner.ID())
		if err != nil || checkpoint == nil || checkpoint.CommunicateCallID != call || checkpoint.WaitingOn != child || checkpoint.ResumedByTurnID != "" || checkpoint.Generation != 1 {
			return false
		}
		sub.mu.Lock()
		released := !sub.running && !sub.driving && !sub.finalizing
		sub.mu.Unlock()
		c.mu.Lock()
		run, live := c.durable[owner.owningDelegateID], c.live[owner.owningDelegateID]
		yielded := run != nil && run.CurrentRunOpen && run.Generation == 1 && live != nil && live.binding != nil && live.binding.yielded
		worker := c.durable[child]
		stillLive := worker != nil && worker.CurrentRunOpen
		c.mu.Unlock()
		state := owner.notificationExecutionState()
		owner.attentionMu.Lock()
		callbacksDone := owner.attentionCallbacks == 0
		owner.attentionMu.Unlock()
		writerHealthy := owner.attachedTranscript().WhileHealthy(func() {}) == nil
		return released && yielded && stillLive && !state.running && !state.recovering && callbacksDone && writerHealthy
	})
	for _, event := range publicationEvents(t, root) {
		if event.DelegateID == owner.owningDelegateID && event.RunFinished != nil {
			t.Fatal("coordinator published terminal report with gated child live")
		}
	}
	checkpoint, err := readWaitCheckpoint(owner.TranscriptPath(), owner.ID())
	if err != nil {
		t.Fatal(err)
	}
	data, err := readTranscriptFull(owner.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	communicated, accepted, completed := 0, 0, 0
	for _, entry := range data.Entries {
		turn := entry.Turn
		if turn.TurnID != checkpoint.ExecutionTurnID {
			continue
		}
		if info := turn.Communicate; info != nil && info.CallID == call {
			if !info.EndTurn || info.EndReason != "waiting_on_work" || info.WaitingOn != child {
				t.Fatalf("invalid wait publication: %+v", info)
			}
			communicated++
		}
		if turn.Completion != nil && turn.Completion.Status == schema.TurnCompleted {
			completed++
		}
		for _, part := range turn.Message.Content {
			if result := part.ToolResult; result != nil && result.ToolCallID == call {
				var state struct {
					EndReason string `json:"communicate_end_reason"`
				}
				if result.IsError || json.Unmarshal(result.ToolState, &state) != nil || state.EndReason != "waiting_on_work" {
					t.Fatalf("invalid wait tool status: %+v", result)
				}
				accepted++
			}
		}
	}
	if communicated != 1 || accepted != 1 || completed != 1 {
		t.Fatalf("wait=%s publication=%d accepted=%d completed=%d want=1,1,1", call, communicated, accepted, completed)
	}
}

func assertUpdateCompleted(t *testing.T, root, coord *Session) {
	t.Helper()
	started, finished := 0, 0
	for _, event := range publicationEvents(t, root) {
		if event.DelegateID != coord.owningDelegateID {
			continue
		}
		if event.RunStarted != nil {
			started++
		}
		if result := event.RunFinished; result != nil {
			finished++
			if result.Generation != 1 || result.Outcome.Status != delegatestore.OutcomeCompleted {
				t.Fatalf("coordinator settlement=%+v", result)
			}
		}
	}
	if started != 1 || finished != 1 {
		t.Fatalf("coordinator generations started=%d finished=%d want=1,1", started, finished)
	}
}

type updateSyncRecord struct {
	ResponseID   string
	Observed     []string
	ResolutionID string
	Failed       bool
}

// Audit the actual file boundary around the existing resolution-only fault.
// No session/controller path is replaced. A readable receipt alone is not
// evidence that its sync succeeded, so keep both writes and sync outcomes.
type updateSyncFS struct {
	*rootAttentionResolutionFailFS
	traceMu sync.Mutex
	records []updateSyncRecord
}

type updateSyncFile struct {
	afero.File
	fs      *updateSyncFS
	pending []updateSyncRecord
}

func (fs *updateSyncFS) OpenFile(path string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.rootAttentionResolutionFailFS.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	return &updateSyncFile{File: file, fs: fs}, nil
}

func (f *updateSyncFile) Write(p []byte) (int, error) {
	if entry, err := transcript.DecodeEntry(p); err == nil {
		turn := entry.Turn
		if turn.Kind == schema.TurnAssistant {
			f.pending = append(f.pending, updateSyncRecord{ResponseID: turn.ResponseID, Observed: slices.Clone(turn.ObservedNotificationIDs)})
		}
		if resolution := turn.AttentionResolution; resolution != nil {
			f.pending = append(f.pending, updateSyncRecord{ResolutionID: resolution.AttentionID})
		}
	}
	return f.File.Write(p)
}

func (f *updateSyncFile) Sync() error {
	err := f.File.Sync()
	f.fs.traceMu.Lock()
	for _, record := range f.pending {
		record.Failed = err != nil
		f.fs.records = append(f.fs.records, record)
	}
	f.fs.traceMu.Unlock()
	f.pending = nil
	return err
}

func installUpdateSyncFault(t *testing.T, s *Session, failures int) *updateSyncFS {
	t.Helper()
	fs := &updateSyncFS{rootAttentionResolutionFailFS: &rootAttentionResolutionFailFS{Fs: afero.NewOsFs(), failures: failures}}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	old := s.attachedTranscript()
	t.Cleanup(func() {
		if err := old.Close(); err != nil {
			t.Error(err)
		}
	})
	replacement, entries, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	replacement.TrackFailures(entries, s.fork.divergence)
	s.mu.Lock()
	s.setTranscriptLocked(replacement)
	s.mu.Unlock()
	t.Cleanup(func() {
		if t.Failed() {
			fs.traceMu.Lock()
			defer fs.traceMu.Unlock()
			t.Logf("filesystem sync trace: %+v", fs.records)
		}
	})
	return fs
}

func assertUpdateSyncFault(t *testing.T, fs *updateSyncFS, id, responseID string, failures int, expected ...string) {
	t.Helper()
	fs.traceMu.Lock()
	records := slices.Clone(fs.records)
	fs.traceMu.Unlock()
	failed, receiptAt, firstFault := 0, -1, -1
	for i, record := range records {
		if record.ResponseID == responseID {
			got, want := slices.Clone(record.Observed), slices.Clone(expected)
			slices.Sort(got)
			slices.Sort(want)
			if record.Failed || !slices.Equal(got, want) {
				t.Fatalf("assistant receipt sync=%+v want=%v", record, want)
			}
			receiptAt = i
		}
		if record.Failed {
			if record.ResolutionID != id {
				t.Fatalf("fault reached wrong record: %+v want resolution=%s", record, id)
			}
			failed++
			if firstFault < 0 {
				firstFault = i
			}
		}
	}
	if failed != failures || receiptAt < 0 || firstFault <= receiptAt {
		t.Fatalf("faults=%d want=%d receiptAt=%d firstFault=%d trace=%+v", failed, failures, receiptAt, firstFault, records)
	}
}

// Payload names below are scenario-owned opaque data, not prompt copy. Resolve
// their immutable identities from raw custody, then pin complete request and
// response ID sets independently of all production staging/fold helpers.
func assertUpdateRequests(t *testing.T, a *messagingAdapter, key string, sess *Session, expected ...[]string) {
	t.Helper()
	a.mu.Lock()
	requests, responses := slices.Clone(a.requests[key]), slices.Clone(a.answered[key])
	a.mu.Unlock()
	assertUpdateResponseRecords(t, key, sess, requests, responses, expected...)
}

func updateResponseID(a *messagingAdapter, key string, n int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.answered[key][n-1].ID
}

func assertUpdateResponseRecords(t *testing.T, key string, sess *Session, requests []llm.Request, responses []llm.Response, expected ...[]string) {
	t.Helper()
	if len(requests) != len(expected) || len(responses) != len(expected) {
		t.Fatalf("script=%s requests=%d responses=%d want=%d", key, len(requests), len(responses), len(expected))
	}
	data, err := readTranscriptFull(sess.TranscriptPath(), "")
	if err != nil {
		t.Fatal(err)
	}
	accepted := map[string]schema.Turn{}
	for _, e := range data.Entries {
		if e.Turn.AttentionID != "" {
			if _, exists := accepted[e.Turn.AttentionID]; exists {
				t.Fatalf("duplicate physical custody id=%s", e.Turn.AttentionID)
			}
			accepted[e.Turn.AttentionID] = e.Turn
		}
		for _, part := range e.Turn.Message.Content {
			if result := part.ToolResult; result != nil && result.IsError {
				t.Fatalf("script=%s tool_call=%s name=%s is_error=%t", key, result.ToolCallID, result.Name, result.IsError)
			}
		}
	}
	for i, req := range requests {
		if req.SessionID != sess.ID() {
			t.Fatalf("script=%s request=%d addressed=%s want=%s", key, i+1, req.SessionID, sess.ID())
		}
		var want []string
		for _, payload := range expected[i] {
			var ids []string
			for id, turn := range accepted {
				if strings.Contains(turn.Message.Text(), payload) {
					ids = append(ids, id)
				}
			}
			if len(ids) != 1 {
				t.Fatalf("payload=%s custody identities=%v want exactly one", payload, ids)
			}
			want = append(want, ids[0])
		}
		for id, turn := range accepted {
			count := strings.Count(messagesText(req.Messages), turn.Message.Text())
			n := 0
			if slices.Contains(want, id) {
				n = 1
			}
			if count != n {
				t.Fatalf("script=%s request=%d id=%s payload count=%d want=%d", key, i+1, id, count, n)
			}
		}
		seen := 0
		for _, e := range data.Entries {
			turn := e.Turn
			if turn.Kind != schema.TurnAssistant || turn.ResponseID != responses[i].ID {
				continue
			}
			seen++
			got := slices.Clone(turn.ObservedNotificationIDs)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("script=%s request=%d response=%s observed=%v want=%v", key, i+1, turn.ResponseID, got, want)
			}
		}
		if seen != 1 {
			t.Fatalf("script=%s request=%d response=%s physical responses=%d want=1", key, i+1, responses[i].ID, seen)
		}
	}
}

// A running delegate's update joins its own delivery queue behind whatever is
// already there. Only the head is planned, and its plan is the update's.
func TestDelegateControllerPostUpdateQueuesInDeliveryOrder(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_target", "")
	actor := delegateActor{lease: &delegateLease{delegateID: "dlg_target", generation: 1}}

	first, firstPlans, err := c.PostUpdate(actor, schema.CommunicateInfo{Message: "first question"}, publicationPacket("first question"), "")
	if err != nil {
		t.Fatalf("PostUpdate first: %v", err)
	}
	if !strings.HasPrefix(first, "dlg_target/update/") || len(firstPlans.deliveries) != 1 ||
		firstPlans.deliveries[0].deliveryID != first || firstPlans.deliveries[0].packet.Kind != delegatestore.PacketUpdate {
		t.Fatalf("first update %q planned %#v, want its own head plan", first, firstPlans.deliveries)
	}
	second, secondPlans, err := c.PostUpdate(actor, schema.CommunicateInfo{Message: "second question"}, publicationPacket("second question"), "")
	if err != nil {
		t.Fatalf("PostUpdate second: %v", err)
	}
	if len(secondPlans.deliveries) != 0 {
		t.Fatalf("second update planned behind the first: %#v", secondPlans.deliveries)
	}
	pending := delegateAggregateSnapshot(t, c, "dlg_target").PendingDeliveries
	if len(pending) != 2 || pending[0].DeliveryID != first || pending[1].DeliveryID != second {
		t.Fatalf("pending deliveries = %#v, want both updates in the order posted", pending)
	}
}

// An update posted through the controller and planned at the head never takes
// the generation's inline waiter. The caller waiting in delegate_send wants
// the report, which carries the update ahead of itself.
func TestDelegateControllerPostedUpdateHeadLeavesTheInlineWaiter(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	if err := c.AttachRuntime(lease, &Session{}); err != nil {
		t.Fatalf("AttachRuntime: %v", err)
	}
	if _, err := c.AdmitStartInput(lease, func() error { return nil }); err != nil {
		t.Fatalf("AdmitStartInput: %v", err)
	}
	_, plans, err := c.PostUpdate(delegateActor{lease: &lease}, schema.CommunicateInfo{Message: "which table?"}, publicationPacket("which table?"), "")
	if err != nil {
		t.Fatalf("PostUpdate: %v", err)
	}
	if len(plans.deliveries) != 1 || plans.deliveries[0].waiter != nil {
		t.Fatalf("update plans = %#v, want one head plan without the waiter", plans.deliveries)
	}
	c.mu.Lock()
	kept := c.live["dlg_target"].waiters[lease.generation]
	c.mu.Unlock()
	if kept != waiter {
		t.Fatalf("generation waiter = %p, want the registered %p still waiting for the report", kept, waiter)
	}
}

// A refused update is never queued: the root has no parent, a stale run cannot
// speak, a stopping delegate's route is closed, a settlement claimant without
// the owning open batch cannot publish, and a blank message says nothing.
func TestDelegateControllerPostUpdateRefusals(t *testing.T) {
	t.Parallel()
	running := delegateLease{delegateID: "dlg_target", generation: 1}
	for _, tc := range []struct {
		name    string
		message string
		actor   func(t *testing.T, c *delegateTreeController) delegateActor
		want    error
	}{
		{"root has no parent", "which table?", func(*testing.T, *delegateTreeController) delegateActor {
			return rootDelegateActor("root-session")
		}, errDelegateNotControllable},
		{"stale run", "which table?", func(*testing.T, *delegateTreeController) delegateActor {
			return delegateActor{lease: &delegateLease{delegateID: "dlg_target", generation: 2}}
		}, errDelegateStaleLease},
		{"stopping", "which table?", func(t *testing.T, c *delegateTreeController) delegateActor {
			if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
				t.Fatalf("StopSubtree: %v", err)
			}
			return delegateActor{lease: &running}
		}, errDelegateTargetBusy},
		{"settlement without owning batch", "which table?", func(t *testing.T, c *delegateTreeController) delegateActor {
			if _, _, err := c.BeginSettlement(running); err != nil {
				t.Fatalf("BeginSettlement: %v", err)
			}
			return delegateActor{lease: &running}
		}, errDelegateTargetBusy},
		{"blank message", "  \n", func(*testing.T, *delegateTreeController) delegateActor {
			return delegateActor{lease: &running}
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := newDelegateControllerTestHarness(t, 1, 1)
			seedDelegateControllerRunning(t, c, "dlg_target", "")
			_, plans, err := c.PostUpdate(tc.actor(t, c), schema.CommunicateInfo{Message: tc.message}, publicationPacket(tc.message), "")
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || len(plans.deliveries) != 0 {
				t.Fatalf("PostUpdate = plans %#v err %v, want %v", plans.deliveries, err, tc.want)
			}
			if pending := delegateAggregateSnapshot(t, c, "dlg_target").PendingDeliveries; len(pending) != 0 {
				t.Fatalf("refused update queued: %#v", pending)
			}
		})
	}
}

// A delegate's communicate(end_turn=false) is an update its parent receives.
// It stays in the delegate's own transcript, lands in an idle root's
// transcript as delegate attention with the update frame, and wakes the root.
// The root's next notification turn presents it and consumes it, and the
// delegate's tool result says it was delivered. (Reworked from the dropped
// caller-route WIP test 6f67c3f327.)
func TestDelegateUpdateWakesAnIdleRoot(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateResponse(false, "need the heading from you") },
		func(llm.Request) llm.Response { return finalResponse("summary written") },
		func(llm.Request) llm.Response { return finalResponse("noted") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	warmStableDelegateUnservedRoot(t, root, fixture)

	pending := rootPendingAttention(root)
	// The direct helper's Complete(true) persists no carrier, so it cannot
	// suppress the canonical terminal notification or prove model observation.
	terminalID := delegateAttentionID(fixture.delegateID + "/delivery/1")
	var updateID string
	for _, id := range pending {
		if strings.HasPrefix(id, delegateAttentionID(fixture.delegateID+"/update/")) {
			updateID = id
		}
	}
	if len(pending) != 2 || updateID == "" || !slices.Contains(pending, terminalID) {
		t.Fatalf("root attention = %v, want one update and terminal %s", pending, terminalID)
	}
	frame := rootAttentionFold(t, root).content[updateID].Text()
	for _, attr := range []string{`delegate_id="` + fixture.delegateID + `"`, `kind="update"`, ">need the heading from you</delegate-notification>"} {
		if !strings.Contains(frame, attr) {
			t.Fatalf("update frame %q lacks %q", frame, attr)
		}
	}
	select {
	case <-wakes:
	default:
		t.Fatal("the update did not wake the root")
	}
	requests := fixture.adapter.Requests()
	if len(requests) < 2 || !strings.Contains(lastToolResultText(requests[1].Messages), `"delivery":"delivered"`) {
		t.Fatalf("the delegate's communicate result does not say delivered")
	}
	var checkpoints []string
	for _, entry := range decodeTranscriptEntries(t, afero.NewOsFs(), transcriptPath(root.stateDir, fixture.childID)) {
		if entry.Turn.Kind == schema.TurnCommunicate && entry.Turn.Communicate != nil && !entry.Turn.Communicate.EndTurn {
			checkpoints = append(checkpoints, entry.Turn.Communicate.Message)
		}
	}
	if len(checkpoints) != 1 || checkpoints[0] != "need the heading from you" {
		t.Fatalf("delegate transcript updates = %#v, want the update recorded", checkpoints)
	}

	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("root notification turn: %v", err)
	}
	requests = fixture.adapter.Requests()
	if !requestContainsText(requests[len(requests)-1], "need the heading from you") {
		t.Fatal("the root's notification turn did not present the update")
	}
	if got := rootAttentionFold(t, root).resolutions[updateID]; got != delegateAttentionConsumed {
		t.Fatalf("update attention = %q, want consumed", got)
	}
}

// An update the journal refuses still succeeds as a communicate call: the
// message is already in the delegate's transcript. The result says it was not
// delivered and why, and nothing is queued for the parent.
func TestDelegateUpdateNotDeliveredStillSucceeds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		refuse func(t *testing.T, c *delegateTreeController, lease delegateLease) delegateLease
	}{
		{"stopping delegate", func(t *testing.T, c *delegateTreeController, lease delegateLease) delegateLease {
			if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), lease.delegateID); err != nil {
				t.Fatalf("StopSubtree: %v", err)
			}
			return lease
		}},
		{"stale run", func(_ *testing.T, _ *delegateTreeController, lease delegateLease) delegateLease {
			return delegateLease{delegateID: lease.delegateID, generation: lease.generation + 1}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c, _ := newDelegateControllerTestHarness(t, 1, 1)
			seedDelegateControllerRunning(t, c, "dlg_target", "")
			runtime := attachDelegateSteerRuntime(t, c, "dlg_target", afero.NewMemMapFs())
			runtime.profile = NewOpenAIProfile("gpt-5.2")
			runtime.delegateController = c
			runtime.owningDelegateID = "dlg_target"
			runtime.cfg.spawn.parentSessionID = "root-session"
			lease := tc.refuse(t, c, delegateLease{delegateID: "dlg_target", generation: 1})

			reg := toolpkg.NewRegistry()
			_ = registerCommunicateTool(reg, newToolDeps(runtime))
			ctx := context.WithValue(context.Background(), delegateRunLeaseContextKey{}, lease)
			// The refusal the controller gives this lease, which the result's
			// reason must explain.
			_, _, refusal := c.PostUpdate(delegateActor{lease: &lease}, schema.CommunicateInfo{Message: "need the schema"}, publicationPacket("need the schema"), "")
			if refusal == nil {
				t.Fatal("the controller accepted an update it should refuse")
			}
			out, err := reg.Get("communicate").Exec(ctx, nil, map[string]any{"message": "need the schema", "end_turn": false})
			if err != nil {
				t.Fatalf("communicate: %v", err)
			}
			var result map[string]any
			if err := json.Unmarshal([]byte(fmt.Sprint(out)), &result); err != nil {
				t.Fatalf("decode result %v: %v", out, err)
			}
			if result["accepted"] != true || result["delivery"] != "not_delivered" {
				t.Fatalf("result = %v, want accepted and not_delivered", result)
			}
			if want := notDeliveredUpdate(refusal).Reason; result["reason"] != want {
				t.Fatalf("reason = %v, want %q", result["reason"], want)
			}
			if pending := delegateAggregateSnapshot(t, c, "dlg_target").PendingDeliveries; len(pending) != 0 {
				t.Fatalf("refused update queued: %#v", pending)
			}
		})
	}
}

// A root mid-turn holds a delegate's update for its next model boundary: the
// request after the tool round presents it, and the turn that presented it
// consumes it, so it earns no wake of its own.
func TestDelegateUpdateReachesARunningRootAtItsNextModelBoundary(t *testing.T) {
	t.Parallel()
	a := finiteUpdateAdapter(t, "WORKER-BRIEF")
	update, boundary, report := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseUpdate, releaseBoundary, releaseReport := sync.OnceFunc(func() { close(update) }), sync.OnceFunc(func() { close(boundary) }), sync.OnceFunc(func() { close(report) })
	defer releaseUpdate()
	defer releaseBoundary()
	defer releaseReport()
	a.finiteScript("root",
		updateReply(delegateToolResponse("create", "WORKER-BRIEF: send update.")),
		updateGate(boundary, updateReply(jobListResponse("list"))),
		updateReply(finalResponse("ROOT_OBSERVED")),
		updateReply(finalResponse("ROOT_SETTLED")),
	)
	a.finiteScript("WORKER-BRIEF",
		updateGate(update, updateReply(updateResponse("upd", "RUNNING_UPDATE"))),
		updateGate(report, updateReply(finalResponse("WORKER_REPORT"))),
	)
	root := newMessagingRoot(t, a, nil)
	traceUpdateFixture(t, root, a)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { _, err := root.ProcessInput(ctx, "start", nil); finished <- err }()
	a.waitForRequests(t, "root", 2)
	releaseUpdate()
	id := waitForDelegateTestCustody(t, root, "RUNNING_UPDATE")
	releaseBoundary()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	assertDelegateTestObservation(t, root, root, id)
	releaseReport()
	child := root.subagents.sessions()[0]
	assertUpdateSource(t, root, child.owningDelegateID, "RUNNING_UPDATE", id)
	waitForStableSupervisionRun(t, root, child.ID())
	waitForRootAttention(t, root, 1)
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	assertUpdateRequests(t, a, "root", root, nil, nil, []string{"RUNNING_UPDATE"}, []string{"RUNNING_UPDATE", "WORKER_REPORT"})
	assertUpdateRequests(t, a, "WORKER-BRIEF", child, nil, nil)
	assertDelegateTestObservation(t, root, root, delegateAttentionID(delegateDeliveryID(child.owningDelegateID, 1)))
}

// An update takes canonical custody while the parent waits in delegate_send.
// The reply carries the earlier result, update and new report in order, once
// each. Only the successful request response consumes their receiver records.
func TestDelegateSendWaitCarriesAnUpdateAheadOfTheReport(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("WORKER-BRIEF")
	releaseFirst := make(chan struct{})
	sendReply, nextRequest, updateResult := make(chan string, 1), make(chan string, 1), make(chan string, 1)
	var (
		mu         sync.Mutex
		delegateID string
		root       *Session
	)
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("create", "WORKER-BRIEF: answer when asked.")
		},
		func(req llm.Request) llm.Response {
			close(releaseFirst)
			id := deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
			waitForCondition(t, 30*time.Second, "first generation canonical custody and source settlement", func() bool {
				c := root.delegateController
				c.mu.Lock()
				a := c.durable[id]
				settled := a != nil && !a.CurrentRunOpen && len(a.PendingDeliveries) == 0 && c.finalizingLocked(id) == nil
				c.mu.Unlock()
				return settled && slices.Contains(rootAttentionFold(t, root).order, delegateAttentionID(delegateDeliveryID(id, 1)))
			})
			mu.Lock()
			delegateID = id
			mu.Unlock()
			return delegateSendResponse("send", id, "Find the drain cursor table.", 60_000)
		},
		func(req llm.Request) llm.Response {
			sendReply <- lastToolResultText(req.Messages)
			nextRequest <- deferredHeadToolResults(req.Messages) + messagesText(req.Messages)
			fold := rootAttentionFold(t, root)
			for _, id := range fold.order {
				if isDelegateUpdateAttentionID(id) && fold.resolutions[id] == delegateAttentionConsumed {
					t.Error("update consumed before the successful assistant response carrying it")
				}
			}
			return finalResponse("root done")
		},
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, releaseFirst, "the root's second request")
			return finalResponse("GEN1_READY")
		},
		func(llm.Request) llm.Response {
			return updateResponse("upd", "UPDATE_ONE: which table holds the drain cursor?")
		},
		func(req llm.Request) llm.Response {
			updateResult <- lastToolResultText(req.Messages)
			return finalResponse("GEN2_REPORT")
		},
	)
	root = newMessagingRoot(t, adapter, nil)
	// TRIPWIRE: half the send's wait; a send that sits out its wait fails here.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	reply := <-sendReply
	first, update, report := strings.Index(reply, "GEN1_READY"), strings.Index(reply, "UPDATE_ONE"), strings.Index(reply, "GEN2_REPORT")
	if first < 0 || update < first || report < update {
		t.Fatalf("delegate_send reply = %q, want the earlier result, then the update, then the new report", reply)
	}
	request := <-nextRequest
	for _, message := range []string{"GEN1_READY", "UPDATE_ONE", "GEN2_REPORT"} {
		if n := strings.Count(request, message); n != 1 {
			t.Fatalf("the root's next request carries %s %d times, want once", message, n)
		}
	}
	if got := <-updateResult; !strings.Contains(got, `"delivery":"delivered"`) {
		t.Fatalf("the delegate's communicate result = %q, want durable receiver custody", got)
	}
	mu.Lock()
	id := delegateID
	mu.Unlock()
	if pending := delegateAggregateSnapshot(t, root.delegateController, id).PendingDeliveries; len(pending) != 0 {
		t.Fatalf("pending deliveries after the reply = %#v, want all acknowledged", pending)
	}
	// Canonical custody survives the inline carrier. Only its successful
	// assistant response proves observation and consumes the receiver records.
	if pending := rootPendingAttention(root); len(pending) != 0 {
		t.Fatalf("root attention after the reply = %v, want none", pending)
	}
	fold := rootAttentionFold(t, root)
	updates := 0
	for _, attentionID := range fold.order {
		if isDelegateUpdateAttentionID(attentionID) {
			updates++
			if !strings.Contains(fold.content[attentionID].Text(), `kind="update"`) || fold.resolutions[attentionID] != delegateAttentionConsumed || fold.deliveryCommits[strings.TrimPrefix(attentionID, "delegate:")] != "send" {
				t.Fatalf("update %s lost its kind, consumption or inline carrier: %+v", attentionID, fold)
			}
		}
	}
	if updates != 1 || len(fold.order) != 3 {
		t.Fatalf("canonical records = %v, want generations 1 and 2 plus one update", fold.order)
	}
	for _, generation := range []uint64{1, 2} {
		deliveryID := delegateDeliveryID(id, generation)
		if !slices.Contains(fold.order, delegateAttentionID(deliveryID)) || fold.deliveryCommits[deliveryID] != "send" {
			t.Fatalf("generation %d lost canonical custody or inline carrier: %+v", generation, fold)
		}
	}
}

// A root awaiting its human partner's answer to ask_user holds a delegate's
// update and report: a wake runs no turn, and once the answer arrives the
// update reaches the model ahead of the report, and both are consumed.
func TestDelegateUpdateWaitsForTheAnswerToAPendingQuestion(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("WORKER-BRIEF")
	release := make(chan struct{})
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("create", "WORKER-BRIEF: ask your parent which database to use.")
		},
		func(llm.Request) llm.Response { return toolCallResponse(askUserCall("ask1", askUserArgsValid())) },
		func(llm.Request) llm.Response { return finalResponse("going with Postgres") },
		func(llm.Request) llm.Response { return finalResponse("noted the worker") },
	)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, release, "the root awaiting its question")
			return updateResponse("upd", "ASK_UPDATE: Postgres or SQLite?")
		},
		func(llm.Request) llm.Response { return finalResponse("ASK_REPORT") },
	)
	root := newMessagingRoot(t, adapter, nil)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "pick a database", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := root.State(); got != SessionAwaiting {
		t.Fatalf("root state = %q, want awaiting its question", got)
	}
	close(release)
	pending := waitForRootAttention(t, root, 2)
	if !isDelegateUpdateAttentionID(pending[0]) || !strings.Contains(rootAttentionFold(t, root).content[pending[1]].Text(), `kind="report"`) {
		t.Fatalf("root attention = %v, want the update, then the report", pending)
	}
	before := len(adapter.requestsFor("root"))
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("wake while awaiting: %v", err)
	}
	if got := len(adapter.requestsFor("root")); got != before {
		t.Fatalf("a wake while awaiting made %d root requests, want none", got-before)
	}
	if _, err := root.ProcessInput(ctx, "use Postgres", nil); err != nil {
		t.Fatalf("answer: %v", err)
	}
	presented := false
	for _, req := range adapter.requestsFor("root")[before:] {
		text := messagesText(req.Messages)
		u, r := strings.Index(text, "ASK_UPDATE"), strings.Index(text, "ASK_REPORT")
		if u >= 0 && r >= 0 {
			if r < u {
				t.Fatalf("the report reached the model before the update:\n%s", text)
			}
			presented = true
			break
		}
	}
	if !presented {
		t.Fatal("no request after the answer presented both the update and the report")
	}
	fold := rootAttentionFold(t, root)
	for _, id := range pending {
		if fold.resolutions[id] != delegateAttentionConsumed {
			t.Fatalf("attention %q = %q, want consumed after the answer", id, fold.resolutions[id])
		}
	}
}

// One delegate's updates reach its parent in the order sent and before its
// report, and an idle parent takes all three in one notification turn.
func TestDelegateUpdatesArriveInOrderBeforeTheReportInOneTurn(t *testing.T) {
	t.Parallel()
	fixture := newColdStableDelegateFixture(t, "")
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("warm") },
		func(llm.Request) llm.Response { return finalResponse("WARM_OBSERVED_731") },
		func(llm.Request) llm.Response { return updateResponse("ordered-first-731", "FIRST_UPDATE") },
		func(llm.Request) llm.Response { return updateResponse("ordered-second-731", "SECOND_UPDATE") },
		func(llm.Request) llm.Response { return finalResponse("THE_REPORT") },
		func(llm.Request) llm.Response { return finalResponse("noted all three") },
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	warmStableDelegateUnservedRoot(t, root, fixture)
	// TRIPWIRE: all provider responses are scripted locally, only a hang reaches this bound.
	warmCtx, cancelWarm := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelWarm()
	if _, err := root.ProcessInputKind(warmCtx, "", nil, EntryNotification); err != nil {
		t.Fatalf("observe warmup: %v", err)
	}
	if warm := rootAttentionFold(t, root); len(warm.observed) != 1 || len(warm.pendingIDs()) != 0 {
		t.Fatalf("warmup receipt = %+v, want one observed and no pending", warm)
	}
	if outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "go on", 0); outcome.result.Err != nil {
		t.Fatalf("send: %v", outcome.result.Err)
	}
	pending := waitForRootAttention(t, root, 3)
	fold := rootAttentionFold(t, root)
	for i, want := range []string{"FIRST_UPDATE", "SECOND_UPDATE", "THE_REPORT"} {
		if frame := fold.content[pending[i]].Text(); !strings.Contains(frame, want) {
			t.Fatalf("attention %d = %q, want %s", i, frame, want)
		}
	}
	if !strings.Contains(fold.content[pending[2]].Text(), `kind="report"`) {
		t.Fatal("the last attention item is not the report")
	}
	before := len(fixture.adapter.Requests())
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatalf("root notification turn: %v", err)
	}
	if got := len(fixture.adapter.Requests()) - before; got != 1 {
		t.Fatalf("the notification turn made %d requests, want one presenting all three", got)
	}
	fold = rootAttentionFold(t, root)
	for _, id := range pending {
		if fold.resolutions[id] != delegateAttentionConsumed {
			t.Fatalf("attention %q = %q, want consumed by the one turn", id, fold.resolutions[id])
		}
	}
}

// An update resumes an idle coordinator's valid wait in the same open
// generation. The worker's report remains gated until update observation.
func TestDelegateUpdateWakesAnIdleParentDelegate(t *testing.T) {
	t.Parallel()
	singleCoordinatorUpdate(t, "idle")
}

// A coordinator resumes a valid wait and relays its worker's update before
// publishing a terminal report. The historical test name is kept for selectors.
func TestDelegateUpdateFromADrainTurnReachesTheRootBeforeTheReport(t *testing.T) {
	t.Parallel()
	singleCoordinatorUpdate(t, "relay")
}

// Each mode has a designated third request for U. Only running mode receives U
// while request two is in flight. Idle and relay first yield a real wait.
func singleCoordinatorUpdate(t *testing.T, mode string) {
	t.Helper()
	a := finiteUpdateAdapter(t, "COORD-BRIEF", "WORKER-BRIEF")
	rootReady := make(chan struct{})
	releaseRoot := sync.OnceFunc(func() { close(rootReady) })
	defer releaseRoot()
	update, boundary, observation, relayWait, report, finish := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseUpdate, releaseBoundary, releaseObservation, releaseRelay, releaseReport, releaseFinish := sync.OnceFunc(func() { close(update) }), sync.OnceFunc(func() { close(boundary) }), sync.OnceFunc(func() { close(observation) }), sync.OnceFunc(func() { close(relayWait) }), sync.OnceFunc(func() { close(report) }), sync.OnceFunc(func() { close(finish) })
	defer releaseUpdate()
	defer releaseBoundary()
	defer releaseObservation()
	defer releaseRelay()
	defer releaseReport()
	defer releaseFinish()
	var workerID string
	a.finiteScript("root", updateReply(delegateToolResponse("coord", "COORD-BRIEF: coordinate.")), updateReply(finalResponse("root waiting")))
	second := func(_ context.Context, req llm.Request) (llm.Response, error) {
		workerID = updateChildID(req)
		if mode == "running" {
			return jobListResponse("boundary"), nil
		}
		return updateWait("wait-initial", workerID, "WAIT_INITIAL"), nil
	}
	third := func(context.Context, llm.Request) (llm.Response, error) {
		if mode == "relay" {
			return updateResponse("relay", "COORD_RELAY"), nil
		}
		return updateWait("wait-observed", workerID, "WAIT_OBSERVED"), nil
	}
	a.finiteScript("COORD-BRIEF", updateGate(rootReady, updateReply(delegateToolResponse("worker", "WORKER-BRIEF: update and report."))), updateGate(boundary, second), updateGate(observation, third))
	if mode == "relay" {
		a.finiteScript("COORD-BRIEF", updateGate(relayWait, func(context.Context, llm.Request) (llm.Response, error) {
			return updateWait("wait-observed", workerID, "WAIT_OBSERVED"), nil
		}))
	}
	a.finiteScript("COORD-BRIEF", updateGate(finish, updateReply(finalResponse("COORD_FINAL"))))
	a.finiteScript("WORKER-BRIEF", updateGate(update, updateReply(updateResponse("upd", "PARENT_UPDATE"))), updateGate(report, updateReply(finalResponse("WORKER_REPORT"))))
	root := newMessagingRoot(t, a, nil)
	traceUpdateFixture(t, root, a)
	serveSupervisionRootWakes(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatal(err)
	}
	releaseRoot()
	reqs := a.waitForRequests(t, "COORD-BRIEF", 2)
	coord := root.getSub(reqs[1].SessionID).sess
	target := updateChildID(reqs[1])
	if mode != "running" {
		releaseBoundary()
		awaitUpdateYield(t, root, coord, "wait-initial", target)
	}
	releaseUpdate()
	// Running mode holds request two, idle mode holds request three. Both prove
	// custody without any earlier successful response crediting U.
	id := waitForDelegateTestCustody(t, coord, "PARENT_UPDATE")
	assertUpdateSource(t, root, target, "PARENT_UPDATE", id)
	releaseBoundary()
	a.waitForRequests(t, "COORD-BRIEF", 3)
	releaseObservation()
	var relayID string
	if mode == "relay" {
		a.waitForRequests(t, "COORD-BRIEF", 4)
		relayID = waitForDelegateTestCustody(t, root, "COORD_RELAY")
		assertUpdateSource(t, root, coord.owningDelegateID, "COORD_RELAY", relayID)
		req := a.requestsFor("COORD-BRIEF")[3]
		var result struct {
			Accepted bool   `json:"accepted"`
			Delivery string `json:"delivery"`
		}
		if err := json.Unmarshal([]byte(lastToolResultText(req.Messages)), &result); err != nil || !result.Accepted || result.Delivery != "delivered" {
			t.Fatalf("relay tool status=%+v err=%v", result, err)
		}
		releaseRelay()
	}
	awaitUpdateYield(t, root, coord, "wait-observed", target)
	assertDelegateTestObservation(t, coord, root, id)
	assertUpdateEmptyWakes(t, coord)
	sets := [][]string{nil, nil, {"PARENT_UPDATE"}}
	if mode == "relay" {
		sets = append(sets, []string{"PARENT_UPDATE"})
	}
	assertUpdateRequests(t, a, "COORD-BRIEF", coord, sets...)
	releaseReport()
	worker := coord.subagents.sessions()[0]
	waitForStableSupervisionRun(t, coord, worker.ID())
	a.waitForRequests(t, "COORD-BRIEF", len(sets)+1)
	releaseFinish()
	waitForStableSupervisionRun(t, root, coord.ID())
	sets = append(sets, []string{"PARENT_UPDATE", "WORKER_REPORT"})
	assertUpdateRequests(t, a, "COORD-BRIEF", coord, sets...)
	assertUpdateRequests(t, a, "WORKER-BRIEF", worker, nil, nil)
	assertDelegateTestObservation(t, coord, root, delegateAttentionID(delegateDeliveryID(target, 1)))
	awaitRootReport(t, root)
	if mode == "relay" {
		data, err := readTranscriptFull(root.TranscriptPath(), "")
		if err != nil {
			t.Fatal(err)
		}
		reportID := delegateAttentionID(delegateDeliveryID(coord.owningDelegateID, 1))
		relayAt, reportAt := -1, -1
		for i, e := range data.Entries {
			if e.Turn.AttentionID == relayID {
				relayAt = i
			}
			if e.Turn.AttentionID == reportID {
				reportAt = i
			}
		}
		if relayAt < 0 || reportAt <= relayAt {
			t.Fatalf("relay=%s at=%d final=%s at=%d", relayID, relayAt, reportID, reportAt)
		}
	}
	assertUpdateCompleted(t, root, coord)
	rootPayloads := []string{"WAIT_OBSERVED", "COORD_FINAL"}
	if mode != "running" {
		rootPayloads = append([]string{"WAIT_INITIAL"}, rootPayloads...)
	}
	if mode == "relay" {
		rootPayloads = []string{"WAIT_INITIAL", "COORD_RELAY", "WAIT_OBSERVED", "COORD_FINAL"}
	}
	finishUpdateRoot(ctx, t, root, a, rootPayloads...)
}

// A report wakes a coordinator in its ordinary drain without waiting for
// another worker's update or completion.
func TestNotificationReportWakesDrainingParent(t *testing.T) {
	t.Parallel()
	notificationReportWakesParent(t, false)
}

// When the write that marks the update consumed fails once, recovery retries
// the marker without running a second model turn for an update the model
// already saw.
func TestDelegateUpdateConsumedMarkerFailureDoesNotRerunTheTurn(t *testing.T) {
	t.Parallel()
	drainingCoordinatorWithObservedReport(t, 1)
}

// A failed marker retry leaves repair work, not unobserved model input. The
// coordinator keeps its healthy worker and never reruns the observed update.
func TestDelegateUpdateConsumedMarkerRetryFailureKeepsTheCoordinatorRunning(t *testing.T) {
	t.Parallel()
	drainingCoordinatorWithObservedReport(t, 2)
}

// A's report is observed before yielding on B. B's update resumes the same
// generation, with A retained in history. Only its derived marker syncs fail.
func drainingCoordinatorWithObservedReport(t *testing.T, failedMarkerWrites int) {
	t.Helper()
	a := finiteUpdateAdapter(t, "COORD-BRIEF", "WORKER-A", "WORKER-B")
	rootReady := make(chan struct{})
	releaseRoot := sync.OnceFunc(func() { close(rootReady) })
	defer releaseRoot()
	first, boundary, update, observation, report, finish := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseFirst, releaseBoundary, releaseUpdate, releaseObservation, releaseReport, releaseFinish := sync.OnceFunc(func() { close(first) }), sync.OnceFunc(func() { close(boundary) }), sync.OnceFunc(func() { close(update) }), sync.OnceFunc(func() { close(observation) }), sync.OnceFunc(func() { close(report) }), sync.OnceFunc(func() { close(finish) })
	defer releaseFirst()
	defer releaseBoundary()
	defer releaseUpdate()
	defer releaseObservation()
	defer releaseReport()
	defer releaseFinish()
	var workerB string
	a.finiteScript("root", updateReply(delegateToolResponse("coord", "COORD-BRIEF: coordinate.")), updateReply(finalResponse("root waiting")))
	a.finiteScript("COORD-BRIEF",
		updateGate(rootReady, updateReply(delegateToolResponse("a", "WORKER-A: report."))),
		updateGate(boundary, updateReply(delegateToolResponse("b", "WORKER-B: update and report."))),
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			workerB = updateChildID(req)
			return updateWait("wait-initial", workerB, "WAIT_INITIAL"), nil
		},
		updateGate(observation, func(context.Context, llm.Request) (llm.Response, error) {
			return updateWait("wait-observed", workerB, "WAIT_OBSERVED"), nil
		}),
		updateGate(finish, updateReply(finalResponse("COORD_FINAL"))),
	)
	a.finiteScript("WORKER-A", updateGate(first, updateReply(finalResponse("SIBLING_REPORT"))))
	a.finiteScript("WORKER-B", updateGate(update, updateReply(updateResponse("upd", "SIBLING_UPDATE"))), updateGate(report, updateReply(finalResponse("B_REPORT"))))
	root := newMessagingRoot(t, a, nil)
	traceUpdateFixture(t, root, a)
	serveSupervisionRootWakes(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatal(err)
	}
	releaseRoot()
	reqs := a.waitForRequests(t, "COORD-BRIEF", 2)
	coord := root.getSub(reqs[1].SessionID).sess
	releaseFirst()
	firstID := waitForDelegateTestCustody(t, coord, "SIBLING_REPORT")
	releaseBoundary()
	a.waitForRequests(t, "COORD-BRIEF", 3)
	target := updateChildID(a.requestsFor("COORD-BRIEF")[2])
	awaitUpdateYield(t, root, coord, "wait-initial", target)
	assertDelegateTestObservation(t, coord, root, firstID)
	faults := installUpdateSyncFault(t, coord, failedMarkerWrites)
	releaseUpdate()
	a.waitForRequests(t, "COORD-BRIEF", 4)
	updateID := waitForDelegateTestCustody(t, coord, "SIBLING_UPDATE")
	assertUpdateSource(t, root, target, "SIBLING_UPDATE", updateID)
	releaseObservation()
	waitForCondition(t, 30*time.Second, "intended update marker sync faults", func() bool { return faults.injectedFailures() == failedMarkerWrites })
	awaitUpdateYield(t, root, coord, "wait-observed", workerB)
	assertUpdateSyncFault(t, faults, updateID, updateResponseID(a, "COORD-BRIEF", 4), failedMarkerWrites, firstID, updateID)
	assertDelegateTestObservation(t, coord, root, updateID)
	assertUpdateEmptyWakes(t, coord)
	assertUpdateRequests(t, a, "COORD-BRIEF", coord, nil, nil, []string{"SIBLING_REPORT"}, []string{"SIBLING_REPORT", "SIBLING_UPDATE"})
	releaseReport()
	worker := coord.subagents.sessions()[1]
	for _, child := range coord.subagents.sessions() {
		if child.owningDelegateID == workerB {
			worker = child
		}
	}
	waitForStableSupervisionRun(t, coord, worker.ID())
	a.waitForRequests(t, "COORD-BRIEF", 5)
	releaseFinish()
	waitForStableSupervisionRun(t, root, coord.ID())
	assertUpdateRequests(t, a, "COORD-BRIEF", coord, nil, nil, []string{"SIBLING_REPORT"}, []string{"SIBLING_REPORT", "SIBLING_UPDATE"}, []string{"SIBLING_REPORT", "SIBLING_UPDATE", "B_REPORT"})
	for _, child := range coord.subagents.sessions() {
		key := "WORKER-A"
		sets := [][]string{nil}
		if child.owningDelegateID == workerB {
			key = "WORKER-B"
			sets = append(sets, nil)
		}
		assertUpdateRequests(t, a, key, child, sets...)
	}
	for _, id := range []string{firstID, updateID, delegateAttentionID(delegateDeliveryID(workerB, 1))} {
		assertDelegateTestObservation(t, coord, root, id)
	}
	awaitRootReport(t, root)
	assertUpdateCompleted(t, root, coord)
	finishUpdateRoot(ctx, t, root, a, "WAIT_INITIAL", "WAIT_OBSERVED", "COORD_FINAL")
}

// B's update takes custody while the coordinator's A-update request is in
// flight. Only the next request credits B, and marker repair runs no model.
func TestDelegateUpdateArrivingMidTurnIsNotRerunWhenTheMarkerCannotBeWritten(t *testing.T) {
	t.Parallel()
	a := finiteUpdateAdapter(t, "COORD-BRIEF", "WORKER-A", "WORKER-B")
	rootReady := make(chan struct{})
	releaseRoot := sync.OnceFunc(func() { close(rootReady) })
	defer releaseRoot()
	first, second, boundary, observed, reportA, reportB, finish := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseFirst, releaseSecond, releaseBoundary, releaseObserved, releaseA, releaseB, releaseFinish := sync.OnceFunc(func() { close(first) }), sync.OnceFunc(func() { close(second) }), sync.OnceFunc(func() { close(boundary) }), sync.OnceFunc(func() { close(observed) }), sync.OnceFunc(func() { close(reportA) }), sync.OnceFunc(func() { close(reportB) }), sync.OnceFunc(func() { close(finish) })
	defer releaseFirst()
	defer releaseSecond()
	defer releaseBoundary()
	defer releaseObserved()
	defer releaseA()
	defer releaseB()
	defer releaseFinish()
	var workerB string
	a.finiteScript("root", updateReply(delegateToolResponse("coord", "COORD-BRIEF: coordinate.")), updateReply(finalResponse("root waiting")))
	a.finiteScript("COORD-BRIEF",
		updateGate(rootReady, updateReply(delegateToolResponse("a", "WORKER-A: update and report."))),
		updateReply(delegateToolResponse("b", "WORKER-B: update and report.")),
		func(_ context.Context, req llm.Request) (llm.Response, error) {
			workerB = updateChildID(req)
			return updateWait("wait-initial", workerB, "WAIT_INITIAL"), nil
		},
		updateGate(boundary, updateReply(jobListResponse("hold"))),
		updateGate(observed, func(context.Context, llm.Request) (llm.Response, error) {
			return updateWait("wait-updates", workerB, "WAIT_UPDATES"), nil
		}),
		func(context.Context, llm.Request) (llm.Response, error) {
			return updateWait("wait-report-a", workerB, "WAIT_REPORT_A"), nil
		},
		updateGate(finish, updateReply(finalResponse("COORD_FINAL"))),
	)
	a.finiteScript("WORKER-A", updateGate(first, updateReply(updateResponse("first", "FIRST_UPDATE"))), updateGate(reportA, updateReply(finalResponse("A_REPORT"))))
	a.finiteScript("WORKER-B", updateGate(second, updateReply(updateResponse("second", "SECOND_UPDATE"))), updateGate(reportB, updateReply(finalResponse("B_REPORT"))))
	root := newMessagingRoot(t, a, nil)
	traceUpdateFixture(t, root, a)
	serveSupervisionRootWakes(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatal(err)
	}
	releaseRoot()
	reqs := a.waitForRequests(t, "COORD-BRIEF", 3)
	coord := root.getSub(reqs[2].SessionID).sess
	target := updateChildID(reqs[2])
	workerA := updateChildID(reqs[1])
	awaitUpdateYield(t, root, coord, "wait-initial", target)
	faults := installUpdateSyncFault(t, coord, 2)
	releaseFirst()
	a.waitForRequests(t, "COORD-BRIEF", 4)
	firstID := waitForDelegateTestCustody(t, coord, "FIRST_UPDATE")
	assertUpdateSource(t, root, workerA, "FIRST_UPDATE", firstID)
	releaseSecond()
	secondID := waitForDelegateTestCustody(t, coord, "SECOND_UPDATE")
	assertUpdateSource(t, root, target, "SECOND_UPDATE", secondID)
	releaseBoundary()
	a.waitForRequests(t, "COORD-BRIEF", 5)
	releaseObserved()
	waitForCondition(t, 30*time.Second, "mid-turn marker failures", func() bool { return faults.injectedFailures() == 2 })
	awaitUpdateYield(t, root, coord, "wait-updates", target)
	assertUpdateSyncFault(t, faults, firstID, updateResponseID(a, "COORD-BRIEF", 5), 2, firstID, secondID)
	assertDelegateTestObservation(t, coord, root, firstID)
	assertDelegateTestObservation(t, coord, root, secondID)
	assertUpdateEmptyWakes(t, coord)
	sets := [][]string{nil, nil, nil, {"FIRST_UPDATE"}, {"FIRST_UPDATE", "SECOND_UPDATE"}}
	assertUpdateRequests(t, a, "COORD-BRIEF", coord, sets...)
	releaseA()
	a.waitForRequests(t, "COORD-BRIEF", 6)
	awaitUpdateYield(t, root, coord, "wait-report-a", target)
	sets = append(sets, []string{"FIRST_UPDATE", "SECOND_UPDATE", "A_REPORT"})
	assertUpdateRequests(t, a, "COORD-BRIEF", coord, sets...)
	releaseB()
	for _, child := range coord.subagents.sessions() {
		waitForStableSupervisionRun(t, coord, child.ID())
	}
	a.waitForRequests(t, "COORD-BRIEF", 7)
	releaseFinish()
	waitForStableSupervisionRun(t, root, coord.ID())
	sets = append(sets, []string{"FIRST_UPDATE", "SECOND_UPDATE", "A_REPORT", "B_REPORT"})
	assertUpdateRequests(t, a, "COORD-BRIEF", coord, sets...)
	for _, child := range coord.subagents.sessions() {
		key := "WORKER-B"
		if child.owningDelegateID == workerA {
			key = "WORKER-A"
		}
		assertUpdateRequests(t, a, key, child, nil, nil)
	}
	for _, id := range []string{firstID, secondID, delegateAttentionID(delegateDeliveryID(workerA, 1)), delegateAttentionID(delegateDeliveryID(target, 1))} {
		assertDelegateTestObservation(t, coord, root, id)
	}
	awaitRootReport(t, root)
	assertUpdateCompleted(t, root, coord)
	finishUpdateRoot(ctx, t, root, a, "WAIT_INITIAL", "WAIT_UPDATES", "WAIT_REPORT_A", "COORD_FINAL")
}

// A coordinator that is mid-run when its worker's update arrives sees it at
// its own next model boundary, and its own generation consumes the update it
// saw, so no successor generation is started for it.
func TestDelegateUpdateReachesARunningParentDelegate(t *testing.T) {
	t.Parallel()
	singleCoordinatorUpdate(t, "running")
}

// appendColdFixtureEvents appends events to the fixture's delegate journal
// before any root opens it, the way a process that crashed left it.
func appendColdFixtureEvents(t *testing.T, fixture coldStableDelegateFixture, events ...delegatestore.Event) []delegatestore.Event {
	t.Helper()
	path := delegateResourceStorePath(fixture.stateDir, fixture.meta.ID)
	stored, err := delegatestore.ReadEvents(path)
	if err != nil {
		t.Fatalf("read delegate journal: %v", err)
	}
	state, err := delegatestore.Fold(stored)
	if err != nil {
		t.Fatalf("fold delegate journal: %v", err)
	}
	store, err := delegatestore.Open(path)
	if err != nil {
		t.Fatalf("open delegate journal: %v", err)
	}
	appended, _, err := store.AppendBatch(state, events)
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("append delegate events: %v", err)
	}
	return appended
}

// A pending update survives a restart and wakes its parent, whether it was
// still in the delegate's journal or already in the parent's transcript.
func TestDelegateUpdateSurvivesRestart(t *testing.T) {
	t.Parallel()
	// The generation that posted the update was still running at the crash.
	// Restore settles it as runtime_lost, and the parent gets the update,
	// then that report, once each.
	t.Run("posted while the run was still going", func(t *testing.T) {
		t.Parallel()
		fixture := newColdStableDelegateFixture(t, "")
		startedAt := time.Unix(1_700_000_300, 0).UTC()
		appended := appendColdFixtureEvents(t, fixture,
			delegatestore.Event{Kind: delegatestore.EventDelegateRunStarted, TS: startedAt, DelegateID: fixture.delegateID,
				RunStarted: &delegatestore.RunStarted{Generation: 1, Trigger: delegatestore.TriggerOwnerInput, StartedAt: startedAt}},
			delegatestore.Event{Kind: delegatestore.EventDelegateUpdatePosted, TS: startedAt, DelegateID: fixture.delegateID,
				UpdatePosted: &delegatestore.UpdatePosted{Generation: 1, Message: "RESTART_UPDATE: which table holds the drain cursor?"}},
		)
		updateID := delegateAttentionID(delegatestore.UpdateDeliveryID(fixture.delegateID, appended[1].Seq))
		reportID := delegateAttentionID(delegateDeliveryID(fixture.delegateID, 1))
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("noted after the restart") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		pending := waitForRootAttention(t, root, 2)
		if !slices.Equal(pending, []string{updateID, reportID}) {
			t.Fatalf("root attention after restore = %v, want the update then the runtime_lost report", pending)
		}
		if !root.hasPendingNotifications() {
			t.Fatal("restore did not arm the root's wake")
		}
		// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
			t.Fatalf("root notification turn: %v", err)
		}
		requests := fixture.adapter.Requests()
		text := messagesText(requests[len(requests)-1].Messages)
		if u, r := strings.Index(text, "RESTART_UPDATE"), strings.Index(text, `kind="report"`); u < 0 || r < u || strings.Count(text, "RESTART_UPDATE") != 1 {
			t.Fatalf("the root's turn after restore = %q, want the update once, then the report", text)
		}
	})
	t.Run("delivered but not yet read", func(t *testing.T) {
		t.Parallel()
		fixture := newColdStableDelegateFixture(t, "")
		fixture.adapter.steps = []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return communicateResponse(false, "UNREAD_UPDATE: which table?") },
			func(llm.Request) llm.Response { return finalResponse("summary written") },
			func(llm.Request) llm.Response { return finalResponse("noted after the restart") },
		}
		root := restoreSupervisionRoot(t, fixture, nil)
		warmStableDelegateUnservedRoot(t, root, fixture)
		pending := rootPendingAttention(root)
		terminalID := delegateAttentionID(delegateDeliveryID(fixture.delegateID, 1))
		var updateID string
		for _, id := range pending {
			if strings.HasPrefix(id, delegateAttentionID(fixture.delegateID+"/update/")) {
				updateID = id
			}
		}
		if len(pending) != 2 || updateID == "" || !slices.Contains(pending, terminalID) {
			t.Fatalf("root attention before restart = %v, want one update and terminal %s", pending, terminalID)
		}
		root.Close()
		restored := restoreSupervisionRoot(t, fixture, nil)
		if got := rootPendingAttention(restored); !slices.Equal(got, pending) {
			t.Fatalf("root attention after restart = %v, want %v", got, pending)
		}
		if !restored.hasPendingNotifications() {
			t.Fatal("restore did not re-arm the unread update's wake")
		}
		// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := restored.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
			t.Fatalf("root notification turn: %v", err)
		}
		requests := fixture.adapter.Requests()
		if !requestContainsText(requests[len(requests)-1], "UNREAD_UPDATE") || !requestContainsText(requests[len(requests)-1], "summary written") {
			t.Fatal("the restored root's turn did not present the update and actual terminal report")
		}
		if got := rootAttentionFold(t, restored).resolutions[updateID]; got != delegateAttentionConsumed {
			t.Fatalf("update attention after restart = %q, want consumed", got)
		}
	})
}

// Replace only this cold fixture's provider. Descriptor restore, executor
// admission and the dedicated session namer keep their real existing paths.
func newCommonWakeFixture(t *testing.T, steps ...updateStep) (coldStableDelegateFixture, *messagingAdapter) {
	t.Helper()
	fixture := newColdStableDelegateFixture(t, "")
	a := finiteUpdateAdapter(t)
	a.rootID = fixture.meta.ID
	a.sessions[fixture.childID] = "common-wake"
	a.finiteScript("common-wake", steps...)
	fixture.client.Register(a)
	return fixture, a
}

func TestDelegateUpdateCommonWakeFiniteProvider(t *testing.T) {
	t.Parallel()
	fixture, a := newCommonWakeFixture(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2", SessionID: fixture.childID, Messages: []llm.Message{llm.User("common-wake-boundary")}}
	var unexpected []string
	a.onUnexpected = func(detail string) { unexpected = append(unexpected, detail) }
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			response, err := fixture.client.Complete(ctx, req)
			if !errors.Is(err, context.Canceled) || len(response.Message.Content) != 0 || response.ID != "" {
				t.Fatalf("finite common-wake boundary returned err=%v response_id=%q text=%q, want cancellation without successful response", err, response.ID, response.Message.Text())
			}
		})
	}
	t.Run("cancel-blocked-response", func(t *testing.T) {
		entered, gate := make(chan struct{}), make(chan struct{})
		defer close(gate)
		a.finiteScript("common-wake", func(ctx context.Context, req llm.Request) (llm.Response, error) {
			close(entered)
			return updateGate(gate, updateReply(finalResponse("must not succeed")))(ctx, req)
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		type result struct {
			response llm.Response
			err      error
		}
		done := make(chan result, 1)
		go func() {
			response, err := fixture.client.Complete(ctx, req)
			done <- result{response: response, err: err}
		}()
		awaitSignal(t, entered, "common-wake provider cancellation gate")
		cancel()
		// TRIPWIRE: cancellation must release the provider without opening gate.
		select {
		case got := <-done:
			if !errors.Is(got.err, context.Canceled) || got.response.ID != "" || len(got.response.Message.Content) != 0 {
				t.Fatalf("canceled gated response=%+v err=%v", got.response, got.err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("common-wake provider ignored cancellation")
		}
	})
	if len(unexpected) != 2 || len(a.requestsFor("common-wake")) != 3 || len(a.answered["common-wake"]) != 0 {
		t.Fatalf("exhaustion diagnostics=%v requests=%d successful responses=%d", unexpected, len(a.requestsFor("common-wake")), len(a.answered["common-wake"]))
	}
}

// The addressed delegate's common wake path runs one leased provider request
// for accepted input. Duplicate wakes cannot run another request, and an empty
// wake after its successful response has no work to admit.
func TestDelegateUpdateCommonWakeObservesOnce(t *testing.T) {
	t.Parallel()
	entered, gate := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	fixture, a := newCommonWakeFixture(t,
		updateReply(finalResponse("warm result")),
		func(ctx context.Context, req llm.Request) (llm.Response, error) {
			close(entered)
			return updateGate(gate, updateReply(finalResponse("observed update")))(ctx, req)
		},
	)
	root := restoreSupervisionRoot(t, fixture, nil)
	traceUpdateFixture(t, root, a)
	sub := warmStableDelegateUnservedRoot(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	updateID := delegateAttentionID(delegatestore.UpdateDeliveryID(fixture.delegateID, 1))
	armStableSupervisionAttention(t, sub, updateID, "ADMISSION_UPDATE: which table?")
	awaitSignal(t, entered, "addressed delegate provider request")
	if appended, err := sub.sess.appendDelegateNotificationDurably(updateID, "ADMISSION_UPDATE: which table?"); err != nil || appended {
		t.Fatalf("duplicate custody appended=%v err=%v", appended, err)
	}
	for range 2 {
		if err := sub.sess.requestNotificationWake(); err != nil {
			t.Fatal(err)
		}
	}
	fold, err := readDelegateAttentionFold(sub.sess.TranscriptPath(), sub.sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fold.pendingIDs(), []string{updateID}) || len(fold.resolutions) != 0 {
		t.Fatalf("in-flight request granted observation: %+v", fold)
	}
	release()
	waitForStableSupervisionRun(t, root, fixture.childID)
	assertDelegateTestObservation(t, sub.sess, nil, updateID)
	for range 2 {
		if err := sub.sess.requestNotificationWake(); err != nil {
			t.Fatal(err)
		}
	}
	// Wakes call admission synchronously, but accepted runs execute in goroutines.
	// Join their completed generations, recovery and callback opportunities before
	// sampling the finite provider. No quiet delay or shutdown hides a late call.
	waitForStableSupervisionRun(t, root, fixture.childID)
	waitForCondition(t, 30*time.Second, "common-wake execution and recovery released", func() bool {
		state := sub.sess.notificationExecutionState()
		sub.sess.attentionMu.Lock()
		callbacksDone := sub.sess.attentionCallbacks == 0
		sub.sess.attentionMu.Unlock()
		return !state.running && !state.recovering && callbacksDone
	})
	root.sendersWG.Wait()
	assertUpdateRequests(t, a, "common-wake", sub.sess, nil, []string{"ADMISSION_UPDATE: which table?"})
}

// awaitRootReport waits until the root's transcript holds attention for a
// delegate's report, which is any attention that is not an update.
func awaitRootReport(t *testing.T, root *Session) {
	t.Helper()
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the coordinator's report to reach the root", func() bool {
		fold, err := readDelegateAttentionFold(transcriptPath(root.stateDir, root.ID()), root.ID())
		return err == nil && slices.ContainsFunc(fold.order, func(id string) bool { return !isDelegateUpdateAttentionID(id) })
	})
}

// An update refused because the controller is retiring says the delegate is
// stopping or finishing, the same as any other closed route, and never leaks
// the controller's own error text.
func TestNotDeliveredUpdateExplainsARetiringController(t *testing.T) {
	t.Parallel()
	want := notDeliveredUpdate(errDelegateTargetBusy)
	got := notDeliveredUpdate(fmt.Errorf("post: %w", ErrRetirementUnavailable))
	if *got != *want {
		t.Fatalf("retiring controller delivery = %#v, want %#v", got, want)
	}
	if got.Status != delegateUpdateNotDelivered || strings.Contains(got.Reason, "could not be recorded") {
		t.Fatalf("retiring controller delivery = %#v, want the stopping or finishing reason", got)
	}
}

// drainingCoordinatorReport runs a coordinator that ends its turn with report
// A while a worker is still running, then lets the worker's update wake its
// finalization drain, where drainTurns script its replies. It returns the
// report frame and the reportPreview the root received, and the coordinator's
// transcript text.
//
// With undisposedJob the coordinator starts a background shell job instead of
// a worker and never disposes of it, so its drain announces the job to it.
func drainingCoordinatorReport(t *testing.T, undisposedJob bool, drainTurns ...func(llm.Request) llm.Response) (frame, preview, transcript string, coordinatorRequests int) {
	t.Helper()
	adapter := newMessagingAdapter("COORD-BRIEF", "WORKER-BRIEF")
	coordinatorIdle, reportGate := make(chan struct{}), make(chan struct{})
	releaseReport := sync.OnceFunc(func() { close(reportGate) })
	defer releaseReport()
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("coord", "COORD-BRIEF: start one worker delegate, then report and wait for it.")
		},
		func(llm.Request) llm.Response { return finalResponse("waiting on the coordinator") },
	)
	startWork := func(llm.Request) llm.Response {
		return delegateToolResponse("worker", "WORKER-BRIEF: ask your parent which table holds the drain cursor.")
	}
	if undisposedJob {
		startWork = func(llm.Request) llm.Response {
			args, _ := json.Marshal(map[string]any{"command": "sleep 600", "mode": "background"})
			return toolCallResponse(llm.ToolCallData{ID: "job", Name: "shell", Arguments: args, Type: "function"})
		}
	}
	coordinatorSteps := []func(llm.Request) llm.Response{
		startWork,
		func(llm.Request) llm.Response { return communicateResponse(true, "REPORT_A: waiting on the worker") },
	}
	for _, drainTurn := range drainTurns {
		coordinatorSteps = append(coordinatorSteps, func(req llm.Request) llm.Response {
			releaseReport()
			return drainTurn(req)
		})
	}
	adapter.script("COORD-BRIEF", coordinatorSteps...)
	adapter.script("WORKER-BRIEF",
		func(llm.Request) llm.Response {
			awaitSignal(t, coordinatorIdle, "the coordinator to end its turn")
			return updateResponse("upd", "DRAIN_UPDATE: which table holds the drain cursor?")
		},
		func(llm.Request) llm.Response {
			awaitSignal(t, reportGate, "the coordinator's drain turn")
			return finalResponse("worker done")
		},
	)
	// The worker's own report can start another coordinator generation, which
	// would change the preview. Hold the first generation at its release
	// boundary, where its report is delivered, and read both views there.
	atRelease, releaseGeneration := make(chan struct{}, 1), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseGeneration) })
	defer release()
	root := newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.TurnEndsProcess = undisposedJob
		cfg.testOnly.subagentBeforeGenerationReleased = func(a *subagent, lease delegateLease) {
			a.mu.Lock()
			isCoordinator := a.stableDescriptor != nil && strings.HasPrefix(a.stableDescriptor.Task, "COORD-BRIEF")
			a.mu.Unlock()
			if isCoordinator && lease.generation == 1 {
				atRelease <- struct{}{}
				<-releaseGeneration
			}
		}
	})
	serveSupervisionRootWakes(t, root)
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := root.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	adapter.waitForRequests(t, "COORD-BRIEF", 2)
	coordinator := waitForSoleChildIdle(t, root)
	close(coordinatorIdle)
	awaitSignal(t, atRelease, "the coordinator's first generation to reach its release boundary")
	// The preview follows the settled run, which is finished while the
	// generation is held.
	// TRIPWIRE: every answer is scripted in process; only a hang reaches it.
	waitForCondition(t, 30*time.Second, "the report preview to settle", func() bool {
		page, err := root.ListActivityDelegates(ctx, appwire.SessionActivityListParams{Ref: encodeRef("", root.ID())})
		if err != nil || len(page.Delegates) != 1 {
			return false
		}
		preview = page.Delegates[0].ReportPreview
		return preview != ""
	})
	// Counted while the generation is held, before a successor can start.
	coordinatorRequests = len(adapter.requestsFor("COORD-BRIEF"))
	release()
	awaitRootReport(t, root)
	fold := rootAttentionFold(t, root)
	reportAt := slices.IndexFunc(fold.order, func(id string) bool { return !isDelegateUpdateAttentionID(id) })
	if id := fold.order[reportAt]; !strings.HasSuffix(id, "/delivery/1") {
		t.Fatalf("the root's first report is %q, want the first generation's delivery", id)
	}
	frame = fold.content[fold.order[reportAt]].Text()
	transcriptRaw, err := os.ReadFile(transcriptPath(coordinator.stateDir, coordinator.ID()))
	if err != nil {
		t.Fatalf("read the coordinator's transcript: %v", err)
	}
	return frame, preview, string(transcriptRaw), coordinatorRequests
}

// bareTextDrainTurn is a drain turn whose model never calls communicate.
func bareTextDrainTurn(llm.Request) llm.Response {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "told the worker"}}}}
}

// A coordinator's later turn in its drain replaces its report: the root
// receives the newer report, in its frame and in the delegate's reportPreview,
// and the earlier one stays in the coordinator's transcript.
func TestDelegateDrainTurnReportReplacesTheEarlierReport(t *testing.T) {
	t.Parallel()
	frame, preview, transcript, _ := drainingCoordinatorReport(t, false, func(llm.Request) llm.Response {
		return communicateResponse(true, "REPORT_B: told the worker to use the v2 schema")
	})
	if !strings.Contains(frame, "REPORT_B") || strings.Contains(frame, "REPORT_A") {
		t.Fatalf("the root's report frame = %q, want the drain turn's report B and not A", frame)
	}
	if preview != "REPORT_B: told the worker to use the v2 schema" {
		t.Fatalf("reportPreview = %q, want report B", preview)
	}
	if !strings.Contains(transcript, "REPORT_A") {
		t.Fatal("the coordinator's transcript lost its earlier report A")
	}
}

// A drain turn whose model never reports ends the turn, not the run: the
// earlier report stands as a completed report, not as a failure. A reply of
// bare text ends a notification turn at once, and an empty response is
// retried until it gives up.
func TestDelegateDrainTurnWithoutAReportKeepsTheEarlierReport(t *testing.T) {
	t.Parallel()
	emptyResponse := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant}}
	}
	for name, tc := range map[string]struct {
		turn     func(llm.Request) llm.Response
		attempts int
	}{
		"bare text":      {bareTextDrainTurn, 1},
		"empty response": {emptyResponse, maxEmptyRetries + 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// Scripted past the attempts, so an extra request would be a model
			// turn the drain turn should not have made.
			turns := slices.Repeat([]func(llm.Request) llm.Response{tc.turn}, tc.attempts+1)
			frame, preview, _, requests := drainingCoordinatorReport(t, false, turns...)
			if !strings.Contains(frame, "REPORT_A") || !strings.Contains(frame, `"kind":"reported"`) || !strings.Contains(frame, `"outcome":"completed"`) {
				t.Fatalf("the root's report frame = %q, want the earlier completed report A", frame)
			}
			if preview != "REPORT_A: waiting on the worker" {
				t.Fatalf("reportPreview = %q, want report A", preview)
			}
			// The delegate call, report A, and the drain turn's attempts.
			if want := 2 + tc.attempts; requests != want {
				t.Fatalf("the coordinator made %d model requests, want %d", requests, want)
			}
		})
	}
}

// A drain that announces an undisposed background job to the coordinator
// discards the announcement turns' text. The report still comes whole from the
// latest accepted report, never from the text the drain returned.
func TestDelegateDrainAnnouncementReportReplacesTheEarlierReport(t *testing.T) {
	t.Parallel()
	frame, preview, _, _ := drainingCoordinatorReport(t, true,
		func(llm.Request) llm.Response { return communicateResponse(true, "REPORT_B: left the job running") },
		func(llm.Request) llm.Response { return communicateResponse(true, "REPORT_C: detached the job") },
	)
	if !strings.Contains(frame, "REPORT_C") || strings.Contains(frame, "REPORT_A") {
		t.Fatalf("the root's report frame = %q, want the latest announcement turn's report C alone", frame)
	}
	if preview != "REPORT_C: detached the job" {
		t.Fatalf("reportPreview = %q, want report C", preview)
	}
}
