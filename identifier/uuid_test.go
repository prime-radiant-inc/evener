package identifier

import (
	"testing"

	"github.com/google/uuid"
)

func TestUUIDBase62Vectors(t *testing.T) {
	tests := []struct {
		name string
		raw  uuid.UUID
		want string
	}{
		{"zero", uuid.UUID{}, "0000000000000000000000"},
		{"one", uuid.UUID{15: 1}, "0000000000000000000001"},
		{"max", uuid.UUID{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, "7n42DGM5Tflk9n8mt7Fhc7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EncodeUUID(tt.raw); got != tt.want {
				t.Errorf("encoded=%q, want %q", got, tt.want)
			}
			decoded, err := DecodeUUID(tt.want)
			if err != nil || decoded != tt.raw {
				t.Errorf("decoded=%x err=%v, want %x", decoded, err, tt.raw)
			}
		})
	}
}

func TestDecodeUUIDRejects(t *testing.T) {
	for _, input := range []string{"", "0", "00000000000000000000000", "-000000000000000000000", "_000000000000000000000", "!000000000000000000000", "zzzzzzzzzzzzzzzzzzzzzzzz"} {
		t.Run(input, func(t *testing.T) {
			if _, err := DecodeUUID(input); err == nil {
				t.Fatalf("DecodeUUID(%q) succeeded", input)
			}
		})
	}
}

func TestValidateUUIDv7Payload(t *testing.T) {
	v7 := uuid.MustParse("018f0f3f-9b3e-7abc-8def-0123456789ab")
	if err := ValidateUUIDv7Payload(EncodeUUID(v7)); err != nil {
		t.Fatalf("valid v7 rejected: %v", err)
	}
	for name, raw := range map[string]uuid.UUID{
		"v4":            uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"),
		"wrong variant": {0x01, 0x8f, 0x0f, 0x3f, 0x9b, 0x3e, 0x7a, 0xbc, 0x0d, 0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateUUIDv7Payload(EncodeUUID(raw)); err == nil {
				t.Fatalf("invalid UUID accepted")
			}
		})
	}
}
