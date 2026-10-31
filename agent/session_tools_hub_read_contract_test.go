package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
	"primeradiant.com/evener/llm"
)

// The capture is the external hub peer, not a replacement for the agent's
// registry, read handler, provider shaping or AppWire runtime connection.
type managerReadContractPeer struct {
	requests chan appwire.SessionOperationRequest
}

func (*managerReadContractPeer) State(context.Context) (appwire.ManagerView, error) {
	return appwire.ManagerView{Manager: true}, nil
}

func (p *managerReadContractPeer) Invoke(_ context.Context, request appwire.SessionOperationRequest) (appwire.SessionOperationResponse, error) {
	p.requests <- request
	return appwire.SessionOperationResponse{}, nil
}

func (*managerReadContractPeer) Watch(context.Context, appwire.SessionWatchRequest) (<-chan appwire.SessionWatchUpdate, error) {
	return nil, appwire.InvalidRequest("unused observation")
}

func managerReadContractTransport(t *testing.T, root *Session) <-chan appwire.SessionOperationRequest {
	t.Helper()
	// TRIPWIRE: all requests are local and synchronous, not paced by this bound.
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	server := appserver.NewServer(appserver.ServerConfig{})
	installed := make(chan appwire.SessionHubClient, 1)
	server.InstallSessionTools(func(_ context.Context, _ appwire.SessionBinding, client appwire.SessionHubClient) error {
		installed <- client
		return nil
	})
	httpServer := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
	t.Cleanup(httpServer.Close)
	transport, err := appwire.DialWebSocket(ctx, "ws"+httpServer.URL[len("http"):], httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	t.Cleanup(func() { _ = client.Close() })
	peer := &managerReadContractPeer{requests: make(chan appwire.SessionOperationRequest, 1)}
	client.SetSessionTools(appwire.SessionBinding{Ref: "local:manager", InstanceID: "instance", HubID: "hub", BindingID: "binding"}, peer)
	client.Start(ctx)
	if _, err := client.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	if err := client.BindSessionTools(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case runtime := <-installed:
		root.SetHubClient(runtime)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return peer.requests
}

func TestManagerReadContract(t *testing.T) {
	t.Parallel()
	root, wire := managerProviderSchemas(t)
	requests := managerReadContractTransport(t, root)
	compiled := map[string]*jsonschema.Schema{}
	for _, name := range []string{"job_list", "job_status", "read_transcript", "model_list"} {
		raw, err := json.Marshal(wire[name])
		if err != nil {
			t.Fatal(err)
		}
		compiled[name], err = jsonschema.CompileString("https://manager.test/read-contract/"+name+".json", string(raw))
		if err != nil {
			t.Fatal(err)
		}
	}
	zero := int64(0)
	type readCase struct {
		name, tool, args string
		valid            bool
		want             appwire.SessionOperationRequest
	}
	cases := []readCase{
		{"discovery_defaults", "job_list", `{"hub":{"mode":"discovery"}}`, true, appwire.SessionOperationRequest{Discover: &appwire.ThreadListParams{}}},
		{"discovery_all_fields", "job_list", `{"hub":{"mode":"discovery","cursor":"next","limit":2,"sortKey":"createdAt","sortDirection":"asc","searchTerm":"needle","statuses":["running"],"sourceIds":["remote"],"includeSubagents":true,"statusOnly":true}}`, true, appwire.SessionOperationRequest{Discover: &appwire.ThreadListParams{Cursor: "next", Limit: 2, SortKey: "createdAt", SortDirection: "asc", SearchTerm: "needle", Statuses: []string{"running"}, SourceIDs: []string{"remote"}, IncludeSubagents: true, StatusOnly: true}}},
		{"tasks_defaults", "job_list", `{"hub":{"mode":"tasks","ref":"local:worker"}}`, true, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:worker"}}},
		{"tasks_all_fields", "job_list", `{"hub":{"mode":"tasks","ref":"local:worker","offset":2,"limit":3,"snapshot":"hash"}}`, true, appwire.SessionOperationRequest{Tasks: &appwire.SessionTasksRequest{Ref: "local:worker", Offset: 2, Limit: 3, Snapshot: "hash"}}},
		{"activity_defaults", "job_list", `{"hub":{"mode":"activity","ref":"local:worker","resource":"summary"}}`, true, appwire.SessionOperationRequest{Activity: &appwire.SessionActivityRequest{Ref: "local:worker", Resource: appwire.SessionActivityResourceSummary}}},
		{"activity_all_fields", "job_list", `{"hub":{"mode":"activity","ref":"remote:worker","resource":"jobs","scope":"subtree","cursor":"next","limit":3}}`, true, appwire.SessionOperationRequest{Activity: &appwire.SessionActivityRequest{Ref: "remote:worker", Resource: appwire.SessionActivityResourceJobs, Scope: appwire.SessionActivityScopeSubtree, Cursor: "next", Limit: 3}}},
		{"metadata", "job_status", `{"hub":{"ref":"local:worker"}}`, true, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: "local:worker"}}},
		{"metadata_empty_job_id", "job_status", `{"hub":{"ref":"local:worker","jobId":""}}`, true, appwire.SessionOperationRequest{Read: &appwire.ThreadReadParams{Ref: "local:worker"}}},
		{"job_metadata", "job_status", `{"hub":{"ref":"local:worker","jobId":"job-1"}}`, true, appwire.SessionOperationRequest{Job: &appwire.JobsGetParams{Ref: "local:worker", JobID: "job-1"}}},
		{"history_defaults", "read_transcript", `{"hub":{"ref":"local:worker"}}`, true, appwire.SessionOperationRequest{Turns: &appwire.ThreadTurnsListParams{Ref: "local:worker"}}},
		{"history_all_fields", "read_transcript", `{"hub":{"ref":"local:worker","cursor":"next","itemLimit":2}}`, true, appwire.SessionOperationRequest{Turns: &appwire.ThreadTurnsListParams{Ref: "local:worker", Cursor: "next", ItemLimit: 2}}},
		{"history_zero_output_fields", "read_transcript", `{"hub":{"ref":"local:worker","jobId":"","maxBytes":0}}`, true, appwire.SessionOperationRequest{Turns: &appwire.ThreadTurnsListParams{Ref: "local:worker"}}},
		{"output_defaults", "read_transcript", `{"hub":{"ref":"local:worker","jobId":"job-1"}}`, true, appwire.SessionOperationRequest{JobOutput: &appwire.JobsOutputParams{Ref: "local:worker", JobID: "job-1"}}},
		{"output_all_fields", "read_transcript", `{"hub":{"ref":"remote:worker","jobId":"job-1","maxBytes":64,"beforeBytes":0}}`, true, appwire.SessionOperationRequest{JobOutput: &appwire.JobsOutputParams{Ref: "remote:worker", JobID: "job-1", MaxBytes: 64, BeforeBytes: &zero}}},
		{"output_zero_history_fields", "read_transcript", `{"hub":{"ref":"local:worker","jobId":"job-1","cursor":"","itemLimit":0}}`, true, appwire.SessionOperationRequest{JobOutput: &appwire.JobsOutputParams{Ref: "local:worker", JobID: "job-1"}}},
		{"models_all_fields", "model_list", `{"hub":{"host":"remote","cwd":"/work","refresh":true}}`, true, appwire.SessionOperationRequest{Models: &appwire.ModelListParams{Harness: "remote", CWD: "/work", Refresh: true}}},
		{"models_defaults", "model_list", `{"hub":{"host":"local"}}`, true, appwire.SessionOperationRequest{Models: &appwire.ModelListParams{Harness: "local"}}},
		{"missing_mode", "job_list", `{"hub":{}}`, false, appwire.SessionOperationRequest{}},
		{"tasks_missing_ref", "job_list", `{"hub":{"mode":"tasks"}}`, false, appwire.SessionOperationRequest{}},
		{"activity_missing_ref", "job_list", `{"hub":{"mode":"activity","resource":"summary"}}`, false, appwire.SessionOperationRequest{}},
		{"activity_missing_resource", "job_list", `{"hub":{"mode":"activity","ref":"local:worker"}}`, false, appwire.SessionOperationRequest{}},
		{"metadata_missing_ref", "job_status", `{"hub":{}}`, false, appwire.SessionOperationRequest{}},
		{"history_missing_ref", "read_transcript", `{"hub":{}}`, false, appwire.SessionOperationRequest{}},
		{"output_missing_ref", "read_transcript", `{"hub":{"jobId":"job-1"}}`, false, appwire.SessionOperationRequest{}},
		{"models_missing_host", "model_list", `{"hub":{}}`, false, appwire.SessionOperationRequest{}},
		{"discovery_resource", "job_list", `{"hub":{"mode":"discovery","resource":"summary"}}`, false, appwire.SessionOperationRequest{}},
		{"discovery_offset", "job_list", `{"hub":{"mode":"discovery","offset":1}}`, false, appwire.SessionOperationRequest{}},
		{"discovery_ref", "job_list", `{"hub":{"mode":"discovery","ref":"local:worker"}}`, false, appwire.SessionOperationRequest{}},
		{"discovery_scope", "job_list", `{"hub":{"mode":"discovery","scope":"subtree"}}`, false, appwire.SessionOperationRequest{}},
		{"discovery_snapshot", "job_list", `{"hub":{"mode":"discovery","snapshot":"hash"}}`, false, appwire.SessionOperationRequest{}},
		{"tasks_cursor", "job_list", `{"hub":{"mode":"tasks","ref":"local:worker","cursor":"next"}}`, false, appwire.SessionOperationRequest{}},
		{"tasks_resource", "job_list", `{"hub":{"mode":"tasks","ref":"local:worker","resource":"summary"}}`, false, appwire.SessionOperationRequest{}},
		{"tasks_scope", "job_list", `{"hub":{"mode":"tasks","ref":"local:worker","scope":"subtree"}}`, false, appwire.SessionOperationRequest{}},
		{"tasks_discovery_filter", "job_list", `{"hub":{"mode":"tasks","ref":"local:worker","searchTerm":"needle"}}`, false, appwire.SessionOperationRequest{}},
		{"activity_offset", "job_list", `{"hub":{"mode":"activity","ref":"local:worker","resource":"summary","offset":1}}`, false, appwire.SessionOperationRequest{}},
		{"activity_snapshot", "job_list", `{"hub":{"mode":"activity","ref":"local:worker","resource":"summary","snapshot":"hash"}}`, false, appwire.SessionOperationRequest{}},
		{"activity_discovery_filter", "job_list", `{"hub":{"mode":"activity","ref":"local:worker","resource":"summary","sourceIds":["remote"]}}`, false, appwire.SessionOperationRequest{}},
		{"metadata_cursor", "job_status", `{"hub":{"ref":"local:worker","cursor":"next"}}`, false, appwire.SessionOperationRequest{}},
		{"metadata_item_limit", "job_status", `{"hub":{"ref":"local:worker","itemLimit":2}}`, false, appwire.SessionOperationRequest{}},
		{"metadata_max_bytes", "job_status", `{"hub":{"ref":"local:worker","maxBytes":64}}`, false, appwire.SessionOperationRequest{}},
		{"metadata_before_bytes", "job_status", `{"hub":{"ref":"local:worker","beforeBytes":0}}`, false, appwire.SessionOperationRequest{}},
		{"history_max_bytes", "read_transcript", `{"hub":{"ref":"local:worker","maxBytes":64}}`, false, appwire.SessionOperationRequest{}},
		{"history_before_bytes", "read_transcript", `{"hub":{"ref":"local:worker","beforeBytes":0}}`, false, appwire.SessionOperationRequest{}},
		{"history_empty_job_id_with_output", "read_transcript", `{"hub":{"ref":"local:worker","jobId":"","maxBytes":64}}`, false, appwire.SessionOperationRequest{}},
		{"output_cursor", "read_transcript", `{"hub":{"ref":"local:worker","jobId":"job-1","cursor":"next"}}`, false, appwire.SessionOperationRequest{}},
		{"output_item_limit", "read_transcript", `{"hub":{"ref":"local:worker","jobId":"job-1","itemLimit":2}}`, false, appwire.SessionOperationRequest{}},
	}
	for _, resource := range []appwire.SessionActivityResource{appwire.SessionActivityResourceDelegates, appwire.SessionActivityResourceWatches} {
		cases = append(cases, readCase{"activity_" + string(resource), "job_list", `{"hub":{"mode":"activity","ref":"local:worker","resource":"` + string(resource) + `"}}`, true, appwire.SessionOperationRequest{Activity: &appwire.SessionActivityRequest{Ref: "local:worker", Resource: resource}}})
	}
	for _, tc := range cases {
		t.Run(tc.tool+"/"+tc.name, func(t *testing.T) {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
				t.Fatal(err)
			}
			args["intent"] = "contract sentinel"
			for name, schema := range map[string]*jsonschema.Schema{"registered": root.reg.Get(tc.tool).Schema, "serialized Responses": compiled[tc.tool]} {
				if err := schema.Validate(args); (err == nil) != tc.valid {
					t.Errorf("%s schema valid=%v, want %v: %v", name, err == nil, tc.valid, err)
				}
			}
			_, err := root.invokeHubRead(t.Context(), tc.tool, args)
			if (err == nil) != tc.valid {
				t.Errorf("read handler valid=%v, want %v: %v", err == nil, tc.valid, err)
			}
			if !tc.valid && err != nil {
				var wireError appwire.WireError
				if !errors.As(err, &wireError) || wireError.Code != appwire.CodeInvalidParams {
					t.Errorf("read handler returned untyped argument refusal: %T %v", err, err)
				}
			}
			checkRequest := func() {
				t.Helper()
				select {
				case got := <-requests:
					if !tc.valid || !reflect.DeepEqual(got, tc.want) {
						t.Errorf("transport request = %+v, want %+v (valid=%v)", got, tc.want, tc.valid)
					}
				default:
					if tc.valid {
						t.Error("valid read did not reach external transport peer")
					}
				}
			}
			checkRequest()
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			result := root.reg.ExecuteCall(t.Context(), root.currentEnv(), llm.ToolCallData{ID: tc.name, Name: tc.tool, Arguments: raw})
			if result.IsError == tc.valid {
				t.Errorf("registry error=%v, want %v: %+v", result.IsError, !tc.valid, result)
			}
			checkRequest()
		})
	}
	for _, tc := range []struct {
		tool, args string
		valid      bool
		fails      bool
	}{
		{"job_list", `{}`, true, false},
		{"job_status", `{"target":"job_missing"}`, true, true},
		{"read_transcript", `{"transcript_ref":"job:job_missing"}`, false, true},
		{"model_list", `{}`, false, true},
	} {
		t.Run(tc.tool+"/ordinary", func(t *testing.T) {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
				t.Fatal(err)
			}
			args["intent"] = "contract sentinel"
			for name, schema := range map[string]*jsonschema.Schema{"registered": root.reg.Get(tc.tool).Schema, "serialized Responses": compiled[tc.tool]} {
				if err := schema.Validate(args); (err == nil) != tc.valid {
					t.Errorf("%s ordinary schema valid=%v, want %v: %v", name, err == nil, tc.valid, err)
				}
			}
			result := root.reg.ExecuteCall(t.Context(), root.currentEnv(), llm.ToolCallData{ID: "ordinary", Name: tc.tool, Arguments: json.RawMessage(tc.args)})
			if result.IsError != tc.fails || len(requests) != 0 {
				t.Fatalf("ordinary call error=%v, want %v, hub requests=%d", result.IsError, tc.fails, len(requests))
			}
		})
	}
}

func TestManagerReadContractOrdinarySession(t *testing.T) {
	t.Parallel()
	root := newMessagingRoot(t, newMessagingAdapter(), nil)
	requests := managerReadContractTransport(t, root)
	for _, tc := range []struct{ tool, args string }{
		{"job_list", `{"hub":{"mode":"discovery"}}`},
		{"job_status", `{"hub":{"ref":"local:worker"}}`},
		{"read_transcript", `{"hub":{"ref":"local:worker"}}`},
		{"model_list", `{"hub":{"host":"local"}}`},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			result := root.reg.ExecuteCall(t.Context(), root.currentEnv(), llm.ToolCallData{ID: "forged", Name: tc.tool, Arguments: json.RawMessage(tc.args)})
			if !result.IsError || len(requests) != 0 {
				t.Fatalf("ordinary session admitted Manager read: error=%v, hub requests=%d", result.IsError, len(requests))
			}
		})
	}
}
