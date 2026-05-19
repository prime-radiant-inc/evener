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

func TestSessionSwitchClearsPendingAttachmentsAndTempFiles(t *testing.T) {
	m := newSessionHubModel(nil)
	path := filepath.Join(t.TempDir(), "paste.png")
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		t.Fatalf("write temp attachment: %v", err)
	}
	m.pendingAttachments = []*PastedImage{{
		Path:      path,
		MediaType: "image/png",
		Origin:    "clipboard-image",
		MarkerN:   1,
	}}
	m.nextAttachmentMarker = 1
	m.session.input.SetValue("[image 1]")

	updated, _ := m.Update(hubSessionMsg{
		detail: hubSessionDetail{
			Ref:       "local:02NEXT",
			SessionID: "02NEXT",
			Title:     "next task",
			State:     "idle",
		},
	})
	got := updated.(hubModel)

	if len(got.pendingAttachments) != 0 {
		t.Fatalf("pendingAttachments len = %d, want 0", len(got.pendingAttachments))
	}
	if got.nextAttachmentMarker != 0 {
		t.Fatalf("nextAttachmentMarker = %d, want 0", got.nextAttachmentMarker)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temp attachment stat err = %v, want not exist", err)
	}
}

func TestRemoveAttachmentDefersTempCleanupDuringSubmit(t *testing.T) {
	m := newSessionHubModel(nil)
	path := filepath.Join(t.TempDir(), "paste.png")
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		t.Fatalf("write temp attachment: %v", err)
	}
	m.pendingAttachments = []*PastedImage{{
		Path:   path,
		Origin: "clipboard-image",
	}}
	m.attachmentSubmitsInFlight = 1

	m.removePendingAttachment(0)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("temp attachment removed while submit in flight: %v", err)
	}

	m.finishAttachmentSubmit()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temp attachment stat err = %v, want not exist after submit finishes", err)
	}
}

func TestTextOnlyCompletionDoesNotReleaseDeferredAttachmentCleanup(t *testing.T) {
	m := newSessionHubModel(nil)
	path := filepath.Join(t.TempDir(), "paste.png")
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		t.Fatalf("write temp attachment: %v", err)
	}
	m.attachmentSubmitsInFlight = 1
	m.deferredAttachmentCleanup = []*PastedImage{{
		Path:   path,
		Origin: "clipboard-image",
	}}

	updated, _ := m.Update(hubSendMsg{text: "text only"})
	got := updated.(hubModel)

	if got.attachmentSubmitsInFlight != 1 {
		t.Fatalf("attachmentSubmitsInFlight = %d, want 1", got.attachmentSubmitsInFlight)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("deferred temp attachment cleaned by text-only completion: %v", err)
	}
}

// TestAltBackspaceRemovesLastAttachment verifies that pressing
// Alt+Backspace in the composer drops the most-recently-added
// attachment chip without touching the rest. Kata 5vxd.
func TestAltBackspaceRemovesLastAttachment(t *testing.T) {
	m := newSessionHubModel(nil)
	first := &PastedImage{Path: "/tmp/one.png", Width: 320, Height: 240}
	second := &PastedImage{Path: "/tmp/two.png", Width: 1024, Height: 768}
	m.pendingAttachments = []*PastedImage{first, second}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace, Alt: true})
	got := updated.(hubModel)

	if n := len(got.pendingAttachments); n != 1 {
		t.Fatalf("after Alt+Backspace len = %d, want 1", n)
	}
	if got.pendingAttachments[0] != first {
		t.Fatalf("after Alt+Backspace pendingAttachments[0] = %+v, want first", got.pendingAttachments[0])
	}
}

// TestCtrlHWithAttachmentsFallsThrough verifies Ctrl-H is not reserved for
// chip removal. Many terminals encode ordinary Backspace as Ctrl-H, so the
// textarea should see the key even when attachments are staged. Kata 5vxd.
func TestCtrlHWithAttachmentsFallsThrough(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.setInputValue("hello")
	m.pendingAttachments = []*PastedImage{{Path: "/tmp/one.png"}}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlH})
	got := updated.(hubModel)

	if n := len(got.pendingAttachments); n != 1 {
		t.Fatalf("pendingAttachments len = %d, want 1", n)
	}
	if got.session.input.Value() == "hello" {
		t.Fatalf("Ctrl-H was swallowed before textarea handling; input still %q", got.session.input.Value())
	}
}

// TestAltBackspaceDoesNotFireDuringPalette verifies that when the
// command palette is open Alt+Backspace falls through to the palette
// handler instead of removing an attachment. Kata 5vxd.
func TestAltBackspaceDoesNotFireDuringPalette(t *testing.T) {
	m := newSessionHubModel(nil)
	first := &PastedImage{Path: "/tmp/one.png", Width: 320, Height: 240}
	second := &PastedImage{Path: "/tmp/two.png", Width: 1024, Height: 768}
	m.pendingAttachments = []*PastedImage{first, second}
	m.openCommandPalette()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace, Alt: true})
	got := updated.(hubModel)

	if n := len(got.pendingAttachments); n != 2 {
		t.Fatalf("pendingAttachments len = %d, want 2 (palette swallowed key)", n)
	}
	if got.commandPalette == nil {
		t.Fatalf("commandPalette closed unexpectedly")
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

	// Composer inserts an "[image N]" marker at the cursor on attach
	// (kata 2stz). It is no longer empty after Ctrl+V.
	if got.session.input.Value() != "[image 1]" {
		t.Fatalf("Ctrl+V textarea = %q, want %q", got.session.input.Value(), "[image 1]")
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

	// Composer inserts an "[image N]" marker at the cursor on attach
	// (kata 2stz). It is no longer empty after Ctrl+Alt+V.
	if got.session.input.Value() != "[image 1]" {
		t.Fatalf("Ctrl+Alt+V textarea = %q, want %q", got.session.input.Value(), "[image 1]")
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

	// Pasted path attaches as an image AND inserts an "[image N]" marker
	// at the cursor (kata 2stz). With setInputValue("before") the cursor
	// ends up at end-of-text, so the marker appends after "before".
	if got.session.input.Value() != "before[image 1]" {
		t.Fatalf("pasted path textarea = %q (want %q)", got.session.input.Value(), "before[image 1]")
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
