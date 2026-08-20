# Makefile and Developer-Docs Decomposition

## Goal

Make the repository's `make` targets discoverable, documented, and unable to
drift out of documentation silently.

Three concrete failures motivate this:

1. **Discovery.** `mutation-floor`, `fuzz-drive`, `coverage-gaps`,
   `coverage-gaps-selftest` and `test-install` appear nowhere in `docs/`,
   `README.md` or `AGENTS.md`. `fuzz-mutation-score`, `fuzz-ledger`,
   `fuzz-oracle-audit`, `fuzz-registry-check` and `test-short` appear only in
   dated plan documents that describe the repo as it was, not as it is.
2. **Navigation.** `docs/testing.md` is 983 lines doing at least five jobs.
3. **Drift.** Nothing forces a new target to be documented, which is how the
   list in (1) grew to eleven without anyone noticing.

A fourth, found while scoping this: `fuzz-drive` carries a rule but no `.PHONY`
declaration. `TestEveryPhonyTargetHasARule` checks that every `.PHONY` name has
a rule; nothing checks the reverse, so a file named `fuzz-drive` at the repo
root would silently turn `make fuzz-drive` into a no-op.

## Decisions (owner-approved 2026-08-20)

Settled; do not re-litigate.

1. **Decompose the Makefile** into per-family `.mk` files. Raised as
   questionable on grounds of size (522 lines) and audit coupling; owner
   confirmed authoring friction is real and the split is in scope.
2. **Every `.PHONY` target must carry a documentation annotation. No
   exemptions.** No ignore-list, no naming-convention carve-out. Implemented
   one notch stricter than stated: the audit keys on **rules**, a strict
   superset of `.PHONY` names, so a target like `fuzz-drive` that is missing
   its `.PHONY` declaration cannot slip through the annotation check by way of
   a second omission.
3. **Generate the reference, hand-write the prose.** A target table is
   generated into a marked region inside each hand-written doc — not a
   separate generated file, and not a fully generated doc.
4. **Tiered annotation schema.** Every target needs a one-line summary; gates
   additionally carry structured fields. A uniform schema would force filler
   prose onto `clean` and `build-tui`.
5. **`docs/developing-evener/`** is the directory name. It absorbs every
   dev-facing doc, not only the five gate docs.
