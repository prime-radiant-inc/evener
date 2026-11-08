package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/modelavailability"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

type numberHookClient struct {
	managerQuestionClient
	intents []string
}

func (c *numberHookClient) Invoke(ctx context.Context, request appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	c.intents = append(c.intents, tool.IntentFromContext(ctx))
	return c.managerQuestionClient.Invoke(ctx, request)
}

// The hook is a real command process. It records stdin without interpreting
// numbers and emits the supplied patch unchanged.
func numberHookPlugin(t *testing.T, updated map[string]any) (string, string) {
	t.Helper()
	output := numberHookJSON(t, map[string]any{"hookSpecificOutput": map[string]any{
		"hookEventName": "PreToolUse", "permissionDecision": "allow", "updatedInput": updated,
	}})
	return numberHookOutputPlugin(t, output)
}

func numberHookOutputPlugin(t *testing.T, output string) (string, string) {
	t.Helper()
	capture := filepath.Join(t.TempDir(), "hook-input.json")
	command := fmt.Sprintf("cat > %q && printf '%%s' '%s'", capture, output)
	raw, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{
		map[string]any{"matcher": "*", "hooks": []any{map[string]any{"type": "command", "command": command}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return writePluginHooks(t, "number-hook", string(raw)), capture
}

func numberHookJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func numberHookView(t *testing.T, capture, callID string) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("real hook did not capture stdin: %v", err)
	}
	var input struct {
		Event string          `json:"hook_event_name"`
		ID    string          `json:"tool_use_id"`
		Input json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	if input.Event != "PreToolUse" || input.ID != callID {
		t.Fatalf("hook identity = %+v", input)
	}
	return input.Input
}

func TestPreToolHookManagerSendNumbers(t *testing.T) {
	t.Parallel()
	pluginDir, capture := numberHookPlugin(t, map[string]any{"intent": "hook-intent-sentinel"})
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) {
		cfg.Manager = true
		cfg.PluginDirs = []string{pluginDir}
	})
	client := &numberHookClient{}
	root.SetHubClient(client)
	for _, mode := range []string{"start", "queue", "steer"} {
		for _, revision := range []*uint64{nil, new(uint64(0)), new(uint64(1<<53 + 1)), new(^uint64(0))} {
			name := "absent"
			if revision != nil {
				name = strconv.FormatUint(*revision, 10)
			}
			t.Run(mode+"/"+name, func(t *testing.T) {
				args := map[string]any{"to": "local:worker", "input_mode": mode, "message": "answer-sentinel", "client_mutation_id": "mutation-sentinel", "expected_instance_id": "instance-sentinel"}
				boot := ""
				if revision != nil {
					args["expected_ask_revision"] = *revision
					boot = "boot-sentinel"
					args["expected_ask_boot_generation"] = boot
				}
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				call := llm.ToolCallData{ID: mode + "-" + name, Name: "delegate_send", Arguments: raw}
				before := len(client.requests)
				result := root.execTool(t.Context(), call, "")
				var view struct {
					Revision *uint64 `json:"expected_ask_revision"`
					Boot     string  `json:"expected_ask_boot_generation"`
				}
				if err := json.Unmarshal(numberHookView(t, capture, call.ID), &view); err != nil {
					t.Errorf("hook revision is not uint64: %v", err)
				} else if !reflect.DeepEqual(view.Revision, revision) || view.Boot != boot {
					t.Errorf("hook ask identity = %s, want revision=%s boot=%q", numberHookJSON(t, view), numberHookJSON(t, revision), boot)
				}
				want := appwire.SessionOperationRequest{}
				input := []appwire.InputItem{{Type: "text", Text: "answer-sentinel"}}
				switch mode {
				case "start":
					want.Start = &appwire.TurnStartParams{Ref: "local:worker", Input: input, ClientMutationID: "mutation-sentinel", ExpectedInstanceID: "instance-sentinel", ExpectedAskRevision: revision, ExpectedAskBootGeneration: boot}
				case "queue":
					want.Queue = &appwire.TurnQueueParams{Ref: "local:worker", Input: input, ClientMutationID: "mutation-sentinel", ExpectedInstanceID: "instance-sentinel", ExpectedAskRevision: revision, ExpectedAskBootGeneration: boot}
				case "steer":
					want.Steer = &appwire.TurnSteerParams{Ref: "local:worker", Input: input, ClientMutationID: "mutation-sentinel", ExpectedInstanceID: "instance-sentinel", ExpectedAskRevision: revision, ExpectedAskBootGeneration: boot}
				}
				if result.IsError || len(client.requests) != before+1 || !reflect.DeepEqual(client.requests[before], want) {
					t.Errorf("hook-mutated send lost exact forwarding: result=%+v requests=%s want=%s", result, numberHookJSON(t, client.requests[before:]), numberHookJSON(t, want))
				} else if client.intents[before] != "hook-intent-sentinel" {
					t.Errorf("Manager send did not receive the hook mutation: intent=%q", client.intents[before])
				}
			})
		}
	}
}

