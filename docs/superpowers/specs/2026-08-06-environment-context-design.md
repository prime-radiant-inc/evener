# Environment Context: diff-rendered per-turn session state

Date: 2026-08-06
Status: approved design, pre-implementation

## Problem

The model learns the working directory, platform, and similar session facts
once, from the static system prompt (`prompts/sections/environment.md.tmpl`),
and never again. Anything that changes mid-session — a worktree switch, the
date rolling over, sandbox escalation, the machine coming under resource
pressure — is invisible to it. Injecting updates naively (a per-turn system
message) would rewrite the system block every provider adapter hoists to the
front of the prompt, invalidating the provider-side prompt cache for the whole
conversation on every turn.

Prompt caching is exact-prefix at every provider serf targets (Anthropic,
OpenAI chat + Responses, Gemini). The only cache-safe place for mutable
content is appended at the tail of the newest turn, and OpenAI Responses
continuation (`previous_response_id`) additionally makes appending the *only*
possible operation. The blessed pattern, per Anthropic's docs and OpenAI's own
Codex agent loop, is append-only context that is re-sent only when it changes
(Codex renders per-section diffs against the previous snapshot).

## Decisions (settled with Jesse, 2026-08-06)

- Diff-rendered environment context, Codex-style: emit only what changed.
- Sections in v1: cwd; local date+hour; sandbox mode; git branch;
  resource pressure (load, memory, disk) gated on high thresholds, probes
  rate-limited.
- Placement: a standalone user-role message appended immediately before the
  turn's real user input. No provider adapter changes.
- Package: `agent/internal/envctx`.
- Data model: a plain `Snapshot` struct + field-by-field diff (not a map, not
  a section interface). Compile-time completeness beats a generic loop at this
  size.
- Rendering: one outer `<environment_context>` tag; plain `label: value`
  lines inside, no inner XML. Path values are double-quoted; identifiers are
  bare; events (pressure clearing) are short sentences.
- OpenAI GPT-5.6+ explicit cache breakpoints (`prompt_cache_breakpoint` /
  `prompt_cache_options`) are a separate follow-up, not part of this feature.

## Design

### Package and types

`agent/internal/envctx`:

```go
type Snapshot struct {
    Cwd           string // absolute working directory
    LocalDateHour string // "2026-08-06 14:00 PDT" — hour granularity, local tz
    Sandbox       string // sandbox mode; always populated, "off" included
    GitBranch     string // current branch; "" outside a git repo
    Pressure      Pressure
}

type Pressure struct {
    Load, Memory, Disk string // human-readable warning; "" when nominal
}

type Collector struct { /* per-probe lastProbed timestamps, cached readings */ }
// Inputs carries the session-owned facts (cwd, sandbox mode); the Collector
// adds what it probes itself (clock, git, pressure).
func (c *Collector) Collect(in Inputs) Snapshot

type Tracker struct { last Snapshot; hasSent bool }
func (t *Tracker) RenderDiff(cur Snapshot) string // "" when nothing changed
```

`Snapshot` contains only strings, so it is comparable with `==` for the
nothing-changed fast path, and it marshals to JSON directly for persistence.

### Collection

- **cwd, sandbox**: read from the session's execution environment each turn.
  Cheap, no throttling.
- **date+hour**: local time truncated to the hour, with zone abbreviation.
  Changes at most hourly, so the diff naturally emits at most once per hour.
- **git branch**: current branch of the working directory, via the existing
  fast-path git helpers. Re-checked each turn; "" outside a repo.
- **pressure probes** (load average, memory pressure, disk fill on the working
  volume): each probe carries a `lastProbed` timestamp inside the Collector
  and re-probes at most every 5 minutes; between probes `Collect` reuses the
  cached reading. Thresholds are v1 constants — load > 2× cores, memory at
  the platform warn level, disk > 90% full — with no config surface until
  something needs tuning. A probe failure yields "" (nominal); probe errors
  are never surfaced to the model.

### Diff and rendering

`RenderDiff` compares field by field against `last`:

- First emission (`hasSent == false`): render every non-empty field,
  including cwd even though the system prompt states it — a few redundant
  tokens once per session buys an unambiguous anchor.
- Later emissions: render only changed fields.
- Pressure fields get the one special case: a transition from non-empty to
  empty renders a clear line ("memory pressure: back to normal") exactly
  once; empty→empty renders nothing.
- A changed-to-empty value on other fields still renders, with an explicit
  placeholder: sandbox is always populated ("off" included) so it never
  empties, and git branch transitioning to "" renders
  `git branch: (not in a git repository)`. A change must never be silent.
- Empty diff → empty string → no message that turn.

Format:

```
<environment_context>
cwd: "/Users/jesse/prime-radiant/toil-suite/serf/.worktrees/webui-workspace-shell"
git branch: worktree-webui-workspace-shell
memory pressure: back to normal
</environment_context>
```

The outer tag exists for attribution — it marks the block as harness-injected
rather than user speech, which every model family serf targets is trained to
respect. Deterministic field order (struct declaration order) keeps
transcripts and tests stable.

### Injection: a new turn kind

Follows the established `TurnSteering` pattern (`schema/turn.go`,
`expandHistory` in `session_model_call.go`): a new `schema.TurnEnvironment`
turn kind whose `Message` is the rendered block as a user-role message.

- At turn start, before appending the `USER_INPUT` turn, the session calls
  `Collect` + `RenderDiff`; a non-empty result is appended as a
  `TurnEnvironment` turn.
- `expandHistory` passes `TurnEnvironment` through as its user-role message
  (the steering case, minus mid-round deferral — envctx only ever lands at a
  turn boundary). It is **included** in model-bound history, unlike the
  presentational kinds (`MODEL_SWITCH`, `TURN_FAILURE`).
- Because it flows through `expandHistory`, both the full-history path and
  the Responses continuation delta path (`session_model_call.go:386`) carry
  it with no extra work, and replays reproduce the exact bytes sent — cached
  prefixes stay byte-stable across restarts.
- The TUI/web transcript renderers get a distinct kind to display as harness
  chrome instead of user speech.
- Mid-turn environment changes (e.g. the model switching worktrees via
  `manage_worktree`) wait for the next turn boundary; the model initiated
  those changes and needs no same-turn reminder.

The static `Working directory:` line in `environment.md.tmpl` stays; envctx
reports drift, the system prompt anchors session start.

### Persistence and resume

- The `TurnEnvironment` turn persists in the transcript like any other turn.
- The Tracker's `last` snapshot and `hasSent` are saved in session meta after
  each emission and restored on resume, so an unchanged environment stays
  silent across restarts.
- Missing meta (pre-feature sessions, corrupted state) → zero Tracker → one
  full re-emission. Append-only, therefore cache-safe; a few dozen tokens of
  noise at worst.

## Out of scope

- OpenAI explicit cache breakpoints (separate follow-up).
- Configurable thresholds, probe intervals, or section selection.
- Same-turn notification of mid-turn environment changes.
- Any provider adapter changes.

## Testing

TDD throughout:

- Table-driven `RenderDiff` unit tests: first emission renders all non-empty
  fields; single-field change renders one line; pressure appear / clear /
  steady-state; path quoting; empty diff renders "".
- Collector tests with a fake clock: probe throttling honors the 5-minute
  floor; cached readings served between probes; probe failure reads as
  nominal.
- `expandHistory` test: `TurnEnvironment` emits a user-role message in
  position, in both full-history and delta scopes.
- Session-level test: the environment message lands immediately before the
  user input turn, round-trips through transcript persistence, and resume
  with saved meta stays silent when nothing changed.
