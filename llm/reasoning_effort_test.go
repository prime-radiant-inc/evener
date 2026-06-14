package llm

import "testing"

func TestClampReasoningEffort(t *testing.T) {
	cases := []struct {
		name      string
		requested string
		levels    []string
		want      string
	}{
		{"xhigh clamps to high when model tops out at high",
			"xhigh", []string{"minimal", "low", "medium", "high"}, "high"},
		{"max clamps to high",
			"max", []string{"low", "medium", "high"}, "high"},
		{"supported level passes through",
			"minimal", []string{"minimal", "low", "medium", "high"}, "minimal"},
		{"below-minimum clamps up to lowest supported",
			"low", []string{"medium", "high"}, "medium"},
		{"empty request passes through (no reasoning)",
			"", []string{"low", "medium", "high"}, ""},
		{"none passes through (reasoning disabled)",
			"none", []string{"low", "medium", "high"}, "none"},
		{"no model levels means no clamping",
			"xhigh", nil, "xhigh"},
		{"xhigh allowed when model supports it",
			"xhigh", []string{"low", "medium", "high", "xhigh"}, "xhigh"},
		{"max treated as top tier equal to xhigh",
			"max", []string{"low", "high", "xhigh"}, "xhigh"},
		{"unknown level passes through",
			"turbo", []string{"low", "medium", "high"}, "turbo"},
		{"case-insensitive match",
			"HIGH", []string{"low", "medium", "high"}, "high"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampReasoningEffort(tc.requested, tc.levels); got != tc.want {
				t.Errorf("ClampReasoningEffort(%q, %v) = %q, want %q", tc.requested, tc.levels, got, tc.want)
			}
		})
	}
}

func TestNormalizeReasoningEffort(t *testing.T) {
	cases := map[string]string{
		"none": "", "null": "", "off": "", "false": "", "0": "",
		"NONE": "", "  none  ": "",
		"minimal": "minimal", "HIGH": "high", "xhigh": "xhigh", "": "",
		"turbo": "turbo",
	}
	for in, want := range cases {
		if got := NormalizeReasoningEffort(in); got != want {
			t.Errorf("NormalizeReasoningEffort(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReasoningBudget_MinimalAndXHigh(t *testing.T) {
	if ReasoningBudget("minimal") <= 0 {
		t.Errorf("ReasoningBudget(minimal) = %d, want a small positive budget", ReasoningBudget("minimal"))
	}
	if ReasoningBudget("xhigh") != ReasoningBudget("max") {
		t.Errorf("ReasoningBudget(xhigh) = %d, want it to equal max %d", ReasoningBudget("xhigh"), ReasoningBudget("max"))
	}
}