func TestPreToolHookManagerPageNumbers(t *testing.T) {
	t.Parallel()
	pluginDir, capture := numberHookPlugin(t, map[string]any{"intent": "hook-intent-sentinel"})
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) {
		cfg.Manager = true
		cfg.PluginDirs = []string{pluginDir}
	})
	client := &numberHookClient{}
	root.SetHubClient(client)
	maxInt := int(^uint(0) >> 1)
	for _, revision := range []uint64{0, 1<<53 + 1, ^uint64(0)} {
		for _, bounds := range []struct {
			name          string
			offset, limit *int
		}{
			{"absent", nil, nil},
			{"zero", new(0), new(0)},
			{"signed", new(-1), new(3)},
			{"signed_extremes", new(maxInt), new(-maxInt - 1)},
		} {
			t.Run(strconv.FormatUint(revision, 10)+"/"+bounds.name, func(t *testing.T) {
				hub := map[string]any{"ref": "local:worker", "expected_ask_revision": revision, "expected_ask_boot_generation": "boot-sentinel"}
				if bounds.offset != nil {
					hub["offset"], hub["limit"] = *bounds.offset, *bounds.limit
				}
				raw, err := json.Marshal(map[string]any{"hub": hub})
				if err != nil {
					t.Fatal(err)
				}
				call := llm.ToolCallData{ID: "page-" + strconv.FormatUint(revision, 10) + "-" + bounds.name, Name: "job_status", Arguments: raw}
				before := len(client.requests)
				result := root.execTool(t.Context(), call, "")
				var view struct {
					Hub struct {
						Revision *uint64 `json:"expected_ask_revision"`
						Boot     string  `json:"expected_ask_boot_generation"`
						Offset   *int    `json:"offset"`
						Limit    *int    `json:"limit"`
					} `json:"hub"`
				}
				if err := json.Unmarshal(numberHookView(t, capture, call.ID), &view); err != nil {
					t.Errorf("hook page identity or bounds are not representable: %v", err)
				} else if !reflect.DeepEqual(view.Hub.Revision, &revision) || view.Hub.Boot != "boot-sentinel" || !reflect.DeepEqual(view.Hub.Offset, bounds.offset) || !reflect.DeepEqual(view.Hub.Limit, bounds.limit) {
					t.Errorf("hook page = %s, want revision=%d offset=%s limit=%s", numberHookJSON(t, view.Hub), revision, numberHookJSON(t, bounds.offset), numberHookJSON(t, bounds.limit))
				}
				want := appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: "local:worker", ExpectedAskRevision: &revision, ExpectedAskBootGeneration: "boot-sentinel", Offset: bounds.offset, Limit: bounds.limit}}
				if result.IsError || len(client.requests) != before+1 || !reflect.DeepEqual(client.requests[before], want) {
					t.Errorf("hook-mutated page lost exact forwarding: result=%+v requests=%s want=%s", result, numberHookJSON(t, client.requests[before:]), numberHookJSON(t, want))
				} else if client.intents[before] != "hook-intent-sentinel" {
					t.Errorf("Manager page did not receive the hook mutation: intent=%q", client.intents[before])
				}
			})
		}
	}
}

