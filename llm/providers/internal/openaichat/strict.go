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
	// A nil root is an absent schema rather than a JSON null to preserve: the
	// projection of it is the empty schema, which is what this returns for it.
	// Only a nil nested inside the schema is a value, and deepCopyAny keeps
	// those so a null enum member survives the copy. The copy reports a nil
	// interface for either, so the root is read with a comma-ok assertion
	// rather than an unchecked one.
	cp, _ := deepCopyAny(in).(map[string]any)
	if cp == nil {
		cp = map[string]any{}
	}
	strictifyJSONSchemaInPlace(cp)
	supplyDerivedTypes(cp)
	return cp
}

// SupplyDerivedTypes returns a deep copy of schema with the explicit type a
// closed scalar value set implies stated on every node that carries one, and
// nothing else changed.
//
// An adapter sends a refused tool with the schema it advertised, which is the
// right shape for the accepted set but not for a provider that requires an
// explicit type on every node: an argument declared {"const": "fixed"} keeps
// its single accepted value and still fails the request. This step states that
// type without narrowing or widening anything, so a refusal costs the tool its
// strict mode and nothing else. A node whose value set implies no single type
// is left exactly as advertised.
func SupplyDerivedTypes(schema map[string]any) map[string]any {
	cp, _ := deepCopyAny(schema).(map[string]any)
	if cp == nil {
		return map[string]any{}
	}
	supplyDerivedTypes(cp)
	return cp
}

