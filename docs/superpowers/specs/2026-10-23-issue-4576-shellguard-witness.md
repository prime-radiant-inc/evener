# Issue #4576: shared browser-guard witness failures

Status: frozen for spec PAR. Jesse authorized implementation after spec PAR.
Base: `e9d57af1e38ec863b7c7e371bb07c288e692a0f6`.

## Scope and intent

Strengthen the floating-fixture witness timeout test and give the floating and
hover-card guards one failure-composition contract. This changes diagnostic
ownership, not guard predicates, waits, journeys, or browser behavior.

Allowed files: this spec, `scripts/shellguard/floatingFixture.mjs`,
`scripts/shellguard/sessionHoverCard.mjs`, their `.test.mjs` files, and a new
`scripts/browserGuardWitness.mjs` plus `scripts/browserGuardWitness.test.js`,
all scripts relative to `cmd/evener-hub/frontend/`.

## Non-goals and preservation

Do not change `Driver.waitPage`, `watchEvaluateFailures`, CDP plumbing, fixture
state collection, browser runs, or production UI. `Driver.waitPage` still catches
rejected `evaluate` calls at `scripts/skillguard/run.mjs:728`; report that fact
without changing it. Preserve both witness expressions and all current output
fields, original error messages, floating host context, and hover `partial`
markers. Keep the floating witness bound at 2000 ms and the hover witness on its
existing CDP tripwire. Do not change any other diagnostic copy.

## Bounded-read behavior

Replace the floating test's timeout wording assertion with fake-clock behavior.
Observe both resolution and rejection so either counts as settlement. At
`WITNESS_READ_MS - 1` the diagnostic promise must remain pending. Advance one
millisecond and require settlement at the bound, a returned Error whose `cause`
is the original error object, and parsed witness `unreadable: ["witness"]`.
Do not assert on timeout wording. Demonstrate red by temporarily moving the real
bound one millisecond early and one millisecond late, then restore it and show
the behavioral test passing. These are mutation checks, not a new guard fix.

## Shared helper contract

Add `witnessFailure(driver, expr, error, options = {})` beside
`browserGuardCdp.mjs`. The first three arguments are the reusable contract;
optional policy keeps existing guard-specific diagnostics unchanged:

- `context` defaults to `{}` and carries serializable host-owned fields.
- `readMs` is optional. Floating supplies its existing `WITNESS_READ_MS`;
  hover leaves it unset and retains `evaluate`'s existing tripwire.
- `partial` defaults to false. Hover enables it so collection failure keeps
  `partial: true`; floating does not gain this field.

The helper evaluates the supplied expression through real `evaluate`, optionally
bounded with the floating implementation's existing timer semantics. Evaluation
rejection or expiration becomes `{ unreadable: ["witness"], error: detail }`,
plus `partial: true` only when requested. Merge host context before page evidence,
as floating does today. If serialization fails, preserve host context and emit
`unreadable: ["payload"]` with the existing serialization error note. Mark hover
partial on this exceptional fallback too. Such non-JSON CDP payloads cannot come
from real return-by-value transport, but must not replace the original failure.

Return an Error with the original error's message followed by the machine
separator `; witness ` and parseable witness JSON. Preserve the original object
as `cause`. Non-Error failures retain floating's existing String conversion.
Never throw the witness's evaluation or serialization failure instead of the
original guard failure. No retries or new diagnostics are introduced.

`describeFloatingFailure` becomes context construction plus this helper call.
`describeHoverFailure` becomes this helper call with its partial policy. The
shared helper owns collection failure, serialization fallback, suffix assembly,
and cause preservation; neither guard retains another copy.

## Verification

Use the existing frontend Vitest runner and `vite.config.ts` jsdom setup:

```sh
cd cmd/evener-hub/frontend
npx vitest run scripts/shellguard/floatingFixture.test.mjs scripts/shellguard/sessionHoverCard.test.mjs scripts/browserGuardWitness.test.js
```

`package.json` already runs Vitest and its default include collects nested
`.test.mjs` plus `.test.js`; top-level `.test.mjs` files are node:test-only, so
use `.test.js` for the new Vitest helper test. Reuse a compatible installed
frontend dependency tree without running `npm ci` through a symlink.

Direct helper tests use fake CDP transport, not mocked internal helpers. Cover
successful evidence, rejected/throwing transport, non-Error rejection,
unserializable evidence, preserved host context, hover partial policy, exact
original cause, parseable suffix after the separator, and timer settlement and
cleanup when enabled. Message checks use test-supplied opaque sentinels and the
machine separator, never human-readable production phrases. Existing guard
tests continue checking real witness expressions and all current diagnostic
fields.

Run spec PAR with two independent competing reviewers before implementation.
Run implementation PAR on immutable base/head input, then simplify-code and
rerun affected checks. Run `make build` as the requested sanity check after
reading its build/preflight scripts. Do not run the full frontend suite,
`make test-web`, or browser guards. Commit, run RoboRev branch review, push one
round, open the PR against main, and shepherd current-head CI/reviews. Merge
updated main locally before final review/push if required. Admin squash merge
only with every required check green and RoboRev findings low or conclusively
refuted. Confirm GitHub's merge state and SHA. Report any actual blocker.
