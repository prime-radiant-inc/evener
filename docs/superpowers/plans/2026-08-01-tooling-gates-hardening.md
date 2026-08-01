# Tooling gates hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

Goal: Make the merge gate canonical, make both generated AppWire outputs blocking, make gitleaks strict only for CI callers, and declare the root module's missing identifier dependency without changing the separate version-alignment observation.

Architecture: Keep gate composition and generated-file validation in the root Makefile, missing-tool policy in scripts/gitleaks-scan.sh, CI strictness in step-local environment, and the root dependency graph in go.mod. Add deterministic fixture tests at the process and filesystem boundaries already used by the repository; do not add a general-purpose wrapper or alter test scheduling.

Tech Stack: GNU/BSD make, Bash selftests, Go 1.25 workspace/module tooling, Go YAML tests, Git fixtures, GitHub Actions YAML.

## Global Constraints

- Read and follow docs/testing.md; default verification is deterministic and offline.
- Preserve published gate coverage and ROOT_FULL=1 semantics.
- merge-approval-gate runs lint, build, and ROOT_FULL=1 test serially and never runs fuzzing.
- lint-generated validates both docs/appwire-protocol.md and cmd/serf-hub/frontend/src/protocol/types.gen.ts.
- Missing gitleaks remains warning-plus-zero unless SERF_GITLEAKS_REQUIRED=1; only CI scan steps set that variable.
- fdhx changes only the root primeradiant.com/serf/identifier v0.0.0 requirement; do not align x/sys or x/text.
- Tests exercise real target behavior at external boundaries, not rendered recipe text or mock existence.
- Every behavior change follows RED, verify failure, minimal GREEN, verify pass, then commit.
- Do not use provider credentials, live model calls, network access, or fuzz searches in default verification.

## Files and responsibilities

Modify:

- Makefile lines 1, 149-156, and 468-478: register the merge selftest, add the canonical target, and diff both generated outputs.
- docs/testing.md lines 36-55: publish the canonical target and retain the explicit expansion.
- docs/conventions/go-workspace.md lines 135-140: describe both generated outputs.
- scripts/run-module-lint-selftest.sh: add generated-drift and real gitleaks fixtures; update the generated diff expectation after the Makefile change.
- scripts/gitleaks-scan.sh lines 12-24: add strict missing-tool mode.
- .github/workflows/ci.yml lines 56-78: set strict mode only on the two CI scan steps.
- lintfamily_audit_test.go: assert strict mode on every CI scan step.
- go.mod lines 76-81: add the root identifier sibling requirement only.

Create:

- scripts/merge-approval-gate-selftest.sh: fake the recursive make boundary and test ordering, scoped environment, output, and fail-fast behavior.

Do not modify go.work, go.sum, sibling go.mod files, fuzz targets, or frontend generated content.

---

### Task 1: Add the canonical merge-approval target (9fcs)

Files:

- Create scripts/merge-approval-gate-selftest.sh.
- Modify Makefile phony list and add the target near the lint/test gate definitions.
- Modify docs/testing.md.
- Test scripts/merge-approval-gate-selftest.sh.

Interfaces:

- The selftest invokes the real outer make with MAKE set to a fake executable; the fake records target and inherited ROOT_FULL.
- The production target exposes make merge-approval-gate with no prerequisites.

- [ ] Step 1: Write the failing recursive-boundary selftest.

Create the script with set -uo pipefail, a mktemp fixture, and an EXIT cleanup trap. The fake make must:

    target="${!#}"
    printf '%s\t%s\n' "$target" "${ROOT_FULL:-}" >>"$state/calls"
    printf 'recursive stdout: %s\n' "$target"
    printf 'recursive stderr: %s\n' "$target" >&2
    [ "$FAKE_FAIL_TARGET" = "$target" ] && exit 23
    exit 0

Run the real outer make as follows, with ROOT_FULL removed from the outer environment:

    env -u ROOT_FULL FAKE_STATE="$state" FAKE_FAIL_TARGET="$failure" \
      "$real_make" -C "$repo_root" -j 4 MAKE="$fake_make" merge-approval-gate

The script must assert a successful call file with exactly:

    lint<TAB>
    build<TAB>
    test<TAB>1

It must also assert that stdout and stderr markers are present, that FAKE_FAIL_TARGET=lint produces only the lint call and a nonzero outer status, and that FAKE_FAIL_TARGET=build produces lint then build and no test.

- [ ] Step 2: Run the selftest and verify RED.

Run:

    scripts/merge-approval-gate-selftest.sh

