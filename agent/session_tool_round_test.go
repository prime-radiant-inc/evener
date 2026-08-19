package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// These two tests cover part of review round 2's finding:
// session_tool_round.go's direct-append steering sites (no-tool-calls retry,
// task reminder) labeled the live SteeringInjectedData event correctly but
// left schema.Turn.SteeringKind empty, so a reload showed no kind at all.
// Each asserts the kind lands on the appended turn, read off s.history the
// way round 1's tests read the flushed compaction records. The third
// direct-append site in this file, loop detection, is covered by extending
// the existing TestSession_LoopDetection_EmitsEventAndInjectsSteering
// (session_lifecycle_test.go) instead of a new test here, since that test
// already drives the real multi-round trigger path end to end.

// TestApplyNoToolCallsDecision_PersistsNoToolCallsKind drives the dec.Retry
// branch directly (the same seam session_tool_round_tail_coverage_fuzz_test.go
// uses under the evenerfuzz tag) rather than through a full model round.
func TestApplyNoToolCallsDecision_PersistsNoToolCallsKind(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	retry, err := s.applyNoToolCallsDecision(noToolCallsDecision{Retry: true, SteeringText: "go on"})
	if !retry || err != nil {
		t.Fatalf("applyNoToolCallsDecision = retry %v err %v, want retry=true err=nil", retry, err)
	}
	s.mu.Lock()
	last := s.history[len(s.history)-1]
	s.mu.Unlock()
	if last.Kind != schema.TurnSteering {
		t.Fatalf("last turn kind = %v, want TurnSteering", last.Kind)
	}
	if last.SteeringKind != events.SteeringKindNoToolCalls {
		t.Errorf("SteeringKind = %q, want %q", last.SteeringKind, events.SteeringKindNoToolCalls)
	}
}

// TestInjectPostToolSteering_PersistsTaskReminderKind drives the task-reminder
// tail of injectPostToolSteering (trigger 3: task_list never used, 10+
// rounds in) directly. Only one trigger is exercised here — the site's fix
// (appendSteeringTurn) is generic over whichever kind maybeInjectTaskReminder
// returns, and round 1's TestMaybeInjectTaskReminder_* tests already cover
// that each trigger returns the right kind value.
func TestInjectPostToolSteering_PersistsTaskReminderKind(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.mu.Lock()
	s.totalRounds = 10 // trigger 3: never used task_list, 10+ rounds in.
	s.mu.Unlock()

	var toolSigs []string
	var toolSigFailed []bool
	if _, err := s.injectPostToolSteering(context.Background(), nil, nil, &toolSigs, &toolSigFailed); err != nil {
		t.Fatalf("injectPostToolSteering: %v", err)
	}

	s.mu.Lock()
	last := s.history[len(s.history)-1]
	s.mu.Unlock()
	if last.Kind != schema.TurnSteering {
		t.Fatalf("last turn kind = %v, want TurnSteering", last.Kind)
	}
	if last.SteeringKind != events.SteeringKindTaskNudge {
		t.Errorf("SteeringKind = %q, want %q", last.SteeringKind, events.SteeringKindTaskNudge)
	}
}

func TestDelegateAttention_DeliveryCommitUsesCallerToolResultFsync(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, firstWaiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	firstPlan := finishDelegateDeliveryGeneration(t, c, lease, "first").deliveries[0]
	if _, err := deliverDelegatePacket(firstPlan, nil); err != nil {
		t.Fatalf("handoff inline delivery: %v", err)
	}
	resolution := <-firstWaiter.resolution
	fs := newDelegateToolResultBarrierFS()
	sess := newDelegateToolResultPersistenceSession(t, c, fs)
	fs.blockSync = true
	sess.queueDelegateDeliveryCommit("delegate-send", resolution.commit)

	done := make(chan error, 1)
	go func() { done <- appendDelegateToolResultFixture(sess, "delegate-send") }()
	select {
	case <-fs.syncEntered:
	case err := <-done:
		t.Fatalf("caller tool-result append returned before fsync: %v", err)
	}
	if got := len(c.durable["dlg_target"].PendingDeliveries); got != 1 {
		t.Fatalf("pending deliveries at blocked fsync = %d, want 1", got)
	}
	close(fs.allowSync)
	if err := <-done; err != nil {
		t.Fatalf("append caller tool results: %v", err)
	}
	entries := decodeTranscriptEntries(t, fs.Fs, "/caller.jsonl")
	last := entries[len(entries)-1].Turn
	if len(last.DelegateDeliveryCommits) != 1 || last.DelegateDeliveryCommits[0].ToolCallID != "delegate-send" || last.DelegateDeliveryCommits[0].DeliveryID != firstPlan.deliveryID {
		t.Fatalf("persisted delivery commits = %#v", last.DelegateDeliveryCommits)
	}
}

