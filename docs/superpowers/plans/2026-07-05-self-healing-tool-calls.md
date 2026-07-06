# Self-Healing Tool Calls Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On the non-strict provider paths, repair off-distribution LLM tool calls (parameter aliasing, type coercion, JSON/unicode repair, drop-unknown-keys) before dispatch, add unknown-tool "did you mean", and replace terse validation errors with model-tuned coaching — all silent to the model but emitting a telemetry event.

**Architecture:** A new pure leaf package `agent/internal/tool/repair` holds the healing functions. A `prepareToolCall` helper in the `agent` package runs them in `execTool` *before* the PreToolUse hooks (so hooks see healed args), only when the call already failed parse/validation (lazy: zero cost on the happy path). Repairs are silent to the model; the model's raw call stays in history; a new `EventToolCallRepaired` records the drift.

**Tech Stack:** Go; `github.com/santhosh-tekuri/jsonschema/v5` (already a dependency) for schema validation and error-tree walking; standard library only inside the `repair` package.

**Spec:** `docs/superpowers/specs/2026-07-04-self-healing-tool-calls-design.md`

## Global Constraints

- The `repair` package MUST import only the standard library (plus `llm` for the fast-follow, not used in this plan). The `*jsonschema.ValidationError` tree-walk happens in the `agent` package, which passes the offending property name into `repair` as a plain `string`.
- Repairs are **silent to the model**: never add an in-context note; the model's original raw arguments stay on the assistant turn (do not mutate the `calls` slice — `execTool` takes `call` by value, keep it that way).
- Numeric coercion MUST produce Go `float64` (JSON's native map type), never `int` — downstream extractors type-assert `v.(float64)`.
- Do NOT change any tool's `Strict` setting. Do NOT relax any tool's `additionalProperties`.
- Do NOT route `providerToolName`/`providerVisibleToolNames` through `currentProfile()` from inside their own bodies — that self-deadlocks `SetModel` (holds the non-reentrant `s.mu` while reaching them). Snapshot the name-map once inside `execTool` (which runs outside `s.mu`) and pass it down.
- Match surrounding code style. Tests: table-driven where natural; assert change-lists as sets (map iteration order is nondeterministic), never by slice order.

---

## File Structure

- Create: `agent/internal/tool/repair/repair.go` — `Change`/`ChangeKind`, `RepairArgs`, alias table, schema helpers.
- Create: `agent/internal/tool/repair/repair_test.go`
- Create: `agent/internal/tool/repair/json.go` — `RepairJSON`.
- Create: `agent/internal/tool/repair/json_test.go`
- Create: `agent/internal/tool/repair/suggest.go` — `SuggestToolName`, `UnknownToolMessage`.
- Create: `agent/internal/tool/repair/suggest_test.go`
- Create: `agent/internal/tool/repair/explain.go` — `ExplainSchemaError`, `ExplainJSONError`.
- Create: `agent/internal/tool/repair/explain_test.go`
- Modify: `agent/events/events.go` — add `EventToolCallRepaired` kind.
- Modify: `agent/events/payloads.go` — add `ToolCallRepairedData`.
- Modify: `agent/events/eventdata.go` — add `eventKind()` method + compile-time assertion.
- Create: `agent/session_tool_repair.go` — `prepareToolCall`, `offendingField`, `toolNameSnapshotHelpers`.
- Create: `agent/session_tool_repair_test.go` — `prepareToolCall` unit tests.
- Modify: `agent/session_tools.go:249-424` — wire `prepareToolCall` into `execTool`.
- Create: `agent/session_tool_repair_integration_test.go` — end-to-end with a fake provider.
- Modify: `internal/appprojector/appwire_projection.go` — case for the new event.
- Modify: `cmd/serf/run.go` — case for the new event in the CLI tool-call switch.

---

### Task 1: `repair` package — Change types, schema helpers, parameter aliasing

**Files:**
- Create: `agent/internal/tool/repair/repair.go`
- Test: `agent/internal/tool/repair/repair_test.go`

**Interfaces:**
- Produces: `type ChangeKind string`; `type Change struct { Kind ChangeKind; Field string; Detail string }`; consts `ChangeAlias`, `ChangeCoerceType`, `ChangeDropUnknown`, `ChangeUnicodeRepair`; `func RepairArgs(params, args map[string]any) (map[string]any, []Change)`. In this task `RepairArgs` applies aliasing only; Tasks 2 and 3 extend it.
- Consumes: nothing.

- [ ] **Step 1: Write the failing test**

```go
package repair

import (
	"reflect"
	"testing"
)

// readFileParams mirrors read_file's real schema (definitions.go): file_path only, additionalProperties:false.
func readFileParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
		},
		"required": []any{"file_path"},
	}
}

// listDirParams mirrors list_dir: declares path natively.
func listDirParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"path": map[string]any{"type": "string"},
		},
	}
}

func TestRepairArgs_Alias_PathToFilePath(t *testing.T) {
	out, changes := RepairArgs(readFileParams(), map[string]any{"path": "/x"})
	if !reflect.DeepEqual(out, map[string]any{"file_path": "/x"}) {
		t.Fatalf("got %v", out)
	}
	if len(changes) != 1 || changes[0].Kind != ChangeAlias || changes[0].Field != "file_path" {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestRepairArgs_Alias_NoOpWhenTargetNative(t *testing.T) {
	// list_dir declares path natively → path must NOT be aliased to file_path.
	out, changes := RepairArgs(listDirParams(), map[string]any{"path": "/x"})
	if !reflect.DeepEqual(out, map[string]any{"path": "/x"}) {
		t.Fatalf("got %v", out)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no changes, got %+v", changes)
	}
}

func TestRepairArgs_Alias_NoOpWhenCanonicalPresent(t *testing.T) {
	out, changes := RepairArgs(readFileParams(), map[string]any{"path": "/a", "file_path": "/b"})
	// file_path already present → do not overwrite; path is left (Task 3 will drop it).
	if out["file_path"] != "/b" {
		t.Fatalf("file_path overwritten: %v", out)
	}
	for _, c := range changes {
		if c.Kind == ChangeAlias {
			t.Fatalf("unexpected alias change: %+v", c)
		}
	}
}

func TestRepairArgs_DoesNotMutateInput(t *testing.T) {
	in := map[string]any{"path": "/x"}
	RepairArgs(readFileParams(), in)
	if _, ok := in["file_path"]; ok {
		t.Fatal("input map was mutated")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/internal/tool/repair/`
Expected: FAIL — `undefined: RepairArgs` / `undefined: ChangeAlias`.

- [ ] **Step 3: Write minimal implementation**

```go
// Package repair heals off-distribution LLM tool calls: it renames aliased
// parameters, coerces mistyped scalars, drops hallucinated keys, and fixes
// broken JSON escapes. It is a pure, standard-library-only leaf package; the
// caller supplies a tool's JSON-Schema parameter object and the parsed args.
package repair

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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/internal/tool/repair/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/tool/repair/repair.go agent/internal/tool/repair/repair_test.go
git commit -m "feat(repair): parameter aliasing with safe-apply rule"
```

---

### Task 2: `repair` — type coercion

**Files:**
- Modify: `agent/internal/tool/repair/repair.go`
- Test: `agent/internal/tool/repair/repair_test.go`

**Interfaces:**
- Produces: extends `RepairArgs` with `applyCoercions`. Numeric coercions yield `float64`.
- Consumes: Task 1's `schemaProps`, `Change`, `ChangeCoerceType`.

- [ ] **Step 1: Write the failing test**

```go
func coerceParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"flag":  map[string]any{"type": "boolean"},
			"count": map[string]any{"type": "integer"},
			"tags":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"name":  map[string]any{"type": "string"},
		},
	}
}

func TestRepairArgs_Coerce_BoolFromString(t *testing.T) {
	out, changes := RepairArgs(coerceParams(), map[string]any{"flag": "true"})
	if out["flag"] != true {
		t.Fatalf("flag = %#v", out["flag"])
	}
	if len(changes) != 1 || changes[0].Kind != ChangeCoerceType {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestRepairArgs_Coerce_NumberIsFloat64(t *testing.T) {
	out, _ := RepairArgs(coerceParams(), map[string]any{"count": "5"})
	f, ok := out["count"].(float64) // MUST be float64, not int
	if !ok || f != 5 {
		t.Fatalf("count = %#v (want float64 5)", out["count"])
	}
}

func TestRepairArgs_Coerce_ScalarToArray(t *testing.T) {
	out, _ := RepairArgs(coerceParams(), map[string]any{"tags": "x"})
	if !reflect.DeepEqual(out["tags"], []any{"x"}) {
		t.Fatalf("tags = %#v", out["tags"])
	}
}

func TestRepairArgs_Coerce_NonNumericStringUntouched(t *testing.T) {
	out, changes := RepairArgs(coerceParams(), map[string]any{"count": "abc"})
	if out["count"] != "abc" {
		t.Fatalf("count = %#v", out["count"])
	}
	for _, c := range changes {
		if c.Field == "count" {
			t.Fatalf("unexpected coercion: %+v", c)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/internal/tool/repair/ -run TestRepairArgs_Coerce`
Expected: FAIL (values not coerced).

- [ ] **Step 3: Write minimal implementation**

Add `import ("strconv"; "strings")` to `repair.go`, extend `RepairArgs`, and add `applyCoercions`:

```go
// In RepairArgs, after the applyAliases line:
	changes = append(changes, applyCoercions(params, out)...)
```

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/internal/tool/repair/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/tool/repair/repair.go agent/internal/tool/repair/repair_test.go
git commit -m "feat(repair): scalar type coercion (float64 numbers)"
```

---

### Task 3: `repair` — drop unknown keys

**Files:**
- Modify: `agent/internal/tool/repair/repair.go`
- Test: `agent/internal/tool/repair/repair_test.go`

**Interfaces:**
- Produces: extends `RepairArgs` with `dropUnknown` (runs last).
- Consumes: Task 1's `additionalPropsFalse`, `schemaProps`, `ChangeDropUnknown`.

- [ ] **Step 1: Write the failing test**

```go
func TestRepairArgs_DropUnknown_OnlyWhenClosed(t *testing.T) {
	out, changes := RepairArgs(readFileParams(), map[string]any{"file_path": "/x", "matchCase": true})
	if _, ok := out["matchCase"]; ok {
		t.Fatalf("matchCase not dropped: %v", out)
	}
	if len(changes) != 1 || changes[0].Kind != ChangeDropUnknown || changes[0].Field != "matchCase" {
		t.Fatalf("changes = %+v", changes)
	}
}

func openParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": true,
		"properties":           map[string]any{"file_path": map[string]any{"type": "string"}},
	}
}

