# Session auto-naming

Evener gives a session a short human-readable title so a human can recognize it
in the sidebar, in history, and in search. A title is advisory decoration, not
part of the conversation: generating one never blocks, fails, or alters a turn.
The implementation lives in `agent/session_namer.go`.

## What generates a name

One structured-output LLM call, not an agent and not a subagent. The call is
handed a single source of text and returns JSON matching a small schema:

```json
{ "name": "Fix Flaky Parser Test" }
```

The system prompt asks for the session's overarching goal in 2–6 words, at most
60 characters, Title Case, with no ending punctuation and no quotes or markdown.
The schema enforces `{name: string, minLength: 1, maxLength: 60}` and forbids
additional properties, so a malformed reply is rejected rather than persisted.
The generated title is then sanitized (`sanitizeSessionName`, same file): trimmed,
collapsed to one line, stripped of wrapping quotes and trailing punctuation, and
truncated at the 60-character cap.

## When naming runs

Two triggers, both asynchronous, both launched with `sendersWG` so a closing
session waits for them:

- **Initial prompt.** The first accepted user input launches the namer
  (`launchInitialPromptNamer`, called from `acceptUserInput`). It runs only while
  the session has no name and no naming attempt is already in flight.
- **Compaction refresh.** A `SUMMARY` or `CHECKPOINT` turn launches a refresh
  (`launchCompactionNamerGated`, from the compaction-turn effect). The existing
  title is passed into the prompt so the model keeps a goal-level title unless
  the checkpoint shows the goal changed. A refresh launched before a newer fold
  published is dropped at completion, so a late async namer cannot overwrite a
  name derived from newer history.

Steering messages, system reminders, and tool output never trigger naming. A
subagent (`IsSubagent`) has its task brief as its original prompt and names from
that brief the same way a root session names from its first prompt.

## Model selection

The namer prefers the configured **fast/cheap model** and otherwise falls back to
the session's active model:

- `fast_cheap_model` in launch config, or `evener --fast-cheap-model` /
  `evener serve --fast-cheap-model <provider/model-or-model>`.
- With no cheap model configured, naming uses the active provider and model. That
  fallback is intentional: naming still happens, it just is not moved to a
  cheaper model.
- A provider-qualified cheap ref (for example `anthropic/claude-haiku-4-5`) routes
  the naming call to that provider rather than the session's provider.

## Request policy

Naming is small and deterministic, so the call is shaped to keep it that way and
to survive models that reason:

- **Reasoning is asked off.** The call carries `ReasoningEffort: "none"`. Request
  shaping sends an explicit off only for a model whose catalog row lists an off
  level and omits the control otherwise; it never turns reasoning on against the
  request. This matters because a reasoning model that spends a small
  output-token budget on chain-of-thought emits **no content at all**, which is
  indistinguishable from a provider failure.
- **No artificial output-token cap.** The title's length is bounded by the prompt
  and by the schema's `maxLength`, not by a token budget. A cap starves any model
  that cannot disable reasoning; a model that can disable it does not need one.
- **Temperature 0 when the catalog vouches for it** (`sessionNamerTemperature`).
  Temperature is omitted when the resolved row cannot confirm support, because a
  rejected parameter costs one dead request per session (issue #834). If a row
  vouches and the provider still rejects it, the call is retried once with the
  parameter dropped.
- **A short 15-second deadline**, and a retry policy that opts out of the
  turn-level rate-limit wall budget so naming never competes with a real turn for
  the same provider bucket.

## Precedence and user renames

Sources rank `user` > `compaction` > `prompt`:

- A user rename via `evener/thread/name/set` (hub RPC `ThreadNameSet`) sets
  `NameSource = "user"` and is never overwritten by either auto-namer path.
- A compaction refresh may replace a prompt-derived or earlier compaction-derived
  name; it may not replace a user name.
- Once a session has any name, the initial-prompt namer does not run again.

Names are stored in session metadata as `name`, `name_source`, and
`name_updated_at`, saved through the normal metadata autosave path.

## Display fallback

`schema.SessionDisplayName` resolves a session's display string in this order:

1. `name` (generated or user-chosen),
2. `original_prompt`,
3. session ID.

A delegate's `original_prompt` is its task brief, so an unnamed subagent row falls
back to that brief. UI code should use the helper rather than re-deriving the
fallback.

## Failure handling

Naming is best-effort and never fails a session. Each attempt is recorded in the
session log as an `advisory` entry with `action: "session_namer"` and an
`outcome` of `success` or `failure`; the full prompt is never written to the log.

On failure the session keeps its previous title (if any) and remains eligible to
try again on the next user input. A quota-exhausted error
(`suppressSessionNamerIfQuotaExhausted`) disables naming for the rest of the
session to avoid re-dispatching a spent allowance every turn; changing the
session's model clears that latch, since the allowance belongs to the provider,
not the session.

## See also

- `agent/session_namer.go` — generation, sanitization, triggers, precedence.
- `agent/schema/snapshot.go` — `SessionDisplayName` and the metadata fields.
- `llm/generate_object.go` — the structured-output call the namer uses.
- `llm/registry_shape.go` — request shaping (reasoning, sampling, token caps).
