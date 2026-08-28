package tui

import (
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-tui/internal/clipboard"
)

// ---- sessionComposerReadOnlyReason ------------------------------------------

func TestCovSessionComposerReadOnlyReason_ActionStateNoQueueWithSend(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = "active"
	m.detail.Capabilities.Send = true
	m.detail.Capabilities.Queue = false
	m.session.processing = true
	if got := m.sessionComposerReadOnlyReason(); got != "" {
		t.Fatalf("active+send+no-queue should not be read-only: %q", got)
	}
}

func TestCovSessionComposerReadOnlyReason_ActionStateNoQueueNoSend(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = "active"
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Queue = false
	m.session.processing = true
	if got := m.sessionComposerReadOnlyReason(); got != "source does not advertise queue" {
		t.Fatalf("active+no-send+no-queue = %q, want 'source does not advertise queue'", got)
	}
}

func TestCovSessionComposerReadOnlyReason_NoSendCapability(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = "idle"
	m.detail.Capabilities.Send = false
	m.detail.Capabilities.Resume = false
	if got := m.sessionComposerReadOnlyReason(); got != "source does not support send" {
		t.Fatalf("no send = %q, want 'source does not support send'", got)
	}
}

func TestCovSessionComposerReadOnlyReason_IdleWithSendReturnsEmpty(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = "idle"
	m.detail.Capabilities.Send = true
	if got := m.sessionComposerReadOnlyReason(); got != "" {
		t.Fatalf("idle+send should not be read-only: %q", got)
	}
}

// ---- composerPanel View: various modes ---------------------------------------

func TestCovComposerPanelView_ReadOnlyWithReason(t *testing.T) {
	withTestColorProfile(t)
	p := composerPanel{
		Label:          "read-only",
		ReadOnlyReason: "source does not support send",
		ShowInput:      true,
		Draft:          "test",
		Width:          80,
	}
	got := p.View()
	if !strings.Contains(got, "source does not support send") {
		t.Fatalf("view should contain read-only reason:\n%s", got)
	}
}

func TestCovComposerPanelView_ReadOnlyEmptyLabelDefaultsToReadOnly(t *testing.T) {
	p := composerPanel{
		ReadOnlyReason: "no capability",
		Width:          80,
	}
	got := p.View()
	if !strings.Contains(got, "read-only: no capability") {
		t.Fatalf("view should contain 'read-only: no capability':\n%s", got)
	}
}

func TestCovComposerPanelView_QueuePreviewShown(t *testing.T) {
	withTestColorProfile(t)
	p := composerPanel{
		QueuePreview: []string{"first queued message", "second queued message"},
		Width:        80,
		ChipContext:  composerContext{Harness: "evener"},
	}
	got := ansiPattern.ReplaceAllString(p.View(), "")
	wantQueue := "queued (2)\n  1. first queued message\n  2. second queued message\n"
	if !strings.Contains(got, wantQueue) {
		t.Fatalf("panel queue block missing exact ordered entries %q:\n%s", wantQueue, got)
	}
	for _, prefix := range []string{"  1. ", "  2. "} {
		if count := strings.Count(got, prefix); count != 1 {
			t.Fatalf("queue entry prefix %q count=%d, want 1:\n%s", prefix, count, got)
		}
	}
	if strings.Contains(got, "  3. ") {
		t.Fatalf("two-entry fixture rendered an unexpected third queue row:\n%s", got)
	}
}

func TestCovComposerPanelView_AwaitingQuestionChip(t *testing.T) {
	withTestColorProfile(t)
	p := composerPanel{
		AwaitingQuestion: true,
		Width:            80,
	}
	got := p.View()
	if !strings.Contains(got, "question waiting") {
		t.Fatalf("view should contain question waiting chip:\n%s", got)
	}
}

func TestCovComposerPanelView_AttachmentsShown(t *testing.T) {
	withTestColorProfile(t)
	p := composerPanel{
		Attachments: []*clipboard.PastedImage{
			{Path: "/tmp/img1.png", Width: 100, Height: 200, MediaType: "image/png"},
		},
		Width: 80,
	}
	got := p.View()
	if !strings.Contains(got, "attachments") {
		t.Fatalf("view should contain attachments header:\n%s", got)
	}
	if !strings.Contains(got, "img1.png") {
		t.Fatalf("view should contain attachment filename:\n%s", got)
	}
	if !strings.Contains(got, "100x200") {
		t.Fatalf("view should contain dimensions:\n%s", got)
	}
}

