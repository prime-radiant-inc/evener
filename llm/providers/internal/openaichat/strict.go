package openaichat

import (
	"bytes"
	"encoding"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
)

// schemaDialect says which JSON Schema dialect a node was written against, to
// the extent this package needs that answer.
//
// Only one keyword's meaning differs among the dialects a tool definition can
// plausibly arrive in: const, which asserts nothing before draft 6 and a single
// value from draft 6 on. The question this file has to answer is narrow — does
// const constrain anything here — so a dialect is modeled as that answer rather
// than as a version. A schema that declares no dialect, or one this file cannot
// place, is read as modern: every schema this repository builds states no
// $schema, every one of those uses const as an assertion, and reading it as an
// annotation instead would drop strict mode from tools that are expressible.
type schemaDialect int

const (
	// dialectNoConst: the node declares a dialect in which const accepts every
	// value, which is draft 4 and earlier.
	dialectNoConst schemaDialect = iota
	// dialectConst: the node declares a dialect in which const asserts its
	// single value.
	dialectConst
)

// readsConst reports whether const constrains a value under the dialect.
func (d schemaDialect) readsConst() bool { return d == dialectConst }

// dialectOf decides the dialect an entire schema was written against, from
// its root, which is the only place the declaration has to appear.
//
// JSON Schema takes $schema at the root. A root that declares an older dialect
// makes const an annotation everywhere below it; a root that declares a modern
// dialect, or declares none at all, has const assert.
func dialectOf(schema map[string]any) schemaDialect {
	raw, has := schema["$schema"]
	if !has {
		// An undeclared dialect is treated as modern. Every schema this
		// repository builds states no $schema and every one of those uses const
		// as an assertion; refusing to read it there would drop strict mode from
		// tools that are otherwise expressible.
		return dialectConst
	}
	// The declaration arrives either as a Go string or as the json.RawMessage a
	// caller wrote it as. Both are the same URI and have to select the same
	// dialect: the answers differ on whether const asserts anything, so reading
	// only the Go spelling made the emitted schema depend on how the caller
	// happened to write the value rather than on the value itself.
	s := dialectURI(raw)
	s = strings.TrimSpace(s)
	if s == "" {
		return dialectConst
	}
	// Draft 4 and earlier have no const. The declarations are written as a URI
	// and are matched case-insensitively because the draft number is the only
	// part that matters.
	lower := strings.ToLower(s)
	for _, older := range []string{"draft-03", "draft-04", "draft-3", "draft-4"} {
		if strings.Contains(lower, older) {
			return dialectNoConst
		}
	}
	return dialectConst
}

// dialectURI returns the text of a $schema declaration as the value it will be
// emitted as, whatever Go representation carries it.
//
// The declaration reaches this package as a plain string, a json.RawMessage, a
// *string or a named string type, and all of those are the same URI once
// encoded. Reading only the first two made the emitted schema depend on how the
// caller happened to spell the value rather than on the value itself: a Draft 4
// URI through a pointer read as "no declaration", the walk assumed a modern
// dialect, and a typeless string const acquired a type under a schema that says
// const asserts nothing. The answer a caller's encoding gives is therefore the
// answer this reads, and a value that does not encode to a JSON string yields
// "", which dialectOf reads as a declaration it cannot place.
func dialectURI(raw any) string {
	if s, ok := raw.(string); ok {
		// The overwhelming majority of declarations are this spelling, and the
		// round trip would allocate for each of them.
		return s
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	var decoded string
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return ""
	}
	return decoded
}

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
	// Only a nil nested inside the schema is a value, and the copy keeps
	// those so a null enum member survives the copy. The copy reports a nil
	// interface for either, so the root is read with a comma-ok assertion
	// rather than an unchecked one.
	cp, _ := copySchema(in).(map[string]any)
	if cp == nil {
		cp = map[string]any{}
	}
	strictifyJSONSchemaInPlace(cp)
	supplyDerivedTypes(cp, dialectOf(cp))
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
	cp, _ := copySchema(schema).(map[string]any)
	if cp == nil {
		return map[string]any{}
	}
	supplyDerivedTypes(cp, dialectOf(cp))
	return cp
}

