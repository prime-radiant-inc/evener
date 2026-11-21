package agent

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// A role- or plugin-changing fork starts with an empty transcript header, so
// only the session meta can say which prompt it launched with. The meta write
// must land by the time the restored session is exposed: a history read opened
// before the first turn computes its prelude once, at open, and a later save
// arrives too late for that handle.
func TestResumedForkFirstHistoryReadCarriesTheLaunchPrompt(t *testing.T) {
	t.Parallel()
	const parentPromptMarker = "MANAGER_PARENT_PROMPT_MARKER_8b41"
	stateDir := t.TempDir()

	parent := newSession(t, withStateDir(stateDir), withConfig(SessionConfig{
		MaxSubagentDepth:        2,
		Manager:                 true,
		ForceRealIO:             true,
		UserInstructionOverride: parentPromptMarker,
		testOnly:                testConfig{skipGitSnapshot: true, noSyncJobStore: true},
	}))
	parent.appendTurn(schema.TurnUserInput, llm.User("fork me"))
	parentID, parentPrompt := parent.ID(), parent.cachedSystemPrompt
	parent.Close()

	childID, err := ForkSession(stateDir, parentID, 1, "forked from manager", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if hdr := sessionHeader(t, stateDir, childID); hdr.SystemPrompt != "" {
		t.Fatalf("setup: fork header prompt = %q, want none", hdr.SystemPrompt)
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if childMeta.LaunchSystemPrompt != "" {
		t.Fatalf("setup: fork meta already saved LaunchSystemPrompt %q", childMeta.LaunchSystemPrompt)
	}

	// An ordinary same-model resume, then a history read before any turn.
	restored := restorePromptState(t, stateDir, childMeta, t.TempDir())
	launchPrompt := restored.cachedSystemPrompt
	if launchPrompt == "" || launchPrompt == parentPrompt {
		t.Fatalf("setup: restored prompt = %q, want the child's own non-empty prompt", launchPrompt)
	}

	saved, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta after restore: %v", err)
	}
	if saved.LaunchSystemPrompt != launchPrompt {
		t.Fatalf("restored fork meta LaunchSystemPrompt = %q, want the rendered prompt %q persisted before the session is exposed", saved.LaunchSystemPrompt, launchPrompt)
	}

	if item := indexPreludeSystemPrompt(t, stateDir, childID); item.Text != launchPrompt {
		t.Fatalf("first history read prelude = %q, want the launch prompt %q", item.Text, launchPrompt)
	}
}

// A fork that inherits its parent's prompt records it in the transcript header.
// The header stays authoritative: the prelude must show it, never a meta
// fallback.
func TestResumedForkFirstHistoryReadKeepsHeaderLaunchPrompt(t *testing.T) {
	t.Parallel()
	const marker = "INHERITING_FORK_PROMPT_MARKER_c510"
	stateDir := t.TempDir()

	parent := newSession(t, withStateDir(stateDir), withConfig(SessionConfig{
		MaxSubagentDepth:        2,
		ForceRealIO:             true,
		UserInstructionOverride: marker,
		testOnly:                testConfig{skipGitSnapshot: true, noSyncJobStore: true},
	}))
	parent.appendTurn(schema.TurnUserInput, llm.User("fork me"))
	parentID, parentPrompt := parent.ID(), parent.cachedSystemPrompt
	parent.Close()

	childID, err := ForkSession(stateDir, parentID, 1, "forked", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if hdr := sessionHeader(t, stateDir, childID); hdr.SystemPrompt != parentPrompt {
		t.Fatalf("setup: fork header prompt = %q, want the inherited parent prompt", hdr.SystemPrompt)
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}

	restorePromptState(t, stateDir, childMeta, t.TempDir())

	if item := indexPreludeSystemPrompt(t, stateDir, childID); item.Text != parentPrompt {
		t.Fatalf("first history read prelude = %q, want the header prompt %q", item.Text, parentPrompt)
	}
}

// indexPreludeSystemPrompt reads the session's system prelude through the
// transcript index, the way a history read does.
func indexPreludeSystemPrompt(t *testing.T, stateDir, id string) appwire.ThreadItem {
	t.Helper()
	path := schema.SessionTranscriptPath(stateDir, id)
	index, err := transcriptindex.Open(path, transcriptindex.DirFor(path))
	if err != nil {
		t.Fatalf("transcriptindex.Open: %v", err)
	}
	t.Cleanup(func() { _ = index.Close() })
	window, err := index.Latest(10)
	if err != nil {
		t.Fatalf("index.Latest: %v", err)
	}
	for _, candidate := range window.Candidates {
		if candidate.TurnID == appwire.SystemPreludeTurnID && candidate.Item.EventKind == appwire.ThreadItemEventKindSystemPrompt {
			return candidate.Item
		}
	}
	t.Fatalf("no system prelude item in the first read of %d candidates: %+v", len(window.Candidates), window.Candidates)
	return appwire.ThreadItem{}
}
