package repair

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

// TestConstraintMessage_MinLength covers the minLength constraint keyword
// (lines 120-127): field is a string below the minimum length.
func TestConstraintMessage_MinLength(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "minLength": 5},
		},
		"required": []string{"name"},
	}
	args := map[string]any{"name": "hi"}
	got := ExplainSchemaError("my_tool", params, args, "name", "minLength")
	want := `my_tool: argument "name" is below minLength (5). Value "hi" is 2 characters.`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestConstraintMessage_MinLengthLimitNotInt covers the schemaInt failure
// path for minLength (line 121-122): minLength is not a number, so
// constraintMessage returns "" and falls back to generic.
func TestConstraintMessage_MinLengthLimitNotInt(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "minLength": "bad"},
		},
		"required": []string{"name"},
	}
	args := map[string]any{"name": "hi"}
	got := ExplainSchemaError("my_tool", params, args, "name", "minLength")
	// Falls back to generic message since limit is not an int.
	if !strings.Contains(got, "wrong type or value") {
		t.Fatalf("got:\n%s\nwant fallback to generic", got)
	}
}

// TestConstraintMessage_MaxLengthLimitNotInt covers the schemaInt failure
// for maxLength (line 113-114).
func TestConstraintMessage_MaxLengthLimitNotInt(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "maxLength": true},
		},
		"required": []string{"name"},
	}
	args := map[string]any{"name": "hi"}
	got := ExplainSchemaError("my_tool", params, args, "name", "maxLength")
	if !strings.Contains(got, "wrong type or value") {
		t.Fatalf("got:\n%s\nwant fallback to generic", got)
	}
}

// TestConstraintMessage_MaxItemsLimitNotInt covers the schemaInt failure
// for maxItems (line 129-130).
func TestConstraintMessage_MaxItemsLimitNotInt(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"items": map[string]any{"type": "array", "maxItems": "bad"},
		},
		"required": []string{"items"},
	}
	args := map[string]any{"items": []any{1, 2}}
	got := ExplainSchemaError("my_tool", params, args, "items", "maxItems")
	if !strings.Contains(got, "wrong type or value") {
		t.Fatalf("got:\n%s\nwant fallback to generic", got)
	}
}

// TestConstraintMessage_MinItemsLimitNotInt covers the schemaInt failure
// for minItems (line 136-137).
func TestConstraintMessage_MinItemsLimitNotInt(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"items": map[string]any{"type": "array", "minItems": "bad"},
		},
		"required": []string{"items"},
	}
	args := map[string]any{"items": []any{1, 2}}
	got := ExplainSchemaError("my_tool", params, args, "items", "minItems")
	if !strings.Contains(got, "wrong type or value") {
		t.Fatalf("got:\n%s\nwant fallback to generic", got)
	}
}

// TestConstraintMessage_EnumEmpty covers the empty-enum path (line 143-145):
// enum is present but empty, so constraintMessage returns "".
func TestConstraintMessage_EnumEmpty(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"color": map[string]any{"type": "string", "enum": []any{}},
		},
		"required": []string{"color"},
	}
	args := map[string]any{"color": "red"}
	got := ExplainSchemaError("my_tool", params, args, "color", "enum")
	if !strings.Contains(got, "wrong type or value") {
		t.Fatalf("got:\n%s\nwant fallback to generic", got)
	}
}

// TestConstraintMessage_FieldSchemaNil covers the nil-fieldSchema path
// (line 106-108): the field has no schema properties.
func TestConstraintMessage_FieldSchemaNil(t *testing.T) {
	params := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
		"required":   []string{"missing"},
	}
	args := map[string]any{"missing": "value"}
	got := constraintMessage("my_tool", params, "missing", "missing", "maxLength", args, "missing")
	if got != "" {
		t.Fatalf("constraintMessage = %q, want empty", got)
	}
}

