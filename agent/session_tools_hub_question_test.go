package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"strconv"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

type managerQuestionClient struct {
	managerReadCapture
	response appwire.SessionOperationResponse
}

func (c *managerQuestionClient) Invoke(_ context.Context, request appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	c.requests = append(c.requests, request)
	return c.response, nil
}

// Check the registered schema and its actual executor separately: direct Exec
// must refuse malformed arguments even when schema validation is bypassed.
func checkManagerQuestionCall(t *testing.T, root *Session, client *managerQuestionClient, name string, args map[string]any, valid bool, want appwire.SessionOperationRequest) {
	t.Helper()
	registered := root.reg.Get(name)
	if err := registered.Schema.Validate(args); (err == nil) != valid {
		t.Errorf("schema valid=%v, want %v: %v", err == nil, valid, err)
	}
	before := len(client.requests)
	out, err := registered.Exec(t.Context(), root.currentEnv(), args)
	if (err == nil) != valid {
		t.Errorf("executor valid=%v, want %v: %v", err == nil, valid, err)
	}
	if !valid && err != nil {
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Errorf("refusal code=%v, want InvalidParams", err)
		}
	}
	if valid && !reflect.DeepEqual(out, client.response) {
		t.Errorf("response changed: got %+v want %+v", out, client.response)
	}
	check := func() {
		t.Helper()
		got := client.requests[before:]
		if !valid {
			if len(got) != 0 {
				t.Errorf("invalid call reached hub: %+v", got)
			}
		} else if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Errorf("hub requests=%+v, want %+v", got, want)
		}
	}
	check()
	before = len(client.requests)
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	result := root.reg.ExecuteCall(t.Context(), root.currentEnv(), llm.ToolCallData{ID: "question-contract", Name: name, Arguments: raw})
	if result.IsError == valid {
		t.Errorf("registry error=%v, want %v: %+v", result.IsError, !valid, result)
	}
	if valid {
		var rendered appwire.SessionOperationResponse
		if err := json.Unmarshal([]byte(result.Output), &rendered); err != nil || result.Truncated || !reflect.DeepEqual(rendered, client.response) {
			t.Errorf("registry changed response: decoded=%+v truncated=%v error=%v", rendered, result.Truncated, err)
		}
	}
	check()
}

