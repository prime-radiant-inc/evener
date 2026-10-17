# Legacy TUI Removal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the interactive terminal dashboard (`cmd/evener-tui/`, `evener tui`) and every reference to it, keeping the non-interactive CLI and all other clients unchanged.

**Architecture:** The TUI is a single Go package reachable only through the `tui` case in `cmd/evener/main.go`'s dispatcher. Deletion is atomic with the gates that name it (timing budget, fuzz registry, copyonly allowlist, fuzz mutation manifest, help-list test, install test), because any one left behind fails a gate. Non-compiling references (docs, comments, scenario cards) then land separately, and a repo-wide sweep plus the full gate sequence closes it out.

**Tech Stack:** Go (module `primeradiant.com/evener`, Go workspace), `make` gates, `rg` for sweeps.

**Spec:** `docs/superpowers/specs/2026-10-16-legacy-tui-removal-design.md`

## Global Constraints

- One `evener` binary only. Do not touch `.goreleaser.yml`, `EVENER_INSTALL_BINS`, or release/install wiring: no separate `evener-tui` binary exists to remove.
- No replacement client, and no compatibility shim. No build tag or `--enable-tui` flag.
- Do not renumber subsystem IDs. `S04` is removed and its references scrubbed; S05–S24 stay.
- `evener tui` gets **no** special handling. The word `tui` is an ordinary unknown first word after removal: the dispatcher falls through to `cliprompt.Read` (`cmd/evener/main.go:251`) and runs it as a prompt. Do not add a notice branch.
- Keep the repo's tests at least as strict as found. Do not delete a test to make a gate pass unless the subject of the test is the TUI itself.
- Never assert on human-readable phrasing in tests (repo rule). The changes below only remove/adjust references; do not add copy assertions.
- Run heavy gates one at a time, never as parallel background jobs.

## Review Focus

Inputs and conditions the spec implies but no task's test exercises; each gets a test or an explicit check in the owning task.

1. **A stale gate key that names the deleted package.** `testing-budget.json` and the copyonly allowlist fail loudly if a key dangles; the tests that enforce this are named in Tasks 1a/4/5. Verify they actually fail before the fix and pass after.
2. **An unknown-word invocation that now runs a model.** `evener tui --help` after removal is args `["tui","--help"]`, which the prompt path joins and sends to a model. Task 1 must remove the `install_test.go` and `e2e-cover.sh` invocations that would otherwise trigger a live run; no new invocation may be added.
3. **A generated artifact left stale.** `docs/appwire-protocol.md` is generated from `internal/appwiredoc/protocol.md.tmpl`; `lint-generated` fails if the doc is not regenerated. Task 6 runs `make generate` and the freshness check.
4. **A prompt golden that drifts.** `agent/testdata/messagingprompt/*.md` is pinned against `agent/prompts/system.md.tmpl`; editing one without the other fails the golden test. Task 2 changes both together and runs the golden test in both directions.
5. **An env var deleted out from under a live reader.** `EVENER_HUB_ADDR` has non-TUI readers (Vite dev server, browser guard); deleting it would break them silently. Task 3 keeps it (rewords the summary) and deletes only the three truly TUI-only vars, with the reader evidence recorded.

---

### Task 1: Remove the package and its gates (atomic)

