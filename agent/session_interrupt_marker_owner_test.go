package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// A mutation-named turn's interrupt marker is that turn's last record, so it
// must name the turn it cut short. The turn releases its durable name while it
// unwinds, before the outer loop appends the marker; without carrying the name
// across that release, activeTurnOwner answers for a turn that is already over
// and the marker is written with no owner.
func TestInterruptMarkerOfMutationNamedTurnCarriesTheTurn(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	var once sync.Once
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	adapter := &agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(req llm.Request) llm.Response {
			once.Do(func() { close(entered) })
			<-ctx.Done()
			return llm.Response{Message: llm.Assistant("never")}
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess := newSession(t, withClient(client), withConfig(SessionConfig{
		StateDir: t.TempDir(),
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	}))
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()
	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	queueOneMutation(t, sess, "cm-interrupt-marker", "interrupt me")

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = sess.ProcessPendingUserInput(ctx, nil)
	}()
	select {
	case <-entered:
	// TRIPWIRE: the model is in-process and the turn has already started; this
	// fires only if the turn never reaches the model at all, not under load.
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never reached the model")
	}
	cancel()
	select {
	case <-done:
	// TRIPWIRE: the cancelled turn unwinds in-process; this fires only on a
	// genuine hang.
	case <-time.After(10 * time.Second):
		t.Fatal("the interrupted turn never returned")
	}

	data, err := readTranscriptFull(transcriptPath(sess.stateDir, sess.id), "")
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	owner := ""
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnUserInput {
			owner = entry.Turn.StableTurnID
		}
	}
	if owner == "" {
		t.Fatal("test setup: the turn wrote no named user input to belong to")
	}
	markers := 0
	for _, entry := range data.Entries {
		if entry.Turn.Kind != schema.TurnSteering || entry.Turn.SteeringKind != events.SteeringKindInterrupted {
			continue
		}
		markers++
		if entry.Turn.OwningTurnID != owner {
			t.Fatalf("the interrupt marker entry is owned by %q, want the turn it interrupted %q", entry.Turn.OwningTurnID, owner)
		}
	}
	if markers != 1 {
		t.Fatalf("interrupt markers in the transcript = %d, want 1", markers)
	}
}
