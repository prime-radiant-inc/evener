# Clean, Residue-Free Test Suite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `make build && make test` produce concise successful output and leave no undeclared per-run system residue.

**Architecture:** Keep complete output captured at each existing test-stream boundary and replay it only when that stream fails. Give every canonical test stream a private `TMPDIR` beneath `run-module-tests.sh`'s already-owned run directory, so green cleanup removes all process-lifetime scratch while red runs retain the same evidence tree as their logs. Use a disposable Apple container cold/warm filesystem and runtime comparison to find anything outside that boundary without making container support a gate dependency.

**Tech Stack:** Bash, GNU Make, Go 1.26, Node.js 26, Vitest 4, Node's built-in test runner, Biome 2, Apple container 1.2.

## Global Constraints

- Address Jesse as Jesse in all reports and questions.
- Make the smallest reasonable change; do not redesign the scheduler or rewrite implementations.
- Successful `make test` output is concise `PASS` summaries with timings; complete output is replayed only for failed streams.
- Never filter final output text in a way that could hide a new failure.
- Each frontend test file has exactly one runner.
- Tests exercise real behavior and meaningful contracts; do not regex-match large generated scripts, commands, JSON, or output snapshots.
- Default tests remain deterministic, offline, and independent of provider credentials or ambient developer-machine state.
- Do not widen or hardcode a timeout to absorb awaitable work.
- Failure diagnostics and their owned scratch may be retained; successful runs must remove per-run scratch, temporary homes, profiles, sockets, listeners, child processes, and undeclared files.
- Reusable Go, npm, and dependency caches and intended repository build outputs are declared state, not silently ignored state; the whole-container audit must name them.
- Before changing tests, follow `docs/testing.md`; before completing frontend work, run `npx biome check --write` on every touched frontend file, `make test-web`, and `make test-web-browser` on this Chrome-capable host.
- Never skip, disable, or evade pre-commit hooks.

---

### Task 1: Give Every Frontend Test One Fast, Correct Runner

**Files:**
- Modify: `cmd/serf-hub/frontend/package.json`
- Modify: `cmd/serf-hub/frontend/scripts/layoutguard/cdp.mjs`
- Modify: `cmd/serf-hub/frontend/scripts/layoutguard/cdp.test.mjs`

**Interfaces:**
- Consumes: `probeBrowserCapability(spawnProcess)` and the existing `npm test` script.
- Produces: `probeBrowserCapability(spawnProcess = spawn, waitForCdpProbe = waitForCdp)`; Vitest owns `viewport.test.mjs`, while Node owns `browserGuardProcess.test.mjs` and `layoutguard/cdp.test.mjs`.

- [ ] **Step 1: Make the CDP failure tests inject the awaited failure**

In `cdp.test.mjs`, retain the real fake process but remove repeated factory bodies by adding these shared helpers:

```js
const chromeStartupSentinel = "injected CDP startup failure";

function spawnChromeThatNeverStarts() {
  const proc = new EventEmitter();
  proc.stderr = new EventEmitter();
  proc.kill = () => {};
  return proc;
}

async function failCdpProbe() {
  throw new Error(chromeStartupSentinel);
}
```

Pass both helpers to every call:

```js
probeBrowserCapability(spawnChromeThatNeverStarts, failCdpProbe)
```

In the first rejection predicate, also assert that `err.message` contains
`chromeStartupSentinel`. Keep the stderr test's scheduled `data` event by using
a one-off wrapper that calls `spawnChromeThatNeverStarts()` and emits on its
`stderr` object.

- [ ] **Step 2: Run one focused Node test to verify red**

Run:

```bash
cd cmd/serf-hub/frontend
node --test --test-name-pattern='includes Chrome binary path' scripts/layoutguard/cdp.test.mjs
```

Expected: FAIL because the existing one-argument implementation ignores
`failCdpProbe`, waits for the real CDP polling loop, and reports
`chrome devtools endpoint never came up` instead of the injected sentinel.

- [ ] **Step 3: Await the injected CDP probe in production**

Change only the probe signature and awaited call in `cdp.mjs`:

```js
export async function probeBrowserCapability(spawnProcess = spawn, waitForCdpProbe = waitForCdp) {
  // existing setup remains unchanged
  await waitForCdpProbe(port);
  // existing success and error handling remains unchanged
}
```

Update the JSDoc with a second `@param` describing the injected async CDP
readiness probe. Do not change the real `waitForCdp` polling behavior.