func TestRepairArgs_DropUnknown_KeptWhenOpen(t *testing.T) {
	out, changes := RepairArgs(openParams(), map[string]any{"file_path": "/x", "extra": 1})
	if _, ok := out["extra"]; !ok {
		t.Fatal("extra dropped despite additionalProperties:true")
	}
	for _, c := range changes {
		if c.Kind == ChangeDropUnknown {
			t.Fatalf("unexpected drop: %+v", c)
		}
	}
}

func TestRepairArgs_Order_AliasBeforeDrop(t *testing.T) {
	// old_str should be aliased to old_string, NOT dropped as unknown.
	editParams := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path":  map[string]any{"type": "string"},
			"old_string": map[string]any{"type": "string"},
			"new_string": map[string]any{"type": "string"},
		},
	}
	out, changes := RepairArgs(editParams, map[string]any{"file_path": "/x", "old_str": "a", "new_string": "b"})
	if out["old_string"] != "a" {
		t.Fatalf("old_str not aliased: %v", out)
	}
	for _, c := range changes {
		if c.Kind == ChangeDropUnknown {
			t.Fatalf("old_str was dropped instead of aliased: %+v", c)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/internal/tool/repair/ -run TestRepairArgs_Drop`
Expected: FAIL (`matchCase` not dropped).

- [ ] **Step 3: Write minimal implementation**

```go
// In RepairArgs, after the applyCoercions line:
	changes = append(changes, dropUnknown(params, out)...)
```

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/internal/tool/repair/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/tool/repair/repair.go agent/internal/tool/repair/repair_test.go
git commit -m "feat(repair): drop hallucinated keys under additionalProperties:false"
```

---

### Task 4: `repair` — JSON repair (broken escapes and lone surrogates)

**Files:**
- Create: `agent/internal/tool/repair/json.go`
- Test: `agent/internal/tool/repair/json_test.go`

**Interfaces:**
- Produces: `func RepairJSON(raw []byte) ([]byte, []Change)`. Returns `(raw, nil)` unchanged when nothing is fixed.
- Consumes: Task 1's `Change`, `ChangeUnicodeRepair`.

- [ ] **Step 1: Write the failing test**

```go
package repair

import (
	"encoding/json"
	"testing"
)

func TestRepairJSON_NoOpOnValid(t *testing.T) {
	in := []byte(`{"a":"b"}`)
	out, changes := RepairJSON(in)
	if string(out) != string(in) || changes != nil {
		t.Fatalf("out=%s changes=%+v", out, changes)
	}
}

func TestRepairJSON_LoneHighSurrogate(t *testing.T) {
	in := []byte(`{"s":"\uD800x"}`) // lone high surrogate, invalid JSON string content
	out, changes := RepairJSON(in)
	if len(changes) == 0 {
		t.Fatal("expected a change")
	}
	var m map[string]string
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("repaired JSON still invalid: %v (%s)", err, out)
	}
	if m["s"] != "�x" {
		t.Fatalf("s = %q", m["s"])
	}
}

func TestRepairJSON_ValidSurrogatePairUntouched(t *testing.T) {
	in := []byte(`{"s":"😀"}`) // 😀, a valid pair
	out, changes := RepairJSON(in)
	if changes != nil {
		t.Fatalf("valid pair altered: %+v", changes)
	}
	if string(out) != string(in) {
		t.Fatalf("out=%s", out)
	}
}

func TestRepairJSON_BrokenEscape(t *testing.T) {
	in := []byte(`{"s":"\uZZ"}`) // \u not followed by 4 hex digits
	out, changes := RepairJSON(in)
	if len(changes) == 0 {
		t.Fatal("expected a change")
	}
	if _, err := json.Marshal(json.RawMessage(out)); err != nil {
		t.Fatalf("output not marshalable: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("repaired JSON still invalid: %v (%s)", err, out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/internal/tool/repair/ -run TestRepairJSON`
Expected: FAIL — `undefined: RepairJSON`.

- [ ] **Step 3: Write minimal implementation**

```go
package repair

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// brokenEscapeRe matches a \u escape with fewer than 4 hex digits followed
	// by a non-hex char or end of string. It captures the trailing char so the
	// replacement can preserve it. Valid \uXXXX (4 hex) never matches.
	brokenEscapeRe = regexp.MustCompile(`\\u([0-9a-fA-F]{0,3})([^0-9a-fA-F]|$)`)
	// uEscapeRe matches a complete \uXXXX escape.
	uEscapeRe = regexp.MustCompile(`\\u([0-9a-fA-F]{4})`)
)

// RepairJSON makes unparseable tool-argument bytes parseable by fixing broken
// \u escapes and lone UTF-16 surrogates in string values. Deliberately narrow:
// it does not attempt general JSON slop repair (trailing commas, etc.). Returns
// (raw, nil) when it changes nothing.
func RepairJSON(raw []byte) ([]byte, []Change) {
	s := string(raw)
	var changes []Change

	if brokenEscapeRe.MatchString(s) {
		s = brokenEscapeRe.ReplaceAllString(s, `�$2`)
		changes = append(changes, Change{Kind: ChangeUnicodeRepair, Detail: `invalid \u escape → �`})
	}

	fixed, surr := fixLoneSurrogates(s)
	s = fixed
	changes = append(changes, surr...)

	if len(changes) == 0 {
		return raw, nil
	}
	return []byte(s), changes
}

func fixLoneSurrogates(s string) (string, []Change) {
	locs := uEscapeRe.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s, nil
	}
	code := func(i int) int64 {
		v, _ := strconv.ParseInt(s[locs[i][2]:locs[i][3]], 16, 32)
		return v
	}
	adjacent := func(i, j int) bool { return locs[j][0] == locs[i][1] }

	var changes []Change
	var b strings.Builder
	last := 0
	for i := range locs {
		c := code(i)
		lone := false
		switch {
		case c >= 0xD800 && c <= 0xDBFF: // high surrogate
			paired := i+1 < len(locs) && adjacent(i, i+1) && code(i+1) >= 0xDC00 && code(i+1) <= 0xDFFF
			lone = !paired
		case c >= 0xDC00 && c <= 0xDFFF: // low surrogate
			pairedPrev := i > 0 && adjacent(i-1, i) && code(i-1) >= 0xD800 && code(i-1) <= 0xDBFF
			lone = !pairedPrev
		}
		if !lone {
			continue
		}
		b.WriteString(s[last:locs[i][0]])
		b.WriteString(`�`)
		last = locs[i][1]
		changes = append(changes, Change{Kind: ChangeUnicodeRepair, Detail: `lone surrogate → �`})
	}
	if len(changes) == 0 {
		return s, nil
	}
	b.WriteString(s[last:])
	return b.String(), changes
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/internal/tool/repair/`
Expected: PASS. (If `TestRepairJSON_BrokenEscape` reveals an unhandled shape, adjust `brokenEscapeRe` and re-run — the test pins the contract.)

- [ ] **Step 5: Commit**

```bash
git add agent/internal/tool/repair/json.go agent/internal/tool/repair/json_test.go
git commit -m "feat(repair): repair broken \\u escapes and lone surrogates"
```

---

### Task 5: `repair` — tool-name suggestion and unknown-tool message

**Files:**
- Create: `agent/internal/tool/repair/suggest.go`
- Test: `agent/internal/tool/repair/suggest_test.go`

**Interfaces:**
- Produces: `func SuggestToolName(requested string, available []string) string` (closest within threshold, else ""); `func UnknownToolMessage(requested string, available []string) string`.
- Consumes: nothing.

- [ ] **Step 1: Write the failing test**

```go
package repair

import (
	"strings"
	"testing"
)

func TestSuggestToolName_CloseMatch(t *testing.T) {
	got := SuggestToolName("reed_file", []string{"read_file", "write_file", "shell"})
	if got != "read_file" {
		t.Fatalf("got %q", got)
	}
}

func TestSuggestToolName_NoMatchBeyondThreshold(t *testing.T) {
	got := SuggestToolName("zzzzzz", []string{"read_file", "shell"})
	if got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestUnknownToolMessage_IncludesSuggestionAndList(t *testing.T) {
	msg := UnknownToolMessage("reed_file", []string{"read_file", "shell"})
	if !strings.Contains(msg, `unknown tool: "reed_file"`) {
		t.Fatalf("msg = %q", msg)
	}
	if !strings.Contains(msg, `Did you mean "read_file"`) {
		t.Fatalf("msg missing suggestion: %q", msg)
	}
	if !strings.Contains(msg, "read_file") || !strings.Contains(msg, "shell") {
		t.Fatalf("msg missing available list: %q", msg)
	}
}

func TestUnknownToolMessage_NoSuggestionWhenFar(t *testing.T) {
	msg := UnknownToolMessage("zzzzzz", []string{"read_file", "shell"})
	if strings.Contains(msg, "Did you mean") {
		t.Fatalf("unexpected suggestion: %q", msg)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/internal/tool/repair/ -run 'Suggest|UnknownTool'`
Expected: FAIL — `undefined: SuggestToolName`.

- [ ] **Step 3: Write minimal implementation**

```go
package repair

import (
	"fmt"
	"strings"
)

// maxAvailableListed caps the available-tools list so an unknown-tool error
// never floods the model's context.
const maxAvailableListed = 30

// SuggestToolName returns the closest name in available to requested within an
// edit-distance threshold of min(2, ceil(len(requested)/3)), or "" if none.
func SuggestToolName(requested string, available []string) string {
	threshold := (len(requested) + 2) / 3 // ceil(len/3)
	if threshold > 2 {
		threshold = 2
	}
	if threshold < 1 {
		threshold = 1
	}
	best, bestDist := "", threshold+1
	for _, name := range available {
		d := levenshtein(requested, name)
		if d < bestDist {
			best, bestDist = name, d
		}
	}
	if bestDist <= threshold {
		return best
	}
	return ""
}

// UnknownToolMessage renders the model-facing error for an unknown tool name.
// requested and available must already be provider-visible names.
func UnknownToolMessage(requested string, available []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "unknown tool: %q.", requested)
	if s := SuggestToolName(requested, available); s != "" {
		fmt.Fprintf(&b, " Did you mean %q?", s)
	}
	listed := available
	if len(listed) > maxAvailableListed {
		listed = listed[:maxAvailableListed]
	}
	fmt.Fprintf(&b, "\nAvailable tools: %s", strings.Join(listed, ", "))
	return b.String()
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/internal/tool/repair/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/tool/repair/suggest.go agent/internal/tool/repair/suggest_test.go
git commit -m "feat(repair): unknown-tool did-you-mean via edit distance"
```

---

### Task 6: `repair` — model-tuned error messages

**Files:**
- Create: `agent/internal/tool/repair/explain.go`
- Test: `agent/internal/tool/repair/explain_test.go`

**Interfaces:**
- Produces: `func ExplainSchemaError(toolName string, params, args map[string]any, offendingField string) string`; `func ExplainJSONError(toolName string, params map[string]any, parseErr string) string`. `offendingField` is supplied by the caller (Task 8); "" means unknown → list all required.
- Consumes: Task 1's `schemaProps`.

- [ ] **Step 1: Write the failing test**

```go
package repair

import (
	"strings"
	"testing"
)

func editParamsForExplain() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path":  map[string]any{"type": "string"},
			"old_string": map[string]any{"type": "string"},
			"new_string": map[string]any{"type": "string"},
		},
		"required": []any{"file_path", "old_string", "new_string"},
	}
}

func TestExplainSchemaError_NamesOffendingField(t *testing.T) {
	msg := ExplainSchemaError("edit_file", editParamsForExplain(), map[string]any{"file_path": "/x"}, "old_string")
	if !strings.Contains(msg, `edit_file`) || !strings.Contains(msg, `"old_string"`) {
		t.Fatalf("msg = %q", msg)
	}
	if !strings.Contains(msg, "Required arguments:") || !strings.Contains(msg, "Example:") {
		t.Fatalf("msg missing required/example: %q", msg)
	}
}

func TestExplainSchemaError_FallbackWhenUnknownField(t *testing.T) {
	msg := ExplainSchemaError("edit_file", editParamsForExplain(), map[string]any{}, "")
	// Must still list required args + example even without a pinpointed field.
	if !strings.Contains(msg, "file_path") || !strings.Contains(msg, "Example:") {
		t.Fatalf("msg = %q", msg)
	}
}

func TestExplainJSONError_MentionsToolAndObject(t *testing.T) {
	msg := ExplainJSONError("read_file", editParamsForExplain(), "unexpected end of JSON input")
	if !strings.Contains(msg, "read_file") || !strings.Contains(msg, "JSON object") {
		t.Fatalf("msg = %q", msg)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/internal/tool/repair/ -run Explain`
Expected: FAIL — `undefined: ExplainSchemaError`.

- [ ] **Step 3: Write minimal implementation**

```go
package repair

import (
	"fmt"
	"sort"
	"strings"
)

// ExplainSchemaError renders model-facing coaching for a call that failed
// validation and could not be repaired. offendingField, when non-empty, names
// the specific bad property; otherwise the message lists all required args.
func ExplainSchemaError(toolName string, params, args map[string]any, offendingField string) string {
	var b strings.Builder
	if offendingField != "" {
		if _, present := args[offendingField]; present {
			fmt.Fprintf(&b, "%s: argument %q has the wrong type or value.", toolName, offendingField)
		} else {
			fmt.Fprintf(&b, "%s: missing required argument %q.", toolName, offendingField)
		}
	} else {
		fmt.Fprintf(&b, "%s: arguments did not match the schema.", toolName)
	}
	req := requiredList(params)
	if len(req) > 0 {
		fmt.Fprintf(&b, "\nRequired arguments: %s.", strings.Join(req, ", "))
	}
	fmt.Fprintf(&b, "\nExample: %s", minimalExample(params))
	return b.String()
}

// ExplainJSONError renders coaching for arguments still unparseable after RepairJSON.
func ExplainJSONError(toolName string, params map[string]any, parseErr string) string {
	return fmt.Sprintf("%s: arguments were not valid JSON (%s). Send a single JSON object, e.g. %s",
		toolName, parseErr, minimalExample(params))
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
	props := schemaProps(params)
	req := asStringSlice(params["required"])
	sort.Strings(req)
	parts := make([]string, 0, len(req))
	for _, r := range req {
		typ := ""
		if p, ok := props[r].(map[string]any); ok {
			typ, _ = p["type"].(string)
		}
		parts = append(parts, fmt.Sprintf("%q: %s", r, examplePlaceholder(typ)))
	}
	return "{" + strings.Join(parts, ", ") + "}"
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/internal/tool/repair/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/internal/tool/repair/explain.go agent/internal/tool/repair/explain_test.go
git commit -m "feat(repair): model-tuned schema and JSON error messages"
```

---

### Task 7: `EventToolCallRepaired` telemetry event

**Files:**
- Modify: `agent/events/events.go` (add EventKind const near `EventToolCallEnd`)
- Modify: `agent/events/payloads.go` (add struct near `ToolCallEndData`)
- Modify: `agent/events/eventdata.go` (add `eventKind()` method + compile-time assertion)
- Test: `agent/events/events_test.go`

**Interfaces:**
- Produces: `events.EventToolCallRepaired EventKind = "TOOL_CALL_REPAIRED"`; `events.ToolCallRepairedData{ ToolName, CallID string; Changes []string }`.
- Consumes: existing `EventData` sealed interface.

- [ ] **Step 1: Write the failing test**

Add to `agent/events/events_test.go`:

```go
func TestToolCallRepairedData_Kind(t *testing.T) {
	ev := New(ToolCallRepairedData{ToolName: "edit_file", CallID: "c1", Changes: []string{"alias:old_string:old_str→old_string"}})
	if ev.Kind != EventToolCallRepaired {
		t.Fatalf("kind = %s", ev.Kind)
	}
	d, ok := ev.Data.(ToolCallRepairedData)
	if !ok || d.ToolName != "edit_file" || len(d.Changes) != 1 {
		t.Fatalf("data = %+v", ev.Data)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/events/ -run TestToolCallRepairedData_Kind`
Expected: FAIL — `undefined: ToolCallRepairedData` / `EventToolCallRepaired`.

- [ ] **Step 3: Write minimal implementation**

In `events.go`, after the `EventToolCallEnd` const line:

```go
	// EventToolCallRepaired reports that a tool call's arguments were healed
	// before dispatch (aliasing, coercion, JSON/unicode repair, drop-unknown).
	// Silent to the model; emitted for drift telemetry.
	EventToolCallRepaired EventKind = "TOOL_CALL_REPAIRED"
```

In `payloads.go`, after `ToolCallEndData`:

```go
// ToolCallRepairedData reports the repairs applied to a tool call's arguments.
// Each entry in Changes is encoded "kind:field:detail".
type ToolCallRepairedData struct {
	ToolName string   `json:"tool_name"`
	CallID   string   `json:"call_id"`
	Changes  []string `json:"changes"`
}
```

In `eventdata.go`, add the `eventKind()` method next to the other `ToolCall*` ones:

```go
func (ToolCallRepairedData) eventKind() EventKind { return EventToolCallRepaired }
```

And add the compile-time assertion alongside the existing `var _ EventData = ...` block:

```go
var _ EventData = ToolCallRepairedData{}
```

(Match the exact form of the surrounding assertions in `eventdata.go`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/events/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/events/
git commit -m "feat(events): add EventToolCallRepaired"
```

---

### Task 8: `prepareToolCall` — the repair orchestrator (agent package)

**Files:**
- Create: `agent/session_tool_repair.go`
- Test: `agent/session_tool_repair_test.go`

**Interfaces:**
- Produces:
  - `type prepareResult struct { Call llm.ToolCallData; Changes []repair.Change; PrevalErr string }`
  - `func prepareToolCall(call llm.ToolCallData, t *tool.RegisteredTool, visibleNames []string, requestedVisible string) prepareResult`
  - `func changeStrings([]repair.Change) []string` (encodes for the event)
  - `func offendingField(err error) string`
- Consumes: Task 1-6 `repair` funcs; `tool.RegisteredTool` (`.Schema *jsonschema.Schema`, `.Definition.Parameters map[string]any`); agent-package `shortHash` (`agent/runtime_dir.go:53`).

- [ ] **Step 1: Write the failing test**

```go
package agent

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

// regTool builds a RegisteredTool with a no-op executor so registration succeeds.
func regTool(def llm.ToolDefinition) tool.RegisteredTool {
	return tool.RegisteredTool{
		Tool: llm.Tool{Definition: def},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return "ok", nil
		},
	}
}

func editTool(t *testing.T) *tool.RegisteredTool {
	t.Helper()
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefEditFile())); err != nil {
		t.Fatalf("register: %v", err)
	}
	return reg.Get("edit_file")
}
```

```go
func TestPrepareToolCall_AliasesArgs(t *testing.T) {
	et := editTool(t)
	call := llm.ToolCallData{ID: "c1", Name: "edit_file",
		Arguments: json.RawMessage(`{"file_path":"/x","old_str":"a","new_string":"b"}`)}
	res := prepareToolCall(call, et, []string{"edit_file"}, "edit_file")
	if res.PrevalErr != "" {
		t.Fatalf("unexpected prevalErr: %s", res.PrevalErr)
	}
	var got map[string]any
	if err := json.Unmarshal(res.Call.Arguments, &got); err != nil {
		t.Fatalf("unmarshal healed: %v", err)
	}
	if got["old_string"] != "a" {
		t.Fatalf("not aliased: %v", got)
	}
	if len(res.Changes) == 0 {
		t.Fatal("expected changes recorded")
	}
}

