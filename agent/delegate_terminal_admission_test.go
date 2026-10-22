package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/tool"
)

func newAdmissionSession(t *testing.T) (*Session, *jobManager) {
	t.Helper()
	s := newTestSession(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "jobs.jsonl"), nil, 0o600); err != nil {
		t.Fatalf("create empty job journal: %v", err)
	}
	jm := &jobManager{dir: dir, sessionID: s.id}
	original := s.jobManager
	s.jobManager = jm
	t.Cleanup(func() { s.jobManager = original })
	return s, jm
}

func assertSteerMentions(t *testing.T, s *Session, want string) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, message := range s.steeringQueue {
		if strings.Contains(message.Text, want) {
			return
		}
	}
	t.Fatalf("steering queue %+v does not mention %q", s.steeringQueue, want)
}

func assertNoSteer(t *testing.T, s *Session) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steeringQueue) != 0 {
		t.Fatalf("steering queue = %+v, want none", s.steeringQueue)
	}
}

func TestTerminalAdmissionRefusesDoneWithRunningJob(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.running = map[string]*runningJob{"job-1": {}}
	jm.mu.Unlock()

	admitted, err := (&subagent{sess: s}).admitTerminalReport(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonDone,
	})
	if err != nil {
		t.Fatalf("admitTerminalReport: %v", err)
	}
	if admitted {
		t.Fatal("done was admitted while a job was running")
	}
	assertSteerMentions(t, s, "job:job-1")
}

func TestTerminalAdmissionRefusesSameBatchWatchCreation(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.watches = map[watchKey]*watchConfig{
		{VisibleSessionID: s.id, Target: "job-1", ReceiverSessionID: s.id}: {watchID: "watch-1", receiverSessionID: s.id},
	}
	jm.mu.Unlock()

	admitted, err := (&subagent{sess: s}).admitTerminalReport(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonDone,
	})
	if err != nil {
		t.Fatalf("admitTerminalReport: %v", err)
	}
	if admitted {
		t.Fatal("done was admitted while a watch was live")
	}
	assertSteerMentions(t, s, "watch:watch-1")
}

func TestTerminalAdmissionRefusesPendingChildDelivery(t *testing.T) {
	s, _ := newAdmissionSession(t)
	s.delegateDeliveryMu.Lock()
	s.pendingDelegateDeliveries = []delegateDeliveryPlan{{deliveryID: "delivery-1"}}
	s.delegateDeliveryMu.Unlock()

	admitted, err := (&subagent{sess: s}).admitTerminalReport(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonDone,
	})
	if err != nil {
		t.Fatalf("admitTerminalReport: %v", err)
	}
	if admitted {
		t.Fatal("done was admitted while a child delivery was pending")
	}
	assertSteerMentions(t, s, "child-delivery:delivery-1")
}

func TestTerminalAdmissionRefusesUnknownEvidence(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.dir = t.TempDir()

	admitted, err := (&subagent{sess: s}).admitTerminalReport(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonDone,
	})
	if err == nil {
		t.Fatal("admitTerminalReport error = nil, want unreadable evidence to terminate")
	}
	if admitted {
		t.Fatal("done was admitted with unreadable job evidence")
	}
	assertNoSteer(t, s)
}

func TestTerminalAdmissionPublishesEmptyDone(t *testing.T) {
	s, _ := newAdmissionSession(t)
	admitted, err := (&subagent{sess: s}).admitTerminalReport(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonDone,
	})
	if err != nil {
		t.Fatalf("admitTerminalReport: %v", err)
	}
	if !admitted {
		t.Fatal("done was refused with no outstanding work")
	}
	assertNoSteer(t, s)
}

func TestTerminalAdmissionPublishesBlockedWithLiveWork(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.running = map[string]*runningJob{"job-1": {}}
	jm.mu.Unlock()
	s.comm = communicateResult{
		called:    true,
		text:      "waiting on the environment",
		endReason: tool.CommunicateEndReasonBlocked,
		blocker:   "repository access",
	}
	a := &subagent{sess: s}

	admitted, err := a.admitTerminalReport(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonBlocked,
		Blocker:     "repository access",
	})
	if err != nil {
		t.Fatalf("admitTerminalReport: %v", err)
	}
	if !admitted {
		t.Fatal("blocked was refused while work was live")
	}
	finish := a.stableDelegateFinish("waiting on the environment", nil)
	if finish.packet == nil {
		t.Fatal("blocked finish has no packet")
	}
	var metadata map[string]any
	if err := json.Unmarshal(finish.packet.Metadata, &metadata); err != nil {
		t.Fatalf("packet metadata: %v", err)
	}
	if got := metadata["blocker"]; got != "repository access" {
		t.Fatalf("blocker metadata = %#v, want repository access", got)
	}
	work, _ := metadata["outstanding_work"].([]any)
	if !slices.ContainsFunc(work, func(value any) bool { return value == "job:job-1" }) {
		t.Fatalf("outstanding_work = %#v, want job:job-1", work)
	}
}

