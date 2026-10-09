// Package activitybound holds the single statement of how the session-activity
// projection bounds free-form delegate text: the prose rune caps and the report
// preview's trim, byte window and proof-of-truncation rule.
//
// It is a leaf: it imports only the standard library, so both package agent and
// internal/appprojector may import it. The projector cannot import package
// agent (package agent's own tests import the projector), which is why this
// shared bounding cannot live there.
package activitybound

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"io"
)

// PacketReported is the terminal-packet kind whose message becomes a settled
// delegate's report preview. It mirrors delegatestore.PacketReported, which
// this leaf cannot import.
const PacketReported = "reported"

const (
	// MaxLabelRunes caps the short free-form text the activity projection
	// copies out of a session's own metadata or a delegate descriptor, such as a
	// name, a branch or a resolved model. It mirrors the hub's own sidebar title
	// cap.
	MaxLabelRunes = 200
	// MaxDelegateProseRunes caps the long free-form text a delegate carries:
	// its brief, description, outcome prose, report preview and worktree path.
	MaxDelegateProseRunes = 4096
	// MaxReportPreviewBytes bounds the raw JSON bytes inspected for a reported
	// packet's preview. A Unicode code point needs at most twelve JSON bytes (an
	// escaped surrogate pair); one extra decoded code point proves truncation and
	// the opening quote needs one more byte. This also bounds whitespace and
	// malformed input work.
	MaxReportPreviewBytes = 1 + 12*(MaxDelegateProseRunes+1)
)

// Truncate caps s at maxRunes runes, appending an ellipsis when it truncates so
// a reader can tell a capped value from a genuinely short one. Rune-safe: never
// splits a multi-byte character.
//
// It walks runes only as far as the cap. Converting the whole string to a
// []rune first would allocate proportional to the INPUT (a 4 MiB label
// becoming a ~16 MiB slice) for a result that keeps at most maxRunes runes,
// which defeats the memory bound the cap exists to enforce.
func Truncate(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	// Runes never outnumber bytes, so a string this short cannot need cutting
	// and does not have to be decoded.
	if len(s) <= maxRunes {
		return s
	}
	runeIndex := 0
	cutAt := -1
	for byteIndex := range s {
		if runeIndex == maxRunes-1 {
			cutAt = byteIndex
		}
		if runeIndex == maxRunes {
			// s[:cutAt] is the whole string short of maxRunes-1 runes.
			return s[:cutAt] + "…"
		}
		runeIndex++
	}
	// Multi-byte runes made the byte fast path conservative: the whole string
	// fits after all.
	return s
}

// BoundMessage returns the bounded JSON window of a reported packet's message
// plus whether that window reaches the message's end. It trims leading
// whitespace and clones at most MaxReportPreviewBytes bytes, so the caller
// never holds the whole unbounded message.
func BoundMessage(raw json.RawMessage) (json.RawMessage, bool) {
	window := raw[:min(len(raw), MaxReportPreviewBytes)]
	content := bytes.TrimLeft(window, " \t\r\n")
	if len(content) == 0 {
		return nil, false
	}
	start := len(window) - len(content)
	end := min(len(raw), start+MaxReportPreviewBytes)
	return bytes.Clone(raw[start:end]), end == len(raw)
}

// Preview decodes a window from BoundMessage (with whether it was complete) and
// returns the bounded preview text and whether it was truncated. Only a prefix
// beyond the text cap proves a truncated preview: a partial window that already
// decodes to the whole text yields nothing, because the durable packet is the
// authority for the full value.
func Preview(window json.RawMessage, complete bool) (string, bool) {
	decoded, err := jsontext.AppendUnquote(nil, window)
	partial := !complete && errors.Is(err, io.ErrUnexpectedEOF)
	report := string(decoded)
	if err != nil && !partial {
		// The bounded complete value may contain surrounding whitespace or
		// replacement characters accepted by the durable JSON decoder.
		if json.Unmarshal(window, &report) != nil {
			return "", false
		}
	}
	preview := Truncate(report, MaxDelegateProseRunes)
	if partial && preview == report {
		return "", false
	}
	return preview, preview != report
}

// ReportPreview returns the bounded preview of a reported packet's raw message
// and whether it was truncated. It is BoundMessage followed by Preview, the
// same trim, byte bound and proof-of-truncation rule the activity read applies.
func ReportPreview(raw json.RawMessage) (string, bool) {
	return Preview(BoundMessage(raw))
}
