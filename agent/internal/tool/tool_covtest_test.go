package tool

import (
	"reflect"
	"strings"
	"testing"
)

func TestCovNormalizeAskUserArgs(t *testing.T) {
	batch := map[string]any{
		"questions": []any{
			map[string]any{
				"question": "Which?",
				"options":  []any{map[string]any{"label": "A", "detail": "first"}},
			},
		},
	}
	out, err := normalizeAskUserArgs(batch)
	if err != nil {
		t.Fatalf("batch form: %v", err)
	}
	if !reflect.DeepEqual(out, batch) {
		t.Fatalf("batch form normalized to %#v, want %#v", out, batch)
	}

	options := []any{
		map[string]any{"label": "A", "detail": "first"},
		map[string]any{"label": "B", "detail": "second"},
	}
	shorthand := map[string]any{
		"question":      "Which one?",
		"options":       options,
		"why":           "need to know",
		"if_unanswered": "skip",
		"multi_select":  true,
		"header":        "Choose",
	}
	want := map[string]any{
		"questions": []any{
			map[string]any{
				"question":      "Which one?",
				"options":       options,
				"why":           "need to know",
				"if_unanswered": "skip",
				"multi_select":  true,
				"header":        "Choose",
			},
		},
	}
	out, err = normalizeAskUserArgs(shorthand)
	if err != nil {
		t.Fatalf("shorthand form: %v", err)
	}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("shorthand normalized to %#v, want %#v", out, want)
	}

	minimal := map[string]any{
		"question":      "q",
		"options":       []any{},
		"why":           "",
		"if_unanswered": "",
		"multi_select":  false,
		"header":        "",
	}
	wantMinimal := map[string]any{
		"questions": []any{
			map[string]any{"question": "q", "options": []any{}},
		},
	}
	out, err = normalizeAskUserArgs(minimal)
	if err != nil {
		t.Fatalf("minimal shorthand: %v", err)
	}
	if !reflect.DeepEqual(out, wantMinimal) {
		t.Fatalf("minimal shorthand normalized to %#v, want %#v", out, wantMinimal)
	}

	invalid := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "both forms",
			args: map[string]any{"questions": []any{}, "question": "q"},
			want: "both 'questions' and 'question'",
		},
		{
			name: "shorthand without options",
			args: map[string]any{"question": "q"},
			want: "'options' is required",
		},
		{
			name: "neither form",
			args: map[string]any{},
			want: "'questions' is required",
		},
		{
			name: "batch plus top-level options",
			args: map[string]any{"questions": []any{}, "options": []any{}},
			want: "'questions' is required",
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeAskUserArgs(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("normalizeAskUserArgs() = (%#v, %v), want nil and error containing %q", got, err, tc.want)
			}
			if got != nil {
				t.Fatalf("invalid arguments returned %#v, want nil", got)
			}
		})
	}
}
