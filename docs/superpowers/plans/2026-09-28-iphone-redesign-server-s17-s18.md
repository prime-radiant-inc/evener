# iPhone redesign, Phase 7: the model on rows and worktree launches (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Every task carries its code, so every task's implementer is Sonnet; the reviewer checks it against this plan and the code as it is on main.

**Goal:** A Board row can name its session's model (S17) and the New session sheet can start a session in a new worktree branch named at launch (S18).

**Architecture:**
- **S17 (PR 36).** The daemon's root row already names its current model (`Thread.ModelProvider`, which follows a switch). The prober keeps it as `LiveEntry.CurrentModel`; `BuildTree` resolves each row's model from one closure (live: the probe's model, else the model the daemon started on; ended: the meta's); `projectShallow` sends `model_name`, the name `model/list` gives the same model, so a row and the model picker agree. A hub serving a controller names its roots' current model on its list rows, so a remote row follows a switch too. No daemon change.
- **S18 (PRs 37 and 38).** The daemon already knows how to make a managed worktree: `manage_worktree create` (native worktree tools). PR 37 lets a launcher ask for one before the first turn: `evener serve --worktree <branch>` runs that same create for a fresh session before the daemon listens, and `launch-check` advertises the flag. PR 38 adds `ThreadStartParams.worktreeBranch`, a read-only `evener/worktree/check` method (the New session sheet's Branch row asks it; the start asks it too), passes the branch to the daemon as `--worktree=<branch>`, and checks a remote host before forwarding, so a host too old to know the field refuses instead of starting on its current branch.

**Tech Stack:** Go 1.27 workspace (root module and `agent/`, see `go.work`), AppWire over WebSocket (`ProtocolVersion` `"evener-appwire-v6"`, `appwire/types.go:30`), TypeScript 6 in `appwire-client/typescript` (vitest from `cmd/evener-hub/frontend`), git (argument vectors only).

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`: 7.2 (the row's last line: "the model's display name when 'Show model on Board rows' is on"), 11 (New session: Branch "current branch, or a new worktree branch (name field)"), 12 (Hub: Display "Show model on Board rows"), 18 (S17, S18). The server plan `docs/superpowers/plans/2026-09-26-iphone-redesign-server-additions.md` holds the lane's Global Constraints. The phone side is the phase 5 plan (`docs/superpowers/plans/2026-09-26-iphone-redesign-phase5-new-session-hub.md`): rulings 8 (no model toggle until S17), 15 (Branch is information only until S18) and 26 (S17 and S18 join the server lane). Written and dry-run against main at `6b194e38b` (see Self-review).

## What was measured

Everything below was read on main at `6b194e38b`.

**The model a session runs (S17).**
- The daemon's root row names its model: `appThreadWithDiagnosticsLocked` sets `Thread.ModelProvider` from `status.Model` (`server/appwire_runtime.go:2438`), the bare model id (`gpt-5.6`), with the provider instance beside it in `Evener.Profile`. `status.Model` follows a switch: `SetModelFunc`'s hook calls `UpdateSessionInfo` right after `SetModel` (`cmd/evener/serve.go:1498-1510`), and startup calls it too (`:1695`). The status-only `thread/list` the prober reads carries the same row (`handleAppThreadList`, `server/appwire_runtime.go:1161`).
- The prober keeps the root's `Profile` (`cmd/evener-hub/internal/hubcore/prober.go:175`) but not its model. `LiveEntry` embeds the rendezvous entry, whose `Model` is the model the daemon started on (`rendezvous/rendezvous.go:35`, written at `cmd/evener/serve.go:1862`) and goes stale after a switch.
- An ended session's model is its meta's (`schema.SessionMeta.Model`, `agent/schema/snapshot.go:144`), which the switch's autosave keeps current (`agent/session.go:1638`).
- `TreeNode` and `hubapi.NavigationSessionSummary` carry no model (`hubcore/tree.go:447-525`, `hubapi/navigation.go:277-371`). `LastMessage` (S1d) is the precedent this plan copies: one closure in `BuildTree` (`lastMessageFor`, `hubcore/tree.go:1161`), a probe field, a fingerprint line, a projection line, a schema bound, a codec entry and the shared fixture.
- The only display name the hub and the phone give a model is `prettifyModelDisplayName` (`cmd/evener-hub/app_models.go:252`; the TUI carries its own copy, `cmd/evener-tui/hub_commands.go:711`): `withDisplayNames` (`:295`) fills every `model/list` row's `displayName` from it, and no source sets one of its own (no Go code assigns `DisplayName` elsewhere). So `gpt-5.6` reads "Gpt 5.6" and `claude-opus-4-7-20260101` reads "Claude Opus 4 7" in the phone's model picker today (`mobile-native/src/modelPickerEntries.ts:32`).
- Remote rows: a controller builds a host's rows from the host's `thread/list` (`appThreadTreeEntries`, `cmd/evener-hub/web_api_tree.go:920`), which already copies `thread.ModelProvider` into both the meta and the entry (`:936`, `:966`). The host fills that field from the rendezvous entry's start model (`threadFromEntry`, `cmd/evener-hub/internal/appsource/local_daemon.go:1181`), so after a switch a remote row would name the old model until the host names its current one.

**How a session starts (S18).**
- `thread/start` is `hubThreadStart` (`cmd/evener-hub/app_threadlifecycle.go:59`). A non-local `Source` is forwarded whole to that host's hub (`:89-94`, `RemoteHubSource.StartThread`, `cmd/evener-hub/internal/appsource/remote_hub_mutations.go:101`). Locally it canonicalizes `cwd` (`:99-106`, `InvalidParams("cwd: …")` on failure), resolves the launch, and calls `Spawner.Spawn` (`:194`), whose failures come back as `HubLaunchError` with the daemon's log tail.
- `ThreadStartParams` (`appwire/types.go:2257`) and `hubcore.SpawnRequest` (`cmd/evener-hub/internal/hubcore/config.go:278`) have no branch or worktree field. `buildSpawnArgs` (`cmd/evener-hub/spawn.go:365`) builds `evener serve`'s argument vector; `spawnDaemon` runs it with `exec.Command`, no shell.
- A hub decodes AppWire params with plain `json.Unmarshal` (`appserver.HandleTyped`, `internal/appserver/router.go:44`), so a hub that predates a field ignores it. A remote host is not always on the controller's build: sshconn installs its own build only when a deploy path is configured (spec 12's Hosts note).
- The hub gates serve flags on the child's `launch-check`: `validateEvenerLaunchContract` (`cmd/evener-hub/spawn.go:1028`) requires `api-log` in `launch_flags`, which `launchcheck.supportedLaunchFlags` advertises (`cmd/evener/internal/launchcheck/launchcheck.go:44`).
- The daemon already makes managed worktrees: `manage_worktree create` is `Session.worktreeCreate` (`agent/session_tools_worktree.go:1282`) over `worktreeCreateCore` (`:1122`). It resolves the canonical project (a linked checkout resolves to its main checkout), validates the name with `worktree.ValidateName` before any git call (`:1167`), runs `git check-ref-format --branch`, refuses an existing branch, writes a sidecar, then runs `git worktree add --lock --reason <marker> -b <name> -- <path> <base sha>` (`:1258`) through `execenv.RunGit`, an argument vector. It swaps the session's environment into the lane, saving the checkout as the restore root (`enterWorktree`, `:805`), and the environment refresh re-reads the git branch (`agent/session_env_swap.go:141-144`). A failure after the add rolls the lane back.
- Lanes live at `<state dir>/worktrees/<project id>/<name>` (`worktreeRootForProject`, `agent/session_tools_worktree.go:771`; the path at `:1154-1160`). A hub-spawned daemon's state dir is the project's runtime dir (`resolveStateDirForProject`, `cmd/evener-hub/spawn.go:802`): `EVENER_STATE_DIR` when the launch configuration's env sets it, else `$XDG_STATE_HOME/evener/projects/<project id>` (`agent/runtime_dir.go:44-50`).
- `worktree.ValidateName` (`agent/internal/worktree/name.go:23-78`): `^[A-Za-z0-9_][A-Za-z0-9_./-]*$`, at most 100 bytes, no `..`, no trailing `/` or `.`, no empty component, no component starting with `.` or ending `.lock`. It lives in `agent/internal`, which `cmd/evener-hub` cannot import; package `agent` exports no wrapper today.
- A resume re-enters a session's lane from its meta (`WorktreePath`, `WorktreeManaged`, `WorktreeRestoreRoot`, `agent/schema/snapshot.go:219-236`; `resumeWorktreeReentry`, `agent/session_worktree_resume.go:34`), so a lane made at launch survives a restart with no new code.
- `evener/git/head` (`cmd/evener-hub/app_git_head.go:21`) is the Branch row's read today; it runs `git -C <dir> rev-parse` through the `gitCommand` seam (`:14`), an argument vector. It is allow-listed on the host admin proxy (`cmd/evener-hub/app_host_admin.go:131`), which is how a client reads a remote host's.
- The web's spawn sheet shows the branch as display-only (`cmd/evener-hub/frontend/src/panes/spawn/Spawn.tsx:447`).

## Global Constraints

- **Wire.** Every change is additive and stays on `ProtocolVersion = "evener-appwire-v6"` (`appwire/types.go:30`); the navigation read stays `representationVersion: 2`.
  - `NavigationSessionSummary.model_name` (snake_case, `hubapi`) is `omitempty`. The codec lists it, and a build that predates it drops the key unread (the tolerant codec, server PR 1).
  - `ThreadStartParams.worktreeBranch` is `omitempty`. A hub that predates it ignores it; PR 38 makes the controller check a host before forwarding it (ruling 9).
  - `evener/worktree/check` is a new method. An older hub answers MethodNotFound, which each client reads as "not supported here" and keeps its fallback.
  - `worktreeBranchRefused` is a new `evenerErrorInfo` value on an existing code (`CodeInvalidParams`); a client that does not know it sees an invalid-params error with the message.
  - **Never add a `FeatureSet` key.** The TS `initialize` decoder refuses unknown feature keys.
- **Casing.** `hubapi` navigation JSON is snake_case (`model_name`); `appwire` JSON is camelCase (`worktreeBranch`). The tagliatelle lint enforces both (`.golangci.yml`).
- **A new navigation summary field** lands in the `hubapi` struct, `projectShallow`, `navigationSessionValueValid`, the codec's `SESSION_KEYS` and `sessionValue`, and the shared fixture `cmd/evener-hub/testdata/navigation/value-records.json` (`TestNavigationValueRecordFixtureNamesEveryWireField` fails until it does). A `LiveEntry` field a row shows is hashed in `rosterFingerprint`.
- **hubcore behavior tests** for the roster and the tree are `fuzzScenario*` functions registered in `FuzzHubcoreScenarios` (`cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go`); an unregistered one never runs, and `golangci-lint run ./cmd/evener-hub/internal/hubcore/` reports it unused. Run them with `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`. Store tests follow the keybindings store's plain `Test*` functions.
- **A new hub method** gets a catalog row (`appwire/protocol.go`), a decision in `TestHostAdminAllowListMatchesCatalog` (`cmd/evener-hub/app_host_admin_test.go`), and an entry in `TestHubRPCRegistersExpectedHandlerSet` (`cmd/evener-hub/app_rpc_test.go`). An allow-listed one also joins `remoteHostAdminMethods` and the read-only table of `TestHostAdminMutationClassificationMatchesAllowList`.
- **The TUI rule.** No PR changes what `internal/appprojector` emits, so no TUI case is needed.
- **Generated files.** After any `appwire` or `hubapi` type change: `make generate`, then `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`, and commit `appwire-client/typescript/types.gen.ts` and `docs/appwire-protocol.md`. `make lint-generated` compares against the index, so run it after `git add`.
- **Git and the shell.** Every git call is an argument vector (`exec.CommandContext`, `execenv.RunGit`), never a shell line. A branch reaches git only after `agent.ValidateWorktreeName` accepts it, which refuses a leading `-`, so no branch can be read as an option; the daemon flag is one argument, `--worktree=<branch>`.
- **Go floors** per touched module (root, and `agent/` for PR 37): `go vet`, the same with `-tags evenerfuzz`, and `GOOS=windows go vet -tags evenerfuzz`; format with `$(go env GOROOT)/bin/gofmt`, never the one on PATH; the pinned `golangci-lint` (2.13.1, `.tool-versions`) on each touched package. Its `modernize` check wants promoted fields in `LiveEntry` literals (`{PID: 1, Model: …}`, not `{Entry: rendezvous.Entry{…}}`) and `wg.Go`.
- **TypeScript floor** (PR 36 only). From `cmd/evener-hub/frontend`: `npx biome check --write` on the touched codec files, `npx vitest run ../../../appwire-client/typescript/state/navigation`, `npm run typecheck`. Never run Biome from the repo root or in `mobile-native`, and never `npm ci` through a symlinked `node_modules`.
- **Targeted tests only.** Run each task's tests and the gates it names; CI runs the full matrix. Some tests fail or hang on macOS only (#2497); CI (Linux) is the judge.
- **Deterministic tests.** No network, no sleeps, no unbounded waits: a notification or a daemon's exit is awaited on its channel with the bounded `time.After` guard the existing tests use. Git tests build a one-commit repository in `t.TempDir()` with `GIT_CONFIG_GLOBAL=/dev/null` and `GIT_CONFIG_NOSYSTEM=1`, and resolve temp paths with `filepath.EvalSymlinks` (macOS's `/var` is a symlink). The daemon test drives a real `runServe` with a scripted provider. Every new test is shown failing before its code lands.
- **Line numbers** are on main at `6b194e38b`. Another lane's merge moves them, so every step also names its anchor: find it by the name.
- **Size,** measured on the dry run (production lines only; tests and generated files excluded): PR 36 about 90, PR 37 about 80, PR 38 about 200. Landing follows the handoff: a regular PR, CI green on the merged head, RoboRev's comment read, /simplify run and its fixes pushed, then an admin squash merge with `--match-head-commit <full sha>`.

## Rulings

Decisions the spec and the server plan leave open, with the reason for each.

**S17, the model on rows**

1. **The wire carries the display name, `model_name`, and not the id.** The spec asks for the display name, and the Board has no model catalog to name an id with. The hub names it with `prettifyModelDisplayName`, the function every `model/list` row's `displayName` comes from, so a row and the model picker say the same words (question 1).
2. **A live row names the model its daemon runs now.** The root row's `modelProvider` follows a switch (`UpdateSessionInfo`), so the prober keeps it as `LiveEntry.CurrentModel` and the fingerprint hashes it: a switch moves nothing else on a row. Before the first probe a live entry names the model its daemon started on; an ended row names its meta's.
3. **Subagent rows carry none,** as with `last_message` (S1d ruling 21): the Board lists top-level sessions, and 500 subagent rows would spend the response's byte budget on names nothing shows. Phase 4's Subagents list reads each delegate's own model from `EvenerDelegateInfo`.
4. **No daemon change.** The daemon already sends the current model; S17 is hub-only, one PR.
5. **A remote row follows a switch.** A hub serving a controller puts its roots' current model in `modelProvider` ahead of the start model (`threadFromEntry`); the controller already reads that field for its rows.

**S18, a new worktree branch at launch**

6. **The daemon makes the lane, with the code `manage_worktree create` already runs.** `evener serve --worktree <branch>` calls the new `Session.StartInNewWorktree`, which is `worktreeCreate(ctx, branch, "")` plus a meta save, for a fresh session, before the daemon listens. The name rule, `check-ref-format`, the existing-branch refusal, the lock, the sidecar, the rollback, the sandbox re-root and resume re-entry are all the existing tool's. A hub-side `git worktree add` was the alternative; it would duplicate that code outside the `agent` module (whose `internal/worktree` the hub cannot import) and leave the lane without a restore root, so the session could never leave or remove it.
7. **Where the lane lives.** On the host that runs the session, at `<that host's state dir for the project>/worktrees/<project id>/<branch>`: by default `$XDG_STATE_HOME/evener/projects/<project id>/worktrees/<project id>/<branch>` (`~/.local/state/…` when `XDG_STATE_HOME` is unset), or `$EVENER_STATE_DIR/worktrees/<project id>/<branch>` when the launch configuration's env sets `EVENER_STATE_DIR` (`resolveStateDirForProject`, `cmd/evener-hub/spawn.go:802`). Never inside the checkout. `<branch>` may hold `/` (`feature/login` is a nested folder), and the name rule keeps every component inside the lane root. The branch is new, named exactly as typed, cut from the checkout's current `HEAD` (questions 2 and 3).
8. **How the name is validated, three times.** `agent.ValidateWorktreeName` (the new export of `worktree.ValidateName`: `^[A-Za-z0-9_][A-Za-z0-9_./-]*$`, at most 100 bytes, no `..`, no leading `-` or `.`, no trailing `/` or `.`, no empty component, no component ending `.lock`) runs first everywhere, before any git call. So no path (`../x`, `/abs`), no option (`-b`) and no shell syntax (space, quote, `$`, `` ` ``, `;`, `@{`) gets further. Then `git check-ref-format --branch`, and a refusal of an existing branch (`git show-ref --verify --quiet refs/heads/<branch>`). The hub runs these in `evener/worktree/check` and in `thread/start` for an early, typed refusal; serve runs the name rule at flag parse; the daemon's create runs all three again at the moment it adds the lane, which is the one that counts.
9. **A remote host is checked before the start is forwarded.** A host whose hub predates S18 would ignore `worktreeBranch` and start the session on the checkout's current branch, which is worse than refusing. `RemoteHubSource.StartThread` calls the host's `evener/worktree/check` first; MethodNotFound becomes `Unavailable("<host> runs an older Evener that can't start a session in a new worktree; update Evener on <host>")`, and a problem the host names becomes the same refusal a local start gives (question 4).
10. **A refused branch is a typed `InvalidParams`.** `worktreeBranchRefused` with `{problem}`: `invalidName`, `notGitRepository` or `branchExists`, so the sheet highlights the Branch row (spec 11: "the field it concerns highlighted") without matching message text. A branch that appears between the check and the daemon's create fails the launch as `HubLaunchError`, with the daemon's reason in its log tail.
11. **The hub requires the daemon to advertise `--worktree`,** through the existing `launch-check` flag contract, and only for a start that asks for a worktree. A daemon binary that predates it refuses such a start as `HubLaunchError` and still launches every other session.
12. **`evener/worktree/check` is on the host admin proxy,** read-only like `evener/git/head`, so the phone can check a remote host's branch the way it reads the host's current branch.
13. **No cleanup is added.** A lane made at launch is a managed worktree like any other: `manage_worktree` exits and removes it, and it stays locked to its session until then.

## Questions for Jesse

Each has a recommendation, and the plan is built to it. None blocks the server work; each can still be overturned in the phone lane.

1. **Which name does a row show for a model?** The hub has no real display names: the picker shows the id prettified ("Gpt 5.6", "Claude Opus 4 7"), not the spec mock's "GLM 5.3 Vision". **Recommendation:** rows use the picker's name, so the two always agree; better names are one follow-up that fixes both at once.
2. **What does a new worktree branch start from?** **Recommendation:** the checkout's current `HEAD`, as `manage_worktree create` does. The alternative is a base picker (for example `origin/main`), which the spec doesn't show.
3. **Is the branch named exactly as typed?** **Recommendation:** yes, no prefix; the same text names the folder. The alternative is a per-user prefix (`jesse/…`), which the spec doesn't show.
4. **What happens on a host running an older Evener?** **Recommendation:** refuse with "studio runs an older Evener that can't start a session in a new worktree; update Evener on studio", rather than start on the current branch without saying so.

## Review Focus

1. **A branch name that escapes the lane root or runs as something.**
   - `../x`, `/abs`, `a/../b`, `-b`, `.hidden`, `$(touch pwned)`, a space, a quote or a backtick must be refused before any git call, on the hub, in serve's flag parse and in the daemon, and must create nothing outside `<state dir>/worktrees/<project id>`.
   - Pinned by `TestValidateWorktreeNameRefusesPathsAndShellSyntax` and `TestStartInNewWorktreeRefusesWithoutLeavingALane` (Task 37.1), `TestValidateStartupWorktree` and `TestServe_WorktreeLaunchRefusalEndsTheLaunch` (Task 37.2), and `TestNewWorktreeProblem`, which also checks a `$(touch pwned)` name ran nothing (Task 38.1).
2. **A worktree start that silently lands on the current branch.**
   - A host whose hub predates S18 ignores the field; a daemon that predates the flag would die on it. Neither may start a session on the checkout's branch.
   - Pinned by the "predates S18" case of `TestRemoteHubStartThreadChecksAWorktreeStartWithTheHost` (Task 38.3) and `TestValidateEvenerLaunchContractRequiresTheFlagsALaunchPasses` (Task 38.2).
3. **An existing branch reused.** A new worktree is always a new branch; a name that exists must be refused, never checked out.
   - Pinned by `TestStartInNewWorktreeRefusesWithoutLeavingALane` (Task 37.1), `TestServe_WorktreeLaunchRefusalEndsTheLaunch` (Task 37.2) and `TestHubRPCThreadStartRefusesAWorktreeBranchBeforeSpawning` (Task 38.2).
4. **A row naming the wrong model.** After a switch, the row must name the new model, on this hub and through a controller, and the change must reach clients even though nothing else on the row moved.
   - Pinned by `fuzzScenarioStatusProber_KeepsTheCurrentModel`, `fuzzScenarioRoster_FingerprintMovesWhenOnlyTheModelMoves` and `fuzzScenarioBuildTree_RowsNameTheirOwnSessionsModel` (Task 36.1), and `TestLocalDaemonSourceListNamesTheRootsCurrentModel` (Task 36.3).

---

## PRs and lanes

| PR | Item | Tasks | Production lines (dry run) | Depends on |
|---|---|---|---|---|
| 36 | S17: the model's display name on rows | 36.1-36.3 | about 90 | none |
| 37 | S18a: `evener serve --worktree` | 37.1-37.2 | about 80 | none |
| 38 | S18b: `worktreeBranch` on thread/start, `evener/worktree/check` | 38.1-38.3 | about 200 | PR 37 |

- **Merge order.** Two independent lanes: PR 36; PR 37 then PR 38 (38 imports `agent.ValidateWorktreeName` and relies on the daemon flag, so branch it from main once 37 has merged).
- **Where the lanes meet.** `appwire/protocol.go`, `appwire/errors.go`, `cmd/evener-hub/app_rpc.go`, `cmd/evener-hub/app_rpc_test.go`, `cmd/evener-hub/app_host_admin_test.go` and the generated files (PR 38). Each edit is an added line beside a named anchor, so a conflict is two adjacent additions: keep both. When `types.gen.ts` or `docs/appwire-protocol.md` conflicts, take either side and run `make generate` again.
- **The server plan's PR map changes with this plan:** S17 (PR 36) and S18 (PRs 37 and 38) are new rows.

## Phone lane handoff

This lane changes `mobile-native/` only through the shared package. Once each PR is on the hub the phone talks to, the phone switches off its fallback. If phase 5 has not started, it builds on these directly.

**S17 (after PR 36).** Phase 5's ruling 8 and spec 7.2.
- `NavigationSessionSummary.model_name` is in `types.gen.ts` and the codec keeps it. Display gains "Show model on Board rows" (off by default); a row shows `model_name` last on its last line when it is on and the row carries one.
- A subagent row never carries one; an ended row carries its meta's.

**S18 (after PRs 37 and 38).** Phase 5's ruling 15 and spec 11.
- The Branch row offers "Current branch (main)" and "New worktree branch", which opens a name field.
- As the name is typed (debounced), call `evener/worktree/check` with `{cwd, branch}`: the local hub for the hub's own machine, through `evener/host/request` for a host. `problem` names the row's footer (the words are the phone lane's; for example `invalidName` "Use letters, numbers, and - _ . /", `branchExists` "That branch already exists"), and `notGitRepository` hides the option. MethodNotFound hides the option (a hub or host too old for S18).
- Start sends `worktreeBranch`. A refusal with `evenerErrorInfo` `worktreeBranchRefused` highlights the Branch row with its `problem`; `actionUnavailable` from a host names that host's update.
- Once started, the row's `branch` is the new branch.

**Known limits.**
- A lane made at launch stays until `manage_worktree` removes it (ruling 13).
- A launch whose worktree the daemon refuses (a branch that appeared after the hub's check) exits before it listens, but, like serve's other failures after the session is built (the boot generation, the listener), it leaves the session's files in the state dir: an ended session with no turns. The hub's check before the spawn makes this a race, not a path a person walks.
- On a case-insensitive filesystem (macOS by default), `Feature` and `feature` name one lane folder; the second create fails at `git worktree add` and rolls back, as `manage_worktree create` already does.
- The web's spawn sheet adopting the Branch choice is web work outside this lane. (#2939)

---

## PR 36: the model's display name on rows, S17 (Tasks 36.1-36.3)

**Branch:** `git fetch origin && git switch -c claude/s17-model-on-rows origin/main`

**What it adds.** Rows carry `model_name`: a top-level session's model by the name `model/list` gives it, following a switch while it is live and from its meta once it has ended; subagent rows carry none. A remote host's rows name their current model too. Nothing on the phone changes yet.

### Task 36.1: live entries and tree rows name the model

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/hubcore/roster.go` (`LiveEntry.CurrentModel` after `LastMessage`, `:125-128`; `ProbeResult.CurrentModel` after its `LastMessage`, `:180-182`; the end of `rosterFingerprint`'s per-session loop, `:651`; `liveEntryFromProbe`, `:1482`; `ReadSpawnedThread`'s result literal, `:1557`)
- Modify: `cmd/evener-hub/internal/hubcore/prober.go` (`Probe`'s result literal, `:176`)
- Modify: `cmd/evener-hub/internal/hubcore/tree.go` (`TreeNode.Model` after `LastMessage`, `:509-512`; a `modelFor` closure after `lastMessageFor`, `:1161-1169`; `buildNode`'s literal, `:1385`; the live-only leaf, `:1639`; the NeedsYou node, `:1742`)
- Create: `cmd/evener-hub/internal/hubcore/model_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go` (register three scenarios)

**Interfaces:**
- Consumes: `appwire.Thread.ModelProvider` (the daemon's current bare model id), `rendezvous.Entry.Model` (promoted on `LiveEntry`), `schema.SessionMeta.Model`.
- Produces: `ProbeResult.CurrentModel`, `LiveEntry.CurrentModel` and `TreeNode.Model`, all `string` (a model id such as `gpt-5.6`). A string needs no clone.

- [ ] **Step 1: Write the failing scenarios**

`cmd/evener-hub/internal/hubcore/model_test.go`:

```go
package hubcore

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/server"
)

// fuzzScenarioStatusProber_KeepsTheCurrentModel: the probe keeps the model the
// listed root runs now, which a row names (S17). The rendezvous entry's Model
// is the one the session started on and goes stale after a model switch.
func fuzzScenarioStatusProber_KeepsTheCurrentModel(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_model",
		state:     appwire.ThreadStatusIdle,
		setup: func(srv *server.Server) {
			srv.UpdateSessionInfo("th_model", "gpt-5.6", "codex-jesse-fsck.com")
		},
	})
	entry.Model = "kimi-k3"
	got := prober.Probe(entry)
	if !got.OK || got.CurrentModel != "gpt-5.6" {
		t.Fatalf("probe = %+v, want the current model gpt-5.6", got)
	}
	if live := liveEntryFromProbe(entry, got); live.CurrentModel != "gpt-5.6" || live.Model != "kimi-k3" {
		t.Fatalf("live entry current model %q start model %q, want the probe's beside the entry's own", live.CurrentModel, live.Model)
	}
}

// fuzzScenarioRoster_FingerprintMovesWhenOnlyTheModelMoves: a row names its
// session's model, which a switch moves while the status and every other
// field a row shows hold still (S17).
func fuzzScenarioRoster_FingerprintMovesWhenOnlyTheModelMoves(t *testing.T) {
	entry := func(model string) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusIdle, CurrentModel: model}}
	}
	if rosterFingerprint(entry("gpt-5.6")) == rosterFingerprint(entry("kimi-k3")) {
		t.Fatal("the roster fingerprint held when only the model moved")
	}
}

// fuzzScenarioBuildTree_RowsNameTheirOwnSessionsModel: a live session's rows
// name its daemon's current model, which outranks both the model it started on
// and the meta the past index may still hold; a live entry no probe has
// reached yet names the model it started on; an ended session's rows name its
// meta's; and a subagent row names none (S17).
func fuzzScenarioBuildTree_RowsNameTheirOwnSessionsModel(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01LIVE", Model: "gpt-5.5", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01ENDED", Model: "claude-opus-4-7", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01CHILD", Model: "kimi-k3-mini", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ParentSessionID: "01LIVE", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, Model: "gpt-5.4", SessionID: "01LIVE", Status: appwire.ThreadStatusAwaiting, RunningSubagentIDs: []string{"01CHILD"}, CurrentModel: "gpt-5.6"},
		{PID: 2, Model: "glm-5.3-vision", SessionID: "01UNPROBED", Status: appwire.ThreadStatusAwaiting},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01LIVE")
	if !inLive || !inProject || liveRow.Model != "gpt-5.6" || projectRow.Model != "gpt-5.6" {
		t.Fatalf("Live row %q (%v), project row %q (%v): both must name the daemon's current model", liveRow.Model, inLive, projectRow.Model, inProject)
	}
	for _, row := range tree.NeedsYou {
		if row.ID == "01LIVE" && row.Model != "gpt-5.6" {
			t.Fatalf("NeedsYou row names %q, want the daemon's current model", row.Model)
		}
	}
	if leaf, found, _, _ := liveAndProjectRowsFor(tree, "01UNPROBED"); !found || leaf.Model != "glm-5.3-vision" {
		t.Fatalf("unprobed leaf = %q (found %v), want the model it started on", leaf.Model, found)
	}
	if _, _, ended, found := liveAndProjectRowsFor(tree, "01ENDED"); !found || ended.Model != "claude-opus-4-7" {
		t.Fatalf("ended row = %q (found %v), want its meta's model", ended.Model, found)
	}
	if len(liveRow.Children) != 1 || liveRow.Children[0].Model != "" {
		t.Fatalf("children = %+v, want the one subagent row with no model", liveRow.Children)
	}
}

// A spawned session's first publication names its model too, so its row names
// it before the next scan.
func TestRosterReadSpawnedThreadPublishesTheCurrentModel(t *testing.T) {
	r, entry := newSpawnedRoster(t)
	if _, err := r.ReadSpawnedThread(t.Context(), entry, func(context.Context) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: "01SPAWNED", SessionID: "01SPAWNED", ModelProvider: "gpt-5.6",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	live, ok := r.Find("01SPAWNED")
	if !ok || live.CurrentModel != "gpt-5.6" {
		t.Fatalf("published entry = %+v, want the model the read carried", live)
	}
}
```

Register the three in `FuzzHubcoreScenarios` (`scenarios_fuzz_test.go`): `fuzzScenarioBuildTree_RowsNameTheirOwnSessionsModel` after `fuzzScenarioBuildTree_RowsCarryTheirOwnSessionsLastMessage`, `fuzzScenarioRoster_FingerprintMovesWhenOnlyTheModelMoves` after `fuzzScenarioRoster_FingerprintMovesWhenOnlyTheLastMessageMoves`, and `fuzzScenarioStatusProber_KeepsTheCurrentModel` after `fuzzScenarioStatusProber_DecodesRunningSubagentStates`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^(FuzzHubcoreScenarios|TestRosterReadSpawnedThreadPublishesTheCurrentModel)$' -count=1`
Expected: FAIL to compile (`CurrentModel` is not a field of `ProbeResult` or `LiveEntry`; `Model` is not a field of `TreeNode`).

- [ ] **Step 3: Implement**

`roster.go`, in `LiveEntry` after `LastMessage`:

```go
	// CurrentModel is the model the session runs now, from its probe (S17);
	// the embedded Entry's Model is the one it started on, and a model switch
	// leaves that behind. A row names it, so rosterFingerprint hashes it: a
	// switch moves nothing else a row shows.
	CurrentModel string
```

in `ProbeResult` after `LastMessage`:

```go
	// CurrentModel mirrors LiveEntry.CurrentModel: the listed root's model as
	// its daemon reports it now (S17).
	CurrentModel string
```

In `rosterFingerprint`, after the per-session loop's last statement, `_, _ = h.Write([]byte(bySess[id].LastMessage))`:

```go
		_, _ = h.Write([]byte{0})
		// A row names its session's model, which a switch moves while
		// everything else a row shows holds still (S17).
		_, _ = h.Write([]byte(bySess[id].CurrentModel))
```

`liveEntryFromProbe`: `CurrentModel:          result.CurrentModel,` after `LastMessage:           result.LastMessage,`. `ReadSpawnedThread`: its literal's last line `Profile: root.Evener.Profile}` becomes `Profile: root.Evener.Profile, CurrentModel: root.ModelProvider}`.

`prober.go`, in `Probe`'s literal after `LastMessage:           root.Evener.LastMessage,`:

```go
		CurrentModel:          root.ModelProvider,
```

`tree.go`, in `TreeNode` after `LastMessage`:

```go
	// Model is the id of the model the session runs (S17): a live session's
	// current model from its probe, an ended one's from its meta. Every
	// builder sets it from one closure; subagent rows have none.
	Model string
```

After the `lastMessageFor` closure (before `turnEndedAtFor`):

```go
	// modelFor resolves the model a session's rows name (S17): a live
	// session's current model from its daemon's probe, which follows a model
	// switch and outranks the meta the past index may still hold, then the
	// model its daemon started on (an entry no probe has reached yet), and an
	// ended one's from its meta, so every row of one session agrees. A
	// subagent row names none: the Board lists top-level sessions, and a tree
	// of 500 subagents would spend the response's byte budget on names no row
	// shows.
	modelFor := func(id, kind string) string {
		if kind == "subagent" {
			return ""
		}
		if entry, live := liveMap[id]; live {
			if entry.CurrentModel != "" {
				return entry.CurrentModel
			}
			return entry.Model // the model its daemon started on
		}
		return metaMap[id].Model
	}
```

In `buildNode`'s literal after `LastMessage:     lastMessageFor(m.ID, kind),`: `Model:           modelFor(m.ID, kind),`. In the live-only leaf's and the NeedsYou node's literals, after `LastMessage:     lastMessageFor(le.SessionID, "session"),`: `Model:           modelFor(le.SessionID, "session"),`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub/internal/hubcore -count=1`, `golangci-lint run ./cmd/evener-hub/internal/hubcore/`
Expected: PASS, `0 issues`.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/internal/hubcore/roster.go cmd/evener-hub/internal/hubcore/prober.go cmd/evener-hub/internal/hubcore/tree.go cmd/evener-hub/internal/hubcore/model_test.go cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go
git commit -m "feat(hub): live entries and top-level rows name their model"
```

### Task 36.2: rows carry `model_name`

**Implementer:** Sonnet.

**Files:**
- Modify: `hubapi/navigation.go` (`NavigationSessionSummary.ModelName` after `LastMessage`, `:313-320`)
- Modify: `cmd/evener-hub/navigation_projection.go` (`navigationModelName` before `projectShallow`, `:1811`; `projectShallow`'s literal, `:1840`)
- Modify: `cmd/evener-hub/navigation_schema.go` (`navigationSessionValueValid`, `:428`)
- Modify: `appwire-client/typescript/state/navigation/codec.ts` (`SESSION_KEYS`, `:179`; `sessionValue`, `:364`)
- Modify: `appwire-client/typescript/state/navigation/codec.test.ts` (after the `last_message` test, `:1437`)
- Modify: `cmd/evener-hub/testdata/navigation/value-records.json`
- Create: `cmd/evener-hub/navigation_model_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`

**Interfaces:**
- Consumes: `TreeNode.Model` (Task 36.1); `prettifyModelDisplayName` (`app_models.go:252`); `truncateNavigationRunes`, `maxNavigationLabelRunes` (512).
- Produces: `NavigationSessionSummary.ModelName string` (`model_name,omitempty`); TypeScript `NavigationSessionSummary.model_name?: string`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/navigation_model_test.go`:

```go
package hub

import (
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// A row names its session's model by the name model/list gives it (S17): the
// Board's "Show model on Board rows" and a row's last line. A dated snapshot
// id names its family, as the picker does; an over-long id is cut to the label
// bound; and a row with no model carries no key.
func TestNavigationRowsCarryTheModelName(t *testing.T) {
	rows := liveNavigationRows(t, []hubcore.TreeNode{
		{ID: "session-gpt", Title: "gpt", Kind: "session", State: "idle", Model: "gpt-5.6"},
		{ID: "session-dated", Title: "dated", Kind: "session", State: "idle", Model: "claude-opus-4-7-20260101"},
		{ID: "session-long", Title: "long", Kind: "session", State: "idle", Model: strings.Repeat("m", maxNavigationLabelRunes+40)},
		{ID: "session-unknown", Title: "unknown", Kind: "session", State: "idle"},
	})
	if got, want := string(navigationSummaryJSONFields(t, rows["session-gpt"])["model_name"]), `"Gpt 5.6"`; got != want {
		t.Fatalf("model_name on the wire = %s, want %s", got, want)
	}
	if got, want := rows["session-dated"].ModelName, prettifyModelDisplayName("claude-opus-4-7-20260101"); got != want || got != "Claude Opus 4 7" {
		t.Fatalf("dated model name = %q, want the picker's %q", got, want)
	}
	if got := utf8.RuneCountInString(rows["session-long"].ModelName); got != maxNavigationLabelRunes {
		t.Fatalf("long model name has %d runes, want the %d-rune label bound", got, maxNavigationLabelRunes)
	}
	if name, carried := navigationSummaryJSONFields(t, rows["session-unknown"])["model_name"]; carried {
		t.Fatalf("a row with no model carries %s", name)
	}
}

// The hub schema holds a row's model name to the label bound the projector
// cuts it to.
func TestNavigationSchemaBoundsTheModelName(t *testing.T) {
	session := navigationSchemaSession("local:schema-session", "schema-session")
	session.ModelName = strings.Repeat("é", maxNavigationLabelRunes)
	if !navigationSessionValueValid(session) {
		t.Fatal("a model name at its bound was refused")
	}
	session.ModelName = strings.Repeat("é", maxNavigationLabelRunes+1)
	if navigationSessionValueValid(session) {
		t.Fatal("a model name past its bound was accepted")
	}
}
```

In `value-records.json`, after the session's `"last_message"` line:

```json
    "model_name": "Gpt 5.6",
```

In `codec.test.ts`, after the test "codec keeps a row's last message within its bound and refuses one past it":

```ts
// A row names its session's model (S17). The codec keeps a name within the hub
// schema's label bound and refuses anything else.
test("codec keeps a row's model name within its bound and refuses one past it", () => {
  const onTheBound = "😀".repeat(512);
  expect(decodedSnapshot(key, snapshotWithSessionField("model_name", onTheBound)).snapshot.entities[0]?.value).toEqual({
    ...sessionValue("local:session"),
    model_name: onTheBound,
  });
  for (const malformed of ["", "m".repeat(513), 7, ["Gpt 5.6"]]) {
    expectContentFreeRejection(key, snapshotWithSessionField("model_name", malformed));
  }
});
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestNavigationRowsCarryTheModelName|TestNavigationSchemaBoundsTheModelName|TestNavigationValueRecordFixtureNamesEveryWireField' -count=1`
Expected: FAIL to compile (`ModelName` is not a field of `NavigationSessionSummary`).

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation/codec.test.ts`
Expected: FAIL: the new test (the codec drops `model_name`, so nothing is refused) and the fixture test.

- [ ] **Step 3: Implement**

`hubapi/navigation.go`, in `NavigationSessionSummary`: the `LastMessage`/`Dormant` pair after `Failure` becomes (gofmt aligns it):

```go
	LastMessage string `json:"last_message,omitempty"`
	// ModelName is the display name of the model the session runs (S17), for
	// the row's last line when a client shows models on rows: the name the
	// hub's model/list gives the same model, so a row and the model picker
	// agree. A live session's is its current model, which follows a switch;
	// an ended one's is its meta's; subagent rows carry none.
	ModelName string `json:"model_name,omitempty"`
	Dormant   bool   `json:"dormant,omitempty"`
```

`navigation_projection.go`, before `func (p navigationProjector) projectShallow(`:

```go
// navigationModelName is the name a row shows for a session's model (S17):
// the name model/list gives a model the registry leaves unnamed
// (withDisplayNames), so a row and the model picker agree. An unknown model
// names nothing.
func navigationModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	return truncateNavigationRunes(prettifyModelDisplayName(model), maxNavigationLabelRunes)
}
```

and in `projectShallow`'s literal after `LastMessage: …`: `ModelName:           navigationModelName(node.Model),`.

`navigation_schema.go`, in `navigationSessionValueValid`'s refusal condition after the `appwire.Excerpt(value.LastMessage, …) != value.LastMessage ||` line:

```go
		utf8.RuneCountInString(value.ModelName) > maxNavigationLabelRunes ||
```

`codec.ts`: add `"model_name",` to `SESSION_KEYS`' optional list after `"last_message",`, and in `sessionValue` after the `last_message` line:

```ts
    optional(value.model_name, (item) => boundedString(item, 512) && item !== "") &&
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched Go files, `make generate`, and `cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/state/navigation/codec.ts ../../../appwire-client/typescript/state/navigation/codec.test.ts`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub -run 'TestNavigation|TestCloneNavigationSummary' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`, `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation && npm run typecheck`
Expected: PASS; `types.gen.ts` gains `model_name?: string` on `NavigationSessionSummary`.

- [ ] **Step 5: Commit**

```bash
git add hubapi/navigation.go cmd/evener-hub/navigation_projection.go cmd/evener-hub/navigation_schema.go cmd/evener-hub/navigation_model_test.go cmd/evener-hub/testdata/navigation/value-records.json appwire-client/typescript/state/navigation/codec.ts appwire-client/typescript/state/navigation/codec.test.ts appwire-client/typescript/types.gen.ts
git commit -m "feat(hub): rows carry their model's display name"
```

### Task 36.3: a remote host's rows name their current model

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go` (`LocalDaemonEntry.CurrentModel` after `Tasks`, `:126`; `threadFromEntry`'s `ModelProvider`, `:1181`)
- Modify: `cmd/evener-hub/app_rpc.go` (`localDaemonEntriesFromRoster`'s root entry, `:140`)
- Create: `cmd/evener-hub/internal/appsource/local_daemon_model_test.go`, `cmd/evener-hub/remote_model_test.go`

**Interfaces:**
- Consumes: `LiveEntry.CurrentModel` (Task 36.1); `NavigationSessionSummary.ModelName` (Task 36.2).
- Produces: `appsource.LocalDaemonEntry.CurrentModel string`.

`appThreadTreeEntries` (`web_api_tree.go:936`, `:966`) already copies a host row's `modelProvider` into the meta and the entry's `Model`, and `modelFor` reads the entry's `Model` when `CurrentModel` is empty, so the controller needs no change: the host has to put its current model in that field.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/internal/appsource/local_daemon_model_test.go`:

```go
package appsource

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller names each root's current
// model on its row, ahead of the model the root started on, so a switch
// reaches the controller (S17). A row whose daemon no probe has reached keeps
// the model it started on.
func TestLocalDaemonSourceListNamesTheRootsCurrentModel(t *testing.T) {
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/switched", ThreadID: "th_switched", SessionID: "sess_switched", Model: "kimi-k3"}, Status: appwire.ThreadStatusIdle, CurrentModel: "gpt-5.6"},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/fresh", ThreadID: "th_fresh", SessionID: "sess_fresh", Model: "kimi-k3"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]string{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread.ModelProvider
	}
	if byID["th_switched"] != "gpt-5.6" || byID["th_fresh"] != "kimi-k3" {
		t.Fatalf("models = %q, want th_switched's current gpt-5.6 and th_fresh's start model kimi-k3", byID)
	}
}
```

`cmd/evener-hub/remote_model_test.go`:

```go
package hub

import (
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows name a root's current model and never lend it to
// the root's in-process subagent aliases.
func TestLocalDaemonEntriesFromRosterNameTheCurrentModelOnlyOnTheRoot(t *testing.T) {
	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{{
		Entry:     rendezvous.Entry{ThreadID: "sess_root", SessionID: "sess_root", Model: "kimi-k3"},
		SessionID: "sess_root", Status: appwire.ThreadStatusIdle,
		RunningSubagentIDs: []string{"sess_child"}, CurrentModel: "gpt-5.6",
	}})
	if len(entries) != 2 || entries[0].CurrentModel != "gpt-5.6" || entries[1].CurrentModel != "" {
		t.Fatalf("entries = %+v, want the root's current model and none on its alias", entries)
	}
}

// A controller reads a remote host's model off its list row, so the remote row
// names it like a local one: an online host's live row from the row as its
// live entry, and an offline host's last-known row, which is not live, from
// the row as its meta (S17).
func TestNavigationRemoteRowsNameTheirModel(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{
		{ID: "online-thread", Source: "remote-online", ModelProvider: "gpt-5.6", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}},
		{ID: "offline-thread", Source: "remote-offline", ModelProvider: "claude-opus-4-7", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}},
	})
	registry := appsource.NewRegistry()
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "remote-online"}, online: true})
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "remote-offline"}, online: false})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})
	web.sources = registry

	captured, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	projection, err := buildNavigationProjection(captured.Inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	for ref, want := range map[string]string{
		"remote-online:online-thread":   "Gpt 5.6",
		"remote-offline:offline-thread": "Claude Opus 4 7",
	} {
		if row := navigationProjectedSummary(t, projection, ref); row.ModelName != want {
			t.Errorf("%s model name = %q (live %v), want %q", ref, row.ModelName, row.Live, want)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/appsource -run 'TestLocalDaemonSourceListNamesTheRootsCurrentModel' -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRosterNameTheCurrentModelOnlyOnTheRoot|TestNavigationRemoteRowsNameTheirModel' -count=1`
Expected: FAIL to compile (`CurrentModel` is not a field of `LocalDaemonEntry`). `TestNavigationRemoteRowsNameTheirModel` pins the controller half, which already works once Task 36.2 is in: it guards the path this task relies on.

- [ ] **Step 3: Implement**

`local_daemon.go`, in `LocalDaemonEntry` after `Tasks`:

```go
	// CurrentModel mirrors hubcore.LiveEntry.CurrentModel: the model the root
	// runs now (S17). threadFromEntry puts it in appwire.Thread.ModelProvider
	// ahead of the embedded Entry's Model, the one the root started on, so a
	// controller hub names a remote session's model after a switch. A
	// read-only alias has none.
	CurrentModel string
```

and in `threadFromEntry`: `ModelProvider: firstLocalNonEmpty(entry.Model, entry.Provider),` becomes `ModelProvider: firstLocalNonEmpty(item.CurrentModel, entry.Model, entry.Provider),`.

`app_rpc.go`, in `localDaemonEntriesFromRoster`'s root entry, after `Tasks:              item.Tasks,`: `CurrentModel:       item.CurrentModel,`. The alias entries are built from their own fields (#2589) and get none.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./cmd/evener-hub/internal/appsource -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRoster|TestNavigationRemote|TestNavigationOffline' -count=1`
Expected: PASS.

Gates: `go vet ./cmd/evener-hub/... ./hubapi/`, the same with `-tags evenerfuzz` and with `GOOS=windows … -tags evenerfuzz`; `golangci-lint run ./cmd/evener-hub/ ./cmd/evener-hub/internal/appsource/ ./cmd/evener-hub/internal/hubcore/ ./hubapi/`; `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`; `git add` then `make lint-generated`.

- [ ] **Step 5: Commit and open PR 36**

```bash
git add cmd/evener-hub/internal/appsource/local_daemon.go cmd/evener-hub/internal/appsource/local_daemon_model_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/remote_model_test.go
git commit -m "feat(hub): a host names its roots' current model to its controller"
```

Title "feat(hub): rows name their model (S17, phase 7 PR 36)". The body says rows carry `model_name` for top-level sessions, live (following a switch) and ended, none for subagents (ruling 3), that the name is the model picker's own (ruling 1, question 1), and that no daemon change was needed (ruling 4).

---

## PR 37: `evener serve --worktree`, S18a (Tasks 37.1-37.2)

**Branch:** `git fetch origin && git switch -c claude/s18a-serve-worktree origin/main`

**What it adds.** A fresh daemon can start inside a new managed worktree on a new branch, made by the same code as `manage_worktree create`, before it listens; `launch-check` advertises the flag. Nothing calls it yet.

### Task 37.1: a session can start in a new worktree

**Implementer:** Sonnet.

**Files:**
- Create: `agent/session_worktree_launch.go`, `agent/session_worktree_launch_test.go`

**Interfaces:**
- Consumes: `worktree.ValidateName` (`agent/internal/worktree/name.go:39`); `Session.worktreeCreate` (`agent/session_tools_worktree.go:1282`); `Session.maybeAutoSave` (`agent/session.go:2513`); test helpers `newWorktreeRepo`, `wtRepo.managedPath`, `wtGit` (`agent/session_tools_worktree_create_test.go`, `_switch_test.go`).
- Produces (Task 37.2 and PR 38 use them):
  - `func ValidateWorktreeName(name string) error`
  - `func (s *Session) StartInNewWorktree(ctx context.Context, branch string) (WorktreeResult, error)`

- [ ] **Step 1: Write the failing tests**

`agent/session_worktree_launch_test.go`:

```go
package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The launcher's worktree (S18) is manage_worktree create run before the first
// turn: a new branch cut from HEAD, a lane under the state dir's worktrees
// folder, and the session inside it, with the meta naming the lane and the
// checkout it came from so a resume re-enters it.
func TestStartInNewWorktreeEntersAFreshLaneOnTheNamedBranch(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	want := r.managedPath(t, r.mainRoot, "feature/login")

	res, err := r.s.StartInNewWorktree(t.Context(), "feature/login")
	if err != nil {
		t.Fatalf("StartInNewWorktree: %v", err)
	}
	if res.Path != want || res.Branch != "feature/login" || res.BaseSHA != r.head || res.MainRoot != r.mainRoot {
		t.Fatalf("result = %+v, want path %s on feature/login from %s in %s", res, want, r.head, r.mainRoot)
	}
	if got := strings.TrimSpace(wtGit(t, res.Path, "rev-parse", "--abbrev-ref", "HEAD")); got != "feature/login" {
		t.Fatalf("the lane's HEAD = %q, want feature/login", got)
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != res.Path {
		t.Fatalf("session works in %q, want the lane %q", got, res.Path)
	}
	meta := r.s.Meta()
	if meta.WorktreePath != res.Path || !meta.WorktreeManaged || meta.WorktreeRestoreRoot != r.mainRoot {
		t.Fatalf("meta worktree = (%q, managed %v, restore %q), want (%q, true, %q)", meta.WorktreePath, meta.WorktreeManaged, meta.WorktreeRestoreRoot, res.Path, r.mainRoot)
	}
}

// A refused launch leaves the session at its checkout with nothing added: no
// lane, no branch. An existing branch is refused rather than reused, so a
// launch never lands on someone's work in progress.
func TestStartInNewWorktreeRefusesWithoutLeavingALane(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	wtGit(t, r.mainRoot, "branch", "taken")
	for name, branch := range map[string]string{
		"an existing branch": "taken",
		"a parent directory": "../escape",
		"an absolute path":   "/tmp/escape",
		"a leading dash":     "-b",
		"a shell expansion":  "$(touch pwned)",
	} {
		if _, err := r.s.StartInNewWorktree(t.Context(), branch); err == nil {
			t.Errorf("%s (%q) was accepted", name, branch)
		}
	}
	if got := r.s.currentEnv().WorkingDirectory(); got != r.mainRoot {
		t.Fatalf("session moved to %q after refusals, want %q", got, r.mainRoot)
	}
	if entries, err := os.ReadDir(filepath.Join(r.stateDir, "worktrees")); err == nil {
		for _, entry := range entries {
			lanes, _ := os.ReadDir(filepath.Join(r.stateDir, "worktrees", entry.Name()))
			for _, lane := range lanes {
				if lane.Name() != ".meta" {
					t.Errorf("a refused launch left %s behind", filepath.Join(entry.Name(), lane.Name()))
				}
			}
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(r.mainRoot), "escape")); !os.IsNotExist(err) {
		t.Fatalf("a refused name reached the filesystem outside the lane root: %v", err)
	}
	if meta := r.s.Meta(); meta.WorktreePath != "" {
		t.Fatalf("meta names a worktree %q after refusals", meta.WorktreePath)
	}
}

func TestValidateWorktreeNameRefusesPathsAndShellSyntax(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"feature/login", "fix-1234", "jesse/s18_branch.v2"} {
		if err := ValidateWorktreeName(name); err != nil {
			t.Errorf("ValidateWorktreeName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{
		"", "../escape", "a/../b", "/abs", "-b", ".hidden", "a/.hidden", "trailing/", "trailing.",
		"a//b", "x.lock", "has space", "semi;colon", "$(cmd)", "back`tick`", "quote'd", "at@{1}",
		strings.Repeat("a", 101),
	} {
		if err := ValidateWorktreeName(name); err == nil {
			t.Errorf("ValidateWorktreeName(%q) = nil, want a refusal", name)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./agent/ -run 'TestStartInNewWorktree|TestValidateWorktreeName' -count=1`
Expected: FAIL to compile (`StartInNewWorktree` and `ValidateWorktreeName` are undefined).

- [ ] **Step 3: Implement**

`agent/session_worktree_launch.go`:

```go
package agent

import (
	"context"

	"primeradiant.com/evener/agent/internal/worktree"
)

// ValidateWorktreeName reports whether name can name a managed worktree and
// the branch cut for it: the native worktree tools' own rule (spec §2). It
// runs no git, so a name it accepts can still be refused by git's
// check-ref-format or by a branch that already exists. It refuses an empty
// name, one over 100 bytes, a leading "-" or ".", "..", a trailing "/" or ".",
// an empty path component, a component ending ".lock", and any character
// outside letters, digits, "_", ".", "/" and "-" (so no space, quote, "$",
// "`", ";" or "@{").
func ValidateWorktreeName(name string) error {
	return worktree.ValidateName(name)
}

// StartInNewWorktree puts a fresh session to work in a new managed worktree
// before its first turn (S18): the launcher's form of manage_worktree create.
// It cuts a new branch named branch from the checkout's HEAD, adds the lane at
// <state dir>/worktrees/<project id>/<branch>, locks it for this session,
// enters it, and saves the session's meta, so the hub's row shows the new
// branch and a resume re-enters the lane. A failure leaves the session where
// it started, with no lane behind it.
func (s *Session) StartInNewWorktree(ctx context.Context, branch string) (WorktreeResult, error) {
	res, err := s.worktreeCreate(ctx, branch, "")
	if err != nil {
		return WorktreeResult{}, err
	}
	s.maybeAutoSave()
	return res, nil
}
```

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./agent/ -run 'TestStartInNewWorktree|TestValidateWorktreeName|TestWorktreeCreate' -count=1`, `cd agent && golangci-lint run .`
Expected: PASS, `0 issues`.

- [ ] **Step 5: Commit**

```bash
git add agent/session_worktree_launch.go agent/session_worktree_launch_test.go
git commit -m "feat(agent): a session can start in a new managed worktree"
```

### Task 37.2: serve's `--worktree` flag

**Implementer:** Sonnet.

**Files:**
- Create: `cmd/evener/serve_worktree.go`, `cmd/evener/serve_worktree_test.go`
- Modify: `cmd/evener/serve.go` (the flag after `traceFile`, `:426`; the check after `rejectPluginSelectionWithResume`, `:442`; the create after `deps.newSession`, `:726-735`)
- Modify: `cmd/evener/internal/launchcheck/launchcheck.go` (`supportedLaunchFlags` and its comment, `:40-44`)
- Modify: `cmd/evener/internal/launchcheck/launchcheck_test.go` (the `api-log` assertion, `:145-147`)

**Interfaces:**
- Consumes: `agent.ValidateWorktreeName`, `(*agent.Session).StartInNewWorktree` (Task 37.1); test helpers `installServeScriptedProvider`, `waitForServeTestRendezvous`, `shutdownServeTestDaemon` (`cmd/evener`), `cmdutil.ResolveSessionMeta`.
- Produces: the serve flag `--worktree <branch>` and `"worktree"` in `launch-check`'s `launch_flags` (PR 38's spawner passes the one and requires the other).

- [ ] **Step 1: Write the failing tests**

`cmd/evener/serve_worktree_test.go`:

```go
package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/cmdutil"
)

// serveWorktreeRepo is a one-commit git repository for a daemon to launch in,
// by its real path, with git isolated from the developer's own configuration.
func serveWorktreeRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=Evener Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "initial"},
		{"branch", "taken"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return repo
}

func serveWorktreeArgs(workDir, stateDir, runDir, branch string) []string {
	return []string{"--model", "openai/gpt-test", "--addr", "127.0.0.1:0", "--dir", workDir, "--state-dir", stateDir, "--run-dir", runDir, "--worktree", branch}
}

// A daemon launched with --worktree is inside a new lane on a new branch
// before it listens (S18), and its saved meta names the lane, the checkout it
// came from and the branch, which the hub's row and a resume read.
func TestServe_WorktreeLaunchStartsInANewLane(t *testing.T) {
	workDir := serveWorktreeRepo(t)
	stateDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})

	done := make(chan error, 1)
	go func() { done <- runServe(serveWorktreeArgs(workDir, stateDir, runDir, "feature/login")) }()
	entry := waitForServeTestRendezvous(t, runDir)
	if err := shutdownServeTestDaemon(context.Background(), entry.Address, entry.SessionID); err != nil {
		t.Fatalf("thread/shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not exit after thread/shutdown")
	}

	meta, err := cmdutil.ResolveSessionMeta(stateDir, entry.SessionID, false)
	if err != nil {
		t.Fatalf("ResolveSessionMeta: %v", err)
	}
	lanes := filepath.Join(stateDir, "worktrees") + string(filepath.Separator)
	if !strings.HasPrefix(meta.WorktreePath, lanes) || !strings.HasSuffix(meta.WorktreePath, filepath.FromSlash("/feature/login")) ||
		!meta.WorktreeManaged || meta.WorktreeRestoreRoot != workDir {
		t.Fatalf("meta worktree = (%q, managed %v, restore %q), want a managed lane under %s ending feature/login, restoring to %s",
			meta.WorktreePath, meta.WorktreeManaged, meta.WorktreeRestoreRoot, lanes, workDir)
	}
	if meta.EnvInfo.GitBranch != "feature/login" {
		t.Fatalf("meta branch = %q, want feature/login", meta.EnvInfo.GitBranch)
	}
	out, err := exec.Command("git", "-C", meta.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(out)) != "feature/login" {
		t.Fatalf("the lane's HEAD = %q (%v), want feature/login", out, err)
	}
}

// A launch whose worktree is refused ends before the daemon listens, so the
// hub reports the refusal as the start's failure and no session is announced.
func TestServe_WorktreeLaunchRefusalEndsTheLaunch(t *testing.T) {
	workDir := serveWorktreeRepo(t)
	installServeScriptedProvider(t, &scriptedProvider{name: "openai"})
	for branch, want := range map[string]string{
		"taken":     `branch "taken" already exists`,
		"../escape": `--worktree: worktree name "../escape"`,
	} {
		stateDir, runDir := t.TempDir(), t.TempDir()
		err := runServe(serveWorktreeArgs(workDir, stateDir, runDir, branch))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("--worktree %s: runServe = %v, want an error containing %q", branch, err, want)
		}
		if entries, _ := os.ReadDir(runDir); len(entries) != 0 {
			t.Fatalf("--worktree %s: a refused launch left a rendezvous behind: %v", branch, entries)
		}
	}
}

func TestValidateStartupWorktree(t *testing.T) {
	if err := validateStartupWorktree("", "sess", true); err != nil {
		t.Fatalf("no --worktree: %v, want nil", err)
	}
	if err := validateStartupWorktree("feature/login", "", false); err != nil {
		t.Fatalf("a fresh launch: %v, want nil", err)
	}
	for name, err := range map[string]error{
		"with --resume":      validateStartupWorktree("feature/login", "sess", false),
		"with --resume-last": validateStartupWorktree("feature/login", "", true),
		"a bad name":         validateStartupWorktree("$(touch pwned)", "", false),
	} {
		if err == nil {
			t.Errorf("--worktree %s was accepted", name)
		}
	}
}
```

In `launchcheck_test.go`, the assertion

```go
	if !slices.Contains(out.LaunchFlags, "api-log") {
		t.Fatalf("launch_flags=%v, want it to advertise api-log", out.LaunchFlags)
	}
```

becomes

```go
	if !slices.Contains(out.LaunchFlags, "api-log") || !slices.Contains(out.LaunchFlags, "worktree") {
		t.Fatalf("launch_flags=%v, want it to advertise api-log and worktree", out.LaunchFlags)
	}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener -run 'TestServe_Worktree|TestValidateStartupWorktree' -count=1`, `go test ./cmd/evener/internal/launchcheck -count=1`
Expected: FAIL to compile (`validateStartupWorktree` is undefined); the launch-check test fails with `launch_flags=[api-log]`.

- [ ] **Step 3: Implement**

`cmd/evener/serve_worktree.go`:

```go
package main

import (
	"errors"
	"fmt"

	"primeradiant.com/evener/agent"
)

// validateStartupWorktree checks serve's --worktree before any startup work
// (S18): it names a new branch for a fresh session, so a resume, which
// re-enters the worktree its session already had, refuses it, and a name the
// worktree rule refuses fails here rather than after the session is built.
func validateStartupWorktree(branch, resume string, resumeLast bool) error {
	if branch == "" {
		return nil
	}
	if resume != "" || resumeLast {
		return errors.New("--worktree starts a fresh session and cannot be used with --resume or --resume-last")
	}
	if err := agent.ValidateWorktreeName(branch); err != nil {
		return fmt.Errorf("--worktree: %w", err)
	}
	return nil
}
```

`serve.go`: after `traceFile := fs.String("trace", …)`:

```go
	worktreeBranch := fs.String("worktree", "", "start a fresh session in a new managed worktree on a new branch of this name, cut from the checkout's HEAD")
```

after the `rejectPluginSelectionWithResume` check:

```go
	if err := validateStartupWorktree(*worktreeBranch, *resume, *resumeLast); err != nil {
		return err
	}
```

and in the fresh-session branch, after `deps.newSession` succeeds (right after its `return fmt.Errorf("session creation: %w", err)` block, still inside `} else {`):

```go
		// A launch into a new worktree (S18) enters it before the daemon
		// listens, so no read and no first turn ever sees the session outside
		// it. A refusal (an existing branch, a checkout that is not a git
		// repository) ends the launch: the hub reports it as the start's
		// failure, and no client ever saw the session live.
		if *worktreeBranch != "" {
			if _, err := sess.StartInNewWorktree(ctx, *worktreeBranch); err != nil {
				sess.Close()
				return fmt.Errorf("start in a new worktree: %w", err)
			}
		}
```

`launchcheck.go`:

```go
// supportedLaunchFlags advertises the serve flags this binary accepts that a
// launcher (the hub) passes on the command line: api-log (which run parses
// too) and worktree (serve only, S18). Keep in sync with the flag definitions
// in cmd/evener's serve command: a launcher gates on this list before passing
// a flag, so a flag listed here must parse.
var supportedLaunchFlags = []string{"api-log", "worktree"}
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./cmd/evener -run 'TestServe_Worktree|TestValidateStartupWorktree|TestServe_FailedTurn' -count=1`, `go test ./cmd/evener/internal/launchcheck -count=1`
Expected: PASS. The daemon test prints serve's own `listening on …` line, as every serve test does.

Gates: `go vet ./cmd/evener/...` and `cd agent && go vet .`, each also with `-tags evenerfuzz` and with `GOOS=windows … -tags evenerfuzz`; `golangci-lint run ./cmd/evener/ ./cmd/evener/internal/launchcheck/`.

- [ ] **Step 5: Commit and open PR 37**

```bash
git add cmd/evener/serve.go cmd/evener/serve_worktree.go cmd/evener/serve_worktree_test.go cmd/evener/internal/launchcheck/launchcheck.go cmd/evener/internal/launchcheck/launchcheck_test.go
git commit -m "feat(serve): --worktree starts a fresh session in a new managed worktree"
```

Title "feat(serve): start a session in a new worktree branch (S18a, phase 7 PR 37)". The body says the flag runs `manage_worktree create`'s own code before the daemon listens (ruling 6), where the lane lives (ruling 7), how the name is validated (ruling 8), and that nothing passes the flag until PR 38.

---

## PR 38: `worktreeBranch` on thread/start and `evener/worktree/check`, S18b (Tasks 38.1-38.3)

**Branch:** `git fetch origin && git switch -c claude/s18b-thread-start-worktree origin/main`, once PR 37 is on main.

**What it adds.** A client can ask whether a directory can start a session in a new worktree branch, and start one: locally the hub checks, then spawns with `--worktree=<branch>`; for a host it checks that host first and refuses when the host is too old to honor the branch.

### Task 38.1: `evener/worktree/check`

**Implementer:** Sonnet.

**Files:**
- Create: `appwire/worktree.go`
- Modify: `appwire/errors.go` (`ErrorWorktreeBranchRefused` after `ErrorPathOutsideSession`, `:52`)
- Modify: `appwire/protocol.go` (a catalog row after `MethodEvenerGitHead`'s, `:191`)
- Modify: `appwire/client.go` (`WorktreeCheck` after `GitHead`, `:719`)
- Create: `cmd/evener-hub/app_worktree_check.go`, `cmd/evener-hub/app_worktree_check_test.go`
- Modify: `cmd/evener-hub/app_rpc.go` (register after `MethodEvenerGitHead`'s handler, `:2309-2311`)
- Modify: `cmd/evener-hub/app_host_admin.go` (allow-list after `MethodEvenerGitHead`, `:131`)
- Modify: `cmd/evener-hub/app_host_admin_test.go` (the policy row after `"evener/git/head"`, `:435`; the read-only row after `MethodEvenerGitHead`, `:1794`)
- Modify: `cmd/evener-hub/app_rpc_test.go` (`TestHubRPCRegistersExpectedHandlerSet`'s list after `MethodEvenerGitHead`, `:12767`)
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: `agent.ValidateWorktreeName` (PR 37); `hubCanonicalizeDir` (`app_threadlifecycle.go:36`); the `gitCommand` seam (`app_git_head.go:14`); test helpers `newHubRPCTestServer`, `dialHubRPC`.
- Produces (Tasks 38.2 and 38.3 use them):
  - `appwire.MethodEvenerWorktreeCheck = "evener/worktree/check"`, `WorktreeCheckParams{CWD, Branch}`, `WorktreeCheckResponse{Problem}`
  - `appwire.WorktreeProblemInvalidName`, `WorktreeProblemNotGitRepository`, `WorktreeProblemBranchExists`
  - `appwire.ErrorWorktreeBranchRefused`, `WorktreeBranchRefusedData{EvenerErrorInfo, Problem}`, `func WorktreeBranchRefused(problem, branch, cwd string) WireError`
  - `(*appwire.Client).WorktreeCheck`
  - `func newWorktreeProblem(ctx context.Context, cwd, branch string) string` (hub)
  - Test helper `worktreeCheckRepo(t) string` (a one-commit checkout with a `taken` branch)

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/app_worktree_check_test.go`:

```go
package hub

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// worktreeCheckRepo is a one-commit git checkout with a branch named "taken",
// by its real path, with git isolated from the developer's own configuration.
func worktreeCheckRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=Evener Test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "initial"},
		{"branch", "taken"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return repo
}

// The check says whether thread/start would take a new worktree branch here
// (S18), and why not: a name the worktree rule or git refuses, a directory
// outside any checkout, or a branch that already exists.
func TestNewWorktreeProblem(t *testing.T) {
	repo := worktreeCheckRepo(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, cwd, branch, want string
	}{
		{"a new branch", repo, "feature/login", ""},
		{"an existing branch", repo, "taken", appwire.WorktreeProblemBranchExists},
		{"a parent directory", repo, "../escape", appwire.WorktreeProblemInvalidName},
		{"a leading dash", repo, "-b", appwire.WorktreeProblemInvalidName},
		{"shell syntax", repo, "$(touch pwned)", appwire.WorktreeProblemInvalidName},
		{"an empty name", repo, "", appwire.WorktreeProblemInvalidName},
		{"outside a checkout", outside, "feature/login", appwire.WorktreeProblemNotGitRepository},
	} {
		if got := newWorktreeProblem(t.Context(), tc.cwd, tc.branch); got != tc.want {
			t.Errorf("%s: problem = %q, want %q", tc.name, got, tc.want)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "pwned")); !os.IsNotExist(err) {
		t.Fatalf("a branch name ran as a shell command: %v", err)
	}
}

// evener/worktree/check answers over the wire, and refuses a working
// directory that does not exist as thread/start's cwd does.
func TestHubRPCWorktreeCheck(t *testing.T) {
	repo := worktreeCheckRepo(t)
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	resp, err := client.WorktreeCheck(context.Background(), appwire.WorktreeCheckParams{CWD: repo, Branch: "taken"})
	if err != nil || resp.Problem != appwire.WorktreeProblemBranchExists {
		t.Fatalf("WorktreeCheck(taken) = %+v, %v; want branchExists", resp, err)
	}
	resp, err = client.WorktreeCheck(context.Background(), appwire.WorktreeCheckParams{CWD: repo, Branch: "feature/login"})
	if err != nil || resp.Problem != "" {
		t.Fatalf("WorktreeCheck(feature/login) = %+v, %v; want no problem", resp, err)
	}
	_, err = client.WorktreeCheck(context.Background(), appwire.WorktreeCheckParams{CWD: filepath.Join(repo, "missing"), Branch: "feature/login"})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("WorktreeCheck(missing cwd) err = %v, want InvalidParams", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestNewWorktreeProblem|TestHubRPCWorktreeCheck' -count=1`
Expected: FAIL to compile (`newWorktreeProblem`, `appwire.WorktreeCheckParams` and `client.WorktreeCheck` are undefined).

- [ ] **Step 3: Implement**

`appwire/worktree.go`:

```go
package appwire

import "fmt"

// MethodEvenerWorktreeCheck asks a hub whether a session could start in a new
// worktree on a branch (S18).
const MethodEvenerWorktreeCheck = "evener/worktree/check"

// The reasons a hub refuses a new worktree branch (S18). They ride
// WorktreeCheckResponse.Problem and WorktreeBranchRefusedData.Problem.
const (
	// WorktreeProblemInvalidName: the name breaks the worktree name rule or
	// git's branch-name rule.
	WorktreeProblemInvalidName = "invalidName"
	// WorktreeProblemNotGitRepository: the working directory is not inside a
	// git checkout, so there is nothing to branch.
	WorktreeProblemNotGitRepository = "notGitRepository"
	// WorktreeProblemBranchExists: the checkout already has a branch of that
	// name. A new worktree is always a new branch, so an existing one is
	// refused rather than reused.
	WorktreeProblemBranchExists = "branchExists"
)

// WorktreeCheckParams names a working directory and the new branch a session
// started there would work on.
type WorktreeCheckParams struct {
	CWD    string `json:"cwd"`
	Branch string `json:"branch"`
}

// WorktreeCheckResponse answers whether thread/start would accept
// WorktreeBranch for this directory now: Problem is empty when it would, else
// one of the WorktreeProblem values. The check runs git on the hub that owns
// the directory, so it describes that host; the start re-checks, since a
// branch can appear in between.
type WorktreeCheckResponse struct {
	Problem string `json:"problem,omitempty"`
}

// WorktreeBranchRefusedData is the WireError.Data of a thread/start refused
// for its WorktreeBranch: the Branch row's reason, as a WorktreeProblem value.
type WorktreeBranchRefusedData struct {
	EvenerErrorInfo ErrorInfo `json:"evenerErrorInfo"`
	Problem         string    `json:"problem"`
}

// WorktreeBranchRefused is thread/start's refusal of a WorktreeBranch for the
// reason problem (a WorktreeProblem value). It is InvalidParams, so a client
// that predates S18 still reads it as a refused start with the message.
func WorktreeBranchRefused(problem, branch, cwd string) WireError {
	var message string
	switch problem {
	case WorktreeProblemNotGitRepository:
		message = fmt.Sprintf("worktreeBranch: %s is not in a git repository", cwd)
	case WorktreeProblemBranchExists:
		message = fmt.Sprintf("worktreeBranch: branch %q already exists", branch)
	default:
		message = fmt.Sprintf("worktreeBranch: %q can't be a branch name", branch)
	}
	return WireError{
		Code:    CodeInvalidParams,
		Message: message,
		Data:    WorktreeBranchRefusedData{EvenerErrorInfo: ErrorWorktreeBranchRefused, Problem: problem},
	}
}
```

`appwire/errors.go`, after `ErrorPathOutsideSession`:

```go
	// ErrorWorktreeBranchRefused marks a thread/start refused for the new
	// worktree branch it asked for (S18); its data names the problem
	// (WorktreeBranchRefusedData). It shares CodeInvalidParams.
	ErrorWorktreeBranchRefused ErrorInfo = "worktreeBranchRefused"
```

`appwire/protocol.go`, after `MethodEvenerGitHead`'s row:

```go
	{MethodEvenerWorktreeCheck, WorktreeCheckParams{}, WorktreeCheckResponse{}, ScopeHub, "Reports whether a session could start in a new worktree on a branch of this name in a working directory (S18): an empty problem, or invalidName, notGitRepository or branchExists."},
```

`appwire/client.go`, after `GitHead`:

```go
func (c *Client) WorktreeCheck(ctx context.Context, params WorktreeCheckParams) (WorktreeCheckResponse, error) {
	var out WorktreeCheckResponse
	err := c.request(ctx, MethodEvenerWorktreeCheck, params, &out)
	return out, err
}
```

`cmd/evener-hub/app_worktree_check.go`:

```go
package hub

import (
	"context"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

// hubWorktreeCheck answers evener/worktree/check for a directory on this hub's
// own machine (S18). A controller reaches a host's through that host's hub.
func hubWorktreeCheck(ctx context.Context, params appwire.WorktreeCheckParams) (appwire.WorktreeCheckResponse, error) {
	cwd, err := hubCanonicalizeDir(params.CWD)
	if err != nil {
		return appwire.WorktreeCheckResponse{}, appwire.InvalidParams("cwd: " + err.Error())
	}
	return appwire.WorktreeCheckResponse{Problem: newWorktreeProblem(ctx, cwd, params.Branch)}, nil
}

// newWorktreeProblem reports why a session could not start in a new worktree
// on branch in the checkout holding cwd, as a WorktreeProblem value, or ""
// when it could. The daemon's own name rule runs first, so a name it refuses
// (a path, a leading "-", shell syntax) never reaches git; every git call is
// an argument vector, never a shell line. The daemon checks all of this again
// when it creates the lane: this answer is the New session sheet's, and a
// start's early refusal.
func newWorktreeProblem(ctx context.Context, cwd, branch string) string {
	if agent.ValidateWorktreeName(branch) != nil {
		return appwire.WorktreeProblemInvalidName
	}
	if gitCommand(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").Run() != nil {
		return appwire.WorktreeProblemNotGitRepository
	}
	if gitCommand(ctx, "git", "-C", cwd, "check-ref-format", "--branch", branch).Run() != nil {
		return appwire.WorktreeProblemInvalidName
	}
	if gitCommand(ctx, "git", "-C", cwd, "show-ref", "--verify", "--quiet", "refs/heads/"+branch).Run() == nil {
		return appwire.WorktreeProblemBranchExists
	}
	return ""
}
```

`app_rpc.go`, after the `MethodEvenerGitHead` handler:

```go
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerWorktreeCheck, hubWorktreeCheck)
```

`app_host_admin.go`, in `remoteHostAdminMethods` after `appwire.MethodEvenerGitHead: {},`:

```go
	// evener/worktree/check is read-only: it runs git in a remote working
	// directory to say whether a session could start there in a new worktree
	// branch (S18), so the New session sheet can name the Branch row's problem
	// for the host it will run on.
	appwire.MethodEvenerWorktreeCheck: {},
```

`app_host_admin_test.go`: in `TestHostAdminAllowListMatchesCatalog`'s `policy` after `"evener/git/head": true, …`:

```go
		"evener/worktree/check": true, // discovery: read-only new-branch check for a remote path (S18)
```

and in `TestHostAdminMutationClassificationMatchesAllowList`'s `readOnly` after `appwire.MethodEvenerGitHead: true,`: `appwire.MethodEvenerWorktreeCheck: true,`.

`app_rpc_test.go`, in `TestHubRPCRegistersExpectedHandlerSet`'s `expected` after `appwire.MethodEvenerGitHead,`: `appwire.MethodEvenerWorktreeCheck,`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files and `make generate`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub -run 'TestNewWorktreeProblem|TestHubRPCWorktreeCheck|TestHubRouterMatchesCatalog|TestHubRPCRegistersExpectedHandlerSet|TestHostAdmin' -count=1`, `go test ./appwire ./internal/appwirets -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add appwire/worktree.go appwire/errors.go appwire/protocol.go appwire/client.go cmd/evener-hub/app_worktree_check.go cmd/evener-hub/app_worktree_check_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_host_admin.go cmd/evener-hub/app_host_admin_test.go cmd/evener-hub/app_rpc_test.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): evener/worktree/check says whether a branch can start a session"
```

### Task 38.2: thread/start takes `worktreeBranch`

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire/types.go` (`ThreadStartParams.WorktreeBranch` after `LaunchOverrides`, `:2270`)
- Modify: `cmd/evener-hub/internal/hubcore/config.go` (`SpawnRequest.WorktreeBranch` after `Provider`, `:288`)
- Modify: `cmd/evener-hub/app_threadlifecycle.go` (the check after `cwd` is canonicalized, `:105`; the `SpawnRequest` literal, `:194-201`)
- Modify: `cmd/evener-hub/spawn.go` (`HubSpawner.Spawn`'s launch-check, `:209`; `buildSpawnArgs`, `:385`; `worktreeLaunchFlag` and `validateEvenerLaunchContract`, `:1020-1064`)
- Create: `cmd/evener-hub/app_threadstart_worktree_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: `newWorktreeProblem`, `appwire.WorktreeBranchRefused`, `worktreeCheckRepo` (Task 38.1); test helpers `fakeRPCSpawner` (`app_rpc_test.go:11642`), `writeFakeEvener`, `assertHubLaunchError`.
- Produces: `appwire.ThreadStartParams.WorktreeBranch string` (`worktreeBranch,omitempty`); `hubcore.SpawnRequest.WorktreeBranch string`; `const worktreeLaunchFlag = "worktree"`; `validateEvenerLaunchContract(ctx, evenerBinary, model string, env []string, flags ...string) error`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/app_threadstart_worktree_test.go`:

```go
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
	"primeradiant.com/evener/rendezvous"
)

// startWorktreeHub is a hub whose spawner records each spawn and starts
// nothing, with an initialized client.
func startWorktreeHub(t *testing.T) (*appwire.Client, *[]hubcore.SpawnRequest) {
	t.Helper()
	var spawns []hubcore.SpawnRequest
	spawner := &fakeRPCSpawner{
		spawn: func(_ context.Context, req hubcore.SpawnRequest) (rendezvous.Entry, error) {
			spawns = append(spawns, req)
			return rendezvous.Entry{PID: 108, Protocol: appwire.ProtocolVersion, SourceID: "local", ThreadID: "th_worktree", SessionID: "sess_worktree"}, nil
		},
	}
	hub := newHubRPCTestServer(t, hubcore.WebConfig{RunDir: t.TempDir(), Spawner: spawner, Past: hubcore.NewPastIndex("")})
	t.Cleanup(hub.Close)
	client := dialHubRPC(t, hub)
	t.Cleanup(func() { _ = client.Close() })
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return client, &spawns
}

// A start in a new worktree hands the branch to the spawner (S18).
func TestHubRPCThreadStartPassesTheWorktreeBranch(t *testing.T) {
	repo := worktreeCheckRepo(t)
	client, spawns := startWorktreeHub(t)
	if _, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{Model: "openai/gpt-5.2", CWD: repo, WorktreeBranch: "feature/login"}); err != nil {
		t.Fatalf("ThreadStart: %v", err)
	}
	if len(*spawns) != 1 || (*spawns)[0].WorktreeBranch != "feature/login" {
		t.Fatalf("spawns = %+v, want one carrying feature/login", *spawns)
	}
}

// A start whose branch the hub refuses never spawns, and its refusal names the
// problem for the New session sheet's Branch row (S18).
func TestHubRPCThreadStartRefusesAWorktreeBranchBeforeSpawning(t *testing.T) {
	repo := worktreeCheckRepo(t)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client, spawns := startWorktreeHub(t)
	for _, tc := range []struct {
		cwd, branch, problem, message string
	}{
		{repo, "taken", appwire.WorktreeProblemBranchExists, `worktreeBranch: branch "taken" already exists`},
		{repo, "../escape", appwire.WorktreeProblemInvalidName, `worktreeBranch: "../escape" can't be a branch name`},
		{outside, "feature/login", appwire.WorktreeProblemNotGitRepository, fmt.Sprintf("worktreeBranch: %s is not in a git repository", outside)},
	} {
		_, err := client.ThreadStart(context.Background(), appwire.ThreadStartParams{Model: "openai/gpt-5.2", CWD: tc.cwd, WorktreeBranch: tc.branch})
		var wire appwire.WireError
		// The Go client prefixes the method to the hub's message.
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams || wire.Message != "appwire thread/start: "+tc.message {
			t.Fatalf("%s: err = %v, want InvalidParams %q", tc.branch, err, tc.message)
		}
		raw, _ := json.Marshal(wire.Data)
		var data appwire.WorktreeBranchRefusedData
		if err := json.Unmarshal(raw, &data); err != nil || data.EvenerErrorInfo != appwire.ErrorWorktreeBranchRefused || data.Problem != tc.problem {
			t.Fatalf("%s: data = %s, want worktreeBranchRefused with problem %s", tc.branch, raw, tc.problem)
		}
	}
	if len(*spawns) != 0 {
		t.Fatalf("a refused start spawned: %+v", *spawns)
	}
}

// The spawn passes the branch as one --worktree=<branch> argument, and only
// for a start that asked for one.
func TestBuildSpawnArgsPassesTheWorktreeBranch(t *testing.T) {
	resolved := launchconfig.Resolved{Effective: launchconfig.Layer{Model: "openai/gpt-5"}}
	if args := buildSpawnArgs(hubcore.SpawnRequest{Resolved: resolved, WorktreeBranch: "feature/login"}); !slices.Contains(args, "--worktree=feature/login") {
		t.Fatalf("args = %v, want --worktree=feature/login", args)
	}
	for _, arg := range buildSpawnArgs(hubcore.SpawnRequest{Resolved: resolved}) {
		if strings.HasPrefix(arg, "--worktree") {
			t.Fatalf("a start with no branch passed %s", arg)
		}
	}
}

// A start in a new worktree needs a child binary that advertises --worktree;
// one that predates it still launches every other session.
func TestValidateEvenerLaunchContractRequiresTheFlagsALaunchPasses(t *testing.T) {
	old := filepath.Join(t.TempDir(), "old-evener")
	writeFakeEvener(t, old, fmt.Sprintf("#!/bin/sh\nprintf '%%s' '{\"protocol\":%q,\"launch_flags\":[\"api-log\"]}'\n", appwire.ProtocolVersion))
	if err := validateEvenerLaunchContract(context.Background(), old, "", nil); err != nil {
		t.Fatalf("a launch without a worktree: %v", err)
	}
	err := validateEvenerLaunchContract(context.Background(), old, "", nil, worktreeLaunchFlag)
	assertHubLaunchError(t, err)
	if !strings.Contains(err.Error(), "--worktree") {
		t.Fatalf("err = %v, want it to name --worktree", err)
	}
	current := filepath.Join(t.TempDir(), "evener")
	writeFakeEvener(t, current, fmt.Sprintf("#!/bin/sh\nprintf '%%s' '{\"protocol\":%q,\"launch_flags\":[\"api-log\",\"worktree\"]}'\n", appwire.ProtocolVersion))
	if err := validateEvenerLaunchContract(context.Background(), current, "", nil, worktreeLaunchFlag); err != nil {
		t.Fatalf("a binary that advertises --worktree: %v", err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestHubRPCThreadStartPassesTheWorktreeBranch|TestHubRPCThreadStartRefusesAWorktreeBranchBeforeSpawning|TestBuildSpawnArgsPassesTheWorktreeBranch|TestValidateEvenerLaunchContractRequiresTheFlagsALaunchPasses' -count=1`
Expected: FAIL to compile (`WorktreeBranch` is not a field of `ThreadStartParams` or `SpawnRequest`; `worktreeLaunchFlag` is undefined).

- [ ] **Step 3: Implement**

`appwire/types.go`, in `ThreadStartParams` after `LaunchOverrides`:

```go
	// WorktreeBranch starts the session in a new managed worktree on a new
	// branch of this name, cut from CWD's HEAD (S18). The worktree lives on
	// the host that runs the session, at <that host's state dir for the
	// project>/worktrees/<project id>/<branch>. Empty starts in CWD as it is.
	// A hub that predates it ignores it, so a controller checks a remote host
	// with evener/worktree/check before forwarding it.
	WorktreeBranch string `json:"worktreeBranch,omitempty"`
```

`hubcore/config.go`, in `SpawnRequest` after `Provider`:

```go
	// WorktreeBranch starts the session in a new managed worktree on a new
	// branch of this name (S18); the spawner passes it as serve's --worktree.
	WorktreeBranch string
```

`app_threadlifecycle.go`, after the block that canonicalizes `workingDir` (ending `workingDir = resolved` / `}`), before `var overrides launchconfig.Layer`:

```go
	// A start in a new worktree (S18) is checked here, before anything is
	// resolved or spawned, so a refused branch comes back as the Branch row's
	// reason rather than as a daemon that failed to launch. The daemon checks
	// again when it creates the lane.
	if branch := params.WorktreeBranch; branch != "" {
		if workingDir == "" {
			return appwire.ThreadStartResponse{}, appwire.InvalidParams("worktreeBranch: a new worktree needs cwd")
		}
		if problem := newWorktreeProblem(ctx, workingDir, branch); problem != "" {
			return appwire.ThreadStartResponse{}, appwire.WorktreeBranchRefused(problem, branch, workingDir)
		}
	}
```

and in the `cfg.Spawner.Spawn(ctx, hubcore.SpawnRequest{…})` literal, after `Provider: modelRef.Provider,`: `WorktreeBranch: params.WorktreeBranch,` (gofmt realigns the literal).

`spawn.go`: in `HubSpawner.Spawn`, the launch-check becomes

```go
	var launchFlags []string
	if req.WorktreeBranch != "" {
		launchFlags = append(launchFlags, worktreeLaunchFlag)
	}
	if err := validateEvenerLaunchContract(ctx, h.EvenerBinary, req.Resolved.Effective.Model, req.Env, launchFlags...); err != nil {
		return rendezvous.Entry{}, err
	}
```

In `buildSpawnArgs`, before `args = append(args, launchconfig.ToArgs(req.Resolved)...)`:

```go
	if req.WorktreeBranch != "" {
		// One argument, never a pair, so no value can be read as a flag of
		// its own; the name rule has already refused a leading "-" (S18).
		args = append(args, "--"+worktreeLaunchFlag+"="+req.WorktreeBranch)
	}
```

After `const requiredLaunchFlag = "api-log"`:

```go
// worktreeLaunchFlag is the serve flag a start in a new worktree passes (S18).
// Only such a start requires it, so a binary that predates it still launches
// every other session.
const worktreeLaunchFlag = "worktree"

// validateEvenerLaunchContract checks the child binary speaks this hub's
// protocol, accepts model, and advertises requiredLaunchFlag and every flag in
// flags, the optional serve flags this launch will pass.
```

`validateEvenerLaunchContract`'s signature gains `flags ...string`, and its last check becomes

```go
	for _, flag := range append([]string{requiredLaunchFlag}, flags...) {
		if !slices.Contains(resp.LaunchFlags, flag) {
			return appwire.HubLaunchError(fmt.Sprintf("evener launch-check did not advertise the --%s flag: upgrade the evener binary the hub spawns", flag))
		}
	}
	return nil
```

The resume path's call (`:279`) passes no flags and is unchanged. Run `$(go env GOROOT)/bin/gofmt -w` on the touched files and `make generate`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub -run 'TestHubRPCThreadStart|TestBuildSpawnArgs|TestValidateEvenerLaunchContract|TestHubSpawner' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add appwire/types.go cmd/evener-hub/internal/hubcore/config.go cmd/evener-hub/app_threadlifecycle.go cmd/evener-hub/spawn.go cmd/evener-hub/app_threadstart_worktree_test.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): thread/start can start a session in a new worktree branch"
```

### Task 38.3: a host is checked before a worktree start is forwarded

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/appsource/remote_hub_mutations.go` (`StartThread`, `:101-119`; `checkWorktreeStart` after it)
- Create: `cmd/evener-hub/internal/appsource/remote_hub_worktree_test.go`

**Interfaces:**
- Consumes: `appwire.MethodEvenerWorktreeCheck`, `WorktreeCheckParams`, `WorktreeCheckResponse`, `WorktreeBranchRefused` (Task 38.1), `ThreadStartParams.WorktreeBranch` (Task 38.2); `RemoteHubSource.call` (`remote_hub_source.go:228`); test helpers `newScriptedRemote`, `scriptedReply`, `lastMethodCall` (`remote_hub_source_test.go`).
- Produces: `func (s *RemoteHubSource) checkWorktreeStart(ctx context.Context, params appwire.ThreadStartParams) error`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/internal/appsource/remote_hub_worktree_test.go`:

```go
package appsource

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
)

// remoteStartMethods lists the methods a scripted host was asked after the
// connection's own initialize, in order.
func remoteStartMethods(calls []remoteCall) []string {
	var methods []string
	for _, call := range calls {
		if call.method != appwire.MethodInitialize {
			methods = append(methods, call.method)
		}
	}
	return methods
}

// A start in a new worktree on another host is checked with that host before
// it is forwarded (S18): a host that can start it gets the start with the
// branch; a host that names a problem refuses it as its own start would; and a
// host whose hub predates S18, which would ignore the branch and start on the
// checkout's current one, is refused before any session starts.
func TestRemoteHubStartThreadChecksAWorktreeStartWithTheHost(t *testing.T) {
	for _, tc := range []struct {
		name        string
		check       scriptedReply
		wantMethods []string
		wantCode    int
		wantInfo    appwire.ErrorInfo
	}{
		{"a host that can", scriptedReply{result: appwire.WorktreeCheckResponse{}}, []string{appwire.MethodEvenerWorktreeCheck, appwire.MethodThreadStart}, 0, ""},
		{"a host that names a problem", scriptedReply{result: appwire.WorktreeCheckResponse{Problem: appwire.WorktreeProblemBranchExists}}, []string{appwire.MethodEvenerWorktreeCheck}, appwire.CodeInvalidParams, appwire.ErrorWorktreeBranchRefused},
		{"a host that predates S18", scriptedReply{wireErr: new(appwire.MethodNotFound(appwire.MethodEvenerWorktreeCheck))}, []string{appwire.MethodEvenerWorktreeCheck}, appwire.CodeUnavailable, appwire.ErrorActionUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, calls := newScriptedRemote(t, "studio", func(method string, _ json.RawMessage) scriptedReply {
				if method == appwire.MethodEvenerWorktreeCheck {
					return tc.check
				}
				return scriptedReply{result: appwire.ThreadStartResponse{Thread: appwire.Thread{ID: "t1", Source: "local", Evener: appwire.EvenerThread{Ref: "local:t1"}}}}
			})
			_, err := source.StartThread(t.Context(), appwire.ThreadStartParams{CWD: "/work/evener", WorktreeBranch: "feature/login"})
			if got := remoteStartMethods(calls()); !slices.Equal(got, tc.wantMethods) {
				t.Fatalf("host was asked %v, want %v", got, tc.wantMethods)
			}
			if tc.wantCode == 0 {
				if err != nil {
					t.Fatalf("StartThread: %v", err)
				}
				var forwarded appwire.ThreadStartParams
				if err := json.Unmarshal(lastMethodCall(t, calls(), appwire.MethodThreadStart), &forwarded); err != nil || forwarded.WorktreeBranch != "feature/login" {
					t.Fatalf("forwarded start = %+v (%v), want the branch", forwarded, err)
				}
				return
			}
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != tc.wantCode {
				t.Fatalf("err = %v, want code %d", err, tc.wantCode)
			}
			raw, _ := json.Marshal(wire.Data)
			var data struct {
				EvenerErrorInfo appwire.ErrorInfo `json:"evenerErrorInfo"`
			}
			if err := json.Unmarshal(raw, &data); err != nil || data.EvenerErrorInfo != tc.wantInfo {
				t.Fatalf("err data = %s, want evenerErrorInfo %s", raw, tc.wantInfo)
			}
		})
	}
}

// A start with no worktree branch is forwarded as it always was, with no check.
func TestRemoteHubStartThreadWithoutAWorktreeAsksNothingElse(t *testing.T) {
	source, calls := newScriptedRemote(t, "studio", func(string, json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.ThreadStartResponse{Thread: appwire.Thread{ID: "t1", Source: "local", Evener: appwire.EvenerThread{Ref: "local:t1"}}}}
	})
	if _, err := source.StartThread(t.Context(), appwire.ThreadStartParams{CWD: "/work/evener"}); err != nil {
		t.Fatalf("StartThread: %v", err)
	}
	if got := remoteStartMethods(calls()); !slices.Equal(got, []string{appwire.MethodThreadStart}) {
		t.Fatalf("host was asked %v, want only thread/start", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/appsource -run 'TestRemoteHubStartThread' -count=1`
Expected: FAIL: every worktree case finds only `thread/start` asked (no check), and the "predates S18" and "names a problem" cases get no error.

- [ ] **Step 3: Implement**

`remote_hub_mutations.go`, in `StartThread` after `remote.Source = ""`:

```go
	if err := s.checkWorktreeStart(ctx, remote); err != nil {
		return appwire.ThreadStartResponse{}, err
	}
```

and after `StartThread`:

```go
// checkWorktreeStart asks the host whether it can start this session in a new
// worktree before the start is forwarded (S18). A host whose hub predates S18
// ignores WorktreeBranch and would start the session on its checkout's current
// branch, so its "method not found" for evener/worktree/check refuses the start
// instead; a host that names a problem refuses it as the host's own start
// would.
func (s *RemoteHubSource) checkWorktreeStart(ctx context.Context, params appwire.ThreadStartParams) error {
	if params.WorktreeBranch == "" {
		return nil
	}
	var check appwire.WorktreeCheckResponse
	err := s.call(ctx, appwire.MethodEvenerWorktreeCheck, appwire.WorktreeCheckParams{CWD: params.CWD, Branch: params.WorktreeBranch}, &check)
	if wire, ok := errors.AsType[appwire.WireError](err); ok && wire.Code == appwire.CodeMethodNotFound {
		return appwire.Unavailable(s.id + " runs an older Evener that can't start a session in a new worktree; update Evener on " + s.id)
	}
	if err != nil {
		return err
	}
	if check.Problem != "" {
		return appwire.WorktreeBranchRefused(check.Problem, params.WorktreeBranch, params.CWD)
	}
	return nil
}
```

`RemoteHubSource.mapCallError` keeps a MethodNotFound `WireError` as it is, which is what the check reads. Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./cmd/evener-hub/internal/appsource -count=1`, `go test ./cmd/evener-hub -run 'TestHubThreadStart|TestHubRPCThreadStart' -count=1`
Expected: PASS.

Gates: `go vet ./cmd/evener-hub/... ./appwire/`, the same with `-tags evenerfuzz` and with `GOOS=windows … -tags evenerfuzz`; `golangci-lint run ./cmd/evener-hub/ ./cmd/evener-hub/internal/appsource/ ./cmd/evener-hub/internal/hubcore/ ./appwire/`; `cd cmd/evener-hub/frontend && npm run typecheck` (the generated types changed); `git add` then `make lint-generated`.

- [ ] **Step 5: Commit and open PR 38**

```bash
git add cmd/evener-hub/internal/appsource/remote_hub_mutations.go cmd/evener-hub/internal/appsource/remote_hub_worktree_test.go
git commit -m "feat(hub): a worktree start on a host is checked with that host first"
```

Title "feat(hub): start a session in a new worktree branch from thread/start (S18b, phase 7 PR 38)". The body names the new field, method and error info (all additive), the three problems, the host check and why (ruling 9, question 4), the launch-check gate (ruling 11), and that the web's spawn sheet does not offer it yet.

---

## Self-review

- **Spec coverage.** S17: 7.2's "the model's display name when 'Show model on Board rows' is on" and 12's Display toggle: PR 36 (the toggle itself is the phone's). S18: 11's "Branch: current branch, or a new worktree branch (name field)" and "the field it concerns highlighted": PRs 37 and 38 (`worktreeBranchRefused` with its problem, and `evener/worktree/check` for the row before Start). Spec 18's fallbacks: every field and method is optional, and the phone lane handoff names the fallback each retires.
- **Dry run.** Every task was built on a scratch branch off `6b194e38b` (never pushed), in PR order, with each task's own tests run red first (a compile failure where the task adds the symbol) and green after, then: `go test ./cmd/evener-hub/... ./appwire/... ./hubapi/... ./cmd/evener/...`, `go test ./agent/ -run 'Worktree'`, `FuzzHubcoreScenarios`, `go vet` (plain, `evenerfuzz`, Windows) on every touched package, `golangci-lint` 2.13.1 on every touched package (0 issues), `make generate` and `make lint-generated`, the navigation codec's vitest suite and the frontend typecheck. The code blocks above are the dry run's files. On this macOS machine the full `cmd/evener` and `cmd/evener-hub/internal/hostfence` packages have failures that main has too (`TestServeResumeServesTheRestoredTranscript`, the `RetainsReferencedScratch` trio, `hostfence`'s script tests, and `agent`'s `TestRollbackFreshDelegateWorktreeUsesCarriedProjectMetadataDir`, each a `/var` against `/private/var` path: #2497's macOS class); everything else passed. (`hostfence` and its script tests were removed 2026-09-29 with the crash-fencing program; comp08 passes 1–2. The rest of this dry-run record is historical.)
- **Placeholders.** None: every task carries its code or names the exact edit and its anchor.
- **Names.** `CurrentModel` (probe, live entry, local daemon entry), `TreeNode.Model`, `modelFor`, `ModelName`/`model_name`, `navigationModelName`; `ValidateWorktreeName`, `StartInNewWorktree`, `validateStartupWorktree`, `WorktreeBranch`/`worktreeBranch`, `worktreeLaunchFlag`, `MethodEvenerWorktreeCheck`, `WorktreeCheckParams`, `WorktreeCheckResponse`, `WorktreeProblem*`, `newWorktreeProblem`, `hubWorktreeCheck`, `WorktreeBranchRefused`, `ErrorWorktreeBranchRefused`, `checkWorktreeStart`. Each is used the same way wherever it appears.
- **Review Focus.** Item 1: Tasks 37.1, 37.2, 38.1. Item 2: Tasks 38.2, 38.3. Item 3: Tasks 37.1, 37.2, 38.2. Item 4: Tasks 36.1, 36.3.
- **Issue filed with this plan.** #2939 (the web's spawn sheet adopts new-worktree launches).
