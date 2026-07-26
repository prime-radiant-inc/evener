package main

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
)

// TestDecodeTranscriptTurnLosesNoField holds the hub's reload decode to the
// turn the daemon wrote: the same transcript bytes must produce the same
// schema.Turn on both sides.
//
// The fixture is built by walking schema.Turn with reflection rather than by
// hand, which is the whole point. A hand-listed fixture only covers the fields
// its author knew about, so a field added to schema.Turn tomorrow arrives
// untested and can be dropped in silence — the failure mode that cost three
// separate changes extra work (katas 3bcx, mcgh, qm9y) and that kata kq8c
// exists to end. Reflection enumerates the type itself, so a new field is under
// test the moment it is declared, with no seed to remember and nothing to
// update here.
func TestDecodeTranscriptTurnLosesNoField(t *testing.T) {
	raw := populatedTurnEntryJSON(t)

	var want transcript.Entry
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("decode entry into the daemon's own type: %v", err)
	}

	got, ok := decodeTranscriptTurn(raw)
	if !ok {
		t.Fatalf("hub decode rejected the entry the daemon accepted:\n%s", raw)
	}
	if !reflect.DeepEqual(got, want.Turn) {
		for _, name := range divergentTurnFields(got, want.Turn) {
			t.Errorf("hub reload decode lost or altered schema.Turn.%s", name)
		}
		t.Fatalf("hub reload decode diverged from the daemon's:\n hub   =%+v\n daemon=%+v\n entry=%s", got, want.Turn, raw)
	}
}

// hubDecodedTurn writes a turn as the daemon persists it and reads it back the
// way the hub's reload path does, so a test asserting on the result is
// asserting on what a returning reader actually gets.
func hubDecodedTurn(t *testing.T, persisted schema.Turn) schema.Turn {
	t.Helper()
	raw, err := json.Marshal(transcript.Entry{Kind: "entry", Seq: 1, Turn: persisted})
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	turn, ok := decodeTranscriptTurn(raw)
	if !ok {
		t.Fatalf("hub decode rejected the entry:\n%s", raw)
	}
	return turn
}

// divergentTurnFields names the top-level schema.Turn fields on which two
// decodes of the same bytes disagree, so a failure points at the field rather
// than at two large structs.
func divergentTurnFields(got, want schema.Turn) []string {
	var names []string
	gotV, wantV := reflect.ValueOf(got), reflect.ValueOf(want)
	for i := range gotV.NumField() {
		if !reflect.DeepEqual(gotV.Field(i).Interface(), wantV.Field(i).Interface()) {
			names = append(names, gotV.Type().Field(i).Name)
		}
	}
	return names
}

// populatedTurnEntryJSON marshals a transcript entry whose turn has every
// JSON-visible field set to a distinctive non-zero value, so a decode that
// drops one leaves an observable zero.
func populatedTurnEntryJSON(t *testing.T) json.RawMessage {
	t.Helper()
	turn := schema.Turn{}
	fillForJSON(t, reflect.ValueOf(&turn).Elem(), 0)
	raw, err := json.Marshal(transcript.Entry{Kind: "entry", Seq: 1, Turn: turn})
	if err != nil {
		t.Fatalf("marshal populated entry: %v", err)
	}
	return raw
}

// fillForJSON sets every exported, JSON-encoded field reachable from v to a
// non-zero value. Values are chosen to survive a JSON round trip unchanged, so
// that any difference between two decodes of the output is attributable to the
// decoding path and not to the fixture.
func fillForJSON(t *testing.T, v reflect.Value, depth int) {
	t.Helper()
	if depth > 8 {
		t.Fatalf("fillForJSON: schema.Turn nests deeper than expected at %s; the fixture builder needs revisiting", v.Type())
	}
	switch v.Type() {
	case reflect.TypeFor[time.Time]():
		// Whole seconds in UTC: the transcript's RFC 3339 encoding is exact for
		// these, so the fixture cannot fail on formatting precision.
		v.Set(reflect.ValueOf(time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)))
		return
	case reflect.TypeFor[json.RawMessage]():
		v.Set(reflect.ValueOf(json.RawMessage(`{"fixture":"raw"}`)))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString("fixture-" + v.Type().Name())
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		// An integral float: JSON has one number type, so this decodes back
		// identically whether the field is float or is widened through `any`.
		v.SetFloat(7)
	case reflect.Interface:
		// `any` payloads (a tool result's content) decode back as strings.
		v.Set(reflect.ValueOf("fixture-any"))
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillForJSON(t, v.Elem(), depth+1)
	case reflect.Slice:
		// []byte encodes as base64 and must stay valid on the way back.
		if v.Type().Elem().Kind() == reflect.Uint8 {
			v.SetBytes([]byte("fixture-bytes"))
			return
		}
		elem := reflect.New(v.Type().Elem()).Elem()
		fillForJSON(t, elem, depth+1)
		v.Set(reflect.Append(v, elem))
	case reflect.Map:
		key := reflect.New(v.Type().Key()).Elem()
		fillForJSON(t, key, depth+1)
		val := reflect.New(v.Type().Elem()).Elem()
		fillForJSON(t, val, depth+1)
		v.Set(reflect.MakeMap(v.Type()))
		v.SetMapIndex(key, val)
	case reflect.Struct:
		for i := range v.NumField() {
			field := v.Type().Field(i)
			if !field.IsExported() || field.Tag.Get("json") == "-" {
				continue
			}
			fillForJSON(t, v.Field(i), depth+1)
		}
	default:
		t.Fatalf("fillForJSON: no fixture value for %s (kind %s); extend the builder", v.Type(), v.Kind())
	}
}
