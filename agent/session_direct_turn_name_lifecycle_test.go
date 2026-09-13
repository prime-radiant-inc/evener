package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// scriptedTurnSession is a session whose model answers with responder, with
// its events drained so nothing parks on the channel.
func scriptedTurnSession(t *testing.T, responder func(llm.Request) llm.Response, opts ...sessionOpt) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: responder})
	s := newSession(t, append([]sessionOpt{
		withClient(client),
		withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: t.TempDir()}),
		withoutGitSnapshot(),
	}, opts...)...)
	return s
}

// A turn that has to name itself owns the records it publishes, and only
// those. The name outliving the turn is not a label problem: activeTurnOwner
// prefers it, so the next turn's timing, hook and compaction records — and
// anything a fold writes while the session is idle — are grouped under a turn
// that is over, in both projections.
func TestSelfMintedTurnNameDoesNotOutliveItsTurn(t *testing.T) {
	t.Parallel()
	t.Run("a turn driven directly", func(t *testing.T) {
		t.Parallel()
		s := scriptedTurnSession(t, func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("done")}
		})
		go func() {
			for range s.Events() {
			}
		}()
		// The error is the scripted model's, not the point: the turn ran and
		// ended either way.
		_, _, _ = s.processOneInput(context.Background(), "direct", nil, EntryUserInput, nil)
		if owner := s.activeTurnOwner(); owner != "" {
			t.Fatalf("after the turn ended, %q still owns everything published next", owner)
		}
	})

	t.Run("a turn whose input could not be written", func(t *testing.T) {
		t.Parallel()
		fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
		s := scriptedTurnSession(t, func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("done")}
		})
		go func() {
			for range s.Events() {
			}
		}()
		if err := s.closeAttachedTranscript(); err != nil {
			t.Fatalf("close default transcript: %v", err)
		}
		writer, err := transcript.NewWriterWithFS(fs, "/session.jsonl", transcript.Header{SessionID: s.id})
		if err != nil {
			t.Fatalf("NewWriterWithFS: %v", err)
		}
		s.attachTranscript(writer)
		fs.fail = true

		// The turn names itself and then fails to write its own input, which
		// is a return between the mint and the end of the turn.
		if _, _, err := s.processOneInput(context.Background(), "unwritable", nil, EntryUserInput, nil); err == nil {
			t.Fatal("test setup: the user input was written even though every write fails")
		}
		if owner := s.activeTurnOwner(); owner != "" {
			t.Fatalf("a turn that failed after naming itself left %q owning what comes next", owner)
		}
	})
}

// The interrupt marker is the interrupted turn's last record. It says what
// happened to that turn, so it belongs to it: unowned, the transcript groups it
// on its own and the live projection puts it wherever the session happens to be.
func TestInterruptMarkerBelongsToTheInterruptedTurn(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	var once sync.Once
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := scriptedTurnSession(t, func(llm.Request) llm.Response {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return llm.Response{Message: llm.Assistant("never")}
	})
	var mu sync.Mutex
	var steering []events.SteeringInjectedData
	var userInputs []events.UserInputData
	go func() {
		for event := range s.Events() {
			mu.Lock()
			switch data := event.Data.(type) {
			case events.SteeringInjectedData:
				steering = append(steering, data)
			case events.UserInputData:
				userInputs = append(userInputs, data)
			}
			mu.Unlock()
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = s.ProcessInput(ctx, "interrupt me", nil)
	}()
	select {
	case <-entered:
	// TRIPWIRE: the scripted model is in-process; this only fires if the turn
	// never reaches the model at all.
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never reached the model")
	}
	cancel()
	select {
	case <-done:
	// TRIPWIRE: the cancelled turn unwinds in-process.
	case <-time.After(10 * time.Second):
		t.Fatal("the interrupted turn never returned")
	}

	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id))
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
	mu.Lock()
	live := append([]events.SteeringInjectedData(nil), steering...)
	mu.Unlock()
	seen := 0
	for _, injected := range live {
		if injected.Kind != events.SteeringKindInterrupted {
			continue
		}
		seen++
		if injected.OwningTurnID != owner {
			t.Fatalf("the live interrupt marker is owned by %q, want %q", injected.OwningTurnID, owner)
		}
	}
	if seen != 1 {
		t.Fatalf("live interrupt markers = %d, want 1 (%d user inputs seen)", seen, len(userInputs))
	}
	if strings.TrimSpace(owner) == "" {
		t.Fatal("unreachable")
	}
}
