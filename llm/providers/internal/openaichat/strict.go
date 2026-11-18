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
// mandatory.
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
// must send the tool non-strict instead.
//
// The rule that decides it is modeledKeywords: a schema is certified only when
// every keyword in it is one StrictifyJSONSchema either rewrites or provably
// leaves alone. A keyword outside that set is refused wherever it appears,
// rather than modelled case by case — combinators, dependentRequired,
// prefixItems, patternProperties and the rest all constrain which keys an
// object may carry, so closing the object can silently start rejecting calls
// the advertised schema accepted. Refusing the keyword keeps the tool's whole
// argument set, at the cost of strict mode on that one tool.
//
// Under that rule the walk still has to establish two things. A node the
// rewrite transforms gets its required list and additionalProperties from the
// projection, so what it advertised must already be equivalent: every property
// required, no undeclared key allowed, and required naming only declared keys.
// A node the rewrite never visits — including a nullable type list such as
// ["object", "null"], and any descendant below one — keeps exactly what it
// advertised, so it must already be strict-valid on its own terms.
//
// A container the walk cannot read is refused rather than trusted. properties
// and items reach this package either as decoded JSON (map[string]any) or as a
// Go caller's typed values (map[string]map[string]any). The rewrite
// type-asserts the same way, so an unreadable properties map would be replaced
// with an empty one, dropping every advertised argument.
func StrictExpressible(schema map[string]any) bool {
	return strictlyExpressibleNode(schema, true)
}