func TestPrepareToolCall_UnknownTool(t *testing.T) {
	call := llm.ToolCallData{ID: "c1", Name: "reed_file", Arguments: json.RawMessage(`{}`)}
	res := prepareToolCall(call, nil, []string{"read_file", "edit_file"}, "reed_file")
	if res.PrevalErr == "" {
		t.Fatal("expected prevalErr for unknown tool")
	}
}

func TestPrepareToolCall_EmptyArgsValidForNoRequiredTool(t *testing.T) {
	reg := tool.NewRegistry()
	def := tool.DefListDir()
	_ = reg.Register(regTool(def)) // regTool: helper building a RegisteredTool with a no-op Exec (see below)
	res := prepareToolCall(
		llm.ToolCallData{ID: "c1", Name: "list_dir", Arguments: json.RawMessage(``)},
		reg.Get("list_dir"), []string{"list_dir"}, "list_dir")
	if res.PrevalErr != "" {
		t.Fatalf("empty args rejected: %s", res.PrevalErr)
	}
}

func TestPrepareToolCall_SynthesizesStableIDWhenEmpty(t *testing.T) {
	et := editTool(t)
	call := llm.ToolCallData{Name: "edit_file",
		Arguments: json.RawMessage(`{"file_path":"/x","old_string":"a","new_string":"b"}`)}
	res := prepareToolCall(call, et, []string{"edit_file"}, "edit_file")
	if res.Call.ID == "" {
		t.Fatal("expected synthesized ID")
	}
}
```

> Add a small `regTool(def llm.ToolDefinition) tool.RegisteredTool` helper in the test file that fills `Exec` with a no-op returning `("", nil)` so registration succeeds. Reuse it in `editTool`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestPrepareToolCall`
Expected: FAIL — `undefined: prepareToolCall`.

