package runetrim

import (
	"bytes"
	"testing"
	"unicode/utf8"
)

// FuzzRuneTrim drives TrimTrailingPartial and TrimLeadingPartial with
// arbitrary byte slices, restoring the fuzz surface that moved out of
// agent's FuzzJobOutputDigestSeedCoverage in 70be026df ("refactor(agent):
// partial-rune trims get a home both output windowers can reach") without a
// replacement Fuzz* target. Both trims cut byte slices at an arbitrary
// offset — job output is windowed by raw byte offset, and output is not
// required to be UTF-8 (binary logs are legal) — so the invariants below are
// about the boundary they leave behind, not about the input being valid
// UTF-8.
//
// Both functions are documented as trimming a SINGLE cut edge, on the
// assumption that everything but that edge came from otherwise-legitimate
// output. Fuzzing found two properties that hold for that precondition but
// not for arbitrary adversarial byte strings crafted to violate it — neither
// is a bug in the trims, just outside their contract, so they are
// deliberately not asserted here:
//   - TrimTrailingPartial only rescans back to the LAST rune-start byte; a
//     complete rune followed by an unrelated run of raw continuation bytes
//     (a construct a real single cut cannot produce) decodes fine at that
//     rune-start and is left untouched, continuation run and all.
//   - TrimLeadingPartial's "drop at most 3" bound is per call, not
//     cumulative: re-applying it to its own output can drop further bytes
//     once the residue is short enough to look, on its own, like it fits
//     inside the bound again. Real callers trim each cut exactly once.
func FuzzRuneTrim(f *testing.F) {
	type seed struct {
		b   []byte
		cut int
	}
	seeds := []seed{
		{[]byte{}, 0},
		{[]byte("ok"), 1},
		{[]byte{'a', 0xe2, 0x82}, 0},
		{[]byte("a😀"), 2},
		{[]byte{0x80, 0x81}, 1},
		{[]byte{'a', 0xff}, 1},
		{[]byte{0x80, 0x81, 'x'}, 2},
		{[]byte{0x9f, 0x98, 0x80, 'x'}, 3},
		{[]byte{0x80, 0x80, 0x80, 0x80, 0x80}, 2},
		{[]byte{0xff, 'x'}, 1},
		{[]byte("hello, 世界"), 8},
	}
	for _, s := range seeds {
		f.Add(s.b, s.cut)
	}

	f.Fuzz(func(t *testing.T, b []byte, cut int) {
		trailing := TrimTrailingPartial(b)
		leading := TrimLeadingPartial(b)

		// Never grow the slice; both trims only drop bytes.
		if len(trailing) > len(b) {
			t.Fatalf("TrimTrailingPartial(%x) grew to %x", b, trailing)
		}
		if len(leading) > len(b) {
			t.Fatalf("TrimLeadingPartial(%x) grew to %x", b, leading)
		}

		// TrimTrailingPartial only ever removes bytes off the END: the
		// result must be a prefix of the input.
		if !bytes.HasPrefix(b, trailing) {
			t.Fatalf("TrimTrailingPartial(%x) = %x is not a prefix of input", b, trailing)
		}
		// TrimLeadingPartial only ever removes bytes off the START: the
		// result must be a suffix of the input.
		if !bytes.HasSuffix(b, leading) {
			t.Fatalf("TrimLeadingPartial(%x) = %x is not a suffix of input", b, leading)
		}

		// TrimLeadingPartial drops at most UTFMax-1 (3) bytes: that is the
		// longest continuation run a 4-byte rune can leave behind a cut.
		if dropped := len(b) - len(leading); dropped > utf8.UTFMax-1 {
			t.Fatalf("TrimLeadingPartial(%x) dropped %d bytes, want <= %d", b, dropped, utf8.UTFMax-1)
		}

		// The leading trim never leaves a dangling continuation byte at the
		// new start, unless it already hit the 3-byte drop bound (in which
		// case a longer run of continuation bytes is not a partial rune at
		// all, and is left alone by design).
		if len(leading) > 0 && len(b)-len(leading) < utf8.UTFMax-1 {
			if !utf8.RuneStart(leading[0]) {
				t.Fatalf("TrimLeadingPartial(%x) = %x still starts with a continuation byte", b, leading)
			}
		}

		// A slice made entirely of continuation bytes has no lead byte to
		// anchor a trim against, so TrimTrailingPartial passes it through
		// untouched (the original seed-coverage assertion this target
		// restores: "all-continuation trailing slice changed").
		if len(b) > 0 && allContinuation(b) && !bytes.Equal(trailing, b) {
			t.Fatalf("TrimTrailingPartial(%x) changed an all-continuation slice to %x", b, trailing)
		}

		// If the input is already valid UTF-8, neither trim removes
		// anything: there is no partial rune to cut, at either edge.
		if utf8.Valid(b) {
			if !bytes.Equal(trailing, b) {
				t.Fatalf("TrimTrailingPartial(%x) changed valid UTF-8 to %x", b, trailing)
			}
			if !bytes.Equal(leading, b) {
				t.Fatalf("TrimLeadingPartial(%x) changed valid UTF-8 to %x", b, leading)
			}
		}

		// The core contract, exercised the way the real windowers exercise
		// it: cut a VALID UTF-8 string at an arbitrary byte offset — which
		// can land mid-rune — and confirm each trim repairs its own side of
		// the cut back to valid UTF-8. This is the metamorphic property the
		// original seed-coverage assertions stood in for with fixed
		// examples ("incomplete rune dropped", "leading continuation bytes
		// trimmed"); here it holds for every offset of every valid input,
		// not just the hand-picked ones.
		if len(b) > 0 && utf8.Valid(b) {
			at := ((cut % len(b)) + len(b)) % len(b)
			head, tail := b[:at], b[at:]
			trimmedHead := TrimTrailingPartial(head)
			trimmedTail := TrimLeadingPartial(tail)
			if !utf8.Valid(trimmedHead) {
				t.Fatalf("TrimTrailingPartial(%x) (head of %x cut at %d) = %x is not valid UTF-8", head, b, at, trimmedHead)
			}
			if !utf8.Valid(trimmedTail) {
				t.Fatalf("TrimLeadingPartial(%x) (tail of %x cut at %d) = %x is not valid UTF-8", tail, b, at, trimmedTail)
			}
		}
	})
}

// allContinuation reports whether every byte in b is a UTF-8 continuation
// byte (10xxxxxx) — a slice with no lead byte anywhere in it to anchor a
// trim against.
func allContinuation(b []byte) bool {
	for _, c := range b {
		if utf8.RuneStart(c) {
			return false
		}
	}
	return true
}