// strictlyExpressibleNode walks one node of a schema already known to use only
// modeled keywords. visited reports whether StrictifyJSONSchema will call into
// this node: the root is visited, and a property or items subschema is visited
// only when its parent is visited and carries the literal object or array type
// the rewrite matches.
func strictlyExpressibleNode(m map[string]any, visited bool) bool {
	if m == nil {
		return true
	}
	// The rewrite resolves no reference and never descends into a definitions
	// table, so a schema using either cannot be certified at any node, whether
	// or not the rewrite transforms it.
	if _, ok := m["$ref"]; ok {
		return false
	}
	for _, defsKey := range []string{"$defs", "definitions"} {
		if defs, ok := m[defsKey].(map[string]any); ok && len(defs) > 0 {
			return false
		}
	}
	literal := schemaLiteralType(m["type"])
	isObject, isArray := schemaTypeFlags(m["type"])
	// Only a visited node whose type is the literal string the rewrite matches
	// is transformed. Everything else keeps what it advertises.
	transformed := visited && (literal == "object" || literal == "array")
	// The rewrite descends into a node whether or not it transforms that node,
	// so a keyword it does not model is unsound at any position — not only on
	// the objects it closes.
	if !keywordsModeled(m) {
		return false
	}
	// A type list may name both shapes at once, as in ["object", "array"].
	// Both are then constraints on the same node, and the rewrite — which
	// matches only a literal type string — transforms neither, so every one of
	// them has to hold as advertised. Checking them in one switch would walk
	// only the object side and leave the array side's items unverified.
	var props map[string]any
	if isObject {
		var ok bool
		props, ok = inspectProperties(m)
		if !ok {
			return false
		}
		if len(props) > 0 && !allPropertiesRequired(props, m["required"]) {
			return false
		}
		// Only the rewrite may supply additionalProperties: false. An object it
		// does not transform keeps the advertised value, and strict mode
		// requires false.
		if !transformed && m["additionalProperties"] != false {
			return false
		}
		// Where it does transform, the rewrite closes the object. That is
		// lossless only if the object did not allow other keys.
		if transformed && !allKeysDeclared(m) {
			return false
		}
		// The rewrite replaces required with exactly the declared property
		// names, so a requirement on a key the node never declares would be
		// dropped rather than projected.
		if transformed {
			for name := range requiredNamesOf(m["required"]) {
				if _, has := props[name]; !has {
					return false
				}
			}
		}
		for _, child := range props {
			childMap, ok := child.(map[string]any)
			if !ok {
				// The rewrite skips an unreadable child, so its contents are
				// never verified either.
				return false
			}
			if !strictlyExpressibleNode(childMap, visited && literal == "object") {
				return false
			}
		}
	}
	if isArray {
		if raw, has := m["items"]; has && raw != nil {
			items, ok := raw.(map[string]any)
			if !ok {
				return false
			}
			if !strictlyExpressibleNode(items, visited && literal == "array") {
				return false
			}
		}
	}
	if !isObject && !isArray {
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
	return true
}

// schemaLiteralType returns the node's type when it is written as a plain
// string, which is the only form StrictifyJSONSchema matches.
func schemaLiteralType(raw any) string {
	s, _ := raw.(string)
	return s
}

// inspectProperties returns a node's properties when they are in the shape the
// rewrite and this walk can read. ok is false when properties is present but
// unreadable, because the rewrite would replace such a value with an empty map
// and drop every advertised argument.
func inspectProperties(m map[string]any) (map[string]any, bool) {
	raw, has := m["properties"]
	if !has || raw == nil {
		return nil, true
	}
	props, ok := raw.(map[string]any)
	return props, ok
}

// schemaTypeFlags reads a schema's type as either a string or a list of
// strings, which are the two shapes the builders and decoded provider schemas
// emit.
func schemaTypeFlags(raw any) (isObject, isArray bool) {
	switch t := raw.(type) {
	case string:
		return t == "object", t == "array"
	case []string:
		for _, name := range t {
			switch name {
			case "object":
				isObject = true
			case "array":
				isArray = true
			}
		}
		return isObject, isArray
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
		return isObject, isArray
	default:
		return false, false
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

// A node the rewrite transforms may not permit keys it does not declare, so
// only an explicit additionalProperties: false is certifiable there.
//
// An absent keyword means the object is open and accepts any key, which the
// walk cannot tell apart from an explicitly permissive object: closing it to
// the declared properties removes every call that carried a key the schema
// never named. JSON Schema reads an omission as an open object, and this walk
// has to read it the same way rather than taking the rewrite's silence for a
// closed shape.
func allKeysDeclared(m map[string]any) bool {
	closed, ok := m["additionalProperties"].(bool)
	return ok && !closed
}

// requiredNamesOf reads a node's required list in either of the shapes the
// builders and decoded provider schemas use.
func requiredNamesOf(raw any) map[string]bool {
	var out map[string]bool
	switch list := raw.(type) {
	case []string:
		out = make(map[string]bool, len(list))
		for _, name := range list {
			out[name] = true
		}
	case []any:
		out = make(map[string]bool, len(list))
		for _, v := range list {
			if name, ok := v.(string); ok {
				out[name] = true
			}
		}
	}
	return out
}

// modeledKeywords lists every JSON Schema keyword StrictifyJSONSchema either
// rewrites or provably leaves alone.
//
// The list is the whole certification. A node the rewrite closes admits
// exactly the keys it declares, so a keyword whose effect depends on keys
// outside that set — or on the object staying open — cannot survive the
// projection. Rather than test those keywords one at a time, a node carrying
// one is refused strict mode outright and the tool is sent as advertised.
//
// Each entry says which of the two it is:
//
//   - rewritten: the projection supplies it, so whatever the schema advertised
//     for it is replaced. type, properties, required and
//     additionalProperties on an object; type and items on an array.
//     description, enum, default and the scalar bounds belong.
//
// Adding a keyword to the rewrite without adding it here makes the gate refuse
// schemas the rewrite does handle — a visible loss of strictness, not a silent
// unsoundness. Adding one here without teaching the rewrite about it would be
// the reverse, and is the mistake this list exists to prevent.
var modeledKeywords = map[string]bool{
	// Rewritten on objects and arrays.
	"type":                 true,
	"properties":           true,
	"required":             true,
	"additionalProperties": true,
	"items":                true,

	// Unaffected: annotations and scalar bounds, which closing an object
	// cannot narrow. These are the keywords a tool builder uses to describe a
	// value it accepts.
	"description": true,
	"enum":        true,
	"default":     true,
	"examples":    true,
	"title":       true,
	"const":       true,
	"format":      true,
	"pattern":     true,
	"minimum":     true,
	"maximum":     true,
	"minLength":   true,
	"maxLength":   true,
	"minItems":    true,
	"maxItems":    true,
	"uniqueItems": true,
}

// keywordsModeled reports whether every keyword on a node is one the
// projection accounts for.
//
// The check is unconditional rather than limited to the nodes the rewrite
// transforms, because the rewrite descends past the nodes it does not
// transform: an untyped wrapper carrying anyOf still has every branch closed,
// and a wrapper carrying dependentRequired or minProperties still constrains
// keys its own closure forbids. Refusing the keyword wherever it appears is
// what makes this a property of the schema rather than of one node's type.
func keywordsModeled(m map[string]any) bool {
	for keyword := range m {
		if !modeledKeywords[keyword] {
			return false
		}
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
