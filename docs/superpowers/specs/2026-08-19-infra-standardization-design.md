# Infrastructure Standardization Design

Date: 2026-08-19
Branch: infra-standardization (off origin/main 89a3689c5)
Status: approved in design review; pending implementation plan

## Goal

Convert the repo's build, test, lint, and release infrastructure from bespoke
machinery to ecosystem-standard tooling, and delete defensive machinery the
owner has ruled unnecessary. One PR; each atomic change lands as its own
commit.

## Decisions (owner-approved 2026-08-19)

| Question | Ruling |
|---|---|
| Overall scope | Full standardization |
| Custom lint programs | Convert where a standard equivalent holds; internalcheck stays |
| Release pipeline | Adopt goreleaser |
| Coverage | No external service; consolidate the floor family to one local script + one floor file (F1b) |
| Audit test framework | Delete 14 of 16 root `*_audit_test.go` plus `workflow_test.go`; KEEP `scriptmktemp_audit_test.go` and the delete-safety half of `makefile_audit_test.go` (the kata 5hs2 tripwires) |
| capability-preflight | Delete; affected tests gain per-test self-detection (`t.Skip`) |
| MEMCAP | Delete |
| merge-into-branch | Delete |
| dependabot | Add: github-actions + gomod only, never npm |
| install.sh | Add checksum verification against the published checksums.txt |
| Release arches | Unchanged: linux/amd64, darwin/arm64 |

Deliberate infrastructure that stays: the fuzz campaign tooling (nightly,
triage, continuous, drive, oracle-audit, bisect, mutation score, ledger,
goldens), `fuzz-gap-check` / `fuzz-registry-check`, the deterministic
`make fuzz` replay, the run-module-tests wave runner, the test-timing budget,
the dev-tooling selftest estate (minus suites for deleted tooling),
scratch-lib and private-go-home, the scenario cards (their policing audits
die; the cards remain), and `merge-approval-gate` itself.

## Changes

### 1. Lint conversions

**1a. Struct-tag casing: `evener-namingcheck` → tagliatelle.**
Enable `tagliatelle` in `.golangci.yml`: root rules `json: snake`,
`toml: snake`. Map the existing carve-outs (currently hardcoded in
`cmd/evener-namingcheck/main.go`):

- camelCase JSON packages (appwire, appsource, appserver, appprojector,
  launchconfig — exact import paths read from main.go at implementation) →
  tagliatelle per-package `overrides` with `rules: {json: camel}`.
- `llm/providers/` full exemption → tagliatelle `overrides` with
  `ignore: true` per provider package, or a golangci path-exclusion if the
  package list proves unwieldy. Implementation verifies which mechanism
  golangci-lint v2.12.2 honors; the exemption must hold either way.
- Per-(file, tag) doctor-contract exemptions → `//nolint:tagliatelle`
  comments at those sites.

Shrink `cmd/evener-namingcheck` to its TOML-data-file scanner (no standard
linter reads TOML data files) and rename it `cmd/evener-tomlcheck`.
`lint-naming` keeps its name and runs only the TOML checker. Delete the Go
AST machinery and its tests; keep the TOML tests. Delete the
`build-namingcheck` target (nothing consumes that binary today).

**1b. Doc comments: `evener-docscheck` → revive `exported`.**
Add `exported` to the revive rules in `.golangci.yml`, scoped to the same
published library dirs docscheck covers today (exact list from
`cmd/evener-docscheck/main.go`), via golangci exclusion rules.
Arguments: `disable-stuttering-check` (stuttering is a naming check, out of
scope for this conversion). Fidelity note: revive also enforces the standard
"comment opens with the declaration name" form, which docscheck did not.
Implementation first measures how many existing comments trip the form and
method checks. If the sweep is small (rule of thumb: under ~50 sites), fix
the comments in the same commit. If large, also pass
`disable-checks-on-methods` and note the narrower rule in the PR. Delete
`cmd/evener-docscheck` and the `lint-docs` target.

**1c. ~~Drop the `lint-gofmt` double gate.~~ Wrong; reversed in review.**
The reasoning was that `.golangci.yml` already enables the `gofmt` and
`goimports` formatters and `lint-golangci` is mandatory, so the standalone
gate is redundant. It is not: golangci-lint formats only the files it
compiles, and the tag-free run compiles none of the ~250 `//go:build
evenerfuzz` / `eval` sources. `lint-gofmt` (`gofmt -l` over every tracked
`.go` file) stays. See "Corrections after review".