**Files:**
- Delete: `cmd/evener-tui/` (whole directory)
- Modify: `cmd/evener/main.go` (import line 22; `cliCommandRunners` field ~line 529; runner closure ~lines 498-500; dispatch case line 556; help line 402)
- Modify: `cmd/evener/main_test.go:283` (drop `"tui"` from the expected list in `TestTopLevelHelpListsSubcommands`)
- Modify: `install_test.go:386` (remove the `evener tui --help` invocation)
- Modify: `testing-budget.json` (remove the 15 `.../cmd/evener-tui...` keys; remove any `TestTUITmuxE2E_*` names from the live-stack E2E class map)
- Modify: `scripts/fuzz/fuzz-targets.txt` (remove every `./cmd/evener-tui...` line)
- Delete: `fuzz/mutations/toolsummary-taskupdate-panic.patch`
- Modify: `fuzz/mutations/manifest.tsv` (remove the `toolsummary-taskupdate-panic` row)
- Modify: `internal/devtool/copyonly/allowlist.tsv` (remove the `cmd/evener-tui/internal/launchconfig/...` entry)
- Modify: `scripts/coverage/e2e-cover.sh` (remove the `--tui` flag, the `TmuxE2E` battery block, and the three `evener tui ...` smoke lines)

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces: a tree where `cmd/evener-tui` does not exist and no build/test/fuzz/coverage gate names it.

- [ ] **Step 1: Confirm the gates fail before the change**

Run, and record the output as the red baseline:
```bash
cd /Users/jesse/git/prime-radiant/evener
rg -c 'cmd/evener-tui' testing-budget.json scripts/fuzz/fuzz-targets.txt fuzz/mutations/manifest.tsv internal/devtool/copyonly/allowlist.tsv
```
Expected: nonzero counts in each file (the references the removal must eliminate).

- [ ] **Step 2: Delete the package and its build/fuzz/coverage artifacts**

```bash
cd /Users/jesse/git/prime-radiant/evener
git rm -r cmd/evener-tui
git rm fuzz/mutations/toolsummary-taskupdate-panic.patch
```

- [ ] **Step 3: Unwire the CLI**

In `cmd/evener/main.go`: remove the `tuicmd "primeradiant.com/evener/cmd/evener-tui"` import, the `tui func(...)` field from `cliCommandRunners`, the `case "tui":` arm, the `tui:` runner closure, and the help line `_, _ = fmt.Fprintf(tw, "  tui\tRun the evener-tui terminal UI\n")`. Keep every other case and runner exactly as-is.

In `cmd/evener/main_test.go` remove `"tui"` from the subcommand slice at line 283. In `install_test.go` remove the `runCommand(t, fixtureRoot, env, evenerBin, "tui", "--help")` line.

- [ ] **Step 4: Remove the stale keys and targets**

From `testing-budget.json` remove the 15 keys beginning `primeradiant.com/evener/cmd/evener-tui` (root plus 14 internals) and any `TestTUITmuxE2E_*` entry under the live-stack E2E class. From `scripts/fuzz/fuzz-targets.txt` remove every line containing `./cmd/evener-tui`. From `fuzz/mutations/manifest.tsv` remove the `toolsummary-taskupdate-panic` row. From `internal/devtool/copyonly/allowlist.tsv` remove the line naming `cmd/evener-tui/internal/launchconfig/`. From `scripts/coverage/e2e-cover.sh` remove the `--tui` case (line ~40), the `run_tui` variable and its block (~lines 144-150), and the three lines `run "$evener" tui --help`, `-h`, `--bogus` (~lines 77-80). Keep the unrelated `# evener tui: help + error path.` comment out too.

- [ ] **Step 5: Build and run the affected gates**

Run:
```bash
cd /Users/jesse/git/prime-radiant/evener
go build ./... && make build-go
go test ./cmd/evener/ -run 'TestTopLevelHelpListsSubcommands'
make lint-fuzz-registry
go test . -run 'TestTimingBudgetClassMapNamesMatchTheTree|TestNoNewCopyOnlyTests|TestInstall' -count=1
```
Expected: each exits 0 with no failures. `go build ./...` succeeds with no unresolved import.

- [ ] **Step 6: Confirm the sweep is now clean in code and gates**

```bash
cd /Users/jesse/git/prime-radiant/evener
rg -l 'evener-tui|cmd/evener-tui' -g '!**/.worktrees/**' -g '!**/.claude/**' -g '!docs/superpowers/**' .
```
Expected: only files scheduled for Tasks 2, 3, 5, 6, 7, 8 (prompt template/goldens, docs, scripts comment, generated doc). Nothing in Go code, fuzz registry, budget, or allowlist.