- [ ] **Step 4: Assign the Node test files explicitly**

Set the package script to this exact command:

```json
"test": "vitest run --exclude scripts/browserGuardProcess.test.mjs --exclude scripts/layoutguard/cdp.test.mjs && node --test scripts/browserGuardProcess.test.mjs scripts/layoutguard/cdp.test.mjs"
```

Do not exclude `scripts/layoutguard/viewport.test.mjs`; it imports Vitest and
must remain in that runner.

- [ ] **Step 5: Format and verify the focused frontend behavior**

Run:

```bash
cd cmd/serf-hub/frontend
npx biome check --write package.json scripts/layoutguard/cdp.mjs scripts/layoutguard/cdp.test.mjs
node --test scripts/layoutguard/cdp.test.mjs
npm test
```

Expected: the Node tests pass in well under the former 61-second polling cost;
Vitest reports no `No test suite found` error; all frontend tests pass.

- [ ] **Step 6: Run the canonical frontend gates**

Run from the repository root:

```bash
make test-web
make test-web-browser
```

Expected: both commands exit zero. `make test-web` prints three concise PASS
lines and no internal debug output or stack trace.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-hub/frontend/package.json cmd/serf-hub/frontend/scripts/layoutguard/cdp.mjs cmd/serf-hub/frontend/scripts/layoutguard/cdp.test.mjs
git commit -m "fix(webtest): isolate Node tests from Vitest"
```

The detailed commit body must describe the duplicate/wrong runner root cause,
the injected awaitable that removes six real ten-second waits, and the commands
used to verify it.

### Task 2: Review and Register the Frontend Content Hash

**Files:**
- Modify: `identifier_audit_test.go`

**Interfaces:**
- Consumes: `identifierSHA256Inventory` and `frontendDistHash` in `cmd/serf-hub/frontend_hash.go`.
- Produces: an exact closed-world audit entry for `sha256.New()` in `frontendDistHash`; no hash implementation changes.

- [ ] **Step 1: Verify the existing audit is red**

Run:

```bash
go test . -run '^TestIdentifierAudit$' -count=1
```

Expected: FAIL naming `cmd/serf-hub/frontend_hash.go`, the `crypto/sha256`
import, and its missing closed-world inventory entry.

- [ ] **Step 2: Add the reviewed inventory entry**

Add this entry beside the other `cmd/serf-hub` content hashes:

```go
	// Fingerprints the complete embedded frontend distribution so operators can
	// identify deployed assets. The digest describes content; it is not a domain identifier.
	"cmd/serf-hub/frontend_hash.go": {"frontendDistHash": {"New()": true}},
