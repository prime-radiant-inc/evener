package main

import (
	"strings"
	"testing"
)

func TestHubTUISampleCorpusCoversRequiredVariants(t *testing.T) {
	corpus := newHubTUISampleCorpus()

	if len(corpus.DashboardTree.Projects) < 2 {
		t.Fatalf("dashboard projects=%d, want at least serf and external project samples", len(corpus.DashboardTree.Projects))
	}
	requireSampleSources(t, corpus.DashboardTree.Live, "serf", "codex-local")

	project := corpus.ProjectHistory
	if project.Name == "" || len(project.Sessions) < 2 {
		t.Fatalf("project history sample incomplete: %+v", project)
	}
	if !hasLiveAndRecent(project.Sessions) {
		t.Fatalf("project history must include live and ended sessions: %+v", project.Sessions)
	}

	for _, name := range []string{"serf-idle", "codex-readonly", "busy-steer", "busy-readonly", "ended"} {
		if _, ok := corpus.Sessions[name]; !ok {
			t.Fatalf("missing session detail sample %q", name)
		}
	}

	codex := corpus.Sessions["codex-readonly"]
	if codex.SourceLabel != "codex-local" || codex.Capabilities.Clear || codex.Capabilities.Shutdown {
		t.Fatalf("codex readonly sample should preserve source label and unsupported actions: %+v", codex)
	}

	if len(corpus.TranscriptEvents) == 0 || !containsMessageKind(corpus.TranscriptEvents, msgTool) {
		t.Fatalf("transcript event samples must include tool grouping input: %+v", corpus.TranscriptEvents)
	}
	if len(corpus.Diagnostics) < 2 {
		t.Fatalf("diagnostic samples=%d, want launch and action-unavailable samples", len(corpus.Diagnostics))
	}
	if len(corpus.AuthStates) < 5 {
		t.Fatalf("auth states=%d, want env/signed-out/signed-in/expired/failed samples", len(corpus.AuthStates))
	}
}

func TestHubTUISampleCorpusHasGoldenRendersForCoreSurfaces(t *testing.T) {
	corpus := newHubTUISampleCorpus()
	required := []string{
		"dashboard-narrow",
		"dashboard-normal",
		"dashboard-wide",
		"project-narrow",
		"project-normal",
		"project-wide",
		"session-idle",
		"session-streaming",
		"session-busy-steer",
		"session-busy-readonly",
		"session-browse",
		"session-fork",
		"spawn-serf",
		"spawn-codex",
		"spawn-auth-required",
		"model-picker",
		"theme-picker",
		"auth-overlay",
		"agents-picker",
		"help-overlay",
		"diagnostics",
	}

	renders := map[string]tuiSampleRender{}
	for _, render := range corpus.Renders {
		renders[render.Name] = render
		if strings.TrimSpace(render.View) == "" {
			t.Fatalf("render %q has empty view", render.Name)
		}
		if render.Width <= 0 {
			t.Fatalf("render %q width=%d, want positive", render.Name, render.Width)
		}
		for _, want := range render.Contains {
			if !strings.Contains(render.View, want) {
				t.Fatalf("render %q missing %q:\n%s", render.Name, want, render.View)
			}
		}
	}
	for _, name := range required {
		if _, ok := renders[name]; !ok {
			t.Fatalf("missing golden render sample %q", name)
		}
	}
}

func TestHubTUISampleCorpusHasFocusAndDraftInteractionSamples(t *testing.T) {
	corpus := newHubTUISampleCorpus()
	required := []string{
		"prompt-owns-printable-shortcuts",
		"picker-owns-filter-navigation",
		"composer-draft-survives-overlay",
		"busy-send-switches-to-steer",
		"unsupported-codex-actions-hidden-or-disabled",
	}
	interactions := map[string]tuiInteractionSample{}
	for _, sample := range corpus.Interactions {
		interactions[sample.Name] = sample
		if strings.TrimSpace(sample.Expected) == "" {
			t.Fatalf("interaction sample %q missing expected outcome", sample.Name)
		}
	}
	for _, name := range required {
		if _, ok := interactions[name]; !ok {
			t.Fatalf("missing interaction sample %q", name)
		}
	}
}

func requireSampleSources(t *testing.T, nodes []hubTreeNode, labels ...string) {
	t.Helper()
	seen := map[string]bool{}
	for _, node := range nodes {
		seen[node.SourceLabel] = true
	}
	for _, label := range labels {
		if !seen[label] {
			t.Fatalf("missing source label %q in sample nodes: %+v", label, nodes)
		}
	}
}

func hasLiveAndRecent(nodes []hubTreeNode) bool {
	var live, recent bool
	for _, node := range nodes {
		if node.Live {
			live = true
		} else {
			recent = true
		}
	}
	return live && recent
}

func containsMessageKind(messages []chatMessage, kind messageKind) bool {
	for _, msg := range messages {
		if msg.Kind == kind {
			return true
		}
	}
	return false
}
