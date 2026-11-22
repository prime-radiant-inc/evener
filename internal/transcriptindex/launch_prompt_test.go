package transcriptindex

import (
	"os"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// The index's prelude is computed once at open, from the transcript header. A
// fork whose header carries no prompt saved the prompt it rendered to its
// meta, and item windows read through this index must still show it.
func TestIndexProjectsLaunchSystemPromptSavedInMeta(t *testing.T) {
	const launchPrompt = "You are the forked agent."
	stateDir := t.TempDir()
	const id = "sess_index_launch_prompt"
	path := schema.SessionTranscriptPath(stateDir, id)
	writeIndexLaunchPromptSession(t, stateDir, id, "", launchPrompt)

	index, err := Open(path, DirFor(path))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = index.Close() })

	window, err := index.Latest(10)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	for _, candidate := range window.Candidates {
		if candidate.TurnID == appwire.SystemPreludeTurnID && candidate.Item.Text == launchPrompt {
			return
		}
	}
	t.Fatalf("no %s item carrying the meta launch prompt %q in %+v", appwire.SystemPreludeTurnID, launchPrompt, window.Candidates)
}

// writeIndexLaunchPromptSession writes a canonical transcript and its meta.
func writeIndexLaunchPromptSession(t *testing.T, stateDir, id, headerPrompt, metaPrompt string) {
	t.Helper()
	path := schema.SessionTranscriptPath(stateDir, id)
	if err := os.MkdirAll(schema.SessionsDir(stateDir), 0o755); err != nil {
		t.Fatal(err)
	}
	header := transcript.Header{SessionID: id, SystemPrompt: headerPrompt}
	content := append(encodeHeader(t, header), encodeEntry(t, 1, user("hello"))...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: id, LaunchSystemPrompt: metaPrompt}); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}
}
