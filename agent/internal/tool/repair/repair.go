// Package repair heals off-distribution LLM tool calls: it renames aliased
// parameters, coerces mistyped scalars, drops hallucinated keys, and fixes
// broken JSON escapes. It is a pure, standard-library-only leaf package; the
// caller supplies a tool's JSON-Schema parameter object and the parsed args.
package repair

import (
	"strconv"
	"strings"
)

// ChangeKind names the category of a single repair.
type ChangeKind string

const (
	ChangeAlias         ChangeKind = "alias"
	ChangeCoerceType    ChangeKind = "coerce_type"
	ChangeDropUnknown   ChangeKind = "drop_unknown"
	ChangeUnicodeRepair ChangeKind = "unicode_repair"
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
// It applies, in order, aliasing, coercion (Task 2), and drop-unknown (Task 3).
// It never mutates its input; it returns a fresh map plus the changes made.
func RepairArgs(params, args map[string]any) (map[string]any, []Change) {
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	var changes []Change
	changes = append(changes, applyAliases(params, out)...)
	changes = append(changes, applyCoercions(params, out)...)
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
		typ, _ := p["type"].(string)
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
			if err != nil {
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
