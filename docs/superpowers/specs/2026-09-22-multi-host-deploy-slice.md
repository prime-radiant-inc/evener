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
hub supplies it nothing to deploy from. `cmd/evener-hub/main.go:428` builds
`sshconn.Options` with `Logger` and `OnEvent` only — `BuildSource`,
`BuildBinary` and `HubAddr` are all unset — so `Manager.canBuild()`
(`manager.go:2444`) and `Manager.canDeploy()` (`manager.go:2459`) are false for
every real hub, and `ensureOnce` refuses a version difference terminally at
`manager.go:1581`. On a host with no `evener` at all, the same absence is why
the preflight's `launch-check` cannot even answer.

The deploy half is therefore dead code in production, and the fleet flow stops
exactly where a user needs it most: adding a second machine.

## What exists today

**The mechanism, specified.** Component 04 §4 "Deploy — `deploy.go`"
(`2026-09-14-multi-host-04-ssh-connection-manager.md:723`) and §5 "Version
auto-match + restart" (`:951`). The atomic push path cross-compiles the
controller's own tree for the host target and stamps the controller's build
identity in-process from `buildinfo` (`GitSHA`, `GitDirty`, `BuildTime`,
`Channel`) rather than re-deriving it from `git` and `date`, so the pushed
binary's `launch-check version` is exactly the controller's — its identity is
true by construction, not by a check.

**The ordering rule the ladder already follows** (`04:974-984`): the attach
branch is entered when either the on-disk `facts.Version` or the running hub's
`version` differs, and it **deploys only when the on-disk version differs** —
a restart left pending by an earlier attempt has already installed the build, so
re-cross-compiling on every reconnect would be a needless build.

**The mechanism, implemented.** `sshconn/deploy.go`: `verifyBuildSource`
(`:128`; its message is where *"an installed hub cannot locate its own source"*
comes from, `:130`), `localBuild` (the cross-compile, chosen from
`Options.BuildBinary`/`Options.BuildSource` at `:327`/`:329`), the atomic push
into a `mktemp` file with a byte-count check, `installerDirs` (`:651`) for the
fallback's `BINDIR`/`EVENER_SHARE_BINDIR`, and the post-install identity check.

**The seams, unset in production.** `Options.BuildBinary` (`manager.go:100`),
`Options.BuildSource` (`:110`), `canBuild()` (`:2444`), `canDeploy()`
(`:2459`).

**The state ladder already has the deploy leg.** `StateDisconnected`,
`StatePreflighting`, `StateDeploying`, `StateRestarting`, `StateAttaching`,
`StateAttached`, `StateReconnecting` (`manager.go:27-33`), and `StateDeploying`
is emitted immediately before the deploy (`manager.go:1509`). Nothing about
observability needs to be invented.

**The decision ladder** (`manager.go:1698-1707`): `deployPossible` is
`canDeploy()`; `devUnverified` is `isUnverifiableVersion(expected) &&
deployPossible && !isDevDeployed(name)`; the branch is `(deployNeeded &&
deployPossible) || devUnverified`. Read carefully, this means a **dev**
controller behaves in two different ways: with no deploy path it refuses (its
`expected` is `"dev"`, so a mismatch reaches the terminal refusal at `:1581`),
and with a deploy path it *forces* a deploy so that both sides end up running
the same unstamped build. This is a refinement of the parent spec's "dev builds
must not auto-match" (`04:764-772`), which was written before that path existed;
the code's version is the one that holds today.

**Two divergences this slice must settle, not paper over.**