- [ ] **Step 7: Commit**

```bash
git add -A cmd/evener-tui cmd/evener/main.go cmd/evener/main_test.go install_test.go testing-budget.json scripts/fuzz/fuzz-targets.txt fuzz/mutations internal/devtool/copyonly/allowlist.tsv scripts/coverage/e2e-cover.sh
git commit -m "refactor: remove the legacy evener TUI package and its gates"
```

---

### Task 2: Embedded prompt template and its goldens

**Files:**
- Modify: `agent/prompts/system.md.tmpl:137`
- Modify: `agent/testdata/messagingprompt/root.md:16`, `root-renamed-result-tool.md:16`, `delegate.md:16`, `delegate-result-schema.md:16`

**Interfaces:**
- Consumes: nothing.
- Produces: prompt prose that no longer names `evener-tui`.

- [ ] **Step 1: Find the golden test that pins the prompt**

```bash
cd /Users/jesse/git/prime-radiant/evener
rg -n 'messagingprompt' --glob '*_test.go' agent/ | head
```
Expected: the test file that compares rendered prompts to the goldens; note its `go test` package and run pattern.

- [ ] **Step 2: Edit the template and the four goldens identically**

In `system.md.tmpl:137` and each of the four goldens, replace the clause `When the task is about evener or evener-tui, run them like any other program under test.` with `When the task is about evener, run it like any other program under test.` Keep the surrounding sentence and the `{{ .ResultToolName }}` / `communicate` / `respond` substitutions unchanged.

- [ ] **Step 3: Run the golden test**

Run the test found in Step 1 (for example `go test ./agent/ -run MessagingPrompt -count=1`).
Expected: PASS, with the goldens matching the newly rendered prompt.

- [ ] **Step 4: Commit**

```bash
git add agent/prompts/system.md.tmpl agent/testdata/messagingprompt
git commit -m "docs(prompt): stop naming evener-tui in the session prompt"
```

---

### Task 3: Environment variables

**Files:**
- Modify: `envvars/envvars.go`
- Modify: `docs/developing-evener/environment.md`
- Modify: `scripts/lib/e2e-lib.sh:163-172`
- Modify: `disposablehub_audit_test.go:131` (only if it lists `EVENER_HUB_AUTH_TOKEN`)
- Test: `envvars/envvars_test.go`, `envvars/cov_ev_registry_test.go` (existing; no edits)

**Interfaces:**
- Consumes: nothing.
- Produces: three TUI-only vars removed from `allVars`; `EVENER_HUB_ADDR` retained with a corrected summary.

- [ ] **Step 1: Record the reader evidence, then decide each var**

```bash
cd /Users/jesse/git/prime-radiant/evener
rg -n 'EVENER_HUB_ADDR|EVENER_HUB_AUTH_TOKEN|EVENER_TUI_LOG_FILE|EVENER_HUB_BIN' \
  -g '*.go' -g '*.ts' -g '*.mjs' -g '*.sh' -g '!**/.worktrees/**' -g '!**/.claude/**' -g '!**/node_modules/**' .
```
Expected classification (already verified during planning — re-check, do not guess):
- `EVENER_HUB_ADDR`: **keep**, reword. Live non-TUI readers: `cmd/evener-hub/frontend/vite.config.ts:12` and `cmd/evener-hub/frontend/scripts/backgroundtranscriptguard/run.mjs:875`.
- `EVENER_HUB_AUTH_TOKEN`: **delete**. Only readers are the TUI (gone), the `e2e-lib.sh` unset list, and a `disposablehub_audit_test.go` map entry.
- `EVENER_TUI_LOG_FILE`: **delete**. Only the TUI reads it.
- `EVENER_HUB_BIN`: **delete**. Only the TUI's `hubstart` reads it.

