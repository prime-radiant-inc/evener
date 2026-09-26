# Host deploy slice — wiring component 04's deploy half

Status: draft for review. This spec adds no new deploy mechanism: component 04
specifies the mechanism and `cmd/evener-hub/internal/sshconn/deploy.go`
implements it. This slice makes it reachable from a real hub, settles the rules
where the code and the parent spec disagree, and pins the result with tests.

## Purpose

The design doc's decision is already made (§2 "Deployment"): *"push a matching
binary over SSH and/or run the installer. The controller is the version
authority: on attach it auto-matches the host to its own build, restarting the
host hub; running sessions keep their old binary."*

A live end-to-end run of this repository's `main` cannot do any of it. Two
refusals, both real, both from that run:

```
evener executable not found on the host: host "paradise-park" launch-check:
zsh:1: command not found: evener

host version does not match the controller: host "paradise-park" runs version
"f49c8076c6", want "f49c8076c6-dirty", and no build source is configured to
deploy the controller's build; set Options.BuildSource
```

The second message is the whole story: the machinery exists and the production
hub supplies it nothing to deploy from. `cmd/evener-hub/main.go:454` builds
`sshconn.Options` with `Logger` and `OnEvent` only — `BuildSource`,
`BuildBinary` and `HubAddr` are all unset — so `Manager.canBuild()`
(`manager.go:2545`) and `Manager.canDeploy()` (`manager.go:2560`) are false for
every real hub, and `ensureOnce` refuses a version difference terminally at
`manager.go:1603`. On a host with no `evener` at all, the same absence is why
the preflight's `launch-check` cannot even answer.

*(Read the last sentence as the state this slice was written against. The version
refusal at `manager.go:1603` was withdrawn on 2026-09-22: a protocol-compatible
host attaches whatever build it runs, so that line no longer exists. The deploy
gap this section describes is what the slice closed, and it still stands.)*

The deploy half is therefore dead code in production, and the fleet flow stops
exactly where a user needs it most: adding a second machine.

## What exists today

**The mechanism, specified.** Component 04 §4 "Deploy — `deploy.go`"
(`2026-09-14-multi-host-04-ssh-connection-manager.md:723`) and §5 "Version
auto-match + restart" (`:965`). The atomic push path cross-compiles the
controller's own tree for the host target and stamps the controller's build
identity in-process from `buildinfo` (`GitSHA`, `GitDirty`, `BuildTime`,
`Channel`) rather than re-deriving it from `git` and `date`, so the pushed
binary's `launch-check version` is exactly the controller's — its identity is
true by construction, not by a check.

**The ordering rule the ladder already follows** (`04:991-995`): the attach
branch is entered when either the on-disk `facts.Version` or the running hub's
`version` differs, and it **deploys only when the on-disk version differs** —
a restart left pending by an earlier attempt has already installed the build, so
re-cross-compiling on every reconnect would be a needless build.

**The mechanism, implemented.** `sshconn/deploy.go`: `verifyBuildSource`
(`:128`; its message is where *"an installed hub cannot locate its own source"*
comes from, `:130`), `localBuild` (the cross-compile, chosen from
`Options.BuildBinary`/`Options.BuildSource` at `:349`/`:351`), the atomic push
into a `mktemp` file with a byte-count check, `installerDirs` (`:743`) for the
fallback's `BINDIR`/`EVENER_SHARE_BINDIR`, and the post-install identity check.

**The seams, unset in production.** `Options.BuildBinary` (`manager.go:100`),
`Options.BuildSource` (`:110`), `canBuild()` (`:2545`), `canDeploy()`
(`:2560`).

**The state ladder already has the deploy leg.** `StateDisconnected`,
`StatePreflighting`, `StateDeploying`, `StateRestarting`, `StateAttaching`,
`StateAttached`, `StateReconnecting` (`manager.go:27-33`), and `StateDeploying`
is emitted immediately before the deploy (`manager.go:1518`). Nothing about
observability needs to be invented.

