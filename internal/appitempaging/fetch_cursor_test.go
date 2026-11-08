package appitempaging

import (
	"encoding/base64"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestFetchCursorRoundTrip(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "view-7", ProjectionVersion: 3}
	position := appwire.ThreadItemPosition{Entry: 17, Item: 4, Sub: 2}
	origins := []FetchCursorOrigin{
		FetchCursorOriginServingDaemon,
		FetchCursorOriginHubCompat,
		FetchCursorOriginSavedIndex,
		FetchCursorOriginRemoteSource,
	}

	for _, origin := range origins {
		t.Run(string(origin), func(t *testing.T) {
			want := FetchCursor{Position: position, ItemVersion: 11, Origin: origin, Offset: 4097}
			encoded, err := EncodeFetchCursor(identity, want)
			if err != nil {
				t.Fatal(err)
			}
			if encoded == "" || strings.Contains(encoded, "=") || strings.ContainsAny(encoded, "+/") {
				t.Fatalf("fetch cursor is not raw URL-safe base64: %q", encoded)
			}
			got, err := DecodeFetchCursor(encoded, identity)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("decoded fetch cursor = %+v, want %+v", got, want)
			}
		})
	}
}

func TestFetchCursorDecodesIdentityFromFetchOnlyRequest(t *testing.T) {
	wantIdentity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "window-view", ProjectionVersion: TranscriptItemProjectionVersion}
	wantCursor := FetchCursor{Position: appwire.ThreadItemPosition{Entry: 9, Item: 2}, ItemVersion: 7, Origin: FetchCursorOriginSavedIndex, Offset: 41}
	encoded, err := EncodeFetchCursor(wantIdentity, wantCursor)
	if err != nil {
		t.Fatal(err)
	}
	gotCursor, gotIdentity, err := DecodeFetchCursorForRef(encoded, wantIdentity.ThreadRef)
	if err != nil {
		t.Fatal(err)
	}
	if gotCursor != wantCursor || gotIdentity != wantIdentity {
		t.Fatalf("decoded = %+v, %+v; want %+v, %+v", gotCursor, gotIdentity, wantCursor, wantIdentity)
	}
	if _, _, err := DecodeFetchCursorForRef(encoded, "local:other"); err == nil {
		t.Fatal("fetch cursor accepted for another request ref")
	}
}

func TestFetchCursorRejectsLegacyMalformedAndWrongIdentity(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "view-7", ProjectionVersion: 3}
	legacy, err := EncodeCursor(identity, appwire.ThreadItemPosition{Entry: 17, Item: 4})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := EncodeFetchCursor(identity, FetchCursor{
		Position:    appwire.ThreadItemPosition{Entry: 17, Item: 4},
		ItemVersion: 11,
		Origin:      FetchCursorOriginServingDaemon,
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		encoded  string
		identity CursorIdentity
	}{
		{name: "legacy paging cursor", encoded: legacy, identity: identity},
		{name: "malformed", encoded: "not-base64", identity: identity},
		{name: "empty", encoded: "", identity: identity},
		{name: "wrong thread", encoded: valid, identity: CursorIdentity{ThreadRef: "local:other", Incarnation: "view-7", ProjectionVersion: 3}},
		{name: "wrong view", encoded: valid, identity: CursorIdentity{ThreadRef: "local:thread", Incarnation: "view-8", ProjectionVersion: 3}},
		{name: "wrong projection", encoded: valid, identity: CursorIdentity{ThreadRef: "local:thread", Incarnation: "view-7", ProjectionVersion: 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeFetchCursor(tt.encoded, tt.identity)
			if err == nil {
				t.Fatal("invalid fetch cursor accepted")
			}
			assertStaleCursorError(t, err)
		})
	}
}