func TestPreToolHookOrdinaryNumbersStayFloat64(t *testing.T) {
	t.Parallel()
	pluginDir, capture := numberHookPlugin(t, map[string]any{"intent": "hook-intent-sentinel"})
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.PluginDirs = []string{pluginDir} })
	snapshot := modelavailability.Capture(t.Context(), []string{"provider"}, "provider", func(context.Context, string) ([]string, error) {
		return []string{"a", "b"}, nil
	}, 30*time.Second)
	root.modelSnapshot = &snapshot
	call := llm.ToolCallData{ID: "ordinary-model-bound", Name: "model_list", Arguments: []byte(`{"max_count":1}`)}
	result := root.execTool(t.Context(), call, "")
	var page modelavailability.Page
	if err := json.Unmarshal([]byte(result.Output), &page); err != nil || result.IsError {
		t.Fatalf("ordinary model-list result=%+v: %v", result, err)
	}
	if !reflect.DeepEqual(page.Choices, []string{"provider/a"}) || page.Next == "" || page.Terminal {
		t.Errorf("ordinary float-dependent max_count was not applied: %+v", page)
	}
	var view map[string]any
	if err := json.Unmarshal(numberHookView(t, capture, call.ID), &view); err != nil || view["max_count"] != float64(1) {
		t.Fatalf("ordinary hook view=%#v: %v", view, err)
	}
	var forwarded map[string]any
	var intent string
	root.RegisterTool("number_probe", "records numeric arguments", map[string]any{"type": "object", "properties": map[string]any{
		"integer": map[string]any{"type": "number"}, "fraction": map[string]any{"type": "number"},
	}}, func(ctx context.Context, args any) (any, error) {
		forwarded = args.(map[string]any)
		intent = tool.IntentFromContext(ctx)
		return nil, nil
	})
	call = llm.ToolCallData{ID: "ordinary-number-types", Name: "number_probe", Arguments: []byte(`{"integer":9007199254740993,"fraction":1.25}`)}
	result = root.execTool(t.Context(), call, "")
	if result.IsError || forwarded["integer"] != float64(9007199254740992) || forwarded["fraction"] != float64(1.25) || intent != "hook-intent-sentinel" {
		t.Errorf("ordinary hook-mutated types/values changed: result=%+v args=%#v intent=%q", result, forwarded, intent)
	}
	view = nil
	if err := json.Unmarshal(numberHookView(t, capture, call.ID), &view); err != nil || view["integer"] != float64(9007199254740992) || view["fraction"] != float64(1.25) {
		t.Errorf("ordinary hook view=%#v: %v", view, err)
	}
	t.Run("numeric_replacements", func(t *testing.T) {
		pluginDir, capture := numberHookPlugin(t, map[string]any{
			"intent": "hook-intent-sentinel", "integer": json.Number("9007199254740993"), "fraction": 2.5,
		})
		root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.PluginDirs = []string{pluginDir} })
		var forwarded map[string]any
		root.RegisterTool("number_probe", "records numeric arguments", map[string]any{"type": "object", "properties": map[string]any{
			"integer": map[string]any{"type": "number"}, "fraction": map[string]any{"type": "number"},
		}}, func(_ context.Context, args any) (any, error) {
			forwarded = args.(map[string]any)
			return nil, nil
		})
		call := llm.ToolCallData{ID: "ordinary-numeric-replacements", Name: "number_probe", Arguments: []byte(`{"integer":0,"fraction":1.25}`)}
		result := root.execTool(t.Context(), call, "")
		if result.IsError || forwarded["integer"] != float64(9007199254740992) || forwarded["fraction"] != float64(2.5) {
			t.Errorf("ordinary numeric replacement decoding changed: result=%+v args=%#v", result, forwarded)
		}
		var view map[string]any
		if err := json.Unmarshal(numberHookView(t, capture, call.ID), &view); err != nil || view["integer"] != float64(0) || view["fraction"] != float64(1.25) {
			t.Errorf("ordinary hook did not receive original input: %#v, %v", view, err)
		}
	})
}

