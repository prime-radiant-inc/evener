# Tech-debt audit: hub-sshconn

Slice: `cmd/evener-hub/internal/sshconn` (non-test). Audited at origin/main in
`.worktrees/debt-audit`. Read-only; no tests were run. Dead-code claims come from a
name-based reachability pass over the non-test files (every exported name and every
package-level var/const treated as a root), then confirmed by hand with grep against
non-test callers inside and outside the package.

## Inventory

| File | Code lines | Comment lines |
| --- | ---: | ---: |
| manager.go | 1992 | 1773 |
| version.go | 1098 | 591 |
| deploy.go | 832 | 487 |
| ownership.go | 623 | 67 |
| selected_restart.go | 434 | 36 |
| ownership_darwin_remote.go | 425 | 18 |
| preflight.go | 361 | 180 |
| ownership_stopped.go | 327 | 12 |
| operations.go | 280 | 81 |
| runner.go | 272 | 106 |
| retained_darwin_remote.go | 142 | 7 |
| systemd_environment.go | 133 | 7 |
| ownership_systemd_remote.go | 123 | 10 |
| endpoint.go | 118 | 20 |
| ownership_listener.go | 90 | 7 |
| ownership_identity.go | 76 | 7 |
| errors.go | 17 | 60 |
| doc.go | 1 | 24 |
| **Total non-test** | **~7.3k code** | **~3.5k comment** (11,410 raw lines) |

Tests: 93 `_test.go` files, 32,626 lines (about 2.9x the production code). Single
package, one `Manager` type.

Largest functions: `ensureOnce` 351 lines (manager.go:1857), `Ensure` 268
(manager.go:553), `deploySelectedWithSupersession` 153 (deploy.go:1071),
`reinstallRetainedTarget` 143 (deploy.go:1237), `resolveStoppedOwner` 138,
`recoverSelectedRestartWithStart` 136 (selected_restart.go:195), `resolveDarwinOwner`
135 (ownership_darwin_remote.go:145), `stoppedServiceEnvironment` 130,
`restartForOperation` 125 (operations.go:253), `AttachUnderGate` 118
(manager.go:916), `supervisorForOwner` 120, `dispatchSelectedRestart` 111
(version.go:1667), `checkSelectedReplacement` 105.

Ownership context (docs/product/subsystems.md S07): sshconn owns attach/reconnect and
the deploy/restart mechanics; the hub's `app_host_*` handlers own admission and
durable records. The fix plans below keep that split and the recovery-over-lockdown
contract (pending restart/deployment intent survives and is retried).

Overall: the package is correct-by-pinning rather than correct-by-structure. Every
review finding got a guard, a flag, a closure, or a paragraph; nothing was ever
folded back into a smaller design. The result is three copies of the attach state
machine, one 350-line decision ladder, a whole superseded restart path that only
tests still exercise, and comments that narrate review history.

---

## Findings

### hub-sshconn-1 (high) Superseded port-based restart/health path survives only to be tested

Locations (all production code, all unreachable from non-test code):
- version.go:476 `detectSupervisor` (32 lines), :290 `detectSupervisorsFrom` (28), :321 `pickSupervisor` (11)
- version.go:1040 `waitHealthy`, :1071 `waitForHealthyHub` (46), :1135 `hubAnswerProvesReplacement`, :1147 `describeHubIdentity`, :1175 `parseHubHealth`, :1185 `parseHubHealthBuild`, :978 `hubHealthRemote`
- version.go:740 `probeListeners`, :776 `listenerAddrRemote`, :786 `hubListenerAddrs`, :819 `listenerOwnsAddr`, :841/:849 `hostIsIPv4/6`, :860 `findHubPID`, :886 `waitPortClear`, :959 `portCleared`
- version.go:1213 `recoverHubArgv`
- version.go:55/64/71 `explicitHostAddr` (live only via selected_restart.go:452), `hostAddrFor`, `hostAddr`; :105 `hubPort`, :116 `argvHubPort`, :132 `loopbackAddr` (only live through the dead health helpers)
- deploy.go:1063 `deploySelected`; ownership.go:206 `revalidateOwnedEndpoint`; manager.go:3688 `setPendingRestart`, :3697 `setPendingStart`; manager.go:466 `sameProcessAs` (its callers, version.go:1094 and :1137, are in dead functions)
- version.go:83 `ValidateHubAddrShape` has zero callers anywhere in the repo, tests included.

