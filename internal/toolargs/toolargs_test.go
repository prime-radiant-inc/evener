package toolargs

import "testing"

func TestFirstNonEmpty(t *testing.T) {
	get := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		name string
		m    map[string]string
		keys []string
		want string
	}{
		{"first wins", map[string]string{"intent": "a", "purpose": "b"}, []string{"intent", "purpose"}, "a"},
		{"fallback to second", map[string]string{"purpose": "b"}, []string{"intent", "purpose"}, "b"},
		{"empty first skipped", map[string]string{"intent": "", "purpose": "b"}, []string{"intent", "purpose"}, "b"},
		{"all empty -> empty", map[string]string{"intent": "", "purpose": ""}, []string{"intent", "purpose"}, ""},
		{"no keys -> empty", map[string]string{"intent": "a"}, nil, ""},
		{"missing key skipped", map[string]string{"description": "d"}, []string{"intent", "purpose", "description"}, "d"},
		{"single key", map[string]string{"intent": "x"}, []string{"intent"}, "x"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FirstNonEmpty(get(tc.m), tc.keys...); got != tc.want {
				t.Fatalf("FirstNonEmpty(%v, %v) = %q, want %q", tc.m, tc.keys, got, tc.want)
			}
		})
	}
}

func TestFirstNonEmptyDeterministic(t *testing.T) {
	get := func(k string) string {
		switch k {
		case "intent":
			return "why"
		case "purpose":
			return "fallback"
		}
		return ""
	}
	keys := []string{"intent", "purpose", "description"}
	a := FirstNonEmpty(get, keys...)
	b := FirstNonEmpty(get, keys...)
	if a != b || a != "why" {
		t.Fatalf("non-deterministic or wrong: %q vs %q", a, b)
	}
}