- [ ] **Step 3: Write minimal implementation**

```go
package agent

import (
	"encoding/json"
	"errors"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"

	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/internal/tool/repair"
	"primeradiant.com/serf/llm"
)

// prepareResult is the outcome of the pre-dispatch repair step. When PrevalErr
// is non-empty, execTool returns it as the tool's error result WITHOUT calling
// ExecuteCall — but still runs the full event/hook lifecycle.
type prepareResult struct {
	Call      llm.ToolCallData
	Changes   []repair.Change
	PrevalErr string
}

// prepareToolCall heals a tool call before dispatch. t is the resolved tool
// (nil if the name is unknown). visibleNames and requestedVisible are already
// provider-visible names (the caller snapshots the name-map outside s.mu).
func prepareToolCall(call llm.ToolCallData, t *tool.RegisteredTool, visibleNames []string, requestedVisible string) prepareResult {
	res := prepareResult{Call: call}
	if strings.TrimSpace(res.Call.ID) == "" {
		res.Call.ID = "call_" + shortHash(res.Call.Arguments)
	}
	if t == nil {
		res.PrevalErr = repair.UnknownToolMessage(requestedVisible, visibleNames)
		return res
	}

	args := map[string]any{}
	if len(res.Call.Arguments) > 0 { // raw len, mirroring ExecuteCall (no TrimSpace)
		if err := json.Unmarshal(res.Call.Arguments, &args); err != nil {
			repaired, c := repair.RepairJSON(res.Call.Arguments)
			res.Changes = append(res.Changes, c...)
			args = map[string]any{}
			if err2 := json.Unmarshal(repaired, &args); err2 != nil {
				res.PrevalErr = repair.ExplainJSONError(requestedVisible, t.Definition.Parameters, err2.Error())
				return res
			}
		}
	}

	if err := t.Schema.Validate(args); err != nil {
		healed, c := repair.RepairArgs(t.Definition.Parameters, args)
		if err2 := t.Schema.Validate(healed); err2 != nil {
			res.PrevalErr = repair.ExplainSchemaError(requestedVisible, t.Definition.Parameters, healed, offendingField(err2))
			return res
		}
		args = healed
		res.Changes = append(res.Changes, c...)
	}

	if len(res.Changes) > 0 {
		if b, err := json.Marshal(args); err == nil {
			res.Call.Arguments = b
		}
	}
	return res
}

// changeStrings encodes changes as "kind:field:detail" for the telemetry event.
func changeStrings(changes []repair.Change) []string {
	out := make([]string, 0, len(changes))
	for _, c := range changes {
		out = append(out, string(c.Kind)+":"+c.Field+":"+c.Detail)
	}
	return out
}

// offendingField extracts the single offending property from a jsonschema
// validation error, or "" when it cannot be pinpointed (e.g. missing-required,
// where the instance location is the parent object). ExplainSchemaError falls
// back to listing all required args in that case.
func offendingField(err error) string {
	var ve *jsonschema.ValidationError
	if !errors.As(err, &ve) {
		return ""
	}
	for len(ve.Causes) > 0 {
		ve = ve.Causes[0]
	}
	loc := strings.Trim(ve.InstanceLocation, "/")
	if loc == "" {
		return ""
	}
	parts := strings.Split(loc, "/")
	return parts[len(parts)-1]
}
```

