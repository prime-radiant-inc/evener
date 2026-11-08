package agent

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func TestAskFenceDrainAndPromoteCarryCapturesAndUnfencedDominates(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"drain_fenced", "drain_mixed", "drain_extra_unfenced", "promote_fenced", "promote_unfenced"} {
		t.Run(mode, func(t *testing.T) {
			s := newDurableAdmissionAskSession(t, SessionConfig{})
			seedDurableAdmissionAsk(t, s)
			s.SetBootGeneration("current")
			initial := s.PendingAskCalls()
			enqueue := func(id string, fenced bool) {
				p := appwire.TurnQueueParams{ClientMutationID: id, Input: clientMutationInput(id, nil, nil)}
				if fenced {
					pair := s.PendingAskCalls()
					p.ExpectedAskRevision = &pair.Revision
					p.ExpectedAskBootGeneration = pair.BootGeneration
				}
				if _, err := s.AcceptClientMutationQueue(p); err != nil {
					t.Fatal(err)
				}
			}
			enqueue("source1", mode != "promote_unfenced")
			appendFenceAsk(t, s, "ask2", initial.Calls[0].TurnID)
			want := initial.Calls
			if mode == "drain_fenced" || mode == "drain_mixed" {
				enqueue("source2", mode == "drain_fenced")
				if mode == "drain_fenced" {
					want = s.PendingAskCalls().Calls
				}
			}
			appendFenceAsk(t, s, "ask3", initial.Calls[0].TurnID)
			var err error
			if mode == "promote_fenced" || mode == "promote_unfenced" {
				_, err = s.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "replacement", Index: 0})
			} else {
				snapshot := s.clientMutations.snapshot()
				p := appwire.TurnDrainAsSteerParams{ClientMutationID: "replacement", ExpectedQueueRevision: snapshot.QueueRevision}
				if mode == "drain_extra_unfenced" {
					p.Input = clientMutationInput("new unfenced input", nil, nil)
				}
				_, err = s.AcceptClientMutationDrainAsSteer(p)
			}
			if err != nil {
				t.Fatal(err)
			}
			reopened, err := newClientMutationStore(s.stateDir, s.ID())
			if err != nil {
				t.Fatal(err)
			}
			record := reopened.snapshot().Journal["replacement"]
			fenced := mode == "drain_fenced" || mode == "promote_fenced"
			if fenced {
				captures := 1
				if mode == "drain_fenced" {
					captures = 2
				}
				if len(record.Preconditions.AskCaptures) != captures || !reflect.DeepEqual(record.Preconditions.askResolution().Calls, want) {
					t.Fatalf("replacement lost captures %+v", record.Preconditions)
				}
			} else if len(record.Preconditions.AskCaptures) != 0 {
				t.Fatalf("unfenced source lost dominance %+v", record.Preconditions)
			}
			if !s.injectDrainedSteering() {
				t.Fatal("replacement not delivered")
			}
			if fenced {
				assertAskEvidence(t, s, "replacement", want, 0)
			} else {
				for _, turn := range s.history {
					if turn.ClientMutationID == "replacement" && turn.AskResolution != nil {
						t.Fatalf("unfenced replacement unexpectedly scoped %+v", turn)
					}
				}
			}
			assertPending := func(sess *Session) {
				got := sess.PendingAskCalls()
				switch mode {
				case "drain_fenced":
					assertFenceSurvivor(t, sess, "ask3")
				case "promote_fenced":
					if len(got.Calls) != 2 || got.Calls[0].CallID != "ask2" || got.Calls[1].CallID != "ask3" {
						t.Fatalf("promote survivors %+v", got)
					}
				default:
					if sess.askPendingCount() != 0 {
						t.Fatalf("unfenced input not whole-set %+v", got)
					}
				}
			}
			assertPending(s)
			meta, dir := s.Meta(), s.stateDir
			s.Close()
			assertPending(restoreAskSession(t, meta, dir))
		})
	}
}

func TestAskFenceCaptureUnionUsesCompositeAndLegacyMaximum(t *testing.T) {
	t.Parallel()
	q1 := appwire.PendingAskCall{TurnID: "same-turn", CallID: "one"}
	q2 := appwire.PendingAskCall{TurnID: "same-turn", CallID: "two"}
	snapshot := clientMutationSnapshot{Journal: map[string]clientMutationRecord{
		"a": {Preconditions: clientMutationPreconditions{AskCaptures: []clientMutationAskCapture{{BootGeneration: "1", Revision: 4, Resolution: schema.AskResolution{Calls: []appwire.PendingAskCall{q1}, LegacyCount: 3}}}}},
		"b": {Preconditions: clientMutationPreconditions{AskCaptures: []clientMutationAskCapture{{BootGeneration: "2", Revision: 1, Resolution: schema.AskResolution{Calls: []appwire.PendingAskCall{q1, q2}, LegacyCount: 2}}}}},
	}}
	var record clientMutationRecord
	carryAskCaptures(&snapshot, &record, []clientMutationQueueEntry{{ClientMutationID: "a"}, {ClientMutationID: "b"}}, false)
	resolution := record.Preconditions.askResolution()
	if resolution == nil || resolution.LegacyCount != 3 || !reflect.DeepEqual(resolution.Calls, []appwire.PendingAskCall{q1, q2}) {
		t.Fatalf("union=%+v", resolution)
	}
	record.Preconditions.AskCaptures[0].Resolution.Calls[0].CallID = "copy-mutated"
	if snapshot.Journal["a"].Preconditions.AskCaptures[0].Resolution.Calls[0] != q1 {
		t.Fatal("transformation aliases source capture")
	}
}
