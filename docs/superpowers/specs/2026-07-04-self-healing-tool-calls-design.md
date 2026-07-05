# Design: Self-healing tool calls

**Status:** Approved for spec review
**Date:** 2026-07-04
**Author:** Bot (with Jesse)
**Motivation:** Armin Ronacher, ["Better Models, Worse Tools"](https://lucumr.pocoo.org/2026/7/4/better-models-worse-tools/)

## Problem

Newer models (Opus 4.8, Sonnet 5, and peers) are reinforcement-trained *inside*
Claude Code's tool harness. They have learned to emit Claude-Code-shaped tool
calls, and they drift off-distribution when a different harness presents a
different schema: they invent extra keys (`requireUnique`, `matchCase`, `type`,
`id`), reach for the parameter names they saw in training (`old_str` for
`old_string`), and occasionally emit malformed JSON (broken `\uXXXX`, lone
surrogates) or leak tool-call markup into prose. Ronacher's thesis: a harness
either has to *prevent* those calls (strict constrained decoding, with quality
tradeoffs) or *absorb* them (a forgiving harness that repairs the slop).

Serf today sits in the worst-case quadrant. `Registry.ExecuteCall`
(`agent/internal/tool/registry.go:446`) validates strictly — nearly every tool
definition carries `additionalProperties:false` plus a `required` list, checked
by `santhosh-tekuri/jsonschema` — but the model runs with `Strict:false`, so
there is no constrained decoding on the wire. Serf therefore **rejects**
off-distribution calls after the fact instead of either preventing or absorbing
them. It pays the cost of strictness (off-distribution rejections) without the
benefit (guaranteed-valid calls).

### Gap analysis: Claude Code trick → serf today

| Claude Code trick | Serf today | Gap |
|---|---|---|
| Parameter aliasing (`old_str`→`old_string`, `path`→`file_path`) | Rejected — `additionalProperties:false` treats aliases as illegal extra keys | Serf is *stricter* than CC |
| Silent unknown-key filtering | Rejected with `additionalProperties "foo" not allowed` | Opposite behavior |
| Unicode escape / lone-surrogate repair | None | Missing |
| Silent type coercion | None at dispatch | Missing |
| Unknown-tool "did you mean" | `unknown tool: <name>`, no suggestion | Missing |
| Leaked `<invoke>` markup recovery | `handleNoToolCalls` steers on bare text but does not scan for leaked calls | Partial |
| Repair + re-dispatch loop | `RepairToolCall` seam exists but is wired only into the secondary `llm.Generate` engine (session-naming), never the real agent path | Ready-made pattern, unwired |
| Model-tuned error messages | Raw jsonschema error text passed through verbatim | Missing |

## Decisions (settled during brainstorming)

1. **Direction: forgiving harness only.** Adopt the self-healing tricks; leave
   `Strict:false` as-is. Keeps serf provider-agnostic; strict constrained
   decoding is explicitly out of scope for this spec.
2. **Repair visibility: silent to the model, visible to telemetry.** A
   successfully repaired call runs and returns a normal result — no in-context
   note, no wasted tokens teaching the model. Every repair emits a telemetry
   event so we can *measure* how often models go off-distribution. This is what
   Claude Code does, and the metric serves honesty better than hiding the repair
   would.
3. **Scope: all four capabilities** — argument normalization (the core),
   unknown-tool "did you mean", model-tuned error messages, and leaked-markup
   recovery.
4. **Architecture: a lazy repair ladder inside `ExecuteCall`.** Repair fires
   only when parse or validation already failed. Serf's existing strictness is
   the trigger, so the happy path is untouched and no valid call is ever mutated.

## Architecture

All argument-level healing hangs off the two failure branches that already exist
in `ExecuteCall` (`registry.go:446`). The healthy path — lookup, unmarshal,
validate, middleware, exec — is unchanged.

```
ExecuteCall(call):
  t, ok := tools[name]
  if !ok:
      return didYouMean(name, toolNames)          # add-on 1

  args, err := json.Unmarshal(call.Arguments)
  if err != nil:
      repaired, changes := repair.RepairJSON(call.Arguments)   # rung 1
      args, err = json.Unmarshal(repaired)
      if err != nil:
          return explainJSONError(name, params, err)           # add-on 2
      record(changes)

  if err := schema.Validate(args); err != nil:
      args2, changes := repair.RepairArgs(t.Definition.Parameters, args)   # rung 2
      if err2 := schema.Validate(args2); err2 != nil:
          return explainSchemaError(name, params, args2, err2)  # add-on 2
      args = args2
      record(changes)

  ... middleware, exec (unchanged) ...
  res.Repairs = changes         # surfaced to execTool for telemetry
```

`repair` is a new leaf package under `agent/internal/tool/repair`. It has no
dependency on `Session`, `Registry`, or the event bus — it is pure functions
over bytes and maps, so it is unit-testable with zero agent scaffolding. It may
import `llm` for the leaked-markup component's `ToolCallData` return type.

### Data flow for telemetry

`ExecuteCall` cannot emit events — it lives in the `tool` package and has no
`s.emit`. So `ExecResult` gains a `Repairs []repair.Change` field.
`execTool` (`agent/session_tools.go:357`) already brackets the call with
`EventToolCallStart` / `EventToolCallEnd`; after `ExecuteCall` returns it emits a
new `EventToolCallRepaired` when `len(res.Repairs) > 0`. The `tool` package stays
pure; emission stays where `s.emit` is available.

## Component 1: argument normalization (`repair.RepairArgs`)

```go
package repair

type ChangeKind string
const (
    ChangeAlias         ChangeKind = "alias"
    ChangeCoerceType    ChangeKind = "coerce_type"
    ChangeDropUnknown   ChangeKind = "drop_unknown"
    ChangeUnicodeRepair ChangeKind = "unicode_repair"
)

type Change struct {
    Kind   ChangeKind
    Field  string // affected key ("" for whole-document JSON repair)
    Detail string // e.g. "old_str→old_string", `"true"→true`, "dropped matchCase"
}

// RepairArgs normalizes a parsed argument map against the tool's JSON-Schema
// parameter object. Applies, in order: alias → coerce → drop-unknown. Returns a
// fresh map (never mutates the input) plus the changes made.
func RepairArgs(params map[string]any, args map[string]any) (map[string]any, []Change)
```

Three normalizers, applied in this order (the order is load-bearing):

### 1a. Parameter aliasing

A built-in table of off-distribution names → canonical names, applied per-tool
under a **safe-apply rule**:

> Rename `X → Y` for a given tool only if: `Y` is a declared property of that
> tool, `X` is *not* a declared property, `X` is present in `args`, and `Y` is
> absent from `args`.

The safe rule is what lets a single global table stay correct across tools:
`path → file_path` fires for `read_file` / `edit_file` / `write_file` (which
declare `file_path` and not `path`) but is a silent no-op for `list_dir` (which
declares `path` natively). No per-tool configuration needed.

Initial table, seeded from serf's *actual* parameter names (verified in
`agent/internal/tool/definitions.go`) plus the Claude-Code names models were
trained on:

| Alias | Canonical | Rationale |
|---|---|---|
| `old_str` | `old_string` | CC edit short form |
| `new_str` | `new_string` | CC edit short form |
| `path` | `file_path` | generic path name |
| `filepath` | `file_path` | spacing variant |
| `filename` | `file_path` | generic name |
| `contents` | `content` | pluralization drift |
| `cmd` | `command` | shell short form |

The table is data, extended as telemetry reveals new drift. Serf's `edit_file`
schema (`file_path` / `old_string` / `new_string` / `replace_all`) already
matches Claude Code's exactly, so the residual edit-tool risk is only the short
forms above.

### 1b. Type coercion

For each key whose declared property schema has a scalar `type`:

- schema `boolean` + string `"true"` / `"false"` (case-insensitive) → bool
- schema `integer` / `number` + a numeric string (`"5"`, `"1.5"`) → number
- schema `array` + a non-array scalar → single-element `[scalar]`

Conservative by construction: coercion only fires when the target type is
unambiguous. A non-numeric string against a `number` schema is left untouched so
it surfaces as a real (model-tuned) error rather than a silent guess.

### 1c. Drop-unknown

Only when the tool schema sets `additionalProperties:false`: remove any remaining
key that matches no declared property, recording each as `ChangeDropUnknown`.
This is Claude Code's "silently filter unexpected keys," except every dropped key
is recorded for telemetry. (`purpose` is injected as a real property by
`WithPurposeParameter`, so it is a declared property and is never dropped here.)

**Order rationale:** alias before drop, so `old_str` becomes `old_string`
instead of being discarded as unknown; coerce before drop, so a coercible key is
never dropped.

