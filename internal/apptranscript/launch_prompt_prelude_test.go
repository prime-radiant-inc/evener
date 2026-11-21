package apptranscript

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A role- or plugin-changing fork starts with a transcript header that carries
// no prompt, and saves the prompt it rendered to its meta instead. The item
// read must project that saved prompt as the system prelude, or the thread
// shows no launch prompt at all.
func TestItemTurnsFromFileProjectsLaunchSystemPromptSavedInMeta(t *testing.T) {
	const launchPrompt = "You are the child agent."
	path := writeLaunchPromptSession(t, "", launchPrompt)

	turns, err := ItemTurnsFromFile(path, 1<<20, nil)
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	item := preludeSystemPromptItem(t, turns)
	if item.Text != launchPrompt {
		t.Fatalf("prelude system prompt = %q, want the meta launch prompt %q", item.Text, launchPrompt)
	}
	if item.EventKind != appwire.ThreadItemEventKindSystemPrompt {
		t.Fatalf("prelude item event kind = %q", item.EventKind)
	}
}

// The transcript header's own prompt is what the session ran with; the meta
// fallback only answers when the header recorded none.
func TestItemTurnsFromFileKeepsHeaderPromptOverMetaFallback(t *testing.T) {
	path := writeLaunchPromptSession(t, "You are the header prompt.", "You are the meta fallback.")

	turns, err := ItemTurnsFromFile(path, 1<<20, nil)
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	if item := preludeSystemPromptItem(t, turns); item.Text != "You are the header prompt." {
		t.Fatalf("prelude system prompt = %q, want the header prompt", item.Text)
	}
}

// The render-failure diagnostic is prose, not a prompt: a session whose render
// failed saved that prose to the meta, and the read must not show it as the
// launch prompt.
func TestItemTurnsFromFileDoesNotProjectRenderFailureProseFromMeta(t *testing.T) {
	path := writeLaunchPromptSession(t, "", "Template rendering failed: forced. Please report this bug.")

	project := func(schema.Turn, string, int) []appwire.ThreadItem {
		return []appwire.ThreadItem{{Type: "userMessage", Text: "hello"}}
	}
	turns, err := ItemTurnsFromFile(path, 1<<20, project)
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	if len(turns) == 0 {
		t.Fatal("no turns projected: the assertion below would pass vacuously")
	}
	for _, turn := range turns {
		if turn.ID == appwire.SystemPreludeTurnID {
			t.Fatalf("render-failure prose projected as the system prelude: %+v", turn.Items)
		}
	}
}

// writeLaunchPromptSession writes a canonical session transcript under a fresh
// state directory, with headerPrompt recorded in its header and metaPrompt
// saved to its meta. Either prompt may be empty.
func writeLaunchPromptSession(t *testing.T, headerPrompt, metaPrompt string) string {
	t.Helper()
	stateDir := t.TempDir()
	const id = "sess_launch_prompt"
	path := schema.SessionTranscriptPath(stateDir, id)
	tw, err := transcript.NewWriter(path, transcript.Header{SessionID: id, SystemPrompt: headerPrompt})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	if err := tw.Append(schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("hello")}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	meta := schema.SessionMeta{ID: id}
	if metaPrompt != "" {
		meta.LaunchSystemPrompt = metaPrompt
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
	return path
}

func preludeSystemPromptItem(t *testing.T, turns []appwire.Turn) appwire.ThreadItem {
	t.Helper()
	for _, turn := range turns {
		if turn.ID != appwire.SystemPreludeTurnID {
			continue
		}
		for _, item := range turn.Items {
			if item.EventKind == appwire.ThreadItemEventKindSystemPrompt {
				return item
			}
		}
	}
	t.Fatalf("no system prelude item in %d turns: %+v", len(turns), turns)
	return appwire.ThreadItem{}
}
