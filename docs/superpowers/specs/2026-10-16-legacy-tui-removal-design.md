# Remove the legacy TUI

Date: 2026-10-16
Status: approved 2026-10-16

## Intent and why

The interactive terminal dashboard (`evener tui`) is being retired. Evener keeps
its non-interactive CLI and its clients: the hub, the browser workspace, the
phone app, and the Playdate client. The terminal story after this change is the
non-interactive CLI (`evener <prompt>`, `evener serve`, `evener doctor`,
`evener models`, `evener providers`, `evener migrate`, and the rest), not an
interactive TUI.

Two forces drive this:

- The TUI is a large surface (152 files, ~93k LOC in `cmd/evener-tui/`, 14
  internal subpackages) whose value overlaps the web and native clients. It
  costs maintenance, fuzzing, coverage, and doc upkeep disproportionate to what
  it uniquely serves.
- Jesse's ruling: "we need a cli. but not a tui." The CLI is a hard requirement;
  the TUI is not.

This is a removal, not a redesign. Nothing replaces the TUI as a client.

## Goals

- `cmd/evener-tui/` is gone from the tree; nothing imports it.
- `evener tui` no longer exists as a subcommand, and no help text, doc, script,
  test, fuzz registry entry, or coverage gate references it.
- The remaining CLI subcommands and the one-shot prompt path behave exactly as
  before.
- `make lint`, `make vet`, `make test`, and the fuzz-registry check stay green.

## Non-goals

- No new terminal client, interactive or otherwise.
- No change to the hub, AppWire protocol, session daemon, browser, native, or
  Playdate clients. They do not depend on the TUI.
- No change to the `appwire` package, which the TUI used but does not own.

## Anti-goals (what would make this a failure even if it "works")

- Leaving a dangling reference: a doc, scenario card, fuzz target, or script
  line that names `evener-tui` or `evener tui` after the package is gone.
- Breaking a shared contract the TUI happened to exercise. One test audits TUI
  source by path (`architecturedoc_audit_test.go`); it must be removed with its
  architecture-doc claim, and any behavior it pins that lives outside the TUI
  must keep a driver.
- Removing an env var or flag a non-TUI reader uses.
- Quietly changing CLI behavior (for example, making `evener` with no args do
  something new).

## Constraints

- One `evener` binary. `.goreleaser.yml` builds only `./cmd/evener/` and its
  comment already states "all former `evener-<x>` binaries are now
  subcommands." There is no separate `evener-tui` executable to remove from
  release, install, or `EVENER_INSTALL_BINS`.
- Only `cmd/evener/main.go` imports the TUI package (verified: `rg
  'primeradiant\.com/evener/cmd/evener-tui'` outside the dir returns that one
  line). No production package outside the TUI reads a TUI internal package.
- Product docs must stay evergreen in the same change (see
  `docs/product/README.md`).

## What was found (the inventory)

**Go code**
- `cmd/evener/main.go`: `tuicmd` import (line 22), the `tui` field on
  `cliCommandRunners`, `case "tui"` in the dispatch switch (line 556), the
  runner closure (lines 498-500), and the help line `tui  Run the evener-tui
  terminal UI` (line 402).
- `cmd/evener/main_test.go`: `TestTopLevelHelpListsSubcommands` (line 277) has
  `"tui"` in its expected subcommand list (line 283). Remove the entry.
  `cmd/evener/seed_coverage_fuzz_test.go` (line 75) registers that test in the
  fuzz seed table; it stays, the test itself just loses one expected string.
- `install_test.go`: line 386 runs `evener tui --help` against the installed
  binary. Remove that invocation (the real-binary install path still needs the
  other subcommands).
- `architecturedoc_audit_test.go` and `scenariosourcecite_audit_test.go`: see the
  doc-audit note above.
- `cmd/evener-tui/`: the whole directory, including `internal/` subpackages:
  clipboard, hubdiagnostics, hubstart, inputhistory, launchconfig, modeldisplay,
  msgrender, pending, toolsummary, transcript, tuipick, tuiprim, tuitext,
  tuitheme.
