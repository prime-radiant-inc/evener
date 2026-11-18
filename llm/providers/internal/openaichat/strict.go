package openaichat

import "sort"

// StrictifyJSONSchema returns a deep copy of a tool parameter schema
// rewritten for OpenAI strict mode: every object gets
// additionalProperties: false, a properties map, and required = every
// property (sorted); arrays and anyOf/oneOf/allOf recurse. It runs only
// when Caps.StrictTools is set (spec §8.2), because the rewrite is not
// reversible by pruning strict afterwards.
//
// The rewrite force-requires every property, so callers must gate it on
// StrictExpressible: a schema that leaves a declared property optional
// cannot be projected into strict mode without making that argument
// mandatory (#6200).
func StrictifyJSONSchema(in map[string]any) map[string]any {
	// Best-effort deep copy + strictification for OpenAI tool schemas.
	// This intentionally handles only the constructs we emit (object/array) and is safe to
	// apply repeatedly (idempotent for our shapes).
	cp := deepCopyAny(in).(map[string]any)
	strictifyJSONSchemaInPlace(cp)
	return cp
}

// StrictExpressible reports whether schema can be projected into OpenAI
// strict mode without changing which arguments are optional.
//
// Strict mode requires every property of every object to be present and
// listed in required. StrictifyJSONSchema enforces that by force-requiring
// every property, which is lossless only for a schema whose every object
// already lists all of its properties as required. For any other schema the
// projection makes a documented-optional argument mandatory, so a caller
// must send the tool non-strict instead (#6200).
//
// The walk mirrors StrictifyJSONSchema's reach exactly: object properties,
// array items, and anyOf/oneOf/allOf subschemas.
func StrictExpressible(schema map[string]any) bool {
	return strictlyExpressibleNode(schema)
}

func strictlyExpressibleNode(m map[string]any) bool {
	if m == nil {
		return true
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "object":
		props, _ := m["properties"].(map[string]any)
		if len(props) > 0 && !allPropertiesRequired(props, m["required"]) {
			return false
		}
		for _, child := range props {
			if childMap, ok := child.(map[string]any); ok && !strictlyExpressibleNode(childMap) {
				return false
			}
		}
	case "array":
		if items, ok := m["items"].(map[string]any); ok && !strictlyExpressibleNode(items) {
			return false
		}
	}
	// The rewrite strictifies combinators whatever the node's own type, so a
	for _, comb := range []string{"anyOf", "oneOf", "allOf"} {
		raw, ok := m[comb]
		if !ok || raw == nil {
			continue
		}
		arr, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, it := range arr {
			if child, ok := it.(map[string]any); ok && !strictlyExpressibleNode(child) {
				return false
			}
		}
	}
	return true
}

// allPropertiesRequired reports whether required names every property. The
// required list reaches this package as []string from the builtin builders and
// as []any from a decoded MCP or provider schema, so both are accepted.
func allPropertiesRequired(props map[string]any, required any) bool {
	switch list := required.(type) {
	case []string:
		seen := make(map[string]bool, len(list))
		for _, name := range list {
			seen[name] = true
		}
		for name := range props {
			if !seen[name] {
				return false
			}
		}
	case []any:
		seen := make(map[string]bool, len(list))
		for _, v := range list {
			if name, ok := v.(string); ok {
				seen[name] = true
			}
		}
		for name := range props {
			if !seen[name] {
				return false
			}
		}
	default:
		return false
	}
	return true
}

func strictifyJSONSchemaInPlace(m map[string]any) {
	if m == nil {
		return
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "object":
		// OpenAI strict mode requires this be present and false.
		m["additionalProperties"] = false

		props, _ := m["properties"].(map[string]any)
		if props == nil {
			props = map[string]any{}
			m["properties"] = props
		}
		// Required must include all properties keys (even for "optional" fields).
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		m["required"] = keys

		// Recurse into property schemas.
		for _, k := range keys {
			if child, ok := props[k].(map[string]any); ok {
				strictifyJSONSchemaInPlace(child)
			}
		}
	case "array":
		if items, ok := m["items"].(map[string]any); ok {
			strictifyJSONSchemaInPlace(items)
		}
	}

	// If the schema uses combinators, strictify any subschemas we can find.
	for _, comb := range []string{"anyOf", "oneOf", "allOf"} {
		raw, ok := m[comb]
		if !ok || raw == nil {
			continue
		}
		arr, ok := raw.([]any)
		if !ok {
			continue
		}
		for _, it := range arr {
			if child, ok := it.(map[string]any); ok {
				strictifyJSONSchemaInPlace(child)
			}
		}
	}
}

func deepCopyAny(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = deepCopyAny(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = deepCopyAny(x[i])
		}
		return out
	default:
		return v
	}
}
