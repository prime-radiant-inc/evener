# Task 3 — provider entry points and first-session handoff

Status: DONE — delegated Task3 implementation and frontend verification complete.

Implementation commit: `e2bff8fe42d29eb8afa9f3d7ae0a7759db46524f`
(`feat(web): route provider onboarding into first sessions`). Normal named-path commit, exit 0; 16 files changed. This report is committed separately after the implementation so it can name the exact implementation commit.

## Scope and changes

All frontend paths below are relative to `cmd/evener-hub/frontend/`.

- `src/panes/settings/sections/credentials/CredentialsSection.tsx` and `.test.tsx`: normal Settings **Connect provider** opens compact discovery; existing instance management remains. Explicit `fullEditor` context retains the original add dialog inside the connector's full-settings escape.
- `src/panes/settings/sections/credentials/ConnectProviderDialog.tsx` and `.test.tsx`: only the narrowly authorized full-editor context and its nonrecursive navigation proof. No connector/draft/auth-core changes.
- `src/panes/spawn/Spawn.tsx` and `.test.tsx`: completion receives the actual instance name, invalidates the pane catalogue cache, refreshes availability, and opens the real model chooser scoped to that instance. Workdir/prompt persist; no implicit Start or default writes. Once this draft enters onboarding, credential refresh cannot run the legacy untouched-model fallback in place of an explicit choice. Existing fallback behavior outside onboarding remains covered.
- `src/panes/session/chrome/ModelSwitchTrigger.tsx` and `.test.tsx`: shared **Connect another provider**, actual-instance filtering with **Show all models**, explicit refreshed catalogue on completion, unchanged model until pick, normal cancellation. Existing desktop/mobile chooser, load-generation guard, diagnostics, and pick behavior remain.
- Parent-approved expansion: `src/panes/session/chrome/ModelSwitch.tsx` and `.test.tsx` forward optional refresh to existing `threadsStore.listModels(refresh)`. A real warmed-store integration test proves fresh `model/list` after keyless setup without an auth notification; no model/default mutation before pick and original `remote:original` ref retained on explicit model-set.
- Parent-approved expansion: `src/panes/settings/Settings.test.tsx` only updates the two old Add-button navigation lookups/comment/local variable. All dialog/pane Escape assertions remain.
- `scripts/spawnguard/run.mjs` and `src/dev/spawnguard-entry.tsx`: additive onboarding scenario; every previous browser assertion retained. `spawnguard.html` unchanged.
- `docs/connecting-a-provider.md`: first connection → actual key/auth route → save/check → model → Start; provider key links, API/subscription distinction, cloud/local/custom, existing-host/advanced access, model-list-only and unsupported checks, repair and cancellation semantics.
- `docs/llm-provider-config-and-launch.md`: leading how-to link; corrected claim that every uncredentialed implicit preset appears in the launch-ready instance list.
- `docs/design/provider-connection-study/README.md`: Jesse's Option A approval recorded separately; historical rankings/alternatives preserved.

No backend, OAuth implementation, store, draft core, shared picker, or design-preview modifications. Port 43127/PID 1101883 untouched.

## TDD and failures resolved

From `cmd/evener-hub/frontend`:

```sh
npx vitest run src/panes/settings/sections/credentials/CredentialsSection.test.tsx src/panes/spawn/Spawn.test.tsx src/panes/session/chrome/ModelSwitchTrigger.test.tsx
```

Runtime red before implementation: **3 failed, 172 passed**, exit 1 (`task3-red.log`). Settings and session failures were the missing discovery/connect-another actions. The initial custom-keyless Spawn fixture incorrectly assumed an explicit endpoint was missing configuration; `useProviderSetup` correctly treats explicit keyless endpoints as configured. That fixture was corrected to use the configured chooser route, without weakening its model/workdir/Start assertions.

Additional decisive fresh-install red against the original HEAD Spawn implementation (restored after the run):

```sh
npx vitest run src/panes/spawn/Spawn.test.tsx -t 'fresh guided connection'
```

**1 failed, 119 skipped**, exit 1: after explicit Continue the server-returned model option never appeared (`task3-guided-red.log`). With implementation, this test passes and also checks cancelled handoff preserves workdir/prompt with no implicit model choice/Start/default write.