- [ ] **Step 2: Edit `envvars/envvars.go`**

Delete the `EVENERHubAuthToken`, `EVENERTUILogFile`, and `EVENERHubBin` `Var` constants and their entries in `allVars`. Change the `EVENERHubAddr` summary to: `Default hub address for local development tooling (the Vite dev-server proxy and browser guards).` Leave `Visibility`, `Secret`, and ordering otherwise intact.

- [ ] **Step 3: Remove the dead references to the deleted vars**

In `scripts/lib/e2e-lib.sh` drop `EVENER_HUB_AUTH_TOKEN` from the `unset` list at line 172 and remove or shorten the comment at lines ~163-166 that explains it in terms of `cmd/evener-tui/internal/hubstart`. In `disposablehub_audit_test.go`, if the env map at line ~131 sets `EVENER_HUB_AUTH_TOKEN`, remove that entry. Do not touch `EVENER_HUB_ADDR` in that map.

- [ ] **Step 4: Update `docs/developing-evener/environment.md`**

Remove the rows for `EVENER_TUI_LOG_FILE`, `EVENER_HUB_BIN`, and (if present) `EVENER_HUB_AUTH_TOKEN`. Reword the `EVENER_HUB_ADDR` description to match the new summary. Leave every other row.

- [ ] **Step 5: Run the envvars and hub isolation tests**

```bash
cd /Users/jesse/git/prime-radiant/evener
go test ./envvars/ -count=1
go test . -run 'TestDisposableHub|TestInstall' -count=1
go build ./...
```
Expected: all PASS; `All()` length equals `len(allVars)`; no unresolved symbol for the removed vars.

- [ ] **Step 6: Commit**

```bash
git add envvars/envvars.go docs/developing-evener/environment.md scripts/lib/e2e-lib.sh disposablehub_audit_test.go
git commit -m "refactor(envvars): drop TUI-only variables and correct EVENER_HUB_ADDR"
```

---

### Task 4: Generated AppWire protocol doc

**Files:**
- Modify: `internal/appwiredoc/protocol.md.tmpl` (two mentions: the prose line and the ASCII diagram)
- Regenerate: `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: nothing.
- Produces: a regenerated `docs/appwire-protocol.md` whose prose no longer names `evener tui`.

- [ ] **Step 1: Edit the template**

In `internal/appwiredoc/protocol.md.tmpl` change the prose that reads `the browser and \`evener tui\` talk to \`evener hub\`` to name only the browser (for example `the browser talks to \`evener hub\``), and update the ASCII diagram line `browser / evener tui  ──WS /rpc──▶  evener hub` to `browser  ──WS /rpc──▶  evener hub`, keeping the column alignment readable. Do not hand-edit `docs/appwire-protocol.md`.

- [ ] **Step 2: Regenerate and verify freshness**

```bash
cd /Users/jesse/git/prime-radiant/evener
make generate
go test . -run 'Generated|LintGenerated' -count=1 || make lint-generated
git diff --stat docs/appwire-protocol.md
```
Expected: `make generate` rewrites `docs/appwire-protocol.md`; the freshness check passes; the diff shows only the two TUI removals.

- [ ] **Step 3: Commit**

```bash
git add internal/appwiredoc/protocol.md.tmpl docs/appwire-protocol.md
git commit -m "docs(appwire): regenerate the protocol reference without the TUI"
```

---

### Task 5: Doc-audit test and coverage comment

**Files:**
- Modify: `architecturedoc_audit_test.go` (delete `TestTUIHubControlDialsAppWire`)
- Modify: `scripts/lib/e2e-lib.sh` (covered by Task 3; do not duplicate here)

**Interfaces:**
- Consumes: nothing.
- Produces: no test reads a TUI source path.

- [ ] **Step 1: Delete the audit test**