func TestTerminalAdmissionBlockedAddsWorkToEmptyMetadata(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.running = map[string]*runningJob{"job-1": {}}
	jm.mu.Unlock()
	candidate := &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonBlocked,
		Blocker:     "repository access",
	}
	admitted, err := (&subagent{sess: s}).admitTerminalReport(context.Background(), candidate)
	if err != nil {
		t.Fatalf("admitTerminalReport: %v", err)
	}
	if !admitted {
		t.Fatal("blocked was refused while work was live")
	}
	var metadata map[string]any
	if err := json.Unmarshal(candidate.Packet.Metadata, &metadata); err != nil {
		t.Fatalf("candidate metadata: %v", err)
	}
	work, _ := metadata["outstanding_work"].([]any)
	if !slices.ContainsFunc(work, func(value any) bool { return value == "job:job-1" }) {
		t.Fatalf("outstanding_work = %#v, want job:job-1", work)
	}
}

func TestTerminalAdmissionBlockedKeepsOwnedShell(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.running = map[string]*runningJob{"job-1": {delegateShell: &stableDelegateShellReceipt{jobID: "job-1"}}}
	jm.mu.Unlock()
	a := &subagent{sess: s}

	work, err := s.outstandingDelegateWork()
	if err != nil {
		t.Fatalf("outstandingDelegateWork: %v", err)
	}
	if !slices.Contains(work.Jobs, "job-1") {
		t.Fatalf("jobs = %v, want the owned shell", work.Jobs)
	}
	admitted, err := a.admitTerminalReport(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonBlocked,
		Blocker:     "access",
	})
	if err != nil || !admitted {
		t.Fatalf("blocked admission = %v, %v; want admitted", admitted, err)
	}
	admitted, err = a.admitTerminalReport(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonDone,
	})
	if err != nil || !admitted {
		t.Fatalf("done admission = %v, %v; want the owned shell drained", admitted, err)
	}
}

func TestTerminalAdmissionWithAttentionRefusesUnresolvedReport(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	s := &Session{id: "root-session", stateDir: c.stateDir, transcriptReady: true}
	writeDelegateAttentionTranscript(t, transcriptPath(s.stateDir, s.id), s.id, "attention-1")
	if ids, err := s.pendingDelegateAttentionIDs(); err != nil || len(ids) == 0 {
		t.Fatalf("pending attention = %v, err = %v", ids, err)
	}

	admitted, err := (&subagent{sess: s}).admitTerminalReportWithAttention(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonDone,
	})
	if err != nil {
		t.Fatalf("admitTerminalReportWithAttention: %v", err)
	}
	if admitted {
		t.Fatal("done was admitted with unresolved delegate attention")
	}
	assertSteerMentions(t, s, "attention:attention-1")
}

func TestTerminalAdmissionRestartSafety(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.running = map[string]*runningJob{"job-1": {}}
	jm.mu.Unlock()

	packet := delegatestore.TerminalPacket{Metadata: json.RawMessage(`{"run_ended_at":"2026-01-01T00:00:00Z"}`)}
	refused := &terminalReportCandidate{Disposition: tool.CommunicateEndReasonDone, Packet: packet}
	admitted, err := (&subagent{sess: s}).admitTerminalReport(context.Background(), refused)
	if err != nil {
		t.Fatalf("admitTerminalReport: %v", err)
	}
	if admitted {
		t.Fatal("done was admitted while a job was running")
	}
	if string(refused.Packet.Metadata) != string(packet.Metadata) {
		t.Fatalf("refused candidate metadata = %s, want it unchanged", refused.Packet.Metadata)
	}

	s.comm = communicateResult{called: true, text: "blocked", endReason: tool.CommunicateEndReasonBlocked, blocker: "access"}
	a := &subagent{sess: s}
	blocked := &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonBlocked,
		Blocker:     "access",
		Packet:      delegatestore.TerminalPacket{Metadata: json.RawMessage(`{"run_ended_at":"2026-01-01T00:00:00Z"}`)},
	}
	admitted, err = a.admitTerminalReport(context.Background(), blocked)
	if err != nil {
		t.Fatalf("admitTerminalReport blocked: %v", err)
	}
	if !admitted {
		t.Fatal("blocked was refused while work was live")
	}
	var metadata map[string]any
	if err := json.Unmarshal(blocked.Packet.Metadata, &metadata); err != nil {
		t.Fatalf("blocked metadata: %v", err)
	}
	if _, ok := metadata["run_ended_at"]; !ok {
		t.Fatalf("blocked metadata = %#v, want the prepared run_ended_at preserved", metadata)
	}
}

