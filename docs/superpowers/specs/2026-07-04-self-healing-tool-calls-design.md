# Design: Self-healing tool calls

**Status:** Approved for spec review (revised after adversarial review)
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
surrogates). Ronacher's thesis: a harness either has to *prevent* those calls
(strict constrained decoding, with quality tradeoffs) or *absorb* them (a
forgiving harness that repairs the slop).

**Serf's stance is provider-dependent, and that is the key correction driving
this design.** Where serf lands on the prevent/absorb/reject spectrum depends
entirely on the wire protocol:

- **OpenAI Responses — "prevent" for the core work tools only.**
  `toResponsesTools` defaults `strict := true` for any tool whose `Strict` field
  is nil, then runs `strictifyJSONSchema` to force `additionalProperties:false`
  and an all-properties `required` list
  (`llm/providers/openai/responses.go:547-557`, `:579-600`). The core tools this
  design targets — `read_file`, `write_file`, `edit_file`, `shell`, `grep`,
  `glob` — set **no** `Strict` field (`agent/internal/tool/definitions.go`), so on
  this path they ship with constrained decoding: the model *cannot* emit
  `old_str`, `path`, or a stray `matchCase` for them here. **But nine tools do set
  `Strict: &strictFalse`** — `delegate`, `job_watch`, `job_status`,
  `job_read_output`, `job_list`, `communicate`, and the three transcript tools
  (`definitions.go:121,192,231,248,273,460,589,611,633`). Those ship to Responses
  *unstrict* (`responses.go:548-550` uses `*t.Strict`), so the model *can* drift
  on them even here — and `communicate` is the result tool, called on every turn
  under `ToolChoice: required` (`agent/session_model_call.go:622`). So Responses
  is in the "prevent" quadrant for the six core tools and in the "reject" quadrant
  for the nine non-strict tools.
- **Anthropic and OpenAI chat-completions — "reject."** The Anthropic adapter
  sends no strict flag; the chat-completions adapter (`ToChatTools`) sets no
  strict flag either. On these paths there is no constrained decoding, so an
  off-distribution call reaches serf and is **hard-rejected** by local
  `santhosh-tekuri/jsonschema` validation in `Registry.ExecuteCall`
  (`agent/internal/tool/registry.go:446`) — nearly every tool carries
  `additionalProperties:false` plus a `required` list. This is the worst-case
  quadrant: strict enough to reject, with no constrained decoding to prevent.

This design heals the **reject** quadrant: the whole tool surface on Anthropic
and OpenAI chat-completions (the provider family the article is about), plus the
nine non-strict tools on OpenAI Responses. On Responses the ladder is a lazy
no-op *only for the six strict core tools* — valid calls never reach it there —
but it is genuinely live for `communicate` and friends. The lazy trigger means
zero cost wherever calls are already valid, regardless of provider.

### Gap analysis: Claude Code trick → serf on the non-strict (Anthropic / chat) paths

| Claude Code trick | Serf today | Gap |
|---|---|---|
| Parameter aliasing (`old_str`→`old_string`, `path`→`file_path`) | Rejected — `additionalProperties:false` treats aliases as illegal extra keys | Serf is *stricter* than CC |
| Silent unknown-key filtering | Rejected with `additionalProperties "foo" not allowed` | Opposite behavior |
| Unicode escape / lone-surrogate repair | None | Missing |
| Silent type coercion | None at dispatch | Missing |
| Unknown-tool "did you mean" | `unknown tool: <name>`, no suggestion | Missing |
| Model-tuned error messages | Raw jsonschema error text passed through verbatim | Missing |

