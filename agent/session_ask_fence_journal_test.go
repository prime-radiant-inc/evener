package agent

import (
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func requireAskFenceError(t *testing.T, err error, info appwire.ErrorInfo) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error=%v, want typed %s", err, info)
	}
	data, ok := appwire.DecodedErrorData(wire.Data)
	if !ok || data.EvenerErrorInfo != info {
		t.Fatalf("error data=%+v, want %s", data, info)
	}
}

func TestAskFenceEmptyAndMalformedCaptureRefused(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(t.TempDir()))
	s.SetBootGeneration("current")
	pair := s.PendingAskCalls()
	_, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "empty-fence", Input: clientMutationInput("input", nil, nil), ExpectedAskRevision: &pair.Revision, ExpectedAskBootGeneration: pair.BootGeneration})
	requireAskFenceError(t, err, appwire.ErrorAskFenceMismatch)
	_, err = s.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "empty-steer-fence", Input: clientMutationInput("input", nil, nil), ExpectedAskRevision: &pair.Revision, ExpectedAskBootGeneration: pair.BootGeneration})
	requireAskFenceError(t, err, appwire.ErrorAskFenceMismatch)
	// Inject the explicitly unreachable malformed representation directly:
	// a nonempty question set whose exhaustive capture names nothing.
	_, err = captureAskFence(&appwire.PendingQuestion{Count: 1}, pair, &pair.Revision, pair.BootGeneration)
	requireAskFenceError(t, err, appwire.ErrorAskFenceMismatch)
	armLiveAskExecution(s)
	appendAsk(s, "legacy-test")
	s.mu.Lock()
	s.askPending = append(s.askPending, askQuestion{Question: "legacy"}, askQuestion{Question: "partial-id", TurnID: "only-one-id"})
	s.mu.Unlock()
	question, state := s.PendingAskSnapshot()
	capture, err := captureAskFence(question, state, &state.Revision, state.BootGeneration)
	if err != nil || len(capture.Resolution.Calls) != 1 || capture.Resolution.LegacyCount != 2 {
		t.Fatalf("nonexhaustive capture=%+v err=%v", capture, err)
	}
}

func TestAskFencePairShapeValidatedAtAllAdmissions(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"start", "queue", "steer"} {
		for _, shape := range []string{"revision-only", "generation-only", "blank-generation"} {
			t.Run(mode+"/"+shape, func(t *testing.T) {
				s := newTestSession(t)
				revision := new(uint64(0))
				generation := "current"
				switch shape {
				case "revision-only":
					generation = ""
				case "generation-only":
					revision = nil
				case "blank-generation":
					generation = " "
				}
				var err error
				switch mode {
				case "start":
					_, err = s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "invalid-pair", Input: clientMutationInput("input", nil, nil), ExpectedAskRevision: revision, ExpectedAskBootGeneration: generation})
				case "queue":
					_, err = s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "invalid-pair", Input: clientMutationInput("input", nil, nil), ExpectedAskRevision: revision, ExpectedAskBootGeneration: generation})
				default:
					_, err = s.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "invalid-pair", Input: clientMutationInput("input", nil, nil), ExpectedAskRevision: revision, ExpectedAskBootGeneration: generation})
				}
				requireAskFenceError(t, err, appwire.ErrorInvalidParams)
			})
		}
	}
}

func TestAskFenceSteerCaptureSurvivesJournalReopenAndCopy(t *testing.T) {
	t.Parallel()
	s := newDurableAdmissionAskSession(t, SessionConfig{})
	seedDurableAdmissionAsk(t, s)
	s.SetBootGeneration("current")
	pair := s.PendingAskCalls()
	params := appwire.TurnSteerParams{ClientMutationID: "fenced-steer", Input: clientMutationInput("answer", nil, nil), ExpectedAskRevision: &pair.Revision, ExpectedAskBootGeneration: pair.BootGeneration}
	first, err := s.AcceptClientMutationSteer(params)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := newClientMutationStore(s.stateDir, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	record := reopened.snapshot().Journal[params.ClientMutationID]
	p := record.Preconditions
	if p.ExpectedAskRevision == nil || *p.ExpectedAskRevision != pair.Revision || p.ExpectedAskBootGeneration != pair.BootGeneration || len(p.AskCaptures) != 1 || !reflect.DeepEqual(p.AskCaptures[0].Resolution.Calls, pair.Calls) {
		t.Fatalf("reopened capture=%+v", p)
	}
	p.AskCaptures[0].Resolution.Calls[0].CallID = "mutated-snapshot"
	if got := reopened.snapshot().Journal[params.ClientMutationID].Preconditions.AskCaptures[0].Resolution.Calls; !reflect.DeepEqual(got, pair.Calls) {
		t.Fatalf("capture aliases snapshot=%+v", got)
	}
	s.clearAskPending()
	replay, err := s.AcceptClientMutationSteer(params)
	if err != nil || replay.Receipt.Disposition != appwire.MutationDispositionReplayed || replay.Receipt.TurnID != first.Receipt.TurnID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

func TestAskFenceRestartRevisionCollisionRefused(t *testing.T) {
	t.Parallel()
	s := newDurableAdmissionAskSession(t, SessionConfig{})
	seedDurableAdmissionAsk(t, s)
	generation, err := schema.NextBootGeneration(s.stateDir, s.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	s.SetBootGeneration(appwire.BootGeneration(generation))
	before := s.PendingAskCalls()
	meta, dir := s.Meta(), s.stateDir
	s.Close()
	restored := restoreAskSession(t, meta, dir)
	generation, err = schema.NextBootGeneration(dir, restored.ID(), 0)
	if err != nil {
		t.Fatal(err)
	}
	restored.SetBootGeneration(appwire.BootGeneration(generation))
	after := restored.PendingAskCalls()
	if before.Revision != after.Revision || before.BootGeneration == after.BootGeneration || !reflect.DeepEqual(before.Calls, after.Calls) {
		t.Fatalf("collision scenario missing: before=%+v after=%+v", before, after)
	}
	_, err = restored.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "pre-restart-answer", Input: clientMutationInput("answer", nil, nil), ExpectedAskRevision: &before.Revision, ExpectedAskBootGeneration: before.BootGeneration})
	requireAskFenceError(t, err, appwire.ErrorAskFenceMismatch)
	assertFenceSurvivor(t, restored, "ask1")
}
