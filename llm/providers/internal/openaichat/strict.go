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
// The walk accepts only what the rewrite provably projects or what is already
// strict-valid, and answers inexpressible for everything it cannot verify. A
// false "inexpressible" costs strict mode on that one tool; a false
// "expressible" sends a strict tool whose optional argument is still optional,
// which OpenAI rejects for the whole request.
//
// Two shapes need care, because StrictifyJSONSchema does not touch them:
//
//   - A type list such as ["object", "null"] — JSON Schema's way of spelling a
//     nullable object, and OpenAI's documented way of spelling an optional
//     field under strict mode. The rewrite matches only a literal "object"
//     string type, so such a node keeps its own required list and its own
//     additionalProperties, and everything below it is left untouched too.
//   - $ref, and the $defs/definitions table it points into. The rewrite never
//     resolves a reference and never descends into the definitions, so a
//     schema using either cannot be certified: certifying the referenced
//     target would certify a projection the rewrite does not perform, and the
//     referenced object would reach OpenAI with its optional property intact.
//     Such a schema is refused rather than resolved.
//
// So the walk tracks whether the rewrite will visit a node. A visited node
// with a literal "object" or "array" string type is transformed, and the
// projection supplies its required list and additionalProperties. Any other
// node — one the rewrite never visits, or one whose type list it does not
// match — keeps exactly what it advertises, so it must already satisfy strict
// mode itself: every property required and additionalProperties false. The
// same rule applies to every descendant of such a node, since nothing below it
// is rewritten either.
//
// Finally, a container the walk cannot read is refused rather than trusted.
// properties, items and the combinator lists reach this package either as
// decoded JSON (map[string]any and []any) or as a Go caller's typed values
// (map[string]map[string]any, []map[string]any). The rewrite type-asserts the
// same way, so an unreadable properties map would be replaced with an empty
// one — dropping every advertised argument — and an unreadable combinator list
// would keep its optional properties. Neither is certifiable.
func StrictExpressible(schema map[string]any) bool {
	return strictlyExpressibleNode(schema, true, nil)
}