// TestConstraintMessage_UnrecognizedKeyword covers the default case
// (line 149): a keyword that is not one of the recognized constraints.
func TestConstraintMessage_UnrecognizedKeyword(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"val": map[string]any{"type": "integer", "minimum": 10},
		},
		"required": []string{"val"},
	}
	args := map[string]any{"val": float64(5)}
	got := constraintMessage("my_tool", params, "val", "val", "minimum", args, "val")
	if got != "" {
		t.Fatalf("constraintMessage = %q, want empty for unrecognized keyword", got)
	}
}

// TestResolveInstanceValue_NilPath covers the empty-path return (line 158-159).
func TestResolveInstanceValue_NilPath(t *testing.T) {
	args := map[string]any{"key": "val"}
	if got := resolveInstanceValue(args, ""); got != nil {
		t.Fatalf("resolveInstanceValue(\"\") = %v, want nil", got)
	}
}

// TestResolveInstanceValue_ArrayOutOfBounds covers the array-index path
// where the index is out of bounds (line 164-166).
func TestResolveInstanceValue_ArrayOutOfBounds(t *testing.T) {
	args := map[string]any{"items": []any{1, 2}}
	if got := resolveInstanceValue(args, "items/5"); got != nil {
		t.Fatalf("resolveInstanceValue out-of-bounds = %v, want nil", got)
	}
}

// TestResolveInstanceValue_NegativeIndex covers the array-index path with
// a negative index — arrayIndex won't match, so it tries map lookup (line 171-174).
func TestResolveInstanceValue_ArrayNotMap(t *testing.T) {
	args := map[string]any{"items": "not-an-array"}
	// "items/0" — arrayIndex("0") is true, but cur is not an array, so returns nil.
	if got := resolveInstanceValue(args, "items/0"); got != nil {
		t.Fatalf("resolveInstanceValue on non-array = %v, want nil", got)
	}
}

// TestResolveInstanceValue_MapNotMap covers the map-step path where cur
// is not a map (line 171-173).
func TestResolveInstanceValue_NestedNotMap(t *testing.T) {
	args := map[string]any{"key": 42}
	// "key/sub" — after resolving "key" to 42 (int), trying to resolve "sub"
	// as a map key fails because 42 is not a map.
	if got := resolveInstanceValue(args, "key/sub"); got != nil {
		t.Fatalf("resolveInstanceValue on non-map = %v, want nil", got)
	}
}

// TestResolveInstanceValue_DeepNested covers a successful deep nested
// resolution with alternating array and map steps.
func TestResolveInstanceValue_DeepNested(t *testing.T) {
	args := map[string]any{
		"items": []any{
			map[string]any{"name": "first"},
		},
	}
	got := resolveInstanceValue(args, "items/0/name")
	if got != "first" {
		t.Fatalf("resolveInstanceValue = %v, want \"first\"", got)
	}
}

// TestSchemaInt covers all three numeric type branches (float64, int, int64)
// and the failure case.
func TestSchemaInt(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want int
		ok   bool
	}{
		{"float64", float64(42), 42, true},
		{"int", int(42), 42, true},
		{"int64", int64(42), 42, true},
		{"string", "42", 0, false},
		{"nil", nil, 0, false},
		{"bool", true, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := schemaInt(tc.v)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("schemaInt(%v) = (%d, %v), want (%d, %v)", tc.v, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestResolveSchemaErrorContainer_NilSchema covers the nil-schema path
// (line 229-230): a segment's current schema node is nil.
func TestResolveSchemaErrorContainer_NilSchema(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"nested": map[string]any{}, // no properties, so resolving nested/x gets nil
		},
	}
	args := map[string]any{"nested": map[string]any{}}
	_, _, _, _, ok := resolveSchemaErrorContainer(params, args, "nested/x")
	if ok {
		t.Fatal("expected ok=false for nil schema resolution")
	}
}

// TestResolveSchemaErrorContainer_ArrayNoItems covers the array-without-items
// path (line 233-234): the schema is type:array but has no items.
func TestResolveSchemaErrorContainer_ArrayNoItems(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"arr": map[string]any{"type": "array"},
		},
	}
	args := map[string]any{"arr": []any{1}}
	_, _, _, _, ok := resolveSchemaErrorContainer(params, args, "arr/0")
	if ok {
		t.Fatal("expected ok=false for array without items")
	}
}

