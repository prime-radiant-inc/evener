package repair

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// refKeywords are the JSON-Schema reference applicators. They resolve to
// another schema node that is not available here, so every conservative guard
// that cannot evaluate them must treat all three alike. The guard key lists
// below derive their reference entries from this one slice — appended, never
// hand-written per list — so $ref, $dynamicRef and $recursiveRef cannot drift
// apart (issue #624 review; mirrors the shared isRefSegment predicate).
var refKeywords = []string{"$ref", "$dynamicRef", "$recursiveRef"}

// ExplainSchemaError renders model-facing coaching for a call that failed
// validation and could not be repaired. instanceLocation, when non-empty, is
// the deepest cause's JSON-Pointer-style path into args (e.g. "old_string",
// "updates/0", "questions/0/header" — as produced by offendingField);
// otherwise the message lists all required args at the top level.
//
// The path is walked against params (schema) and args (instance) in lockstep
// to find the container the error is really about, so a nested failure
// reports the real field, its location, and that container's required list
// — not a misleading top-level guess. Any segment the walk can't resolve
// (malformed path, schema shape mismatch) falls back to treating the whole
// original string as a single top-level field name.
//
// constraintKeywordLocation, when non-empty, is the deepest cause's
// KeywordLocation (e.g. "properties/x/maxLength", "oneOf/0/not"), supplied by
// the caller (offendingKeywordLocation). Its last segment names the keyword; a
// bare keyword ("maxLength") is a single-segment location. Its root segment
// (after a $ref wrapper) drives branch attribution: a cause under a root-level
// oneOf is explained as that branch rule rather than by an arm-internal leaf
// read against a top-level property (issue #621). When the field is
// present and the keyword is a recognized value constraint (maxLength,
// minLength, minItems, maxItems, enum), the message names the actual
// constraint, its limit, and the offending value/length instead of the
// generic "wrong type or value" message — which otherwise sends the caller
// debugging a parameter that was correctly supplied (issue #193). A present
// field never gets the "Required arguments" tail, recognized keyword or not:
// that tail lists already-satisfied sibling fields, which is misleading
// regardless of whether the specific constraint can be detailed.
func ExplainSchemaError(toolName string, params, args map[string]any, instanceLocation, constraintKeywordLocation string) string {
	var b strings.Builder

	constraintKeyword := lastKeywordSegment(constraintKeywordLocation)
	branchKeyword := branchCombinator(constraintKeywordLocation)

	containerSchema := params
	containerPath := ""
	field := ""
	present := false
	haveField := false

	if instanceLocation != "" {
		haveField = true
		if cs, cp, f, pres, ok := resolveSchemaErrorContainer(params, args, instanceLocation); ok {
			containerSchema, containerPath, field, present = cs, cp, f, pres
		} else {
			// Fall back: treat the whole original path string as a single
			// top-level field name (today's flat behavior).
			containerSchema, containerPath, field = params, "", instanceLocation
			_, present = args[instanceLocation]
		}
	}

	// A oneOf nested inside a property's (or array item's) schema (issue #624)
	// fails with a keyword location that descends through that combinator, but its
	// instance location is the container property (or an array index the
	// missing-field fallback cannot resolve). Explain that combinator, scoped to
	// the holder's own instance path, before the root-combinator handling below.
	// enclosingOneOf returns false for a root-level combinator, so issue #621's
	// prefix attribution still owns those failures.
	if msg := nestedOneOfMessage(toolName, params, constraintKeywordLocation, instanceLocation); msg != "" {
		return msg
	}

	// A branch-combinator failure is attributed to the branch, not to any single
	// property: the deepest cause is the combinator itself (/oneOf/0/not, issue
	// #618) or a keyword nested inside an arm
	// (/oneOf/0/properties/sandbox/enum, issue #621 — which leaf the walk reaches
	// depends on the arm order). Render the branch-level constraints before the
	// present-field path below, whose constraintMessage reads a leaf keyword
	// against the container's TOP-LEVEL property and can announce an
	// allowed-values list — say, the top-level enum listing "off" — that the
	// failing branch actually narrows away (issue #621).
	//
	// The early return is taken only when the branch prose actually covers the
	// failure (see branchCoversFailure): a constraint on a present property is a
	// single-argument defect the prose never names — it renders only the
	// combinator and its arms' required/enum — so claiming the failure is "not
	// any single argument's type or value" while showing a requirement the call
	// already satisfied ("send all of \"x\"" when x was sent) is worse than the
	// present-field path, which names the property and its defect (issue #621
	// review).
	refWrapped := refWrappedKeywordLocation(constraintKeywordLocation)

	// A reference-wrapped cause always takes this path, whatever the leaf keyword
	// or whether the property is present: its schema lives in the referenced
	// node, so neither branchRequirement nor constraintMessage can read it
	// safely, and the top-level required list and example describe a different
	// schema (a /$ref/required failure would otherwise report top-level required
	// fields the call already supplied).
	if refWrapped {
		return toolName + ": arguments did not match the schema."
	}
	if hasRefSegment(constraintKeywordLocation) && !present {
		// A reference reached deeper in the path names no present field, so the
		// missing-field path would invent one; stay generic.
		return toolName + ": arguments did not match the schema."
	}
	if branchKeyword != "" {
		if branchCoversFailure(params, constraintKeyword, constraintKeywordLocation, present) {
			if msg := oneOfConstraintMessage(toolName, params, branchKeyword, constraintKeywordLocation); msg != "" {
				return msg
			}
			// The combinator was identified but its branches could not be
			// rendered: return the bare generic mismatch — no "Required arguments"
			// tail (it would claim a supplied argument was required) and no
			// Example (minimalExample renders only top-level required fields,
			// which need not satisfy the combinator).
			return toolName + ": arguments did not match the schema."
		}
		if !present {
			// Attributed to a combinator, not covered by its prose, and naming no
			// present field: a nested item/property defect the walk could not
			// resolve. Name nothing rather than a field that does not exist
			//.
			return toolName + ": arguments did not match the schema."
		}
	}

	// A constraint reached inside a root-level combinator may come from a schema
	// node that differs from the top-level property: an arm can carry a
	// stricter maxLength, or a narrower enum, than the top-level property, so
	// constraintMessage would state a limit or allowed-values list that does not
	// apply. Dropping the keyword leaves the present-field
	// path naming the property without the false detail.
	if branchKeyword != "" || hasRefSegment(constraintKeywordLocation) {
		constraintKeyword = ""
	}

	// A present-but-invalid field is a value-constraint violation, never a
	// missing-argument problem — the "Required arguments" tail below (which
	// lists already-satisfied sibling fields) is the misdirection issue #193
	// reports, so it never applies here, regardless of whether the specific
	// constraint keyword is one constraintMessage knows how to detail.
	if present {
		fullPath := field
		if containerPath != "" {
			fullPath = containerPath + "." + field
		}
		// The wrong branch's array can also reject a present field (issue
		// #626 round 2: {"action":"update","tasks":[{"type":123}]}), and the
		// early return here would otherwise bypass the branch-naming below —
		// entering the same unrecoverable loop one step later, via a type
		// error instead of a missing one. Attribute the branch here too.
		ctx := newBranchCtx(params, args, containerPath)
		if constraintKeyword != "" {
			if specific := constraintMessage(toolName, containerSchema, fullPath, field, constraintKeyword, args, instanceLocation); specific != "" {
				return specific + ctx.wrongBranchTail(args)
			}
		}
		// A branch-attributed failure reaches here when the branch prose could
		// not describe it, and a failure behind a $ref reaches here because the
		// referenced schema owns the real constraint. In both cases the example
		// is the top-level required shape, unchecked against what actually
		// failed — for a oneOf arm it can satisfy no branch — so this path names
		// the field without one.
		if branchKeyword != "" || hasRefSegment(constraintKeywordLocation) {
			return fmt.Sprintf("%s: argument %q has the wrong type or value.%s", toolName, fullPath, ctx.wrongBranchTail(args))
		}
		return fmt.Sprintf("%s: argument %q has the wrong type or value.\nExample: %s%s", toolName, fullPath, ctx.example(params), ctx.wrongBranchTail(args))
	}

	switch {
	case !haveField:
		fmt.Fprintf(&b, "%s: arguments did not match the schema.", toolName)
	case containerPath == "":
		fmt.Fprintf(&b, "%s: missing required argument %q.", toolName, field)
	default:
		fmt.Fprintf(&b, "%s: missing required argument %q in %s.", toolName, field, containerPath)
	}

	// A conditional sub-schema's required list describes one action's branch,
	// not the call the caller made (issue #626: task_list's tasks[0] belongs to
	// append, but an update caller read the bare list as describing its own
	// call and retried into an unrecoverable loop). When the failing container
	// sits inside an array property scoped to an action the caller did not
	// send, name that branch, and point the caller at the array its own action
	// takes instead.
	ctx := newBranchCtx(params, args, containerPath)
	branchNamed := ctx.branchValue != ""
	// The Example shows the caller's own branch shape whenever the failure
	// sits inside an action-scoped array — a same-branch caller (append
	// missing prompt) needs it just as much as a wrong-branch one (issue
	// #626 complaint 3: the action-only Example gave neither a usable
	// template). Only the caller-sent-wrong-array tail is wrong-branch-only.
	req := requiredList(containerSchema)
	if len(req) > 0 {
		if branchNamed {
			fmt.Fprintf(&b, "\nRequired arguments in %s for action %q: %s.", containerPath, ctx.branchValue, strings.Join(req, ", "))
		} else if containerPath == "" {
			fmt.Fprintf(&b, "\nRequired arguments: %s.", strings.Join(req, ", "))
		} else {
			fmt.Fprintf(&b, "\nRequired arguments in %s: %s.", containerPath, strings.Join(req, ", "))
		}
	}
	if branchNamed || ctx.actionArrays != nil {
		fmt.Fprintf(&b, "\nExample: %s", ctx.example(params))
		if tail := ctx.takesClause(args); tail != "" {
			b.WriteString(tail)
		}
	} else {
		fmt.Fprintf(&b, "\nExample: %s", sentArrayExample(params, args, containerPath))
	}
	return b.String()
}

// sentArrayExample renders an example for a failure inside an array item the
// caller actually sent: the array property with one minimal item template
// ({"update": [{"id": 0}]}). A schema with no top-level required (presence-
// based tools like task_list) otherwise renders a bare {}, which gives a
// caller that just mis-shaped an array item no usable template. Falls back
// to minimalExample when the failure is not in a sent array property or the
// array has no item schema.
func sentArrayExample(params, args map[string]any, containerPath string) string {
	root := pathRootProperty(containerPath)
	if root == "" {
		return minimalExample(params)
	}
	if _, sent := args[root]; !sent {
		return minimalExample(params)
	}
	item := arrayItemSchema(params, root)
	if item == nil {
		return minimalExample(params)
	}
	return fmt.Sprintf(`{%q: [%s]}`, root, minimalExample(item))
}

// arrayItemSchema returns the item schema of a declared array property, or
// nil when the property is not a declared array or has no item schema.
// Shared descent for the example renderers (sentArrayExample,
// actionExample).
func arrayItemSchema(params map[string]any, arrayName string) map[string]any {
	props := schemaProps(params)
	arraySchema := schemaMap(props, arrayName)
	if arraySchema == nil || !schemaIsArray(arraySchema) {
		return nil
	}
	return schemaMap(arraySchema, "items")
}

// branchCtx is the branch context for one explained error, computed once
// and shared by the present-field and missing-field paths (issue #626 round
// 3: the two paths previously computed it independently, twice each).
type branchCtx struct {
	selectorName string
	actionValue  string
	branchValue  string // non-empty only when the failure sits in another action's branch
	actionArrays []string
}

// newBranchCtx resolves the branch context for the failing container's
// path. selectorName and actionValue are set whenever the schema has an
// action selector the caller exercised (a same-branch caller still gets its
// own branch's Example); branchValue is non-empty only when the container's
// property is scoped to a different action's branch.
func newBranchCtx(params, args map[string]any, containerPath string) branchCtx {
	selectorName, actionValue, branchValue := namedBranch(params, args, containerPath)
	return branchCtx{
		selectorName: selectorName,
		actionValue:  actionValue,
		branchValue:  branchValue,
		actionArrays: actionScopedArrays(schemaProps(params), actionValue),
	}
}

// wrongBranchTail renders the wrong-branch attribution appended to a
// present-field failure's message. Empty when the failure is not in another
// action's branch. Unlike takesClause it names the branch itself, because a
// present-field message carries no branch-phrased line of its own.
func (c branchCtx) wrongBranchTail(args map[string]any) string {
	if c.branchValue == "" {
		return ""
	}
	missing := missingArray(c.actionArrays, args)
	sent := sentArgNames(c.selectorName, args)
	if missing == "" || sent == "" {
		// The correct array was also sent (or nothing else was): still name
		// the branch so the caller knows where the failure came from.
		return fmt.Sprintf(" (this failure is in the array for action %q)", c.branchValue)
	}
	return fmt.Sprintf(" (this failure is in the array for action %q; your action %q takes %q, not %s)", c.branchValue, c.actionValue, missing, sent)
}

// takesClause renders the "your action takes X" line appended after the
// Example on the missing-field path. Empty when the caller's action has no
// action-scoped array missing from the call.
func (c branchCtx) takesClause(args map[string]any) string {
	missing := missingArray(c.actionArrays, args)
	if missing == "" {
		return ""
	}
	sent := sentArgNames(c.selectorName, args)
	if sent == "" {
		return ""
	}
	return fmt.Sprintf(" Your action %q takes %q (sent: %s).", c.actionValue, missing, sent)
}

// example renders the Example for this failure: the caller's own branch
// shape when the failure sits inside an action-scoped array (same-branch or
// wrong-branch), the generic top-level Example otherwise.
func (c branchCtx) example(params map[string]any) string {
	if c.branchValue == "" && c.actionArrays == nil {
		return minimalExample(params)
	}
	return actionExample(params, c.selectorName, c.actionValue, c.actionArrays)
}

// namedBranch reports the branch context for a conditional-sub-schema
// failure (issue #626): the action selector's name, the action value the
// caller sent, and the action value whose branch the failing container's
// property is scoped to. selectorName and actionValue are set whenever the
// schema has an action selector the caller exercised (a same-branch caller
// still gets its own branch's Example); branchValue is non-empty only when
// the property's "For X:" description tag names a member of the selector's
// enum that differs from the caller's action — prose such as read_file's
// "For large files read in slices: ..." on offset parses to a tag, but no
// selector enum contains it, so it cannot masquerade as a branch. The enum
// membership check does not materialize a []string copy, keeping the
// not-firing path allocation-free.
// containerPath is the DISPLAY form produced by resolveSchemaErrorContainer
// ("tasks[0]", not the JSON-Pointer "tasks/0" that ExplainSchemaError takes
// as its instanceLocation) — pathRootProperty parses the bracket form.
func namedBranch(params, args map[string]any, containerPath string) (selectorName, actionValue, branchValue string) {
	selectorName, actionValue = actionSelector(params, args)
	if selectorName == "" {
		return "", "", ""
	}
	props := schemaProps(params)
	tag := actionTag(schemaMap(props, pathRootProperty(containerPath)))
	if tag == "" || tag == actionValue {
		return selectorName, actionValue, ""
	}
	if !listContains(schemaMap(props, selectorName)["enum"], tag) {
		return selectorName, actionValue, ""
	}
	return selectorName, actionValue, tag
}

