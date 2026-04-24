# Implementation Spec: `--output-schema` flag for serf

**Flag name rationale:** `--output-schema` matches Codex CLI's flag. Claude CLI uses `--json-schema` for the same concept; toil's claude_runner adapter translates from its single `OutputSchemaJSON` request field to whichever CLI flag the runner natively uses.

**Status:** Approved for implementation
**Related:** serf issue #2 (heuristic bug), toil issue in-flight

## Goal

Replace the brittle env-var + key-name heuristic path (`SERF_SUBMIT_RESULT_REQUIRED_DATA_KEYS` + `defCommunicateWithRequiredDataKeys`) with a new CLI flag `--output-schema <string>` that accepts a full JSON schema verbatim. The schema becomes the `communicate` tool's `output` field schema, exactly as supplied, with no inference. Ends the silent data-loss bug tracked in serf issue #2.

## 1. CLI flag specification

Add `--output-schema` to:

- **`cmd/serf/main.go`** (the top-level `serf run` command) — added to `flag.String`, wired into `runConfig.jsonSchema`, forwarded to `runConfig`.
- **`cmd/serf/serve.go`** (the `serf serve` subcommand) — added to its `fs.String`, passed into `SelectProfile`.

**Semantics:**

- Flag value: raw JSON string carrying a JSON Schema object (e.g. `--json-schema '{"type":"object","properties":{"plan":{"type":"string"}},"required":["plan"],"additionalProperties":false}'`).
- If absent or empty: `communicate.output` uses the default permissive shape from `defCommunicate()`. No-op.
- If present: parsed with `json.Unmarshal` into `map[string]any` at flag-parse time. On parse failure: the command exits 1 with `serf: --json-schema: invalid JSON: <err>` written to stderr.
- The parsed schema is NOT further validated (no JSON-Schema-draft validation). Provider adapters reject malformed schemas downstream; those errors surface to the user.
- Whitespace-only values are treated as absent.
- YAGNI: no `@/path/to/file` support. Inline only.

**Flag help text:**

```
--json-schema <json>  JSON Schema applied to the communicate tool's output field.
                      Replaces the default permissive schema. Inline JSON string.
```

## 2. New plumbing in `cmdutil`

**File: `cmdutil/cmdutil.go`**

Replace `SelectProfile` with a version that takes an optional schema JSON string and no longer reads the env var or applies key-name heuristics.

New signature:

```go
func SelectProfile(provider, model, outputSchemaJSON string) (agent.ProviderProfile, error)
```

Implementation:

- Parse `outputSchemaJSON` (if non-empty after `strings.TrimSpace`) into `map[string]any`. On parse error, return `fmt.Errorf("invalid --json-schema: %w", err)`.
- Build the raw provider profile via the existing `NewOpenAIProfile` / `NewAnthropicProfile` / `NewGeminiProfile` / etc. dispatch.
- If `outputSchemaJSON` was non-empty: wrap via the new `agent.WithCommunicateOutputSchema(p, parsedMap)`.
- `WithAllowedDecisions` still applies afterward — `SERF_ALLOWED_DECISIONS` is untouched by this change.

Delete the env-var fallback block and the helper `parseCommunicateRequiredDataKeys` — nothing else should call it. Delete `WithCommunicateRequiredDataKeys` wrapping from every provider branch.

Every caller of `SelectProfile` must update to the new three-arg signature:

- `cmd/serf/run.go` — pass `cfg.jsonSchema`.
- `cmd/serf/serve.go` — pass `*jsonSchema`.
- Any other internal callers (confirm none via `grep -rn "SelectProfile(" serf/`).

## 3. New function: `WithCommunicateOutputSchema`

**File: `agent/profile_overrides.go`**

Add:

```go
// WithCommunicateOutputSchema returns a cloned profile whose `communicate` tool
// has its `output` property schema replaced wholesale by the given schema.
// Passing nil or an empty map returns p unchanged.
func WithCommunicateOutputSchema(p ProviderProfile, outputSchema map[string]any) ProviderProfile
```

