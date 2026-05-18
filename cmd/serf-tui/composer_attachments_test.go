package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestPendingAttachmentsMutability verifies that the hub session model
// exposes a pendingAttachments slice and an addPendingAttachment helper
// that appends to it. This is the foundation for the rest of the
// composer-attachment UX.
func TestPendingAttachmentsMutability(t *testing.T) {
	m := newSessionHubModel(nil)

	if got := len(m.pendingAttachments); got != 0 {
		t.Fatalf("initial pendingAttachments len = %d, want 0", got)
	}

	img := &PastedImage{
		Path:      "/tmp/pretend.png",
		MediaType: "image/png",
		Width:     320,
		Height:    240,
		Size:      1234,
		Origin:    "clipboard-image",
	}
	m.addPendingAttachment(img)

	if got := len(m.pendingAttachments); got != 1 {
		t.Fatalf("after addPendingAttachment len = %d, want 1", got)
	}
	if m.pendingAttachments[0] != img {
		t.Fatalf("pendingAttachments[0] = %+v, want the added image", m.pendingAttachments[0])
	}
}

// TestAttachmentChipsRender verifies the composer renders one chip per
// pending attachment, in the documented "📎 <name> (WxH) [×]" format.
func TestAttachmentChipsRender(t *testing.T) {
	m := newSessionHubModel(nil)
	m.pendingAttachments = []*PastedImage{
		{Path: "/tmp/screenshot-one.png", MediaType: "image/png", Width: 320, Height: 240},
		{Path: "/tmp/screenshot-two.png", MediaType: "image/png", Width: 1024, Height: 768},
	}

	view := m.sessionView()
	for _, want := range []string{
		"📎 screenshot-one.png (320x240) [×]",
		"📎 screenshot-two.png (1024x768) [×]",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("composer view missing chip %q:\n%s", want, view)
		}
	}
}

// TestRemoveAttachmentDropsByIndex verifies the removePendingAttachment
// helper removes the requested chip and leaves the rest in order.
func TestRemoveAttachmentDropsByIndex(t *testing.T) {
	m := newSessionHubModel(nil)
	first := &PastedImage{Path: "/tmp/one.png", Width: 320, Height: 240}
	second := &PastedImage{Path: "/tmp/two.png", Width: 1024, Height: 768}
	m.pendingAttachments = []*PastedImage{first, second}

	m.removePendingAttachment(0)

	if got := len(m.pendingAttachments); got != 1 {
		t.Fatalf("after remove len = %d, want 1", got)
	}
	if m.pendingAttachments[0] != second {
		t.Fatalf("after remove pendingAttachments[0] = %+v, want second", m.pendingAttachments[0])
	}
}

// fakeClipboardSourceForKeybind provides an in-memory image so Ctrl+V
// pushes a real PastedImage onto pendingAttachments.
func fakeClipboardSourceForKeybind(t *testing.T) ClipboardSource {
	t.Helper()
	return &fakeClipboard{
		filesErr:       errors.New("no file list"),
		imageBytes:     []byte("\x89PNG\r\n\x1a\nfake-png-bytes"),
		imageMediaType: "image/png",
	}
}

// TestCtrlVKeybindAttachesClipboardImage verifies that pressing Ctrl+V
// in the composer reads the (faked) clipboard, pushes the resulting
// PastedImage onto pendingAttachments, and does NOT insert characters
// into the textarea.
func TestCtrlVKeybindAttachesClipboardImage(t *testing.T) {
	m := newSessionHubModel(nil)
	m.clipboardSource = fakeClipboardSourceForKeybind(t)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	got := updated.(hubModel)

	if got.session.input.Value() != "" {
		t.Fatalf("Ctrl+V leaked into textarea: %q", got.session.input.Value())
	}
	if len(got.pendingAttachments) != 1 {
		t.Fatalf("Ctrl+V attachments len = %d, want 1", len(got.pendingAttachments))
	}
	t.Cleanup(func() {
		for _, att := range got.pendingAttachments {
			_ = os.Remove(att.Path)
		}
	})
}

// TestCtrlAltVKeybindAttachesClipboardImage verifies the WSL-friendly
// Ctrl+Alt+V binding behaves identically to Ctrl+V.
func TestCtrlAltVKeybindAttachesClipboardImage(t *testing.T) {
	m := newSessionHubModel(nil)
	m.clipboardSource = fakeClipboardSourceForKeybind(t)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v"), Alt: true})
	got := updated.(hubModel)

	if got.session.input.Value() != "" {
		t.Fatalf("Ctrl+Alt+V leaked into textarea: %q", got.session.input.Value())
	}
	if len(got.pendingAttachments) != 1 {
		t.Fatalf("Ctrl+Alt+V attachments len = %d, want 1", len(got.pendingAttachments))
	}
	t.Cleanup(func() {
		for _, att := range got.pendingAttachments {
			_ = os.Remove(att.Path)
		}
	})
}

// TestPastedPathAttachesImage verifies that a bracketed paste of a path
// pointing at an existing image file is interpreted as an attachment
// (not inserted into the textarea).
func TestPastedPathAttachesImage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(path, []byte("\x89PNG\r\n\x1a\n"), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	m := newSessionHubModel(nil)
	m.session.setInputValue("before")

	updated, _ := m.Update(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune(path),
		Paste: true,
	})
	got := updated.(hubModel)

	if got.session.input.Value() != "before" {
		t.Fatalf("pasted path leaked into textarea: %q (want %q)", got.session.input.Value(), "before")
	}
	if len(got.pendingAttachments) != 1 {
		t.Fatalf("attachments len = %d, want 1", len(got.pendingAttachments))
	}
	if got.pendingAttachments[0].Path != path {
		t.Fatalf("attached path = %q, want %q", got.pendingAttachments[0].Path, path)
	}
}

// TestPastedNonPathFallsThroughToTextarea verifies that pasted text that
// doesn't look like a path is inserted into the textarea as normal.
func TestPastedNonPathFallsThroughToTextarea(t *testing.T) {
	m := newSessionHubModel(nil)

	updated, _ := m.Update(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("not a path"),
		Paste: true,
	})
	got := updated.(hubModel)

	if got.session.input.Value() != "not a path" {
		t.Fatalf("non-path paste should land in textarea, got %q", got.session.input.Value())
	}
	if len(got.pendingAttachments) != 0 {
		t.Fatalf("non-path paste should not add attachments, got %d", len(got.pendingAttachments))
	}
}

// TestPastedNonImagePathFallsThroughToTextarea verifies that pasting a
// path with a non-image extension is inserted into the textarea
// verbatim instead of being attached.
func TestPastedNonImagePathFallsThroughToTextarea(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	m := newSessionHubModel(nil)

	updated, _ := m.Update(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune(path),
		Paste: true,
	})
	got := updated.(hubModel)

	if got.session.input.Value() != path {
		t.Fatalf("non-image path paste should land in textarea, got %q", got.session.input.Value())
	}
	if len(got.pendingAttachments) != 0 {
		t.Fatalf("non-image path paste should not add attachments, got %d", len(got.pendingAttachments))
	}
}
