package tui

import (
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-tui/internal/tuitheme"
)

// Every named sample has an independent required/forbidden signature. The
// pairwise comparison is only a supplemental guard: branch swaps must fail the
// signature before inequality is considered.
func TestCovSampleRenderFromRealWidget_SemanticFixtures(t *testing.T) {
	withTestColorProfile(t)
	tuitheme.SetTheme("dark")
	tests := []struct {
		name      string
		width     int
		required  []string
		forbidden []string
	}{
		{"dashboard-narrow", 60, []string{"EVENER LIVE", "\nq quit"}, []string{"details", "dashboard  q quit"}},
		{"dashboard-normal", 80, []string{"EVENER LIVE", "dashboard  q quit"}, []string{"\nq quit", "details"}},
		{"dashboard-wide", 200, []string{"EVENER LIVE", "details", "Action:   enter launches"}, []string{"\nq quit"}},
		{"session-idle", 80, []string{"draft stays visible", "enter send"}, []string{"all task steps completed", "FORK DRAFT"}},
		{"session-streaming", 80, []string{"\n┃  > What agent harness", "all task steps completed"}, []string{"esc/i/q: compose", "▶ ┃  >"}},
		{"session-busy-steer", 80, []string{"Please also check old TUI command parity", "ctrl+s steer"}, []string{"read-only:", "draft kept"}},
		{"session-busy-readonly", 80, []string{"draft kept", "read-only:"}, []string{"ctrl+s steer", "Please also check"}},
		{"session-browse", 80, []string{"▶ ┃  > What agent harness", "esc/i/q: compose"}, []string{"\n┃  > What agent harness", "draft stays visible"}},
		{"session-fork", 80, []string{"edited prompt", "FORK DRAFT"}, []string{"draft stays visible", "ctrl+s steer"}},
		{"ask-card-pending", 80, []string{"Which datastore for the ingest path?", "● IDLE"}, []string{"● YOUR MOVE", "Ready to deploy"}},
		{"ask-chip-waiting", 80, []string{"question waiting", "● YOUR MOVE"}, []string{"● IDLE", "review answers"}},
		{"ask-overlay-single", 80, []string{"Ready to deploy the migration?", "Yes, deploy"}, []string{"review answers", "skipped (no answer)"}},
		{"ask-overlay-multi-review", 80, []string{"review answers", "2. [Naming] → skipped (no answer)"}, []string{"Ready to deploy", "Yes, deploy"}},
		{"spawn-evener", 80, []string{"Implement the next TUI task", "openai/gpt-5.5"}, []string{"openai/gpt-4.1", "OpenAI login required"}},
		{"spawn-auth-required", 80, []string{"OpenAI login required", "openai/gpt-4.1"}, []string{"openai/gpt-5.5", "Implement the next TUI task"}},
		{"model-picker", 80, []string{"Select model", "openai/gpt-5.5"}, []string{"Select theme", "worker - restore"}},
		{"theme-picker", 80, []string{"Select theme", "system"}, []string{"openai/gpt-5.5", "worker - restore"}},
		{"auth-overlay", 80, []string{"Signed in with Evener-owned OAuth state.", "source: evener"}, []string{"provider listing failed", "Start failed"}},
		{"agents-picker", 80, []string{"worker - restore auth commands", "explorer - inspect old TUI"}, []string{"Select model", "Select theme"}},
		{"help-overlay", 80, []string{"Available commands:", "/help"}, []string{"worker - restore", "provider listing failed"}},
		{"diagnostics", 80, []string{"Start failed: model provider is not reported", "Clear is not available for this source"}, []string{"provider listing failed", "Signed in with"}},
		{"appshell-normal", 80, []string{"Live now", "local daemon session"}, []string{"Loading hub dashboard", "Could not reach"}},
		{"appshell-loading", 80, []string{"Loading hub dashboard...", "ctrl+o dashboard"}, []string{"Live now", "Could not reach"}},
		{"appshell-error", 80, []string{"Could not reach the configured Hub.", "r retry"}, []string{"Live now", "Loading hub dashboard"}},
		{"topbar-session", 80, []string{"evener / session / Restore hub TUI widgets"}, []string{"enter open", "EVENER LIVE"}},
		{"actionbar-normal", 80, []string{"enter open  p project  n new  ctrl+o dashboard  q quit"}, []string{"n new\nctrl+o"}},
		{"actionbar-wrapped", 40, []string{"enter open  p project  n new\nctrl+o dashboard  q quit"}, []string{"n new  ctrl+o"}},
		{"picker-empty", 80, []string{"No matching items", "Filter: missing"}, []string{"source does not advertise clear", "provider listing failed"}},
		{"picker-disabled", 80, []string{"/clear", "source does not advertise clear"}, []string{"No matching items", "provider listing failed"}},
		{"picker-error", 80, []string{"provider listing failed", "Retry /model after signing in."}, []string{"source does not advertise clear", "No matching items"}},
	}

	views := make(map[string]string, len(tests))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rendered, ok := sampleRenderFromRealWidget(tt.name, tt.width)
			if !ok {
				t.Fatalf("sample %q was not recognized", tt.name)
			}
			if rendered.Name != tt.name || rendered.Width != tt.width || rendered.Theme != "dark" {
				t.Fatalf("sample metadata = {Name:%q Width:%d Theme:%q}, want {%q %d dark}", rendered.Name, rendered.Width, rendered.Theme, tt.name, tt.width)
			}
			plain := ansiPattern.ReplaceAllString(rendered.View, "")
			for _, required := range tt.required {
				if !strings.Contains(plain, required) {
					t.Errorf("sample %q missing required signature %q:\n%s", tt.name, required, plain)
				}
			}
			for _, forbidden := range tt.forbidden {
				if strings.Contains(plain, forbidden) {
					t.Errorf("sample %q contains forbidden signature %q:\n%s", tt.name, forbidden, plain)
				}
			}
			views[tt.name] = plain
		})
	}

	for i, left := range tests {
		for _, right := range tests[i+1:] {
			if views[left.name] == views[right.name] {
				t.Errorf("samples %q and %q rendered identically", left.name, right.name)
			}
		}
	}
}

func TestCovSampleRenderFromRealWidget_UnknownReturnsFalse(t *testing.T) {
	rendered, ok := sampleRenderFromRealWidget("nonexistent-sample", 80)
	if ok {
		t.Fatalf("unknown sample returned ok=true: %+v", rendered)
	}
	if rendered.Name != "" || rendered.Width != 0 || rendered.View != "" || len(rendered.Contains) != 0 || rendered.Theme != "" {
		t.Fatalf("unknown sample = %+v, want zero render", rendered)
	}
}
