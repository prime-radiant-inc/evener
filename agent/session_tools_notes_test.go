package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// newNotesToolSession builds a minimal Session with a real registry, suitable
// for invoking the notes tools through sess.reg.ExecuteCall (mirroring
// testGoalSession in session_tools_goal_test.go).
func newNotesToolSession(t *testing.T) *Session {
	t.Helper()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess
}

// notesToolExec invokes a notes tool through the session registry.
func notesToolExec(ctx context.Context, t *testing.T, s *Session, id, name string, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res := s.reg.ExecuteCall(ctx, s.env, llm.ToolCallData{
		ID:        id,
		Name:      name,
		Arguments: json.RawMessage(raw),
	})
	if res.IsError {
		t.Fatalf("%s: unexpected error: %s", name, res.Output)
	}
	return res.Output
}

// agentNoteForTest reads the stored agent note under the session lock.
func (s *Session) agentNoteForTest() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentNote
}

// nextNotesEvent returns the next NOTES_UPDATED or URLS_UPDATED event payload.
func nextNotesEvent(t *testing.T, sess *Session, want events.EventKind) events.EventData {
	t.Helper()
	// TRIPWIRE: mutations emit synchronously; one second only bounds a wedged regression.
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatalf("session event stream closed before %s", want)
			}
			if ev.Kind != want {
				continue
			}
			return ev.Data
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

func TestNotesAgentSetTool(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	ctx := context.Background()
	out := notesToolExec(ctx, t, s, "n1", "notes_agent_set", map[string]any{"note": "agent says hi"})
	if out == "" {
		t.Fatalf("output = %q, want non-empty", out)
	}
	if got := s.agentNoteForTest(); got != "agent says hi" {
		t.Fatalf("agent note = %q, want %q", got, "agent says hi")
	}
	data, ok := nextNotesEvent(t, s, events.EventNotesUpdated).(events.NotesUpdatedData)
	if !ok {
		t.Fatalf("NOTES_UPDATED payload = %T", nextNotesEvent(t, s, events.EventNotesUpdated))
	}
	if data.AgentNote != "agent says hi" {
		t.Fatalf("NOTES_UPDATED agent note = %q, want %q", data.AgentNote, "agent says hi")
	}
}

func TestUrlsAddRemoveTool(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	ctx := context.Background()
	added := notesToolExec(ctx, t, s, "u1", "urls_add", map[string]any{"url": "https://x.test/y", "label": "why"})
	if added == "" {
		t.Fatalf("urls_add output = %q, want non-empty", added)
	}
	urls := s.sessionURLsForTest()
	if len(urls) != 1 {
		t.Fatalf("url list length = %d, want 1", len(urls))
	}
	entry := urls[0]
	if entry.URL != "https://x.test/y" || entry.Label != "why" {
		t.Fatalf("entry = %+v, want url https://x.test/y label why", entry)
	}
	if _, ok := nextNotesEvent(t, s, events.EventUrlsUpdated).(events.UrlsUpdatedData); !ok {
		t.Fatal("URLS_UPDATED payload has wrong type")
	}
	// Entry id must round-trip into remove.
	removed := notesToolExec(ctx, t, s, "u2", "urls_remove", map[string]any{"id": entry.ID})
	if removed == "" {
		t.Fatalf("urls_remove output = %q, want non-empty", removed)
	}
	if got := s.sessionURLsForTest(); len(got) != 0 {
		t.Fatalf("url list after remove = %+v, want empty", got)
	}
}

func TestNotesReadTool(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	ctx := context.Background()
	if _, err := s.SetHumanNote("fixture", "human hello"); err != nil {
		t.Fatal(err)
	}
	if _, changed := s.setAgentNote("agent hello"); !changed {
		t.Fatal("agent set not reported as change")
	}
	if _, err := s.addSessionURL("https://x.test/y", "x"); err != nil {
		t.Fatalf("add: %v", err)
	}
	out := notesToolExec(ctx, t, s, "r1", "notes_read", map[string]any{})
	for _, want := range []string{"human hello", "agent hello", "https://x.test/y"} {
		if !strings.Contains(out, want) {
			t.Fatalf("notes_read output = %q, want it to contain %q", out, want)
		}
	}
}

// TestUrlsRemoveUsingOnlyToolReturnedInfo verifies the M2 contract: a model
// holding nothing but tool-returned Output text (never session internals) can
// add a URL, read it back, and remove it. The urls_add output, the notes_read
// output, and the injected context block must each carry the entry id that
// urls_remove requires.
func TestUrlsRemoveUsingOnlyToolReturnedInfo(t *testing.T) {
	t.Parallel()
	s := newNotesToolSession(t)
	ctx := context.Background()
	added := notesToolExec(ctx, t, s, "u1", "urls_add", map[string]any{"url": "https://x.test/y", "label": "why"})
	entry := s.sessionURLsForTest()[0]
	if !strings.Contains(added, entry.ID) {
		t.Fatalf("urls_add output = %q, want it to carry entry id %q", added, entry.ID)
	}
	read := notesToolExec(ctx, t, s, "r1", "notes_read", map[string]any{})
	if !strings.Contains(read, entry.ID) {
		t.Fatalf("notes_read output = %q, want it to carry entry id %q", read, entry.ID)
	}
	block := s.notesContextBlock()
	if !strings.Contains(block, entry.ID) {
		t.Fatalf("context block = %q, want it to carry entry id %q", block, entry.ID)
	}
	// Remove using only the id surfaced by the tool outputs (which is also
	// the id in the context block — all three come from one rendering).
	id := entry.ID
	for _, out := range []string{added, read, block} {
		if !strings.Contains(out, id) {
			t.Fatalf("output %q does not carry id %q", out, id)
		}
	}
	removed := notesToolExec(ctx, t, s, "u2", "urls_remove", map[string]any{"id": id})
	if removed == "" {
		t.Fatalf("urls_remove output = %q, want non-empty", removed)
	}
	if got := s.sessionURLsForTest(); len(got) != 0 {
		t.Fatalf("url list after remove = %+v, want empty", got)
	}
}
