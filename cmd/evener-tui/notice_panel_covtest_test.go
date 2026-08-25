package tui

import (
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// ---- View: state-colored bar with explicit state -----------------------------

func TestCovNoticePanelView_ExplicitState(t *testing.T) {
	withTestColorProfile(t)
	n := noticePanel{
		Title:      "Test",
		Summary:    "Something happened",
		Source:     "hub",
		Reason:     "timeout",
		NextAction: "retry",
		State:      "active",
	}
	got := ansiPattern.ReplaceAllString(n.View(), "")
	want := "▍ ● Something happened\n  source hub · cause timeout\n  next  retry"
	if got != want {
		t.Fatalf("notice view = %q, want %q", got, want)
	}
}

func TestCovNoticePanelView_EmptyStateDefaultsToIdle(t *testing.T) {
	withTestColorProfile(t)
	n := noticePanel{
		Summary:    "idle test",
		Source:     "hub",
		NextAction: "do nothing",
	}
	got := n.View()
	n.State = "idle"
	if want := n.View(); got != want {
		t.Fatalf("empty state render differs from explicit idle:\nempty: %q\n idle: %q", got, want)
	}
	n.State = "active"
	if active := n.View(); got == active {
		t.Fatalf("empty state render is indistinguishable from active state")
	}
}

func TestCovNoticePanelView_EmptySummaryFallsBackToTitle(t *testing.T) {
	withTestColorProfile(t)
	n := noticePanel{
		Title:      "My Title",
		Source:     "hub",
		NextAction: "act",
	}
	got := ansiPattern.ReplaceAllString(n.View(), "")
	if want := "▍ ● My Title\n  source hub · cause \n  next  act"; got != want {
		t.Fatalf("title fallback view = %q, want %q", got, want)
	}
}

// ---- Text: full field coverage ----------------------------------------------

func TestCovNoticePanelText_AllFields(t *testing.T) {
	got := noticePanel{
		Title:      "Full Notice",
		Category:   "net",
		Summary:    "Something broke",
		Source:     "hub",
		Reason:     "conn refused",
		NextAction: "retry now",
	}.Text()
	plain := ansiPattern.ReplaceAllString(got, "")
	want := "Full Notice\nSomething broke\ncategory: net\nsource: hub\nreason: conn refused\nnext: retry now"
	if plain != want {
		t.Fatalf("notice text = %q, want %q", plain, want)
	}
}

func TestCovNoticePanelText_OnlyTitle(t *testing.T) {
	got := noticePanel{Title: "Just Title"}.Text()
	if plain := ansiPattern.ReplaceAllString(got, ""); plain != "Just Title" {
		t.Fatalf("title-only text = %q, want %q", plain, "Just Title")
	}
}

func TestCovNoticePanelText_EmptyAll(t *testing.T) {
	got := noticePanel{}.Text()
	if strings.TrimSpace(got) != "" {
		t.Fatalf("empty notice text should be empty: %q", got)
	}
}

// ---- addNotice: dedup by key ------------------------------------------------

func TestCovAddNotice_DedupReplacesExisting(t *testing.T) {
	m := newSessionHubModel(nil)
	m.addNotice(noticePanel{Title: "T", Category: "cat", Source: "src", Summary: "first"})
	m.addNotice(noticePanel{Title: "T", Category: "cat", Source: "src", Summary: "second"})
	if len(m.notices) != 1 {
		t.Fatalf("notices count = %d, want 1 (deduped)", len(m.notices))
	}
	if m.notices[0].Summary != "second" {
		t.Fatalf("deduped notice summary = %q, want second (replaced)", m.notices[0].Summary)
	}
}

func TestCovAddNotice_DifferentKeyAppends(t *testing.T) {
	m := newSessionHubModel(nil)
	m.addNotice(noticePanel{Title: "T", Category: "cat1", Source: "src"})
	m.addNotice(noticePanel{Title: "T", Category: "cat2", Source: "src"})
	if len(m.notices) != 2 {
		t.Fatalf("notices count = %d, want 2", len(m.notices))
	}
}

func TestCovAddNotice_EmptyTitleAndSummarySkipped(t *testing.T) {
	m := newSessionHubModel(nil)
	m.addNotice(noticePanel{Category: "cat"})
	if len(m.notices) != 0 {
		t.Fatalf("empty title+summary should be skipped: %d notices", len(m.notices))
	}
}

// ---- dismissNotice ----------------------------------------------------------

func TestCovDismissNotice_EmptyListNoOp(t *testing.T) {
	m := newSessionHubModel(nil)
	m.dismissNotice()
	if len(m.notices) != 0 {
		t.Fatalf("dismiss on empty should be no-op")
	}
}

func TestCovDismissNotice_RemovesFirst(t *testing.T) {
	m := newSessionHubModel(nil)
	m.addNotice(noticePanel{Title: "T1", Category: "cat1", Summary: "S1"})
	m.addNotice(noticePanel{Title: "T2", Category: "cat2", Summary: "S2"})
	m.dismissNotice()
	if len(m.notices) != 1 || m.notices[0].Summary != "S2" {
		t.Fatalf("dismiss should remove first: %+v", m.notices)
	}
}

// ---- clearNoticesByCategory -------------------------------------------------

func TestCovClearNoticesByCategory_RemovesMatching(t *testing.T) {
	m := newSessionHubModel(nil)
	m.addNotice(noticePanel{Title: "T1", Category: "net", Summary: "S1"})
	m.addNotice(noticePanel{Title: "T2", Category: "auth", Summary: "S2"})
	m.addNotice(noticePanel{Title: "T3", Category: "net", Summary: "S3"})
	m.clearNoticesByCategory("net")
	if len(m.notices) != 1 || m.notices[0].Category != "auth" {
		t.Fatalf("clear by category should remove matching: %+v", m.notices)
	}
}

func TestCovClearNoticesByCategory_EmptyCategoryNoOp(t *testing.T) {
	m := newSessionHubModel(nil)
	m.addNotice(noticePanel{Title: "T1", Category: "net"})
	m.clearNoticesByCategory("")
	if len(m.notices) != 1 {
		t.Fatalf("empty category should be no-op")
	}
}

func TestCovClearNoticesByCategory_NoneMatching(t *testing.T) {
	m := newSessionHubModel(nil)
	m.addNotice(noticePanel{Title: "T1", Category: "net"})
	m.clearNoticesByCategory("auth")
	if len(m.notices) != 1 {
		t.Fatalf("no matching should leave all notices")
	}
}

// ---- classifyWarningCategory ------------------------------------------------

func TestCovClassifyWarningCategory_ProviderCause(t *testing.T) {
	cause := &appwire.DiagnosticCause{Kind: "provider"}
	if got := classifyWarningCategory("some message", cause); got != "provider" {
		t.Fatalf("provider cause = %q, want provider", got)
	}
}

func TestCovClassifyWarningCategory_NonProviderCause(t *testing.T) {
	cause := &appwire.DiagnosticCause{Kind: "evener"}
	if got := classifyWarningCategory("some message", cause); got != "evener" {
		t.Fatalf("non-provider cause = %q, want evener", got)
	}
}

func TestCovClassifyWarningCategory_ProviderErrorMessage(t *testing.T) {
	if got := classifyWarningCategory("Provider error: timeout", nil); got != "provider" {
		t.Fatalf("provider error message = %q, want provider", got)
	}
}

func TestCovClassifyWarningCategory_EvenerErrorMessage(t *testing.T) {
	if got := classifyWarningCategory("Evener error: something", nil); got != "evener" {
		t.Fatalf("evener error message = %q, want evener", got)
	}
}

func TestCovClassifyWarningCategory_DefaultEvener(t *testing.T) {
	if got := classifyWarningCategory("random message", nil); got != "evener" {
		t.Fatalf("default = %q, want evener", got)
	}
}

// ---- noticeCategoryForError -------------------------------------------------

func TestCovNoticeCategoryForError_ProviderUnavailable(t *testing.T) {
	err := appwire.WireError{Code: appwire.CodeUnavailable, Message: "down", Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorProviderUnavailable}}
	if got := noticeCategoryForError(err, "fallback"); got != "provider" {
		t.Fatalf("provider unavailable = %q, want provider", got)
	}
}

