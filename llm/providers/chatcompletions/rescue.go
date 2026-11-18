package chatcompletions

import (
	"bytes"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v5"
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

type chatArgumentRepair struct{}

// RepairToolArguments first recovers fields whose own schema rejects their
// supplied strings. A literal string may carry a missing sibling only when a
// single explicit parameter tail produces a complete, schema-valid call.
func (chatArgumentRepair) RepairToolArguments(raw string, schema *jsonschema.Schema) string {
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &fields) != nil || fields == nil {
		return raw
	}
	changed := repairRejectedChatFields(fields, schema)
	if proposal := recoverMissingXMLArgument(fields, schema); proposal != "" {
		return proposal
	}
	if changed {
		return string(appendJSONObject(nil, fields))
	}
	return raw
}

func recoverMissingXMLArgument(fields map[string]json.RawMessage, schema *jsonschema.Schema) string {
	if !slices.ContainsFunc(schema.Required, func(name string) bool { return fields[name] == nil }) {
		return ""
	}
	var candidate string
	for field, token := range fields {
		content, ok := decodeJSONStringContent(token)
		if !ok || schema.Properties[field] == nil || !claudeXMLParamHintRE.Match(content) {
			continue
		}
		head, params, explicit := splitXMLParameterTail(content)
		if !explicit {
			continue
		}
		var addsRequired, conflict bool
		for name := range params {
			if fields[name] != nil {
				conflict = true
				break
			}
			addsRequired = addsRequired || slices.Contains(schema.Required, name)
		}
		if conflict || !addsRequired {
			continue
		}
		// The split may use only absent siblings. Keep all other literal
		// arguments exactly as the model supplied them.
		maps.Copy(params, fields)
		params[field] = appendJSONString(nil, head)
		proposal := appendJSONObject(nil, params)
		var args map[string]any
		if json.Unmarshal(proposal, &args) != nil || schema.Validate(args) != nil {
			continue
		}
		if candidate != "" {
			return ""
		}
		candidate = string(proposal)
	}
	return candidate
}

// repairRejectedChatFields attempts to recover tool call arguments when a model
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
// Encoded arrays and objects also qualify when the field rejects the string.
func repairRejectedChatFields(fields map[string]json.RawMessage, schema *jsonschema.Schema) bool {
	rejected := func(field string, value []byte) bool {
		property := schema.Properties[field]
		return property != nil && property.Validate(string(value)) != nil
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
		if !rejected(k, content) {
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

	// Recover rejected fields independently of XML in accepted text fields.
	hasXML := false
	for k, v := range fields {
		if content, ok := decodeJSONStringContent(v); ok && claudeXMLParamHintRE.Match(content) && rejected(k, content) {
			hasXML = true
			break
		}
	}
	if !hasXML {
		return parsedAnyJSONString
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
		if !ok || !rejected(k, content) {
			continue
		}
		head, params, _ := splitXMLParameterTail(content)
		if len(params) == 0 {
			continue
		}
		fields[k] = appendJSONString(nil, head)
		maps.Copy(extracted, params)
		changed = true
	}

	// Merge extracted params into the top-level object. Parent fields take
	// precedence if there's a collision — the parent had the "primary" value.
	for k, v := range extracted {
		if _, exists := fields[k]; !exists {
			fields[k] = v
		}
	}

	return changed
}

// A rejected field can split at its first parameter tag. Splitting an accepted
// string requires an explicit quote terminator after a plain value, immediately
// followed by parameter tags with distinct names.
func splitXMLParameterTail(content []byte) (head []byte, params map[string]json.RawMessage, explicit bool) {
	tags := claudeXMLParamOpenRE.FindAllSubmatchIndex(content, -1)
	if len(tags) == 0 {
		return nil, nil, false
	}
	head, gap, found := bytes.Cut(content[:tags[0][0]], []byte(`">`))
	explicit = found && len(bytes.TrimSpace(head)) != 0 && bytes.IndexByte(head, '<') < 0 && len(bytes.TrimSpace(gap)) == 0
	params = make(map[string]json.RawMessage, len(tags))
	for i, tag := range tags {
		start, end := tag[2], tag[3] // double-quoted name
		if start < 0 {
			start, end = tag[4], tag[5] // single-quoted name
		}
		name := string(content[start:end])
		if params[name] != nil {
			explicit = false
		}
		end = len(content)
		if i+1 < len(tags) {
			end = tags[i+1][0]
		}
		value := content[tag[1]:end]
		if cut := lastIndexFoldASCII(value, claudeXMLParamCloseTag); cut >= 0 {
			if len(bytes.TrimSpace(value[cut+len(claudeXMLParamCloseTag):])) != 0 {
				explicit = false
			}
			value = value[:cut]
		}
		value = bytes.TrimSpace(value)
		// Keep JSON parameter tokens, including their original numeric spelling.
		// Out-of-range numbers must remain strings for the tool-call decoder.
		var parsed any
		if json.Unmarshal(value, &parsed) == nil {
			params[name] = value
		} else {
			params[name] = appendJSONString(nil, value)
		}
	}
	return head, params, explicit
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