func TestDelegateAttention_DeliveryCommitAppendFailureLeavesNAndNPlusOnePending(t *testing.T) {
	c, firstPlan, firstWaiter, _, secondWaiter := controllerWithTwoDelegateDeliveries(t, true, true)
	if _, err := deliverDelegatePacket(firstPlan, nil); err != nil {
		t.Fatalf("handoff inline delivery: %v", err)
	}
	resolution := <-firstWaiter.resolution
	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	sess := newDelegateToolResultPersistenceSession(t, c, fs)
	sess.queueDelegateDeliveryCommit("delegate-send", resolution.commit)
	fs.fail = true

	if err := appendDelegateToolResultFixture(sess, "delegate-send"); !errors.Is(err, errInjectedTranscriptWrite) {
		t.Fatalf("append caller tool results = %v, want injected failure", err)
	}
	if got := len(c.durable["dlg_target"].PendingDeliveries); got != 2 {
		t.Fatalf("pending deliveries after append failure = %d, want 2", got)
	}
	c.mu.Lock()
	secondStillWaiting := c.live["dlg_target"].waiters[2] == secondWaiter
	c.mu.Unlock()
	if !secondStillWaiting {
		t.Fatal("N+1 waiter was released after N caller append failure")
	}
}

func TestDelegateAttention_DeliveryCommitReleasesNPlusOneOnlyAfterNFsync(t *testing.T) {
	c, firstPlan, firstWaiter, _, secondWaiter := controllerWithTwoDelegateDeliveries(t, true, true)
	if _, err := deliverDelegatePacket(firstPlan, nil); err != nil {
		t.Fatalf("handoff inline delivery: %v", err)
	}
	resolution := <-firstWaiter.resolution
	fs := newDelegateToolResultBarrierFS()
	sess := newDelegateToolResultPersistenceSession(t, c, fs)
	fs.blockSync = true
	sess.queueDelegateDeliveryCommit("delegate-send", resolution.commit)

	done := make(chan error, 1)
	go func() { done <- appendDelegateToolResultFixture(sess, "delegate-send") }()
	select {
	case <-fs.syncEntered:
	case err := <-done:
		t.Fatalf("caller tool-result append returned before fsync: %v", err)
	}
	select {
	case got := <-secondWaiter.resolution:
		t.Fatalf("N+1 released before N fsync: %#v", got)
	default:
	}
	close(fs.allowSync)
	if err := <-done; err != nil {
		t.Fatalf("append caller tool results: %v", err)
	}
	select {
	case got := <-secondWaiter.resolution:
		if got.packet == nil || got.commit == nil || got.fallback {
			t.Fatalf("N+1 resolution = %#v", got)
		}
	default:
		t.Fatal("N+1 was not released after N caller fsync")
	}
}

