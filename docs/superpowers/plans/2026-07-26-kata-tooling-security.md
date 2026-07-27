# Kata Tooling and Security Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the clean-worktree Go install test deterministic and update the frontend's vulnerable React Router dependency to its upstream-patched release.

**Architecture:** Keep `web-preflight` as the production gate for a real local TypeScript compiler. Make the test-only npm shim model the minimum successful `npm ci` result, and exercise the real preflight recipe in a temporary fixture with no frontend dependencies while the existing install integration test covers the full install contract. Upgrade only the direct `react-router` declaration and its lockfile entry from v8.2.0 to the first patched v8.3.0 release, then verify the actual SPA/library-mode behavior and audit contracts.

**Tech Stack:** Go `testing`, GNU Make, POSIX shell fixtures, npm/package-lock v3, React/TypeScript/Vite, Kata.

## Global Constraints

- Default Go tests remain deterministic and must not require provider credentials, network access, quota, ambient npm installs, or model behavior.
- Do not delete or overwrite this worktree's existing `cmd/serf-hub/frontend/node_modules`; clean-state reproduction uses an isolated fixture or controlled dependency stub.
- q1bt must preserve the real `web-preflight` behavior; do not skip the install test or disable the preflight globally.
- q1bt tests assert installed artifacts and tool behavior, not generated Makefile command text.
- fkbh upgrades only the necessary direct/transitive packages; do not run a broad dependency refresh or `npm audit fix`.
- Verify React Router advisory GHSA-qwww-vcr4-c8h2 against its first patched version 8.3.0. The advisory's lack of a patched v7 line is context only; this repository is already on v8, so fkbh is a same-major v8.2.0 -> v8.3.0 upgrade. Record exact before/after versions, evidence links, and test commands in both Kata comments.
- Verify the exact SPA/library-mode imports and behavior; do not claim this app was exploitable without evidence that it uses unstable RSC APIs.
- Do not close q1bt or fkbh.

---

### Task 1: Lock the clean-install regression contract

**Files:**
- Modify: `install_test.go`

**Interfaces:**
- Consumes: the real `Makefile` `install -> build-web -> web-preflight` dependency chain and the existing temporary frontend/toolchain fixture helpers.
- Produces: a deterministic behavioral test proving the real preflight bootstraps successfully with no pre-existing frontend `node_modules`, plus a test npm shim that creates the local `tsc` behavior required by the real preflight.

- [ ] **Step 1: Write the failing behavioral test**

Add a root-module test that copies the real `Makefile` into `t.TempDir`, creates only its `cmd/serf-hub/frontend` directory and a lockfile, leaves `node_modules` absent, runs the real `make web-preflight` with the controlled npm environment, and asserts the resulting local `tsc --version` behavior. This tests the executable preflight contract in an isolated fixture, not the rendered Makefile text.

The test shape is:

```go
func TestWebPreflightBootstrapsMissingFrontendDependencies(t *testing.T) {
	t.Parallel()
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	fixtureRoot := t.TempDir()
	frontendDir := filepath.Join(fixtureRoot, "cmd", "serf-hub", "frontend")
	if err := os.MkdirAll(frontendDir, 0o755); err != nil {
		t.Fatalf("mkdir frontend: %v", err)
	}
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fixtureRoot, "Makefile"), makefile, 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(frontendDir, "package-lock.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write package-lock.json: %v", err)
	}
	env := installTestEnv(t, t.TempDir(), nil)
	runCommand(t, fixtureRoot, npmShimEnv(t, env), "make", "web-preflight")
	tscInfo, err := os.Stat(filepath.Join(frontendDir, "node_modules", ".bin", "tsc"))
	if err != nil {
		t.Fatalf("preflight did not install local tsc: %v", err)
	}
	if tscInfo.Mode().Perm()&0o111 == 0 {
		t.Fatalf("local tsc is not executable: mode %s", tscInfo.Mode())
	}
}
```

- [ ] **Step 2: Run the focused test to verify the expected failure**

Run:

```bash
go test -count=1 -run '^TestWebPreflightBootstrapsMissingFrontendDependencies$' .
```

Expected: FAIL during `web-preflight` because the test's npm shim currently exits from `npm ci` without creating `node_modules/.bin/tsc`.