**The decision ladder** (`manager.go:1738-1746`): `deployPossible` is
`canDeploy()`; `devUnverified` is `isUnverifiableVersion(expected) &&
deployPossible && !isDevDeployed(name)`; the branch is `(deployNeeded &&
deployPossible) || devUnverified`. Read carefully, this means a **dev**
controller behaves in two different ways: with no deploy path it attaches — a
build version is not an attach gate (`04` §5), and a `"dev"` controller speaks
the same protocol as the host — and with a deploy path it *forces* a deploy so
that both sides end up running the same unstamped build. This is a refinement of
the parent spec's "dev builds must not auto-match" (`04:752-760`), which was
written before that path existed; the code's version is the one that holds today.

**Two divergences this slice must settle, not paper over.**

- **The installer fallback's reference for a snapshot controller (decided, D4).**
  The parent spec used to say the fallback is *release-only*: it called such a
  build "a build the installer fallback can no longer produce, since it is
  release-only" (`04:1066` before this slice corrected the passage; the
  corrected text is now `04:1079-1081`) and **refused** a snapshot controller
  rather than leave it holding a build from a commit the controller never
  intended (`04:794-803`, the snapshot arm; the pre-correction objection that
  running the installer first and discovering the mismatch afterwards is not
  acceptable is recorded at `04:805-819`). The code does the opposite:
  `installerRefFor` (`deploy.go:705`)
  returns the mutable `snapshot` tag for `channel == "snapshot"` (`:717`), with
  a terminal refusal once the tag moves past the controller's commit. Jesse
  ruled the code's behavior correct and the parent spec wrong, so **this slice
  corrects the parent spec** rather than the code; D4 states the amendment and
  what it has to be honest about.
- **The snapshot push path's identity check.** The parent spec is explicit that
  for a snapshot controller the push path is the surviving deploy path, and that
  a version-only health check cannot tell two snapshot builds apart, so
  `waitHealthy` must reject a missing or mismatched `backend_git_sha` — and it
  names that as a tracked follow-up, "not a present fact" (`04:1096-1107`).
  This slice settles it (D5).

## Decisions this slice needs (yours)

### D1 — where a hub that was not started from a checkout gets a binary

The push path cross-compiles from a **source tree**, which an installed hub does
not have (`verifyBuildSource` says so itself). Three ways to close that; they
are not exclusive:

- **`-deploy-binary <path>` (operator-supplied artifact).** The operator gives
  the hub a pre-built `evener` for the host's target. No Go toolchain, no source
  tree, no host network. It is the one path whose identity is **not** true by
  construction, so this slice must verify it where that is possible: on the host,
  after the push, through the existing `launch-check` probe, with a mismatch a
  **failed verification** and never an attach (`04:826-831`). A wrong artifact
  costs a wasted push and a refused attach.
- **`-build-source <path>` (checkout).** The operator points the hub at an
  evener checkout; the hub cross-compiles the host's target from it with the
  buildinfo stamping component 04 already specifies. Needs Go and the source on
  the controller.
- **Installer fallback** (host network required), subject to D4.

**Recommendation:** wire both seams, with precedence `-deploy-binary` then
`-build-source` then the installer fallback. A hub with none of the three keeps
today's terminal refusal, and the message names both flags instead of
`Options.BuildSource` (an internal name the operator cannot act on). No
download-by-URL path in this slice: the parent spec's own analysis is that a
snapshot controller has no immutable reference to fetch by, and inventing one
here would relitigate §4.

### D2 — what consents to a write on a host (decided: controller-level only)

Jesse ruled: this slice lands **controller-level behavior only**. Configuring a
deploy path on the controller is the consent, and that rule **stands**: a host
cannot opt out of the controller replacing its binary.