func TestFetchCursorRequiresStrictOriginVersionAndOffset(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "view-7", ProjectionVersion: 3}
	valid := `{"version":1,"kind":"fetch","thread_ref":"local:thread","incarnation":"view-7","projection_version":3,"position":{"entry":17,"item":4},"item_version":11,"origin":"serving-daemon","offset":0}`
	tests := map[string]string{
		"missing codec version":    strings.Replace(valid, `"version":1,`, "", 1),
		"zero codec version":       strings.Replace(valid, `"version":1`, `"version":0`, 1),
		"fractional codec version": strings.Replace(valid, `"version":1`, `"version":1.5`, 1),
		"missing origin":           strings.Replace(valid, `,"origin":"serving-daemon"`, "", 1),
		"unknown origin":           strings.Replace(valid, `"origin":"serving-daemon"`, `"origin":"other"`, 1),
		"missing item version":     strings.Replace(valid, `,"item_version":11`, "", 1),
		"zero item version":        strings.Replace(valid, `"item_version":11`, `"item_version":0`, 1),
		"negative item version":    strings.Replace(valid, `"item_version":11`, `"item_version":-1`, 1),
		"fractional version":       strings.Replace(valid, `"item_version":11`, `"item_version":1.5`, 1),
		"string item version":      strings.Replace(valid, `"item_version":11`, `"item_version":"11"`, 1),
		"missing offset":           strings.Replace(valid, `,"offset":0`, "", 1),
		"negative offset":          strings.Replace(valid, `"offset":0`, `"offset":-1`, 1),
		"fractional offset":        strings.Replace(valid, `"offset":0`, `"offset":1.5`, 1),
		"string offset":            strings.Replace(valid, `"offset":0`, `"offset":"1"`, 1),
		"wrong form kind":          strings.Replace(valid, `"kind":"fetch"`, `"kind":"window"`, 1),
		"missing position":         strings.Replace(valid, `,"position":{"entry":17,"item":4}`, "", 1),
		"unknown field":            strings.TrimSuffix(valid, `}`) + `,"extra":true}`,
		"malformed JSON":           `{`,
	}

	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			encoded := base64.RawURLEncoding.EncodeToString([]byte(raw))
			_, err := DecodeFetchCursor(encoded, identity)
			if err == nil {
				t.Fatal("invalid fetch cursor accepted")
			}
			assertStaleCursorError(t, err)
		})
	}

	for name, cursor := range map[string]FetchCursor{
		"missing origin":    {Position: appwire.ThreadItemPosition{Entry: 17}, ItemVersion: 11},
		"unknown origin":    {Position: appwire.ThreadItemPosition{Entry: 17}, ItemVersion: 11, Origin: FetchCursorOrigin("other")},
		"zero item version": {Position: appwire.ThreadItemPosition{Entry: 17}, Origin: FetchCursorOriginSavedIndex},
	} {
		t.Run("encode "+name, func(t *testing.T) {
			encoded, err := EncodeFetchCursor(identity, cursor)
			if err == nil {
				t.Fatal("invalid fetch cursor encoded")
			}
			if encoded != "" {
				t.Fatalf("rejected cursor returned %d encoded bytes", len(encoded))
			}
			assertStaleCursorError(t, err)
		})
	}
}

func TestLegacyCursorEncodingRemainsStable(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "inc-1", ProjectionVersion: 1}
	encoded, err := EncodeCursor(identity, appwire.ThreadItemPosition{Entry: 2, Item: 1})
	if err != nil {
		t.Fatal(err)
	}
	const want = "eyJ2ZXJzaW9uIjoxLCJ0aHJlYWRfcmVmIjoibG9jYWw6dGhyZWFkIiwiaW5jYXJuYXRpb24iOiJpbmMtMSIsInByb2plY3Rpb25fdmVyc2lvbiI6MSwiYmVmb3JlIjp7ImVudHJ5IjoyLCJpdGVtIjoxfX0"
	if encoded != want {
		t.Fatalf("legacy cursor encoding changed: got %q, want %q", encoded, want)
	}
}
