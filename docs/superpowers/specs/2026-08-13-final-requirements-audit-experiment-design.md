# Final Requirements Audit Experiment

## Goal

Test one runtime-scaffolding hypothesis: a non-interactive root agent that has
completed its own task list may catch an unmet requirement if Serf gives it one
final, explicit verification task containing the original assignment verbatim.

This is an experiment, not a proposed product change. It must isolate this one
intervention and preserve enough evidence to explain either result.

## Evidence and Hypothesis

In the archived Terminal-Bench 2.1 Luna run, all ten non-timeout semantic
failures that used a root task list marked every root task done before
finishing. A final task would therefore have run in the cohort whose validation
failures motivated this experiment.

The hypothesis is:

> After completing a nonempty task list, a root agent that manually checks the
> original assignment once more will detect and correct at least some concrete
> requirement mismatches before delivering its result.

The first real target is `sqlite-with-gcov`. Its failed run completed all four
root tasks but left coverage artifacts under `/app/sqlite-build`, despite the
assignment requiring SQLite to be compiled in `/app/sqlite`. This is a direct,
manually checkable requirement mismatch rather than a timeout, provider error,
or hidden-domain failure.

## Treatment

The treatment applies only to fresh root sessions with `NonInteractive` set.
Interactive sessions and subagent sessions are unchanged.

When the model first calls the result tool with `end_turn=true`, Serf checks the
root task store. If the list is nonempty and every task is `done` or
`cancelled`, Serf does not deliver that result. It appends one `verify` task,
starts it, and continues the current activation. The task prompt is exactly:

```text
The original task assigned to you was '<verbatim copy>' - Manually check and confirm that each requirement was met and report that in your final response
```

`<verbatim copy>` is replaced by the exact text of the first user input. Serf
adds no instructions to fix findings, use particular tools, or perform
task-specific checks.

The intervention fires at most once per experimental session. A session with
no task list or an unfinished task list follows existing behavior. The audit
task must use the ordinary task lifecycle: the model receives its current-task
steering and remains responsible for doing the work, marking it done, and
delivering a later final response. Serf does not force the audit task complete.

The first attempted final response is experimental scaffolding, not user
output. It must not emit a terminal communication event, invoke a watch
callback, or become the CLI result. Only the later response is delivered.

## Implementation Boundary

The experiment will add the smallest session-level finalization guard before
the result tool commits terminal side effects. The guard owns three facts:

- whether this is an eligible non-interactive root session;
- whether an audit has already been injected; and
- the original first-input text used to build the audit task.

It will use the existing task store to append and start the audit task and the
existing current-task steering path to continue the model loop. It will not
change task dependency semantics, task schemas, default agent templates,
system prompts, or Harbor task resources.

Experiment-only state may remain in memory because the real screening run is a
fresh, single Harbor attempt with no session resume. Crash-safe persistence is
product-design work and is deliberately outside this experiment.

## Deterministic Validation

Before a live run, a scripted provider will exercise the real session and tool
paths:

1. The model creates and completes an ordinary task, then attempts a terminal
   result.
2. Serf withholds that result, appends and starts one audit task, and gives the
   model another round.
3. The model observes the original input unchanged inside the injected task,
   completes the audit, and submits a second terminal result.
4. The caller receives only the second result.

Separate behavioral cases will prove that interactive roots, non-interactive
subagents, empty task lists, and unfinished task lists retain existing
finalization behavior. Tests will inspect structured task state and delivered
events; they will not match a rendered script or large prompt with regexes.

## Real Screening Run

Run only `sqlite-with-gcov` from Terminal-Bench 2.1 with GPT-5.6-Luna at maximum
reasoning, its official task resources and timeout, one attempt, zero Harbor
retries, and no upload or submission. Record:

- the exact Serf commit and binary hash;
- the exact task checksum and original-prompt hash;
- whether and when the audit task was injected, started, and completed;
- every tool call and file change after audit injection;
- whether the final response reports a requirement-by-requirement check;
- the verifier reward, output, and any exception.

A useful behavioral treatment requires the audit task to run and to inspect the
concrete deliverable after injection. Reward `1` is a positive task outcome.
Reward `0`, an exception, or an audit that merely asserts success without
checking the deliverable requires root-cause analysis before another task is
run.

If the first treatment is positive, the next target is
`count-dataset-tokens`, which tests the same mechanism against a semantic scope
choice rather than a filesystem-layout requirement. No clean passing task will
be rerun during this experiment.

## Non-Goals

- No product merge based on one treatment run.
- No changes to system-prompt guidance, task wording beyond the stated
  treatment, delegate behavior, Harbor limits, or Terminal-Bench assets.
- No benchmark-specific hints in Serf.
- No control rerun in the initial screen; the archived failed trajectory is the
  baseline, and any positive signal must be confirmed separately before a merge
  proposal.