func TestCovComposerPanelView_AttachmentWithNoDims(t *testing.T) {
	withTestColorProfile(t)
	p := composerPanel{
		Attachments: []*clipboard.PastedImage{
			{Path: "/tmp/img2.png"},
		},
		Width: 80,
	}
	got := p.View()
	if strings.Contains(got, "0x0") {
		t.Fatalf("view should not contain 0x0 dims:\n%s", got)
	}
}

func TestCovComposerPanelView_NilAttachmentSkipped(t *testing.T) {
	withTestColorProfile(t)
	p := composerPanel{
		Attachments: []*clipboard.PastedImage{nil, {Path: "/tmp/real.png", Width: 10, Height: 10}},
		Width:       80,
	}
	got := p.View()
	if !strings.Contains(got, "real.png") {
		t.Fatalf("view should contain the non-nil attachment:\n%s", got)
	}
}

func TestCovComposerPanelView_KeysFallbackWhenNoChipContext(t *testing.T) {
	withTestColorProfile(t)
	p := composerPanel{
		Keys:  []string{"enter: send", "esc: browse"},
		Width: 80,
	}
	got := p.View()
	if !strings.Contains(got, "enter: send") {
		t.Fatalf("view should contain keys:\n%s", got)
	}
}

func TestCovComposerPanelView_QueueModeFooterWithCanSteer(t *testing.T) {
	withTestColorProfile(t)
	p := composerPanel{
		Label:       "queue",
		CanSteer:    true,
		Width:       80,
		ChipContext: composerContext{Harness: "evener"},
	}
	got := p.View()
	if !strings.Contains(got, "steer") {
		t.Fatalf("queue mode with steer should show steer hint:\n%s", got)
	}
}

func TestCovComposerPanelView_ForkModeFooter(t *testing.T) {
	withTestColorProfile(t)
	p := composerPanel{
		Label:       "fork draft",
		Width:       80,
		ChipContext: composerContext{Harness: "evener"},
	}
	got := p.View()
	if !strings.Contains(got, "fork") {
		t.Fatalf("fork mode should show fork hint:\n%s", got)
	}
}

// ---- filepathBase ------------------------------------------------------------

func TestCovFilepathBase_EmptyReturnsEmpty(t *testing.T) {
	if got := filepathBase(""); got != "" {
		t.Fatalf("filepathBase(\"\") = %q, want empty", got)
	}
}

func TestCovFilepathBase_NoSeparatorReturnsInput(t *testing.T) {
	if got := filepathBase("filename.txt"); got != "filename.txt" {
		t.Fatalf("filepathBase(\"filename.txt\") = %q, want filename.txt", got)
	}
}

func TestCovFilepathBase_ForwardSlash(t *testing.T) {
	if got := filepathBase("/tmp/dir/file.png"); got != "file.png" {
		t.Fatalf("filepathBase with forward slash = %q, want file.png", got)
	}
}

func TestCovFilepathBase_Backslash(t *testing.T) {
	if got := filepathBase("C:\\Users\\test\\file.png"); got != "file.png" {
		t.Fatalf("filepathBase with backslash = %q, want file.png", got)
	}
}

// ---- itoa --------------------------------------------------------------------

func TestCovItoa_Zero(t *testing.T) {
	if got := itoa(0); got != "0" {
		t.Fatalf("itoa(0) = %q, want 0", got)
	}
}

func TestCovItoa_Positive(t *testing.T) {
	if got := itoa(123); got != "123" {
		t.Fatalf("itoa(123) = %q, want 123", got)
	}
}

func TestCovItoa_Negative(t *testing.T) {
	if got := itoa(-42); got != "-42" {
		t.Fatalf("itoa(-42) = %q, want -42", got)
	}
}

func TestCovItoa_Large(t *testing.T) {
	if got := itoa(9999999); got != "9999999" {
		t.Fatalf("itoa(9999999) = %q, want 9999999", got)
	}
}

// ---- renderAttachmentChips: empty and nil -----------------------------------

