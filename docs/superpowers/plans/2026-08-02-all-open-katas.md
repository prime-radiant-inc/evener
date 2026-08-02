# All Open Katas

## Goal

Implement and verify every kata currently open in the fresh worktree, one
issue at a time, then merge the completed work into `webui-workspace-shell`.
The measured queue is `jwxb`, `z93c`, `q48p`, `ypbx`, `5syn`, `a5a9`, `mkn3`,
and `8jqp`.

## Architecture

Keep each fix at the owning boundary. Lint and scenario-card defects stay in
their existing files. Runtime defects must first be reproduced through the
real boundary named by the kata and then fixed at the component where the
evidence places the fault. `mkn3` remains gated on an authorized measurement
of a real daemon log, and `8jqp` remains gated on Jesse's choice of durable
cross-session notification ownership because the issue documents multiple
incompatible designs.

## Tech Stack

- Go 1.25 with the repository's existing test and lint targets.
- CSS modules and the existing frontend Biome lint target.
- Markdown-driven live scenario cards, checked by the repository's scenario
  audit tests.
- `kata` as the issue ledger and Git worktrees for isolation.

## Global Constraints

- Read `docs/testing.md` before adding or changing tests.
- Use deterministic tests by default; live provider scenarios require their
  existing explicit opt-in.
- Apply test-first discipline: establish a meaningful failing check, make the
  smallest fix, run focused verification, then commit the kata before moving
  on.
- Preserve unrelated work and scope staging to the current kata.
- Close a kata only after recording substantive typed evidence in the ledger.
- Do not access Jesse's live hub state for `mkn3` or choose an architecture for
  `8jqp` without the required authorization.

## Tasks

### 1. Fix `5syn`'s post-merge Go lint failure

- RED: run the failing lint/merge-approval check and capture the
  `strings.Split` modernization diagnostic at
  `scenariosourcecite_audit_test.go`.
- GREEN: replace only the NUL-delimited iteration with the repository's
  Go-supported sequence iteration while preserving empty-entry filtering and
  path indexing.
- Verify the focused audit and `make merge-approval-gate`, commit, and close
  `5syn`.

### 2. Fix `jwxb`'s duplicate CSS fallback declaration

- RED: run the frontend lint target and capture Biome's duplicate-property
  finding in `AppShell.module.css`.
- GREEN: preserve the `100vh` fallback and `100dvh` mobile viewport behavior
  using the smallest lint-accepted CSS structure.
- Run frontend lint/typecheck/tests as applicable, commit, and close `jwxb`.

### 3. Make `ypbx`'s queue scenario tmux target safe

- RED: run the existing scenario-card audit or a shell-level reproduction with
  a dotted `mktemp` basename and show the derived tmux target is unsafe.
- GREEN: derive a collision-resistant tmux name containing only tmux-safe
  target characters, update the card's matching prose, and preserve cleanup
  ownership.
- Run the relevant scenario audits, commit, and close `ypbx`.

### 4. Make `q48p`'s queue readiness visible and authoritative

- RED: exercise the current readiness block against the documented pane shape
  or assert its brittle status-line contract in the focused scenario checks.
- GREEN: wait for visible queue-composer evidence and retain the REST assertion
  for active state, turn identity, and queue capability.
- Run scenario audits, commit, and close `q48p`.

### 5. Investigate and resolve `a5a9` only at the proven boundary

- RED: run the real verbose-serve regression and the lossless bridge tests,
  recording whether the failure is event delivery, observer delivery, or
  synchronous persistence.
- Trace the event path through bridge, observer, and `maybeAutoSave`; do not
  treat the historical counter as proof of a bridge failure.
- If a deterministic production defect is isolated, add its smallest RED test,
  fix it, verify the full relevant package, commit, and close `a5a9`. If the
  evidence proves the kata's oracle is conflating boundaries instead, repair
  only that test contract and record the limitation rather than guessing at a
  production change.

### 6. Reproduce and resolve `z93c` through a scripted real serve/appwire path

- RED: construct a deterministic scripted-provider run through `serf serve`
  and the appwire subscription, asserting that a completed shell has its
  `TOOL_RESULTS` persisted and delivered before the completion marker is
  accepted.
- Trace the shell completion, transcript persistence, and authoritative event
  delivery boundaries to identify the first missing transition.
- Add the smallest root-cause fix and regression test, verify the relevant
  serve/appwire packages, commit, and close `z93c`.

### 7. Resolve the measurement gate for `mkn3`

- Do not inspect Jesse's real state directories from this worktree.
- Ask Jesse for an authorized measurement or a supplied sample/decision. If
  authorized data establishes the correct bound, implement the narrow bound
  and test it; otherwise leave the kata open with the precise blocker.

### 8. Resolve the architecture gate for `8jqp`

- Preserve the current no-send behavior until the durable ownership and
  reachability contract is chosen.
- Ask Jesse to choose the cross-session durable routing model before changing
  jobstore/event schemas or restart behavior. Then add a RED restart test,
  implement the chosen boundary, verify it, commit, and close `8jqp`.

## Integration

After all unblocked kata commits are verified, re-check the ledger, run the
requested integration gates from the clean implementation branch, merge the
branch into `webui-workspace-shell` with an explicit merge commit if histories
diverged, rerun destination verification, and remove only the temporary
worktree and branch after confirming merged ancestry and a clean destination.
