package protocolhttp

// body_json.go encodes the built request body without reflection.
//
// Prepare used to hand the body to encoding/json, whose v1 encoder reaches
// every node through reflect: an arshaler lookup and an interface wrapper per
// map, slice and string. At 800 turns of history the body is a 1.8 MB tree of
// about 4,800 maps, 1,600 slices and 8,800 strings, and that generic walk cost
// 2.3 MB and 25,638 allocations for the marshal (#6409). The protocol builders
// put only a few concrete types into a body — string, bool, int, int64,
// float64, nil, map[string]any, []map[string]any, []any and []string — so this
// file encodes those directly and hands every other value to encoding/json
// unchanged.
//
// The bytes are the contract. The fallback is what keeps it: a type this file
// does not name, and a string holding a byte it will not vouch for, is encoded
// by the same code as before. TestMarshalBodyMatchesStdlib and
// protocolhttp's TestPrepareBodyBytesMatchStdlib pin that equivalence.
//
// The buffer is sized from a no-allocation pre-pass over the same tree. That
// costs one extra walk and saves the doubling chain a growing buffer pays for
// a 1.8 MB body: at 800 turns a fresh growing buffer allocated 10.6 MB to
// produce 1.8 MB, while a sized buffer allocates the result once.

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"slices"
	"strconv"
)

// MarshalBody encodes a built request body, returning bytes the caller owns.
// Every call allocates its own result: the body is retained for the life of
// the request (the wire body, the retry body, the API-log record), so a shared
// or pooled buffer would alias across requests.
func MarshalBody(body map[string]any) ([]byte, error) {
	return appendBody(make([]byte, 0, bodyJSONSize(body)), body)
}

// bodyJSONSize estimates how many bytes appendBody writes for v. It is a
// capacity hint, not a contract: an under-estimate costs one grow-and-copy,
// and the estimate is deliberately generous on the widths that are cheap to
// bound (numbers) so that a body of the usual shape is encoded in one
// allocation.
func bodyJSONSize(v any) int {
	switch x := v.(type) {
	case nil:
		return 4
	case string:
		return bodyJSONStringSize(x)
	case bool:
		return 5
	case int, int64:
		return 20
	case float64:
		return 24
	case map[string]any:
		if x == nil {
			return 4
		}
		size := 2
		for k, e := range x {
			size += bodyJSONStringSize(k) + 1 + bodyJSONSize(e) + 1
		}
		return size
	case []map[string]any:
		if x == nil {
			return 4
		}
		size := 2
		for _, e := range x {
			size += bodyJSONSize(e) + 1
		}
		return size
	case []any:
		if x == nil {
			return 4
		}
		size := 2
		for _, e := range x {
			size += bodyJSONSize(e) + 1
		}
		return size
	case []string:
		if x == nil {
			return 4
		}
		size := 2
		for _, e := range x {
			size += bodyJSONStringSize(e) + 1
		}
		return size
	case json.RawMessage:
		// Copied as-is (only ever compacted, which shrinks), so its own length
		// bounds it.
		return len(x) + 2
	case []byte:
		return base64.StdEncoding.EncodedLen(len(x)) + 2
	default:
		// Any other value goes to encoding/json, whose size is not known
		// without encoding it. A modest hint keeps this a hint: the buffer
		// grows if the value is larger, which costs one copy and never changes
		// the bytes.
		return 64
	}
}

// bodyJSONStringSize bounds appendBodyString's output for s. Printable ASCII
// costs one byte; each byte that needs escaping costs six, and this counts the
// worst case only for the bytes that actually appear, so an ordinary tool
// result is sized exactly.
func bodyJSONStringSize(s string) int {
	size := 2
	for i := 0; i < len(s); i++ {
		switch b := s[i]; {
		case b >= 0x80:
			// Unproven: appendBodyString hands the whole string to
			// encoding/json, which may expand one byte to six (\ufffd).
			return size + len(s)*6
		case bodyJSONPlain[b]:
			size++
		default:
			size += 6
		}
	}
	return size
}