> If `ve.InstanceLocation` is not the exact field name on this vendored `jsonschema/v5`, the failing test will show it; adjust the field access to the actual `*ValidationError` shape. The `""` fallback keeps `ExplainSchemaError` correct regardless.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestPrepareToolCall`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session_tool_repair.go agent/session_tool_repair_test.go
git commit -m "feat(agent): prepareToolCall repair orchestrator"
```

---

### Task 9: Wire `prepareToolCall` into `execTool` + end-to-end test

**Files:**
- Modify: `agent/session_tools.go` (`execTool`, lines 249-424)
- Create: `agent/session_tool_repair_integration_test.go`

**Interfaces:**
- Consumes: Task 8's `prepareToolCall`, `changeStrings`; `events.ToolCallRepairedData`; existing `s.reg.Get`/`s.reg.Names`, `s.providerVisibleToolNames`, `s.providerToolName`, `s.currentProfile`, `s.emit`.
- Produces: healed dispatch behavior + `EventToolCallRepaired` emission.

**Wiring (do this at the top of `execTool`, after the first `abortIfClosing` at ~:252 and before the PreToolUse block at ~:254):**

- [ ] **Step 1: Write the failing integration test**

Model the fake-provider harness on `agent/session_openai_malformed_tool_call_test.go` (httptest SSE server, `writeResponsesFunctionCall`, `mustJSON`, `NewSession`, `sess.RegisterTool`, `sess.Events()`). Anthropic is the cleanest non-strict path, but Responses works for a `Strict:false`-style custom tool. Use a custom registered tool whose schema has `additionalProperties:false` so an aliased arg fails validation and triggers repair.

