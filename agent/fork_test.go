package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/envctx"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// buildParentSession creates a parent session with a 4-turn transcript
// (USER, ASSISTANT, USER, ASSISTANT) and a corresponding meta file.
// Returns the parentID and stateDir.
func buildParentSession(t *testing.T) (stateDir, parentID string) {
	t.Helper()
	stateDir = t.TempDir()
	parentID = "01PARENT00000000000000001"

	tpath := schema.SessionTranscriptPath(stateDir, parentID)
	tw, err := transcript.NewWriter(tpath, transcript.Header{
		SessionID:  parentID,
		CreatedAt:  time.Now().UTC(),
		ProfileID:  "openai",
		Model:      "gpt-5.2",
		WorkingDir: "/tmp/test",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}

	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("first task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("first reply")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second task")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("second reply")),
	}
	for _, turn := range turns {
		if err := tw.Append(turn); err != nil {
			t.Fatalf("Append turn: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close transcript: %v", err)
	}

	meta := schema.SessionMeta{
		ID:        parentID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    schema.ConfigSnapshot{MaxToolRoundsPerInput: 50},
		EnvInfo:   schema.EnvironmentInfo{WorkingDir: "/tmp/test"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		TurnCount: 2,
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("SaveSessionMeta: %v", err)
	}

	return stateDir, parentID
}

func appendParentTranscript(t *testing.T, stateDir, parentID, suffix string) {
	t.Helper()
	path := schema.SessionTranscriptPath(stateDir, parentID)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open parent transcript: %v", err)
	}
	if _, err := f.WriteString(suffix); err != nil {
		_ = f.Close()
		t.Fatalf("append parent transcript: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close parent transcript: %v", err)
	}
}

func sessionArtifactNames(t *testing.T, stateDir string) string {
	t.Helper()
	entries, err := os.ReadDir(schema.SessionsDir(stateDir))
	if err != nil {
		t.Fatalf("read session artifacts: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return strings.Join(names, "\n")
}

// TestForkSession_CopiesPrefixAndAppliesEdit verifies the core fork semantics:
// the child gets the first (divergenceTurn-1) USER_INPUT turns from the parent
// followed by the edited message, and meta is wired up correctly on both sides.
func TestForkSession_CopiesPrefixAndAppliesEdit(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)

	childID, err := ForkSession(stateDir, parentID, 3, "second task, table-driven", "before TDD")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if err := identifier.ValidateSessionID(childID); err != nil {
		t.Fatalf("child session ID %q: %v", childID, err)
	}

	// childID must be non-empty and distinct from parentID.
	if childID == "" {
		t.Fatal("childID is empty")
	}
	if childID == parentID {
		t.Fatalf("childID should differ from parentID, got %q", childID)
	}

	// Child meta assertions.
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.ParentSessionID != parentID {
		t.Errorf("child ParentSessionID: got %q, want %q", childMeta.ParentSessionID, parentID)
	}
	if childMeta.DivergenceTurn != 3 {
		t.Errorf("child DivergenceTurn: got %d, want 3", childMeta.DivergenceTurn)
	}
	if childMeta.TurnCount != 1 {
		t.Errorf("child TurnCount: got %d, want 1", childMeta.TurnCount)
	}
	if childMeta.ForkLabel != "" {
		t.Errorf("child ForkLabel should be empty, got %q", childMeta.ForkLabel)
	}

	// Parent meta should have been updated with the fork label.
	parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}
	if parentMeta.ForkLabel != "before TDD" {
		t.Errorf("parent ForkLabel: got %q, want %q", parentMeta.ForkLabel, "before TDD")
	}

	// Child transcript must contain the edited message in its last entry line.
	childTranscriptPath := schema.SessionTranscriptPath(stateDir, childID)
	data, err := os.ReadFile(childTranscriptPath)
	if err != nil {
		t.Fatalf("ReadFile(child transcript): %v", err)
	}
	if !strings.Contains(string(data), "second task, table-driven") {
		t.Error("child transcript does not contain the edited message text")
	}

	// Read child transcript via readTranscript and verify structure.
	_, entries, _, err := readTranscript(context.Background(), childTranscriptPath, "")
	if err != nil {
		t.Fatalf("readTranscript(child): %v", err)
	}

	// Expected entries:
	//   0: USER_INPUT "first task"         (turn 1 from parent)
	//   1: ASSISTANT  "first reply"        (non-USER turn, included in prefix)
	//   2: USER_INPUT "second task, table-driven"  (the edited divergence turn)
	if len(entries) != 3 {
		t.Fatalf("child transcript entry count: got %d, want 3", len(entries))
	}

	// First two entries are the prefix (turn 1 user + turn 1 assistant from parent).
	if entries[0].Turn.Kind != schema.TurnUserInput {
		t.Errorf("entries[0].Kind: got %q, want %q", entries[0].Turn.Kind, schema.TurnUserInput)
	}
	if entries[0].Turn.Message.Text() != "first task" {
		t.Errorf("entries[0] text: got %q, want %q", entries[0].Turn.Message.Text(), "first task")
	}
	if entries[1].Turn.Kind != schema.TurnAssistant {
		t.Errorf("entries[1].Kind: got %q, want %q", entries[1].Turn.Kind, schema.TurnAssistant)
	}

	// Third entry is the edited turn.
	if entries[2].Turn.Kind != schema.TurnUserInput {
		t.Errorf("entries[2].Kind: got %q, want %q", entries[2].Turn.Kind, schema.TurnUserInput)
	}
	if entries[2].Turn.Message.Text() != "second task, table-driven" {
		t.Errorf("entries[2] text: got %q, want %q", entries[2].Turn.Message.Text(), "second task, table-driven")
	}
}

