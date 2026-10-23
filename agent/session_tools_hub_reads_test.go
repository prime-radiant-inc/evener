package agent

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

type managerReadCapture struct {
	managerStateForUnit
	requests []appwire.SessionOperationRequest
}

func (c *managerReadCapture) Invoke(_ context.Context, request appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	c.requests = append(c.requests, request)
	return appwire.SessionOperationResponse{Actor: appwire.SessionActor{Kind: "manager"}}, nil
}

func TestManagerReadProjectionRejectsLocalOnlyArguments(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	client := &managerReadCapture{}
	root.SetHubClient(client)
	cases := []struct{ name, args string }{
		{"read_transcript", `{"hub":{"ref":"local:worker","format":"jsonl"}}`},
		{"read_transcript", `{"hub":{"ref":"local:worker","expand_turn":4}}`},
		{"read_transcript", `{"hub":{"ref":"local:worker","range":"1-20"}}`},
		{"read_transcript", `{"hub":{"ref":"local:worker"},"format":"jsonl"}`},
		{"job_status", `{"hub":{"ref":"local:worker","target":"job_123"}}`},
		{"job_list", `{"hub":{"mode":"discovery"},"include_nested":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.args, func(t *testing.T) {
			result := root.reg.ExecuteCall(t.Context(), root.currentEnv(), llm.ToolCallData{ID: "invalid-hub-read", Name: tc.name, Arguments: json.RawMessage(tc.args)})
			if !result.IsError || len(client.requests) != 0 {
				t.Fatalf("local-only argument reached projected hub reader: result=%+v requests=%+v", result, client.requests)
			}
		})
	}
}

func TestManagerReadProjectionUsesExistingTools(t *testing.T) {
	root := newMessagingRoot(t, newMessagingAdapter(), func(cfg *SessionConfig) { cfg.Manager = true })
	client := &managerReadCapture{}
	root.SetHubClient(client)
	for _, tc := range []struct {
		name, args string
		assert     func(appwire.SessionOperationRequest) bool
	}{
		{"job_list", `{"hub":{"mode":"discovery","limit":2}}`, func(r appwire.SessionOperationRequest) bool { return r.Discover != nil && r.Discover.Limit == 2 }},
		{"job_status", `{"hub":{"ref":"local:worker"}}`, func(r appwire.SessionOperationRequest) bool { return r.Read != nil && r.Read.Ref == "local:worker" }},
		{"read_transcript", `{"hub":{"ref":"local:worker","cursor":"older","itemLimit":2}}`, func(r appwire.SessionOperationRequest) bool {
			return r.Turns != nil && r.Turns.Cursor == "older" && r.Turns.ItemLimit == 2
		}},
		{"job_list", `{"hub":{"mode":"tasks","ref":"local:worker","offset":2,"limit":2,"snapshot":"hash"}}`, func(r appwire.SessionOperationRequest) bool {
			return r.Tasks != nil && r.Tasks.Ref == "local:worker" && r.Tasks.Offset == 2 && r.Tasks.Limit == 2 && r.Tasks.Snapshot == "hash"
		}},
		{"job_list", `{"hub":{"mode":"activity","ref":"local:worker","resource":"delegates"}}`, func(r appwire.SessionOperationRequest) bool {
			return r.Activity != nil && r.Activity.Resource == appwire.SessionActivityResourceDelegates
		}},
		{"job_list", `{"hub":{"mode":"activity","ref":"local:worker","resource":"jobs","scope":"subtree","cursor":"older","limit":1}}`, func(r appwire.SessionOperationRequest) bool {
			return r.Activity != nil && r.Activity.Ref == "local:worker" && r.Activity.Resource == appwire.SessionActivityResourceJobs && r.Activity.Scope == appwire.SessionActivityScopeSubtree && r.Activity.Cursor == "older" && r.Activity.Limit == 1
		}},
		{"job_status", `{"hub":{"ref":"local:worker","jobId":"job-1"}}`, func(r appwire.SessionOperationRequest) bool { return r.Job != nil && r.Job.JobID == "job-1" }},
		{"read_transcript", `{"hub":{"ref":"local:worker","jobId":"job-1","maxBytes":64}}`, func(r appwire.SessionOperationRequest) bool {
			return r.JobOutput != nil && r.JobOutput.JobID == "job-1" && r.JobOutput.MaxBytes == 64
		}},
		{"model_list", `{"hub":{"host":"remote","refresh":true}}`, func(r appwire.SessionOperationRequest) bool {
			return r.Models != nil && r.Models.Harness == "remote" && r.Models.Refresh
		}},
	} {
		t.Run(tc.name+"/"+tc.args, func(t *testing.T) {
			before := len(client.requests)
			result := root.reg.ExecuteCall(t.Context(), root.currentEnv(), llm.ToolCallData{ID: "hub-read", Name: tc.name, Arguments: json.RawMessage(tc.args)})
			if result.IsError || len(client.requests) != before+1 || !tc.assert(client.requests[before]) {
				t.Fatalf("existing tool failed typed hub read: %+v requests=%+v", result, client.requests[before:])
			}
		})
	}
}