Real session-cache red:

```sh
npx vitest run src/panes/session/chrome/ModelSwitch.test.tsx -t 'keyless connection refreshes'
```

Before refresh forwarding: **1 failed, 25 skipped**, exit 1, missing newly returned model (`task3-session-cache-red.log`). After forwarding and correcting the new test's wire-field oracle against generated `ThreadModelSetParams` (`ref`, `modelProvider`, `model`): **1 passed, 25 skipped**, exit 0 (`task3-session-cache-green.log`). Production setModel/store code was unchanged.

Other failures were fixed rather than hidden:

- Initial helper invocation used absent `python`; rerun with `python3` actually installed tests before recording runtime red. The accidental baseline-only run is not counted as red.
- Biome reported an optional-chain requirement; corrected it. Standalone TypeScript caught unknown recorded-RPC params and a mistaken test callback field; fixed using the generated types.
- First full `make test-web`: **437 files passed / 1 failed; 9960 tests passed / 2 failed**. Both failures were the old Settings Add-button lookup; parent approved navigation-only repair with every Escape assertion retained.
- First browser gate failed at explicit Start. Diagnostic method evidence stopped after `evener/path/validate`; root cause was the fixture using `last-working-dir` (picker fallback) as launch prefill. The scenario now uses production `?dir=` prefill and awaits actual Start RPC. No deadline enlargement or model/workdir assertion reduction. The fixture still deliberately stops at the scripted `thread/start` boundary.

## Final verification

- Biome `check --write` on all 12 touched `src` paths: exit 0.
- `npx tsc --noEmit`: exit 0.
- Expanded named focused command:

```sh
npx vitest run src/panes/settings/Settings.test.tsx src/panes/settings/sections/credentials/CredentialsSection.test.tsx src/panes/spawn/Spawn.test.tsx src/panes/session/chrome/ModelSwitchTrigger.test.tsx src/panes/settings/sections/credentials/ConnectProviderDialog.test.tsx src/panes/session/chrome/ModelSwitch.test.tsx
```

**6 files passed; 251 tests passed**, exit 0 (`task3-final-focused.log`). The existing keyless test's `act(...)` environment warnings were also present in the baseline-only run; no assertions were muted.

- Focused `TMPDIR=/tmp node scripts/spawnguard/run.mjs`: exit 0 at **320, 390, 899, 900, 1440px**. Retained directory/breakpoint/control-row/accessibility/eight-attachment/overflow checks plus onboarding masks, focused missing-key validation, collapsed advanced, cancel/draft, explicit Continue, actual model choice and explicit Start with exact model/workdir.
- Final `make test-web`: exit 0 — `PASS web-typecheck`, `PASS web-test`, `PASS web-lint`.
- Independent documentation check: all local Markdown links in the three touched docs resolve; design study from `## Brief` onward is byte-identical to pre-task HEAD.
- Final `TMPDIR=/tmp make test-web-browser`: exit 0 — `PASS web-layoutguard`, `PASS web-overflowguard`, `PASS web-shellguard`, `PASS web-spawnguard`, `PASS web-transcriptscrollguard`.
- `git diff --check`: exit 0 before submission and after implementation commit. `git status --short` was empty after implementation commit (this not-yet-added report was ignored until explicitly staged).

Evidence logs are retained outside source under `/tmp/evener-sandbox-1958759275/`: `task3-red.log`, `task3-guided-red.log`, `task3-session-cache-red.log`, `task3-session-cache-green.log`, `task3-final-focused.log`, `task3-spawnguard.log`, `task3-final-test-web.log`, and `task3-final-browser.log`. Implementation/debug scripts and temporary Spawn copy were removed.

## Boundaries and handoff

The durable FakeClient browser scenario is **geometry/component integration evidence, not E2E**. It uses no real credential or external provider and deliberately refuses successful session creation at the scripted RPC boundary. Parent owns the frozen built production-hub/browser proof with isolated state and an external HTTP provider, independent reviews, final whole-branch lint/vet/test/browser gates, and final preview availability check. No live-provider generation or OAuth-account proof is claimed here.

Production source was frozen and parent notified after the focused browser passed. Expanded named/format/typecheck success was separately reported. No merge, push, amend, or hook skipping.