// TestResolveSchemaErrorContainer_TerminalArrayIndexAllPresent covers the
// terminal array-index path where all required fields are present, so ok=false
// (line 265).
func TestResolveSchemaErrorContainer_TerminalArrayIndexAllPresent(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"arr": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "object", "required": []string{"x"}},
			},
		},
	}
	args := map[string]any{"arr": []any{map[string]any{"x": 1}}}
	_, _, _, _, ok := resolveSchemaErrorContainer(params, args, "arr/0")
	if ok {
		t.Fatal("expected ok=false when all required present at terminal array index")
	}
}

// TestArrayIndex covers all branches of arrayIndex: empty, non-numeric,
// valid, and non-digit characters.
func TestArrayIndex(t *testing.T) {
	tests := []struct {
		seg string
		idx int
		ok  bool
	}{
		{"", 0, false},
		{"abc", 0, false},
		{"0", 0, true},
		{"42", 42, true},
		{"-1", 0, false},
		{"1a", 0, false},
		{"99999999999999999999999999", 0, false}, // overflow
	}
	for _, tc := range tests {
		t.Run(tc.seg, func(t *testing.T) {
			got, ok := arrayIndex(tc.seg)
			if got != tc.idx || ok != tc.ok {
				t.Fatalf("arrayIndex(%q) = (%d, %v), want (%d, %v)", tc.seg, got, ok, tc.idx, tc.ok)
			}
		})
	}
}

// TestFormatPath covers the formatPath function's array-index and property
// branches (lines 313-323).
func TestFormatPath(t *testing.T) {
	tests := []struct {
		segs []string
		want string
	}{
		{[]string{"updates", "0"}, "updates[0]"},
		{[]string{"questions", "0", "header"}, "questions[0].header"},
		{[]string{"name"}, "name"},
		{[]string{"a", "b"}, "a.b"},
		{[]string{"arr", "5", "nested", "2"}, "arr[5].nested[2]"},
	}
	for _, tc := range tests {
		t.Run(strings.Join(tc.segs, "/"), func(t *testing.T) {
			got := formatPath(tc.segs)
			if got != tc.want {
				t.Fatalf("formatPath(%v) = %q, want %q", tc.segs, got, tc.want)
			}
		})
	}
}

// TestParseExcerpt_EmptyRaw covers the empty-raw return (line 358-359).
func TestParseExcerpt_EmptyRaw(t *testing.T) {
	got := parseExcerpt(io.EOF, nil)
	if got != "" {
		t.Fatalf("parseExcerpt(nil) = %q, want empty", got)
	}
}

// TestParseExcerpt_UnexpectedEOF covers the io.EOF/io.ErrUnexpectedEOF path
// (line 362-367).
func TestParseExcerpt_UnexpectedEOF(t *testing.T) {
	raw := []byte(`{"key": "val`)
	got := parseExcerpt(io.ErrUnexpectedEOF, raw)
	if !strings.Contains(got, "ended with") {
		t.Fatalf("parseExcerpt(io.ErrUnexpectedEOF) = %q, want 'ended with'", got)
	}
}

// TestParseExcerpt_UnexpectedEOFCapped covers the tail-capping path when the
// raw exceeds the window size (line 363-365).
func TestParseExcerpt_UnexpectedEOFCapped(t *testing.T) {
	raw := []byte(`{"key": "` + strings.Repeat("x", 500))
	got := parseExcerpt(io.ErrUnexpectedEOF, raw)
	if !strings.Contains(got, "...") {
		t.Fatalf("parseExcerpt capped = %q, want ellipsis", got)
	}
}

// TestParseExcerpt_SyntaxError covers the *json.SyntaxError path with a
// valid offset (line 369-391).
func TestParseExcerpt_SyntaxError(t *testing.T) {
	raw := []byte(`{"key": }`)
	var v any
	err := json.Unmarshal(raw, &v)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Fatalf("error = %T, want *json.SyntaxError", err)
	}
	got := parseExcerpt(err, raw)
	if !strings.Contains(got, "near byte") || !strings.Contains(got, ">>>") {
		t.Fatalf("parseExcerpt = %q, want 'near byte' and '>>>'", got)
	}
}

