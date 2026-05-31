package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"primeradiant.com/serf/cmd/serf-tui/internal/clipboard"
)

// addPendingAttachment appends a captured image to the composer's
// pending-attachment list. The model cleans up paste-owned temp files when
// the attachment leaves the composer.
//
// The image is assigned a monotonically-increasing MarkerN and the
// literal "[image N]" token is inserted at the textarea's current
// cursor position so the user can reposition or delete it inline. Kata
// 2stz.
func (m *hubModel) addPendingAttachment(img *clipboard.PastedImage) {
	if img == nil {
		return
	}
	m.nextAttachmentMarker++
	img.MarkerN = m.nextAttachmentMarker
	m.session.input.InsertString("[image " + strconv.Itoa(img.MarkerN) + "]")
	m.pendingAttachments = append(m.pendingAttachments, img)
}

// removePendingAttachment drops the attachment at the given index. Out
// of range indices are silently ignored so handler callsites don't need
// to bounds-check after a re-render race.
//
// If the removed attachment carries a marker, the first occurrence of
// its "[image N]" token is stripped from the textarea. Numbering is not
// renumbered; gaps in the surviving markers are intentional. Kata 2stz.
func (m *hubModel) removePendingAttachment(idx int) {
	if idx < 0 || idx >= len(m.pendingAttachments) {
		return
	}
	removed := m.pendingAttachments[idx]
	if removed != nil && removed.MarkerN > 0 {
		tok := "[image " + strconv.Itoa(removed.MarkerN) + "]"
		text := m.session.input.Value()
		if i := strings.Index(text, tok); i >= 0 {
			m.session.input.SetValue(text[:i] + text[i+len(tok):])
		}
	}
	m.cleanupPendingAttachmentFile(removed)
	m.pendingAttachments = append(m.pendingAttachments[:idx], m.pendingAttachments[idx+1:]...)
}

func (m *hubModel) clearPendingAttachments(cleanupFiles bool) {
	if cleanupFiles {
		for _, img := range m.pendingAttachments {
			m.cleanupPendingAttachmentFile(img)
		}
	}
	m.pendingAttachments = nil
	m.nextAttachmentMarker = 0
}

func (m *hubModel) clearSubmittedAttachments(submitted []*clipboard.PastedImage, cleanupFiles bool) {
	if len(submitted) == 0 {
		return
	}
	submittedSet := make(map[*clipboard.PastedImage]struct{}, len(submitted))
	for _, img := range submitted {
		if img == nil {
			continue
		}
		submittedSet[img] = struct{}{}
		if cleanupFiles {
			m.cleanupPendingAttachmentFile(img)
		}
	}
	if len(submittedSet) == 0 {
		return
	}
	kept := m.pendingAttachments[:0]
	for _, img := range m.pendingAttachments {
		if _, ok := submittedSet[img]; ok {
			continue
		}
		kept = append(kept, img)
	}
	m.pendingAttachments = kept
	if len(m.pendingAttachments) == 0 && cleanupFiles {
		m.nextAttachmentMarker = 0
	}
}

func (m *hubModel) restoreSubmittedAttachments(submitted []*clipboard.PastedImage) {
	if len(submitted) == 0 {
		return
	}
	present := make(map[*clipboard.PastedImage]struct{}, len(m.pendingAttachments))
	for _, img := range m.pendingAttachments {
		if img != nil {
			present[img] = struct{}{}
		}
	}
	restored := make([]*clipboard.PastedImage, 0, len(submitted)+len(m.pendingAttachments))
	for _, img := range submitted {
		if img == nil {
			continue
		}
		if _, ok := present[img]; ok {
			continue
		}
		restored = append(restored, img)
		if img.MarkerN > m.nextAttachmentMarker {
			m.nextAttachmentMarker = img.MarkerN
		}
	}
	m.pendingAttachments = append(restored, m.pendingAttachments...)
}

func (m *hubModel) restoreFailedComposerPayload(draft string, submitted []*clipboard.PastedImage) bool {
	if m.session.input.Value() != "" || len(m.pendingAttachments) > 0 {
		m.clearSubmittedAttachments(submitted, true)
		return false
	}
	m.restoreSubmittedAttachments(submitted)
	m.session.setInputValue(draft)
	return true
}

func (m *hubModel) noteUnrestoredFailedComposerPayload(action, draft string, submitted []*clipboard.PastedImage) {
	preview := strings.TrimSpace(draft)
	if preview == "" {
		switch len(submitted) {
		case 0:
			return
		case 1:
			preview = "[image]"
		default:
			preview = fmt.Sprintf("[%d images]", len(submitted))
		}
	}
	m.addSessionSystem(fmt.Sprintf("%s failed; preserved current draft instead of restoring failed payload: %s", action, preview))
}

func (m *hubModel) snapshotPendingAttachmentsForSubmit() []*clipboard.PastedImage {
	if len(m.pendingAttachments) == 0 {
		return nil
	}
	m.attachmentSubmitsInFlight++
	submitted := append([]*clipboard.PastedImage(nil), m.pendingAttachments...)
	m.clearSubmittedAttachments(submitted, false)
	return submitted
}

func (m *hubModel) finishAttachmentSubmit() {
	if m.attachmentSubmitsInFlight > 0 {
		m.attachmentSubmitsInFlight--
	}
	if m.attachmentSubmitsInFlight != 0 {
		return
	}
	for _, img := range m.deferredAttachmentCleanup {
		cleanupPendingAttachmentFile(img)
	}
	m.deferredAttachmentCleanup = nil
}

func (m *hubModel) cleanupPendingAttachmentFile(img *clipboard.PastedImage) {
	if m.attachmentSubmitsInFlight > 0 {
		m.deferredAttachmentCleanup = append(m.deferredAttachmentCleanup, img)
		return
	}
	cleanupPendingAttachmentFile(img)
}

func cleanupPendingAttachmentFile(img *clipboard.PastedImage) {
	if img == nil || img.Path == "" {
		return
	}
	switch img.Origin {
	case "clipboard-image", "wsl":
		_ = os.Remove(img.Path)
	}
}
