package appitempaging

import (
	"encoding/base64"
	"math"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// FuzzCursorCodec drives both arbitrary opaque tokens and valid cursor values
// through the public codec. It checks the decode/encode fixed point, identity
// fencing, canonical URL-safe output, rebase semantics, and stable wire errors.
// Registry: native:.:./internal/appitempaging:FuzzCursorCodec
func FuzzCursorCodec(f *testing.F) {
	const canonical = "eyJ2ZXJzaW9uIjoxLCJ0aHJlYWRfcmVmIjoibG9jYWw6dGhyZWFkIiwiaW5jYXJuYXRpb24iOiJpbmMtMSIsInByb2plY3Rpb25fdmVyc2lvbiI6MSwiYmVmb3JlIjp7ImVudHJ5IjoyLCJpdGVtIjoxfX0"
	malformedUTF8 := append([]byte(`{"version":1,"thread_ref":"`), 0xff)
	malformedUTF8 = append(malformedUTF8, []byte(`","incarnation":"inc-1","projection_version":1,"before":{"entry":2,"item":1}}`)...)
	f.Add("not-a-cursor", "local:thread", "inc-1", uint16(1), uint64(2), uint32(1))
	f.Add("t", "local:thread", "inc-1", uint16(1), uint64(2), uint32(1))
	f.Add(canonical, "local:thread", "inc-1", uint16(1), uint64(7), uint32(3))
	f.Add(base64.RawURLEncoding.EncodeToString(malformedUTF8), "\ufffd", "inc-1", uint16(1), uint64(2), uint32(1))
	f.Add("", "", "", uint16(0), uint64(math.MaxUint64), uint32(math.MaxUint32))
	f.Add("", string([]byte{0xff}), "inc-1", uint16(1), uint64(2), uint32(1))
	f.Add("", "local:thread", string([]byte{0xff}), uint16(1), uint64(2), uint32(1))
	f.Add("", strings.Repeat("x", 800_000), "inc-1", uint16(1), uint64(2), uint32(1))

	f.Fuzz(func(t *testing.T, encoded, threadRef, incarnation string, projectionVersion uint16, entry uint64, item uint32) {
		identity := CursorIdentity{
			ThreadRef:         threadRef,
			Incarnation:       incarnation,
			ProjectionVersion: projectionVersion,
		}
		before := appwire.ThreadItemPosition{Entry: entry, Item: item}

		canonicalEncoded, err := EncodeCursor(identity, before)
		if err != nil {
			if canonicalEncoded != "" {
				t.Fatalf("rejected identity returned %d encoded bytes", len(canonicalEncoded))
			}
			assertStaleCursorError(t, err)
		} else {
			if canonicalEncoded == "" || strings.Contains(canonicalEncoded, "=") || strings.ContainsAny(canonicalEncoded, "+/") {
				t.Fatalf("cursor is not nonempty raw URL-safe base64: %q", canonicalEncoded)
			}
			decoded, err := DecodeCursor(canonicalEncoded, identity)
			if err != nil {
				t.Fatalf("decode encoded cursor: %v", err)
			}
			if decoded != before {
				t.Fatalf("cursor round-trip = %+v, want %+v", decoded, before)
			}
			reencoded, err := EncodeCursor(identity, decoded)
			if err != nil {
				t.Fatalf("re-encode decoded cursor: %v", err)
			}
			if reencoded != canonicalEncoded {
				t.Fatalf("cursor encoding is not a fixed point: first=%q second=%q", canonicalEncoded, reencoded)
			}
		}

		decoded, err := DecodeCursor(encoded, identity)
		if err != nil {
			assertStaleCursorError(t, err)
			return
		}

		reencoded, err := EncodeCursor(identity, decoded)
		if err != nil {
			t.Fatalf("encode successfully decoded cursor: %v", err)
		}
		decodedAgain, err := DecodeCursor(reencoded, identity)
		if err != nil {
			t.Fatalf("decode canonicalized cursor: %v", err)
		}
		if decodedAgain != decoded {
			t.Fatalf("decode/encode fixed point = %+v, want %+v", decodedAgain, decoded)
		}

		rebased, err := RebaseCursor(encoded, before)
		if err != nil {
			t.Fatalf("rebase valid cursor: %v", err)
		}
		rebasedBefore, err := DecodeCursor(rebased, identity)
		if err != nil {
			t.Fatalf("decode rebased cursor: %v", err)
		}
		if rebasedBefore != before {
			t.Fatalf("rebased cursor = %+v, want %+v", rebasedBefore, before)
		}
	})
}
