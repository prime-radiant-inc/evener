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
