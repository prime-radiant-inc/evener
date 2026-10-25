package chatcompletions

import (
	"encoding/json"
	"testing"
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
	if got := rescueClaudeXMLArgs(raw); got != raw {
		t.Fatalf("rescue changed input\n in=%q\nout=%q", raw, got)
	}
}

func TestRescueClaudeXMLArgsPreservesUntouchedRawBytes(t *testing.T) {
	raw := `{"action":"update\">\n<parameter name=\"tasks\">[{\"id\":2}]</parameter>","note":"bad` + "\xfe" + `","n":1.50}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "action", `"update"`)
	wantToken(t, fields, "tasks", `[{"id":2}]`)
	wantToken(t, fields, "note", `"bad`+"\xfe"+`"`)
	wantToken(t, fields, "n", `1.50`)
}

func TestRescueClaudeXMLArgsParsedAnyJSONStringPreservesRawBytes(t *testing.T) {
	raw := `{"payload":"{\"n\":1.50,\"s\":\"bad` + "\xfe" + `\"}","other":"x` + "\xfe" + `"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "payload", `{"n":1.50,"s":"bad`+"\xfe"+`"}`)
	wantToken(t, fields, "other", `"x`+"\xfe"+`"`)

	raw = `{"payload":"[1.50,\"\\u0041\",\"bad` + "\xfe" + `\"]","other":"x` + "\xfe" + `"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "payload", `[1.50,"\u0041","bad`+"\xfe"+`"]`)
	wantToken(t, fields, "other", `"x`+"\xfe"+`"`)
}

func TestRescueClaudeXMLArgsNoRescueReturnsInputUnchanged(t *testing.T) {
	wantUnchanged(t, `{"note":"bad`+"\xfe"+`","n":1.50}`)
	wantUnchanged(t, `{"x":"<parameter foo\u003e"}`)
	wantUnchanged(t, `null`)
	wantUnchanged(t, `[1,2,3]`)
	wantUnchanged(t, `42`)
	wantUnchanged(t, `"x"`)
	wantUnchanged(t, `{}`)
}

func TestRescueClaudeXMLArgsOrdinaryXMLSemantics(t *testing.T) {
	raw := `{"action":"go\">\n<parameter name='x'>1.50</parameter><parameter name='action'>child</parameter>","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "action", `"go"`)
	wantToken(t, fields, "x", `1.50`)
	wantToken(t, fields, "note", `"ok"`)

	raw = `{"action":"update\">\n<parameter name=\"x\">plain text","q":"v"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "action", `"update"`)
	wantToken(t, fields, "x", `"plain text"`)
	wantToken(t, fields, "q", `"v"`)

	raw = `{"action":"a\">\n<parameter name=\"v\">  plain text  </parameter>","q":"v"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "v", `"plain text"`)

	raw = `{"action":"a\">\n<parameter name=\"v\">\u00a0 1 \u00a0</parameter>","q":"v"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "v", `1`)

	raw = `{"action":"a\">\n<parameter name=\"a\">1<parameter name=\"b\">two"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "a", `1`)
	wantToken(t, fields, "b", `"two"`)

	raw = `{"action":"a\">\n<parameter name=\"v\">x</parameter></parameter>","q":"v"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "v", `"x`+closeTag+`"`)

	raw = `{"action":"a\">b\">\n<parameter name=\"x\">1</parameter>","note":"ok"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "action", `"a"`)
	wantToken(t, fields, "x", `1`)
}

func TestRescueClaudeXMLArgsCarrierPrefixInvalidByte(t *testing.T) {
	raw := `{"action":"up` + "\xfe" + `da\">\n<parameter name=\"x\">1","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "action", `"up`+"\xfe"+`da"`)
	wantToken(t, fields, "x", `1`)
}

func TestRescueClaudeXMLArgsPreservesUntouchedEscapedSpellings(t *testing.T) {
	raw := `{"action":"a\">\n<parameter name=\"x\">1</parameter>","emoji":"\ud83d\ude00","html":"a<b&c"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "emoji", `"\ud83d\ude00"`)
	wantToken(t, fields, "html", `"a<b&c"`)
}

func TestRescueClaudeXMLArgsBothShapes(t *testing.T) {
	raw := `{"payload":"{\"n\":1.50,\"s\":\"bad` + "\xfe" + `\"}","action":"go\">\n<parameter name=\"x\">1</parameter>","note":"x` + "\xfe" + `"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "payload", `{"n":1.50,"s":"bad`+"\xfe"+`"}`)
	wantToken(t, fields, "action", `"go"`)
	wantToken(t, fields, "x", `1`)
	wantToken(t, fields, "note", `"x`+"\xfe"+`"`)
}

func TestRescueClaudeXMLArgsCarrierAndExtractedBytes(t *testing.T) {
	raw := `{"action":"up` + "\xfe" + `da\">\n<parameter name=\"n\">1.50</parameter><parameter name=\"s\">bad` + "\xfe" + `</parameter><parameter name=\"h\">a<b&c</parameter>","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "action", `"up`+"\xfe"+`da"`)
	wantToken(t, fields, "n", `1.50`)
	wantToken(t, fields, "s", `"bad`+"\xfe"+`"`)
	wantToken(t, fields, "h", `"a<b&c"`)
	wantToken(t, fields, "note", `"ok"`)
}

func TestRescueClaudeXMLArgsMultiCarrierPrecedence(t *testing.T) {
	raw := `{"a":"go\">\n<parameter name=\"x\">1</parameter>","b":"go\">\n<parameter name=\"x\">2</parameter>"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "x", `2`)
}

func TestRescueClaudeXMLArgsXMLGateSemantics(t *testing.T) {
	wantUnchanged(t, `{"x":"\u003cparameter\r\nname=\"x\"\u003e1"}`)
	wantUnchanged(t, `{"payload":"{\"a\":1}","b":"\u003cparameter foo\u003e"}`)
	out := rescueClaudeXMLArgs(`{"payload":"{\"a\":1}","b":"plain"}`)
	fields := fieldsOf(t, out)
	wantToken(t, fields, "payload", `{"a":1}`)
}

func TestRescueClaudeXMLArgsParsedAnyPreservesInnerEscapeSpelling(t *testing.T) {
	raw := `{"payload":"{\"s\":\"\\u0041\",\"n\":1.50}","other":"x"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "payload", `{"s":"\u0041","n":1.50}`)
}

func TestRescueClaudeXMLArgsExtractedValidJSONKeepsEscapeSpelling(t *testing.T) {
	raw := `{"action":"go\">\n<parameter name=\"j\">{\"s\":\"\\u0041\"}</parameter>","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "j", `{"s":"\u0041"}`)
}

func TestRescueClaudeXMLArgsClosingTagByteOffsets(t *testing.T) {
	raw := `{"action":"go\">\n<parameter name=\"x\">İ</parameter>","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "x", `"İ"`)

	raw = `{"action":"go\">\n` + "<parameter name=\\\"x\\\">" + "\u212A" + closeTag + `","note":"ok"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "x", "\"\u212A\"")

	raw = `{"action":"go\">\n<parameter name=\"x\">` + "\xfe\xfe\xfe\xfe\xfe\xfe\xfe" + `</parameter>","note":"ok"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "x", `"`+"\xfe\xfe\xfe\xfe\xfe\xfe\xfe"+`"`)
}

func TestRescueClaudeXMLArgsExtractedOverflowNumberStaysString(t *testing.T) {
	raw := `{"action":"go\">\n` + "<parameter name=\\\"x\\\">1e999" + closeTag + `","note":"ok"}`
	fields := fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "x", `"1e999"`)

	raw = `{"action":"go\">\n` + "<parameter name=\\\"x\\\">{\\\"a\\\":1e999}" + closeTag + `","note":"ok"}`
	fields = fieldsOf(t, rescueClaudeXMLArgs(raw))
	wantToken(t, fields, "x", `"{\"a\":1e999}"`)
}

func TestRescueClaudeXMLArgsReplacedFieldWithCompleteOpenIsNotInspected(t *testing.T) {
	inner := `{"a":"` + "<parameter name=\\\"x\\\">y" + `"}`
	rawBytes, err := json.Marshal(map[string]string{"p": inner})
	if err != nil {
		t.Fatal(err)
	}
	fields := fieldsOf(t, rescueClaudeXMLArgs(string(rawBytes)))
	wantToken(t, fields, "p", inner)
	if _, ok := fields["x"]; ok {
		t.Fatalf("replaced field was inspected for XML: %v", fields)
	}
}

func TestFromChatCompletionResponseRescuePreservesNonCanonicalJSON(t *testing.T) {
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
	resp, err := fromChatCompletionResponse(raw, nil)
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
