package repair

import (
	"reflect"
	"slices"
	"testing"
)

// readFileParams mirrors read_file's real schema (definitions.go): file_path only, additionalProperties:false.
func readFileParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
		},
		"required": []any{"file_path"},
	}
}

// listDirParams mirrors list_dir: declares path natively.
func listDirParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
	}
}

func TestRepairArgs_Alias_PathToFilePath(t *testing.T) {
	out, changes := RepairArgs(readFileParams(), map[string]any{"path": "/x"})
	if !reflect.DeepEqual(out, map[string]any{"file_path": "/x"}) {
		t.Fatalf("got %v", out)
	}
	if len(changes) != 1 || changes[0].Kind != ChangeAlias || changes[0].Field != "file_path" {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestRepairArgs_Alias_NoOpWhenTargetNative(t *testing.T) {
	// list_dir declares path natively → path must NOT be aliased to file_path.
	out, changes := RepairArgs(listDirParams(), map[string]any{"path": "/x"})
	if !reflect.DeepEqual(out, map[string]any{"path": "/x"}) {
		t.Fatalf("got %v", out)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no changes, got %+v", changes)
	}
}

func TestRepairArgs_Alias_NoOpWhenCanonicalPresent(t *testing.T) {
	out, changes := RepairArgs(readFileParams(), map[string]any{"path": "/a", "file_path": "/b"})
	// file_path already present → do not overwrite; path is left (Task 3 will drop it).
	if out["file_path"] != "/b" {
		t.Fatalf("file_path overwritten: %v", out)
	}
	for _, c := range changes {
		if c.Kind == ChangeAlias {
			t.Fatalf("unexpected alias change: %+v", c)
		}
	}
}

func TestRepairArgs_DoesNotMutateInput(t *testing.T) {
	in := map[string]any{"path": "/x"}
	RepairArgs(readFileParams(), in)
	if _, ok := in["file_path"]; ok {
		t.Fatal("input map was mutated")
	}
}

func coerceParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"flag":  map[string]any{"type": "boolean"},
			"count": map[string]any{"type": "integer"},
			"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"name":  map[string]any{"type": "string"},
		},
	}
}

func TestRepairArgs_Coerce_BoolFromString(t *testing.T) {
	out, changes := RepairArgs(coerceParams(), map[string]any{"flag": "true"})
	if out["flag"] != true {
		t.Fatalf("flag = %#v", out["flag"])
	}
	if len(changes) != 1 || changes[0].Kind != ChangeCoerceType {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestRepairArgs_Coerce_NumberIsFloat64(t *testing.T) {
	out, _ := RepairArgs(coerceParams(), map[string]any{"count": "5"})
	f, ok := out["count"].(float64) // MUST be float64, not int
	if !ok || f != 5 {
		t.Fatalf("count = %#v (want float64 5)", out["count"])
	}
}

func TestRepairArgs_Coerce_NullableIntegerFromString(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"expand_turn": map[string]any{"type": []any{"integer", "null"}},
		},
	}
	out, changes := RepairArgs(params, map[string]any{"expand_turn": "1"})
	if got, ok := out["expand_turn"].(float64); !ok || got != 1 {
		t.Fatalf("expand_turn = %#v, want float64(1)", out["expand_turn"])
	}
	if len(changes) != 1 || changes[0].Kind != ChangeCoerceType || changes[0].Field != "expand_turn" {
		t.Fatalf("changes = %+v, want coercion of expand_turn", changes)
	}
}

func TestNullableUnionNonNullType(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want string
	}{
		{name: "any nullable integer", in: []any{"integer", "null"}, want: "integer"},
		{name: "string nullable boolean", in: []string{"null", "boolean"}, want: "boolean"},
		{name: "missing null", in: []any{"integer", "number"}},
		{name: "multiple non-null members", in: []any{"integer", "number", "null"}},
		{name: "duplicate null", in: []any{"integer", "null", "null"}},
		{name: "malformed member", in: []any{"integer", 1}},
		{name: "not a union", in: "integer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nullableUnionNonNullType(tc.in); got != tc.want {
				t.Fatalf("nullableUnionNonNullType(%#v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRepairArgs_Coerce_ScalarToArray(t *testing.T) {
	out, _ := RepairArgs(coerceParams(), map[string]any{"tags": "x"})
	if !reflect.DeepEqual(out["tags"], []any{"x"}) {
		t.Fatalf("tags = %#v", out["tags"])
	}
}

func TestRepairArgs_Coerce_NonNumericStringUntouched(t *testing.T) {
	out, changes := RepairArgs(coerceParams(), map[string]any{"count": "abc"})
	if out["count"] != "abc" {
		t.Fatalf("count = %#v", out["count"])
	}
	for _, c := range changes {
		if c.Field == "count" {
			t.Fatalf("unexpected coercion: %+v", c)
		}
	}
}

func TestRepairArgs_Coerce_NonFiniteNumberStringsUntouched(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-Inf"} {
		t.Run(value, func(t *testing.T) {
			out, changes := RepairArgs(coerceParams(), map[string]any{"count": value})
			if out["count"] != value {
				t.Fatalf("count = %#v, want original %q", out["count"], value)
			}
			for _, change := range changes {
				if change.Field == "count" {
					t.Fatalf("non-finite number was coerced: %+v", change)
				}
			}
		})
	}
}

func openParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties":           map[string]any{"file_path": map[string]any{"type": "string"}},
	}
}