- [ ] **Step 3: Implement the smallest fixture fix**

Update `npmShimEnv`'s fake `npm ci` implementation so it creates `node_modules/.bin/tsc`, writes a `Version 6.0.3` response for `--version`, and marks it executable. Leave the production Makefile guard and the no-network/no-provider test boundary unchanged.

- [ ] **Step 4: Run the focused q1bt tests to verify green behavior**

Run:

```bash
go test -count=1 -run '^(TestInstallHomeGeneratedHome|TestWebPreflightBootstrapsMissingFrontendDependencies)$' .
```

Expected: PASS, with the existing install layout/runtime assertions and the new clean-dependency fixture assertion both exercising the real preflight/install path without network or provider access.

- [ ] **Step 5: Commit q1bt**

```bash
git add install_test.go
git commit -m "test: make clean install coverage deterministic"
```

Append a ready-for-review comment to q1bt naming the commit, the original red reproduction, the root cause, and the exact focused tests. Leave q1bt open.

### Task 2: Upgrade the vulnerable React Router package

**Files:**
- Modify: `cmd/serf-hub/frontend/package.json`
- Modify: `cmd/serf-hub/frontend/package-lock.json`

**Interfaces:**
- Consumes: the existing direct `react-router` dependency and the app's hand-rolled routing implementation.
- Produces: a lockfile-resolved `react-router` 8.3.0 dependency as the deliberate same-major v8.2.0 -> v8.3.0 upgrade, with no unrelated lockfile churn.

- [ ] **Step 1: Confirm the application compatibility boundary**

Verify the exact SPA/library-mode routing imports and behavior in `cmd/serf-hub/frontend/src`, including the current absence of runtime imports from `react-router` or `react-router-dom`; record the current direct and lockfile versions as `^8.2.0` and `8.2.0`. Do not infer exploitability: the advisory only affects unstable RSC APIs, and no RSC evidence is present unless inspection finds otherwise.

- [ ] **Step 2: Update only the direct dependency**

Change the direct declaration to `"react-router": "^8.3.0"` and regenerate only the corresponding lockfile package metadata with npm, preserving unrelated lockfile entries. This stays on the existing v8 major and selects the advisory's `first_patched_version` 8.3.0; the lack of a patched v7 line is not a migration concern for this app.

- [ ] **Step 3: Run the dependency checks**

Run from `cmd/serf-hub/frontend`:

```bash
npm ci
npm audit
npm run typecheck
npm run test
npm run lint
npm run build
```

Expected: the audit no longer reports GHSA-qwww-vcr4-c8h2, and all frontend checks/build complete successfully without changing the SPA's observed behavior.

- [ ] **Step 4: Inspect dependency scope and commit fkbh**

Use `git diff -- package.json package-lock.json` and `npm ls react-router` to confirm only the necessary package changed and resolves to 8.3.0, then commit:

```bash
git add cmd/serf-hub/frontend/package.json cmd/serf-hub/frontend/package-lock.json
git commit -m "fix(frontend): upgrade patched react-router release"
```

Append a ready-for-review comment to fkbh naming the commit, exact before/after versions, the GitHub Advisory API/advisory and upstream release links, the `first_patched_version` evidence, compatibility finding, lack of RSC exploitability evidence, `npm audit`, and all frontend verification commands. Leave fkbh open.

### Task 3: Run final repository verification

**Files:**
- Verify: all changed files and repository status

**Interfaces:**
- Consumes: the q1bt and fkbh commits.
- Produces: fresh evidence for the narrow clean-worktree Go test, repository build, frontend checks, and a clean worktree at a reported HEAD.

- [ ] **Step 1: Run focused and clean-state verification**

Run the focused q1bt tests in the root module, the relevant runtime fixture tests, and the repository build. If a clean frontend dependency state is needed, use a temporary fixture or controlled PATH rather than removing an existing worktree install.

- [ ] **Step 2: Run the required final checks**

Run the narrowest meaningful clean-worktree equivalent of `go test ./...`, `make build`, and the frontend audit/full checks, capturing exit codes and failure counts.

- [ ] **Step 3: Verify scope and status**

Run `git diff --check`, `git status --short --branch`, and `git rev-parse HEAD`; confirm no unrelated files changed, q1bt/fkbh remain open, and report the exact HEAD and commands.