Evidence: the reachability pass reports about 340 unreachable production lines before
counting the helpers they alone keep alive (`ListenerProbeRemote` is exported only
so `cmd/evener-hub/app_host_deploy_e2e_test.go:806` can use it). Comments admit it:
version.go:53 "preserves address resolution for the legacy probe helpers",
version.go:62 "retained for their direct tests", version.go:1068 "backs the legacy
health-wait tests", version.go:1721 "Unlike the old
port-based path". Production restart goes `restartHub` -> `restartHubWithStartProof`
-> selected-owner path (version.go:532); none of the list above is called.
Test pins: `legacy_restart_test.go`, `version_test.go` (3,811 lines),
`round12_test.go`, `round13_test.go` call `findHubPID`, `waitPortClear`,
`hubListenerAddrs`, `listenerOwnsAddr`, `detectSupervisor`, `waitHealthy`,
`setPendingRestart`, `setPendingStart`, `hubPort`.

Impact: roughly 340-450 lines of security-sensitive shell/parse code (pid ownership,
listener matching) that reads as live but is not. A maintainer fixing "the restart
path" can fix the wrong copy. Tests give false coverage confidence.

Fix plan:
1. Delete the functions listed above (including `ListenerProbeRemote` export; move the
   one e2e test onto the surviving probe or inline its command). Keep
   `identityFromReport`, `numericPID`, `processAliveRemote`, `waitProcessExit`,
   `listenerProbeRemote` only if something live still needs them (re-run the
   reachability pass after each deletion; `supervisorSet` and `pickSupervisor` family go
   with `detectSupervisorsFrom`).
2. Delete or rewrite the tests that exist only to drive deleted functions. Any
   assertion that is really about selected-owner restart behavior should already exist
   in `selected_restart*_test.go`; where it does not, port the scenario to the real
   entry point (`restartHub`) with the scripted Runner.
3. `setPendingRestart/Start` callers in tests (deploy_test, gate_test, remove_host_test,
   update_host_test, selected_callers_test) should seed state through
   `recordDeployment` or direct struct literals via a tiny test helper.
4. Update the S07 row only if it mentions the port path (it does not).

Size: about -400 production lines, -1.5k to -2.5k test lines. Risk: low for
production (provably unreachable), medium for test churn; needs Jesse's go-ahead per
"never throw away implementations without permission" (these are superseded, but ask).

---

### hub-sshconn-2 (high) The attach/publish/announce state machine exists three times

Locations: `Ensure` manager.go:553-820, `AttachUnderGate` manager.go:916-1033,
`reconnectOnce` manager.go:2766-2863.

Evidence: each repeats the same sequence with small drift:
`ensureOnce` -> reject if `ch.isClosed()||ch.isLost()` -> `reg.SameRegistration`
recheck -> `publishChannel` -> `afterPublish` seam -> second `isLost/isClosed` check ->
detach-then-attach events -> `startSupervise`, with `_ = ch.Close()` and
`lock.Unlock()` hand-placed on each of roughly ten exits in `Ensure` alone. Terminal-error
handling (`baseCtx.Err() != nil && !isTerminal(err)` -> `ErrManagerClosed`) is
copy-pasted in all three (manager.go:681, :1002, :2848), and the comments say so
("exactly as Ensure does", "mirror Ensure's non-close failure handling").
The variants have genuinely different tails (reconnect clears the slot and retries,
AttachUnderGate defers supervision to a handoff, Ensure starts a supervisor), and the
differences are not pinned in one place, which is how `Ensure` ended up with an
`isClosed() || ch.isClosed()` double check after `startSupervise` that the other two
lack.

Impact: any change to publish semantics (the area with the most review history: the
"round-6" stale-true window, lostAfterPublish, restoreChannel) must be made and
tested in three places. Highest drift risk in the package.

Fix plan: extract one `publishAttached(name, host, ch, stale, mode)` helper that owns
validate -> recheck -> publish -> post-publish death handling -> events, returning a
small result enum (`published`, `droppedKeepPredecessor`, `droppedGiveBack`,
`closed`, `hostGone`). The three callers keep only their policy tail (start
supervisor / return handoff / backoff decision). Also extract
`mapAttemptErr(ctx, err)` for the Close-vs-terminal mapping. Do it test-first: the
existing publish-race tests (`attach_under_gate_test.go`, `gate_test.go`,
`entry_binding_recovery_test.go`) are the safety net; add one table test driving
the helper through each outcome.

Size: -150 to -250 lines net. Risk: medium-high (concurrency ordering); do it as its
own PR before any other manager.go change.

---

### hub-sshconn-3 (high) `ensureOnce` is a 351-line ladder, and operations.go re-implements its deploy and restart legs

Locations: manager.go:1857-2207; operations.go:95-198 (`DeployForOperation`,
`deployForOperationWithSupersession`), operations.go:253-377 (`restartForOperation`).

