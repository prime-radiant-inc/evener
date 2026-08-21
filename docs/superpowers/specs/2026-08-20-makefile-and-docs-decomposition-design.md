# Makefile and Developer-Docs Decomposition

> Revised 2026-08-20 after adversarial review. See **Corrections after review**
> for what changed and why; nine defects in the first draft are recorded there.

## Goal

Make the repository's `make` targets discoverable, documented, and unable to
drift out of documentation silently.

Three concrete failures motivate this:

1. **Discovery.** `mutation-floor`, `fuzz-drive`, `coverage-gaps`,
   `coverage-gaps-selftest` and `test-install` have no target-level
   documentation anywhere in `docs/`, `README.md` or `AGENTS.md`.
   `fuzz-mutation-score`, `fuzz-ledger`, `fuzz-registry-check` and `test-short`
   are named only in dated plan documents that describe the repo as it was.
2. **Navigation.** `docs/testing.md` is 983 lines doing at least five jobs.
3. **Drift.** Nothing forces a new target to be documented, which is how the
   list in (1) grew without anyone noticing.

Two more, found while scoping:

4. `fuzz-drive` carries a rule but no `.PHONY` declaration.
   `TestEveryPhonyTargetHasARule` checks that every `.PHONY` name has a rule;
   nothing checks the reverse.
5. A `.PHONY` target with a rule line but an **empty recipe** is also
   undetected: `make` prints "Nothing to be done" and exits 0, and
   `makefiletargets_audit_test.go`'s parser records the rule as present because
   it only looks at column-zero `name:` lines. This is a third variant of the
   `lint-fuzz-registry` failure — a gate that reports green while executing
   nothing — and no audit covers it today.

## Decisions (owner-approved 2026-08-20)

Settled; do not re-litigate.

1. **Decompose the Makefile** into per-family `.mk` files. Raised as
   questionable on grounds of size (522 lines) and audit coupling; owner
   confirmed authoring friction is real and the split is in scope.
2. **Every target must carry a documentation annotation. No exemptions.** No
   ignore-list, no naming-convention carve-out. Implemented one notch stricter
   than stated: the audit keys on **rules**, a strict superset of `.PHONY`
   names, so a target like `fuzz-drive` that is missing its `.PHONY`
   declaration cannot slip through by way of a second omission.
3. **Generate the reference, hand-write the prose.** A target table is
   generated into a marked region inside each hand-written doc — not a separate
   generated file, and not a fully generated doc.
4. **Tiered annotation schema.** Every target needs a one-line summary; gates
   additionally carry structured fields. A uniform schema would force filler
   prose onto `clean` and `build-tui`.
5. **`docs/developing-evener/`** is the directory name. It absorbs every
   dev-facing doc, not only the family docs.
