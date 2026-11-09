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

	for _, test := range []struct {
		direction WindowCursorDirection
		resume    *appwire.ThreadTurnWindow
	}{
		{direction: WindowCursorDirectionBackward, resume: &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-c", Limit: 2}}},
		{direction: WindowCursorDirectionForward, resume: &appwire.ThreadTurnWindow{Since: "turn-a", Limit: 2}},
	} {
		t.Run(string(test.direction), func(t *testing.T) {
			want := WindowCursor{Position: position, ItemVersion: 7, ResumeWindow: test.resume}
			encoded, err := EncodeWindowCursor(identity, test.direction, want)
			if err != nil {
				t.Fatal(err)
			}
			if encoded == "" || strings.Contains(encoded, "=") || strings.ContainsAny(encoded, "+/") {
				t.Fatalf("window cursor is not raw URL-safe base64: %q", encoded)
			}
			got, err := DecodeWindowCursor(encoded, identity, test.direction)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("decoded window cursor = %+v, want %+v", got, want)
			}
			byRef, gotIdentity, gotDirection, err := DecodeWindowCursorForRef(encoded, identity.ThreadRef)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(byRef, want) || gotIdentity != identity || gotDirection != test.direction {
				t.Fatalf("ref-scoped decode = (%+v, %+v, %q), want (%+v, %+v, %q)", byRef, gotIdentity, gotDirection, want, identity, test.direction)
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
	want := WindowCursor{
		Position: position, ItemVersion: 7,
		ResumeWindow: &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-c", Limit: 2}},
	}
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
}

func TestWindowCursorRejectsDirectionConfusion(t *testing.T) {
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "messages-view", ProjectionVersion: TranscriptItemProjectionVersion}
	backwardCursor := WindowCursor{
		Position: appwire.ThreadItemPosition{Entry: 23, Item: 6}, ItemVersion: 7,
		ResumeWindow: &appwire.ThreadTurnWindow{Before: &appwire.ThreadTurnBoundary{TurnID: "turn-c", Limit: 2}},
	}
	forwardCursor := WindowCursor{
		Position: appwire.ThreadItemPosition{Entry: 23, Item: 6}, ItemVersion: 7,
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
		Position: appwire.ThreadItemPosition{Entry: 23, Item: 6}, ItemVersion: 7,
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
		Position: appwire.ThreadItemPosition{Entry: 23, Item: 6}, ItemVersion: 7,
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

	validRaw := `{"version":1,"kind":"window","thread_ref":"local:thread","incarnation":"messages-view","projection_version":2,"direction":"forward","position":{"entry":23,"item":6},"item_version":7,"resume_window":{"since":"turn-a","limit":2}}`
	for name, raw := range map[string]string{
		"missing codec version":  strings.Replace(validRaw, `"version":1,`, "", 1),
		"wrong codec version":    strings.Replace(validRaw, `"version":1`, `"version":2`, 1),
		"missing direction":      strings.Replace(validRaw, `,"direction":"forward"`, "", 1),
		"unknown direction":      strings.Replace(validRaw, `"direction":"forward"`, `"direction":"sideways"`, 1),
		"missing position":       strings.Replace(validRaw, `,"position":{"entry":23,"item":6}`, "", 1),
		"missing item version":   strings.Replace(validRaw, `,"item_version":7`, "", 1),
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
		Position: appwire.ThreadItemPosition{Entry: 23}, ItemVersion: 7,
		ResumeWindow: &appwire.ThreadTurnWindow{Turn: "turn-a"},
	}); err == nil {
		t.Fatalf("invalid direction encoded as %q", encoded)
	} else {
		assertStaleCursorError(t, err)
	}

	for name, cursor := range map[string]WindowCursor{
		"missing resume window": {Position: appwire.ThreadItemPosition{Entry: 23}, ItemVersion: 7},
		"moving latest resume": {
			Position: appwire.ThreadItemPosition{Entry: 23}, ItemVersion: 7,
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