- `internal/binresolve/sibling.go`: doc comment names `evener-tui` as an
  example sibling. The package itself is used by `cmd/evener-hub/main.go` and
  stays; only the comment needs rewording.
- Tests naming the TUI as a comment or fixture (keep the test, reword or drop
  the reference): `agent/session_communicate_issue831_test.go`,
  `agent/system_prompt_test.go`, `appwire/client_test.go`,
  `cmd/evener-hub/app_auth_wire_fixture_test.go`, `cmd/evener-hub/app_rpc_test.go`,
  `cmd/evener-hub/internal/appsource/local_daemon_test.go`,
  `cmd/evener/internal/rvreg/rvreg_test.go`, `cmd/evener/serve_goal_test.go`,
  `internal/binresolve/sibling_test.go`, `rendezvous/identity_test.go`,
  `rendezvous/ownership_test.go`, `internal/devtool/copyonly/inventory_test.go`.
- Comment-only mentions in other languages and fixtures (reword, or confirm the
  mention is an intentional historical note and leave it):
  `appwire/appwiretest/scripted_transport.go`,
  `appwire-client/typescript/reducer.ts` and `reducer.test.ts`,
  `cmd/evener-hub/frontend/src/panes/session/transcript/messages/UserMessageItem.tsx`
  and its `.test.tsx`. Also two fuzz corpora carry the string.

**Doc-audit tests that name TUI source by path (must be updated)**
- `architecturedoc_audit_test.go`: `TestTUIHubControlDialsAppWire` reads
  `cmd/evener-tui/internal/hubstart/hub_start.go` and asserts `dialHubRPC` dials
  AppWire. With the file gone this test has nothing to audit; delete it and its
  architecture-doc claim.
- `scenariosourcecite_audit_test.go`: its `cmd/evener-tui/pending.go` mention is
  an in-test example of an anchorless *mention* (which the audit deliberately
  does not check), not a card citation. No edit needed. (Verified against the
  test source; the earlier draft of this doc was wrong to flag it.)

**Env vars** (`envvars/envvars.go`)
- `EVENER_TUI_LOG_FILE` — TUI-only. Delete.
- `EVENER_HUB_BIN` — summary says "used by evener tui autostart"; only the TUI's
  `hubstart` reads it. Delete unless a non-TUI reader is found.
- `EVENER_HUB_ADDR` / `EVENER_HUB_AUTH_TOKEN` — summaries say "for evener-tui",
  but hub browser tests set them. Verify each var's production readers before
  deciding; keep a var only if a non-TUI production reader exists, and reword
  the summary otherwise. Do not delete a var a test or the hub relies on.

**Build, coverage, fuzz, scripts**
- `make/building.mk` / `Makefile`: no `evener-tui` build or install entry
  (`EVENER_INSTALL_BINS := evener`). The TUI package builds only because it is a
  package in the root module; removing the directory removes it. Note
  `UPGRADING.md` still describes old `make build-tui` / `make build-hub` targets
  that no longer exist (see Docs).
- `testing-budget.json`: 15 `primeradiant.com/evener/cmd/evener-tui...` package
  keys (root 40.39s plus the 14 internal subpackages). Remove them, and the four
  `TestTUITmuxE2E_*` names if they appear in the live-stack E2E class map.
  `TestTimingBudgetClassMapNamesMatchTheTree` fails if a key names a package that
  no longer exists, so this must land with the deletion.
- `scripts/fuzz/fuzz-targets.txt`: 32 target lines under `./cmd/evener-tui...`.
  Remove them; `make lint-fuzz-registry` (`fuzz-registry-check`) must stay green.
- `fuzz/mutations/manifest.tsv` (line 8): the `toolsummary-taskupdate-panic`
  mutation points at `. :FuzzSummarizeTool`, a now-deleted target, and
  `toolsummary-taskupdate-panic.patch` edits
  `cmd/evener-tui/internal/toolsummary/tool_summary.go`. Remove both the manifest
  row and the patch file.
