package agent

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

func projectTranscriptFile(t *testing.T, path string) []appwire.Turn {
	t.Helper()
	toolNames := map[string]string{}
	turns, err := apptranscript.ItemTurnsFromFile(path, transcriptJSONLMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		return apptranscript.ProjectTurn(turnID, entryIndex, turn, toolNames, nil, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	return turns
}

// A transcript written before persisted identity, resumed by this build: its
// lines are never rewritten, today's projection of them is unchanged, and
// every entry the resumed session appends carries identity.
func TestALegacyTranscriptResumesUnchanged(t *testing.T) {
	s, _ := newExecutionSession(t)
	stateDir, id, path := s.stateDir, s.ID(), s.TranscriptPath()
	header := s.attachedTranscript().Header()
	s.Close()
	// Rewrite the transcript as a pre-identity build left it.
	w, err := transcript.NewWriterNoSync(path, header)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("a legacy question")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("a legacy answer")),
		schema.NewTurn(schema.TurnHookCompleted, llm.User("a legacy hook")),
	} {
		if _, err := w.Record(turn, transcript.RecordOptions{Place: transcript.PlaceVerbatim}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	legacyTurns := projectTranscriptFile(t, path)

	restored := restoreExecutionSession(t, stateDir, id)
	if _, err := restored.ProcessInput(context.Background(), "a new question", nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(after, legacy) {
		t.Fatal("resuming rewrote the legacy lines")
	}
	if got := projectTranscriptFile(t, path)[:len(legacyTurns)]; !reflect.DeepEqual(got, legacyTurns) {
		t.Fatal("the projection of the legacy turns changed")
	}
	_, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries[3:] {
		if entry.Turn.Format != schema.TurnFormatIdentity || entry.Turn.TurnID == "" {
			t.Fatalf("a resumed entry lacks identity: %s %+v", entry.Turn.Kind, entry.Turn)
		}
	}
}

// A client turn the process died running is reclaimed on restore: it is left
// open, runs again under its reserved id, reopens, and ends with one
// completion — never an interrupted one.
func TestAReclaimedTurnReopensAndCompletesOnce(t *testing.T) {
	s, _ := newExecutionSession(t)
	s.SetClientMutationStartWakeFunc(func() {})
	started, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "reclaimed-start", ExpectedInstanceID: s.ID(),
		Input: []appwire.InputItem{{Type: "text", Text: "interrupted by a crash"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := s.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	// The turn starts and records its input; then the process dies.
	s.beginExecution(claimed.StableTurnID)
	if err := s.acceptUserInput(withQueuedClientMutation(context.Background(), claimed), claimed.Text, claimed.Images, nil, true); err != nil {
		t.Fatal(err)
	}
	stateDir, id := s.stateDir, s.ID()
	s.Close()

	restored := restoreExecutionSession(t, stateDir, id)
	turnID := started.Turn.ID
	if got := completionsOf(transcriptTurnsOf(t, restored), turnID); len(got) != 0 {
		t.Fatalf("restore closed the turn it reclaims: %v", got)
	}
	restored.SetClientMutationStartWakeFunc(func() {})
	if _, _, err := restored.ProcessClientMutationStart(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	turns := transcriptTurnsOf(t, restored)
	if got := completionsOf(turns, turnID); len(got) != 1 || got[0] != schema.TurnCompleted {
		t.Fatalf("completions of the reclaimed turn = %v, want one completed", got)
	}
	reopens, kinds := 0, 0
	for _, turn := range turns {
		if turn.TurnID != turnID {
			continue
		}
		if turn.Kind == schema.TurnReopen {
			reopens++
		}
		if turn.TurnKind != "" {
			kinds++
		}
	}
	if reopens != 1 || kinds != 1 {
		t.Fatalf("reclaimed turn has %d reopen markers and %d TurnKind stamps, want 1 and 1", reopens, kinds)
	}
}
