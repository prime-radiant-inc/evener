# Serf Gate Matrix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make Serf's build and test gate ownership authoritative in `docs/testing.md`, run the existing browser guards automatically in CI, and add the missing deterministic Go test stream to the existing CI job decomposition.

**Architecture:** Keep `make merge-approval-gate` as the serial local contract `lint -> build -> ROOT_FULL=1 make test`. Add one small `make test-web-browser` target that owns the three existing Chrome-based browser guards without folding them into the deterministic `test-web` target. Preserve the current CI jobs, run the browser target from the web job, and run `ROOT_FULL=1 WEB=0 make test` from the Go job because the web job already owns frontend unit/type/lint coverage.

**Tech Stack:** GNU Make, Bash/sh gate scripts, GitHub Actions YAML, Markdown, Go module test runners, Node/npm frontend scripts, headless Chrome via CDP.

## Global Constraints

- Treat `AGENTS.md` and `docs/testing.md` as binding.
- Default tests remain deterministic and must not issue provider requests from a credential alone.
- Browser, provider, live-service, and release checks are explicitly classified; unavailable dependencies are failures or limitations, never silent passes.
- Preserve unrelated files and the parent checkout's intentional deletion of `docs/superpowers/plans/2026-08-02-all-open-katas.md`.
- Do not implement unrelated Katas, launcher/worktree-manager repairs, managed-service restarts, WebKit/Safari support, or a new browser framework.
- Keep `make merge-approval-gate` serial and unchanged unless current-tree evidence proves a narrowly scoped correction is necessary.
- Use bare exit codes for gate verdicts; do not infer success from log text.
- Commit the plan separately, then commit the scoped implementation with a detailed message.

---

### Task 1: Add the local browser-gate entry point

**Files:**
- Modify: `Makefile:1` to add `test-web-browser` to `.PHONY`.
- Modify: `Makefile:57-78` to add the browser-specific target after `test-web`.

**Interfaces:**
- Consumes: the existing `web-preflight` target and the three committed npm scripts `layoutguard`, `overflowguard`, and `spawnguard`.
- Produces: `make test-web-browser`, which runs all three guards from `cmd/serf-hub/frontend`, reports each guard's exit status, runs every guard even if an earlier guard fails, and returns the first nonzero guard status.

- [ ] **Step 1: Establish the RED wiring case.**

Run from the worktree root:

```sh
make -n test-web-browser
```

Expected: nonzero exit with Make reporting that no rule exists for `test-web-browser`.

- [ ] **Step 2: Add the minimal target.**

Add this target after `test-web` and before the binary build targets:

```make
# test-web-browser runs the real browser-only frontend guards. They stay out
# of test-web because jsdom cannot evaluate the CSS cascade or browser geometry.
# Run every guard so one missing browser or failing case does not hide the
# remaining guard's verdict; return the first nonzero status.
test-web-browser: web-preflight
	@set -u; cd cmd/serf-hub/frontend && \
	status=0; \
	for guard in layoutguard overflowguard spawnguard; do \
		if npm run $$guard; then \
			printf 'PASS  web-%s\\n' "$$guard"; \
		else \
			guard_status=$$?; \
			printf 'FAIL  web-%s (exit %s)\\n' "$$guard" "$$guard_status" >&2; \
			[ "$$status" -ne 0 ] || status="$$guard_status"; \
		fi; \
	done; \
	exit "$$status"
```

- [ ] **Step 3: Verify the target composition.**

Run:

```sh
make -n test-web-browser
```

Expected: the dry run shows `web-preflight` followed by `npm run layoutguard`, `npm run overflowguard`, and `npm run spawnguard` in that order.

- [ ] **Step 4: Run the real browser gate.**

Run:

```sh
make test-web-browser
```

Expected: all three guards execute against the current frontend; Chrome absence, Vite failure, or a guard failure returns nonzero and is reported as a limitation/failure.

### Task 2: Wire the existing CI jobs to the complete matrix

**Files:**
- Modify: `.github/workflows/ci.yml:24-25` to invoke `make test-web-browser` in the existing web job.
- Modify: `.github/workflows/ci.yml:65-66` to add the missing full deterministic Go/module/tooling test stream with `ROOT_FULL=1 WEB=0`.

**Interfaces:**
- Consumes: `make test-web-browser`, the existing web job's Node setup, and the existing `make test` runner.
- Produces: CI coverage for all three browser guards plus the full non-browser deterministic test stream without duplicating frontend tests in the Go job.

- [ ] **Step 1: Add the browser gate to the web job.**

Change the web job's command to run the existing web checks and build, then run the new browser gate:

```yaml
      - name: Test, build, and browser guards
        run: make test-web && make build-web && make test-web-browser
```

- [ ] **Step 2: Add the missing deterministic test step.**

After the existing race step, add:

```yaml
      - name: Deterministic tests (full root suite; frontend covered by web job)
        run: ROOT_FULL=1 WEB=0 make test
```

This preserves the current CI decomposition while giving the Go job the full root suite, all non-root module tests, and script self-tests. `WEB=0` avoids duplicating the web job's `test-web` stream.

- [ ] **Step 3: Verify the CI command strings without executing GitHub Actions.**

