package repair

import (
	"reflect"
	"slices"
	"strings"
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

func TestRepairArgs_NestedTaskItemAliases(t *testing.T) {
	t.Parallel()
	// task_list add: title -> description
	taskListParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"add": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"description": map[string]any{"type": "string"},
						"prompt":      map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	out, changes := RepairArgs(taskListParams, map[string]any{
		"add": []any{
			map[string]any{"title": "step 1", "prompt": "p1"},
		},
	})
	addItems := out["add"].([]any)
	first := addItems[0].(map[string]any)
	if first["description"] != "step 1" {
		t.Fatalf("description = %v, want step 1", first["description"])
	}
	if _, hasTitle := first["title"]; hasTitle {
		t.Fatal("title should be removed")
	}
	var foundAlias bool
	for _, c := range changes {
		if c.Kind == ChangeAlias && c.Field == "add[0].description" {
			foundAlias = true
			break
		}
	}
	if !foundAlias {
		t.Fatalf("changes missing add[0].description alias: %+v", changes)
	}

	// delegate task_list: description -> title
	delegateParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task_list": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":  map[string]any{"type": "string"},
						"prompt": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	outDel, changesDel := RepairArgs(delegateParams, map[string]any{
		"task_list": []any{
			map[string]any{"description": "step 1", "prompt": "p1"},
		},
	})
	delItems := outDel["task_list"].([]any)
	firstDel := delItems[0].(map[string]any)
	if firstDel["title"] != "step 1" {
		t.Fatalf("title = %v, want step 1", firstDel["title"])
	}
	if _, hasDesc := firstDel["description"]; hasDesc {
		t.Fatal("description should be removed")
	}
	foundAlias = false
	for _, c := range changesDel {
		if c.Kind == ChangeAlias && c.Field == "task_list[0].title" {
			foundAlias = true
			break
		}
	}
	if !foundAlias {
		t.Fatalf("changes missing task_list[0].title alias: %+v", changesDel)
	}
}

func TestRepairArgs_DoesNotMutateNestedTaskArgs(t *testing.T) {
	t.Parallel()
	taskListParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"add": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"description": map[string]any{"type": "string"},
						"prompt":      map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	item := map[string]any{"title": "step 1", "prompt": "p1"}
	in := map[string]any{
		"add": []any{item},
	}
	out, _ := RepairArgs(taskListParams, in)
	if _, hasDesc := item["description"]; hasDesc {
		t.Fatal("RepairArgs mutated input item map by adding description")
	}
	if _, hasTitle := item["title"]; !hasTitle {
		t.Fatal("RepairArgs mutated input item map by deleting title")
	}
	outItem := out["add"].([]any)[0].(map[string]any)
	if outItem["description"] != "step 1" {
		t.Fatalf("out description = %v, want step 1", outItem["description"])
	}
}

func TestRepairArgs_DoesNotCorruptMCPToolDeclaringBothTitleAndDescription(t *testing.T) {
	t.Parallel()
	mcpParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"add": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title":       map[string]any{"type": "string"},
						"description": map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	args := map[string]any{
		"add": []any{
			map[string]any{"title": "my title", "prompt": "p1"},
		},
	}
	out, changes := RepairArgs(mcpParams, args)
	if len(changes) != 0 {
		t.Fatalf("expected no changes for MCP tool declaring both title and description, got %+v", changes)
	}
	outItem := out["add"].([]any)[0].(map[string]any)
	if outItem["title"] != "my title" {
		t.Fatalf("title = %v, want my title", outItem["title"])
	}
	if _, hasDesc := outItem["description"]; hasDesc {
		t.Fatal("description should not be added when schema declares title")
	}
}

func TestRepairArgs_DuplicateAliasKeyRecordsChange(t *testing.T) {
	t.Parallel()
	taskListParams := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"add": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"description": map[string]any{"type": "string"},
						"prompt":      map[string]any{"type": "string"},
					},
				},
			},
		},
	}
	args := map[string]any{
		"add": []any{
			map[string]any{"title": "my title", "description": "my desc", "prompt": "p1"},
		},
	}
	out, changes := RepairArgs(taskListParams, args)
	if len(changes) == 0 {
		t.Fatal("deleting duplicate alias key must record a change")
	}
	outItem := out["add"].([]any)[0].(map[string]any)
	if outItem["description"] != "my desc" {
		t.Fatalf("description = %v, want my desc", outItem["description"])
	}
	if _, hasTitle := outItem["title"]; hasTitle {
		t.Fatal("title duplicate should be removed")
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

// A lone object sent for an array is wrapped first, then judged as an item, so
// an empty optional enum inside it is dropped like any other item's.
func TestRepairArgs_ScalarWrappedArrayItemEmptyEnumIsAbsent(t *testing.T) {
	params := map[string]any{"properties": map[string]any{"items": map[string]any{
		"type": "array",
		"items": map[string]any{
			"type":       "object",
			"properties": map[string]any{"mode": map[string]any{"type": "string", "enum": []any{"a", "b"}}},
			"required":   []any{"x"},
		},
	}}}
	out, _ := RepairArgs(params, map[string]any{"items": map[string]any{"x": "y", "mode": ""}})
	if want := map[string]any{"items": []any{map[string]any{"x": "y"}}}; !reflect.DeepEqual(out, want) {
		t.Fatalf("got %v, want %v", out, want)
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

// A property defined through a combinator accepts null only as its branches
// say: anyOf and oneOf when some branch does, allOf when every branch does.
func TestRepairArgs_NullOnApplicatorPropertyFollowsItsBranches(t *testing.T) {
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "integer"}
	null := map[string]any{"type": "null"}
	for _, tc := range []struct {
		name     string
		schema   map[string]any
		wantKept bool
	}{
		{name: "anyOf without null", schema: map[string]any{"anyOf": []any{str, num}}},
		{name: "anyOf optional shape", schema: map[string]any{"anyOf": []any{str, null}}, wantKept: true},
		{name: "oneOf without null", schema: map[string]any{"oneOf": []any{str, num}}},
		{name: "oneOf with null", schema: map[string]any{"oneOf": []any{null, str}}, wantKept: true},
		{name: "allOf with a branch refusing null", schema: map[string]any{"allOf": []any{map[string]any{"type": []any{"string", "null"}}, str}}},
		{name: "allOf every branch accepting null", schema: map[string]any{"allOf": []any{map[string]any{"type": []any{"string", "null"}}, map[string]any{"maxLength": 3}}}, wantKept: true},
		{name: "nested anyOf without null", schema: map[string]any{"anyOf": []any{map[string]any{"anyOf": []any{str, num}}, num}}},
		{name: "unresolved ref", schema: map[string]any{"$ref": "#/$defs/name"}, wantKept: true},
		{name: "anyOf with a false branch", schema: map[string]any{"anyOf": []any{str, false}}},
		{name: "anyOf null beside a false branch", schema: map[string]any{"anyOf": []any{null, false}}, wantKept: true},
		{name: "allOf with a true branch", schema: map[string]any{"allOf": []any{true, str}}},
		{name: "not null", schema: map[string]any{"not": null}},
		{name: "not string", schema: map[string]any{"not": str}, wantKept: true},
		{name: "not an anyOf", schema: map[string]any{"not": map[string]any{"anyOf": []any{null, str}}}, wantKept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := map[string]any{"type": "object", "properties": map[string]any{"v": tc.schema}}
			out, _ := RepairArgs(params, map[string]any{"v": nil})
			if _, kept := out["v"]; kept != tc.wantKept {
				t.Fatalf("null kept = %t, want %t; out=%v", kept, tc.wantKept, out)
			}
		})
	}
}

// multiEditParams mirrors the edits[]-only edit_file schema (additionalProperties: false on root and items).
func multiEditParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
			"edits": map[string]any{
				"type":     "array",
				"minItems": 1,
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"properties": map[string]any{
						"old_string":  map[string]any{"type": "string"},
						"new_string":  map[string]any{"type": "string"},
						"replace_all": map[string]any{"type": "boolean"},
					},
					"required": []string{"old_string", "new_string"},
				},
			},
		},
		"required": []string{"file_path", "edits"},
	}
}

func cloneTestValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		cp := make(map[string]any, len(x))
		for k, val := range x {
			cp[k] = cloneTestValue(val)
		}
		return cp
	case []any:
		cp := make([]any, len(x))
		for i, val := range x {
			cp[i] = cloneTestValue(val)
		}
		return cp
	default:
		return v
	}
}

func TestRepairArgs_SingleItemArrayPromotion_LegacyTopLevel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args map[string]any
		want map[string]any
	}{
		{
			name: "canonical top-level fields",
			args: map[string]any{
				"file_path":   "a.go",
				"old_string":  "old",
				"new_string":  "new",
				"replace_all": true,
			},
			want: map[string]any{
				"file_path": "a.go",
				"edits": []any{
					map[string]any{
						"old_string":  "old",
						"new_string":  "new",
						"replace_all": true,
					},
				},
			},
		},
		{
			name: "aliased top-level fields",
			args: map[string]any{
				"path":    "b.go",
				"old_str": "old",
				"new_str": "new",
			},
			want: map[string]any{
				"file_path": "b.go",
				"edits": []any{
					map[string]any{
						"old_string": "old",
						"new_string": "new",
					},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			before := cloneTestValue(tc.args)
			out, changes := RepairArgs(multiEditParams(), tc.args)
			if !reflect.DeepEqual(tc.args, before) {
				t.Fatalf("input mutated: before=%#v after=%#v", before, tc.args)
			}
			if !reflect.DeepEqual(out, tc.want) {
				t.Fatalf("got %#v, want %#v", out, tc.want)
			}
			if len(changes) == 0 {
				t.Fatal("expected recorded changes for promoted call")
			}
		})
	}
}