- `internal/devtool/copyonly/allowlist.tsv` (line 13): an allowlist entry for
  `cmd/evener-tui/internal/launchconfig/launch_schema_covtest_test.go`. Remove
  it; `TestNoNewCopyOnlyTests` fails on a stale entry.
- `scripts/coverage/e2e-cover.sh`: the `--tui` flag, the tmux battery invocation
  (`go test -run 'TmuxE2E' ... ./cmd/evener-tui/`), and the three `evener tui`
  smoke runs. Remove.
- `scripts/lib/e2e-lib.sh`: a comment explaining why `EVENER_HUB_AUTH_TOKEN` is
  unset references `cmd/evener-tui/internal/hubstart`. Reword or drop.

**Scenario cards** (`test/scenarios/`, 14 TUI cards plus `INDEX.md` entries)
- TUI-named cards: `tui-effort-command`, `tui-goal-set-and-complete`,
  `tui-interrupt-live-turn`, `tui-model-switch`, `tui-paste-image-from-clipboard`,
  `tui-paste-image-path`, `tui-queue-then-completes`, `tui-queue-then-drain-as-steer`,
  `tui-steer-in-idle-fails-fast`, `tui-steer-live-turn`, `tui-steer-success-reconciles`,
  `tui-workspace-navigation`, `ask-tui-answer`,
  `ended-session-metrics-tui-and-web`.
- Cards whose driver is the TUI but whose subject is shared:
  `cli-sibling-binary` (drives `binresolve` through `evener tui`),
  `cli-help` (asserts usage across `evener tui` and `evener openai`),
  `status-vocabulary-roundtrip` and the `model-picker-*` family (assert shared
  vocabulary/model rules partly through the TUI). Triage each: keep the shared
  assertion with a surviving driver (web/native/CLI) where one exists, delete
  the TUI-only steps, and delete the card when nothing else can drive it.

**Embedded prompt and its goldens (live, pinned)**
- `agent/prompts/system.md.tmpl` (line 137): the sentence "When the task is about
  evener or evener-tui, run them like any other program under test" names the
  TUI. Reword.
- `agent/testdata/messagingprompt/{root,root-renamed-result-tool,delegate,delegate-result-schema}.md`:
  four goldens carry that same sentence. Regenerate or hand-edit them in the same
  change; a golden test pins them.

**Generated docs**
- `docs/appwire-protocol.md` is generated (header says so). Its prose lives in
  `internal/appwiredoc/protocol.md.tmpl`, which names `evener tui` twice (the
  prose and the ASCII diagram). Edit the template and run `make generate`;
  `lint-generated` fails if the checked-in doc is stale.

**Docs**
- `README.md`: the `Evener TUI (Terminal User Interface)` section (from
  `## Evener TUI` to the next `##`), the intro line, the subcommand list, and
  the credential-path note.