// listContains reports whether v is a string list ([]string hand-built or
// []any from JSON) containing s, without materializing a []string copy.
// Sibling of hasListEntries.
func listContains(v any, s string) bool {
	switch list := v.(type) {
	case []string:
		return slices.Contains(list, s)
	case []any:
		for _, e := range list {
			if str, ok := e.(string); ok && str == s {
				return true
			}
		}
	}
	return false
}

// actionSelector finds the action/operation selector in params: a required
// string property with an enum, whose value the caller actually sent. It
// walks the schema's required list (not the properties map) so selection is
// deterministic when more than one property qualifies, and materializes no
// string slices: this runs on every explained error, including MCP schemas
// where it never fires.
func actionSelector(params, args map[string]any) (name, value string) {
	props := schemaProps(params)
	if props == nil {
		return "", ""
	}
	switch required := params["required"].(type) {
	case []string:
		for _, propName := range required {
			if v, ok := selectorValue(props, args, propName); ok {
				return propName, v
			}
		}
	case []any:
		for _, raw := range required {
			propName, ok := raw.(string)
			if !ok {
				continue
			}
			if v, ok := selectorValue(props, args, propName); ok {
				return propName, v
			}
		}
	}
	return "", ""
}

// selectorValue reports whether propName is the action selector — a string
// property with a non-empty enum that the caller sent a value for — and
// returns that value. The enum is checked for presence only, not
// materialized; namedBranch materializes it once a branch tag actually needs
// validating against it.
func selectorValue(props, args map[string]any, propName string) (string, bool) {
	p := schemaMap(props, propName)
	if p == nil {
		return "", false
	}
	if t, _ := p["type"].(string); t != "string" {
		return "", false
	}
	sent, _ := args[propName].(string)
	if sent == "" || !hasListEntries(p["enum"]) {
		return "", false
	}
	return sent, true
}

// hasListEntries reports whether v is a non-empty slice — the shapes a
// schema's enum or required list may carry ([]string hand-built, []any from
// JSON) — without materializing a []string copy.
func hasListEntries(v any) bool {
	switch s := v.(type) {
	case []string:
		return len(s) > 0
	case []any:
		return len(s) > 0
	}
	return false
}

// actionTag returns the action value a property schema is scoped to by its
// description tag ("For append: ..." → "append"), or "" when the description
// carries no such tag. Only task_list's tasks/updates descriptions carry the
// tag today; the enum-membership check in namedBranch is what makes a parsed
// tag count as a branch.
func actionTag(propSchema map[string]any) string {
	if propSchema == nil {
		return ""
	}
	desc, _ := propSchema["description"].(string)
	rest, ok := strings.CutPrefix(desc, "For ")
	if !ok {
		return ""
	}
	if value, _, ok := strings.Cut(rest, ":"); ok {
		return value
	}
	return ""
}

// pathRootProperty extracts the top-level property name a display path
// starts with ("tasks[0]" → "tasks", "updates[0].id" → "updates"). Returns
// "" for a path with no leading property segment.
func pathRootProperty(containerPath string) string {
	if containerPath == "" {
		return ""
	}
	seg := containerPath
	if idx := strings.IndexAny(seg, "[."); idx >= 0 {
		seg = seg[:idx]
	}
	return seg
}

// actionScopedArrays returns the array property names scoped to the given
// action value by their description tag, sorted for deterministic output.
func actionScopedArrays(props map[string]any, actionValue string) []string {
	if actionValue == "" {
		return nil
	}
	var out []string
	for propName, p := range props {
		schema, ok := p.(map[string]any)
		if ok && schemaIsArray(schema) && actionTag(schema) == actionValue {
			out = append(out, propName)
		}
	}
	sort.Strings(out)
	return out
}

// missingArray returns the first action-scoped array name absent from the
// call — the array the caller should have sent instead of the one that failed
// (issue #626: update takes "updates", not "tasks") — or "" when every
// action-scoped array was supplied.
func missingArray(actionArrays []string, args map[string]any) string {
	for _, name := range actionArrays {
		if _, ok := args[name]; !ok {
			return name
		}
	}
	return ""
}