func TestPreToolHookManagerNumberReplacements(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"delegate_send", "job_status"} {
		for _, revision := range []json.Number{"0", "9007199254740993", "18446744073709551615", "18446744073709551616", "1e400", "-1e400", "1e-400", "-1", "1.5", "9007199254740993.5"} {
			t.Run(name+"/"+revision.String(), func(t *testing.T) {
				pair := map[string]any{"expected_ask_revision": revision, "expected_ask_boot_generation": "boot-sentinel"}
				patch := map[string]any{"intent": "hook-intent-sentinel"}
				args := map[string]any{"to": "local:worker", "input_mode": "queue", "message": "answer-sentinel", "client_mutation_id": "mutation-sentinel", "expected_instance_id": "instance-sentinel", "expected_ask_revision": 1, "expected_ask_boot_generation": "boot-sentinel"}
				maxInt := int(^uint(0) >> 1)
				if name == "job_status" {
					pair["ref"], pair["offset"], pair["limit"] = "local:worker", maxInt, -maxInt-1
					patch["hub"] = pair
					args = map[string]any{"hub": map[string]any{"ref": "local:worker", "expected_ask_revision": 1, "expected_ask_boot_generation": "boot-sentinel"}}
				} else {
					patch["expected_ask_revision"] = revision
				}
				pluginDir, capture := numberHookPlugin(t, patch)
				root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) {
					cfg.Manager = true
					cfg.PluginDirs = []string{pluginDir}
				})
				client := &numberHookClient{}
				root.SetHubClient(client)
				call := llm.ToolCallData{ID: "numeric-replacement", Name: name, Arguments: []byte(numberHookJSON(t, args))}
				result := root.execTool(t.Context(), call, "")
				// The hook saw a valid, distinct original revision. The revision
				// below can only reach the hub by parsing the hook's real stdout.
				var view struct {
					Revision uint64 `json:"expected_ask_revision"`
					Hub      struct {
						Revision uint64 `json:"expected_ask_revision"`
					} `json:"hub"`
				}
				if err := json.Unmarshal(numberHookView(t, capture, call.ID), &view); err != nil || (name == "delegate_send" && view.Revision != 1) || (name == "job_status" && view.Hub.Revision != 1) {
					t.Fatalf("hook did not receive original arguments: %+v, %v", view, err)
				}
				wantRevision, err := strconv.ParseUint(revision.String(), 10, 64)
				if err != nil {
					if !result.IsError || len(client.requests) != 0 {
						t.Errorf("invalid hook replacement was forwarded: result=%+v requests=%s", result, numberHookJSON(t, client.requests))
					}
					return
				}
				want := appwire.SessionOperationRequest{}
				if name == "job_status" {
					want.Read = &appwire.ThreadReadParams{Ref: "local:worker", ExpectedAskRevision: &wantRevision, ExpectedAskBootGeneration: "boot-sentinel", Offset: new(maxInt), Limit: new(-maxInt - 1)}
				} else {
					want.Queue = &appwire.TurnQueueParams{Ref: "local:worker", Input: []appwire.InputItem{{Type: "text", Text: "answer-sentinel"}}, ClientMutationID: "mutation-sentinel", ExpectedInstanceID: "instance-sentinel", ExpectedAskRevision: &wantRevision, ExpectedAskBootGeneration: "boot-sentinel"}
				}
				if result.IsError || len(client.requests) != 1 || !reflect.DeepEqual(client.requests[0], want) {
					t.Errorf("hook numeric replacement lost exact forwarding: result=%+v requests=%s want=%s", result, numberHookJSON(t, client.requests), numberHookJSON(t, want))
				} else if client.intents[0] != "hook-intent-sentinel" {
					t.Errorf("numeric replacement lost hook intent: %q", client.intents[0])
				}
			})
		}
	}
}

func numberHookOriginalCall(t *testing.T, name string) (llm.ToolCallData, appwire.SessionOperationRequest) {
	t.Helper()
	args := map[string]any{"to": "local:worker", "input_mode": "queue", "message": "answer-sentinel", "client_mutation_id": "mutation-sentinel", "expected_instance_id": "instance-sentinel", "expected_ask_revision": 1, "expected_ask_boot_generation": "boot-sentinel"}
	want := appwire.SessionOperationRequest{Queue: &appwire.TurnQueueParams{Ref: "local:worker", Input: []appwire.InputItem{{Type: "text", Text: "answer-sentinel"}}, ClientMutationID: "mutation-sentinel", ExpectedInstanceID: "instance-sentinel", ExpectedAskRevision: new(uint64(1)), ExpectedAskBootGeneration: "boot-sentinel"}}
	if name == "job_status" {
		args = map[string]any{"hub": map[string]any{"ref": "local:worker", "expected_ask_revision": 1, "expected_ask_boot_generation": "boot-sentinel"}}
		want = appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: "local:worker", ExpectedAskRevision: new(uint64(1)), ExpectedAskBootGeneration: "boot-sentinel"}}
	}
	return llm.ToolCallData{ID: "hook-original", Name: name, Arguments: []byte(numberHookJSON(t, args))}, want
}