// supplyDerivedTypes states the explicit type implied by a closed scalar value
// set on every node that carries one. A provider enforcing the strict subset
// requires a type on every node, and StrictExpressible certifies a typeless
// enum or const node only because that type can be supplied without changing
// the accepted set. The walk therefore has to reach every such node, including
// the ones strictifyJSONSchemaInPlace never descends into — a child of a
// nullable object, say — because the certification covers those too.
//
// The positions below are the whole schema-bearing keyword set, not the subset
// a run of reviews happened to name. A keyword that holds a subschema and is
// missing from these tables leaves a typeless node on the wire, which costs the
// whole request; the tables are what keeps that a one-line fix rather than
// another round in which the next missing keyword is found.
//
// The shape is written down rather than guessed from the value, because
// properties is a map of names to schemas and not a schema: reading its keys as
// keywords would let a property named "enum" turn its parent into a value set.
// A keyword that admits a boolean (additionalProperties, unevaluatedProperties,
// unevaluatedItems) is left alone when the value is not a schema.
var (
	// singleSubschemaKeywords hold exactly one subschema.
	singleSubschemaKeywords = []string{
		"not", "items", "contains", "propertyNames",
		"if", "then", "else",
		"additionalProperties", "unevaluatedProperties", "unevaluatedItems",
		// additionalItems is a boolean or a schema, like
		// additionalProperties, and the loop below skips the boolean.
		// Draft 7 applies it to the positions a tuple leaves open, so a
		// typeless scalar enum there reaches the provider unwalked.
		"additionalItems",
	}
	// listSubschemaKeywords hold a list of subschemas.
	listSubschemaKeywords = []string{
		"allOf", "anyOf", "oneOf", "prefixItems",
		// items holds a schema or, before draft 2020-12, the tuple form: a
		// list of schemas applied by position. It is in both tables because
		// it is both shapes, and each loop skips the value the other reads.
		// Leaving the list form out left every position in a Draft 7 tuple
		// unwalked, so a typeless enum inside one reached the provider and
		// cost the whole request.
		"items",
	}
	// mapSubschemaKeywords hold a map of names to subschemas.
	mapSubschemaKeywords = []string{
		"properties", "patternProperties", "dependentSchemas",
		"$defs", "definitions",
		// dependencies is Draft 7's name for dependentSchemas, and it
		// admits two value shapes: a schema, and a list of property names
		// that must be present together. The loop below visits only values
		// that are schemas, so the property-name form is preserved as
		// advertised rather than read as one.
		"dependencies",
	}
)

