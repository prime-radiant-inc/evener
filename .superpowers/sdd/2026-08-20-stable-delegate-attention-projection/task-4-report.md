# Task 4 Report: Trust Stable Delegate Attention in Hub Cards

## Status

Complete. Stable delegate projections now solely own hydrated lifecycle, outcome, reason, timing, exhaustion, resumability, and attention. Expanded child watches retain content and latest-quote activity only. Obsolete child/follow-up reconciliation production and tests were deleted.

## Base / Head

- Base and pre-Task-4 HEAD: `eeb455ab5c894fa9efc7082be80e85da194748ea`
- Deliverable commit: `refactor(web): trust stable delegate attention` (final hash is reported in the handoff; a commit cannot embed its own hash)

## Exact paths

Production:

- `cmd/evener-hub/frontend/src/panes/session/transcript/ToolCallItem.tsx`
- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx`
- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.tsx`
- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModuleStore.ts`
- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/watchedChild.tsx` (deleted)

Tests:

- `cmd/evener-hub/frontend/src/panes/session/transcript/flow/LivenessLine.test.tsx`
- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx`
- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/subagentModule.test.tsx`
- `cmd/evener-hub/frontend/src/panes/session/transcript/tools/watchedChild.test.tsx` (deleted)
- `cmd/evener-hub/frontend/src/panes/transcript/Transcript.test.tsx`

Report:

- `.superpowers/sdd/2026-08-20-stable-delegate-attention-projection/task-4-report.md`

The two paths beyond the original plan allowlist were explicitly authorized in the updated brief/ledger: `LivenessLine.test.tsx` only deletes obsolete `liveKind` tests, and `Transcript.test.tsx` only adds the required `needsAttention: false` fixture field. Task 3 production was not changed.

## Behavioral RED

Required command:

```text
cd cmd/evener-hub/frontend
npx vitest run src/panes/session/transcript/tools/subagentModule.test.tsx
```

This fresh worktree initially lacked `node_modules`; the first attempt reached the offline npm registry and failed with `ENOTFOUND`. Dependencies were then copied from the existing checkout at `/Users/jesse/git/prime-radiant/evener/cmd/evener-hub/frontend/node_modules`, per the fresh-worktree dependency guidance.

The executable RED then ran 44 tests: 43 passed and the new real `ToolCallItem` card test failed with `expected undefined to be 'true'`. Stable `needsAttention=true` was ignored while the watched child reported `active`, isolating the intended ownership defect.

## GREEN and verification

Focused command over the four plan files:

```text
npx vitest run \
  src/panes/session/transcript/tools/subagentModule.test.tsx \
  src/panes/session/transcript/tools/subagentModuleStore.test.ts \
  src/panes/session/transcript/ToolCallItem.test.tsx \
  src/panes/session/transcript/tools/jobTools.test.tsx
```

Result: PASS, 4 files and 136 tests.

Formatting:

- `npx biome check --write` over all allowed surviving touched frontend paths: PASS after removing one genuinely unused `TurnModel` import identified by the first formatter run.

Full frontend gate:

- `web-typecheck`: PASS.
- Vitest: PASS, 343 files and 6,706 tests.
- `web-lint`: PASS.
- `make test-web`: nonzero because its subsequent Node browser-process selftests require operations denied by this fixed workspace sandbox. The Node summary was 17 passed / 21 failed; failures report `listen EPERM: operation not permitted 127.0.0.1` and `spawnSync /bin/ps EPERM`. No changed Task 4 source/test failed. Retained gate logs: `/private/var/folders/46/dz2z92w907j150sqxn8b8y1c0000gn/T/evener-sandbox-2565723561/evener-test-web.5a4yqi`.

Required absence command:

```text
rg -n 'liveKind|setWatchedLiveKind|rowKindFromChildStatus|WatchedChildIndicator' cmd/evener-hub/frontend/src
```

Result: expected exit 1 with no output.

- `git diff --check`: PASS.
- Browser geometry is not implicated; `make test-web-browser` was not run, per the brief.

## Tests added / removed

