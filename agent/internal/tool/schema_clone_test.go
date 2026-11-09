package tool

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/llm"
)

const exactTypedSchemaNumber = json.Number("9007199254740993")

type typedSchemaStruct struct {
	Entry map[string]any `json:"entry"`
}

type typedNestedObjectSchema struct {
	Type    string         `json:"type"`
	Default map[string]any `json:"default"`
}

func TestCloneSchemaMapTypedContainerIsolation(t *testing.T) {
	t.Parallel()

	newEntry := func() map[string]any {
		return map[string]any{
			"marker": "original",
			"number": exactTypedSchemaNumber,
			"float":  float64(1.25),
		}
	}
	cases := []struct {
		name  string
		value func(map[string]any) any
	}{
		{name: "typed map", value: func(entry map[string]any) any {
			return map[string]map[string]any{"entry": entry}
		}},
		{name: "typed slice", value: func(entry map[string]any) any {
			return []map[string]any{entry}
		}},
		{name: "typed array", value: func(entry map[string]any) any {
			return [1]map[string]any{entry}
		}},
		{name: "struct", value: func(entry map[string]any) any {
			return typedSchemaStruct{Entry: entry}
		}},
		{name: "pointer", value: func(entry map[string]any) any {
			return &typedSchemaStruct{Entry: entry}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sourceEntry := newEntry()
			clone := CloneSchemaMap(map[string]any{"container": tc.value(sourceEntry)})
			assertTypedSchemaContainerContents(t, clone["container"], "original")

			sourceEntry["marker"] = "source-mutated"
			assertTypedSchemaContainerContents(t, clone["container"], "original")

			replaceTypedSchemaMarker(t, clone["container"], "clone-mutated")
			if got := sourceEntry["marker"]; got != "source-mutated" {
				t.Errorf("source marker after clone mutation = %#v, want source-mutated", got)
			}
			assertTypedSchemaContainerContents(t, clone["container"], "clone-mutated")
		})
	}

	t.Run("nil and empty values", func(t *testing.T) {
		if got := CloneSchemaMap(nil); got != nil {
			t.Fatalf("CloneSchemaMap(nil) = %#v, want nil", got)
		}
		empty := CloneSchemaMap(map[string]any{})
		if empty == nil || len(empty) != 0 {
			t.Fatalf("CloneSchemaMap(empty) = %#v, want non-nil empty map", empty)
		}

		var typedNil []typedSchemaStruct
		clone := CloneSchemaMap(map[string]any{
			"typed_nil":   typedNil,
			"typed_empty": map[string]typedSchemaStruct{},
			"nil":         nil,
		})
		assertTypedSchemaJSON(t, clone["typed_nil"], "null")
		assertTypedSchemaJSON(t, clone["typed_empty"], "{}")
		assertTypedSchemaJSON(t, clone["nil"], "null")
	})

	t.Run("unserializable container fallback", func(t *testing.T) {
		unserializable := map[string]func(){"first": func() {}}
		clone := CloneSchemaMap(map[string]any{"container": unserializable})
		got, ok := clone["container"].(map[string]func())
		if !ok {
			t.Fatalf("unserializable fallback type = %T, want map[string]func()", clone["container"])
		}
		unserializable["second"] = func() {}
		if len(got) != 2 {
			t.Fatalf("unserializable fallback no longer returns the source value: len = %d, want 2", len(got))
		}
	})
}

func TestWithIntentParameterTypedSchemaIsolation(t *testing.T) {
	t.Parallel()

	sourceDefault := map[string]any{
		"marker": "original",
		"number": exactTypedSchemaNumber,
		"float":  float64(1.25),
	}
	decorated := WithIntentParameter(llm.ToolDefinition{
		Name: "typed_schema",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"payload": typedNestedObjectSchema{
					Type:    "object",
					Default: sourceDefault,
				},
			},
		},
	})
	if _, err := compileSchema(decorated.Parameters); err != nil {
		t.Fatalf("compile valid typed nested schema: %v", err)
	}

	semantic := typedSchemaSemanticValue(t, decorated.Parameters)
	root := typedSchemaObject(t, semantic)
	properties := typedSchemaObject(t, root["properties"])
	if _, ok := properties["payload"]; !ok {
		t.Fatal("decorated schema lost the typed payload property")
	}
	if _, ok := properties["intent"]; !ok {
		t.Fatal("decorated schema did not add the intent property")
	}
	assertTypedSchemaContainerContents(t, properties["payload"], "original")

	sourceDefault["marker"] = "source-mutated"
	assertTypedSchemaContainerContents(t, decorated.Parameters, "original")

	replaceTypedSchemaMarker(t, decorated.Parameters, "clone-mutated")
	if got := sourceDefault["marker"]; got != "source-mutated" {
		t.Errorf("source marker after decorated clone mutation = %#v, want source-mutated", got)
	}
	assertTypedSchemaContainerContents(t, decorated.Parameters, "clone-mutated")
}

