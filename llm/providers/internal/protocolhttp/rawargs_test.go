package protocolhttp

import (
	"bytes"
	"encoding/json"
	"testing"
	"unicode/utf16"
)

// TestRawStringContent_StdlibCrossCheck is the mandated oracle: for every
// valid-UTF-8 token, RawStringContent output == stdlib json.Unmarshal into
// string (we diverge from stdlib ONLY where stdlib is lossy); for
// invalid-UTF-8 tokens, raw bytes pass through with 0xff intact.
func TestRawStringContent_StdlibCrossCheck(t *testing.T) {
	cases := []struct {
		name  string
		token []byte // a complete JSON string token, including quotes
	}{
		{"empty", []byte(`""`)},
		{"plain ascii", []byte(`"hello"`)},
		{"with spaces", []byte(`"hello world"`)},
		{"escaped quote", []byte(`"say \"hi\""`)},
		{"escaped backslash", []byte(`"a\\b"`)},
		{"escaped slash", []byte(`"a\/b"`)},
		{"escaped backspace", []byte(`"a\bb"`)},
		{"escaped formfeed", []byte(`"a\fb"`)},
		{"escaped newline", []byte(`"a\nb"`)},
		{"escaped cr", []byte(`"a\rb"`)},
		{"escaped tab", []byte(`"a\tb"`)},
		{"unicode escape", []byte(`"\u00e9"`)},               // é
		{"unicode escape 4 digit", []byte(`"\u4e16\u754c"`)}, // 世界
		{"surrogate pair", []byte(`"\ud83d\ude00"`)},         // 😀
		{"surrogate pair u+10000", []byte(`"\ud800\udc00"`)},
		{"mixed escapes", []byte(`"a\nb\t\"c\\d"`)},
		{"json object as string", []byte(`"{\"x\":1,\"y\":2}"`)},
		{"json array as string", []byte(`"[1,2,3]"`)},
		{"non canonical json string", []byte(`"{ \"b\" : 2 , \"a\" : 1 }"`)},
		{"unicode null", []byte(`"\u0000"`)},
		{"all named escapes", []byte(`"\"\\\//\b\f\n\r\t"`)},
		{"high codepoint", []byte(`"\uffff"`)},
		{"bmp greek", []byte(`"\u03b1\u03b2\u03b3"`)}, // αβγ
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RawStringContent(tc.token)
			if err != nil {
				t.Fatalf("RawStringContent: %v", err)
			}
			// Stdlib cross-check: json.Unmarshal into string.
			var std string
			if err := json.Unmarshal(tc.token, &std); err != nil {
				t.Fatalf("stdlib json.Unmarshal: %v", err)
			}
			if !bytes.Equal(got, []byte(std)) {
				t.Fatalf("RawStringContent = %q, stdlib = %q", got, std)
			}
		})
	}
}

// TestRawStringContent_InvalidUTF8Preserved asserts raw bytes >= 0x80 pass
// through unchanged — the core divergence from stdlib which coerces invalid
// UTF-8 to U+FFFD.
func TestRawStringContent_InvalidUTF8Preserved(t *testing.T) {
	cases := []struct {
		name  string
		token []byte
		want  []byte
	}{
		{
			name:  "raw 0xff inside json string",
			token: []byte("\"" + `{"x":"` + "\xff" + `"}` + "\""),
			want:  []byte(`{"x":"` + "\xff" + `"}`),
		},
		{
			name:  "raw 0xfe 0xff bom",
			token: []byte("\"\xfe\xff\""),
			want:  []byte("\xfe\xff"),
		},
		{
			name:  "lone continuation byte 0x80",
			token: []byte("\"\x80\""),
			want:  []byte("\x80"),
		},
		{
			name:  "mixed valid and invalid utf8",
			token: []byte("\"" + "abc\xff" + "def" + "\""),
			want:  []byte("abc\xffdef"),
		},
		{
			name:  "raw 0xc3 0xa9 valid utf8 pair passes through",
			token: []byte("\"\xc3\xa9\""), // é in UTF-8
			want:  []byte("\xc3\xa9"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RawStringContent(tc.token)
			if err != nil {
				t.Fatalf("RawStringContent: %v", err)
			}
			if !bytes.Equal(got, tc.want) {
				t.Fatalf("RawStringContent = %q (% x), want %q (% x)", got, got, tc.want, tc.want)
			}
			// Verify no U+FFFD substitution occurred (0xEF 0xBF 0xBD).
			if bytes.Contains(got, []byte{0xEF, 0xBF, 0xBD}) {
				t.Fatalf("output contains U+FFFD (0xEF 0xBF 0xBD), want raw bytes preserved: % x", got)
			}
		})
	}
}

