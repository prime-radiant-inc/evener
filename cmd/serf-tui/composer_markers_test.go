package main

import (
	"strings"
	"testing"
)

// TestAddPendingAttachment_InsertsMarkerAtCursor verifies that adding a
// single attachment to an empty composer inserts the "[image 1]" literal
// at the cursor position and assigns MarkerN=1 to the attachment.
// Kata 2stz.
func TestAddPendingAttachment_InsertsMarkerAtCursor(t *testing.T) {
	m := newSessionHubModel(nil)

	img := &PastedImage{Path: "/tmp/one.png", MediaType: "image/png"}
	m.addPendingAttachment(img)

	if got := m.session.input.Value(); got != "[image 1]" {
		t.Fatalf("input value = %q, want %q", got, "[image 1]")
	}
	if img.MarkerN != 1 {
		t.Fatalf("img.MarkerN = %d, want 1", img.MarkerN)
	}
}

// TestAddPendingAttachment_NumbersMonotonic verifies that successive
// attachments receive monotonically increasing marker numbers starting
// at 1. Kata 2stz.
func TestAddPendingAttachment_NumbersMonotonic(t *testing.T) {
	m := newSessionHubModel(nil)

	a := &PastedImage{Path: "/tmp/a.png"}
	b := &PastedImage{Path: "/tmp/b.png"}
	c := &PastedImage{Path: "/tmp/c.png"}
	m.addPendingAttachment(a)
	m.addPendingAttachment(b)
	m.addPendingAttachment(c)

	if a.MarkerN != 1 || b.MarkerN != 2 || c.MarkerN != 3 {
		t.Fatalf("markers = (%d, %d, %d), want (1, 2, 3)", a.MarkerN, b.MarkerN, c.MarkerN)
	}
	want := "[image 1][image 2][image 3]"
	if got := m.session.input.Value(); got != want {
		t.Fatalf("input value = %q, want %q", got, want)
	}
}

// TestAddPendingAttachment_InsertsAtCursorPosition verifies that the
// marker text is inserted at the textarea's current cursor position,
// not blindly appended to the end. Kata 2stz.
func TestAddPendingAttachment_InsertsAtCursorPosition(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.input.SetValue("hello world")
	// Move cursor back to position 5 (between "hello" and " world").
	// SetCursor is column-based within the current row.
	m.session.input.CursorStart()
	m.session.input.SetCursor(5)

	img := &PastedImage{Path: "/tmp/one.png"}
	m.addPendingAttachment(img)

	want := "hello[image 1] world"
	if got := m.session.input.Value(); got != want {
		t.Fatalf("input value = %q, want %q", got, want)
	}
}

// TestRemovePendingAttachment_StripsMarker verifies that removing an
// attachment strips its "[image N]" marker from the textarea while
// leaving the surviving attachment's marker (and its number) intact.
// Kata 2stz.
func TestRemovePendingAttachment_StripsMarker(t *testing.T) {
	m := newSessionHubModel(nil)
	a := &PastedImage{Path: "/tmp/a.png"}
	b := &PastedImage{Path: "/tmp/b.png"}
	m.addPendingAttachment(a)
	m.addPendingAttachment(b)

	m.removePendingAttachment(0)

	if len(m.pendingAttachments) != 1 {
		t.Fatalf("attachments len = %d, want 1", len(m.pendingAttachments))
	}
	if m.pendingAttachments[0].MarkerN != 2 {
		t.Fatalf("surviving attachment MarkerN = %d, want 2", m.pendingAttachments[0].MarkerN)
	}
	got := m.session.input.Value()
	if strings.Contains(got, "[image 1]") {
		t.Fatalf("input still contains stripped marker [image 1]: %q", got)
	}
	if !strings.Contains(got, "[image 2]") {
		t.Fatalf("input missing surviving marker [image 2]: %q", got)
	}
}

// TestNextMarker_AfterRemoval_DoesNotReuse verifies that marker numbers
// are never reused: removing the first attachment then adding a new one
// must produce marker 3, not 1. Gaps in the marker sequence are allowed.
// Kata 2stz.
func TestNextMarker_AfterRemoval_DoesNotReuse(t *testing.T) {
	m := newSessionHubModel(nil)
	a := &PastedImage{Path: "/tmp/a.png"}
	b := &PastedImage{Path: "/tmp/b.png"}
	m.addPendingAttachment(a)
	m.addPendingAttachment(b)

	m.removePendingAttachment(0)

	c := &PastedImage{Path: "/tmp/c.png"}
	m.addPendingAttachment(c)

	if c.MarkerN != 3 {
		t.Fatalf("new attachment MarkerN = %d, want 3 (never reuse 1)", c.MarkerN)
	}
	got := m.session.input.Value()
	if strings.Contains(got, "[image 1]") {
		t.Fatalf("input must not contain reused marker [image 1]: %q", got)
	}
	if !strings.Contains(got, "[image 3]") {
		t.Fatalf("input missing newly inserted marker [image 3]: %q", got)
	}
}

// TestNextMarker_AfterRemovingHighest_DoesNotReuse verifies the high-water
// marker counter survives removing the highest (and only) marker. Kata 2stz.
func TestNextMarker_AfterRemovingHighest_DoesNotReuse(t *testing.T) {
	m := newSessionHubModel(nil)
	a := &PastedImage{Path: "/tmp/a.png"}
	m.addPendingAttachment(a)

	m.removePendingAttachment(0)

	b := &PastedImage{Path: "/tmp/b.png"}
	m.addPendingAttachment(b)

	if b.MarkerN != 2 {
		t.Fatalf("new attachment MarkerN = %d, want 2 (never reuse removed highest marker)", b.MarkerN)
	}
	got := m.session.input.Value()
	if strings.Contains(got, "[image 1]") {
		t.Fatalf("input must not contain reused marker [image 1]: %q", got)
	}
	if !strings.Contains(got, "[image 2]") {
		t.Fatalf("input missing newly inserted marker [image 2]: %q", got)
	}
}

// TestRemovePendingAttachment_NoOpForUnassignedMarker verifies that
// removing an attachment whose MarkerN is 0 (unassigned, defensive case)
// does not mutate the textarea or panic. Kata 2stz.
func TestRemovePendingAttachment_NoOpForUnassignedMarker(t *testing.T) {
	m := newSessionHubModel(nil)
	m.session.input.SetValue("preserved text")
	// Manually craft a PastedImage with MarkerN=0 and append without
	// going through addPendingAttachment, which assigns a marker.
	m.pendingAttachments = []*PastedImage{{Path: "/tmp/legacy.png", MarkerN: 0}}

	m.removePendingAttachment(0)

	if len(m.pendingAttachments) != 0 {
		t.Fatalf("attachments len = %d, want 0", len(m.pendingAttachments))
	}
	if got := m.session.input.Value(); got != "preserved text" {
		t.Fatalf("input value = %q, want %q (no spurious SetValue)", got, "preserved text")
	}
}
