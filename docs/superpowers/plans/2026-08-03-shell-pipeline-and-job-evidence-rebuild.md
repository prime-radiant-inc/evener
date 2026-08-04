# Shell Pipeline and Job Evidence Rebuild Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Make shell pipeline status trustworthy on every supported POSIX execution path, and make the agent guidance rely on Serf's existing durable job evidence instead of a fragile output marker.

**Architecture:** The local execution environment will select only a shell that supports the explicitly requested `pipefail` option. It will never fall back to `/bin/sh` while still passing Bash-specific options; if Bash is unavailable, process startup will fail explicitly. Shell and job instructions will describe the existing automatic output retention, completion notification, exit status, and `read_transcript` path. An external `tee` artifact remains an optional user-directed escape hatch, not a completion protocol.

**Tech Stack:** Go, `os/exec`, POSIX shell behavior, Serf shell/job tools, Markdown prompt sections.

## Global Constraints

- Preserve Windows `cmd.exe` behavior.
- Preserve the caller's effective `PATH` when looking for Bash.
- Keep the implementation small and avoid changing the command-runtime interface unless the existing interface cannot express the contract.
- Test real shell/job behavior; do not add tests that assert prompt wording or rendered command strings.
- Do not execute tests until a Sol subagent has reviewed this plan and the exact implementation diff.

## Task 1: Establish a regression test for shell selection

- Update the execution-environment test to verify that POSIX shell commands select Bash with `-o pipefail` and do not select `/bin/sh` as a fallback.
- Keep the existing behavioral pipeline test for a failing first stage followed by `tail`, plus a clean pipeline, so the reported exit status is verified end to end.
- Add a behavioral background-job test with output that has no trailing newline; assert automatic completion status/exit code and successful `read_transcript` retrieval without a marker.
- Make the test use the existing executable/stat seams where possible and keep it valid on Windows by skipping POSIX-only assertions.

## Task 2: Fix the POSIX shell resolver

- Resolve `/bin/bash` first, then a `bash` executable through the inherited effective `PATH`.
- If no Bash executable is available, retain an explicit startup failure rather than invoking a shell that cannot honor `pipefail`.
- Keep `cmd.exe` unchanged on Windows.
- Update comments and nearby tests to state the invariant: a POSIX invocation that asks for `pipefail` must run under Bash.
- Surface the actual shell startup error in the immediate tool result; a missing Bash must not become an empty `start_failed` response.

## Task 3: Replace marker-based job guidance

- Remove the default `EXIT=` plus `job_watch(output_match=...)` recipe from the workflow prompt.
- Explain that background shell jobs are automatically logged, completion notifications carry status/exit code, and retained output can be read with `read_transcript(transcript_ref="job:<job_id>")`.
- Explain that `job_watch` is for genuine intermediate readiness conditions, not ordinary completion.
- Keep an optional absolute-path `tee` example only for preserving an external full artifact beyond retained output; qualify `SERF_SCRATCH_DIR` because it is not present in every execution environment, recommend the delegate's scratch directory when available, and require reporting the artifact path to the parent.
- Update the shell tool definition consistently without adding prompt-string tests.

## Task 4: Sol review gate

- Give a Sol subagent this plan and the complete uncommitted diff.
- Require a read-only, adversarial review of correctness, portability, test quality, and prompt/tool-contract consistency.
- Do not run tests until Sol approves the current diff; if the diff changes after review, obtain a follow-up review of the changed diff before testing.

## Task 5: Verification and handoff

- Run the focused execution-environment and shell/job tests first, including the visible launch-failure diagnostic and the actual terminal notification callback.
- Run the relevant agent package suite and formatting/lint gates.
- Run the repository suite and distinguish unrelated pre-existing failures from regressions.
- Commit the rebuilt fix with the plan and implementation evidence, then report exact commands and results.