**1d. Keep `evener-internalcheck`.** No standard linter checks "exported API
must not name internal types." It stays as-is.

### 2. Makefile mechanics

- Extract the two ~40-line inline shell programs into
  `scripts/web/test-web.sh` and `scripts/web/test-web-browser.sh`, written
  on `scripts/lib/scratch-lib.sh` (`scratch_dir`/`scratch_rm`, bash shebang,
  no variable-fed deletes). Same checks, same concurrency, same PASS/FAIL
  output; the Makefile targets become one-line calls.
- Add the missing `e2e-cover` to `.PHONY`.

### 3. Tool versions and CI dedup

- New `.tool-versions` (asdf/mise convention) pins `golangci-lint 2.12.2`
  and `gitleaks 8.30.1`. CI sources versions from this file; a new
  `make tools` target installs the pinned versions locally. This ends the
  local-vs-CI lint drift (the pin lived only in ci.yml).
- ci.yml installs golangci-lint via the official
  `golangci/golangci-lint-action` with `install-only: true`, version read
  from `.tool-versions`. `make lint` remains the single family list.
- Install gitleaks once per job (today it is installed twice in ci.yml).
- New composite action `.github/actions/setup-toolchain` (setup-go with
  `go-version-file: go.work`, setup-node 22 with npm cache) replaces the
  three duplicated setup stanzas across ci.yml and binaries.yml.

### 4. goreleaser release pipeline

New `.goreleaser.yml`:

- `before` hook runs `make build-web` (evener-hub embeds the SPA).
- Five builds (evener, evener-hub, evener-tui, evener-doctor,
  evener-migrate), targets linux/amd64 + darwin/arm64, `CGO_ENABLED=0`.
  The evener build carries the buildinfo ldflags (GitSHA, GitDirty,
  BuildTime, Channel) via goreleaser templates.
- Archives: `evener_<os>_<arch>.tar.gz`, directory-wrapped, five binaries —
  byte-layout identical to today's `make dist` output, verified by
  side-by-side comparison against main.
- `checksums.txt` (sha256) — same name as today.
- Tagged releases: `goreleaser release --clean` publishes the GitHub
  release with generated notes (replaces the hand-rolled `gh release`
  flow).
- Snapshot channel: unchanged semantics (force-moved `snapshot` tag,
  prerelease), reimplemented on `goreleaser release --snapshot --clean`
  output plus `gh release upload`.

`binaries.yml` collapses to a single job (goreleaser cross-compiles both
targets from one runner; the matrix dies). PR/main runs build the snapshot
artifacts and upload them as workflow artifacts; tags publish the release;
main additionally refreshes the snapshot channel. `make dist` becomes a
thin wrapper: `goreleaser release --snapshot --clean --single-target`.
`workflow_test.go` is deleted with the audit framework, not rewritten.

### 5. Coverage consolidation (F1b)

One script, one floor file:

- New `scripts/coverage/coverage-floor.sh`: per module, run the unit suite
  and the deterministic fuzz replay under `-coverprofile`, merge to the
  union profile (reusing the existing covstmt/union logic), and compare
  against `scripts/coverage/coverage-floors.txt`. A `web` row covers the
  frontend (vitest v8 line coverage; parse an existing report when present
  instead of re-running). Flags: bare = report, `--check` = fail on a drop,
  `--bless` = raise floors. One `make coverage-floor` target.
- Deleted targets: `test-coverage-floor`, `fuzz-coverage`,
  `fuzz-coverage-global`, `coverage-union`, `web-coverage-floor` and the
  `*-selftest` variants of the union/web floors.
- Deleted scripts/data: `coverage-union.sh`, `web-coverage-floor.sh`,
  `fuzz-coverage.sh`, `fuzz-coverage-global.sh` (+ selftests),
  `testcov-global-floors.txt`, `covunion-floors.txt`, `webcov-floors.txt`,
  `fuzzcov-floors.txt`, `fuzzcov-global-floors.txt`,
  `fuzzcov-global-exclusions.txt`, `fuzzcov-ignore.txt`,
  `cmd/evener-dev/coveragefloor.go` (+ test).
- `scripts/coverage/coverage-gaps.sh` and its selftest stay (a report, not
  a gate).
- `fuzz-targets.txt` slims to `tag:module:package:name` rows; the
  coverpkg/focus fields die with the per-target coverage accounting.
  Consumers (`run-fuzz.sh`, gap-check, registry-check, triage,
  oracle-audit) drop the two fields.