// sentArgNames renders the argument names the caller actually sent, sorted,
// excluding the action selector itself so the contrast the message draws —
// this array, not that one — stays sharp. Returns "" when nothing else was
// sent.
func sentArgNames(selectorName string, args map[string]any) string {
	names := make([]string, 0, len(args))
	for name := range args {
		if name == selectorName {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return ""
	}
	return strings.Join(names, ", ")
}

// actionExample renders a minimal example for the branch the caller actually
// used: the action plus its tagged array with one minimal item. The item
// renders through minimalExample (the same sorted required list with type
// placeholders it builds for any schema), wrapped in the array's brackets.
// Falls back to minimalExample(params) when the branch has no tagged array
// or the array has no item schema.
func actionExample(params map[string]any, selectorName, actionValue string, actionArrays []string) string {
	for _, arrayName := range actionArrays {
		item := arrayItemSchema(params, arrayName)
		if item == nil {
			continue
		}
		return fmt.Sprintf(`{%q: %q, %q: [%s]}`, selectorName, actionValue, arrayName, minimalExample(item))
	}
	return minimalExample(params)
}

// constraintMessage renders a specific constraint-violation message for a
// present field whose schema rejected it: the field's display path, the
// constraint name, its limit, and the actual value/length. Returns "" when
// the keyword is not one of the recognized constraints (maxLength,
// minLength, minItems, maxItems, enum, required) or when the schema/value
// shape doesn't match the keyword, so the caller falls back to the generic
// "wrong type or value" message.
func constraintMessage(toolName string, containerSchema map[string]any, displayPath string, field, keyword string, args map[string]any, instanceLocation string) string {
	fieldSchema, _ := schemaProps(containerSchema)[field].(map[string]any)
	if fieldSchema == nil {
		return ""
	}
	value := resolveInstanceValue(args, instanceLocation)
	switch keyword {
	case "maxLength":
		limit, ok := schemaInt(fieldSchema["maxLength"])
		if !ok {
			return ""
		}
		s, _ := value.(string)
		n := utf8.RuneCountInString(s)
		return fmt.Sprintf("%s: argument %q exceeds maxLength (%d). Value %q is %d characters.", toolName, displayPath, limit, s, n)
	case "minLength":
		limit, ok := schemaInt(fieldSchema["minLength"])
		if !ok {
			return ""
		}
		s, _ := value.(string)
		n := utf8.RuneCountInString(s)
		return fmt.Sprintf("%s: argument %q is below minLength (%d). Value %q is %d characters.", toolName, displayPath, limit, s, n)
	case "maxItems":
		limit, ok := schemaInt(fieldSchema["maxItems"])
		if !ok {
			return ""
		}
		arr, _ := value.([]any)
		return fmt.Sprintf("%s: argument %q exceeds maxItems (%d). Value has %d items.", toolName, displayPath, limit, len(arr))
	case "minItems":
		limit, ok := schemaInt(fieldSchema["minItems"])
		if !ok {
			return ""
		}
		arr, _ := value.([]any)
		return fmt.Sprintf("%s: argument %q is below minItems (%d). Value has %d items.", toolName, displayPath, limit, len(arr))
	case "enum":
		allowed := formatEnumValues(fieldSchema["enum"])
		if len(allowed) == 0 {
			return ""
		}
		return fmt.Sprintf("%s: argument %q is not one of the allowed values: %s. Value is %q.", toolName, displayPath, strings.Join(allowed, ", "), fmt.Sprint(value))
	case "required":
		// The field itself is present but its object value is missing required
		// properties (the issue #627 shape: communicate's output object without
		// its nested message/data/artifacts). Name them rather than reporting
		// the whole object as a wrong type or value, and show the accepted
		// shape, resolved from the field's own schema (the container it lives
		// in), never from a same-named top-level property.
		inst, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		missing := missingRequired(fieldSchema, inst)
		if len(missing) == 0 {
			return ""
		}
		for i, name := range missing {
			missing[i] = fmt.Sprintf("%s.%s", displayPath, name)
		}
		return fmt.Sprintf("%s: argument %q is missing required properties: %s.\nExample: %s",
			toolName, displayPath, strings.Join(missing, ", "), exampleForField(containerSchema, field))
	}
	return ""
}

// exampleForField renders a minimal example naming just the failing field,
// with its nested required shape expanded (issue #627: the example must show
// the accepted output envelope, not a bare {}). containerSchema is the schema
// of the object holding field — the example resolves field's shape there, so
// a nested field never picks up a same-named top-level property's schema.
func exampleForField(containerSchema map[string]any, field string) string {
	props := schemaProps(containerSchema)
	if prop, ok := props[field].(map[string]any); ok {
		typ := exampleSchemaType(prop["type"])
		return fmt.Sprintf("{%q: %s}", field, exampleValue(prop, typ))
	}
	return exampleObject(containerSchema, true)
}

// exampleSchemaType preserves a direct schema type, or selects the only
// non-null scalar from a two-member nullable union. Other union shapes retain
// the existing string-placeholder fallback rather than guessing an example.
func exampleSchemaType(v any) string {
	if typ, ok := v.(string); ok {
		return typ
	}
	typ := nullableUnionNonNullType(v)
	switch typ {
	case "string", "integer", "number", "boolean":
		return typ
	default:
		return ""
	}
}

// resolveInstanceValue walks a JSON-Pointer-style path (e.g.
// "questions/0/header") against the parsed args, alternating object-property
// and array-index steps, and returns the value at that location (or nil when
// any step can't be resolved). Mirrors the instance walk in
// resolveSchemaErrorContainer without mutating it.
func resolveInstanceValue(args map[string]any, path string) any {
	if path == "" {
		return nil
	}
	var cur any = args
	for seg := range strings.SplitSeq(path, "/") {
		if idx, isIdx := arrayIndex(seg); isIdx {
			arr, ok := cur.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return nil
			}
			cur = arr[idx]
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[seg]
	}
	return cur
}

// schemaInt extracts an integer from a JSON-Schema numeric constraint value,
// tolerating the float64/int/int64 forms a Go map[string]any or JSON-unmarshaled
// schema may carry.
func schemaInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

// resolveSchemaErrorContainer walks instanceLocation (split on "/") against
// params/args in lockstep, alternating object-property steps (schema
// properties[seg]) and array-index steps (schema items, when the current
// schema node is type:array and seg parses as an integer).
//
// Two shapes come out the other end:
//   - The final segment resolves as a declared property of its parent
//     schema node (e.g. ".../header"): the field is that property, its
//     container is the parent, and containerPath is the path to the parent
//     ("questions[0]" for "questions/0/header"). present reports whether the
//     field's value is set in the parent instance — the caller uses that to
//     choose "wrong type" vs "missing required" (with field rewritten to the
//     full path for the "wrong type" case, done by the caller).
//   - The final segment is an array index with nothing after it (e.g.
//     "updates/0"): container and the node the error is actually about are
//     the same object — the diff is which of that item's required
//     properties is absent from the instance. field is the first missing
//     one (schema/required-list order); present is always false.
//
// ok is false when any step can't be resolved against the schema (a
// malformed path, or a schema shape that doesn't match it) — the caller
// falls back to flat top-level behavior in that case. Never panics.
func resolveSchemaErrorContainer(params, args map[string]any, instanceLocation string) (containerSchema map[string]any, containerPath, field string, present, ok bool) {
	segs := strings.Split(instanceLocation, "/")

	schemas := make([]map[string]any, len(segs)+1)
	insts := make([]any, len(segs)+1)
	kinds := make([]bool, len(segs)) // true = array-index step

	schemas[0] = params
	insts[0] = any(args)

	for i, seg := range segs {
		cur := schemas[i]
		if cur == nil {
			return nil, "", "", false, false
		}
		if idx, isIdx := arrayIndex(seg); isIdx && schemaIsArray(cur) {
			items, _ := cur["items"].(map[string]any)
			if items == nil {
				return nil, "", "", false, false
			}
			schemas[i+1] = items
			kinds[i] = true
			if arr, isArr := insts[i].([]any); isArr && idx >= 0 && idx < len(arr) {
				insts[i+1] = arr[idx]
			}
			continue
		}
		prop, declared := schemaProps(cur)[seg].(map[string]any)
		if !declared {
			return nil, "", "", false, false
		}
		schemas[i+1] = prop
		if m, isMap := insts[i].(map[string]any); isMap {
			insts[i+1] = m[seg]
		}
	}

	n := len(segs)
	if kinds[n-1] {
		// Terminal array-index step: the resolved item itself is the
		// container — find its first missing required property.
		item := schemas[n]
		itemInst, _ := insts[n].(map[string]any)
		if missing := missingRequired(item, itemInst); len(missing) > 0 {
			return item, formatPath(segs), missing[0], false, true
		}
		return nil, "", "", false, false
	}

	// Terminal property step: the parent is the container.
	parent := schemas[n-1]
	name := segs[n-1]
	parentInst, _ := insts[n-1].(map[string]any)
	_, isPresent := parentInst[name]
	return parent, formatPath(segs[:n-1]), name, isPresent, true
}

// arrayIndex parses seg as a non-negative decimal array index.
func arrayIndex(seg string) (int, bool) {
	if seg == "" {
		return 0, false
	}
	for _, r := range seg {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(seg)
	if err != nil {
		return 0, false
	}
	return n, true
}

func schemaIsArray(schema map[string]any) bool {
	t, _ := schema["type"].(string)
	return t == "array"
}

func schemaIsObject(schema map[string]any) bool {
	t, _ := schema["type"].(string)
	return t == "object"
}

// requiredNames returns a schema's "required" list as strings, in schema
// order (unlike requiredList, it doesn't annotate types — callers that need
// the diff order call this; callers that render the coaching text call
// requiredList).
func requiredNames(schema map[string]any) []string {
	return asStringSlice(schema["required"])
}

// formatPath renders a split instance-location path for display: "[N]" for
// array-index segments, ".name" for object-property segments (no leading dot
// on the first segment). E.g. ["updates", "0"] -> "updates[0]";
// ["questions", "0", "header"] -> "questions[0].header".
func formatPath(segs []string) string {
	var b strings.Builder
	for i, seg := range segs {
		if _, isIdx := arrayIndex(seg); isIdx {
			b.WriteString("[")
			b.WriteString(seg)
			b.WriteString("]")
			continue
		}
		if i > 0 {
			b.WriteString(".")
		}
		b.WriteString(seg)
	}
	return b.String()
}

// lastKeywordSegment returns the last path segment of a JSON-Schema
// KeywordLocation ("oneOf/0/not" -> "not"). A bare keyword ("maxLength") is
// returned unchanged, so callers that only know the segment may pass it
// directly.
func lastKeywordSegment(location string) string {
	if i := strings.LastIndex(location, "/"); i >= 0 {
		return location[i+1:]
	}
	return location
}

// isBareOneOfLocation reports whether a failing KeywordLocation names the
// schema's top-level oneOf node itself rather than a nested combinator. The
// validator emits a JSON Pointer ("/oneOf"); a caller that only knows the
// segment passes "oneOf". Both trim to a single "oneOf" segment, while a
// nested failure's location ("/oneOf/0/oneOf", "/properties/x/oneOf") keeps
// interior slashes and is not bare.
func isBareOneOfLocation(location string) bool {
	return strings.Trim(location, "/") == "oneOf"
}

// branchCombinator returns the root-level branch combinator a failure's
// KeywordLocation lives under — "oneOf" for a cause inside a root-level oneOf
// (the combinator itself, an arm, or a `not` arm) and "not" for a root-level
// `not` — or "" when the failure is not branch-attributed.
//
// Attributing by the deepest cause's location prefix, not its leaf keyword, is
// what makes arm order irrelevant: with the positive oneOf arm ordered first
// the deepest cause is /oneOf/0/properties/sandbox/enum, whose bare "enum"
// would be read against the TOP-LEVEL property and render an allowed-values
// list that does not apply (issue #621). A `$ref` wrapper is skipped first:
// jsonschema reports a combinator behind a reference as /$ref/oneOf/.... A
// combinator nested under properties/items is not attributed (the explainer
// reads branch lists off the top-level schema; nested combinators are issue
// #624), and a root anyOf/allOf keeps its leaf keyword
// until its branches can be rendered.
func branchCombinator(keywordLocation string) string {
	segs := strings.Split(strings.Trim(keywordLocation, "/"), "/")
	i := 0
	for i < len(segs) && isRefSegment(segs[i]) {
		i++
	}
	if i >= len(segs) {
		return ""
	}
	switch segs[i] {
	case "oneOf", "not":
		return segs[i]
	}
	return ""
}

// oneOfConstraintMessage renders an honest explanation for a validation
// failure caused by a branch-combinator constraint (today: the delegate
// schema's oneOf sandbox/sandbox_net pairing rule) rather than by any single
// argument's type or value. It describes each branch's requirements in terms
// of the properties it constrains, so the model can see which argument
// combination to change or omit. Returns "" when params carries no usable
// branch list for the failing keyword or no branch can be described, letting
// the caller fall back to the generic message.
//
// A bare top-level combinator — the deepest cause is the top-level oneOf node
// itself, with no per-arm child causes — is the multiple-match shape: oneOf
// means exactly-one, so the arguments matched more than one branch. There is no
// failing branch to describe (every branch's requirements are already
// satisfied), so enumerating them would coach nothing; name the over-match and
// its recovery instead (issue #623). The location, not just the keyword, is
// what distinguishes this from a *nested* oneOf failure: the deepest-first walk
// makes a nested combinator's location "/oneOf/0/oneOf", which is not the
// top-level list branchList can describe, so it keeps the branch enumeration.
func oneOfConstraintMessage(toolName string, params map[string]any, keyword, keywordLocation string) string {
	branches, source := branchList(params, keyword)
	if len(branches) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: arguments violate a conditional rule on the combination of arguments (the schema's %s constraint), not any single argument's type or value.", toolName, source)
	if keyword == "oneOf" && isBareOneOfLocation(keywordLocation) {
		// The multiple-match shape: no failing branch to describe, so name the
		// over-match and its recovery instead of enumerating requirements the
		// arguments already satisfy. No Example either: minimalExample renders
		// only top-level required fields and ignores the oneOf arms, so it would
		// print an object matching zero branches, contradicting the coaching.
		fmt.Fprintf(&b, "\nThe arguments matched more than one branch; make them satisfy exactly one.")
		return b.String()
	}
	rendered := false
	for i, br := range branches {
		desc := branchRequirement(br)
		if desc == "" {
			continue
		}
		rendered = true
		fmt.Fprintf(&b, "\nBranch %d requires: %s.", i, desc)
	}
	if !rendered {
		return ""
	}
	// The example is the top-level required shape, which minimalExample
	// substitutes placeholders into. Emit it only when the schema's own
	// constraints and the example's shape agree that it is valid.
	showExample := !hasUnmodeledConstraint(params, keyword) &&
		!examplePropertyConstrained(params) && rootAcceptsObject(params)
	if keyword == "oneOf" {
		// A oneOf needs the example to match exactly one branch; a `not` arm of
		// a root oneOf keeps its template when that holds, so the shape does not
		// lose it.
		showExample = showExample && oneOfExampleMatchesExactlyOneBranch(params)
	} else {
		showExample = showExample && rootNotExampleIsValid(params)
	}
	if showExample {
		fmt.Fprintf(&b, "\nExample: %s", minimalExample(params))
	}
	return b.String()
}

// oneOfExampleMatchesExactlyOneBranch reports whether minimalExample(params) —
// the top-level required properties — satisfies exactly one branch of the root
// oneOf, making it a usable template rather than one that would match zero or
// several. It answers only when every branch it has to judge constrains nothing
// but presence, where the answer is exact; a branch that constrains values
// returns false rather than guess.
func oneOfExampleMatchesExactlyOneBranch(params map[string]any) bool {
	arms, _ := params["oneOf"].([]any)
	if len(arms) == 0 {
		return false
	}
	keys := map[string]bool{}
	for _, name := range requiredNames(params) {
		keys[name] = true
	}
	satisfiedCount := 0
	for _, raw := range arms {
		arm, _ := raw.(map[string]any)
		if arm == nil {
			return false
		}
		satisfied, known := armOutcome(arm, keys)
		if !known {
			// Whether the branch matches cannot be decided from presence, so
			// the example cannot be shown to match exactly one.
			return false
		}
		if satisfied {
			satisfiedCount++
		}
	}
	return satisfiedCount == 1
}

// armOutcome reports whether an object carrying exactly keys satisfies a
// branch, judged from presence alone. known is false when the branch or its
// `not` constrains a value, so the caller cannot decide and must not emit an
// example. A `not` rejects only when its whole inner schema matches, so a
// non-presence-only `not` (or one with no required names, which rejects
// unconditionally) is unknown here rather than rejecting.
func armOutcome(arm map[string]any, keys map[string]bool) (satisfied, known bool) {
	// An arm bearing a reference defers to the referenced schema, which is not
	// available here: whether its own sibling keywords apply depends on the
	// dialect ($ref siblings are ignored under draft-07 and applied under
	// 2020-12), so the branch cannot be judged from presence.
	for key := range arm {
		if isRefSegment(key) {
			return false, false
		}
	}
	// A missing required property fails the branch on presence alone, whatever
	// else the branch constrains.
	for _, name := range requiredNames(arm) {
		if !keys[name] {
			return false, true
		}
	}
	if forbidden := schemaMap(arm, "not"); forbidden != nil {
		if !isPresenceOnlySchema(forbidden) || len(requiredNames(forbidden)) == 0 {
			return false, false
		}
		all := true
		for _, name := range requiredNames(forbidden) {
			if !keys[name] {
				all = false
				break
			}
		}
		if all {
			return false, true
		}
	}
	if !isPresenceConstraint(arm) {
		return false, false
	}
	return true, true
}

// isPresenceConstraint reports whether a branch constrains only presence: a
// required list, and a `not` whose own schema is a non-empty presence-only
// required list. A `not` with no required names always rejects, and one that
// constrains a value only rejects on that value, so neither is a presence
// constraint the example can be judged against.
func isPresenceConstraint(arm map[string]any) bool {
	for key := range arm {
		switch key {
		case "required", "description", "title", "$comment":
		case "not":
			forbidden := schemaMap(arm, "not")
			if !isPresenceOnlySchema(forbidden) || len(requiredNames(forbidden)) == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// hasUnmodeledConstraint reports whether params carries a constraint other than
// the one being described that minimalExample does not model. The example is
// the top-level required shape, so object-level cardinality and name rules, a
// dependent requirement, a conditional, a value assertion on the object itself,
// a sibling combinator, a $ref, or a type union it cannot render all make it
// unsafe to suggest. Property keywords such as additionalProperties or items do
// not: the example carries only declared required properties
// .
func hasUnmodeledConstraint(params map[string]any, described string) bool {
	for key := range params {
		if key == described {
			continue
		}
		if unmodeledRootExampleKey(key) {
			return true
		}
	}
	return unresolvableType(params)
}

// rootAcceptsObject reports whether the top-level schema accepts the JSON object
// minimalExample renders: an absent type, "object", or a union that includes it.
// A scalar or null root would otherwise be handed an example of the wrong JSON
// type.
func rootAcceptsObject(params map[string]any) bool {
	raw, ok := params["type"]
	if !ok {
		return true
	}
	if t, isString := raw.(string); isString {
		return t == "object"
	}
	return slices.Contains(asStringSlice(raw), "object")
}

// unmodeledRootExampleKey reports whether a root keyword is an assertion the
// top-level example cannot be shown to satisfy: object-level cardinality, name
// and dependency rules, a conditional, a value assertion on the object itself,
// or a combinator. Every reference keyword counts, so $ref, $dynamicRef and
// $recursiveRef cannot drift apart.
func unmodeledRootExampleKey(key string) bool {
	if isRefSegment(key) {
		return true
	}
	switch key {
	case "minProperties", "maxProperties", "propertyNames",
		"dependentRequired", "dependentSchemas", "dependencies", "if",
		"patternProperties",
		"enum", "const", "oneOf", "anyOf", "allOf", "not":
		return true
	}
	return false
}

// unresolvableType reports whether a schema declares a type the placeholder
// cannot render — a union that is not a two-member nullable scalar
// .
func unresolvableType(schema map[string]any) bool {
	rawType, ok := schema["type"]
	if !ok {
		return false
	}
	if t, isString := rawType.(string); isString {
		switch t {
		case "string", "integer", "number", "boolean", "array", "object", "null":
			return false
		}
		// Anything else the placeholder cannot render would be suggested as
		// "...", asserting the wrong type.
		return true
	}
	return exampleSchemaType(rawType) == ""
}

// hasRefSegment reports whether any segment of the keyword location is a $ref —
// a reference on a property as well as at the root. The referenced schema owns
// the real constraint, so the top-level property cannot be read against it and
// any example built from it is unverified.
func hasRefSegment(keywordLocation string) bool {
	segs := strings.Split(strings.Trim(keywordLocation, "/"), "/")
	for i := 0; i < len(segs); i++ {
		// A properties/patternProperties keyword is followed by exactly one
		// property name, which may itself be spelled like a keyword; the segment
		// after that name is a keyword again.
		if segs[i] == "properties" || segs[i] == "patternProperties" {
			i++
			continue
		}
		if isRefSegment(segs[i]) {
			return true
		}
	}
	return false
}

// isRefSegment reports whether a keyword-location segment is a JSON-Schema
// reference keyword. $dynamicRef and $recursiveRef resolve like $ref, so a
// constraint reached through one is equally unreadable from the top-level
// schema.
func isRefSegment(seg string) bool {
	switch seg {
	case "$ref", "$dynamicRef", "$recursiveRef":
		return true
	}
	return false
}

// hasUnmodeledKey reports whether a schema node declares anything the example
// placeholder does not satisfy: a value constraint, a combinator or reference
// the renderer does not resolve, a nested assertion, or a type union it cannot
// render.
func hasUnmodeledKey(schema map[string]any) bool {
	for _, key := range []string{
		"enum", "const", "pattern", "format",
		"minLength", "maxLength", "minimum", "maximum",
		"exclusiveMinimum", "exclusiveMaximum", "multipleOf",
		"minItems", "maxItems", "uniqueItems", "items", "prefixItems",
		"contains", "minContains", "maxContains",
		"minProperties", "maxProperties", "dependentRequired", "dependentSchemas", "dependencies",
		"propertyNames", "additionalProperties", "patternProperties",
		"oneOf", "anyOf", "allOf", "not", "if", "$ref", "$dynamicRef", "$recursiveRef",
	} {
		if _, ok := schema[key]; ok {
			return true
		}
	}
	return unresolvableType(schema)
}

// exampleUnconstrained reports whether the value minimalExample renders for a
// property is free of anything the placeholder does not model. It mirrors the
// renderer's shape: a scalar property renders as its type placeholder, while an
// object property is expanded exactly one level, so a constraint on any of its
// required properties — or a nested object that would render as a bare "{}"
// despite requiring keys — makes the example unsafe.
func exampleUnconstrained(schema map[string]any) bool {
	if hasUnmodeledKey(schema) {
		return false
	}
	if len(requiredNames(schema)) == 0 || !schemaIsObject(schema) {
		return true
	}
	props := schemaProps(schema)
	for _, name := range requiredNames(schema) {
		prop := schemaMap(props, name)
		if prop == nil {
			return false
		}
		if hasUnmodeledKey(prop) {
			return false
		}
		if schemaIsObject(prop) && len(requiredNames(prop)) > 0 {
			return false
		}
	}
	return true
}

// examplePropertyConstrained reports whether any property the example will
// carry — the top-level required ones, expanded one level deep — declares a
// constraint the placeholder minimalExample substitutes ("...", "0", ...) would
// violate. Such a value is unmodeled, so appending the example would coach a
// retry that fails on a constraint the message never mentioned
// .
func examplePropertyConstrained(params map[string]any) bool {
	props := schemaProps(params)
	for _, name := range requiredNames(params) {
		prop := schemaMap(props, name)
		if prop == nil || !exampleUnconstrained(prop) {
			return true
		}
	}
	return false
}

// rootNotExampleIsValid reports whether minimalExample(params) satisfies the
// root `not` being described. The example carries the top-level required
// properties, and not/required rejects only when EVERY forbidden property is
// present, so it is invalid exactly when the required list contains all of
// them.
func rootNotExampleIsValid(params map[string]any) bool {
	forbidden := schemaMap(params, "not")
	if forbidden == nil {
		return true
	}
	names := requiredNames(forbidden)
	if len(names) == 0 {
		return true
	}
	req := requiredNames(params)
	for _, name := range names {
		if !slices.Contains(req, name) {
			return true
		}
	}
	return false
}

// branchList returns the schema's branch list for the failing combinator
// keyword and the keyword that names it in the message. A root-level "not"
// names a single forbidden-properties schema, not a branch list, so it is
// wrapped as one branch. There is deliberately no sibling fallback: borrowing,
// say, the top-level oneOf's branches for an unrelated "not" failure would
// describe a constraint the call satisfies.
func branchList(params map[string]any, keyword string) (branches []any, source string) {
	if keyword == "not" {
		if notSchema, ok := params["not"].(map[string]any); ok {
			// Wrap the not's own schema so branchRequirement describes it as the
			// forbidden properties it is, not as a requirement to send them.
			return []any{map[string]any{"not": notSchema}}, "not"
		}
		return nil, "not"
	}
	list, _ := params[keyword].([]any)
	return list, keyword
}

// branchCoversFailure reports whether oneOfConstraintMessage's branch prose
// explains this failure rather than the present-field path. The prose names
// only the combinator and, per arm, the properties that arm requires (with
// their enums), so it covers a branch-structural failure (present is false: the
// combinator itself, a `not` arm, an arm's own required list) and — on a
// supplied property — an arm enum branchRequirement will actually render. Any
// other constraint on a present property (its type, length, a nested
// requirement, a nested combinator, an enum the arm does not render) is a
// single-argument defect the prose never names, so it falls through to the
// present-field path.
func branchCoversFailure(params map[string]any, keyword, keywordLocation string, present bool) bool {
	if present {
		return keyword == "enum" && branchRendersArmEnum(params, keywordLocation)
	}
	// present is false both for a genuinely absent field and for a terminal item
	// or nested property the walk cannot resolve, so the location decides: only
	// branch structure (the combinator, an arm, an arm's required list or `not`)
	// is described by branch prose. A constraint nested beneath an arm's
	// properties/items is a single argument's or item's defect, and claiming the
	// branch covers it would tell the caller to supply what it already sent
	// while naming nothing about the failing item.
	return branchLocationIsStructural(keywordLocation)
}

// branchLocationIsStructural reports whether a keyword location names branch
// structure: the combinator itself, an arm, an arm's own required list, or an
// arm's `not` (plus the nested combinator node, whose outer branches the
// explainer enumerates). A location deeper than that belongs to a property or
// item, not to the branch.
func branchLocationIsStructural(keywordLocation string) bool {
	segs := strings.Split(strings.Trim(keywordLocation, "/"), "/")
	i := 0
	for i < len(segs) && isRefSegment(segs[i]) {
		i++
	}
	if i >= len(segs) {
		return false
	}
	switch segs[i] {
	case "not":
		return len(segs) == i+1
	case "oneOf", "anyOf", "allOf":
	default:
		return false
	}
	rest := segs[i+1:]
	switch {
	case len(rest) == 0:
		return true // the bare combinator (multiple-match shape)
	case len(rest) == 1:
		return true // an arm node
	case len(rest) == 2:
		// /<combinator>/<arm>/<keyword>
		switch rest[1] {
		case "required", "not", "oneOf", "anyOf", "allOf":
			return true
		}
	}
	return false
}

// branchRendersArmEnum reports whether keywordLocation names an arm's own
// property enum that branchRequirement renders. branchRequirement iterates the
// ARM's required slice, so it renders "<prop>" must be one of ... only for a
// direct property (/oneOf/<i>/properties/<prop>/enum) that the arm lists as
// required; an enum deeper in the arm, or on a property the arm does not
// require, is never rendered.
func branchRendersArmEnum(params map[string]any, keywordLocation string) bool {
	segs := strings.Split(strings.Trim(keywordLocation, "/"), "/")
	if len(segs) != 5 || segs[0] != "oneOf" || segs[2] != "properties" || segs[4] != "enum" {
		return false
	}
	idx, err := strconv.Atoi(segs[1])
	if err != nil || idx < 0 {
		return false
	}
	branches, _ := params["oneOf"].([]any)
	if idx >= len(branches) {
		return false
	}
	branch, _ := branches[idx].(map[string]any)
	// branchRequirement describes an arm whose `not` is presence-only, and
	// returns nothing at all when the `not` constrains a value — so such an
	// arm's enums are never rendered. Claiming coverage would drop the failing
	// property behind a bare generic message instead of letting the
	// present-field path name it.
	if forbidden := schemaMap(branch, "not"); forbidden != nil && notProhibition(forbidden) == "" {
		return false
	}
	// The location is a JSON Pointer, so a property name containing "/" or "~"
	// arrives escaped; unescape before comparing to the arm's required names
	// (RFC 6901: ~1 -> /, then ~0 -> ~).
	prop := strings.ReplaceAll(segs[3], "~1", "/")
	prop = strings.ReplaceAll(prop, "~0", "~")
	return slices.Contains(requiredNames(branch), prop)
}

// refWrappedKeywordLocation reports whether the failing cause sits behind a
// $ref. A combinator reached through a reference keeps its branches in the
// referenced schema, so they cannot be read off params: rendering the
// top-level list instead could describe an unrelated sibling combinator that
// merely shares the keyword.
func refWrappedKeywordLocation(keywordLocation string) bool {
	segs := strings.Split(strings.Trim(keywordLocation, "/"), "/")
	return len(segs) > 0 && isRefSegment(segs[0])
}

// notProhibition renders a negated schema as a "do not send ..." prohibition,
// or "" when that wording would be false. not/required rejects only when EVERY
// listed property is present, so the presence phrasing is accurate only for a
// schema that constrains nothing but presence — a negated schema that also
// constrains values ({"required": ["mode"], "properties": {"mode": {"enum":
// ["off"]}}} accepts mode="on", so "do not send mode" is wrong). Returning ""
// lets the caller fall back to the generic mismatch rather than coach a retry
// with the very value the example would include.
func notProhibition(forbidden map[string]any) string {
	if !isPresenceOnlySchema(forbidden) {
		return ""
	}
	names := requiredNames(forbidden)
	if len(names) == 0 {
		return ""
	}
	// not/required rejects only when EVERY listed property is present, so
	// forbidding each one individually would over-claim: for
	// {"not": {"required": ["a", "b"]}}, sending just "a" is valid.
	if len(names) > 1 {
		return "do not send all of " + joinQuoted(names) + " together"
	}
	return "do not send " + joinQuoted(names)
}

// isPresenceOnlySchema reports whether a schema's only validation keyword is
// "required". Annotations (description, title, $comment) do not constrain, so
// they are ignored.
func isPresenceOnlySchema(schema map[string]any) bool {
	for key := range schema {
		switch key {
		case "required", "description", "title", "$comment":
		default:
			return false
		}
	}
	return true
}

// branchRequirement renders one branch's requirement in prose: the
// properties it requires, and for a "not" branch, the properties it forbids
// being present.
func branchRequirement(branch any) string {
	schema, _ := branch.(map[string]any)
	if schema == nil {
		return ""
	}
	// A reference-bearing arm defers to the referenced schema, whose shape is
	// not available here; under a dialect that ignores $ref siblings its own
	// keywords do not apply, so neither requirement is described.
	for key := range schema {
		if isRefSegment(key) {
			return ""
		}
	}
	// An arm can carry a `not` and a required list at once. Describe both: a
	// prohibition alone would hide the missing (or ill-typed) required property
	// the caller has to fix, leaving nothing actionable in the message
	//.
	described := describeRequired(schema)
	if forbidden := schemaMap(schema, "not"); forbidden != nil {
		prohibition := notProhibition(forbidden)
		if prohibition == "" {
			// The negated schema constrains values, so the arm's failure is not
			// a presence matter and the required summary would name fields the
			// call already supplied.
			return ""
		}
		if described == "" {
			return prohibition
		}
		return described + ", " + prohibition
	}
	if described == "" {
		// A scalar arm (no required properties) such as {"const":"a"} or
		// {"enum":[...]} would otherwise describe nothing, leaving the generic
		// wrong-value message with no hint at the conditional constraint
		// (issue #624 review).
		return scalarRequirement(schema)
	}
	return described
}

// scalarRequirement renders a branch that names no required properties — a
// scalar arm such as {"const": "a"}, {"enum": [...]}, or {"type": "string"} —
// so its conditional constraint is explained. Empty when the arm carries none
// of these.
func scalarRequirement(schema map[string]any) string {
	var parts []string
	if c, present := schema["const"]; present {
		parts = append(parts, "must equal "+formatEnumValue(c))
	}
	if allowed := formatEnumValues(schema["enum"]); len(allowed) > 0 {
		parts = append(parts, "must be one of "+strings.Join(allowed, ", "))
	}
	if typ, ok := schema["type"].(string); ok && typ != "" {
		parts = append(parts, "must be a "+typ)
	}
	return strings.Join(parts, ", ")
}

// describeRequired renders a branch's own required properties, with an enum
// constraint on any of them. Empty when the branch requires nothing (or names
// no properties), so callers can treat "" as "nothing to say about presence".
func describeRequired(schema map[string]any) string {
	req := requiredNames(schema)
	if len(req) == 0 {
		return ""
	}
	parts := []string{"send all of " + joinQuoted(req)}
	props := schemaProps(schema)
	for _, name := range req {
		prop := schemaMap(props, name)
		if allowed := formatEnumValues(prop["enum"]); len(allowed) > 0 {
			parts = append(parts, fmt.Sprintf("%q must be one of %s", name, strings.Join(allowed, ", ")))
		}
	}
	return strings.Join(parts, ", ")
}

func joinQuoted(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, strconv.Quote(n))
	}
	return strings.Join(quoted, ", ")
}

// schemaMap returns the named key's value as a non-nil object schema, or nil.
func schemaMap(schema map[string]any, key string) map[string]any {
	m, _ := schema[key].(map[string]any)
	return m
}

// ExplainJSONError renders coaching for arguments still unparseable after
// RepairJSON. raw is the argument text that failed to parse; when it is
// non-empty, an excerpt of the failing region is included so the model can
// see what it actually sent.
func ExplainJSONError(toolName string, params map[string]any, parseErr error, raw []byte) string {
	excerpt := parseExcerpt(parseErr, raw)
	if excerpt != "" {
		excerpt += " "
	}
	return fmt.Sprintf("%s: arguments were not valid JSON (%s). %sSend a single JSON object, e.g. %s",
		toolName, parseErr, excerpt, minimalExample(params))
}

// ExplainTruncatedCall renders the prevalidation error for a tool call whose
// argument stream was cut off because the response hit the output-token
// limit. Distinct from ExplainJSONError on purpose: the JSON is incomplete,
// not malformed, and coaching the model about syntax sends it debugging a
// problem it doesn't have.
func ExplainTruncatedCall(toolName string) string {
	return toolName + ": tool call truncated — the response hit the output-token limit " +
		"before the arguments finished streaming. The call was NOT executed and the lost " +
		"content cannot be recovered. Re-issue the work in smaller pieces (e.g. write the " +
		"file in sections across multiple calls)."
}

// parseExcerpt renders the region of raw that failed parsing. A decoder syntax
// error shows a window around its one-based byte offset; an error that only
// carries EOF shows the tail, which is where the parser stopped. Returns ""
// when raw is empty.
func parseExcerpt(parseErr error, raw []byte) string {
	s := string(raw)
	if s == "" {
		return ""
	}
	const window = 120
	if isUnexpectedJSONEOF(parseErr) {
		tail := s
		if len(tail) > window {
			tail = "..." + tail[len(tail)-window:]
		}
		return fmt.Sprintf("Your input ended with: %q.", tail)
	}
	var se *json.SyntaxError
	if errors.As(parseErr, &se) && se.Offset > 0 {
		position := se.Offset - 1 // SyntaxError.Offset is one-based.
		if se.Error() == "unexpected end of JSON input" {
			// The decoder stopped after the final byte; there is no offending
			// byte to place the marker before, so keep the raw suffix contiguous.
			position = int64(len(s))
		}
		if position > int64(len(s)) {
			position = int64(len(s))
		}
		off := int(position)
		start := max(off-window/2, 0)
		end := min(off+window/2, len(s))
		prefix, suffix := "", ""
		if start > 0 {
			prefix = "..."
		}
		if end < len(s) {
			suffix = "..."
		}
		return fmt.Sprintf("Failing input near byte %d: %q.",
			se.Offset, prefix+s[start:off]+">>>"+s[off:end]+suffix)
	}
	tail := s
	if len(tail) > window {
		tail = "..." + tail[len(tail)-window:]
	}
	return fmt.Sprintf("Your input ended with: %q.", tail)
}

func isUnexpectedJSONEOF(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return false
}

func requiredList(params map[string]any) []string {
	props := schemaProps(params)
	var out []string
	for _, r := range asStringSlice(params["required"]) {
		typ := ""
		if p, ok := props[r].(map[string]any); ok {
			typ, _ = p["type"].(string)
		}
		if typ != "" {
			out = append(out, fmt.Sprintf("%s (%s)", r, typ))
		} else {
			out = append(out, r)
		}
	}
	return out
}

func minimalExample(params map[string]any) string {
	return exampleObject(params, true)
}

// exampleObject renders an object schema's required properties as
// "name: placeholder" pairs, sorted alphabetically. expandNested also
// expands an object property that itself declares required keys one level
// deep (issue #627: communicate's output example must show the accepted
// envelope, not a bare {}).
//
// The required list is copied before sorting — a schema's "required" value
// may be a []string held by reference (DefCommunicateNamed builds it that
// way, and asStringSlice returns such a slice as-is), so sorting in place
// would corrupt the shared schema for every later message and every
// registry clone that shares it.
func exampleObject(schema map[string]any, expandNested bool) string {
	props := schemaProps(schema)
	req := append([]string(nil), asStringSlice(schema["required"])...)
	sort.Strings(req)
	parts := make([]string, 0, len(req))
	for _, name := range req {
		propSchema := props[name]
		typ := ""
		if p, ok := propSchema.(map[string]any); ok {
			typ = exampleSchemaType(p["type"])
		}
		placeholder := examplePlaceholder(typ)
		if expandNested {
			placeholder = exampleValue(propSchema, typ)
		}
		parts = append(parts, fmt.Sprintf("%q: %s", name, placeholder))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// exampleValue renders a property's placeholder: examplePlaceholder for
// scalars, or (for an object property that declares its own required list)
// the nested shape via exampleObject, one level deep.
func exampleValue(prop any, typ string) string {
	placeholder := examplePlaceholder(typ)
	if typ != "object" {
		return placeholder
	}
	p, ok := prop.(map[string]any)
	if !ok {
		return placeholder
	}
	if len(asStringSlice(p["required"])) == 0 {
		return placeholder
	}
	return exampleObject(p, false)
}

func examplePlaceholder(typ string) string {
	switch typ {
	case "integer", "number":
		return "0"
	case "boolean":
		return "false"
	case "null":
		return "null"
	case "array":
		return "[]"
	case "object":
		return "{}"
	default:
		return `"..."`
	}
}

// asStringSlice renders a schema's "required" list as a []string, handling
// both the []string a hand-built schema may carry and the []any a
// JSON-decoded one unmarshals to. Non-string elements are dropped: a
// required list's entries are property names, always strings, and a schema
// violating that fails compilation before ExplainSchemaError can run. An
// "enum" list renders through formatEnumValues instead, which must keep
// values of every JSON type.
func asStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// formatEnumValues renders a schema's "enum" list as JSON literals, one
// string per value, ready to join with ", " into a message a model reads as
// JSON syntax: a string quoted ("open"), any other type as its own literal
// (1, true, null, [1,2]). Quoting a non-string would visually assert the
// wrong JSON type and coach a retry that fails validation the same way, so
// both enum call sites (constraintMessage and branchRequirement) render
// through here rather than quoting on their own. v can carry the list as
// any slice or array type — []any (a JSON-decoded schema), []string (many
// hand-built ones), or a genuinely typed slice like []int or []bool, which
// schema compilation accepts and preserves through cloning just the same —
// so this walks by reflection rather than naming each shape. A non-slice,
// non-array v, including nil, returns nil.
func formatEnumValues(v any) []string {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil
	}
	out := make([]string, rv.Len())
	for i := range out {
		out[i] = formatEnumValue(rv.Index(i).Interface())
	}
	return out
}

// formatEnumValue renders one enum value as its own JSON literal, making
// json.Marshal the single rule for what a value looks like in JSON —
// including its number formatting, which turns to scientific notation
// outside roughly the 1e-6..1e21 magnitude range (1e21 -> "1e+21"). Every
// value here comes from a compiled schema's enum list and is JSON
// marshalable, so the fmt.Sprint fallback only guards an error that cannot
// happen in practice.
func formatEnumValue(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// pathStep is one instance-path segment with the schema step that produced it:
// an object key ("properties"/"additionalProperties"/"propertyNames") or an
// array index ("items"). The kind cannot be recovered from the segment text —
// a property may legally be named "0" — so traversal records it for both the
// display path and the Example (issue #624 review).
type pathStep struct {
	name  string
	index bool
}

// branchConstraintMessage renders the branch explanation for a oneOf nested
// inside the container whose schema is holder, reached at propertyPath (the
// kinded instance path enclosingOneOf resolved). The intro names that container
// and the Example renders a complete document with that container's accepted
// value. root and rootBranch are the tool schema and the enclosing root oneOf
// branch (if any) the Example must also satisfy. It describes each branch's
// requirements in terms of the properties it constrains, so the model can see
// which argument combination to change or omit. Returns "" when holder carries
// no usable branch list or no branch can be described.
//
// overMatch (the combinator's own node is the deepest cause, so oneOf matched
// more than one branch) has no failing branch to describe; name the over-match
// and its recovery instead, with no Example, which would print an object
// matching zero branches. Root-level combinator handling lives in
// oneOfConstraintMessage, which issue #621's prefix attribution owns.
func branchConstraintMessage(toolName string, root, holder map[string]any, rootBranch map[string]any, propertyPath []pathStep, overMatch bool, leafConstraint string, exampleUnsafe bool) string {
	branches, source := branchList(holder, "oneOf")
	if len(branches) == 0 {
		return ""
	}
	var b strings.Builder
	if overMatch {
		// The multiple-match shape: no failing branch to describe, so name the
		// over-match and its recovery instead of enumerating requirements the
		// arguments already satisfy. No Example: minimalExample renders only
		// top-level required fields and ignores the oneOf arms, so it would print
		// an object matching zero branches, contradicting the coaching.
		fmt.Fprintf(&b, "%s: argument %q matched more than one branch of the schema's %s constraint; make it satisfy exactly one.", toolName, stepPathDisplay(propertyPath), source)
		return b.String()
	}
	subject := "the combination of its properties"
	if !objectShaped(holder) {
		// A scalar holder (e.g. type string with const/enum arms) is not a
		// combination of properties (issue #624 review).
		subject = "its value"
	}
	if leafConstraint != "" {
		// The failure is a branch-internal leaf the prose does not describe:
		// name it rather than claim no single argument's type or value is at
		// fault.
		fmt.Fprintf(&b, "%s: argument %q violates a conditional rule on %s (the schema's %s constraint); the failing branch constraint is %q.", toolName, stepPathDisplay(propertyPath), subject, source, leafConstraint)
	} else {
		fmt.Fprintf(&b, "%s: argument %q violates a conditional rule on %s (the schema's %s constraint), not the argument's own type or value.", toolName, stepPathDisplay(propertyPath), subject, source)
	}
	rendered := false
	for i, br := range branches {
		desc := branchRequirement(br)
		if desc == "" {
			continue
		}
		rendered = true
		fmt.Fprintf(&b, "\nBranch %d requires: %s.", i, desc)
	}
	if !rendered {
		return ""
	}
	example := ""
	if !exampleUnsafe {
		// A walk that passed through anyOf/allOf/not left constraints the scoped
		// builder does not model, so any Example it produced could violate the
		// enclosing applicator; omit it rather than coach an invalid retry
		// (issue #624 review).
		example = scopedBranchExample(root, rootBranch, propertyPath, branchValueExample(holder, branches))
	}
	if example != "" {
		fmt.Fprintf(&b, "\nExample: %s", example)
	}
	return b.String()
}

// nestedOneOfMessage explains a oneOf nested inside a property's (or array
// item's) schema (issue #624). The failing keyword's location
// ("properties/opts/oneOf/0/required") is walked to the schema node holding
// that oneOf, and its branches are rendered against that node's own instance
// path (derived in lockstep from the keyword and instance locations), so a
// branch failure on a deeper property is not misattributed to that property.
// Returns "" when the location descends through no nested oneOf — a bare
// keyword, a direct constraint, or a root-level combinator (handled on the
// missing-field path) — letting the generic message stand.
func nestedOneOfMessage(toolName string, params map[string]any, keywordLocation, instanceLocation string) string {
	holderPath, holder, rootBranch, overMatch, applicatorUnsafe, ok := enclosingOneOf(params, keywordLocation, instanceLocation)
	if !ok {
		return ""
	}
	// A branch-internal leaf constraint (a property's type, minimum, pattern,
	// ...) is a single-argument defect the branch prose never names; pass it so
	// the message names the actual constraint instead of claiming the failure is
	// no argument's type or value (issue #624 review).
	leafConstraint := ""
	if leaf := lastKeywordSegment(keywordLocation); !branchProseCovers(leaf) {
		leafConstraint = leaf
	}
	return branchConstraintMessage(toolName, params, holder, rootBranch, holderPath, overMatch, leafConstraint, applicatorUnsafe)
}

// branchProseCovers reports whether the branch requirements
// branchRequirement renders name the failing keyword: a combinator node
// itself, an arm's required list, an enum or const an arm narrows, or a `not`
// prohibition. A leaf constraint outside this set (type, minimum, pattern, ...)
// is not described, so the combinator message must not stand in for it.
func branchProseCovers(leaf string) bool {
	switch leaf {
	case "oneOf", "required", "not":
		return true
	}
	// "enum"/"const" are left out deliberately: the prose renders an enum only
	// for a required property and a const only for a required-less (scalar) arm,
	// so a branch such as {required:["a"], properties:{a:{const:7}}} would
	// otherwise deny that any value is at fault and never say a must equal 7
	// (issue #624 review). The leaf keyword is named instead.
	return false
}

// objectShaped reports whether a schema describes an object's properties
// (declares properties, a required list, or type object), as opposed to a
// scalar whose conditional rule is about its value.
func objectShaped(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	if _, present := schema["properties"]; present {
		return true
	}
	if len(requiredNames(schema)) > 0 {
		return true
	}
	typ, _ := schema["type"].(string)
	return typ == "object"
}

// enclosingOneOf walks a failing keyword's location path against params and
// returns the oneOf the path descends through that is scoped to a non-root
// instance path (e.g. "properties/opts/oneOf/0/required" -> opts's schema),
// together with that combinator's own kinded instance path. The schema steps
// (properties/<name>, items, additionalProperties, propertyNames) consume one
// instance segment and record its kind, so the instance path is the prefix of
// instanceLocation that reaches the holder — not the deeper path of the leaf
// keyword, which may sit on a property inside a branch.
//
// A root combinator is traversed (its branch index names a subschema) but never
// selected as the holder, so a property-level oneOf inside a root branch
// ("/oneOf/0/properties/opts/oneOf") is still found. The DEEPEST combinator
// scoped to a non-root path is the holder: a oneOf nested inside an arm's oneOf
// is the combinator that failed, so it replaces an outer candidate rather than
// leaving the message to enumerate arms the call already satisfies. Wrapper
// steps (anyOf/allOf arm indexes, not, prefixItems) are traversed without
// selecting a holder; patternProperties and $ref cannot be resolved here and
// decline.
//
// ok is false when the path names no such combinator (a bare keyword, a direct
// constraint, a root-level combinator, or a JSON Pointer that doesn't resolve),
// so an unrelated combinator is never blamed. Both locations use JSON Pointer
// escaping (~1, ~0) and are accepted with or without a leading slash.
func enclosingOneOf(params map[string]any, keywordLocation, instanceLocation string) (holderPath []pathStep, holder, rootBranch map[string]any, overMatch, applicatorUnsafe, ok bool) {
	ksegs := splitPointer(keywordLocation)
	isegs := splitPointer(instanceLocation)
	cur := params
	var path []pathStep
	consumed := 0
	holderOneOf, lastOneOf := -1, -1
	// applicatorUnsafe records that the walk passed through anyOf/allOf/not,
	// whose surrounding constraints the scoped Example builder does not model:
	// it can produce a value the enclosing applicator rejects, so the caller
	// must omit the Example (issue #624 review).
	applicatorUnsafe = false
	for i := 0; i < len(ksegs); i++ {
		switch ksegs[i] {
		case "properties":
			if i+1 >= len(ksegs) || consumed >= len(isegs) {
				return nil, nil, nil, false, false, false
			}
			child, isSchema := schemaProps(cur)[ksegs[i+1]].(map[string]any)
			if !isSchema {
				return nil, nil, nil, false, false, false
			}
			cur = child
			path = append(path, pathStep{name: ksegs[i+1]})
			consumed++
			i++
		case "items":
			if consumed >= len(isegs) {
				return nil, nil, nil, false, false, false
			}
			child, isSchema := cur["items"].(map[string]any)
			if !isSchema {
				return nil, nil, nil, false, false, false
			}
			cur = child
			path = append(path, pathStep{name: isegs[consumed], index: true})
			consumed++
		case "propertyNames":
			// propertyNames constrains the property NAME, not the value a
			// value-scoped Example would place there, so a combinator nested
			// under it cannot be expressed: decline and let the caller fall back
			// to the generic message (issue #624 review).
			return nil, nil, nil, false, false, false
		case "additionalProperties":
			if consumed >= len(isegs) {
				return nil, nil, nil, false, false, false
			}
			child, isSchema := cur[ksegs[i]].(map[string]any)
			if !isSchema {
				return nil, nil, nil, false, false, false
			}
			cur = child
			path = append(path, pathStep{name: isegs[consumed]})
			consumed++
		case "oneOf":
			// A combinator's arms apply to the same instance as the combinator,
			// so its branch index consumes no instance segment. The DEEPEST
			// combinator scoped to a non-root path is the holder: a oneOf nested
			// inside an arm's oneOf is the combinator that actually failed, and
			// enumerating the outer arms would describe a node the call already
			// satisfies (issue #624 review).
			lastOneOf = i
			if consumed > 0 {
				holder = cur
				holderPath = slices.Clone(path)
				holderOneOf = i
			}
			if i+1 < len(ksegs) {
				if branches, isList := cur["oneOf"].([]any); isList {
					if idx, isIdx := arrayIndex(ksegs[i+1]); isIdx && idx >= 0 && idx < len(branches) {
						if child, isSchema := branches[idx].(map[string]any); isSchema {
							if consumed == 0 && rootBranch == nil {
								// The outermost root oneOf branch also constrains
								// the root instance, so the Example must satisfy
								// it too (issue #624 review).
								rootBranch = child
							}
							cur = child
							i++
							continue
						}
					}
				}
			}
		case "anyOf", "allOf":
			// A wrapper's subschema applies to the same instance, so descend its
			// named arm without consuming an instance segment and without
			// selecting a holder, letting a oneOf inside it be found (issue #624
			// review).
			if i+1 < len(ksegs) {
				if arms, isList := cur[ksegs[i]].([]any); isList {
					if idx, isIdx := arrayIndex(ksegs[i+1]); isIdx && idx >= 0 && idx < len(arms) {
						if child, isSchema := arms[idx].(map[string]any); isSchema {
							cur = child
							applicatorUnsafe = true
							i++
							continue
						}
					}
				}
			}
			return nil, nil, nil, false, false, false
		case "not":
			child, isSchema := cur["not"].(map[string]any)
			if !isSchema {
				return nil, nil, nil, false, false, false
			}
			cur = child
			applicatorUnsafe = true
		case "prefixItems":
			if i+1 >= len(ksegs) || consumed >= len(isegs) {
				return nil, nil, nil, false, false, false
			}
			items, isList := cur["prefixItems"].([]any)
			if !isList {
				return nil, nil, nil, false, false, false
			}
			idx, isIdx := arrayIndex(ksegs[i+1])
			if !isIdx || idx < 0 || idx >= len(items) {
				return nil, nil, nil, false, false, false
			}
			child, isSchema := items[idx].(map[string]any)
			if !isSchema {
				return nil, nil, nil, false, false, false
			}
			cur = child
			path = append(path, pathStep{name: isegs[consumed], index: true})
			consumed++
			i++
		case "patternProperties":
			// A pattern-keyed subschema cannot be matched to a concrete property
			// name here: decline rather than guess.
			return nil, nil, nil, false, false, false
		case "$ref", "$dynamicRef", "$recursiveRef":
			// The referenced schema is not available in params: decline.
			return nil, nil, nil, false, false, false
		default:
			if holder == nil {
				return nil, nil, nil, false, false, false
			}
			return holderPath, holder, rootBranch, holderOneOf == lastOneOf && lastKeywordSegment(keywordLocation) == "oneOf", applicatorUnsafe, true
		}
	}
	if holder == nil {
		return nil, nil, nil, false, false, false
	}
	return holderPath, holder, rootBranch, holderOneOf == lastOneOf && lastKeywordSegment(keywordLocation) == "oneOf", applicatorUnsafe, true
}

// branchValueExample renders a minimal value the holder and exactly one branch
// accept — the value a caller could put at the combinator's instance path —
// e.g. {"x": "..."}. The object carries the holder's own required properties
// plus the branch's, each value honoring the branch's and holder's
// const/enum/type/numeric constraints, because a branch-ignoring placeholder
// would produce an "accepted shape" Example that fails validation on retry
// (issue #624 review). It returns "" when no branch names required properties,
// a value cannot be built, or the object would satisfy zero or more than one
// branch, so the caller omits the Example rather than coach an invalid retry.
func branchValueExample(holder map[string]any, branches []any) string {
	holderRequired := requiredNames(holder)
	for _, br := range branches {
		schema, _ := br.(map[string]any)
		if schema == nil || schemaMap(schema, "not") != nil {
			continue
		}
		req := requiredNames(schema)
		if len(req) == 0 {
			continue
		}
		names := mergeNames(holderRequired, req)
		parts := make([]string, 0, len(names))
		present := make(map[string]bool, len(names))
		buildable := true
		for _, name := range names {
			value, ok := exampleValueFor(holder, schema, name)
			if !ok {
				buildable = false
				break
			}
			parts = append(parts, fmt.Sprintf("%q: %s", name, value))
			present[name] = true
		}
		if !buildable {
			continue
		}
		if matchingBranchCount(branches, present) != 1 {
			continue
		}
		// Object-level constraints (minProperties/maxProperties, a const/enum on
		// the object itself, ...) can make a required-only object invalid; omit
		// rather than assert it (issue #624 review).
		if !objectExampleAllowed(holder, schema, names) {
			continue
		}
		// The object must be one both schemas' declared types accept: a holder
		// or branch typed as an array cannot be satisfied by an object
		// (issue #624 review).
		if !candidateMatchesType(map[string]any{}, holder["type"]) || !candidateMatchesType(map[string]any{}, schema["type"]) {
			continue
		}
		sort.Strings(parts)
		return "{" + strings.Join(parts, ", ") + "}"
	}
	return ""
}

// objectExampleAllowed reports whether an example object carrying exactly names
// satisfies the object-level constraints the holder and selected branch impose.
// Constraints this file cannot evaluate make it decline, so the caller omits
// the Example.
func objectExampleAllowed(holder, branch map[string]any, names []string) bool {
	return exampleLevelAllowed(holder, []map[string]any{branch}, names)
}

// exampleLevelAllowed reports whether an example object carrying exactly names
// satisfies the object-level constraints of the primary schema — whose own
// oneOf is exempt, since it is the combinator being explained — and of every
// extra schema that also constrains this level (a selected root oneOf branch,
// say). Constraints this file cannot evaluate decline the level.
func exampleLevelAllowed(primary map[string]any, extras []map[string]any, names []string) bool {
	if !objectLevelEvaluable(primary, true) {
		return false
	}
	for _, extra := range extras {
		if !objectLevelEvaluable(extra, false) {
			return false
		}
	}
	all := append([]map[string]any{primary}, extras...)
	for _, schema := range all {
		if !objectConstraintSetSatisfied(schema, names) {
			return false
		}
	}
	return true
}

// objectConstraintSetSatisfied reports whether an object carrying names meets
// schema's property-count and additionalProperties constraints. A name is
// declared only by this schema: a sibling schema declaring it does not satisfy
// this schema's additionalProperties refusal.
func objectConstraintSetSatisfied(schema map[string]any, names []string) bool {
	if schema == nil {
		return true
	}
	count := float64(len(names))
	if minimum, ok := schemaFloat(schema["minProperties"]); ok && count < minimum {
		return false
	}
	if maximum, ok := schemaFloat(schema["maxProperties"]); ok && count > maximum {
		return false
	}
	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		props := schemaProps(schema)
		for _, name := range names {
			if _, declared := props[name]; !declared {
				return false
			}
		}
	}
	return true
}

// unevaluableObjectKeys are object-level keywords whose effect on validity this
// file cannot decide from a property-name list alone, so an Example against a
// schema carrying one is omitted rather than asserted. "oneOf" is handled
// separately (the holder's own oneOf is expected); "additionalProperties" is
// handled by objectExampleAllowed's declared-property check.
var unevaluableObjectKeys = append([]string{
	"allOf", "anyOf", "not", "oneOf",
	"dependentRequired", "dependencies", "dependentSchemas",
	"if", "then", "else",
	"patternProperties", "propertyNames", "unevaluatedProperties",
	"const", "enum",
}, refKeywords...)

// objectLevelEvaluable reports whether this file can decide an object's
// validity against schema from its property names. isHolder exempts the
// schema's own oneOf (it is the combinator being explained); a branch's nested
// oneOf is unevaluable. A schema-valued additionalProperties is unevaluable
// too.
func objectLevelEvaluable(schema map[string]any, isHolder bool) bool {
	if schema == nil {
		return true
	}
	for _, key := range unevaluableObjectKeys {
		if _, present := schema[key]; present {
			if key == "oneOf" && isHolder {
				continue
			}
			return false
		}
	}
	if additional, present := schema["additionalProperties"]; present {
		if _, isBool := additional.(bool); !isBool {
			return false
		}
	}
	return true
}

// mergeNames returns the union of two property-name lists, order-preserving and
// de-duplicated.
func mergeNames(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]bool, len(a)+len(b))
	for _, lists := range [][]string{a, b} {
		for _, name := range lists {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// matchingBranchCount counts the branches whose required list the built object
// satisfies. oneOf means exactly-one, so an Example is only usable when this
// count is 1; a required-list-only approximation is what this file can decide
// without a full validator, and anything it cannot decide is declined.
func matchingBranchCount(branches []any, present map[string]bool) int {
	count := 0
	for _, br := range branches {
		switch schema := br.(type) {
		case map[string]any:
			matched := true
			for _, name := range requiredNames(schema) {
				if !present[name] {
					matched = false
					break
				}
			}
			if matched {
				count++
			}
		case bool:
			// A boolean true branch matches any instance; false matches none.
			if schema {
				count++
			}
		}
	}
	return count
}

// exampleValueFor chooses a value for one required property that both the
// branch and the holder accept, or declines (ok=false) so the caller omits the
// Example rather than coach a retry that fails validation (issue #624 review).
// A declared const/enum is used only if the candidate also satisfies the other
// constraints both schemas impose on the property (finding: an enum candidate
// may violate a sibling minimum/multipleOf); with no const/enum it renders a
// numeric bound or a type-appropriate placeholder.
func exampleValueFor(holder, branch map[string]any, name string) (string, bool) {
	bp, _ := schemaProps(branch)[name].(map[string]any)
	hp, _ := schemaProps(holder)[name].(map[string]any)
	// A property schema written as the boolean false rejects every value, so no
	// Example can satisfy it; a boolean true is unconstrained and falls through
	// as the nil schema does (issue #624 review).
	if isFalsePropertySchema(branch, name) || isFalsePropertySchema(holder, name) {
		return "", false
	}
	// An explicitly empty enum admits no value: any Example for this property
	// is wrong by construction, so decline before the candidate/placeholder
	// paths can emit one (issue #624 review). This is per branch, so an empty
	// enum on a sibling arm does not suppress a satisfiable branch.
	if hasEmptyEnum(bp) || hasEmptyEnum(hp) {
		return "", false
	}
	candidates := append(declaredCandidates(bp), declaredCandidates(hp)...)
	if len(candidates) > 0 {
		// A declared value set is exhaustive: fall through to a generated value
		// would violate the very enum/const it must satisfy.
		for _, candidate := range candidates {
			// Validate and emit the SAME value: canonicalize once, so a typed
			// container whose json.Marshal differs from its reflected shape
			// ([]byte -> base64, typed nil map -> null) cannot pass validation as
			// one JSON type and render as another (issue #624 review).
			canonical := canonicalJSONValue(candidate)
			if !sameJSONEncoding(candidate, canonical) {
				// The canonical form denotes a different JSON value (a []byte is
				// base64, a typed nil map is null): decline rather than rewrite
				// the declared constant.
				continue
			}
			if candidateSatisfies(canonical, bp, hp) {
				return formatEnumValue(canonical), true
			}
		}
		return "", false
	}
	typ, typDeclared := schemaTypeOf(bp, hp)
	if typDeclared && typ == "" {
		// A declared type shape this file cannot render faithfully (an
		// ambiguous union, say): omit rather than emit a placeholder of the
		// wrong type (issue #624 review).
		return "", false
	}
	if typ == "integer" || typ == "number" {
		// If either schema requires an integer, generate one: an integer value
		// is also a valid number, so it satisfies both.
		if declaresInteger(bp) || declaresInteger(hp) {
			typ = "integer"
		}
		value, ok := numericExample(typ, bp, hp)
		if !ok {
			return "", false
		}
		// The generated value must be one both schemas' declared types accept —
		// the branch-preferred type alone can violate the holder (issue #624
		// review). Omit when the types conflict.
		if !candidateMatchesType(value, bp["type"]) || !candidateMatchesType(value, hp["type"]) {
			return "", false
		}
		return strconv.FormatFloat(value, 'f', -1, 64), true
	}
	// A generic placeholder cannot guarantee constraints such as a pattern, a
	// format, a bound, or a nested shape; omit the Example rather than coach a
	// value that fails validation on retry.
	if hasPlaceholderRisk(bp) || hasPlaceholderRisk(hp) {
		return "", false
	}
	// An object placeholder is only valid when the object's own required list is
	// empty and its shape is evaluable; otherwise omit rather than emit {}
	// against a schema that demands nested properties (issue #624 review).
	if typ == "object" &&
		(len(requiredNames(bp)) > 0 || len(requiredNames(hp)) > 0 ||
			!objectLevelEvaluable(bp, false) || !objectLevelEvaluable(hp, false)) {
		return "", false
	}
	var value any
	switch typ {
	case "boolean":
		value = false
	case "null":
		value = nil
	case "array":
		value = []any{}
	case "object":
		value = map[string]any{}
	default:
		// No declared type: a string satisfies an unconstrained property.
		value = "..."
	}
	if !candidateMatchesType(value, bp["type"]) || !candidateMatchesType(value, hp["type"]) {
		return "", false
	}
	return formatEnumValue(value), true
}

// declaresInteger reports whether a schema's declared type names "integer"
// (directly or as the non-null member of a nullable union).
func declaresInteger(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	declared, present := schema["type"]
	if !present {
		return false
	}
	return declaredTypeName(declared) == "integer"
}

// schemaTypeOf returns the property's declared type, preferring the branch's
// schema (the narrowing one) and falling back to the holder's, plus whether a
// type was declared at all. A nullable union ("integer" or "null") yields its
// non-null member; a shape with no single non-null member yields "" with
// declared=true, so the caller can omit the Example rather than guess a type.
func schemaTypeOf(branch, holder map[string]any) (string, bool) {
	for _, schema := range []map[string]any{branch, holder} {
		if schema == nil {
			continue
		}
		if declared, present := schema["type"]; present {
			return declaredTypeName(declared), true
		}
	}
	return "", false
}

// declaredTypeName resolves a "type" declaration to one renderable type name: a
// scalar name as-is, or the non-null member of a nullable union. It returns ""
// for any shape with no single non-null member (a bare list without "null", a
// union of two concrete types), which the caller treats as unsupported.
func declaredTypeName(declared any) string {
	if name, ok := declared.(string); ok {
		return name
	}
	return nullableUnionNonNullType(declared)
}

// declaredCandidates returns a schema's const value or its enum members, in
// declared order, or nil when it declares neither. Values keep their Go types
// (the []any a JSON-decoded schema carries, or a hand-built slice).
func declaredCandidates(schema map[string]any) []any {
	if schema == nil {
		return nil
	}
	if c, present := schema["const"]; present {
		return []any{c}
	}
	rv := reflect.ValueOf(schema["enum"])
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out
}

// hasEmptyEnum reports whether schema declares an "enum" key with no members.
// That is distinct from declaring no enum at all: an empty enum admits no
// value, so no Example can satisfy it (issue #624 review).
func hasEmptyEnum(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	enum, present := schema["enum"]
	if !present {
		return false
	}
	return len(declaredCandidates(map[string]any{"enum": enum})) == 0
}

// candidateSatisfies reports whether a const/enum candidate satisfies every
// constraint both schemas impose on the property: the other schema's own
// enum/const and declared type, numeric bounds/multipleOf, and string length.
// A schema carrying a constraint this file cannot evaluate (pattern, a nested
// shape, ...) makes the candidate decline, so the caller omits the Example
// rather than assert a value the branch or holder rejects (issue #624 review).
func candidateSatisfies(candidate any, schemas ...map[string]any) bool {
	candidate = canonicalJSONValue(candidate)
	if slices.ContainsFunc(schemas, hasUnhandledCandidateRisk) {
		return false
	}
	for _, schema := range schemas {
		if !candidateMatchesSchema(candidate, schema) {
			return false
		}
	}
	return true
}

// candidateMatchesSchema checks one candidate against a single property schema:
// its const, its enum membership, its declared type, and any numeric/string
// bounds it declares.
func candidateMatchesSchema(candidate any, schema map[string]any) bool {
	if schema == nil {
		return true
	}
	candidate = canonicalJSONValue(candidate)
	if declared, present := schema["const"]; present && !jsonEqual(candidate, declared) {
		return false
	}
	if enum, present := schema["enum"]; present && !listHasValue(enum, candidate) {
		return false
	}
	if !candidateMatchesType(candidate, schema["type"]) {
		return false
	}
	if !integerCandidateExact(candidate) && hasNumericConstraint(schema) {
		// A large integer loses precision in float64, so a bound or multipleOf
		// could be validated against a different value than the one rendered
		// (issue #624 review): decline rather than assert an off-by-one Example.
		return false
	}
	switch v := candidate.(type) {
	case map[string]any:
		return objectCandidateSatisfies(v, schema)
	case []any:
		return arrayCandidateSatisfies(v, schema)
	}
	if v, ok := numericValue(candidate); ok {
		return numericValueSatisfies(v, schema)
	}
	if s, ok := candidate.(string); ok {
		n := float64(utf8.RuneCountInString(s))
		if limit, ok := schemaFloat(schema["minLength"]); ok && n < limit {
			return false
		}
		if limit, ok := schemaFloat(schema["maxLength"]); ok && n > limit {
			return false
		}
	}
	return true
}

// canonicalJSONValue converts a candidate to the shapes the validator sees
// after json.Unmarshal — map[string]any and []any — so a typed const such as
// map[string]string is validated recursively rather than falling through the
// map/slice switch unrecognized (issue #624 review). Values already canonical
// (and non-container scalars) are returned unchanged.
func canonicalJSONValue(v any) any {
	switch t := v.(type) {
	case nil, bool, string, float64:
		return v
	case map[string]any:
		if t == nil {
			// A typed nil container marshals to null, not {} (issue #624 review).
			return nil
		}
		// Recurse: a typed container nested inside an otherwise canonical map
		// ({"data": []byte{...}}) must be canonicalized too, or validation sees
		// an array while rendering emits the base64 string (issue #624 review).
		out := make(map[string]any, len(t))
		for key, value := range t {
			out[key] = canonicalJSONValue(value)
		}
		return out
	case []any:
		if t == nil {
			// A typed nil slice marshals to null, not [] (issue #624 review).
			return nil
		}
		out := make([]any, len(t))
		for i, value := range t {
			out[i] = canonicalJSONValue(value)
		}
		return out
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Map:
		if rv.IsNil() {
			return nil
		}
		if rv.Type().Key().Kind() != reflect.String {
			return v
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = canonicalJSONValue(iter.Value().Interface())
		}
		return out
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return nil
		}
		out := make([]any, rv.Len())
		for i := range out {
			out[i] = canonicalJSONValue(rv.Index(i).Interface())
		}
		return out
	}
	return v
}

// sameJSONEncoding reports whether two values encode to identical JSON, so a
// candidate may be validated in its canonical form only when that form denotes
// the same value (issue #624 review).
func sameJSONEncoding(a, b any) bool {
	aJSON, aErr := json.Marshal(a)
	bJSON, bErr := json.Marshal(b)
	return aErr == nil && bErr == nil && bytes.Equal(aJSON, bJSON)
}

// integerCandidateExact reports whether an integer candidate round-trips through
// float64, i.e. its magnitude is at most 2^53. Larger integers are not exactly
// representable, so float64-based numeric validation could disagree with the
// exact JSON the candidate renders.
func integerCandidateExact(candidate any) bool {
	const maxExact = 1 << 53
	if i, ok := integerValue(candidate); ok {
		// Every integer kind (including named aliases) is checked, not only the
		// concrete int/int64/uint/uint64 types (issue #624 review).
		return i.IsInt64() && i.Int64() >= -maxExact && i.Int64() <= maxExact
	}
	return true
}

// hasNumericConstraint reports whether schema declares a numeric bound or
// multipleOf.
func hasNumericConstraint(schema map[string]any) bool {
	for _, key := range []string{"minimum", "exclusiveMinimum", "maximum", "exclusiveMaximum", "multipleOf"} {
		if _, present := schema[key]; present {
			return true
		}
	}
	return false
}

// numericConstraintsExact reports whether every numeric constraint schema
// declares holds its exact value in float64. The code converts bounds and
// multipleOf to float64, so a value that does not survive that conversion (a
// float32, or an integer above 2^53) would be decided against a rounded number
// and could emit an Example the compiled schema rejects (issue #624 review).
func numericConstraintsExact(schema map[string]any) bool {
	for _, key := range []string{"minimum", "exclusiveMinimum", "maximum", "exclusiveMaximum", "multipleOf"} {
		v, present := schema[key]
		if !present {
			continue
		}
		if i, ok := integerValue(v); ok {
			if !i.IsInt64() || i.Int64() < -(1<<53) || i.Int64() > 1<<53 {
				return false
			}
			continue
		}
		if reflect.ValueOf(v).Kind() != reflect.Float64 {
			// A float32 (or any other kind) would round on conversion.
			return false
		}
	}
	return true
}

// unevaluableObjectCandidateKeys are object-level keywords whose effect on a
// concrete candidate this file cannot decide (they are not part of the
// recursive walk below), so a schema carrying one rejects the candidate.
// const/enum are already checked for the candidate itself; required,
// minProperties, maxProperties and additionalProperties are checked here.
var unevaluableObjectCandidateKeys = append([]string{
	"allOf", "anyOf", "oneOf", "not",
	"dependentRequired", "dependencies", "dependentSchemas",
	"if", "then", "else",
	"patternProperties", "propertyNames", "unevaluatedProperties",
}, refKeywords...)

// objectCandidateSatisfies reports whether a concrete object candidate meets
// the object constraints schema declares: required, property-count bounds,
// additionalProperties, and each declared property's schema (recursively). A
// constraint it cannot evaluate makes the candidate decline, so the caller
// omits the Example rather than assert it (issue #624 review).
func objectCandidateSatisfies(obj map[string]any, schema map[string]any) bool {
	if slices.ContainsFunc(unevaluableObjectCandidateKeys, func(key string) bool {
		_, present := schema[key]
		return present
	}) {
		return false
	}
	if additional, present := schema["additionalProperties"]; present {
		if _, isBool := additional.(bool); !isBool {
			return false
		}
	}
	for _, name := range requiredNames(schema) {
		if _, present := obj[name]; !present {
			return false
		}
	}
	count := float64(len(obj))
	if minimum, ok := schemaFloat(schema["minProperties"]); ok && count < minimum {
		return false
	}
	if maximum, ok := schemaFloat(schema["maxProperties"]); ok && count > maximum {
		return false
	}
	props := schemaProps(schema)
	if additional, ok := schema["additionalProperties"].(bool); ok && !additional {
		for name := range obj {
			if _, declared := props[name]; !declared {
				return false
			}
		}
	}
	for name, value := range obj {
		switch prop := props[name].(type) {
		case map[string]any:
			if !candidateMatchesSchema(value, prop) {
				return false
			}
		case bool:
			// A property schema of false rejects every value.
			if !prop {
				return false
			}
		}
	}
	return true
}

// isFalsePropertySchema reports whether a schema declares the named property as
// the boolean schema false (which admits no value). A nil or boolean-true
// property schema is not false.
func isFalsePropertySchema(schema map[string]any, name string) bool {
	if schema == nil {
		return false
	}
	declared, present := schemaProps(schema)[name]
	if !present {
		return false
	}
	allow, isBool := declared.(bool)
	return isBool && !allow
}

// arrayCandidateSatisfies reports whether a concrete array candidate meets the
// array constraints schema declares. items/prefixItems/contains/uniqueItems
// cannot be decided here, so their presence declines the candidate.
func arrayCandidateSatisfies(arr []any, schema map[string]any) bool {
	for _, key := range append([]string{
		"items", "prefixItems", "contains", "uniqueItems", "unevaluatedItems",
		"const", "enum", "allOf", "anyOf", "oneOf", "not",
		"if", "then", "else",
	}, refKeywords...) {
		if _, present := schema[key]; present {
			return false
		}
	}
	count := float64(len(arr))
	if minimum, ok := schemaFloat(schema["minItems"]); ok && count < minimum {
		return false
	}
	if maximum, ok := schemaFloat(schema["maxItems"]); ok && count > maximum {
		return false
	}
	return true
}

// jsonEqual compares two JSON values for equality: both operands are
// canonicalized first (a typed const such as map[string]string and the
// json.Unmarshal-shaped candidate must compare equal), numbers by value (an int
// const and a float64 enum member are the same JSON number), everything else
// structurally.
func jsonEqual(a, b any) bool {
	a, b = canonicalJSONValue(a), canonicalJSONValue(b)
	if _, aok := numericValue(a); aok {
		ar, aExact := numericRat(a)
		br, bExact := numericRat(b)
		if aExact && bExact {
			// Compare every numeric representation exactly: a float64 == would
			// equate distinct numbers above 2^53, whether both operands are
			// integers or one is a float (issue #624 review).
			return ar.Cmp(br) == 0
		}
		return false
	}
	return reflect.DeepEqual(a, b)
}

// numericRat returns a numeric candidate's exact value, matching the schema
// validator's decimal interpretation (a float's shortest decimal form, an
// integer's exact value). ok is false for a non-numeric value or a form with no
// rational representation.
func numericRat(v any) (*big.Rat, bool) {
	if i, ok := integerValue(v); ok {
		return new(big.Rat).SetInt(i), true
	}
	f, ok := numericValue(v)
	if !ok {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(f, 'g', -1, 64))
	return r, ok
}

// integerValue returns a candidate's exact integer value for the signed and
// unsigned integer kinds (including named aliases), or ok=false for any other
// kind. It lets jsonEqual compare integer const/enum values without the
// precision loss of a float64 round-trip (issue #624 review).
func integerValue(v any) (*big.Int, bool) {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return big.NewInt(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return new(big.Int).SetUint64(rv.Uint()), true
	}
	return nil, false
}

// listHasValue reports whether a schema's enum list (any slice or array shape)
// contains candidate under jsonEqual.
func listHasValue(enum, candidate any) bool {
	for _, member := range declaredCandidates(map[string]any{"enum": enum}) {
		if jsonEqual(member, candidate) {
			return true
		}
	}
	return false
}

// candidateMatchesType reports whether candidate is one of the JSON types a
// schema's "type" declares: a single name or a union list. An absent or
// unrecognized type shape imposes no constraint here.
func candidateMatchesType(candidate, declared any) bool {
	switch t := declared.(type) {
	case string:
		return valueHasJSONType(candidate, t)
	case []any:
		return slices.ContainsFunc(t, func(e any) bool {
			name, ok := e.(string)
			return ok && valueHasJSONType(candidate, name)
		})
	case []string:
		return slices.ContainsFunc(t, func(name string) bool {
			return valueHasJSONType(candidate, name)
		})
	}
	return true
}

// valueHasJSONType reports whether v is a JSON value of the named type. An
// unknown type name imposes no constraint (true).
func valueHasJSONType(v any, name string) bool {
	switch name {
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		kind := reflect.ValueOf(v).Kind()
		return kind == reflect.Slice || kind == reflect.Array
	case "number":
		_, ok := numericValue(v)
		return ok
	case "integer":
		f, ok := numericValue(v)
		return ok && f == math.Trunc(f)
	}
	return true
}

// unhandledCandidateRiskKeys are constraints candidateSatisfies cannot
// evaluate for an arbitrary candidate, so their presence makes it decline.
var unhandledCandidateRiskKeys = append([]string{
	"pattern", "format",
	"minItems", "maxItems", "uniqueItems",
	"minProperties", "maxProperties", "contains",
	"properties", "items", "propertyNames", "additionalProperties",
	"allOf", "anyOf", "oneOf", "not",
	"if", "then", "else",
	"dependentRequired", "dependencies", "dependentSchemas",
	"patternProperties", "unevaluatedProperties", "unevaluatedItems",
}, refKeywords...)

// hasUnhandledCandidateRisk reports whether schema declares a constraint
// candidateSatisfies cannot evaluate.
func hasUnhandledCandidateRisk(schema map[string]any) bool {
	for _, key := range unhandledCandidateRiskKeys {
		if _, present := schema[key]; present {
			return true
		}
	}
	return false
}

// numericValue extracts a JSON number candidate as a float64, covering every Go
// numeric type (including named aliases, via reflection) so a candidate such as
// int32(3) is not silently exempt from numeric validation (issue #624 review).
func numericValue(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}
	return 0, false
}

// satisfiesNumeric reports whether v meets every numeric bound and multipleOf
// the schemas declare. A nil schema contributes no constraint.
func satisfiesNumeric(v float64, schemas ...map[string]any) bool {
	for _, schema := range schemas {
		if !numericValueSatisfies(v, schema) {
			return false
		}
	}
	return true
}

// numericValueSatisfies reports whether v meets one schema's numeric bounds and
// multipleOf. The multipleOf tolerance scales with the declared multiple (a
// fraction of it), so a multiple smaller than a fixed absolute tolerance is not
// waved through — a sub-tolerance multiple would otherwise accept every value
// (issue #624 review).
func numericValueSatisfies(v float64, schema map[string]any) bool {
	if !numericConstraintsExact(schema) {
		// A constraint this file cannot hold exactly in float64 must not be
		// decided against a rounded value (issue #624 review).
		return false
	}
	if minimum, ok := schemaFloat(schema["minimum"]); ok && v < minimum {
		return false
	}
	if minimum, ok := schemaFloat(schema["exclusiveMinimum"]); ok && v <= minimum {
		return false
	}
	if maximum, ok := schemaFloat(schema["maximum"]); ok && v > maximum {
		return false
	}
	if maximum, ok := schemaFloat(schema["exclusiveMaximum"]); ok && v >= maximum {
		return false
	}
	if m, ok := schemaFloat(schema["multipleOf"]); ok && m != 0 {
		// JSON Schema requires exact divisibility; a tolerance would accept a
		// near-miss like 1.0000000005 as a multiple of 1 (issue #624 review).
		isMultiple, decided := rationalMultiple(v, m)
		if !decided || !isMultiple {
			return false
		}
	}
	return true
}

// rationalMultiple reports whether v is an exact integer multiple of m, using
// the same arithmetic as the schema validator: both numbers are interpreted as
// the decimal rationals their shortest forms denote (fmt.Sprint, then big.Rat),
// and v/m must be an integer. There is no tolerance and no fixed-width
// conversion, so a near-miss like 1.0000000005 is rejected as a multiple of 1
// and a huge integral value cannot overflow (issue #624 review). decided is
// false only when a value has no decimal rational form (NaN/Inf); the caller
// treats that as "not a multiple" and omits the Example.
func rationalMultiple(v, m float64) (isMultiple, decided bool) {
	if m == 0 {
		return false, true
	}
	vRat, okV := new(big.Rat).SetString(strconv.FormatFloat(v, 'g', -1, 64))
	mRat, okM := new(big.Rat).SetString(strconv.FormatFloat(m, 'g', -1, 64))
	if !okV || !okM {
		return false, false
	}
	return new(big.Rat).Quo(vRat, mRat).IsInt(), true
}

// schemaFloat extracts a JSON-Schema numeric value (minimum and friends),
// tolerating the float64/int/int64 forms a Go map or JSON-decoded schema may
// carry. Unlike schemaInt it preserves fractions, so a bound like 0.5 is not
// silently truncated (issue #624 review).
func schemaFloat(v any) (float64, bool) {
	return numericValue(v)
}

// placeholderRiskKeys are schema keywords whose constraint a generic
// type-placeholder value cannot guarantee. A property schema carrying any of
// them makes exampleValueFor decline, so the caller omits the Example.
var placeholderRiskKeys = append([]string{
	"pattern", "format", "maxLength", "minLength",
	"maximum", "exclusiveMaximum", "exclusiveMinimum", "multipleOf",
	"minItems", "maxItems", "uniqueItems",
	"minProperties", "maxProperties", "contains",
	"properties", "items", "propertyNames", "additionalProperties",
	"allOf", "anyOf", "oneOf", "not",
	"if", "then", "else",
	"dependentRequired", "dependencies", "dependentSchemas",
	"patternProperties", "unevaluatedProperties", "unevaluatedItems",
}, refKeywords...)

// hasPlaceholderRisk reports whether schema declares a constraint a generic
// placeholder value cannot guarantee. A nil schema carries no risk.
func hasPlaceholderRisk(schema map[string]any) bool {
	for _, key := range placeholderRiskKeys {
		if _, present := schema[key]; present {
			return true
		}
	}
	return false
}

// numBound is one lower or upper numeric bound, inclusive or exclusive.
type numBound struct {
	value     float64
	exclusive bool
}

// numericExample renders a numeric property's smallest satisfying value: the
// tightest lower bound across the branch and holder, rounded up for an integer
// and rendered exactly for a number. Fractions are preserved (minimum 0.5 is
// not truncated to 0) and an integer minimum of 0.5 becomes 1 (issue #624
// review). ok is false when no bound can be rendered faithfully, so the caller
// omits the Example rather than coach an invalid retry.
func numericExample(typ string, branch, holder map[string]any) (float64, bool) {
	if hasUnhandledCandidateRisk(branch) || hasUnhandledCandidateRisk(holder) {
		return 0, false
	}
	if !numericConstraintsExact(branch) || !numericConstraintsExact(holder) {
		// Generating against a rounded bound could emit a value the compiled
		// schema rejects (issue #624 review): omit rather than convert.
		return 0, false
	}
	lower, hasLower := bindingLower(branch, holder)
	upper, hasUpper := bindingUpper(branch, holder)
	multiple, hasMultiple := bindingMultiple(branch, holder)
	var candidate float64
	// fromUpper records that the candidate derives from an upper bound alone, so
	// a multipleOf snap must go downward to stay within it.
	fromUpper := false
	switch typ {
	case "integer":
		switch {
		case hasLower && lower.exclusive:
			candidate = math.Floor(lower.value) + 1
		case hasLower:
			candidate = math.Ceil(lower.value)
		case hasUpper && upper.exclusive:
			candidate, fromUpper = math.Ceil(upper.value)-1, true
		case hasUpper:
			candidate, fromUpper = math.Floor(upper.value), true
		}
		// An integer property must land on an integer multiple of multipleOf:
		// snapping 1 with multipleOf 0.7 to 1.4 would emit a non-integer.
		if hasMultiple {
			v, ok := integerMultipleWithin(candidate, multiple, fromUpper)
			if !ok {
				return 0, false
			}
			candidate = v
		}
	default: // number
		switch {
		case hasLower && lower.exclusive:
			// Pick an interior value strictly above the bound; the exact bounds
			// check below still rejects it if it is not valid (issue #624
			// review).
			candidate = lower.value + 1
		case hasLower:
			candidate = lower.value
		case hasUpper && upper.exclusive:
			candidate, fromUpper = upper.value-1, true
		case hasUpper:
			candidate, fromUpper = math.Min(0, upper.value), true
		}
		// Snap up to the declared multiple so a sub-tolerance multipleOf
		// (smaller than a fixed absolute epsilon) cannot wave the bound itself
		// through (issue #624 review).
		if hasMultiple {
			// Snap in exact decimal arithmetic. Binary arithmetic would emit
			// 0.30000000000000004 for minimum 0.3 with multipleOf 0.1, which the
			// exact multiple check then rejects although 0.3 is valid (issue
			// #624 review). An upper bound alone snaps down, not past it.
			snapped, ok := snapMultiple(candidate, multiple, !fromUpper)
			if !ok {
				return 0, false
			}
			candidate = snapped
		}
	}
	if !satisfiesNumeric(candidate, branch, holder) {
		return 0, false
	}
	return candidate, true
}

// snapMultiple returns candidate rounded (up, or down when upward is false) to
// the nearest multiple of m, computed in exact decimal rationals and converted
// back to the nearest float64. ok is false when the decimal forms cannot be
// formed, so the caller omits the Example.
func snapMultiple(candidate, m float64, upward bool) (float64, bool) {
	cRat, okC := new(big.Rat).SetString(strconv.FormatFloat(candidate, 'g', -1, 64))
	mRat, okM := new(big.Rat).SetString(strconv.FormatFloat(m, 'g', -1, 64))
	if !okC || !okM || mRat.Sign() == 0 {
		return 0, false
	}
	q := new(big.Rat).Quo(cRat, mRat)
	k := new(big.Int).Div(q.Num(), q.Denom()) // floor for a positive denominator
	if upward && new(big.Rat).SetInt(k).Cmp(q) < 0 {
		k.Add(k, big.NewInt(1))
	}
	out := new(big.Rat).Mul(new(big.Rat).SetInt(k), mRat)
	f, _ := out.Float64()
	return f, true
}

// bindingMultiple returns the first declared multipleOf (branch first, then
// holder), or ok=false when neither declares one.
func bindingMultiple(schemas ...map[string]any) (float64, bool) {
	for _, schema := range schemas {
		if m, ok := schemaFloat(schema["multipleOf"]); ok && m != 0 {
			return math.Abs(m), true
		}
	}
	return 0, false
}

// integerMultipleWithin returns the nearest integer multiple of m at or above
// candidate (or at or below it when downward), for an integer-typed property.
// Expressing m as a reduced fraction num/den, an integer k*m is integral
// exactly when den divides k, so the valid integers are the multiples of num.
// ok is false when m has no finite decimal form within the scaling bound (then
// the Example is omitted rather than asserted off-grid or non-integral).
func integerMultipleWithin(candidate, m float64, downward bool) (float64, bool) {
	if m <= 0 {
		return 0, false
	}
	num, den := decimalRatio(m)
	if num == 0 || den == 0 {
		return 0, false
	}
	num /= gcdInt(num, den)
	if num <= 0 {
		return 0, false
	}
	const maxSnap = float64(1 << 62)
	if candidate > maxSnap || candidate < -maxSnap {
		// A bound this far out overflows int64 in the conversion below, which
		// would snap to a wrong multiple: decline instead (issue #624 review).
		return 0, false
	}
	var value int64
	if downward {
		value = floorDiv(int64(math.Floor(candidate)), num) * num
	} else {
		value = ceilDiv(int64(math.Ceil(candidate)), num) * num
	}
	return float64(value), true
}

// decimalRatio returns m as a reduced-ready fraction num/den with den a power
// of ten, via at most twelve scale steps; (0, 0) when m has no such finite form.
func decimalRatio(m float64) (int64, int64) {
	den := int64(1)
	for range 12 {
		scaled := m * float64(den)
		if math.Abs(scaled-math.Round(scaled)) < 1e-9 {
			return int64(math.Round(scaled)), den
		}
		den *= 10
	}
	return 0, 0
}

// gcdInt returns the non-negative greatest common divisor of a and b.
func gcdInt(a, b int64) int64 {
	a, b = absInt(a), absInt(b)
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func absInt(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ceilDiv returns the smallest multiple of unit that is >= value, for positive
// unit and any value.
func ceilDiv(value, unit int64) int64 {
	q := value / unit
	if value%unit > 0 {
		q++
	}
	return q
}

// floorDiv returns the largest multiple of unit that is <= value, for positive
// unit and any value.
func floorDiv(value, unit int64) int64 {
	q := value / unit
	if value%unit < 0 {
		q--
	}
	return q
}

// bindingLower returns the tightest lower bound the two schemas declare
// (maximum of minimum/exclusiveMinimum), preferring the exclusive form when the
// values tie.
func bindingLower(schemas ...map[string]any) (numBound, bool) {
	var best numBound
	set := false
	for _, schema := range schemas {
		if minimum, ok := schemaFloat(schema["minimum"]); ok {
			if !set || minimum > best.value {
				best, set = numBound{value: minimum}, true
			}
		}
		if minimum, ok := schemaFloat(schema["exclusiveMinimum"]); ok {
			if !set || minimum >= best.value {
				best, set = numBound{value: minimum, exclusive: true}, true
			}
		}
	}
	return best, set
}

// bindingUpper returns the tightest upper bound the two schemas declare
// (minimum of maximum/exclusiveMaximum), preferring the exclusive form when the
// values tie.
func bindingUpper(schemas ...map[string]any) (numBound, bool) {
	var best numBound
	set := false
	for _, schema := range schemas {
		if maximum, ok := schemaFloat(schema["maximum"]); ok {
			if !set || maximum < best.value {
				best, set = numBound{value: maximum}, true
			}
		}
		if maximum, ok := schemaFloat(schema["exclusiveMaximum"]); ok {
			if !set || maximum <= best.value {
				best, set = numBound{value: maximum, exclusive: true}, true
			}
		}
	}
	return best, set
}

// scopedBranchExample renders a complete document for root with valueExample
// placed at the kinded instance path instPath, filling the required properties
// of every enclosing object level (root and intermediate ancestors alike) with
// constraint-aware values. A failure deep under a container therefore yields a
// document that validates end to end — {"request": {"opts": {"x": "..."},
// "task": "..."}} — rather than one that drops an ancestor's required sibling
// (issue #624 review). It returns "" when a required level cannot be satisfied
// or a one-element array would break an enclosing array's constraints, letting
// the caller omit the Example.
func scopedBranchExample(root, rootBranch map[string]any, instPath []pathStep, valueExample string) string {
	if valueExample == "" || len(instPath) == 0 {
		return ""
	}
	var extras []map[string]any
	if rootBranch != nil {
		extras = []map[string]any{rootBranch}
	}
	out, ok := scopedExampleValue(root, extras, instPath, valueExample)
	if !ok {
		return ""
	}
	return out
}

// scopedExampleValue renders the JSON value of schema at instPath, with leaf at
// the end of the path. extras are additional schemas constraining this level
// (a selected root oneOf branch); they apply only to this call, not to nested
// levels. ok is false when a required level cannot be satisfied.
func scopedExampleValue(schema map[string]any, extras []map[string]any, instPath []pathStep, leaf string) (string, bool) {
	if len(instPath) == 0 {
		return leaf, true
	}
	step := instPath[0]
	if step.index {
		items, isSchema := schema["items"].(map[string]any)
		if !isSchema || !arrayExampleAllowed(schema) {
			return "", false
		}
		element, ok := scopedExampleValue(items, nil, instPath[1:], leaf)
		if !ok {
			return "", false
		}
		return "[" + element + "]", true
	}
	schemas := append([]map[string]any{schema}, extras...)
	var childValue string
	if len(instPath) == 1 {
		// The terminal property is still constrained by every schema that
		// declares it. When more than one does, their constraints cannot be
		// merged into the value built for the holder alone, so decline rather
		// than emit a leaf the root rejects (issue #624 review).
		if declaringCount(schemas, step.name) > 1 {
			return "", false
		}
		childValue = leaf
	} else {
		childSchema := propertySchema(schemas, step.name)
		if childSchema == nil {
			return "", false
		}
		value, ok := scopedExampleValue(childSchema, nil, instPath[1:], leaf)
		if !ok {
			return "", false
		}
		childValue = value
	}
	// Fill this level's required properties: the one on the path with the value
	// built above, the rest with constraint-aware values of their own. Required
	// names come from the primary schema and any extra (root branch) alike.
	fields := map[string]string{step.name: childValue}
	for _, name := range effectiveRequired(schemas) {
		if name == step.name {
			continue
		}
		value, ok := exampleValueForAny(schemas, name)
		if !ok {
			return "", false
		}
		fields[name] = value
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	if !exampleLevelAllowed(schema, extras, names) {
		return "", false
	}
	if len(extras) > 0 {
		// When an enclosing root oneOf branch was selected, the completed
		// document must still match exactly that one branch: a repair that now
		// satisfies a second branch is an over-match and invalid (issue #624
		// review).
		if !exactlyOneRootBranch(schema, names) {
			return "", false
		}
	} else if _, hasOneOf := schema["oneOf"]; hasOneOf {
		// This level declares a oneOf the path did not descend through (object
		// properties are evaluated before combinators, so a property-nested
		// failure carries no oneOf prefix). The built object was neither made to
		// satisfy nor checked against it, and may match zero branches; decline
		// rather than emit it (issue #624 review).
		return "", false
	}
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%q: %s", name, fields[name]))
	}
	return "{" + strings.Join(parts, ", ") + "}", true
}

// exactlyOneRootBranch reports whether an object carrying names matches exactly
// one branch of schema's oneOf. A schema with no oneOf imposes nothing.
func exactlyOneRootBranch(schema map[string]any, names []string) bool {
	branches, _ := schema["oneOf"].([]any)
	if len(branches) == 0 {
		return true
	}
	present := make(map[string]bool, len(names))
	for _, name := range names {
		present[name] = true
	}
	return matchingBranchCount(branches, present) == 1
}

// propertySchema returns the first schema among schemas that declares the named
// property as an object schema, or nil. It lets a value be resolved from a root
// oneOf branch that declares a property the root schema itself does not. A
// boolean declaration is a constraint too: false admits no value so it declines
// (nil), true is unconstrained; and when more than one schema declares the
// property their constraints cannot be merged into one child schema here, so it
// declines rather than silently drop one (issue #624 review).
func propertySchema(schemas []map[string]any, name string) map[string]any {
	var found map[string]any
	for _, schema := range schemas {
		declared, present := schemaProps(schema)[name]
		if !present {
			continue
		}
		if allow, isBool := declared.(bool); isBool {
			if !allow {
				return nil
			}
			continue
		}
		prop, ok := declared.(map[string]any)
		if !ok {
			continue
		}
		if found != nil {
			return nil
		}
		found = prop
	}
	return found
}

// declaringCount returns how many of schemas declare the named property.
func declaringCount(schemas []map[string]any, name string) int {
	count := 0
	for _, schema := range schemas {
		if _, present := schemaProps(schema)[name]; present {
			count++
		}
	}
	return count
}

// effectiveRequired returns the union of required property names across
// schemas, order-preserving and de-duplicated.
func effectiveRequired(schemas []map[string]any) []string {
	var out []string
	seen := make(map[string]bool)
	for _, schema := range schemas {
		for _, name := range requiredNames(schema) {
			if !seen[name] {
				seen[name] = true
				out = append(out, name)
			}
		}
	}
	return out
}

// exampleValueForAny renders a value for name that satisfies every schema
// declaring it: a property declared by the root and narrowed by the selected
// root oneOf branch is generated against both, not just the first. A single
// declaring schema is used as its own holder and branch.
func exampleValueForAny(schemas []map[string]any, name string) (string, bool) {
	var declaring []map[string]any
	for _, schema := range schemas {
		if _, present := schemaProps(schema)[name]; present {
			declaring = append(declaring, schema)
		}
	}
	switch len(declaring) {
	case 0:
		return exampleValueFor(schemas[0], schemas[0], name)
	case 1:
		return exampleValueFor(declaring[0], declaring[0], name)
	default:
		// exampleValueFor merges one holder and one branch; with more declaring
		// schemas the remaining constraints are not modelled, so decline rather
		// than assert a value that may violate one.
		if len(declaring) > 2 {
			return "", false
		}
		return exampleValueFor(declaring[1], declaring[0], name)
	}
}

// arrayExampleAllowed reports whether a one-element array example can satisfy
// the enclosing array schema. Cardinality and item constraints this file cannot
// decide make it decline, so the caller omits the Example rather than assert a
// one-element array the schema rejects (issue #624 review).
func arrayExampleAllowed(array map[string]any) bool {
	if array == nil {
		return true
	}
	if minimum, ok := schemaFloat(array["minItems"]); ok && minimum > 1 {
		return false
	}
	if maximum, ok := schemaFloat(array["maxItems"]); ok && maximum < 1 {
		return false
	}
	for _, key := range append([]string{
		"uniqueItems", "contains", "prefixItems",
		"const", "enum", "allOf", "anyOf", "oneOf", "not",
		"if", "then", "else", "unevaluatedItems",
	}, refKeywords...) {
		if _, present := array[key]; present {
			return false
		}
	}
	if items, present := array["items"]; present {
		if allow, isBool := items.(bool); isBool && !allow {
			return false
		}
	}
	return true
}

// stepPathDisplay renders kinded instance-path steps for a message: object keys
// joined with ".", array indices as "[N]" (e.g. "questions[0].opts"). Unlike
// formatPath it never infers an index from the segment text, so a property
// named "0" stays a key (issue #624 review, finding 4).
func stepPathDisplay(steps []pathStep) string {
	var b strings.Builder
	for i, step := range steps {
		if step.index {
			b.WriteString("[")
			b.WriteString(step.name)
			b.WriteString("]")
			continue
		}
		if i > 0 {
			b.WriteString(".")
		}
		b.WriteString(step.name)
	}
	return b.String()
}

// splitPointer splits a JSON-Pointer-style location into its decoded tokens.
// jsonschema renders property names containing "/" or "~" as "~1"/"~0" (RFC
// 6901), so both the keyword location and the instance location need decoding
// before they can name a schema property (issue #624 review). Returns nil for
// an empty location; accepts a location with or without a leading slash.
func splitPointer(location string) []string {
	location = strings.Trim(location, "/")
	if location == "" {
		return nil
	}
	segs := strings.Split(location, "/")
	for i, seg := range segs {
		segs[i] = decodePointerToken(seg)
	}
	return segs
}

// decodePointerToken undoes RFC 6901 escaping in one JSON Pointer token: "~1"
// is "/" and "~0" is "~" (~1 before ~0, so "~01" decodes to "~1").
func decodePointerToken(token string) string {
	if !strings.Contains(token, "~") {
		return token
	}
	token = strings.ReplaceAll(token, "~1", "/")
	return strings.ReplaceAll(token, "~0", "~")
}
