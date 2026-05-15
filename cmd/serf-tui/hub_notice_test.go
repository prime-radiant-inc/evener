package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/internal/appwire"
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
		if !strings.Contains(view, "AppWire error") || !strings.Contains(view, "category: appwire") || !strings.Contains(view, "ctrl+x: dismiss notice") {
			t.Fatalf("notice did not persist in view:\n%s", view)
		}
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if cmd != nil {
		t.Fatal("dismissing a notice should be synchronous")
	}
	if got := updated.(hubModel).sessionView(); strings.Contains(got, "AppWire error") {
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
	if !strings.Contains(plain, "  AppWire error") || !strings.Contains(plain, "  ctrl+x: dismiss notice") {
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
	got := updated.(hubModel).sessionView()
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
	for _, want := range []string{"Auth error", "category: auth", "source: serf", "reason: auth endpoint unavailable", "next: Retry /auth openai or check Hub auth configuration."} {
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

func TestHubModelAppWireAndProviderErrorsRenderStructuredNotices(t *testing.T) {
	tests := []struct {
		name string
		msg  tea.Msg
		want []string
	}{
		{
			name: "send",
			msg:  hubSendMsg{text: "draft", err: appwire.SessionUnavailable("local daemon unavailable")},
			want: []string{"Send failed", "category: appwire", "reason: local daemon unavailable", "next: Check the hub connection and retry the action."},
		},
		{
			name: "action",
			msg:  hubActionMsg{action: "compact", err: appwire.Unavailable("codex source does not support compact")},
			want: []string{"Action failed", "category: action", "reason: codex source does not support compact"},
		},
		{
			name: "provider",
			msg:  hubSessionModelsMsg{err: appwire.WireError{Code: appwire.CodeUnavailable, Message: "OpenAI login required", Data: appwire.ErrorData{SerfErrorInfo: appwire.ErrorProviderUnavailable}}},
			want: []string{"Provider unavailable", "category: provider", "reason: OpenAI login required", "next: Check provider auth and model availability."},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newSessionHubModel(nil)
			updated, _ := m.Update(tc.msg)
			got := updated.(hubModel).sessionView()
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("notice missing %q:\n%s", want, got)
				}
			}
		})
	}
}