Remove the whole `TestTUIHubControlDialsAppWire` function from `architecturedoc_audit_test.go`, including its preceding explanatory comment about the TUI's control path. If the file registers that test in a fuzz seed table or a list, remove the registration too.

- [ ] **Step 2: Verify the file still compiles and its other tests pass**

```bash
cd /Users/jesse/git/prime-radiant/evener
go vet . && go test . -run 'ArchitectureDoc|TestTUI' -count=1
```
Expected: PASS; no test named `TestTUI...` remains.

- [ ] **Step 3: Commit**

```bash
git add architecturedoc_audit_test.go
git commit -m "test: drop the TUI AppWire doc-audit that had no subject left"
```

---

### Task 6: Scenario cards triage

**Files:**
- Delete (TUI-only): `test/scenarios/tui-effort-command.md`, `tui-goal-set-and-complete.md`, `tui-interrupt-live-turn.md`, `tui-model-switch.md`, `tui-paste-image-from-clipboard.md`, `tui-paste-image-path.md`, `tui-queue-then-completes.md`, `tui-queue-then-drain-as-steer.md`, `tui-steer-in-idle-fails-fast.md`, `tui-steer-live-turn.md`, `tui-steer-success-reconciles.md`, `tui-workspace-navigation.md`, `ask-tui-answer.md`, `ended-session-metrics-tui-and-web.md`
- Modify: `test/scenarios/INDEX.md` and the shared cards below

**Interfaces:**
- Consumes: nothing.
- Produces: no scenario card or INDEX entry references the TUI; every shared assertion keeps a surviving driver or is recorded as dropped.

- [ ] **Step 1: Classify each shared card by its actual driver**

For each of `cli-sibling-binary.md`, `cli-help.md`, `status-vocabulary-roundtrip.md`, and the `model-picker-*.md` family, read the card and answer: does it drive the assertion through `evener tui` only, or does it have a web/native/CLI driver too?

```bash
cd /Users/jesse/git/prime-radiant/evener
for f in cli-sibling-binary cli-help status-vocabulary-roundtrip; do echo "== $f =="; rg -n 'evener tui|evener-tui|web|npm|node|curl' test/scenarios/$f.md | head; done
```
Record the answer per card. `cli-sibling-binary` drives `binresolve` (which the hub still uses in production) through `evener tui`; either retarget it to a surviving driver for sibling resolution or delete it with a one-line note in the commit message saying the production consumer remains covered by `internal/binresolve/sibling_test.go`.

- [ ] **Step 2: Delete TUI-only cards and update the INDEX**

```bash
cd /Users/jesse/git/prime-radiant/evener
git rm test/scenarios/tui-effort-command.md test/scenarios/tui-goal-set-and-complete.md test/scenarios/tui-interrupt-live-turn.md test/scenarios/tui-model-switch.md test/scenarios/tui-paste-image-from-clipboard.md test/scenarios/tui-paste-image-path.md test/scenarios/tui-queue-then-completes.md test/scenarios/tui-queue-then-drain-as-steer.md test/scenarios/tui-steer-in-idle-fails-fast.md test/scenarios/tui-steer-live-turn.md test/scenarios/tui-steer-success-reconciles.md test/scenarios/tui-workspace-navigation.md test/scenarios/ask-tui-answer.md test/scenarios/ended-session-metrics-tui-and-web.md
```
Then remove their entries from `test/scenarios/INDEX.md`, and remove the TUI mention from each shared card's INDEX line and body (Steps 1's findings decide delete vs reword).

- [ ] **Step 3: Run the scenario corpus checks**

```bash
cd /Users/jesse/git/prime-radiant/evener
go test . -run 'Scenario|Scenarios' -count=1
```
Expected: PASS. The scenario-source-citation audit must not report a card citing a TUI path; the INDEX must not list a deleted card.

- [ ] **Step 4: Commit**

