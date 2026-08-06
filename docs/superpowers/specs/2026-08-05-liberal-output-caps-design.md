# Liberal Output Caps

Stop truncating model responses at an arbitrary 4096-token cap, and stop
misdiagnosing the truncation as a JSON problem when it does happen.

## Problem

Session 0342840HEyE9jiql6OxEeZ (Kimi K3 via the anthropic-format adapter) had
three tool calls fail with "arguments were not valid JSON (unexpected end of
JSON input)". The API log shows all three requests went out with
`max_tokens: 4096`, used exactly 4096 output tokens, and stopped with
`stop_reason: "max_tokens"` — the server cut the `write_file` argument stream
mid-string at ~15.7KB. The model then misread the invalid-JSON error as an
escaping problem and burned turns working around it with chunked writes and
heredocs.

Two defects:

1. `llm/providers/anthropic/request.go` hardcodes a 4096 default when the
   request sets no `MaxTokens`, and the agent loop never sets one (only the
   session namer does, intentionally). The openaicompat adapter already
   defaults its cap from the model catalog (`fillFromCatalog`); the anthropic
   adapter never got that treatment. Google's adapter omits the field when
   unset, which yields the provider's model max — already fine.
2. When a length-truncated tool call fails to parse, the prevalidation error
   describes a JSON syntax problem without mentioning that the response hit
   the output-token cap. The model can't self-correct from that message.

Truncation itself is server-side and cannot be disabled: `max_tokens` is a
required Messages-API parameter with no "unlimited" sentinel, and beyond it
sits the model's architectural output ceiling, which no request parameter
removes. The strongest available posture is to always request the model's
maximum and to name truncation accurately when it still happens.

## Design

### 1. Cap resolution: always request the model's maximum

Precedence for the effective `max_tokens`, resolved per request:

1. Explicit `MaxTokens` on the request — only intentional callers set this
   (e.g. the session namer's small cap).
2. Instance config: the model's `max_output_tokens` in `providers.toml`.
3. Embedded catalog: `ModelInfo.MaxOutputTokens`, guarded against junk data
   the same way openaicompat's `fillFromCatalog` is (a claimed cap that
   equals or exceeds the context window is ignored — input and output share
   the window, so it can't be real).
4. Liberal fallback: 32000. A model that can't do 32000 gets a loud 400,
   which is a catalog gap to fix, not a silent truncation.

Defense in depth — the resolution lives in two layers:

- **Adapter layer (anthropic):** replace the hardcoded 4096 with steps 3–4.
  The adapter mirrors openaicompat's catalog-default pattern. Google and
  openaicompat already behave liberally and are unchanged.
- **Agent layer:** the agent's model-call path fills `MaxTokens` from steps
  2–3 (instance config first, so per-instance overrides keep winning) before
  the request reaches any provider. A future adapter with a bad default
  can't reintroduce the bug.

### 2. Truncation-aware tool errors

Thread the turn's finish reason into `execTool` → `prepareToolCall`. When
tool-call arguments fail to parse AND the finish reason is
`llm.FinishReasonLength`:

- Skip `repair.RepairJSON`. A repair that "heals" truncated JSON by closing
  the open string would silently execute a truncated write — worse than
  failing.
- Emit a truncation-specific prevalidation error in place of the generic
  invalid-JSON one: the response hit the output-token limit before the
  arguments finished streaming, nothing was executed, and the model should
  emit smaller content per call (e.g. write the file in sections).

Valid JSON on a length-stopped turn executes normally — the truncation may
have landed after the tool call closed.

## Non-goals

- **No self-healing of truncated arguments.** The tail bytes are gone;
  nothing client-side can recover them.
- **No auto-continue of length-truncated responses.** A partial `tool_use`
  block cannot be prefilled and resumed — the API only accepts complete
  blocks. Text-only continuation is possible but rare once caps sit at model
  max, and silent stitching risks seams worse than a visible `length` stop.
  Revisit if telemetry shows it mattering.
- **No changes to the existing repair package's escaping/alias/coercion
  behavior.** It addresses real drift and is not implicated here.

## Testing

TDD throughout.

- Cap resolution, adapter layer: catalog hit; catalog silent → 32000;
  junk-data guard (cap ≥ context window → fallback); explicit request cap
  wins.
- Cap resolution, agent layer: instance config beats catalog; explicit
  caller cap untouched; resolution reaches the wire request.
- `prepareToolCall`: length finish + unparseable args → truncation message,
  no repair applied; length finish + valid args → executes; non-length
  finish + unparseable args → existing invalid-JSON path unchanged.
