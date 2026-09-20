package agent

// Durable persistence for user-input image attachments. Pasted images
// reach the model as inline ContentImage parts, but until now the bytes
// existed nowhere the model (or a later session) could reach them again:
// once context folding drops the image part, the image was gone. These
// helpers write each accepted attachment into the session's state
// directory so the recorded user message can name a stable, re-readable
// path.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/agent/events"
)

// attachmentsSubdir is the per-session directory, under
// <stateDir>/sessions/<id>/, where persisted input attachments live.
const attachmentsSubdir = "attachments"

// attachmentPathPrefixLen is how many hex characters of the attachment's
// sha256 prefix the stored filename carries. Combined with the sanitized
// original name it makes the path unique per content, stable across
// retries, and deduplicating for repeated pastes of the same bytes.
const attachmentPathPrefixLen = 16

// persistInputImages writes each image's bytes under
// <stateDir>/sessions/<id>/attachments/ and returns copies of the
// attachments carrying their on-disk Path. Sessions without a state
// directory (library/test shape) get their input back untouched, and a
// write failure degrades the same way: the image still rides the turn
// inline and its Path stays empty, so nothing is announced that a reader
// could not fetch. Each failure is reported as a diagnostic warning
// instead of failing the turn — the inline copy is unaffected and the
// turn's own acceptance must not hinge on disk writes.
func (s *Session) persistInputImages(images []ImageAttachment) []ImageAttachment {
	if s == nil || len(images) == 0 || s.stateDir == "" {
		return images
	}
	dir := filepath.Join(s.stateDir, sessionsSubdir, s.id, attachmentsSubdir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.emitDiagnosticWarning(events.WarningData{Message: fmt.Sprintf("persist input attachments: %v", err)})
		return images
	}
	out := make([]ImageAttachment, len(images))
	copy(out, images)
	for i := range out {
		if out[i].Path != "" {
			continue // already persisted by an earlier build of the same input
		}
		name := sanitizeAttachmentName(out[i])
		sum := sha256.Sum256(out[i].Data)
		path := filepath.Join(dir, hex.EncodeToString(sum[:])[:attachmentPathPrefixLen]+"-"+name)
		if err := os.WriteFile(path, out[i].Data, 0o600); err != nil {
			s.emitDiagnosticWarning(events.WarningData{Message: fmt.Sprintf("persist input attachment %q: %v", name, err)})
			continue
		}
		out[i].Path = path
	}
	return out
}

// sanitizeAttachmentName derives the stored filename for an attachment:
// the original name's stem, restricted to filesystem-safe ASCII and
// capped in length, plus the canonical extension for the media type.
func sanitizeAttachmentName(img ImageAttachment) string {
	base := filepath.Base(img.Name)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	name := strings.Trim(b.String(), "._")
	// The builder only emits ASCII, so a byte slice here is rune-safe.
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		name = "image"
	}
	return name + attachmentExtensionForMediaType(img.MediaType)
}

// attachmentExtensionForMediaType maps an attachment's MIME type to its
// canonical file extension, defaulting to PNG: the composer surfaces
// re-encode everything to PNG, and unknown types still get a name a reader
// can make sense of.
func attachmentExtensionForMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}
