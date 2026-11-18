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
//
// It matches a literal "object"/"array" string type only, and it resolves no
// $ref and does not descend into $defs or definitions. A node in one of those
// shapes keeps whatever it advertises, which StrictExpressible accounts for.
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
// The walk accepts only what the rewrite provably projects or what is already
// strict-valid, and answers inexpressible for everything it cannot verify. A
// false "inexpressible" costs strict mode on that one tool; a false
// "expressible" sends a strict tool whose optional argument is still optional,
// which OpenAI rejects for the whole request (#6200).
//
// Two shapes need care, because StrictifyJSONSchema does not touch them:
//
//   - A type list such as ["object", "null"] — JSON Schema's way of spelling a
//     nullable object, and OpenAI's documented way of spelling an optional
//     field under strict mode. The rewrite matches only a literal "object"
//     string type, so such a node keeps its own required list and its own
//     additionalProperties. It is certifiable only when it is strict-valid
//     already: every property required and additionalProperties false.
//   - $ref, and the $defs/definitions table it points into. The rewrite never
//     resolves a reference and never descends into the definitions, so a
//     schema using either cannot be certified: certifying the referenced
//     target would certify a projection the rewrite does not perform, and the
//     referenced object would reach OpenAI with its optional property intact.
//     Such a schema is refused rather than resolved.
//
// Likewise a node that declares properties or items under a type the rewrite
// does not recognise keeps its advertised optionality, so it is refused.
func StrictExpressible(schema map[string]any) bool {
	return strictlyExpressibleNode(schema)
}

func strictlyExpressibleNode(m map[string]any) bool {
	if m == nil {
		return true
	}
	if _, ok := m["$ref"]; ok {
		return false
	}
	for _, defsKey := range []string{"$defs", "definitions"} {
		if defs, ok := m[defsKey].(map[string]any); ok && len(defs) > 0 {
			return false
		}
	}
	isObject, isArray, listed := schemaTypeFlags(m["type"])
	switch {
	case isObject:
		props, _ := m["properties"].(map[string]any)
		if len(props) > 0 && !allPropertiesRequired(props, m["required"]) {
			return false
		}
		// A type list is not rewritten, so the node must already be
		// strict-valid: strict mode requires additionalProperties: false on
		// every object, and an absent key is not the same as false.
		if listed && m["additionalProperties"] != false {
			return false
		}
		for _, child := range props {
			if childMap, ok := child.(map[string]any); ok && !strictlyExpressibleNode(childMap) {
				return false
			}
		}
	case isArray:
		if items, ok := m["items"].(map[string]any); ok && !strictlyExpressibleNode(items) {
			return false
		}
	default:
		// An object- or array-shaped node the rewrite does not recognise: it
		// keeps whatever it advertises, so an optional property behind it stays
		// optional under strict mode.
		if _, has := m["properties"]; has {
			return false
		}
		if _, has := m["items"]; has {
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

// schemaTypeFlags reads a schema's type as either a string or a list of
// strings, which are the two shapes the builders and decoded provider schemas
// emit. listed reports that a list was given, which is how the caller tells a
// node the rewrite cannot match (it tests only a literal "object" string) from
// one it can.
func schemaTypeFlags(raw any) (isObject, isArray, listed bool) {
	switch t := raw.(type) {
	case string:
		return t == "object", t == "array", false
	case []string:
		for _, name := range t {
			switch name {
			case "object":
				isObject = true
			case "array":
				isArray = true
			}
		}
		return isObject, isArray, true
	case []any:
		for _, v := range t {
			if name, ok := v.(string); ok {
				switch name {
				case "object":
					isObject = true
				case "array":
					isArray = true
				}
			}
		}
		return isObject, isArray, true
	default:
		return false, false, false
	}
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
