package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestClientMutationQueueEditingLocationsRestore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic"}
	s := newSkillSelectionDiskSession(t, root, adapter)
	id := s.ID()
	seen, stop := captureEvents(s)
	var params appwire.TurnQueueParams
	if err := json.Unmarshal([]byte(`{"clientMutationId":"locations-318","input":[{"type":"text","text":"/same /same /same","mentions":[{"kind":"command","name":"same","offset":0},{"kind":"skill","name":"same","offset":6}]},{"type":"command","name":"same"},{"type":"skill","name":"same"}]}`), &params); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AcceptClientMutationQueue(params); err != nil {
		t.Fatal(err)
	}
	stop()
	var pushed *events.QueueChangedData
	for _, event := range *seen {
		if event.Kind == events.EventQueueChanged {
			data := event.Data.(events.QueueChangedData)
			if data.Depth == 1 {
				pushed = &data
			}
		}
	}
	if pushed == nil || !reflect.DeepEqual(pushed.Mentions, [][]appwire.InputMention{params.Input[0].Mentions}) {
		t.Fatalf("accepted queue push lost editing locations: %+v", pushed)
	}
	restored := restoreSkillSelectionDiskSession(t, root, id, adapter)
	t.Cleanup(restored.Close)
	queue, pending := restored.ClientMutationProjection()
	for label, value := range map[string]any{"queue": queue, "acceptedInput": pending[0].Input[0]} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		var mentions any
		if err := json.Unmarshal(fields["mentions"], &mentions); err != nil {
			t.Fatalf("%s dropped authoritative mentions: %s (%v)", label, data, err)
		}
		want := `[{"kind":"command","name":"same","offset":0},{"kind":"skill","name":"same","offset":6}]`
		if label == "queue" {
			want = "[" + want + "]"
		}
		var expected any
		if err := json.Unmarshal([]byte(want), &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(mentions, expected) {
			t.Fatalf("%s mentions = %v, want %v", label, mentions, expected)
		}
	}
}

func TestClientMutationCommandSelection(t *testing.T) {
	t.Parallel()
	for _, route := range []string{"start", "queue", "steer", "drain"} {
		t.Run(route, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			const sentinel = "BODY_318[]|[]"
			pluginDir := writePluginCommand(t, "pkg", "probe", "BODY_318[$ARGUMENTS]|[$1] !`printf x >> invoked; printf /pkg:nested`")
			if err := os.WriteFile(filepath.Join(pluginDir, "commands", "nested.md"), []byte("!`printf x >> nested-invoked`"), 0o600); err != nil {
				t.Fatal(err)
			}
			writeEvenerwideCommandFile(t, root, "inert", "!`printf x >> inert-invoked` @missing [$ARGUMENTS][$1]")
			writeSkillMD(t, root, "inert", "---\nname: inert\ndescription: fixture\n---\nSKILL_318")
			adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
				return toolCallResponse(communicateCall("done-1", "ok"))
			}}
			s := newSession(t, withAdapter(adapter), withDir(root), withProfile(newAnthropicProfile("claude-test")), withConfig(SessionConfig{PluginDirs: []string{pluginDir}}), withoutGitSnapshot())
			_, stop := captureEvents(s)
			defer stop()
			const original = "REQUEST_318 /pkg:probe tail /nested"
			input := []appwire.InputItem{{Type: "text", Text: original}, {Type: "command", Name: "pkg:probe"}, {Type: "command", Name: "pkg:probe"}, {Type: "command", Name: "inert"}, {Type: "skill", Name: "inert"}}
			var err error
			switch route {
			case "start":
				_, err = s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "command-start", Input: input})
				if err == nil {
					_, _, err = s.ProcessClientMutationStart(context.Background(), nil)
				}
			case "queue":
				_, err = s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "command-queue", Input: input})
				if err == nil {
					_, _, err = s.ProcessPendingUserInput(context.Background(), nil)
				}
			case "steer":
				_, err = s.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "command-steer", Input: input})
				if err == nil {
					_, err = s.ProcessInput(context.Background(), "CARRIER_318", nil)
				}
			case "drain":
				_, err = s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "command-queue", Input: input})
				if err == nil {
					queue, _ := s.ClientMutationProjection()
					_, err = s.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{ClientMutationID: "command-drain", ExpectedQueueRevision: queue.Revision})
				}
				if err == nil {
					_, err = s.ProcessInput(context.Background(), "CARRIER_318", nil)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			invoked, err := os.ReadFile(filepath.Join(root, "invoked"))
			if err != nil || string(invoked) != "x" {
				t.Fatalf("plugin invocation count: bytes=%q err=%v", invoked, err)
			}
			if _, err := os.Stat(filepath.Join(root, "inert-invoked")); !os.IsNotExist(err) {
				t.Fatalf("discovered command executed: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "nested-invoked")); !os.IsNotExist(err) {
				t.Fatalf("generated output invoked another command: %v", err)
			}
			requests := adapter.Requests()
			if len(requests) != 1 {
				t.Fatalf("requests=%d", len(requests))
			}
			texts := userMessageTexts(requests[0])
			data := strings.Join(texts, "\n")
			if strings.Count(data, original) != 1 {
				t.Fatalf("original data missing or duplicated: %q", texts)
			}
			// Opaque fixture output proves empty args and body transport. It is
			// command data, not an assertion on assembled prompt instructions.
			if strings.Count(data, sentinel+" /pkg:nested") != 1 {
				t.Fatalf("empty-argument fixture output missing: %q", texts)
			}
			if envelopes := requestSkillEnvelopes(t, requests[0]); len(envelopes) != 1 || envelopes[0].Doc.Name != "inert" {
				t.Fatalf("same-spelling skill lost its intent: %+v", envelopes)
			}
		})
	}
}