The reasoning, for the record: `canDeploy()` is today purely "is a path
configured" (`manager.go:2560`), so a configured path would deploy to every host
the operator connects. That is acceptable because the design doc already decided
auto-match on attach, because a source or binary is a deliberate act by the
operator who runs the controller, and because the blast radius is that
operator's own hosts over that operator's own credentials. The per-host field
is therefore **decided against, not deferred**: no per-host `deploy = "auto" |
"never"` field, and none of its four surfaces (the config schema,
`hostreg.Host`, the `HostEntry` wire type, the host edit dialog), is added. Do
not implement it without a new decision.

### D3 — what the user sees

**Recommendation:** nothing new. The leg is already visible as `StateDeploying`
(`manager.go:1518`), a failure already lands in the row's `lastAttachError`
(which is how both refusals above reached the settings pane verbatim), and the
hub should log the leg — path used, source, host, target — with no secret and no
file contents in either place. As landed there is exactly one deploy-log line:
the startup line naming the path the hub was given (criterion 10, Evidence); the
per-attach leg itself (host, target) is not logged.

### D4 — the installer fallback for a snapshot controller (decided: amend the parent spec)

Jesse ruled the **code's** behavior correct and the parent spec's rule wrong.
So this slice does not change `installerRefFor`; it **corrects component 04's
spec** to state the rule the code implements:

- release channel: the stamped `ReleaseTag` (a Git SHA is never a substitute),
  and a release build carrying no stamped tag is refused (`ErrDeploy`) rather
  than passed a SHA;
- snapshot channel: the mutable `snapshot` tag, accepted **as a best-effort
  pin**, verified after the install by the same on-host identity check, and
  refused **terminally** once the tag no longer points at this controller's
  commit — instead of re-fetching the same artifact forever;
- dev/dirty: refused (`ErrDeploy`), as today;
- `latest` is never passed.

The amendment must be honest about the trade it accepts, because the spec's
previous text rejected exactly this and that objection does not evaporate by
being overruled. What the accepted trade means in practice: for a snapshot
controller, the installer path writes a binary whose commit is **not provable
from the artifact reference**, and the proof arrives only afterwards. So the
failure mode the old text named is real and remains: after the tag moves past
the controller's commit, the install has already replaced the host's binary, and
the deploy reports failure while the host holds a snapshot build from an unknown
(possibly newer) commit. What makes it acceptable is the remedy and its cost:
the push path — now actually available, per D1 — is the path that carries a
provable identity, and the operator is told so by the terminal refusal's
message. The amendment states that, instead of claiming an immutability the tag
does not have.

**Re-examined, and held.** After this ruling was recorded, the master design
doc's tracked-follow-up ledger turned out to carry the *opposite* decision as
unimplemented work: "[04] installer fallback is release-only (round 22)", which
would have `installerRefFor` lose its `snapshot` arm (`design.md:670`, whose
entry is now titled "[04] installer fallback's reference (round 22; reconsidered
and reversed 2026-09-22)"). That was
put back to Jesse, since the earlier question had not mentioned it, and he held
the ruling. The ledger entry now records the reversal and keeps its superseded
reasoning as the record (`5ddf8efd94`), so the code, component 04, and the master
doc all state the same rule.

### D5 — the snapshot push path's identity check

If D4 goes my way, the push path becomes the *only* deploy path for a snapshot
controller, which makes the parent spec's `backend_git_sha` requirement
(`04:1096-1107`) load-bearing rather than a nicety: a version-only check cannot
tell two snapshot builds sharing a `version` apart.

**Recommendation, included in this slice unless you strike it:** implement it —
`waitHealthy` takes the expected Git SHA and treats a missing or mismatched
`backend_git_sha` as not yet healthy for a snapshot pin, exactly as the parent
spec describes. It is small, it closes a tracked follow-up (`04:1096-1107`
calls it out as "not a present fact"), and this slice is what makes snapshot
deploys reachable, so leaving it out would ship the wiring that needs it.