func TestCovNoticeCategoryForError_ActionUnavailable(t *testing.T) {
	err := appwire.WireError{Code: appwire.CodeUnavailable, Message: "no action", Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorActionUnavailable}}
	if got := noticeCategoryForError(err, ""); got != "action" {
		t.Fatalf("action unavailable = %q, want action", got)
	}
}

func TestCovNoticeCategoryForError_SessionUnavailable(t *testing.T) {
	err := appwire.WireError{Code: appwire.CodeUnavailable, Message: "no session", Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorSessionUnavailable}}
	if got := noticeCategoryForError(err, ""); got != "appwire" {
		t.Fatalf("session unavailable = %q, want appwire", got)
	}
}

func TestCovNoticeCategoryForError_MethodNotFound(t *testing.T) {
	err := appwire.WireError{Code: appwire.CodeMethodNotFound, Message: "not found", Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorMethodNotFound}}
	if got := noticeCategoryForError(err, ""); got != "appwire" {
		t.Fatalf("method not found = %q, want appwire", got)
	}
}

func TestCovNoticeCategoryForError_InvalidParams(t *testing.T) {
	err := appwire.WireError{Code: appwire.CodeInvalidParams, Message: "bad params", Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorInvalidParams}}
	if got := noticeCategoryForError(err, ""); got != "appwire" {
		t.Fatalf("invalid params = %q, want appwire", got)
	}
}

func TestCovNoticeCategoryForError_HubLaunch(t *testing.T) {
	err := appwire.WireError{Code: appwire.CodeUnavailable, Message: "launch fail", Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorHubLaunch}}
	if got := noticeCategoryForError(err, ""); got != "launch" {
		t.Fatalf("hub launch = %q, want launch", got)
	}
}

