package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/appwire"
)

func TestNoticePanelTextIncludesCategoryAndNextAction(t *testing.T) {
	got := noticePanel{
		Title:      "Network error",
		Category:   "network",
		Summary:    "Hub request failed.",
		Source:     "hub",
		Reason:     "connection refused",
		NextAction: "Check the hub process and retry.",
	}.Text()

	for _, want := range []string{"Network error", "category: network", "Hub request failed.", "source: hub", "reason: connection refused", "next: Check the hub process and retry."} {
		if !strings.Contains(got, want) {
			t.Fatalf("notice text missing %q:\n%s", want, got)
		}
	}
}

func TestHubModelNoticesPersistUntilDismissed(t *testing.T) {
	m := newSessionHubModel(nil)
	m.addNotice(noticePanel{
		Title:      "AppWire error",
		Category:   "appwire",
		Summary:    "Hub request failed.",
		Source:     "serf",
		Reason:     "method not found",
		NextAction: "Restart the matching serf-hub binary.",
	})

	first := m.sessionView()
	second := m.sessionView()
	for _, view := range []string{first, second} {
		// View() format: summary on first line, source·cause on second, next on third.
		if !strings.Contains(view, "Hub request failed.") || !strings.Contains(view, "method not found") || !strings.Contains(view, "ctrl+x: dismiss notice") {
			t.Fatalf("notice did not persist in view:\n%s", view)
		}
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if cmd != nil {
		t.Fatal("dismissing a notice should be synchronous")
	}
	updatedModel := updated.(hubModel)
	if got := updatedModel.sessionView(); strings.Contains(got, "AppWire error") {
		t.Fatalf("notice remained after dismissal:\n%s", got)
	}
}

func TestHubModelNoticesRenderAsPane(t *testing.T) {
	withTestColorProfile(t)
	m := newSessionHubModel(nil)
	m.width = 100
	m.addNotice(noticePanel{
		Title:    "AppWire error",
		Category: "appwire",
		Summary:  "Hub request failed.",
	})

	got := m.renderNotices()
	if !strings.Contains(got, "\x1b[") {
		t.Fatalf("notice pane should render terminal styling:\n%s", got)
	}
	plain := ansiPattern.ReplaceAllString(got, "")
	// View() format: summary on first line (with ▍ ● prefix), ctrl+x hint at the end.
	if !strings.Contains(plain, "Hub request failed.") || !strings.Contains(plain, "ctrl+x: dismiss notice") {
		t.Fatalf("notice pane should have pane padding:\n%s", plain)
	}
}

func TestHubModelClearsActionUnavailableNoticeWhenSessionChanges(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SourceLabel = "codex-local"
	m.addActionUnavailableNotice("send", "Send is not available for this session.", "source does not support send")

	updated, _ := m.Update(hubSessionMsg{
		detail: hubSessionDetail{
			Ref:         "local:01SERF",
			SessionID:   "01SERF",
			SourceLabel: "serf",
			Title:       "Serf replay",
			State:       "ended",
		},
	})
	updatedModel := updated.(hubModel)
	got := updatedModel.sessionView()
	if strings.Contains(got, "Action unavailable") || strings.Contains(got, "source: codex-local") {
		t.Fatalf("session-scoped notice leaked after session change:\n%s", got)
	}
}

func TestHubModelAuthErrorsRenderStructuredNoticeAndClearOnSuccess(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SourceLabel = "serf"

	updated, _ := m.Update(hubAuthStatusMsg{err: appwire.Unavailable("auth endpoint unavailable")})
	m = updated.(hubModel)
	got := m.sessionView()
	// View() format: summary line contains the notice summary/title, source·cause on second line.
	for _, want := range []string{"auth endpoint unavailable", "serf", "Retry /auth openai or check Hub auth configuration."} {
		if !strings.Contains(got, want) {
			t.Fatalf("auth error notice missing %q:\n%s", want, got)
		}
	}

	updated, _ = m.Update(hubAuthLoginCompleteMsg{resp: appwire.AuthLoginCompleteResponse{Status: appwire.AuthStatusResponse{
		Provider:     "openai",
		Supported:    true,
		SignedIn:     true,
		ActiveSource: "oauth",
		Email:        "j@example.com",
	}}})
	m = updated.(hubModel)
	got = m.sessionView()
	if strings.Contains(got, "Auth error") {
		t.Fatalf("successful login did not clear auth notice:\n%s", got)
	}
	if !strings.Contains(got, "OpenAI login complete. OpenAI auth: oauth (j@example.com)") {
		t.Fatalf("login success message missing:\n%s", got)
	}
}

// TestNoticePanel_CauseProviderClassifiesProvider (kata 5q3p) verifies
// that classifyWarningCategory prefers the typed Cause.Kind when present
// and returns "provider" without inspecting the message.
func TestNoticePanel_CauseProviderClassifiesProvider(t *testing.T) {
	cause := &appwire.DiagnosticCause{Kind: "provider", Provider: "openai", Status: 429}
	got := classifyWarningCategory("some unrelated text", cause)
	if got != "provider" {
		t.Fatalf("classifyWarningCategory with provider cause: got %q, want %q", got, "provider")
	}
}

// TestNoticePanel_NoCauseFallsBackToMessageMatch (kata 5q3p) verifies the
// substring fallback path: with no Cause, a "provider error: ..." message
// is still classified as provider so legacy NotifyWarning payloads keep
// working.
func TestNoticePanel_NoCauseFallsBackToMessageMatch(t *testing.T) {
	got := classifyWarningCategory("provider error: openai rate limited", nil)
	if got != "provider" {
		t.Fatalf("classifyWarningCategory message fallback: got %q, want %q", got, "provider")
	}
}

// TestNoticePanel_NoCauseNonProviderMessage (kata 5q3p) regression-locks
// the non-provider branch of the substring fallback path: a "serf error:"
// message with no Cause must classify as serf, never as provider.
func TestNoticePanel_NoCauseNonProviderMessage(t *testing.T) {
	got := classifyWarningCategory("serf error: configuration", nil)
	if got != "serf" {
		t.Fatalf("classifyWarningCategory serf-message fallback: got %q, want %q", got, "serf")
	}
}

func TestHubModelAppWireAndProviderErrorsRenderStructuredNotices(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.Msg
		want []string
	}{
		{
			name: "send",
			msg:  hubSendMsg{text: "draft", err: appwire.SessionUnavailable("local daemon unavailable")},
			// View() format: source·cause line and next line are visible.
			want: []string{"local daemon unavailable", "Check the hub connection and retry the action."},
		},
		{
			name: "action",
			msg:  hubActionMsg{action: "compact", err: appwire.Unavailable("codex source does not support compact")},
			want: []string{"codex source does not support compact"},
		},
		{
			name: "provider",
			msg:  hubSessionModelsMsg{err: appwire.WireError{Code: appwire.CodeUnavailable, Message: "OpenAI login required", Data: appwire.ErrorData{SerfErrorInfo: appwire.ErrorProviderUnavailable}}},
			want: []string{"OpenAI login required", "Check provider auth and model availability."},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newSessionHubModel(nil)
			updated, _ := m.Update(tc.msg)
			updatedModel := updated.(hubModel)
			got := updatedModel.sessionView()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("notice missing %q:\n%s", want, got)
				}
			}
		})
	}
}
