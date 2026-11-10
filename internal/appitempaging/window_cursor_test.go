package appitempaging

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestWindowCursorRoundTripByDirection(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "messages-view", ProjectionVersion: TranscriptItemProjectionVersion}
	position := appwire.ThreadItemPosition{Entry: 23, Item: 6, Sub: 1}
	emptyRangeDigest := strings.Repeat("ab", 32)

	for _, test := range []struct {
		name      string
		direction WindowCursorDirection
		want      WindowCursor
	}{
		{
			name:      "backward item anchor",
			direction: WindowCursorDirectionBackward,
			want: WindowCursor{
				AnchorTurnID: "turn-c", Position: position, ItemVersion: 7,
				ResumeWindow: &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-c", Limit: 2}},
			},
		},
		{
			name:      "backward empty range",
			direction: WindowCursorDirectionBackward,
			want: WindowCursor{
				EmptyRangeDigest: emptyRangeDigest,
				ResumeWindow:     &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-c", Limit: 2}},
			},
		},
		{
			name:      "forward item anchor",
			direction: WindowCursorDirectionForward,
			want: WindowCursor{
				AnchorTurnID: "turn-a", Position: position, ItemVersion: 7,
				ResumeWindow: &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 2},
			},
		},
		{
			name:      "forward empty range",
			direction: WindowCursorDirectionForward,
			want: WindowCursor{
				EmptyRangeDigest: emptyRangeDigest,
				ResumeWindow:     &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 2},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := EncodeWindowCursor(identity, test.direction, test.want)
			if err != nil {
				t.Fatal(err)
			}
			if encoded == "" || strings.Contains(encoded, "=") || strings.ContainsAny(encoded, "+/") {
				t.Fatalf("window cursor is not raw URL-safe base64: %q", encoded)
			}
			if len(encoded) > maxCursorEncodedBytes {
				t.Fatalf("encoded window cursor length = %d, maximum %d", len(encoded), maxCursorEncodedBytes)
			}
			got, err := DecodeWindowCursor(encoded, identity, test.direction)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("decoded window cursor = %+v, want %+v", got, test.want)
			}
			byRef, gotIdentity, gotDirection, err := DecodeWindowCursorForRef(encoded, identity.ThreadRef)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(byRef, test.want) || gotIdentity != identity || gotDirection != test.direction {
				t.Fatalf("ref-scoped decode = (%+v, %+v, %q), want (%+v, %+v, %q)", byRef, gotIdentity, gotDirection, test.want, identity, test.direction)
			}
			if _, _, _, err := DecodeWindowCursorForRef(encoded, "local:other"); err == nil {
				t.Fatal("window cursor crossed thread refs during predecode")
			}
		})
	}
}

func TestWindowCursorTranslatesHubNamespace(t *testing.T) {
	remote := CursorIdentity{ThreadRef: "local:thread", Incarnation: "messages-view", ProjectionVersion: TranscriptItemProjectionVersion}
	position := appwire.ThreadItemPosition{Entry: 23, Item: 6, Sub: 1}

	for name, want := range map[string]WindowCursor{
		"item anchor": {
			AnchorTurnID: "turn-c", Position: position, ItemVersion: 7,
			ResumeWindow: &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-c", Limit: 2}},
		},
		"empty range": {
			EmptyRangeDigest: strings.Repeat("ab", 32),
			ResumeWindow:     &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-c", Limit: 2}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := EncodeWindowCursor(remote, WindowCursorDirectionBackward, want)
			if err != nil {
				t.Fatal(err)
			}
			translated, err := TranslateWindowCursorRef(encoded, remote.ThreadRef, "host-b:thread")
			if err != nil {
				t.Fatal(err)
			}
			controller := remote
			controller.ThreadRef = "host-b:thread"
			got, err := DecodeWindowCursor(translated, controller, WindowCursorDirectionBackward)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("translated window cursor = %+v, want %+v", got, want)
			}
			if _, err := DecodeWindowCursor(translated, remote, WindowCursorDirectionBackward); err == nil {
				t.Fatal("translated window cursor retained remote hub ownership")
			}
		})
	}
}

func TestWindowCursorRejectsDirectionConfusion(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "messages-view", ProjectionVersion: TranscriptItemProjectionVersion}
	backwardCursor := WindowCursor{
		AnchorTurnID: "turn-c", Position: appwire.ThreadItemPosition{Entry: 23, Item: 6}, ItemVersion: 7,
		ResumeWindow: &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-c", Limit: 2}},
	}
	forwardCursor := WindowCursor{
		AnchorTurnID: "turn-a", Position: appwire.ThreadItemPosition{Entry: 23, Item: 6}, ItemVersion: 7,
		ResumeWindow: &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 2},
	}
	backward, err := EncodeWindowCursor(identity, WindowCursorDirectionBackward, backwardCursor)
	if err != nil {
		t.Fatal(err)
	}
	forward, err := EncodeWindowCursor(identity, WindowCursorDirectionForward, forwardCursor)
	if err != nil {
		t.Fatal(err)
	}

	for name, test := range map[string]struct {
		encoded string
		want    WindowCursorDirection
	}{
		"backward as forward": {encoded: backward, want: WindowCursorDirectionForward},
		"forward as backward": {encoded: forward, want: WindowCursorDirectionBackward},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeWindowCursor(test.encoded, identity, test.want)
			if err == nil {
				t.Fatal("opposite-direction window cursor accepted")
			}
			assertStaleCursorError(t, err)
		})
	}
}

