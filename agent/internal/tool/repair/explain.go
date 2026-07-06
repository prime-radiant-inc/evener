package repair

import (
	"fmt"
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

// ExplainJSONError renders coaching for arguments still unparseable after RepairJSON.
func ExplainJSONError(toolName string, params map[string]any, parseErr string) string {
	return fmt.Sprintf("%s: arguments were not valid JSON (%s). Send a single JSON object, e.g. %s",
		toolName, parseErr, minimalExample(params))
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
	req := asStringSlice(params["required"])
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
