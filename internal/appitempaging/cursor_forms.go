package appitempaging

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
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
	// Token names the view owner's retained entry this cursor was minted from.
	// Tokens are never reused, so an entry evicted after the cursor was minted
	// cannot be repopulated with a later enrichment of the same item version.
	Token string
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
	Token             string                     `json:"token"`
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
	Token             *string                   `json:"token"`
}

// EncodeFetchCursor creates a hub-owned cursor for one settled item. Item
// version zero and origins outside the four serving arrangements are refused.
func EncodeFetchCursor(identity CursorIdentity, cursor FetchCursor) (string, error) {
	if !validIdentity(identity) || cursor.ItemVersion == 0 || !validFetchCursorOrigin(cursor.Origin) || !validFetchToken(cursor.Token) {
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
		Token:             cursor.Token,
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
		Token:       *wire.Token,
	}, nil
}

// DecodeFetchCursorForRef validates a self-contained fetch cursor against the
// fetch-only request's required ref and returns the embedded view identity.
func DecodeFetchCursorForRef(encoded, threadRef string) (FetchCursor, CursorIdentity, error) {
	var wire fetchCursorDecodeWire
	if err := decodeCursorForm(encoded, &wire); err != nil || !validFetchCursorShape(wire) {
		return FetchCursor{}, CursorIdentity{}, appwire.TranscriptItemCursorStale()
	}
	identity := CursorIdentity{
		ThreadRef:         *wire.ThreadRef,
		Incarnation:       *wire.Incarnation,
		ProjectionVersion: *wire.ProjectionVersion,
	}
	if identity.ThreadRef != threadRef || identity.ProjectionVersion != TranscriptItemProjectionVersion {
		return FetchCursor{}, CursorIdentity{}, appwire.TranscriptItemCursorStale()
	}
	return FetchCursor{
		Position:    wire.Position.position(),
		ItemVersion: *wire.ItemVersion,
		Origin:      *wire.Origin,
		Offset:      *wire.Offset,
		Token:       *wire.Token,
	}, identity, nil
}

// TranslateFetchCursorRef replaces one hub's thread namespace with another's
// while preserving the source view, item and range fence. The destination owns
// remote fetch resolution, so it also supplies the origin that will resolve the
// translated cursor.
func TranslateFetchCursorRef(encoded, fromThreadRef, toThreadRef string, origin FetchCursorOrigin) (string, error) {
	cursor, identity, err := DecodeFetchCursorForRef(encoded, fromThreadRef)
	if err != nil || toThreadRef == "" || !validFetchCursorOrigin(origin) {
		return "", appwire.TranscriptItemCursorStale()
	}
	identity.ThreadRef = toThreadRef
	cursor.Origin = origin
	return EncodeFetchCursor(identity, cursor)
}

func validFetchCursorWire(wire fetchCursorDecodeWire, want CursorIdentity) bool {
	return validFetchCursorShape(wire) &&
		cursorIdentityMatches(wire.ThreadRef, wire.Incarnation, wire.ProjectionVersion, want)
}

