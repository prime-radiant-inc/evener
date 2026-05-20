# Automatic Session Naming with `fast_cheap_model`

Date: 2026-05-20

## Summary

Serf should automatically assign concise names to sessions using a globally/project configurable fast cheap model. The naming operation is a single side LLM call, not a full agent session. The result is stored in session metadata and logged to the session log as advisory bookkeeping.

New launch setting:

```toml
fast_cheap_model = "openai/gpt-5-mini"
```

New daemon flag:

```sh
serf serve --fast-cheap-model openai/gpt-5-mini
```

The setting should feed the existing cheap-model path used by compaction and other cheap side calls.

## Goals

1. Generate a short display name for a session from the initial user prompt.
2. Refresh that name from compaction output when a better summary/checkpoint becomes available.
3. Configure the fast cheap model through existing launch settings layers and UI.
4. Implement naming as one cheap LLM call, not an agent/subagent/session.
5. Persist the generated name in session metadata.
6. Record the naming attempt in the session log as advisory metadata.
7. Preserve current behavior when naming is disabled, unavailable, or fails.

## Non-goals

- Do not create a new agent type for session naming.
- Do not run tools during naming.
- Do not expose naming as a user-visible assistant turn.
- Do not require generated names for old sessions.
- Do not remove or replace `OriginalPrompt`; it remains the fallback title/search text.
- Do not log full prompts or full compaction bodies in advisory entries.

## Launch configuration

### New field

Add `fast_cheap_model` to launch config layers.

Example:

```toml
model = "openai/gpt-5.5"
fast_cheap_model = "openai/gpt-5-mini"
```

### Layering

`fast_cheap_model` follows the same precedence model as `model`:

1. global launch config
2. in-repo `.serf/launch.toml`, if trusted
3. hub project launch config
4. per-launch overrides

The resolved effective value is passed to daemon launch args.

### Files and areas to update

- `internal/launchconfig/types.go`
  - add `FastCheapModel string `toml:"fast_cheap_model,omitempty"``
- launch merge/provenance code
  - merge as a scalar string field
  - provenance key: `fast_cheap_model`
- appwire launch config types
  - add `FastCheapModel` to the wire layer struct
- hub launch settings UI
  - add editable row in `cmd/serf-tui/launch_settings_panel.go`
- launch arg rendering
  - `internal/launchconfig/args.go` emits `--fast-cheap-model <value>`
- hub daemon spawn tests
  - expect the new flag when configured

## CLI / daemon behavior

### New flag

Add to `serf serve`:

```sh
--fast-cheap-model <provider/model-or-model-ref>
```

The flag should accept the same provider-qualified format as launch `model`, e.g.:

```sh
--fast-cheap-model openai/gpt-5-mini
--fast-cheap-model anthropic/claude-haiku-4-5-20251001
--fast-cheap-model ollama/qwen2.5-coder:7b
```

### Resolution

`--fast-cheap-model` should be resolved with the same model-ref parsing semantics as the main model where practical.

Expected behavior:

- If omitted, current provider default `CheapModel()` behavior remains unchanged.
- If present and same provider as the active profile, use the specified model as the cheap model.
- If present and provider-qualified for another provider, the cheap side call should use that provider if the existing client/profile architecture supports it.
- If cross-provider cheap calls are not currently supported cleanly, initial implementation may validate that the provider matches the active session provider and return a clear launch/config error otherwise. Do not silently ignore the provider.

### Profile/API shape

Preferred approach:

- Add an override field to profile/session config so `profile.CheapModel()` returns the resolved `fast_cheap_model` model name.
- Keep existing callers unchanged:
  - compaction
  - web fetch
  - checkpoint prediction
  - memory crystals
  - recursive distill
  - session namer

If provider switching for cheap calls requires broader architecture, represent the desired cheap model as a structured resolved model ref instead of squeezing provider into `CheapModel() string`.

## Session metadata

Extend `agent.SessionMeta` with optional fields:

