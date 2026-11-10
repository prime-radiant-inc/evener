# Issue #4922: the system prompt and job-control guide must not urge `detached` where the environment refuses it

Status: frozen for review, 2026-10-25. The issue's acceptance criteria authorize this bounded correction.

## The mismatch

`agent/prompts/system.md.tmpl` tells every session, sandboxed or not:

> Start a process the task wants left running, such as a server, in `detached` mode.

A sandboxed `LocalExecutionEnvironment` with a provisioned wrapper answers
`DetachSupported() == false`, and `DetachCommand` refuses on exactly that
predicate with `ErrDetachUnsupported` (`agent/execenv/local.go`, pinned by
`TestDetachSupportedMatchesDetachCommandsRefusal`). The #4422 change already made
the shell schema follow the same answer: `Session.sessionShellDefinition` calls
`tool.DefShellWithDetach(detachSupportedBy(s.env))`, so a sandboxed session's
`mode` enum omits `detached` and its mode description says the environment cannot
disown a process. The prompt was outside that change's file scope, so the two
model-facing surfaces still disagree: the schema withholds the mode while the
prompt tells the model to use it.

`docs/job-control.md` carries the same unconditional posture in prose and in its
decision tables (the lifetime table, the default model-facing posture, the
model-facing guidance requirement, the wait-primitive table, the shell tool's
defaults and `mode="detached"` description, and the foreground-wait bounds).
Those statements are true where the environment can detach; they do not say when
it cannot.

## Contract

### System prompt

`promptData` gains `DetachSupported bool`, set in `buildPromptData` from
`detachSupportedBy(env)` — the same helper the shell schema reads, with the
already-resolved environment the render path holds, never `currentEnv()` (the
render path holds `s.mu`). The dataset is the `execenv.DetachSupportReporter`
answer: false when the environment does not report, false when a wrapper is
provisioned, and false on a platform without detached-process attributes.

The Background work paragraph keeps its current sentence, byte for byte, for a
detach-capable session. A session that cannot detach gets the same actionable
structure with modes it can actually call:

> This environment cannot disown a process, so the shell tool offers no `detached` mode and refuses a call that asks for one. Start a process the task wants left running, such as a server, in `background` mode, which runs it as a session-owned job, or in the foreground when you need its output before continuing. Nothing can outlive this session. Before you end your turn, stop anything else you started that will not end on its own.

No other prompt section changes. The shell schema, the
`LocalExecutionEnvironment.DetachSupported` capability, `DetachCommand`,
wrapper provisioning, and the drain escalation are untouched.

### Job-control guide

Every place in `docs/job-control.md` that recommends or describes choosing
detached mode is conditioned on the environment's detach capability: the
lifetime table's detached row, the "keep application-owned logs" note, the
default model-facing posture, the model-facing guidance requirement, the
wait-primitive decision table row, the shell tool's defaults and
`mode="detached"` entry, and the foreground-wait bounds. Each keeps the true
statement for a capable environment and states the limitation where it applies:
a sandboxed environment with a wrapper refuses the mode with
`ErrDetachUnsupported`, the model-facing schema omits it, and the session should
use a session-owned background job instead, accepting that the process stops
with the session. Two further detached mentions are factual and stay as they
are: a detached process has no managed job record, and detached execution
bypasses the job runtime timer.

## Pinning

The Background work section is pinned whole with `agenttest.CheckGolden` in the
existing detached-shell prose golden test (`agent/detached_shell_prose_test.go`,
`testdata/detachedshell/`), one golden for a detach-capable delegate and one for
a sandboxed (wrapper-bearing) explorer delegate. Both cases render through the
real session path (`buildPromptDelegate` → `renderSystemPrompt`); the section is
extracted by its heading, not asserted by substring. The fixture pins
`envInfo.Resources` so the section's resource-caps paragraph renders on every
host (caps are observable only on some), and the capable case skips on a host
that cannot disown a process. Regenerate with the existing `-update-prompt` flag
and read the diff. No test asserts the new sentence as a hardcoded phrase.

`TestPromptDataDetachCapability` proves the wiring structurally: the sandboxed
explorer delegate's environment has a provisioned wrapper and reports no detach
support, and `promptData.DetachSupported` matches the plain delegate
environment's own `DetachSupportReporter` answer (the capability itself is
pinned by `agent/execenv`'s `TestDetachSupportedMatchesDetachCommandsRefusal`).

## Scope and non-goals

Allowed changes are this spec, `agent/prompt_data.go`, `agent/session_prompts.go`,
`agent/prompts/system.md.tmpl`, `agent/detached_shell_prose_test.go`,
`agent/system_prompt_test.go`, the new `agent/testdata/detachedshell` goldens,
and `docs/job-control.md`.

Non-goals:

- No change to `DetachSupported`, `DetachCommand`, wrapper provisioning, sandbox
  policy, or detached-process lifetime.
- No change to the shell schema or its mode descriptions from #4422.
- No change to the drain escalation's detached guidance
  (`agent/session_jobtree_drain.go`), which already branches on the capability.
- No new prose test that asserts a phrase; no labels; no merge.

## Verification

Red: add the two section goldens and generate them before the template change.
The sandboxed golden records the unconditional "in `detached` mode" sentence;
after the template change, `go test ./agent -run 'TestDetachedShellProseGoldens$'`
fails on that golden with the removed sentence in the diff. Regenerate with
`-update-prompt`, confirm the sandboxed diff replaces it with the foreground /
background guidance, and confirm the capable golden is byte-identical.

Green commands:

- `go test ./agent -run 'TestDetachedShellProseGoldens$|TestPromptDataDetachCapability$|TestPromptDataTypedInputs$|TestSystemPromptRendersForEveryConfiguration$' -count=1`
- `make docs-check` — a site-build guard: the guide is in
  `docs-site/publication.json`, so the target builds it and validates its links.
  It does not inspect the conditioned prose; the prose change is verified by
  diff review.
- `make build`

## Review process

Freeze this spec and review it with two independent competing PAR reviewers;
resolve legitimate findings. Review the implementation diff with another
two-reviewer PAR round, run simplify-code, commit, run the branch RoboRev
review, then push and open the PR. Fetch and merge current main before the final
push if it moved. Do not merge; the parent owns the CI wait.
