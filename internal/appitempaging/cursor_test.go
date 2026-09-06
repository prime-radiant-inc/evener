package appitempaging

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestCursorRoundTripAndAppendStability(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "incarnation-1", ProjectionVersion: 1}
	before := appwire.ThreadItemPosition{Entry: 10, Item: 4}
	encoded, err := EncodeCursor(identity, before)
	if err != nil {
		t.Fatal(err)
	}
	if encoded == "" || strings.Contains(encoded, "=") || strings.ContainsAny(encoded, "+/") {
		t.Fatalf("cursor is not raw URL-safe base64: %q", encoded)
	}
	got, err := DecodeCursor(encoded, identity)
	if err != nil {
		t.Fatal(err)
	}
	if got != before {
		t.Fatalf("decoded position = %+v, want %+v", got, before)
	}
	// Appending later positions does not change the identity or meaning of an
	// existing cursor.
	candidates := makeCandidates(5, "turn_1")
	appended := append(append([]TranscriptItemCandidate(nil), candidates...), makeCandidates(1, "turn_1")[0])
	appended[len(appended)-1].Position = appwire.ThreadItemPosition{Entry: 10, Item: 5}
	appended[len(appended)-1].Item.TranscriptKey = "key-5"
	appended[len(appended)-1].Item.Position = &appended[len(appended)-1].Position
	if got, err := DecodeCursor(encoded, identity); err != nil || got != before {
		t.Fatalf("cursor changed after append: %+v, %v", got, err)
	}
	selectedBefore, _, err := SelectCandidates(candidates, &got, 40)
	if err != nil {
		t.Fatal(err)
	}
	selectedAfter, _, err := SelectCandidates(appended, &got, 40)
	if err != nil {
		t.Fatal(err)
	}
	if len(selectedBefore) != len(selectedAfter) || selectedBefore[0].Item.TranscriptKey != selectedAfter[0].Item.TranscriptKey {
		t.Fatalf("append changed cursor page: before=%+v after=%+v", selectedBefore, selectedAfter)
	}
}

func TestCursorUsesCanonicalJSONFieldNames(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "incarnation-1", ProjectionVersion: 1}
	canonical := `{"version":1,"thread_ref":"local:thread","incarnation":"incarnation-1","projection_version":1,"before":{"entry":2,"item":1}}`
	encoded := base64.RawURLEncoding.EncodeToString([]byte(canonical))
	if _, err := DecodeCursor(encoded, identity); err != nil {
		t.Fatalf("canonical cursor rejected: %v", err)
	}

	camelCase := strings.NewReplacer("thread_ref", "threadRef", "projection_version", "projectionVersion").Replace(canonical)
	encoded = base64.RawURLEncoding.EncodeToString([]byte(camelCase))
	if _, err := DecodeCursor(encoded, identity); err == nil {
		t.Fatal("camelCase cursor accepted")
	}
}

func TestCursorRejectsFutureAndUnreconstructibleBoundaries(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "incarnation-1", ProjectionVersion: 1}
	candidates := makeCandidates(3, "turn_1")
	for name, position := range map[string]appwire.ThreadItemPosition{
		"future":              {Entry: 11, Item: 0},
		"before source":       {Entry: 9, Item: 0},
		"missing intra-entry": {Entry: 10, Item: 9},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := EncodeCursor(identity, position)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeCursor(encoded, identity)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = SelectCandidates(candidates, &decoded, 40)
			if err == nil {
				t.Fatal("invalid boundary accepted")
			}
			assertStaleCursorError(t, err)
			if strings.Contains(err.Error(), encoded) {
				t.Fatalf("error echoed cursor token")
			}
		})
	}
}

func TestCursorRejectsStaleMalformedAndUnknownValues(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "incarnation-1", ProjectionVersion: 1}
	encoded, err := EncodeCursor(identity, appwire.ThreadItemPosition{Entry: 2, Item: 1})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"wrong thread":      encoded,
		"wrong incarnation": encoded,
		"wrong projection":  encoded,
		"malformed":         "not-base64",
		"empty":             "",
	} {
		wantIdentity := identity
		switch name {
		case "wrong thread":
			wantIdentity.ThreadRef = "local:other"
		case "wrong incarnation":
			wantIdentity.Incarnation = "incarnation-2"
		case "wrong projection":
			wantIdentity.ProjectionVersion = 2
		}
		t.Run(name, func(t *testing.T) {
			_, err := DecodeCursor(want, wantIdentity)
			if err == nil {
				t.Fatal("stale cursor accepted")
			}
			assertStaleCursorError(t, err)
		})
	}

	raw := transcriptItemCursorV1{
		Version:           CursorVersion,
		ThreadRef:         identity.ThreadRef,
		Incarnation:       identity.Incarnation,
		ProjectionVersion: identity.ProjectionVersion,
		Before:            appwire.ThreadItemPosition{Entry: 2, Item: 1},
	}
	for name, suffix := range map[string]string{
		"unknown field": `,"unknown":"value"`,
		"second value":  ` {"version":1}`,
	} {
		t.Run(name, func(t *testing.T) {
			body, err := json.Marshal(raw)
			if err != nil {
				t.Fatal(err)
			}
			if name == "unknown field" {
				body = append(body[:len(body)-1], []byte(suffix+"}")...)
			} else {
				body = append(body, suffix...)
			}
			encoded := base64.RawURLEncoding.EncodeToString(body)
			_, err = DecodeCursor(encoded, identity)
			if err == nil {
				t.Fatal("invalid cursor accepted")
			}
			assertStaleCursorError(t, err)
			if strings.Contains(err.Error(), encoded) {
				t.Fatalf("error echoed cursor token")
			}
		})
	}
}