func TestWindowCursorRejectsDifferentWindowView(t *testing.T) {
	base := CursorIdentity{ThreadRef: "local:thread", Incarnation: "index-incarnation", ProjectionVersion: TranscriptItemProjectionVersion}
	messages := appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}, ItemTypes: []string{"messages"}}
	reasoning := appwire.ThreadWindowParams{TurnWindow: &appwire.ThreadTurnWindow{Latest: 2}, ItemTypes: []string{"reasoning"}}
	messagesIdentity := base
	messagesIdentity.Incarnation = messages.ViewIncarnation(base.Incarnation, 0)
	reasoningIdentity := base
	reasoningIdentity.Incarnation = reasoning.ViewIncarnation(base.Incarnation, 0)

	encoded, err := EncodeWindowCursor(messagesIdentity, WindowCursorDirectionBackward, WindowCursor{
		AnchorTurnID: "turn-c", Position: appwire.ThreadItemPosition{Entry: 23, Item: 6}, ItemVersion: 7,
		ResumeWindow: &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-c", Limit: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeWindowCursor(encoded, reasoningIdentity, WindowCursorDirectionBackward); err == nil {
		t.Fatal("window cursor crossed item-type views")
	} else {
		assertStaleCursorError(t, err)
	}
}

func TestWindowCursorRejectsLegacyMalformedIdentityAndDirection(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "messages-view", ProjectionVersion: TranscriptItemProjectionVersion}
	legacy, err := EncodeCursor(identity, appwire.ThreadItemPosition{Entry: 23, Item: 6})
	if err != nil {
		t.Fatal(err)
	}
	valid, err := EncodeWindowCursor(identity, WindowCursorDirectionForward, WindowCursor{
		AnchorTurnID: "turn-a", Position: appwire.ThreadItemPosition{Entry: 23, Item: 6}, ItemVersion: 7,
		ResumeWindow: &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 2},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		encoded   string
		identity  CursorIdentity
		direction WindowCursorDirection
	}{
		{name: "legacy paging cursor", encoded: legacy, identity: identity, direction: WindowCursorDirectionForward},
		{name: "malformed", encoded: "not-base64", identity: identity, direction: WindowCursorDirectionForward},
		{name: "wrong thread", encoded: valid, identity: CursorIdentity{ThreadRef: "local:other", Incarnation: "messages-view", ProjectionVersion: TranscriptItemProjectionVersion}, direction: WindowCursorDirectionForward},
		{name: "wrong view", encoded: valid, identity: CursorIdentity{ThreadRef: "local:thread", Incarnation: "other-view", ProjectionVersion: TranscriptItemProjectionVersion}, direction: WindowCursorDirectionForward},
		{name: "unknown expected direction", encoded: valid, identity: identity, direction: WindowCursorDirection("sideways")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeWindowCursor(tt.encoded, tt.identity, tt.direction)
			if err == nil {
				t.Fatal("invalid window cursor accepted")
			}
			assertStaleCursorError(t, err)
		})
	}

	validRaw := `{"version":1,"kind":"window","thread_ref":"local:thread","incarnation":"messages-view","projection_version":2,"direction":"forward","anchor_turn_id":"turn-a","position":{"entry":23,"item":6},"item_version":7,"resume_window":{"since":"turn-a","limit":2}}`
	digestRaw := `{"version":1,"kind":"window","thread_ref":"local:thread","incarnation":"messages-view","projection_version":2,"direction":"forward","empty_range_digest":"` + strings.Repeat("ab", 32) + `","resume_window":{"since":"turn-a","limit":2}}`
	for name, raw := range map[string]string{
		"missing codec version":  strings.Replace(validRaw, `"version":1,`, "", 1),
		"wrong codec version":    strings.Replace(validRaw, `"version":1`, `"version":2`, 1),
		"missing direction":      strings.Replace(validRaw, `,"direction":"forward"`, "", 1),
		"unknown direction":      strings.Replace(validRaw, `"direction":"forward"`, `"direction":"sideways"`, 1),
		"missing proof":          strings.Replace(strings.Replace(strings.Replace(validRaw, `,"anchor_turn_id":"turn-a"`, "", 1), `,"position":{"entry":23,"item":6}`, "", 1), `,"item_version":7`, "", 1),
		"both proofs":            strings.Replace(validRaw, `,"resume_window"`, `,"empty_range_digest":"`+strings.Repeat("ab", 32)+`","resume_window"`, 1),
		"anchor without turn ID": strings.Replace(validRaw, `,"anchor_turn_id":"turn-a"`, "", 1),
		"missing position":       strings.Replace(validRaw, `,"position":{"entry":23,"item":6}`, "", 1),
		"missing item version":   strings.Replace(validRaw, `,"item_version":7`, "", 1),
		"zero item version":      strings.Replace(validRaw, `"item_version":7`, `"item_version":0`, 1),
		"digest with position":   strings.Replace(validRaw, `"anchor_turn_id":"turn-a"`, `"empty_range_digest":"`+strings.Repeat("ab", 32)+`"`, 1),
		"short digest":           strings.Replace(digestRaw, strings.Repeat("ab", 32), strings.Repeat("ab", 31), 1),
		"non-hex digest":         strings.Replace(digestRaw, strings.Repeat("ab", 32), strings.Repeat("ag", 32), 1),
		"uppercase digest":       strings.Replace(digestRaw, strings.Repeat("ab", 32), strings.Repeat("AB", 32), 1),
		"missing resume window":  strings.Replace(validRaw, `,"resume_window":{"since":"turn-a","limit":2}`, "", 1),
		"moving latest resume":   strings.Replace(validRaw, `{"since":"turn-a","limit":2}`, `{"latest":2}`, 1),
		"wrong resume direction": strings.Replace(validRaw, `{"since":"turn-a","limit":2}`, `{"before":{"turnId":"turn-c","limit":2}}`, 1),
		"fetch kind":             strings.Replace(validRaw, `"kind":"window"`, `"kind":"fetch"`, 1),
		"malformed JSON":         `{`,
	} {
		t.Run(name, func(t *testing.T) {
			encoded := base64.RawURLEncoding.EncodeToString([]byte(raw))
			_, err := DecodeWindowCursor(encoded, identity, WindowCursorDirectionForward)
			if err == nil {
				t.Fatal("invalid window cursor accepted")
			}
			assertStaleCursorError(t, err)
		})
	}

	if encoded, err := EncodeWindowCursor(identity, WindowCursorDirection("sideways"), WindowCursor{
		AnchorTurnID: "turn-a", Position: appwire.ThreadItemPosition{Entry: 23}, ItemVersion: 7,
		ResumeWindow: &appwire.ThreadTurnWindow{Turn: "turn-a"},
	}); err == nil {
		t.Fatalf("invalid direction encoded as %q", encoded)
	} else {
		assertStaleCursorError(t, err)
	}

	for name, cursor := range map[string]WindowCursor{
		"missing proof": {},
		"both proofs": {
			AnchorTurnID: "turn-a", Position: appwire.ThreadItemPosition{Entry: 23}, ItemVersion: 7,
			EmptyRangeDigest: strings.Repeat("ab", 32),
			ResumeWindow:     &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 2},
		},
		"anchor without turn ID": {
			Position: appwire.ThreadItemPosition{Entry: 23}, ItemVersion: 7,
			ResumeWindow: &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 2},
		},
		"anchor with zero version": {
			AnchorTurnID: "turn-a", Position: appwire.ThreadItemPosition{Entry: 23},
			ResumeWindow: &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 2},
		},
		"digest with anchor position": {
			Position: appwire.ThreadItemPosition{Entry: 23}, ItemVersion: 7,
			EmptyRangeDigest: strings.Repeat("ab", 32),
			ResumeWindow:     &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 2},
		},
		"short digest": {
			EmptyRangeDigest: strings.Repeat("ab", 31),
			ResumeWindow:     &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 2},
		},
		"non-hex digest": {
			EmptyRangeDigest: strings.Repeat("ag", 32),
			ResumeWindow:     &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 2},
		},
		"uppercase digest": {
			EmptyRangeDigest: strings.Repeat("AB", 32),
			ResumeWindow:     &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 2},
		},
		"missing resume window": {
			AnchorTurnID: "turn-a", Position: appwire.ThreadItemPosition{Entry: 23}, ItemVersion: 7,
		},
		"moving latest resume": {
			AnchorTurnID: "turn-a", Position: appwire.ThreadItemPosition{Entry: 23}, ItemVersion: 7,
			ResumeWindow: &appwire.ThreadTurnWindow{Latest: 2},
		},
	} {
		t.Run(name+" encode", func(t *testing.T) {
			if encoded, err := EncodeWindowCursor(identity, WindowCursorDirectionForward, cursor); err == nil {
				t.Fatalf("invalid resume selector encoded as %q", encoded)
			} else {
				assertStaleCursorError(t, err)
			}
		})
	}
}
