# Final bounded repair: guided full-editor return

## Scope and root cause

Base: `79d68bc52`, branch `wip/provider-connection-a`. Implements only the Important finding in `final-review.md`; no replanning, backend/store/OAuth-core/full-editor/widget changes, merge, push, or baseline/first-session proof rerun.

Before the fix, `ConnectProviderDialog.tsx:45–57` replaced `ProviderConnection` with management/full settings. That unmounted the selected provider and `SelectedConnection`'s actual created name, credential draft, saved/configured state and original access-review baseline. Back consequently reopened discovery, not the pending guided connection. Persisted host settings were not lost; volatile guided repair context was.

## Runtime red before production edits

Added an actual `ConnectProviderDialog` regression (now starting at `ConnectProviderDialog.test.tsx:62`): select provider, use the real AddInstanceDialog to create `team-custom`, fail credential save at the scripted AppWire boundary, verify focused error and retained draft, open management → Full provider settings → Back, verify the draft, retry, and require the exact same name with only one create.

The decisive pre-fix run of the exact focused command exited **1**, **67 passed / 1 failed**. At pre-format test line85, `screen.queryByLabelText("API key")` was null after Back; its expected retained value was `repair-draft`. This confirms the precise unmount boundary, rather than merely reproducing a standalone child-state test.

Two preceding attempts were fixture setup failures, not product red: the endpoint field is labelled `Base URL (optional)`, and this suite has no `toHaveFocus`/`toHaveValue` matchers. Corrected those to the actual label and native activeElement/property assertions, without changing the acceptance assertion. Their raw logs are retained separately.

## Minimal production design

- `ConnectProviderDialog.tsx:45–65`: always retain the guided owner in a stable position; pass whether the guided view is visible. Management/settings remain the existing conditional components.
- `ProviderConnection.tsx:14–19, 78–85`: optional `visible` defaults to existing behavior. Discovery returns null while away; the selected owner remains mounted.
- `ProviderConnection.tsx:146–190, 211–217, 245–260, 410–413`: retain only component-local state. Departure invalidates operation generation, phase, result, review and OAuth state, and closes configuration subviews. Async-current guards also require visibility. Invisible guided owners render null before any dialog/editor/OAuth subtree; no hidden credential field, dialog, focus trap or polling component is retained. Error focus is visibility-gated and resumes in the current dialog on return.
- Actual name, draft, saved/configured flags and original baseline survive repair. The existing refresh-and-check comparison still forces explicit review when source or destination changes; no automatic credential rewrite or provider check occurs on return.
- Existing provider-identity draft guard remains active while away, permanently discarding a draft when the resolved provider changes. Change provider unmounts the selected owner; actual caller dismissal unmounts the wrapper. No browser storage/shared state, fake return DTO, auth-capability filter, durable verification state or default write was added.
- Full editor/auth superset and existing labels are unchanged; no user-doc wording change was needed. `ProviderConnection.test.tsx` was not modified. All earlier assertions, including cross-provider isolation, remain intact.

## Added runtime coverage

All tests render real production wrapper/components/stores and script only AppWire responses; they are **component integration tests, not backend/provider E2E proof**.

- `ConnectProviderDialog.test.tsx:62`: custom creation once, failed credential save, focused error, full settings return, exact draft/name retry and final callback.
- `:221`: departure during save, refresh, check, or an already successful result; settle the actual deferred operation, require no additional calls or completion callback, no hidden dialogs/inputs, current-dialog focus, preserved draft and enabled explicit retry.
- `:261`: pending guided OAuth start cannot launch fallback/hidden flow while away; draft and enabled sign-in return.
- `:291`: changed destination and changed source each require fresh explicit review after return, with no extra credential save or provider check before confirmation.
- `:317`: resolved provider identity changes while away clear the draft, cannot submit it to the new provider, and cannot resurrect it when the old identity returns.
- `:336`: Change provider, Cancel after return, and Close while away clear secrets on the actual unmount/reopen lifecycle; no background auth calls.

Mutation check: temporarily remove only excursion invalidation while retaining the owner. The five excursion tests fail (save/refresh/check stay busy, success Continue incorrectly survives, OAuth Sign in stays disabled), exit **1**. Restore the effect and rerun green. This proves keeping the component mounted alone is insufficient and the new assertions detect that boundary. Mutation fully restored before final gates.

## Verification commands and raw evidence

All logs below are under `/tmp/evener-sandbox-1958759275/`; test output captured with `2>&1 | tee` under Bash pipefail, never hidden or warning-muted.

From `cmd/evener-hub/frontend`:

```sh
npx vitest run src/panes/settings/sections/credentials/ConnectProviderDialog.test.tsx src/panes/settings/sections/credentials/ProviderConnection.test.tsx
```

- `repair-red.log`: exit1, wrong fixture endpoint label (not product evidence).
- `repair-red-confirmed.log`: exit1, unsupported fixture matcher (not product evidence despite filename).
- `repair-red-runtime.log`: exit1, confirmed missing guided draft after Back; 67 pass / 1 fail.
- `repair-green-initial.log`: exit0; 68 tests pass after minimal lifetime repair.
- `repair-edges.log`: exit0; 78 tests pass before adding pending-OAuth case.
- `repair-green-final.log`: exit0; **79 tests, 2 files pass**, with all new cases and final formatted production source.
- `repair-green-final-typed.log`: exit0; **79 tests, 2 files pass** after completing the OAuth fixture's required wire fields. No stderr warnings in focused output.