// TestParseExcerpt_SyntaxErrorUnexpectedEOF covers the special case where
// the syntax error is "unexpected end of JSON input" (line 372-375), which
// positions the marker at the end of the input.
func TestParseExcerpt_SyntaxErrorUnexpectedEOF(t *testing.T) {
	raw := []byte(`{"key": "val`)
	err := realJSONError(t, raw)
	if _, ok := errors.AsType[*json.SyntaxError](err); !ok {
		t.Fatalf("error = %T, want *json.SyntaxError", err)
	}
	got := parseExcerpt(err, raw)
	if !strings.Contains(got, "near byte") {
		t.Fatalf("parseExcerpt = %q, want 'near byte'", got)
	}
}

// TestParseExcerpt_SyntaxErrorCapped covers the prefix/suffix ellipsis path
// (lines 384-388).
func TestParseExcerpt_SyntaxErrorCapped(t *testing.T) {
	// Create a large input that triggers the window ellipsis.
	raw := []byte(`{"key": "` + strings.Repeat("x", 500) + `"}`)
	// Make it invalid by adding a bad char far in.
	raw = append(raw[:len(raw)-1], []byte("!!!}")...)
	var v any
	err := json.Unmarshal(raw, &v)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	got := parseExcerpt(err, raw)
	// Should contain the marker and possibly ellipsis.
	if !strings.Contains(got, "near byte") {
		t.Fatalf("parseExcerpt = %q, want 'near byte'", got)
	}
}

// TestParseExcerpt_NonSyntaxError covers the fallback tail path (line 392-397)
// for an error that is neither io.EOF nor *json.SyntaxError.
func TestParseExcerpt_NonSyntaxError(t *testing.T) {
	raw := []byte(`some raw input that is not JSON`)
	got := parseExcerpt(errors.New("some other error"), raw)
	if !strings.Contains(got, "ended with") {
		t.Fatalf("parseExcerpt(non-syntax) = %q, want 'ended with'", got)
	}
}

// TestParseExcerpt_NonSyntaxErrorCapped covers the tail-capping for the
// fallback path (line 393-395).
func TestParseExcerpt_NonSyntaxErrorCapped(t *testing.T) {
	raw := []byte(strings.Repeat("x", 500))
	got := parseExcerpt(errors.New("other error"), raw)
	if !strings.Contains(got, "...") {
		t.Fatalf("parseExcerpt capped = %q, want ellipsis", got)
	}
}

// TestIsUnexpectedJSONEOF covers both branches of isUnexpectedJSONEOF.
func TestIsUnexpectedJSONEOF(t *testing.T) {
	if !isUnexpectedJSONEOF(io.EOF) {
		t.Error("io.EOF should be unexpected EOF")
	}
	if !isUnexpectedJSONEOF(io.ErrUnexpectedEOF) {
		t.Error("io.ErrUnexpectedEOF should be unexpected EOF")
	}
	if isUnexpectedJSONEOF(errors.New("other")) {
		t.Error("other error should not be unexpected EOF")
	}
}

// TestSchemaIsArray covers schemaIsArray (line 293-295).
func TestSchemaIsArray(t *testing.T) {
	if !schemaIsArray(map[string]any{"type": "array"}) {
		t.Error("type:array should be true")
	}
	if schemaIsArray(map[string]any{"type": "object"}) {
		t.Error("type:object should be false")
	}
	if schemaIsArray(map[string]any{}) {
		t.Error("missing type should be false")
	}
}

// TestRequiredNames covers requiredNames (line 302-304).
func TestRequiredNames(t *testing.T) {
	schema := map[string]any{"required": []string{"a", "b", "c"}}
	got := requiredNames(schema)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("requiredNames = %v, want [a b c]", got)
	}
}