func TestManagerAskFenceSendContract(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	client := &managerQuestionClient{}
	root.SetHubClient(client)
	for _, mode := range []string{"start", "queue", "steer"} {
		for _, typed := range []bool{false, true} {
			for _, revision := range []*uint64{nil, new(uint64(0)), new(uint64(23))} {
				fence := "unfenced"
				if revision != nil {
					fence = "revision_" + strconv.FormatUint(*revision, 10)
				}
				t.Run(mode+"/"+map[bool]string{false: "message", true: "input"}[typed]+"/"+fence, func(t *testing.T) {
					args := map[string]any{"to": "local:worker", "input_mode": mode, "client_mutation_id": "mutation-sentinel", "expected_instance_id": "instance-sentinel", "message": "answer-sentinel"}
					input := []appwire.InputItem{{Type: "text", Text: "answer-sentinel"}}
					if typed {
						delete(args, "message")
						args["input"] = []any{map[string]any{"type": "text", "text": "answer-sentinel"}}
					}
					boot := ""
					if revision != nil {
						args["expected_ask_revision"], args["expected_ask_boot_generation"] = *revision, "boot-sentinel"
						boot = "boot-sentinel"
					}
					want := appwire.SessionOperationRequest{}
					switch mode {
					case "start":
						want.Start = &appwire.TurnStartParams{Ref: "local:worker", Input: input, ClientMutationID: "mutation-sentinel", ExpectedInstanceID: "instance-sentinel", ExpectedAskRevision: revision, ExpectedAskBootGeneration: boot}
					case "queue":
						want.Queue = &appwire.TurnQueueParams{Ref: "local:worker", Input: input, ClientMutationID: "mutation-sentinel", ExpectedInstanceID: "instance-sentinel", ExpectedAskRevision: revision, ExpectedAskBootGeneration: boot}
					case "steer":
						want.Steer = &appwire.TurnSteerParams{Ref: "local:worker", Input: input, ClientMutationID: "mutation-sentinel", ExpectedInstanceID: "instance-sentinel", ExpectedAskRevision: revision, ExpectedAskBootGeneration: boot}
					}
					checkManagerQuestionCall(t, root, client, "delegate_send", args, true, want)
				})
			}
		}
	}
	base := map[string]any{"to": "local:worker", "input_mode": "queue", "message": "answer-sentinel", "client_mutation_id": "mutation-sentinel", "expected_instance_id": "instance-sentinel"}
	for name, fields := range map[string]map[string]any{
		"missing_boot":     {"expected_ask_revision": 0},
		"missing_revision": {"expected_ask_boot_generation": "boot-sentinel"},
		"negative":         {"expected_ask_revision": -1, "expected_ask_boot_generation": "boot-sentinel"},
		"fraction":         {"expected_ask_revision": 1.5, "expected_ask_boot_generation": "boot-sentinel"},
		"overflow":         {"expected_ask_revision": json.Number("18446744073709551616"), "expected_ask_boot_generation": "boot-sentinel"},
		"revision_string":  {"expected_ask_revision": "0", "expected_ask_boot_generation": "boot-sentinel"},
		"revision_null":    {"expected_ask_revision": nil, "expected_ask_boot_generation": "boot-sentinel"},
		"revision_bool":    {"expected_ask_revision": false, "expected_ask_boot_generation": "boot-sentinel"},
		"boot_number":      {"expected_ask_revision": 0, "expected_ask_boot_generation": 3},
		"boot_null":        {"expected_ask_revision": 0, "expected_ask_boot_generation": nil},
		"boot_blank":       {"expected_ask_revision": 0, "expected_ask_boot_generation": " "},
		"both_null":        {"expected_ask_revision": nil, "expected_ask_boot_generation": nil},
		"both_inputs":      {"input": []any{}},
		"ordinary_wait":    {"max_wait_ms": 0},
		"unknown":          {"unknown": 0},
	} {
		t.Run(name, func(t *testing.T) {
			args := maps.Clone(base)
			maps.Copy(args, fields)
			checkManagerQuestionCall(t, root, client, "delegate_send", args, false, appwire.SessionOperationRequest{})
		})
	}
	for name, fields := range map[string]map[string]any{
		"revision": {"expected_ask_revision": 0},
		"boot":     {"expected_ask_boot_generation": "boot-sentinel"},
		"pair":     {"expected_ask_revision": 0, "expected_ask_boot_generation": "boot-sentinel"},
	} {
		t.Run("ordinary/"+name, func(t *testing.T) {
			args := map[string]any{"to": "dlg_missing", "message": "answer-sentinel"}
			maps.Copy(args, fields)
			checkManagerQuestionCall(t, root, client, "delegate_send", args, false, appwire.SessionOperationRequest{})
		})
	}
}