```sh
npx vitest run src/panes/settings/sections/credentials/ConnectProviderDialog.test.tsx -t 'repair excursion'
```

- `repair-invalidation-mutation-red.log`: exit1; 5 failed / 34 skipped under deliberate invalidation-removal mutation, subsequently restored.

```sh
npx biome check --write src/panes/settings/sections/credentials/ConnectProviderDialog.tsx src/panes/settings/sections/credentials/ConnectProviderDialog.test.tsx src/panes/settings/sections/credentials/ProviderConnection.tsx
```

- Exit0, `Checked 3 files ... Fixed 3 files.` Formatting only; no ignored diagnostics.

```sh
npx vitest run src/panes/settings/sections/credentials/ConnectProviderDialog.test.tsx src/panes/settings/sections/credentials/ProviderConnection.test.tsx src/panes/settings/sections/credentials/instanceDialogs.test.tsx src/panes/settings/sections/credentials/CredentialsSection.test.tsx src/panes/spawn/Spawn.test.tsx src/panes/session/chrome/ModelSwitchTrigger.test.tsx
```

- `repair-green-impacted.log`: exit0; **278 tests, 6 files pass**. These existing suites cover the unchanged editors and impacted Settings/Spawn/model-switch entry points. Complete retained output checked for stderr/warning/error diagnostics: none.

From repository root:

```sh
make test-web
TMPDIR=/tmp make test-web-browser
git diff --check
```

- First `make test-web`: exit2; `web-test` and `web-lint` passed, `web-typecheck` found TS2322 in the new deferred OAuth fallback fixture: missing `flowId`, `userCode`, `verificationUrl`, `intervalSeconds`. Added those required wire fields with the generated `AuthDeviceStartResponse` type; no production or assertion changes. Raw `repair-test-web.log`; full failed-run logs retained at `/tmp/evener-sandbox-1958759275/evener-test-web.on0rTh`.
- Browser gate: exit0; `PASS web-layoutguard`, `PASS web-overflowguard`, `PASS web-shellguard`, `PASS web-spawnguard`, `PASS web-transcriptscrollguard`. Raw `repair-test-web-browser.log`.
- `git diff --check`: exit0 after final source formatting.

- Final `make test-web` rerun: **exit0**, `PASS web-typecheck`, `PASS web-test`, `PASS web-lint`. Raw runner output `repair-test-web-final.log`, job `job_034MmbIFXVCO5RKUgkA6eU_vg4poOpzMY6H`.

### Whole-suite stderr evidence limitation

An additional audit of the retained **first**, typecheck-failing whole-web run found React stderr in its otherwise passing unit stream. `evener-test-web.on0rTh/test.log:6` names Session's `cold-start skeleton stays through durable outbox settlement after an identified user echo`; line13 names Composer's `confirmed goal replacement exits recovery without deleting its durable recovery row`. The warning is `The current testing environment is not configured to support act(...)`. The audit counted 4680 such lines across that run, with stderr blocks in Session, Composer, Composer.integration and several other test files; exact file counts are in `repair-warning-audit.log`. That run's unit output reports 438 files / 9975 tests passing, plus 64 Node tests passing. No warning was muted or assertion weakened.

This is **not evidence that the latest run warned or failed**. The final runner exited0, but `scripts/web/test-web.sh:79–80` deletes successful child logs after printing only the three gate summaries. The ephemeral `evener-test-web.jwKGZo` directory seen while work was running was absent after completion, so the latest raw child `test.log` could not be audited. The retained final runner summary is not sufficient to claim whole-suite warning cleanliness. Focused and impacted raw output *was* captured and audited, and is warning-free. No whole-suite/baseline comparison rerun was made. Parent was informed of the exact evidence and this limitation; no speculative out-of-scope test changes were made.

## Commits and delivery

- `78254c0e0` — `fix(web): preserve guided connection during full-editor repair`. Normal named-path commit of exactly `ConnectProviderDialog.tsx`, `ConnectProviderDialog.test.tsx`, and `ProviderConnection.tsx`; hooks not skipped. Production freeze checkpoint sent before whole-web verification, and commit identity sent to parent for parallel final delta review.
- This report is committed separately under its named path after the implementation commit. No unrelated tracked files changed, no prior workspace files deleted, and the audit scratch script was removed after writing its retained evidence summary.

Bounded implementation and requested gates: **DONE_WITH_CONCERNS**, solely for the explicitly bounded whole-suite raw-warning evidence limitation above. Final independent approval and any further warning investigation remain parent-owned.

## Limitations and retained identity

- No new real-provider/network E2E success is claimed. The new repair evidence is at the scripted AppWire/real-component boundary. Browser guards are separate general geometry regressions, not a substitute for the repair test or the historical real first-session proof.
- The completed real desktop/mobile first-session proof remains historical at `e2bff8fe4`; it was neither repeated nor reconstructed and does not identify this repaired source revision.
- Parent owns remaining whole-branch gates and final independent delta approval. No new whole-branch lint/vet/Go-test pass is implied by these frontend gates.
- Original preview remained at PID `1101883`, `python3 -m http.server 43127 --bind 0.0.0.0 --directory docs/design/provider-connection-study`; HTTP200 verified. No replacement preview was started.