```bash
git add test/scenarios
git commit -m "test(scenarios): retire the TUI cards and re-drive the shared ones"
```

---

### Task 7: User and product docs

**Files:**
- Modify: `README.md` (the `## Evener TUI` section, the intro line, the subcommand list, the credential-path note)
- Modify: `docs/architecture.md` (process list, the diagram, the `evener tui` client table row)
- Modify: `docs/product/subsystems.md` (the `S04 Terminal dashboard` row, the `TUI` mermaid node and edge, S04 references in the task table)
- Modify: `docs/product/friction.md` (cases `U01`, `U02`, their summary rows, S04 recovery-table references)
- Modify: `UPGRADING.md` (forward-looking claims only: the current-subcommand list and the `evener tui` example; keep the historical migration record)
- Modify: `docs/getting-started.md`, `docs/evener-hub.md`, `docs/llm-provider-config-and-launch.md`, `docs/web-ui/keybindings.md`, `docs/evener-hub-remote-operations.md`, `docs/developing-evener/testing.md`, `docs/developing-evener/agentic-testing.md`, `docs/developing-evener/conventions/{go-workspace,naming,agent-fleets}.md`

**Interfaces:**
- Consumes: nothing.
- Produces: live docs describe no TUI; historical/dated records keep their wording.

- [ ] **Step 1: Enumerate every live-doc mention**

```bash
cd /Users/jesse/git/prime-radiant/evener
rg -n -i 'evener[- ]tui|\bTUI\b' README.md UPGRADING.md docs/architecture.md docs/product/subsystems.md docs/product/friction.md docs/getting-started.md docs/evener-hub.md docs/llm-provider-config-and-launch.md docs/web-ui/keybindings.md docs/evener-hub-remote-operations.md docs/developing-evener/testing.md docs/developing-evener/agentic-testing.md docs/developing-evener/conventions
```
Expected: the list of lines each task step below removes or rewords. Files under `docs/superpowers/{plans,proofs,handoffs,specs}/` are dated records: leave them.

- [ ] **Step 2: README**

Delete the `## Evener TUI (Terminal User Interface)` section through the next `##`. Fix the intro line (line ~7) and the subcommand list (line ~39) to drop `tui`, and the credential-path note (line ~64) that mentions `hub/TUI`.

- [ ] **Step 3: Product docs**

In `docs/product/subsystems.md` delete the `S04 Terminal dashboard` table row, remove the `TUI` node and its `TUI --> Hub` edge from the mermaid overview, and scrub `S04` from the "find the owner" task table and the recovery tables. Do not renumber other IDs. In `docs/product/friction.md` delete the `U01 TUI uncertain submission` and `U02 TUI failed submission attachments` sections, their rows in the summary table, and `S04` from the recovery tables. Leave other case IDs intact.

- [ ] **Step 4: Architecture, UPGRADING, and the remaining live docs**

In `docs/architecture.md` remove the TUI from the process list, the process diagram, and the client table row; keep the AppWire description accurate for the browser. In `UPGRADING.md` remove only the forward-looking lines (the current subcommand list at line ~42 and the `evener tui` example at line ~62); keep the historical "five old binaries" migration record and the `make build-tui` history. In each remaining file from Step 1, reword the TUI mention to a surviving surface or delete the sentence, matching surrounding style.

- [ ] **Step 5: Run the doc audits**

```bash
cd /Users/jesse/git/prime-radiant/evener
go test . -run 'Doc|Architecture|Scenario|Product' -count=1
```
Expected: PASS. No audit reports a stale source link or a missing subsystem row.

- [ ] **Step 6: Commit**

```bash
git add README.md UPGRADING.md docs/
git commit -m "docs: remove the legacy TUI from user and product references"
```

---

### Task 8: Comment-only mentions in other languages

