package hub

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/appwire/appwiretest"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

type forkingRelaySource struct {
	relayBroadcastSource
	forkCalled bool
	forkParams appwire.ThreadForkParams
	response   appwire.ThreadForkResponse
}

func (s *forkingRelaySource) ForkThread(_ context.Context, params appwire.ThreadForkParams) (appwire.ThreadForkResponse, error) {
	s.forkCalled = true
	s.forkParams = params
	return s.response, nil
}

func TestHubRPCThreadForkRoutesNonLocalCapableSource(t *testing.T) {
	source := &forkingRelaySource{
		id: "codex",
		thread: appwire.Thread{
			ID:        "th_fork",
			SessionID: "th_fork",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_fork", Capabilities: appwire.ThreadCapabilities{ForkFromTurn: true}},
		},
		notifications: make(chan appwire.Notification, 1),
		canceled:      make(chan struct{}, 1),
		response: appwire.ThreadForkResponse{Thread: appwire.Thread{
			ID:        "th_child",
			SessionID: "th_child",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_child"},
		}},
	}
	srv := newWebSourceTestServer(t, source)

	client := dialHubRPCInitialized(t, srv)
	resp, err := appwiretest.ThreadFork(context.Background(), client, appwire.ThreadForkParams{
		Ref:           "codex:th_fork",
		SourceItemKey: "codex-turn-1",
		Model:         "gpt-5-codex",
		ModelProvider: "openai",
	})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if !source.forkCalled {
		t.Fatal("non-local source ForkThread was not called")
	}
	if source.forkParams.SourceItemKey != "codex-turn-1" || source.forkParams.EditedInput != "" {
		t.Fatalf("fork params=%+v", source.forkParams)
	}
	if source.forkParams.Model != "gpt-5-codex" || source.forkParams.ModelProvider != "openai" {
		t.Fatalf("remote fork model passthrough = (%q, %q), want (%q, %q)", source.forkParams.ModelProvider, source.forkParams.Model, "openai", "gpt-5-codex")
	}
	if resp.Thread.Evener.Ref != "codex:th_child" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadForkRoutesNonLocalWholeThreadForkWithoutTurnForkCapability(t *testing.T) {
	source := &forkingRelaySource{
		id: "codex",
		thread: appwire.Thread{
			ID:        "th_whole_fork",
			SessionID: "th_whole_fork",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_whole_fork", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 1),
		canceled:      make(chan struct{}, 1),
		response: appwire.ThreadForkResponse{Thread: appwire.Thread{
			ID:        "th_whole_child",
			SessionID: "th_whole_child",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_whole_child"},
		}},
	}
	srv := newWebSourceTestServer(t, source)

	client := dialHubRPCInitialized(t, srv)
	resp, err := appwiretest.ThreadFork(context.Background(), client, appwire.ThreadForkParams{Ref: "codex:th_whole_fork"})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if !source.forkCalled {
		t.Fatal("whole-thread fork was not routed to source")
	}
	if source.forkParams.SourceItemKey != "" || source.forkParams.EditedInput != "" || source.forkParams.Label != "" {
		t.Fatalf("fork params=%+v", source.forkParams)
	}
	if resp.Thread.Evener.Ref != "codex:th_whole_child" {
		t.Fatalf("thread=%+v", resp.Thread)
	}
}

func TestHubRPCThreadForkReturnsUnavailableWhenNonLocalSourceCannotFork(t *testing.T) {
	source := &forkingRelaySource{
		id: "codex",
		thread: appwire.Thread{
			ID:        "th_no_fork",
			SessionID: "th_no_fork",
			Source:    "codex",
			Evener:    appwire.EvenerThread{Ref: "codex:th_no_fork", Capabilities: appwire.ThreadCapabilities{Send: true}},
		},
		notifications: make(chan appwire.Notification, 1),
		canceled:      make(chan struct{}, 1),
	}
	srv := newWebSourceTestServer(t, source)

	client := dialHubRPCInitialized(t, srv)
	err := client.Request(context.Background(), appwire.MethodThreadFork, appwire.ThreadForkParams{
		Ref:           "codex:th_no_fork",
		SourceItemKey: "codex-turn-1",
	}, &appwire.ThreadForkResponse{})
	if err == nil {
		t.Fatal("ThreadFork succeeded for source without fork capability")
	}
	if source.forkCalled {
		t.Fatal("fork reached source despite missing capability")
	}
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error %T does not preserve WireError: %v", err, err)
	}
	if wire.Code != appwire.CodeUnavailable {
		t.Fatalf("wire=%+v", wire)
	}
}

func TestHubRPCThreadForkCreatesForkedThread(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-fork-0000000000")
	parentID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := appwiretest.ThreadFork(context.Background(), client, appwire.ThreadForkParams{
		Ref:           "local:" + parentID,
		SourceItemKey: "apptranscript-item-v2:turn_3:2:0",
		EditedInput:   "second task, edited",
		Label:         "before edit",
	})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if resp.Thread.ID == "" || resp.Thread.ID == parentID || resp.Thread.Evener.Ref != "local:"+resp.Thread.ID {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.ParentSessionID != parentID || childMeta.DivergenceTurn != 3 {
		t.Fatalf("child meta=%+v", childMeta)
	}
}

// TestHubRPCThreadForkDeferInput verifies the fork-from-message flow (issue
// #42): deferInput forks the thread at the source turn WITHOUT appending a
// replacement message, so the child transcript holds only the prefix and the
// response carries the original input text for the client to stage in its
// composer. The forked session must not auto-run the message.
func TestHubRPCThreadForkDeferInput(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-fork-0000000000")
	parentID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := appwiretest.ThreadFork(context.Background(), client, appwire.ThreadForkParams{
		Ref:           "local:" + parentID,
		SourceItemKey: "apptranscript-item-v2:turn_3:2:0",
		DeferInput:    true,
	})
	if err != nil {
		t.Fatalf("ThreadFork: %v", err)
	}
	if resp.Thread.ID == "" || resp.Thread.ID == parentID || resp.Thread.Evener.Ref != "local:"+resp.Thread.ID {
		t.Fatalf("thread=%+v", resp.Thread)
	}
	if resp.OriginalInput != "second task" {
		t.Fatalf("OriginalInput=%q, want %q", resp.OriginalInput, "second task")
	}
	childMeta, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
	if err != nil {
		t.Fatalf("LoadSessionMeta(child): %v", err)
	}
	if childMeta.ParentSessionID != parentID || childMeta.DivergenceTurn != 3 {
		t.Fatalf("child meta=%+v", childMeta)
	}
	// The child transcript must contain only the prefix entries [U1, A1]:
	// no trailing USER_INPUT turn that would auto-run on open.
	raw, err := os.ReadFile(filepath.Join(stateDir, "sessions", resp.Thread.ID+".transcript.jsonl"))
	if err != nil {
		t.Fatalf("read child transcript: %v", err)
	}
	if strings.Contains(string(raw), "second task") {
		t.Fatalf("deferred fork must not copy the diverging user message:\n%s", raw)
	}
}

// TestHubRPCThreadForkDeferInputRejectsEditedInput verifies that deferInput
// and editedInput are mutually exclusive: one either replaces the message
// inline (editedInput) or hands it back for editing (deferInput), never both.
func TestHubRPCThreadForkDeferInputRejectsEditedInput(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-fork-0000000000")
	parentID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	err := client.Request(context.Background(), appwire.MethodThreadFork, appwire.ThreadForkParams{
		Ref:           "local:" + parentID,
		SourceItemKey: "apptranscript-item-v2:turn_3:2:0",
		EditedInput:   "second task, edited",
		DeferInput:    true,
	}, &appwire.ThreadForkResponse{})
	if err == nil {
		t.Fatal("ThreadFork with both editedInput and deferInput should fail")
	}
	requireWireError(t, err, appwire.CodeInvalidParams)
}

func readForkTranscriptHeader(t *testing.T, path string) transcript.Header {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open child transcript: %v", err)
	}
	defer f.Close()
	hdr, err := transcript.ReadHeader(t.Context(), bufio.NewReader(f), transcript.DefaultMaxLineBytes)
	if err != nil {
		t.Fatalf("ReadHeader(child): %v", err)
	}
	return hdr
}

func TestHubRPCThreadForkModelOverride(t *testing.T) {
	for _, tc := range []struct {
		name              string
		params            appwire.ThreadForkParams
		wantProfile       string
		wantModel         string
		wantOriginalInput string
	}{
		{
			name: "provider and model",
			params: appwire.ThreadForkParams{
				SourceItemKey: "apptranscript-item-v2:turn_3:2:0",
				EditedInput:   "second task, edited",
				ModelProvider: "anthropic",
				Model:         "claude-opus-4-6",
			},
			wantProfile: "anthropic",
			wantModel:   "claude-opus-4-6",
		},
		{
			name: "qualified model with whitespace",
			params: appwire.ThreadForkParams{
				SourceItemKey: "apptranscript-item-v2:turn_3:2:0",
				EditedInput:   "second task, edited",
				Model:         "  Anthropic/claude-opus-4-6  ",
			},
			wantProfile: "anthropic",
			wantModel:   "claude-opus-4-6",
		},
		{
			name: "provider with slash-containing model id",
			params: appwire.ThreadForkParams{
				SourceItemKey: "apptranscript-item-v2:turn_3:2:0",
				EditedInput:   "second task, edited",
				ModelProvider: "openrouter",
				Model:         "deepseek/deepseek-v4-flash",
			},
			wantProfile: "openrouter",
			wantModel:   "deepseek/deepseek-v4-flash",
		},
		{
			name: "deferInput with model override",
			params: appwire.ThreadForkParams{
				SourceItemKey: "apptranscript-item-v2:turn_3:2:0",
				DeferInput:    true,
				ModelProvider: "anthropic",
				Model:         "claude-opus-4-6",
			},
			wantProfile:       "anthropic",
			wantModel:         "claude-opus-4-6",
			wantOriginalInput: "second task",
		},
		{
			name: "default empty inherits parent",
			params: appwire.ThreadForkParams{
				SourceItemKey: "apptranscript-item-v2:turn_3:2:0",
				EditedInput:   "second task, edited",
			},
			wantProfile: "openai",
			wantModel:   "gpt-5",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			stateDir := filepath.Join(root, "projects", "project-fork-0000000000")
			parentID := buildRPCParentSession(t, stateDir)
			past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))

			hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
			defer hub.Close()
			client := dialHubRPCInitialized(t, hub)

			params := tc.params
			params.Ref = "local:" + parentID
			resp, err := appwiretest.ThreadFork(context.Background(), client, params)
			if err != nil {
				t.Fatalf("ThreadFork: %v", err)
			}
			if resp.OriginalInput != tc.wantOriginalInput {
				t.Fatalf("OriginalInput = %q, want %q", resp.OriginalInput, tc.wantOriginalInput)
			}
			if resp.Thread.ModelProvider != tc.wantModel || resp.Thread.Evener.Profile != tc.wantProfile {
				t.Fatalf("response thread model/profile = (%q, %q), want (%q, %q)", resp.Thread.ModelProvider, resp.Thread.Evener.Profile, tc.wantModel, tc.wantProfile)
			}

			childMeta, err := schema.LoadSessionMeta(stateDir, resp.Thread.ID)
			if err != nil {
				t.Fatalf("LoadSessionMeta(child): %v", err)
			}
			if childMeta.ProfileID != tc.wantProfile || childMeta.Model != tc.wantModel {
				t.Fatalf("child meta profile/model = (%q, %q), want (%q, %q)", childMeta.ProfileID, childMeta.Model, tc.wantProfile, tc.wantModel)
			}
			childHeader := readForkTranscriptHeader(t, filepath.Join(stateDir, "sessions", resp.Thread.ID+".transcript.jsonl"))
			if childHeader.ProfileID != tc.wantProfile || childHeader.Model != tc.wantModel {
				t.Fatalf("child header profile/model = (%q, %q), want (%q, %q)", childHeader.ProfileID, childHeader.Model, tc.wantProfile, tc.wantModel)
			}

			parentMeta, err := schema.LoadSessionMeta(stateDir, parentID)
			if err != nil {
				t.Fatalf("LoadSessionMeta(parent): %v", err)
			}
			if parentMeta.ProfileID != "openai" || parentMeta.Model != "gpt-5" {
				t.Fatalf("parent meta mutated: (%q, %q)", parentMeta.ProfileID, parentMeta.Model)
			}
		})
	}
}