**Decided and landed** (`320de73215`): the check is in, the hub really does
publish the field (`web_api.go:87`), and the three restart call sites pass the
pin. The cold-bootstrap start path passes it too (`13099d0fba`): the binary it
launches is the one already verified on disk, but the version-equality rule that
accepted it cannot tell two snapshot builds apart, so a start on a stopped host
would otherwise attach to a commit this controller did not install. What the
start path deliberately lacks is a predecessor identity to compare — nothing was
running to confuse the started hub with — not the pin. `snapshot_pin_test.go`
pins the channel source, the wait, the restart wiring, and the start wiring.

## Contract

- **Flags** (`evener hub`, alongside `-addr`, `-config`, `-evener`,
  `-appwire-trace`, defined in the `flag.NewFlagSet("evener hub", …)` at
  `main.go:798`):
  - `-deploy-binary <path>` — a pre-built `evener` for the host's target. Sets
    `Options.BuildBinary`. Validated where it is read; a bad value fails hub
    startup naming the flag, not the first attach.
  - `-build-source <path>` — an evener checkout's module root. Sets
    `Options.BuildSource`; `verifyBuildSource` keeps its existing refusals (a
    dirty tree, an ignored-but-compiled `.go` file, a path that is not an evener
    checkout).
- **Precedence** when more than one is available: `-deploy-binary`, then
  `-build-source`, then the installer fallback (D4/D5 permitting).
- **When a deploy happens** is unchanged from `04:1006-1016`: on an **on-disk**
  version difference **and only when a deploy path is configured** — with none,
  the host keeps its build and attaches — not on every reconnect, and a restart
  is re-verified before attach rather than re-deployed.
- **Identity.** The cross-compiled path's identity is true by construction. The
  operator-supplied path is verified on the host after the push, and the
  installer path keeps its existing post-install check; in every case a build
  that cannot be confirmed is a **failed verification** and never an attach
  (`04:826-831`).
- **Refusals** keep their existing types and gain actionable messages. The
  refusals that name `-deploy-binary` and `-build-source` are the ones with
  nothing to install: the missing-executable preflight refusal and the installer
  fallbacks. None says *why* no path is available (dev/dirty/snapshot): that
  clause was in an earlier draft of this Contract and is not in the shipped
  messages, which say only that no build source is configured to deploy the
  controller's build. (The terminal *version* refusal that also carried this
  remedy was withdrawn on 2026-09-22 — a protocol-compatible host attaches
  whatever build it runs; criterion 5, Evidence.) A deploy
  that used a configured path but left the host on another build is a separate
  terminal refusal, `ErrDeployUnstamped` (criterion 8). `sshconn` is a library
  and must not learn the CLI's flag names, so the names arrive through `Options`
  — one field holding the help text the hub fills in, empty when the embedder
  has no flags to name, with the manager's own sentence as the default. Tests
  assert the default, the wired form, and that `main.go` passes it.
- **Deliverables beyond the code**: the component 04 spec correction (D4), the
  `docs/evener-hub.md` deploy section (what to set, what gets written where,
  what is refused), and this slice's own spec kept true to what lands.
- **No new state, no new wire type, no frontend change.** `StateDeploying`
  already exists; the row's `lastAttachError` already carries the reason.

## Non-scope

- The per-host `deploy` field and its four surfaces (D2: decided against — the
  controller-level rule stands).
- Any new deploy mechanism: no download-by-URL, no new artifact reference, no
  change to the push path's stamping.
- Restart deferral while clients are attached (`04` §Open questions):
  unchanged, and this slice must not pick a default silently.
- The host-list RPC and multi-hop cycle detection (design §2, §6).

## D6 — the master doc's other decided deploy items (found late, landed here)

While implementing, the master design doc's tracked-follow-up ledger turned out
to carry two further **decided but unimplemented** items in this same deploy
half. Both were done here rather than left as "tracked", because this is the
slice that owns them and both are small.

- **Run target must be `evener`** (`design.md:651`, round 22).
  `installableEvenerBasename` accepted `evener-dev`, the development/test tooling
  binary — no `hub` subcommand and no `launch-check` — so a host configured with
  it *installed and then failed* preflight, health and restart, after the
  controller had written to it. It is now narrowed to `evener`, and anything
  else is refused by `checkRunTarget` (`deploy.go:421`) **before any probe,
  push or install** (`84eb525e70`); the ordering is pinned by a test whose
  runner fails the test if any remote command runs at all.