func validFetchCursorShape(wire fetchCursorDecodeWire) bool {
	if wire.ThreadRef == nil || wire.Incarnation == nil || wire.ProjectionVersion == nil {
		return false
	}
	return wire.Version != nil && *wire.Version == cursorFormVersion &&
		wire.Kind != nil && *wire.Kind == fetchCursorKind &&
		validIdentity(CursorIdentity{ThreadRef: *wire.ThreadRef, Incarnation: *wire.Incarnation, ProjectionVersion: *wire.ProjectionVersion}) &&
		validCursorPosition(wire.Position) &&
		wire.ItemVersion != nil && *wire.ItemVersion != 0 &&
		wire.Origin != nil && validFetchCursorOrigin(*wire.Origin) &&
		wire.Offset != nil && wire.Token != nil && validFetchToken(*wire.Token)
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

// WindowCursor reconstructs one stable resume range and validates it with
// either a settled item anchor or a digest of an all-empty range.
type WindowCursor struct {
	AnchorTurnID     string
	Position         appwire.ThreadItemPosition
	ItemVersion      uint64
	EmptyRangeDigest string
	ResumeWindow     *appwire.ThreadTurnWindow
}

type windowCursorWire struct {
	Version           uint8                       `json:"version"`
	Kind              string                      `json:"kind"`
	ThreadRef         string                      `json:"thread_ref"`
	Incarnation       string                      `json:"incarnation"`
	ProjectionVersion uint16                      `json:"projection_version"`
	Direction         WindowCursorDirection       `json:"direction"`
	AnchorTurnID      *string                     `json:"anchor_turn_id,omitempty"`
	Position          *appwire.ThreadItemPosition `json:"position,omitempty"`
	ItemVersion       *uint64                     `json:"item_version,omitempty"`
	EmptyRangeDigest  *string                     `json:"empty_range_digest,omitempty"`
	ResumeWindow      *appwire.ThreadTurnWindow   `json:"resume_window"`
}

type windowCursorDecodeWire struct {
	Version           *uint8                    `json:"version"`
	Kind              *string                   `json:"kind"`
	ThreadRef         *string                   `json:"thread_ref"`
	Incarnation       *string                   `json:"incarnation"`
	ProjectionVersion *uint16                   `json:"projection_version"`
	Direction         *WindowCursorDirection    `json:"direction"`
	AnchorTurnID      *string                   `json:"anchor_turn_id"`
	Position          *cursorPositionDecodeWire `json:"position"`
	ItemVersion       *uint64                   `json:"item_version"`
	EmptyRangeDigest  *string                   `json:"empty_range_digest"`
	ResumeWindow      *appwire.ThreadTurnWindow `json:"resume_window"`
}

// EncodeWindowCursor creates a continuation with an explicit exclusive-bound
// direction. It is a separate form from the legacy before-only paging cursor.
func EncodeWindowCursor(identity CursorIdentity, direction WindowCursorDirection, cursor WindowCursor) (string, error) {
	if !validIdentity(identity) || !validWindowCursorDirection(direction) || !validWindowCursorResume(direction, cursor.ResumeWindow) || !validWindowCursorProof(cursor) {
		return "", appwire.TranscriptItemCursorStale()
	}
	wire := windowCursorWire{
		Version:           cursorFormVersion,
		Kind:              windowCursorKind,
		ThreadRef:         identity.ThreadRef,
		Incarnation:       identity.Incarnation,
		ProjectionVersion: identity.ProjectionVersion,
		Direction:         direction,
		ResumeWindow:      cursor.ResumeWindow,
	}
	if cursor.AnchorTurnID != "" {
		wire.AnchorTurnID = &cursor.AnchorTurnID
		wire.Position = &cursor.Position
		wire.ItemVersion = &cursor.ItemVersion
	} else {
		wire.EmptyRangeDigest = &cursor.EmptyRangeDigest
	}
	return encodeCursorForm(wire)
}

// DecodeWindowCursor validates the complete identity fence and requires the
// direction expected by the caller, so a forward continuation cannot be spent
// as a backward continuation or vice versa.
func DecodeWindowCursor(encoded string, want CursorIdentity, wantDirection WindowCursorDirection) (WindowCursor, error) {
	var wire windowCursorDecodeWire
	if err := decodeCursorForm(encoded, &wire); err != nil || !validIdentity(want) || !validWindowCursorDirection(wantDirection) || !validWindowCursorWire(wire, want, wantDirection) {
		return WindowCursor{}, appwire.TranscriptItemCursorStale()
	}
	return windowCursorFromWire(wire), nil
}

// DecodeWindowCursorForRef validates the self-contained continuation shape
// before the caller resolves its stable resume selector. The full view and
// direction fences remain mandatory when DecodeWindowCursor spends it.
func DecodeWindowCursorForRef(encoded, threadRef string) (WindowCursor, CursorIdentity, WindowCursorDirection, error) {
	var wire windowCursorDecodeWire
	if err := decodeCursorForm(encoded, &wire); err != nil || !validWindowCursorShape(wire) {
		return WindowCursor{}, CursorIdentity{}, "", appwire.TranscriptItemCursorStale()
	}
	identity := CursorIdentity{
		ThreadRef:         *wire.ThreadRef,
		Incarnation:       *wire.Incarnation,
		ProjectionVersion: *wire.ProjectionVersion,
	}
	if identity.ThreadRef != threadRef {
		return WindowCursor{}, CursorIdentity{}, "", appwire.TranscriptItemCursorStale()
	}
	return windowCursorFromWire(wire), identity, *wire.Direction, nil
}

// TranslateWindowCursorRef replaces one hub's thread namespace with another's
// while preserving the source view, direction and boundary. Strict decoding
// prevents a remote peer from smuggling an unknown cursor form through the
// controller under its namespace.
func TranslateWindowCursorRef(encoded, fromThreadRef, toThreadRef string) (string, error) {
	cursor, identity, direction, err := DecodeWindowCursorForRef(encoded, fromThreadRef)
	if err != nil || toThreadRef == "" {
		return "", appwire.TranscriptItemCursorStale()
	}
	identity.ThreadRef = toThreadRef
	return EncodeWindowCursor(identity, direction, cursor)
}

func windowCursorFromWire(wire windowCursorDecodeWire) WindowCursor {
	cursor := WindowCursor{ResumeWindow: wire.ResumeWindow}
	if wire.AnchorTurnID != nil {
		cursor.AnchorTurnID = *wire.AnchorTurnID
		cursor.Position = wire.Position.position()
		cursor.ItemVersion = *wire.ItemVersion
	} else {
		cursor.EmptyRangeDigest = *wire.EmptyRangeDigest
	}
	return cursor
}

func validWindowCursorWire(wire windowCursorDecodeWire, want CursorIdentity, wantDirection WindowCursorDirection) bool {
	return validWindowCursorShape(wire) &&
		cursorIdentityMatches(wire.ThreadRef, wire.Incarnation, wire.ProjectionVersion, want) &&
		wire.Direction != nil && *wire.Direction == wantDirection
}

func cursorIdentityMatches(threadRef, incarnation *string, projectionVersion *uint16, want CursorIdentity) bool {
	return threadRef != nil && *threadRef == want.ThreadRef &&
		incarnation != nil && *incarnation == want.Incarnation &&
		projectionVersion != nil && *projectionVersion == want.ProjectionVersion
}

func validWindowCursorShape(wire windowCursorDecodeWire) bool {
	if wire.ThreadRef == nil || wire.Incarnation == nil || wire.ProjectionVersion == nil {
		return false
	}
	return wire.Version != nil && *wire.Version == cursorFormVersion &&
		wire.Kind != nil && *wire.Kind == windowCursorKind &&
		validIdentity(CursorIdentity{ThreadRef: *wire.ThreadRef, Incarnation: *wire.Incarnation, ProjectionVersion: *wire.ProjectionVersion}) &&
		wire.Direction != nil && validWindowCursorDirection(*wire.Direction) &&
		validWindowCursorProofWire(wire) &&
		validWindowCursorResume(*wire.Direction, wire.ResumeWindow)
}

func validWindowCursorProof(cursor WindowCursor) bool {
	hasAnchor := cursor.AnchorTurnID != ""
	hasDigest := cursor.EmptyRangeDigest != ""
	if hasAnchor == hasDigest {
		return false
	}
	if hasAnchor {
		return cursor.ItemVersion != 0
	}
	return cursor.Position == (appwire.ThreadItemPosition{}) && cursor.ItemVersion == 0 && validEmptyRangeDigest(cursor.EmptyRangeDigest)
}

func validWindowCursorProofWire(wire windowCursorDecodeWire) bool {
	hasAnchor := wire.AnchorTurnID != nil
	hasDigest := wire.EmptyRangeDigest != nil
	if hasAnchor == hasDigest {
		return false
	}
	if hasAnchor {
		return *wire.AnchorTurnID != "" && validCursorPosition(wire.Position) && wire.ItemVersion != nil && *wire.ItemVersion != 0
	}
	return wire.Position == nil && wire.ItemVersion == nil && validEmptyRangeDigest(*wire.EmptyRangeDigest)
}

func validEmptyRangeDigest(digest string) bool {
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == digest
}

func validWindowCursorResume(direction WindowCursorDirection, window *appwire.ThreadTurnWindow) bool {
	if window == nil || window.Latest != 0 || appwire.ValidateThreadWindowParams(appwire.ThreadWindowParams{TurnWindow: window}, 0) != nil {
		return false
	}
	if direction == WindowCursorDirectionBackward {
		return window.Before != nil
	}
	return direction == WindowCursorDirectionForward && (window.Since != "" || window.Turn != "")
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

// validFetchToken reports whether token is a well-formed non-reused retained
// entry token: a store-scoped random prefix, a dash, and a hexadecimal counter.
func validFetchToken(token string) bool {
	if len(token) < 18 || len(token) > 64 || token[16] != '-' {
		return false
	}
	for index, char := range token {
		if index == 16 {
			continue
		}
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
