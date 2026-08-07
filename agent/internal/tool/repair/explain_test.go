package repair

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func editParamsForExplain() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path":  map[string]any{"type": "string"},
			"old_string": map[string]any{"type": "string"},
			"new_string": map[string]any{"type": "string"},
		},
		"required": []any{"file_path", "old_string", "new_string"},
	}
}

func TestExplainSchemaError_NamesOffendingField(t *testing.T) {
	msg := ExplainSchemaError("edit_file", editParamsForExplain(), map[string]any{"file_path": "/x"}, "old_string")
	if !strings.Contains(msg, `edit_file`) || !strings.Contains(msg, `"old_string"`) {
		t.Fatalf("msg = %q", msg)
	}
	if !strings.Contains(msg, "Required arguments:") || !strings.Contains(msg, "Example:") {
		t.Fatalf("msg missing required/example: %q", msg)
	}
}

func TestExplainSchemaError_FallbackWhenUnknownField(t *testing.T) {
	msg := ExplainSchemaError("edit_file", editParamsForExplain(), map[string]any{}, "")
	// Must still list required args + example even without a pinpointed field.
	if !strings.Contains(msg, "file_path") || !strings.Contains(msg, "Example:") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestExplainJSONError_MentionsToolAndObject(t *testing.T) {
	msg := ExplainJSONError("read_file", editParamsForExplain(), realJSONError(t, []byte(`{"file_path":`)), nil)
	if !strings.Contains(msg, "read_file") || !strings.Contains(msg, "JSON object") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestExplainJSONError_ShowsTailWhenTruncated(t *testing.T) {
	raw := []byte(`{"file_path": "/tmp/x", "content": "hello wor`)
	msg := ExplainJSONError("write_file", editParamsForExplain(), io.ErrUnexpectedEOF, raw)
	// The excerpt is %q-quoted, so the raw text appears escaped.
	if !strings.Contains(msg, `{\"file_path\": \"/tmp/x\", \"content\": \"hello wor`) {
		t.Fatalf("msg omitted the failing input: %q", msg)
	}
	if !strings.Contains(msg, "ended with") {
		t.Fatalf("msg did not mark the truncation point: %q", msg)
	}
}

func TestExplainJSONError_UsesOffsetAtRealTruncatedEOF(t *testing.T) {
	raw := []byte(`{"file_path": "/tmp/x", "content": "hello wor`)
	err := realJSONError(t, raw)
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("json.Unmarshal error = %T %v, want *json.SyntaxError", err, err)
	}
	want := fmt.Sprintf("Failing input near byte %d: %q.", syntaxErr.Offset, string(raw)+">>>")
	if got := parseExcerpt(err, raw); got != want {
		t.Fatalf("parseExcerpt = %q, want %q", got, want)
	}
}

func TestExplainJSONError_UsesOneBasedSyntaxOffset(t *testing.T) {
	raw := []byte(`{"file_path": "/tmp/x", }`)
	err := realJSONError(t, raw)
	var syntaxErr *json.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Fatalf("json.Unmarshal error = %T %v, want *json.SyntaxError", err, err)
	}
	position := int(syntaxErr.Offset) - 1
	want := fmt.Sprintf("Failing input near byte %d: %q.", syntaxErr.Offset, string(raw[:position])+">>>"+string(raw[position:]))
	if got := parseExcerpt(err, raw); got != want {
		t.Fatalf("parseExcerpt = %q, want %q", got, want)
	}
}

func TestExplainJSONError_ShowsWindowAroundOffset(t *testing.T) {
	raw := []byte(`{"file_path": "/tmp/x", }`)
	var v any
	err := json.Unmarshal(raw, &v)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	msg := ExplainJSONError("write_file", editParamsForExplain(), err, raw)
	if !strings.Contains(msg, "near byte") || !strings.Contains(msg, ">>>") {
		t.Fatalf("msg did not mark the failing offset: %q", msg)
	}
	if !strings.Contains(msg, "JSON object") {
		t.Fatalf("msg dropped the coaching: %q", msg)
	}
}

func TestExplainJSONError_CapsExcerptLength(t *testing.T) {
	raw := []byte(`{"content": "` + strings.Repeat("x", 5000))
	msg := ExplainJSONError("write_file", editParamsForExplain(), realJSONError(t, raw), raw)
	if len(msg) > 1000 {
		t.Fatalf("msg not capped (%d bytes): %.200q...", len(msg), msg)
	}
	if !strings.Contains(msg, "...") {
		t.Fatalf("truncated excerpt missing ellipsis: %q", msg)
	}
}

