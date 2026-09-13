package agent

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func TestRestoreFailureEnvironmentIsSeededWithoutLiveReplay(t *testing.T) {
	dir := t.TempDir()
	sess := newQueuePersistTestSession(t, dir)
	id := sess.ID()
	start, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "restore-environment",
		Input:            []appwire.InputItem{{Type: "text", Text: "restore-input"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := sess.claimClientMutationStart(); err != nil || !claimed {
		t.Fatalf("claim start: claimed=%v err=%v", claimed, err)
	}
	// Persist the failure intent, then stop before recovery has appended any
	// model context or input. Resume owns the remaining durable transaction.
	if err := sess.beginClientMutationFailure("restore-environment", errors.New("fixture failure")); err != nil {
		t.Fatal(err)
	}
	sess.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	_, entries, ok := restored.RestoredTranscript()
	if !ok {
		t.Fatal("restore did not retain its transcript for projection")
	}
	environments, inputs := 0, 0
	for _, entry := range entries {
		switch entry.Turn.Kind {
		case schema.TurnEnvironment:
			environments++
			if entry.Turn.StableTurnID == "" || entry.Turn.StableTurnID == start.Turn.ID {
				t.Fatalf("environment identity = %q, runnable identity = %q", entry.Turn.StableTurnID, start.Turn.ID)
			}
		case schema.TurnUserInput:
			if entry.Turn.StableTurnID == start.Turn.ID {
				inputs++
			}
		}
	}
	if environments != 1 || inputs != 1 {
		t.Fatalf("seeded recovery entries: environment=%d input=%d", environments, inputs)
	}
	var observed []events.EventKind
	drained := make(chan struct{})
	restored.ConsumeEventsLossless(func(event events.SessionEvent) {
		observed = append(observed, event.Kind)
	}, func() { close(drained) })
	restored.Close()
	<-drained
	if len(observed) == 0 || observed[0] != events.EventSessionStart {
		t.Fatalf("first restored event must introduce the session; observed %v", observed)
	}
	for _, kind := range observed {
		if kind == events.EventEnvironment {
			t.Fatal("restore replayed environment already present in the seed transcript")
		}
	}
}