Evidence: `ensureOnce` interleaves 12+ concerns (preflight, endpoint probe, pending
settle, live attach, capability gate, ensure-decision, durable deploy hook, deploy,
re-read contract, unstamped check, restart hook, restart, bootstrap, final attach).
`if finishEnsureOp != nil { finishEnsureOp(err) }` appears 9 times (15 mentions of
`finishEnsureOp` in manager.go). The deploy leg
(`deploySelectedWithAuthority` -> `acknowledgeDeployment` -> `setResolvedTarget` ->
`reReadLaunchContract` -> `deployedBuildNotStamped` -> `clearPendingRestart`) is
spelled out again in `deployForOperationWithSupersession` (operations.go:145-176),
and the restart leg (version + pin selection via `expectedServedBuild`, pending
override, `recoverSelectedRestart` vs `restartHub`) again in `restartForOperation`.
operations.go's own header says the entry points are "deliberately the same code
paths Ensure drives", but they are parallel copies, so a fix to one leg is not a
fix to the other.

Impact: highest-cognitive-load function in the repo; every new recovery case adds a
branch and another comment. The friction doc points at it (friction.md:887, 1048).

Fix plan: split `ensureOnce` into named phases returning a small `ensurePlan`
(`preflight`, `settlePending`, `tryLiveAttach`, `decide`, `runDeployLeg`,
`runRestartLeg`, `bootstrap`, `attach`). Make `runDeployLeg` and `runRestartLeg` the
single implementations that `DeployForOperation`/`restartForOperation` also call,
each taking a `finish func(error)` that is wrapped once (`defer`-style with a named
return) instead of 9 inline calls. No behavior change; existing
`compatible_*`, `selected_*`, `deploy_*` tests are the guard. Update the
"find the owner" references in docs/product/friction.md to stop using line anchors
(see hub-sshconn-17).

Size: -250 to -400 lines after the helpers land. Risk: high; sequence after
hub-sshconn-1 and -2.

---

### hub-sshconn-4 (medium) manager.go is a 3,965-line grab bag with seven per-host maps

Locations: manager.go (whole), `clearHostCaches` manager.go:1834, Manager struct
manager.go:380-447.

Evidence: one file holds lifecycle events, per-host gate (`hostLockGate`,
manager.go:3012-3220), supervisor loops, host CRUD under the gate
(`AddHost/UpdateHost/RemoveHost/DetachHost` and their `*UnderGate`/`*UnderLock`
variants, manager.go:1305-1830), the ensure ladder, deploy/restart hook wiring,
build-version predicates, deploy-remedy text, the `pendingRestartState` machine
(manager.go:3569-3715), and `Channel` (manager.go:3776-3965). Per-host state is split
across seven `map[string]...` fields keyed by name (`locks`, `chans`, `announced`,
`supervisors`, `resolvedTargets`, `pendingRestarts`, `ownerWitnesses`), and
`clearHostCaches` has to remember three of them by hand.

Impact: no navigable seams; merge conflicts; the host-keyed state cannot be reasoned
about as one record.

Fix plan: mechanical file split with no semantic change first (`channel.go`,
`hostgate.go`, `host_mutations.go`, `supervise.go`, `pending_restart.go`,
`events.go`, `versions.go`); then fold the three recovery caches
(`resolvedTargets`, `pendingRestarts`, `ownerWitnesses`) into one
`hostRecovery` struct per host so `clearHostCaches` is `delete(m.recovery, name)`.

Size: ~0 net lines for the split; -30 for the struct fold. Risk: low (split), low-medium (fold).

---

### hub-sshconn-5 (medium) Narrating and review-history comments, stale references, and RoundN tests

Evidence:
- 3.5k comment lines vs 7.3k code. manager.go has 1773 comment lines against 1992 code.
  `UpdateHost` carries a 55-line doc comment (manager.go:1608-1670) for a 20-line body.
- Review-history leaks: version.go:427 "used to be fatal (round eleven)", :663 "(round
  eleven)", deploy.go:296, :307, :402, :566, :589 "(round thirteen)", manager.go:2712
  "the stale-true window the round-6 review named", manager.go:26-28 "04a's chain ...
  belong to 04b", operations.go:3 and :95/:234 "The 04b deploy/restart entry points",
  errors.go:49 "In 04a this is surfaced; the version-match deploy that fixes it is
  04b", deploy.go:1061 "while main's callback retains write acknowledgement".
- References to functions that no longer exist: `restartBare` (version.go:115, :1418),
  `markDevDeployed` (deploy.go:363), `recoverRestart` (manager.go:1945).
- Spec section tokens ("deploy pipeline 08b §6", "spec 04 §...") appear 25 times
  in comments without a path.
- Tests are named for review rounds: `round11_test.go`, `round12_test.go`,
  `round13_test.go`, `deploy_round8_test.go`, `deploy_round10_test.go`, 58
  `Round<N>` mentions in test names/comments.
  `TestRound13RestartBareRefusesAnIPv6WildcardListenerForAnIPv4Endpoint` names a
  function that does not exist.
- ownership_*.go goes the other way: 327 lines in ownership_stopped.go with 12
  comment lines, awk programs embedded in Go raw strings uncommented
  (ownership.go:270-322, ownership_stopped.go:205-216).

