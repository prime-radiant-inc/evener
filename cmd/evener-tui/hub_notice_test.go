package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/transcript"
)

func TestNoticePanelTextIncludesCategoryAndNextAction(t *testing.T) {
	fields := []string{"title-fixture", "category-fixture", "summary-fixture", "source-fixture", "reason-fixture", "next-fixture"}
	got := noticePanel{
		Title:      fields[0],
		Category:   fields[1],
		Summary:    fields[2],
		Source:     fields[3],
		Reason:     fields[4],
		NextAction: fields[5],
	}.Text()

	for _, want := range fields {
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
		Source:     "evener",
		Reason:     "method not found",
		NextAction: "Restart the matching evener-hub binary.",
	})

	first := m.sessionView()
	second := m.sessionView()
	for _, view := range []string{first, second} {
		// View() format: summary on first line, source·cause on second, next on third.
		if !strings.Contains(view, "Hub request failed.") || !strings.Contains(view, "method not found") {
			t.Fatalf("notice did not persist in view:\n%s", view)
		}
	}
	if len(m.notices) != 1 {
		t.Fatalf("rendering changed the notice list: %+v", m.notices)
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	if cmd != nil {
		t.Fatal("dismissing a notice should be synchronous")
	}
	updatedModel := updated.(hubModel)
	if _, ok := noticeByCategory(updatedModel, "appwire"); ok {
		t.Fatalf("notice remained after dismissal: %+v", updatedModel.notices)
	}
}

func TestHubModelDashboardRendersNotices(t *testing.T) {
	m := newHubModel(nil, "http://hub.test")
	m.width = 100
	m.addNotice(noticePanel{
		Title:      "Upgrade complete",
		Category:   "upgrade",
		Summary:    "Evener upgraded to snapshot.",
		Source:     "hub",
		Reason:     "evener_linux_amd64.tar.gz",
		NextAction: "Restart evener-tui and evener-hub to use the upgraded binaries.",
	})

	got := m.dashboardView()
	for _, want := range []string{"Evener upgraded to snapshot.", "evener_linux_amd64.tar.gz", "Restart evener-tui and evener-hub"} {
		if !strings.Contains(got, want) {
			t.Fatalf("dashboard notice missing %q:\n%s", want, got)
		}
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
	// View() format: summary on first line (with ▍ ● prefix).
	if !strings.Contains(plain, "Hub request failed.") {
		t.Fatalf("notice pane should carry the notice:\n%s", plain)
	}
}

func TestHubModelClearsActionUnavailableNoticeWhenSessionChanges(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SourceLabel = "remote"
	m.addActionUnavailableNotice("send", "Send is not available for this session.", "source does not support send")

	updated, _ := m.Update(hubSessionMsg{
		detail: hubSessionDetail{
			Ref:         "local:01EVENER",
			SessionID:   "01EVENER",
			SourceLabel: "evener",
			Title:       "Evener replay",
			State:       "ended",
		},
	})
	updatedModel := updated.(hubModel)
	if notice, ok := noticeByCategory(updatedModel, "action-unavailable"); ok {
		t.Fatalf("session-scoped notice leaked after session change: %+v", notice)
	}
}

func TestHubModelAuthErrorsRenderStructuredNoticeAndClearOnSuccess(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SourceLabel = "evener"

	updated, _ := m.Update(hubAuthStatusMsg{err: appwire.Unavailable("auth endpoint unavailable")})
	m = updated.(hubModel)
	notice, ok := noticeByCategory(m, "auth")
	if !ok {
		t.Fatalf("auth error raised no auth notice: %+v", m.notices)
	}
	if notice.Reason != "auth endpoint unavailable" || notice.Source != "evener" || notice.NextAction == "" {
		t.Fatalf("auth notice = %+v, want the failure as its reason, the session source, and a next action", notice)
	}
	got := m.sessionView()
	// View() format: summary line contains the notice summary/title, source·cause on second line.
	for _, want := range []string{"auth endpoint unavailable", "evener"} {
		if !strings.Contains(got, want) {
			t.Fatalf("auth error notice missing %q:\n%s", want, got)
		}
	}

	messagesBefore := len(m.session.messages)
	updated, _ = m.Update(hubAuthLoginCompleteMsg{resp: appwire.AuthLoginCompleteResponse{Status: appwire.AuthStatusResponse{
		Provider:     "openai",
		Supported:    true,
		SignedIn:     true,
		ActiveSource: "oauth",
		Email:        "j@example.com",
	}}})
	m = updated.(hubModel)
	if notice, ok := noticeByCategory(m, "auth"); ok {
		t.Fatalf("successful login did not clear auth notice: %+v", notice)
	}
	if !m.authStatus.SignedIn || m.authStatus.Provider != "openai" || m.authStatus.Email != "j@example.com" {
		t.Fatalf("login success did not record the signed-in status: %+v", m.authStatus)
	}
	if len(m.session.messages) != messagesBefore+1 || m.session.messages[len(m.session.messages)-1].Kind != transcript.MsgSystem {
		t.Fatalf("login success did not report itself in the session: %+v", m.session.messages)
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
// the non-provider branch of the substring fallback path: a "evener error:"
// message with no Cause must classify as evener, never as provider.
func TestNoticePanel_NoCauseNonProviderMessage(t *testing.T) {
	got := classifyWarningCategory("evener error: configuration", nil)
	if got != "evener" {
		t.Fatalf("classifyWarningCategory evener-message fallback: got %q, want %q", got, "evener")
	}
}

func TestHubModelAppWireAndProviderErrorsRenderStructuredNotices(t *testing.T) {
	tests := []struct {
		name     string
		msg      tea.Msg
		category string
		reason   string
	}{
		{
			name:     "send",
			msg:      hubSendMsg{text: "draft", err: appwire.SessionUnavailable("local daemon unavailable")},
			category: "appwire",
			reason:   "local daemon unavailable",
		},
		{
			name:     "action",
			msg:      hubActionMsg{action: "compact", err: appwire.Unavailable("remote source does not support compact")},
			category: "action",
			reason:   "remote source does not support compact",
		},
		{
			name:     "provider",
			msg:      hubSessionModelsMsg{err: appwire.WireError{Code: appwire.CodeUnavailable, Message: "OpenAI login required", Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorProviderUnavailable}}},
			category: "provider",
			reason:   "OpenAI login required",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := newSessionHubModel(nil)
			updated, _ := m.Update(tc.msg)
			updatedModel := updated.(hubModel)
			notice, ok := noticeByCategory(updatedModel, tc.category)
			if !ok {
				t.Fatalf("no %s notice in %+v", tc.category, updatedModel.notices)
			}
			if notice.Reason != tc.reason || notice.NextAction == "" {
				t.Fatalf("notice = %+v, want reason %q and a next action", notice, tc.reason)
			}
			// View() format: source·cause line is visible.
			if got := updatedModel.sessionView(); !strings.Contains(got, tc.reason) {
				t.Fatalf("notice reason missing from view:\n%s", got)
			}
		})
	}
}
