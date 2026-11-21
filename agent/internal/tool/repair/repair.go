// Package repair heals off-distribution LLM tool calls: it renames aliased
// parameters, coerces mistyped scalars, drops optional arguments sent as a
// null their schema refuses (or "" on an enum), drops hallucinated keys, and
// fixes broken JSON escapes. It is a pure, standard-library-only leaf package;
// the caller supplies a tool's JSON-Schema parameter object and the parsed
// args.
package repair

import (
	"encoding/json"
	"maps"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
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

const (
	// maxStringifiedJSONBytes and maxStringifiedJSONDepth bound decoding of
	// stringified JSON array/object arguments without importing agent/internal/tool.
	maxStringifiedJSONBytes = 2 << 20
	maxStringifiedJSONDepth = 64
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
	"old_str":    "old_string",
	"oldText":    "old_string",
	"old_text":   "old_string",
	"new_str":    "new_string",
	"newText":    "new_string",
	"new_text":   "new_string",
	"replaceAll": "replace_all",
	"path":       "file_path",
	"filepath":   "file_path",
	"filename":   "file_path",
	"contents":   "content",
	"cmd":        "command",
}

type aliasGroup struct {
	canonical string
	aliases   []string
}

var sortedAliasGroups = buildSortedAliasGroups(aliasTable)

func buildSortedAliasGroups(table map[string]string) []aliasGroup {
	byCanonical := make(map[string][]string)
	for alias, canonical := range table {
		byCanonical[canonical] = append(byCanonical[canonical], alias)
	}
	canonicals := slices.Sorted(maps.Keys(byCanonical))
	groups := make([]aliasGroup, 0, len(canonicals))
	for _, canonical := range canonicals {
		aliases := byCanonical[canonical]
		slices.Sort(aliases)
		groups = append(groups, aliasGroup{canonical: canonical, aliases: aliases})
	}
	return groups
}

// RepairArgs normalizes args against the tool's JSON-Schema parameter object
// in a deterministic 5-phase order:
//  1. Root aliases (preserving competing aliases with differing values).
//  2. Single-item array promotion when a required array-of-objects key is
//     absent and unknown root keys cover its items.required, or mixed-form
//     conflict preservation when the array key is present (including null).
//  3. Root coercions (including bounded JSON decoding for object arrays and
//     bare-object wrapping).
//  4. Recursive nested object and array-item repair (copy-on-write).
//  5. Root empty-optional dropping and unknown-key dropping (skipping
//     preserved mixed-form or competing-alias keys).
//
// keep names argument paths (as "add[0].reasoning_effort") whose explicit empty
// value the tool's handler judges itself, so repair leaves them. It never
// mutates its input; it returns a fresh map plus the changes made.
func RepairArgs(params, args map[string]any, keep ...string) (map[string]any, []Change) {
	out := make(map[string]any, len(args))
	maps.Copy(out, args)
	var changes []Change

	// Phase 1: Root aliases.
	aliasChanges, rootPreserve := applyAliases(params, out, nil)
	changes = append(changes, aliasChanges...)

	// Phase 2: Single-item array promotion and mixed-form conflict detection.
	promoChanges, mixedPreserve := promoteSingleItemArrays(params, out, rootPreserve)
	changes = append(changes, promoChanges...)
	for k := range mixedPreserve {
		if rootPreserve == nil {
			rootPreserve = make(map[string]bool)
		}
		rootPreserve[k] = true
	}

	// Phase 3: Root coercions.
	changes = append(changes, applyCoercions(params, out, nil)...)

	// Phase 4: Recursive nested object and array-item repair.
	changes = append(changes, repairNestedProperties(params, out, nil, keep)...)

	// Phase 5: Root empty-optional dropping and unknown-key dropping.
	changes = append(changes, dropOwnEmptyOptionals(params, out, nil, keep)...)
	changes = append(changes, dropUnknown(params, out, nil, rootPreserve)...)

	return out, changes
}

func fieldDisplay(path []pathStep, key string) string {
	if len(path) == 0 {
		return key
	}
	return stepPathDisplay(append(slices.Clip(path), pathStep{name: key}))
}

// applyAliases renames aliased keys to canonical names under the safe-apply
// rule: rename X→Y only when Y is a declared property, X is not, X is present,
// and Y is absent. Iterates canonical targets and their aliases in sorted
// order. When Y is absent and multiple aliases targeting Y are present with
// differing values, none is promoted and all are returned in preserveUnknown so
// dropUnknown leaves them in place for schema validation to reject.
func applyAliases(schema, obj map[string]any, path []pathStep) ([]Change, map[string]bool) {
	var changes []Change
	var preserveUnknown map[string]bool

	for _, group := range sortedAliasGroups {
		canonical := group.canonical
		if !isPropDeclared(schema, canonical) {
			continue
		}
		var present []string
		for _, alias := range group.aliases {
			if _, hasAlias := obj[alias]; !hasAlias {
				continue
			}
			if isPropDeclared(schema, alias) {
				continue // alias is a real parameter for this schema
			}
			present = append(present, alias)
		}
		if len(present) == 0 {
			continue
		}
		if _, hasCanonical := obj[canonical]; hasCanonical {
			continue
		}
		if len(present) == 1 {
			alias := present[0]
			obj[canonical] = obj[alias]
			delete(obj, alias)
			changes = append(changes, Change{Kind: ChangeAlias, Field: fieldDisplay(path, canonical), Detail: alias + "→" + canonical})
			continue
		}
		allEqual := true
		firstVal := obj[present[0]]
		for _, alias := range present[1:] {
			if !reflect.DeepEqual(obj[alias], firstVal) {
				allEqual = false
				break
			}
		}
		if allEqual {
			winner := present[0]
			obj[canonical] = firstVal
			delete(obj, winner)
			changes = append(changes, Change{Kind: ChangeAlias, Field: fieldDisplay(path, canonical), Detail: winner + "→" + canonical})
			for _, dup := range present[1:] {
				delete(obj, dup)
				changes = append(changes, Change{Kind: ChangeDropUnknown, Field: fieldDisplay(path, dup), Detail: "dropped duplicate alias " + dup})
			}
			continue
		}
		if preserveUnknown == nil {
			preserveUnknown = make(map[string]bool, len(present))
		}
		for _, alias := range present {
			preserveUnknown[alias] = true
		}
	}
	return changes, preserveUnknown
}

type taskItemAliasSpec struct {
	alias     string
	canonical string
}

func applyTaskItemAlias(item map[string]any, path []pathStep, aliasKey, canonicalKey string) []Change {
	aliasVal, hasAlias := item[aliasKey]
	if !hasAlias {
		return nil
	}
	canonVal, hasCanon := item[canonicalKey]
	if isBlank(aliasVal) {
		delete(item, aliasKey)
		return []Change{{Kind: ChangeDropUnknown, Field: fieldDisplay(path, aliasKey), Detail: "dropped blank alias " + aliasKey}}
	}
	if !hasCanon || isBlank(canonVal) {
		item[canonicalKey] = aliasVal
		delete(item, aliasKey)
		return []Change{{Kind: ChangeAlias, Field: fieldDisplay(path, canonicalKey), Detail: aliasKey + "→" + canonicalKey}}
	}
	delete(item, aliasKey)
	return []Change{{Kind: ChangeDropUnknown, Field: fieldDisplay(path, aliasKey), Detail: "dropped duplicate alias " + aliasKey}}
}

// promoteSingleItemArrays inspects every required property K of root params
// whose schema is "type": "array" with an object items schema carrying a
// non-empty required list. Unknown root keys mapping (directly or via
// aliasTable) to declared properties of K.items either promote into K: [item]
// when K is completely absent and all K.items.required keys are covered, or
// are marked in preserveUnknown when K is already present (including nil) so
// Phase 5 leaves the mixed-form call invalid for schema validation to reject.
func promoteSingleItemArrays(params, obj map[string]any, rootPreserve map[string]bool) ([]Change, map[string]bool) {
	props := schemaProps(params)
	if len(props) == 0 {
		return nil, nil
	}
	var changes []Change
	var preserveUnknown map[string]bool

	for _, arrayKey := range asStringSlice(params["required"]) {
		propSchema, ok := props[arrayKey].(map[string]any)
		if !ok || !schemaIsArray(propSchema) {
			continue
		}
		itemsSchema, ok := propSchema["items"].(map[string]any)
		if !ok || !schemaIsObject(itemsSchema) {
			continue
		}
		itemRequired := asStringSlice(itemsSchema["required"])
		if len(itemRequired) == 0 || len(schemaProps(itemsSchema)) == 0 {
			continue
		}

		var matchedKeys []string
		covered := make(map[string]bool, len(itemRequired))
		for _, key := range slices.Sorted(maps.Keys(obj)) {
			if isPropDeclared(params, key) || rootPreserve[key] {
				continue
			}
			target := ""
			if isPropDeclared(itemsSchema, key) {
				target = key
			} else if aliasTarget, ok := aliasTable[key]; ok && isPropDeclared(itemsSchema, aliasTarget) {
				target = aliasTarget
			}
			if target == "" {
				continue
			}
			matchedKeys = append(matchedKeys, key)
			covered[target] = true
		}
		if len(matchedKeys) == 0 {
			continue
		}

		if _, hasKey := obj[arrayKey]; hasKey {
			if preserveUnknown == nil {
				preserveUnknown = make(map[string]bool, len(matchedKeys))
			}
			for _, key := range matchedKeys {
				preserveUnknown[key] = true
			}
			continue
		}

		allCovered := true
		for _, req := range itemRequired {
			if !covered[req] {
				allCovered = false
				break
			}
		}
		if !allCovered {
			continue
		}

		item := make(map[string]any, len(matchedKeys))
		for _, key := range matchedKeys {
			item[key] = obj[key]
			delete(obj, key)
		}
		obj[arrayKey] = []any{item}
		changes = append(changes, Change{Kind: ChangeCoerceType, Field: arrayKey, Detail: "fields→[object]"})
	}
	return changes, preserveUnknown
}

func itemSchemaDeclaresOnly(params map[string]any, arrayKey, targetProp, aliasProp string) bool {
	props := schemaProps(params)
	if props == nil {
		return false
	}
	arraySchema, ok := props[arrayKey].(map[string]any)
	if !ok {
		return false
	}
	itemsSchema, ok := arraySchema["items"].(map[string]any)
	if !ok {
		return false
	}
	itemProps := schemaProps(itemsSchema)
	if itemProps == nil {
		return false
	}
	_, hasTarget := itemProps[targetProp]
	_, hasAlias := itemProps[aliasProp]
	return hasTarget && !hasAlias
}

func isBlank(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	return strings.TrimSpace(s) == ""
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
// For array properties whose items declare type "object", string values are
// boundedly decoded as JSON arrays/objects (never wrapping plain non-JSON
// strings) and bare objects are wrapped into single-element slices.
func applyCoercions(schema, obj map[string]any, path []pathStep) []Change {
	props := schemaProps(schema)
	if len(props) == 0 {
		return nil
	}
	var changes []Change
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		raw := obj[key]
		p, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		field := fieldDisplay(path, key)
		typ := coercibleScalarType(p["type"])
		switch typ {
		case "boolean":
			s, ok := raw.(string)
			if !ok {
				continue
			}
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "true":
				obj[key] = true
				changes = append(changes, Change{Kind: ChangeCoerceType, Field: field, Detail: `"` + s + `"→true`})
			case "false":
				obj[key] = false
				changes = append(changes, Change{Kind: ChangeCoerceType, Field: field, Detail: `"` + s + `"→false`})
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
			obj[key] = f
			changes = append(changes, Change{Kind: ChangeCoerceType, Field: field, Detail: `"` + s + `"→` + s})
		case "array":
			if _, isArr := raw.([]any); isArr || raw == nil {
				continue
			}
			itemsSchema, hasItems := p["items"].(map[string]any)
			if hasItems && schemaIsObject(itemsSchema) {
				switch v := raw.(type) {
				case string:
					if decoded, detail, ok := decodeBoundedObjectArrayJSON(v); ok {
						obj[key] = decoded
						changes = append(changes, Change{Kind: ChangeCoerceType, Field: field, Detail: detail})
					}
				case map[string]any:
					obj[key] = []any{v}
					changes = append(changes, Change{Kind: ChangeCoerceType, Field: field, Detail: "scalar→[scalar]"})
				}
				continue
			}
			obj[key] = []any{raw}
			changes = append(changes, Change{Kind: ChangeCoerceType, Field: field, Detail: "scalar→[scalar]"})
		}
	}
	return changes
}

func decodeBoundedObjectArrayJSON(raw string) ([]any, string, bool) {
	if len(raw) > maxStringifiedJSONBytes || !utf8.ValidString(raw) || !withinJSONDepth(raw, maxStringifiedJSONDepth) {
		return nil, "", false
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || (trimmed[0] != '[' && trimmed[0] != '{') {
		return nil, "", false
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return nil, "", false
	}
	switch v := decoded.(type) {
	case []any:
		return v, "json_string→array", true
	case map[string]any:
		return []any{v}, "json_string→[object]", true
	default:
		return nil, "", false
	}
}

func withinJSONDepth(s string, maxDepth int) bool {
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			if inString {
				escaped = !escaped
			}
			continue
		case '"':
			if !escaped {
				inString = !inString
			}
			escaped = false
			continue
		default:
			escaped = false
		}
		if inString {
			continue
		}
		switch s[i] {
		case '{', '[':
			depth++
			if depth > maxDepth {
				return false
			}
		case '}', ']':
			depth--
		}
	}
	return true
}