// supplyDerivedTypes states the explicit type implied by a closed scalar value
// set on every node that carries one. A provider enforcing the strict subset
// requires a type on every node, and StrictExpressible certifies a typeless
// enum or const node only because that type can be supplied without changing
// the accepted set. The walk therefore has to reach every such node, including
// the ones strictifyJSONSchemaInPlace never descends into — a child of a
// nullable object, say — because the certification covers those too.
func supplyDerivedTypes(m map[string]any) {
	if m == nil {
		return
	}
	if _, has := m["type"]; !has {
		if name, ok := derivedScalarType(m); ok {
			m["type"] = name
		}
	}
	if props, ok := m["properties"].(map[string]any); ok {
		for _, child := range props {
			if childMap, ok := child.(map[string]any); ok {
				supplyDerivedTypes(childMap)
			}
		}
	}
	if items, ok := m["items"].(map[string]any); ok {
		supplyDerivedTypes(items)
	}
	// A schema-valued additionalProperties holds a schema. The rewrite never
	// descends into it — closing an object replaces it outright — so a tool that
	// was refused strict mode keeps this node exactly as advertised, and a
	// typeless node there still fails a provider that requires explicit types.
	// A boolean additionalProperties is not a schema and is left alone.
	if ap, ok := m["additionalProperties"].(map[string]any); ok {
		supplyDerivedTypes(ap)
	}
	if pp, ok := m["patternProperties"].(map[string]any); ok {
		for _, child := range pp {
			if childMap, ok := child.(map[string]any); ok {
				supplyDerivedTypes(childMap)
			}
		}
	}
	for _, comb := range []string{"anyOf", "oneOf", "allOf"} {
		arr, ok := m[comb].([]any)
		if !ok {
			continue
		}
		for _, it := range arr {
			if child, ok := it.(map[string]any); ok {
				supplyDerivedTypes(child)
			}
		}
	}
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
	// A node that constrains nothing admits every JSON value. Strict mode
	// cannot be established for it: the projection closes objects and rewrites
	// arrays, but it cannot state a subset of "anything", so what the provider
	// enforces is decided by the provider rather than by this walk. An empty
	// schema, a missing type, an object with no properties, and an array with
	// no items are all that shape.
	if !constrainsSomething(m, isObject, isArray) {
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
		// Strict mode admits only a required list that names exactly the
		// declared properties. A name the object never declares makes the
		// projected schema unsatisfiable, and the rewrite cannot repair it: it
		// rewrites required only where it transforms the node, so a node it
		// leaves alone would carry the extra name to the provider. This holds
		// for every object node, not only the transformed ones.
		if !requiredNamesDeclared(props, m["required"]) {
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
		// An array side of a type list has to name its items. Without them it
		// admits any element, which strict mode cannot verify, and the rewrite
		// cannot supply one: it matches only the literal "array" type string,
		// so a list never reaches the branch that would walk items.
		raw, has := m["items"]
		if !has || raw == nil {
			return false
		}
		items, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		if !strictlyExpressibleNode(items, visited && literal == "array") {
			return false
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

// constrainsSomething reports whether a node states any constraint at all. An
// empty schema admits every JSON value, and so does an object that declares no
// properties without closing itself, and an array that names no items. Strict
// mode is a promise about the set of accepted values, and this walk can only
// keep that promise for a node whose accepted set it can read.
//
// A closed object with no declared properties is not that shape: it admits
// exactly the empty object, which is the one argument set a parameterless tool
// takes. An array that names items is likewise constrained, whether or not the
// walk transforms it.
func constrainsSomething(m map[string]any, isObject, isArray bool) bool {
	switch {
	case isObject:
		props, ok := inspectProperties(m)
		if !ok {
			return false
		}
		return len(props) > 0 || m["additionalProperties"] == false
	case isArray:
		raw, has := m["items"]
		return has && raw != nil
	default:
		// Any other node must name a type or carry a constraint that narrows
		// the value set whatever type the value has: without one it is the
		// empty schema, which admits every value.
		if _, has := m["type"]; has {
			return true
		}
		// A typeless node accepts values of every type, so only a keyword whose
		// accepted set this file can read narrows it. That answer is read out of
		// modeledKeywords rather than listed again here: a keyword the table
		// does not carry scores notCovered, which does not equal readableByWalk,
		// so it cannot certify a node.
		if !certifiedByReadableKeyword(m) {
			return false
		}
		// A provider enforcing the strict subset also requires an explicit type
		// on every node. A closed value set whose members all share one scalar
		// type supplies that type without changing the set, and the projection
		// states it; anything else would have to guess, so it is refused.
		_, typed := derivedScalarType(m)
		return typed
	}
}

// certifiedByReadableKeyword reports whether the node carries a keyword whose
// accepted set this file can read. It is the derived form of "a keyword the
// rewrite does not model cannot certify a node": the answer comes from the
// coverage table, so a keyword added to the rewrite without a readable class
// cannot acquire the power to certify.
func certifiedByReadableKeyword(m map[string]any) bool {
	for keyword := range m {
		if modeledKeywords[keyword] == readableByWalk {
			return true
		}
	}
	return false
}

// enumMembers reads an enum's members as a slice of values.
//
// The same JSON enum reaches this package in two concrete shapes: a Go builder
// writes []string, and a decoded provider or MCP schema yields []any. They
// describe the same set, so they must reach the inference the same way. A shape
// that is neither is unreadable and infers nothing.
func enumMembers(raw any) ([]any, bool) {
	switch list := raw.(type) {
	case []any:
		return list, true
	case []string:
		values := make([]any, len(list))
		for i, member := range list {
			values[i] = member
		}
		return values, true
	default:
		return nil, false
	}
}

// derivedScalarType returns the JSON type implied by a node's closed value set
// when every member is a non-null scalar of one type: {"const": "fixed"} and
// {"enum": ["a", "b"]} both imply "string". The projection states that type,
// which a strict-enforcing provider requires on every node, and the accepted
// set is unchanged because every member already has that type.
//
// Anything else reports false rather than guessing. A mixed set, a container
// value, or a set containing null cannot be named by one type without dropping
// or widening a member, and a node typed from a guess is exactly the
// certification this gate exists to withhold.
func derivedScalarType(m map[string]any) (string, bool) {
	if _, has := m["type"]; has {
		return "", false
	}
	var values []any
	switch {
	case m["const"] != nil:
		values = []any{m["const"]}
	case m["enum"] != nil:
		list, ok := enumMembers(m["enum"])
		if !ok {
			return "", false
		}
		values = list
	default:
		return "", false
	}
	if len(values) == 0 {
		return "", false
	}
	name := ""
	for _, value := range values {
		var this string
		switch value.(type) {
		case string:
			this = "string"
		case bool:
			this = "boolean"
		case float64, int, int64:
			this = "number"
		default:
			return "", false
		}
		if name == "" {
			name = this
		} else if name != this {
			return "", false
		}
	}
	return name, true
}

// requiredNamesDeclared reports whether every name in required is a declared
// property. Strict mode admits required only as exactly the declared property
// names, so a name the object never declares makes the schema unsatisfiable
// there. This is the other half of allPropertiesRequired: that one asks whether
// every property is required, this one whether every requirement is a property.
func requiredNamesDeclared(props map[string]any, required any) bool {
	switch list := required.(type) {
	case []string:
		for _, name := range list {
			if _, has := props[name]; !has {
				return false
			}
		}
	case []any:
		for _, v := range list {
			if name, ok := v.(string); ok {
				if _, has := props[name]; !has {
					return false
				}
			}
		}
	case nil:
		return true
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

// coverage says what the projection does with a keyword. It answers one
// question: can this keyword appear in a schema that is certified at all?
//
// It deliberately does not answer whether a keyword can certify a node on its
// own. Those are different questions, and a single list answering both is wrong
// the moment they diverge: minLength is covered — the rewrite neither reads nor
// rewrites it, and closing an enclosing object cannot narrow it — yet
// {"minLength": 0} accepts every JSON value, so it certifies nothing. The
// predicate therefore reads the two answers from separate tables below, and the
// certifying table is derived from this one rather than maintained beside it.
type coverage int

const (
	// notCovered: the rewrite cannot account for the keyword, so any node
	// carrying it is refused.
	notCovered coverage = iota
	// rewritten: the projection supplies the keyword, so whatever the schema
	// advertised for it is replaced.
	rewritten
	// leftAlone: the rewrite neither reads nor rewrites it, and its effect
	// cannot depend on a key set the projection changes.
	leftAlone
	// readableByWalk: leftAlone, and the set of values it admits is one this
	// file can read from the keyword alone. Only these may certify a node that
	// names no type, because such a node accepts values of every type.
	readableByWalk
)

// modeledKeywords classifies every JSON Schema keyword the projection covers.
//
// Adding a keyword to the rewrite without adding it here makes the gate refuse
// schemas the rewrite handles — a visible loss of strictness, not a silent
// unsoundness. Adding one here without teaching the rewrite about it would be
// the reverse, and is the mistake this table exists to prevent.
//
// The certifying answer is not listed anywhere: constrainsSomething reads it
// back out of this table. A keyword that is missing, notCovered, rewritten or
// merely leftAlone cannot certify, so a new entry cannot silently acquire the
// power to certify a node.
var modeledKeywords = map[string]coverage{
	// Rewritten on objects and arrays: the projection supplies them.
	"type":                 rewritten,
	"properties":           rewritten,
	"required":             rewritten,
	"additionalProperties": rewritten,
	"items":                rewritten,

	// Annotations and bounds: closing an enclosing object cannot narrow them.
	// None of them names a value set this walk can read, so none certifies a
	// typeless node — a bound narrows one type and leaves the others open, and
	// its identity form rejects nothing at all.
	"description": leftAlone,
	"default":     leftAlone,
	"examples":    leftAlone,
	"title":       leftAlone,
	"format":      leftAlone,
	"pattern":     leftAlone,
	"minimum":     leftAlone,
	"maximum":     leftAlone,
	"minLength":   leftAlone,
	"maxLength":   leftAlone,
	"minItems":    leftAlone,
	"maxItems":    leftAlone,
	"uniqueItems": leftAlone,

	// A closed set of values, readable from the keyword alone, so it narrows a
	// node of any type.
	"enum":  readableByWalk,
	"const": readableByWalk,
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
		if modeledKeywords[keyword] == notCovered {
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
		// A typed nil map marshals as JSON null. Rebuilding it as an empty map
		// would emit {} and change what the schema accepts. The nil interface
		// returned here is indistinguishable from a nil map to every caller of
		// this function: a map read from a nil interface panics either way, and
		// StrictifyJSONSchema handles the root with a comma-ok assertion.
		if x == nil {
			return nil
		}
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = deepCopyAny(vv)
		}
		return out
	case []any:
		// A typed nil slice marshals as JSON null for the same reason.
		if x == nil {
			return nil
		}
		out := make([]any, len(x))
		for i := range x {
			out[i] = deepCopyAny(x[i])
		}
		return out
	default:
		return v
	}
}
