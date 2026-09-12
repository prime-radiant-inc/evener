# Task 2 — Compact provider connection

Status: **DONE**. Task 2 implementation, required gates and self-review are complete. Implementation commit: `33df1b7fe` (`feat(web): add compact provider connection flow`).

## Scope and implementation

- New `ProviderConnection.tsx`, `.module.css`, `.test.tsx` under `cmd/evener-hub/frontend/src/panes/settings/sections/credentials/`.
- Updated sibling `ConnectProviderDialog.tsx`, `.test.tsx`, and `instanceDialogs.tsx`.
- Approved narrow expansion: `cmd/evener-hub/frontend/src/widgets/input/index.tsx` and `input.test.tsx`, adding only optional native `required` and `autoComplete` passthrough. The widget previously could not represent the required credential field.
- Approved narrow caller-test expansion: `cmd/evener-hub/frontend/src/panes/spawn/Spawn.test.tsx`, inserting only the two management-navigation clicks in the existing keyless refresh case. All original RPC, refresh and model assertions are unchanged.
- No backend, store, OAuth dialog, full editor, entry-point production, global defaults, model selection, or user documentation changes.
- Existing management is retained behind explicit host-access navigation; its full-settings route reuses `CredentialsSection`/`InstanceSheet` and all existing operations. Existing management assertions remain intact, with test setup navigating the new explicit route.
- AddInstanceDialog gains `initialBase?: string` and returns the actual created name. Existing zero-argument callbacks remain assignable.
- Popular choices use real catalogue IDs: Anthropic, OpenAI, `google` labeled Gemini, OpenRouter. All providers searches all returned descriptors. Codex remains its own OAuth identity, never OpenAI API access.
- Credential input is volatile local state and masked, including JSON. Public implicit providers write directly under the real ID without create RPCs.
- The new operation performs save, real store refresh, freshness/source/destination inspection, then model-list check. Saved, active, and checked are separate visible facts. Success requires explicit Continue; unsupported checks require explicit unverified continuation. No provider response text is echoed.
- Source shadowing or changed destinations require explicit review before checking, including a directly stored key retained after endpoint edits.
- Settings-save/key-failure preserves the actual created name and draft. A superseded create listing offers reload/repair, not duplicate creation.
- Optional/no-auth endpoints do not require a key; existing-host access retains the host's resolved authentication. Configuration, JSON/ADC and existing device/redirect flows remain available using actual setup auth modes.
- Dismissal, change-provider, reconnect, metadata changes and debounced auth refresh invalidate stale work/results. Store `fetch()` failures and superseded refreshes are not accepted as proof of fresh metadata.

## TDD evidence

Commands below ran from `cmd/evener-hub/frontend` unless noted. Runtime failures were observed before the corresponding implementation; imports/build failures were not counted as red.

1. `npx vitest run src/panes/settings/sections/credentials/ProviderConnection.test.tsx`
   - Initial red: **3 failed**, exit 1, missing Anthropic/All providers. The new tests initially imported the real existing `ConnectProviderDialog` under the future component name; no placeholder component or internal mock was used. After implementing the new component the import was changed to it.
   - Retained output: `job:job_034MkC3zcMNUHDLkrgkB5q_El5AC5NUqE4I`.
   - Discovery green: **3 passed**, exit 0.
2. Same command after adding save/repair/auth assertions:
   - Red: **14 failed / 3 passed**, missing save/check/auth operations. `job:job_034MkC3zcMNUHDLkrgkB5q_oIp4lTe2r1GV`.
   - Green: **17 passed**, exit 0.
3. `npx vitest run src/widgets/input/input.test.tsx`
   - Red: **1 failed / 13 passed**, required was false rather than true.
   - Green: **14 passed**, exit 0.
4. `npx vitest run src/panes/settings/sections/credentials/ConnectProviderDialog.test.tsx -t 'opens compact discovery'`
   - Red: **1 failed**, missing compact default route. `job:job_034MkC3zcMNUHDLkrgkB5q_eDdfqR1MRBBq`.
   - Green included in focused credential suite.
