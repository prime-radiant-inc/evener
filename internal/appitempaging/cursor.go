package appitempaging

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
)

const (
	CursorVersion         uint8 = 1
	maxCursorEncodedBytes       = 1 << 20
)

type CursorIdentity struct {
	ThreadRef         string
	Incarnation       string
	ProjectionVersion uint16
}

type transcriptItemCursorV1 struct {
	Version           uint8                      `json:"version"`
	ThreadRef         string                     `json:"thread_ref"`
	Incarnation       string                     `json:"incarnation"`
	ProjectionVersion uint16                     `json:"projection_version"`
	Before            appwire.ThreadItemPosition `json:"before"`
}

// EncodeCursor creates the canonical opaque cursor representation. Identity
// values are fences, not client-visible transcript data, and are validated
// before they are encoded.
func EncodeCursor(identity CursorIdentity, before appwire.ThreadItemPosition) (string, error) {
	if !validIdentity(identity) {
		return "", appwire.TranscriptItemCursorStale()
	}
	raw, err := json.Marshal(transcriptItemCursorV1{
		Version:           CursorVersion,
		ThreadRef:         identity.ThreadRef,
		Incarnation:       identity.Incarnation,
		ProjectionVersion: identity.ProjectionVersion,
		Before:            before,
	})
	if err != nil {
		return "", appwire.TranscriptItemCursorStale()
	}
	if base64.RawURLEncoding.EncodedLen(len(raw)) > maxCursorEncodedBytes {
		return "", appwire.TranscriptItemCursorStale()
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeCursor validates the cursor's complete identity fence. The caller
// remains responsible for checking that the decoded boundary is not future and
// is reconstructible in its current source index.
func DecodeCursor(encoded string, want CursorIdentity) (appwire.ThreadItemPosition, error) {
	cursor, err := decodeCursor(encoded)
	if err != nil || !validIdentity(want) || cursor.ThreadRef != want.ThreadRef || cursor.Incarnation != want.Incarnation || cursor.ProjectionVersion != want.ProjectionVersion {
		return appwire.ThreadItemPosition{}, appwire.TranscriptItemCursorStale()
	}
	return cursor.Before, nil
}

// RebaseCursor preserves a valid cursor's identity while replacing its
// exclusive boundary. It is used when a source has already validated a
// continuation position and must not expose its internal representation.
func RebaseCursor(encoded string, before appwire.ThreadItemPosition) (string, error) {
	cursor, err := decodeCursor(encoded)
	if err != nil {
		return "", appwire.TranscriptItemCursorStale()
	}
	return EncodeCursor(CursorIdentity{
		ThreadRef:         cursor.ThreadRef,
		Incarnation:       cursor.Incarnation,
		ProjectionVersion: cursor.ProjectionVersion,
	}, before)
}

func decodeCursor(encoded string) (transcriptItemCursorV1, error) {
	if encoded == "" || len(encoded) > maxCursorEncodedBytes {
		return transcriptItemCursorV1{}, appwire.TranscriptItemCursorStale()
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || !utf8.Valid(raw) {
		return transcriptItemCursorV1{}, appwire.TranscriptItemCursorStale()
	}
	var cursor transcriptItemCursorV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return transcriptItemCursorV1{}, appwire.TranscriptItemCursorStale()
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return transcriptItemCursorV1{}, appwire.TranscriptItemCursorStale()
	}
	if cursor.Version != CursorVersion || !validIdentity(CursorIdentity{
		ThreadRef:         cursor.ThreadRef,
		Incarnation:       cursor.Incarnation,
		ProjectionVersion: cursor.ProjectionVersion,
	}) {
		return transcriptItemCursorV1{}, appwire.TranscriptItemCursorStale()
	}
	return cursor, nil
}

func validIdentity(identity CursorIdentity) bool {
	return utf8.ValidString(identity.ThreadRef) && utf8.ValidString(identity.Incarnation) && strings.TrimSpace(identity.ThreadRef) != "" && strings.TrimSpace(identity.Incarnation) != "" && identity.ProjectionVersion != 0
}
