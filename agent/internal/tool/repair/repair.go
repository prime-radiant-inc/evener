// Package repair heals off-distribution LLM tool calls: it renames aliased
// parameters, coerces mistyped scalars, drops optional enum arguments sent as
// "" or null, drops hallucinated keys, and fixes broken JSON escapes. It is a
// pure, standard-library-only leaf package; the caller supplies a tool's
// JSON-Schema parameter object and the parsed args.
package repair

import (
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

// ChangeKind names the category of a single repair.
type ChangeKind string

const (
	ChangeAlias             ChangeKind = "alias"
	ChangeCoerceType        ChangeKind = "coerce_type"
	ChangeDropUnknown       ChangeKind = "drop_unknown"
	ChangeUnicodeRepair     ChangeKind = "unicode_repair"
	ChangeMissingOuterBrace ChangeKind = "missing_outer_brace"
	ChangeQuoteObjectKey    ChangeKind = "quote_object_key"
	ChangeFillRequired      ChangeKind = "fill_required"
	ChangeNormalizeDefault  ChangeKind = "normalize_default"
	ChangeSynthesize        ChangeKind = "synthesize"
	ChangeCopy              ChangeKind = "copy"
	ChangePromoteJSONObject ChangeKind = "promote_json_object"
)

// Change records one repair for telemetry. Field is the affected key ("" for a
// whole-document JSON repair); Detail is a human-readable summary.
type Change struct {
	Kind   ChangeKind
	Field  string
	Detail string
}

// aliasTable maps off-distribution parameter names to their canonical names.
// An entry only fires under the safe-apply rule in applyAliases; grow it as
// telemetry reveals new drift.
var aliasTable = map[string]string{
	"old_str":  "old_string",
	"new_str":  "new_string",
	"path":     "file_path",
	"filepath": "file_path",
	"filename": "file_path",
	"contents": "content",
	"cmd":      "command",
}

// RepairArgs normalizes args against the tool's JSON-Schema parameter object.
// It applies, in order, aliasing, coercion, dropping empty optional enums, and
// drop-unknown. keep names argument paths (as "add[0].reasoning_effort") whose
// explicit empty value the tool's handler judges itself, so repair leaves them.
// It never mutates its input; it returns a fresh map plus the changes made.
func RepairArgs(params, args map[string]any, keep ...string) (map[string]any, []Change) {
	out := make(map[string]any, len(args))
	maps.Copy(out, args)
	var changes []Change
	changes = append(changes, applyAliases(params, out)...)
	changes = append(changes, applyCoercions(params, out)...)
	changes = append(changes, dropEmptyOptionalEnums(params, out, nil, keep)...)
	changes = append(changes, dropUnknown(params, out)...)
	return out, changes
}

// applyAliases renames aliased keys to canonical names under the safe-apply
// rule: rename X→Y only when Y is a declared property, X is not, X is present,
// and Y is absent.
func applyAliases(params, args map[string]any) []Change {
	var changes []Change
	for alias, canonical := range aliasTable {
		if _, hasAlias := args[alias]; !hasAlias {
			continue
		}
		if isPropDeclared(params, alias) {
			continue // alias is a real parameter for this tool
		}
		if !isPropDeclared(params, canonical) {
			continue
		}
		if _, hasCanonical := args[canonical]; hasCanonical {
			continue
		}
		args[canonical] = args[alias]
		delete(args, alias)
		changes = append(changes, Change{Kind: ChangeAlias, Field: canonical, Detail: alias + "→" + canonical})
	}
	return changes
}

func schemaProps(params map[string]any) map[string]any {
	p, _ := params["properties"].(map[string]any)
	return p
}

func isPropDeclared(params map[string]any, key string) bool {
	_, ok := schemaProps(params)[key]
	return ok
}

func additionalPropsFalse(params map[string]any) bool {
	ap, ok := params["additionalProperties"].(bool)
	return ok && !ap
}

// applyCoercions converts unambiguously-mistyped scalar args to the declared
// type. Numbers become float64 (JSON's native map type). It never coerces an
// ambiguous value (e.g. a non-numeric string against a number schema).
func applyCoercions(params, args map[string]any) []Change {
	props := schemaProps(params)
	var changes []Change
	for key, raw := range args {
		p, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		typ := coercibleScalarType(p["type"])
		switch typ {
		case "boolean":
			s, ok := raw.(string)
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "true":
				args[key] = true
				changes = append(changes, Change{Kind: ChangeCoerceType, Field: key, Detail: `"` + s + `"→true`})
			case "false":
				args[key] = false
				changes = append(changes, Change{Kind: ChangeCoerceType, Field: key, Detail: `"` + s + `"→false`})
			}
		case "integer", "number":
			s, ok := raw.(string)
			if !ok {
				continue
			}
			f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				continue
			}
			args[key] = f
			changes = append(changes, Change{Kind: ChangeCoerceType, Field: key, Detail: `"` + s + `"→` + s})
		case "array":
			if _, isArr := raw.([]any); isArr {
				continue
			}
			args[key] = []any{raw}
			changes = append(changes, Change{Kind: ChangeCoerceType, Field: key, Detail: "scalar→[scalar]"})
		}
	}
	return changes
}