func TestDelegateWaitOnRunningJobKeepsRunOpenAndResumes(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.running = map[string]*runningJob{"job-1": {}}
	jm.mu.Unlock()
	a := &subagent{sess: s}

	accepted, err := a.admitTerminalWait(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonWaitingOnWork,
		WaitingOn:   "job-1",
	})
	if err != nil {
		t.Fatalf("admitTerminalWait: %v", err)
	}
	if !accepted {
		t.Fatal("wait on a running job was refused")
	}
	assertNoSteer(t, s)
	work, err := s.outstandingDelegateWork()
	if err != nil {
		t.Fatalf("outstandingDelegateWork: %v", err)
	}
	if !slices.Contains(work.Jobs, "job-1") {
		t.Fatalf("jobs = %v, want the awaited job still live", work.Jobs)
	}

	jm.mu.Lock()
	jm.running = nil
	jm.mu.Unlock()
	admitted, err := a.admitTerminalReport(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonDone,
	})
	if err != nil {
		t.Fatalf("admitTerminalReport after wake: %v", err)
	}
	if !admitted {
		t.Fatal("the later done report was refused after the awaited job settled")
	}
}

func TestDelegateWaitOnSettledJobIsRefusedAndSteered(t *testing.T) {
	s, _ := newAdmissionSession(t)
	accepted, err := (&subagent{sess: s}).admitTerminalWait(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonWaitingOnWork,
		WaitingOn:   "job-1",
	})
	if err != nil {
		t.Fatalf("admitTerminalWait: %v", err)
	}
	if accepted {
		t.Fatal("wait on a settled job was accepted")
	}
	assertSteerMentions(t, s, "job-1")
}

func TestDelegateWaitOnRemovedWatchGetsCancellationNotice(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.watches = map[watchKey]*watchConfig{
		{VisibleSessionID: s.id, Target: "job-1", ReceiverSessionID: s.id}: {watchID: "watch-1", receiverSessionID: s.id},
	}
	jm.mu.Unlock()
	jm.mu.Lock()
	delete(jm.watches, watchKey{VisibleSessionID: s.id, Target: "job-1", ReceiverSessionID: s.id})
	jm.mu.Unlock()

	accepted, err := (&subagent{sess: s}).admitTerminalWait(context.Background(), &terminalReportCandidate{
		Disposition: tool.CommunicateEndReasonWaitingOnWork,
		WaitingOn:   "watch-1",
	})
	if err != nil {
		t.Fatalf("admitTerminalWait: %v", err)
	}
	if accepted {
		t.Fatal("wait on a removed watch was accepted")
	}
	assertSteerMentions(t, s, "watch-1")
}

func TestDelegateWaitOnChildMustBeDescendant(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerRunning(t, c, "dlg_self", "")
	seedDelegateControllerRunning(t, c, "dlg_child", "dlg_self")
	seedDelegateControllerRunning(t, c, "dlg_sibling", "")
	s := &Session{id: "child-session", delegateController: c, owningDelegateID: "dlg_self"}

	if err := s.validateWaitTarget(context.Background(), "dlg_sibling"); err == nil {
		t.Fatal("a sibling delegate was accepted as a wait target")
	}
	if err := s.validateWaitTarget(context.Background(), "dlg_self"); err == nil {
		t.Fatal("the caller itself was accepted as a wait target")
	}
	if err := s.validateWaitTarget(context.Background(), "dlg_child"); err != nil {
		t.Fatalf("descendant child wait target: %v", err)
	}
}

func TestDelegateWaitSatisfiedByJobWatchAndInput(t *testing.T) {
	s, jm := newAdmissionSession(t)
	jm.mu.Lock()
	jm.running = map[string]*runningJob{"job-1": {}}
	jm.mu.Unlock()
	if s.terminalWaitSatisfied(context.Background(), "job-1") {
		t.Fatal("a running job satisfied the wait")
	}
	jm.mu.Lock()
	delete(jm.running, "job-1")
	jm.mu.Unlock()
	if !s.terminalWaitSatisfied(context.Background(), "job-1") {
		t.Fatal("a settled job did not satisfy the wait")
	}

	s.inputQueue = []queuedInput{{ID: "input-1"}}
	if !s.terminalWaitSatisfied(context.Background(), "input-1") {
		t.Fatal("queued input did not satisfy the wait")
	}
	s.steeringQueue = []steeringMessage{{StableTurnID: "steer-1", Text: "parent note"}}
	if !s.terminalWaitSatisfied(context.Background(), "job-1") {
		t.Fatal("parent steering did not satisfy the wait")
	}
}