Impact: comments teach the wrong function names and a history nobody can verify;
Jesse's naming rule says name things by domain, not history.

Fix plan: one sweep PR, comments and test names only (no code): delete history/round
phrases, replace "04a/04b/08b §6" with the doc path in docs/product or drop it,
rename round tests by the behavior they pin (merge into the topical test file),
shrink the `UpdateHost`/`Ensure`/`AttachUnderGate` essays to the invariant plus a
pointer, and add short "why" comments to the undocumented awk/shell blocks.
Run last, after structural PRs, to avoid conflicts.

Size: about -1,200 comment lines, +60. Risk: very low.

---

### hub-sshconn-6 (medium) Registration identity compare open-coded six times

Locations: selected_restart.go:189, ownership.go:212, ownership.go:226 (`sameOwner`),
ownership_darwin_remote.go:190, ownership_listener.go:25, deploy.go:1121; related
`Channel.MatchesRegistration` (manager.go:3854) and manager.go:619.

Evidence: all spell `host.Equal(x) && host.Generation == x.Generation &&
host.IncarnationID == x.IncarnationID && host.PresenceEpoch == x.PresenceEpoch`.
hostreg already owns `SameRegistration(a, b)` (hostreg.go:846 = Equal + Generation +
IncarnationID) but not the PresenceEpoch term, so sshconn re-spells the whole
expression. deploy.go:1240/1255 mix `hostreg.SameRegistration` with a separate
`PresenceEpoch !=` check, showing the drift.

Impact: adding or changing an identity field requires finding six hand-written
conditions; a missed one silently weakens an ownership fence.

Fix plan: add `hostreg.SameRegistrationAndPresence(a, b Host) bool` (or fold
PresenceEpoch into `SameRegistration` if hostreg's other callers can take it; ask
Jesse), test it in hostreg, and replace the six sites. Cross-slice (hostreg), so
land the hostreg helper first.

Size: -25 lines. Risk: low.

---

### hub-sshconn-7 (medium) The four-key owner environment allowlist is spelled in eight places

Locations: ownership.go:302-303 (awk), :351 `ownerEnvKeys`, :459 (grep -E),
ownership_stopped.go:205 (awk regexp), ownership_darwin_remote.go:70 (Go switch), :376
(awk regexp), selected_restart.go:489 `freshOwnerEnvironmentRemote` (four copy-pasted
`if [ "${X+x}" = x ]` blocks), plus `length("EVENER_HUB_LISTEN_ADDR")` at
ownership.go:310 as a magic max key length.

Evidence: `HOME|XDG_CONFIG_HOME|XDG_STATE_HOME|EVENER_HUB_LISTEN_ADDR` literal in each.
Adding a key (or the hub changing which env selects its root) means eight edits across
shell, awk and Go.

Fix plan: one `ownerEnvKeys` slice in Go; build each remote snippet and the Go switch
from it (`ownerEnvRegexp()`, `ownerEnvAwkCondition()`, a loop for the printf blocks,
`slices.Contains` for the switch). Guard with a single test that asserts every
generated snippet mentions every key (behavior, not wording). Better: check whether
`EVENER_HUB_LISTEN_ADDR` already has a name in `envvars` and reuse it.

Size: -40 lines. Risk: low-medium (shell quoting); the existing ownership_*_test.go
fakes exercise the commands.

---

### hub-sshconn-8 (medium) Process-evidence capture is re-implemented per OS and per recovery flow

Locations: ownership.go:106-193 `resolveLinuxOwner`; ownership_darwin_remote.go:145-280
`resolveDarwinOwner`; deploy.go:1275-1324 (inline linux revalidation inside the
`reinstallRetainedTarget` closure); retained_darwin_remote.go:21-110
`revalidateRetainedDarwin`; ownership_systemd_remote.go:67.

Evidence: every one of them does generation read -> `checkHubOwner` ->
`recoverHubArgvRaw` -> cwd read -> environment read -> supervisor/definition compare
-> generation re-read. The cwd step has diverged: linux uses `readlink
/proc/<pid>/cwd` three times (ownership.go:121, deploy.go:1287,
ownership_systemd_remote.go:67); darwin parses `lsof -a -p <pid> -d cwd -Fn` twice
with different semantics (ownership_darwin_remote.go:212 takes the LAST `n/` line,
retained_darwin_remote.go:78 refuses MORE THAN ONE). Same "pid is gone" probe is
inlined three times (see hub-sshconn-13). The retained linux check inside
deploy.go is a 50-line copy of `resolveLinuxOwner` that compares against `prior`
instead of building a new owner.

Impact: the ownership proofs are the safety core of kill/restart authority; two
copies disagreeing on lsof output handling is a latent bug, and every new proof
step (like the vnode/image checks) needs adding per copy.