6. **Link policy: update live references, leave history stale.** Dated plans,
   notes and proofs under `docs/superpowers/` and `docs/design/` keep their
   existing paths, consistent with the precedent set by
   `2026-08-19-infra-standardization-design.md` ("Historical plan docs ... stay
   untouched").

## Changes

### 1. Makefile → `make/*.mk`

The root `Makefile` retains **cross-family** variables (`LDFLAGS`, `PREFIX`,
`BINDIR`, `GO_MODULES`, `FUZZ_GO_MODULES`, …), the `run_quiet_lint` define, an
explicit default goal, and a single `include` line. Variables used by exactly
one family move with that family: `LINT_TARGETS` to `make/linting.mk`,
`FUZZ_SEED_REPLAY` to `make/fuzzing.mk`.

**The default goal must become explicit.** Today it is implicit — `build` at
`Makefile:15` is simply the first rule in the file, and there is no
`.DEFAULT_GOAL` anywhere. Once the rules live in globbed includes, "first rule
wins" resolves against glob order, which is alphabetical and therefore an
accident of filenames. The root file gains `.DEFAULT_GOAL := build`. Verified
working with the target defined in an include (see Toolchain constraint).

Five files, **named for the doc that documents them**. That is the membership
rule, and it makes the generator's mapping a filename stem rather than a table
someone has to maintain:

| File | Targets |
| --- | --- |
| `make/building.mk` | build, build-runtime, build-go, build-hub, build-web, web-preflight, build-tui, build-doctor, build-all, build-linux, build-llmcall, build-migrate, dist, install, install-home, install-system, test-install, tools, generate, clean, refresh-model-catalog |
| `make/testing.mk` | test, test-short, test-fuzz, test-race, test-web, test-web-browser, test-dev-tooling, test-timing-budget, test-timing-budget-selftest, test-rebaseline, merge-approval-gate, vet |
| `make/linting.mk` | lint, lint-naming, lint-gofmt, lint-evenerfuzz, lint-eval, lint-internal, lint-golangci, lint-generated, lint-fuzz-registry, secret-scan |
| `make/fuzzing.mk` | fuzz, fuzz-seeds, fuzz-nightly, fuzz-triage, fuzz-continuous, fuzz-bisect, fuzz-bisect-selftest, fuzz-oracle-audit, fuzz-oracle-audit-selftest, fuzz-mutation-score, fuzz-ledger, fuzz-gap-check, fuzz-registry-check, fuzz-goldens, fuzz-corpus-scan, fuzz-drive, mutation-floor |
| `make/coverage.mk` | coverage-floor, coverage-floor-selftest, coverage-gaps, coverage-gaps-selftest, e2e-cover |

Notes on two placements that could go either way:

- `mutation-floor` reads like a coverage ratchet but invokes
  `scripts/fuzz/fuzz-mutation-score.sh`. It is fuzz-family.
- `vet` is Go analysis, but it is a standalone required-CI gate rather than one
  of `LINT_TARGETS`. It goes with the test family, matching how the gate matrix
  already lists it beside `make test-race`.

Each `.mk` declares its own `.PHONY`. The single 64-name `.PHONY` line is
deleted; `fuzz-drive` gains the declaration it is missing today.

`include make/*.mk` uses a glob, so a new family file is picked up without
editing the root Makefile. Glob order is alphabetical and stable; no target
depends on include order because the default goal is set explicitly.

#### Toolchain constraint: GNU Make 3.81

macOS ships GNU Make **3.81** (2006); CI runners have 4.x. Everything here must
work on both, so the design uses no feature newer than 3.81. Verified on 3.81
before writing this spec:

| Behaviour | Result |
| --- | --- |
| `include make/*.mk` with matching files | works |
| Prerequisite in one `.mk` referring to a target in another | works |
| Root-file variable read inside an included `.mk` | works |
| `.DEFAULT_GOAL := build` in root, `build` defined in an include | works |
| `include make/*.mk` matching **nothing** | hard error, exit 2 — `make/*.mk: No such file or directory` followed by `No rule to make target 'make/*.mk'` |

The last row matters for the fixture tests in §5: a fixture that forgets to
copy `make/*.mk` fails immediately with a message naming the missing include,
rather than degrading into a confusing "no rule to make target `web-preflight`".

### 2. Target annotation schema

A `##` comment block immediately above the rule. Plain `#` comments keep their
current meaning — implementation rationale for someone editing the recipe — and
are not published.

```make
## Remove built binaries and the .build tree.
clean:
	rm -f evener evener-hub ...

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

- The **summary** is the first `##` line. Required for every target. One
  sentence, imperative or noun-phrase, ending in a period.
- **Fields** are `## <key>: <value>`. Keys: `proves`, `trigger`, `requires`,
  `fails-when`. All optional; a target with none renders as a compact row.
- **Continuations** are `##` followed by three spaces, appended to the previous
  field with a single joining space.
- Unknown keys are an error, not a comment. A typo'd `## trigers:` must fail
  the build rather than vanish from the output.

**The narrative moves out.** Several targets carry long explanatory comments
today — `lint-evenerfuzz`'s is fifteen lines and is currently the best
documentation that target has anywhere. That prose relocates to the
hand-written body of the corresponding doc. `##` carries only what fits a table
cell. Generating table cells from essays produces an unreadable table and a
doc that says nothing.

### 3. Generator: `internal/maketargetsdoc`

Follows the pattern already established by `internal/appwiredoc`: a Go program
under `internal/`, driven by `//go:generate`, emitting output that carries a
DO-NOT-EDIT marker and is gated for staleness by `lint-generated`.

It differs from `appwiredoc` in one way, and deliberately: `appwiredoc` owns
its whole output file and keeps prose in a `.tmpl`. Here the docs are mostly
prose, so the generator **rewrites a marked region in place**:

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
- Targets with structured fields render as the wide table above. Targets with
  only a summary render as a compact two-column list below it, under a
  `Other targets` subheading, so `clean` does not get four empty cells.
- A doc with no marked region, a region with no matching `.mk`, or an
  unterminated region is an error.
- Content outside the markers is never touched.

`make generate` gains the invocation. `lint-generated` gains the five docs in
its `git diff --exit-code` list. **No new gate is introduced** — staleness
detection already exists and already runs in `make lint`.

### 4. `make help`

The same binary in `-mode help`, so there is one annotation parser rather than
two implementations that can disagree. Prints targets grouped by family with
their summaries. Becomes the default goal's neighbour, not the default goal
itself — `make` with no argument keeps building.

### 5. Audits

| Test | Change |
| --- | --- |
| `TestEveryPhonyTargetHasARule` | Read `Makefile` + `make/*.mk` as one set instead of `os.ReadFile("Makefile")`. |
| `TestEveryLintTargetIsPhonyAndHasARule` | Same. |
| `TestNoMakefileRecipeFeedsVariableToRecursiveDelete` | Same; the delete-safety predicate must see every recipe line in the repo, not just the root file's. |
| `install_fuzz_test.go` | Same. |
| **New:** `TestEveryTargetHasASummaryAnnotation` | Every rule in the file set has a `##` summary. Keyed on **rules**, not `.PHONY`, so `fuzz-drive`-shaped omissions cannot hide. No exemption list. |
| **New:** `TestEveryRuleIsPhony` | Closes the reverse direction of `TestEveryPhonyTargetHasARule`. Catches `fuzz-drive` today. |
| `install_test.go`, `runtime_pair_build_test.go` | These copy `Makefile` into a fixture root and run `make` there. They must copy `make/*.mk` too. |

The fixture copies fail loudly if missed — `make` errors on a missing include
rather than reporting success — but they are listed because the failure would
otherwise look like an unrelated regression.

### 6. `docs/developing-evener/`

`docs/testing.md` (983 lines) splits five ways:

- **`building.md`** — build, dist, install; the frontend setup boundary;
  `web-preflight`.
- **`testing.md`** — the test-family gate narrative; "A Test That Never Runs";
  the `tmux capture-pane` warning; the three browser guards; real-`git`
  worktree tests; hub fixture seeding; the disposable-hub HOME rule; live e2e.
- **`linting.md`** — the eight lint passes; why the two tagged passes exist;
  the `server/appwire_*.go` camelCase regime; the golangci-lint cross-checkout
  cache hazard (issue #290).
- **`coverage.md`** — **owns** the two-track explanation (test track unioned
  with deterministic fuzz-seed replay), why a default-gate `-cover` number is
  neither whole-repo coverage nor "how well is this tested", and the
  EXECUTED-vs-TESTED distinction for `cmd/evener-hub/cov_*_test.go`.
- **`fuzzing.md`** — absorbs today's `docs/fuzzing.md`. Links to `coverage.md`
  for the two-track idea and does not restate it.

The coverage/fuzzing boundary is the one place this split can make things
worse: the two-track idea is the most load-bearing coverage fact in the repo
and it is inherently about both. `coverage.md` owns it outright and
`fuzzing.md` links; duplicating it in both is how it rots out of sync.

Also relocated, unchanged in content: `dev-checklist.md`, `agentic-testing.md`,
`agent-test-serial-prefix.md`, `performance-profiling.md`, `environment.md`,
`worktrees.md`, and `conventions/`.

`docs/` top level is left as subsystem reference — `job-control.md`,
`llm-providers.md`, `appwire-protocol.md`, `sandboxing.md`, `hooks.md`,
`architecture.md`, and the existing `design/`, `research/`, `specs/`,
`skills/`, `subagent-management/`, `superpowers/`, `tools/`, `web-ui/` trees.

### 7. Link policy

Inbound references, measured 2026-08-20:

| Doc | Total | In dated plans/notes/proofs |
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

Approximately 150 live references are updated: Go sources, scripts,
`AGENTS.md`, `Makefile`, `testing-budget.json`, `fuzz/README.md`, current docs,
and the `test/` scenario corpus. Approximately 144 in dated plans, notes and
proofs are left pointing at the old paths.

86 of `agentic-testing.md`'s referrers are scenario files pinned by
`scenariosourcecite_audit_test.go`, so that chunk is machine-verified rather
than eyeballed.

## Commit plan (atomic, each leaves the gates green)

1. **Makefile → `make/*.mk`; audits read the file set.** No target behaviour
   changes, no annotations yet. Adds `fuzz-drive` to `.PHONY` and adds
   `TestEveryRuleIsPhony`. Verified by planting a regression (see Verification).
2. **Annotations, generator, `make help`, annotation audit.** Generated regions
   land in the docs at their *current* paths — `docs/testing.md`,
   `docs/fuzzing.md` — so this commit is reviewable without a rename diff on
   top of it.
3. **Doc split, directory move, link sweep.** The largest and most mechanical
   diff, landing last on a tree whose gates already verify the target
   inventory.

Splitting 2 from 3 is the point of the ordering: a reviewer reading commit 2
sees the annotation prose against unmoved files, and a reviewer reading commit 3
sees a rename-and-relink diff with no semantic content mixed in.

## Verification

- `make lint`, `make test`, `make test-dev-tooling` green at each commit.
- **Commit 1 audit-strength check.** The audits being rewritten are the
  tripwires that caught `lint-fuzz-registry` reporting PASS while running
  nothing (`makefiletargets_audit_test.go`'s header documents the incident).
  Making them file-set-aware must not weaken them, so commit 1 includes a
  deliberate check: delete a recipe body in `make/linting.mk` and confirm
  `TestEveryPhonyTargetHasARule` still fails; remove a `.PHONY` entry and
  confirm the new reverse test fails. Both are reverted before commit.
- **Commit 2 generator check.** `make generate && git diff --exit-code` clean;
  then mutate one `##` summary, re-run `make lint`, confirm `lint-generated`
  fails with the stale-output message.
- **Commit 3 link check.** No live (non-historical) reference to a moved path
  survives; `scenariosourcecite_audit_test.go` passes.

All lint runs in this worktree use an isolated `GOLANGCI_LINT_CACHE`. A
content-identical checkout at a second path is precisely the condition that
poisons the shared cache (issue #290); doing this work in a worktree without
isolating the cache would break the main checkout's `make lint` the moment the
worktree is removed.

## Risks and fidelity notes

- **Audit weakening (highest).** Covered by the commit-1 planted-regression
  check above.
- **Partial repo fixtures.** `install_test.go` and `runtime_pair_build_test.go`
  construct fixture roots and run `make` in them. They must copy `make/*.mk`
  alongside `Makefile`. This risk is smaller than it first looked: a missing
  include is a hard error naming the missing file (see Toolchain constraint),
  not a silent degradation.
- **Annotation prose quality.** Moving `lint-evenerfuzz`'s fifteen-line comment
  into `linting.md` is a genuine rewrite, not a cut-and-paste. The risk is that
  it gets compressed into a table cell and loses the reasoning. It does not go
  in the table; it goes in the prose body.
- **Doc-split judgement.** The five-way split of a 983-line file is the
  least mechanical part of commit 3 and the most likely to need a second pass.
- **Stale historical links.** ~144 links in dated documents will not resolve
  after commit 3. This is the accepted cost of decision 6.

## Out of scope

- The per-checkout `GOLANGCI_LINT_CACHE` guard from issue #290. Parked by
  ruling until the bug recurs; the worktree-local isolation used during this
  work is a local measure, not a repo change.
- Rewriting the historical plan and proof documents under `docs/design/plans/`
  and `docs/superpowers/plans/`.
- Any change to what the gates actually check. This work moves and documents
  targets; it does not alter a single recipe's behaviour.
