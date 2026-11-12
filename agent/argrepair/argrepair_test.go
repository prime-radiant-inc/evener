package argrepair_test

import (
	"testing"

	"primeradiant.com/evener/agent/argrepair"
)

func TestRepairArgs_AliasesTaskListTitleToDescription(t *testing.T) {
	t.Parallel()
	params := map[string]any{
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
			map[string]any{"title": "step 1", "prompt": "p1"},
		},
	}
	out, _ := argrepair.RepairArgs(params, args)
	item := out["add"].([]any)[0].(map[string]any)
	if item["description"] != "step 1" {
		t.Fatalf("description = %v, want step 1", item["description"])
	}
	if _, hasTitle := item["title"]; hasTitle {
		t.Fatal("title should be removed")
	}
}

func TestRepairArgs_AliasesDelegateDescriptionToTitle(t *testing.T) {
	t.Parallel()
	params := map[string]any{
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
	args := map[string]any{
		"task_list": []any{
			map[string]any{"description": "step 1", "prompt": "p1"},
		},
	}
	out, _ := argrepair.RepairArgs(params, args)
	item := out["task_list"].([]any)[0].(map[string]any)
	if item["title"] != "step 1" {
		t.Fatalf("title = %v, want step 1", item["title"])
	}
	if _, hasDesc := item["description"]; hasDesc {
		t.Fatal("description should be removed")
	}
}
