package chatcompletions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

const closeTag = "</parameter>"

func fieldsOf(t *testing.T, out string) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &fields); err != nil {
		t.Fatalf("rescued args are not valid JSON: %v\n%s", err, out)
	}
	return fields
}

func wantToken(t *testing.T, fields map[string]json.RawMessage, key, want string) {
	t.Helper()
	got, ok := fields[key]
	if !ok {
		t.Fatalf("missing field %q in %v", key, fields)
	}
	if string(got) != want {
		t.Fatalf("field %q token = %q, want %q", key, got, want)
	}
}

func wantUnchanged(t *testing.T, raw string) {
	t.Helper()
	if got := rescueClaudeXMLArgs(raw, nil); got != raw {
		t.Fatalf("rescue changed input\n in=%q\nout=%q", raw, got)
	}
}

// fieldParams builds a tool parameters schema declaring one property of the
// given JSON Schema type.
func fieldParams(name string, typ any) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{name: map[string]any{"type": typ}},
	}
}

// writeFileToolParams is write_file's parameter schema shape: both file_path
// and content are strings, and an object-typed sibling lets the wire tests
// prove the schema reached the rescue. The two wire tests share it so they
// keep proving the same thing.
func writeFileToolParams() []llm.ToolDefinition {
	return []llm.ToolDefinition{{Name: "write_file", Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
			"content":   map[string]any{"type": "string"},
			"metadata":  map[string]any{"type": "object"},
		},
	}}}
}

func TestRescueClaudeXMLArgsPreservesUntouchedRawBytes(t *testing.T) {
	t.Parallel()
	raw := `{"action":"update\">\n<parameter name=\"tasks\">[{\"id\":2}]</parameter>","note":"bad` + "\xfe" + `","n":1.50}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "action", `"update"`)
	wantToken(t, fields, "tasks", `[{"id":2}]`)
	wantToken(t, fields, "note", `"bad`+"\xfe"+`"`)
	wantToken(t, fields, "n", `1.50`)
}

func TestRescueClaudeXMLArgsParsedAnyJSONStringPreservesRawBytes(t *testing.T) {
	t.Parallel()
	raw := `{"payload":"{\"n\":1.50,\"s\":\"bad` + "\xfe" + `\"}","other":"x` + "\xfe" + `"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, fieldParams("payload", "object")))
	wantToken(t, fields, "payload", `{"n":1.50,"s":"bad`+"\xfe"+`"}`)
	wantToken(t, fields, "other", `"x`+"\xfe"+`"`)

	raw = `{"payload":"[1.50,\"\\u0041\",\"bad` + "\xfe" + `\"]","other":"x` + "\xfe" + `"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw, fieldParams("payload", "array")))
	wantToken(t, fields, "payload", `[1.50,"\u0041","bad`+"\xfe"+`"]`)
	wantToken(t, fields, "other", `"x`+"\xfe"+`"`)
}

func TestRescueClaudeXMLArgsNoRescueReturnsInputUnchanged(t *testing.T) {
	t.Parallel()
	wantUnchanged(t, `{"note":"bad`+"\xfe"+`","n":1.50}`)
	wantUnchanged(t, `{"x":"<parameter foo\u003e"}`)
	wantUnchanged(t, `null`)
	wantUnchanged(t, `[1,2,3]`)
	wantUnchanged(t, `42`)
	wantUnchanged(t, `"x"`)
	wantUnchanged(t, `{}`)
}

func TestRescueClaudeXMLArgsOrdinaryXMLSemantics(t *testing.T) {
	t.Parallel()
	raw := `{"action":"go\">\n<parameter name='x'>1.50</parameter><parameter name='action'>child</parameter>","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "action", `"go"`)
	wantToken(t, fields, "x", `1.50`)
	wantToken(t, fields, "note", `"ok"`)

	raw = `{"action":"update\">\n<parameter name=\"x\">plain text","q":"v"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "action", `"update"`)
	wantToken(t, fields, "x", `"plain text"`)
	wantToken(t, fields, "q", `"v"`)

	raw = `{"action":"a\">\n<parameter name=\"v\">  plain text  </parameter>","q":"v"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "v", `"plain text"`)

	raw = `{"action":"a\">\n<parameter name=\"v\">\u00a0 1 \u00a0</parameter>","q":"v"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "v", `1`)

	raw = `{"action":"a\">\n<parameter name=\"a\">1<parameter name=\"b\">two"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "a", `1`)
	wantToken(t, fields, "b", `"two"`)

	raw = `{"action":"a\">\n<parameter name=\"v\">x</parameter></parameter>","q":"v"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "v", `"x`+closeTag+`"`)

	raw = `{"action":"a\">b\">\n<parameter name=\"x\">1</parameter>","note":"ok"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "action", `"a"`)
	wantToken(t, fields, "x", `1`)
}