func TestRepairArgs_DropUnknown_OnlyWhenClosed(t *testing.T) {
	out, changes := RepairArgs(readFileParams(), map[string]any{"file_path": "/x", "matchCase": true})
	if _, ok := out["matchCase"]; ok {
		t.Fatalf("matchCase not dropped: %v", out)
	}
	if len(changes) != 1 || changes[0].Kind != ChangeDropUnknown || changes[0].Field != "matchCase" {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestRepairArgs_DropUnknown_KeptWhenOpen(t *testing.T) {
	out, changes := RepairArgs(openParams(), map[string]any{"file_path": "/x", "extra": 1})
	if _, ok := out["extra"]; !ok {
		t.Fatal("extra dropped despite additionalProperties:true")
	}
	for _, c := range changes {
		if c.Kind == ChangeDropUnknown {
			t.Fatalf("unexpected drop: %+v", c)
		}
	}
}

func TestRepairArgs_Order_AliasBeforeDrop(t *testing.T) {
	// old_str should be aliased to old_string, NOT dropped as unknown.
	editParams := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path":  map[string]any{"type": "string"},
			"old_string": map[string]any{"type": "string"},
			"new_string": map[string]any{"type": "string"},
		},
	}
	out, changes := RepairArgs(editParams, map[string]any{"file_path": "/x", "old_str": "a", "new_string": "b"})
	if out["old_string"] != "a" {
		t.Fatalf("old_str not aliased: %v", out)
	}
	for _, c := range changes {
		if c.Kind == ChangeDropUnknown {
			t.Fatalf("old_str was dropped instead of aliased: %+v", c)
		}
	}
}

func emptyEnumParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"mode":     map[string]any{"type": "string", "enum": []any{"fast", "slow"}},
			"kind":     map[string]any{"type": "string", "enum": []string{"a", "b"}},
			"blankok":  map[string]any{"type": "string", "enum": []any{"", "x"}},
			"nullok":   map[string]any{"type": []any{"string", "null"}, "enum": []any{"x", nil}},
			"freeform": map[string]any{"type": "string"},
		},
		"required": []any{"kind"},
	}
}

func TestRepairArgs_EmptyOptionalEnumIsAbsent(t *testing.T) {
	for _, value := range []any{"", nil} {
		out, changes := RepairArgs(emptyEnumParams(), map[string]any{"kind": "a", "mode": value})
		if !reflect.DeepEqual(out, map[string]any{"kind": "a"}) {
			t.Fatalf("mode=%#v: got %v", value, out)
		}
		if len(changes) != 1 || changes[0].Kind != ChangeNormalizeDefault || changes[0].Field != "mode" {
			t.Fatalf("mode=%#v: changes = %+v", value, changes)
		}
	}
}

func TestRepairArgs_EmptyEnumKeptWhenRequiredOrAllowedOrNotEnum(t *testing.T) {
	args := map[string]any{"kind": "", "blankok": "", "nullok": nil, "freeform": ""}
	out, changes := RepairArgs(emptyEnumParams(), args)
	if !reflect.DeepEqual(out, args) {
		t.Fatalf("got %v, want unchanged %v", out, args)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no changes, got %+v", changes)
	}
}

func TestRepairArgs_EmptyOptionalEnumKeptPathIsLeft(t *testing.T) {
	args := map[string]any{"kind": "a", "mode": ""}
	out, changes := RepairArgs(emptyEnumParams(), args, "mode")
	if !reflect.DeepEqual(out, args) || len(changes) != 0 {
		t.Fatalf("kept path repaired: out=%v changes=%+v", out, changes)
	}
}

