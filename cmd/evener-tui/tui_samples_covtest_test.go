package tui

import (
	"testing"
)

// ---- sampleRenderFromRealWidget: various sample names -----------------------

func TestCovSampleRenderFromRealWidget_DashboardNarrow(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("dashboard-narrow", 60)
	if !ok {
		t.Fatalf("dashboard-narrow should be a recognized sample")
	}
	if r.Name != "dashboard-narrow" {
		t.Fatalf("name = %q, want dashboard-narrow", r.Name)
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_DashboardNormal(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("dashboard-normal", 80)
	if !ok {
		t.Fatalf("dashboard-normal should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_DashboardWide(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("dashboard-wide", 200)
	if !ok {
		t.Fatalf("dashboard-wide should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_SessionIdle(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("session-idle", 80)
	if !ok {
		t.Fatalf("session-idle should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_SessionStreaming(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("session-streaming", 80)
	if !ok {
		t.Fatalf("session-streaming should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_SessionBusySteer(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("session-busy-steer", 80)
	if !ok {
		t.Fatalf("session-busy-steer should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_SessionBusyReadonly(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("session-busy-readonly", 80)
	if !ok {
		t.Fatalf("session-busy-readonly should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_SessionBrowse(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("session-browse", 80)
	if !ok {
		t.Fatalf("session-browse should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_SessionFork(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("session-fork", 80)
	if !ok {
		t.Fatalf("session-fork should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_AskCardPending(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("ask-card-pending", 80)
	if !ok {
		t.Fatalf("ask-card-pending should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_AskChipWaiting(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("ask-chip-waiting", 80)
	if !ok {
		t.Fatalf("ask-chip-waiting should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_AskOverlaySingle(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("ask-overlay-single", 80)
	if !ok {
		t.Fatalf("ask-overlay-single should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_AskOverlayMultiReview(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("ask-overlay-multi-review", 80)
	if !ok {
		t.Fatalf("ask-overlay-multi-review should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_SpawnEvener(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("spawn-evener", 80)
	if !ok {
		t.Fatalf("spawn-evener should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_SpawnCodex(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("spawn-codex", 80)
	if !ok {
		t.Fatalf("spawn-codex should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_SpawnAuthRequired(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("spawn-auth-required", 80)
	if !ok {
		t.Fatalf("spawn-auth-required should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_ModelPicker(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("model-picker", 80)
	if !ok {
		t.Fatalf("model-picker should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_ThemePicker(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("theme-picker", 80)
	if !ok {
		t.Fatalf("theme-picker should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_AuthOverlay(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("auth-overlay", 80)
	if !ok {
		t.Fatalf("auth-overlay should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_AgentsPicker(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("agents-picker", 80)
	if !ok {
		t.Fatalf("agents-picker should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_HelpOverlay(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("help-overlay", 80)
	if !ok {
		t.Fatalf("help-overlay should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_Diagnostics(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("diagnostics", 80)
	if !ok {
		t.Fatalf("diagnostics should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_AppShellNormal(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("appshell-normal", 80)
	if !ok {
		t.Fatalf("appshell-normal should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_AppShellLoading(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("appshell-loading", 80)
	if !ok {
		t.Fatalf("appshell-loading should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_AppShellError(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("appshell-error", 80)
	if !ok {
		t.Fatalf("appshell-error should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_TopbarSession(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("topbar-session", 80)
	if !ok {
		t.Fatalf("topbar-session should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_ActionbarNormal(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("actionbar-normal", 80)
	if !ok {
		t.Fatalf("actionbar-normal should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_ActionbarWrapped(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("actionbar-wrapped", 40)
	if !ok {
		t.Fatalf("actionbar-wrapped should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_PickerEmpty(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("picker-empty", 80)
	if !ok {
		t.Fatalf("picker-empty should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_PickerDisabled(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("picker-disabled", 80)
	if !ok {
		t.Fatalf("picker-disabled should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_PickerError(t *testing.T) {
	r, ok := sampleRenderFromRealWidget("picker-error", 80)
	if !ok {
		t.Fatalf("picker-error should be a recognized sample")
	}
	if r.View == "" {
		t.Fatalf("view should be non-empty")
	}
}

func TestCovSampleRenderFromRealWidget_UnknownReturnsFalse(t *testing.T) {
	_, ok := sampleRenderFromRealWidget("nonexistent-sample", 80)
	if ok {
		t.Fatalf("unknown sample should return ok=false")
	}
}