5. Self-review red/green regressions in ProviderConnection.test.tsx:
   - `-t 'popular Gemini'`: real Google identity absent from shortlist; red `job:job_034MkC3zcMNUHDLkrgkB5q_nijqqZVNCkje`.
   - `-t 'configuration refresh invalidates'`: stale OAuth startup opened the old authorization browser; one failing assertion, then green after invalidation fix.
   - `-t 'discarded create listing'`: missing Reload connection; red `job:job_034MkC3zcMNUHDLkrgkB5q_hJp19Wd3oWHJ`.
   - `-t 'discovery load failure'`: focus stayed on Popular providers rather than the recovery alert; one failing assertion, then green.
   - `-t 'resolved setup auth modes'`: vendor key help remained despite OAuth-only resolved setup; one failing assertion, then green.
   - `-t 'empty required key'`: no associated inline validation explanation; red `job:job_034MkC3zcMNUHDLkrgkB5q_oYYxXXc1kqGr`, then green.

Fixture corrections: scripted list replies now return independently cloned objects, matching real decoded network responses. Reusing the same JavaScript arrays incorrectly tripped the deliberate fresh-list identity check. An initially repeated run used a wrong relative script path and made no edit; the corrected fixture run passed. Unsupported Testing Library `exact` options and one missing required scripted OAuth response field were fixed before TypeScript verification. No failing assertions were removed or weakened.

## Coverage of required edges

The 36 real-component cases cover: zero launch-ready discovery, one required password field, key/billing help, complete catalogue search, cloud full-form preselection, canonical Gemini ID, empty-key focus/validation, direct ID save/check/named continuation, save failure, settings-saved/key-failed repair, discarded create metadata recovery, auth rejection, endpoint/configuration failures, unsupported continuation, source shadowing, changed destination review, swallowed fetch failure, superseded fetch, optional local/no-auth access, JSON route/mask, ADC help, device/redirect route selection, resolved auth-mode override, read-only settings versus writable credential store, hidden setup, host-access retention, cancel/change-provider draft isolation, stale save/refresh/check completion, reconnect, stale OAuth start, auth notification/refetch invalidation, and initial loading failure recovery.

Existing full-editor/OAuth/store behavior is additionally exercised by the unchanged credential dependency suites in the focused gate.

## Verification

- Named touched source paths formatted with `npx biome check --write`: **9 files checked**, exit 0.
- `npx vitest run src/panes/settings/sections/credentials src/widgets/input/input.test.tsx`: **14 files / 307 tests passed**, exit 0.
- `npx tsc --noEmit`: exit 0.
- `git diff --check`: exit 0.
- Initial root `make test-web`: typecheck and web lint passed; **437 files / 9,951 tests passed**, one caller test failed because it still expected immediate management rather than the new discovery default. Evidence: `job:job_034MkC3zcMNUHDLkrgkB5q_QsXPvNa9thfh`; full logs `/tmp/evener-sandbox-1958759275/evener-test-web.tyqT53`.
- Approved `Spawn.test.tsx` navigation adaptation retains the existing keyless check RPC/availability/model assertions. `npx biome check --write src/panes/spawn/Spawn.test.tsx`: exit 0, no additional edits. `npx vitest run src/panes/spawn/Spawn.test.tsx -t 'successful keyless testing refreshes availability without an auth notification'`: **1 passed / 117 skipped**, exit 0. No Spawn production change.
- Root `make test-web` rerun after caller repair passed: `PASS web-typecheck`, `PASS web-test`, `PASS web-lint`, exit 0 (`job:job_034MkC3zcMNUHDLkrgkB5q_s89xd0m87DkU`).
- Final self-review added `-t 'changing base in the full form'`: runtime red at the old provider title/help (`job:job_034MkC3zcMNUHDLkrgkB5q_y9GoWLplftt5`), then green. Continuing from the full form now uses the actual created provider for auth/help/title and clears a draft when crossing providers. Full root gate after that correction passed (`job:job_034MkC3zcMNUHDLkrgkB5q_s25Ml06Gd9Qi`, all three subgates, exit 0).
- Dependency-contract audit added `-t 'device authorization survives'`: a real delayed authorized-list refresh cancelled DeviceCodeDialog's polling effect because the new caller supplied an unstable completion callback. The corrected controlled-clock test failed at missing Continue (`job:job_034MkC3zcMNUHDLkrgkB5q_9kXHSgj1APFB`), then passed after stabilizing only the new caller callback. It proves actual device authorization -> refresh -> auth/test -> named continuation. An earlier test-fixture attempt timed out due user-event/fake-clock coupling; that timeout was not counted as valid red, and was fixed with an act-wrapped DOM click before measuring the poll.
- Frozen final root `make test-web`: **PASS web-typecheck; PASS web-test; PASS web-lint**, exit 0. Retained evidence: `job:job_034MkC3zcMNUHDLkrgkB5q_WZ11PB6virX0`.