**Files:**
- Modify (as needed): `appwire/appwiretest/scripted_transport.go`, `appwire-client/typescript/reducer.ts`, `appwire-client/typescript/reducer.test.ts`, `cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.tsx`, `cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.test.tsx`

**Interfaces:**
- Consumes: nothing.
- Produces: no stray comment names the TUI.

- [ ] **Step 1: Read each mention and decide**

```bash
cd /Users/jesse/git/prime-radiant/evener
rg -n -i 'tui' appwire/appwiretest/scripted_transport.go appwire-client/typescript/reducer.ts appwire-client/typescript/reducer.test.ts cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.tsx cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.test.tsx
```
Reword a mention that describes current behavior; leave one that is an intentional historical note and say why in the commit message.

- [ ] **Step 2: Verify the TS and Go packages still pass**

```bash
cd /Users/jesse/git/prime-radiant/evener
go build ./...
cd cmd/evener-hub/frontend && npx biome check --write src/panes/session/transcript/messages/UserMessageItem.tsx src/panes/session/transcript/messages/UserMessageItem.test.tsx
```
Expected: `go build` passes; Biome reports no violations on the touched files.

- [ ] **Step 3: Commit**

```bash
git add appwire/appwiretest/scripted_transport.go appwire-client/typescript cmd/evener-hub/frontend/src
git commit -m "docs: drop TUI mentions from cross-language comments"
```

---

### Task 9: Final sweep and full gates

**Files:**
- Modify: only if the sweep finds a missed reference.

**Interfaces:**
- Consumes: Tasks 1-8 complete.
- Produces: a verified-clean branch.

- [ ] **Step 1: Repo-wide sweep**

```bash
cd /Users/jesse/git/prime-radiant/evener
rg -n -i 'evener[- ]tui|cmd/evener-tui' -g '!**/.worktrees/**' -g '!**/.claude/**' -g '!docs/superpowers/plans/**' -g '!docs/superpowers/proofs/**' -g '!docs/superpowers/handoffs/**' -g '!docs/superpowers/specs/2026-10-16-legacy-tui-removal-design.md' .
```
Expected: empty. Any hit is a missed reference; fix it in the file it appears in and amend this task's work.

- [ ] **Step 2: Confirm the package is gone and the CLI still works**

```bash
cd /Users/jesse/git/prime-radiant/evener
test ! -e cmd/evener-tui && echo "package gone"
make build 2>/dev/null || go build ./...
./evener --help | rg -c 'tui' || echo "tui not listed"
```
Expected: `package gone`; help output contains no `tui`.

- [ ] **Step 3: Run the gates, one at a time**

```bash
cd /Users/jesse/git/prime-radiant/evener
make lint
make vet
make test
```
Expected: each exits 0. If `make test` is long, run it; it is the source of truth for this change. Do not run these three as parallel background jobs.

- [ ] **Step 4: Commit any fixes**

```bash
git add -u
git commit -m "test: final TUI-removal sweep fixes"
```

---

## Self-Review

- **Spec coverage:** package deletion and CLI unwiring (Task 1); gates named in the design — testing budget, fuzz registry, mutation manifest, copyonly allowlist, coverage script, help/install tests (Task 1); env vars (Task 3); doc-audit test and e2e comment (Tasks 5, 3); generated protocol doc (Task 4); prompt template and goldens (Task 2); scenario cards (Task 6); product/user docs including S04 and friction U01/U02 (Task 7); cross-language comments (Task 8); verification (Task 9). The spec's "no special handling for `evener tui`" is a Global Constraint and is checked in Task 9 Step 2.
- **Step scan:** every step names a file, a command, or an exact edit. Deletion tasks state what to remove by name; no step says "handle edge cases".
- **Type consistency:** no new types are introduced; the only named symbols are existing ones being removed.
- **Review Focus:** each of the five items is pinned to a task (1, 1, 4, 2, 3 respectively).
- **Proportion:** the plan is mostly deletions and single-line edits, matching a removal spec of this size.
