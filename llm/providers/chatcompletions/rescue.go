package chatcompletions

import (
	"bytes"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"unicode/utf16"
	"unicode/utf8"
)

// claudeXMLParamOpenRE matches the OPEN of an Anthropic-style parameter tag.
// Some models (notably MiniMax M2.7 via OpenRouter) emit Claude/MiniMax XML
// tool-call syntax inside JSON tool arguments when they revert from JSON to
// XML mid-generation. Closing tags are often missing, so we scan opens and
// slice to the next open or end-of-string.
//
// Pattern details (adapted from zeroclaw-labs/zeroclaw PR #1189 which solved
// the same parsing problem for MiniMax at the content level):
//   - Case-insensitive (`(?i)`) matches tag-case variation.
//   - Permissive attribute matching: other attributes can appear before `name=`.
//   - Both double and single quoted attribute values.
var claudeXMLParamOpenRE = regexp.MustCompile(
	"(?i)<parameter\\b[^>]*\\bname\\s*=\\s*(?:\"([^\"]+)\"|'([^']+)')[^>]*>",
)

// claudeXMLParamHintRE is the cheap wake-up gate: a parameter tag name
// followed by whitespace. It matches today's three substring checks, so a
// carriage return after the tag name does not fire it.
var claudeXMLParamHintRE = regexp.MustCompile("(?i)<parameter[ \t\n]")

// claudeXMLParamCloseTag is the optional closing tag stripped from an
// extracted parameter value. Models often omit the closing tag.
const claudeXMLParamCloseTag = "</parameter>"

// rescueClaudeXMLArgs attempts to recover tool call arguments when a model
// mixes Claude-style XML tool call syntax into the JSON arguments field.
//
// Examples:
//
//	Input:  {"action":"append\">\n<parameter name=\"tasks\">[{...}]"}   (no close tag)
//	Output: {"action":"append","tasks":[{...}]}
//
//	Input:  {"action":"update\">\n<parameter name=\"updates\">[{...}]"}   (no close tag)
//	Output: {"action":"update","updates":[{...}]}
//
// Argument bytes are handled as opaque JSON tokens, so a rescue preserves
// byte-faithful content the standard map decode would coerce: invalid UTF-8,
// non-canonical number spellings, escape spellings, and HTML characters in
// fields the rescue does not rewrite. Only a rewritten carrier prefix and
// non-JSON extracted values are re-escaped, as JSON requires.
//
// If the input contains neither rescue shape, it is returned unchanged. If
// rescue is not possible, the original input is returned so the usual
// schema-validation error path can surface the problem.
func rescueClaudeXMLArgs(raw string) string {
	if raw == "" {
		return raw
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		return raw
	}

	// Second-order rescue: for any top-level field whose value is a string
	// that looks like a JSON array or object (starts with `[` or `{`), try
	// parsing it. Models sometimes emit JSON-encoded strings for array/object
	// fields instead of the parsed value. This is a separate bug from the
	// Claude-XML corruption but benefits from the same rescue path.
	parsedAnyJSONString := false
	for k, v := range fields {
		content, ok := decodeJSONStringContent(v)
		if !ok {
			continue
		}
		trimmed := bytes.TrimSpace(content)
		if len(trimmed) == 0 || (trimmed[0] != '[' && trimmed[0] != '{') {
			continue
		}
		var asJSON any
		if err := json.Unmarshal(trimmed, &asJSON); err != nil {
			continue
		}
		switch asJSON.(type) {
		case []any, map[string]any:
			fields[k] = json.RawMessage(trimmed)
			parsedAnyJSONString = true
		}
	}

	// Quick exit: if no string value contains an XML parameter hint, nothing
	// more to rescue. Case-insensitive because models sometimes emit
	// uppercase tag names.
	hasXML := false
	for _, v := range fields {
		if content, ok := decodeJSONStringContent(v); ok && claudeXMLParamHintRE.Match(content) {
			hasXML = true
			break
		}
	}
	if !hasXML {
		if parsedAnyJSONString {
			return string(appendJSONObject(nil, fields))
		}
		return raw
	}

	// For each string value, check whether it carries a `"><parameter...` tail.
	// If so, split at the `">` boundary: the prefix is the real value, the tail
	// holds the parameter blocks whose name/value pairs should become siblings.
	// Sorted keys make a name extracted by more than one carrier deterministic:
	// the later-sorting carrier wins.
	changed := false
	extracted := make(map[string]json.RawMessage)
	for _, k := range sortedFieldKeys(fields) {
		content, ok := decodeJSONStringContent(fields[k])
		if !ok {
			continue
		}
		openMatch := claudeXMLParamOpenRE.FindIndex(content)
		if openMatch == nil {
			continue
		}
		// Split at the first `">` before the parameter block. If there's no
		// `">` (value not terminated with a quote-close-bracket), fall back to
		// splitting at the `<` of the parameter tag.
		idx := bytes.Index(content[:openMatch[0]], []byte(`">`))
		if idx < 0 {
			idx = openMatch[0]
		}
		fields[k] = json.RawMessage(appendJSONString(nil, content[:idx]))
		changed = true

		tail := content[idx:]
		// Find all parameter opens. Between consecutive opens (or from an open
		// to end-of-string), the value is the parameter content. Submatch
		// indices are:
		//   m[0..1] = whole match
		//   m[2..3] = double-quoted name (or -1 if single-quoted)
		//   m[4..5] = single-quoted name (or -1 if double-quoted)
		opens := claudeXMLParamOpenRE.FindAllSubmatchIndex(tail, -1)
		for i, m := range opens {
			var paramName string
			if m[2] >= 0 {
				paramName = string(tail[m[2]:m[3]])
			} else if m[4] >= 0 {
				paramName = string(tail[m[4]:m[5]])
			}
			valStart := m[1] // index just after the `>` of this open tag
			valEnd := len(tail)
			if i+1 < len(opens) {
				valEnd = opens[i+1][0]
			}
			paramValue := tail[valStart:valEnd]
			// Strip the last closing tag (case-insensitive) if present, then
			// trim Unicode whitespace exactly as strings.TrimSpace did.
			if cut := lastIndexFoldASCII(paramValue, claudeXMLParamCloseTag); cut >= 0 {
				paramValue = paramValue[:cut]
			}
			paramValue = bytes.TrimSpace(paramValue)
			// A parameter value that is itself JSON is inserted with its exact
			// bytes. Otherwise it is inserted as a byte-preserving JSON string.
			// Use json.Unmarshal rather than json.Valid for the branch decision:
			// a syntactically valid number outside float64 range (for example
			// 1e999) must keep today's string treatment, so the emitted
			// arguments stay decodable by the tool-call path.
			var asJSON any
			if err := json.Unmarshal(paramValue, &asJSON); err == nil {
				extracted[paramName] = append(json.RawMessage(nil), paramValue...)
			} else {
				extracted[paramName] = json.RawMessage(appendJSONString(nil, paramValue))
			}
		}
	}

	if !changed {
		return raw
	}

	// Merge extracted params into the top-level object. Parent fields take
	// precedence if there's a collision — the parent had the "primary" value.
	for k, v := range extracted {
		if _, exists := fields[k]; !exists {
			fields[k] = v
		}
	}

	return string(appendJSONObject(nil, fields))
}