## Self-review and boundaries

- Reviewed changed production files against the brief and actual Task1 registry identities. Full editors and OAuth implementations are reused, not replaced. No compatibility route is based on missing catalogue metadata; management is an explicit user choice.
- New CSS uses existing design tokens, compact two-column discovery, narrow-screen single-column wrapping, and breakable destination/source summaries. No motion was added.
- UI tests use the repository's scripted appwire network boundary with real components and stores. They are **not** real provider or end-to-end backend verification. Task1 supplies isolated real HTTP/backend proof.
- Browser visual verification, actual external OAuth/ADC/cloud accounts, provider generation, Task3 first-session/model handoff, and final branch gates remain outside this unit. No live credentials were used. The design-preview process was not changed.

## Commits

- `33df1b7fe` — `feat(web): add compact provider connection flow`, nine explicitly named source/test paths, normal hooks; no merge or push.
- This report is committed separately under its explicitly owned SDD path. `.superpowers/` is ignored by default, so only this named report is explicitly force-added, matching the existing tracked Task1 report; no other SDD artifacts are included.
- No known unresolved Task2 defect. Browser/live-provider checks and Task3 entry points remain the next unit's boundaries. When changing Settings entry points in Task3, retain the full-editor escape from the connector rather than recursively reopening the guided flow.

## Independent review cycle 1 — deferred draft isolation

The independent review found an Important defect in the original implementation: if a newer listing discarded the create response, the created row was absent during `onSuccess`. Its callback-only clearing therefore missed a change from Anthropic to OpenAI. Later reload resolution kept the Anthropic draft in the OpenAI input. This supersedes the original self-review's no-known-defect statement above.

### Reproduction and bounded fix

- Preserved the independent reproduction from `task2-review-draft-isolation-repro.py` as a durable component test, including its final OpenAI-dialog and empty-value assertions.
- Added same-provider preservation cases for both explicit Reload connection and ordinary listing resolution, plus cross-provider ordinary-listing resolution.
- Only `ProviderConnection.tsx`, `ProviderConnection.test.tsx` and this report are changed in this cycle. No OAuth, store, full editor, widget, or Task3 edits.
- Replaced the untagged local credential value with a local `{ providerId, value }` draft. Unknown created metadata leaves its origin intact. A resolved mismatching provider exposes an empty value immediately and clears the old draft; a matching provider keeps it. This applies wherever the row resolves, not only in the reload callback. Removed the incomplete callback-specific clearing rather than adding another special-case path.

### Red and green evidence

From `cmd/evener-hub/frontend`:

1. `npx vitest run src/panes/settings/sections/credentials/ProviderConnection.test.tsx -t 'review regression:'`
   - **RED**, exit 1: **2 failed / 2 passed / 36 skipped**. Both cross-provider paths expected `value: ""` but received `anthropic-private-draft`; both same-provider preservation cases passed before the fix. Observed at 04:01 UTC on 2026-09-11.
   - **GREEN**, exit 0: **4 passed / 36 skipped**, including the unchanged independent reproduction. Repeated after formatting, also exit 0.
2. `npx vitest run src/panes/settings/sections/credentials src/widgets/input/input.test.tsx`
   - **14 files / 311 tests passed**, exit 0; ProviderConnection now has 40 cases.
3. `npx biome check --write src/panes/settings/sections/credentials/ProviderConnection.tsx src/panes/settings/sections/credentials/ProviderConnection.test.tsx`
   - **2 files checked**, exit 0.
4. `git diff --check`: exit 0. Inspection of the changed production behavior confirmed that same-provider recovery is not blanket-cleared and both explicit and ordinary metadata resolution use the same origin check.
5. Root `make test-web`: **PASS web-typecheck; PASS web-test; PASS web-lint**, exit 0. Evidence: `job:job_034MkC3zcMNUHDLkrgkB5q_St1PbtK0aohJ`.

### Review-cycle submission

- **DONE**: the Important draft-isolation finding is fixed; same-provider deferred recovery remains intact.
- Fix commit: `37928c628` — `fix(web): isolate provider drafts across deferred setup recovery`, only the two named component/test paths, normal hooks, no amend.
- This report update is committed separately under its named path. No merge or push. No further review sweep or changes outside the finding were performed.