6. **Link policy: update live references, leave history stale.** Dated plans,
   notes and proofs under `docs/superpowers/` and `docs/design/` keep their
   existing paths, consistent with the precedent set by
   `2026-08-19-infra-standardization-design.md` ("Historical plan docs ... stay
   untouched").
7. **The generated table replaces the Canonical Gate Matrix.** The matrix's
   per-target rows become generated output in each family doc. Its rows that
   are not make targets move to hand-written prose. The repo ends with one
   authoritative gate table, and it is generated.

## Changes

### 1. Makefile → `make/*.mk`

The root `Makefile` retains cross-family variables, the `run_quiet_lint`
define, an explicit default goal, and a single `include` line. **The root file
contains no rules** — enforced by audit, because the generator reads
`make/*.mk` only, so a rule left in the root would be annotated but have no
destination doc.

Variables move with their family unless genuinely shared. `GO_MODULES`,
`FUZZ_GO_MODULES` and `DEV_TOOLING_TEST_SCRIPTS` stay in the root. `LDFLAGS`,
`PREFIX`, `BINDIR`, `EVENER_SHARE_BINDIR`, `INSTALL_BUILD_DIR`,
`EVENER_INSTALL_BINS` and `BUILD_CHANNEL` are read only by building-family
targets and move to `make/building.mk`. `LINT_TARGETS` moves to
`make/linting.mk`; `FUZZ_SEED_REPLAY` and `FUZZ_GOWORK` to `make/fuzzing.mk`.

**The include must be anchored to the makefile, not the cwd:**

```make
include $(dir $(lastword $(MAKEFILE_LIST)))make/*.mk
```

A bare `include make/*.mk` resolves against the **current working directory**.
`scripts/gate/merge-approval-gate-selftest.sh:151-156` runs
`make -f "$repo_root/Makefile"` after `cd "$make_repo"`, so a relative include
can never resolve there. Verified on GNU Make 3.81: bare include from a foreign
cwd exits 2; the `MAKEFILE_LIST`-anchored form succeeds, including a
cross-file prerequisite.

**The default goal must become explicit.** Today it is implicit — `build` at
`Makefile:15` is simply the first rule, and there is no `.DEFAULT_GOAL`
anywhere. Once the rules live in globbed includes, "first rule wins" resolves
against glob order, which is alphabetical and therefore an accident of
filenames. The root gains `.DEFAULT_GOAL := build`.

Six files, **named for the doc that documents them**. That is the membership
rule, and it makes the generator's mapping a filename stem rather than a table
someone has to maintain:

| File | Doc | Targets |
| --- | --- | --- |
| `make/building.mk` | `building.md` | build, build-runtime, build-go, build-hub, build-web, web-preflight, build-tui, build-doctor, build-all, build-linux, build-llmcall, build-migrate, dist, install, install-home, install-system, test-install |
| `make/testing.mk` | `testing.md` | test, test-short, test-race, test-web, test-web-browser, test-dev-tooling, test-timing-budget, test-timing-budget-selftest, test-rebaseline, merge-approval-gate, vet |
| `make/linting.mk` | `linting.md` | lint, lint-naming, lint-gofmt, lint-evenerfuzz, lint-eval, lint-internal, lint-golangci, lint-generated, lint-fuzz-registry, secret-scan |
| `make/fuzzing.mk` | `fuzzing.md` | test-fuzz, fuzz, fuzz-seeds, fuzz-nightly, fuzz-triage, fuzz-continuous, fuzz-bisect, fuzz-bisect-selftest, fuzz-oracle-audit, fuzz-oracle-audit-selftest, fuzz-mutation-score, fuzz-ledger, fuzz-gap-check, fuzz-registry-check, fuzz-goldens, fuzz-corpus-scan, fuzz-drive, mutation-floor |
| `make/coverage.mk` | `coverage.md` | coverage-floor, coverage-gaps, coverage-gaps-selftest, e2e-cover |
| `make/repo.mk` | `README.md` | help, clean, generate, tools, refresh-model-catalog |

65 targets: the 64 retained rules after the approved selftest retirement, plus
`help`. Placements that could go either way:

- `mutation-floor` reads like a coverage ratchet but invokes
  `scripts/fuzz/fuzz-mutation-score.sh`. Fuzz family.
- `test-fuzz` is a required-CI test gate, but what it runs is the
  seqfuzz/schemafuzz `rapid.Check` family, and the 109-line section of
  `docs/testing.md` explaining it is fuzz content. Fuzz family. Family
  assignment tracks **which doc explains the target**, not which CI job runs it.
- `vet` is Go analysis but is not in `LINT_TARGETS`; it is a standalone gate
  listed beside `make test-race`. Test family.
- `make/repo.mk` covers repo chores that belong to no gate family. Its doc,
  `docs/developing-evener/README.md`, doubles as the directory index.

Each `.mk` declares its own `.PHONY`. The single 64-name `.PHONY` line is
deleted; `fuzz-drive` gains the declaration it is missing today.

#### Toolchain constraint: GNU Make 3.81

macOS ships GNU Make **3.81** (2006); CI runners have 4.x. Everything here must
work on both, so the design uses no feature newer than 3.81. Verified on 3.81:

| Behaviour | Result |
| --- | --- |
| `include $(dir $(lastword $(MAKEFILE_LIST)))make/*.mk`, normal cwd | works |
| Same, invoked as `make -f <abs>/Makefile` from a foreign cwd | works |
| Bare `include make/*.mk`, `make -f <abs>/Makefile` from a foreign cwd | **fails**, exit 2 |
| Prerequisite in one `.mk` referring to a target in another | works |
| Root-file variable read inside an included `.mk` | works |
| Root-file `define` expanded via `$(call …)` from an include | works |
| `.DEFAULT_GOAL := build` in root, `build` defined in an include | works |
| Glob order | alphabetical |
| `include` matching **nothing** | hard error, exit 2, names the missing pattern |
| `.PHONY` target with a rule line and empty recipe | "Nothing to be done", **exit 0** |

The last row is motivation item 5: make will not tell you a gate is hollow.

### 2. Target annotation schema

A `##` comment block immediately above the rule. Plain `#` comments keep their
current meaning — implementation rationale for someone editing the recipe — and
are not published.

```make
## Remove the built binaries from the repo root.
clean:
	rm -f evener evener-hub evener-tui evener-doctor llmcall evener-migrate evener-linux-amd64

## Go lint, formatting, tagged floors, generated outputs, and secrets.
## proves: TOML naming; gofmt over every tracked .go file; the evenerfuzz and
##   eval compile floors; the internal-type check; golangci-lint across every
##   workspace module.
## trigger: required CI; local pre-merge.
## requires: golangci-lint, gitleaks.
## fails-when: any member of LINT_TARGETS exits nonzero.
lint: $(LINT_TARGETS)
```

Grammar:

- The **summary** is the leading run of `##` lines that contain no `key:`
  prefix, joined with single spaces. Required for every target.
- **Fields** are `## <key>: <value>`. Keys: `proves`, `trigger`, `requires`,
  `fails-when`. All optional.
- **Continuations** are `##` followed by three spaces, appended to the previous
  field or to the summary with a single joining space.
- Unknown keys are an error. A typo'd `## trigers:` must fail the build rather
  than vanish from the output.
- **Target-specific variable lines do not carry annotations.** `install-home`
  and `install-system` each have a `PREFIX := …` line at column zero
  (`Makefile:103,106`) followed by a separate prerequisite line. The parser
  attaches the block to the line bearing the prerequisites/recipe, and treats a
  `##` block above a target-specific variable line as an error rather than
  silently publishing a summary against an assignment.
- The block must be **contiguous with the rule**. `lint-naming`'s explanatory
  comment (`Makefile:410-412`) is separated from its rule by the
  `define run_quiet_lint` block; a mechanical `#`→`##` conversion there would
  misattribute it. Blocks separated from their rule by any non-comment line are
  an error.

**The narrative moves out.** Several targets carry long explanatory comments
today — `lint-evenerfuzz`'s runs 19 lines (`Makefile:428-446`) and is currently
the best documentation that target has anywhere. That prose relocates to the
hand-written body of the corresponding doc. `##` carries only what fits a table
cell. Generating table cells from essays produces an unreadable table and a doc
that says nothing.

### 3. Generator: `internal/maketargetsdoc`

Follows the pattern established by `internal/appwiredoc`: a Go program under
`internal/`, driven by `//go:generate`, emitting output carrying a DO-NOT-EDIT
marker and gated for staleness by `lint-generated`.

It differs from `appwiredoc` in one way, deliberately: `appwiredoc` owns its
whole output file and keeps prose in a `.tmpl`. Here the docs are mostly prose,
so the generator **rewrites a marked region in place**:

```markdown
<!-- BEGIN GENERATED: make targets. Edit make/linting.mk, then run `make generate`. -->
| Command | What it proves | Trigger | Requires | Fails when |
| --- | --- | --- | --- | --- |
| `make lint` | … | … | … | … |
<!-- END GENERATED -->
```

Behaviour:

- Parses `make/*.mk`; the file stem selects the destination doc
  (`make/linting.mk` → `docs/developing-evener/linting.md`).
- Targets with structured fields render as the wide table. Targets with only a
  summary render as a compact two-column list under an `Other targets`
  subheading, so `clean` does not get four empty cells. A family whose targets
  all lack fields emits only the compact list, with no empty wide table.
- A doc with no marked region, a region with no matching `.mk`, a `.mk` with no
  destination doc, or an unterminated region is an error.
- Content outside the markers is never touched.

**`lint-generated` must run the generator, not just diff its output.** Today
the recipe carries its own copy of `go generate ./appwire/...`
(`Makefile:504-505`) rather than calling the `generate` target. Adding five doc
paths to its `git diff --exit-code` list without widening what it regenerates
would produce a gate that is green forever while checking nothing — the exact
`lint-fuzz-registry` failure this design exists to prevent. The fix is to make
`lint-generated` invoke `$(MAKE) generate`, which also removes the existing
latent duplication between the two recipes.

### 4. `make help`

The same binary in `-mode help`, so there is one annotation parser rather than
two implementations that can disagree. Prints targets grouped by family with
their summaries. It lives in `make/repo.mk` and is documented in
`docs/developing-evener/README.md` like any other target. `make` with no
argument keeps building.

### 5. Audits and other Makefile consumers

| File | Change |
| --- | --- |
| `makefiletargets_audit_test.go` | `TestEveryPhonyTargetHasARule` and `TestEveryLintTargetIsPhonyAndHasARule` read `Makefile` + `make/*.mk` as one set. Its comment at `:85-86` referencing the gate matrix needs updating for decision 7. |
| `makefile_audit_test.go` | Delete-safety predicate must see every recipe line in the repo, not just the root file's. |
| `install_fuzz_test.go` | Reads `Makefile`; becomes file-set aware. |
| `install_test.go`, `runtime_pair_build_test.go` | Copy `Makefile` into fixture roots and run `make` there; must copy `make/*.mk` too. |
| `scripts/gate/merge-approval-gate-selftest.sh` | Runs `make -f <abs>/Makefile` from a foreign cwd. Fixed by the anchored include (§1); the fixture at `:86-92` does not need `make/`. Registered as `gate/merge-approval-gate` in `DEV_TOOLING_TEST_SCRIPTS` (`Makefile:149`), so it is in `make test-dev-tooling` and CI. |
| `agent/workspace_info.go` | **Product code.** `parseMakefileTargets` (`:196`, `:252-315`) reads only the root `Makefile` and has no `include` handling. After the split it returns variable names, and because `len(targets) > 0` still holds it emits *wrong* content rather than none. Must follow the include. Needs a test against a split-shaped fixture; every existing test uses `t.TempDir()` fixtures and would stay green. |
| `scenariosourcecite_audit_test.go` | Hardcodes `docs/agentic-testing.md` at `:214` and `t.Fatalf`s on a read error at `:75`. Moving that file breaks this test. |
| **New:** `TestEveryTargetHasASummaryAnnotation` | Every rule in the file set has a `##` summary. Keyed on rules, not `.PHONY`. No exemption list. |
| **New:** `TestEveryRuleIsPhony` | Closes the reverse of `TestEveryPhonyTargetHasARule`. Catches `fuzz-drive` today. |
| **New:** `TestEveryRuleHasARecipe` | Closes motivation item 5: a rule line with an empty recipe is a hollow gate that make reports as success. |
| **New:** `TestRootMakefileHasNoRules` | Enforces §1's rule that all rules live in `make/*.mk`, so nothing can be annotated-but-unpublishable. |

### 6. `docs/developing-evener/`

#### Where every section of `docs/testing.md` goes

| Section | Destination |
| --- | --- |
| `## Test Reliability Policy` (:3) | testing.md |
| `## Flakes and Timeouts` (:36) | testing.md |
| `## Destructive Operations and the Tooling Test Estate` (:66) | testing.md |
| `## Canonical Gate Matrix` (:140) | **dissolved** — see below |
| `## Post-Merge Gate` (:173), incl. `### Frontend setup boundary` (:233) and `### Whole-system residue audit` (:251) | testing.md, **kept whole**; building.md cross-links to the frontend subsection rather than moving it out of its parent |
| `## The seqfuzz/schemafuzz Family Lives Only in make test-fuzz` (:291) | fuzzing.md (with `test-fuzz` itself) |
| `## Proving a Type Survives a Round Trip` (:400) | fuzzing.md — it is about property/round-trip coverage where hand-written fixtures are insufficient |
| `## The Three Browser Guards` (:443) | testing.md |
| `## A Single tmux capture-pane Can Lie` (:539) | testing.md |
| `## A Test That Never Runs` (:626) | testing.md |
| `## Real git in Worktree Tests` (:709) + 3 subsections | testing.md |
| `## Seeding Hub Fixtures` (:789) | testing.md |
| `## A Disposable Hub Needs Its Own HOME` (:816) | testing.md |
| `## A Live Run Uses the Machine's Build Cache` (:839) | testing.md (live-test prose) |
| `## MCP Server E2E` (:853) | testing.md |
| `## Environment Variable Tests` (:868) | testing.md |
| `## OpenAI Codex Backend E2E` (:883) | testing.md |
| `## Anthropic Messages API E2E` (:936) | testing.md |

**Dissolving the gate matrix.** Its per-target rows become the generated
regions in each family doc. Five rows are not make targets and become
hand-written prose in `testing.md` under "Gates that are not make targets":
`scripts/web/web-preflight.sh`, `ROOT_FULL=1 make test`, the
`EVENER_LIVE_TESTS=1` opt-in block, the
`EVENER_E2E_LIVE=1 scripts/coverage/e2e-cover.sh` block, and the
"Launcher health checks … outside this repository's gates" row.

The matrix's seven columns do not map one-to-one onto the generated five. Two
columns are dropped by design: **Scope** collapses into the summary, and
**Owner and follow-up** is boilerplate ("Evener CI/tooling; no new follow-up
currently") on nearly every row. Cells too large for a table — `make
test-web-browser`'s determinism cell runs to several hundred words — become
prose in the family doc, with the generated row carrying the one-line version.
This is the single largest piece of judgement work in the whole change and it
is not mechanical.

#### Family docs

- **`building.md`** — build, dist, install; `web-preflight`.
- **`testing.md`** — as mapped above.
- **`linting.md`** — the eight lint passes; why the two tagged passes exist;
  the `server/appwire_*.go` camelCase regime; the golangci-lint cross-checkout
  cache hazard (issue #290).
- **`coverage.md`** — **owns** the two-track explanation (test track unioned
  with deterministic fuzz-seed replay), why a default-gate `-cover` number is
  neither whole-repo coverage nor "how well is this tested", and the
  EXECUTED-vs-TESTED distinction for `cmd/evener-hub/cov_*_test.go`.
- **`fuzzing.md`** — absorbs today's `docs/fuzzing.md` plus the two sections
  above. Links to `coverage.md` for the two-track idea; does not restate it.
- **`README.md`** — directory index plus the `make/repo.mk` targets.

The coverage/fuzzing boundary is the one place this split can make things
worse: the two-track idea is the most load-bearing coverage fact in the repo
and is inherently about both. `coverage.md` owns it and `fuzzing.md` links.

#### Complete `docs/` disposition

All 33 entries, so commit 3's scope is determined rather than inferred.

**Move** (9): `testing.md`, `fuzzing.md`, `dev-checklist.md`,
`agentic-testing.md`, `agent-test-serial-prefix.md`,
`performance-profiling.md`, `environment.md`, `worktrees.md`, `conventions/`.

**Stay** (24): `architecture.md`, `appwire-protocol.md`, `job-control.md`,
`llm-providers.md`, `llm-provider-config-and-launch.md`, `ollama.md`,
`openai-spend-diagnostics.md`, `sandboxing.md`, `hooks.md`, `skills.md`,
`terminal-bench.md`, `subagent-runtime-contracts.md`,
`evener-hub-remote-operations.md`, `evener-hub-web-routing.md`,
`resume-model-context-fix-plan.md`, `design/`, `research/`, `specs/`,
`skills/`, `subagent-management/`, `superpowers/`, `tools/`, `web-ui/`,
`original-attractor-specs/`.

Rationale for the borderline stays: `skills.md` and `terminal-bench.md`
document capabilities and an external benchmark, not this repo's gates;
`subagent-runtime-contracts.md` and the two `evener-hub-*` files are subsystem
reference; `resume-model-context-fix-plan.md` is a dated plan.

### 7. Link policy

**Inbound.** Referring-file counts, measured 2026-08-20 (files, not
occurrences; the occurrence count is roughly 228):

| Doc | Referring files | In dated plans/notes/proofs |
| --- | --- | --- |
| `docs/testing.md` | 130 | 91 |
| `docs/agentic-testing.md` | 109 | 15 |
| `docs/conventions/` | 20 | 14 |
| `docs/environment.md` | 15 | 9 |
| `docs/fuzzing.md` | 10 | 6 |
| `docs/worktrees.md` | 5 | 4 |
| `docs/performance-profiling.md` | 5 | 5 |
| `docs/dev-checklist.md` | 0 | 0 |
| `docs/agent-test-serial-prefix.md` | 0 | 0 |

Live references are updated: Go sources, scripts, `AGENTS.md`, `Makefile`,
`testing-budget.json`, `fuzz/README.md`, current docs, and the `test/` scenario
corpus. References in dated plans, notes and proofs are left as-is.

**Outbound.** References *from* the moving docs *to* unmoved paths break on the
move and are invisible to an inbound-only check. Seven exist:

```
docs/fuzzing.md:12,100,275   ](../fuzz/README.md)
docs/fuzzing.md:13,149       ](skills/fuzzing-an-api-surface/SKILL.md)
docs/fuzzing.md:14           ](design/fuzzing-toolkit-design.md)
docs/environment.md:83       ](sandboxing.md#caches-are-contained-never-poisoned)
```

Each gains one `../` level. Non-link path mentions in prose need the same
treatment — `docs/testing.md:870` names `docs/environment.md`, and both files
move.

## Commit plan (atomic, each leaves the gates green)

The doc move comes **before** the generator, because the generator's stem→doc
mapping needs all six destination docs to exist and three of them
(`building.md`, `linting.md`, `coverage.md`) do not exist today.

1. **Makefile → `make/*.mk`; consumers updated.** Anchored include, explicit
   default goal, per-file `.PHONY`, `fuzz-drive` declared. Updates the four
   Makefile-reading tests, the two fixture tests, and
   `agent/workspace_info.go`. Adds `TestEveryRuleIsPhony`,
   `TestEveryRuleHasARecipe`, `TestRootMakefileHasNoRules`. No target behaviour
   changes, no annotations yet. Verified by planted regressions (below).
2. **Doc split, directory move, link sweep.** Creates all six docs under
   `docs/developing-evener/` with hand-written prose and empty generated-region
   markers; updates live inbound refs and the seven outbound links; fixes
   `scenariosourcecite_audit_test.go:214`. **The gate matrix is copied over
   intact and is not dissolved here** — see below.
3. **Annotations, generator, `make help`, annotation audit, matrix dissolution.**
   Fills the marked regions. Rewires `lint-generated` to call `$(MAKE) generate`
   and adds the six docs to its diff list. Adds
   `TestEveryTargetHasASummaryAnnotation`. Only now deletes the gate matrix,
   because this is the first commit in which generated output exists to replace
   it.

Dissolving the matrix in commit 2 would leave the repo with the matrix gone and
its replacement not yet generated — gates green, but no gate reference at all
for the length of one commit. Deferring the deletion to commit 3 costs one
commit of duplication (an intact matrix beside empty markers) and buys
continuous documentation.

Splitting 2 from 3 keeps the rename-and-relink diff free of semantic content,
and keeps the annotation prose reviewable against files that are already in
their final location.

## Verification

- `make lint`, `make test`, `make test-dev-tooling` green at each commit.
- **Commit 1 planted regressions.** The audits being rewritten are the
  tripwires that caught `lint-fuzz-registry` reporting PASS while running
  nothing. Three plants, each reverted after:
  - Delete a whole **rule line** from `make/linting.mk` → `TestEveryPhonyTargetHasARule` must fail.
    (Deleting the *recipe body* does **not** work as a plant: the parser records
    a rule from any column-zero `name:` line and never inspects the tab-indented
    recipe, so the audit passes. That is what `TestEveryRuleHasARecipe` is for.)
  - Empty a recipe body → `TestEveryRuleHasARecipe` must fail.
  - Remove a `.PHONY` entry → `TestEveryRuleIsPhony` must fail.
- **Commit 1 consumer check.** `make -f "$PWD/Makefile" build` from a foreign
  cwd must succeed, and `scripts/gate/merge-approval-gate-selftest.sh` must
  pass. Run `parseMakefileTargets` against the post-split root and confirm it
  returns real targets, not variable names.
- **Commit 2 link check.** No live (non-historical) inbound reference to a
  moved path survives, **and** every relative link inside a moved doc resolves.
  A grep for `](…)` targets that do not exist on disk is the check; the repo
  has no link checker today, so this is a scripted one-off unless we choose to
  keep it.
- **Commit 3 generator check.** `make generate && git diff --exit-code` clean;
  then mutate one `##` summary, re-run `make lint`, confirm `lint-generated`
  fails. This check is only meaningful once `lint-generated` calls
  `$(MAKE) generate` — as originally written it would have reported a false
  pass.

All lint runs in this worktree use an isolated `GOLANGCI_LINT_CACHE`. A
content-identical checkout at a second path is precisely the condition that
poisons the shared cache (issue #290); doing this work in a worktree without
isolating the cache would break the main checkout's `make lint` the moment the
worktree is removed.

## Risks and fidelity notes

- **Dissolving the gate matrix (highest).** Its richest cells do not fit a
  generated table and must be rewritten as prose without losing content. This
  is the least mechanical work in the change.
- **Audit weakening.** Covered by the three planted regressions above.
- **`agent/workspace_info.go` is product code.** Commit 1 now changes behaviour
  of the agent's workspace prompt. Every existing test uses `t.TempDir()`
  fixtures, so the suite would stay green through a regression here; a
  split-shaped fixture test is required, not optional.
- **Annotation prose quality.** Moving `lint-evenerfuzz`'s 19-line comment into
  `linting.md` is a genuine rewrite, not a cut-and-paste. It goes in the prose
  body, not the table.
- **Stale historical links.** Links in dated documents will not resolve after
  commit 2. Accepted cost of decision 6.
- **Annotation accuracy is unaudited.** `TestEveryTargetHasASummaryAnnotation`
  checks presence, not truth. A summary can drift from its recipe and no gate
  will say so. Accepted; the alternative is unbuildable.

## Out of scope

- The per-checkout `GOLANGCI_LINT_CACHE` guard from issue #290. Parked by
  ruling until the bug recurs.
- Rewriting the historical plan and proof documents.
- Any change to what the gates check. This work moves, documents and audits
  targets; it does not alter a recipe's behaviour. (`agent/workspace_info.go`
  is the one deliberate product change, and it exists to preserve today's
  behaviour across the split, not to alter it.)

## Corrections after review

Two independent adversarial reviews of the first draft found nine defects
between them, eight each with substantial overlap. All are fixed above.

1. **`lint-generated` would have been vacuous.** The draft added five doc paths
   to its `git diff` list; the recipe regenerates only `./appwire/...`, so
   nothing would ever have been stale. Now calls `$(MAKE) generate`.
2. **`scripts/gate/merge-approval-gate-selftest.sh` was missed entirely**, and
   a bare relative include cannot work there. Include is now anchored to
   `MAKEFILE_LIST`; fix verified on 3.81.
3. **`agent/workspace_info.go` was missed** — a silent product regression the
   draft's "no product impact" claim explicitly denied.
4. **Commit 2 was impossible as ordered** — three destination docs did not
   exist. Commits 2 and 3 are swapped.
5. **The claim that 86 scenario references were "machine-verified" by
   `scenariosourcecite_audit_test.go` was false.** That audit resolves only
   `.go/.ts/.tsx/.js/.jsx` (`:226-228`) and never `.md`. It also hardcodes the
   doc path at `:214`, so the move breaks it outright.
6. **The planted-regression check could not fire.** Deleting a recipe body
   leaves the rule line, which the parser records. Corrected, and the gap it
   exposed became `TestEveryRuleHasARecipe` and motivation item 5.
7. **The Canonical Gate Matrix had no owner.** Now decision 7, with a
   section-by-section disposition.
8. **Outbound links were unaccounted for.** Seven break on the move; they were
   invisible to the draft's inbound-only criterion.
9. **A third of `docs/testing.md` had no assigned home**, and
   `### Frontend setup boundary` was assigned to a different file from its
   parent section. §6 now maps every section, and keeps that subsection with
   its parent.

Smaller corrections: `LDFLAGS`/`PREFIX`/`BINDIR` are building-family, not
cross-family; `make help` had no family home (now `make/repo.mk`, which also
resolves rules-in-root having no destination doc); the `clean` example
annotation described a `.build` cleanup the recipe does not do;
`lint-evenerfuzz`'s comment is 19 lines, not 15; the annotation grammar had no
rule for target-specific variable lines, for comments separated from their rule
by an intervening block, or for multi-line summaries; the §7 counts are
referring files, not occurrences.

## Corrections during execution

The design survived implementation largely intact, but execution found six
defects in it. Each is recorded here because the sections above still read as
originally written.

1. **§3's table was missing a Summary column.** It fixed five columns —
   `Command | What it proves | Trigger | Requires | Fails when` — while §6 said
   "Scope collapses into the summary". Together those meant a gate target's
   `##` summary was required by the audit, printed by `make help`, published in
   no doc, and therefore gated by nothing: 36 of 65 targets. Proven by planting
   a mutated summary on `coverage-floor` and watching `make lint` pass on an
   empty diff while `make help` printed the planted text. The wide table is now
   `Command | Summary | What it proves | Trigger | Requires | Fails when`.
   Accepted cost: on single-fact gates such as `lint-naming`, `proves` mildly
   restates the summary.

2. **`lint-generated` must diff against `HEAD`, not the index.** `git diff
   --exit-code -- <paths>` compares the working tree to the index, so a
   regeneration that is staged but not committed passes the gate while `HEAD`
   is stale. Now `git diff --exit-code HEAD -- <paths>`. Measured consequence,
   correcting an overclaim made when this was ruled: HEAD-diff is strictly
   stricter in both directions and relaxes nothing — `git add` simply stops
   silencing it. The trade is that `make lint` is now red on any uncommitted
   change to those eight paths, including hand-written prose outside the marker
   regions, because the diff is path-scoped rather than region-scoped.

3. **`go generate` runs a directive from its own package's directory.** Wiring
   `go generate ./internal/maketargetsdoc/...` would have run the generator with
   root `internal/maketargetsdoc`, where the `make/*.mk` glob matches nothing;
   `filepath.Glob` returns `(nil, nil)`, so it would have regenerated nothing
   and exited 0, and `lint-generated` would have diffed six unchanged docs
   forever. The directive carries `-root ../..` and the generator hard-errors on
   an empty glob.

4. **§2 never mentions `.PHONY`.** `.PHONY: a b c` at column zero is
   syntactically indistinguishable from a rule line and every family file opens
   with one, so the parser must special-case dot-directives.

5. **A plain `#` comment does not break annotation contiguity.** §2 says "any
   non-comment line", and a `#` line is a comment — but the first parser
   rejected it, which would have failed every target whose existing `#`
   rationale sits directly above its rule. Convention, now standardised: the
   `##` block goes last, immediately above the rule, below any surviving `#`
   narrative. Related: summaries must be capitalised, because a summary opening
   with a lowercase word immediately followed by `: ` is read as a field attempt
   and rejected as an unknown key.

6. **Decision 6's historical exclusion omitted `docs/superpowers/research/`.**
   It holds fourteen dated evidence records of the same character as
   `plans/`, `notes/` and `proofs/`. Three were wrongly rewritten and reverted.
   The exclusion is `(plans|notes|proofs|specs|research)`.

Two commit-plan deviations, both deliberate:

- **The gate matrix survives commit 2 and is retired in commit 3**, the same
  commit that generates its replacement. Dissolving it earlier would leave the
  repo with no gate reference at all for the length of one commit.
- **`make/repo.mk` is annotated in the `make help` task, not the annotation
  task**, so that the two tasks — run concurrently in separate worktrees —
  could not collide on one file.