```

Do not add backward compatibility or change the hash algorithm.

- [ ] **Step 3: Verify the audit and hash behavior**

Run:

```bash
go test . -run '^TestIdentifierAudit$' -count=1
go test ./cmd/serf-hub -run '^TestFrontendDistHash' -count=1
```

Expected: both commands pass.

- [ ] **Step 4: Commit**

```bash
git add identifier_audit_test.go
git commit -m "test(identifier): review frontend content hash"
```

The detailed commit body must explain why the digest is content identity rather
than a Serf domain identifier and record the focused test evidence.

### Task 3: Remove the Dev-Tooling Test's Ambient Wall-Clock Assertion

**Files:**
- Modify: `cmd/serf-test-dev-tooling/wave_test.go`

**Interfaces:**
- Consumes: `runWave`, injected `checkLeaksFn`, and `checkLeaksTimeout`.
- Produces: a regression test whose completion is proven by `runWave` returning and whose behavior is proven by structured exit/output assertions, with no ambient two-second performance assertion.

- [ ] **Step 1: Reproduce the sighted flake**

Use the captured baseline as red evidence, then run the focused test once:

```bash
go test ./cmd/serf-test-dev-tooling -run '^TestWaveCompletesDespiteBlockedLeakCheck$' -count=1
```

The captured canonical run failed with:

```text
wave took 3.185793917s, should complete within ~2s despite blocked leak check
```

The failure is scheduler/load time, not a violated wave contract: `runWave`
returned and the later assertions can directly prove the timed-out leak check,
the other suite's PASS, and the nonzero result.

- [ ] **Step 2: Delete only the ambient timing assertion**

Remove `start := time.Now()`, `elapsed := time.Since(start)`, and this block:

```go
if elapsed > 2*time.Second {
	t.Fatalf("wave took %v, should complete within ~2s despite blocked leak check; suggests fix didn't work", elapsed)
}
```

Update the numbered comment so it says that the call returning proves the wave
completed, while the remaining assertions prove the timeout result and sibling
progress. Keep the production timeout injection and all four behavioral
assertions. Do not widen either timeout.

- [ ] **Step 3: Loop the focused regression**

Run:

```bash
go test ./cmd/serf-test-dev-tooling -run '^TestWaveCompletesDespiteBlockedLeakCheck$' -count=50
```

Expected: PASS 50 times with no wall-clock assertion.

- [ ] **Step 4: Run the package suite**

Run:

```bash
go test ./cmd/serf-test-dev-tooling -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-test-dev-tooling/wave_test.go
git commit -m "test(wave): assert blocked leak behavior directly"
```

The detailed commit body must record the sighted 3.185-second failure and explain
why return plus result assertions are causal evidence while a two-second wall
bound is ambient-machine state.

### Task 4: Put Every Canonical Test Stream Under One Cleanup Owner

**Files:**
- Modify: `scripts/run-module-tests.sh`
- Modify: `scripts/run-module-tests-selftest.sh`
- Modify: `scripts/reclaim-test-debris.sh`
- Modify: `docs/testing.md`

**Interfaces:**
- Consumes: the runner's existing `logdir`, `cleanup` trap, `logpath(name)`, and failure-log retention contract.
- Produces: `tmppath(name)`, a private per-stream `TMPDIR` under `$logdir/tmp`; green cleanup removes it with `$logdir`, while failed/interrupted runs retain it beside their logs.

- [ ] **Step 1: Extend the real runner selftest with per-stream residue**

In both fake executables in `run-module-tests-selftest.sh`, record the stream and
its received `TMPDIR`, then create a marker there. For fake Go, add after module
selection:

```bash
mkdir -p "$TMPDIR"
printf '%s\t%s\n' "$module" "$TMPDIR" >>"$FAKE_STATE/tmpdirs"
: >"$TMPDIR/go-residue"
```

For fake Make, add after `stream=web`:

```bash
mkdir -p "$TMPDIR"
printf '%s\t%s\n' "$stream" "$TMPDIR" >>"$FAKE_STATE/tmpdirs"
: >"$TMPDIR/web-residue"
```

Add this helper:

```bash
recorded_tmpdirs() {
	cut -f2 "$state/tmpdirs" 2>/dev/null | sort -u
}
```

After the all-pass assertions, require four unique roots and require that every
recorded root is gone:

```bash
assert_eq "$(recorded_tmpdirs | wc -l | tr -d ' ')" "4" "every passing stream owns a distinct temporary root"
leftovers="$(while IFS= read -r dir; do [ ! -e "$dir" ] || printf '%s\n' "$dir"; done < <(recorded_tmpdirs))"
assert_eq "$leftovers" "" "a successful run removes every stream's temporary root"
```

- [ ] **Step 2: Run the selftest to verify red**

Run:

```bash
scripts/run-module-tests-selftest.sh
```

Expected: FAIL because all fake streams currently inherit the same ambient
`case_dir`, and that directory still exists after the successful run.

- [ ] **Step 3: Add per-stream temporary roots to the runner**

Beside `logpath`, add the matching normalized path helper:

```bash
tmppath() { printf '%s/tmp/%s' "$logdir" "$(printf '%s' "$1" | tr '/.' '__')"; }
```

In `run_wave`, resolve `tmp="$(tmppath "$m")"` and launch each module as:

```bash
( mkdir -p "$tmp" && cd "$m" && TMPDIR="$tmp" run_module "$m" "$extra" ) >"$log" 2>&1 &
```

Add `tmp` to the function's locals. For the frontend stream, resolve
`web_tmp="$(tmppath web)"` and launch it as:

```bash
( mkdir -p "$web_tmp" && TMPDIR="$web_tmp" /usr/bin/time -p "${MAKE:-make}" test-web ) >"$(logpath web)" 2>&1 &
```

Do not change the cleanup trap: removing `$logdir` on green is the ownership
mechanism, and retaining `$logdir` on red preserves logs plus the exact scratch
that produced them.

- [ ] **Step 4: Verify green cleanup and red retention**

Run:

```bash
scripts/run-module-tests-selftest.sh
scripts/agent-test-shards-selftest.sh
```

Expected: both selftests pass. The module selftest proves distinct roots are
removed after success and existing failure/interruption cases still retain the
named log directory. The shard selftest proves nested shard cleanup still works
under an inherited private `TMPDIR`.

- [ ] **Step 5: Correct documentation to match the actual owner**

In `docs/testing.md`, extend the `make test` gate description and Post-Merge
Gate discussion with these exact facts:

- every Go module and frontend stream receives a distinct private `TMPDIR`
  beneath the runner's per-run log directory;
- a green run removes the complete directory, including test-created process
  lifetime scratch and Node compile cache;
- a failed or interrupted run retains that directory and prints its path so
  diagnostics are not destroyed; and
- standard reusable caches outside `TMPDIR` are audited separately rather than
  claimed as temporary cleanup.

In `reclaim-test-debris.sh`, correct the header's first item: stale
`agent-test-shards.*` directories come from failed or interrupted historical
runs; current green runs remove their directories. Do not change its deletion
behavior.

- [ ] **Step 6: Run the canonical test gate with an independently empty outer temp root**

Run:

```bash
audit_tmp="$(mktemp -d -t serf-green-test-audit.XXXXXX)"
TMPDIR="$audit_tmp" make test
find "$audit_tmp" -mindepth 1 -print
rmdir "$audit_tmp"
```

Expected: `make test` exits zero with only concise PASS summaries; `find` prints
nothing; `rmdir` succeeds. If the gate fails, retain its reported internal log
directory and root-cause the failure before proceeding.

- [ ] **Step 7: Commit**

```bash
git add scripts/run-module-tests.sh scripts/run-module-tests-selftest.sh scripts/reclaim-test-debris.sh docs/testing.md
git commit -m "fix(test): own and clean every stream temp root"
```

The detailed commit body must record the baseline evidence (3,853 entries,
including 3,810 `serf-sandbox-*` directories and Node compile cache), explain
green removal versus red retention, and list the selftest and canonical-gate
evidence.

### Task 5: Prove the Complete Build/Test Cycle in Apple Container

**Files:**
- Modify: `docs/testing.md`
- Runtime evidence: this plan's ignored SDD workspace under `.superpowers/sdd/2026-08-07-clean-residue-free-test-suite/`

**Interfaces:**
- Consumes: the committed branch, Apple container 1.2, and the successful `make build && make test` contract from Tasks 1-4.
- Produces: cold and warm whole-filesystem manifests, process/listener/socket snapshots, captured concise outputs, and a documented classification of every changed path.

- [ ] **Step 1: Create an ignored audit context**

Under this plan's SDD workspace, create `audit/Dockerfile` with:

```dockerfile
FROM golang:1.26.5-bookworm AS go
FROM node:26.5.0-bookworm
COPY --from=go /usr/local/go /usr/local/go
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential ca-certificates git make procps python3 iproute2 \
    && rm -rf /var/lib/apt/lists/*
ENV PATH=/usr/local/go/bin:$PATH
WORKDIR /work/serf
```

Build it:

```bash
container build --progress plain -t serf-test-audit:2026-08-07 audit
```

- [ ] **Step 2: Start one disposable audit container from the clean image**

Create a committed-source archive and copy it into the container:

```bash
git archive --format=tar -o "$SDD_WORKSPACE/audit/serf.tar" HEAD
container create --name serf-test-audit --cpus 10 --memory 16G serf-test-audit:2026-08-07 sleep infinity
container start serf-test-audit
container copy "$SDD_WORKSPACE/audit/serf.tar" serf-test-audit:/tmp/serf.tar
container exec serf-test-audit bash -lc 'tar -xf /tmp/serf.tar -C /work/serf && rm /tmp/serf.tar'
```

The source archive contains committed files only, so the audit cannot inherit
host caches, ignored assets, or ambient uncommitted changes.

- [ ] **Step 3: Capture the pre-cycle whole-system state**

For each filesystem snapshot, capture paths/metadata and file hashes while
excluding only virtual kernel filesystems:

```bash
container exec serf-test-audit bash -lc \
  'find / \( -path /proc -o -path /sys -o -path /dev -o -path /run \) -prune -o -printf "%y|%m|%u:%g|%p|%l\n" | LC_ALL=C sort; printf "--- SHA256 ---\n"; find / \( -path /proc -o -path /sys -o -path /dev -o -path /run \) -prune -o -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum' \
  >"$SDD_WORKSPACE/audit/before.fs"
container exec serf-test-audit ps -ef >"$SDD_WORKSPACE/audit/before.ps"
container exec serf-test-audit ss -lntup >"$SDD_WORKSPACE/audit/before.listeners"
container exec serf-test-audit bash -lc \
  'find / \( -path /proc -o -path /sys -o -path /dev -o -path /run \) -prune -o -type s -print | LC_ALL=C sort' \
  >"$SDD_WORKSPACE/audit/before.sockets"
```

- [ ] **Step 4: Run and snapshot the cold cycle**

Run:

```bash
container exec -w /work/serf serf-test-audit bash -lc 'make build && make test' \
  >"$SDD_WORKSPACE/audit/cold.output" 2>&1
```

Capture `after-cold.fs`, `after-cold.ps`, `after-cold.listeners`, and
`after-cold.sockets` with the exact commands from Step 3.

Expected: exit zero. The test portion contains concise PASS summaries; no test
debug output or stack traces appear.

- [ ] **Step 5: Run and snapshot the warm cycle**

Run the same `make build && make test` command again, capturing
`warm.output`, then capture `after-warm.*` with the Step 3 commands.

Expected: exit zero. The only persistent cold-cycle additions are declared
repository build/dependency outputs and reusable caches, expected under:

```text
/work/serf/serf
/work/serf/serf-hub
/work/serf/cmd/serf-hub/frontend/dist
/work/serf/cmd/serf-hub/frontend/node_modules
/root/.cache/go-build
/root/.npm
/go/pkg/mod
```

Build metadata may replace the two runtime binaries on the warm cycle. No warm
cycle may add a per-run scratch tree, temporary home, browser profile, log,
socket, listener, or child process. Any changed path outside the declared list
is a failed task: root-cause it, amend this plan with the exact remediation as a
new task, and do not classify it away.

- [ ] **Step 6: Record the measured contract in testing documentation**

Add a concise `Whole-system residue audit` subsection to `docs/testing.md` that
records:

- the audit date, Apple container version, Go/Node image versions, and cold/warm method;
- the exact declared output/cache roots observed;
- that virtual kernel filesystems were the only filesystem exclusions;
- whether the warm comparison found zero undeclared per-run paths;
- whether process, listener, and socket state returned to the baseline; and
- that Apple container remains diagnostic evidence rather than a local/CI gate dependency.

Do not claim zero residue unless the saved manifests prove it.

- [ ] **Step 7: Clean up the audit runtime and commit only documentation**

Stop and delete the exact named container, delete the exact audit image, remove
the archive and generated manifests from this plan's ignored workspace after
their findings have been recorded, and stop the Apple container system service
because it was stopped before this work began:

```bash
container stop serf-test-audit
container delete serf-test-audit
container image delete serf-test-audit:2026-08-07
container system stop
```

Then commit:

```bash
git add docs/testing.md
git commit -m "docs(test): record whole-system residue audit"
```

The detailed commit body must list the cold declared state, warm diff verdict,
runtime-state verdict, and exact image/tool versions.

### Task 6: Final Canonical Verification

**Files:**
- Verification only; no planned code changes.

**Interfaces:**
- Consumes: all prior task commits.
- Produces: final evidence that the branch meets the approved design without uncommitted residue.

- [ ] **Step 1: Format all touched frontend files**

Run:

```bash
cd cmd/serf-hub/frontend
npx biome check --write package.json scripts/layoutguard/cdp.mjs scripts/layoutguard/cdp.test.mjs
```

Expected: exit zero and no uncommitted formatting change.

- [ ] **Step 2: Run focused tooling and frontend gates**

Run from the repository root:

```bash
make test-dev-tooling
make test-web
make test-web-browser
```

Expected: all exit zero with concise successful output.

- [ ] **Step 3: Run the full intended build/test cycle**

Run:

```bash
make build
make test
```

Expected: both exit zero; `make test` prints only concise PASS summaries.

- [ ] **Step 4: Verify repository state**

Run:

```bash
git diff --check
git status --short
```

Expected: no whitespace errors and no uncommitted or untracked files from the
build/test cycle.

- [ ] **Step 5: Commit only if verification changed tracked generated output**

No commit is expected. If a documented generated file changes, inspect it and
commit it intentionally with a detailed message; never use `git add -A`.