// A fork child is written as a root, so it must keep a restricted root parent's
// persisted memory-save verdict but must not inherit a delegate parent's
// save-tool removal: the child registers the save tools on launch.
func TestForkSessionPreservesMemorySaveUnavailable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		unavailable bool
		delegate    bool
		want        bool
	}{
		{"restricted-root", true, false, true},
		{"delegate-parent", true, true, false},
		{"capable-root", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stateDir, parentID := buildParentSession(t)
			parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
			if err != nil {
				t.Fatalf("LoadSessionMeta(parent): %v", err)
			}
			parentMeta.MemorySaveUnavailable = tc.unavailable
			parentMeta.IsSubagent = tc.delegate
			if err := schema.SaveSessionMeta(stateDir, parentMeta); err != nil {
				t.Fatalf("SaveSessionMeta(parent): %v", err)
			}
			childID, _, err := ForkSessionAtUserTurn(stateDir, parentID, 3, "")
			if err != nil {
				t.Fatalf("ForkSessionAtUserTurn: %v", err)
			}
			childMeta, err := schema.LoadSessionMeta(stateDir, childID)
			if err != nil {
				t.Fatalf("LoadSessionMeta(child): %v", err)
			}
			if childMeta.MemorySaveUnavailable != tc.want {
				t.Fatalf("child MemorySaveUnavailable = %t, want %t", childMeta.MemorySaveUnavailable, tc.want)
			}
		})
	}
}

func TestUnrestoredLegacyManagerForkAsideDiscardsParentPrompt(t *testing.T) {
	t.Parallel()
	const parentMarker = "LEGACY_MANAGER_ANCESTOR_MARKER_7f12"
	stateDir := t.TempDir()

	parent := newSession(t, withStateDir(stateDir), withConfig(SessionConfig{
		MaxSubagentDepth: 2, Manager: true, ForceRealIO: true,
		UserInstructionOverride: parentMarker,
		testOnly:                testConfig{skipGitSnapshot: true, noSyncJobStore: true},
	}))
	parent.appendTurn(schema.TurnUserInput, llm.User("fork me"))
	parentID, parentPrompt, parentBaseline := parent.ID(), parent.cachedSystemPrompt, parent.Meta().LaunchEnvBaseline
	parent.Close()

	// Create an ordinary fork from the Manager, then plant the pre-#6959 legacy
	// shape on disk and leave it UNRESTORED (LaunchSystemPrompt == "").
	legacyForkID, err := ForkSession(stateDir, parentID, 1, "legacy fork", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	rewriteLegacyForkHeader(t, stateDir, legacyForkID, parentPrompt)
	legacyMeta, err := schema.LoadSessionMeta(stateDir, legacyForkID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(legacyFork): %v", err)
	}
	staleBaseline := parentBaseline
	staleBaseline.Model = "legacy-unrestored-manager-model"
	legacyMeta.LaunchSystemPrompt = ""
	legacyMeta.LaunchEnvBaseline = staleBaseline
	if err := schema.SaveSessionMeta(stateDir, legacyMeta); err != nil {
		t.Fatalf("SaveSessionMeta(legacyFork): %v", err)
	}

	// Open an aside (--resume-with matching config) from the unrestored legacy fork.
	asideCfg := legacyMeta.Config.Clone()
	asideID, err := AsideSessionWithConfig(stateDir, legacyForkID, asideCfg)
	if err != nil {
		t.Fatalf("AsideSessionWithConfig: %v", err)
	}
	if hdr := sessionHeader(t, stateDir, asideID); hdr.SystemPrompt != "" {
		t.Fatalf("aside of unrestored legacy manager fork copied header prompt %q, want empty", hdr.SystemPrompt)
	}
	asideMeta, err := schema.LoadSessionMeta(stateDir, asideID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(aside): %v", err)
	}
	if asideMeta.LaunchEnvBaseline != (envctx.Snapshot{}) {
		t.Fatalf("aside baseline = %+v, want zero snapshot", asideMeta.LaunchEnvBaseline)
	}

	restored := restorePromptState(t, stateDir, asideMeta, t.TempDir())
	if restored.cachedSystemPrompt == parentPrompt || restored.Meta().LaunchSystemPrompt != restored.cachedSystemPrompt {
		t.Fatalf("restored aside kept parent Manager prompt or failed to persist LaunchSystemPrompt:\n%s", restored.cachedSystemPrompt)
	}
}

func TestRecoveredForkChildRejectsPreRecoveryDescendantHeader(t *testing.T) {
	t.Parallel()
	const managerMarker = "RECOVERED_ANCESTOR_MANAGER_MARKER_5e21"
	stateDir := t.TempDir()

	manager := newSession(t, withStateDir(stateDir), withConfig(SessionConfig{
		MaxSubagentDepth: 2, Manager: true, ForceRealIO: true,
		UserInstructionOverride: managerMarker,
		testOnly:                testConfig{skipGitSnapshot: true, noSyncJobStore: true},
	}))
	manager.appendTurn(schema.TurnUserInput, llm.User("fork 1"))
	managerID, managerPrompt := manager.ID(), manager.cachedSystemPrompt
	manager.Close()

	// Fork1 was created from Manager before #6959, then Fork2 was forked from Fork1
	// before #6959 (both headers copied managerPrompt, both LaunchSystemPrompt == "").
	fork1ID, err := ForkSession(stateDir, managerID, 1, "fork1", "")
	if err != nil {
		t.Fatalf("ForkSession(fork1): %v", err)
	}
	rewriteLegacyForkHeader(t, stateDir, fork1ID, managerPrompt)
	fork2ID, err := ForkSession(stateDir, fork1ID, 1, "fork2", "")
	if err != nil {
		t.Fatalf("ForkSession(fork2): %v", err)
	}
	rewriteLegacyForkHeader(t, stateDir, fork2ID, managerPrompt)

	// Resume Fork1 on the new build: it recovers and persists its non-Manager LaunchSystemPrompt.
	fork1Meta, err := schema.LoadSessionMeta(stateDir, fork1ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(fork1): %v", err)
	}
	restoredFork1 := restorePromptState(t, stateDir, fork1Meta, t.TempDir())
	fork1Prompt := restoredFork1.cachedSystemPrompt
	if fork1Prompt == managerPrompt || restoredFork1.Meta().LaunchSystemPrompt != fork1Prompt {
		t.Fatalf("fork1 did not recover its own non-Manager prompt")
	}
	restoredFork1.Close()

	// Later resume Fork2 (saved pre-recovery with managerPrompt in its header):
	// Fork2 must compare against Fork1's recovered LaunchSystemPrompt and recover too.
	fork2Meta, err := schema.LoadSessionMeta(stateDir, fork2ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(fork2): %v", err)
	}
	restoredFork2 := restorePromptState(t, stateDir, fork2Meta, t.TempDir())
	if restoredFork2.cachedSystemPrompt == managerPrompt {
		t.Fatalf("fork2 kept stale Manager prompt from pre-recovery header instead of recovering:\n%s", restoredFork2.cachedSystemPrompt)
	}
	if restoredFork2.Meta().LaunchSystemPrompt != restoredFork2.cachedSystemPrompt {
		t.Fatalf("fork2 did not persist recovered LaunchSystemPrompt")
	}
}

// An aside can carry a different config from its parent. Its child meta must
// take an explicit memory disable from that config, keep a restricted root
// parent's verdict, and still not inherit a delegate parent's save-tool removal.
func TestAsideSessionPreservesMemorySaveUnavailable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		parentUnavailable  bool
		parentDelegate     bool
		childDisableMemory bool
		want               bool
	}{
		{"capable-parent-disabled-aside", false, false, true, true},
		{"restricted-parent-capable-aside", true, false, false, true},
		{"delegate-parent-capable-aside", true, true, false, false},
		{"capable-parent-capable-aside", false, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stateDir, parentID := buildParentSession(t)
			parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
			if err != nil {
				t.Fatalf("LoadSessionMeta(parent): %v", err)
			}
			parentMeta.MemorySaveUnavailable = tc.parentUnavailable
			parentMeta.IsSubagent = tc.parentDelegate
			if err := schema.SaveSessionMeta(stateDir, parentMeta); err != nil {
				t.Fatalf("SaveSessionMeta(parent): %v", err)
			}
			childID, err := AsideSessionWithConfig(stateDir, parentID, schema.ConfigSnapshot{DisableMemory: tc.childDisableMemory})
			if err != nil {
				t.Fatalf("AsideSessionWithConfig: %v", err)
			}
			childMeta, err := schema.LoadSessionMeta(stateDir, childID)
			if err != nil {
				t.Fatalf("LoadSessionMeta(child): %v", err)
			}
			if childMeta.MemorySaveUnavailable != tc.want {
				t.Fatalf("child MemorySaveUnavailable = %t, want %t", childMeta.MemorySaveUnavailable, tc.want)
			}
		})
	}
}