// nestedEnumParams nests emptyEnumParams under an object property and in
// array items, each level with its own required list.
func nestedEnumParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts":  emptyEnumParams(),
			"items": map[string]any{"type": "array", "items": emptyEnumParams()},
		},
	}
}

func nestedEnumArgs() map[string]any {
	return map[string]any{
		"opts": map[string]any{"kind": "a", "mode": ""},
		"items": []any{
			map[string]any{"kind": "b"},
			map[string]any{"kind": "", "mode": nil},
		},
	}
}

func TestRepairArgs_NestedEmptyOptionalEnumIsAbsent(t *testing.T) {
	args := nestedEnumArgs()
	out, changes := RepairArgs(nestedEnumParams(), args)
	want := map[string]any{
		"opts": map[string]any{"kind": "a"},
		"items": []any{
			map[string]any{"kind": "b"},
			map[string]any{"kind": ""}, // required at its own level, so left for validation
		},
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("got %v, want %v", out, want)
	}
	var fields []string
	for _, c := range changes {
		fields = append(fields, c.Field)
	}
	slices.Sort(fields)
	if !reflect.DeepEqual(fields, []string{"items[1].mode", "opts.mode"}) {
		t.Fatalf("changed fields = %v", fields)
	}
	if !reflect.DeepEqual(args, nestedEnumArgs()) {
		t.Fatalf("input mutated: %v", args)
	}
}

// nullOptionalParams declares one optional field of each JSON type a model
// leaves out by sending null, a nested object with its own required list, a
// schema-nullable field, and a required field.
func nullOptionalParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"name":     map[string]any{"type": "string"},
			"count":    map[string]any{"type": "integer"},
			"flag":     map[string]any{"type": "boolean"},
			"tags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"opts":     map[string]any{"type": "object", "properties": map[string]any{"depth": map[string]any{"type": "integer"}, "label": map[string]any{"type": "string"}}, "required": []any{"label"}},
			"nullable": map[string]any{"type": []any{"string", "null"}},
			"id":       map[string]any{"type": "string"},
		},
		"required": []any{"id"},
	}
}

func TestRepairArgs_NullOptionalIsAbsent(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        map[string]any
		keep        []string
		want        map[string]any
		wantChanged []string
	}{
		{name: "string", args: map[string]any{"id": "x", "name": nil}, want: map[string]any{"id": "x"}, wantChanged: []string{"name"}},
		{name: "integer", args: map[string]any{"id": "x", "count": nil}, want: map[string]any{"id": "x"}, wantChanged: []string{"count"}},
		{name: "boolean", args: map[string]any{"id": "x", "flag": nil}, want: map[string]any{"id": "x"}, wantChanged: []string{"flag"}},
		{name: "array", args: map[string]any{"id": "x", "tags": nil}, want: map[string]any{"id": "x"}, wantChanged: []string{"tags"}},
		{name: "object", args: map[string]any{"id": "x", "opts": nil}, want: map[string]any{"id": "x"}, wantChanged: []string{"opts"}},
		{name: "nested", args: map[string]any{"id": "x", "opts": map[string]any{"label": "l", "depth": nil}}, want: map[string]any{"id": "x", "opts": map[string]any{"label": "l"}}, wantChanged: []string{"opts.depth"}},
		{name: "required stays for validation", args: map[string]any{"id": nil}, want: map[string]any{"id": nil}},
		{name: "nested required stays for validation", args: map[string]any{"id": "x", "opts": map[string]any{"label": nil}}, want: map[string]any{"id": "x", "opts": map[string]any{"label": nil}}},
		{name: "schema-nullable stays null", args: map[string]any{"id": "x", "nullable": nil}, want: map[string]any{"id": "x", "nullable": nil}},
		{name: "handler-judged stays", args: map[string]any{"id": "x", "count": nil}, keep: []string{"count"}, want: map[string]any{"id": "x", "count": nil}},
		{name: "empty plain string stays", args: map[string]any{"id": "x", "name": ""}, want: map[string]any{"id": "x", "name": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, changes := RepairArgs(nullOptionalParams(), tc.args, tc.keep...)
			if !reflect.DeepEqual(out, tc.want) {
				t.Fatalf("got %v, want %v", out, tc.want)
			}
			var changed []string
			for _, c := range changes {
				if c.Kind != ChangeNormalizeDefault {
					t.Fatalf("unexpected change %+v", c)
				}
				changed = append(changed, c.Field)
			}
			if !slices.Equal(changed, tc.wantChanged) {
				t.Fatalf("changed fields = %v, want %v", changed, tc.wantChanged)
			}
		})
	}
}