```go
Name          string    `json:"name,omitempty"`
NameSource    string    `json:"name_source,omitempty"`
NameUpdatedAt time.Time `json:"name_updated_at,omitempty"`
```

Recommended source values:

- `prompt`
- `checkpoint`
- `summary`
- `manual` reserved for future user renaming

### Display fallback

Add a helper, either method or function:

```go
func SessionDisplayName(meta SessionMeta) string {
    if strings.TrimSpace(meta.Name) != "" {
        return strings.TrimSpace(meta.Name)
    }
    if strings.TrimSpace(meta.OriginalPrompt) != "" {
        return strings.TrimSpace(meta.OriginalPrompt)
    }
    return meta.ID
}
```

All UI/history code should use this helper rather than duplicating fallback logic.

### Backward compatibility

Old metadata files without `name` fields must continue to load normally.

Existing sessions display by `OriginalPrompt` until renamed by future compaction or other explicit migration behavior.

## Naming operation

### Shape

The session namer is a single side LLM call:

```go
resp, err := client.Complete(ctx, llm.Request{
    Model: profile.CheapModel(),
    Messages: []llm.Message{
        llm.System(sessionNamingSystemPrompt),
        llm.User(input),
    },
    MaxOutputTokens: 32,
})
```

It must not:

- create a `Session`
- create a subagent
- call tools
- append a normal assistant turn
- consume task-list flow

### Prompt contract

System prompt should be restrictive and deterministic. Example:

```text
Create a concise title for a coding-agent session.
Rules:
- 3 to 8 words
- no quotes
- no trailing punctuation
- single line only
- describe the user's task or session outcome
- do not mention "session", "conversation", or "assistant"
- do not include secrets
- avoid full file paths unless essential
Return only the title.
```

User input should include source and content:

```text
Source: initial prompt

<text>
```

or:

```text
Source: compaction summary

<text>
```

### Sanitization

After the LLM returns text:

1. trim whitespace
2. take the first non-empty line only
3. strip wrapping quotes/backticks
4. strip trailing punctuation if benign
5. reject if empty
6. reject if longer than a fixed limit, recommended 80 chars
7. reject generic titles such as:
   - `Untitled`
   - `New Session`
   - `Coding Task`
   - `Task`
8. reject strings containing newlines after sanitization

If rejected, do not change session metadata.

## Hook points

### Initial prompt naming

Trigger after the first real `TurnUserInput` is committed to session history/transcript.

Rules:

- Use only the first user task prompt, not steering/system reminders/tool output.
- If `SessionMeta.Name` is already set, skip.
- Run asynchronously with a bounded timeout.
- On success, set:
  - `Name`
  - `NameSource = "prompt"`
  - `NameUpdatedAt = now`
- Persist via existing metadata autosave path.
- Append advisory session-log entry.

### Compaction naming

Use the existing compaction callback in `agent/session.go`:

```go
s.contextMgr.OnCompactionTurn = func(t Turn) {
    // existing transcript append/reminder behavior
    ...

    if t.Kind == TurnCheckpoint || t.Kind == TurnSummary {
        s.maybeAutoNameFromCompactionTurn(t)
    }
}
```

Rules:

- `TurnSummary` source maps to `summary`.
- `TurnCheckpoint` source maps to `checkpoint`.
- Compaction-derived names may overwrite prompt-derived names.
- Do not overwrite `manual` names.
- Avoid older async prompt naming calls overwriting newer compaction-derived names.

Recommended implementation: keep a monotonic naming generation counter or compare source priority under `Session.mu`.

Source priority:

```text
manual > summary/checkpoint > prompt > empty
```

## Session log advisory entry

The naming call should be visible in the session log as advisory metadata.

### SessionLogEntry change

Extend `agent.SessionLogEntry` minimally:

```go
Kind string `json:"kind,omitempty"`
```

Semantics:

- empty or `action`: normal existing behavior
- `advisory`: side observation/bookkeeping, not an agent action

### Success entry

Example JSONL entry:

