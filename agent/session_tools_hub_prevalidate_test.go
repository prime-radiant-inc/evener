package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/modelavailability"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func managerPrevalidationFields(path string, value any) map[string]any {
	if path == "expected_ask_revision" {
		return map[string]any{path: value}
	}
	return map[string]any{"hub": map[string]any{path[len("hub."):]: value}}
}

func TestManagerQuestionPrevalidationNumbers(t *testing.T) {
	t.Parallel()
	validate := managerQuestionPrevalidator(nil)
	maxInt := int(^uint(0) >> 1)
	for _, path := range []string{"expected_ask_revision", "hub.expected_ask_revision", "hub.offset", "hub.limit"} {
		valid := []string{"0", "9007199254740993", "18446744073709551615"}
		invalid := []string{"-1", "18446744073709551616", "1.5", "1e0", "1e1000001", "1e-1000001", "-1e1000001"}
		if path == "hub.offset" || path == "hub.limit" {
			valid = []string{"0", "-1", strconv.Itoa(maxInt), strconv.Itoa(-maxInt - 1)}
			invalid = []string{"18446744073709551616", "-18446744073709551616", "1.5", "1e0", "1e1000001", "1e-1000001", "-1e1000001"}
		}
		for _, group := range []struct {
			values []string
			valid  bool
		}{{valid, true}, {invalid, false}} {
			for _, value := range group.values {
				t.Run(path+"/"+value, func(t *testing.T) {
					args := managerPrevalidationFields(path, json.Number(value))
					want := managerPrevalidationFields(path, json.Number(value))
					err := validate(args)
					if group.valid {
						if err != nil {
							t.Fatalf("valid selected number refused: %v", err)
						}
					} else {
						var wire appwire.WireError
						if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
							t.Fatalf("selected number refusal=%T, want InvalidParams", err)
						}
					}
					if !reflect.DeepEqual(args, want) {
						t.Errorf("prevalidation changed selected number or map: got=%#v want=%#v", args, want)
					}
				})
			}
		}
	}
}

