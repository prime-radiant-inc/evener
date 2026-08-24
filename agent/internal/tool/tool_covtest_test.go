package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCovNormalizeAskUserArgs covers all branches of normalizeAskUserArgs
// (registry.go lines 141-206).
func TestCovNormalizeAskUserArgs(t *testing.T) {
	// Case 1: questions present, no question/options → use as-is.
	args := map[string]any{"questions": []any{map[string]any{"question": "Which?", "options": []any{}}}}
	out, err := normalizeAskUserArgs(args)
	if err != nil || out == nil {
		t.Fatalf("case 1: out=%v err=%v", out, err)
	}

	// Case 2: question + options present, no questions → wrap into batch form.
	args = map[string]any{
		"question":      "Which one?",
		"options":       []any{map[string]any{"label": "A"}},
		"why":           "need to know",
		"if_unanswered": "skip",
		"multi_select":  true,
		"header":        "Choose",
	}
	out, err = normalizeAskUserArgs(args)
	if err != nil {
		t.Fatalf("case 2: err=%v", err)
	}
	questions, ok := out["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("case 2: questions=%v", out["questions"])
	}
	wrapped, ok := questions[0].(map[string]any)
	if !ok {
		t.Fatalf("case 2: wrapped type = %T", questions[0])
	}
	if wrapped["question"] != "Which one?" || wrapped["why"] != "need to know" ||
		wrapped["if_unanswered"] != "skip" || wrapped["multi_select"] != true ||
		wrapped["header"] != "Choose" {
		t.Fatalf("case 2: wrapped = %+v", wrapped)
	}

	// Case 2 without optional fields.
	args = map[string]any{"question": "q", "options": []any{}}
	out, err = normalizeAskUserArgs(args)
	if err != nil {
		t.Fatalf("case 2 minimal: err=%v", err)
	}
	questions, _ = out["questions"].([]any)
	if len(questions) != 1 {
		t.Fatalf("case 2 minimal: questions = %v", out["questions"])
	}

	// Sub-case 3a: both questions and question present → error.
	_, err = normalizeAskUserArgs(map[string]any{
		"questions": []any{},
		"question":  "q",
	})
	if err == nil || !strings.Contains(err.Error(), "both 'questions' and 'question'") {
		t.Fatalf("case 3a: err=%v", err)
	}

	// Sub-case 3b: question present but options missing → error.
	_, err = normalizeAskUserArgs(map[string]any{"question": "q"})
	if err == nil || !strings.Contains(err.Error(), "'options' is required") {
		t.Fatalf("case 3b: err=%v", err)
	}

	// Sub-case 3c: neither questions nor question+options → error.
	_, err = normalizeAskUserArgs(map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "'questions' is required") {
		t.Fatalf("case 3c: err=%v", err)
	}

	// Sub-case 3a: both questions and options present (no question).
	_, err = normalizeAskUserArgs(map[string]any{
		"questions": []any{},
		"options":   []any{},
	})
	if err == nil || !strings.Contains(err.Error(), "'questions' is required") {
		t.Fatalf("case questions+options: err=%v", err)
	}
}

// TestCovDefManageWorktreeDisposeOnly covers DefManageWorktreeDisposeOnly
// (definitions.go lines 684+).
func TestCovDefManageWorktreeDisposeOnly(t *testing.T) {
	def := DefManageWorktreeDisposeOnly()
	if def.Name != "manage_worktree" {
		t.Fatalf("Name = %q", def.Name)
	}
	if !strings.Contains(def.Description, "dispose") {
		t.Fatalf("Description should mention dispose: %q", def.Description)
	}
	params := def.Parameters
	if params["type"] != "object" {
		t.Fatalf("type = %v", params["type"])
	}
	// Verify the schema is valid JSON.
	_, err := json.Marshal(def)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
}