Fix plan: introduce a small `processEvidence` reader per OS
(`generation()`, `cwd()`, `environment()`, `argv()`), returning a value struct, and
make resolve/revalidate/retained compare `processEvidence` values (`sameEvidence`)
instead of re-running the probes in-line. Linux and Darwin each implement the four
probes once. This is the one genuine seam here (two real implementations), not a
speculative interface. Pin the lsof-cwd divergence with a red test first (two `n/`
lines) to decide which rule is right before unifying.

Size: -200 to -300 lines. Risk: high (ownership/safety logic); do after the
decision ladder split and with Jesse reviewing the unified cwd rule.

---

### hub-sshconn-9 (medium) Deploy-selection wrappers, flag parameters and a 143-line closure

Locations: deploy.go:1063-1071 (`deploySelected` -> `deploySelectedWithAuthority` ->
`deploySelectedWithSupersession`), operations.go:141-145
(`deployForOperation` -> `...WithSupersession`), deploy.go:1237-1379
`reinstallRetainedTarget`, deploy.go:323 `deploy(..., retainedTarget ...string)`,
operations.go:95 `DeployForOperation(..., journals ...DeploymentJournal)`.

Evidence: three/two-layer pass-through wrappers add one parameter each
(`realEndpointAuthority bool`, `supersede func() error`); `deploySelected` is dead
(hub-sshconn-1). Variadics are used as optional arguments but only the first
element is read (`journals[0]`, `retainedTarget[0]`). `deploySelectedWithSupersession`
has a nested `checkRetained` and `beforeWrite` closure (153 lines total) that reads
`m.pendingRestart` three times and locks `m.mu` by hand. `deploy()` ends with two
identical returns:

    if m.ownExecutableCannotServe(facts) { ...check...; return m.deployInstaller(...) }
    return m.deployInstaller(ctx, host, facts, guardedWrite)   // deploy.go:408-415

and `deployPush`/`deployInstaller`/`reinstallRetainedTarget` all re-read the
launch contract the same way afterwards.

Fix plan: collapse to one `deploySelected(ctx, host, facts, deployOpts)` with a
`deployOpts{realEndpointAuthority, supersede, recorders, retainedTarget *string,
journal *DeploymentJournal}` struct; turn the two closures into methods on a small
`retainedDeploy` value; delete the redundant final branch in `deploy()`. Pure
refactor; `deploy_*_test.go`, `retained_*_test.go` guard it. Requires Jesse's OK
only if the exported `DeployForOperation` signature changes (its single caller is
cmd/evener-hub/app_host_manage.go:1526).

Size: -80 lines. Risk: medium.

---

### hub-sshconn-10 (medium) `pendingRestartState` is a 13-field mode bag mutated through scattered lock-and-store

Locations: manager.go:486-515 (type), 3569-3715 (helpers), 15 direct
`m.pendingRestarts[host.Name] = ...` writes under hand-rolled `m.mu.Lock()` at
operations.go:80,346,350,365; deploy.go:1191,1197,1204-1206; version.go:555,1661,1707;
selected_restart.go:20; manager.go:2221.

Evidence: behavior is selected by booleans (`deployment`, `restart`, `start`,
`writeAcknowledged`) plus `command != ""` and `selected != nil`, combined in
`active()`, `needsDeployment()`, `completedBy()`, `validateExecutable()`; the legal
combinations are only documented in prose. There is no `storePending(name, state)`
helper, so each writer repeats lock/store/unlock and some "restore previous" rollback
logic (version.go:1658-1662, operations.go:348-352, deploy.go:1195-1198).
`pending.command` is now only written by `dispatchSelectedRestart`
(version.go:1700-1704) and `recordDeployment` carry-over, since
`setPendingRestart/Start` are dead; the code says stored text is never trusted
("never trust stored text", version.go:1728), yet `command == ""` is read in 8 places
(manager.go:2259, :3582, :3619; operations.go:149, :304; selected_restart.go:325;
deploy.go:1202) as a "restart was dispatched" marker.