- **That refusal is terminal, not retryable** (`1371f0957a`). The first cut
  returned `ErrDeploy`, which this manager retries by design — the same mistake
  the dirty-controller refusal's own comment records from round thirteen ("the
  supervisor retried it forever and the cause was discarded"). It now has its
  own sentinel, `errRunTargetUnservable`, classified terminal by `isTerminal`,
  exported as `ErrRunTargetUnservable` and surfaced by the attach handler as the
  same typed launch error the dirty-controller refusal produces. The master
  doc's entry was corrected to name it and say why terminal is right, since its
  letter had said `ErrDeploy`.

`installerDirs` does **not** stay out of that rule: it calls the same
`checkRunTarget` (`deploy.go:754`) before it derives BINDIR, so a configured
`evener_path` naming `evener-dev` — or any basename other than `evener` — is
refused terminally before the installer runs, exactly as the push path refuses
it. `install.sh` still ships an `evener-dev` file (`install.sh:5`), but that is
about *what the installer installs*, not where a host hub may be run from. So
`installerDirs` does not need to understand the `evener-dev` name itself:
`checkRunTarget` owns that rule for both deploy paths, and `installerDirs`'
default case (no `evener_path`) always resolves to the installer's own
`~/.local/bin/evener`.

## Testing

- **Unit, no SSH** — the `Runner` seam and `Options.BuildBinary` exist so the
  argv is assertable: precedence (binary beats source beats installer), which
  flag each refusal names, and the channel rules of D4/D5 driving both paths.
  `sshconn`'s existing tests for `verifyBuildSource`, `localBuild`, the push and
  `installerDirs` stay as they are.
- **The operator-artifact verification** — a pushed `-deploy-binary` that
  reports the wrong version on the host is a failed verification, not an attach,
  and the failure reaches the row. As landed the push path does not probe the
  host itself; `ensureOnce` judges the launch contract it re-reads after the
  deploy (and after a restart), so a deploy that left another build is refused
  terminally there, before any attach (Evidence, criterion 8).
- **Flag validation** — a bad `-deploy-binary`/`-build-source` fails startup
  naming the flag; a hub with neither still starts (a local-only controller must
  not require a deploy path).
- **Gated live, one opt-in, never in `make test`** — extend the shape already
  shipped by `cmd/evener-hub/app_host_e2e_test.go` (same env gate, same private
  loopback hub, same skip discipline). Its host contract grows from "already
  carries a matching build" to "is disposable and may be written to", and the
  test must cover the case that made this slice necessary: a host with **no**
  `evener` on it ends up running the controller's exact build and attaches.
- **The deploy check is gated separately, because it writes.** As shipped
  (`75e8fe934f`), `app_host_deploy_e2e_test.go` needs `EVENER_SSH_E2E_DEPLOY=1`
  in addition to the existing variables, skips with a message that says the host
  will be written to, and writes only inside a directory it creates on the host
  for the purpose — proving, in a deferred check, that whatever `evener` the
  host already had is byte-identical afterwards. It runs both flags: the
  cross-compile path (`-build-source`) and the operator-artifact path
  (`-deploy-binary`).

## Acceptance criteria

1. `-deploy-binary` set to a stamped `evener` for the host's target: a
   disposable host with **no** `evener` attaches, and the binary left on it
   reports the controller's `buildinfo.Version()` from `launch-check`.
2. `-build-source` set and no `-deploy-binary`: the same host attaches the same
   way with the binary cross-compiled on the controller.
3. With both set, the binary path is used — asserted on the argv the fake runner
   records, not inferred.
4. With neither set, behavior is today's, except the refusal names
   `-deploy-binary` and `-build-source`.