func TestManagerPendingAskPageContract(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	client := &managerQuestionClient{response: appwire.SessionOperationResponse{Read: &appwire.ThreadReadResponse{
		PendingAskPage: &appwire.PendingAskPage{Composites: []appwire.PendingAskCall{{TurnID: "turn-sentinel", CallID: "call-sentinel"}}, Total: 5, NextOffset: new(3), CalllessCount: 2},
	}}}
	root.SetHubClient(client)
	for _, tc := range []struct {
		name string
		hub  map[string]any
		want appwire.SessionOperationRequest
	}{
		{"metadata", map[string]any{"ref": "local:worker"}, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: "local:worker"}}},
		{"metadata_empty_job", map[string]any{"ref": "local:worker", "jobId": ""}, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: "local:worker"}}},
		{"job", map[string]any{"ref": "local:worker", "jobId": "job_sentinel"}, appwire.SessionOperationRequest{Job: &appwire.JobsGetParams{Ref: "local:worker", JobID: "job_sentinel"}}},
		{"page_defaults", map[string]any{"ref": "local:worker", "expected_ask_revision": 0, "expected_ask_boot_generation": "boot-sentinel"}, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: "local:worker", ExpectedAskRevision: new(uint64(0)), ExpectedAskBootGeneration: "boot-sentinel"}}},
		{"page_bounds", map[string]any{"ref": "remote:worker", "expected_ask_revision": 23, "expected_ask_boot_generation": "boot-sentinel", "offset": 2, "limit": 3}, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: "remote:worker", ExpectedAskRevision: new(uint64(23)), ExpectedAskBootGeneration: "boot-sentinel", Offset: new(2), Limit: new(3)}}},
		{"page_zero_bounds", map[string]any{"ref": "local:worker", "expected_ask_revision": 0, "expected_ask_boot_generation": "boot-sentinel", "offset": 0, "limit": 0}, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: "local:worker", ExpectedAskRevision: new(uint64(0)), ExpectedAskBootGeneration: "boot-sentinel", Offset: new(0), Limit: new(0)}}},
		{"page_negative_bounds_for_server_normalization", map[string]any{"ref": "local:worker", "expected_ask_revision": 0, "expected_ask_boot_generation": "boot-sentinel", "offset": -1, "limit": -2}, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: "local:worker", ExpectedAskRevision: new(uint64(0)), ExpectedAskBootGeneration: "boot-sentinel", Offset: new(-1), Limit: new(-2)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkManagerQuestionCall(t, root, client, "job_status", map[string]any{"hub": tc.hub}, true, tc.want)
		})
	}
	page := map[string]any{"ref": "local:worker", "expected_ask_revision": 0, "expected_ask_boot_generation": "boot-sentinel", "offset": 0, "limit": 0}
	invalid := map[string]map[string]any{
		"offset_only":       {"ref": "local:worker", "offset": 0},
		"limit_only":        {"ref": "local:worker", "limit": 0},
		"revision_only":     {"ref": "local:worker", "expected_ask_revision": 0},
		"boot_only":         {"ref": "local:worker", "expected_ask_boot_generation": "boot-sentinel"},
		"job_offset_zero":   {"ref": "local:worker", "jobId": "job_sentinel", "offset": 0},
		"job_limit_zero":    {"ref": "local:worker", "jobId": "job_sentinel", "limit": 0},
		"job_revision_zero": {"ref": "local:worker", "jobId": "job_sentinel", "expected_ask_revision": 0},
		"job_boot_empty":    {"ref": "local:worker", "jobId": "job_sentinel", "expected_ask_boot_generation": ""},
	}
	for name, fields := range map[string]map[string]any{
		"page_job":              {"jobId": "job_sentinel"},
		"page_empty_job":        {"jobId": ""},
		"offset_string":         {"offset": "0"},
		"limit_bool":            {"limit": false},
		"offset_fraction":       {"offset": 0.5},
		"limit_fraction":        {"limit": 0.5},
		"offset_null":           {"offset": nil},
		"limit_null":            {"limit": nil},
		"offset_overflow":       {"offset": json.Number("9223372036854775808")},
		"limit_overflow":        {"limit": json.Number("9223372036854775808")},
		"revision_negative":     {"expected_ask_revision": -1},
		"revision_fraction":     {"expected_ask_revision": 1.5},
		"revision_overflow":     {"expected_ask_revision": json.Number("18446744073709551616")},
		"revision_string":       {"expected_ask_revision": "0"},
		"revision_bool":         {"expected_ask_revision": false},
		"revision_null":         {"expected_ask_revision": nil},
		"boot_null":             {"expected_ask_boot_generation": nil},
		"boot_blank":            {"expected_ask_boot_generation": " "},
		"boot_number":           {"expected_ask_boot_generation": 0},
		"job_argument_zero":     {"maxBytes": 0},
		"history_argument_zero": {"itemLimit": 0},
		"camel_case_pair":       {"expectedAskRevision": 0},
	} {
		invalid[name] = maps.Clone(page)
		maps.Copy(invalid[name], fields)
	}
	invalid["missing_ref"] = maps.Clone(page)
	delete(invalid["missing_ref"], "ref")
	for name, hub := range invalid {
		t.Run(name, func(t *testing.T) {
			checkManagerQuestionCall(t, root, client, "job_status", map[string]any{"hub": hub}, false, appwire.SessionOperationRequest{})
		})
	}
	t.Run("ordinary_mixed", func(t *testing.T) {
		checkManagerQuestionCall(t, root, client, "job_status", map[string]any{"hub": page, "target": "job_sentinel"}, false, appwire.SessionOperationRequest{})
	})
	t.Run("history_has_no_page", func(t *testing.T) {
		checkManagerQuestionCall(t, root, client, "read_transcript", map[string]any{"hub": page}, false, appwire.SessionOperationRequest{})
	})
}
