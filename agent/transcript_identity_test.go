package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// newIdentitySession is a stateful session with a scripted provider: every
// request answers with a terminal communicate.
func newIdentitySession(t *testing.T) *Session {
	t.Helper()
	done := func(llm.Request) llm.Response { return communicateResponse(true, "done") }
	return newSession(t,
		withSteps(done, done, done, done, done, done, done, done),
		withConfig(SessionConfig{
			StateDir:         t.TempDir(),
			MaxSubagentDepth: 1,
			NoProjectPrompts: true,
			testOnly:         testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		}))
}

func transcriptEntries(t *testing.T, s *Session) []transcript.Entry {
	t.Helper()
	_, entries, _, err := readTranscript(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestSessionWritesCarryIdentityAndModel(t *testing.T) {
	s := newIdentitySession(t)
	// A model switch before the first execution of a fresh session is a
	// startup entry: the prelude.
	if err := s.SetModel("gpt-5.4"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessInput(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	entries := transcriptEntries(t, s)
	if len(entries) < 3 {
		t.Fatalf("only %d entries", len(entries))
	}
	for i, entry := range entries {
		turn := entry.Turn
		if turn.Format != schema.TurnFormatIdentity || turn.TurnID == "" {
			t.Errorf("entry %d (%s) lacks identity: format %d, turn %q", i, turn.Kind, turn.Format, turn.TurnID)
		}
		if turn.Model == "" {
			t.Errorf("entry %d (%s) has no model", i, turn.Kind)
		}
	}
	first := entries[0].Turn
	if first.Kind != schema.TurnModelSwitch || first.TurnID != appwire.SystemPreludeTurnID || first.TurnKind != schema.TurnSpanPrelude {
		t.Fatalf("first entry = %s in %q (%s), want the prelude's model switch", first.Kind, first.TurnID, first.TurnKind)
	}
}

func TestAttentionDeliveredWhileIdleIsADeliveryTurn(t *testing.T) {
	s := newIdentitySession(t)
	if _, err := s.appendDelegateNotificationDurably("delegate:d1", "the delegate reported"); err != nil {
		t.Fatal(err)
	}
	entries := transcriptEntries(t, s)
	last := entries[len(entries)-1].Turn
	if last.AttentionID != "delegate:d1" || last.TurnKind != schema.TurnSpanDelivery || !strings.HasPrefix(last.TurnID, "t_") {
		t.Fatalf("attention entry = %+v", last)
	}
}

func TestColdAttentionIsADeliveryTurn(t *testing.T) {
	s := newIdentitySession(t)
	path, id := s.TranscriptPath(), s.ID()
	s.Close()
	if _, err := appendColdDelegateNotificationDurablyWithOpen(path, id, "delegate:cold", "a cold report", time.Now(), transcript.OpenWriterForSession); err != nil {
		t.Fatal(err)
	}
	_, entries, _, err := readTranscript(path)
	if err != nil {
		t.Fatal(err)
	}
	last := entries[len(entries)-1].Turn
	if last.AttentionID != "delegate:cold" || last.TurnKind != schema.TurnSpanDelivery || last.Format != schema.TurnFormatIdentity {
		t.Fatalf("cold attention entry = %+v", last)
	}
}

func TestForkCopiesKeepTheParentsIdentity(t *testing.T) {
	s := newIdentitySession(t)
	if _, err := s.ProcessInput(context.Background(), "first", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessInput(context.Background(), "second", nil); err != nil {
		t.Fatal(err)
	}
	parent := transcriptEntries(t, s)
	divergence := 0
	for i, entry := range parent {
		if entry.Turn.Kind == schema.TurnUserInput && entry.Turn.Message.Text() == "second" {
			divergence = i + 1
		}
	}
	stateDir := filepath.Dir(filepath.Dir(s.TranscriptPath()))
	childID, err := ForkSession(stateDir, s.ID(), divergence, "edited second", "")
	if err != nil {
		t.Fatal(err)
	}
	_, child, _, err := readTranscript(filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range divergence - 1 {
		if child[i].Turn.TurnID != parent[i].Turn.TurnID || child[i].Turn.TurnKind != parent[i].Turn.TurnKind || child[i].Turn.Format != parent[i].Turn.Format {
			t.Fatalf("copied entry %d identity = (%q, %q), parent (%q, %q)", i, child[i].Turn.TurnID, child[i].Turn.TurnKind, parent[i].Turn.TurnID, parent[i].Turn.TurnKind)
		}
	}
	edited := child[len(child)-1].Turn
	if edited.Message.Text() != "edited second" || edited.TurnKind != schema.TurnSpanDelivery {
		t.Fatalf("edited entry = %+v", edited)
	}
	if _, err := os.Stat(s.TranscriptPath()); err != nil {
		t.Fatal(err)
	}
}