Impact: recovery correctness (the product's retry contract) depends on states that no
type enforces.

Fix plan: (1) add `storePendingRestart` and `restorePendingRestart(previous)` and
route the 15 writes through them; (2) replace the booleans with an explicit `kind`
(`deployOnly`, `deployThenRestart`, `restart`, `start`) after enumerating reachable
combinations from tests; (3) replace the `command == ""` marker with an explicit
`dispatched bool` and keep the text only where `selected_restart.go:34` needs it.

Size: -60 lines, clearer invariants. Risk: medium; step 2 needs a state-table test.

---

### hub-sshconn-11 (medium) Error taxonomy: alias pairs, a hand-kept terminal list, and a doc that contradicts the code

Locations: manager.go:2895-2928 `isTerminal`/`Terminal`; manager.go:2930-2988 (six
`ErrX = errX` exported aliases, about 60 lines of near-identical comment);
preflight.go:424 `ErrExecutableMissing = errExecutableMissing`; errors.go:17-31
`ErrSSHAuth`; preflight.go:484-545 (`authFailureMarkers`, `isAuthFailure`,
`isSSHAuthFailure`, `sshDiagnostic`).

Evidence:
- Seven sentinels exist twice (private for use inside, exported alias for the hub),
  and sentinels live in four files (errors.go, preflight.go, deploy.go, endpoint.go).
- Terminality is a 19-line `errors.Is` list in a different file from the sentinel
  definitions; adding a terminal error means remembering the list.
- errors.go:17-31 says "in production the manager classifies no failure as
  ErrSSHAuth", but `sshRunFailure` (preflight.go:521) returns it when a Start failure
  carries an auth marker. The code is correct; the doc is wrong. ErrSSHAuth is
  non-terminal and exists only to relabel a retryable failure; the hub's only
  consumers are two appsource tests plus a comment.
- `config.go:599` in cmd/evener-hub defines a second, unrelated `ErrHostAddr`.

Fix plan: export the sentinels directly (delete the private twins and the alias
block); define terminality once, either by wrapping terminal errors in a
`terminalError` type or by a single `var terminalErrors = []error{...}` next to the
sentinels in errors.go; delete `Terminal`'s extra indirection (keep the exported
function, drop `isTerminal` or vice versa); fix the ErrSSHAuth comment or delete the
sentinel and its marker machinery if the hub's appsource mapping (internal/appsource/
remote_hub_source.go:386) can key off `ErrSSHStart` alone (ask Jesse; touches an
exported name). Guard: one table test that every sentinel is classified
terminal/non-terminal exactly once.

Size: -110 lines. Risk: low-medium (rename across cmd/evener-hub).

---

### hub-sshconn-12 (medium) Dead or never-set public surface and test seams inside `Options`

