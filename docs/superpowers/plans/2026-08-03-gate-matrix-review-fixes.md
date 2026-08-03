# Gate Matrix Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Correct the three validated review findings without changing the gate boundaries: compile every non-fuzz Go workspace module in CI, clean up browser children on startup failure, and allocate distinct available browser ports.

**Architecture:** Add a small `make build-go` target that reuses the existing `GO_MODULES` list and make CI invoke it after the runtime build. Centralize the Vite/Chrome process lifecycle and OS-backed port allocation for the two Vite-backed browser guards in one dependency-free Node helper, with a focused Node test for cleanup and distinct ports.

**Tech Stack:** GNU Make, GitHub Actions, Node.js ESM, `node:child_process`, `node:fs`, `node:net`, and Node’s built-in test runner.

## Global Constraints

- Preserve deterministic default tests and keep provider/live behavior opt-in.
- Treat `AGENTS.md` and `docs/testing.md` as binding.
- Keep the parent checkout’s intentional deletion untouched.
- Do not add a browser framework, WebKit runner, retry loop, or unrelated refactor.
- Use real child-process lifecycle behavior at the external process boundary; use only injected process startup in the focused lifecycle unit test.
- Verify each fix with a RED-to-GREEN cycle and record bare exit codes.

---

### Task 1: Compile every non-fuzz Go workspace module in CI

**Files:**
- Modify: `Makefile:1` to add `build-go` to `.PHONY`.
- Modify: `Makefile` after `GO_MODULES` to add the explicit all-module build target.
- Modify: `.github/workflows/ci.yml:42-43` to run `make build && make build-go`.
- Modify: `docs/testing.md` to document `make build-go` and change the CI build command reference.

**Interfaces:**
- Consumes: the existing `GO_MODULES := . agent llm auth envvars invariant identifier` list.
- Produces: `make build-go`, which runs `go build ./...` once inside every non-fuzz workspace module and returns nonzero on the first module failure.

- [x] **Step 1: Establish the RED wiring case.**

Run:

```sh
make -n build-go
```

Expected: Make exits nonzero because `build-go` does not yet exist.

- [x] **Step 2: Add the minimal target and CI wiring.**

Add:

```make
build-go:
	@for m in $(GO_MODULES); do (cd $$m && go build ./...) || exit 1; done
```

Change the CI build command to:

```yaml
      - name: Build runtime pair and all Go workspace packages
        run: make build && make build-go
```

- [x] **Step 3: Update the matrix.**

Add a `make build-go` row stating that it compiles every non-fuzz workspace module, is deterministic, is required in the CI build job, and fails on any module compilation error. Change the `make build` CI reference from `make build && go build ./...` to `make build && make build-go`.

- [x] **Step 4: Verify the target.**

Run `make build-go`, `git diff --check`, and `rg -n "build-go|Build runtime pair" Makefile .github/workflows/ci.yml docs/testing.md`. Expected: all seven non-fuzz modules compile and every command reference names the new target.

### Task 2: Make browser startup cleanup-safe and port-safe

**Files:**
- Create: `cmd/serf-hub/frontend/scripts/browserGuardProcess.mjs` for Chrome discovery, OS-backed port allocation, child startup, and cleanup.
- Create: `cmd/serf-hub/frontend/scripts/browserGuardProcess.test.mjs` for lifecycle and port behavior.
- Modify: `cmd/serf-hub/frontend/scripts/overflowguard/run.mjs` to use the shared lifecycle.
- Modify: `cmd/serf-hub/frontend/scripts/spawnguard/run.mjs` to use the shared lifecycle.
- Modify: `cmd/serf-hub/frontend/package.json` so `npm test` runs the focused Node lifecycle test after Vitest.

**Interfaces:**
- `findAvailablePort(excludedPorts)` returns a kernel-selected local port not present in `excludedPorts`.
- `startBrowserGuard({ frontend, profilePrefix, chromeArgs, spawnProcess, chromeBinary })` resolves Chrome before starting children, allocates distinct ports, starts Vite and Chrome, returns their ports and a cleanup function, and cleans all started resources if startup throws.

- [x] **Step 1: Establish the RED lifecycle tests.**

Create `browserGuardProcess.test.mjs` with these two behaviors:

```js
test("allocates distinct local ports", async () => {
  const first = await findAvailablePort();
  const second = await findAvailablePort([first]);
  assert.notEqual(first, second);
  assert.ok(first > 0);
  assert.ok(second > 0);
});

test("cleans Vite when Chrome startup throws", async () => {
  const killed = [];
  let calls = 0;
  const spawnProcess = (command) => {
    calls++;
    if (calls === 1) return { stderr: { on() {} }, kill: () => killed.push(command) };
    throw new Error("chrome startup failed");
  };

  await assert.rejects(
    startBrowserGuard({
      frontend: process.cwd(),
      profilePrefix: "browser-guard-test-",
      chromeBinary: "/fake/chrome",
      spawnProcess,
    }),
    /chrome startup failed/,
  );
  assert.deepEqual(killed, ["./node_modules/.bin/vite"]);
});
```

Run `node --test scripts/browserGuardProcess.test.mjs`. Expected: RED because the helper does not yet exist.

- [x] **Step 2: Implement the helper.**

Use `net.createServer().listen(0, "127.0.0.1")` to obtain each port, close the reservation before returning, reject excluded ports, resolve Chrome before creating the profile directory, define cleanup before either child starts, and kill whichever children have started plus remove the profile on any synchronous startup error.

- [x] **Step 3: Migrate both Vite-backed guards.**

Replace each script’s local `pickPort`, Chrome candidate list, profile creation, child spawning, and cleanup block with `startBrowserGuard`. Preserve each guard’s existing Vite page, viewport sweep, Chrome flags, error text, and final exit code.

- [x] **Step 4: Verify the lifecycle fix.**

Run the focused lifecycle tests, `make test-web`, `make test-web-browser`, and `git diff --check`. Expected: the injected Chrome-start failure kills the Vite child, the ports differ, and all existing frontend guards still pass.

### Task 3: Final verification and commit

**Files:**
- Verify: `Makefile`, `.github/workflows/ci.yml`, `docs/testing.md`, `cmd/serf-hub/frontend/package.json`, the new helper/test, and both browser runners.

- [x] **Step 1: Run the affected build and test gates.**

Run `make build-go`, `make test-web`, `make build-web`, and `make test-web-browser`, recording each exit code.

- [x] **Step 2: Run broader verification.**

Run `go test ./...`, `git diff --check`, `git diff --stat`, and `git status --short --branch`. Run `make merge-approval-gate` if the disk preflight permits; otherwise report its exact environmental failure without lowering the floor.

- [x] **Step 3: Inspect and commit.**

Confirm only the scoped implementation files and this plan changed, then commit with a detailed message describing workspace compilation, browser lifecycle cleanup, OS-backed ports, tests, and any disk limitation.
