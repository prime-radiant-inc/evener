package tui

import (
	"strings"
	"testing"
)

// Every named sample must prove its own semantic fixture reached the real
// widget. Recognition plus a non-empty view lets every branch return the same
// arbitrary rendering; the per-sample marker and pairwise comparison do not.
func TestCovSampleRenderFromRealWidget_SemanticFixtures(t *testing.T) {
	withTestColorProfile(t)
	tests := []struct {
		name   string
		width  int
		marker string
	}{
		{"dashboard-narrow", 60, "EVENER LIVE"},
		{"dashboard-normal", 80, "EVENER LIVE"},
		{"dashboard-wide", 200, "EVENER LIVE"},
		{"session-idle", 80, "draft stays visible"},
		{"session-streaming", 80, "What agent harness is running"},
		{"session-busy-steer", 80, "Please also check old TUI command parity"},
		{"session-busy-readonly", 80, "draft kept"},
		{"session-browse", 80, "What agent harness is running"},
		{"session-fork", 80, "edited prompt"},
		{"ask-card-pending", 80, "Which datastore for the ingest path?"},
		{"ask-chip-waiting", 80, "question waiting"},
		{"ask-overlay-single", 80, "Ready to deploy the migration?"},
		{"ask-overlay-multi-review", 80, "review answers"},
		{"spawn-evener", 80, "Implement the next TUI task"},
		{"spawn-codex", 80, "codex-local"},
		{"spawn-auth-required", 80, "OpenAI login required"},
		{"model-picker", 80, "openai/gpt-5.5"},
		{"theme-picker", 80, "Select theme"},
		{"auth-overlay", 80, "Signed in with Evener-owned OAuth state."},
		{"agents-picker", 80, "worker - restore auth commands"},
		{"help-overlay", 80, "/help"},
		{"diagnostics", 80, "Start failed: model provider is not reported"},
		{"appshell-normal", 80, "Live now"},
		{"appshell-loading", 80, "Loading hub dashboard..."},
		{"appshell-error", 80, "Could not reach the configured Hub."},
		{"topbar-session", 80, "Restore hub TUI widgets"},
		{"actionbar-normal", 80, "enter open"},
		{"actionbar-wrapped", 40, "ctrl+o dashboard"},
		{"picker-empty", 80, "No matching"},
		{"picker-disabled", 80, "source does not advertise clear"},
		{"picker-error", 80, "provider listing failed"},
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
			if !strings.Contains(plain, tt.marker) {
				t.Fatalf("sample %q missing fixture marker %q:\n%s", tt.name, tt.marker, plain)
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
