package interactiveartifacts

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestToolRequestContracts(t *testing.T) {
	for _, tt := range []struct {
		tool, raw string
		valid     bool
	}{
		{"artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"<script>not parsed as JS</script>"}`, true},
		{"artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"é\r\n","initialState":{"n":1.0}}`, true},
		{"artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"H","artifactId":"A","expectedSourceRevision":1,"expectedStateVersion":2}`, true},
		{"artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"H","artifactId":"A","expectedSourceRevision":1}`, false},
		{"artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"H","expectedStateVersion":1}`, false},
		{"artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"H","artifactId":"A","expectedSourceRevision":1,"expectedStateVersion":1,"initialState":{}}`, false},
		{"artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"H","initialState":null}`, false},
		{"artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"H","format":"markdown"}`, false},
		{"artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"H","formatVersion":2}`, false},
		{"artifact_read", `{"artifactId":"A"}`, true},
		{"artifact_read", `{"artifactId":"A","include":["source","state","diagnostics"],"sourceStartLine":2,"sourceEndLine":5}`, true},
		{"artifact_read", `{"artifactId":"A","sourceStartLine":1}`, false},
		{"artifact_read", `{"artifactId":"A","include":["state"],"sourceEndLine":2}`, false},
		{"artifact_read", `{"artifactId":"A","include":["source"],"sourceStartLine":3,"sourceEndLine":2}`, false},
		{"artifact_read", `{"artifactId":"A","include":["private"]}`, false},
		{"artifact_list", `{}`, true},
		{"artifact_list", `{"cursor":"opaque","limit":100}`, true},
		{"artifact_list", `{"limit":101}`, false},
		{"artifact_list", `{"limit":0}`, false},
		{"artifact_open", `{"artifactId":"A"}`, true},
		{"artifact_get_view", `{"artifactId":"A","knownSourceRevision":1,"knownStateVersion":1}`, true},
		{"artifact_get_view", `{"artifactId":"A","knownStateVersion":0}`, false},
		{"artifact_save_state", `{"artifactId":"A","mutationId":"M","expectedSourceRevision":1,"expectedStateVersion":1,"state":{"n":1.0}}`, true},
		{"artifact_save_state", `{"artifactId":"A","mutationId":"M","expectedSourceRevision":1,"expectedStateVersion":1,"state":[]}`, false},
		{"artifact_save_state", `{"artifactId":"A","mutationId":"M","expectedSourceRevision":1,"expectedStateVersion":1,"state":{"n":9007199254740993}}`, false},
		{"artifact_save_state", `{"artifactId":"A","mutationId":"M","expectedSourceRevision":9007199254740993,"expectedStateVersion":1,"state":{}}`, false},
		{"artifact_report_diagnostic", `{"artifactId":"A","sourceRevision":1,"message":"runtime","kind":"runtime"}`, true},
		{"artifact_report_diagnostic", `{"artifactId":"A","sourceRevision":1,"message":"runtime","kind":"validation"}`, true},
		{"artifact_report_diagnostic", `{"artifactId":"A","sourceRevision":1,"message":"runtime","kind":"other"}`, false},
	} {
		t.Run(tt.tool+"/"+tt.raw, func(t *testing.T) {
			_, err := ParseRequest(tt.tool, []byte(tt.raw), false)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v error=%v", tt.valid, err)
			}
		})
	}
}

func TestModelProjectionRejectsHostField(t *testing.T) {
	tools, err := Tools()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		raw := tool.InputSchema.(*jsonschema.Schema)
		projected, err := ModelSchema(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := projected.Properties["mutationId"]; ok || slices.Contains(projected.Required, "mutationId") {
			t.Fatalf("%s exposed mutationId", tool.Name)
		}
		if tool.Name == "artifact_publish" || tool.Name == "artifact_save_state" {
			if _, ok := raw.Properties["mutationId"]; !ok || !slices.Contains(raw.Required, "mutationId") {
				t.Fatal("public schema mutated")
			}
		}
	}
	request := []byte(`{"title":"T","summary":"S","html":"H"}`)
	if _, err := ParseRequest("artifact_publish", request, true); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseRequest("artifact_publish", []byte(`{"title":"T","summary":"S","html":"H","mutationId":"injected"}`), true); err == nil {
		t.Fatal("accepted model mutationId override")
	}
	if _, err := ParseRequest("artifact_publish", request, false); err == nil {
		t.Fatal("public service accepted missing mutationId")
	}
}

func TestToolVisibilityAndResourceAssociation(t *testing.T) {
	tools, err := Tools()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"artifact_publish": "model", "artifact_read": "model", "artifact_list": "model", "artifact_open": "model", "artifact_get_view": "app", "artifact_save_state": "app", "artifact_report_diagnostic": "app"}
	if len(tools) != len(want) {
		t.Fatalf("got %d tools", len(tools))
	}
	for _, tool := range tools {
		visibility, ok := want[tool.Name]
		if !ok {
			t.Fatalf("unexpected tool %s", tool.Name)
		}
		delete(want, tool.Name)
		ui := tool.Meta["ui"].(map[string]any)
		if !reflect.DeepEqual(ui["visibility"], []string{visibility}) {
			t.Fatalf("visibility for %s = %v", tool.Name, ui["visibility"])
		}
		uri, declared := ui["resourceUri"]
		if tool.Name == "artifact_open" {
			if uri != "ui://evener-artifacts/viewer-v1.html" {
				t.Fatal("wrong viewer resource")
			}
		} else if declared {
			t.Fatal("non-open tool declared viewer")
		}
		if tool.OutputSchema == nil {
			t.Fatalf("%s lacks typed output contract", tool.Name)
		}
	}
	if ViewerResource.URI != "ui://evener-artifacts/viewer-v1.html" || ViewerResource.MIMEType != "text/html;profile=mcp-app" {
		t.Fatal("wrong resource contract")
	}
}

func TestRequestInjectionAndBounds(t *testing.T) {
	for _, field := range []string{"namespaceId", "realmId", "principalId", "runtimeGeneration", "unexpected"} {
		raw := `{"artifactId":"A","` + field + `":"injected"}`
		if _, err := ParseRequest("artifact_open", []byte(raw), false); err == nil {
			t.Fatalf("accepted %s", field)
		}
	}
	for _, raw := range []string{
		`{"mutationId":"M","title":"T","summary":"S","html":"` + strings.Repeat("a", MaxSourceBytes+1) + `"}`,
		`{"mutationId":"M","title":"T","summary":"S","html":"H","initialState":{"x":"` + strings.Repeat("a", MaxStateBytes) + `"}}`,
		`{"artifactId":"A","sourceRevision":1,"message":"` + strings.Repeat("a", 4097) + `"}`,
	} {
		tool := "artifact_publish"
		if strings.Contains(raw, "message") {
			tool = "artifact_report_diagnostic"
		}
		if _, err := ParseRequest(tool, []byte(raw), false); err == nil {
			t.Fatal("accepted oversized body")
		}
	}
	if _, err := ParseRequest("artifact_open", []byte(`{"artifactId":"A","artifactId":"B"}`), false); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	if _, err := ParseRequest("unknown", []byte(`{}`), false); err == nil {
		t.Fatal("unknown tool accepted")
	}
}

func TestTypedRequestPreservesSourceAndState(t *testing.T) {
	parsed, err := ParseRequest("artifact_publish", []byte(`{"mutationId":"M","title":"T","summary":"S","html":"é\r\n<script>!</script>","initialState":{"n":1.0}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	request, ok := parsed.(*PublishRequest)
	if !ok {
		t.Fatalf("unexpected type %T", parsed)
	}
	if request.HTML != "é\r\n<script>!</script>" {
		t.Fatal("authored source bytes rewritten")
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(request.InitialState, &state); err != nil {
		t.Fatal(err)
	}
	if string(state["n"]) != "1.0" {
		t.Fatal("typed state lost raw number")
	}
	if request.Format != "html" || request.FormatVersion != 1 {
		t.Fatal("create defaults missing")
	}
	if string(request.InitialState) == "{}" {
		t.Fatal("initial state reset")
	}
	parsed, err = ParseRequest("artifact_publish", []byte(`{"mutationId":"M","title":"T","summary":"S","html":"H"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if string(parsed.(*PublishRequest).InitialState) != "{}" {
		t.Fatal("initial state default missing")
	}
	parsed, err = ParseRequest("artifact_list", []byte(`{}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.(*ListRequest).Limit != 20 {
		t.Fatal("list default missing")
	}
}

func TestResultSchemasValidateCompactReceipts(t *testing.T) {
	for _, tt := range []struct {
		raw   string
		valid bool
	}{
		{`{"status":"committed","mutationId":"M","artifactId":"A","sourceRevision":1,"stateVersion":2}`, true},
		{`{"status":"committed","mutationId":"M","artifactId":"A","sourceRevision":1,"stateVersion":2,"state":{"private":true}}`, false},
		{`{"status":"rejected","error":{"code":"SOURCE_CONFLICT","retryable":false,"sourceRevision":3,"stateVersion":4}}`, true},
		{`{"status":"rejected","error":{"code":"BUSY","retryable":true}}`, true},
		{`{"status":"rejected","error":{"code":"NOT_FOUND_OR_FORBIDDEN","retryable":false}}`, true},
		{`{"status":"rejected","error":{"code":"NOT_FOUND_OR_FORBIDDEN","retryable":false,"sourceRevision":3,"stateVersion":4}}`, false},
		{`{"status":"rejected","error":{"code":"UNKNOWN","retryable":false}}`, false},
	} {
		err := ValidateResult("artifact_save_state", []byte(tt.raw))
		if (err == nil) != tt.valid {
			t.Fatalf("valid=%v error=%v for %s", tt.valid, err, tt.raw)
		}
	}
}

func TestOpaqueIdentifiersMustBeNonempty(t *testing.T) {
	for _, raw := range []string{
		`{"artifactId":""}`,
		`{"mutationId":"","title":"T","summary":"S","html":"H"}`,
		`{"mutationId":"M","title":"T","summary":"S","html":"H","artifactId":"","expectedSourceRevision":1,"expectedStateVersion":1}`,
	} {
		tool := "artifact_publish"
		if raw == `{"artifactId":""}` {
			tool = "artifact_open"
		}
		if _, err := ParseRequest(tool, []byte(raw), false); err == nil {
			t.Fatalf("accepted empty identifier in %s", raw)
		}
	}
}

func TestConflictRetryabilityIsTerminal(t *testing.T) {
	for _, raw := range []string{
		`{"status":"rejected","error":{"code":"SOURCE_CONFLICT","retryable":true,"sourceRevision":3,"stateVersion":4}}`,
		`{"status":"rejected","error":{"code":"STATE_CONFLICT","retryable":true,"sourceRevision":3,"stateVersion":4}}`,
		`{"status":"rejected","error":{"code":"BUSY","retryable":false}}`,
	} {
		if err := ValidateResult("artifact_publish", []byte(raw)); err == nil {
			t.Fatalf("accepted incorrect retryability for %s", raw)
		}
	}
}

func TestVersionUsesExactSubmittedInteger(t *testing.T) {
	for _, token := range []string{"1.0", "1e0", "9007199254740991"} {
		request, err := ParseRequest("artifact_get_view", []byte(`{"artifactId":"A","knownStateVersion":`+token+`}`), false)
		if err != nil {
			t.Fatal(err)
		}
		if request.(*GetViewRequest).KnownStateVersion <= 0 {
			t.Fatal("version not decoded")
		}
	}
	for _, token := range []string{"0", "-1", "1.0000000000000000000001", "9007199254740991.1", "9007199254740993"} {
		if _, err := ParseRequest("artifact_get_view", []byte(`{"artifactId":"A","knownStateVersion":`+token+`}`), false); err == nil {
			t.Fatalf("accepted version %s", token)
		}
	}
}

func TestModelSchemaDetachesMutableNestedFields(t *testing.T) {
	public, err := inputSchema("artifact_publish")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := ModelSchema(public)
	if err != nil {
		t.Fatal(err)
	}
	projected.OneOf[1].Required[0] = "injected"
	projected.Properties["format"].Enum[0] = "markdown"
	*projected.Properties["expectedSourceRevision"].Maximum = 1
	originalArgs, err := ParseJSON([]byte(`{"mutationId":"M","title":"T","summary":"S","html":"H","format":"html","artifactId":"A","expectedSourceRevision":2,"expectedStateVersion":1}`), MaxRequestBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSchema(public, originalArgs); err != nil {
		t.Errorf("projected edits changed public update contract: %v", err)
	}
	public = &jsonschema.Schema{Type: "object", Properties: map[string]*jsonschema.Schema{"payload": {Enum: []any{map[string]any{"flag": true}}}, "constant": {Const: new(any(map[string]any{"flag": true}))}}}
	projected, err = ModelSchema(public)
	if err != nil {
		t.Fatal(err)
	}
	projected.Properties["payload"].Enum[0].(map[string]any)["flag"] = false
	(*projected.Properties["constant"].Const).(map[string]any)["flag"] = false
	if err := validateSchema(public, map[string]any{"payload": map[string]any{"flag": true}, "constant": map[string]any{"flag": true}}); err != nil {
		t.Errorf("projected enum/const value edit changed public schema: %v", err)
	}
}

func TestModelSchemaRejectsNonserializableContract(t *testing.T) {
	schema := &jsonschema.Schema{Enum: []any{func() {}}}
	if _, err := ModelSchema(schema); err == nil {
		t.Fatal("accepted a schema that cannot be transmitted as JSON")
	}
}