func realJSONError(t *testing.T, raw []byte) error {
	t.Helper()
	var value any
	err := json.Unmarshal(raw, &value)
	if err == nil {
		t.Fatalf("json.Unmarshal(%q) succeeded, want a parse error", raw)
	}
	return err
}

func TestExplainTruncatedCall(t *testing.T) {
	msg := ExplainTruncatedCall("write_file")
	for _, want := range []string{"write_file", "truncated", "output-token limit", "NOT executed"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}
}

// taskListParamsForExplain mirrors DefTaskList's updates-item schema (this
// package must stay dependency-free of agent/internal/tool, so the fixture is
// hand-built rather than calling the real Def* function).
func taskListParamsForExplain() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"action": map[string]any{
				"type": "string",
				"enum": []string{"view", "append", "update"},
			},
			"updates": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":     map[string]any{"type": "integer"},
						"status": map[string]any{"type": "string", "enum": []string{"open", "in_progress", "done", "cancelled"}},
						"notes":  map[string]any{"type": "string"},
					},
					"required": []string{"id", "status"},
				},
			},
		},
		"required": []string{"action"},
	}
}

// askUserParamsForExplain mirrors DefAskUser's questions-item schema.
func askUserParamsForExplain() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"questions": map[string]any{
				"type":     "array",
				"minItems": 1,
				"maxItems": 4,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"header":   map[string]any{"type": "string", "maxLength": 12},
						"question": map[string]any{"type": "string"},
						"options": map[string]any{
							"type":     "array",
							"minItems": 2,
							"maxItems": 5,
							"items":    map[string]any{"type": "object"},
						},
					},
					"required": []string{"question", "options"},
				},
			},
		},
		"required": []string{"questions"},
	}
}

func TestExplainSchemaError_ArrayItemMissingRequiredField(t *testing.T) {
	params := taskListParamsForExplain()
	args := map[string]any{
		"action":  "update",
		"updates": []any{map[string]any{"id": float64(1), "notes": "x"}},
	}
	got := ExplainSchemaError("task_list", params, args, "updates/0")
	want := "task_list: missing required argument \"status\" in updates[0].\n" +
		"Required arguments in updates[0]: id (integer), status (string).\n" +
		"Example: {\"action\": \"...\"}"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestExplainSchemaError_NestedPropertyWrongTypeOrValue(t *testing.T) {
	params := askUserParamsForExplain()
	args := map[string]any{
		"questions": []any{map[string]any{
			"header":   strings.Repeat("x", 20),
			"question": "q",
			"options":  []any{},
		}},
	}
	got := ExplainSchemaError("ask_user", params, args, "questions/0/header")
	want := "ask_user: argument \"questions[0].header\" has the wrong type or value.\n" +
		"Required arguments in questions[0]: question (string), options (array).\n" +
		"Example: {\"questions\": []}"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestExplainSchemaError_FlatTopLevelUnchanged(t *testing.T) {
	// Regression: a single-segment instance location (today's only case)
	// must still produce the unqualified "Required arguments:" line, with
	// no "in <container>" text anywhere.
	msg := ExplainSchemaError("edit_file", editParamsForExplain(), map[string]any{"file_path": "/x"}, "old_string")
	want := "edit_file: missing required argument \"old_string\".\n" +
		"Required arguments: file_path (string), old_string (string), new_string (string).\n" +
		"Example: {\"file_path\": \"...\", \"new_string\": \"...\", \"old_string\": \"...\"}"
	if msg != want {
		t.Fatalf("got:\n%s\nwant:\n%s", msg, want)
	}
	if strings.Contains(msg, " in ") {
		t.Fatalf("flat top-level message must not gain an 'in <container>' qualifier: %q", msg)
	}
}

func TestExplainSchemaError_DoesNotMutateStringRequired(t *testing.T) {
	required := []string{"new_string", "file_path", "old_string"}
	params := editParamsForExplain()
	params["required"] = required

	first := ExplainSchemaError("edit_file", params, map[string]any{}, "")
	second := ExplainSchemaError("edit_file", params, map[string]any{}, "")
	if first != second {
		t.Fatalf("explanation changed across calls:\nfirst:  %q\nsecond: %q", first, second)
	}
	if want := []string{"new_string", "file_path", "old_string"}; !reflect.DeepEqual(required, want) {
		t.Fatalf("required slice mutated: got %#v, want %#v", required, want)
	}
}