- `UPGRADING.md`: the old-binary migration table and prose (lines 42-62, 88,
  116) describe `evener-tui` and `make build-tui`. This is a historical upgrade
  guide; decide whether to keep the "five old binaries" history (it is a record
  of a past migration) and only drop forward-looking claims, or leave it whole.
  **My call:** keep the historical migration record intact and remove only the
  forward-looking claim that `evener tui` is current (line 42's subcommand list
  and line 62's `evener tui` example). A dated migration record is not a live
  reference.
- `docs/product/subsystems.md`: the `S04 Terminal dashboard` row, the `TUI` node
  and edge in the mermaid overview, and S04 references in the task table and
  elsewhere.
- `docs/product/friction.md`: cases `U01 TUI uncertain submission` and
  `U02 TUI failed submission attachments`, their summary-table rows, and the S04
  references in the recovery tables. These gaps resolve by deletion.
- `docs/architecture.md`: the process/diagram mentions and the `evener tui`
  client table row.
- `docs/getting-started.md`, `docs/evener-hub.md`,
  `docs/llm-provider-config-and-launch.md`, `docs/web-ui/keybindings.md`,
  `docs/appwire-protocol.md`, `docs/evener-hub-remote-operations.md`,
  `docs/developing-evener/environment.md`, `docs/developing-evener/testing.md`,
  `docs/developing-evener/agentic-testing.md` (includes a "Driving the TUI with
  tmux" section), `docs/developing-evener/conventions/{go-workspace,naming,agent-fleets}.md`.
  Each needs the TUI reference removed or reworded to a surface that survives.

## Decisions made (my calls, stated so they can be checked)

- **No replacement client.** The non-interactive CLI is the terminal story. No
  new interactive surface is built.
- **Delete, do not preserve behind a flag.** The subcommand, the package, the
  doc-audit test, and the fuzz targets are removed outright. No
  `--enable-tui` or build-tag gate. (Backward compatibility is not added without
  explicit approval; none is requested.)
- **Subsystem IDs are not renumbered.** `S04` becomes a gap; the other S-IDs stay
  as they are. Renumbering S05-S24 and every reference across the docs would churn
  a large surface for no product value. All `S04` references get scrubbed.
- **One coherent change.** Docs, code, tests, scripts, and fuzz registry land
  together so no dangling reference exists mid-merge.

## What is left to the builder

- Per-env-var reader verification (`EVENER_HUB_ADDR`, `EVENER_HUB_AUTH_TOKEN`)
  with the exact `rg` evidence, then keep-and-reword or delete.
- Per-scenario-card triage: for each shared card, name the surviving driver and
  the assertion kept; for each TUI-only card, delete it and its `INDEX.md` entry.
  `cli-sibling-binary` in particular needs either a new driver for `binresolve`
  (the hub uses it in production) or deletion with a note.
- The exact wording for reworded comments and doc lines.
- Confirmation that no generated file (for example a doc-audit golden) names the
  TUI, via the repo's generated-output freshness check.

## Top-level dispatch after removal (the one hazard)

`dispatchCLICommandWith` returns `(false, "", nil)` for an unknown first word
(line 537) and for the empty arg list. Control then falls through to the prompt
path: `cliprompt.Read(fs.Args(), ...)` joins the args and runs them as a prompt
(`cmd/evener/main.go:251`). So today `evener tui` reaches the TUI case; after
removing that case, an unknown word like `tui` becomes a *prompt* sent to a
model. This is existing behavior for any unknown word (that is how `evener "fix
the bug"` works) and must not change generally.

Consequence for the acceptance check: after removal, `evener tui` does **not**
exit non-zero. It either runs a session whose prompt is the word "tui", or, with
no other args and an interactive terminal, prints usage and exits 1 because the
prompt is empty. The plan must state the real behavior, not assert an error.

This leaves one product choice for Jesse: should `evener tui`, and the word `tui`
as a first argument generally, be special-cased with a short "the TUI was
removed; use `evener --help`" notice, or left to run as an ordinary prompt? See
"Open questions for Jesse" below.

## How done is checked

- `rg -i 'evener[- ]tui' --glob '!**/.worktrees/**' --glob '!**/.claude/**'`
  across the repo returns only intentional historical notes under
  `docs/superpowers/{plans,proofs,handoffs}/` (dated records, which stay) and
  nothing in code, scripts, live docs, fuzz registry, or scenarios.
- `go build ./...` and `make build-go` pass.
- `make lint` (golangci-lint, naming, generated-output freshness, fuzz-registry,
  secret check) passes.
- `make vet` and `make test` pass.
- `evener --help` no longer lists `tui`. `evener tui --help` after removal runs
  as a prompt of the word "tui" (existing unknown-word behavior), not an error.
- `evener "prompt"`, `evener serve`, and `evener doctor` are unchanged.

## Open questions for Jesse

None. Jesse ruled on 2026-10-16: no special handling for `evener tui`. After
removal the word `tui` is an ordinary unknown first word, so `evener tui` runs it
as a prompt (or prints usage and exits 1 when the prompt is empty on an
interactive terminal). Do not add a removal notice or a bespoke branch.

Confirmed on the same date: do not renumber subsystem IDs.
