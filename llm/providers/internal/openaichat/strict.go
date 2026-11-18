package openaichat

import (
	"reflect"
	"sort"
)

// StrictifyJSONSchema returns a deep copy of a tool parameter schema
// rewritten for OpenAI strict mode: every object gets
// additionalProperties: false, a properties map, and required = every
// property (sorted); arrays and anyOf/oneOf/allOf recurse. It runs only
// when Caps.StrictTools is set (spec §8.2), because the rewrite is not
// reversible by pruning strict afterwards.
func StrictifyJSONSchema(in map[string]any) map[string]any {
	// Best-effort deep copy + strictification for OpenAI tool schemas.
	// This intentionally handles only the constructs we emit (object/array) and is safe to
	// apply repeatedly (idempotent for our shapes).
	cp := deepCopyAny(in).(map[string]any)
	strictifyJSONSchemaInPlace(cp)
	return cp
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

// SameSerializedLength reports whether original and projected serialize to the
// same number of bytes, without serializing either.
//
// The projections rewrite every object's required list from its properties, so
// a schema that already names exactly those properties is emitted as a
// permutation of itself: the bytes are reordered, not resized. A caller that
// prices a catalog by its byte length can therefore keep the count it already
// holds, where marshaling both schemas to discover that they measure the same
// costs two full encodes of the catalog on every pricing call, at admission and
// again before dispatch.
//
// The answer is conservative by design. Two schemas count as the same length
// only when this walk can account for every difference between them, which here
// means the order of a required list and the Go spelling of the containers a
// builder writes (a typed properties map and its decoded twin serialize
// identically). Anything else — a stated type, a closed root, an added or
// dropped keyword — is reported as different so the caller measures it.
// Reporting a difference costs one marshal, while reporting a sameness that
// does not hold would price a catalog the wire never carries.
func SameSerializedLength(original, projected map[string]any) bool {
	return sameLengthValue(original, projected)
}

// sameLengthValue compares two schema values for serialized length.
func sameLengthValue(a, b any) bool {
	if am, ok := asSchemaMap(a); ok {
		bm, ok := asSchemaMap(b)
		// A nil map marshals as JSON null and an empty one as {}, so the two
		// spellings are only interchangeable when neither side is nil.
		if !ok || (am == nil) != (bm == nil) {
			return false
		}
		return sameLengthMap(am, bm)
	}
	if as, ok := asSchemaList(a); ok {
		bs, ok := asSchemaList(b)
		if !ok || (as == nil) != (bs == nil) || len(as) != len(bs) {
			return false
		}
		for i := range as {
			if !sameLengthValue(as[i], bs[i]) {
				return false
			}
		}
		return true
	}
	// A scalar, or a shape this walk does not model. Equal values serialize to
	// equal lengths whatever their Go spelling; anything this cannot read as
	// equal is left for the caller to measure.
	return reflect.DeepEqual(a, b)
}

// sameLengthMap compares two schema objects key for key.
func sameLengthMap(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}
		if k == "required" {
			// The projection rebuilds required from properties in sorted
			// order, so the only difference it can make here is the order of
			// the same names.
			if !sameNameSet(av, bv) {
				return false
			}
			continue
		}
		if !sameLengthValue(av, bv) {
			return false
		}
	}
	return true
}

// sameNameSet reports whether a and b are lists of the same names in any order.
func sameNameSet(a, b any) bool {
	as, ok := asSchemaList(a)
	if !ok {
		return false
	}
	bs, ok := asSchemaList(b)
	if !ok || len(as) != len(bs) {
		return false
	}
	seen := make(map[string]int, len(as))
	for _, v := range as {
		name, ok := v.(string)
		if !ok {
			return false
		}
		seen[name]++
	}
	for _, v := range bs {
		name, ok := v.(string)
		if !ok {
			return false
		}
		seen[name]--
		if seen[name] < 0 {
			return false
		}
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// asSchemaMap reads the two map spellings a schema arrives in: the decoded one
// and the typed one a Go builder writes. Both serialize identically.
func asSchemaMap(v any) (map[string]any, bool) {
	switch m := v.(type) {
	case map[string]any:
		return m, true
	case map[string]map[string]any:
		if m == nil {
			// A typed nil map marshals as JSON null, which is not the empty
			// object. Keep the nil so the caller can tell them apart.
			return nil, true
		}
		out := make(map[string]any, len(m))
		for k, vv := range m {
			out[k] = vv
		}
		return out, true
	default:
		return nil, false
	}
}

// asSchemaList reads the list spellings a schema arrives in: the decoded one
// and the typed ones a Go builder writes.
func asSchemaList(v any) ([]any, bool) {
	switch s := v.(type) {
	case []any:
		return s, true
	case []map[string]any:
		if s == nil {
			return nil, true
		}
		out := make([]any, len(s))
		for i := range s {
			out[i] = s[i]
		}
		return out, true
	case []string:
		if s == nil {
			return nil, true
		}
		out := make([]any, len(s))
		for i := range s {
			out[i] = s[i]
		}
		return out, true
	default:
		return nil, false
	}
}
