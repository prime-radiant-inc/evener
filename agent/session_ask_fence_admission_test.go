package agent

import (
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestAskFenceAdmissionRefusesStalePair(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"start", "queue", "steer"} {
		t.Run(mode, func(t *testing.T) {
			s := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(t.TempDir()))
			armLiveAskExecution(s)
			appendAsk(s, "Q1")
			appendAsk(s, "Q2")
			payload := []byte(`{"clientMutationId":"stale-answer","input":[{"type":"text","text":"answer-sentinel"}],"expectedAskRevision":1,"expectedAskBootGeneration":"previous-boot"}`)
			accept := func() error {
				switch mode {
				case "start":
					var params appwire.TurnStartParams
					if err := json.Unmarshal(payload, &params); err != nil {
						t.Fatal(err)
					}
					_, err := s.AcceptClientMutationStart(params)
					return err
				case "queue":
					var params appwire.TurnQueueParams
					if err := json.Unmarshal(payload, &params); err != nil {
						t.Fatal(err)
					}
					_, err := s.AcceptClientMutationQueue(params)
					return err
				default:
					var params appwire.TurnSteerParams
					if err := json.Unmarshal(payload, &params); err != nil {
						t.Fatal(err)
					}
					_, err := s.AcceptClientMutationSteer(params)
					return err
				}
			}
			for attempt := range 2 {
				err := accept()
				var wire appwire.WireError
				if !errors.As(err, &wire) {
					t.Fatalf("attempt %d admitted a stale answer, error=%v", attempt, err)
				}
				data, ok := appwire.DecodedErrorData(wire.Data)
				if !ok || data.EvenerErrorInfo != "ask-fence-mismatch" || data.ClientMutationID != "stale-answer" || data.MutationOutcome != appwire.MutationOutcomeNotAccepted {
					t.Fatalf("attempt %d refusal = %#v, error=%v", attempt, wire, err)
				}
			}
			if got := s.askPendingCount(); got != 2 {
				t.Fatalf("pending questions = %d, want 2", got)
			}
			snapshot := s.clientMutations.snapshot()
			if snapshot.Journal["stale-answer"].OperationState != clientMutationOperationRejected || len(snapshot.InputQueue) != 0 || len(snapshot.PendingExecutions) != 0 {
				t.Fatalf("refused mutation has runnable work: %+v", snapshot)
			}
			if got := s.clientMutationTranscriptItems("stale-answer", ""); got.User || got.Steering {
				t.Fatalf("refused answer reached transcript: %+v", got)
			}
		})
	}
}