```go
func TestSession_RepairsAliasedArgAndEmitsEvent(t *testing.T) {
	// ... set up httptest server as in session_openai_malformed_tool_call_test.go ...
	// Request 1: model calls "widget" with {"pathx":"/x"} where the tool declares
	//            "file_path" and additionalProperties:false, and "pathx" is NOT a
	//            registered alias — instead use a real alias case: tool declares
	//            "file_path"; model sends {"path":"/x"}.
	// Request 2: model calls communicate(end_turn=true) to finish.

	var gotArgs map[string]any
	sess.RegisterTool("widget", "does a thing", map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"file_path": map[string]any{"type": "string"}},
		"required":             []any{"file_path"},
	}, func(ctx context.Context, args map[string]any) (any, error) {
		gotArgs = args
		return "done", nil
	})

	var repaired []events.ToolCallRepairedData
	done := make(chan struct{})
	go func() {
		for ev := range sess.Events() {
			if d, ok := ev.Data.(events.ToolCallRepairedData); ok {
				repaired = append(repaired, d)
			}
		}
		close(done)
	}()

	// drive the session with one input turn that elicits the widget call...
	// (ProcessInput as in the reference test)

	// assertions:
	if gotArgs["file_path"] != "/x" {
		t.Fatalf("tool did not receive healed file_path: %v", gotArgs)
	}
	if _, ok := gotArgs["path"]; ok {
		t.Fatalf("unhealed 'path' reached the tool: %v", gotArgs)
	}
	// after sess.Close(), drain:
	<-done
	if len(repaired) == 0 || repaired[0].ToolName != "widget" {
		t.Fatalf("EventToolCallRepaired not emitted: %+v", repaired)
	}
}
```