func TestPreToolHookManagerPageOverflowReplacements(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"offset", "limit"} {
		for _, number := range []json.Number{"1e400", "-1e400", "1e-400"} {
			t.Run(key+"/"+number.String(), func(t *testing.T) {
				patch := map[string]any{"hub": map[string]any{"ref": "local:worker", "expected_ask_revision": 1, "expected_ask_boot_generation": "boot-sentinel", key: number}}
				pluginDir, capture := numberHookPlugin(t, patch)
				root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) {
					cfg.Manager = true
					cfg.PluginDirs = []string{pluginDir}
				})
				client := &numberHookClient{}
				root.SetHubClient(client)
				call, _ := numberHookOriginalCall(t, "job_status")
				result := root.execTool(t.Context(), call, "")
				numberHookView(t, capture, call.ID)
				if !result.IsError || len(client.requests) != 0 {
					t.Errorf("invalid page replacement reached hub: result=%+v requests=%s", result, numberHookJSON(t, client.requests))
				}
			})
		}
	}
}

func TestPreToolHookNumberFallbackPreservesOriginal(t *testing.T) {
	t.Parallel()
	wrap := func(update string) string {
		return "{\n  \"hookSpecificOutput\": {\"permissionDecision\":\"allow\",\"updatedInput\":" + update + "}}"
	}
	for _, name := range []string{"delegate_send", "job_status", "number_probe"} {
		for _, control := range []struct {
			name, output string
			fallback     bool
		}{
			{"nonselected_overflow", wrap(`{"unrelated":1e400,"intent":"patch-sentinel"}`), true},
			{"mixed_selected_and_nonselected", wrap(`{"expected_ask_revision":1e400,"unrelated":1e400}`), true},
			{"mixed_hub_selected_and_nonselected", wrap(`{"hub":{"expected_ask_revision":1e400,"unrelated":1e400}}`), true},
			{"envelope_overflow", `{"extra":1e400,"hookSpecificOutput":{"updatedInput":{"intent":"patch-sentinel"}}}`, true},
			{"case_sensitive_envelope", `{"HookSpecificOutput":{"updatedInput":{"expected_ask_revision":1e400}}}`, true},
			{"null_update", wrap(`null`), false},
			{"array_update", wrap(`[]`), false},
			{"scalar_update", wrap(`3`), false},
			{"malformed", wrap(`{"expected_ask_revision":1e400`), true},
			{"trailing_json", wrap(`{"intent":"patch-sentinel"}`) + ` null`, true},
		} {
			t.Run(name+"/"+control.name, func(t *testing.T) {
				output := control.output
				pluginDir, capture := numberHookOutputPlugin(t, output)
				root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) {
					cfg.Manager = name != "number_probe"
					cfg.PluginDirs = []string{pluginDir}
				})
				collect := drainEvents(root)
				client := &numberHookClient{}
				root.SetHubClient(client)
				call, want := numberHookOriginalCall(t, name)
				var ordinary map[string]any
				if name == "number_probe" {
					root.RegisterTool(name, "captures ordinary input", map[string]any{"type": "object"}, func(_ context.Context, args any) (any, error) {
						ordinary = args.(map[string]any)
						return nil, nil
					})
					call.Arguments = []byte(`{"value":1.25}`)
				}
				result := root.execTool(t.Context(), call, "")
				numberHookView(t, capture, call.ID)
				if result.IsError {
					t.Errorf("fallback refused original valid call: %+v", result)
				}
				if name == "number_probe" {
					if !reflect.DeepEqual(ordinary, map[string]any{"value": float64(1.25)}) {
						t.Errorf("ordinary fallback changed original input: %#v", ordinary)
					}
				} else if len(client.requests) != 1 || !reflect.DeepEqual(client.requests[0], want) || client.intents[0] != "" {
					t.Errorf("fallback applied an update: requests=%s want=%s intents=%v", numberHookJSON(t, client.requests), numberHookJSON(t, want), client.intents)
				}
				root.Close()
				var messages []string
				var exits []int
				for _, event := range collect() {
					if warning, ok := event.Data.(events.WarningData); ok && warning.Source == string(diagnostic.SourceHook) {
						messages = append(messages, warning.Message)
					}
					if end, ok := event.Data.(events.HookEndData); ok && end.Event == "PreToolUse" {
						exits = append(exits, end.ExitCode)
					}
				}
				if !reflect.DeepEqual(exits, []int{0}) {
					t.Errorf("real hook exit dispositions=%v", exits)
				}
				// This is the test-supplied raw protocol payload, not error copy.
				if control.fallback && !reflect.DeepEqual(messages, []string{output}) {
					t.Errorf("fallback changed original hook stdout bytes: got=%q want=%q", messages, output)
				} else if !control.fallback && len(messages) != 0 {
					t.Errorf("ignored nonobject update became plain output: %q", messages)
				}
			})
		}
	}
}