func TestRepairArgs_SingleItemArrayPromotion_IncompleteRequiredNotPromoted(t *testing.T) {
	t.Parallel()
	args := map[string]any{
		"file_path":  "a.go",
		"old_string": "only_old",
	}
	out, _ := RepairArgs(multiEditParams(), args)
	if _, hasEdits := out["edits"]; hasEdits {
		t.Fatalf("incomplete top-level fields should not promote to edits: %#v", out)
	}
}

func TestRepairArgs_MixedFormPreservesConflictingRootKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		editsVal  any
		extraKeys map[string]any
	}{
		{
			name:      "edits array plus top-level old_string and new_string",
			editsVal:  []any{map[string]any{"old_string": "x", "new_string": "y"}},
			extraKeys: map[string]any{"old_string": "a", "new_string": "b"},
		},
		{
			name:      "edits null plus top-level old_string and new_string",
			editsVal:  nil,
			extraKeys: map[string]any{"old_string": "a", "new_string": "b"},
		},
		{
			name:      "edits null plus single aliased conflict",
			editsVal:  nil,
			extraKeys: map[string]any{"old_str": "a"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{
				"file_path": "a.go",
				"edits":     tc.editsVal,
			}
			for k, v := range tc.extraKeys {
				args[k] = v
			}
			before := cloneTestValue(args)
			out, _ := RepairArgs(multiEditParams(), args)
			if !reflect.DeepEqual(args, before) {
				t.Fatalf("input mutated: before=%#v after=%#v", before, args)
			}
			if _, hasEdits := out["edits"]; !hasEdits {
				t.Fatalf("edits key was removed: %#v", out)
			}
			for k, wantV := range tc.extraKeys {
				gotV, present := out[k]
				if !present || !reflect.DeepEqual(gotV, wantV) {
					t.Fatalf("conflicting root key %q was dropped or altered: got=(%#v, %t), want=%#v, out=%#v", k, gotV, present, wantV, out)
				}
			}
		})
	}
}