func TestForkAndAsidePreserveCheapModel(t *testing.T) {
	t.Parallel()
	operations := []struct {
		name   string
		create func(string, string) (string, error)
	}{
		{
			name: "fork",
			create: func(stateDir, parentID string) (string, error) {
				return ForkSession(stateDir, parentID, 3, "edited", "")
			},
		},
		{
			name: "aside",
			create: func(stateDir, parentID string) (string, error) {
				return AsideSession(stateDir, parentID)
			},
		},
	}
	routes := []struct {
		name         string
		ref          string
		wantProvider string
		wantModel    string
	}{
		{name: "relative", ref: "gpt-5-mini", wantProvider: "openai", wantModel: "gpt-5-mini"},
		{name: "qualified", ref: "anthropic/claude-haiku-4-5-20251001", wantProvider: "anthropic", wantModel: "claude-haiku-4-5-20251001"},
	}
	for _, operation := range operations {
		for _, route := range routes {
			t.Run(operation.name+"/"+route.name, func(t *testing.T) {
				stateDir, parentID := buildParentSession(t)
				parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
				if err != nil {
					t.Fatalf("LoadSessionMeta(parent): %v", err)
				}
				parentMeta.CheapModel = route.ref
				if err := schema.SaveSessionMeta(stateDir, parentMeta); err != nil {
					t.Fatalf("SaveSessionMeta(parent): %v", err)
				}

				childID, err := operation.create(stateDir, parentID)
				if err != nil {
					t.Fatalf("%s: %v", operation.name, err)
				}
				childMeta, err := schema.LoadSessionMeta(stateDir, childID)
				if err != nil {
					t.Fatalf("LoadSessionMeta(child): %v", err)
				}
				if childMeta.CheapModel != route.ref {
					t.Fatalf("child CheapModel = %q, want %q", childMeta.CheapModel, route.ref)
				}

				client := llm.NewClient()
				client.Register(&fakeAdapter{name: "openai"})
				client.Register(&fakeAdapter{name: "anthropic"})
				restored, err := RestoreSessionFromMetaWithConfig(
					client,
					NewOpenAIProfile(childMeta.Model),
					execenv.NewLocalExecutionEnvironment(t.TempDir()),
					childMeta,
					RestoreSessionConfig{StateDir: stateDir},
				)
				if err != nil {
					t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
				}
				defer restored.Close()
				providerName, model := restored.profile.CheapModelRef()
				if providerName != route.wantProvider || model != route.wantModel {
					t.Fatalf("restored CheapModelRef() = (%q, %q), want (%q, %q)",
						providerName, model, route.wantProvider, route.wantModel)
				}
			})
		}
	}
}