func assertTypedSchemaContainerContents(t *testing.T, value any, wantMarker string) {
	t.Helper()

	semantic := typedSchemaSemanticValue(t, value)
	entry, ok := findTypedSchemaMarker(semantic)
	if !ok {
		t.Fatalf("semantic value %#v has no marker entry", semantic)
	}
	if got := entry["marker"]; got != wantMarker {
		t.Errorf("marker = %#v, want %q", got, wantMarker)
	}
	number, ok := entry["number"].(json.Number)
	if !ok {
		t.Errorf("number type = %T, want json.Number", entry["number"])
		return
	}
	if number != exactTypedSchemaNumber {
		t.Errorf("number = %q, want %q", number, exactTypedSchemaNumber)
	}
	float, ok := entry["float"].(json.Number)
	if !ok {
		t.Errorf("semantic float type = %T, want json.Number", entry["float"])
		return
	}
	if float != json.Number("1.25") {
		t.Errorf("semantic float = %q, want 1.25", float)
	}
}

func typedSchemaSemanticValue(t *testing.T, value any) any {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal semantic schema value: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var semantic any
	if err := decoder.Decode(&semantic); err != nil {
		t.Fatalf("decode semantic schema value: %v", err)
	}
	return semantic
}

func typedSchemaObject(t *testing.T, value any) map[string]any {
	t.Helper()

	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("semantic schema value type = %T, want object", value)
	}
	return object
}

func findTypedSchemaMarker(value any) (map[string]any, bool) {
	switch value := value.(type) {
	case map[string]any:
		if _, ok := value["marker"]; ok {
			return value, true
		}
		for _, child := range value {
			if entry, ok := findTypedSchemaMarker(child); ok {
				return entry, true
			}
		}
	case []any:
		for _, child := range value {
			if entry, ok := findTypedSchemaMarker(child); ok {
				return entry, true
			}
		}
	}
	return nil, false
}

func replaceTypedSchemaMarker(t *testing.T, value any, replacement string) {
	t.Helper()

	if !replaceTypedSchemaMarkerValue(reflect.ValueOf(value), replacement) {
		t.Fatal("clone contains no mutable marker")
	}
}

func replaceTypedSchemaMarkerValue(value reflect.Value, replacement string) bool {
	if !value.IsValid() {
		return false
	}
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return false
		}
		value = value.Elem()
	}

	switch value.Kind() {
	case reflect.Map:
		for _, key := range value.MapKeys() {
			child := value.MapIndex(key)
			if key.Kind() == reflect.String && key.String() == "marker" {
				replacementValue := reflect.ValueOf(replacement)
				if replacementValue.Type().AssignableTo(value.Type().Elem()) {
					value.SetMapIndex(key, replacementValue)
					return true
				}
				if value.Type().Elem().Kind() == reflect.Interface {
					value.SetMapIndex(key, replacementValue)
					return true
				}
			}
			if replaceTypedSchemaMarkerValue(child, replacement) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if replaceTypedSchemaMarkerValue(value.Index(i), replacement) {
				return true
			}
		}
	case reflect.Struct:
		for _, field := range value.Fields() {
			if replaceTypedSchemaMarkerValue(field, replacement) {
				return true
			}
		}
	}
	return false
}

func assertTypedSchemaJSON(t *testing.T, value any, want string) {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal schema value: %v", err)
	}
	if got := string(raw); got != want {
		t.Fatalf("schema JSON = %s, want %s", got, want)
	}
}
