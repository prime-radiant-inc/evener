# Issue #3728: eval collector records session-input completion

Status: frozen for PAR, 2026-10-25.

Base: `d85b5d7c9cb4cdac5b011595965129dc0f6f7c13`, a merge of
`origin/main` at `013b584c4179a3062c2ad02271e39cb953032763`.

## Goal and scope

`agent/eval.go` exports `evalMetrics.Completed` as JSON `completed`, initializes
it false, and never assigns it. Retained eval evidence therefore cannot tell an
observed successful input boundary from a run that ended another way or was
never observed to end. Define the metric from the terminal event the collector
really sees, assign it in `evalCollector.ProcessEvent`, and cover completion
against budget and infrastructure termination with deterministic tests.

This change touches `agent/eval.go`, `agent/eval_test.go`, the collector
sentence in `docs/developing-evener/agentic-testing.md`, and this spec. The
memory evaluation already copies `collector.Metrics()` into its retained stage
evidence; it needs no edit, because the populated field replaces the constant
false it retains today.

## Chosen meaning

`Completed` is true when the latest input terminal boundary the collector
observed was successful: an `EventSessionEnd` whose `SessionEndData.Reason` is
`input_complete`.

That meaning is grounded in the session lifecycle, not in a new event:

- `processOneInput`'s successful settle tail emits exactly one
  `EventSessionEnd{Reason: "input_complete"}` for the input, and `Close`'s
  `sessionEndEmitted` gate suppresses its later `session_closed` teardown.
- An input whose turn returns an error emits
  `EventSessionEnd{Reason: "turn_failed"}`, including a provider failure and
  budget exhaustion, unless the session is closing, in which case no input
  boundary is emitted. A known skill route that fails admission emits a turn
  failure event, settles idle, and returns a nil error, so it still reaches the
  successful `input_complete` tail; the memory evaluation's plain prompts do
  not use that route.
- An interrupted input emits `EventSessionEnd{Reason: "interrupted"}` when its
  interrupt marker is accepted and the session is not closing; otherwise the
  generic failure tail emits `turn_failed`, or the closing session emits no
  input boundary.
- `session_closed` describes session teardown, not an input outcome. It neither
  sets nor clears the field, so a forced terminal close cannot erase an
  already-observed successful boundary.

False therefore means "no successful input boundary was observed": the input's
turn returned an error, the input was interrupted, or it never reached a
terminal event. Conversely, a known skill route that fails admission still
reaches `input_complete`, so the field can be true even though a visible turn
failure was recorded. It is not a measured claim that the evaluated task was
unsolvable, and it is not task success.

`communicate(end_turn=true)` asks the session to end its turn. It is not
completion evidence and the collector must not read the tool call; only the
session's subsequent terminal boundary can set the field. Even when that
boundary arrives, the field records session-input completion, not that the eval
task was solved: the executable task checks and the retained tool chronology
remain the separate task evidence.

## Behavior

`evalCollector.ProcessEvent` handles `EventSessionEnd` carrying
`events.SessionEndData`:

- `input_complete` sets `Completed = true`.
- `turn_failed` and `interrupted` set `Completed = false`, so the latest input
  outcome wins when a trace holds more than one input.
- Any other reason, including `session_closed`, leaves the last observed input
  boundary unchanged.
- An untyped or missing payload is ignored.

`communicate(end_turn=true)` is an `EventCommunicate` with `EndTurn` set. It
must never set the field, with or without a later session boundary.

Every existing metric (`TurnCount`, token totals, compaction, fork summaries,
`TotalTokens`) behaves exactly as today.

The exported field's doc comment states this meaning and the false cases.

## Red-first tests in `agent/eval_test.go`

Deterministic event tests, all named `TestEvalCollector_*` so the focused
`-run 'Test.*(Eval|Collector|Completed)'` command executes every one:

1. `SESSION_START`, `ASSISTANT_TEXT_END`, `SESSION_END(input_complete)` →
   `Completed` true.
2. `SESSION_END(turn_failed)` after activity → false.
3. `SESSION_END(input_complete)` → true, then `SESSION_END(interrupted)` →
   false.
4. `SESSION_END(session_closed)` only, after activity → false.
5. No `SESSION_END` → false; an `EventSessionEnd` with a non-`SessionEndData`
   payload → false and no panic.
6. `input_complete` followed by `session_closed` → true (teardown ignored).
7. `input_complete` followed by `turn_failed` → false, and `turn_failed`
   followed by `input_complete` → true (latest input boundary wins).
8. `EventCommunicate{EndTurn: true}` and no `SESSION_END` → false.

Scripted-provider tests, also named `TestEvalCollector_*`, with no credentials
and no live calls:

9. A fake adapter returns a `communicate(end_turn=true)` response; the real
   `Session.ProcessInput` succeeds; the captured session events fed to the
   collector set `Completed` true. This is the retained stage shape named in the
   issue. Test 8, not this test, excludes a collector keyed on the tool call.
10. A fake adapter returns a non-communicate tool call with
   `MaxToolRoundsPerInput: 1`; `ProcessInput` returns typed tool-round budget
   exhaustion; the captured events leave `Completed` false.

Red check: every assertion that requires the field to become true (tests 1, 6,
7 and 9) fails against the current code, where the field is never assigned.
The clearing assertions (tests 2, 3, 4, 8 and 10) stay green until a success
has been observed, and remain as regression guards against turning those
endings into completion.

Both scripted sessions leave `SessionConfig.StateDir` empty, so the asynchronous
session namer cannot consume a scripted adapter step.

## Non-goals

No change to the session lifecycle, event vocabulary, `communicate` semantics,
memory-eval grading, or task-success interpretation. No invented event, no
tri-state JSON type change, no live provider call, no full local suite, no
labels.

## Verification and review

- Red-then-green: `go test ./agent -run 'Test.*(Eval|Collector|Completed)' -count=1`.
  For fast iteration, `go test ./agent -run '^TestEvalCollector_|^TestNewEvalCollector_' -count=1`
  selects only this change's tests; the broader command above is the required
  final check.
- Build: `make build`.
- PAR: two independent competing reviewers receive this frozen spec and source
  baseline, then the immutable implementation diff.
- simplify-code, one commit, branch RoboRev review, push, PR, settle command.
  CI owns full-suite verification.