// decodeJSONStringContent returns the content bytes of a JSON string token
// (including its surrounding quotes) without coercing bytes that
// encoding/json would decode to U+FFFD. Named escapes and valid \uXXXX
// surrogate pairs decode normally; unpaired surrogates become U+FFFD (stdlib
// parity); raw bytes >= 0x80 pass through unchanged.
func decodeJSONStringContent(token []byte) ([]byte, bool) {
	if len(token) < 2 || token[0] != '"' || token[len(token)-1] != '"' {
		return nil, false
	}
	body := token[1 : len(token)-1]
	if bytes.IndexByte(body, '\\') < 0 {
		return body, true
	}
	out := make([]byte, 0, len(body))
	for i := 0; i < len(body); i++ {
		b := body[i]
		if b != '\\' {
			out = append(out, b)
			continue
		}
		i++
		if i >= len(body) {
			return nil, false
		}
		switch body[i] {
		case '"', '\\', '/':
			out = append(out, body[i])
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			if i+4 >= len(body) {
				return nil, false
			}
			r, ok := decodeJSONHex4(body[i+1 : i+5])
			if !ok {
				return nil, false
			}
			i += 4
			if utf16.IsSurrogate(r) {
				if r <= 0xDBFF && i+7 <= len(body) && body[i+1] == '\\' && body[i+2] == 'u' {
					if r2, ok := decodeJSONHex4(body[i+3 : i+7]); ok && r2 >= 0xDC00 && r2 <= 0xDFFF {
						r = utf16.DecodeRune(r, r2)
						i += 6
					} else {
						r = utf8.RuneError
					}
				} else {
					r = utf8.RuneError
				}
			}
			out = utf8.AppendRune(out, r)
		default:
			return nil, false
		}
	}
	return out, true
}

// decodeJSONHex4 reads exactly four hexadecimal digits into a rune.
func decodeJSONHex4(b []byte) (rune, bool) {
	if len(b) < 4 {
		return 0, false
	}
	var r rune
	for _, c := range b[:4] {
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

// appendJSONString appends a JSON string token containing s. It escapes only
// bytes JSON requires, so raw invalid UTF-8 in s survives and HTML characters
// are not rewritten.
func appendJSONString(dst, s []byte) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	for _, b := range s {
		switch b {
		case '"', '\\':
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
			if b < 0x20 {
				dst = append(dst, '\\', 'u', '0', '0', hex[b>>4], hex[b&0xf])
			} else {
				dst = append(dst, b)
			}
		}
	}
	return append(dst, '"')
}

// appendJSONObject serializes fields with sorted keys, writing each value
// token verbatim.
func appendJSONObject(dst []byte, fields map[string]json.RawMessage) []byte {
	dst = append(dst, '{')
	for i, k := range sortedFieldKeys(fields) {
		if i > 0 {
			dst = append(dst, ',')
		}
		dst = appendJSONString(dst, []byte(k))
		dst = append(dst, ':')
		dst = append(dst, fields[k]...)
	}
	return append(dst, '}')
}

// sortedFieldKeys returns the map's keys in deterministic order.
func sortedFieldKeys(fields map[string]json.RawMessage) []string {
	return slices.Sorted(maps.Keys(fields))
}

// lastIndexFoldASCII returns the last byte index of sub in b, comparing
// case-insensitively without changing byte offsets. Seeking the original
// bytes avoids the index/length mismatch of lowercasing first. bytes.EqualFold
// is Unicode-aware; the fixed ASCII closing tag's letters have no non-ASCII
// simple-fold equivalents that could match it.
func lastIndexFoldASCII(b []byte, sub string) int {
	n := len(sub)
	needle := []byte(sub)
	for i := len(b) - n; i >= 0; i-- {
		if bytes.EqualFold(b[i:i+n], needle) {
			return i
		}
	}
	return -1
}
