# LLM Core Formal Audit Design

## Intent

Use TLA+ and TLC to search Evener's provider-agnostic `llm` library for
state-machine bugs, reproduce credible counterexamples against real Go code,
and fix the bugs the reproductions confirm.

Jesse selected the LLM-library scope and TLC, then approved including verified
fixes on 2026-10-24. The audit does not change production behavior on the strength
of a model alone.

## Scope and contracts

Cover stream event publication, retry admission and cancellation, and completed
tool-round accounting. Start at `llm/stream_generate.go`, `llm/stream_retry.go`,
`llm/retry_util.go`, and their public types. Follow downstream code as needed to
check whether a proposed input is possible and whether callers observe it.

Properties to investigate:

- Content already published to a caller cannot be silently replayed by a retry.
- Caller cancellation prevents a subsequent attempt from starting after the
  retry loop has observed cancellation.
- Ordinary failures respect the configured retry count; rate-limit failures
  respect their separately configured wall budget.
- A later failure does not erase completed tool-round results and their usage.
- Only a settled model response can cause automatic tool execution, and tool
  execution respects the configured round limit.

Treat each property as an audit hypothesis until its contract and source
correspondence have been checked. State any uncertain contract explicitly.

## Approaches

1. **TLC on the LLM library, selected by Jesse.** Finite state spaces give
   concrete counterexample traces that can become deterministic Go tests.
2. **Include the session lifecycle.** Adds fallback, transcript writes, and
   crash recovery but requires a larger abstraction. Deferred.
3. **Lean proofs.** Useful for selected unbounded algorithmic properties but
   a poorer first fit for this search through bounded execution sequences.
   Deferred.

## Method and deliverables

Keep source-linked models, checker configurations, findings, and reproduction
commands in `docs/formal/llm/`. Use separate small models rather than one model
of the whole library. Name bounds, fairness assumptions, abstractions, and
excluded behaviors. Retain reproducible pre-fix configurations where useful.

Use scripted providers only at the external LLM boundary. Unit tests exercise
real `Client`, `StreamGenerate`, `RetryStream`, accumulators, and tool execution.
Each confirmed bug needs an observed failing test before its production fix and
a passing run afterward. Fixed regression tests belong in the default suite,
without an opt-in build tag. No live provider calls are required.

Use one isolated worktree and leave existing untracked work in the main checkout
untouched. Download only the TLC tool needed for the checks, pin its version and
checksum, and keep state-space output in session scratch. Keep local Go checks
targeted; CI owns broad regression gates. No new runtime dependencies.

An independent review checks model/source correspondence, the legitimacy of the
claimed properties, test oracles, and fix scope. The report separates confirmed
bugs, rejected hypotheses, and bounded checks with no counterexample. Passing
TLC checks are not a proof of the whole library or of live provider behavior.