func TestRescueClaudeXMLArgsCarrierPrefixInvalidByte(t *testing.T) {
	t.Parallel()
	raw := `{"action":"up` + "\xfe" + `da\">\n<parameter name=\"x\">1","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "action", `"up`+"\xfe"+`da"`)
	wantToken(t, fields, "x", `1`)
}

func TestRescueClaudeXMLArgsPreservesUntouchedEscapedSpellings(t *testing.T) {
	t.Parallel()
	raw := `{"action":"a\">\n<parameter name=\"x\">1</parameter>","emoji":"\ud83d\ude00","html":"a<b&c"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "emoji", `"\ud83d\ude00"`)
	wantToken(t, fields, "html", `"a<b&c"`)
}

func TestRescueClaudeXMLArgsBothShapes(t *testing.T) {
	t.Parallel()
	raw := `{"payload":"{\"n\":1.50,\"s\":\"bad` + "\xfe" + `\"}","action":"go\">\n<parameter name=\"x\">1</parameter>","note":"x` + "\xfe" + `"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, fieldParams("payload", "object")))
	wantToken(t, fields, "payload", `{"n":1.50,"s":"bad`+"\xfe"+`"}`)
	wantToken(t, fields, "action", `"go"`)
	wantToken(t, fields, "x", `1`)
	wantToken(t, fields, "note", `"x`+"\xfe"+`"`)
}

func TestRescueClaudeXMLArgsCarrierAndExtractedBytes(t *testing.T) {
	t.Parallel()
	raw := `{"action":"up` + "\xfe" + `da\">\n<parameter name=\"n\">1.50</parameter><parameter name=\"s\">bad` + "\xfe" + `</parameter><parameter name=\"h\">a<b&c</parameter>","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "action", `"up`+"\xfe"+`da"`)
	wantToken(t, fields, "n", `1.50`)
	wantToken(t, fields, "s", `"bad`+"\xfe"+`"`)
	wantToken(t, fields, "h", `"a<b&c"`)
	wantToken(t, fields, "note", `"ok"`)
}

func TestRescueClaudeXMLArgsMultiCarrierPrecedence(t *testing.T) {
	t.Parallel()
	raw := `{"a":"go\">\n<parameter name=\"x\">1</parameter>","b":"go\">\n<parameter name=\"x\">2</parameter>"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "x", `2`)
}

func TestRescueClaudeXMLArgsXMLGateSemantics(t *testing.T) {
	t.Parallel()
	wantUnchanged(t, `{"x":"\u003cparameter\r\nname=\"x\"\u003e1"}`)
	wantUnchanged(t, `{"payload":"{\"a\":1}","b":"\u003cparameter foo\u003e"}`)
	out := rescueClaudeXMLArgs(`{"payload":"{\"a\":1}","b":"plain"}`, fieldParams("payload", "object"))
	fields := fieldsOf(t, out)
	wantToken(t, fields, "payload", `{"a":1}`)
}

func TestRescueClaudeXMLArgsParsedAnyPreservesInnerEscapeSpelling(t *testing.T) {
	t.Parallel()
	raw := `{"payload":"{\"s\":\"\\u0041\",\"n\":1.50}","other":"x"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, fieldParams("payload", "object")))
	wantToken(t, fields, "payload", `{"s":"\u0041","n":1.50}`)
}

