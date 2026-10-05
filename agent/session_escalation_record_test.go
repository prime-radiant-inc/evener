package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
)

// recordingEscalationSession is escalatableSession with a transcript, so the
// approval history NOTICE has somewhere to land.
func recordingEscalationSession(t *testing.T) *Session {
	t.Helper()
	s := newSession(t, withConfig(SessionConfig{
		StateDir: t.TempDir(),
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	s.SetSubscriberCountFunc(func() int { return 1 })
	return s
}

// approvalDecisions lists the approval NOTICE entries the transcript holds.
func approvalDecisions(t *testing.T, s *Session) []schema.ApprovalDecisionNotice {
	t.Helper()
	var decisions []schema.ApprovalDecisionNotice
	for _, entry := range transcriptEntries(t, s) {
		if notice := entry.Turn.Notice; entry.Turn.Kind == schema.TurnNotice && notice != nil && notice.Kind == schema.NoticeApprovalDecision {
			if notice.ApprovalDecision == nil {
				t.Fatalf("approval NOTICE without its payload: %+v", notice)
			}
			decisions = append(decisions, *notice.ApprovalDecision)
		}
	}
	return decisions
}

// A human's Allow or Deny is history (S16): the transcript records the
// decision, naming the tool, the card's kind and the path, so approval
// history reads the same after a reload as it did live.
func TestEscalation_RecordsTheHumansDecision(t *testing.T) {
	for _, approve := range []bool{true, false} {
		s := recordingEscalationSession(t)
		res, denied := deniedResult("/etc/hosts")
		done := make(chan tool.ExecResult, 1)
		go func() {
			done <- s.escalateOnSandboxDenial(context.Background(), "write_file", res, func(context.Context) tool.ExecResult { return succeededResult() })
		}()
		ids := awaitPending(t, s, 1)
		if err := s.ResolveSandboxEscalation(ids[0], approve); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		<-done
		want := schema.ApprovalDecisionNotice{EscalationID: ids[0], Approved: approve, Tool: denied.Tool, Kind: "file_tool", DeniedPath: denied.Path}
		if got := approvalDecisions(t, s); len(got) != 1 || got[0] != want {
			t.Fatalf("approve=%v recorded %+v, want exactly %+v", approve, got, want)
		}
	}
}

// Stopping the turn or closing the session ends the wait without a human
// decision, so nothing is recorded as allowed or denied.
func TestEscalation_RecordsNoDecisionWithoutAHuman(t *testing.T) {
	t.Run("turn interrupted", func(t *testing.T) {
		s := recordingEscalationSession(t)
		res, _ := deniedResult("/etc/hosts")
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan tool.ExecResult, 1)
		go func() { done <- s.escalateOnSandboxDenial(ctx, "write_file", res, noRerun(t)) }()
		awaitPending(t, s, 1)
		cancel()
		<-done
		if got := approvalDecisions(t, s); len(got) != 0 {
			t.Fatalf("an interrupted escalation recorded %+v", got)
		}
	})
	t.Run("session closed", func(t *testing.T) {
		s := recordingEscalationSession(t)
		res, _ := deniedResult("/etc/hosts")
		done := make(chan tool.ExecResult, 1)
		go func() { done <- s.escalateOnSandboxDenial(context.Background(), "write_file", res, noRerun(t)) }()
		awaitPending(t, s, 1)
		s.Close()
		select {
		case got := <-done:
			if !got.IsError {
				t.Fatal("Close must still resolve a blocked escalation to the typed denial")
			}
		case <-time.After(30 * time.Second): // TRIPWIRE: in-process; fires only if Close fails to unblock the escalation.
			t.Fatal("Close did not unblock the escalation")
		}
		if got := approvalDecisions(t, s); len(got) != 0 {
			t.Fatalf("a closed session recorded %+v as a decision", got)
		}
	})
}