func TestManagerQuestionPrevalidationScope(t *testing.T) {
	t.Parallel()
	validate := managerQuestionPrevalidator(nil)
	for _, raw := range []string{
		`{"expected_ask_revision":"1e1000001","hub":{"offset":"1e-1000001","limit":false}}`,
		`{"expected_ask_revision":null,"hub":{"expected_ask_revision":false,"offset":null,"limit":[]}}`,
		`{"expected_ask_revision":-1,"hub":{"expected_ask_revision":1.5,"offset":-1.5,"limit":2.5}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var args, want map[string]any
			if err := json.Unmarshal([]byte(raw), &args); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &want); err != nil {
				t.Fatal(err)
			}
			if err := validate(args); err != nil || !reflect.DeepEqual(args, want) {
				t.Errorf("non-json.Number values changed: args=%#v error=%v", args, err)
			}
		})
	}
	ordinary := map[string]any{
		"offset": json.Number("1e1000001"), "limit": json.Number("1e-1000001"),
		"unrelated": json.Number("1e1000001"),
		"hub":       map[string]any{"expectedAskRevision": json.Number("1e1000001"), "ordinary": json.Number("1e-1000001")},
	}
	if err := validate(ordinary); err != nil {
		t.Fatalf("ordinary numeric field was prevalidated: %v", err)
	}
	calls := 0
	priorError := errors.New("prior-prevalidator-sentinel")
	composed := managerQuestionPrevalidator(func(map[string]any) error {
		calls++
		return priorError
	})
	if err := composed(map[string]any{"expected_ask_revision": json.Number("1e1000001")}); err != priorError || calls != 1 {
		t.Fatalf("previous prevalidator error/order changed: error=%v calls=%d", err, calls)
	}
	composed = managerQuestionPrevalidator(func(args map[string]any) error {
		args["expected_ask_revision"] = json.Number("1e1000001")
		return nil
	})
	var wire appwire.WireError
	if err := composed(map[string]any{}); !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("selected check did not run after previous prevalidator: %T", err)
	}
}

func TestManagerQuestionPrevalidationComposition(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	client := &managerQuestionClient{}
	root.SetHubClient(client)
	calls := map[string]int{}
	priorError := errors.New("registration-prevalidator-sentinel")
	for _, name := range []string{"delegate_send", "job_status"} {
		base := *root.reg.Get(name)
		base.PreValidate = func(args map[string]any) error {
			calls[name]++
			if args["prior_refusal"] == true {
				return priorError
			}
			return nil
		}
		if err := root.reg.Register(base); err != nil {
			t.Fatal(err)
		}
	}
	for round := range 3 {
		root.mu.Lock()
		root.hubEnabled = true
		root.composeHubRegistry()
		root.mu.Unlock()
		for _, name := range []string{"delegate_send", "job_status"} {
			t.Run(name+"/"+strconv.Itoa(round), func(t *testing.T) {
				registered := root.reg.Get(name)
				refused := map[string]any{"prior_refusal": true, "expected_ask_revision": json.Number("1e1000001")}
				if err := registered.PreValidate(refused); err != priorError {
					t.Fatalf("registration replaced previous prevalidator error: %v", err)
				}
				calls[name] = 0
				call, want := numberHookOriginalCall(t, name)
				call.ID = name + "-" + strconv.Itoa(round)
				if round == 1 {
					call.Arguments = call.Arguments[:len(call.Arguments)-1]
				} else if round == 2 && name == "delegate_send" {
					var args map[string]any
					if err := json.Unmarshal(call.Arguments, &args); err != nil {
						t.Fatal(err)
					}
					args["expected_ask_revision"] = "1"
					call.Arguments = []byte(numberHookJSON(t, args))
				}
				prepared := prepareToolCall(call, registered, []string{name}, name, "communicate", "")
				if prepared.PrevalErr != "" || calls[name] != 1 {
					t.Fatalf("preparation failed or repeated previous callback: %+v calls=%d", prepared, calls[name])
				}
				if (round == 1 || round == 2 && name == "delegate_send") && len(prepared.Changes) == 0 {
					t.Fatal("fixture did not exercise JSON repair or scalar healing")
				}
				before := len(client.requests)
				result := root.reg.ExecutePreparedCall(t.Context(), root.currentEnv(), prepared.Call)
				if result.IsError || calls[name] != 1 || len(client.requests) != before+1 || !reflect.DeepEqual(client.requests[before], want) {
					t.Errorf("prepared dispatch changed previous validation or forwarding: result=%+v calls=%d requests=%s", result, calls[name], numberHookJSON(t, client.requests[before:]))
				}
			})
		}
	}
	root.mu.Lock()
	root.hubEnabled = false
	root.composeHubRegistry()
	root.mu.Unlock()
	for _, name := range []string{"delegate_send", "job_status"} {
		registered := root.reg.Get(name)
		calls[name] = 0
		if err := registered.PreValidate(map[string]any{"expected_ask_revision": json.Number("1e1000001")}); err != nil || calls[name] != 1 {
			t.Errorf("Manager-off did not restore previous prevalidator: error=%v calls=%d", err, calls[name])
		}
		if registered.UnmarshalArgs != nil {
			t.Errorf("Manager-off retained opted-in decoder on %s", name)
		}
	}
}

func TestManagerQuestionPrevalidationOrdinaryRepair(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	snapshot := modelavailability.Capture(t.Context(), []string{"provider"}, "provider", func(context.Context, string) ([]string, error) {
		return []string{"a", "b"}, nil
	}, 30*time.Second)
	root.modelSnapshot = &snapshot
	registered := root.reg.Get("model_list")
	if registered.PreValidate != nil || registered.UnmarshalArgs != nil {
		t.Fatal("ordinary model_list opted into Manager numeric handling")
	}
	call := llm.ToolCallData{ID: "ordinary-healed-model-bound", Name: "model_list", Arguments: []byte(`{"max_count":"1"}`)}
	prepared := prepareToolCall(call, registered, []string{call.Name}, call.Name, "communicate", "")
	if prepared.PrevalErr != "" || len(prepared.Changes) == 0 {
		t.Fatalf("ordinary numeric string was not healed: %+v", prepared)
	}
	var args map[string]any
	if err := registered.UnmarshalArguments(prepared.Call.Arguments, &args); err != nil || args["max_count"] != float64(1) {
		t.Fatalf("ordinary repaired value/type changed: args=%#v error=%v", args, err)
	}
	result := root.reg.ExecutePreparedCall(t.Context(), root.currentEnv(), prepared.Call)
	var page modelavailability.Page
	if err := json.Unmarshal([]byte(result.Output), &page); err != nil || result.IsError {
		t.Fatalf("ordinary repaired dispatch failed: result=%+v error=%v", result, err)
	}
	if !reflect.DeepEqual(page.Choices, []string{"provider/a"}) || page.Next == "" || page.Terminal {
		t.Errorf("ordinary float-dependent repaired bound was not applied: %+v", page)
	}
}