Accepted loss: per-area web floors and per-target focus-set metrics. The
ratchet keeps whole-module (plus whole-frontend) granularity.

### 6. Fuzz mechanics

Extract the inline rapid seed-bank replay loop (current Makefile `fuzz`
recipe) to `scripts/fuzz/rapid-replay.sh`, logic verbatim, scratch-lib
discipline. Everything else in the fuzz subsystem is unchanged.

### 7. Removals

**7a. MEMCAP.** Delete `scripts/fuzz/run-capped.sh`, the `ifeq Darwin` /
`MEMCAP` plumbing, and every `$(MEMCAP)` prefix in the Makefile. Rewrite
the "Memory safety" section of docs/fuzzing.md. Consequence (accepted): on
Linux, test/fuzz runs lose the per-run memory ceiling.

**7b. merge-into-branch.** Delete `scripts/gate/merge-into-branch.sh`, its
selftest, the two make targets, and its `DEV_TOOLING_TEST_SCRIPTS` entry;
scrub docs references. Consequence (accepted): merges use plain
`git merge`; the kata h2tb wrong-branch protection is gone.

**7c. capability-preflight → per-test self-detection.**
Delete `cmd/evener-dev/capabilitypreflight.go` (+ test), the preflight
dance in `merge-approval-gate` (the target becomes a plain
`lint && build && ROOT_FULL=1 make test && test-dev-tooling` chain), and
the `EVENER_GATE_CAPABILITY_SKIP` plumbing in `run-module-tests.sh` and
`internal/devtool/gatesurface` (the capability skip patterns die; the
gate's fuzz-exclusion surface stays). The probe implementations in
`internal/devtool/capabilityprobe` (loopback-bind, chrome-cdp,
process-inspect, git-cache) move behind a small test-helper package
(e.g. `RequireLoopback(t)`, `RequireChromeCDP(t)`, …) that calls `t.Skip`
when the host lacks the capability. Apply the helpers to exactly the tests
the deleted skip patterns covered — implementation maps the gatesurface
patterns to the concrete test list and touches no others.

### 8. Audit framework deletion (with two survivors)

Delete 14 root `*_audit_test.go` files and `workflow_test.go`. Keep two
safety tripwires tied to the kata 5hs2 incident (a cleanup script deleted a
home directory):

- `scriptmktemp_audit_test.go` stays whole (scratch-lib discipline and the
  count-pinned list of variable-fed recursive deletes in scripts).
- `makefile_audit_test.go` is stripped to
  `TestNoMakefileRecipeFeedsVariableToRecursiveDelete` and its helpers and
  allowlist; its build-all/install parity test dies.

Because these two survive, the lockstep rule survives with them: any commit
that rewords a pinned delete recipe (the test-web extraction in particular)
updates the allowlist in the same commit, and new scripts keep using
scratch-lib.

Enforcement that ends (accepted): lint-family drift pinning, scenario-card
rules, envvar-registry doc sync, sha256 call-site inventory, binaries.yml
shape pinning. The audited *mechanisms* being replaced in this PR (exact
recipe texts, the lint list in CI, the workflow shape) need no successor;
the mechanisms that remain (scratch-lib, scenario cards, envvars registry)
continue by convention. `docs/testing.md` sections that document the deleted
audits are rewritten.

### 9. dependabot

New `.github/dependabot.yml`: `github-actions` (weekly) and `gomod`
covering all eight workspace modules via `directories` (weekly). No npm
entry — explicit owner ruling.

### 10. install.sh checksum verification

Download `checksums.txt` alongside the archive; verify with `sha256sum`
(Linux) or `shasum -a 256` (macOS); fail hard on mismatch; fail closed
with a clear message when neither tool exists.

## Docs

`docs/testing.md` loses or rewrites: the audit-framework references, the
post-merge-gate preflight explanation, the coverage-floor rows of the gate
matrix, and the dev-tooling suite list changes. `docs/fuzzing.md` loses the
MEMCAP section and gains the slimmed manifest format. Historical plan docs
under `docs/design/plans` and `docs/superpowers/plans` stay untouched.
Each doc edit rides with the commit that causes it; a final docs sweep
commit catches strays.

## Commit plan (atomic, each leaves the gates green)

1. Delete 14 audit files + `workflow_test.go`; strip
   `makefile_audit_test.go` to the delete-safety test. First, because the
   deleted exact-text pins would otherwise fail every later commit. The two
   surviving safety audits keep their lockstep rule.
2. tagliatelle replaces namingcheck-Go; namingcheck shrinks to
   evener-tomlcheck; `build-namingcheck` dies.
3. revive `exported` replaces docscheck; `lint-docs` dies.
4. Drop `lint-gofmt`.
5. Extract test-web / test-web-browser to scripts/web/.
6. `.PHONY` fix.
7. `.tool-versions` + `make tools` + CI version sourcing + gitleaks-once +
   composite setup action.
8. goreleaser: `.goreleaser.yml`, new `binaries.yml`, `make dist` wrapper,
   install.sh checksums.
9. Coverage consolidation to one script + one floor file.
10. Extract rapid-replay loop to a script.
11. Remove MEMCAP.
12. Remove merge-into-branch.
13. capability-preflight removal + per-test self-detection helpers.
14. dependabot.yml.
15. Docs sweep (only what earlier commits did not already carry).

## Verification

- Per commit: `make lint`, `make test`; plus `make test-dev-tooling` when
  tooling changes, `make test-web` / `make test-web-browser` for the web
  script moves, `make fuzz-seeds` for fuzz-surface changes.
- goreleaser: run `goreleaser release --snapshot --clean` and diff the
  artifact names and archive contents against `make dist` output from
  main; they must match except for version metadata.
- install.sh: exercise against a local HTTP fixture serving the snapshot
  artifacts and checksums; prove a tampered archive fails closed.
- Coverage: run the new `make coverage-floor` and confirm it reproduces
  the union numbers the deleted tracks reported (spot-check two modules).
- Final: full local `make merge-approval-gate`, then CI green on the PR.

## Corrections after review

The design review of the implementation (PR #278) rejected it and found
four defects and three enforcement losses this spec did not anticipate.
Recorded here so the spec stops authorizing decisions the implementation
had to reverse.

**Four defects, all execution-proven by the reviewer:**

1. A 3.9 MB compiled `evener-fuzzregistry` was committed. Dropped by
   rewriting the branch; `.gitignore` now lists every `cmd/` binary.
2. `binaries.yml`'s snapshot job lost `actions/checkout`, so its `git` and
   `gh` steps would have run in a workspace with no repository. It fires
   only on a push to main, which is why PR CI could not see it. §8's
   "`workflow_test.go` is deleted, not rewritten" was wrong to that extent:
   two slim tests now pin the shape CI structurally cannot exercise.
3. §10's checksum verification failed open. `grep … | sha256sum -c -` reports
   the pipeline's last status, macOS `/sbin/sha256sum` exits 0 on empty
   input, and the published `checksums.txt` names artifacts as `dist/<name>`,
   which the pattern never matched — so a tampered archive installed with
   exit 0. Fixed on both sides: the lookup accepts either name form and
   demands exactly one match before the checksum tool runs.
4. §4's `make dist` never worked: `{{ .Env.BUILD_CHANNEL }}` under
   goreleaser's `missingkey=error` is a hard failure when the variable is
   unset, which is the local case. Now `envOrDefault`.

**Three enforcement losses, none of them on §5's accepted-loss list:**

5. `server/appwire_*.go`'s camelCase regime. §1a assumed the carve-outs were
   all package-shaped; that path is file-shaped inside a snake_case package,
   and tagliatelle's overrides are per-package only. `.golangci-appwire.yml`
   is the second half of the split.
6. The `fuzz` module was outside `lint-golangci`'s module list, so 29
   struct-tag sites went ungated. §1a assumed golangci reached everything
   the filesystem-walking `evener-namingcheck` did; it reaches only what the
   module list names.
7. Build-tagged sources are compiled by no lint pass, so neither tagliatelle
   nor the gofmt formatter saw them. Each tag floor gains a tagliatelle run
   under its tag, and 1c's deletion is reversed.

**And one gap in §5:** the consolidated ratchet had no below-floor case, so
`p < f - tol` was unproven. Restored, with the tolerance band.

## Risks and fidelity notes

- revive `exported` sweep size unknown → measured first; fallback
  documented in 1b.
- tagliatelle per-package override semantics (exact package paths, no
  globs) → verified against golangci-lint v2.12.2 during implementation;
  fallback is golangci path-exclusions.
- goreleaser archive byte-layout must match install.sh's extraction
  expectations → fixture-verified in section 4 verification.
- MEMCAP / merge-into-branch / preflight / audit-framework protections are
  removed by explicit owner ruling, not by oversight.
- The snapshot channel remains a mutable force-moved tag; that is a
  product decision, out of this PR's scope.