func TestManagerQuestionSelectedExponentDecoding(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	client := &managerQuestionClient{}
	root.SetHubClient(client)
	for _, name := range []string{"delegate_send", "job_status"} {
		keys := []string{"expected_ask_revision"}
		if name == "job_status" {
			keys = append(keys, "offset", "limit")
		}
		for _, key := range keys {
			for _, number := range []json.Number{"1e400", "-1e400", "1e-400"} {
				t.Run(name+"/"+key+"/"+number.String(), func(t *testing.T) {
					call, _ := numberHookOriginalCall(t, name)
					var args map[string]any
					if err := json.Unmarshal(call.Arguments, &args); err != nil {
						t.Fatal(err)
					}
					fields := args
					if name == "job_status" {
						fields = args["hub"].(map[string]any)
					}
					fields[key] = number
					call.Arguments = []byte(numberHookJSON(t, args))
					registered := root.reg.Get(name)
					var decoded map[string]any
					if err := registered.UnmarshalArguments(call.Arguments, &decoded); err != nil {
						t.Fatalf("selected number failed before schema/typed validation: %v", err)
					}
					fields = decoded
					if name == "job_status" {
						fields = decoded["hub"].(map[string]any)
					}
					if fields[key] != number {
						t.Errorf("selected numeric token=%#v, want %#v", fields[key], number)
					}
					_, err := registered.Exec(t.Context(), root.currentEnv(), decoded)
					var wire appwire.WireError
					if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
						t.Errorf("typed executor did not refuse selected number: %T, %v", err, err)
					}
					result := root.reg.ExecuteCall(t.Context(), root.currentEnv(), call)
					if !result.IsError {
						t.Errorf("registry executed invalid selected number: %+v", result)
					}
					for _, repair := range []bool{false, true} {
						input := call
						if repair {
							input.Arguments = input.Arguments[:len(input.Arguments)-1]
						}
						prepared := prepareToolCall(input, registered, []string{name}, name, "communicate", "")
						if prepared.PrevalErr == "" {
							result := root.reg.ExecutePreparedCall(t.Context(), root.currentEnv(), prepared.Call)
							if !result.IsError {
								t.Errorf("prepared invalid selected number executed, JSON repair=%v: %+v", repair, result)
							}
						}
					}
					if len(client.requests) != 0 {
						t.Fatalf("invalid selected number forwarded: %s", numberHookJSON(t, client.requests))
					}
				})
			}
		}
	}
}

func TestManagerQuestionOrdinaryDecodeDiagnostics(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"expected_ask_revision":1,"unrelated":1e400}`,
		`{"expected_ask_revision":1e400,"unrelated":1e400}`,
		`{"hub":{"expected_ask_revision":18446744073709551615,"unrelated":1e400}}`,
		`{"hub":{"offset":1e400,"unrelated":1e400}}`,
		`{"hub":{"limit":0,"ordinary":[1e400]}}`,
		`{"expected_ask_revision":1`,
		`{"expected_ask_revision":1} null`,
		`[]`,
		`3`,
	} {
		t.Run(raw, func(t *testing.T) {
			got, want := map[string]any{"seed": float64(7)}, map[string]any{"seed": float64(7)}
			gotErr := unmarshalManagerQuestionArguments([]byte(raw), &got)
			wantErr := json.Unmarshal([]byte(raw), &want)
			var gotSyntax, wantSyntax *json.SyntaxError
			var gotType, wantType *json.UnmarshalTypeError
			switch {
			case errors.As(wantErr, &wantSyntax):
				if !errors.As(gotErr, &gotSyntax) || gotSyntax.Offset != wantSyntax.Offset {
					t.Errorf("ordinary syntax error changed: got=%#v want=%#v", gotErr, wantErr)
				}
			case errors.As(wantErr, &wantType):
				if !errors.As(gotErr, &gotType) || gotType.Value != wantType.Value || gotType.Type != wantType.Type || gotType.Offset != wantType.Offset || gotType.Struct != wantType.Struct || gotType.Field != wantType.Field {
					t.Errorf("ordinary typed error changed: got=%#v want=%#v", gotErr, wantErr)
				}
			default:
				t.Fatalf("fixture did not trigger an ordinary JSON failure: %T", wantErr)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ordinary failed-decode view changed: got=%#v want=%#v", got, want)
			}
		})
	}
}