func supplyDerivedTypes(m map[string]any, dialect schemaDialect) {
	if m == nil {
		return
	}
	// $schema may be declared again at an embedded schema resource, and it
	// governs the keywords below that node. A const under a draft-4 resource
	// asserts nothing, so the outer dialect must not be applied through it.
	// The gate already refuses a node carrying $schema, so this only ever
	// reaches the non-strict supply, where it is the whole point: stating a
	// type there would narrow what the advertised schema accepts.
	if _, has := m["$schema"]; has {
		dialect = dialectOf(m)
	}
	if _, has := m["type"]; !has {
		if name, ok := derivedScalarType(m, dialect); ok {
			m["type"] = name
		}
	}
	for _, keyword := range singleSubschemaKeywords {
		if sub, ok := m[keyword].(map[string]any); ok {
			supplyDerivedTypes(sub, dialect)
		}
	}
	for _, keyword := range listSubschemaKeywords {
		arr, ok := m[keyword].([]any)
		if !ok {
			continue
		}
		for _, it := range arr {
			if sub, ok := it.(map[string]any); ok {
				supplyDerivedTypes(sub, dialect)
			}
		}
	}
	for _, keyword := range mapSubschemaKeywords {
		named, ok := m[keyword].(map[string]any)
		if !ok {
			continue
		}
		for _, v := range named {
			if sub, ok := v.(map[string]any); ok {
				supplyDerivedTypes(sub, dialect)
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
	// The walk follows property and items subschemas with no cycle tracking of
	// its own, and it does so before any copy exists. A schema that contains
	// itself is therefore refused here rather than followed: this is the
	// exported entry point, so it is reachable without a request builder and
	// cannot rely on one having checked first.
	if CyclicSchema(schema) {
		return false
	}
	return strictlyExpressibleNode(schema, true, dialectOf(schema))
}

// CyclicSchema reports whether schema contains itself.
//
// The walk follows the ancestor chain rather than a global visited set: the
// same subschema value reachable from two positions is legitimate Go — a
// caller may share one subtree between two branches — and only a value that
// contains itself is a cycle. Tracking every visited container instead would
// reject the shared one.
//
// This is a separate question from what the projection does with a cycle. The
// copy cuts a looping edge and carries on, because the empty schema admits
// everything the original did. Strict eligibility cannot: it recurses over the
// caller's own tree before any copy exists, so a cyclic tree exhausts the stack
// inside the walk. A request builder therefore asks this first and refuses the
// request, which is the one answer that is both honest and finite.
func CyclicSchema(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	return containsCycle(schema, map[containerKey]bool{})
}

// ErrCyclicSchema reports a tool schema whose value contains itself. A request
// builder refuses such a schema rather than projecting it.
var ErrCyclicSchema = errors.New("tool schema contains a cycle")

// containsCycle walks the containers of v, with path holding the identity of
// every ancestor currently being walked.
func containsCycle(v any, path map[containerKey]bool) bool {
	key, tracked := containerIdentity(v)
	if tracked {
		if path[key] {
			return true
		}
		path[key] = true
		defer delete(path, key)
	}
	switch x := v.(type) {
	case map[string]any:
		for _, vv := range x {
			if containsCycle(vv, path) {
				return true
			}
		}
	case map[string]map[string]any:
		for _, vv := range x {
			if containsCycle(vv, path) {
				return true
			}
		}
	case []any:
		for _, vv := range x {
			if containsCycle(vv, path) {
				return true
			}
		}
	case []map[string]any:
		for i := range x {
			if containsCycle(x[i], path) {
				return true
			}
		}
	}
	return false
}

// strictlyExpressibleNode walks one node of a schema already known to use only
// modeled keywords. visited reports whether StrictifyJSONSchema will call into
// this node: the root is visited, and a property or items subschema is visited
// only when its parent is visited and carries the literal object or array type
// the rewrite matches.
func strictlyExpressibleNode(m map[string]any, visited bool, dialect schemaDialect) bool {
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
	// A type is only readable in the spellings schemaTypeFlags and the rewrite
	// both understand. A caller may supply it as json.RawMessage, and then
	// neither reading sees an object or an array: the shape checks pass over it,
	// the presence of the key alone satisfies constrainsSomething, and the
	// rewrite — which matches a plain string — transforms nothing. The tool then
	// reaches a strict-enforcing provider as an open object and the whole
	// request is rejected. A representation this file cannot read must not be
	// certified: the absence of a finding here is not a finding.
	if !typeRepresentationReadable(m["type"]) {
		return false
	}
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
	if !constrainsSomething(m, isObject, isArray, dialect) {
		return false
	}
	// items is legal on a node of any type, and validators apply it wherever it
	// appears. The rewrite descends into it only when the node's type is the
	// literal "array", so an items on an object node — or on a node with no type
	// at all — reaches the provider exactly as advertised. It is walked here on
	// every node, with visited false wherever the rewrite does not follow, so a
	// node inside it has to be strict-valid on its own terms rather than being
	// certified unchecked.
	if raw, has := m["items"]; has && raw != nil {
		items, ok := raw.(map[string]any)
		if !ok {
			// An unreadable items is one neither the rewrite nor this walk can
			// verify.
			return false
		}
		childVisited := visited && literal == "array"
		if !strictlyExpressibleNode(items, childVisited, dialect) {
			return false
		}
	}
	// A type list may name both shapes at once, as in ["object", "array"].
	// Both are then constraints on the same node, and the rewrite — which
	// matches only a literal type string — transforms neither, so every one of
	// them has to hold as advertised. Checking them in one switch would walk
	// only the object side and leave the array side's items unverified.
	if !isObject {
		// properties constrains object instances only, and the rewrite closes
		// only a node whose type is the literal "object". On any other node —
		// an array, or one with no type — the keyword reaches the provider
		// exactly as advertised with its children unchecked, so a child that
		// states no type would be sent into a strict-enforcing provider that
		// rejects it. Refusing is what keeps the gate's answer true rather
		// than exempting the one shape it does not walk.
		if _, has := m["properties"]; has {
			return false
		}
	}
	// additionalProperties is legal on a node of any type, and a schema value —
	// as opposed to a boolean — constrains the object instances among them. The
	// rewrite reads the keyword only to write `false` on an object it closes, so
	// a schema value reaches the provider exactly as advertised: a nested object
	// with an optional property behind it is then validated by strict
	// enforcement and can reject the whole request. The keyword is walked here
	// on every node for the same reason items is, above.
	if raw, has := m["additionalProperties"]; has && raw != nil {
		if _, isBool := raw.(bool); !isBool {
			sub, ok := raw.(map[string]any)
			if !ok {
				// An unreadable subschema is one neither the rewrite nor this
				// walk can verify.
				return false
			}
			// Where the node is an object the rewrite does not transform, the
			// keyword is left as advertised; where it is transformed to false,
			// the subschema is dropped. Both are decided below, so the walk only
			// needs the contents to be certifiable in their own right.
			if !strictlyExpressibleNode(sub, false, dialect) {
				return false
			}
		}
	}
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
			if !strictlyExpressibleNode(childMap, visited && literal == "object", dialect) {
				return false
			}
		}
	}
	if isArray {
		// An array side of a type list has to name its items. Without them it
		// admits any element, which strict mode cannot verify, and the rewrite
		// cannot supply one: it matches only the literal "array" type string,
		// so a list never reaches the branch that would walk items. The walk
		// itself happened above, on every node carrying the keyword.
		if raw, has := m["items"]; !has || raw == nil {
			return false
		}
	}
	if !isObject && !isArray {
		// An object- or array-shaped node the rewrite does not recognise: it
		// keeps whatever it advertises, so an optional property behind it stays
		// optional under strict mode.
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

// typeRepresentationReadable reports whether a node's type value is in a
// spelling this file can read. An absent type is readable: it means the node
// names no type, and the rest of the walk already handles that. A present value
// that is not a string or a list of strings is a representation nothing here
// decoded, and certifying it would be certifying a shape no check examined.
func typeRepresentationReadable(raw any) bool {
	switch t := raw.(type) {
	case nil:
		return true
	case string:
		return true
	case []string:
		return true
	case []any:
		for _, v := range t {
			if _, ok := v.(string); !ok {
				return false
			}
		}
		return true
	default:
		return false
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
func constrainsSomething(m map[string]any, isObject, isArray bool, dialect schemaDialect) bool {
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
		_, typed := derivedScalarType(m, dialect)
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

// enumMembers reads an enum's members as the values the request encoder writes
// for them.
//
// The same JSON enum reaches this package in many concrete shapes. A Go builder
// writes a typed slice — []string for names, []int for numbers — and a decoded
// provider or MCP schema yields []any. They describe the same set of JSON
// values, so they have to reach the inference the same way: an enum spelled
// []int{0, 1} and one spelled []any{0, 1} serialize identically, and giving
// only the second an explicit type would make the emitted schema depend on the
// caller's Go type rather than on the schema.
//
// The members are read from the whole enum's encoding rather than extracted
// element by element through reflection. Reflection loses encoders that belong
// to a pointer receiver: a slice of a named integer type whose MarshalJSON is
// declared on *T encodes as its method's output on the wire, because a slice
// element is addressable and the encoder therefore reaches the pointer's method
// set — but an element lifted out with Interface() is a T, whose method set does
// not include it. Asking the encoder what the enum is avoids that difference
// entirely, and makes []T and []any of the same values agree by construction.
//
// A shape that does not encode as a JSON array, or one whose encoding fails, is
// unreadable and infers nothing.
func enumMembers(raw any) ([]any, bool) {
	if raw == nil {
		return nil, false
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	var decoded []any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

// derivedScalarType returns the JSON type implied by a node's closed value set
// when every member is a non-null scalar of one type: {"const": "fixed"} and
// {"enum": ["a", "b"]} both imply "string". The projection states that type,
// which a strict-enforcing provider requires on every node, and the accepted
// set is unchanged because every member already has that type.
//
// One rule with three inputs decides it, because the three failures it has had
// were each one input read partially:
//
//   - representation: how the caller spelled the value. A typed slice, a
//     decoded []any and a json.Number all carry the same JSON scalar, and
//     reading only one spelling made the emitted schema depend on the caller's
//     Go type.
//   - the keyword: const or enum. Both name a closed set; neither is a
//     constraint the caller can rely on having.
//   - the dialect: whether the keyword is an assertion at all. Draft 4 has no
//     const, so {"const": "fixed"} accepts every value there; adding a type
//     would reject values the advertised schema accepted, which is the one
//     thing this function must never do.
//
// Anything else reports false rather than guessing. A mixed set, a container
// value, or a set containing null cannot be named by one type without dropping
// or widening a member, and a node typed from a guess is exactly the
// certification this gate exists to withhold.
func derivedScalarType(m map[string]any, dialect schemaDialect) (string, bool) {
	if _, has := m["type"]; has {
		return "", false
	}
	var values []any
	switch {
	case m["const"] != nil:
		// const arrived in draft 6. Under an older dialect the keyword asserts
		// nothing, so the node's accepted set is unconstrained and no type may
		// be stated for it.
		if !dialect.readsConst() {
			return "", false
		}
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
		this, ok := scalarJSONType(value)
		if !ok {
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

// scalarJSONType names the JSON type of one scalar value in any spelling this
// package can receive: the Go scalars a builder writes, the types a
// json.Number-carrying decode yields, and the concrete aliases of them.
//
// A value with its own JSON representation is classified by what it encodes to,
// which is what the request carries. A named integer type with a string
// marshaler has a Go kind of int and a wire value of "fast"; classifying it by
// kind states "number" beside a set of strings and rejects every advertised
// member. This is the same rule enumMembers applies to a whole enum, applied to
// one member, so members and the type cannot disagree.
//
// A container, a nil, or a numeric type that does not marshal as a JSON number
// reports false, because the caller cannot name it without changing the set.
func scalarJSONType(value any) (string, bool) {
	if _, isNumber := value.(json.Number); !isNumber {
		if encoded, ok := encodeAsJSON(value); ok {
			// The decoded value is a plain scalar in the same spellings the
			// rest of this function already reads, so classify that rather
			// than the original.
			return scalarJSONType(encoded)
		}
	}
	switch v := value.(type) {
	case string:
		return "string", true
	case bool:
		return "boolean", true
	case json.Number:
		// Only a number literal is a JSON number. Anything else the decoder
		// stored in a Number is not one this can name.
		if _, err := v.Float64(); err != nil {
			if _, intErr := v.Int64(); intErr != nil {
				return "", false
			}
		}
		return "number", true
	}
	rv := reflect.ValueOf(value)
	if !rv.IsValid() {
		return "", false
	}
	switch rv.Kind() {
	case reflect.String:
		return "string", true
	case reflect.Bool:
		return "boolean", true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "number", true
	default:
		return "", false
	}
}

// encodeAsJSON returns the value the request encoder sends for v, decoded into
// the plain spellings scalarJSONType reads, when that differs from v's Go kind.
// It reports false when v has no encoding of its own, which is the common case.
//
// The encoding is produced by json.Marshal rather than by calling the value's
// MarshalJSON method. A typed nil pointer to a value-receiver encoder — the
// shape a caller writes as (*json.RawMessage)(nil) or (*time.Time)(nil) — is a
// json.Marshaler whose method dereferences the nil receiver, so calling it
// panics during request construction. json.Marshal emits null for exactly that
// value without invoking the method, because a nil pointer is not encoded
// through the value's own encoder; asking it instead of the method is what
// makes this classify the value the request carries.
//
// A value with no encoder of its own encodes as itself, so there is nothing
// here the caller's kind switch cannot already name and the answer is false.
// That keeps one rule in charge of the decision: ask the encoder, never the Go
// kind, whenever there is an encoder to ask.
func encodeAsJSON(value any) (any, bool) {
	rv := reflect.ValueOf(value)
	if !rv.IsValid() {
		return nil, false
	}
	// A nil pointer to a method-bearing type has no encoder of its own: the
	// request carries null. Reporting it as encoded keeps the caller's scalar
	// switch from reading a Go kind that never reaches the wire.
	if rv.Kind() == reflect.Ptr && rv.IsNil() {
		if rv.Type().Implements(jsonMarshalerType) || rv.Type().Implements(textMarshalerType) {
			return nil, true
		}
		return nil, false
	}
	if !rv.Type().Implements(jsonMarshalerType) && !rv.Type().Implements(textMarshalerType) {
		return nil, false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, false
	}
	return decoded, true
}

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
)

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

// copySchema copies a schema for the projection, cutting any reference cycle
// it meets. It is the only copy the two exported entry points use.
//
// A Go caller can build a schema graph that loops, by linking a child back to
// an ancestor through a typed properties map. Before the typed arms below
// existed, such a map fell through to the default arm and was handed to
// encoding/json unchanged, which returned a cycle error and cost the request.
// Copying it recursively instead turns that error into unbounded recursion and
// a stack overflow, which takes the whole process rather than the request. A
// cycle is therefore detected here and replaced with the empty schema, so the
// request is still built and the looping edge is the only thing lost.
//
// The detection follows the ancestor chain rather than a global visited set:
// two siblings may legitimately share one subtree, and refusing the second
// sighting would silently drop a schema the caller wrote. Only a node that
// contains itself is a cycle.
func copySchema(v any) any {
	return copySchemaValue(v, nil)
}

// copySchemaValue copies v, with path holding the container identity of every
// ancestor currently being copied.
func copySchemaValue(v any, path map[containerKey]bool) any {
	if key, ok := containerIdentity(v); ok {
		if path[key] {
			// The node contains itself. The empty schema is the one value that
			// admits everything the original did, so the looping edge is not
			// narrowed by the cut.
			return map[string]any{}
		}
		if path == nil {
			path = map[containerKey]bool{}
		}
		path[key] = true
		defer delete(path, key)
	}
	return deepCopyWith(v, path)
}

// containerKey identifies one container occurrence for cycle detection: the
// address it starts at, and — for a slice — how many elements that particular
// header covers.
//
// The length is part of the key because a slice header is not an identity. A Go
// caller can reuse the first branches of a wide anyOf list through a shorter
// prefix slice in a later branch: same start address, fewer elements, and the
// shorter one therefore does not contain the branch that points back at it. The
// prefix is acyclic, and telling it apart from the wide slice it aliases is
// exactly what the length does. A real loop repeats one slice value, so it
// repeats its header and its length with it, and the shorter view of it is a
// different container rather than the same one seen twice.
//
// Maps carry length -1: their address alone identifies them, and a map has no
// aliasing view to confuse it with.
type containerKey struct {
	addr   uintptr
	length int
}

// containerIdentity returns a value that identifies the container v points at,
// for cycle detection.
//
// Slices are tracked as well as maps. A cycle can close through either: a
// properties map may hold a slice whose element is the map's own schema, which
// is legal Go and was the case the map-only guard missed. A zero-length slice is
// skipped because its data pointer may be nil or shared between unrelated empty
// slices, and it cannot be part of a cycle anyway.
func containerIdentity(v any) (containerKey, bool) {
	switch x := v.(type) {
	case map[string]any:
		if x == nil {
			return containerKey{}, false
		}
		return containerKey{addr: reflect.ValueOf(x).Pointer(), length: -1}, true
	case map[string]map[string]any:
		if x == nil {
			return containerKey{}, false
		}
		return containerKey{addr: reflect.ValueOf(x).Pointer(), length: -1}, true
	case []any:
		if len(x) == 0 {
			return containerKey{}, false
		}
		return containerKey{addr: reflect.ValueOf(x).Pointer(), length: len(x)}, true
	case []map[string]any:
		if len(x) == 0 {
			return containerKey{}, false
		}
		return containerKey{addr: reflect.ValueOf(x).Pointer(), length: len(x)}, true
	default:
		return containerKey{}, false
	}
}

// deepCopyWith copies a schema value, normalising the Go spellings a builder
// writes into the decoded spellings the walk reads.
//
// path carries the ancestor chain, so a looping edge is cut rather than
// recursed into.
func deepCopyWith(v any, path map[containerKey]bool) any {
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
			out[k] = copySchemaValue(vv, path)
		}
		return out
	case map[string]map[string]any:
		// A Go builder writes properties as map[string]map[string]any, and the
		// default arm would return that map itself: the copy would share the
		// children with the caller, so supplying a type inside one would mutate
		// the caller's schema. Normalising to map[string]any here is what makes
		// the copy independent and lets the walk reach each child.
		if x == nil {
			return nil
		}
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = copySchemaValue(vv, path)
		}
		return out
	case []map[string]any:
		// The slice sibling of the arm above: a Go builder writes anyOf,
		// oneOf and allOf as []map[string]any, and the default arm would return
		// the caller's slice. The walk only reads []any, so every branch was
		// skipped and a typeless node inside one reached the provider
		// unchanged.
		if x == nil {
			return nil
		}
		out := make([]any, len(x))
		for i := range x {
			out[i] = copySchemaValue(x[i], path)
		}
		return out
	case []any:
		// A typed nil slice marshals as JSON null for the same reason.
		if x == nil {
			return nil
		}
		out := make([]any, len(x))
		for i := range x {
			out[i] = copySchemaValue(x[i], path)
		}
		return out
	case json.RawMessage:
		// A caller may supply a subschema as raw JSON. The walk and the
		// projection both branch on map[string]any, so a raw child is invisible
		// to them: a typeless enum behind one reaches the provider without the
		// explicit type its decoded twin is given, and the request is rejected
		// for a node that was never read. Decoding it here is what makes the
		// answer depend on the schema rather than on the Go type the caller
		// happened to choose.
		//
		// A raw list is decoded for the same reason. A combinator list supplied
		// this way was opaque at every list-valued keyword, so its branches were
		// never walked: a typeless scalar enum inside one reached the provider
		// without the explicit type its decoded twin is given. The branches are
		// decoded and then re-encoded unchanged, so what the caller advertised
		// is what the request carries.
		//
		// A raw scalar is left untouched: it is a value rather than a list of
		// subschemas, and nothing here rewrites one.
		if len(x) == 0 {
			return v
		}
		// The whole value has to be one JSON value before it is decoded. A
		// decoder reads the first value and stops, so a schema object followed
		// by trailing bytes decodes cleanly and the suffix is dropped — the
		// request would then carry a valid prefix where the encoder previously
		// rejected the tool. Malformed values are left as they are so that
		// rejection still happens at the encoder.
		if !json.Valid(x) {
			return v
		}
		var decoded any
		decoder := json.NewDecoder(bytes.NewReader(x))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return v
		}
		switch decoded.(type) {
		case map[string]any, []any:

		default:
			return v
		}
		return copySchemaValue(decoded, path)
	default:
		return v
	}
}
