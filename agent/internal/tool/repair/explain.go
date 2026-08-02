package repair

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// ExplainSchemaError renders model-facing coaching for a call that failed
// validation and could not be repaired. offendingField, when non-empty, names
// the specific bad property; otherwise the message lists all required args.
func ExplainSchemaError(toolName string, params, args map[string]any, offendingField string) string {
	var b strings.Builder
	if offendingField != "" {
		if _, present := args[offendingField]; present {
			fmt.Fprintf(&b, "%s: argument %q has the wrong type or value.", toolName, offendingField)
		} else {
			fmt.Fprintf(&b, "%s: missing required argument %q.", toolName, offendingField)
		}
	} else {
		fmt.Fprintf(&b, "%s: arguments did not match the schema.", toolName)
	}
	req := requiredList(params)
	if len(req) > 0 {
		fmt.Fprintf(&b, "\nRequired arguments: %s.", strings.Join(req, ", "))
	}
	fmt.Fprintf(&b, "\nExample: %s", minimalExample(params))
	return b.String()
}

// ExplainJSONError renders coaching for arguments still unparseable after
// RepairJSON. raw is the argument text that failed to parse; when it is
// non-empty, an excerpt of the failing region is included so the model can
// see what it actually sent.
func ExplainJSONError(toolName string, params map[string]any, parseErr error, raw []byte) string {
	excerpt := parseExcerpt(parseErr, raw)
	if excerpt != "" {
		excerpt += " "
	}
	return fmt.Sprintf("%s: arguments were not valid JSON (%s). %sSend a single JSON object, e.g. %s",
		toolName, parseErr, excerpt, minimalExample(params))
}

// parseExcerpt renders the region of raw that failed parsing. A decoder syntax
// error shows a window around its one-based byte offset; an error that only
// carries EOF shows the tail, which is where the parser stopped. Returns ""
// when raw is empty.
func parseExcerpt(parseErr error, raw []byte) string {
	s := string(raw)
	if s == "" {
		return ""
	}
	const window = 120
	if isUnexpectedJSONEOF(parseErr) {
		tail := s
		if len(tail) > window {
			tail = "..." + tail[len(tail)-window:]
		}
		return fmt.Sprintf("Your input ended with: %q.", tail)
	}
	var se *json.SyntaxError
	if errors.As(parseErr, &se) && se.Offset > 0 {
		position := se.Offset - 1 // SyntaxError.Offset is one-based.
		if se.Error() == "unexpected end of JSON input" {
			// The decoder stopped after the final byte; there is no offending
			// byte to place the marker before, so keep the raw suffix contiguous.
			position = int64(len(s))
		}
		if position > int64(len(s)) {
			position = int64(len(s))
		}
		off := int(position)
		start := max(off-window/2, 0)
		end := min(off+window/2, len(s))
		prefix, suffix := "", ""
		if start > 0 {
			prefix = "..."
		}
		if end < len(s) {
			suffix = "..."
		}
		return fmt.Sprintf("Failing input near byte %d: %q.",
			se.Offset, prefix+s[start:off]+">>>"+s[off:end]+suffix)
	}
	tail := s
	if len(tail) > window {
		tail = "..." + tail[len(tail)-window:]
	}
	return fmt.Sprintf("Your input ended with: %q.", tail)
}

func isUnexpectedJSONEOF(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return false
}

func requiredList(params map[string]any) []string {
	props := schemaProps(params)
	var out []string
	for _, r := range asStringSlice(params["required"]) {
		typ := ""
		if p, ok := props[r].(map[string]any); ok {
			typ, _ = p["type"].(string)
		}
		if typ != "" {
			out = append(out, fmt.Sprintf("%s (%s)", r, typ))
		} else {
			out = append(out, r)
		}
	}
	return out
}

func minimalExample(params map[string]any) string {
	props := schemaProps(params)
	req := append([]string(nil), asStringSlice(params["required"])...)
	sort.Strings(req)
	parts := make([]string, 0, len(req))
	for _, r := range req {
		typ := ""
		if p, ok := props[r].(map[string]any); ok {
			typ, _ = p["type"].(string)
		}
		parts = append(parts, fmt.Sprintf("%q: %s", r, examplePlaceholder(typ)))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func examplePlaceholder(typ string) string {
	switch typ {
	case "integer", "number":
		return "0"
	case "boolean":
		return "false"
	case "array":
		return "[]"
	case "object":
		return "{}"
	default:
		return `"..."`
	}
}

func asStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}