// TestExamplePlaceholder covers all type branches (lines 439-452).
func TestExamplePlaceholder(t *testing.T) {
	tests := []struct {
		typ  string
		want string
	}{
		{"integer", "0"},
		{"number", "0"},
		{"boolean", "false"},
		{"array", "[]"},
		{"object", "{}"},
		{"string", `"..."`},
		{"", `"..."`},
	}
	for _, tc := range tests {
		t.Run(tc.typ, func(t *testing.T) {
			got := examplePlaceholder(tc.typ)
			if got != tc.want {
				t.Fatalf("examplePlaceholder(%q) = %q, want %q", tc.typ, got, tc.want)
			}
		})
	}
}

// TestMinimalExample covers the minimalExample function with various types
// (lines 424-437).
func TestMinimalExample(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":   map[string]any{"type": "string"},
			"count":  map[string]any{"type": "integer"},
			"flag":   map[string]any{"type": "boolean"},
			"items":  map[string]any{"type": "array"},
			"config": map[string]any{"type": "object"},
		},
		"required": []string{"name", "count", "flag", "items", "config"},
	}
	got := minimalExample(params)
	// Should contain all required fields sorted alphabetically.
	for _, key := range []string{"config", "count", "flag", "items", "name"} {
		if !strings.Contains(got, fmt.Sprintf("%q:", key)) {
			t.Fatalf("minimalExample missing %q: %s", key, got)
		}
	}
}

// TestExplainSchemaError_ContainerPathMissingField covers the "missing
// required argument in <container>" path (line 82-83) where containerPath
// is non-empty.
func TestExplainSchemaError_ContainerPathMissingField(t *testing.T) {
	params := taskListParamsForExplain()
	args := map[string]any{
		"action":  "update",
		"updates": []any{map[string]any{"status": "done"}},
	}
	got := ExplainSchemaError("task_list", params, args, "updates/0", "")
	if !strings.Contains(got, "missing required argument") || !strings.Contains(got, "updates[0]") {
		t.Fatalf("got:\n%s\nwant missing required in updates[0]", got)
	}
}

// TestExplainSchemaError_ContainerPathRequiredList covers the "Required
// arguments in <container>" tail (line 91-92).
func TestExplainSchemaError_ContainerPathRequiredList(t *testing.T) {
	params := taskListParamsForExplain()
	args := map[string]any{
		"action":  "update",
		"updates": []any{map[string]any{}},
	}
	got := ExplainSchemaError("task_list", params, args, "updates/0", "")
	if !strings.Contains(got, "Required arguments in updates[0]:") {
		t.Fatalf("got:\n%s\nwant 'Required arguments in updates[0]:'", got)
	}
}

// TestExplainSchemaError_NoInstanceLocation covers the !haveField case
// (line 78-79) where instanceLocation is empty.
func TestExplainSchemaError_NoInstanceLocation(t *testing.T) {
	got := ExplainSchemaError("my_tool", editParamsForExplain(), map[string]any{}, "", "")
	if !strings.Contains(got, "arguments did not match the schema") {
		t.Fatalf("got:\n%s\nwant 'arguments did not match the schema'", got)
	}
}

// TestExplainSchemaError_PresentFieldWithContainerPath covers the
// present-field path with containerPath != "" (line 66-68).
func TestExplainSchemaError_PresentFieldWithContainerPath(t *testing.T) {
	params := askUserParamsWithHeaderMaxLengthForExplain()
	args := map[string]any{
		"questions": []any{map[string]any{
			"header":   strings.Repeat("x", 20),
			"question": "q",
			"options":  []any{},
		}},
	}
	got := ExplainSchemaError("ask_user", params, args, "questions/0/header", "maxLength")
	if !strings.Contains(got, "questions[0].header") {
		t.Fatalf("got:\n%s\nwant 'questions[0].header'", got)
	}
}

// TestExplainSchemaError_PresentFieldUnhandledKeyword covers the
// present-field path with an unhandled keyword (line 73-74): falls back to
// generic message.
func TestExplainSchemaError_PresentFieldUnhandledKeyword(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"val": map[string]any{"type": "string"},
		},
		"required": []string{"val"},
	}
	args := map[string]any{"val": "hello"}
	got := ExplainSchemaError("my_tool", params, args, "val", "pattern")
	if !strings.Contains(got, "wrong type or value") {
		t.Fatalf("got:\n%s\nwant 'wrong type or value'", got)
	}
}

