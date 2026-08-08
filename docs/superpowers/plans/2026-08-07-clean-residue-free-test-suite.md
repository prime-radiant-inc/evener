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
- Cleanup follows ownership: Serf removes scratch at application lifecycle points that prove it is disposable; normal session end alone is insufficient because sessions can resume or hand artifacts to a human. The runner removes remaining test-process-owned scratch only after green process exit.
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

This task deliberately does not change `Session.Close` or turn session end into
an unconditional scratch deletion point. The private root is owned by the test
process runner; removing it after every green child process exits cannot break a
later session resume. If investigation finds scratch from an application path
that already proves the session/candidate/lane is unadopted or disposed, fix
that application's owner instead of relying on this outer cleanup.

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
- this process-exit cleanup is not a session-end policy and does not delete
  scratch that a resumable live application still owns;
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
- how each Serf-owned scratch finding was assigned either to an application
  lifecycle owner or to the enclosing test-process owner, including why no
  resumable session still owned anything removed; and
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

### Task 5A: Remediate the First Whole-System Audit

The first cold/warm audit passed both cycles with concise output, but its
filesystem comparison was red. It found Go telemetry and Serf state under the
ambient user directories, Node and Biome state outside the stream `TMPDIR`, a
WSL clipboard test-created `/mnt/t`, repeated Git index refreshes, and an audit
image whose copied Go toolchain had lost `GOPATH=/go`. These findings may not be
added to the declared cache list merely to make the comparison green.

A host run of the canonical browser gate exposed a second residue mechanism:
layoutguard printed PASS while Chrome main/crashpad children remained parented
to PID 1, overflowguard did not finish, and interrupting Make left Vite and
Chrome alive. The browser helpers only called `child.kill()` without awaiting
exit, deleted profiles immediately, and the runners forced `process.exit`;
Make also signalled the npm wrapper instead of the Node process that owned
cleanup. Chrome crashpad independently wrote beneath the ambient application
support directory despite the private browser profile.

On macOS with Chrome 151, the fresh-profile overflow guard also blocked before
its first network socket: CDP `Page.navigate` remained pending until Chrome was
terminated because the network service was waiting on the real keychain.
Adding only `--use-mock-keychain` made the same navigation return in 14 ms and
the complete overflow sweep finish in 2.49 seconds. Separately,
`--disable-crash-reporter` did not prevent Chrome from advancing the ambient
Crashpad `settings.dat` mtime; setting `BREAKPAD_DUMP_LOCATION` beneath the
owned profile kept the ambient file unchanged and created the 40-byte settings
database only inside the disposable root.

That redirect then exposed the last lifecycle race: macOS Crashpad double-forks
to PID 1 in a process group separate from Chrome, then briefly keeps writing the
private database after the browser group disappears. Immediate profile removal
therefore intermittently failed with `ENOTEMPTY`. The browser owner must
request `Browser.close` over the browser CDP endpoint and give that orderly
shutdown the existing bounded grace window. An unreachable, rejected, or
still-pending CDP close falls back to TERM and then KILL through the same
lifecycle. On POSIX, Chrome and Vite launch in detached process groups, and
profile removal waits until each exact group no longer exists. On macOS,
cleanup captures before shutdown and rescans after the groups disappear for
only the Crashpad handler whose command contains the canonical random profile's
exact `--database=.../Crashpad` argument. It observes that escaped identity for
a bounded grace period without signaling the reusable numeric PID. If the
handler remains, cleanup fails and retains the private profile as evidence.

**Files:**
- Modify: `Makefile`
- Modify: `scripts/build-runtime-pair.sh`
- Create: `scripts/private-go-home.sh`
- Create: `scripts/private-go-home-selftest.sh`
- Modify: `scripts/run-module-tests.sh`
- Modify: `scripts/run-module-tests-selftest.sh`
- Modify: `runtime_pair_build_test.go`
- Modify: `cmd/serf-hub/frontend/scripts/browserGuardProcess.mjs`
- Modify: `cmd/serf-hub/frontend/scripts/browserGuardProcess.test.mjs`
- Modify: `cmd/serf-hub/frontend/scripts/layoutguard/cdp.mjs`
- Modify: `cmd/serf-hub/frontend/scripts/layoutguard/cdp.test.mjs`
- Modify: `cmd/serf-hub/frontend/scripts/layoutguard/run.mjs`
- Modify: `cmd/serf-hub/frontend/scripts/overflowguard/run.mjs`
- Modify: `cmd/serf-hub/frontend/scripts/spawnguard/run.mjs`
- Modify: `cmd/llmcall/coverage_test.go`
- Modify: `cmd/serf-tui/internal/clipboard/clipboard_paste_test.go`
- Modify: `docs/testing.md`
- Modify only in ignored audit evidence: `audit/Dockerfile`

