// Package runetrim realigns byte slices that were cut at an arbitrary offset so
// the cut edge does not fall inside a UTF-8 rune. Job output is windowed by raw
// byte offset — the live and closed-file tail/head readers, the retention
// pruner — and a window whose edge lands mid-rune renders as a replacement
// character.
//
// Job output is not required to be UTF-8 (binary logs are legal), so both trims
// are conservative: they drop only the bytes a cut could have orphaned. The
// caller applies them to ITS OWN cuts alone; an edge that is the file's own
// first or last byte passes through untouched.
package runetrim

import "unicode/utf8"

// TrimTrailingPartial drops an incomplete UTF-8 sequence left dangling at the
// END of a byte slice cut at an arbitrary offset, so slicing valid UTF-8 output
// never yields an invalid tail fragment. The final sequence goes whenever it
// does not decode as one whole rune — an orphaned prefix like 0xF0 0x9F, and
// equally a byte that could never open a rune at all.
func TrimTrailingPartial(b []byte) []byte {
	i := len(b) - 1
	for i >= 0 && !utf8.RuneStart(b[i]) {
		i--
	}
	if i < 0 {
		return b
	}
	if r, size := utf8.DecodeRune(b[i:]); r == utf8.RuneError && size == 1 {
		return b[:i]
	}
	return b
}

// TrimLeadingPartial drops UTF-8 continuation bytes left dangling at the START
// of a byte slice cut at an arbitrary offset. It drops at most three: that is the
// longest continuation run a 4-byte rune can leave behind a cut, so output that
// is not UTF-8 at all keeps every byte past that bound instead of having a whole
// run of 0x80-0xBF bytes eaten.
func TrimLeadingPartial(b []byte) []byte {
	i := 0
	for i < len(b) && i < utf8.UTFMax-1 && !utf8.RuneStart(b[i]) {
		i++
	}
	return b[i:]
}