Expected: nonzero exit because the current Makefile reports no rule for merge-approval-gate. This failure must be attributable to the missing target, not a fixture or shell syntax error.

- [ ] Step 3: Implement the smallest Makefile target.

Add merge-approval-gate to .PHONY and add these three separate recipe lines:

    # merge-approval-gate is the canonical serial post-merge gate.
    merge-approval-gate:
            @$(MAKE) lint
            @$(MAKE) build
            @ROOT_FULL=1 $(MAKE) test

Do not add prerequisites, .NOTPARALLEL, pipes, output filters, or fuzz targets. Separate recipe lines provide ordering under an outer -j invocation, and recursive $(MAKE) preserves the jobserver and child status.

- [ ] Step 4: Run the selftest and verify GREEN.

Run scripts/merge-approval-gate-selftest.sh. Expected: exit 0 with three ordered calls, ROOT_FULL only on test, visible child output, and both fail-fast cases passing.

- [ ] Step 5: Update the public gate documentation.

Change docs/testing.md so make merge-approval-gate is the canonical command. Retain the explicit three-command expansion immediately below it for diagnosis and evidence, and state that fuzz remains separate.

- [ ] Step 6: Commit the completed Kata.

Run:

    git add Makefile docs/testing.md scripts/merge-approval-gate-selftest.sh
    git diff --cached --check
    git commit -m "build: add the canonical merge approval gate"
    kata close 9fcs --done --message "Added and verified the serial merge-approval-gate target." --commit "$(git rev-parse HEAD)" --evidence "merge selftest and canonical gate pass"

---

### Task 2: Make generated TypeScript drift blocking (dq4t)

Files:

- Modify scripts/run-module-lint-selftest.sh near its Makefile integration fixture.
- Modify Makefile lines 468-478.
- Modify docs/conventions/go-workspace.md lines 135-140.
- Test scripts/run-module-lint-selftest.sh.

Interfaces:

- The fixture runs the real copied lint-generated target with a fake go generator and real Git index/HEAD behavior.
- The target reports both generated paths and returns the generator/diff failure status.

- [ ] Step 1: Add a behavioral generated-drift fixture before changing the target.

Create a throwaway Git repository with current Markdown and stale committed TypeScript. Copy the real Makefile. Put a fake go first on PATH that accepts only generate ./appwire/... and rewrites both files to known-current contents. Initialize and commit with explicit local identity. Run make lint-generated in that fixture with real git.

The fixture must assert nonzero status and that the output names types.gen.ts. The current target will incorrectly return zero because it diffs only the Markdown path, so this is the RED behavior. Do not assert the rendered Makefile command.

- [ ] Step 2: Run the lint selftest and verify RED.

Run:

    scripts/run-module-lint-selftest.sh

Expected: nonzero from the new stale-TypeScript case, with the diagnostic explaining that the target returned zero when the committed TypeScript output was stale.

- [ ] Step 3: Extend the Makefile and documentation.

Change lint-generated to run one git diff --exit-code over these exact paths:

    docs/appwire-protocol.md
    cmd/serf-hub/frontend/src/protocol/types.gen.ts

Use a diagnostic that names both outputs and says to run make generate and commit. Update the adjacent generate comments and docs/conventions/go-workspace.md so both outputs are described as generated and gated.

- [ ] Step 4: Update the existing integration expectation and verify GREEN.

Change the existing run-module-lint-selftest expected family line to:

    git diff --exit-code -- docs/appwire-protocol.md cmd/serf-hub/frontend/src/protocol/types.gen.ts

Run scripts/run-module-lint-selftest.sh. Expected: exit 0, including the stale-TypeScript fixture, all prior lint-family assertions, and warning preservation.

- [ ] Step 5: Mutation-check the oracle.

Temporarily remove the TypeScript path from lint-generated, rerun the selftest, and confirm only the stale-TypeScript case goes green and the selftest fails. Restore the path and rerun the selftest to confirm GREEN.

- [ ] Step 6: Commit and close the Kata.

Run:

    git add Makefile docs/conventions/go-workspace.md scripts/run-module-lint-selftest.sh
    git diff --cached --check
    git commit -m "build: gate every generated AppWire output"
    kata close dq4t --done --message "lint-generated now blocks stale Markdown or TypeScript output." --commit "$(git rev-parse HEAD)" --evidence "stale TypeScript fixture and lint selftest pass"

---

### Task 3: Make missing gitleaks fatal only for CI callers (m716)

Files:

- Modify scripts/gitleaks-scan.sh lines 12-24.
- Modify scripts/run-module-lint-selftest.sh with direct real-script cases.
- Modify .github/workflows/ci.yml lines 56-78.
- Modify lintfamily_audit_test.go with a structured workflow test.
- Test the shell selftest and the focused Go workflow test.

Interfaces:

- SERF_GITLEAKS_REQUIRED=1 is the only strict-mode opt-in.
- The aggregate repository scan and corpus scan CI steps each set that variable through step-level env.

- [ ] Step 1: Add direct optional/required gitleaks tests before changing the script.

Use a fake git at the external dependency boundary so the real scan script resolves a known repository root while PATH contains no gitleaks. Assert that the unset case returns zero with the existing warning. Assert that SERF_GITLEAKS_REQUIRED=1 returns nonzero and contains this future diagnostic:

    error: gitleaks is required but not installed; cannot run repo secret scan

- [ ] Step 2: Add the structured CI test before changing the workflow.

Add TestCISetsStrictGitleaksModeOnScanSteps to lintfamily_audit_test.go. Parse the existing workflow type and inspect step.Run only. For every step whose run block contains make lint or make fuzz-corpus-scan, require step.Env["SERF_GITLEAKS_REQUIRED"] == "1"; require exactly two scan-bearing steps. The current workflow must fail this test with a named missing environment setting.

- [ ] Step 3: Run the RED tests.

Run:

    scripts/run-module-lint-selftest.sh
    go test ./... -run '^TestCISetsStrictGitleaksModeOnScanSteps$' -count=1

Expected: the shell test reports required mode incorrectly exits zero, and the Go test reports missing strict environment on current CI steps.

- [ ] Step 4: Implement the strict script branch.

In scripts/gitleaks-scan.sh, inside the missing-command branch, check only the literal value 1:

    if [ "${SERF_GITLEAKS_REQUIRED:-}" = 1 ]; then
            echo "error: gitleaks is required but not installed; cannot run $mode secret scan (install: https://github.com/gitleaks/gitleaks)" >&2
            exit 1
    fi
    echo "warning: gitleaks not installed; skipping $mode secret scan (install: https://github.com/gitleaks/gitleaks)" >&2
    exit 0

Use the default-empty expansion safely when the variable is unset. Do not change installed-tool invocation, scan arguments, modes, or corpus traversal.

- [ ] Step 5: Opt CI steps into strict mode and verify GREEN.

Add this exact env block under both the aggregate lint step and the corpus secret-scan step:

    env:
      SERF_GITLEAKS_REQUIRED: "1"

Run:

    scripts/run-module-lint-selftest.sh
    go test ./... -run '^TestCISetsStrictGitleaksModeOnScanSteps$' -count=1

Expected: both pass. Also run the real script with PATH=/usr/bin:/bin and strict mode; if that PATH contains gitleaks, report the environmental limitation and rely on the fake-boundary test rather than weakening it.

- [ ] Step 6: Mutation-check, commit, and close.

Remove the strict branch temporarily and rerun the shell selftest; it must fail. Restore it, then run:

    git add scripts/gitleaks-scan.sh scripts/run-module-lint-selftest.sh .github/workflows/ci.yml lintfamily_audit_test.go
    git diff --cached --check
    git commit -m "ci: fail closed when required gitleaks is missing"
    kata close m716 --done --message "CI opts into fatal missing-gitleaks mode while local skip remains green." --commit "$(git rev-parse HEAD)" --evidence "optional/required fixtures and workflow test pass"

---

### Task 4: Declare the root identifier sibling (fdhx)

Files:

- Modify go.mod lines 76-81 only.
- Test with an external scratch consumer under a system temporary directory; no repository test file is needed for this metadata-only repair.

Interfaces:

- The root go.mod must declare every sibling package its root packages import under module graph pruning.
- go.work remains the only local directory mapping; no local path enters go.mod.

- [ ] Step 1: Build the readonly external-consumer RED probe.