- [ ] **Step 1: Contain Go process state without discarding reusable caches**

Use one sourced POSIX helper to create a private `HOME` and XDG roots beneath
the build stage or module-stream `TMPDIR`. Copy the caller's GOENV file into
that owned root so subprocess writes cannot mutate ambient configuration, and
preserve explicit, configured, or platform-default `GOPATH` and `GOCACHE`
locations. Prove green removal and failed/interrupted retention with the real
runner selftest and prove the helper behavior directly.

- [ ] **Step 2: Contain frontend process state**

Disable Node's automatic compile cache for preflight/build/browser commands and
give each concurrent `test-web` check its own private `HOME`, temporary, and XDG
roots. Exercise the real Make targets with fake npm so the test observes
structured environment inputs and verifies successful cleanup.

For the browser gate, use one reusable async child lifecycle that sends TERM,
awaits the owned process lifetime, escalates to KILL only after a bounded grace
period, removes the Chrome profile after every owned process is gone, and is
idempotent. POSIX Chrome and Vite children launch in detached process groups;
cleanup observes each exact group until `kill(-pgid, 0)` reports `ESRCH`, while
Win32 and test fakes retain direct-child exit handling.
Chrome owners first await a CDP `Browser.close`; successful close receives that
same grace window to finish orderly shutdown, while failed or pending close
causally enters the TERM-to-KILL fallback.
Because macOS Crashpad escapes Chrome's process group, scan both before closing
Chrome and after its process group disappears. Capture a PID only when its
command names `chrome_crashpad_handler` and the canonical random profile's
exact `--database=.../Crashpad` argument, then deduplicate the two scans.
Observe that exact identity for a bounded grace period but never signal its
numeric PID; if it remains, reject cleanup and retain the profile rather than
risk targeting a reused process.
Install temporary SIGINT/SIGTERM handlers that await that same cleanup before
exit; every layoutguard, overflowguard, and spawnguard runner must await cleanup
and set an exit code instead of forcing early process exit. Invoke those Node
entrypoints directly from Make, give each one private HOME/TMPDIR/XDG roots,
disable Chrome crash reporting in every launch argument set, add
`--use-mock-keychain` for Darwin fresh profiles, and set
`BREAKPAD_DUMP_LOCATION` to the private profile's `Crashpad` directory for
every Chrome child. Capture each guard's output separately: green prints only
the three verdicts and removes its owned root, while red replays only failed
guard logs and retains and names the root. Clear tracked PIDs after Make waits
so an EXIT trap cannot signal an already-reaped or reused PID.

The RED contracts are behavior-level fake-child tests for profile-removal
ordering, TERM-to-KILL escalation, idempotence, signal-handler cleanup, and
crash-reporting arguments, Darwin mock-keychain arguments, the Chrome child's
private `BREAKPAD_DUMP_LOCATION`, exact escaped-helper discovery before and
after group shutdown, stuck-helper profile retention, and PID-reuse rejection,
plus real Make fixtures whose fake direct Node process causally acknowledges
TERM and withholds exit until released and whose guard output proves concise
green verdicts versus failed-log replay and retention. The focused Node command is
`node --test scripts/browserGuardProcess.test.mjs scripts/layoutguard/cdp.test.mjs`
(27 tests after these contracts). GREEN requires those focused Node and Go
tests, `make test-web`, and the canonical `make test-web-browser` gate on a
Chrome-capable host; a printed browser PASS is not sufficient unless the
process and filesystem residue audits are also clean.

- [ ] **Step 3: Remove the two direct test leaks at their causal boundaries**

Make `TestPasteClipboardImage_FallsBackToWSL` fake only the filesystem stat for
the exact converted path while retaining the real path conversion and result
assertions; it must not write under `/mnt`. Make
`TestLLMCallProfilesAndOptions` inject an empty client and assert the intended
unknown-provider failure instead of constructing ambient provider state before
the behavior under test.

- [ ] **Step 4: Prevent a read-only build metadata query from refreshing Git state**

Use a no-optional-locks `diff-files --quiet` dirty probe and prove the index
hash is unchanged across `make build`; keep the same tracked-worktree dirty
semantics as the existing build metadata.

- [ ] **Step 5: Correct the audit image and repeat Task 5 from a clean image**

Set `GOPATH=/go` and include it in `PATH` in the audit image, rebuild it, create
a new container from the reviewed committed HEAD, and replace every cold/warm
manifest. Only a new comparison with zero undeclared paths may proceed to Task
5 documentation and cleanup.

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
npx biome check --write package.json scripts/browserGuardProcess.mjs scripts/browserGuardProcess.test.mjs scripts/layoutguard/cdp.mjs scripts/layoutguard/cdp.test.mjs scripts/layoutguard/run.mjs scripts/overflowguard/run.mjs scripts/spawnguard/run.mjs
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
