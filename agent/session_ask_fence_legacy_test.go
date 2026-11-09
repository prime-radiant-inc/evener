package agent

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// These fixtures represent pre-canonical transcript asks, whose restored
// questions have no complete composite. Production live appends are named.
func TestAskFenceLegacyAndMixedCaptureResolvesOnlyAdmissionPopulation(t *testing.T) {
	t.Parallel()
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "mixed"}[mixed], func(t *testing.T) {
			s := newDurableAdmissionAskSession(t, SessionConfig{})
			round := fenceAskRound(t, "restored-ask", false, "legacy1", "legacy2")
			if mixed {
				named := fenceAskRound(t, "restored-ask", true, "named")
				for i := range round {
					round[i].Message.Content = append(round[i].Message.Content, named[i].Message.Content...)
				}
			}
			turns := append([]schema.Turn{{Kind: schema.TurnUserInput, TurnID: "restored-ask", Message: llm.User("seed")}}, round...)
			for _, turn := range turns {
				if err := s.appendTurnAfterTranscriptWrite(turn, func() error {
					_, err := s.recordTranscriptLocked(turn, transcript.DoorDurable, transcript.PlaceInTurn("restored-ask"))
					return err
				}, func() { s.history = append(s.history, turn) }); err != nil {
					t.Fatal(err)
				}
			}
			pending, found := deriveRestoredAskPending(s.history, 0, nil)
			if !found {
				t.Fatal("legacy fixture not restored")
			}
			s.mu.Lock()
			s.setAskPendingLocked(pending)
			s.mu.Unlock()
			s.SetBootGeneration("current")
			_, pair := s.PendingAskSnapshot()
			named := 0
			if mixed {
				named = 1
			}
			if pair.CalllessCount != 2 || len(pair.Calls) != named {
				t.Fatalf("wrong restored population=%+v", pair)
			}
			if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "legacy-answer", Input: clientMutationInput("answer", nil, nil), ExpectedAskRevision: &pair.Revision, ExpectedAskBootGeneration: pair.BootGeneration}); err != nil {
				t.Fatal(err)
			}
			appendFenceAsk(t, s, "later", "restored-ask")
			claimed := s.popQueueHead()
			if err := s.acceptUserInput(withQueuedClientMutation(context.Background(), claimed), claimed.Text, nil, nil, false); err != nil {
				t.Fatal(err)
			}
			assertAskEvidence(t, s, "legacy-answer", pair.Calls, 2)
			assertFenceSurvivor(t, s, "later")
			meta, dir := s.Meta(), s.stateDir
			s.Close()
			assertFenceSurvivor(t, restoreAskSession(t, meta, dir), "later")
		})
	}
}