Exactly one real card behavior test was added/rewritten in `subagentModule.test.tsx`. It uses `ToolCallItem` and proves in one behavior:

- stable `needsAttention=true` keeps the needs-you glyph and hidden status across watched child `active`, `idle`, and `awaiting` content status;
- stable false suppresses child-derived attention;
- stable terminal lifecycle wins after child content returns to `active`.

Deleted rather than translated:

- three pure child-status lifecycle mapping tests;
- two collapsed lean-watch reconciliation tests;
- the child-`notLoaded` lifecycle reconciliation test;
- live overlay reason/exhaustion tests;
- the child transcript run-window fallback test;
- five follow-up-tool spawn-row mutation tests;
- two `LivenessLine` `liveKind` overlay tests;
- all three `watchedChild.test.tsx` tests (lean mount, release, and status write-back).

Unchanged plan test files were still included in focused formatting/testing where requested.

## Deleted reconciliation paths

- `WatchedChildIndicator` and its collapsed-row mount;
- `rowKindFromChildStatus`;
- `liveKind`, `setWatchedLiveKind`, terminal no-resurrection logic, and row patch helper;
- follow-up `job_status`, `job_stop`, and `delegate_send` writes into spawn rows;
- child-derived lifecycle, attention, and transcript run-window fallback;
- live reason/exhaustion row shadows.

The full expanded child watch remains for quotes, turns, call/turn counts, and latest-quote activity.

## Line deltas

- Production: `+35 / -240` (net `-205`).
- Tests: `+82 / -512` (net `-430`).
- Total frontend diff: `+117 / -752` (net `-635`).

No helper, harness, fake store, matrix, cache, poller, migration, or abstraction was added.

## Self-review / concerns

- Hydrated lifecycle/outcome is read from the matching owner `ThreadModel.delegates` entry in both the card and top-level tool-row status; frozen output is used only before that entry exists.
- Hydrated reason, timing, exhaustion, and resumability use explicit stable-entry branches, so a missing stable field cannot fall through to stale frozen/child data.
- Attention is only `stable?.needsAttention ?? false`; generic child status no longer produces card attention.
- Child status is retained only to mark the latest expanded quote as active, which is the brief's permitted latest-quote content behavior.
- Frozen delegate output now directly parses its own exhaustion/resumability values for honest pre-hydration fallback; follow-up tools cannot mutate them.
- Required obsolete symbols are absent across all frontend `src` production and tests.
- Concern: the aggregate `make test-web` gate remains environmentally incomplete because the fixed sandbox denies loopback listening and `/bin/ps` to unrelated browser-process selftests. Typecheck, all 6,706 Vitest tests, Biome, and the focused Task 4 suite passed. The parent should rerun `make test-web` outside this restricted sandbox for a zero-exit aggregate verdict.
- Scratch directory: `/private/var/folders/46/dz2z92w907j150sqxn8b8y1c0000gn/T/evener-sandbox-2565723561`; retain the gate evidence directory named above if diagnosis is needed.

## Fix round 1/5: stale ownership comments

The reviewer approved the spec behavior, runtime implementation, tests, and
YAGNI result, with one LOW documentation finding. Two comments in
`subagentModule.tsx` still described deleted follow-up row mutation and the
deleted collapsed lean watch. This follow-up changes comments only:

- the header now states that only the `delegate` spawn materializes frozen
  pre-hydration row state and the stable owner projection supplies hydrated
  state;
- the `autoExpand` comment now states that child watching exists only in the
  expanded body for content, while collapsed lifecycle and attention come from
  the stable owner projection.

The parent reran `make test-web` outside the restricted sandbox. It exited 0
with `PASS web-typecheck`, `PASS web-test`, and `PASS web-lint`, resolving the
environmental concern recorded above.

Comment-only follow-up verification:

- `npx biome check --write src/panes/session/transcript/tools/subagentModule.tsx`: PASS;
- `npx vitest run src/panes/session/transcript/tools/subagentModule.test.tsx`: PASS (35/35);
- `git diff --check`: PASS.

No runtime behavior or test code changed in this fix round.
