package agent

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestBoundReadMarkdownEnvelopeWithHint_EscapeHeavyExpansion covers the
// escape-heavy expansion fallback path (lines 1163-1187) where the expansion
// itself exceeds the serialized hard cap after content bounding.
func TestBoundReadMarkdownEnvelopeWithHint_EscapeHeavyExpansion(t *testing.T) {
	// Create raw bytes that are non-UTF8 so the expansion is base64-encoded,
	// which expands 3 bytes -> 4 chars, making it easier to exceed the cap.
	raw := make([]byte, 300000)
	for i := range raw {
		raw[i] = 0xFF // non-UTF8, forces base64 encoding
	}
	expansion := &transcriptTurnExpansion{
		ExpandTurn:     1,
		OffsetBytes:    0,
		BytesReturned:  len(raw),
		TotalBytes:     len(raw),
		Representation: transcriptV2JSONLRepresentation,
		Encoding:       "base64",
		Data:           base64.StdEncoding.EncodeToString(raw),
	}
	envelope := readMarkdownEnvelope{
		TranscriptRef: "local:abc",
		Format:        "markdown",
		ContentType:   "text/markdown",
		Content:       "some content",
		Meta:          readMarkdownMeta{TurnsTotal: 1, TurnsRendered: 1},
		Expansion:     expansion,
	}
	out, err := boundReadMarkdownEnvelopeWithHint(envelope, transcriptExpansionReadHint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The output should be bounded — either the expansion was shrunk or removed.
	encoded, _ := json.MarshalIndent(out, "", "  ")
	if len(encoded) > hardCapChars {
		t.Fatalf("output exceeds hard cap: %d > %d", len(encoded), hardCapChars)
	}
}

// TestBoundReadMarkdownEnvelopeWithHint_ExpansionDecodeError covers the
// decode error path (lines 1150-1152).
func TestBoundReadMarkdownEnvelopeWithHint_ExpansionDecodeError(t *testing.T) {
	envelope := readMarkdownEnvelope{
		TranscriptRef: "local:abc",
		Format:        "markdown",
		ContentType:   "text/markdown",
		Content:       strings.Repeat("x", hardCapChars+1000), // force over-cap
		Meta:          readMarkdownMeta{TurnsTotal: 1, TurnsRendered: 1},
		Expansion: &transcriptTurnExpansion{
			ExpandTurn:     1,
			OffsetBytes:    0,
			BytesReturned:  10,
			TotalBytes:     10,
			Representation: transcriptV2JSONLRepresentation,
			Encoding:       "base64",
			Data:           "!!!invalid-base64!!!", // invalid base64
		},
	}
	_, err := boundReadMarkdownEnvelopeWithHint(envelope, transcriptExpansionReadHint)
	if err == nil {
		t.Fatal("expected error for invalid base64 expansion")
	}
}

// TestBoundReadMarkdownEnvelopeWithHint_LargeContentNoExpansion covers the
// path where content exceeds the cap and there's no expansion (line 1146).
func TestBoundReadMarkdownEnvelopeWithHint_LargeContentNoExpansion(t *testing.T) {
	envelope := readMarkdownEnvelope{
		TranscriptRef: "local:abc",
		Format:        "markdown",
		ContentType:   "text/markdown",
		Content:       strings.Repeat("x", hardCapChars+1000),
		Meta:          readMarkdownMeta{TurnsTotal: 1, TurnsRendered: 1},
	}
	out, err := boundReadMarkdownEnvelopeWithHint(envelope, transcriptExpansionReadHint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Meta.Truncated {
		t.Fatal("expected truncated content")
	}
	encoded, _ := json.MarshalIndent(out, "", "  ")
	if len(encoded) > hardCapChars {
		t.Fatalf("output exceeds hard cap: %d > %d", len(encoded), hardCapChars)
	}
}

// TestBoundReadMarkdownEnvelopeWithHint_FitsAsIs covers the path where
// the envelope fits without any bounding (line 1142-1144).
func TestBoundReadMarkdownEnvelopeWithHint_FitsAsIs(t *testing.T) {
	envelope := readMarkdownEnvelope{
		TranscriptRef: "local:abc",
		Format:        "markdown",
		ContentType:   "text/markdown",
		Content:       "short content",
		Meta:          readMarkdownMeta{TurnsTotal: 1, TurnsRendered: 1},
	}
	out, err := boundReadMarkdownEnvelopeWithHint(envelope, transcriptExpansionReadHint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Content != "short content" {
		t.Fatalf("expected content unchanged, got %q", out.Content)
	}
	if out.Meta.Truncated {
		t.Fatal("expected not truncated")
	}
}

// TestBoundReadMarkdownEnvelopeWithHint_BoundedExpansion covers the path
// where content is bounded and expansion fits (lines 1156-1158).
func TestBoundReadMarkdownEnvelopeWithHint_BoundedExpansion(t *testing.T) {
	// Content is large but can be bounded; expansion is small and fits.
	raw := []byte("small expansion data")
	envelope := readMarkdownEnvelope{
		TranscriptRef: "local:abc",
		Format:        "markdown",
		ContentType:   "text/markdown",
		Content:       strings.Repeat("x", hardCapChars-1000),
		Meta:          readMarkdownMeta{TurnsTotal: 1, TurnsRendered: 1},
		Expansion: &transcriptTurnExpansion{
			ExpandTurn:     1,
			OffsetBytes:    0,
			BytesReturned:  len(raw),
			TotalBytes:     len(raw),
			Representation: transcriptV2JSONLRepresentation,
			Encoding:       "utf8",
			Data:           string(raw),
		},
	}
	out, err := boundReadMarkdownEnvelopeWithHint(envelope, transcriptExpansionReadHint)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	encoded, _ := json.MarshalIndent(out, "", "  ")
	if len(encoded) > hardCapChars {
		t.Fatalf("output exceeds hard cap: %d > %d", len(encoded), hardCapChars)
	}
}

// TestTranscriptEnvelopeWithExpansionBytes_NoContinuation covers the
// nil-continuation path when the data reaches the end (line 1314).
func TestTranscriptEnvelopeWithExpansionBytes_NoContinuation(t *testing.T) {
	env := readMarkdownEnvelope{
		Expansion: &transcriptTurnExpansion{
			ExpandTurn:  1,
			OffsetBytes: 0,
			TotalBytes:  5,
		},
	}
	result := transcriptEnvelopeWithExpansionBytes(env, []byte("hello"))
	if result.Continuation != nil {
		t.Fatal("expected nil continuation when data reaches total")
	}
}

// TestTranscriptEnvelopeWithExpansionBytes_WithContinuation covers the
// continuation path (line 1312).
func TestTranscriptEnvelopeWithExpansionBytes_WithContinuation(t *testing.T) {
	env := readMarkdownEnvelope{
		Expansion: &transcriptTurnExpansion{
			ExpandTurn:  1,
			OffsetBytes: 0,
			TotalBytes:  100,
		},
	}
	result := transcriptEnvelopeWithExpansionBytes(env, []byte("hello"))
	if result.Continuation == nil {
		t.Fatal("expected continuation when data does not reach total")
	}
	if result.Continuation.OffsetBytes != 5 {
		t.Fatalf("expected continuation offset 5, got %d", result.Continuation.OffsetBytes)
	}
}

// TestTranscriptEnvelopeWithExpansionBytes_NonUTF8 covers the base64 path
// (lines 1305-1308).
func TestTranscriptEnvelopeWithExpansionBytes_NonUTF8(t *testing.T) {
	env := readMarkdownEnvelope{
		Expansion: &transcriptTurnExpansion{
			ExpandTurn:  1,
			OffsetBytes: 0,
			TotalBytes:  3,
		},
	}
	data := []byte{0xFF, 0xFE, 0xFD}
	result := transcriptEnvelopeWithExpansionBytes(env, data)
	if result.Expansion.Encoding != "base64" {
		t.Fatalf("expected base64 encoding, got %q", result.Expansion.Encoding)
	}
}