Implementation pattern mirrors `WithAllowedDecisions`:

1. Guard on `p == nil` or `len(outputSchema) == 0` → return p.
2. Type-switch on `*baseProfile` vs `*anthropicProfile` to obtain `bp`. For any other concrete type, return p (same fallback as existing overrides).
3. Clone the profile shallowly, clone its `toolDefs` slice.
4. For each `toolDef` named `communicate`: deep-copy its `Parameters` via JSON round-trip (same technique as `addDecisionToSchema`, to avoid shared-map mutation), then overwrite `Parameters["properties"]["output"]` with a **deep copy** of the provided `outputSchema`, and ensure `"output"` appears in `Parameters["required"]` (the top-level required list of the tool's parameters).
5. Preserve the surrounding structure: `message`, `await_reply`, and `output` remain the three top-level properties of `communicate`; only `output`'s schema is replaced.
6. Return the correctly-typed clone (same wrapper logic as existing functions).

**Why replace the whole `output` schema rather than just `output.data`:** the goal is a single explicit contract. The caller (toil) decides whether to include `message`, `artifacts`, `data`, or a completely different shape. Serf no longer opines.

**Strict-mode interaction:** The OpenAI adapter's `strictifyJSONSchemaInPlace` already walks the whole `Parameters` tree and injects `additionalProperties: false` and fully-populated `required` on every object. Any user-supplied schema flows through this pass untouched in structure. If the user supplies `additionalProperties: true`, strictify will overwrite to `false` — call this out in the flag help. Users who need lax object shapes should use `additionalProperties: false` + enumerated properties.

## 4. Per-provider path to structured output

No per-provider adapter changes are needed. The `communicate` tool's `Parameters` map already flows into each provider's tool-definition encoding:

- **OpenAI** (`llm/providers/openai/adapter.go:toResponsesTools`): passes `t.Parameters` as `parameters`, then `strictifyJSONSchema` strictifies the whole tree when `Strict != false`.
- **Anthropic** (`llm/providers/anthropic/adapter.go:toAnthropicTools`): passes `t.Parameters` as `input_schema`, strips `anyOf`/`oneOf`/`allOf` at top level only.
- **Google** (`llm/providers/google/adapter.go:toGeminiFunctionDecls`): passes `sanitizeGeminiSchema(t.Parameters)` — recursively removes `additionalProperties`, which is fine.
- **OpenAI-compat** and **openrouter-anthropic** reuse OpenAI and Anthropic shapes respectively.

The `communicate` tool ships with `Strict: &strictFalse` today. **Do not change this.** Leaving strict=false means user schemas go through unchanged on OpenAI. If we ever want to flip `Strict`, it's a follow-up — not this change.

## 5. Tests

**File: `cmdutil/cmdutil_test.go`** — add:

- `TestSelectProfile_NoSchema` — `SelectProfile("openai", "gpt-5.2", "")` succeeds; returned profile's `communicate` tool has the default permissive `output` subschema.
- `TestSelectProfile_ValidSchema` — pass a valid schema, verify the `communicate` tool's `output` schema matches.
- `TestSelectProfile_InvalidJSON` — pass `"{not json"`, expect error whose message contains `invalid --json-schema`.
- `TestSelectProfile_WhitespaceOnly` — pass `"   "`, expect behavior identical to empty string (no override).

**File: `agent/profile_overrides_test.go`** — add:

- `TestWithCommunicateOutputSchema_ReplacesOutput` — build an OpenAI profile; supply a schema; verify `output` was replaced.
- `TestWithCommunicateOutputSchema_MakesOutputRequired` — verify `Parameters["required"]` contains `"output"`.
- `TestWithCommunicateOutputSchema_NilOrEmpty_NoOp` — nil map and empty map both return the profile unchanged.
- `TestWithCommunicateOutputSchema_Anthropic` — same as above but with `NewAnthropicProfile`, verify the returned type is `*anthropicProfile`.
- `TestWithCommunicateOutputSchema_WithAllowedDecisions_StackOrder` — apply schema then decisions; verify `decision` got added into the user-supplied `output.properties` (since `addDecisionToSchema` mutates `output.properties`). Document the stacking order in a code comment.

**File: `cmd/serf/main_test.go`** / `run_test.go` — one black-box test that invokes main-style argument parsing with `--json-schema '...'` and asserts `cfg.jsonSchema` threads through to `run(cfg)` correctly.

## 6. What gets deleted

**`cmdutil/cmdutil.go`:**
- Env-var read of `SERF_SUBMIT_RESULT_REQUIRED_DATA_KEYS` / `SERF_COMMUNICATE_REQUIRED_DATA_KEYS`.
- `parseCommunicateRequiredDataKeys` function.
- All `agent.WithCommunicateRequiredDataKeys(...)` calls in every provider branch.

**`agent/profile_overrides.go`:**
- `WithCommunicateRequiredDataKeys` function.
- `defCommunicateWithRequiredDataKeys` function.
- `stringArraySchema`, `componentsSchema`, `tasksSchema` — only called from `defCommunicateWithRequiredDataKeys`; re-grep to confirm zero external callers before deleting.

**`agent/profile_overrides_test.go` + `agent/profile_test.go`:**
- `TestWithCommunicateRequiredDataKeys_AddsRequiredKeysToSchema`
- `TestWithCommunicateRequiredDataKeys_PlanDocIsString`
- `TestWithCommunicateRequiredDataKeys_TasksSchemaHasItems`
- `TestWithCommunicateRequiredDataKeys_StoryResultsIsObject`
- `TestWithAllowedDecisions_WithRequiredDataKeys_BothApplied` — replace with `TestWithAllowedDecisions_WithOutputSchema_BothApplied`.
- `TestAnthropicProfile_WithCommunicateRequiredDataKeys`.

**Env vars:** `SERF_SUBMIT_RESULT_REQUIRED_DATA_KEYS` and `SERF_COMMUNICATE_REQUIRED_DATA_KEYS` — no longer read anywhere. No deprecation shim. No warning on set. Clean removal.

## 7. User-facing doc updates

**`cmd/serf/main.go` Usage block:** add `--json-schema <json>` under Options.

**`README.md`:**
- Add `--json-schema <json>` to the flags table.
- Add a short paragraph under a new "Structured output" subsection with one working example that matches the Claude-CLI style.
- Remove any `SERF_SUBMIT_RESULT_REQUIRED_DATA_KEYS` references.

## 8. Risks and edge cases

- **Schema mutation by `strictifyJSONSchema`:** OpenAI adapter rewrites `required` to include every key in `properties`. Users who supply a schema with optional fields will see them become required on OpenAI. Document this in the flag help.
- **Anthropic top-level `anyOf`/`oneOf`/`allOf` strip:** if a user-supplied schema's top-level `output` schema uses a combinator, Anthropic's adapter strips it. Note this in the flag help.
- **Gemini sanitization:** `additionalProperties` is silently dropped. Not a correctness bug.
- **Double-application risk:** If a future caller applies `WithCommunicateOutputSchema` twice, the second call silently replaces the first. The function comment should say so.

## Critical Files for Implementation

- `/Users/jesse/prime-radiant/toil-suite/serf/cmd/serf/main.go`
- `/Users/jesse/prime-radiant/toil-suite/serf/cmd/serf/run.go`
- `/Users/jesse/prime-radiant/toil-suite/serf/cmd/serf/serve.go`
- `/Users/jesse/prime-radiant/toil-suite/serf/cmdutil/cmdutil.go`
- `/Users/jesse/prime-radiant/toil-suite/serf/agent/profile_overrides.go`