## Component 1 (cont.): JSON repair (`repair.RepairJSON`)

```go
// RepairJSON makes unparseable tool-argument bytes parseable by fixing lone
// surrogates and broken \uXXXX escape sequences in string values. Returns
// (raw, nil) unchanged when it finds nothing to fix.
func RepairJSON(raw []byte) ([]byte, []Change)
```

Scope is deliberately narrow — the two failure modes the article names:

- **Lone surrogates:** an unpaired `\uD800`–`\uDFFF` escape (or a raw lone
  surrogate rune) is replaced with U+FFFD, or, when it forms a valid pair with an
  adjacent escape, combined correctly.
- **Broken `\uXXXX`:** a `\u` not followed by four hex digits is escaped so the
  document parses.

RepairJSON does **not** attempt general JSON slop repair (trailing commas,
unquoted keys, single quotes). Those are out of scope; if they appear, the
model-tuned JSON error (add-on 2) coaches the model to re-emit.

## Component 2 (add-on 1): unknown-tool "did you mean"

Replace the bare `unknown tool: <name>` at `registry.go:457`.

```go
// SuggestToolName returns the closest registered tool name to requested, or ""
// if none is within the similarity threshold.
func SuggestToolName(requested string, available []string) string
```

Levenshtein distance with a threshold of `min(2, ceil(len/3))` edits. The
message becomes:

```
unknown tool: "reed_file". Did you mean "read_file"?
Available tools: read_file, write_file, edit_file, shell, grep, glob, ...
```

The available-tools list is capped (e.g. first N by name) so an unknown-tool
error never floods the context. `Registry` already holds the canonical names in
`r.tools`; gather its keys.

## Component 3 (add-on 2): model-tuned error messages

When repair fails, replace the terse library text at `registry.go:464` and `:473`
with actionable coaching derived from `t.Definition.Parameters`:

```go
// ExplainSchemaError renders a model-facing message naming the tool, the
// offending argument(s), the expected shape, and a minimal valid example.
func ExplainSchemaError(toolName string, params map[string]any, args map[string]any, verr error) string

// ExplainJSONError renders a model-facing message for arguments that stay
// unparseable after RepairJSON.
func ExplainJSONError(toolName string, params map[string]any, parseErr error) string
```

Schema-error output leads with the actionable statement, then a minimal example
built from the required properties and their declared types, then appends the
raw jsonschema detail for precision:

```
edit_file: missing required argument "old_string" (string).
Required arguments: file_path (string), old_string (string), new_string (string).
Example: {"file_path": "...", "old_string": "...", "new_string": "..."}
(schema detail: missing properties: "old_string")
```

## Component 4 (add-on 3): leaked tool-call markup recovery

The riskiest piece; scoped conservatively. When the turn loop finds a response
with assistant text but zero structured tool calls (the bare-text branch that
today routes to `handleNoToolCalls`, `agent/session_tool_round.go:56`), first
attempt recovery:

```go
// RecoverLeakedCalls scans assistant prose for tool-call markup that leaked out
// of the structured channel and returns any well-formed calls it can parse.
// knownTools gates recovery to real tool names so prose that merely mentions a
// tool is not misread as a call.
func RecoverLeakedCalls(text string, knownTools []string) []llm.ToolCallData
```

Recognized, only when well-formed and naming a known tool:

- Anthropic-style `<invoke name="X">…<parameter name="k">v</parameter>…</invoke>`
  (optionally inside `<function_calls>`).
- `<tool_call>{"name":"X","arguments":{…}}</tool_call>` and the bare
  `{"name":"X","arguments":{…}}` object form.

On ≥1 recovered call: emit `EventToolCallRecovered` (telemetry), synthesize the
`ToolCallData`, and route them into `execToolBatch` exactly as if the provider
had returned them — so recovered calls flow through the *same* repair ladder
above. On zero recovered calls: fall through to the existing
`handleNoToolCalls` steering unchanged. Recovery never loops: a response either
yields calls (executed once) or falls through to the existing retry budget.

**Integration point to confirm at implementation time:** the exact variable
holding the assistant text at the `len(calls)==0` decision in
`agent/session_lifecycle.go` (near the reverse-name-mapping at `:662` and the
`execToolBatch` / `handleNoToolCalls` fork). The surrounding structure is known;
the precise binding must be read from the code, not assumed.