func coercibleScalarType(v any) string {
	if typ, ok := v.(string); ok {
		return typ
	}
	typ := nullableUnionNonNullType(v)
	switch typ {
	case "boolean", "integer", "number":
		return typ
	default:
		return ""
	}
}

// nullableUnionNonNullType returns the non-null type in an exact two-member
// nullable union. It deliberately makes no judgment about which schema types a
// caller supports; callers retain their own scalar allowlists.
func nullableUnionNonNullType(v any) string {
	var first, second string
	switch values := v.(type) {
	case []any:
		if len(values) != 2 {
			return ""
		}
		var ok bool
		first, ok = values[0].(string)
		if !ok {
			return ""
		}
		second, ok = values[1].(string)
		if !ok {
			return ""
		}
	case []string:
		if len(values) != 2 {
			return ""
		}
		first, second = values[0], values[1]
	default:
		return ""
	}
	if first == "null" && second != "null" {
		return second
	}
	if second == "null" && first != "null" {
		return first
	}
	return ""
}

// dropEmptyOptionalEnums removes an optional enum argument sent as "" or null,
// which models often send for a field they mean to leave out, so the
// parameter's documented default applies. It descends into nested objects and
// array items, each judged against its own schema's required list. A required
// argument, an enum that lists the empty value itself, or a kept path stays for
// validation or the handler to judge. obj is modified in place; the caller
// owns it.
func dropEmptyOptionalEnums(schema, obj map[string]any, path []pathStep, keep []string) []Change {
	props := schemaProps(schema)
	required := asStringSlice(schema["required"])
	var changes []Change
	for key, raw := range obj {
		p, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		at := append(slices.Clip(path), pathStep{name: key})
		if raw != nil && raw != "" {
			if nested, c := dropEmptyEnumsWithin(p, raw, at, keep); len(c) > 0 {
				obj[key] = nested
				changes = append(changes, c...)
			}
			continue
		}
		field := stepPathDisplay(at)
		if slices.Contains(required, key) || !hasListEntries(p["enum"]) || listHasValue(p["enum"], raw) || slices.Contains(keep, field) {
			continue
		}
		delete(obj, key)
		changes = append(changes, Change{Kind: ChangeNormalizeDefault, Field: field, Detail: "dropped empty optional " + field})
	}
	return changes
}

// dropEmptyEnumsWithin applies dropEmptyOptionalEnums inside a nested object or
// array value. It copies only the containers it changes, so the caller's value
// is never modified, and returns value itself when nothing changed.
func dropEmptyEnumsWithin(schema map[string]any, value any, path []pathStep, keep []string) (any, []Change) {
	switch v := value.(type) {
	case map[string]any:
		if schemaProps(schema) == nil {
			return value, nil
		}
		repaired := maps.Clone(v)
		if changes := dropEmptyOptionalEnums(schema, repaired, path, keep); len(changes) > 0 {
			return repaired, changes
		}
	case []any:
		items, ok := schema["items"].(map[string]any)
		if !ok {
			return value, nil
		}
		var repaired []any
		var changes []Change
		for i, element := range v {
			nested, c := dropEmptyEnumsWithin(items, element, append(slices.Clip(path), pathStep{name: strconv.Itoa(i), index: true}), keep)
			if len(c) == 0 {
				continue
			}
			if repaired == nil {
				repaired = slices.Clone(v)
			}
			repaired[i] = nested
			changes = append(changes, c...)
		}
		if repaired != nil {
			return repaired, changes
		}
	}
	return value, nil
}

// dropUnknown removes keys matching no declared property, but only when the
// schema forbids extra properties. It runs last so aliased/coerced keys survive.
func dropUnknown(params, args map[string]any) []Change {
	if !additionalPropsFalse(params) {
		return nil
	}
	props := schemaProps(params)
	var changes []Change
	for key := range args {
		if _, ok := props[key]; ok {
			continue
		}
		delete(args, key)
		changes = append(changes, Change{Kind: ChangeDropUnknown, Field: key, Detail: "dropped " + key})
	}
	return changes
}

// missingRequired lists the container schema's required property names that
// are absent from the instance object, in schema order.
func missingRequired(container map[string]any, inst map[string]any) []string {
	var missing []string
	for _, r := range asStringSlice(container["required"]) {
		if _, present := inst[r]; !present {
			missing = append(missing, r)
		}
	}
	return missing
}
