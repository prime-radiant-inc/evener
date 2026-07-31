package runetrim

import (
	"bytes"
	"testing"
)

func TestTrimTrailingPartial(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []byte
		want []byte
	}{
		{name: "complete rune kept", in: []byte("ok"), want: []byte("ok")},
		{name: "incomplete rune dropped", in: []byte{'a', 0xe2, 0x82}, want: []byte{'a'}},
		{name: "whole multi-byte rune kept", in: []byte("a😀"), want: []byte("a😀")},
		{name: "all continuation bytes kept", in: []byte{0x80, 0x81}, want: []byte{0x80, 0x81}},
		{name: "empty", in: []byte{}, want: []byte{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := TrimTrailingPartial(tc.in); !bytes.Equal(got, tc.want) {
				t.Fatalf("TrimTrailingPartial(%x) = %x, want %x", tc.in, got, tc.want)
			}
		})
	}
}

func TestTrimLeadingPartial(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []byte
		want []byte
	}{
		{name: "rune start kept", in: []byte("ok"), want: []byte("ok")},
		{name: "continuation bytes dropped", in: []byte{0x80, 0x81, 'x'}, want: []byte{'x'}},
		{name: "tail of a 4-byte rune dropped", in: []byte{0x9f, 0x98, 0x80, 'x'}, want: []byte{'x'}},
		// A continuation run longer than any rune could leave behind a cut is not a
		// partial rune, so only the three bytes a 4-byte rune could account for go.
		{name: "over-long continuation run bounded", in: []byte{0x80, 0x80, 0x80, 0x80, 0x80}, want: []byte{0x80, 0x80}},
		{name: "invalid lead byte kept", in: []byte{0xff, 'x'}, want: []byte{0xff, 'x'}},
		{name: "empty", in: []byte{}, want: []byte{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := TrimLeadingPartial(tc.in); !bytes.Equal(got, tc.want) {
				t.Fatalf("TrimLeadingPartial(%x) = %x, want %x", tc.in, got, tc.want)
			}
		})
	}
}
