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

func TestReasoningEffortRank(t *testing.T) {
	if ReasoningEffortRank("max") != ReasoningEffortRank("xhigh") {
		t.Errorf("max and xhigh should share a rank: max=%d xhigh=%d", ReasoningEffortRank("max"), ReasoningEffortRank("xhigh"))
	}
	ladder := []string{"minimal", "low", "high", "max"}
	for i := 1; i < len(ladder); i++ {
		if ReasoningEffortRank(ladder[i-1]) >= ReasoningEffortRank(ladder[i]) {
			t.Errorf("ranks should be strictly increasing: %s(%d) is not < %s(%d)",
				ladder[i-1], ReasoningEffortRank(ladder[i-1]), ladder[i], ReasoningEffortRank(ladder[i]))
		}
	}
	if ReasoningEffortRank("") != 0 || ReasoningEffortRank("bogus") != 0 {
		t.Error("unknown/empty effort should rank 0")
	}
	if ReasoningEffortRank("MAX") != ReasoningEffortRank("max") {
		t.Error("rank should be case-insensitive")
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

func TestIsOpenAICompatEncryptedReasoning(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{name: "compat array", in: `[{"type":"reasoning.encrypted","id":"rc_1","data":"D"}]`, want: true},
		{name: "multi item", in: `[{"type":"reasoning.encrypted","id":"a","data":"x"},{"type":"reasoning.encrypted","id":"b","data":"y"}]`, want: true},
		{name: "empty", in: "", want: false},
		{name: "openai opaque blob", in: "gAAAAABopaqueblob", want: false},
		{name: "json array of other items", in: `[{"type":"reasoning.text","text":"t"}]`, want: false},
		{name: "mixed types rejected", in: `[{"type":"reasoning.encrypted","id":"a","data":"x"},{"type":"other"}]`, want: false},
		{name: "empty array", in: `[]`, want: false},
		{name: "not json", in: "[broken", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsOpenAICompatEncryptedReasoning(tc.in); got != tc.want {
				t.Errorf("IsOpenAICompatEncryptedReasoning(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
