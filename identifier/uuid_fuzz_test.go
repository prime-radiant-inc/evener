package identifier

import (
	"testing"

	"github.com/google/uuid"
)

func FuzzUUIDBase62RoundTrip(f *testing.F) {
	for _, value := range []uuid.UUID{{}, {15: 1}, {0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, uuid.MustParse("018f0f3f-9b3e-7abc-8def-0123456789ab")} {
		f.Add(value[:])
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) != 16 {
			return
		}
		var value uuid.UUID
		copy(value[:], raw)
		encoded := EncodeUUID(value)
		decoded, err := DecodeUUID(encoded)
		if err != nil {
			t.Fatalf("DecodeUUID(%q): %v", encoded, err)
		}
		if decoded != value {
			t.Fatalf("decoded=%x, want %x", decoded, value)
		}
	})
}