func TestDelegateAttention_DeliveryCommitsPreserveExactToolCallPairsAndStayProviderPrivate(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 2)
	seedDelegateControllerIdle(t, c, "dlg_first", "")
	seedDelegateControllerIdle(t, c, "dlg_second", "")
	firstLease, firstWaiter := startDelegateDeliveryGeneration(t, c, "dlg_first", true)
	firstPlan := finishDelegateDeliveryGeneration(t, c, firstLease, "first result").deliveries[0]
	secondLease, secondWaiter := startDelegateDeliveryGeneration(t, c, "dlg_second", true)
	secondPlan := finishDelegateDeliveryGeneration(t, c, secondLease, "second result").deliveries[0]
	if _, err := deliverDelegatePacket(firstPlan, nil); err != nil {
		t.Fatalf("deliver first packet: %v", err)
	}
	if _, err := deliverDelegatePacket(secondPlan, nil); err != nil {
		t.Fatalf("deliver second packet: %v", err)
	}
	firstResolution := <-firstWaiter.resolution
	secondResolution := <-secondWaiter.resolution

	fs := afero.NewMemMapFs()
	sess := newDelegateToolResultPersistenceSession(t, c, fs)
	sess.queueDelegateDeliveryCommit("call-first", firstResolution.commit)
	sess.queueDelegateDeliveryCommit("call-second", secondResolution.commit)
	calls := []llm.ToolCallData{
		{ID: "call-second", Name: "delegate_send"},
		{ID: "call-first", Name: "delegate_send"},
	}
	results := []tool.ExecResult{
		{CallID: "call-second", ToolName: "delegate_send", Output: `{"status":"completed"}`},
		{CallID: "call-first", ToolName: "delegate_send", Output: `{"status":"completed"}`},
	}
	parts := []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call-second", Name: "delegate_send", Content: results[0].Output}},
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call-first", Name: "delegate_send", Content: results[1].Output}},
	}
	if err := sess.appendToolResults(context.Background(), calls, results, parts); err != nil {
		t.Fatalf("append paired delegate results: %v", err)
	}

	entries := decodeTranscriptEntries(t, fs, "/caller.jsonl")
	persisted := entries[len(entries)-1].Turn
	want := map[string]string{
		"call-first":  firstPlan.deliveryID,
		"call-second": secondPlan.deliveryID,
	}
	if len(persisted.DelegateDeliveryCommits) != len(want) {
		t.Fatalf("persisted delivery pairs = %#v", persisted.DelegateDeliveryCommits)
	}
	for _, commit := range persisted.DelegateDeliveryCommits {
		if want[commit.ToolCallID] != commit.DeliveryID {
			t.Fatalf("persisted delivery pair = %#v, want %q", commit, want[commit.ToolCallID])
		}
		delete(want, commit.ToolCallID)
	}
	if len(want) != 0 {
		t.Fatalf("missing persisted delivery pairs = %#v", want)
	}
	sess.mu.Lock()
	live := sess.history[len(sess.history)-1]
	sess.mu.Unlock()
	if len(live.DelegateDeliveryCommits) != 0 {
		t.Fatalf("live provider turn exposed delivery commits = %#v", live.DelegateDeliveryCommits)
	}
	providerJSON, err := json.Marshal(expandHistory([]schema.Turn{live}, replayScope{}))
	if err != nil {
		t.Fatalf("marshal provider history: %v", err)
	}
	if bytes.Contains(providerJSON, []byte(firstPlan.deliveryID)) || bytes.Contains(providerJSON, []byte(secondPlan.deliveryID)) {
		t.Fatalf("provider history exposed private delivery IDs: %s", providerJSON)
	}
}

func newDelegateToolResultPersistenceSession(t *testing.T, c *delegateTreeController, fs afero.Fs) *Session {
	t.Helper()
	writer, err := transcript.NewWriterWithFS(fs, "/caller.jsonl", transcript.Header{SessionID: "caller"})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	sess := &Session{id: "caller", delegateController: c}
	sess.attachTranscript(writer)
	c.rootRuntime = sess
	return sess
}

func appendDelegateToolResultFixture(sess *Session, callID string) error {
	calls := []llm.ToolCallData{{ID: callID, Name: "delegate_send"}}
	results := []tool.ExecResult{{CallID: callID, ToolName: "delegate_send", Output: `{"status":"completed"}`}}
	parts := []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: callID, Name: "delegate_send", Content: results[0].Output}}}
	return sess.appendToolResults(context.Background(), calls, results, parts)
}

func decodeTranscriptEntries(t *testing.T, fs afero.Fs, path string) []transcript.Entry {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	if !scanner.Scan() {
		t.Fatal("transcript header missing")
	}
	var entries []transcript.Entry
	for scanner.Scan() {
		entry, err := transcript.DecodeEntry(scanner.Bytes())
		if err != nil {
			t.Fatalf("decode transcript entry: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan transcript: %v", err)
	}
	return entries
}

type delegateToolResultBarrierFS struct {
	afero.Fs
	syncEntered chan struct{}
	allowSync   chan struct{}
	blockSync   bool
	once        sync.Once
}

func newDelegateToolResultBarrierFS() *delegateToolResultBarrierFS {
	return &delegateToolResultBarrierFS{
		Fs:          afero.NewMemMapFs(),
		syncEntered: make(chan struct{}),
		allowSync:   make(chan struct{}),
	}
}

func (fs *delegateToolResultBarrierFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &delegateToolResultBarrierFile{File: file, fs: fs}, nil
}

type delegateToolResultBarrierFile struct {
	afero.File
	fs *delegateToolResultBarrierFS
}

func (file *delegateToolResultBarrierFile) Sync() error {
	if !file.fs.blockSync {
		return file.File.Sync()
	}
	file.fs.once.Do(func() { close(file.fs.syncEntered) })
	<-file.fs.allowSync
	return file.File.Sync()
}
