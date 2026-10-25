# Tech-debt audit: build-gates-scripts

Slice: repo-root `*_test.go` audit/gate tests, `Makefile`, `make/`, `scripts/`, `.github/workflows`, `testing-budget.json`, `fuzz/`, `cmd/evener-fuzz*`, `test/`.
Worktree: `.worktrees/debt-audit` (origin/main). Read-only audit; no test suites were run.

## Overview and inventory

| Area | Files | Lines |
| --- | --- | --- |
| Root `*_test.go` (the root package has NO non-test Go file) | 40 | 12,678 |
| of which `*_audit_test.go` (16 files) | 16 | ~5,500 |
| of which gate/timing/shell-lib tests (`gate*`, `timingbudget*`, `loadaware`, `testscope`) | 11 | ~4,100 |
| `Makefile` + `make/*.mk` | 7 | 1,276 (77 KB; 558 `##` and 374 `#` comment lines) |
| `scripts/` | 65 | 10,673 (shell is ~8,500) |
| `.github/workflows` + `.github/actions` | 9 | 1,291 (workflows only) |
| `testing-budget.json` | 1 | 12 KB (6.7 KB is prose in `_` fields) |
| `fuzz/` module | ~60 | 5,754 Go + 24 drive tasks + 4 patches |
| `cmd/evener-fuzz*` | 3 | ~4,000 |
| `test/` | 144 | 123 scenario cards (1.3 MB) + 2 shell scripts |
| `internal/maketargetsdoc` (generator for the make-target doc tables) | 14 | 3,642 (900 prod, 2,744 test) |

Repo-wide context: Go `_test.go` is 1,086,108 lines against 392,342 non-test Go lines (2.8:1).

The big picture. The slice has two layers of tooling that were never reconciled.