- **The installer fallback's reference for a snapshot controller (decided, D4).**
  The parent spec says the fallback is *release-only* (`04:1064` calls it "a
  build the installer fallback can no longer produce, since it is release-only")
  and that a snapshot controller is **refused** rather than left holding a build
  from a commit the controller never intended (`04:783-800`, which rejects
  "run the installer first and discover the mismatch afterwards" in as many
  words). The code does the opposite: `installerRefFor` (`deploy.go:615-640`)
  returns the mutable `snapshot` tag for `channel == "snapshot"` (`:627`), with
  a terminal refusal once the tag moves past the controller's commit. Jesse
  ruled the code's behavior correct and the parent spec wrong, so **this slice
  corrects the parent spec** rather than the code; D4 states the amendment and
  what it has to be honest about.
- **The snapshot push path's identity check.** The parent spec is explicit that
  for a snapshot controller the push path is the surviving deploy path, and that
  a version-only health check cannot tell two snapshot builds apart, so
  `waitHealthy` must reject a missing or mismatched `backend_git_sha` — and it
  names that as a tracked follow-up, "not a present fact" (`04:1064-1094`).
  Today it is still not a fact.

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
  **failed verification** and never an attach (`04:818-828`). A wrong artifact
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
deploy path on the controller is the consent; the per-host `deploy = "auto" |
"never"` field is deferred to its own slice.

The reasoning, for the record: `canDeploy()` is today purely "is a path
configured" (`manager.go:2459`), so a configured path would deploy to every host
the operator connects. That is acceptable because the design doc already decided
auto-match on attach, because a source or binary is a deliberate act by the
operator who runs the controller, and because the blast radius is that
operator's own hosts over that operator's own credentials. The per-host field
remains worth having — it changes the config schema, `hostreg.Host`, the
`HostEntry` wire type, and the host edit dialog that just shipped, four surfaces
that each deserve their own review and none of which the mechanism needs.

### D3 — what the user sees

**Recommendation:** nothing new. The leg is already visible as `StateDeploying`
(`manager.go:1509`), a failure already lands in the row's `lastAttachError`
(which is how both refusals above reached the settings pane verbatim), and the
hub should log the leg — path used, source, host, target — with no secret and no
file contents in either place.

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

### D5 — the snapshot push path's identity check

If D4 goes my way, the push path becomes the *only* deploy path for a snapshot
controller, which makes the parent spec's `backend_git_sha` requirement
(`04:1064-1094`) load-bearing rather than a nicety: a version-only check cannot
tell two snapshot builds sharing a `version` apart.

**Recommendation, included in this slice unless you strike it:** implement it —
`waitHealthy` takes the expected Git SHA and treats a missing or mismatched
`backend_git_sha` as not yet healthy for a snapshot pin, exactly as the parent
spec describes. It is small, it closes a tracked follow-up (`04:1064-1094`
calls it out as "not a present fact"), and this slice is what makes snapshot
deploys reachable, so leaving it out would ship the wiring that needs it.

## Contract

- **Flags** (`evener hub`, alongside `-addr`, `-config`, `-evener`,
  `-appwire-trace`, defined in the `flag.NewFlagSet("evener hub", …)` at
  `main.go:769`):
  - `-deploy-binary <path>` — a pre-built `evener` for the host's target. Sets
    `Options.BuildBinary`. Validated where it is read; a bad value fails hub
    startup naming the flag, not the first attach.
  - `-build-source <path>` — an evener checkout's module root. Sets
    `Options.BuildSource`; `verifyBuildSource` keeps its existing refusals (a
    dirty tree, an ignored-but-compiled `.go` file, a path that is not an evener
    checkout).
- **Precedence** when more than one is available: `-deploy-binary`, then
  `-build-source`, then the installer fallback (D4/D5 permitting).
- **When a deploy happens** is unchanged from `04:974-984`: on an **on-disk**
  version difference, not on every reconnect, and a restart is re-verified
  before attach rather than re-deployed.
- **Identity.** The cross-compiled path's identity is true by construction. The
  operator-supplied path is verified on the host after the push, and the
  installer path keeps its existing post-install check; in every case a build
  that cannot be confirmed is a **failed verification** and never an attach
  (`04:818-828`).
- **Refusals** keep their existing types (`ErrVersionMismatch`, `ErrDeploy`,
  `ErrRestart`) and gain actionable messages: the terminal version refusal names
  `-deploy-binary` and `-build-source`, and says *why* no path is available when
  the controller is dev, dirty, or snapshot. `sshconn` is a library and must not
  learn the CLI's flag names, so the names arrive through `Options` — one field
  holding the help text the hub fills in, empty when the embedder has no flags
  to name, with the manager's own sentence as the default. Tests assert both the
  default and the wired form.
- **Deliverables beyond the code**: the component 04 spec correction (D4), the
  `docs/evener-hub.md` deploy section (what to set, what gets written where,
  what is refused), and this slice's own spec kept true to what lands.
- **No new state, no new wire type, no frontend change.** `StateDeploying`
  already exists; the row's `lastAttachError` already carries the reason.

## Non-scope

- The per-host `deploy` field and its four surfaces (D2, deferred by
  recommendation).
- Any new deploy mechanism: no download-by-URL, no new artifact reference, no
  change to the push path's stamping.
- Restart deferral while clients are attached (`04` §Open questions):
  unchanged, and this slice must not pick a default silently.
- The host-list RPC and multi-hop cycle detection (design §2, §6).
- `installerDirs`' acceptance of an `evener-dev` basename (`deploy.go:663`):
  tracked, not here (it is about *where* the installer writes, and no wiring
  decision depends on it).

## Testing

- **Unit, no SSH** — the `Runner` seam and `Options.BuildBinary` exist so the
  argv is assertable: precedence (binary beats source beats installer), which
  flag each refusal names, and the channel rules of D4/D5 driving both paths.
  `sshconn`'s existing tests for `verifyBuildSource`, `localBuild`, the push and
  `installerDirs` stay as they are.
- **The operator-artifact verification** — a pushed `-deploy-binary` that
  reports the wrong version on the host is a failed verification, not an attach,
  and the failure reaches the row.
- **Flag validation** — a bad `-deploy-binary`/`-build-source` fails startup
  naming the flag; a hub with neither still starts (a local-only controller must
  not require a deploy path).
- **Gated live, one opt-in, never in `make test`** — extend the shape already
  shipped by `cmd/evener-hub/app_host_e2e_test.go` (same env gate, same private
  loopback hub, same skip discipline). Its host contract grows from "already
  carries a matching build" to "is disposable and may be written to", and the
  test must cover the case that made this slice necessary: a host with **no**
  `evener` on it ends up running the controller's exact build and attaches.

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
5. A dev controller with no deploy path refuses with a message saying the
   controller build carries no identity to deploy; a dev controller **with** a
   deploy path forces the deploy (the `devUnverified` rule at
   `manager.go:1699-1706`) and the host ends up on the same unstamped build.
   Both halves are asserted.
6. A dirty controller refuses the push path and the installer fallback, each
   message naming the remedy.
7. Per D4: the amended snapshot rule is the asserted one — the installer path is
   admitted for a snapshot controller, the post-install identity check is what
   confirms the build, and a controller whose commit the tag has moved past is
   refused terminally with a message naming the push path. Per D5, a snapshot
   deploy is confirmed by `backend_git_sha`, not by `version` alone. Component
   04's spec and the code state the same rule afterwards.
8. A pushed artifact whose on-host identity does not match is a failed
   verification and never an attach.
9. `make test` and `go test ./...` stay free of SSH, free of any Go-toolchain
   requirement for the deploy tests, and free of any write to a real host; the
   gated test still skips with a message naming its variables.
10. The hub logs the deploy leg (path used, source, host, target) and never logs
    a credential or a file's contents.

## Open questions

- **Environment fallbacks.** The hub has none today and a supervisor can pass
  flags, so this spec follows that style; `EVENER_DEPLOY_BINARY` /
  `EVENER_BUILD_SOURCE` would be a small addition with a test, not a redesign.
- **Where the operator is told.** `docs/evener-hub.md` should gain the deploy
  requirement in this slice: what to set, what gets written where, what is
  refused. Whether the host pane should *say* "this hub has no deploy path"
  before a user hits the refusal is a UI question for another slice; today the
  row's `lastAttachError` carries it.
- **The deferred per-host field.** If D2 goes the other way, size the four
  surfaces before writing that spec.