// TestAsStringSlice covers asStringSlice with various input types.
func TestAsStringSlice(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want []string
	}{
		{"[]string", []string{"a", "b"}, []string{"a", "b"}},
		{"[]any", []any{"a", "b"}, []string{"a", "b"}},
		{"[]any with non-strings", []any{"a", 1, "b"}, []string{"a", "b"}},
		{"nil", nil, nil},
		{"string", "hello", nil},
		{"int", 42, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := asStringSlice(tc.v)
			if len(got) != len(tc.want) {
				t.Fatalf("asStringSlice(%v) = %v, want %v", tc.v, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("asStringSlice(%v)[%d] = %q, want %q", tc.v, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestFormatEnumValues covers formatEnumValues, the shared renderer for
// constraintMessage's and branchRequirement's enum clauses. Each case's
// want is json.Marshal's own output for that value: a string quoted,
// everything else its own JSON literal, never Go's %v syntax.
func TestFormatEnumValues(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want []string
	}{
		{"[]string", []string{"open", "closed"}, []string{`"open"`, `"closed"`}},
		{"[]any strings", []any{"open", "closed"}, []string{`"open"`, `"closed"`}},
		{"[]any integers (float64, MCP-decoded shape)", []any{float64(1), float64(2), float64(3)}, []string{"1", "2", "3"}},
		{"[]any booleans", []any{true, false}, []string{"true", "false"}},
		{"[]any null", []any{nil}, []string{"null"}},
		{"[]any mixed types", []any{"open", float64(1), true, nil}, []string{`"open"`, "1", "true", "null"}},
		// A composite enum member renders as its own JSON value, not Go's %v
		// syntax (which would print "[1 2]"/"map[a:1]").
		{"[]any nested array", []any{[]any{float64(1), float64(2)}}, []string{"[1,2]"}},
		{"[]any nested object", []any{map[string]any{"a": float64(1)}}, []string{`{"a":1}`}},
		// JSON number encoding only turns to scientific notation above
		// ~1e21 or below ~1e-6 magnitude, so a whole-number enum value like
		// 1e20 renders as a plain decimal. Past that range it renders as
		// "1e+21" — still valid JSON, and matching json.Marshal is the
		// contract, not decimal notation at every magnitude.
		{"[]any large float stays decimal", []any{1e20}, []string{"100000000000000000000"}},
		{"[]any float past json.Marshal's decimal range", []any{1e21}, []string{"1e+21"}},
		// A genuinely typed slice or array (not []any) renders the same as
		// its []any equivalent — reflection walks any slice/array kind
		// uniformly, so these need no dedicated case in formatEnumValues.
		{"[]int", []int{1, 2, 3}, []string{"1", "2", "3"}},
		{"[]bool", []bool{true, false}, []string{"true", "false"}},
		{"[]float64", []float64{1.5, 2.5}, []string{"1.5", "2.5"}},
		{"[3]int array (not slice)", [3]int{1, 2, 3}, []string{"1", "2", "3"}},
		{"nil", nil, nil},
		{"string", "hello", nil},
		{"int", 42, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatEnumValues(tc.v)
			if len(got) != len(tc.want) {
				t.Fatalf("formatEnumValues(%v) = %v, want %v", tc.v, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("formatEnumValues(%v)[%d] = %q, want %q", tc.v, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestExplainJSONError_EmptyRaw covers ExplainJSONError with nil raw, which
// produces no excerpt.
func TestExplainJSONError_EmptyRaw(t *testing.T) {
	got := ExplainJSONError("my_tool", editParamsForExplain(), errors.New("parse error"), nil)
	if !strings.Contains(got, "my_tool") || !strings.Contains(got, "JSON object") {
		t.Fatalf("got:\n%s\nwant tool name and JSON object", got)
	}
}