From the repository root, use this exact scratch probe. The repository path is captured before changing directory, so every replacement points at the worktree rather than the consumer itself:

    repo_root="$(pwd -P)"
    probe_root="$(mktemp -d -t serf-root-consumer.XXXXXX)"
    trap 'rm -rf "$probe_root"' EXIT
    go list ./... >"$probe_root/root-packages"
    (
            cd "$probe_root" || exit 1
            go mod init example.com/serf-root-consumer
            go mod edit -require=primeradiant.com/serf@v0.0.0
            go mod edit \
                    -replace=primeradiant.com/serf@v0.0.0="$repo_root" \
                    -replace=primeradiant.com/serf/agent@v0.0.0="$repo_root/agent" \
                    -replace=primeradiant.com/serf/auth@v0.0.0="$repo_root/auth" \
                    -replace=primeradiant.com/serf/envvars@v0.0.0="$repo_root/envvars" \
                    -replace=primeradiant.com/serf/fuzz@v0.0.0="$repo_root/fuzz" \
                    -replace=primeradiant.com/serf/identifier@v0.0.0="$repo_root/identifier" \
                    -replace=primeradiant.com/serf/invariant@v0.0.0="$repo_root/invariant" \
                    -replace=primeradiant.com/serf/llm@v0.0.0="$repo_root/llm"
            GOWORK=off GOFLAGS=-mod=mod GOPROXY=off GOSUMDB=off go mod download all
    )
    set +e
    (
            cd "$probe_root" || exit 1
            GOWORK=off GOFLAGS=-mod=readonly GOPROXY=off GOSUMDB=off \
                    go list -deps $(cat "$probe_root/root-packages")
    )
    probe_status=$?
    set -e
    printf 'root consumer dependency probe exit=%s\n' "$probe_status"

Expected RED: nonzero status naming primeradiant.com/serf/identifier as replaced but not required. The only -mod=mod command is inside the scratch module; never point it at the worktree.

- [ ] Step 2: Add exactly the missing root requirement.

Insert primeradiant.com/serf/identifier v0.0.0 in the existing sibling block between envvars and llm. Do not run go mod tidy. Do not edit go.work, go.sum, x/sys, x/text, or any sibling module.

- [ ] Step 3: Rerun dependency and test-package probes.

With the same scratch module and explicit root package list, run both:

    GOWORK=off GOFLAGS=-mod=readonly GOPROXY=off GOSUMDB=off go list -deps $(cat "$probe_root/root-packages")
    GOWORK=off GOFLAGS=-mod=readonly GOPROXY=off GOSUMDB=off go list -deps -test $(cat "$probe_root/root-packages")

Expected: both exit zero. Confirm git diff -- go.mod go.sum go.work shows only the one new go.mod line.

- [ ] Step 4: Commit and close the Kata.

Run:

    git add go.mod
    git diff --cached --check
    git commit -m "build: declare identifier in the root module"
    kata close fdhx --done --message "Root go.mod now declares identifier for external consumers." --commit "$(git rev-parse HEAD)" --evidence "readonly dependency and test-package probes pass"

---

### Task 5: Integrated verification and final scope review

Files: verification only; no new production files.

- [ ] Step 1: Run focused checks.

Run:

    scripts/merge-approval-gate-selftest.sh
    scripts/run-module-lint-selftest.sh
    go test ./... -run '^(TestCIRunsEveryFamilyOfTheLintGate|TestEveryLintFamilyJoinsTheAggregateGate|TestCISetsStrictGitleaksModeOnScanSteps)$' -count=1
    git diff --check

Expected: all commands exit zero.

- [ ] Step 2: Run the aggregate tooling selftest.

Run make selftest. Expected: every listed selftest, including the new merge selftest, reports PASS; no provider or network request occurs.

- [ ] Step 3: Run the canonical merge gate.

Run make merge-approval-gate from the repository root. Expected: bare exit zero, with lint, build, and full-root test coverage in that order. Do not append go test ./... or make fuzz.

- [ ] Step 4: Verify final scope and history.

Run:

    git status --short
    git log --oneline --decorate -8
    git diff b35af9b1f...HEAD --stat
    git diff b35af9b1f...HEAD -- go.mod go.work go.sum

Expected: only planned files changed, go.work and go.sum have no diff, and no generated frontend output was accidentally committed.

- [ ] Step 5: Report evidence.

Report the worktree path, each implementation commit, each Kata close result, focused test outputs, canonical gate exit, and any environmental limitation. Do not claim completion from a prior baseline; use fresh final command output.

## Plan self-review

- 9fcs is covered by the recursive fake, ordering/environment/output/failure assertions, docs update, and final canonical target.
- dq4t is covered by a committed-stale TypeScript fixture, both pathspecs, diagnostic, convention docs, and mutation check.
- m716 is covered by the real script boundary, optional/required behavior, both CI step environments, structured YAML test, and mutation check.
- fdhx is covered by readonly dependency and test-package probes plus an explicit exclusion of version alignment.
- The final task verifies focused tests, aggregate selftests, canonical acceptance, clean scope, and per-Kata evidence.
- No placeholder implementation steps, unspecified files, provider-dependent tests, or fuzz work remain.