// strictlyExpressibleNode walks one node. visited reports whether
// StrictifyJSONSchema will call into this node: the root is visited, a
// property or items subschema is visited only when its parent is visited and
// carries the literal object or array type the rewrite matches, and combinator
// branches are visited whenever the node itself is.
//
// inherited carries the enclosing node's properties when this node is one of
// its combinator branches, because the rewrite closes a branch that declares
// no properties of its own against them.
func strictlyExpressibleNode(m map[string]any, visited bool, inherited map[string]any) bool {
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
	literal := schemaLiteralType(m["type"])
	isObject, isArray := schemaTypeFlags(m["type"])
	// Only a visited node whose type is the literal string the rewrite matches
	// is transformed. Everything else keeps what it advertises.
	transformed := visited && (literal == "object" || literal == "array")
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
		//
		// A transformed branch that declares no properties is closed against
		// the enclosing node's, so those are the keys it may require.
		declared := props
		if transformed && len(props) == 0 {
			declared = inherited
		}
		if transformed {
			for name := range requiredNamesOf(m["required"]) {
				if _, has := declared[name]; !has {
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
			if !strictlyExpressibleNode(childMap, visited && literal == "object", nil) {
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
			if !strictlyExpressibleNode(items, visited && literal == "array", nil) {
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
	// The rewrite strictifies combinators from any node it visits, whatever the
	// node's own type, so branches stay reachable.
	for _, comb := range []string{"anyOf", "oneOf", "allOf"} {
		raw, ok := m[comb]
		if !ok || raw == nil {
			continue
		}
		arr, ok := raw.([]any)
		if !ok {
			// Present but unreadable: the rewrite skips it, so any optional
			// property inside would reach the provider unchanged.
			return false
		}
		// A branch declaring no properties of its own is closed against this
		// node's, so this node's are the ones it may require. oneOf branches
		// are not closed against them, because all of them would then match the
		// same objects.
		var branchInherited map[string]any
		if comb != "oneOf" {
			branchInherited = props
		}
		for _, it := range arr {
			child, ok := it.(map[string]any)
			if !ok {
				return false
			}
			if !strictlyExpressibleNode(child, visited, branchInherited) {
				return false
			}
			// Closing a branch sets additionalProperties: false against the
			// branch's own properties. A branch that declares none would accept
			// only {}, while this node requires every property it declares, so
			// no branch could be satisfied. The rewrite instead gives such a
			// branch this node's properties, which is lossless because this node
			// already requires all of them.
			//
			// A branch that declares properties of its own is refused: closing
			// it narrows it to those keys alone, while the enclosing object
			// forbids exactly the ones it leaves out, so the two can agree only
			// when the sets match — a shape no builder here emits, and not worth
			// certifying blind.
			branchProps, ok := inspectProperties(child)
			if !ok || len(branchProps) > 0 {
				return false
			}
			// anyOf and allOf tolerate the branch matching whenever this node
			// does. oneOf does not: every such branch would match the same
			// objects, so its exactly-one rule would reject them all.
			if comb == "oneOf" {
				return false
			}
		}
	}
	// A branch may require a key this node never declares. Closing the object
	// then forbids that key, so the projection can satisfy neither branch.
	if transformed {
		declared, _ := m["properties"].(map[string]any)
		for _, comb := range []string{"anyOf", "oneOf"} {
			arr, ok := m[comb].([]any)
			if !ok {
				continue
			}
			for _, it := range arr {
				branch, ok := it.(map[string]any)
				if !ok {
					continue
				}
				for name := range requiredNamesOf(branch["required"]) {
					if _, has := declared[name]; !has {
						return false
					}
				}
			}
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

// A node the rewrite transforms may not permit keys it does not declare.
// Strict mode requires additionalProperties: false there, so the projection
// closes the object, and a schema that explicitly allows other keys — true, or
// a schema for their values — would end up accepting none of them. Only an
// absent key, or false, is lossless under the rewrite.
func allKeysDeclared(m map[string]any) bool {
	raw, has := m["additionalProperties"]
	if !has || raw == nil {
		return true
	}
	allowed, ok := raw.(bool)
	return ok && !allowed
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

func strictifyJSONSchemaInPlace(m map[string]any) {
	strictifyNode(m, nil)
}

// strictifyNode rewrites one node. enclosingProps carries the properties of the
// nearest enclosing object that this node is a combinator branch of, so a
// branch declaring none of its own can be closed against them instead of
// against an empty map.
func strictifyNode(m map[string]any, enclosingProps map[string]any) {
	if m == nil {
		return
	}
	typ, _ := m["type"].(string)
	switch typ {
	case "object":
		// OpenAI strict mode requires this be present and false.
		m["additionalProperties"] = false

		props, _ := m["properties"].(map[string]any)
		if len(props) == 0 && len(enclosingProps) > 0 {
			// A branch that declares no properties is closed against the
			// enclosing object's, which the enclosing node already requires.
			// Closing it against an empty map would leave it accepting only {}.
			props = make(map[string]any, len(enclosingProps))
			for k, v := range enclosingProps {
				props[k] = deepCopyAny(v)
			}
			m["properties"] = props
		}
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
				strictifyNode(child, nil)
			}
		}
	case "array":
		if items, ok := m["items"].(map[string]any); ok {
			strictifyNode(items, nil)
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
		// A property-less branch inherits the enclosing object's properties.
		// oneOf does not, because every such branch would then match the same
		// objects and its exactly-one rule would reject them all.
		var inherited map[string]any
		if comb != "oneOf" {
			inherited, _ = m["properties"].(map[string]any)
		}
		for _, it := range arr {
			if child, ok := it.(map[string]any); ok {
				strictifyNode(child, inherited)
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