func TestCursorEncodeRejectsIdentitiesItCannotRoundTrip(t *testing.T) {
	valid := CursorIdentity{ThreadRef: "local:thread", Incarnation: "incarnation-1", ProjectionVersion: 1}
	for name, identity := range map[string]CursorIdentity{
		"invalid UTF-8 thread":      {ThreadRef: string([]byte{0xff}), Incarnation: valid.Incarnation, ProjectionVersion: valid.ProjectionVersion},
		"invalid UTF-8 incarnation": {ThreadRef: valid.ThreadRef, Incarnation: string([]byte{0xff}), ProjectionVersion: valid.ProjectionVersion},
		"oversized thread":          {ThreadRef: strings.Repeat("x", 800_000), Incarnation: valid.Incarnation, ProjectionVersion: valid.ProjectionVersion},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := EncodeCursor(identity, appwire.ThreadItemPosition{Entry: 2, Item: 1})
			if err == nil {
				t.Fatalf("non-round-trippable identity encoded as %d bytes", len(encoded))
			}
			if encoded != "" {
				t.Fatalf("rejected identity returned %d encoded bytes", len(encoded))
			}
			assertStaleCursorError(t, err)
		})
	}
}

func TestCursorRejectsMalformedWireUTF8BeforeJSONNormalization(t *testing.T) {
	const invalidBytes = 300_000
	body := append([]byte(`{"version":1,"thread_ref":"`), bytes.Repeat([]byte{0xff}, invalidBytes)...)
	body = append(body, []byte(`","incarnation":"inc-1","projection_version":1,"before":{"entry":2,"item":1}}`)...)
	encoded := base64.RawURLEncoding.EncodeToString(body)
	if len(encoded) > maxCursorEncodedBytes {
		t.Fatalf("malformed cursor seed is %d bytes, want at most %d", len(encoded), maxCursorEncodedBytes)
	}

	want := CursorIdentity{
		ThreadRef:         strings.Repeat("\ufffd", invalidBytes),
		Incarnation:       "inc-1",
		ProjectionVersion: 1,
	}
	if _, err := DecodeCursor(encoded, want); err == nil {
		t.Fatal("cursor with malformed wire UTF-8 accepted after JSON normalization")
	} else {
		assertStaleCursorError(t, err)
	}
}

func TestCursorRebasePreservesIdentity(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "codex:thread", Incarnation: "inc-7", ProjectionVersion: 1}
	oldBefore := appwire.ThreadItemPosition{Entry: 4, Item: 2}
	newBefore := appwire.ThreadItemPosition{Entry: 3, Item: 1}
	encoded, err := EncodeCursor(identity, oldBefore)
	if err != nil {
		t.Fatal(err)
	}
	rebased, err := RebaseCursor(encoded, newBefore)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeCursor(rebased, identity)
	if err != nil {
		t.Fatal(err)
	}
	if got != newBefore {
		t.Fatalf("rebased position = %+v, want %+v", got, newBefore)
	}
}

func assertStaleCursorError(t *testing.T, err error) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("error = %T, want appwire.WireError", err)
	}
	if wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("code = %d, want %d", wire.Code, appwire.CodeInvalidParams)
	}
	data, ok := wire.Data.(appwire.ErrorData)
	if !ok {
		t.Fatalf("data = %T, want appwire.ErrorData", wire.Data)
	}
	if data.EvenerErrorInfo != appwire.ErrorTranscriptItemCursorStale {
		t.Fatalf("error info = %q, want %q", data.EvenerErrorInfo, appwire.ErrorTranscriptItemCursorStale)
	}
	if data.RetryDisposition != appwire.RetryDispositionAutomatic {
		t.Fatalf("retry disposition = %q, want %q", data.RetryDisposition, appwire.RetryDispositionAutomatic)
	}
}