> Adapt the exact `RegisterTool` signature and executor shape to the reference test (it registers `my_strict_tool` there — copy that call's shape). Use the Responses `writeResponsesFunctionCall` helper for the two model turns.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestSession_RepairsAliasedArgAndEmitsEvent`
Expected: FAIL — the tool receives `{"path":"/x"}` (or the call errors), no repaired event.

- [ ] **Step 3: Write the wiring in `execTool`**

Insert after the first `abortIfClosing` and before the PreToolUse hook block:

```go
	// Self-heal off-distribution tool calls before hooks/dispatch. Snapshot the
	// provider name-map here (execTool runs outside s.mu) — never lock inside
	// providerToolName, which has under-lock callers (SetModel).
	nameMap := s.currentProfile().ToolNameMap()
	visibleNames := providerVisibleFromMap(s.reg.Names(), nameMap)
	requestedVisible := providerNameFromMap(call.Name, nameMap)
	prep := prepareToolCall(call, s.reg.Get(call.Name), visibleNames, requestedVisible)
	call = prep.Call
	if len(prep.Changes) > 0 {
		s.emit(events.EventToolCallRepaired, events.ToolCallRepairedData{
			ToolName: call.Name,
			CallID:   call.ID,
			Changes:  changeStrings(prep.Changes),
		})
	}
```

Add two small pure helpers (in `session_tool_repair.go`) that map names using a snapshot rather than reading `s.profile` — so no lock is taken on the hot path:

```go
func providerNameFromMap(name string, nameMap map[string]string) string {
	if v, ok := nameMap[name]; ok {
		return v
	}
	return name
}

func providerVisibleFromMap(names []string, nameMap map[string]string) []string {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, n := range names {
		v := providerNameFromMap(n, nameMap)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
```

Then, at the dispatch site (where `execTool` currently calls `s.reg.ExecuteCall` at ~:357), short-circuit on `prep.PrevalErr`:

```go
	var res tool.ExecResult
	if prep.PrevalErr != "" {
		res = tool.ExecResult{
			ToolName:   call.Name,
			CallID:     call.ID,
			Output:     prep.PrevalErr,
			FullOutput: prep.PrevalErr,
			IsError:    true,
		}
	} else {
		res = s.reg.ExecuteCall(ctx, s.env, call)
	}
	res.DurationMS = time.Since(toolStart).Milliseconds()
```

> Keep the existing `EventToolCallStart`/`EventToolCallEnd` emission and Pre/PostToolUse hook blocks exactly where they are — `prep` never short-circuits the lifecycle; only the `ExecuteCall` invocation is conditional. Confirm the exact `tool.ExecResult` field names against `agent/internal/tool/registry.go` (`ToolName`, `CallID`, `Output`, `FullOutput`, `IsError`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestSession_RepairsAliasedArgAndEmitsEvent`
Then the full agent suite: `go test ./agent/...`
Expected: PASS, no regressions.

- [ ] **Step 5: Add the repaired-then-denied telemetry guard**

Add a test that registers a PreToolUse hook denying `widget`, sends the aliased call, and asserts `EventToolCallRepaired` is STILL emitted (because emission happens before the hook block). Model hook registration on existing hook tests in `agent/`.

```bash
go test ./agent/ -run 'TestSession_Repairs'
```
Expected: PASS (repaired event present even when denied).

- [ ] **Step 6: Commit**

```bash
git add agent/session_tools.go agent/session_tool_repair.go agent/session_tool_repair_integration_test.go
git commit -m "feat(agent): wire self-healing repair into execTool before hooks"
```

---

### Task 10: Surface the event in consumers

**Files:**
- Modify: `internal/appprojector/appwire_projection.go`
- Modify: `cmd/serf/run.go`
- Test: `internal/appprojector/appwire_projection_test.go` (if the package has one; else assert via `go build` + a projector unit test)

**Interfaces:**
- Consumes: `events.EventToolCallRepaired`, `events.ToolCallRepairedData`.

- [ ] **Step 1: Write the failing test**

In `internal/appprojector/appwire_projection_test.go`, add a test that projects a `SessionEvent` of kind `EventToolCallRepaired` and asserts it does not hit the `default:` drop (e.g. it produces a projected record or annotation). Match the package's existing projection-test style; assert on whatever observable the other tool-call cases produce.

```go
func TestProjection_ToolCallRepaired(t *testing.T) {
	ev := events.New(events.ToolCallRepairedData{ToolName: "edit_file", CallID: "c1", Changes: []string{"alias:old_string:old_str→old_string"}})
	// project ev through the same entrypoint the other cases use; assert it is handled, not dropped.
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/appprojector/ -run ToolCallRepaired`
Expected: FAIL (event dropped by `default:`).

- [ ] **Step 3: Add the switch cases**

In `appwire_projection.go`, add a `case events.EventToolCallRepaired:` to the top-level `switch event.Kind` (near the `EventToolCallStart`/`End` cases) that records/annotates the repair on the corresponding tool-call bubble (follow the pattern the `EventToolCallEnd` case uses to locate the bubble by `CallID`).

In `cmd/serf/run.go`, add a `case events.EventToolCallRepaired:` to the CLI's tool-call event switch that prints a concise dim line (e.g. `↻ repaired edit_file: old_str→old_string`) using `ToolCallRepairedData.Changes`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/appprojector/ && go build ./...`
Expected: PASS and clean build.

- [ ] **Step 5: Commit**

```bash
git add internal/appprojector/appwire_projection.go cmd/serf/run.go internal/appprojector/appwire_projection_test.go
git commit -m "feat: surface EventToolCallRepaired in projector and CLI"
```

---

## Final verification

- [ ] **Run the whole suite:** `go test ./...` — Expected: PASS.
- [ ] **Vet + race on the touched packages:** `go vet ./agent/... ./internal/appprojector/... && go test -race ./agent/ -run 'Repair|PrepareToolCall'` — Expected: clean, no data races (validates the name-map-snapshot concurrency fix).
- [ ] **Manual smoke (optional):** run `serf` against an Anthropic model on a task that reliably triggers `old_str`/`path` drift and confirm the repaired event appears in the CLI output and the tool runs.

## Notes carried forward (out of scope this plan)

- **Leaked tool-call markup recovery** is a separate fast-follow spec (requires rewriting the already-committed assistant turn to carry `tool_use` parts). See the spec's "Fast-follow" section.
- **Nested-key healing** (`communicate.output`, `task_list.tasks[]`) is deferred; `RepairArgs` is top-level-only.
- **`tools/tool-fluency`** may add an `EventToolCallRepaired` case to measure drift; not required for this feature to function.
