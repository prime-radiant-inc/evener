package agent

import (
	"fmt"
	"strconv"
	"strings"

	"primeradiant.com/serf/llm"
)

// salvageText renders a partial response's recoverable output: text parts
// verbatim, in order; then for each incomplete tool call, a marker block:
//
//	[incomplete tool call: <name> — this call never executed]
//	<field>: <extracted string value>
//
// Returns "" when nothing recoverable. Reasoning parts are ignored — a dying
// stream's thinking is never salvaged, only its text and any tool-call
// arguments in flight.
func salvageText(partial *llm.Response) string {
	if partial == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(partial.Text())
	for _, tc := range partial.ToolCalls() {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "[incomplete tool call: %s — this call never executed]", tc.Name)
		for _, field := range partialJSONStringFields(string(tc.Arguments)) {
			b.WriteString("\n")
			fmt.Fprintf(&b, "%s: %s", field.Key, field.Value)
		}
	}
	return b.String()
}

// partialJSONStringFields extracts ALL top-level string-valued fields from
// possibly-truncated JSON object text, in encounter order. Non-string
// top-level values (numbers, bools, null, nested objects/arrays) are skipped
// rather than extracted. A key or value cut off mid-scan ends extraction at
// the last complete field, preserving the unterminated tail of a value that
// was mid-string when the stream died.
//
// Generalizes partialJSONStringField (agent/session_stream.go), which the
// streaming preview path still uses to pull a single named field; both share
// the string-body scanner in scanPartialJSONStringBody.
func partialJSONStringFields(raw string) []struct{ Key, Value string } {
	var fields []struct{ Key, Value string }
	rest := strings.TrimLeft(raw, " \t\r\n")
	if !strings.HasPrefix(rest, "{") {
		return fields
	}
	rest = rest[1:]
	for {
		rest = strings.TrimLeft(rest, " \t\r\n")
		if rest == "" || rest[0] == '}' {
			return fields
		}
		if rest[0] != '"' {
			return fields
		}
		key, afterKey, terminated := scanPartialJSONStringBody(rest[1:])
		if !terminated {
			return fields
		}
		rest = strings.TrimLeft(afterKey, " \t\r\n")
		if rest == "" || rest[0] != ':' {
			return fields
		}
		rest = strings.TrimLeft(rest[1:], " \t\r\n")
		if rest == "" {
			return fields
		}
		if rest[0] == '"' {
			value, afterValue, _ := scanPartialJSONStringBody(rest[1:])
			fields = append(fields, struct{ Key, Value string }{key, value})
			rest = afterValue
			if rest == "" {
				return fields
			}
		} else {
			var ok bool
			rest, ok = skipPartialJSONValue(rest)
			if !ok {
				return fields
			}
		}
		rest = strings.TrimLeft(rest, " \t\r\n")
		if rest == "" {
			return fields
		}
		switch rest[0] {
		case ',':
			rest = rest[1:]
		default:
			return fields
		}
	}
}

// scanPartialJSONStringBody decodes a JSON string body (the bytes
// immediately following its opening quote) up to the closing quote,
// resolving \", \\, \/, \uXXXX (with surrogate-pair handling via
// unquoteJSONUnicodeEscape), and other standard escapes via
// strconv.UnquoteChar. If the body is truncated before a closing quote — the
// stream died mid-string — it returns everything decoded so far,
// terminated=false, and no remainder, since nothing after an unterminated
// string can be trusted.
func scanPartialJSONStringBody(rest string) (value, remaining string, terminated bool) {
	var b strings.Builder
	for len(rest) > 0 {
		ch := rest[0]
		if ch == '"' {
			return b.String(), rest[1:], true
		}
		if ch == '\\' {
			if len(rest) >= 2 && rest[1] == '/' {
				b.WriteByte('/')
				rest = rest[2:]
				continue
			}
			if strings.HasPrefix(rest, `\u`) {
				r, tail, ok := unquoteJSONUnicodeEscape(rest)
				if !ok {
					return b.String(), "", false
				}
				b.WriteRune(r)
				rest = tail
				continue
			}
			r, _, tail, err := strconv.UnquoteChar(rest, '"')
			if err != nil {
				return b.String(), "", false
			}
			b.WriteRune(r)
			rest = tail
			continue
		}
		b.WriteByte(ch)
		rest = rest[1:]
	}
	return b.String(), "", false
}

// skipPartialJSONValue advances past one JSON value (object, array, number,
// bool, or null) that raw begins with, returning the text after it. It
// reports ok=false when the value is truncated before its end can be
// determined, so the caller stops rather than misreading trailing bytes as a
// new field.
func skipPartialJSONValue(raw string) (rest string, ok bool) {
	switch raw[0] {
	case '{', '[':
		open, closeCh := raw[0], byte('}')
		if open == '[' {
			closeCh = ']'
		}
		depth := 1
		i := 1
		for i < len(raw) {
			switch raw[i] {
			case '"':
				_, tail, terminated := scanPartialJSONStringBody(raw[i+1:])
				if !terminated {
					return "", false
				}
				i = len(raw) - len(tail)
				continue
			case open:
				depth++
			case closeCh:
				depth--
				if depth == 0 {
					return raw[i+1:], true
				}
			}
			i++
		}
		return "", false
	default:
		i := 0
		for i < len(raw) && raw[i] != ',' && raw[i] != '}' && raw[i] != ']' &&
			raw[i] != ' ' && raw[i] != '\t' && raw[i] != '\r' && raw[i] != '\n' {
			i++
		}
		if i == len(raw) {
			return "", false
		}
		return raw[i:], true
	}
}