5. A dev controller with no deploy path attaches: the host answers the
   controller's `launch-check`, so the protocol matches, and a build version is
   not an attach gate (`04` §5). A dev controller **with** a deploy path forces
   the deploy (the `devUnverified` rule at `manager.go:1738-1746`) and the host
   ends up on the same unstamped build. Both are asserted. **Superseded
   2026-09-22:** this criterion previously required the no-deploy-path case to
   refuse terminally. That refusal was withdrawn because it refused working,
   protocol-compatible hosts and named a remedy the operator did not need; the
   difference is reported instead (Evidence, criterion 5).
6. A dirty controller refuses the push path and the installer fallback, each
   message naming the remedy.
7. Per D4: the amended snapshot rule is the asserted one — the installer path is
   admitted for a snapshot controller, the post-install identity check is what
   confirms the build, and a controller whose commit the tag has moved past is
   refused terminally with a message naming the push path. Per D5, a snapshot
   deploy is confirmed by `backend_git_sha`, not by `version` alone. Component
   04's spec and the code state the same rule afterwards.
8. A pushed artifact whose on-host identity does not match is a failed
   verification and never an attach: after the deploy (or the restart onto a
   deployed build) the controller judges the launch contract it re-read, and a
   remaining difference is the terminal `ErrDeployUnstamped` refusal
   (Evidence, criterion 8).
9. `make test` and `go test ./...` stay free of SSH, free of any Go-toolchain
   requirement for the deploy tests, and free of any write to a real host; the
   gated test still skips with a message naming its variables.
10. The hub logs, at startup, which deploy path it was started with: the
    `-deploy-binary` / `-build-source` value, and which one wins when both are
    set. That line is the whole deploy-leg log — no host, no target, no file's
    contents, no credential. Nothing else about a deploy leg is logged, and the
    hub's unrelated startup lines are unchanged.

## Evidence

What pins each criterion, and what does not. A test named here is a unit test in
`cmd/evener-hub` or `cmd/evener-hub/internal/sshconn`, unless it is called the
gated live check. **unproven** names missing behavior, not merely missing
coverage.

1. **Pinned, gated live** — `TestHostDeployNoEvenerE2E`
   (`cmd/evener-hub/app_host_deploy_e2e_test.go`), the `-deploy-binary` case:
   the host ends the run on the controller's own `launch-check` version. Runs
   only under `EVENER_SSH_E2E_DEPLOY=1`, so the default suite exercises it as a
   skip.
