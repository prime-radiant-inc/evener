package repair

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// ExplainSchemaError renders model-facing coaching for a call that failed
// validation and could not be repaired. instanceLocation, when non-empty, is
// the deepest cause's JSON-Pointer-style path into args (e.g. "old_string",
// "updates/0", "questions/0/header" — as produced by offendingField);
// otherwise the message lists all required args at the top level.
//
// The path is walked against params (schema) and args (instance) in lockstep
// to find the container the error is really about, so a nested failure
// reports the real field, its location, and that container's required list
// — not a misleading top-level guess. Any segment the walk can't resolve
// (malformed path, schema shape mismatch) falls back to treating the whole
// original string as a single top-level field name.
func ExplainSchemaError(toolName string, params, args map[string]any, instanceLocation string) string {
	var b strings.Builder

	containerSchema := params
	containerPath := ""
	field := ""
	present := false
	haveField := false

	if instanceLocation != "" {
		haveField = true
		if cs, cp, f, pres, ok := resolveSchemaErrorContainer(params, args, instanceLocation); ok {
			containerSchema, containerPath, field, present = cs, cp, f, pres
		} else {
			// Fall back: treat the whole original path string as a single
			// top-level field name (today's flat behavior).
			containerSchema, containerPath, field = params, "", instanceLocation
			_, present = args[instanceLocation]
		}
	}

	switch {
	case !haveField:
		fmt.Fprintf(&b, "%s: arguments did not match the schema.", toolName)
	case present:
		fullPath := field
		if containerPath != "" {
			fullPath = containerPath + "." + field
		}
		fmt.Fprintf(&b, "%s: argument %q has the wrong type or value.", toolName, fullPath)
	case containerPath == "":
		fmt.Fprintf(&b, "%s: missing required argument %q.", toolName, field)
	default:
		fmt.Fprintf(&b, "%s: missing required argument %q in %s.", toolName, field, containerPath)
	}

	req := requiredList(containerSchema)
	if len(req) > 0 {
		if containerPath == "" {
			fmt.Fprintf(&b, "\nRequired arguments: %s.", strings.Join(req, ", "))
		} else {
			fmt.Fprintf(&b, "\nRequired arguments in %s: %s.", containerPath, strings.Join(req, ", "))
		}
	}
	fmt.Fprintf(&b, "\nExample: %s", minimalExample(params))
	return b.String()
}

// resolveSchemaErrorContainer walks instanceLocation (split on "/") against
// params/args in lockstep, alternating object-property steps (schema
// properties[seg]) and array-index steps (schema items, when the current
// schema node is type:array and seg parses as an integer).
//
// Two shapes come out the other end:
//   - The final segment resolves as a declared property of its parent
//     schema node (e.g. ".../header"): the field is that property, its
//     container is the parent, and containerPath is the path to the parent
//     ("questions[0]" for "questions/0/header"). present reports whether the
//     field's value is set in the parent instance — the caller uses that to
//     choose "wrong type" vs "missing required" (with field rewritten to the
//     full path for the "wrong type" case, done by the caller).
//   - The final segment is an array index with nothing after it (e.g.
//     "updates/0"): container and the node the error is actually about are
//     the same object — the diff is which of that item's required
//     properties is absent from the instance. field is the first missing
//     one (schema/required-list order); present is always false.
//
// ok is false when any step can't be resolved against the schema (a
// malformed path, or a schema shape that doesn't match it) — the caller
// falls back to flat top-level behavior in that case. Never panics.
func resolveSchemaErrorContainer(params, args map[string]any, instanceLocation string) (containerSchema map[string]any, containerPath, field string, present, ok bool) {
	segs := strings.Split(instanceLocation, "/")

	schemas := make([]map[string]any, len(segs)+1)
	insts := make([]any, len(segs)+1)
	kinds := make([]bool, len(segs)) // true = array-index step

	schemas[0] = params
	insts[0] = any(args)

	for i, seg := range segs {
		cur := schemas[i]
		if cur == nil {
			return nil, "", "", false, false
		}
		if idx, isIdx := arrayIndex(seg); isIdx && schemaIsArray(cur) {
			items, _ := cur["items"].(map[string]any)
			if items == nil {
				return nil, "", "", false, false
			}
			schemas[i+1] = items
			kinds[i] = true
			if arr, isArr := insts[i].([]any); isArr && idx >= 0 && idx < len(arr) {
				insts[i+1] = arr[idx]
			}
			continue
		}
		prop, declared := schemaProps(cur)[seg].(map[string]any)
		if !declared {
			return nil, "", "", false, false
		}
		schemas[i+1] = prop
		if m, isMap := insts[i].(map[string]any); isMap {
			insts[i+1] = m[seg]
		}
	}

	n := len(segs)
	if kinds[n-1] {
		// Terminal array-index step: the resolved item itself is the
		// container — find its first missing required property.
		item := schemas[n]
		itemInst, _ := insts[n].(map[string]any)
		for _, r := range requiredNames(item) {
			if _, present := itemInst[r]; !present {
				return item, formatPath(segs), r, false, true
			}
		}
		return nil, "", "", false, false
	}

	// Terminal property step: the parent is the container.
	parent := schemas[n-1]
	name := segs[n-1]
	parentInst, _ := insts[n-1].(map[string]any)
	_, isPresent := parentInst[name]
	return parent, formatPath(segs[:n-1]), name, isPresent, true
}

// arrayIndex parses seg as a non-negative decimal array index.
func arrayIndex(seg string) (int, bool) {
	if seg == "" {
		return 0, false
	}
	for _, r := range seg {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(seg)
	if err != nil {
		return 0, false
	}
	return n, true
}

func schemaIsArray(schema map[string]any) bool {
	t, _ := schema["type"].(string)
	return t == "array"
}

// requiredNames returns a schema's "required" list as strings, in schema
// order (unlike requiredList, it doesn't annotate types — callers that need
// the diff order call this; callers that render the coaching text call
// requiredList).
func requiredNames(schema map[string]any) []string {
	return asStringSlice(schema["required"])
}

// formatPath renders a split instance-location path for display: "[N]" for
// array-index segments, ".name" for object-property segments (no leading dot
// on the first segment). E.g. ["updates", "0"] -> "updates[0]";
// ["questions", "0", "header"] -> "questions[0].header".
func formatPath(segs []string) string {
	var b strings.Builder
	for i, seg := range segs {
		if _, isIdx := arrayIndex(seg); isIdx {
			b.WriteString("[" + seg + "]")
			continue
		}
		if i > 0 {
			b.WriteString(".")
		}
		b.WriteString(seg)
	}
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

// ExplainTruncatedCall renders the prevalidation error for a tool call whose
// argument stream was cut off because the response hit the output-token
// limit. Distinct from ExplainJSONError on purpose: the JSON is incomplete,
// not malformed, and coaching the model about syntax sends it debugging a
// problem it doesn't have.
func ExplainTruncatedCall(toolName string) string {
	return toolName + ": tool call truncated — the response hit the output-token limit " +
		"before the arguments finished streaming. The call was NOT executed and the lost " +
		"content cannot be recovered. Re-issue the work in smaller pieces (e.g. write the " +
		"file in sections across multiple calls)."
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
