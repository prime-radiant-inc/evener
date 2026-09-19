package repair

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

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
	return described
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