```json
{
  "kind": "advisory",
  "turn": 1,
  "action": "session_namer",
  "outcome": "success",
  "summary": "Suggested prompt-derived session name: Launch Config Cheap Model"
}
```

### Failure entry

```json
{
  "kind": "advisory",
  "turn": 1,
  "action": "session_namer",
  "outcome": "failure",
  "summary": "Session name generation failed; keeping fallback title",
  "failures": ["cheap model call failed: ..."]
}
```

### Privacy rule

Do not include the full original prompt or full compaction summary in the advisory entry. Include only:

- generated name
- source type
- success/failure
- concise error reason if failure

### Context injection

If `SessionLog.String()` is injected into model context, advisory entries should either:

1. render clearly as advisory, e.g.
   ```text
   Turn 1 [session_namer advisory] success: Suggested prompt-derived session name: Launch Config Cheap Model
   ```

or

2. be excluded from context injection.

Recommended: include them in persisted log, but exclude advisory entries from context injection unless there is a clear use case for the agent seeing naming bookkeeping.

## Hub display/search behavior

### Display

All session-list/history/thread display names should prefer:

1. `SessionMeta.Name`
2. `SessionMeta.OriginalPrompt`
3. session ID

Likely areas:

- `cmd/serf-hub/tree.go`
- past session rendering
- appwire thread previews if metadata is available

### Search

Past-session search should match both generated name and original prompt.

Update:

- in-memory search logic in `cmd/serf-hub/past.go`
- SQLite FTS schema/indexing

FTS should include a `name` column. Existing FTS index can be rebuilt. If schema migration is awkward, use a versioned FTS table name.

## Error handling

Naming failure must be non-fatal.

On any of these, keep fallback behavior and log advisory failure:

- no fast cheap model available
- model validation error
- LLM call timeout
- LLM provider error
- invalid/sanitized-empty title
- metadata save failure

The main session turn must continue unaffected.

## Timeouts and cancellation

- Naming calls should have a short timeout, recommended 10-20 seconds.
- Naming goroutines should respect session shutdown/close if there is an existing session context.
- Do not block user turn processing on naming completion.

## Tests

### Launch config tests

- `fast_cheap_model` parses from TOML.
- merge precedence matches `model`.
- provenance records `fast_cheap_model`.
- `ToArgs` emits `--fast-cheap-model <value>`.
- launch settings UI includes editable `fast_cheap_model` row.
- appwire round-trip preserves `FastCheapModel`.

### Serve/profile tests

- `serf serve --fast-cheap-model ...` sets the cheap model override.
- existing behavior remains when flag is omitted.
- invalid provider/model produces a clear error or diagnostic.

### Metadata tests

- old `meta.json` without name fields loads.
- new name fields persist.
- display helper falls back correctly.

### Namer tests

- initial prompt triggers exactly one cheap LLM call.
- generated name is sanitized and persisted.
- invalid generated name is rejected.
- LLM failure does not fail the session.
- advisory success/failure entries are appended.
- no full prompt is written to advisory log entry.

### Compaction tests

- `TurnSummary` updates prompt-derived name.
- `TurnCheckpoint` updates prompt-derived name.
- compaction name does not overwrite `manual` source.
- older prompt-naming goroutine cannot overwrite newer summary/checkpoint name.

### Hub tests

- display prefers generated name.
- search matches generated name.
- search still matches original prompt.
- old sessions with no generated name render as before.

## Implementation order

1. Add `fast_cheap_model` to launch config/appwire/TUI and emit `--fast-cheap-model`.
2. Add `serf serve --fast-cheap-model` and cheap-model override support.
3. Add session metadata name fields and display helper.
4. Add advisory `SessionLogEntry.Kind` handling.
5. Implement `agent/session_namer.go` as a single LLM call helper.
6. Hook initial user prompt naming.
7. Hook compaction turn naming.
8. Update hub display/search and FTS indexing.
9. Add tests across launch config, serve wiring, namer, session log, compaction, and hub history.