func TestCovNoticeCategoryForError_FallbackUsed(t *testing.T) {
	err := errors.New("plain error")
	if got := noticeCategoryForError(err, "custom"); got != "custom" {
		t.Fatalf("fallback = %q, want custom", got)
	}
}

func TestCovNoticeCategoryForError_NoFallbackDefaultsAppwire(t *testing.T) {
	err := errors.New("plain error")
	if got := noticeCategoryForError(err, ""); got != "appwire" {
		t.Fatalf("no fallback = %q, want appwire", got)
	}
}

func TestCovNoticeCategoryForError_WireErrorNonErrorData(t *testing.T) {
	err := appwire.WireError{Code: 500, Message: "weird", Data: "not error data"}
	if got := noticeCategoryForError(err, "fb"); got != "fb" {
		t.Fatalf("non-ErrorData wire error = %q, want fb", got)
	}
}

// ---- noticeSummaryForError --------------------------------------------------

func TestCovNoticeSummaryForError_ProviderUnavailableMessage(t *testing.T) {
	err := appwire.WireError{Code: appwire.CodeUnavailable, Message: "down", Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorProviderUnavailable}}
	if got := noticeSummaryForError(err, "fallback"); got != "Check provider auth and runtime readiness." {
		t.Fatalf("provider unavailable summary = %q", got)
	}
}

func TestCovNoticeSummaryForError_NoFallbackDefault(t *testing.T) {
	err := errors.New("plain")
	if got := noticeSummaryForError(err, ""); got != "Hub request failed." {
		t.Fatalf("no fallback = %q, want 'Hub request failed.'", got)
	}
}

func TestCovNoticeSummaryForError_FallbackWithPeriod(t *testing.T) {
	err := errors.New("plain")
	if got := noticeSummaryForError(err, "my fallback"); got != "my fallback." {
		t.Fatalf("fallback with period = %q, want 'my fallback.'", got)
	}
}

func TestCovNoticeSummaryForError_WireErrorNonProvider(t *testing.T) {
	err := appwire.WireError{Code: 500, Message: "err", Data: appwire.ErrorData{EvenerErrorInfo: appwire.ErrorActionUnavailable}}
	if got := noticeSummaryForError(err, "fb"); got != "fb." {
		t.Fatalf("non-provider wire error = %q, want 'fb.'", got)
	}
}

// ---- addActionUnavailableNotice ---------------------------------------------

func TestCovAddActionUnavailableNotice_WithReason(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SourceLabel = "codex"
	m.addActionUnavailableNotice("send", "send not available", "source does not support send")
	if len(m.notices) != 1 {
		t.Fatalf("notice not added: %d", len(m.notices))
	}
	if m.notices[0].Reason != "source does not support send" {
		t.Fatalf("reason = %q", m.notices[0].Reason)
	}
}

func TestCovAddActionUnavailableNotice_AutoReasonWhenEmpty(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SourceLabel = "codex"
	m.addActionUnavailableNotice("send", "send not available", "")
	if len(m.notices) != 1 {
		t.Fatalf("notice not added: %d", len(m.notices))
	}
	if !strings.Contains(m.notices[0].Reason, "send") {
		t.Fatalf("auto reason = %q, want contains 'send'", m.notices[0].Reason)
	}
}

func TestCovAddActionUnavailableNotice_EmptyActionNoAutoReason(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SourceLabel = "codex"
	m.addActionUnavailableNotice("", "summary", "")
	if len(m.notices) != 1 {
		t.Fatalf("notice not added: %d", len(m.notices))
	}
	if m.notices[0].Reason != "" {
		t.Fatalf("empty action should not auto-generate reason: %q", m.notices[0].Reason)
	}
}

// ---- renderNotices: empty returns empty string -------------------------------

func TestCovRenderNotices_EmptyReturnsEmpty(t *testing.T) {
	m := newSessionHubModel(nil)
	if got := m.renderNotices(); got != "" {
		t.Fatalf("empty notices should render empty string: %q", got)
	}
}

// ---- sourceLabelForNotice ---------------------------------------------------

func TestCovSourceLabelForNotice_UsesSourceLabel(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SourceLabel = "my-source"
	if got := m.sourceLabelForNotice(); got != "my-source" {
		t.Fatalf("sourceLabel = %q, want my-source", got)
	}
}

func TestCovSourceLabelForNotice_FallsBackToRef(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SourceLabel = ""
	m.detail.Ref = "local:01ABC"
	got := m.sourceLabelForNotice()
	if got == "" {
		t.Fatalf("sourceLabel should fall back to ref text: %q", got)
	}
}

// ---- addHubErrorNotice -------------------------------------------------------

func TestCovAddHubErrorNotice_AddsNotice(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.SourceLabel = "hub"
	m.addHubErrorNotice("Send failed", "provider", errors.New("connection refused"), "retry")
	if len(m.notices) != 1 {
		t.Fatalf("notice not added: %d", len(m.notices))
	}
	n := m.notices[0]
	if n.Title != "Send failed" || n.Reason != "connection refused" || n.NextAction != "retry" {
		t.Fatalf("notice fields wrong: %+v", n)
	}
}
