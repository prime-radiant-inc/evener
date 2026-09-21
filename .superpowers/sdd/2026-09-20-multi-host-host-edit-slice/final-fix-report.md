# Final fix report: host-edit slice

Date: 2026-09-21
Reviewed range fixed: `e58d72fea7..d4bfcb2d2e`
Status: **DONE**

## Findings addressed

### 1. Roots edits could resurrect obsolete retention without a remote cache

**Production fix**

- `cmd/evener-hub/app_host_manage.go:1421-1430` updates `Update`'s lifecycle comment with the ownership-fence reason.
- `cmd/evener-hub/app_host_manage.go:1546-1572` now performs the roots-change finish sequence in the required order:
  1. `remoteCache.RemoveSource(name)` when wired;
  2. `sources.Remove(name)` when wired;
  3. `forgetLastGoodThreads(name)` when wired;
  4. `registerSource(stored)`.
- The non-roots path remains a complete no-op, and all three existing nil guards remain.
- Retiring both old identities before the retention clear means an in-flight old-source walk fails either its cache-generation fence or its source-instance ownership check. Keeping the cache removal before registration preserves the new generation.

**Regression coverage**

- `cmd/evener-hub/app_host_manage_update_test.go:824-916` adds `TestHostManageRootsEditCannotResurrectOldRetention`.
- It runs both `cache_wired` and `cache_absent`, seeds a real `appwire.Thread` retained row, captures the old ownership token, and uses channels around `forgetLastGoodThreads` to hold the exact post-clear finish window without a scheduling sleep. An old-owner store is attempted while that window is held; completed update retention must remain empty.
- The pre-existing non-roots/source-generation coverage remains at `cmd/evener-hub/app_host_manage_update_test.go:744-821`.

**Documentation correction**

- `docs/superpowers/specs/2026-09-20-multi-host-host-edit-slice.md:173-183` now specifies identity retirement, retention clear, then replacement registration, with both fence explanations.
- `docs/superpowers/plans/2026-09-20-multi-host-host-edit-slice.md:2291-2318` now gives the same safe recipe and rationale.

**RED evidence (before production reorder)**

Command:

```text
go test ./cmd/evener-hub/ -run '^TestHostManageRootsEditCannotResurrectOldRetention$' -count=1 -v
```

Result: **exit 1**. `cache_wired` passed; `cache_absent` failed with:

```text
old-root retention survived the completed roots edit: [{ID:retired-root-session ... CWD:/old-root ... Source:side ...}]
```

Before that behavioral RED, the first test-authoring invocation found a test-only compile error because `appwire.Thread` is not comparable. I replaced `slices.Equal` with concrete retained-row field checks and reran against unchanged production ordering; the exit-1 behavioral result above is the RED counted for the fix.

**GREEN evidence (after production reorder)**

The same command exited **0**:

```text
--- PASS: TestHostManageRootsEditCannotResurrectOldRetention
    --- PASS: .../cache_wired
    --- PASS: .../cache_absent
PASS
ok primeradiant.com/evener/cmd/evener-hub 0.099s
```

### 2. Host update inherited the ordinary 30-second RPC deadline

**Production fix**

- `cmd/evener-hub/frontend/src/stores/hosts.ts:223-235` passes `{ timeoutMs: 35 * 60_000 }` to `evener/host/update`, matching Connect's explicit lifecycle budget at line 242.

**Regression coverage**

- `cmd/evener-hub/frontend/src/stores/hosts.test.ts:112-165` adds a fake-clock test using the real `hostsStore`, `AppwireClient`, and `FakeSocket`.
- It proves transport liveness with `ping`, holds the update beyond 30 seconds, asserts it is still pending and the client remains ready, then returns successful update and list responses and requires the store promise to resolve.

**RED evidence (before timeout fix)**

Command, from `cmd/evener-hub/frontend`:

```text
npx vitest run src/stores/hosts.test.ts -t 'update remains pending past the ordinary RPC deadline and ultimately resolves'
```

Result: **exit 1**, one failed test:

```text
RequestTimeoutError: AppwireClient: "evener/host/update" timed out after 30000ms
```

**GREEN evidence (after timeout fix)**

The same command exited **0**: 1 test passed, 11 skipped. The affected two-file suite also exited **0**:

```text
npx vitest run src/stores/hosts.test.ts src/panes/settings/sections/hosts.test.tsx
```

Result: 2 files passed, 26 tests passed.

The finding called out a plan omission, but the explicit scope for this wave limited documentation edits to the unsafe roots-ordering text. The runtime timeout and its regression test are fixed; no unrelated timeout prose was added to the plan.

### 3. Comparator test did not isolate fields

- `cmd/evener-hub/frontend/src/stores/hosts.test.ts:206-230` now accumulates earlier changes. The currently published row and next response therefore differ only by the field under test on each iteration, so omitting that field from `hostRowEqual` makes the assertion fail.
- Covered by the 26-test focused frontend pass and full `make test-web` pass below.

### 4. Inline-refusal test did not prove SSH-address association

- `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.test.tsx:166-194` scopes the alert lookup to the SSH-address `FormRow`, proves that row owns the labelled input, and checks the alert id is `${addressInput.id}-error`.
- Covered by the 26-test focused frontend pass and full `make test-web` pass below.

## Formatting and verification

Frontend formatting/linting, run separately from `cmd/evener-hub/frontend` as required:

```text
npx biome check --write src/stores/hosts.ts
npx biome check --write src/stores/hosts.test.ts
npx biome check --write src/panes/settings/sections/hosts.test.tsx
```

All three exited **0** (`Checked 1 file`; each fixed formatting on its first run).

Required Go package gate:

```text
go test ./appwire/ ./cmd/evener-hub/ ./cmd/evener-hub/internal/hostreg/ ./cmd/evener-hub/internal/sshconn/ ./cmd/evener-hub/internal/appsource/ ./cmd/evener-hub/internal/hubcore/
```

Result: **exit 0**.

```text
ok primeradiant.com/evener/appwire (cached)
ok primeradiant.com/evener/cmd/evener-hub 157.140s
ok primeradiant.com/evener/cmd/evener-hub/internal/hostreg (cached)
ok primeradiant.com/evener/cmd/evener-hub/internal/sshconn (cached)
ok primeradiant.com/evener/cmd/evener-hub/internal/appsource 5.276s
ok primeradiant.com/evener/cmd/evener-hub/internal/hubcore 17.975s
```

Other required gates:

```text
gofmt -l appwire cmd/evener-hub
```

Result: **exit 0, no output**.

```text
go build ./...
```

Result: **exit 0**.

```text
make test-web
```

Result: **exit 0**.

```text
PASS  web-typecheck
PASS  web-test
PASS  web-lint
```

`git diff --check` also exited **0**. Per the brief, I did not run whole-repo `make test`, `make lint`, or `make test-api-package`.

## Commits and committed paths

### `0b6b9e4b18` — `fix(hosts): fence retention stores across roots edits`

- `cmd/evener-hub/app_host_manage.go`
- `cmd/evener-hub/app_host_manage_update_test.go`
- `docs/superpowers/specs/2026-09-20-multi-host-host-edit-slice.md`
- `docs/superpowers/plans/2026-09-20-multi-host-host-edit-slice.md`

### `4cb60f6426` — `fix(web): budget host updates for gate waits`

- `cmd/evener-hub/frontend/src/stores/hosts.ts`
- `cmd/evener-hub/frontend/src/stores/hosts.test.ts`
- `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.test.tsx`

This report is committed separately at:

- `.superpowers/sdd/2026-09-20-multi-host-host-edit-slice/final-fix-report.md`

No `git add -A` was used.

## Unaddressed findings and concerns

None of the two Important or two requested Minor findings remains unaddressed. The previously deferred §6 items remain deliberately deferred, as required; no omitted-field merge, mutation/generation/incarnation guard, host-count cap, or build/skew signal was added. No subagents or reviewers were dispatched.
