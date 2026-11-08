package agent

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
)

func assertAskEvidence(t *testing.T, s *Session, id string, calls []appwire.PendingAskCall, legacy int) schema.Turn {
	t.Helper()
	for _, turn := range s.history {
		if turn.ClientMutationID == id && turn.AskResolution != nil {
			if !reflect.DeepEqual(turn.AskResolution.Calls, calls) || turn.AskResolution.LegacyCount != legacy {
				t.Fatalf("resolving turn=%+v want calls=%+v legacy=%d", turn, calls, legacy)
			}
			return turn
		}
	}
	t.Fatalf("no resolving evidence for %s", id)
	return schema.Turn{}
}

func TestAskFenceRefusedAnswerStaysPendingAcrossRestore(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"queue", "steer"} {
		t.Run(mode, func(t *testing.T) {
			s := newDurableAdmissionAskSession(t, SessionConfig{})
			seedDurableAdmissionAsk(t, s)
			s.SetBootGeneration("current")
			pair := s.PendingAskCalls()
			appendFenceAsk(t, s, "second", pair.Calls[0].TurnID)
			queue := appwire.TurnQueueParams{ClientMutationID: "refused", Input: clientMutationInput("answer", nil, nil), ExpectedAskRevision: &pair.Revision, ExpectedAskBootGeneration: pair.BootGeneration}
			steer := appwire.TurnSteerParams{ClientMutationID: queue.ClientMutationID, Input: queue.Input, ExpectedAskRevision: queue.ExpectedAskRevision, ExpectedAskBootGeneration: queue.ExpectedAskBootGeneration}
			reject := func(sess *Session) {
				var err error
				if mode == "queue" {
					_, err = sess.AcceptClientMutationQueue(queue)
				} else {
					_, err = sess.AcceptClientMutationSteer(steer)
				}
				requireAskFenceError(t, err, appwire.ErrorAskFenceMismatch)
			}
			reject(s)
			meta, dir := s.Meta(), s.stateDir
			s.Close()
			restored := restoreAskSession(t, meta, dir)
			if got := restored.PendingAskCalls(); len(got.Calls) != 2 || got.CalllessCount != 0 {
				t.Fatalf("refusal resolved asks=%+v", got)
			}
			reject(restored)
			for _, turn := range restored.history {
				if turn.ClientMutationID == "refused" {
					t.Fatalf("refused answer became durable: %+v", turn)
				}
			}
			if len(restored.clientMutations.snapshot().PendingExecutions) != 0 {
				t.Fatal("refusal replay created pending execution")
			}
		})
	}
}

func TestAskFenceAllResolvingFailureWritersKeepCapturedEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"recovered_before_append", "recovered_after_user_crash", "steer_append_failure", "steer_selection_failure"} {
		t.Run(mode, func(t *testing.T) {
			s := newDurableAdmissionAskSession(t, SessionConfig{})
			ctx := seedDurableAdmissionAsk(t, s)
			s.SetBootGeneration("current")
			pair := s.PendingAskCalls()
			id := "failing-answer"
			if mode == "recovered_before_append" || mode == "recovered_after_user_crash" {
				_, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: id, Input: clientMutationInput("answer", nil, nil), ExpectedAskRevision: &pair.Revision, ExpectedAskBootGeneration: pair.BootGeneration})
				if err != nil {
					t.Fatal(err)
				}
				appendFenceAsk(t, s, "later", pair.Calls[0].TurnID)
				failure := errors.New("injected pre-append failure")
				s.clientMutationPreAppendFailure = func(schema.Turn) error { return failure }
				crash := errors.New("crash after recovered user")
				if mode == "recovered_after_user_crash" {
					s.clientMutationFailureRecoveryFault = func(boundary string) error {
						if boundary == "after_user" {
							return crash
						}
						return nil
					}
				}
				_, ran, err := s.ProcessClientMutationStart(ctx, nil)
				if !ran || !errors.Is(err, failure) || (mode == "recovered_after_user_crash" && !errors.Is(err, crash)) {
					t.Fatalf("ran=%v error=%v", ran, err)
				}
			} else {
				var skills []string
				if mode == "steer_selection_failure" {
					skills = []string{"no-such-skill"}
				}
				_, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: id, Input: clientMutationInput("answer", nil, skills), ExpectedAskRevision: &pair.Revision, ExpectedAskBootGeneration: pair.BootGeneration})
				if err != nil {
					t.Fatal(err)
				}
				appendFenceAsk(t, s, "later", pair.Calls[0].TurnID)
				if mode == "steer_append_failure" {
					refusal := refuseSteerAppends(s, id)
					refusal.refuse.Store(true)
					_, ran, err := s.ProcessPendingUserInput(ctx, nil)
					if err == nil || !ran || refusal.refusals.Load() != 1 {
						t.Fatalf("ran=%v error=%v refusals=%d", ran, err, refusal.refusals.Load())
					}
				} else {
					_, ran, err := s.ProcessPendingUserInput(ctx, nil)
					if err != nil || ran {
						t.Fatalf("ran=%v error=%v", ran, err)
					}
				}
			}
			evidenceID := id
			if mode == "steer_append_failure" {
				// The generic carrier diagnostic has no ClientMutationID.
				evidenceID = ""
			}
			turn := assertAskEvidence(t, s, evidenceID, pair.Calls, 0)
			if mode == "steer_append_failure" || mode == "steer_selection_failure" {
				if turn.Kind != schema.TurnFailure || turn.Error == nil || !turn.Error.SteeringCarrier {
					t.Fatalf("wrong failure writer %+v", turn)
				}
			} else if turn.Kind != schema.TurnUserInput {
				t.Fatalf("wrong recovery writer %+v", turn)
			}
			assertFenceSurvivor(t, s, "later")
			meta, dir := s.Meta(), s.stateDir
			s.Close()
			assertFenceSurvivor(t, restoreAskSession(t, meta, dir), "later")
		})
	}
}