func TestForkSession_RejectsMixedAPICallWithoutCreatingChild(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	appendParentTranscript(t, stateDir, parentID, "{\"kind\":\"api_call\"}\n")
	before := sessionArtifactNames(t, stateDir)

	childID, err := ForkSession(stateDir, parentID, 3, "edited", "")
	if !errors.Is(err, transcript.ErrUnsupportedFormat) {
		t.Fatalf("ForkSession error = %v, want transcript.ErrUnsupportedFormat", err)
	}
	if childID != "" {
		t.Fatalf("ForkSession child id = %q, want empty", childID)
	}
	if after := sessionArtifactNames(t, stateDir); after != before {
		t.Fatalf("session artifacts changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestForkSession_RejectsUnsupportedHeader(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		transform func(string) string
	}{
		{
			name: "version one",
			transform: func(raw string) string {
				return strings.Replace(raw, `"format_version":2`, `"format_version":1`, 1)
			},
		},
		{
			name: "missing version",
			transform: func(raw string) string {
				return strings.Replace(raw, `"format_version":2,`, "", 1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			stateDir, parentID := buildParentSession(t)
			parentPath := schema.SessionTranscriptPath(stateDir, parentID)
			raw, err := os.ReadFile(parentPath)
			if err != nil {
				t.Fatalf("read parent transcript: %v", err)
			}
			if err := os.WriteFile(parentPath, []byte(tt.transform(string(raw))), 0o644); err != nil {
				t.Fatalf("write parent transcript: %v", err)
			}

			if _, err := ForkSession(stateDir, parentID, 3, "edited", ""); !errors.Is(err, transcript.ErrUnsupportedFormat) {
				t.Fatalf("ForkSession error = %v, want transcript.ErrUnsupportedFormat", err)
			}
		})
	}
}

func TestForkSession_RejectsUnknownTranscriptFields(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		transform func(string) string
	}{
		{
			name: "header",
			transform: func(raw string) string {
				return strings.Replace(raw, `"format_version":2`, `"format_version":2,"unknown":true`, 1)
			},
		},
		{
			name: "entry",
			transform: func(raw string) string {
				return strings.Replace(raw, `"seq":0`, `"seq":0,"unknown":true`, 1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stateDir, parentID := buildParentSession(t)
			parentPath := schema.SessionTranscriptPath(stateDir, parentID)
			raw, err := os.ReadFile(parentPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(parentPath, []byte(tt.transform(string(raw))), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := ForkSession(stateDir, parentID, 3, "edited", ""); err == nil {
				t.Fatal("ForkSession accepted an unknown transcript field")
			}
		})
	}
}

func TestForkSession_UsesFirstNonEmptyRecordAsHeader(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	parentPath := schema.SessionTranscriptPath(stateDir, parentID)
	raw, err := os.ReadFile(parentPath)
	if err != nil {
		t.Fatalf("read parent transcript: %v", err)
	}
	if err := os.WriteFile(parentPath, append([]byte("\n"), raw...), 0o644); err != nil {
		t.Fatalf("write parent transcript: %v", err)
	}

	childID, err := ForkSession(stateDir, parentID, 3, "edited", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if childID == "" {
		t.Fatal("ForkSession child id is empty")
	}
}

func TestForkSession_RejectsMalformedCompleteInteriorRecordWithoutCreatingChild(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	appendParentTranscript(t, stateDir, parentID, "{not valid json\n")
	before := sessionArtifactNames(t, stateDir)
	childID, err := ForkSession(stateDir, parentID, 3, "edited", "")
	if err == nil {
		t.Fatal("ForkSession error is nil, want malformed-record failure")
	}
	if childID != "" {
		t.Fatalf("ForkSession child id = %q, want empty", childID)
	}
	if after := sessionArtifactNames(t, stateDir); after != before {
		t.Fatalf("session artifacts changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestForkSession_RejectsMalformedCompleteEntry(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	appendParentTranscript(t, stateDir, parentID, "{\"kind\":\"entry\",\"turn\":\"not-an-object\"}\n")

	var lineErr *transcript.LineError
	if _, err := ForkSession(stateDir, parentID, 3, "edited", ""); !errors.As(err, &lineErr) {
		t.Fatalf("ForkSession error = %v, want malformed-entry failure", err)
	}
}

func TestForkSession_IgnoresIncompleteFinalRecord(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	appendParentTranscript(t, stateDir, parentID, "{\"kind\":\"entry\",\"seq\":9,\"tur")

	childID, err := ForkSession(stateDir, parentID, 3, "edited", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if childID == "" {
		t.Fatal("ForkSession child id is empty")
	}
}

func TestForkSession_ChildLineagePreservedAcrossMetaRewrite(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	childID, err := ForkSession(stateDir, parentID, 3, "second task, table-driven", "before TDD")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}

	c := llm.NewClient()
	sess, err := RestoreSessionFromMeta(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), childMeta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	rewritten := sess.Meta()
	if rewritten.ParentSessionID != parentID {
		t.Errorf("rewritten ParentSessionID: got %q, want %q", rewritten.ParentSessionID, parentID)
	}
	if rewritten.DivergenceTurn != 3 {
		t.Errorf("rewritten DivergenceTurn: got %d, want 3", rewritten.DivergenceTurn)
	}
	if rewritten.IsSubagent {
		t.Error("rewritten IsSubagent: got true, want false for fork child")
	}
}

func TestForkSession_ParentForkLabelPreservedAcrossMetaRewrite(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	if _, err := ForkSession(stateDir, parentID, 3, "second task, table-driven", "before TDD"); err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}

	c := llm.NewClient()
	sess, err := RestoreSessionFromMeta(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	rewritten := sess.Meta()
	if rewritten.ForkLabel != "before TDD" {
		t.Errorf("rewritten ForkLabel: got %q, want %q", rewritten.ForkLabel, "before TDD")
	}
}

func TestForkSession_RestorePreservesAcceptedInputBudget(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}
	parentMeta.Config = (SessionConfig{MaxTurns: 7}).toSnapshot()
	if err := schema.SaveSessionMeta(stateDir, parentMeta); err != nil {
		t.Fatalf("SaveSessionMeta(parent): %v", err)
	}

	childID, err := ForkSession(stateDir, parentID, 3, "second task, edited", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.AcceptedInputTurns != 2 {
		t.Fatalf("child accepted input turns = %d, want 2", childMeta.AcceptedInputTurns)
	}

	adapter := &agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(llm.Request) llm.Response {
			return agenttest.CommunicateResponse(true, "done")
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	restored := restoreBudgetSession(t, client, childMeta, stateDir)

	restored.mu.Lock()
	acceptedAtRestore := restored.turns
	restored.mu.Unlock()
	if acceptedAtRestore != 2 {
		t.Fatalf("restored accepted input turns = %d, want 2", acceptedAtRestore)
	}
	if got := countBudgetSteering(budgetHistory(restored), rootTurnBudgetWarning); got != 0 {
		t.Fatalf("warning count before first restored input = %d, want 0", got)
	}

	for i := range 5 {
		if _, err := restored.ProcessInput(context.Background(), "continued input", nil); err != nil {
			t.Fatalf("ProcessInput(%d): %v", i+1, err)
		}
		if i == 0 {
			requests := adapter.Requests()
			if len(requests) != 1 || !requestHasExactUserMessage(requests[0], rootTurnBudgetWarning) {
				t.Fatalf("first restored request missing five-turn warning: %+v", requests)
			}
		}
	}
	if got := countBudgetSteering(budgetHistory(restored), rootTurnBudgetWarning); got != 1 {
		t.Fatalf("warning count after budget use = %d, want 1", got)
	}
	restored.mu.Lock()
	acceptedAtLimit := restored.turns
	restored.mu.Unlock()
	if acceptedAtLimit != 7 {
		t.Fatalf("accepted input turns at limit = %d, want 7", acceptedAtLimit)
	}

	out, err := restored.ProcessInput(context.Background(), "over budget", nil)
	if out != "" {
		t.Fatalf("over-budget output = %q, want empty", out)
	}
	requireBudgetExhaustion(t, err, exhaustedBudgetTurns, 7, false)
	if got := len(adapter.Requests()); got != 5 {
		t.Fatalf("model requests after rejected input = %d, want 5", got)
	}
}

func TestForkSession_PreservesSelectedPluginDirs(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)
	parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(parent): %v", err)
	}
	want := []string{"/plugins/selected-alpha", "/plugins/selected-beta"}
	parentMeta.Config.PluginDirs = want
	if err := schema.SaveSessionMeta(stateDir, parentMeta); err != nil {
		t.Fatalf("SaveSessionMeta(parent): %v", err)
	}

	childID, err := ForkSession(stateDir, parentID, 3, "second task, selected plugins", "")
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if !slices.Equal(childMeta.Config.PluginDirs, want) {
		t.Fatalf("child PluginDirs = %v, want %v", childMeta.Config.PluginDirs, want)
	}
}

// TestForkSession_RejectsOutOfRangeDivergence verifies that divergenceTurn=0
// and divergenceTurn exceeding the parent's USER_INPUT count both return errors.
func TestForkSession_RejectsOutOfRangeDivergence(t *testing.T) {
	t.Parallel()
	stateDir, parentID := buildParentSession(t)

	// divergenceTurn=0 must error.
	_, err := ForkSession(stateDir, parentID, 0, "irrelevant", "")
	if err == nil {
		t.Error("ForkSession(divergenceTurn=0) should return an error")
	} else if !errors.Is(err, ErrDivergencePositionOutOfRange) {
		t.Errorf("divergenceTurn=0 error should wrap ErrDivergencePositionOutOfRange, got %v", err)
	}

	// divergenceTurn exceeding count (parent has 2 USER_INPUT turns).
	_, err = ForkSession(stateDir, parentID, 10, "irrelevant", "")
	if err == nil {
		t.Error("ForkSession(divergenceTurn=10) should return an error when parent has only 2 USER_INPUT turns")
	} else if !errors.Is(err, ErrDivergencePositionOutOfRange) {
		t.Errorf("divergenceTurn=10 error should wrap ErrDivergencePositionOutOfRange, got %v", err)
	}
}

// TestForkSession_RejectsMissingParent verifies that a missing parent transcript
// causes ForkSession to return an error.
func TestForkSession_RejectsMissingParent(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()

	_, err := ForkSession(stateDir, "NONEXISTENT_SESSION", 1, "hello", "")
	if err == nil {
		t.Error("ForkSession with missing parent should return an error")
	}
}

// TestForkSession_RejectsUnsafeParentIDBeforeReadingTranscript is the kata
// 1gc4 canonical case: readForkParent joins parentID into a transcript path
// and reads it before schema.LoadSessionMetaWithFS ever gets a chance to
// validate the ID, so a traversal-shaped parentID must be refused at
// readForkParent itself, not merely reported as "not found" after an
// attempted read outside the sessions dir.
func TestForkSession_RejectsUnsafeParentIDBeforeReadingTranscript(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()

	// A file a traversal parentID could reach if the join were not validated
	// first: <stateDir>/sessions/../escaped.transcript.jsonl == <stateDir>/escaped.transcript.jsonl.
	victim := filepath.Join(stateDir, "escaped.transcript.jsonl")
	if err := os.WriteFile(victim, []byte("not a session's transcript"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := ForkSession(stateDir, "../escaped", 1, "hello", "")
	if !errors.Is(err, schema.ErrInvalidSessionID) {
		t.Fatalf("ForkSession with traversal parentID error = %v, want schema.ErrInvalidSessionID", err)
	}
}

// TestForkSession_RejectsReservedDeviceNameParentID pins the same guard
// against a Windows reserved device name, which schema.ValidateSessionID
// refuses regardless of platform (agent/schema/snapshot.go).
func TestForkSession_RejectsReservedDeviceNameParentID(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()

	_, err := ForkSession(stateDir, "CON", 1, "hello", "")
	if !errors.Is(err, schema.ErrInvalidSessionID) {
		t.Fatalf("ForkSession with reserved device name parentID error = %v, want schema.ErrInvalidSessionID", err)
	}
}

// A transcript record larger than the old 10 MiB fork limit is still within the
// transcript line cap, so fork and aside must replay it.
func TestForkAndAsideReplayLargeTranscriptLine(t *testing.T) {
	t.Parallel()
	fs := afero.NewMemMapFs()
	stateDir := "/state"
	parentID := "01PARENT00000000000000001"

	tw, err := transcript.NewWriterWithFS(fs, schema.SessionTranscriptPath(stateDir, parentID), transcript.Header{
		SessionID: parentID,
		CreatedAt: time.Now().UTC(),
		ProfileID: "openai",
		Model:     "gpt-5.2",
	})
	if err != nil {
		t.Fatalf("transcript.NewWriterWithFS: %v", err)
	}
	huge := strings.Repeat("x", 11<<20)
	for _, turn := range []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(huge)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("reply")),
		schema.NewTurn(schema.TurnUserInput, llm.User("second task")),
	} {
		if err := tw.Append(turn); err != nil {
			t.Fatalf("Append turn: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close transcript: %v", err)
	}
	if err := schema.SaveSessionMetaWithFS(fs, stateDir, schema.SessionMeta{
		ID:        parentID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    schema.ConfigSnapshot{MaxToolRoundsPerInput: 50},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		TurnCount: 2,
	}); err != nil {
		t.Fatalf("SaveSessionMetaWithFS: %v", err)
	}

	if _, err := forkSessionFS(fs, stateDir, parentID, 3, "edited", ""); err != nil {
		t.Fatalf("forkSessionFS: %v", err)
	}
	if _, err := asideSessionFSWithConfig(fs, stateDir, parentID, nil); err != nil {
		t.Fatalf("asideSessionFSWithConfig: %v", err)
	}
}

func TestRootForkReusesParentSystemPromptVerbatim(t *testing.T) {
	t.Parallel()
	const rootMarker = "PARENT_LAUNCH_PROMPT_9c17"
	// The child keeps the parent's launch prompt and environment baseline only
	// while it is the same role. Each case names what makes it a different one;
	// `reconfigure` is the --resume-with selection, which reaches the same writer.
	// The marker keeps every assertion off template prose.
	for _, tc := range []struct {
		name   string
		opts   []ForkOption
		resave func(*schema.SessionMeta)
		resume *schema.ConfigSnapshot
		shares bool
	}{
		{name: "root-to-root", shares: true},
		{name: "same-provider-model-override", opts: []ForkOption{WithForkModel("openai", "gpt-5.4")}, shares: true},
		{name: "manager", resave: func(m *schema.SessionMeta) { m.Manager, m.Config.Manager = true, true }},
		{name: "delegate", resave: func(m *schema.SessionMeta) { m.IsSubagent = true }},
		{name: "agent-reselected", resume: &schema.ConfigSnapshot{AgentName: "other-agent"}},
		{name: "plugins-reselected", resume: &schema.ConfigSnapshot{PluginDirs: []string{"/plugins/fresh"}}},
		{name: "cross-provider-model-override", opts: []ForkOption{WithForkModel("anthropic", "claude-opus-4-5")}, shares: false},
	} {
		stateDir := t.TempDir()
		parent := newSession(t, withStateDir(stateDir), withConfig(SessionConfig{MaxSubagentDepth: 2,
			ForceRealIO: true, UserInstructionOverride: rootMarker, testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true}}))
		parent.appendTurn(schema.TurnUserInput, llm.User("fork me"))
		parentID, launchPrompt, baseline := parent.ID(), parent.cachedSystemPrompt, parent.Meta().LaunchEnvBaseline
		if tc.resave != nil {
			parent.Close()
			meta, err := schema.LoadSessionMeta(stateDir, parentID)
			if err != nil {
				t.Fatalf("LoadSessionMeta(parent): %v", err)
			}
			tc.resave(&meta)
			if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
				t.Fatalf("SaveSessionMeta(parent): %v", err)
			}
		}
		childID, err := ForkSession(stateDir, parentID, 1, "forked", "", tc.opts...)
		if tc.resume != nil {
			childID, err = AsideSessionWithConfig(stateDir, parentID, *tc.resume)
		}
		if err != nil {
			t.Fatalf("%s: fork: %v", tc.name, err)
		}
		childMeta, err := schema.LoadSessionMeta(stateDir, childID)
		if err != nil {
			t.Fatalf("LoadSessionMeta: %v", err)
		}
		want, wantBaseline := launchPrompt, baseline
		if !tc.shares {
			want, wantBaseline = "", envctx.Snapshot{}
		}
		if hdr := sessionHeader(t, stateDir, childID); hdr.SystemPrompt != want {
			t.Fatalf("%s fork recorded %q, want %q", tc.name, hdr.SystemPrompt, want)
		} else if childMeta.LaunchEnvBaseline != wantBaseline {
			t.Fatalf("%s fork baseline = %+v, want %+v", tc.name, childMeta.LaunchEnvBaseline, wantBaseline)
		}
		if tc.shares && !strings.Contains(want, rootMarker) {
			t.Fatalf("setup: the inherited prompt does not carry the fixture marker:\n%s", want)
		}
	}
}

func sessionHeader(t *testing.T, stateDir, id string) transcript.Header {
	t.Helper()
	hdr, _, _, err := readTranscript(t.Context(), schema.SessionTranscriptPath(stateDir, id), stateDir)
	if err != nil {
		t.Fatalf("readTranscript(%s): %v", id, err)
	}
	return hdr
}

// rewriteLegacyForkHeader rewrites an existing transcript header on disk to
// simulate a pre-#6959 fork whose header copied parentHeader.SystemPrompt
// unconditionally while meta.LaunchSystemPrompt remained empty.
func rewriteLegacyForkHeader(t *testing.T, stateDir, childID string, legacyPrompt string) {
	t.Helper()
	tpath := schema.SessionTranscriptPath(stateDir, childID)
	hdr, entries, _, err := readTranscript(t.Context(), tpath, stateDir)
	if err != nil {
		t.Fatalf("readTranscript(%s): %v", childID, err)
	}
	hdr.SystemPrompt = legacyPrompt
	if err := os.Remove(tpath); err != nil {
		t.Fatalf("remove transcript: %v", err)
	}
	tw, err := transcript.NewWriter(tpath, hdr)
	if err != nil {
		t.Fatalf("transcript.NewWriter: %v", err)
	}
	for _, entry := range entries {
		if err := tw.Append(entry.Turn); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestLegacyMismatchedForkRestoreDiscardsParentPromptAndPersistsChildPrompt(t *testing.T) {
	t.Parallel()
	const parentMarker = "LEGACY_PARENT_PROMPT_MARKER_41a8"
	const childMarker = "LEGACY_CHILD_PROMPT_MARKER_9c3e"

	for _, tc := range []struct {
		name       string
		makeParent func(t *testing.T, stateDir string) (parentID, parentPrompt string, parentBaseline envctx.Snapshot)
		makeChild  func(t *testing.T, stateDir, parentID string) string
		checkChild func(t *testing.T, parentPrompt, childPrompt string)
	}{
		{
			name: "manager",
			makeParent: func(t *testing.T, stateDir string) (string, string, envctx.Snapshot) {
				parent := newSession(t, withStateDir(stateDir), withConfig(SessionConfig{
					MaxSubagentDepth: 2, Manager: true, ForceRealIO: true,
					UserInstructionOverride: parentMarker,
					testOnly:                testConfig{skipGitSnapshot: true, noSyncJobStore: true},
				}))
				parent.appendTurn(schema.TurnUserInput, llm.User("fork me"))
				pID, prompt, base := parent.ID(), parent.cachedSystemPrompt, parent.Meta().LaunchEnvBaseline
				parent.Close()
				return pID, prompt, base
			},
			makeChild: func(t *testing.T, stateDir, parentID string) string {
				childID, err := ForkSession(stateDir, parentID, 1, "forked from manager", "")
				if err != nil {
					t.Fatalf("ForkSession: %v", err)
				}
				return childID
			},
			checkChild: func(t *testing.T, parentPrompt, childPrompt string) {
				if childPrompt == parentPrompt {
					t.Fatalf("restored manager fork kept parent Manager prompt verbatim:\n%s", childPrompt)
				}
			},
		},
		{
			name: "resume-with-plugins-and-agent-changed",
			makeParent: func(t *testing.T, stateDir string) (string, string, envctx.Snapshot) {
				parent := newSession(t, withStateDir(stateDir), withConfig(SessionConfig{
					MaxSubagentDepth: 2, ForceRealIO: true,
					AgentName:               "default",
					PluginDirs:              []string{"/plugins/old"},
					UserInstructionOverride: parentMarker,
					testOnly:                testConfig{skipGitSnapshot: true, noSyncJobStore: true},
				}))
				parent.appendTurn(schema.TurnUserInput, llm.User("aside me"))
				pID, prompt, base := parent.ID(), parent.cachedSystemPrompt, parent.Meta().LaunchEnvBaseline
				parent.Close()
				return pID, prompt, base
			},
			makeChild: func(t *testing.T, stateDir, parentID string) string {
				cfg := (SessionConfig{
					MaxSubagentDepth:        2,
					AgentName:               "other-agent",
					PluginDirs:              []string{"/plugins/fresh"},
					UserInstructionOverride: childMarker,
				}).toSnapshot()
				childID, err := AsideSessionWithConfig(stateDir, parentID, cfg)
				if err != nil {
					t.Fatalf("AsideSessionWithConfig: %v", err)
				}
				return childID
			},
			checkChild: func(t *testing.T, _, childPrompt string) {
				if strings.Contains(childPrompt, parentMarker) || !strings.Contains(childPrompt, childMarker) {
					t.Fatalf("restored --resume-with fork kept parent prompt instead of child prompt:\n%s", childPrompt)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stateDir := t.TempDir()
			parentID, parentPrompt, parentBaseline := tc.makeParent(t, stateDir)
			childID := tc.makeChild(t, stateDir, parentID)

			// Plant the pre-#6959 legacy shape on disk: parent's prompt in child header,
			// empty LaunchSystemPrompt, and parent's LaunchEnvBaseline (with a marker model
			// to prove a mismatched parent baseline is ignored on recovery).
			rewriteLegacyForkHeader(t, stateDir, childID, parentPrompt)
			childMeta, err := schema.LoadSessionMeta(stateDir, childID)
			if err != nil {
				t.Fatalf("LoadSessionMeta(child): %v", err)
			}
			staleBaseline := parentBaseline
			staleBaseline.Model = "legacy-mismatched-parent-model"
			childMeta.LaunchSystemPrompt = ""
			childMeta.LaunchEnvBaseline = staleBaseline
			if err := schema.SaveSessionMeta(stateDir, childMeta); err != nil {
				t.Fatalf("SaveSessionMeta(child): %v", err)
			}

			// First restore: discards legacy parent header, renders child's own prompt,
			// persists LaunchSystemPrompt, and seeds child's own LaunchEnvBaseline.
			restored := restorePromptState(t, stateDir, childMeta, t.TempDir())
			childPrompt := restored.cachedSystemPrompt
			tc.checkChild(t, parentPrompt, childPrompt)
			if !restored.systemPromptFrozen {
				t.Fatal("recovered child prompt is not frozen")
			}
			savedMeta := restored.Meta()
			if savedMeta.LaunchSystemPrompt != childPrompt {
				t.Fatalf("LaunchSystemPrompt = %q, want %q", savedMeta.LaunchSystemPrompt, childPrompt)
			}
			if savedMeta.LaunchEnvBaseline.Model == staleBaseline.Model || savedMeta.LaunchEnvBaseline.Model == "" {
				t.Fatalf("LaunchEnvBaseline.Model = %q, want child's own baseline (not stale %q)", savedMeta.LaunchEnvBaseline.Model, staleBaseline.Model)
			}
			restored.Close()

			// Second restore: LaunchSystemPrompt is authoritative over the legacy header
			// that remains on disk, and stays frozen.
			reloadedMeta, err := schema.LoadSessionMeta(stateDir, childID)
			if err != nil {
				t.Fatalf("LoadSessionMeta(reloaded): %v", err)
			}
			restored2 := restorePromptState(t, stateDir, reloadedMeta, t.TempDir())
			if restored2.cachedSystemPrompt != childPrompt || !restored2.systemPromptFrozen {
				t.Fatalf("second restore did not freeze recovered child prompt verbatim:\ngot:\n%s\nwant:\n%s", restored2.cachedSystemPrompt, childPrompt)
			}
			if restored2.Meta().LaunchSystemPrompt != childPrompt {
				t.Fatalf("second restore cleared LaunchSystemPrompt: got %q, want %q", restored2.Meta().LaunchSystemPrompt, childPrompt)
			}
			restored2.Close()

			// Forking the recovered child inherits the recovered child prompt (from
			// LaunchSystemPrompt), NOT the legacy parent prompt still in child's header.
			grandchildID, err := ForkSession(stateDir, childID, 1, "grandchild", "")
			if err != nil {
				t.Fatalf("ForkSession(grandchild): %v", err)
			}
			if hdr := sessionHeader(t, stateDir, grandchildID); hdr.SystemPrompt != childPrompt {
				t.Fatalf("grandchild header prompt = %q, want recovered child prompt %q", hdr.SystemPrompt, childPrompt)
			}
			grandchildMeta, err := schema.LoadSessionMeta(stateDir, grandchildID)
			if err != nil {
				t.Fatalf("LoadSessionMeta(grandchild): %v", err)
			}
			if grandchildMeta.LaunchEnvBaseline != savedMeta.LaunchEnvBaseline {
				t.Fatalf("grandchild baseline = %+v, want %+v", grandchildMeta.LaunchEnvBaseline, savedMeta.LaunchEnvBaseline)
			}
		})
	}
}

func TestForkSession_WithForkModel(t *testing.T) {
	t.Parallel()
	t.Run("fork session overrides child profile and model", func(t *testing.T) {
		t.Parallel()
		stateDir, parentID := buildParentSession(t)

		childID, err := ForkSession(stateDir, parentID, 3, "second task, switched", "", WithForkModel("anthropic", "claude-opus-4-5"))
		if err != nil {
			t.Fatalf("ForkSession: %v", err)
		}
		childMeta, err := schema.LoadSessionMeta(stateDir, childID)
		if err != nil {
			t.Fatalf("LoadSessionMeta(child): %v", err)
		}
		if childMeta.ProfileID != "anthropic" || childMeta.Model != "claude-opus-4-5" {
			t.Fatalf("childMeta profile/model = (%q, %q), want (%q, %q)", childMeta.ProfileID, childMeta.Model, "anthropic", "claude-opus-4-5")
		}
		childHeader := sessionHeader(t, stateDir, childID)
		if childHeader.ProfileID != "anthropic" || childHeader.Model != "claude-opus-4-5" {
			t.Fatalf("childHeader profile/model = (%q, %q), want (%q, %q)", childHeader.ProfileID, childHeader.Model, "anthropic", "claude-opus-4-5")
		}
		parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
		if err != nil {
			t.Fatalf("LoadSessionMeta(parent): %v", err)
		}
		if parentMeta.ProfileID != "openai" || parentMeta.Model != "gpt-5.2" {
			t.Fatalf("parentMeta mutated: (%q, %q)", parentMeta.ProfileID, parentMeta.Model)
		}
	})

	t.Run("fork session at user turn overrides child profile and model", func(t *testing.T) {
		t.Parallel()
		stateDir, parentID := buildParentSession(t)

		childID, originalInput, err := ForkSessionAtUserTurn(stateDir, parentID, 3, "", WithForkModel("openai", "gpt-5.4"))
		if err != nil {
			t.Fatalf("ForkSessionAtUserTurn: %v", err)
		}
		if originalInput != "second task" {
			t.Fatalf("originalInput = %q, want %q", originalInput, "second task")
		}
		childMeta, err := schema.LoadSessionMeta(stateDir, childID)
		if err != nil {
			t.Fatalf("LoadSessionMeta(child): %v", err)
		}
		if childMeta.ProfileID != "openai" || childMeta.Model != "gpt-5.4" {
			t.Fatalf("childMeta profile/model = (%q, %q), want (%q, %q)", childMeta.ProfileID, childMeta.Model, "openai", "gpt-5.4")
		}
		childHeader := sessionHeader(t, stateDir, childID)
		if childHeader.ProfileID != "openai" || childHeader.Model != "gpt-5.4" {
			t.Fatalf("childHeader profile/model = (%q, %q), want (%q, %q)", childHeader.ProfileID, childHeader.Model, "openai", "gpt-5.4")
		}
	})

	t.Run("rejects profile without model before creating child", func(t *testing.T) {
		t.Parallel()
		stateDir, parentID := buildParentSession(t)
		before := sessionArtifactNames(t, stateDir)

		if _, err := ForkSession(stateDir, parentID, 3, "edited", "", WithForkModel("anthropic", "")); err == nil {
			t.Fatal("ForkSession accepted profileID without model")
		}
		if after := sessionArtifactNames(t, stateDir); after != before {
			t.Fatalf("artifacts changed on error:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
}

// A cross-provider fork regenerates its launch system prompt for the target
// provider on restore instead of keeping the parent's provider-specific prompt
// (including its surface project doc files).
func TestCrossProviderForkRegeneratesProviderPromptOnRestore(t *testing.T) {
	t.Parallel()
	const claudeDocSentinel = "ANTHROPIC_CLAUDE_DOC_SENTINEL_7a2c"
	const agentsDocSentinel = "SHARED_AGENTS_DOC_SENTINEL_4b19"
	stateDir := t.TempDir()
	workDir := t.TempDir()
	writeTestFile(t, filepath.Join(workDir, "AGENTS.md"), agentsDocSentinel)
	writeTestFile(t, filepath.Join(workDir, "CLAUDE.md"), claudeDocSentinel)

	parentClient := llm.NewClient()
	parentClient.Register(&fakeAdapter{name: "openai"})
	parent, err := NewSession(parentClient, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), SessionConfig{
		StateDir:    stateDir,
		ForceRealIO: true,
		testOnly:    testConfig{skipGitSnapshot: true, noSyncJobStore: true},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	parent.appendTurn(schema.TurnUserInput, llm.User("start"))
	parentID, parentPrompt := parent.ID(), parent.cachedSystemPrompt
	parent.Close()
	if strings.Contains(parentPrompt, claudeDocSentinel) {
		t.Fatalf("setup: OpenAI parent prompt unexpectedly included CLAUDE.md sentinel")
	}

	childID, err := ForkSession(stateDir, parentID, 1, "continue on anthropic", "", WithForkModel("anthropic", "claude-opus-4-5"))
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if hdr := sessionHeader(t, stateDir, childID); hdr.SystemPrompt != "" {
		t.Fatalf("cross-provider child header inherited SystemPrompt, want empty for regenerate on restore")
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, childID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.LaunchEnvBaseline != (envctx.Snapshot{}) {
		t.Fatalf("cross-provider child LaunchEnvBaseline = %+v, want zero", childMeta.LaunchEnvBaseline)
	}

	anthropicProfile, err := provider.Resolve(provider.EmbeddedRegistry(), "anthropic/claude-opus-4-5")
	if err != nil {
		t.Fatalf("provider.Resolve(anthropic): %v", err)
	}
	childClient := llm.NewClient()
	childClient.Register(&fakeAdapter{name: "anthropic"})
	restored, err := RestoreSessionFromMetaWithConfig(
		childClient,
		anthropicProfile,
		execenv.NewLocalExecutionEnvironment(workDir),
		childMeta,
		RestoreSessionConfig{
			StateDir: stateDir,
			testOnly: testConfig{skipGitSnapshot: true, noSyncJobStore: true},
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if !restored.systemPromptFrozen || restored.cachedSystemPrompt == parentPrompt {
		t.Fatalf("restored cross-provider fork did not render a fresh frozen prompt (frozen=%t)", restored.systemPromptFrozen)
	}
	if !strings.Contains(restored.cachedSystemPrompt, claudeDocSentinel) || !strings.Contains(restored.cachedSystemPrompt, agentsDocSentinel) {
		t.Fatalf("restored Anthropic fork prompt did not load CLAUDE.md + AGENTS.md:\n%s", restored.cachedSystemPrompt)
	}
	if saved := restored.Meta(); saved.LaunchSystemPrompt != restored.cachedSystemPrompt {
		t.Fatalf("restored child meta LaunchSystemPrompt did not persist regenerated prompt")
	}
}
