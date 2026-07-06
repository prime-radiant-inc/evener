package repair

import (
	"reflect"
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