// TestRawStringContent_UnpairedSurrogates asserts unpaired surrogates decode
// to U+FFFD (stdlib parity), with a one-line comment saying so.
func TestRawStringContent_UnpairedSurrogates(t *testing.T) {
	cases := []struct {
		name  string
		token []byte
	}{
		{"lone high surrogate", []byte(`"\ud83d"`)},
		{"high then non-surrogate", []byte(`"\ud83d\u0041"`)}, // high surrogate followed by 'A'
		{"lone low surrogate", []byte(`"\udc00"`)},
		{"high at end of string", []byte(`"abc\ud83d"`)},
	}

	// Unpaired surrogates decode to U+FFFD (stdlib parity).
	ufffd := []byte{0xEF, 0xBF, 0xBD}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RawStringContent(tc.token)
			if err != nil {
				t.Fatalf("RawStringContent: %v", err)
			}
			// Stdlib cross-check.
			var std string
			if err := json.Unmarshal(tc.token, &std); err != nil {
				t.Fatalf("stdlib json.Unmarshal: %v", err)
			}
			if !bytes.Equal(got, []byte(std)) {
				t.Fatalf("RawStringContent = %q, stdlib = %q", got, std)
			}
			if !bytes.Contains(got, ufffd) {
				t.Fatalf("expected U+FFFD for unpaired surrogate, got % x", got)
			}
		})
	}
}

// TestRawStringContent_SurrogatePairCombines asserts valid surrogate pairs
// combine into the correct codepoint, not two U+FFFDs.
func TestRawStringContent_SurrogatePairCombines(t *testing.T) {
	// U+1F600 (😀) = \ud83d\ude00
	got, err := RawStringContent([]byte(`"\ud83d\ude00"`))
	if err != nil {
		t.Fatalf("RawStringContent: %v", err)
	}
	want := []byte("\xf0\x9f\x98\x80") // U+1F600 in UTF-8
	if !bytes.Equal(got, want) {
		t.Fatalf("got % x, want % x (U+1F600)", got, want)
	}

	// Also verify it matches utf16.DecodeRune.
	r := utf16.DecodeRune(0xd83d, 0xde00)
	if string(got) != string(r) {
		t.Fatalf("got %q, utf16.DecodeRune = %q", got, r)
	}
}

// TestRawStringContent_FastPathNoEscapes verifies the fast path (no backslash)
// preserves raw bytes, including invalid UTF-8.
func TestRawStringContent_FastPathNoEscapes(t *testing.T) {
	// A token with no backslash and a raw 0xff byte.
	token := []byte("\"" + `{"x":"` + "\xff" + `"}` + "\"")
	got, err := RawStringContent(token)
	if err != nil {
		t.Fatalf("RawStringContent: %v", err)
	}
	want := []byte(`{"x":"` + "\xff" + `"}`)
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestRawStringContent_ErrorsOnMalformedToken asserts structurally invalid
// tokens return an error so the caller can fall back.
func TestRawStringContent_ErrorsOnMalformedToken(t *testing.T) {
	cases := []struct {
		name  string
		token []byte
	}{
		{"not a string", []byte(`null`)},
		{"number", []byte(`42`)},
		{"object", []byte(`{"a":1}`)},
		{"single quote", []byte(`'hello'`)},
		{"empty bytes", []byte{}},
		{"single byte", []byte(`"`)},
		{"truncated escape", []byte(`"abc\`)},
		{"invalid escape char", []byte(`"\x"`)},
		{"truncated u escape", []byte(`"\u12"`)},
		{"non-hex u escape", []byte(`"\uZZZZ"`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := RawStringContent(tc.token)
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.token)
			}
		})
	}
}

// TestRawStringContent_NoEscapesMatchesStdlib is a broader cross-check for
// the fast path: a variety of valid-UTF-8 strings without escapes.
func TestRawStringContent_NoEscapesMatchesStdlib(t *testing.T) {
	strs := []string{
		"",
		"a",
		"hello world",
		`plain text with no quotes`,
		`unicode: αβγδ 中文 日本語`,
		`emoji: 😀🎉`,
		`mixed: αβg 😀 中`,
	}
	for _, s := range strs {
		token := []byte(`"` + s + `"`)
		got, err := RawStringContent(token)
		if err != nil {
			t.Fatalf("RawStringContent(%q): %v", token, err)
		}
		var std string
		if err := json.Unmarshal(token, &std); err != nil {
			t.Fatalf("stdlib json.Unmarshal(%q): %v", token, err)
		}
		if !bytes.Equal(got, []byte(std)) {
			t.Fatalf("RawStringContent = %q, stdlib = %q", got, std)
		}
	}
}
