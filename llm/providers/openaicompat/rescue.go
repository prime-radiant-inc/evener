package openaicompat

import (
	"encoding/json"
	"regexp"
	"strings"
)

// claudeXMLParamOpenRE matches the OPEN of an Anthropic-style <parameter name="KEY"> tag.
// Some models (notably MiniMax M2.7 via OpenRouter) emit Claude/MiniMax XML
// tool-call syntax inside JSON tool arguments when they revert from JSON to
// XML mid-generation. Closing </parameter> tags are often missing, so we scan
// opens and slice to the next open or end-of-string.
//
// Pattern details (adapted from zeroclaw-labs/zeroclaw PR #1189 which solved
// the same parsing problem for MiniMax at the content level):
//   - Case-insensitive (`(?i)`) matches tag-case variation.
//   - Permissive attribute matching: other attributes can appear before `name=`.
//   - Both double and single quoted attribute values.
var claudeXMLParamOpenRE = regexp.MustCompile(
	`(?i)<parameter\b[^>]*\bname\s*=\s*(?:"([^"]+)"|'([^']+)')[^>]*>`,
)

// rescueClaudeXMLArgs attempts to recover tool call arguments when a model
// mixes Claude-style XML tool call syntax into the JSON arguments field.
//
// Examples:
//
//	Input:  {"action":"append\">\n<parameter name=\"tasks\">[{...}]</parameter>"}
//	Output: {"action":"append","tasks":[{...}]}
//
//	Input:  {"action":"update\">\n<parameter name=\"updates\">[{...}]"}   (no close tag)
//	Output: {"action":"update","updates":[{...}]}
//
// If the input is already valid and contains no XML syntax, it is returned
// unchanged. If rescue is not possible, the original input is returned so the
// usual schema-validation error path can surface the problem.
func rescueClaudeXMLArgs(raw string) string {
	if raw == "" {
		return raw
	}
	// The raw JSON is escaped — `<parameter name=\"KEY\">` — so we cannot
	// detect the pattern in the raw bytes. Parse first, then check the
	// unescaped string values.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return raw
	}
	// Second-order rescue: for any top-level field whose value is a string
	// that looks like a JSON array or object (starts with `[` or `{`), try
	// parsing it. Models sometimes emit JSON-encoded strings for array/object
	// fields instead of the parsed value. This is a separate bug from the
	// Claude-XML corruption but benefits from the same rescue path.
	parsedAnyJSONString := false
	for k, v := range parsed {
		s, ok := v.(string)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(s)
		if !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
			continue
		}
		var asJSON any
		if err := json.Unmarshal([]byte(trimmed), &asJSON); err == nil {
			// Only replace if the parsed result is a composite type (array/object),
			// not a scalar — avoid mangling genuine strings that happen to start
			// with `[` or `{`.
			switch asJSON.(type) {
			case []any, map[string]any:
				parsed[k] = asJSON
				parsedAnyJSONString = true
			}
		}
	}

	// Quick exit: if no string value contains an XML <parameter ... tag,
	// nothing more to rescue. Case-insensitive because models sometimes
	// emit uppercase tag names.
	hasXML := false
	for _, v := range parsed {
		if s, ok := v.(string); ok {
			lower := strings.ToLower(s)
			if strings.Contains(lower, "<parameter ") || strings.Contains(lower, "<parameter\t") || strings.Contains(lower, "<parameter\n") {
				hasXML = true
				break
			}
		}
	}
	if !hasXML {
		if parsedAnyJSONString {
			if out, err := json.Marshal(parsed); err == nil {
				return string(out)
			}
		}
		return raw
	}

	// For each string value, check whether it carries a `"><parameter...` tail.
	// If so, split at the `">` boundary: the prefix is the real value, the tail
	// holds the <parameter> blocks whose name/value pairs should become siblings.
	changed := false
	extracted := make(map[string]any)
	for k, v := range parsed {
		s, ok := v.(string)
		if !ok {
			continue
		}
		// Find where the XML <parameter starts (case-insensitive, tolerant of
		// tab/newline after "parameter"). Use regex so we can find the exact
		// offset and let the caller split there.
		openMatch := claudeXMLParamOpenRE.FindStringIndex(s)
		if openMatch == nil {
			continue
		}
		// Split at the first `">` before the parameter block. If there's no
		// `">` (value not terminated with a quote-close-bracket), fall back to
		// splitting at the `<` of the parameter tag.
		idx := strings.Index(s[:openMatch[0]], `">`)
		if idx < 0 {
			idx = openMatch[0]
		}
		cleanValue := s[:idx]
		parsed[k] = cleanValue
		changed = true

		tail := s[idx:]
		// Find all <parameter name="KEY"> opens. Between consecutive opens (or
		// from an open to end-of-string), the value is the parameter content.
		// The regex has two alternations for the name: double-quoted (group 1)
		// and single-quoted (group 2). Submatch indices are:
		//   m[0..1] = whole match
		//   m[2..3] = double-quoted name (or -1 if single-quoted)
		//   m[4..5] = single-quoted name (or -1 if double-quoted)
		opens := claudeXMLParamOpenRE.FindAllStringSubmatchIndex(tail, -1)
		for i, m := range opens {
			var paramName string
			if m[2] >= 0 {
				paramName = tail[m[2]:m[3]]
			} else if m[4] >= 0 {
				paramName = tail[m[4]:m[5]]
			}
			if paramName == "" {
				continue
			}
			valStart := m[1] // index just after the `>` of this open tag
			valEnd := len(tail)
			if i+1 < len(opens) {
				valEnd = opens[i+1][0]
			}
			paramValue := tail[valStart:valEnd]
			// Strip trailing </parameter> (case-insensitive) if present.
			lowerVal := strings.ToLower(paramValue)
			if cut := strings.LastIndex(lowerVal, "</parameter>"); cut >= 0 {
				paramValue = paramValue[:cut]
			}
			// Strip any leading/trailing whitespace and quotes that may remain.
			paramValue = strings.TrimSpace(paramValue)
			// If the parameter value is itself JSON (array/object/number/bool/null),
			// parse it. Otherwise keep as string.
			var asJSON any
			if err := json.Unmarshal([]byte(paramValue), &asJSON); err == nil {
				extracted[paramName] = asJSON
			} else {
				extracted[paramName] = paramValue
			}
		}
	}

	if !changed {
		return raw
	}

	// Merge extracted params into the top-level object. Parent fields take
	// precedence if there's a collision — the parent had the "primary" value.
	for k, v := range extracted {
		if _, exists := parsed[k]; !exists {
			parsed[k] = v
		}
	}

	out, err := json.Marshal(parsed)
	if err != nil {
		return raw
	}
	return string(out)
}