func TestRescueClaudeXMLArgsExtractedValidJSONKeepsEscapeSpelling(t *testing.T) {
	t.Parallel()
	raw := `{"action":"go\">\n<parameter name=\"j\">{\"s\":\"\\u0041\"}</parameter>","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "j", `{"s":"\u0041"}`)
}

func TestRescueClaudeXMLArgsClosingTagByteOffsets(t *testing.T) {
	t.Parallel()
	raw := `{"action":"go\">\n<parameter name=\"x\">İ</parameter>","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "x", `"İ"`)

	raw = `{"action":"go\">\n` + "<parameter name=\\\"x\\\">" + "\u212A" + closeTag + `","note":"ok"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "x", "\"\u212A\"")

	raw = `{"action":"go\">\n<parameter name=\"x\">` + "\xfe\xfe\xfe\xfe\xfe\xfe\xfe" + `</parameter>","note":"ok"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "x", `"`+"\xfe\xfe\xfe\xfe\xfe\xfe\xfe"+`"`)
}

func TestRescueClaudeXMLArgsExtractedOverflowNumberStaysString(t *testing.T) {
	t.Parallel()
	raw := `{"action":"go\">\n` + "<parameter name=\\\"x\\\">1e999" + closeTag + `","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "x", `"1e999"`)

	raw = `{"action":"go\">\n` + "<parameter name=\\\"x\\\">{\\\"a\\\":1e999}" + closeTag + `","note":"ok"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "x", `"{\"a\":1e999}"`)
}

func TestRescueClaudeXMLArgsReplacedFieldWithCompleteOpenIsNotInspected(t *testing.T) {
	t.Parallel()
	inner := `{"a":"` + "<parameter name=\\\"x\\\">y" + `"}`
	rawBytes, err := json.Marshal(map[string]string{"p": inner})
	if err != nil {
		t.Fatal(err)
	}
	fields := fieldsOf(t, rescueClaudeXMLArgs(string(rawBytes), fieldParams("p", "object")))
	wantToken(t, fields, "p", inner)
	if _, ok := fields["x"]; ok {
		t.Fatalf("replaced field was inspected for XML: %v", fields)
	}
}

func TestFromChatCompletionResponseRescuePreservesNonCanonicalJSON(t *testing.T) {
	t.Parallel()
	args := `{"action":"update\">\n` + "<parameter name=\\\"x\\\">" + `1` + closeTag + `","n":1.50}`
	raw := map[string]any{
		"id":    "resp_1",
		"model": "test-model",
		"choices": []any{
			map[string]any{
				"index":         float64(0),
				"finish_reason": "tool_calls",
				"message": map[string]any{
					"role": "assistant",
					"tool_calls": []any{
						map[string]any{
							"id":   "call_1",
							"type": "function",
							"function": map[string]any{
								"name":      "update",
								"arguments": args,
							},
						},
					},
				},
			},
		},
	}
	resp, err := fromChatCompletionResponse(raw, nil, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	fields := fieldsOf(t, string(calls[0].Arguments))
	wantToken(t, fields, "n", `1.50`)
	wantToken(t, fields, "action", `"update"`)
	wantToken(t, fields, "x", `1`)
}

// TestRescueClaudeXMLArgsEmptyReturnsEmpty pins the empty-arguments short
// circuit: nothing to inspect means nothing to change.
func TestRescueClaudeXMLArgsEmptyReturnsEmpty(t *testing.T) {
	t.Parallel()
	if got := rescueClaudeXMLArgs("", nil); got != "" {
		t.Fatalf("rescue of empty arguments = %q, want empty", got)
	}
}

// TestRescueClaudeXMLArgsKeepsStringFieldJSONText reproduces #7426: a string
// field whose text happens to be JSON (write_file's content writing a JSON
// file) must stay a JSON string. Without the called tool's schema the rescue
// cannot know the field is a string, so it must not rewrite it.
func TestRescueClaudeXMLArgsKeepsStringFieldJSONText(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"file_path":"forbidden.json","content":"{\"value\":42}\n"}`,
		`{"file_path":"list.json","content":"[1,2,3]","note":"ok"}`,
		`{"file_path":"nested.json","content":"{\"a\":{\"b\":[true,null]}}"}`,
	} {
		if got := rescueClaudeXMLArgs(raw, nil); got != raw {
			t.Fatalf("rescue rewrote string field JSON text\n in=%s\nout=%s", raw, got)
		}
		// The callers' schema says content is a string, so the same input
		// still keeps its text once the schema is supplied.
		params := map[string]any{"type": "object", "properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
			"content":   map[string]any{"type": "string"},
			"note":      map[string]any{"type": "string"},
		}}
		if got := rescueClaudeXMLArgs(raw, params); got != raw {
			t.Fatalf("rescue rewrote a string-typed field\n in=%s\nout=%s", raw, got)
		}
	}
}

// TestRescueClaudeXMLArgsSecondOrderSchemaGate pins the #7426 gate together
// with the coercion it must keep: only fields the schema types as an array or
// object inline JSON text. String, number, boolean, and undeclared fields keep
// their wire token, and correctly typed values pass through untouched.
func TestRescueClaudeXMLArgsSecondOrderSchemaGate(t *testing.T) {
	t.Parallel()
	params := map[string]any{"type": "object", "properties": map[string]any{
		"obj":     map[string]any{"type": "object"},
		"arr":     map[string]any{"type": "array"},
		"nullarr": map[string]any{"type": []string{"array", "null"}},
		"nullobj": map[string]any{"type": []any{"object", "null"}},
		"str":     map[string]any{"type": "string"},
		"num":     map[string]any{"type": "number"},
		"flag":    map[string]any{"type": "boolean"},
	}}
	raw := `{"obj":"{\"a\":1}","arr":"[1,2]","nullarr":"[true]","nullobj":"{\"c\":3}","str":"{\"a\":1}","num":"[9]","flag":"{\"b\":2}","other":"[3]"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, params))
	wantToken(t, fields, "obj", `{"a":1}`)
	wantToken(t, fields, "arr", `[1,2]`)
	wantToken(t, fields, "nullarr", `[true]`)
	wantToken(t, fields, "nullobj", `{"c":3}`)
	wantToken(t, fields, "str", `"{\"a\":1}"`)
	wantToken(t, fields, "num", `"[9]"`)
	wantToken(t, fields, "flag", `"{\"b\":2}"`)
	wantToken(t, fields, "other", `"[3]"`)

	// Already-typed arguments are never rewritten.
	typed := `{"n":42,"b":true,"arr":[1,2],"obj":{"k":"v"},"s":"text"}`
	if got := rescueClaudeXMLArgs(typed, params); got != typed {
		t.Fatalf("typed arguments rewritten\n in=%s\nout=%s", typed, got)
	}
}

// completeWithTools runs the production Protocol.Complete path for a scripted
// response body with the given tools on the request, returning the decoded
// tool call arguments. Running through Complete is what proves the adapter
// forwards req.Tools to the decode, not just that the decoder honors tools.
func completeWithTools(t *testing.T, body []byte, tools []llm.ToolDefinition) []byte {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	res := resolved(func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
	res.Transport = registry.Transport{Auth: registry.AuthBearer, BaseURL: srv.URL + "/v1", Endpoint: "/chat/completions", StreamEndpoint: "/chat/completions", ModelsEndpoint: "/models", CountTokensEndpoint: registry.EndpointUnsupported}
	res.Credential = registry.Credential{Value: "k-1", Source: "api_key"}
	p := &Protocol{Client: srv.Client()}
	req := userReq("hi")
	req.Tools = tools
	resp, err := p.Complete(context.Background(), llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	calls := resp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	return []byte(calls[0].Arguments)
}

// TestProtocolCompleteKeepsWriteFileJSONContentString pins #7426 at the wire
// decode: a chatcompletions tool call whose arguments carry a JSON file as
// write_file's string content must reach the tool with content still a
// string. The object-typed metadata field must still be unwrapped, which
// proves Complete forwarded the tool schema to the rescue rather than merely
// skipping the rescue.
func TestProtocolCompleteKeepsWriteFileJSONContentString(t *testing.T) {
	t.Parallel()
	args := `{"file_path":"forbidden.json","content":"{\"value\":42}\n","metadata":"{\"owner\":\"ci\"}"}`
	argsJSON := completeWithTools(t, rawArgsBody(t, args), writeFileToolParams())
	var decoded struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
		Metadata struct {
			Owner string `json:"owner"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(argsJSON, &decoded); err != nil {
		t.Fatalf("arguments no longer decode with content as a string: %v\n%s", err, argsJSON)
	}
	if decoded.FilePath != "forbidden.json" || decoded.Content != "{\"value\":42}\n" || decoded.Metadata.Owner != "ci" {
		t.Fatalf("arguments = %+v, want the JSON file text preserved", decoded)
	}
}

// TestProtocolCompleteKeepsWriteFileLiteralXMLContent pins the Claude-XML
// scanner off legitimate file data: write_file content that is complete valid
// JSON text holding a `<parameter>` element (with the `">` that used to look
// like a corruption boundary) is a JSON string field, not a corrupt XML
// carrier, and must reach the tool verbatim.
func TestProtocolCompleteKeepsWriteFileLiteralXMLContent(t *testing.T) {
	t.Parallel()
	content := `{"xml":"<tool name=\"write_file\"><parameter name='content'>example</parameter></tool>"}`
	args := jsonOf(t, map[string]string{"file_path": "literal.xml.json", "content": content})
	got := completeWithTools(t, rawArgsBody(t, args), writeFileToolParams())
	var decoded struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("arguments no longer decode with content as a string: %v\n%s", err, got)
	}
	if decoded.FilePath != "literal.xml.json" || decoded.Content != content {
		t.Fatalf("arguments = %+v, want the literal XML content preserved", decoded)
	}
}

// TestStreamKeepsWriteFileJSONContentString is the streaming twin of the
// non-stream regression: decodeStream must pass the request's tool schemas to
// the rescue, so a JSON file carried as write_file's string content survives
// while the object-typed metadata field is still unwrapped.
func TestStreamKeepsWriteFileJSONContentString(t *testing.T) {
	t.Parallel()
	args := `{"file_path":"forbidden.json","content":"{\"value\":42}\n","metadata":"{\"owner\":\"ci\"}"}`
	srv, _ := server(t, http.StatusOK, rawArgsStreamSSE(args))
	res := liveRes(srv, func(c *registry.Caps) { c.FinishReasonMap = map[string]string{"tool_calls": "tool_calls"} })
	p := &Protocol{Client: srv.Client()}
	req := userReq("hi")
	req.Tools = writeFileToolParams()
	s, err := p.Stream(context.Background(), llm.ShapeRequest(req, res), res)
	if err != nil {
		t.Fatal(err)
	}
	var final *llm.Response
	for ev := range s.Events() {
		if ev.Type == llm.StreamEventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == llm.StreamEventFinish {
			final = ev.Response
		}
	}
	if final == nil {
		t.Fatal("stream ended without a finish event")
	}
	calls := final.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(calls))
	}
	var got struct {
		Content  string `json:"content"`
		Metadata struct {
			Owner string `json:"owner"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(calls[0].Arguments, &got); err != nil {
		t.Fatalf("arguments no longer decode with content as a string: %v\n%s", err, calls[0].Arguments)
	}
	if got.Content != "{\"value\":42}\n" || got.Metadata.Owner != "ci" {
		t.Fatalf("arguments = %+v, want the JSON file text preserved and metadata unwrapped", got)
	}
}

// TestRescueClaudeXMLArgsSecondOrderInvalidJSONKept pins the second-order
// rescue's decode guard: a string value that merely looks like JSON but does
// not parse is left exactly as it was.
func TestRescueClaudeXMLArgsSecondOrderInvalidJSONKept(t *testing.T) {
	t.Parallel()
	params := fieldParams("payload", "object")
	for _, raw := range []string{`{"payload":"[oops"}`, `{"payload":"{oops"}`} {
		if got := rescueClaudeXMLArgs(raw, params); got != raw {
			t.Fatalf("rescue changed invalid JSON text\n in=%q\nout=%q", raw, got)
		}
	}
}

// TestRescueClaudeXMLArgsCarrierWithoutQuoteBoundary pins the split fallback:
// when a carrier's XML open tag has no `">` before it, the split happens at
// the tag itself, leaving the carrier empty.
func TestRescueClaudeXMLArgsCarrierWithoutQuoteBoundary(t *testing.T) {
	t.Parallel()
	raw := `{"action":"<parameter name=\"x\">1","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "action", `""`)
	wantToken(t, fields, "x", `1`)
	wantToken(t, fields, "note", `"ok"`)
}

// TestRescueClaudeXMLArgsJSONTextFieldSkipsXMLScan pins the per-field XML gate:
// a field whose value is complete valid JSON object or array text is the
// field's own string content when the schema does not type it as a container,
// so its <parameter> text is data and is never scanned — even when it contains
// a `">` that looks like a corruption boundary. A carrier that is not
// complete JSON text still rescues, with or without the legacy `">` boundary.
func TestRescueClaudeXMLArgsJSONTextFieldSkipsXMLScan(t *testing.T) {
	t.Parallel()
	params := fieldParams("content", "string")
	for _, content := range []string{
		`{"xml":"<tool name=\"write_file\"><parameter name='content'>example</parameter></tool>"}`,
		`["<parameter name='content'>example</parameter>"]`,
	} {
		raw := jsonOf(t, map[string]string{"file_path": "a.json", "content": content})
		if got := rescueClaudeXMLArgs(raw, params); got != raw {
			t.Fatalf("JSON text field was scanned for XML carriers\n in=%s\nout=%s", raw, got)
		}
	}
	for _, raw := range []string{
		`{"action":"<parameter name=\"x\">1"}`,
		`{"action":"go\">\n<parameter name=\"x\">1"}`,
	} {
		fields := fieldsOf(t, rescueClaudeXMLArgs(raw, params))
		wantToken(t, fields, "x", `1`)
	}
}

// TestRescueClaudeXMLArgsControlBytesRoundTrip pins the byte-preserving
// escape handling on both sides of the rescue: control escapes in the wire
// strings decode to their single bytes and re-encode to exactly JSON's
// required escapes — named ones for \b \f \n \r \t, \u00XX for every other
// control byte.
func TestRescueClaudeXMLArgsControlBytesRoundTrip(t *testing.T) {
	t.Parallel()
	raw := `{"action":"go\">\n<parameter name=\"x\">a\nb\bc\fd\re\tf` + `\u0001` + `g</parameter>","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "action", `"go"`)
	wantToken(t, fields, "x", `"a\nb\bc\fd\re\tf\u0001g"`)
	wantToken(t, fields, "note", `"ok"`)
}

// TestRescueClaudeXMLArgsUnpairedSurrogates pin the lone-surrogate handling:
// a high surrogate with no valid low partner and a lone low surrogate both
// decode to U+FFFD (stdlib parity), one per unpaired escape, rather than
// rejecting the arguments.
func TestRescueClaudeXMLArgsUnpairedSurrogates(t *testing.T) {
	t.Parallel()
	raw := `{"action":"go\">\n<parameter name=\"hi\">` + `\ud800` + `\udb00` + `x</parameter>","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "hi", "\"\uFFFD\uFFFDx\"")

	raw = `{"action":"go\">\n<parameter name=\"lo\">` + `\udc00` + `y</parameter>","note":"ok"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "lo", "\"\uFFFDy\"")
}

// TestRescueClaudeXMLArgsUppercaseHexEscape pins that \uXXXX escapes with
// uppercase hex digits decode — OpenRouter-style listings are not normalized
// to lowercase.
func TestRescueClaudeXMLArgsUppercaseHexEscape(t *testing.T) {
	t.Parallel()
	raw := `{"action":"go\">\n<parameter name=\"up\">` + `\uABCD` + `z</parameter>","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw, nil))
	wantToken(t, fields, "up", "\"\uABCDz\"")
}