func TestClientMutationCommandSelectionUnavailableThenRecovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pluginDir := writePluginCommand(t, "pkg", "probe", "BODY_RECOVERY_318[$ARGUMENTS]")
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	s := newSession(t, withAdapter(adapter), withDir(root), withProfile(newAnthropicProfile("claude-test")), withConfig(SessionConfig{PluginDirs: []string{pluginDir}}), withoutGitSnapshot())
	_, stop := captureEvents(s)
	defer stop()
	for index, name := range []string{"probe", "pkg:probe"} {
		id := []string{"unavailable-318", "recovered-318"}[index]
		if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: id, Input: []appwire.InputItem{{Type: "command", Name: name}}}); err != nil {
			t.Fatal(err)
		}
		if _, ran, err := s.ProcessClientMutationStart(context.Background(), nil); err != nil || !ran {
			t.Fatalf("process: ran=%v err=%v", ran, err)
		}
		turn := findClientMutationTurn(s, id, schema.TurnUserInput)
		if turn == nil || turn.CommandInput == nil || !slices.Equal(turn.CommandInput.Names, []string{name}) || turn.SkillState != nil {
			t.Fatalf("command intent lost or disguised as skill: %+v", turn)
		}
		if len(adapter.Requests()) != index {
			t.Fatalf("unavailable exact identity dispatched or recovery failed: requests=%d", len(adapter.Requests()))
		}
	}
}

func TestClientMutationCommandSelectionQueueRestore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeEvenerwideCommandFile(t, root, "probe", "BODY_RESTORE_318[$ARGUMENTS]")
	adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
		return toolCallResponse(communicateCall("done-1", "ok"))
	}}
	s := newSkillSelectionDiskSession(t, root, adapter)
	id := s.ID()
	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "restore-318", Input: []appwire.InputItem{{Type: "command", Name: "probe"}}}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	restored := restoreSkillSelectionDiskSession(t, root, id, adapter)
	t.Cleanup(restored.Close)
	queue, _ := restored.ClientMutationProjection()
	if len(queue.CommandNames) != 1 || !slices.Equal(queue.CommandNames[0], []string{"probe"}) {
		t.Fatalf("restored queue intent: %+v", queue)
	}
	_, stop := captureEvents(restored)
	defer stop()
	if _, ran, err := restored.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
		t.Fatalf("restored consumption: ran=%v err=%v", ran, err)
	}
	turn := findClientMutationTurn(restored, "restore-318", schema.TurnUserInput)
	if turn == nil || turn.CommandInput == nil || turn.CommandInput.OriginalText != "" || !slices.Equal(turn.CommandInput.Names, []string{"probe"}) {
		t.Fatalf("restored command intent: %+v", turn)
	}
	if !strings.Contains(turn.Message.Text(), "BODY_RESTORE_318[]") {
		t.Fatalf("restored body data missing: %+v", turn.Message)
	}
}