2. **Pinned, gated live** — the same test's `-build-source` case.
3. **Pinned** — `TestDeployWiringPrefersDeployBinary` (the hub's flag precedence)
   and `TestDeployPrefersTheOperatorArtifactOverTheBuildSource` (the manager's
   dispatch, asserted on the argv the fake runner records and the bytes the push
   streams).
4. **Pinned** — `TestHubDeployHelpNamesBothFlags`,
   `TestDeployWiringWithNeitherSetsHelpOnly`, `TestRunMainWithoutDeployFlagsStarts`,
   and `TestPreflightMissingExecutableCarriesTheDeployRemedy` (the one remaining
   sshconn refusal that carries the remedy): no build seam is installed, the hub
   still starts, and each remaining refusal names both flags. The seam is pinned
   on all three sides: the hub's help text, the sshconn refusal, and the line that
   passes the former as `Options.DeployHelp` (`main.go`) —
   `TestRunMainPassesDeployHelpToTheSSHManager` captures the `sshconn.Options` the
   hub hands the manager through the deps seam, so removing that wiring fails.
5. **Pinned.** The attach: `TestDevControllerWithoutADeployPathAttaches` (a dev
   controller with no deploy path attaches to a host running another build),
   with `TestEnsureAttachesToAnotherBuildWhenProtocolMatches` (a stamped
   controller against a host on another build, the skew reported) and
   `TestFirstAttachBootstrapsStoppedHostOnAnotherBuild` (the same host stopped:
   the bootstrap start is judged against the build the host runs). The forced
   deploy: `TestDevControllerWithADeployPathForcesTheDeploy` (the decision, and
   the build the decision produces). **Superseded 2026-09-22:** this row pinned
   `TestDevControllerWithoutADeployPathRefuses` until the refusal was withdrawn
   (criterion 5); the identity wording it noted as unclaimed stays unclaimed.
6. **Pinned** — `TestDirtyControllerRefusalsNameTheRemedy` (both refusals and
   each remedy clause), with `TestRound13DirtyControllerDeployRefusalIsTerminal`
   pinning the push refusal's type and terminality.
7. **Pinned** — `TestRound8InstallerVersionMismatchIsTerminal` (a moved tag is
   refused terminally, not retried) and
   `TestInstallerMovedTagRefusalNamesThePushPath` (that refusal names the
   remedy); `TestInstallerRefusalsNameSuppliedDeployHelp` and
   `TestInstallerRefusalsKeepTheLibraryRemedy` pin the remedy seam;
   `TestEnsureInstallerFallbackDeploysPinnedRelease` and
   `TestInstallerFallbackRecordsDefaultRunTarget` pin the release/snapshot
   admission; `TestSnapshotPinGitSHASourcesFromBuildChannel`,
   `TestWaitHealthySnapshotPin`, `TestRestartHubPinsSnapshotBuildFromBuildChannel`
   and `TestFirstAttachBootstrapPinsSnapshotBuild` (the cold-bootstrap start
   path) pin the `backend_git_sha` identity. Component 04 and the code state the
   same rule.
8. **Pinned** — `TestEnsurePostDeployBuildMismatchRefusesTerminally`
   (`cmd/evener-hub/internal/sshconn/deploy_test.go`), three cases. The
   running-host case is the hole this closed: a live hub unit answers the
   restart's health probe with its own build, so the restart reads as successful
   while the binary the controller addressed reports another one — before the
   gate, `Ensure` returned nil and the bridge started. The stopped-host case pins
   that the refusal fires before the first-attach bootstrap starts anything. Both
   assert the terminal `errDeployUnstamped`, that the error is not `ErrDeploy`,
   and that no bridge was started; the third case pins that a deploy whose fresh
   facts match still attaches. `TestHostAttachTerminalFailuresAreTypedWireErrors`
   pins the hub-side mapping of `ErrDeployUnstamped` to `HubLaunchError`. The
   gate is the refreshed-facts version comparison in `ensureOnce`
   (`manager.go`), judged only where a deploy ran. The installer path
   keeps its own post-install check (`deployInstaller`'s `probeLaunchCheck`). A
   pass that merely started or restarted the hub is not judged here: it launched
   the build already on disk, which the host is allowed to keep (2026-09-22).
9. **Pinned** — `TestHostDeployNoEvenerE2E` skips with a message naming
   `EVENER_SSH_E2E_DEPLOY=1`, `EVENER_SSH_E2E=1` and `EVENER_SSH_E2E_HOST`, so
   the default `go test` runs no ssh and writes to no host; the deploy unit
   tests need no Go toolchain (`Options.BuildBinary` is their seam, and the
   tests that drive the production builder put a `go` shim on `PATH`).
10. **Pinned** — `TestRunMainLogsTheDeployPathItWasGiven`: the startup line
    names the deploy path and, with both flags, the winner; no host, target,
    file contents, or credential is logged.

## Open questions

- **Environment fallbacks.** The hub has none today and a supervisor can pass
  flags, so this spec follows that style; `EVENER_DEPLOY_BINARY` /
  `EVENER_BUILD_SOURCE` would be a small addition with a test, not a redesign.
- **Where the operator is told.** `docs/evener-hub.md` should gain the deploy
  requirement in this slice: what to set, what gets written where, what is
  refused. Whether the host pane should *say* "this hub has no deploy path"
  before a user hits the refusal is a UI question for another slice; today the
  row's `lastAttachError` carries it.
- **The per-host field (resolved).** D2 decides against it: controller-level
  configuration is the consent, and a host cannot opt out of the controller
  replacing its binary.
