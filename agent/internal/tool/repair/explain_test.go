package repair

import (
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
	msg := ExplainJSONError("read_file", editParamsForExplain(), "unexpected end of JSON input")
	if !strings.Contains(msg, "read_file") || !strings.Contains(msg, "JSON object") {
		t.Fatalf("msg = %q", msg)
	}
}
