package appitempaging

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
)

const cursorFormVersion uint8 = 1

const (
	fetchCursorKind  = "fetch"
	windowCursorKind = "window"
)

// FetchCursorOrigin identifies the serving arrangement that can resolve a
// fetch cursor. Remote sources are resolved by the controller hub, not by
// forwarding the cursor to the remote hub.
type FetchCursorOrigin string

const (
	FetchCursorOriginServingDaemon FetchCursorOrigin = "serving-daemon"
	FetchCursorOriginHubCompat     FetchCursorOrigin = "hub-compat"
	FetchCursorOriginSavedIndex    FetchCursorOrigin = "saved-index"
	FetchCursorOriginRemoteSource  FetchCursorOrigin = "remote-source"
)

// FetchCursor addresses one settled item and a byte offset in that item's
// version-fenced canonical wire encoding.
type FetchCursor struct {
	Position    appwire.ThreadItemPosition
	ItemVersion uint64
	Origin      FetchCursorOrigin
	Offset      uint64
}

type fetchCursorWire struct {
	Version           uint8                      `json:"version"`
	Kind              string                     `json:"kind"`
	ThreadRef         string                     `json:"thread_ref"`
	Incarnation       string                     `json:"incarnation"`
	ProjectionVersion uint16                     `json:"projection_version"`
	Position          appwire.ThreadItemPosition `json:"position"`
	ItemVersion       uint64                     `json:"item_version"`
	Origin            FetchCursorOrigin          `json:"origin"`
	Offset            uint64                     `json:"offset"`
}

type fetchCursorDecodeWire struct {
	Version           *uint8                    `json:"version"`
	Kind              *string                   `json:"kind"`
	ThreadRef         *string                   `json:"thread_ref"`
	Incarnation       *string                   `json:"incarnation"`
	ProjectionVersion *uint16                   `json:"projection_version"`
	Position          *cursorPositionDecodeWire `json:"position"`
	ItemVersion       *uint64                   `json:"item_version"`
	Origin            *FetchCursorOrigin        `json:"origin"`
	Offset            *uint64                   `json:"offset"`
}

// EncodeFetchCursor creates a hub-owned cursor for one settled item. Item
// version zero and origins outside the four serving arrangements are refused.
func EncodeFetchCursor(identity CursorIdentity, cursor FetchCursor) (string, error) {
	if !validIdentity(identity) || cursor.ItemVersion == 0 || !validFetchCursorOrigin(cursor.Origin) {
		return "", appwire.TranscriptItemCursorStale()
	}
	return encodeCursorForm(fetchCursorWire{
		Version:           cursorFormVersion,
		Kind:              fetchCursorKind,
		ThreadRef:         identity.ThreadRef,
		Incarnation:       identity.Incarnation,
		ProjectionVersion: identity.ProjectionVersion,
		Position:          cursor.Position,
		ItemVersion:       cursor.ItemVersion,
		Origin:            cursor.Origin,
		Offset:            cursor.Offset,
	})
}

// DecodeFetchCursor validates the fetch form and its complete identity fence.
// The caller remains responsible for checking the position, item version, and
// offset against the current source item.
func DecodeFetchCursor(encoded string, want CursorIdentity) (FetchCursor, error) {
	var wire fetchCursorDecodeWire
	if err := decodeCursorForm(encoded, &wire); err != nil || !validIdentity(want) || !validFetchCursorWire(wire, want) {
		return FetchCursor{}, appwire.TranscriptItemCursorStale()
	}
	return FetchCursor{
		Position:    wire.Position.position(),
		ItemVersion: *wire.ItemVersion,
		Origin:      *wire.Origin,
		Offset:      *wire.Offset,
	}, nil
}

func validFetchCursorWire(wire fetchCursorDecodeWire, want CursorIdentity) bool {
	return wire.Version != nil && *wire.Version == cursorFormVersion &&
		wire.Kind != nil && *wire.Kind == fetchCursorKind &&
		wire.ThreadRef != nil && *wire.ThreadRef == want.ThreadRef &&
		wire.Incarnation != nil && *wire.Incarnation == want.Incarnation &&
		wire.ProjectionVersion != nil && *wire.ProjectionVersion == want.ProjectionVersion &&
		validCursorPosition(wire.Position) &&
		wire.ItemVersion != nil && *wire.ItemVersion != 0 &&
		wire.Origin != nil && validFetchCursorOrigin(*wire.Origin) &&
		wire.Offset != nil
}

func validFetchCursorOrigin(origin FetchCursorOrigin) bool {
	switch origin {
	case FetchCursorOriginServingDaemon, FetchCursorOriginHubCompat, FetchCursorOriginSavedIndex, FetchCursorOriginRemoteSource:
		return true
	default:
		return false
	}
}