func TestCovRenderAttachmentChips_EmptyList(t *testing.T) {
	withTestColorProfile(t)
	got := renderAttachmentChips(nil)
	if !strings.Contains(got, "attachments") {
		t.Fatalf("empty attachments should still render header:\n%s", got)
	}
}

// ---- renderQueuePreview -----------------------------------------------------

func TestCovRenderQueuePreview_EmptyList(t *testing.T) {
	withTestColorProfile(t)
	got := renderQueuePreview(nil, 80)
	if !strings.Contains(got, "queued") {
		t.Fatalf("empty queue preview should render header:\n%s", got)
	}
}

func TestCovRenderQueuePreview_LongEntryTruncated(t *testing.T) {
	withTestColorProfile(t)
	longLine := strings.Repeat("x", 100)
	got := ansiPattern.ReplaceAllString(renderQueuePreview([]string{longLine}, 30), "")
	want := "queued (1)\n  1. " + strings.Repeat("x", 23) + "…\n"
	if got != want {
		t.Fatalf("truncated queue preview = %q, want %q", got, want)
	}
}

func TestCovRenderQueuePreview_MultiLineEntryUsesFirstLine(t *testing.T) {
	withTestColorProfile(t)
	got := ansiPattern.ReplaceAllString(renderQueuePreview([]string{"first line\nsecond line"}, 80), "")
	if want := "queued (1)\n  1. first line\n"; got != want {
		t.Fatalf("multiline queue preview = %q, want %q", got, want)
	}
}

// ---- renderComposerDraft ----------------------------------------------------

func TestCovRenderComposerDraft_SimpleLine(t *testing.T) {
	got := renderComposerDraft("hello", 80, 5)
	if want := "> hello█\n"; got != want {
		t.Fatalf("simple draft = %q, want %q", got, want)
	}
}

func TestCovRenderComposerDraft_MaxLinesOneShowsEllipsis(t *testing.T) {
	got := renderComposerDraft("line1\nline2\nline3", 80, 1)
	if want := "> ...█\n"; got != want {
		t.Fatalf("one-line truncated draft = %q, want %q", got, want)
	}
}

func TestCovRenderComposerDraft_ExceedsMaxLinesShowsEllipsis(t *testing.T) {
	got := renderComposerDraft("a\nb\nc\nd\ne", 80, 3)
	if want := "> ...\n  d\n  e█\n"; got != want {
		t.Fatalf("three-line truncated draft = %q, want %q", got, want)
	}
}

func TestCovRenderComposerDraft_WidthLE2NoWrap(t *testing.T) {
	got := renderComposerDraft("no wrap here", 2, 5)
	if want := "> no wrap here█\n"; got != want {
		t.Fatalf("width<=2 draft = %q, want %q", got, want)
	}
}

// ---- sessionComposerMode: fork and read-only --------------------------------

func TestCovSessionComposerMode_ForkMode(t *testing.T) {
	m := newSessionHubModel(nil)
	m.forkDraft = &hubForkDraft{EntryIndex: 0, OriginalText: "x", Label: "x"}
	if got := m.sessionComposerMode(); got != hubComposerModeFork {
		t.Fatalf("fork mode = %v, want hubComposerModeFork", got)
	}
}

func TestCovSessionComposerMode_ReadOnlyWhenRunningNoQueueNoSend(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = "active"
	m.session.processing = true
	m.detail.Capabilities.Queue = false
	m.detail.Capabilities.Send = false
	if got := m.sessionComposerMode(); got != hubComposerModeReadOnly {
		t.Fatalf("active+no-queue+no-send = %v, want hubComposerModeReadOnly", got)
	}
}

func TestCovSessionComposerMode_SendWhenRunningWithSendNoQueue(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = "active"
	m.session.processing = true
	m.detail.Capabilities.Queue = false
	m.detail.Capabilities.Send = true
	if got := m.sessionComposerMode(); got != hubComposerModeSend {
		t.Fatalf("active+send+no-queue = %v, want hubComposerModeSend", got)
	}
}

func TestCovSessionComposerMode_ReadOnlyWhenNoSendAndLive(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.State = "idle"
	m.detail.Capabilities.Send = false
	m.detail.Live = true
	m.detail.Capabilities.Resume = true
	if got := m.sessionComposerMode(); got != hubComposerModeReadOnly {
		t.Fatalf("live+no-send = %v, want hubComposerModeReadOnly", got)
	}
}
