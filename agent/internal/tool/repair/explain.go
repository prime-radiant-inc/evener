package repair

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
// constraintKeyword, when non-empty, is the failing JSON-Schema keyword's
// name (the last segment of the deepest cause's KeywordLocation, e.g.
// "maxLength"), supplied by the caller (offendingKeyword). When the field is
// present and the keyword is a recognized value constraint (maxLength,
// minLength, minItems, maxItems, enum), the message names the actual
// constraint, its limit, and the offending value/length instead of the
// generic "wrong type or value" message — which otherwise sends the caller
// debugging a parameter that was correctly supplied (issue #193). A present
// field never gets the "Required arguments" tail, recognized keyword or not:
// that tail lists already-satisfied sibling fields, which is misleading
// regardless of whether the specific constraint can be detailed.
func ExplainSchemaError(toolName string, params, args map[string]any, instanceLocation, constraintKeyword string) string {
	var b strings.Builder

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
		return fmt.Sprintf("%s: argument %q has the wrong type or value.\nExample: %s%s", toolName, fullPath, ctx.example(params), ctx.wrongBranchTail(args))
	}

	// A branch-combinator failure names no property: the deepest cause is the
	// combinator (e.g. #/oneOf/0/not), so there is no field to walk and the
	// missing-field fallback below would misreport a present, valid argument
	// as missing (issue #618). Explain the constraint itself instead.
	if isBranchKeyword(constraintKeyword) {
		if msg := oneOfConstraintMessage(toolName, params, constraintKeyword); msg != "" {
			return msg
		}
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
		fmt.Fprintf(&b, "\nExample: %s", minimalExample(params))
	}
	return b.String()
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
	props := schemaProps(params)
	for _, arrayName := range actionArrays {
		arraySchema := schemaMap(props, arrayName)
		if arraySchema == nil {
			continue
		}
		item := schemaMap(arraySchema, "items")
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
		allowed := asStringSlice(fieldSchema["enum"])
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
		typ, _ := prop["type"].(string)
		return fmt.Sprintf("{%q: %s}", field, exampleValue(prop, typ))
	}
	return exampleObject(containerSchema, true)
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

// isBranchKeyword reports whether a failing JSON-Schema keyword is one of the
// combinators whose branches oneOfConstraintMessage can describe ("oneOf",
// and the "not" that lives inside a oneOf branch — the delegate shape). A
// keyword outside this set has no describable branch list; widening to
// anyOf/allOf needs per-keyword branch phrasing (issues #621-625).
func isBranchKeyword(keyword string) bool {
	switch keyword {
	case "oneOf", "not":
		return true
	}
	return false
}

// oneOfConstraintMessage renders an honest explanation for a validation
// failure caused by a branch-combinator constraint (today: the delegate
// schema's oneOf sandbox/sandbox_net pairing rule) rather than by any single
// argument's type or value. It describes each branch's requirements in terms
// of the properties it constrains, so the model can see which argument
// combination to change or omit. Returns "" when params carries no usable
// branch list for the failing keyword or no branch can be described, letting
// the caller fall back to the generic message.
func oneOfConstraintMessage(toolName string, params map[string]any, keyword string) string {
	branches, source := branchList(params, keyword)
	if len(branches) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: arguments violate a conditional rule on the combination of arguments (the schema's %s constraint), not any single argument's type or value.", toolName, source)
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
	fmt.Fprintf(&b, "\nExample: %s", minimalExample(params))
	return b.String()
}

// branchList returns the schema's branch list for the failing combinator
// keyword and the keyword that names it in the message. A "not" failure
// inside a oneOf branch (the delegate shape) has no top-level "not" list —
// the failing not's KeywordLocation path was reduced to its last segment by
// offendingKeyword — so it falls back to the enclosing oneOf and reports
// that as the source keyword (keyword-path walking is issues #621-625).
func branchList(params map[string]any, keyword string) (branches []any, source string) {
	list, _ := params[keyword].([]any)
	if list != nil {
		return list, keyword
	}
	list, _ = params["oneOf"].([]any)
	return list, "oneOf"
}

// branchRequirement renders one branch's requirement in prose: the
// properties it requires, and for a "not" branch, the properties it forbids
// being present.
func branchRequirement(branch any) string {
	schema, _ := branch.(map[string]any)
	if schema == nil {
		return ""
	}
	if forbidden := schemaMap(schema, "not"); forbidden != nil {
		names := requiredNames(forbidden)
		if len(names) == 0 {
			return ""
		}
		return "do not send " + joinQuoted(names)
	}
	req := requiredNames(schema)
	if len(req) == 0 {
		return ""
	}
	parts := []string{"send all of " + joinQuoted(req)}
	props := schemaProps(schema)
	for _, name := range req {
		prop := schemaMap(props, name)
		if allowed := asStringSlice(prop["enum"]); len(allowed) > 0 {
			parts = append(parts, fmt.Sprintf("%q must be one of %s", name, joinQuoted(allowed)))
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
		typ := ""
		if p, ok := props[name].(map[string]any); ok {
			typ, _ = p["type"].(string)
		}
		placeholder := examplePlaceholder(typ)
		if expandNested {
			placeholder = exampleValue(props[name], typ)
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
	case "array":
		return "[]"
	case "object":
		return "{}"
	default:
		return `"..."`
	}
}

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
