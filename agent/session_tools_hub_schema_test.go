package agent

import (
	"encoding/json"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
)

func TestManagerSchemaArgumentBranches(t *testing.T) {
	t.Parallel()
	root, wire := managerProviderSchemas(t)
	cases := []struct {
		tool, name, args string
		valid            bool
	}{
		{"job_list", "ordinary", `{}`, true},
		{"job_list", "hub", `{"hub":{"mode":"discovery"}}`, true},
		{"job_list", "mixed", `{"hub":{"mode":"discovery"},"limit":1}`, false},
		{"job_list", "missing_mode", `{"hub":{}}`, false},
		{"job_list", "wrong_mode", `{"hub":{"mode":"other"}}`, false},
		{"job_status", "ordinary", `{"target":"job_1"}`, true},
		{"job_status", "hub", `{"hub":{"ref":"local:worker"}}`, true},
		{"job_status", "mixed", `{"hub":{"ref":"local:worker"},"target":"job_1"}`, false},
		{"job_status", "missing", `{}`, false},
		{"job_status", "malformed", `{"hub":{"ref":1}}`, false},
		{"read_transcript", "ordinary_unavailable_in_profile", `{"transcript_ref":"job:job_1"}`, false},
		{"read_transcript", "hub", `{"hub":{"ref":"local:worker","itemLimit":2}}`, true},
		{"read_transcript", "mixed", `{"hub":{"ref":"local:worker"},"format":"jsonl"}`, false},
		{"read_transcript", "missing_ref", `{"hub":{}}`, false},
		{"read_transcript", "malformed", `{"hub":{"ref":"local:worker","itemLimit":"two"}}`, false},
		{"model_list", "ordinary_unavailable_in_profile", `{}`, false},
		{"model_list", "hub", `{"hub":{"host":"local"}}`, true},
		{"model_list", "mixed", `{"hub":{"host":"local"},"max_count":1}`, false},
		{"model_list", "missing_host", `{"hub":{}}`, false},
		{"model_list", "malformed", `{"hub":{"host":false}}`, false},
		{"job_watch", "ordinary", `{"operation":"list"}`, true},
		{"job_watch", "hub", `{"operation":"create","hub":{"ref":"local:worker","events":["status"]}}`, true},
		{"job_watch", "missing_operation", `{"hub":{"ref":"local:worker","events":[]}}`, false},
		{"job_watch", "wrong_operation", `{"operation":"other"}`, false},
		{"job_watch", "malformed", `{"operation":"create","hub":{"ref":1,"events":[]}}`, false},
		{"delegate", "ordinary", `{"prompt":"work"}`, true},
		{"delegate", "hub", `{"prompt":"work","hub":{"cwd":"/tmp","model":"model","watch":{"events":[]}}}`, true},
		{"delegate", "mixed", `{"prompt":"work","max_wait_ms":1,"hub":{"cwd":"/tmp","model":"model","watch":{"events":[]}}}`, false},
		{"delegate", "missing_prompt", `{"hub":{"cwd":"/tmp","model":"model","watch":{"events":[]}}}`, false},
		{"delegate", "malformed", `{"prompt":"work","hub":{"cwd":"/tmp","model":"model","watch":{"events":true}}}`, false},
		{"delegate_send", "ordinary", `{"to":"dlg_1","message":"work"}`, true},
		{"delegate_send", "ordinary_wait", `{"to":"dlg_1","message":"work","max_wait_ms":1}`, true},
		{"delegate_send", "hub_message", `{"to":"local:worker","input_mode":"start","message":"work","client_mutation_id":"id","expected_instance_id":"instance"}`, true},
		{"delegate_send", "hub_input", `{"to":"local:worker","input_mode":"queue","input":[{"type":"text","text":"work"}],"client_mutation_id":"id","expected_instance_id":"instance"}`, true},
		{"delegate_send", "both_input_forms", `{"to":"local:worker","input_mode":"steer","message":"work","input":[],"client_mutation_id":"id","expected_instance_id":"instance"}`, false},
		{"delegate_send", "mixed_wait", `{"to":"local:worker","input_mode":"start","message":"work","client_mutation_id":"id","expected_instance_id":"instance","max_wait_ms":1}`, false},
		{"delegate_send", "mixed_ordinary", `{"to":"dlg_1","message":"work","client_mutation_id":"id"}`, false},
		{"delegate_send", "missing_receipt_identity", `{"to":"local:worker","input_mode":"start","message":"work"}`, false},
		{"delegate_send", "wrong_mode", `{"to":"local:worker","input_mode":"other","message":"work","client_mutation_id":"id","expected_instance_id":"instance"}`, false},
		{"delegate_send", "malformed", `{"to":"dlg_1","message":false}`, false},
		{"job_stop", "ordinary", `{"target":"job_1"}`, true},
		{"job_stop", "interrupt", `{"hub":{"mode":"interrupt","arguments":{"ref":"local:worker","clientMutationId":"id","expectedInstanceId":"instance"}}}`, true},
		{"job_stop", "delegate", `{"hub":{"mode":"delegate","arguments":{"ref":"local:worker","delegateId":"dlg_1"}}}`, true},
		{"job_stop", "owned_delegate", `{"hub":{"mode":"owned_delegate","arguments":{"ref":"local:worker","delegateId":"dlg_1"}}}`, true},
		{"job_stop", "job", `{"hub":{"mode":"job","arguments":{"ref":"local:worker","jobId":"job_1"}}}`, true},
		{"job_stop", "shutdown", `{"hub":{"mode":"shutdown","arguments":{"ref":"local:worker"}}}`, true},
		{"job_stop", "force_stop", `{"hub":{"mode":"force_stop","arguments":{"ref":"local:worker"}}}`, true},
		{"job_stop", "mixed", `{"target":"job_1","hub":{"mode":"shutdown","arguments":{"ref":"local:worker"}}}`, false},
		{"job_stop", "missing", `{}`, false},
		{"job_stop", "wrong_mode", `{"hub":{"mode":"other","arguments":{"ref":"local:worker"}}}`, false},
		{"job_stop", "wrong_payload", `{"hub":{"mode":"job","arguments":{"ref":"local:worker","delegateId":"dlg_1"}}}`, false},
		{"job_stop", "malformed", `{"hub":{"mode":"shutdown","arguments":{"ref":1}}}`, false},
		{"session_control", "clear", `{"action":"clear","arguments":{"ref":"local:worker","clientMutationId":"id","expectedInstanceId":"instance"}}`, true},
		{"session_control", "resume_optional_fields", `{"action":"resume","arguments":{"ref":"local:worker"}}`, true},
		{"session_control", "missing", `{"action":"clear"}`, false},
		{"session_control", "wrong_action", `{"action":"other","arguments":{"ref":"local:worker"}}`, false},
		{"session_control", "wrong_payload", `{"action":"clear","arguments":{"ref":"local:worker","name":"other"}}`, false},
		{"session_control", "forged_proof", `{"action":"clear","arguments":{"ref":"local:worker","managerProof":{}}}`, false},
		{"session_control", "malformed", `{"action":"clear","arguments":{"ref":false}}`, false},
	}
	compiled := map[string]*jsonschema.Schema{}
	for _, name := range managerComposedSchemaNames {
		raw, err := json.Marshal(wire[name])
		if err != nil {
			t.Fatal(err)
		}
		compiled[name], err = jsonschema.CompileString("https://manager.test/"+name+".json", string(raw))
		if err != nil {
			t.Fatalf("compile serialized %s: %v", name, err)
		}
	}
	for _, tc := range cases {
		t.Run(tc.tool+"/"+tc.name, func(t *testing.T) {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.args), &args); err != nil {
				t.Fatal(err)
			}
			if err := root.reg.Get(tc.tool).Schema.Validate(args); (err == nil) != tc.valid {
				t.Errorf("registered schema valid=%v, want %v: %v", err == nil, tc.valid, err)
			}
			args["intent"] = "Checking Manager argument branches."
			if err := compiled[tc.tool].Validate(args); (err == nil) != tc.valid {
				t.Errorf("serialized Responses schema valid=%v, want %v: %v", err == nil, tc.valid, err)
			}
		})
	}
}