A `RepairToolCall` seam exists in the low-level `llm.Generate` loop
(`llm/generate.go:89`, invoked inside the loop's tool-execution path) but is
**unwired**: it is only ever set in tests, and the one agent caller of that loop
(session naming, `agent/session_namer.go:62`) uses `GenerateObject` with no tools,
so it can never fire. The primary agent path has no equivalent. This design builds
the equivalent on the primary path.

## Decisions (settled during brainstorming, refined after review)

1. **Direction: forgiving harness for the non-strict paths.** Adopt the
   self-healing tricks. Do **not** change any tool's `Strict` setting — in
   particular, leave OpenAI Responses' strict-by-default (`nil → true`) intact.
   Strict constrained decoding is out of scope; the article's "turn on strict"
   fix is already in effect on the one path where serf can apply it.
2. **Repair visibility: silent to the model, visible to telemetry.** A
   successfully repaired call runs and returns a normal result. The model's
   original raw arguments stay on the assistant turn in history (see Data flow);
   the model never sees the repaired args and gets no in-context note. Every
   repair emits a telemetry event so we can *measure* how often models drift off
   serf's schema. The metric serves honesty better than hiding the repair would.
3. **Scope: three capabilities in this spec** — argument normalization (the
   core), unknown-tool "did you mean", and model-tuned error messages.
   **Leaked-markup recovery is split to a fast-follow** (see the final section):
   review showed it requires rewriting already-committed history, which is far
   larger than the other three and independently sequenced.
4. **Architecture: repair orchestration lives in the session layer
   (`execTool`), before PreToolUse hooks.** Pure repair functions live in a new
   `repair` package. `execTool` owns orchestration because it — unlike
   `Registry.ExecuteCall`, which sits in the lower `tool` package — has the
   three things repair needs: it runs *before* the PreToolUse hooks (so hooks
   see healed args), it has the provider name-map (so messages name
   model-visible tools), and it has `s.emit` for telemetry.

## Architecture

### Why the session layer, not `ExecuteCall`

The obvious home is `Registry.ExecuteCall` (`registry.go:446`), where parsing and
validation already live. Adversarial review found three reasons that is the wrong
layer:

1. **Hook ordering.** PreToolUse hooks run in `execTool` at
   `agent/session_tools.go:254-294`, reading the raw arguments (`:259`) and
   optionally rewriting them (`:282`), *before* `ExecuteCall` runs at `:357`. If
   repair happened inside `ExecuteCall`, a policy hook would validate the
   *pre-repair* args and then repair could change them — e.g. a hook that gates
   `edit_file` on `file_path` sees `{path: "/etc/…"}`, allows it, and repair then
   aliases `path→file_path` to a location the hook never inspected. That breaks
   serf's invariant that hooks see the arguments that will actually run.
2. **Provider name-map.** `ExecuteCall` lives in the `tool` package and only has
   canonical registry keys. Under name-mapping profiles (Codex maps
   `glob→list_dir`, `shell→exec_command`; Gemini maps `list_dir→list_directory`),
   an unknown-tool suggestion or an "available tools" list built from canonical
   keys would name tools the model cannot call. Only the session layer has
   `providerToolName` / `providerVisibleToolNames` (`session_tools.go:223-244`).
3. **Telemetry.** `ExecuteCall` has no `s.emit`. `execTool` does.

So repair moves up one layer. `ExecuteCall` is **unchanged** and remains the
validating authority (belt-and-suspenders): a healed call passes its validation.

### The repair step in `execTool`

The repair step is a pure-ish helper `prepare(call)` inserted at the top of
`execTool`, after `abortIfClosing` (`session_tools.go:250`) and **before** the
PreToolUse hook block (`:253`). Crucially it **does not return out of
`execTool`** — it returns healed args (or a ready error string) so that the
existing lifecycle (PreToolUse → `EventToolCallStart` → exec-or-error →
`EventToolCallEnd` → PostToolUse) runs for *every* call, including unknown and
unrepairable ones. Review round two found that an early return would drop the
start/end events and PostToolUse for exactly the failing calls this design wants
to make visible.

```
prepare(call) -> (call, changes, prevalErr):
  # prevalErr, when set, is a model-tuned error string; the call still runs the
  # full hook + event lifecycle but skips ExecuteCall and returns prevalErr.

  if call.ID == "":
      call.ID = "call_" + shortHash(call.Arguments)   # stable ID BEFORE any rewrite
                                                       # (matches ExecuteCall's scheme, registry.go:449)
  t := reg.Get(call.Name)                              # EXISTING accessor (registry.go:390)
  if t == nil:
      visible := providerVisibleToolNames(reg.Names()) # Names() EXISTS (registry.go:429)
      return call, nil, unknownToolMessage(providerToolName(call.Name), visible)

  args := {}
  if len(bytes.TrimSpace(call.Arguments)) > 0:         # mirror ExecuteCall's empty-args
      if json.Unmarshal(call.Arguments, &args) != nil: #   handling (registry.go:462-470)
          repaired, c := repair.RepairJSON(call.Arguments); changes += c
          if json.Unmarshal(repaired, &args) != nil:
              return call, changes, repair.ExplainJSONError(providerToolName(call.Name), t.Definition.Parameters, ...)

  if t.Schema.Validate(args) != nil:
      args2, c := repair.RepairArgs(t.Definition.Parameters, args)
      if t.Schema.Validate(args2) != nil:
          return call, changes, repair.ExplainSchemaError(providerToolName(call.Name), t.Definition.Parameters, args2, ...)
      args = args2; changes += c

  if len(changes) > 0:
      call.Arguments = marshal(args)   # PreToolUse + start-event + ExecuteCall now see healed args
  return call, changes, nil
```

`execTool` then, at its existing sites: runs PreToolUse (on healed args); emits
`EventToolCallStart`; emits `EventToolCallRepaired` when `changes` is non-empty;
calls `ExecuteCall` **unless** `prevalErr` is set, in which case it builds the
error `ExecResult` directly (skipping `ExecuteCall`); emits `EventToolCallEnd`;
runs PostToolUse. Both the start and end events therefore carry the *healed* args
(the raw call lives only in history + the `EventToolCallRepaired` diff — see Data
flow).

**Empty arguments are common and valid:** streaming accumulators yield `[]byte("")`
when no argument text arrives, and tools with no required properties (`list_dir`,
`job_list`, the transcript tools) are legitimately called with none. `prepare`
treats empty/whitespace `call.Arguments` as `{}` exactly as `ExecuteCall` does, so
these calls validate and are never spuriously rejected.

The happy path pays one extra `json.Unmarshal` + `Schema.Validate` to decide
whether repair is needed (both cheap; the schema was compiled once at
registration). Valid calls skip repair entirely and are never mutated.

`repair` is a new leaf package under `agent/internal/tool/repair`. It depends only
on the standard library (and, for the fast-follow, `llm` for `ToolCallData`). It
has no dependency on `Session`, `Registry`, or the event bus — pure functions
over bytes and maps, unit-testable with zero agent scaffolding. There is no import
cycle: `tool → repair` and `agent → repair` are acyclic, and `repair` needs no
`tool` import.

### Data flow: history fidelity and telemetry

`emitAssistantResponse` appends the assistant turn holding the model's **raw**
tool call *before* `execToolBatch` runs (`agent/session_lifecycle.go:649` vs the
tool-exec path at `:715`). `execTool` rewrites only its working copy of the call.
Therefore:

- **History keeps the model's original arguments.** On the next request they
  replay under existing provider rules (OpenAI-family replays malformed args as
  `{}` via `ToolArgumentsString`, `llm/providers/internal/openaichat/openaichat.go:63`;
  Anthropic likewise). The model never sees the repaired args — the "silent to
  the model" contract.
- **Telemetry carries the diff.** `execTool` emits `EventToolCallRepaired` with
  the change list. This is the sole record that a repair happened, by design.

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
// parameter object. Applies, in order: alias → coerce → drop-unknown. Operates
// on TOP-LEVEL keys only. Returns a fresh map (never mutates the input) plus the
// changes made.
func RepairArgs(params map[string]any, args map[string]any) (map[string]any, []Change)
```

Three normalizers, applied in this order (the order is load-bearing):

### 1a. Parameter aliasing

A built-in table of off-distribution names → canonical names, applied per-tool
under a **safe-apply rule**:

> Rename `X → Y` for a given tool only if: `Y` is a declared property of that
> tool, `X` is *not* a declared property, `X` is present in `args`, and `Y` is
> absent from `args`.

The safe rule lets one global table stay correct across tools: `path → file_path`
fires for `read_file` / `edit_file` / `write_file` (which declare `file_path` and
not `path`) but is a silent no-op for `list_dir` / `grep` / `glob` (which declare
`path` natively — verified in `definitions.go`). No per-tool configuration.

Initial table, seeded from serf's *actual* parameter names plus Claude-Code names:

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
schema already matches Claude Code's exactly, so the residual edit-tool risk is
only the short forms above.

### 1b. Type coercion

For each key whose declared property schema has a scalar `type`:

- schema `boolean` + string `"true"` / `"false"` (case-insensitive) → bool
- schema `integer` / `number` + a numeric string (`"5"`, `"1.5"`) → **`float64`**
- schema `array` + a non-array scalar → single-element `[scalar]`

**Numbers must be produced as Go `float64`, not `int`.** Downstream argument
extractors type-assert `v.(float64)` (JSON's native type in a `map[string]any`) —
e.g. `optionalIntArg` (`agent/session_tools.go:795-804`) silently returns nil on
any other Go type. Coercing to `int` would pass schema validation yet be silently
dropped by the executor.

Conservative by construction: coercion fires only when the target type is
unambiguous. A non-numeric string against a `number` schema is left untouched so
it surfaces as a real (model-tuned) error.

### 1c. Drop-unknown

Only when the tool schema sets `additionalProperties:false`: remove any remaining
key that matches no declared property, recording each as `ChangeDropUnknown`.
This is Claude Code's "silently filter unexpected keys," except every dropped key
is recorded for telemetry. (`purpose` is injected as a real property by
`WithPurposeParameter` before schema compilation, `registry.go:281`,`:304`, so it
is a declared property and is never dropped.)

**Order rationale:** alias before drop, so `old_str` becomes `old_string`
instead of being discarded; coerce before drop, so a coercible key is never
dropped.

**Scope limit:** `RepairArgs` normalizes top-level keys only. A few tools carry
nested object/array schemas (`task_list.tasks[]`, `communicate.output`) whose item
schemas do not set `additionalProperties:false`; off-distribution keys nested
inside them are out of scope for this version.

## Component 1 (cont.): JSON repair (`repair.RepairJSON`)

```go
// RepairJSON makes unparseable tool-argument bytes parseable by fixing lone
// surrogates and broken \uXXXX escape sequences in string values. Returns
// (raw, nil) unchanged when it finds nothing to fix.
func RepairJSON(raw []byte) ([]byte, []Change)
```

Scope is deliberately narrow — the two failure modes the article names: unpaired
`\uD800`–`\uDFFF` escapes (replaced with U+FFFD, or combined when an adjacent
escape completes a valid pair) and a `\u` not followed by four hex digits. It does
**not** attempt general JSON slop repair (trailing commas, unquoted keys, single
quotes); those fall through to the model-tuned JSON error.

## Component 2: unknown-tool "did you mean"

Built in `execTool` (which has the name-map), replacing the reliance on the bare
`unknown tool: <name>` from `registry.go:457`.

```go
// SuggestToolName returns the closest name in available to requested, or ""
// if none is within the similarity threshold.
func SuggestToolName(requested string, available []string) string
```

Levenshtein distance, threshold `min(2, ceil(len(requested)/3))` where `requested`
is the model's tool name. `execTool` passes the **provider-visible** tool names
(`s.providerVisibleToolNames` over the registry's names) and the provider form of
the requested name, so both the suggestion and the capped available-tools list
name tools the model can actually call:

```
unknown tool: "reed_file". Did you mean "read_file"?
Available tools: read_file, write_file, edit_file, shell, grep, list_dir, ...
```

No new registry accessors are needed: `Registry.Get(name) *RegisteredTool`
(`registry.go:390`, `RLock`-guarded, returns the tool's `Schema` + `Definition`)
and `Registry.Names() []string` (`registry.go:429`) already exist. `prepare` uses
both directly.

## Component 3: model-tuned error messages

When repair fails, replace the terse library text with actionable coaching
derived from `t.Definition.Parameters`, built in `execTool` so the tool name is
the provider-visible form:

```go
func ExplainSchemaError(toolName string, params map[string]any, args map[string]any, verr error) string
func ExplainJSONError(toolName string, params map[string]any, parseErr error) string
```

Schema-error output leads with the actionable statement, then a minimal example
built from the required properties and their declared types:

```
edit_file: missing required argument "old_string" (string).
Required arguments: file_path (string), old_string (string), new_string (string).
Example: {"file_path": "...", "old_string": "...", "new_string": "..."}
```

**Implementation note:** naming the specific offending property requires walking
the `*jsonschema.ValidationError` tree from `santhosh-tekuri/jsonschema/v5`
(`t.Schema.Validate` returns it), not string-matching the flattened `%v` text at
`registry.go:473`. When the tree cannot pinpoint a single property, fall back to
listing all required properties + the raw detail. This extraction is the main
implementation risk in Component 3.

## Telemetry

One new event type on the existing `events` bus (`agent/events/events.go`),
emitted by `execTool` when the repair change list is non-empty:

- `EventToolCallRepaired` — `ToolCallRepairedData{ToolName, CallID, Changes []string}`,
  each `Change` encoded as `"kind:field:detail"`. Also written to the session log.

This yields the metric the article implies we should want: how often, and in
what way, models drift off serf's schema on the non-strict paths.

**Consumer updates required** (not just the type definition): `EventData` is a
sealed set and several consumers switch on event kinds with a `default:` that
silently drops unknown kinds. To surface the repair event they need a case:
`internal/appprojector/appwire_projection.go` (the hub/TUI projection; has a
`default:` at `:533`, so no crash, but no bubble without a case), `cmd/serf/run.go`
(the CLI renderer's tool-call switch), and `tools/tool-fluency/…` (the natural
place to *measure* the drift metric). `server/bridge.go` needs **no** change: it
records every event via `RecordAppEvent` before its state-only switch, so the
event already flows to the projector path above.

## Error handling and edge cases

- **No happy-path change.** Valid calls skip repair (one extra unmarshal +
  validate) and are never mutated; healthy behavior is unchanged.
- **Full lifecycle preserved.** Because `prepare` returns rather than short-
  circuiting `execTool`, unknown-tool and unrepairable calls still emit
  `EventToolCallStart`/`EventToolCallEnd` and run Pre/PostToolUse hooks exactly as
  today — only the *message* they carry improves. This keeps them visible to the
  UI projection and to `job_watch` `assistant.tool` error observers.
- **Hooks see healed args.** Repair runs before PreToolUse, preserving the
  invariant that hooks inspect the arguments that will run.
- **History fidelity vs telemetry args.** The model's original raw arguments
  remain on the assistant turn (`execTool` takes `call` by value, so rewriting
  `call.Arguments` cannot reach the appended turn); the model never sees the
  repair. Note the *telemetry* side differs deliberately: `EventToolCallStart`/
  `End` carry the healed args, and the raw call is reconstructable from history +
  the `EventToolCallRepaired` diff. Consumers treating the start event as "what
  the model literally emitted" must join against the repair event.
- **Stable callID.** `prepare` assigns the synthesized `call.ID`
  (`"call_"+shortHash`) *before* any argument rewrite, so start/end/tool_result
  identities agree even for the rare empty-ID path (Anthropic/OpenAI always send
  IDs; this covers the streaming-accumulator gap).
- **`additionalProperties:false` stays.** Drop-unknown handles invented-extra-key
  calls within the existing strict schema; no tool definition is relaxed and no
  `Strict` value is changed.
- **Concurrency.** `prepare` calls `providerToolName`/`providerVisibleToolNames`,
  which today read `s.profile` **unlocked** (`session_tools.go:228`), and
  `execTool` runs read-only batches in parallel goroutines
  (`session_tool_round.go:104-120`) while `SetModel` may swap `s.profile` under
  `s.mu`. The implementation must route these through `currentProfile()` (which
  locks, per its own contract at `session.go:433`) or snapshot the name-map once,
  to avoid a data race the current unlocked readers only avoid by being on a
  colder path.

## Testing plan

Pristine output required: any test that intentionally triggers a repair or error
captures and asserts the resulting event/message.

- **`repair` package (unit, table-driven):**
  - `RepairJSON`: lone surrogate, broken `\uXXXX`, valid pair, no-op.
  - `RepairArgs` aliasing: `path→file_path` applies for `read_file`, no-ops for
    `list_dir`/`grep`/`glob`; each short-form alias; safe-rule negatives.
  - `RepairArgs` coercion: `"true"`→bool, `"5"`→`float64` (assert Go type),
    scalar→array; non-numeric string left untouched.
  - `RepairArgs` drop-unknown: dropped only under `additionalProperties:false`;
    order interaction (aliased key not dropped).
  - `SuggestToolName`: close match within threshold, no match beyond threshold.
  - `ExplainSchemaError` / `ExplainJSONError`: expected coaching + example; the
    ValidationError-tree extraction path and its fallback.
- **`execTool` (integration, `agent` package, with a fake profile):**
  - malformed JSON repaired → tool runs, `EventToolCallRepaired` emitted;
  - aliased arg runs; unknown key dropped and runs;
  - **hook sees healed args** (a PreToolUse hook asserts `file_path` present after
    an `path` alias) — the regression guard for the review's hook-ordering finding;
  - **empty/whitespace args to a no-required-args tool succeed** (`list_dir`
    with `""`) — guard for the empty-args regression;
  - **unrepairable and unknown-tool calls still emit `EventToolCallStart` +
    `EventToolCallEnd`** and run PostToolUse — guard for the dropped-lifecycle
    finding;
  - unrepairable → model-tuned error; unknown tool under a **name-mapping
    profile** → suggestion/list uses provider-visible names (name-map guard).

## Fast-follow (separate spec): leaked tool-call markup recovery

Deferred out of this spec because adversarial review showed it is materially
harder than it first appears. When a response has assistant text but zero
structured tool calls, the text may contain leaked `<invoke>` / `<tool_call>`
markup. Recovering it is **not** just "parse the text and route the calls,"
because:

- `emitAssistantResponse` (`session_lifecycle.go:649`) has already appended the
  assistant turn as **text-only** — with no `ContentToolCall` parts — to both
  history and the transcript, *before* the `len(calls)==0` fork at `:672`.
- Routing synthesized calls through `execToolBatch` would append `tool_result`
  turns whose IDs match no `tool_use` block. The next request then serializes
  `[assistant(text), tool_result(id)]`, which **both** providers reject
  (OpenAI `function_call_output` with no matching `function_call` → 400;
  Anthropic `tool_result` with no preceding `tool_use` → 400). Serf's
  `repairOrphanedToolResults` (`agent/history_repair.go`) only fixes the opposite
  direction and cannot save this.
- Calls synthesized in that branch also bypass the `canonicalToolName` mapping
  loop at `:662-664`, so provider/CC-style names (`exec_command`, `Read`) would
  not resolve in the canonical-keyed registry.

A correct recovery must **rewrite the already-committed assistant turn** (history
*and* transcript) to carry `ContentToolCall` parts, strip the leaked markup,
canonicalize the recovered names, and then let them flow through the normal hook
+ repair-ladder path. That history-rewrite is the real work and earns its own
spec, sequenced after this one lands. `repair.RecoverLeakedCalls(text,
knownTools)` (a pure scanner returning `[]llm.ToolCallData`, gated on known tool
names to avoid false positives from prose) is the piece that carries forward.

## Files touched

- **New:** `agent/internal/tool/repair/` (`repair.go`, `json.go`, `suggest.go`,
  `explain.go` + tests).
- `agent/internal/tool/registry.go` — **no change**; `Get` and `Names` already
  exist and `ExecuteCall` stays as-is.
- `agent/session_tools.go` — `prepare` step in `execTool` before PreToolUse hooks
  (returning healed args or a model-tuned error, never short-circuiting the
  lifecycle); route the `prevalErr` result through the existing start/end/exec
  path; unknown-tool did-you-mean and model-tuned errors using provider-visible
  names; emit `EventToolCallRepaired`. Route `providerToolName` through a locked
  read (concurrency note above).
- `agent/events/events.go` — `EventToolCallRepaired` + `ToolCallRepairedData`.
- `internal/appprojector/appwire_projection.go`, `cmd/serf/run.go`,
  `tools/tool-fluency/…` — switch cases for the new event so it projects/renders
  and the drift metric is measurable. `server/bridge.go` needs no change.
- Tests as above.