func TestRepairArgs_CombinedQuirk_StringifiedAndBareObject(t *testing.T) {
	t.Parallel()
	want := map[string]any{
		"file_path": "a.go",
		"edits": []any{
			map[string]any{
				"old_string":  "a",
				"new_string":  "b",
				"replace_all": true,
			},
		},
	}
	for _, tc := range []struct {
		name  string
		edits any
	}{
		{
			name:  "stringified JSON array",
			edits: `[{"old_str":"a","newText":"b","replaceAll":"true","notes":"x"}]`,
		},
		{
			name:  "stringified single JSON object",
			edits: `{"old_str":"a","newText":"b","replaceAll":"true","notes":"x"}`,
		},
		{
			name: "bare object",
			edits: map[string]any{
				"old_str":    "a",
				"newText":    "b",
				"replaceAll": "true",
				"notes":      "x",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{
				"file_path": "a.go",
				"edits":     tc.edits,
			}
			before := cloneTestValue(args)
			out, changes := RepairArgs(multiEditParams(), args)
			if !reflect.DeepEqual(args, before) {
				t.Fatalf("input mutated: before=%#v after=%#v", before, args)
			}
			if !reflect.DeepEqual(out, want) {
				t.Fatalf("got %#v, want %#v", out, want)
			}
			fields := map[string]bool{}
			for _, c := range changes {
				fields[c.Field] = true
			}
			for _, requiredField := range []string{"edits", "edits[0].old_string", "edits[0].new_string", "edits[0].replace_all", "edits[0].notes"} {
				if !fields[requiredField] {
					t.Fatalf("expected change on %q, got changes=%+v", requiredField, changes)
				}
			}
		})
	}
}

func TestRepairArgs_CompetingAliases(t *testing.T) {
	t.Parallel()
	t.Run("differing values inside edits item preserved deterministically", func(t *testing.T) {
		t.Parallel()
		args := map[string]any{
			"file_path": "a.go",
			"edits": []any{
				map[string]any{
					"old_string": "a",
					"new_str":    "x",
					"newText":    "y",
				},
			},
		}
		before := cloneTestValue(args)
		out, _ := RepairArgs(multiEditParams(), args)
		if !reflect.DeepEqual(args, before) {
			t.Fatalf("input mutated: before=%#v after=%#v", before, args)
		}
		item := out["edits"].([]any)[0].(map[string]any)
		if _, hasCanon := item["new_string"]; hasCanon {
			t.Fatalf("conflicting aliases should not pick an arbitrary winner: %#v", item)
		}
		if item["new_str"] != "x" || item["newText"] != "y" {
			t.Fatalf("competing aliases must be preserved past dropUnknown: %#v", item)
		}
		outAgain, _ := RepairArgs(multiEditParams(), args)
		if !reflect.DeepEqual(outAgain, out) {
			t.Fatalf("non-deterministic output: first=%#v second=%#v", out, outAgain)
		}
	})

	t.Run("differing values at root preserved deterministically", func(t *testing.T) {
		t.Parallel()
		args := map[string]any{
			"path":     "/a",
			"filepath": "/b",
		}
		out, _ := RepairArgs(readFileParams(), args)
		if _, hasCanon := out["file_path"]; hasCanon {
			t.Fatalf("conflicting root aliases should not pick an arbitrary winner: %#v", out)
		}
		if out["path"] != "/a" || out["filepath"] != "/b" {
			t.Fatalf("competing root aliases must be preserved past dropUnknown: %#v", out)
		}
	})

	t.Run("identical values promote once and drop duplicate", func(t *testing.T) {
		t.Parallel()
		args := map[string]any{
			"file_path": "a.go",
			"edits": []any{
				map[string]any{
					"old_string": "a",
					"new_str":    "same",
					"newText":    "same",
				},
			},
		}
		out, _ := RepairArgs(multiEditParams(), args)
		want := map[string]any{
			"file_path": "a.go",
			"edits": []any{
				map[string]any{
					"old_string": "a",
					"new_string": "same",
				},
			},
		}
		if !reflect.DeepEqual(out, want) {
			t.Fatalf("got %#v, want %#v", out, want)
		}
	})

	t.Run("canonical key wins when present alongside competing aliases", func(t *testing.T) {
		t.Parallel()
		args := map[string]any{
			"file_path": "a.go",
			"edits": []any{
				map[string]any{
					"old_string": "a",
					"new_string": "canonical",
					"new_str":    "x",
					"newText":    "y",
				},
			},
		}
		out, _ := RepairArgs(multiEditParams(), args)
		want := map[string]any{
			"file_path": "a.go",
			"edits": []any{
				map[string]any{
					"old_string": "a",
					"new_string": "canonical",
				},
			},
		}
		if !reflect.DeepEqual(out, want) {
			t.Fatalf("got %#v, want %#v", out, want)
		}
	})
}