func TestClientMutationCommandPreparationAdmissionRetry(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"environment", "user"} {
		for _, retry := range []string{"queue", "restore", "promote", "drain"} {
			t.Run(boundary+"/"+retry, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				pluginDir := writePluginCommand(t, "pkg", "probe", "BODY_PINNED_318 !`printf x >> invoked; printf OUTPUT_PINNED_318`")
				adapter := &agenttest.ScriptedAdapter{Provider: "anthropic", Responder: func(llm.Request) llm.Response {
					return toolCallResponse(communicateCall("done-1", "ok"))
				}}
				s := newSession(t, withAdapter(adapter), withDir(root), withProfile(newAnthropicProfile("claude-test")), withConfig(SessionConfig{StateDir: root, PluginDirs: []string{pluginDir}}), withoutGitSnapshot())
				_, stop := captureEvents(s)
				defer stop()
				const original = "ORIGINAL_RETRY_318 /pkg:probe prose"
				if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "retry-318", Input: []appwire.InputItem{{Type: "text", Text: original}, {Type: "command", Name: "pkg:probe"}, {Type: "command", Name: "pkg:probe"}}}); err != nil {
					t.Fatal(err)
				}
				if boundary == "user" {
					if err := s.maybeAppendEnvironmentContext(); err != nil {
						t.Fatal(err)
					}
				}
				fs := attachEnvironmentFailureFS(t, s)
				failure := errors.New("zero-byte " + boundary + " admission failure")
				fs.mu.Lock()
				fs.writeFailure = failure
				fs.mu.Unlock()
				if _, _, err := s.ProcessPendingUserInput(context.Background(), nil); !errors.Is(err, failure) {
					t.Fatalf("first attempt = %v, want %v", err, failure)
				}
				if invoked, err := os.ReadFile(filepath.Join(root, "invoked")); err != nil || string(invoked) != "x" || len(adapter.Requests()) != 0 {
					t.Fatalf("failed admission: invoked=%q err=%v requests=%d", invoked, err, len(adapter.Requests()))
				}
				owner := "retry-318"
				switch retry {
				case "restore":
					id := s.ID()
					stop()
					s.Close()
					meta, err := schema.LoadSessionMeta(root, id)
					if err != nil {
						t.Fatal(err)
					}
					client := llm.NewClient()
					client.Register(adapter)
					s, err = RestoreSessionFromMetaWithConfig(client, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), meta, RestoreSessionConfig{StateDir: root})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(s.Close)
					_, stopRestored := captureEvents(s)
					defer stopRestored()
				case "promote":
					owner = "promoted-318"
					if _, err := s.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: owner, Index: 0}); err != nil {
						t.Fatal(err)
					}
				case "drain":
					owner = "drained-318"
					queue, _ := s.ClientMutationProjection()
					if _, err := s.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{ClientMutationID: owner, ExpectedQueueRevision: queue.Revision}); err != nil {
						t.Fatal(err)
					}
				}
				// The accepted input owns the prepared bytes, not a fresh expansion
				// of whatever the same catalog key happens to contain on retry.
				delete(s.pluginCommands, "pkg:probe")
				if retry == "promote" || retry == "drain" {
					if _, err := s.ProcessInput(context.Background(), "CARRIER_RETRY_318", nil); err != nil {
						t.Fatal(err)
					}
				} else if _, ran, err := s.ProcessPendingUserInput(context.Background(), nil); err != nil || !ran {
					t.Fatalf("retry: ran=%v err=%v", ran, err)
				}
				if invoked, err := os.ReadFile(filepath.Join(root, "invoked")); err != nil || string(invoked) != "x" {
					t.Fatalf("retry executed again: invoked=%q err=%v", invoked, err)
				}
				kind := schema.TurnUserInput
				if retry == "promote" || retry == "drain" {
					kind = schema.TurnSteering
				}
				turn := findClientMutationTurn(s, owner, kind)
				if turn == nil || turn.CommandInput == nil || turn.CommandInput.OriginalText != original || strings.Count(turn.Message.Text(), "OUTPUT_PINNED_318") != 1 {
					t.Fatalf("prepared bytes/original prompt lost after retry: %+v", turn)
				}
			})
		}
	}
}
