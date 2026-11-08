package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/modelavailability"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestManagerAskRevisionSurvivesJSONRepair(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	client := &managerQuestionClient{}
	root.SetHubClient(client)
	for _, revision := range []uint64{0, 1<<53 + 1, ^uint64(0)} {
		t.Run(strconv.FormatUint(revision, 10), func(t *testing.T) {
			args := map[string]any{"hub": map[string]any{
				"ref": "local:worker", "expected_ask_revision": revision,
				"expected_ask_boot_generation": "boot-sentinel",
			}}
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			call := llm.ToolCallData{ID: "repaired-revision", Name: "job_status", Arguments: raw[:len(raw)-1]}
			prepared := prepareToolCall(call, root.reg.Get(call.Name), []string{call.Name}, call.Name, "communicate", "")
			if prepared.PrevalErr != "" || len(prepared.Changes) == 0 {
				t.Fatalf("JSON repair did not produce a validated call: %+v", prepared)
			}
			var repaired struct {
				Hub struct {
					Revision uint64 `json:"expected_ask_revision"`
				} `json:"hub"`
			}
			if err := json.Unmarshal(prepared.Call.Arguments, &repaired); err != nil || repaired.Hub.Revision != revision {
				t.Errorf("repaired revision=%d, want %d: %v", repaired.Hub.Revision, revision, err)
			}
			before := len(client.requests)
			result := root.reg.ExecutePreparedCall(t.Context(), root.currentEnv(), prepared.Call)
			want := appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{
				Ref: "local:worker", ExpectedAskRevision: new(revision), ExpectedAskBootGeneration: "boot-sentinel",
			}}
			if result.IsError || len(client.requests) != before+1 || !reflect.DeepEqual(client.requests[before], want) {
				t.Errorf("repaired dispatch failed to retain revision %d: %+v, requests=%+v", revision, result, client.requests[before:])
			}
		})
	}
}

func TestOrdinaryModelListKeepsFloatDependentBounds(t *testing.T) {
	t.Parallel()
	for _, manager := range []bool{false, true} {
		t.Run(strconv.FormatBool(manager), func(t *testing.T) {
			root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = manager })
			snapshot := modelavailability.Capture(t.Context(), []string{"provider"}, "provider", func(context.Context, string) ([]string, error) {
				return []string{"a", "b"}, nil
			}, 30*time.Second)
			root.modelSnapshot = &snapshot
			for _, prepare := range []bool{false, true} {
				t.Run("prepared_"+strconv.FormatBool(prepare), func(t *testing.T) {
					call := llm.ToolCallData{ID: "ordinary-model-bound", Name: "model_list", Arguments: []byte(`{"max_count":1}`)}
					execute := root.reg.ExecuteCall
					if prepare {
						prepared := prepareToolCall(call, root.reg.Get(call.Name), []string{call.Name}, call.Name, "communicate", "")
						if prepared.PrevalErr != "" {
							if manager {
								return
							}
							t.Fatal(prepared.PrevalErr)
						}
						if manager {
							t.Fatal("Manager-only profile prepared an unavailable ordinary call")
						}
						call = prepared.Call
						execute = root.reg.ExecutePreparedCall
					}
					result := execute(t.Context(), root.currentEnv(), call)
					if manager {
						if !result.IsError {
							t.Fatal("Manager-only profile executed an unavailable ordinary call")
						}
						return
					}
					var page modelavailability.Page
					if err := json.Unmarshal([]byte(result.Output), &page); err != nil || result.IsError {
						t.Fatalf("ordinary model-list result=%+v: %v", result, err)
					}
					if !reflect.DeepEqual(page.Choices, []string{"provider/a"}) || page.Next == "" || page.Terminal {
						t.Errorf("ordinary max_count=1 was not applied: %+v", page)
					}
				})
			}
		})
	}
}

func TestManagerQuestionNumberDecoderIsScoped(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	for _, definition := range root.reg.Definitions() {
		want := definition.Name == "delegate_send" || definition.Name == "job_status"
		if got := root.reg.Get(definition.Name).UnmarshalArgs != nil; got != want {
			t.Errorf("%s opted into argument decoding=%v, want %v", definition.Name, got, want)
		}
	}
	raw := []byte(`{"expected_ask_revision":9007199254740993,"max_wait_ms":17,"unrelated":[1.25,9007199254740993],"hub":{"expected_ask_revision":18446744073709551615,"offset":9007199254740993,"limit":23,"max_count":3}}`)
	for _, name := range []string{"delegate_send", "job_status"} {
		t.Run(name, func(t *testing.T) {
			var got, want map[string]any
			if err := root.reg.Get(name).UnmarshalArguments(raw, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			if got["expected_ask_revision"] != json.Number("9007199254740993") {
				t.Errorf("top-level exact revision=%#v", got["expected_ask_revision"])
			}
			delete(got, "expected_ask_revision")
			delete(want, "expected_ask_revision")
			gotHub, wantHub := got["hub"].(map[string]any), want["hub"].(map[string]any)
			for key, value := range map[string]json.Number{"expected_ask_revision": "18446744073709551615", "offset": "9007199254740993", "limit": "23"} {
				if gotHub[key] != value {
					t.Errorf("%s exact value=%#v, want %#v", key, gotHub[key], value)
				}
				delete(gotHub, key)
				delete(wantHub, key)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ordinary argument decoding changed: got %#v, want %#v", got, want)
			}
		})
	}
}

func TestManagerAskRevisionPreparationRefusesUnrepresentableValues(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	client := &managerQuestionClient{}
	root.SetHubClient(client)
	for _, name := range []string{"delegate_send", "job_status"} {
		for _, revision := range []json.Number{"-1", "18446744073709551616", "1.5", "9007199254740993.5"} {
			t.Run(name+"/"+revision.String(), func(t *testing.T) {
				pair := map[string]any{"expected_ask_revision": revision, "expected_ask_boot_generation": "boot-sentinel"}
				args := pair
				if name == "job_status" {
					pair["ref"] = "local:worker"
					args = map[string]any{"hub": pair}
				} else {
					args["to"], args["input_mode"], args["message"] = "local:worker", "queue", "answer-sentinel"
					args["client_mutation_id"], args["expected_instance_id"] = "mutation-sentinel", "instance-sentinel"
				}
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				call := llm.ToolCallData{ID: "invalid-prepared-revision", Name: name, Arguments: raw}
				prepared := prepareToolCall(call, root.reg.Get(name), []string{name}, name, "communicate", "")
				if prepared.PrevalErr == "" {
					result := root.reg.ExecutePreparedCall(t.Context(), root.currentEnv(), prepared.Call)
					if !result.IsError {
						t.Errorf("prepared dispatch accepted unrepresentable revision %s", revision)
					}
				}
				if len(client.requests) != 0 {
					t.Fatalf("unrepresentable revision reached the hub: %+v", client.requests)
				}
			})
		}
	}
}