func TestAskFenceIncorporatedReplayDoesNotClearLaterAsk(t *testing.T) {
	t.Parallel()
	s := newDurableAdmissionAskSession(t, SessionConfig{})
	seedDurableAdmissionAsk(t, s)
	s.SetBootGeneration("current")
	pair := s.PendingAskCalls()
	_, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "replay-answer", Input: clientMutationInput("answer", nil, nil), ExpectedAskRevision: &pair.Revision, ExpectedAskBootGeneration: pair.BootGeneration})
	if err != nil {
		t.Fatal(err)
	}
	claimed := s.popQueueHead()
	ctx := withQueuedClientMutation(context.Background(), claimed)
	if err := s.acceptUserInput(ctx, claimed.Text, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	appendFenceAsk(t, s, "later", pair.Calls[0].TurnID)
	if err := s.acceptUserInput(ctx, claimed.Text, nil, nil, false); err != nil {
		t.Fatal(err)
	}
	assertFenceSurvivor(t, s, "later")
	assertAskEvidence(t, s, "replay-answer", pair.Calls, 0)
	meta, dir := s.Meta(), s.stateDir
	s.Close()
	assertFenceSurvivor(t, restoreAskSession(t, meta, dir), "later")
}

func TestAskFenceInlineEvidenceRetainsCallsButExcludesActiveExecutionLink(t *testing.T) {
	t.Parallel()
	s := newDurableAdmissionAskSession(t, SessionConfig{})
	s.beginExecution("inline-self")
	s.mu.Lock()
	execution := s.execution.turnID
	s.mu.Unlock()
	appendFenceAsk(t, s, "same-execution-ask", execution)
	s.SetBootGeneration("current")
	pair := s.PendingAskCalls()
	_, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "inline-answer", Input: clientMutationInput("answer", nil, nil), ExpectedAskRevision: &pair.Revision, ExpectedAskBootGeneration: pair.BootGeneration})
	if err != nil {
		t.Fatal(err)
	}
	appendFenceAsk(t, s, "same-execution-later", execution)
	if !s.injectDrainedSteering() {
		t.Fatal("no inline delivery")
	}
	turn := assertAskEvidence(t, s, "inline-answer", pair.Calls, 0)
	if len(turn.AnswerToTurnIDs) != 0 {
		t.Fatalf("active execution self-link=%v", turn.AnswerToTurnIDs)
	}
	items, _ := apptranscript.ProjectEntryParts(execution, 0, turn, nil, nil, nil)
	if len(items) != 1 || items[0].Observation != nil {
		t.Fatalf("empty answer links manufactured an observation edge: %+v", items)
	}
	s.completeExecution(schema.TurnCompleted)
	assertFenceSurvivor(t, s, "same-execution-later")
	meta, dir := s.Meta(), s.stateDir
	s.Close()
	assertFenceSurvivor(t, restoreAskSession(t, meta, dir), "same-execution-later")
}
