# Job Control — Autonomous Execution Runbook

Standing instructions for the agent driving `superpowers:subagent-driven-development` through the six
job-control phase plans. **Read this before starting.** The skill already gives you the per-task
loop — fresh implementer subagent → spec-compliance review → code-quality review → commit — and
continuous execution (no human check-ins between tasks). This runbook layers the few guardrails that
make an *unattended* run safe.

## Execution order

Run the six plans in order. Do not start a phase until the previous one is fully green:

1. `2026-06-08-job-control-phase-1-jobstore.md`
2. `2026-06-08-job-control-phase-2-shell-jobs.md`
3. `2026-06-08-job-control-phase-3-delegate.md`
4. `2026-06-08-job-control-phase-4-watches.md`
5. `2026-06-08-job-control-phase-5-nested-jobs.md`
6. `2026-06-08-job-control-phase-6-cutover.md`

The design contract is `docs/superpowers/specs/2026-06-08-job-control-design.md` — consult it when a
plan task references a `§`-section.

**Phase gate (hard).** At each phase boundary, `make test` + the full `make lint` (golangci ×4 +
`serf-namingcheck`/`internalcheck`/`docscheck`) must be green across all modules before the next phase
begins. The phase plans' final task runs this; never skip it, never start the next plan on a red gate.

All work stays on branch `job-control-spec`. Merging to `main` is a **separate human step** (branch
protection: PR + `build-and-test`) — never merge autonomously.

## Rule #1 — no fake-green (the one that actually matters)

Unattended TDD fails when an agent that can't make a test pass honestly weakens the test, stubs the
implementation, or asserts mocked behavior to keep moving. That is worse than stopping.

- The **code-quality reviewer** must treat each of these as a **blocking** finding: a test weakened
  or deleted to pass; an implementation stubbed/short-circuited to satisfy a test; a test that asserts
  mocked behavior instead of real logic.
- "This test can't pass honestly" is **BLOCKED → escalate to the human** — never a quietly-weakened
  test.
- Test output must be **pristine**. Expected error output is captured and asserted, not silenced.

## Honor the grep-confirm notes

The later plans build on earlier-phase APIs and carry `grep -n … agent/jobs.go`-style confirm notes.
Each fresh implementer reads the **current committed code** for any prior-phase symbol and matches it
— never assume the plan's *planned* signature. Because every task commits before the next, the real
API is always on disk by the time a dependent task runs.

## Do NOT build (v1 out-of-scope — building it is a defect, not progress)

- Nested **delegate** jobs (only nested **shell** jobs are in scope).
- Durable watches across restart (watches are in-memory).
- Shell async-approval (`awaiting_permission` running state) — v1 is synchronous `permission_required`.
- The `not_controllable` runtime path — leave the one-line reserved comment; do not wire it.
- The internal "subagent" symbol **rename** — Phase 6 repoints only model-/UI-facing surfaces; private
  internal names (the salvaged delegate runtime: `spawnAgent`/`subagent`/`SubagentStatus`/…) may stay.
- Multi-job barriers / any-of-all-of watches / named job groups.
- Messageable shell jobs / long-running REPL stdin.

(Rationale: spec §16 "Decisions & deferred" + the V1 non-goals.)

## Heavier scrutiny

The **Phase 3 `result_schema` capture task** has been wrong three times in adversarial review. Give
it an extra-careful quality review: confirm the raw `args["output"]` is captured **past**
`normalizeNodeOutput` (not the `{message,data,artifacts}` envelope), that the prose result comes from
`communicate`'s top-level `message` parameter, and that `structured_result_valid` is treated as
true-by-construction (the schema is enforced at the `communicate` call boundary, `registry.go:424`).

## Escalate (BLOCKED) when

- A test cannot go green honestly.
- A plan step contradicts the merged code (a `grep`-confirm reveals real drift the task can't safely
  reconcile on its own).
- A genuine design decision appears that is not already settled in spec §16.

Otherwise execute continuously — don't ask "should I continue?", run the plans.