Locations: manager.go:168 `Options.EnsureDeploy` (never read; wiring is the atomic
pointer via `SetEnsureDeployHook`); manager.go:174 `Options.HubListenAddr` (no
production caller sets it; production `main.go:592-598` sets only Logger,
BuildBinary, BuildSource, OwnExecutable, DeployDisabled, DeployHelp, OnEvent);
manager.go:1131 `HandshakeIfAttached` (hubcore config.go:163 comment: "which has no
production caller"); `ConnectTimeout`, `ServerAliveInterval`, `ServerAliveCountMax`,
`BackoffBase/Max`, `ClientName/Version`, `Stderr` are never set by production either;
manager.go:200 `AfterUpdateHostSwap` is an exported hook that exists only for two hub
tests; the other seven hooks (`beforeHostGate`, `afterPublish`, `beforeSuperviseGate`,
`superviseExited`, `beforeSuperviseClear`, `beforeSuperviseStart`, `afterChildExit`)
are called inline in production paths (e.g. manager.go:2705, :2711).
`EnsureDeployHook`/`EnsureRestartHook` are identical types with identical
atomic setter/getter pairs (manager.go:3223-3278).

Impact: `Options` reads as 30 knobs of which 7 are used; every test seam adds a
conditional call to hot lifecycle code; the doc comment on `EnsureDeploy` describes
behavior that does not exist.

Fix plan: delete `EnsureDeploy`, `ValidateHubAddrShape` (hub-sshconn-1), and
`HandshakeIfAttached`; decide with Jesse whether the six tuning knobs stay (tests do
use them) but move the never-set ones into one unexported `tuning` struct tests can
set; replace the two hook types with one `type EnsureHook func(hostreg.Host)
(finish func(error), error)` and one generic `atomicHook[T]` (or just a mutex-guarded
pair of fields); move `AfterUpdateHostSwap` behind a `_test`-built accessor or an
`export_test.go`-style setter if the hub tests can live with it.

Size: -90 lines. Risk: low. Exported-API deletions need Jesse's approval.

---

### hub-sshconn-13 (low) Small helpers reinvented or duplicated

Evidence:
- "exit status 1 means absent" probe: `var exit *exec.ExitError; errors.As(err,&exit)
  && exit.ExitCode()==1` at deploy.go:1227, deploy.go:1262, preflight.go:358,
  preflight.go:442; and the identical `if test -x P; then exit 0; else exit 1; fi`
  shell at deploy.go:1226 and :1261. Add `exitedWith(err, 1)` and
  `executableTestRemote(path)`.
- "pid is gone" check: `strings.TrimSpace(string(out)) == pidGoneMarker` inlined at
  deploy.go:1335, retained_darwin_remote.go:32, selected_restart.go:217 beside the
  full `waitProcessExit` switch (version.go:932). Add `processGone(ctx,host,pid)
  (bool, error)`.
- ownership_darwin_remote.go:379 `equalStrings` (NUL-join compare, which also
  conflates `["a\x00b"]` with `["a","b"]`) next to `slices.Equal` used everywhere
  else; replace its one caller at selected_restart.go:173.
- version.go:220 `isNumericUID` is used for pids, inodes, fds and uids; rename to
  `isDigits`. `numericPID` returns the string it was given and ten callers discard
  it (`_, err := numericPID`).
- manager.go:3371-3381 `isDevVersion/isDirtyVersion`: the `-dirty` suffix is parsed
  in four places (manager.go:3381, selected_restart.go:344,
  cmd/evener-hub/main.go:71, cmd/evener-hub/app_update.go:237) while buildinfo
  produces it (buildinfo.go:24,35). Move `IsDirty(v)` / `TrimDirty(v)` into
  buildinfo.
- "usable channel" is defined three times: `liveChannel` (manager.go:3289),
  `installedLiveChannel` (:1069), `Attached` (:3305).
- `requiredLaunchFlag = "api-log"` is defined in sshconn/preflight.go:22,
  cmd/evener-hub/spawn.go:1146, and launchcheck's supported list.
- runner.go:103 `execRunner.Run` is a one-line pass-through to `runOneShot`, its only
  caller.
- `hostLockGate.isHeld`, `Lock`, `holderOf` and the `TryAcquire/HoldAs` pair re-resolve
  the entry by name with manual ref-count juggling (manager.go:3191-3215).

Fix plan: five tiny helper extractions plus two moves into `buildinfo`/a shared const;
each is a few lines and covered by existing tests. Do these opportunistically inside
the PRs that touch the same file (not as drive-bys).

Size: -50 lines. Risk: very low.

---

### hub-sshconn-14 (low) Dead preflight fields and an environment probe feeding them

Locations: preflight.go:35-75 `Preflight.ConfigRoot/StateRoot/UnameOS/UnameMachine`,
preflight.go:108-163 (`envProbeScript`, `parseEnvProbe`, `resolveRoots`),
preflight.go:253-262.

Evidence: grep across cmd/evener-hub finds no reader of `Preflight.ConfigRoot`,
`StateRoot`, `UnameOS` or `UnameMachine`; only `manager_test.go:157-161` asserts
them. The only environment fact consumed is `Home` (deploy.go:677, :937). The probe
costs one extra SSH round trip per preflight and pulls in `cmdutil` and `userdirs`
solely to compute two unread values. doc.go:7 still advertises "HOME/XDG roots".
Remote roots are now discovered by the resolved-endpoint path, not guessed from XDG.

Fix plan: reduce the probe to `printf %s "$HOME"` (parse in a 5-line helper), drop
the four fields, `resolveRoots`, the two imports, and the manager_test assertions,
update doc.go. Behavior change is one fewer remote command argument shape; confirm
no fake runner script keys on `envProbeScript` text before cutting (tests/fakes_test).

Size: -70 lines. Risk: low.

---

### hub-sshconn-15 (low) Single-embedder indirection for deploy remedies

Locations: manager.go:151-156 `Options.DeployHelp`, manager.go:3471-3506
(`deployRemedy`, `deployHelp`, `installerRemedy`, `unstampedRemedy` with library
default strings), cmd/evener-hub/deploy_flags.go:62-195 (`hubDeployHelp*` constants).

Evidence: "sshconn is a library and must not learn those names" justifies a remedy
callback, but the package is `internal/` with exactly one embedder, and the default
strings ("set Options.BuildSource", "use the atomic push path or Options.BuildBinary")
are text no operator should ever see. Tests assert on them
(`preflight_test.go:325` checks `"set Options.BuildSource"`).

Fix plan: keep `DeployHelp` but drop the three library-default variants (make it
required when deploying is possible, one `m.opts.DeployHelp` read), and remove the
copy assertions. Alternatively move the hub-specific remedy builders into one place.
Low value; only do it with the comment sweep.

Size: -35 lines. Risk: low.

---

### hub-sshconn-16 (low) Defensive `m.reg == nil` guards in 11 places

Locations: manager.go:558, :932, :1143, :1306, :1463, :1558, :1596, :1687, :1814;
operations.go:50, :205.

Evidence: `New(reg, ...)` takes the production registry; the nil case exists only
because `detach_host_test.go:76` and `update_host_test.go:149` call `New(nil, ...)`.
Several carry bespoke error strings ("sshconn: RemoveHostUnderGate with no registry").

Fix plan: require a non-nil registry in `New` (panic or documented precondition), fix
the two tests to pass an empty `hostreg.New(nil)`-style registry, delete 11 guards.
Needs Jesse's nod since it tightens a constructor contract.

Size: -45 lines. Risk: low.

---

### hub-sshconn-17 (low) Stale file:line anchors and mismatched comments

Evidence:
- docs/product/friction.md:887 anchors `Manager.Ensure` at `manager.go#L537` (it is
  at :553), :1048 anchors `bootstrapHub` at `#L2182` (now :2210), :1050 anchors
  `relaunchCommand` at `version.go#L1689` (now :1562). Line-number anchors rot on
  every edit; link to the symbol or file.
- version.go:132 comment says `loopbackAddr` mirrors "cmd/evener-hub/attach.go
  loopbackAddr"; attach.go has no such function.
- preflight.go:71 doc comment is headed `osArchTargetsSupported` but the function is
  `targetSupported`.
- Code comments cite `install.sh:7-18`, `install.sh:21-30`, `install.sh:32-38`,
  `.goreleaser.yml`, `cmd/evener-hub/spawn.go:770`, `launchcheck.go:40-54`
  (preflight.go:19-21, :72, :82, :94, :168; runner.go:68; deploy.go:924). spawn.go:770
  is now unrelated code.
- `mapOS/mapArch/targetSupported` (preflight.go:73-107) restate install.sh's tables in
  Go; if both change independently they drift (no test ties them).

Fix plan: replace line anchors with symbol/file references; fix the two wrong
comments; add one test that loads `install.sh` and asserts it maps the same
os/arch pairs as `mapOS/mapArch` (behavior, no copy), or generate the Go table from
the script's data. Update docs/product/friction.md in the same change.

Size: ~20 lines touched. Risk: very low.

---

### hub-sshconn-18 (medium) Test suite: 32.6k lines, pins dead code, and 50+ copy assertions

Locations: `legacy_restart_test.go`, `version_test.go` (3,811 lines), `round11/12/13_test.go`
(1,206 lines), `deploy_round8/10_test.go` (579 lines), plus
`deploy_test.go:*`, `gate_test.go:291,431,561`, `preflight_test.go:325`,
`round12_test.go:388,426`, `deploy_round10_test.go:274,277`.

Evidence: 50 assertions of the form `strings.Contains(err.Error(), "<phrase>")` and 91
`strings.Contains` checks overall, which AGENTS.md prohibits ("Never assert on
phrasing"). Examples: `"systemctl restart failed"`, `"operation store is not
configured"`, `"set Options.BuildSource"`, `"still running"`, `"non-numeric"`. The
package has sentinel errors and a `RunError` type that already support behavioral
assertions. Many tests drive functions deleted by hub-sshconn-1, so they assert on
mocks of a path production never takes. Several tests are one scenario copy-pasted
with a different fake script (the `selected_*` family).

Fix plan: when deleting the legacy path (hub-sshconn-1), delete its tests; convert the
remaining phrase checks to `errors.Is` on the sentinel or to an assertion on
recorded runner commands; fold round-named tests into topical files; introduce one
table-driven harness for the `selected_*` scenarios. Do not reduce coverage of live
behavior: for each deleted test, name the surviving test that covers the live
equivalent in the PR.

Size: -2k to -4k test lines. Risk: medium (coverage must be argued per deletion).

---

## Recommended fix sequence

1. hub-sshconn-1 (delete the superseded restart/health path and its tests) and the
   dead-surface parts of -12, -14, -16. Provable, shrinks everything after it.
2. Small shared helpers that unblock later PRs: -6 (hostreg `SameRegistrationAndPresence`,
   cross-slice, land first), -7 (one env-key list), -13 (exitedWith, processGone,
   buildinfo dirty helpers).
3. hub-sshconn-11 (single error taxonomy and terminal classification); it is a
   prerequisite for cleanly extracting the publish helper.
4. hub-sshconn-2 (one publish/announce helper for Ensure, AttachUnderGate,
   reconnectOnce).
5. hub-sshconn-10 (pending-restart store helpers, then explicit kind), then
   hub-sshconn-9 (single deploy-selection entry point).
6. hub-sshconn-3 (split `ensureOnce`, share the deploy/restart legs with
   operations.go); depends on 2, 5, 9.
7. hub-sshconn-8 (per-OS process-evidence reader), after Jesse decides the lsof-cwd
   rule; highest risk, keep it isolated.
8. hub-sshconn-4 (file split, fold recovery caches) once the code has stopped moving.
9. hub-sshconn-18 (test cleanup, rounds renamed, copy assertions to behavior),
   interleaved with each of the above for the tests they touch, final sweep last.
10. hub-sshconn-5, -15, -17 (comment/doc sweep) as the last PR, including the
    friction.md anchors; the S07 row in docs/product/subsystems.md needs no edit
    unless hub-sshconn-1 changes the described restart behavior (it should not).

Gates per PR: targeted `go vet ./cmd/evener-hub/internal/sshconn/` locally, tests via
CI per the repo's rule; concurrency-sensitive PRs (2, 3, 8) should also run the race
lane in CI.