// appendBody appends v's encoding. The named cases cover every type the
// protocol builders put in a body; everything else goes to encoding/json,
// which keeps Marshalers (json.RawMessage, typed schema values), []byte,
// json.Number, and the numeric types not listed here byte-for-byte identical
// to what the response used to carry.
func appendBody(dst []byte, v any) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return append(dst, "null"...), nil
	case string:
		return appendBodyString(dst, x), nil
	case bool:
		if x {
			return append(dst, "true"...), nil
		}
		return append(dst, "false"...), nil
	case int:
		return strconv.AppendInt(dst, int64(x), 10), nil
	case int64:
		return strconv.AppendInt(dst, x, 10), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			// encoding/json's own UnsupportedValueError, not a copy of its text.
			return appendStdlib(dst, x)
		}
		return appendBodyFloat(dst, x), nil
	case map[string]any:
		if x == nil {
			return append(dst, "null"...), nil
		}
		return appendBodyMap(dst, x)
	case []map[string]any:
		if x == nil {
			return append(dst, "null"...), nil
		}
		dst = append(dst, '[')
		for i, e := range x {
			if i > 0 {
				dst = append(dst, ',')
			}
			// A nil element is null, not {}: encoding/json keys null off the
			// map being nil, and appendBodyMap would range an empty map.
			if e == nil {
				dst = append(dst, "null"...)
				continue
			}
			var err error
			if dst, err = appendBodyMap(dst, e); err != nil {
				return dst, err
			}
		}
		return append(dst, ']'), nil
	case []any:
		if x == nil {
			return append(dst, "null"...), nil
		}
		dst = append(dst, '[')
		for i, e := range x {
			if i > 0 {
				dst = append(dst, ',')
			}
			var err error
			if dst, err = appendBody(dst, e); err != nil {
				return dst, err
			}
		}
		return append(dst, ']'), nil
	case []string:
		if x == nil {
			return append(dst, "null"...), nil
		}
		dst = append(dst, '[')
		for i, e := range x {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendBodyString(dst, e)
		}
		return append(dst, ']'), nil
	default:
		return appendStdlib(dst, v)
	}
}

// appendStdlib encodes v with encoding/json and appends the result. Marshal
// returns a Marshaler's output already compacted and HTML-escaped, so these
// bytes are what the same value contributed before this file existed.
func appendStdlib(dst []byte, v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return dst, err
	}
	return append(dst, b...), nil
}

// appendBodyMap writes m with sorted keys, the order encoding/json uses for
// map[string]any. The key slice is stack-allocated for the usual body object,
// which holds a handful of fields.
func appendBodyMap(dst []byte, m map[string]any) ([]byte, error) {
	var stack [12]string
	keys := stack[:0]
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	dst = append(dst, '{')
	for i, k := range keys {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendBodyString(dst, k)
		dst = append(dst, ':')
		var err error
		if dst, err = appendBody(dst, m[k]); err != nil {
			return dst, err
		}
	}
	return append(dst, '}'), nil
}

// appendBodyFloat writes f as encoding/json writes a float64: the shortest
// representation that round-trips, in 'f' form except below 1e-6 or at 1e21
// and above, where 'e' form is used with its exponent unpadded (e-09 → e-9).
func appendBodyFloat(dst []byte, f float64) []byte {
	format := byte('f')
	if abs := math.Abs(f); abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	dst = strconv.AppendFloat(dst, f, format, -1, 64)
	if format == 'e' {
		if n := len(dst); n >= 4 && dst[n-4] == 'e' && dst[n-3] == '-' && dst[n-2] == '0' {
			dst[n-2] = dst[n-1]
			dst = dst[:n-1]
		}
	}
	return dst
}

// bodyJSONPlain reports whether b is copied into a JSON string as-is.
// encoding/json's htmlSafeSet passes 0x20 through 0x7e except ", \, <, > and
// &, and passes 0x7f as well, so this table matches it byte for byte.
var bodyJSONPlain = func() (plain [256]bool) {
	for b := 0x20; b < 0x80; b++ {
		plain[b] = true
	}
	for _, b := range []byte{'"', '\\', '<', '>', '&'} {
		plain[b] = false
	}
	return plain
}()

const bodyJSONHex = "0123456789abcdef"

// appendBodyString quotes s with encoding/json's escaping: <, > and & become
// \u003c-style escapes, a control byte gets its short escape where JSON has
// one and \u00XX otherwise, and 0x7f passes through.
//
// A byte at or above 0x80 sends the whole string to encoding/json, which owns
// invalid-UTF-8 replacement, U+2028 and U+2029, and every multibyte run. The
// fast path is printable ASCII, which is what tool results and JSON schemas
// are made of; the edge cases stay with the code that already handled them.
func appendBodyString(dst []byte, s string) []byte {
	// mark is where this string starts: the multibyte fallback rewinds here,
	// discarding every byte this call wrote, not just one.
	mark := len(dst)
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		b := s[i]
		if bodyJSONPlain[b] {
			continue
		}
		if b >= 0x80 {
			// Unproven byte: re-encode the whole string from scratch.
			src, err := json.Marshal(s)
			if err != nil {
				// Marshal of a string cannot fail; quote nothing rather than
				// emit a half string.
				return append(dst[:mark], `""`...)
			}
			return append(dst[:mark], src...)
		}
		dst = append(dst, s[start:i]...)
		switch b {
		case '\\', '"':
			dst = append(dst, '\\', b)
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			dst = append(dst, '\\', 'u', '0', '0', bodyJSONHex[b>>4], bodyJSONHex[b&0xF])
		}
		start = i + 1
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}