func coercibleScalarType(v any) string {
	if typ, ok := v.(string); ok {
		return typ
	}
	typ := nullableUnionNonNullType(v)
	switch typ {
	case "boolean", "integer", "number", "array":
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

// repairNestedProperties walks nested objects and arrays in obj (copy-on-write)
// and applies nested aliases, coercions, empty-optional removal, and unknown-key
// removal at every nested object level.
func repairNestedProperties(schema, obj map[string]any, path []pathStep, keep []string) []Change {
	props := schemaProps(schema)
	if len(props) == 0 {
		return nil
	}
	var changes []Change
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		raw := obj[key]
		if raw == nil || raw == "" {
			continue
		}
		propSchema, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		var taskAlias taskItemAliasSpec
		if len(path) == 0 {
			switch key {
			case "add":
				if itemSchemaDeclaresOnly(schema, "add", "description", "title") {
					taskAlias = taskItemAliasSpec{alias: "title", canonical: "description"}
				}
			case "task_list":
				if itemSchemaDeclaresOnly(schema, "task_list", "title", "description") {
					taskAlias = taskItemAliasSpec{alias: "description", canonical: "title"}
				}
			}
		}
		at := append(slices.Clip(path), pathStep{name: key})
		if nested, c := repairNestedValue(propSchema, raw, at, keep, taskAlias); len(c) > 0 {
			obj[key] = nested
			changes = append(changes, c...)
		}
	}
	return changes
}

func repairNestedValue(schema map[string]any, value any, path []pathStep, keep []string, taskAlias taskItemAliasSpec) (any, []Change) {
	switch v := value.(type) {
	case map[string]any:
		if schemaProps(schema) == nil {
			return value, nil
		}
		repaired := maps.Clone(v)
		var changes []Change
		aliasChanges, preserveUnknown := applyAliases(schema, repaired, path)
		changes = append(changes, aliasChanges...)
		if taskAlias.alias != "" {
			changes = append(changes, applyTaskItemAlias(repaired, path, taskAlias.alias, taskAlias.canonical)...)
		}
		changes = append(changes, applyCoercions(schema, repaired, path)...)
		changes = append(changes, repairNestedProperties(schema, repaired, path, keep)...)
		changes = append(changes, dropOwnEmptyOptionals(schema, repaired, path, keep)...)
		changes = append(changes, dropUnknown(schema, repaired, path, preserveUnknown)...)
		if len(changes) > 0 {
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
			elemPath := append(slices.Clip(path), pathStep{name: strconv.Itoa(i), index: true})
			nested, c := repairNestedValue(items, element, elemPath, keep, taskAlias)
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

// dropOwnEmptyOptionals removes an optional argument sent as null when its
// schema does not accept null, and an optional enum argument sent as "" when
// the enum does not list "". Models often send these for a field they mean to
// leave out, so the parameter's documented default applies. "" on a plain
// string stays: it can be a meaningful value. A required argument or a kept
// path stays for validation or the handler to judge. obj is modified in place;
// the caller owns it.
func dropOwnEmptyOptionals(schema, obj map[string]any, path []pathStep, keep []string) []Change {
	props := schemaProps(schema)
	if len(props) == 0 {
		return nil
	}
	required := asStringSlice(schema["required"])
	var changes []Change
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		raw := obj[key]
		if raw != nil && raw != "" {
			continue
		}
		p, ok := props[key].(map[string]any)
		if !ok {
			continue
		}
		field := fieldDisplay(path, key)
		if slices.Contains(required, key) || !EmptyMeansAbsent(p, raw) || slices.Contains(keep, field) {
			continue
		}
		delete(obj, key)
		changes = append(changes, Change{Kind: ChangeNormalizeDefault, Field: field, Detail: "dropped empty optional " + field})
	}
	return changes
}

// EmptyMeansAbsent reports whether raw, which is null or "", is a value the
// property's schema refuses and so can only mean the model left it out. null
// counts whenever the schema refuses it; "" only on an enum that doesn't list
// it. The agent-layer hub-branch guard consults it too, so its refusal skips
// exactly the placeholders dropEmptyOptionals drops and names only arguments
// the caller actually configured.
func EmptyMeansAbsent(schema map[string]any, raw any) bool {
	if raw == nil {
		return !schemaAcceptsNull(schema)
	}
	return hasListEntries(schema["enum"]) && !listHasValue(schema["enum"], raw)
}

// schemaAcceptsNull reports whether schema admits null: its own keywords
// (schemaOwnKeywordsMatch, which reads them without applying a combinator),
// then its combinators — some anyOf or oneOf branch (oneOf's "exactly one"
// can't turn a null-accepting branch into a refusal worth repairing), and
// every allOf branch. The package resolves no $ref, so a property behind one
// counts as accepting null and its null stays for validation to judge.
func schemaAcceptsNull(schema map[string]any) bool {
	if !schemaOwnKeywordsMatch(nil, schema) {
		return false
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		if branches, _ := schema[keyword].([]any); len(branches) > 0 && !slices.ContainsFunc(branches, branchAcceptsNull) {
			return false
		}
	}
	allOf, _ := schema["allOf"].([]any)
	for _, branch := range allOf {
		if !branchAcceptsNull(branch) {
			return false
		}
	}
	if negated, present := schema["not"]; present && definitelyAcceptsNull(negated) {
		return false
	}
	return true
}

// branchAcceptsNull applies schemaAcceptsNull to one combinator branch. A
// boolean schema accepts null exactly when it is true; any other non-object
// branch imposes nothing here.
func branchAcceptsNull(branch any) bool {
	switch schema := branch.(type) {
	case bool:
		return schema
	case map[string]any:
		return schemaAcceptsNull(schema)
	}
	return true
}

// definitelyAcceptsNull reports whether a `not` subschema is sure to accept
// null, so the `not` refuses it. Only true, or an object whose own keywords
// accept null and that carries no combinator, conditional or reference of its
// own, is sure; anything else keeps the null for validation to judge.
func definitelyAcceptsNull(negated any) bool {
	switch schema := negated.(type) {
	case bool:
		return schema
	case map[string]any:
		for key := range schema {
			switch {
			case isRefSegment(key):
				return false
			case key == "anyOf", key == "oneOf", key == "allOf", key == "not", key == "if":
				return false
			}
		}
		return schemaOwnKeywordsMatch(nil, schema)
	}
	return false
}

// dropUnknown removes keys matching no declared property, but only when the
// schema forbids extra properties. Keys in preserveUnknown are left untouched so
// ambiguous or mixed-form calls fail schema validation.
func dropUnknown(schema, obj map[string]any, path []pathStep, preserveUnknown map[string]bool) []Change {
	if !additionalPropsFalse(schema) {
		return nil
	}
	props := schemaProps(schema)
	var changes []Change
	for _, key := range slices.Sorted(maps.Keys(obj)) {
		if _, ok := props[key]; ok || preserveUnknown[key] {
			continue
		}
		delete(obj, key)
		changes = append(changes, Change{Kind: ChangeDropUnknown, Field: fieldDisplay(path, key), Detail: "dropped " + key})
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
