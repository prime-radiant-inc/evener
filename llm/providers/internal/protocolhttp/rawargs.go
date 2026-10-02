package protocolhttp

import (
	"bytes"
	"errors"
	"fmt"
	"unicode/utf16"
)

// rawStringContent unescapes a JSON string token (the bytes including the
// surrounding double quotes, as captured by json.RawMessage) into its
// byte-faithful content, preserving raw bytes that encoding/json would coerce
// when decoding into a Go string.
//
// It is used by the string-form provider adapters (chatcompletions,
// responses, anthropic-stream) to capture tool-call argument bytes that the
// lossy map[string]any decode at the protocolhttp layer would mutate —
// invalid-UTF-8 bytes are coerced to U+FFFD by json.Unmarshal into string,
// losing fidelity before the recording site ever sees the message.
//
// Divergence from encoding/json is deliberate and minimal:
//   - Raw bytes >= 0x80 pass through unchanged (encoding/json coerces invalid
//     UTF-8 to U+FFFD when decoding into a Go string).
//   - Unpaired surrogates decode to U+FFFD (encoding/json parity).
//   - Named escapes and valid \uXXXX pairs decode normally.
//
// An unescape edge case (unpaired surrogate, lone low surrogate) never errors
// the whole decode; only a structurally invalid token (missing quotes,
// truncated escape) returns an error so the caller can fall back to the
// existing map-decoded args.
func RawStringContent(token []byte) ([]byte, error) {
	if len(token) < 2 || token[0] != '"' || token[len(token)-1] != '"' {
		return nil, errors.New("rawStringContent: token is not a JSON string")
	}
	s := token[1 : len(token)-1]
	// Fast path: no backslash means no escapes — the content is the bytes
	// between the quotes, verbatim. This preserves raw non-UTF-8 bytes.
	if bytes.IndexByte(s, '\\') < 0 {
		return append([]byte(nil), s...), nil
	}
	var out bytes.Buffer
	out.Grow(len(s))
	for i := 0; i < len(s); {
		b := s[i]
		if b != '\\' {
			out.WriteByte(b)
			i++
			continue
		}
		i++ // skip backslash
		if i >= len(s) {
			return nil, errors.New("rawStringContent: truncated escape at end of string")
		}
		switch s[i] {
		case '"', '\\', '/':
			out.WriteByte(s[i])
			i++
		case 'b':
			out.WriteByte(0x08)
			i++
		case 'f':
			out.WriteByte(0x0c)
			i++
		case 'n':
			out.WriteByte(0x0a)
			i++
		case 'r':
			out.WriteByte(0x0d)
			i++
		case 't':
			out.WriteByte(0x09)
			i++
		case 'u':
			i++ // skip 'u'
			r, ok := decodeHex4(s, &i)
			if !ok {
				return nil, errors.New("rawStringContent: truncated \\u escape")
			}
			if r >= 0xD800 && r <= 0xDBFF {
				// High surrogate: look for a following low surrogate.
				if i+6 <= len(s) && s[i] == '\\' && s[i+1] == 'u' {
					save := i
					i += 2 // skip \u
					r2, ok2 := decodeHex4(s, &i)
					if ok2 && r2 >= 0xDC00 && r2 <= 0xDFFF {
						r = utf16.DecodeRune(r, r2)
					} else {
						// Unpaired high surrogate → U+FFFD (stdlib parity).
						r = 0xFFFD
						i = save // don't consume the second \u
					}
				} else {
					// Unpaired high surrogate → U+FFFD (stdlib parity).
					r = 0xFFFD
				}
			} else if r >= 0xDC00 && r <= 0xDFFF {
				// Unpaired low surrogate → U+FFFD (stdlib parity).
				r = 0xFFFD
			}
			out.WriteRune(r)
		default:
			return nil, fmt.Errorf("rawStringContent: invalid escape \\%c", s[i])
		}
	}
	return out.Bytes(), nil
}

// decodeHex4 reads exactly 4 hex digits from s starting at *i, advancing *i
// past them, and returns the decoded rune. Returns false if there are not
// enough bytes or a non-hex digit is encountered.
func decodeHex4(s []byte, i *int) (rune, bool) {
	if *i+4 > len(s) {
		return 0, false
	}
	var r rune
	for range 4 {
		c := s[*i]
		*i++
		var v byte
		switch {
		case c >= '0' && c <= '9':
			v = c - '0'
		case c >= 'a' && c <= 'f':
			v = c - 'a' + 10
		case c >= 'A' && c <= 'F':
			v = c - 'A' + 10
		default:
			return 0, false
		}
		r = r*16 + rune(v)
	}
	return r, true
}