Run:

```sh
git diff --check
make -n test-web-browser
make -qp | rg -n -A 8 '^test:|^test-web-browser:'
```

Expected: no whitespace errors; the Make database shows the deterministic test target's runner and the browser dry run contains all three guards. Use `make -qp` for recursive Make targets because GNU Make executes recipes containing `$(MAKE)` even under `-n`.

### Task 3: Publish one authoritative gate matrix

**Files:**
- Modify: `docs/testing.md:36-65` to add the canonical matrix and update the post-merge gate explanation.
- Modify: `docs/testing.md:99-114` to describe all three browser guards and their CI/manual status.
- Modify: the live-test sections of `docs/testing.md` to include every repository-owned provider/live/e2e entry point that is part of the matrix.

**Interfaces:**
- Consumes: the current Makefile, CI workflows, frontend package scripts, browser runner failure behavior, and the existing live-test opt-in variables.
- Produces: one table with these columns: gate and exact command; scope; proof; trigger; determinism/external requirements; failure/unavailable-tool behavior; owner and follow-up Kata where applicable.

- [ ] **Step 1: Add rows for deterministic and required CI gates.**

Document exact ownership for `make lint`, `make build`, `make build-web`, `make build-runtime`, `make test`, `ROOT_FULL=1 make test`, `make test-web`, `make test-race`, `make fuzz`, `make fuzz-gap-check`, `make fuzz-corpus-scan`, `make vet`, and `make merge-approval-gate`. State that fuzz search targets such as `make fuzz-nightly` remain manual/nightly and that `make merge-approval-gate` does not run fuzzing.

- [ ] **Step 2: Add the browser row and correct the browser section.**

Document `make test-web-browser` as a required CI browser gate using headless Chrome. State that `test-web` owns typecheck/Vitest/Biome only; the three browser guards own real-browser geometry and production-tree checks. A missing Chrome/Chromium binary, Vite startup failure, or guard error is a nonzero failure. WebKit/Safari has no repository-owned runner and is an explicit unresolved/manual gap, not a pass.

- [ ] **Step 3: Add live, release, and operational rows.**

Classify provider/live/e2e commands as explicit opt-in only, including `SERF_LIVE_TESTS=1`, `SERF_MCP_E2E=1`, `SERF_OPENAI_CODEX_E2E=1`, `SERF_ANTHROPIC_E2E=1`, `SERF_E2E_LIVE=1`, and `SERF_SEATBELT_LIVE=1` where their existing commands are repository-owned. Classify `make dist` as release/distribution-only, `web-preflight` and `disk-reclaim.sh --check` as setup/operational prerequisites, and launcher/managed-service/SDD/Kata responsibilities as outside Serf.

- [ ] **Step 4: Record mismatches and intentional gaps.**

List the current CI decomposition, browser dependency limitation, WebKit/Safari gap, and any command that remains manual or release-only. Use functional owners (`Serf CI`, `frontend`, `release`, `tooling`, or `launcher/worktree manager`) and reference existing Kata IDs only; do not create unrelated Katas during this task.

- [ ] **Step 5: Verify documentation/command agreement.**

Run:

```sh
git diff --check
rg -n 'test-web-browser|layoutguard|overflowguard|spawnguard|merge-approval-gate|ROOT_FULL=1 WEB=0 make test' docs/testing.md Makefile .github/workflows/ci.yml
make -qp | rg -n -A 8 '^merge-approval-gate:|^test-web-browser:'
```

Expected: every documented command exists with the documented ownership, the three browser scripts appear in both the Makefile target and CI workflow, and the Make database still shows the merge gate's serial lint/build/test recipe without executing it.

### Task 4: Run affected verification and commit the implementation

**Files:**
- Verify: `docs/testing.md`, `Makefile`, `.github/workflows/ci.yml`.

- [ ] **Step 1: Run the cheap checks first.**

Run `git diff --check`, `make -n test-web-browser`, `make -qp` for recursive Make targets, and the documentation/command-reference searches from Task 3.

- [ ] **Step 2: Run the affected frontend gates.**

Run:

```sh
make test-web
make build-web
make test-web-browser
```

Record each command's bare exit code and report missing Node/npm/Chrome as a limitation rather than a pass.

- [ ] **Step 3: Run the complete deterministic gate because CI ownership changed.**

Run:

```sh
make merge-approval-gate
```

Record the separate lint, build, and `ROOT_FULL=1 make test` exit codes, plus any environmental limitation.

- [ ] **Step 4: Inspect the final diff and status.**

Run:

```sh
git diff --check
git status --short --branch
git diff --stat
git diff -- Makefile .github/workflows/ci.yml docs/testing.md
```

Confirm that only the three scoped files and this plan are changed in the worktree, and that the parent checkout's intentional deletion was not imported or modified.

- [ ] **Step 5: Commit the implementation.**

```sh
git add Makefile .github/workflows/ci.yml docs/testing.md
git commit -m "test: publish and wire Serf gate matrix"
```

The commit body must explain the canonical gate, the separate browser gate, the CI decomposition, deterministic/live ownership, and the intentionally unresolved WebKit/Safari gap.