1. A hand-rolled shell gate (`run-module-tests.sh`, `gate-bounded.sh`, `load-aware-workers.sh`, a Python-in-bash timing ratchet) with thousands of lines of Go test scaffolding around it. `docs/developing-evener/testing.md:236-247` says tooling that accumulates logic belongs in Go and that faking the toolchain is banned. The newer half (`cmd/evener-dev`, `internal/devtool/*`) follows that rule. The older half has not been ported, and a few duplicate homes exist while the port is partial.
2. A large family of "audit" tests in the root package. Some enforce real invariants (delete-safety, workflow guards). Many pin prose, comments, Makefile text or doc headings that were wrong once, which is a change-detector per past incident. `testing.md:228` states "Test depth caps at one meta-level ... nothing tests the tests". Several files violate that (tests of the audit's own predicate, floors on how many things the scanner matched, allowlists that are empty).

The 2026-08-19 line in `scripts/coverage/coverage-floors.txt` records that ~5.5k lines of audit tests were already deleted once. The pattern has regrown.

## Findings

### build-gates-scripts-1 (high): Per-package timing ratchet has been disarmed since #172; the 664-line bash+Python script and 1,231 lines of tests guard mostly-warn-only behavior

Locations:
- `testing-budget.json` (top-level keys: `_`, `perTestCeilingSeconds`, `_perTestCeilingClasses`, `perTestCeilingClasses`, `packages`; no `metric` key)
- `scripts/gate/test-timing-budget.sh:484-485` (`had_metric = budget.get("metric") == WALL_METRIC`, `stale_units = not no_baseline and not had_metric`)
- `scripts/gate/test-timing-budget.sh:536-565` (`STALE UNITS` branch, `WARN ... not enforced until the #141 rebaseline`)
- `scripts/gate/test-timing-budget.sh:318-660` (embedded `python3 - ... <<'PY'`)
- `timingbudget_test.go` (1,024 lines, 49 funcs), `timingbudget_classes_test.go` (207 lines)
- `.github/workflows/ci.yml:52-56`, `make/testing.mk:238` (the callers)

Evidence:
- The committed budget has no `"metric"` key, so every run takes the `stale_units` path. In that path any package over 1.5x its budget is downgraded: `WARN {pkg}: ... over stale budget ... not enforced until the #141 rebaseline`. The 110 per-package numbers therefore never fail CI. The script's own header and the budget's `_` field both say "predate issue #172 ... a rebaseline regenerates them (#141)".
- Only the 7 `perTestCeilingClasses` (35 test names) can fail, and the 2026-10-14 ruling (#4615) already removed four of the original classes because wall time "measures the runner, not the code".
- The comparator is 343 lines of Python inside a bash heredoc (`<<'PY'` at line 318, `PY` at line 660). `evener-dev timing-parse` (`cmd/evener-dev/timingparse.go`) is already Go. The other half of the same pipeline is still Python-in-bash, testable only by forking bash with fixture files.
- The test suite is mostly input-validation of a JSON file: roughly 15 tests are variants of "fails on a non-object / non-numeric / non-finite X" plus a "...InEveryMode" twin (`timingbudget_test.go:562-1024`).

Impact: Large maintenance drag guarding a ratchet that is not actually ratcheting. Every new JSON-shape hole triggers another "fails in every mode" test plus Python branch. The warn-only state hides that the gate's headline (`fail at 1.5x`) is off.

Fix plan:
1. Decide with Jesse: re-bless on the CI host class (`make test-rebaseline` on a CI run, which records `"metric"`), or drop the per-package ratio and keep only the class ceilings. Rebaselining must be a CI measurement, not a local one (see tests-in-CI memory).
2. Either way, delete the `stale_units` branch, the `STALE UNITS` message, `TestTimingBudgetWarnsOnBudgetsInThePreWallTimeUnits`, `TestTimingBudgetFailsOnWallTimeBudgets` (units tests) and the `no_baseline` special cases that exist only to ride out the rollout.
3. Port the comparator to `evener-dev dev timing-compare` beside `timing-parse` with table-driven Go tests over the budget struct (a typed `json.Unmarshal` into a struct rejects non-object / non-numeric shapes for free, deleting the whole "malformed ... in every mode" family). Delete the Python heredoc and its Go-driven bash tests.
4. Update `docs/developing-evener/testing.md` timing section and `make/testing.mk` annotations.

Size: about -1,700 lines (script ~-500, tests ~-1,000, doc ~-200), +300 Go.
Risk: medium. Needs Jesse's decision on rebaseline vs drop. Class-ceiling behavior must be preserved by tests before deleting the Python.

### build-gates-scripts-2 (high): The shell gate stack contradicts the repo's own "belongs in Go" rule and carries ~2,500 lines of test scaffolding

Locations:
- `scripts/gate/run-module-tests.sh` (862 lines, 15 `${VAR:-}` knobs)
- `scripts/lib/gate-bounded.sh` (228), `scripts/lib/load-aware-workers.sh` (416), `scripts/lib/gate-budgets.sh` (134), `scripts/lib/gate-root-shards.sh` (67), `scripts/lib/gate-scratch-root.sh` (68), `scripts/lib/gate-surface-lib.sh` (37), `scripts/lib/timing-tsv-lib.sh` (65)
- Tests: `gatemodulerunner_test.go` (782), `loadawareworkers_test.go` (537), `gatebounded_test.go` (385), `gatebudgets_test.go` (383), `gatescratchroot_test.go` (179), `gaterootshards_test.go` (98), `gatesurface_test.go` (56), `testscope_test.go` (59)
- `docs/developing-evener/testing.md:236-247`
- Already-Go counterparts: `cmd/evener-dev/agentshards.go`, `shardplan.go`, `modulelint.go`, `webgate.go`, `internal/devtool/{procgroup,scratch,shardrun}`

Evidence:
- `testing.md:244-247`: "Tooling that accumulates real logic belongs in Go under go test ... shell stays for glue." The rule names "port-on-touch" as the trigger.
- `load-aware-workers.sh` is 416 lines of POSIX-sh cgroup v1/v2/hybrid mountinfo parsing (functions `load_aware_cgroup_mount_fields`, `..._hierarchy_cores`, `..._walk_cores`, `..._unescape`). `loadawareworkers_test.go` is 537 lines of cgroup fixtures. go.mod says `go 1.27.0`; the Go runtime (since 1.25, to be confirmed against the 1.27 release notes) derives default `GOMAXPROCS` from cgroup CPU limits, which covers the quota half.
- `gate-bounded.sh` re-implements process-tree stop with TERM then KILL escalation that `internal/devtool/procgroup` already provides ("the Go home of the stop_children/process-group discipline the shell runners each hand-rolled", `procgroup.go:3-7`). `gatebounded_test.go:244-385` tests it a second time in shell.
- Knob sprawl across `Makefile`/CI: `TEST_SCOPE`, `RACE_SCOPE`, `RACE_ROOT_PART`, `ROOT_FULL`, `ROOT_REST`, `HUB_SHARDS`, `CLI_SHARDS`, `HUB_SHARD_COUNT`, `AGENT_SHARDS`, `AGENT_PARALLEL`, `WAVE1`, `WAVE2`, `WEB`, `MODULES` (`make/testing.mk:207,266-270`; `run-module-tests.sh`). `make/testing.mk:266` encodes a `case` mapping `RACE_ROOT_PART` to three other env vars, then `gate-root-shards.sh` decodes them again.
- The usage header at `run-module-tests.sh:22-25` documents a path that no longer exists (`scripts/run-module-tests.sh`; see finding 15).

Impact: The largest single maintenance cost in the slice. Each gate tweak needs shell edits, a fixture-driven Go test that forks bash, and awareness of two parallel implementations (shell and `evener-dev`).

Fix plan (port-on-touch, per the existing rule, in this order):
1. `evener-dev dev module-tests` reusing `procgroup`, `scratch`, `shardrun`, the existing `shardplan`/`agentshards`, and `timingparse` in-process (removes `EVENER_DEV_BIN` bootstrap and `go_test_json_to_tsv` shelling).
2. Replace `load-aware-workers.sh` with a small Go function: effective cores = `runtime.GOMAXPROCS(0)` minus rounded-up load1, floor 1. Delete the cgroup parsers. Keep one shell caller only if `package.json:12` and `test-web-browser.sh` still need it, by calling `evener-dev dev workers --cap N`.
3. Move the env-knob decoding into one Go struct (`RACE_ROOT_PART` becomes a single flag), deleting the `case` in `make/testing.mk:266`.
4. Delete the shell libs and their Go tests as each piece lands; table-test the Go replacements directly (no bash forking).

Size: about -4,300 lines of shell+tests, +1,200 Go.
Risk: medium-high. It is the gate everything depends on. Land per piece behind CI (do not validate with local full runs). Behavior-preserving, so the existing runner tests become the port's characterization suite before deletion.

### build-gates-scripts-3 (medium): Prose/comment-pinning "audit" tests lock one-off doc corrections into the permanent suite

Locations:
- `doctorcomments_audit_test.go:1-132` (pins a `projRef` doc comment names its callers and that a symlink-policy comment cites `#2205`/`#2275`)
- `transcriptcomments_audit_test.go:1-127` (pins `decodeStrictJSON`'s comment cites two named tests; walks the whole tree for `func Test...`)
- `sandboxbreakerdocs_audit_test.go:1-170` (pins `agent/sandbox/policy.go` package doc and `docs/architecture.md` sections via `regexp` for "enforces nothing", "both keyed on tool name", and requires the word "normalized")
- `lazytranscriptloading_audit_test.go:1-37` (a scenario card must contain "near-top" and "too short to scroll" and not "IntersectionObserver")
- `appwirelegacypath_audit_test.go:1-102` (reads EVERY git-tracked file looking for the never-existing evener-hub assets path for appwire.js; requires `cmd/serf-hub/assets/appwire.js` to appear somewhere)

Evidence:
- Each header names the incident it came from (`issue #2624`, `#2782`, `#2403`, `#3057`, `#2819`). The oracle in each is a hardcoded phrase from the correction, the exact pattern `testing.md:60-110` ("Copy Is Not a Test Oracle") bans for tests of UI strings. Applying it to comments and docs is the same defect.
- `appwirelegacypath_audit_test.go:80-101` reads every tracked file in the repo on each run and fails if a deleted-file path is spelled the wrong way; the "real path" needle must appear at least once, so deleting the references fails the test.
- `sandboxbreakerdocs_audit_test.go:26` asserts `(?i)enforces nothing|gated off until M5|no file tool or spawned command` never reappears; this is a ratchet against a sentence that cannot plausibly return.
- Helper duplication inside the set: `declaredTestNames`/`ignoredAuditDir` (transcriptcomments), `safeAuditPackageDoc`, `safeAuditMarkdownSection`, `safeAuditHeadingLevel` (sandbox), `declDocByName` (doctor) are each used by a single test.

Impact: ~570 lines that cannot catch a behavior regression, fail on a legitimate rewording, and stand as precedent for the next incident-shaped audit.

Fix plan: delete all five files. Where an incident left a real invariant (e.g. `decodeStrictJSON` reader split), it is already covered by the named behavior tests (`TestPastThreadReadQuarantinesOneUnknownTurnField`, `TestTranscriptReadersAndRawOutputRejectUnknownFields`), which stay. No doc update is needed beyond removing any mention in `testing.md`.

Size: -568 lines.
Risk: low. Confirm the cited behavior tests exist first (the audit itself checks this) and keep those.

### build-gates-scripts-4 (medium): `scenariosourcecite_audit_test.go` is 967 lines, includes its own regex TypeScript parser, and tests itself

Locations:
- `scenariosourcecite_audit_test.go:68,130,248,284,310,359,418,453,877,943` (10 tests)
- `scenariosourcecite_audit_test.go:611-676` (`scenarioTSDeclarationPatterns`, `scenarioStripTSBlockComments`, `scenarioTSDeclarationsIn`)
- `scenariosourcecite_audit_test.go:858` (`scenarioMinBareLineAnchors = 80`)
- `disposablehub_audit_test.go:516-540` (a second scenario-card scan with its own exemption table)

Evidence:
- Only 4 of the 10 tests check the corpus (`...CitationsResolve`, `...SymbolsAreDeclared`, `...BareLineAnchorsResolve`, `...DeviceFlowCitationCitesPackageDefault`). The other six test the scanner: `...NeedleReadsEveryCompiledExtension`, `...DeclarationsPreserveReceiverQualification`, `...TSDeclarationsRecognizeExportForms`, `...TSDeclarationsIgnoreBlockComments`, `...TrackedFilesByBaseIgnoresUntrackedFiles`, `...BareLineAnchorResolutionPinsTheRule`. This is the "tests testing tests" `testing.md:228` caps.
- The floor constant has been hand-lowered from 90 to 80 when a TUI was removed (comment at `:850-858`), so deleting cards means editing a number in a test.
- The file's own header concedes the weakest part: "What this does NOT check is the anchor behind the path ... `DocPane.tsx:133` and `DocPane.tsx:1` are the same claim here." The line-number check only tests that the cited line is within the file's length.
- 21 commits touch the file (`git log --oneline` count), which is the churn of a regex-parsing project, not a guard.

Impact: The cards are agent-run scenario instructions. A stale path in a card is an annoyance, not a product failure, and this audit has absorbed a regex TS declaration parser and Go AST qualification logic.

Fix plan: reduce to the smallest check that matters: for each backticked `path` (no `#`/`:` anchor logic) in `test/scenarios/*.md`, assert a tracked file with that suffix exists (about 60 lines, one test). Delete the bare-line-anchor logic, the TS regex parser, the symbol-declaration check and all six scanner tests (~800 lines). If symbol drift becomes a problem later, add it back with a real parser, not regex.

Size: -800 lines.
Risk: low-medium. It lowers what is checked; Jesse should confirm the symbol and line-anchor checks are not wanted. The cards have already moved off `:NNN` toward `#symbol` anchors, so the bare-anchor audit guards a shrinking population.

### build-gates-scripts-5 (medium): Makefile-structure audits are 1,373 lines of text scans over 6 small files, and duplicate the parser in `internal/maketargetsdoc`

Locations:
- `makefiletargets_audit_test.go:1-1219` (24 tests)
- `makefile_audit_test.go:1-154`
- `makefiletargets_audit_test.go:570-580` (regexes that "mirror internal/maketargetsdoc's fieldAttempt (parse.go) ... the two must agree")
- `makefiletargets_audit_test.go:773-787` (`lintGateCommands`, a Go map restating one fragment of every lint recipe)
- `makefiletargets_audit_test.go:446-497` (three tests that test `rulesWithoutRecipesInLines`)
- `makefiletargets_audit_test.go:1141-1219` (`TestLintBiomeRunsTheFrontendLintScript`, `TestLintGolangciLintsDarwinOffDarwin`: `strings.Contains(recipe, "GOOS=darwin")`, `"!= darwin"`, `"npm run lint"`)

Evidence:
- Each audit exists because of one real defect (PR #273: a lint rule silently left out of `LINT_TARGETS`). `TestMakeLintRunsEveryGateInLintTargets` (`:889`) already answers that definitively by asking `make -n -k lint`. The other textual proxies (`TestEveryPhonyTargetHasARule`, `TestEveryRuleIsPhony`, `TestEveryRuleHasARecipe`, `TestExactlyOneLintTargetsDefinition`, `TestRootMakefileHasNoRules`, `TestEveryLintingRuleIsInLintTargets`) re-derive rule structure by regex. The test file itself admits in its comments that these are "a textual proxy".
- Substring assertions on recipe text (`strings.Contains(recipe, "host_goos")`) fail when the recipe is refactored without changing behavior, and pass when the recipe is broken in a way that keeps the substrings.
- Two independent make parsers exist (this test file's `ruleLineName`/`makefileRecipes` and `internal/maketargetsdoc/parse.go`), with a comment asking humans to keep them in sync.

Impact: Every Makefile refactor touches two parsers and a mirror map. The 12 KB of rationale in test comments exceeds the value of what is checked.

Fix plan: keep `TestMakeLintRunsEveryGateInLintTargets` and `lint-generated`'s staleness diff. Replace the rest with one test using `make -pn` (the printed database lists `.PHONY` membership and recipes for every target) asserting: every rule is phony and has a recipe. Delete the Darwin/Biome substring tests (the CI job and `make lint-golangci` exercise those paths for real). Delete `lintGateCommands` by asserting `make -n lint` contains the same number of gate invocations as `LINT_TARGETS` has entries.

Size: -1,000 lines, +80.
Risk: low-medium. Fewer textual guards, but each deleted one is covered by a behavioral check (dry-run, CI).

### build-gates-scripts-6 (medium): `internal/maketargetsdoc` is 3,642 lines to render `##` comments into markdown tables, 75% of it tests, including a 1,218-line coverage-gaps file

Locations:
- `internal/maketargetsdoc/main_gaps_test.go:1-1218` (83 tests; names such as `TestRewriteRegionNoMatchingEndUnreachable`, `TestLoadFamiliesReadFileError`)
- `internal/maketargetsdoc/{main,parse,render,help}.go` (900 lines)
- `make/*.mk`: 558 `##` annotation lines; `make/repo.mk:77-87` (`generate`)
- `make/linting.mk` `lint-generated` (diff gate for the tables)
- `docs/developing-evener/{testing,building,linting,coverage,fuzzing}.md` generated regions

Evidence:
- The same text exists three times: the `#` rationale comment above the recipe, the `##` annotation block (proves/trigger/requires/fails-when), and the rendered table in `docs/developing-evener/*.md`. See `make/linting.mk` `lint-eval`: a 10-line `#` paragraph ("This tag never had a floor at all and duly rotted ...") followed by a 9-line `##` block saying the same thing.
- `main_gaps_test.go` is named for its purpose: error-path coverage ("covers the os.ReadFile error path in loadFamilies").
- The tool needs a `parse_fuzz_test.go` and an orphan-region checker (`checkOrphanRegions`) for what is a single-purpose docs script.

Impact: Documentation of 73 make targets costs ~3.6k lines plus a staleness gate that fails the required lint job whenever a comment is edited without regenerating.

Fix plan: keep `make help` (`help.go`, 98 lines). Drop the markdown table generator (`render.go`, region rewriting, orphan check, `lint-generated`'s doc diff for these tables) and let docs link to `make help`, or hand-maintain a short list. Fold the `#`/`##` double comments into one block per target. Needs Jesse's call because it changes a recorded Makefile/docs decomposition decision.

Size: -2,300 lines (Go) and about -300 comment lines in make/*.mk.
Risk: medium (docs product decision; `AGENTS.md` requires evergreen docs in the same change).

### build-gates-scripts-7 (medium): Gate-surface constants live in a shell lib AND an unused Go package, with a drift test between them

Locations:
- `scripts/lib/gate-surface-lib.sh:19,24` (`GATE_FUZZ_TEST_SKIP`, `GATE_TEST_RUN`)
- `internal/devtool/gatesurface/gatesurface.go:1-26`, `gatesurface_test.go` (38), `drift_test.go` (101)
- `gatesurface_test.go:15` (root, 56 lines; tests only `gate_shard_skip`)
- `cmd/evener-dev/agentshards.go:214` (third literal: `"^(Test|Example)"`)

Evidence:
- `grep gatesurface` across all `.go` outside the package finds zero importers. The package header says "the single definition of which tests the gate runs" and the drift test says "two homes that drift are worse than no ratchet". There are three homes, and the one with consumers is not the Go one.
- `drift_test.go:11` cites `docs/superpowers/specs/2026-08-17-dev-tooling-in-go-design.md`, which is not tracked at HEAD (finding 15).
- `FuzzTestSkip` regex is a 300-char hand-maintained list of test names to skip; it is a classification that lives in a gate script rather than next to the tests.

Impact: A ratchet protecting a duplicate nobody uses. When the runner is ported (finding 2), the Go constant would be the natural home, but today it is dead code with a test.

Fix plan: delete `internal/devtool/gatesurface` entirely now (165 lines incl. tests). When finding 2 ports the runner, create the package then, with its first real consumer, and delete the shell lib. In the meantime make `agentshards.go:214` read the shell constant's value from env as the other shard flags do, or accept the literal as is.

Size: -165 lines now, -60 more with the port.
Risk: low.

### build-gates-scripts-8 (medium): Shell-hygiene audits are hand-rolled lexers with allowlists, two of which are empty

Locations:
- `scriptmktemp_audit_test.go:1-570` (6 tests)
- `recursivedelete_audit_test.go:1-337`
- `makefile_audit_test.go:27` (`var makefileVariableFedDeletes = map[string]string{}`)
- `scriptmktemp_audit_test.go:538` (`var scratchOrderAllowedPaths = map[string]bool{}`)
- `make/testing.mk:233` (`@tsv="$$(mktemp)"; trap 'rm -f "$$tsv"' ...`)
- `.tool-versions` (no shellcheck)

Evidence:
- 1,061 lines implement a textual shell scanner for three rules: no `mktemp -t`/bare `mktemp`, no variable-fed `rm -r`, arm the EXIT trap before minting scratch. The scanner's own predicate has a test (`TestRecursiveDeletePredicateCatchesBothLineages`, `TestScratchOrderAuditCatchesMintBeforeTrap`, ...) and the allowlists are keyed by exact path with exact counts.
- Two allowlists are empty (`makefileVariableFedDeletes`, `scratchOrderAllowedPaths`), so the Makefile delete audit and the trap-ordering audit protect nothing today but still ship parsers, three mutation tests and a "this list is the finding" essay.
- The rules are not applied to the `Makefile` for mktemp: `make/testing.mk:233` uses bare `mktemp` (on macOS the Darwin per-user dir, exactly the leak `mktempEscapesTMPDIR` was written to prevent, see `scriptmktemp_audit_test.go:12-28`). The audit only walks `scripts/` and `install.sh` (`scriptShellFiles`).
- No `shellcheck` anywhere in CI; the audits are a partial homemade substitute.

Impact: ~1,000 lines of fragile lexing; the one real violation in the Makefile is outside its scope.

Fix plan:
1. Fix `make/testing.mk:233` to `mktemp "$${TMPDIR:-/tmp}/evener-gate-timing.XXXXXX"`.
2. Delete the Makefile delete audit (empty allowlist, `makefile_audit_test.go` 154 lines) and the trap-ordering audit with its 3 mutation tests (`scriptmktemp_audit_test.go:380-570`).
3. Keep the `mktemp` and `rm -rf $var` scans in one file with one shared predicate; drop the tests of the predicate except one table test.
4. Raise with Jesse whether to add `shellcheck` to `make tools` instead (new dependency, so it needs approval).

Size: -450 lines now.
Risk: low.

### build-gates-scripts-9 (medium): Broken and orphaned live-eval shell harness duplicates Go eval suites

Locations:
- `test/live-compaction-dense-value-test.sh:12`, `test/live-compaction-retention-eval.sh:13` (both `. "$SCRIPT_DIR/../scripts/live-eval-isolation.sh"`)
- `scripts/lib/live-eval-isolation.sh` (70 lines; the real location)
- `scriptmktemp_audit_test.go:216` (allowlist entry for the lib)
- `agent/internal/contextmgr/compaction_{erosion,comparison,elicit,multineedle}_eval_test.go`, `agent/forced_note_live_test.go` (Go eval-tagged equivalents)
- `docs/design/2026-06-14-compaction-live-results.md:4,71` (only reference)

Evidence:
- `ls scripts/live-eval-isolation.sh` fails. The path was moved to `scripts/lib/` and the two callers were not updated, so both scripts die at line 12/13 after passing the `EVENER_LIVE_TESTS=1` check. Last touched 2026-08-18, and the lib has no other consumer (`grep live_eval_begin` finds only these two).
- `make lint-eval` compiles the Go eval suites for "context-compaction quality, forced notes" (`make/linting.mk` `lint-eval`), the same subject.

Impact: Dead on arrival scripts and a lib kept alive only by an audit allowlist entry.

Fix plan: delete `test/live-compaction-*.sh` (216 lines), `scripts/lib/live-eval-isolation.sh` (70) and its allowlist line; the recorded results doc stays as a historical design note (update its "Harness:" lines to say the harness was retired in favor of the contextmgr eval tests).

Size: -290 lines.
Risk: low.

### build-gates-scripts-10 (medium): `deploy-hub-endpoint-test.sh` is a 573-line unwired self-test with fake `launchctl`, contrary to "no selftest scripts"

Locations:
- `scripts/ops/deploy-hub-endpoint-test.sh:1-573` (bash header plus embedded Python at line 11)
- `docs/evener-hub-remote-operations.md:259` (only reference)
- `scripts/ops/deploy-hub.sh` (444 lines, the subject; last commit 2026-10-22)

Evidence:
- It fakes the toolchain (its own header: "No real launchctl, build, SSH or hub is invoked ... a closed PATH"), which `testing.md:239-247` bans, and it is not run by `make test`, any workflow, or any Go test (`grep deploy-hub-endpoint-test` finds only the doc).
- Added 2026-10-22 (one commit), after the `scripts/*-selftest.sh pattern dies` ruling recorded in memory.
- Python inside bash again (see finding 1): the third such script in `scripts/gate`, `scripts/ops`.

Impact: Test code for a deploy script that no gate runs, so it silently rots with `deploy-hub.sh`.

Fix plan: either (a) port the cases that matter to a Go test that runs `deploy-hub.sh` against a disposable repo and a fake `launchctl` placed on PATH only if Jesse approves (it is the same toolchain-faking the rule bans), or (b) port `deploy-hub.sh` itself to `evener-dev` (preferred by the doc: "has outgrown shell") and test the Go. Until then, wire nothing new. Ask Jesse which; (b) is bigger.

Size: -573 shell, +300 Go test if (a).
Risk: medium. Touches a macOS deploy path.

### build-gates-scripts-11 (medium): Four copies of the npm-install "ready or refuse" preflight

Locations:
- `scripts/web/web-preflight.sh` (71)
- `scripts/sdk/api-package-preflight.sh` (136)
- `scripts/native/native-preflight.sh` (117)
- `make/repo.mk:62-76` (`docs-check` recipe; symlink check and `-nt` freshness inline)
- Tests: `buildscripts_test.go:21-400` (web and api-package), `install_test.go:60-316` (native preflight, 256 lines in an `install.sh` test file)
- `AGENTS.md` ("Frontend gates") describes all three separately

Evidence:
- All four implement: refuse `npm ci` through a symlinked `node_modules`, compare the shared install's `package-lock.json`, skip install when `node_modules -nt package-lock.json`, then prove the toolchain runs. The comment in `web-preflight.sh:13-21` and `api-package-preflight.sh:15-25` explain the same incident in near-identical words.
- `TestNativePreflightRefusesUnreadyInstalls` lives in `install_test.go` (a 1,611-line file about `install.sh`), unrelated to it.

Impact: A safety property ("never `npm ci` through a symlink and delete the shared install") re-implemented four times, each with its own tests; fixing one leaves three.

Fix plan: one `scripts/lib/npm-install-ready.sh DIR HEALTH_CMD` (or a Go `evener-dev dev npm-ready --dir X --probe "tsc --version"`) implementing the symlink and freshness logic once; each preflight becomes the directory and the probe. Test the library once; delete the three copies of the symlink tests, keep one per probe. Move the native test to `buildscripts_test.go`. Update `AGENTS.md`/`building.md` only if script names change (they should not).

Size: -300 lines.
Risk: low-medium (symlinked shared installs are a real hazard; characterization tests first).

### build-gates-scripts-12 (low-medium): Root test package has seven near-identical file/command helpers and four git/markdown walkers

Locations:
- Write-file helpers: `loadawareworkers_test.go:153` (`writeFixtureFile`), `scriptmktemp_audit_test.go:380` (`writeAuditScriptFixture`), `gatemodulerunner_test.go:488` (`writeRunnerFixtureFile`), `repofixture_test.go:30` (`writeTestFile`), `install_test.go:1541` (`writeExecutable`), `scripts_fuzz_shared_test.go:62` (`writeExecutableFixture`)
- Command runners: `hooks_test.go:47,56` (`runIn`, `runInErr`), `repofixture_test.go:12` (`runGit`), `install_test.go:1441` (`runCommand`)
- Tree walkers: `git ls-files -z` in `appwirelegacypath_audit_test.go:87`, `scenariosourcecite_audit_test.go:679`, `install_test.go:1373`; `filepath.WalkDir` in `transcriptcomments_audit_test.go:88`, `scriptmktemp_audit_test.go:75`, `getting_started_test.go:81`
- Fenced-markdown parsers: `disposablehub_audit_test.go:24,78,452-470` (three variants), `getting_started_test.go:22` (regex)

Evidence: `writeFixtureFile` and `writeAuditScriptFixture` are byte-for-byte the same function. `writeTestFile` (`repofixture_test.go:30-48`) is the one that holds `syscall.ForkLock` to avoid ETXTBSY (golang/go#22315) and explains why; `writeExecutable` repeats that logic independently, and the other four do not take the lock, so they can fail with ETXTBSY under parallel forks.

Impact: Divergent fixes (only two of six writers protect against ETXTBSY), plus copies a reader must compare.

Fix plan: keep `repofixture_test.go` as the one home: `writeTestFile` (already locked), a `writeExecutableFile` wrapper, `runCmd`, `trackedFiles(t)`, `fencedBlocks(t, path)`. Replace callers mechanically; delete the six writers and the extra walkers. Many callers disappear anyway with findings 3, 4, 5, 8.

Size: -150 lines.
Risk: low.

### build-gates-scripts-13 (low-medium): `tools-golangci` and `tools-gitleaks` duplicate a retry loop and are tested by faking `curl`, `go`, and `sleep` on PATH

Locations:
- `make/repo.mk:12-33`
- `tools_golangci_retry_test.go:1-47`, `tools_gitleaks_retry_test.go:1-48`

Evidence: The two recipes differ only in the install command; the tests are line-for-line parallel and write a fake `curl`/`go`/`sleep` into a temp PATH. `testing.md:239-247`: "Faking the toolchain to test a script is banned outright." The tests assert the retry count (`after 4 attempts`, sleeps `3 10 30`), i.e. the implementation, not an outcome.

Impact: 95 lines of banned-style tests for a retry loop whose failure mode is "CI install flakes once".

Fix plan: delete both tests. Fold the two recipes into one `scripts/ops/install-tool.sh golangci-lint|gitleaks` that retries once, or accept the duplicate recipes. CI exercises the real install on every run.

Size: -95 test lines, -12 recipe lines.
Risk: low.

### build-gates-scripts-14 (low-medium): Incident narratives and dated rulings are repeated in three evergreen files

Locations:
- `testing-budget.json` `_` (1,246 chars) and `_perTestCeilingClasses` (2,515 chars)
- `scripts/gate/test-timing-budget.sh:19-100` (80-line header, rulings of 2026-10-13, 10-14, 10-16, issues #4601 #4615 #4699 #4608 #4629 #4609 #4614 #4618 #4622)
- `docs/developing-evener/testing.md:1300-1345` (13 dated entries in the file; "Four classes were removed on 2026-10-14 ...")
- `make/testing.mk` timing annotations; `internal/devtool/...` headers cite similar incident numbers

Evidence: The class-removal story is told in all three places with the same figures (`52.26s`, `15s`, `30.09-32.28s`, `7.6-9.6s`). `AGENTS.md` says to keep "dated narratives, rollout logs, and PR progress out of these references". `testing-budget.json` is also read by the script as data, so 3.7 KB of prose rides in the config.

Impact: Three places to update when a class changes; the config file is mostly story.

Fix plan: state the current rule once in `testing.md` (what a class is, what is warn-only, how to add a class). Reduce `testing-budget.json` to data with a one-line `_` pointing at the doc; cut the script header to usage plus the pointer. History goes to the commit/PR messages.

Size: -250 lines of prose.
Risk: low. Do alongside finding 1.

### build-gates-scripts-15 (low): Stale paths in usage text and a dangling spec citation

Locations:
- `scripts/gate/run-module-tests.sh:22-25` (`scripts/run-module-tests.sh`; real: `scripts/gate/`)
- `scripts/ops/setup-gocache.sh:22,33,49` (`scripts/setup-gocache.sh`; line 33 is an error message printed to users)
- `scripts/ops/gitleaks-scan.sh:6,7` (`scripts/gitleaks-scan.sh`)
- `scripts/fuzz/fuzz-mutation-score.sh:35` (`scripts/fuzz-mutation-score.sh`)
- `scripts/lib/e2e-lib.sh:3` (two `scripts/e2e-*.sh` paths), `scripts/coverage/fuzzcov-ignore.txt:11` (`scripts/fuzz-coverage-global.sh`), `scriptmktemp_audit_test.go:324` (`scripts/scratch-lib.sh`), `cmd/evener-dev/agentshards.go:8` (`scripts/agent-test-shards.sh`)
- `docs/superpowers/specs/2026-08-17-dev-tooling-in-go-design.md`: cited by `cmd/evener-dev/main.go:2`, `internal/devtool/gatesurface/drift_test.go:11`, `fuzz/README.md:144`, `testing.md:238`; `git ls-files` shows it is not tracked

Evidence: a script that resolves `scripts/**/*.{sh,txt}` strings in `scripts make Makefile .github docs/developing-evener *.go cmd/evener-dev` and checks each exists reports the 14 stale entries above. The spec cited as the source of the "port-on-touch" rule (also used in the fix plans above) is not in the tree.

Impact: Usage text that points nowhere; the policy citation is unreachable.

Fix plan: correct each path; either restore the spec (it exists in git history, commits `85f8639e7c`, `ab906d0d5c`) as an evergreen design doc, or move the port-on-touch rule text into `testing.md` and repoint the four citations at it. `docs-check` already validates markdown links; leave comment paths to review rather than adding another audit.

Size: ~30 lines edited.
Risk: low.

### build-gates-scripts-16 (low-medium): Fuzz campaign toolkit is manual-only, state files are empty, and two make targets run the same script

Locations:
- `scripts/fuzz/fuzz-triage.sh` (493), `fuzz-continuous.sh` (256), `fuzz-drive.sh` (254), `fuzz-bisect.sh` (153), `fuzz-oracle-audit.sh` (172), `fuzz-mutation-score.sh` (91), `run-fuzz.sh` (100), `rapid-replay.sh` (23): 1,542 lines
- `fuzz/state/ledger.json` and `buckets.json` (both `{}`; one commit, `88e6970a9c`)
- `fuzz/mutations/manifest.tsv` (4 mutations) versus 553 native rows in `scripts/fuzz/fuzz-targets.txt`
- `make/fuzzing.mk:191` (`mutation-floor`) and `:225` (`fuzz-mutation-score`), both `scripts/fuzz/fuzz-mutation-score.sh`
- `make/fuzzing.mk:233-235` (`fuzz-ledger`, jq over `{}`)
- `scripts_fuzz_bisect_test.go` (143), `scripts_fuzz_oracle_audit_test.go` (186), `scripts_fuzz_shared_test.go` (68)
- `fuzz/README.md:140-146` ("honest tests arrive when the tool ports to Go")

Evidence: CI runs only `make fuzz-gap-check`, `fuzz-registry-check`, `fuzz` (`.github/workflows/ci.yml:294-297,459`). The other targets (`fuzz-triage`, `-continuous`, `-drive`, `-bisect`, `-oracle-audit`, `-mutation-score`, `mutation-floor`) are documented "manual/on-demand". Most scripts were last modified 2026-08-18..09-01 (no touch since). The oracle audit's headline claim, "every fuzz oracle reddens on its bug class", is checked for 4 of 553 targets.

Impact: ~1,500 lines of unrun shell plus its tests and an empty ledger.

Fix plan: merge `mutation-floor` and `fuzz-mutation-score` into one target. Ask Jesse whether triage, continuous, drive and the ledger are still used; if not, delete them with `fuzz/state`, `fuzz-ledger` and the `scripts_fuzz_*` tests. If kept, port-on-touch per `fuzz/README.md`.

Size: -50 lines (merge) up to -2,200 (full prune).
Risk: low for the merge; the prune needs Jesse.

### build-gates-scripts-17 (low): CI does not use its own `setup-toolchain` action in the two jobs that match it exactly

Locations:
- `.github/actions/setup-toolchain/action.yml:12-30` (setup-go, go-cache-restore, setup-node 22)
- `.github/workflows/ci.yml:22-58` (`web-unit`), `:60-93` (`web-browser`), `:191-299` (`lint-golangci`, `lint-repository`, `fuzz-checks`)
- Pin drift: `ci.yml:27,65,134,155` use `actions/setup-node@v7.1.0`; `setup-toolchain/action.yml:27` uses `@v7`

Evidence: `web-unit` and `web-browser` hand-write checkout, setup-node (22, npm cache), setup-go (go.work, `cache: false`), `go-cache-restore`: the composite's three steps, in a different order. `lint-golangci`, `lint-repository`, `fuzz-checks` repeat the setup-go plus cache-restore pair without Node.

Impact: Go-version or cache-key changes need five edits; the node action pin has already drifted between files.

Fix plan: use `./.github/actions/setup-toolchain` with `go-cache-kind: web` in the two web jobs. For the Go-only jobs, consider a `node: false` input only if the extra Node install costs real time (YAGNI otherwise). Align the setup-node pin.

Size: -50 lines of YAML.
Risk: low (verify cache keys via CI).

### build-gates-scripts-18 (low): The Go module list is spelled out in five places

Locations: `Makefile:5` (`GO_MODULES`), `scripts/gate/run-module-tests.sh:65`, `scripts/gate/test-timing-budget.sh:137`, `scripts/coverage/coverage-floor.sh:34`, `cmd/evener-dev/modulelint.go:27` (with `fuzz`), authoritative source `go.work`.

Evidence: all five are literal strings `". agent llm auth envvars invariant identifier execsupport"` (three with `fuzz`). `testscope_test.go` partitions `GO_MODULES` but nothing checks the other four against `go.work`.

Impact: Adding a module needs five synchronized edits; a miss silently drops the module from one gate.

Fix plan: derive from `go.work` (`go work edit -json | jq -r '.Use[].DiskPath'`, or a small `evener-dev dev modules` that prints them), with `fuzz` filtered where the gate excludes it. Export once from the Makefile (`GO_MODULES`) and have scripts read `MODULES` from env, as `run-module-tests.sh` already does.

Size: ~-20 lines.
Risk: low.

### build-gates-scripts-19 (medium, cross-cutting): Coverage-chasing test files make up 38k lines of Go tests, driven by a coverage floor that is not even a required gate

Locations:
- 183 files named `*_gaps_test.go`, `*_covtest_test.go`, `*coverage_test.go`, `*_cov_test.go` totaling 38,114 lines (3.5% of Go test lines), e.g. `agent/doctor/audit_metrics_covtest_test.go:1-25`, `internal/maketargetsdoc/main_gaps_test.go`
- `scripts/coverage/coverage-floors.txt` (floors 92.3 to 100.0; header: "the number worth driving toward 100%")
- `make/coverage.mk`, `docs/developing-evener/coverage.md:55-57` ("Local/on-demand; not required CI")

Evidence: `audit_metrics_covtest_test.go:8-9`: "covers the 'unknown metric' error path inside the apilog health metric resolver (audit.go:423)" — a line-number cite that rots, with an assertion that only checks `err != nil`. The floors file itself lists unreachable branches it declines to contort around, so the 100% goal is stated and then conceded.

Impact: Test lines that exist to turn a coverage number green, assert little, and cost review and maintenance time. They are indistinguishable from real tests in `go test` output.

Fix plan: change the floors header to state the floor is a no-regression ratchet, not a target. Grandfather nothing: sample-review the `*_gaps`/`*_covtest` files per package (start with `agent`, `internal/maketargetsdoc`) and delete tests whose only oracle is "returns an error" on a trivially-reachable path; keep those that pin a recovery/preservation contract. Lower floors in the same change (floors are hand-editable per the file header). Per `AGENTS.md`, do not rely on coverage as proof of behavior.

Size: up to -30,000 lines over several PRs; first PR -3,000.
Risk: medium (coverage floors and any unrelated reliance on those tests; per-package review).

### build-gates-scripts-20 (low): `rewrite-package-imports.mjs` is a finished one-time migration tool (460 lines)

Locations: `scripts/sdk/rewrite-package-imports.mjs:1-460`; header lines 2-12.

Evidence: The header says "Run it once per migration step ... this script is the one-time rewrite, not a gate" for "SDK migration row A4". The migration landed (`e2baa5f72e`, #1272). `make lint-package-imports` is the standing check and finds no path spellings. Nothing references the script (the other `scripts/sdk/*.mjs` files are shared with mobile-native and `appwire-client` tests; this one is not).

Impact: 460 lines of AST-rewriting code to maintain for a task with zero remaining work.

Fix plan: delete the file; confirm `module-specifiers.mjs`, `resolve-source.mjs`, `source-files.mjs` keep their other consumers (`mobile-native/scripts/check-script-imports.mts`, `appwire-client/typescript/scripts/*`); remove any mention in `appwire-client/typescript/README.md` or docs. Git history keeps it.

Size: -460 lines.
Risk: low.

### build-gates-scripts-21 (low): Workflow- and config-pinning tests restate the YAML in Go

Locations:
- `ios_workflow_test.go:1-104` (constant `iosArchiveGuard` copies the full `if:` expression from `.github/workflows/ios-testflight.yml`)
- `release_workflow_test.go:44-146`
- `buildscripts_test.go:472-530` (`TestReleaseArchiveDoesNotCarryEvenerDev`, `TestReleaseWorkflowDoesNotExpectEvenerDev`, which assert absence of a retired binary)
- `mobile_native_gitignore_test.go:1-31`, `gitleaks_dist_allowlist_test.go:1-79`

Evidence: `TestIOSWorkflowJobGuards` compares `job.If != want.cond` against a 190-char string copied from the workflow; any intended edit needs the same edit twice. `TestReleaseWorkflowDoesNotExpectEvenerDev` greps `binaries.yml` for the substring `evener-dev\b` after stripping comments. `TestMobileNativeGitignoreIgnoresDerivedDataBuild` shells `git check-ignore` for one path from issue #3325. Some are defensible (TestFlight uploads are costly and irreversible, so the trigger shape is worth a test), but the job `if:` string comparison is a change detector.

Impact: Small: each is 30-100 lines, but they add one more place to edit with every workflow change.

Fix plan: keep the trigger-shape test (`TestIOSWorkflowTriggersOnlyOnVersionTags`) and the "tag is on main" step check. Delete the verbatim-`if:` comparison, the two evener-dev absence tests, and the gitignore test (the ignore rule is its own documentation). Keep `gitleaks` allowlist test (it has a negative control).

Size: -250 lines.
Risk: low.

## Not findings (checked, left alone)

- `install_test.go` fake `curl`/`uname`: memory records Jesse declining removal (2026-09-25).
- `copyonly_audit_test.go` + `internal/devtool/copyonly`: encodes the written "Copy Is Not a Test Oracle" rule with a shrinking allowlist; it is the meta-test that most clearly earns its place.
- `packageimportpaths_audit_test.go`: drives a real script over real fixture trees (negative-finding gate that "could not fail" otherwise).
- `fuzz/` helper modules (`oracle`, `schemagen`, `fault`, `edgeseeds` etc.) have many importers outside `fuzz/`.

## Recommended fix sequence

1. Cheap deletions, one PR (about -1,800 lines, low risk): findings 3 (doc-pin audits), 7 (dead gatesurface package), 9 (broken live-eval scripts), 13 (retry fakes), 20 (rewrite-package-imports), 21 (workflow YAML restatements), the empty-allowlist halves of 8, plus the `make/testing.mk:233` mktemp fix. Update `testing.md` only where it cites a deleted file.
2. Doc and path hygiene (finding 15, finding 14's prose cut, finding 17, finding 18). Small, mechanical, can ride with step 1 or follow it.
3. Decision round with Jesse (one question at a time): (a) timing ratchet: re-bless on CI or drop per-package ratio (finding 1); (b) docs table generator vs `make help` only (finding 6); (c) fuzz campaign scripts keep or prune, and the `deploy-hub-endpoint-test.sh` fate (findings 16, 10); (d) shellcheck instead of the home-grown shell lexers (finding 8).
4. Test-consolidation PR: findings 4, 5, 12 (shrink scenario-cite and Makefile audits, one helper home).
5. Timing ratchet port to Go once (a) is decided (finding 1), then delete the Python heredoc and its tests.
6. Port the shell gate to `evener-dev` piece by piece, each piece behavior-preserving and CI-verified (finding 2), then fold the preflight copies into one library (finding 11).
7. Coverage-chasing pruning, per package, with floors lowered in the same PRs (finding 19). Start where tests are plainly coverage-named and assert only `err != nil`.