## Telemetry

Two new event types on the existing `events` bus, each also written to the
session log:

- `EventToolCallRepaired` — `ToolCallRepairedData{ToolName, CallID, Changes []string}`,
  emitted by `execTool` when `ExecResult.Repairs` is non-empty. `Changes` encodes
  each `repair.Change` as `"kind:field:detail"`.
- `EventToolCallRecovered` — `ToolCallRecoveredData{Count int, ToolNames []string}`,
  emitted in the turn loop when leaked markup is recovered.

These give the metric the article implies we should want: how often, and in what
way, models drift off serf's schema. The repaired call itself is silent to the
model.

## Error handling and edge cases

- **No happy-path change.** Valid calls never enter the ladder, so behavior for
  well-formed calls is byte-for-byte unchanged.
- **Repair strictly improves failures.** Worst case, repair fails to fix a bad
  call and we surface a *better* (model-tuned) error than today.
- **History fidelity.** The original malformed arguments remain on the assistant
  turn in local history; only the *executed* args are repaired. This matches the
  existing malformed-args safe-replay contract
  (`llm/providers/internal/openaichat/openaichat.go:63`).
- **`additionalProperties:false` stays.** Drop-unknown handles the "model
  invented extra fields" case within the existing strict schema; we do not relax
  any tool definition.
- **Purpose parameter.** `purpose` is a declared property on every tool via
  `WithPurposeParameter`, so it is never dropped and never aliased.
- **Recovery gating.** `RecoverLeakedCalls` requires a known tool name, so prose
  that discusses a tool ("I'll use read_file next") is not misparsed as a call.

## Testing plan

Pristine output required: any test that intentionally triggers a repair or error
captures and asserts the resulting event/message.

- **`repair` package (unit, table-driven):**
  - `RepairJSON`: lone surrogate, broken `\uXXXX`, valid pair, no-op.
  - `RepairArgs` aliasing: `path→file_path` applies for `read_file`, no-ops for
    `list_dir`; each short-form alias; safe-rule negatives (target already
    present; alias is itself a declared property).
  - `RepairArgs` coercion: `"true"`→bool, `"5"`→int, scalar→array; non-numeric
    string left untouched.
  - `RepairArgs` drop-unknown: dropped only under `additionalProperties:false`;
    order interaction (alias-then-not-dropped).
  - `SuggestToolName`: close match within threshold, no match beyond threshold.
  - `ExplainSchemaError` / `ExplainJSONError`: expected coaching + example shape.
  - `RecoverLeakedCalls`: `<invoke>`, `<tool_call>` JSON, bare object; unknown
    tool name rejected; prose-only returns none.
- **`ExecuteCall` (integration, `agent/internal/tool`):** malformed JSON
  repaired → tool runs + `Repairs` populated; aliased arg runs; unknown key
  dropped and runs; unrepairable → model-tuned error; unknown tool →
  did-you-mean. Extends the existing
  `agent/session_openai_malformed_tool_call_test.go` pattern.
- **Session (end-to-end with a fake provider):** a response with a bare
  `<invoke>` block → recovered, executed, `EventToolCallRecovered` asserted; a
  repaired call → `EventToolCallRepaired` asserted with the right change kinds.

## Risks and open questions

- **Leaked-markup false positives.** Mitigated by requiring well-formed markup
  *and* a known tool name. If telemetry shows misfires, tighten to require the
  markup to be the whole response or a fenced block.
- **Alias table drift.** The table is a maintenance surface. Telemetry
  (`EventToolCallRepaired` alias counts) tells us which entries earn their keep
  and reveals new drift to add.
- **Coercion over-reach.** Kept conservative; the risk is coercing something the
  model meant literally. Only unambiguous scalar→declared-type conversions fire.

## Files touched

- **New:** `agent/internal/tool/repair/` (`repair.go`, `json.go`, `suggest.go`,
  `explain.go`, `recover.go` + tests).
- `agent/internal/tool/registry.go` — repair ladder in `ExecuteCall`;
  `Repairs` field on `ExecResult`; did-you-mean; model-tuned errors.
- `agent/session_tools.go` — emit `EventToolCallRepaired` in `execTool`.
- `agent/session_lifecycle.go` — leaked-markup recovery before
  `handleNoToolCalls`.
- `agent/events/` — `EventToolCallRepaired`, `EventToolCallRecovered` + data
  types.
- Tests as above.
```