func TestRepairArgs_ObjectArrayEdgeCasesAndNonMutation(t *testing.T) {
	t.Parallel()

	t.Run("plain non-JSON string is not wrapped", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{"not json", "123", "true", "null"} {
			args := map[string]any{"file_path": "a.go", "edits": raw}
			out, changes := RepairArgs(multiEditParams(), args)
			if out["edits"] != raw {
				t.Fatalf("raw=%q was wrapped or changed: %#v", raw, out["edits"])
			}
			if len(changes) != 0 {
				t.Fatalf("raw=%q recorded unexpected changes: %+v", raw, changes)
			}
		}
	})

	t.Run("deeply nested JSON string exceeds depth guard and stays untouched", func(t *testing.T) {
		t.Parallel()
		deep := strings.Repeat("[", 66) + `{"old_str":"a","new_str":"b"}` + strings.Repeat("]", 66)
		args := map[string]any{"file_path": "a.go", "edits": deep}
		out, _ := RepairArgs(multiEditParams(), args)
		if out["edits"] != deep {
			t.Fatalf("overly deep JSON string should be left untouched")
		}
	})

	t.Run("malformed array items do not panic and valid siblings still repair", func(t *testing.T) {
		t.Parallel()
		args := map[string]any{
			"file_path": "a.go",
			"edits": []any{
				nil,
				"str",
				map[string]any{"old_str": "a", "new_str": "b", "notes": "x"},
			},
		}
		before := cloneTestValue(args)
		out, _ := RepairArgs(multiEditParams(), args)
		if !reflect.DeepEqual(args, before) {
			t.Fatalf("input mutated: before=%#v after=%#v", before, args)
		}
		edits := out["edits"].([]any)
		if edits[0] != nil || edits[1] != "str" {
			t.Fatalf("malformed items altered unexpectedly: %#v", edits)
		}
		wantThird := map[string]any{"old_string": "a", "new_string": "b"}
		if !reflect.DeepEqual(edits[2], wantThird) {
			t.Fatalf("valid sibling item = %#v, want %#v", edits[2], wantThird)
		}
	})

	t.Run("typo old_sting drops unknown and leaves old_string missing", func(t *testing.T) {
		t.Parallel()
		args := map[string]any{
			"file_path": "a.go",
			"edits": []any{
				map[string]any{"old_sting": "a", "new_string": "b"},
			},
		}
		before := cloneTestValue(args)
		out, _ := RepairArgs(multiEditParams(), args)
		if !reflect.DeepEqual(args, before) {
			t.Fatalf("input mutated: before=%#v after=%#v", before, args)
		}
		item := out["edits"].([]any)[0].(map[string]any)
		if _, hasOld := item["old_string"]; hasOld {
			t.Fatalf("typo old_sting must not heal to old_string: %#v", item)
		}
		if _, hasTypo := item["old_sting"]; hasTypo {
			t.Fatalf("typo old_sting should be dropped by additionalProperties:false: %#v", item)
		}
	})
}