// WindowCursorDirection states which exclusive boundary a window continuation
// carries. Backward resumes latest windows; forward resumes since windows.
type WindowCursorDirection string

const (
	WindowCursorDirectionBackward WindowCursorDirection = "backward"
	WindowCursorDirectionForward  WindowCursorDirection = "forward"
)

type windowCursorWire struct {
	Version           uint8                      `json:"version"`
	Kind              string                     `json:"kind"`
	ThreadRef         string                     `json:"thread_ref"`
	Incarnation       string                     `json:"incarnation"`
	ProjectionVersion uint16                     `json:"projection_version"`
	Direction         WindowCursorDirection      `json:"direction"`
	Position          appwire.ThreadItemPosition `json:"position"`
}

type windowCursorDecodeWire struct {
	Version           *uint8                    `json:"version"`
	Kind              *string                   `json:"kind"`
	ThreadRef         *string                   `json:"thread_ref"`
	Incarnation       *string                   `json:"incarnation"`
	ProjectionVersion *uint16                   `json:"projection_version"`
	Direction         *WindowCursorDirection    `json:"direction"`
	Position          *cursorPositionDecodeWire `json:"position"`
}

// EncodeWindowCursor creates a continuation with an explicit exclusive-bound
// direction. It is a separate form from the legacy before-only paging cursor.
func EncodeWindowCursor(identity CursorIdentity, direction WindowCursorDirection, position appwire.ThreadItemPosition) (string, error) {
	if !validIdentity(identity) || !validWindowCursorDirection(direction) {
		return "", appwire.TranscriptItemCursorStale()
	}
	return encodeCursorForm(windowCursorWire{
		Version:           cursorFormVersion,
		Kind:              windowCursorKind,
		ThreadRef:         identity.ThreadRef,
		Incarnation:       identity.Incarnation,
		ProjectionVersion: identity.ProjectionVersion,
		Direction:         direction,
		Position:          position,
	})
}

// DecodeWindowCursor validates the complete identity fence and requires the
// direction expected by the caller, so a forward continuation cannot be spent
// as a backward continuation or vice versa.
func DecodeWindowCursor(encoded string, want CursorIdentity, wantDirection WindowCursorDirection) (appwire.ThreadItemPosition, error) {
	var wire windowCursorDecodeWire
	if err := decodeCursorForm(encoded, &wire); err != nil || !validIdentity(want) || !validWindowCursorDirection(wantDirection) || !validWindowCursorWire(wire, want, wantDirection) {
		return appwire.ThreadItemPosition{}, appwire.TranscriptItemCursorStale()
	}
	return wire.Position.position(), nil
}

func validWindowCursorWire(wire windowCursorDecodeWire, want CursorIdentity, wantDirection WindowCursorDirection) bool {
	return wire.Version != nil && *wire.Version == cursorFormVersion &&
		wire.Kind != nil && *wire.Kind == windowCursorKind &&
		wire.ThreadRef != nil && *wire.ThreadRef == want.ThreadRef &&
		wire.Incarnation != nil && *wire.Incarnation == want.Incarnation &&
		wire.ProjectionVersion != nil && *wire.ProjectionVersion == want.ProjectionVersion &&
		wire.Direction != nil && *wire.Direction == wantDirection && validWindowCursorDirection(*wire.Direction) &&
		validCursorPosition(wire.Position)
}

func validWindowCursorDirection(direction WindowCursorDirection) bool {
	return direction == WindowCursorDirectionBackward || direction == WindowCursorDirectionForward
}

type cursorPositionDecodeWire struct {
	Entry *uint64 `json:"entry"`
	Item  *uint32 `json:"item"`
	Sub   uint32  `json:"sub,omitempty"`
}

func validCursorPosition(position *cursorPositionDecodeWire) bool {
	return position != nil && position.Entry != nil && position.Item != nil
}

func (position cursorPositionDecodeWire) position() appwire.ThreadItemPosition {
	return appwire.ThreadItemPosition{Entry: *position.Entry, Item: *position.Item, Sub: position.Sub}
}

func encodeCursorForm(wire any) (string, error) {
	raw, err := json.Marshal(wire)
	if err != nil || base64.RawURLEncoding.EncodedLen(len(raw)) > maxCursorEncodedBytes {
		return "", appwire.TranscriptItemCursorStale()
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCursorForm(encoded string, wire any) error {
	if encoded == "" || len(encoded) > maxCursorEncodedBytes {
		return appwire.TranscriptItemCursorStale()
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || !utf8.Valid(raw) {
		return appwire.TranscriptItemCursorStale()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(wire); err != nil {
		return appwire.TranscriptItemCursorStale()
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return appwire.TranscriptItemCursorStale()
	}
	return nil
}
