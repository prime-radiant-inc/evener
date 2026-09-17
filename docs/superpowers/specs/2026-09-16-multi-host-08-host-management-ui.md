# Component spec 08 — Host management UI (add-host dialog, Connect, deploy/restart surface)

Status: not started. This spec is the hand-off for the implementing session.

Depends on: PR #1603 (the attached-only seams and the shared origin guard).
The symbols this spec cites that do not yet exist on main —
`sshManager.ChannelIfAttached`, the `RemoteHostClientIfAttached`/
`RemoteHostHandshake` seams, the attach classifier, and the shared origin
guard (bridge origin metadata plus the rule that remote-originated,
peer-forwarded requests never reach controller-side dialing or SSH-affecting
handlers) — land with #1603 and MUST be on main before this component starts.
`evener/host/attach` itself and the spawn picker's Connect trigger also ship in
#1603; this component does not extend that method. Do not start this component
until #1603 is landed.

Related: `2026-09-14-multi-host-evener-design.md` (topology), `…-03-host-config.md`
(host registry), `…-04-ssh-connection-manager.md` (attach + deploy/restart
machinery), `…-06-fleet-view.md` (picker, rail badges, online flags),
`…-07-remote-admin.md` (admin proxy). Components 01–07 are landed on main; this
component adds the user-facing management surface they deliberately left out.

Purpose: three user stories, all impossible today. A user adds a remote server
from the UI (today hosts are `[[hosts]]` entries in the controller's
`hub.toml`, read once at startup — adding one means editing a file and
restarting). A user connects on demand from a Hosts section with per-host state
and a Connect action. A user deploys and restarts from the UI (component 04b's
machinery exists inside `sshconn.Manager` but is reachable only as an `Ensure`
side effect). The spawn host picker (06b) stays hidden while zero remote hosts
are configured, so this component is what makes the federation fully operable
without a terminal.

## 1. Glossary

Every later section uses these terms with exactly these meanings.

**MutationId / operationId / id.** `mutationId` is the client-supplied
idempotency key on `add`/`update`/`remove`: opaque, non-empty, at most 128
bytes, no required structure. `operationId` is the client-supplied operation
ID on `deploy`/`restart` with the same shape rules; its meaning per method is
pinned in §11 (on `deploy`/`restart` responses `id` is the controller-assigned
record id and `clientOperationId` echoes the caller's value; the `operations`
`operationId` filter matches `clientOperationId` while its `id` filter matches
the record id; `orphan-resolve` takes `{id}` as the record id;
`teardown-retry` takes `{remnantId}`, never an operation id). A replay is a
call repeating a previously used key.

**Intent.** A durable record the controller writes before acting, so a crash
leaves recovery instructions on disk. The three intents are the
`swapStarted`/`teardownStarted` flags plus runtime phase on a staged mutation
marker (§5), the `pendingStoreSync` revocation intent (§8), and the
`pendingCompensation` phase record (§8). An in-memory plan (a `HostPlan` the UI
renders, a staged-but-unpersisted change) is never an intent.

**Marker.** Each marker below is a distinct persisted value; no section uses
"marker" for any other:
- Staged-receipt marker (`pendingMutation`): the transient per-host entry the
  step-(2) sidecar write carries before the post-commit write replaces it with
  the finalized receipt (§5).
- Finalizing claim (`finalizingMutation`): the same entry under a
  server-generated opaque attempt token while a foreign path finalizes it (§5).
- Reconcile marker: the (re-read `hub.toml` fingerprint, about-to-drop sidecar
  entry) pair the post-rename reconcile stages in the same write as its
  cleanup (§6).
- Pruned marker (`prunedReceipts`): the scoped key plus `{prunedAt}` a
  count/TTL compaction persists when it drops a superseded receipt (§6).
- Cleared-remnant marker (`remnantId → clearedAt` in `teardownRemnants`): the
  proof a remnant already cleared, so a lost-response retry returns
  `already-cleared` (§6).
- Bootstrap-attempt fence: the durable record on a host's sidecar entry that a
  first-contact delivery started, so a crash cannot reopen the unfenced
  exception (§9).
- `helperInstalled`: the sidecar flag that a host carries the pinned fencing
  helper (§9).

**Guard / gate / fence / lock / mutex.** The origin guard is the #1603 shared
pre-admission hook refusing honestly-marked remote-originated requests (§3). A
per-host gate serializes deploy/restart/`plan`/teardown work for one host name;
acquisition is try-acquire and never waits (§7). The remnant fence refuses
every lifecycle and attach path on a name holding an open teardown remnant
(§6). The orphan fence refuses the same paths on a name holding an open
`orphan-unverified` record (§9). The fencing quarantine refuses the same paths
on a name whose fencing kill/wait timed out (§9). The process-wide mutation
lock serializes sidecar read-modify-write only, never across a teardown; lock
order is fixed with the mutation lock outermost and the store mutex innermost
(§5, §7). The store mutex serializes operation-store read-modify-write (§7).
The remote lease is the per-host exclusive lease the fencing wrapper holds
across re-check plus irreversible action (§9).

**Epoch.** The fencing epoch is a worker's durable (controller boot id,
per-host monotonic op sequence) presented on every SSH command (§9). The
guard-file sequence is the remote guard file's own monotonic sequence, the
total order across controller restarts (§9). The presence epoch is the per-host
monotonic removal/presence counter the sidecar advances on every add, remove,
re-add, and expiry purge (§10, §15). The store epoch (`quarantineEpoch`) is
the durable counter advanced once per corrupt-store quarantine (§7). The
wall-clock high-water mark is the greatest wall-clock value observed by any
mint/capture write, used only by the rollback guard (§8).

**Boundary.** A persisted ownership description a verifier checks before
signaling a possibly-live process. The four variants form the `BoundaryEntry`
kind union (§11): `local-linux` (cgroup identity plus launcher-observed
pid/start time bound to the pre-spawn nonce), `local-darwin` (pgid plus session
id plus launcher-observed pid/start time), `local-markerless` (the persisted
pre-spawn boundary only, when the crash landed before the launcher marker),
`remote-fencing` (the timed-out epoch's fencing epoch plus guard-file epoch
plus lease-tracked entries each carrying ownership identity).

**Generation.** The per-name monotonic counter minted by `add`/re-add and
advanced by `update`, persisted in the sidecar. Its six uses are: the live
generation (the registry's current value for a name); the pinned generation (a
receipt, record, or token's copy of the value it ran under); the high-water
mark (the greatest value a name ever carried, surviving removals); the mirrored
generation (the high-water copy in the operation store); the tombstone's
removed generation (the value the removed incarnation carried); the discarded
generation (a compensated-away value boot rolls back, kept as floor so no later
mutation reuses it). An incarnation id is the opaque server-generated string
minted beside the generation on every `add`/re-add, never derived from it and
never reused; the pair (generation, incarnation id) is the guarded-mutation and
dedup identity everywhere (§5, §15).

**Receipt / remnant / tombstone.** A receipt is the durable finalized outcome
of a host mutation, keyed by (mutationId, host name, mutation kind,
post-commit generation, incarnation id) (§5). A remnant is the durable
in-progress teardown record of a committed-with-teardown-failure mutation,
addressed by its opaque `remnantId` (§6). A tombstone is the durable
removed-host record carrying retained rows (§15).

## 2. PR sequence

This table is the only place the 08a/08b/08c split is defined.

| Method / artifact | 08a | 08b | 08c |
|---|---|---|---|
| `evener/host/list` behavior + hand-written types | ships | union catalog + regenerated client | — |
| `evener/host/add` behavior + hand-written types | ships | union catalog + regenerated client | — |
| `evener/host/update` behavior + hand-written types | ships | union catalog + regenerated client | — |
| `evener/host/remove` behavior + hand-written types | ships | union catalog + regenerated client | — |
| `evener/host/teardown-retry` behavior + hand-written types | ships | union catalog + regenerated client | — |
| `evener/host/status` | — | ships (handler + catalog + client) | consumes |
| `evener/host/plan` + token mint | — | ships (handler + catalog + client) | consumes |
| `evener/host/deploy` | — | ships (handler + catalog + client) | consumes |
| `evener/host/restart` | — | ships (handler + catalog + client) | consumes |
| `evener/host/operations` + pagination | — | ships (handler + catalog + client) | consumes (polling) |
| `evener/host/running` probe | — | ships (handler + catalog + client) | — |
| `evener/host/orphan-resolve` | — | ships (handler + catalog + client) | — |
| `attachUnderGate` primitive + live host-set surface (registry/manager) | ships | uses (restart reattach) | — |
| Sidecar, staged commit, receipts, remnants, tombstones, generations | ships | mirrors generations in store | — |
| Operation-store skeleton `remove` depends on (`host-removed` mark path, outstanding-token-row purge path, atomic writes, no 08b behavior behind them) | ships as store-owned helpers | full store (records, dedup, tokens, reconciliation) | — |
| Union-registration generator work (`internal/appwirets/emit.go`) | — | ships | — |
| Origin guard pre-admission hook at request ingress | ships (guard-before-admission ordered on 08a surface) | dedup/token orderings asserted where they ship | — |
| Catalog registration + regenerated TypeScript client for union-shaped methods | — (08a ships no catalog entry and no regenerated client for a union-shaped response) | ships | — |
| Non-union 08a helpers | register immediately | — | — |
| Hosts settings section, dialogs, stores, polling, deploy confirmation | — | — | ships |
| 08a tests (§16) | ships | — | — |
| 08b tests (§16) | — | ships | — |
| 08c tests (§16) | — | — | ships |

08b stacks on 08a; 08c stacks on 08b. Hand-written Go request/response structs
land in the same PR as their handlers so the backend contract is reviewable;
public catalog registration plus the regenerated client for union-shaped
responses arrive with 08b. No undocumented provisional types ship in any PR.

## 3. Scope and non-scope

Scope: the twelve controller-side methods besides `attach` — `evener/host/list`,
`add`, `update`, `remove`, `status`, `plan`, `deploy`, `restart`,
`operations`, the local `evener/host/running` probe handler, the
`evener/host/teardown-retry` repair mutation, and the
`evener/host/orphan-resolve` crash-recovery mutation — plus durable persistence
of host entries with hot-apply, the controller-side operation store, the Hosts
settings section (§13), and the catalog/client/registration work per the §2
table. `evener/host/attach` ships with #1603 and is relied on, not
re-specified.

Cross-component effects (each owned by the cited section, none introduced
elsewhere): component-03 validation rules and the 63-remote-host cap enforced
on mutations, boot load, registry paths, and live reconcile (§4, §14, §15);
component-04b deploy/restart internals reused verbatim under the operation
model (§8); component-05 `Online()`/broker rebind consuming the live host set
(§15); component-06 navigation poke consuming the live host set, manifest
`sources` enumeration as the registered-source set, and the picker staying
hidden at zero remotes (§13, §15); the component-07a host-admin controller's
per-host forwarding and notification fan-outs starting and stopping with the
staged commit and its rollback (§6, §14); decision-source (archive/favorite)
validation rewired from the startup `RemoteHosts` snapshot to the live set
(§6); `GET /api/health` demoted to the human-readable surface carrying no
`healthy` field — `evener/host/running` is the authority (§4); source-registry
`Remove(name)` (or equivalent deregistration) added where predecessors had no
register/unregister path (§14, §15); AppWire catalog entries, request/response
structs, the regenerated TypeScript client, and frontend settings-section
registration per the §2 table (§11, §14).

Non-scope: the remote-admin proxy (`app_host_admin.go`) and its forwarded
allow-list are unchanged in interface. The methods above are controller-local:
they act on the controller's config and its sshconn manager, never on a remote
hub's surface. They MUST NOT be added to `remoteHostAdminMethods` (negative
test required). The allow-list is not the security boundary. The true security
boundary is edge admission/auth: the hub's capability-token auth gating every
non-exempt route plus the existing admin-mutation admission the methods ride.
Any capability-token holder is fully authorized, and a hostile peer holding it
can omit the cooperative bridge marker and present as local. The origin guard
is therefore an honest-peer recursion terminator, not a security boundary: it
terminates an honest A→B→A forwarding cycle past depth 1, and the v1 trust
assumption is that peer hubs are cooperative (the marker is client-asserted and
carries no secret). Residual risk — a compromised or hostile token holder
invoking controller-local or SSH-affecting methods by omitting the marker — is
contained by token secrecy (0600 token file, loopback-only bridge dial,
no-proxy handshake), not by the guard.

Every `evener/host/*` request (all thirteen methods, `attach` included) passes
through the shared origin guard: landed by #1603 at the dial seam
(`guardRemoteHostDial`) and the remote-client dispatch seam
(`guardRemoteDispatch`), and extended by 08a to the common
request-ingress/router boundary as a pre-admission hook. Honestly-marked
remote-originated, peer-forwarded requests are refused before admission —
before dedup and before token validation on the phases where those stages
exist — and before any handler runs. The mutating handlers (`attach`, `plan`,
`deploy`, `restart`, `add`, `update`, `remove`, `teardown-retry`,
`orphan-resolve`) are unreachable to honestly-marked peer-forwarded requests; a
markerless request is local-originated by construction and takes the full
admission path. The reads (`list`/`status`/`operations`/`running`) are guarded
equally because they disclose the controller's topology and operation state.
(`attach`'s guard routing ships and is test-pinned with #1603; this component
pins the other twelve.) The one direction-scoped exception is the
`evener/host/running` peer probe: a controller-originated request issued only
through `sshManager.ChannelIfAttached(name)` over a live channel peered by the
#1603 handshake, admitted on the remote only over that same attached session
(§4). The exception never admits a browser- or forwarded-origin request, the
probe is never forwarded onward to a third hub, and a missing or unverified
handshake stays an unauthenticated-probe refusal — so the A→B→A chaining the
#1603 guard closes stays closed.

Also out of scope: per-host detach/disconnect (no `evener/host/detach`;
`Manager.Close` remains whole-manager); multi-tenancy or auth changes (the
methods ride the existing admin-mutation admission; no new capability type);
the native client (hub web only); the hub's `ServerInfo.Version` constant
(`"0.1.0"` at `cmd/evener-hub/main.go:46` — version display reads the
preflight facts, which already report the remote's real build; the constant is
a separate owner decision); any change to lazy attachment semantics. The
attached-only seams (#1603) stay exactly as landed. `evener/host/list`,
`status`, and `operations` never dial — test-pinned — and `evener/host/attach`
remains the only user-facing trigger of the attach/bootstrap cycle besides an
explicit `SourceID`. The sanctioned non-user paths are the operation-owned
`attachUnderGate` attach/reattach (restart's reattach and attach-first, plus
deploy's planned restart — §4) and the two deliberate non-attach SSH uses:
`plan`'s gated preflight refresh (run with no gate held, the gate acquired only
after it completes — §4) and the deploy/restart worker's post-operation
preflight (the same one-shot preflight — §7). Both are bounded one-shot
preflight command executions over the manager's transport that never initialize
a channel or touch a supervisor.
## 4. Host mutations: list, add, update, remove

All four are hub-side handlers registered like every other hub method (the
router-vs-catalog test pins registration), classified as mutations where they
mutate, admission-gated like the hub's other settings mutations, origin-guarded
per §3, with catalog entries and regenerated clients per the §2 table and the
exact shapes in §11.

`evener/host/list` is a read: params `{}`; response `{hosts: HostRow[]}`. It
never dials and never takes the mutation lock. Every configured host renders
its full effective `HostConfig` fields plus live state: `attached` (via
`sshManager.ChannelIfAttached(name)`), preflight facts when known (installed
version, OS/arch), last attach error, whether the manager is mid-`Ensure`, and
the origin marker (`hub.toml` vs sidecar — the effective source after merge).
08a extends the manager to expose the channel's pinned (generation,
incarnation id) beside the lookup; the row reports the channel's live state
only when that pair equals the registry snapshot's current pair for the name,
and otherwise renders from the (generation, incarnation id)-scoped last-known
store (§10) as detached/unknown. Last-known facts, their ages, and attach
errors come from that manager-owned store: the `ChannelIfAttached` lookup
reports only the current channel, so offline rows would otherwise go blank.
Tombstones appear as rows with `removed: true` plus their retained-row count —
one surface, no separate retained view — rendered from the tombstone's retained
effective `HostConfig` with `attached: false`, `midEnsure: false`, and the
removed entry's `origin`, `generation`, and `incarnationId`; the facts/error
optionals stay absent (never null) per the absent-when-unknown rule (§11).
`list` never prunes durably: expiry is in-memory filtering only (expired
tombstones are omitted from the response), while durable pruning of expired
tombstones lands on the mutation path — every sidecar mutation prunes expired
entries in its atomic write under the mutation lock — so the read-only
contract holds and `list` cannot contend with mutations.

Before serving any admitted call — `list`/`status` included — the hub compares
the on-disk `hub.toml` fingerprint against a cached fingerprint lock-free. On a
match the read serves immediately from the last published snapshot. On a
mismatch the read still serves immediately, but never the raw stale snapshot:
it synchronously filters the last published snapshot against the current
on-disk file state (a lock-free re-read of the file bytes' host set — names,
not content — plus a synchronous content compare for names present in both:
the re-read parses each still-present declared entry and compares its
effective-entry content hash against the snapshot's persisted per-name
fingerprint; a name whose content changed renders unavailable — its row omitted
from `list`, its `status` a typed changed-entry refusal directing the caller to
wait for the reconcile, never the old SSH/path config served as current), and
the reconcile is scheduled asynchronously (debounced — at most one reconcile
in flight, coalescing rapid successive edits). The async reconcile re-compares
fingerprints on completion: a still-present mismatch reschedules another pass
instead of settling, so a coalesced mid-flight edit is never silently lost. A
read arriving while a reconcile holds the lock serves the file-filtered
snapshot instead of waiting — no read ever fails busy for a fingerprint
reason. The "invisible by next read" guarantee applies to the file-filtered
read: a host deleted on disk is absent from the filtered view, and a host
whose entry content changed on disk renders unavailable, on the very next read
even while the full reconcile is still in flight; the async reconcile
converges the full snapshot (facts bindings, generations) behind the
already-correct admission view. A changed entry is invisible-by-next-read
exactly like a delete because serving the old entry would serve stale
connection data; the full reconcile runs async so reads never block on file
I/O or the mutation lock.

Read isolation: "no mutation lock" does not mean "no synchronization". Writers
publish two immutable snapshots through atomic pointers: under the mutation
lock every staged commit publishes a new registry+tombstone snapshot (entries,
tombstones, generations, high-water marks); under a small store mutex every
last-known-store update (facts refresh, attach outcome, worker publish)
publishes a new copy-on-write facts snapshot. `list` loads both pointers
lock-free and joins them in memory — each half is internally consistent, so a
concurrent swap can never tear a row — and cross-half skew is resolved by the
(generation, incarnation id) key (facts whose pair no longer matches the
snapshot's current pair for the name render absent, exactly as after an
update). `status` reads the same two snapshots for its single row.

`evener/host/add` is a mutation: params are one full host entry (all seven
`HostConfig` fields; `name` required) plus optional `mutationId`. It validates
with the component-03 rules (name validation, the `[[hosts]]` field validation
at `cmd/evener-hub/config.go:177`). It enforces the component-03 host-count
limit: at most 63 remote hosts (the `local` entry is the 64th manifest source —
one host over the cap fails navigation for the entire hub, not just the extra
host). The staged commit validates the merged post-change live set against the
cap under the mutation lock before the sidecar persist, and the same check runs
on the boot load and the registry `Add`/`Update` paths, so no sidecar or
runtime commit can push the registry over it. Over-cap is refused with the
typed `ErrTooManyHosts`; boot over-cap is a hard startup error naming both
sources and their counts that refuses to serve. `add` refuses any name already
declared in `hub.toml` or held as a live sidecar entry — the duplicate refusal
applies to live entries only: a name present solely as a tombstone is accepted
(re-add) past the remnant fence (a re-add naming a host with an open teardown
remnant is refused with `remnant-open` until `teardown-retry` completes that
generation's teardown — §6), and the same staged commit purges that tombstone
in its atomic sidecar write. A hand-created duplicate name across files at boot
is a hard startup error (§6). Surfaced validation errors are the dialog's
inline errors.

`evener/host/update` is a mutation: the target name MUST be a live sidecar
entry. A `hub.toml`-declared name is refused with the "edit the file"
explanation; a name absent from both files — or present solely as a tombstone —
is refused as not-found. Params are `{name, entry (the six non-name
HostConfig fields), mutationId, expectedGeneration, expectedIncarnationId}` —
the idempotency key and the (generation, incarnation id) guard, all three
required together. Presence of all three is validated before the dedup check
(missing any is a validation refusal committing nothing). The pair is checked
under the mutation lock against the target's current (generation, incarnation
id) before staging, past the dedup check — a replay never reaches it. A
mismatch on either is the typed `stale-entry` refusal committing nothing; the
UI re-reads the row and retries against the current pair. A delayed retry
carrying a pre-bump generation with no matching current-generation receipt is
refused the same way, so it can never overwrite fields an intervening update
changed. `update` is refused with the typed busy error while the host's
per-host gate is held (past the dedup check — a replay returns its receipt
without consulting the gate). It mutates every field except `name`: names are
immutable (they key source IDs, cached rows, manager state, and file entries;
renaming is remove + add). `update` never inserts a new name; only `add` can.
An update that changes what a live supervisor or channel is bound to rebinds
them to the new entry (or tears them down) as part of its staged commit —
commit first, then rebind/teardown, gate released last (§7). Every update
advances the host's registry generation and, as part of the same staged
commit, clears or (generation, incarnation id)-keys all name-keyed resolved
state — resolved deploy targets, deployment state, last-known facts entries,
supervisor bindings, and channel handles — before rebinding, so the next
operation can never serve the pre-update configuration. Required-key shape is
mandatory because optional fields make a first-time call indistinguishable from
a lost-response retry after re-add. A replay carrying a known key returns the
recorded receipt without re-applying.

`evener/host/remove` is a mutation: live sidecar entries only (same refusal
for `hub.toml` names as update; a name present solely as a tombstone is
refused as not-found, never re-removed). Params are `{name, mutationId,
expectedGeneration, expectedIncarnationId}` with the same required-together
presence, dedup ordering, under-lock pair check, and `stale-entry` refusal as
update. It is refused with the same typed busy error while the host's
per-host gate is held (past the dedup check); remove never waits and never
cancels, so its staged supervisor/channel teardown cannot race a push or a
`waitHealthy`. A refused remove leaves the in-flight operation to finish and
record its normal terminal state. A successful remove marks that host's
operation records `host-removed` (readable history, never a dedup match; the
mark lands with 08b's store — and a crash between the sidecar commit and the
mark has the mark reconstructed from the tombstone at boot — §7). Removing an
attached host stops its supervisor and detaches. The removed host's cached
snapshot rows are not silently dropped: the source's last-known-good snapshot
is retained as an explicit tombstone (§15), and the UI warns when the host has
live remote threads (host data on the remote is untouched). A lost-response
`remove` retry landing after a re-add minted a new generation and carrying a
fresh key refuses as stale instead of tearing down the new incarnation; a
retry carrying the original key follows the superseded-receipt rule (§5) and
returns the recorded `committed` receipt.

`evener/host/attach` is unchanged by this component: it wraps the dialing
closure, errors are classified through the #1603 attach classifier
(ssh-start/deadline chains map to typed `SessionUnavailable`, caller-context
cancellation stays raw), and it is idempotent.

`evener/host/status` is a read: params `{name}`; response is the host's `list`
row plus the deploy plan inputs — `controllerBuild`, `resolvedTargetPath?`,
`restartFollows?`, `factsRevision?`, `factsAgeSec?`, and `planRefusal?` (exact
shapes in §11). It never dials and mints nothing. Stale facts are marked with
their age. `restartFollows` and `planRefusal` render from the (generation,
incarnation id)-scoped running-state/refusal snapshots in the manager store
(§10), and stay absent when no snapshot exists for the current pair. The
deploy confirmation renders `plan`'s minted response, never `status` (§13).

`evener/host/plan`, `deploy`, `restart`, and `status` on a name present solely
as a tombstone — or absent from both files — return the same typed not-found as
`update`/`remove`, scoped to remnant-free tombstones only: where the name holds
an open remnant (exactly the state a failed `remove` leaves — tombstone plus
open remnant), the remnant fence (§6) is evaluated first and these methods
refuse with `remnant-open` naming the blocking `remnantId`, never not-found.
Only `list` surfaces tombstone-only names (as `removed: true` rows) and only
the `add` re-add path accepts them.

## 5. Mutation serialization, idempotency, and crash recovery

Host mutations are serialized by one process-wide lock. `add`/`update`/`remove`
hold it only across stage, persist, and swap plus the remnant/receipt state
transitions — released across post-commit teardowns and re-acquired to
finalize — and so does every other sidecar read-modify-write:
`teardown-retry`'s remnant clearance, retention-expiry pruning, and marker
finalization all run under the same lock for their transitions only, never
across a teardown. The lock is released across slow teardowns so one host's
teardown never blocks unrelated hosts. Lock order is fixed: the process-wide
mutation lock is outermost, the store mutex innermost (§7). Concurrent
read-modify-write on the sidecar cannot lose updates.

`add` accepts an optional opaque `mutationId`; `update`/`remove` require
`mutationId` with `expectedGeneration` plus `expectedIncarnationId` (§4) — a
keyless `update`/`remove` never stages. The staged commit persists a durable
mutation receipt in two writes — the single explicit receipt write point. The
step-(2) sidecar write carries a transient staged-receipt marker: the scoped
key plus `stagedAt`, a `swapStarted` intent (false at stage time, flipped true
in its own atomic sidecar write under the mutation lock before the runtime
transition begins; a finalizing claim carries it as true), a `teardownStarted`
flag (false at stage time, flipped true in its own atomic write after the swap
and before the first teardown; a finalizing claim carries the flag as true),
the collision-reconcile armed intent (the validation-read `hub.toml`
fingerprint the commit staged against; the post-commit write replaces the
marker with the finalized receipt, clearing the armed intent with it, and the
reconcile staging supersedes it with the re-read fingerprint when a race is
found — §6), and the staged provisional payload (explicitly provisional
outcome, row, generation, a pre-minted `remnantId`, and the pinned teardown
target — the in-progress remnant: the staged supervisor/channel/fan-out
teardown description for this commit, resolvable without the live entry; the
step-(2) write lands before the post-commit rebind executes the first
teardown, so the target and its remnant are durable before any irreversible
teardown destroys a handle). The markers form a map keyed by host name — at
most one staged entry per host, so a staged marker for host A never blocks a
mutation on host B, and every marker write preserves other hosts' entries
verbatim. The post-commit write replaces the marker with the finalized
mutation receipt: the scoped key, the outcome, the resulting row, and the
resulting generation (row schema in §6). A replay carrying a known key returns
the recorded finalized receipt without re-applying: a retried `add` cannot
duplicate, a retried `remove` cannot fail not-found, and a retried `update`
cannot double-apply or double-bump the generation.

A replay naming a still-staged marker never re-applies. While the original
commit holds the mutation lock the replay fails fast with the transient busy
form (retry with backoff). Once the lock is free and the marker persists — the
post-commit write failed or its response was lost while the process stayed
alive — the replay first reads the marker's persisted runtime phase and
`swapStarted` intent and finalizes by phase (recovery re-applies the staged
runtime set first so sidecar and runtime converge before any teardown destroys
a handle; the swap re-resolves idempotently, so an already-applied swap lands
on the same values):
- Phase `runtime-swapped` (or an unknown post-swap phase): re-run the marker's
  pinned teardown to completion (the pinned target is idempotent, so a
  teardown that already ran is a no-op and an interrupted one completes) to
  derive the real outcome and remnant, then finalize the receipt from that
  observed result in one atomic sidecar write and return the finalized receipt
  (`committed`, or `committed-with-teardown-failure` with the pre-minted
  `remnantId` when the re-run actually failed — never the staged provisional
  outcome on its own).
- Phase `staged` with `swapStarted: false`: finalize as `committed` with no
  remnant, without running any teardown (no runtime swap occurred, so there is
  no swapped-out lifecycle to tear down — running the pinned teardown here
  would destroy the still-live old runtime), but only after re-applying the
  staged runtime set to the live handles first (the sidecar already holds the
  new config, so a finalize that skips the swap strands the live process
  against the new durable state).
- Phase `staged` with `swapStarted: true` (ambiguous — the crash landed
  between the intent flip and the non-atomic runtime transition, so the swap
  may or may not have applied): re-apply the staged runtime set first, then
  re-run the pinned teardown to completion, then flip the phase to
  `runtime-swapped` and finalize the receipt from that observed result with
  the pre-minted `remnantId` when the re-run actually failed. Every ambiguous
  post-intent state keeps its pinned remnant, never a teardown-first mismatch.

Any mutation-path write that finds a marker it did not stage for its own host
finalizes that marker first — a marker for a different host rides along
untouched in the same atomic writes and never forces finalization, so
unrelated-host mutations proceed while serializing only same-host
teardown/finalize work plus the sidecar write itself. The pinned-teardown
re-run executes outside the mutation lock. The finder first atomically claims
the marker in one sidecar write under the lock — replacing the staged-receipt
marker with a finalizing claim carrying the same scoped key plus a
server-generated opaque attempt token. Any other path finding a finalizing
claim waits for or recovers the claim instead of re-finalizing; a live
claimant re-runs to completion before responding busy, finalizing by the
marker's persisted phase exactly like a live replay above (staged/unswapped
markers finalize without a teardown; swapped or ambiguous markers re-run the
pinned teardown). A dead claimant's claim (crashed or vanished holder) is
adopted by running the same phase-aware recovery under the same
generation/incarnation guards and finalizing under the claimant's attempt
token. The finder try-acquires the remnant's host gate after claiming and
before running the teardown — a held gate means a live committer still owns
the host, so the finder releases the claim back to the staged-receipt marker,
responds busy, and finalizes nothing; only a dead claim on a gate-free host is
adopted. The finder then releases the lock, runs the pinned teardown with no
lock held under the marker's generation/incarnation guards, and re-takes the
lock to persist the finalized receipt plus real remnant (verifying the attempt
token still owns the claim), so the foreign commit is durably recoverable
(receipt plus real remnant) before the new mutation stages. A live process
never accumulates an orphaned marker, and `list` already shows the committed
row (disk holds the entry), so read-after-unknown converges even before
finalization lands. The claim plus attempt token plus host-gate try-acquire
prevents double-finalize and concurrent-teardown races between committer,
foreign mutator, and `teardown-retry`.

Crash-window recovery: the step-(2) write persists a `teardownStarted` flag
with the marker (false at stage time), and the commit flips it to true in its
own atomic sidecar write after the swap and before the first teardown executes
— under the mutation lock, before the mutation lock is released across
post-commit teardowns — so the first teardown runs only after that flip is
durable. The staged sidecar write additionally persists a runtime phase with
the marker (`staged` at stage time, flipped to `runtime-swapped` in the same
atomic write that flips `teardownStarted` after a successful swap). A marker
found in phase `runtime-swapped` (or in an unknown phase that follows the
swap) recovers with the pinned teardown target even when `teardownStarted`
reads false — the swap may have applied — while only a phase-`staged` marker
with `teardownStarted: false` AND `swapStarted: false` finalizes without a
remnant; every other post-intent state keeps the pinned remnant. Boot
finalizes each host entry by the flag (a leftover finalizing claim counts as
teardown-started: the claim write sets the flag true, since a live claimant is
about to run the teardown under the claim): a marker with `teardownStarted:
false` AND `swapStarted: false` finalizes as `committed` with no remnant —
after re-applying the staged runtime set to the live handles first (no swap
ran, so there is no swapped-out lifecycle to tear down — but the sidecar
already holds the new config, so the live runtime must converge to it before
the receipt finalizes) — and the receipt records `bootRecovered: true` (the
optional receipt field in §6); a marker with `teardownStarted: true` recovers
as a durable teardown remnant — the pinned target plus the pre-minted
`remnantId` become the remnant record, the receipt records
`committed-with-teardown-failure` with that `remnantId` plus `bootRecovered:
true`, and the operator resumes through `evener/host/teardown-retry` — so a
lost-response retry after the crash returns the recovered receipt or the retry
handle instead of re-applying, and a crash after teardown began never loses its
repair target. Only staged-plus-unswapped-plus-unbegun finalizes without a
remnant; every other post-intent state keeps its repair target so post-teardown
crashes never lose the handle. Compensation's stash-restore removes the marker
with the prior bytes — pre-commit only: once the first teardown executes, the
commit-point rule (§6) applies and the in-progress remnant is the forward-repair
handle, never a stash restore.

Receipt scope: the dedup key is (mutationId, host name, mutation kind, host
generation at commit — the resulting post-commit generation: for `update`, the
post-bump value the same commit advances to, pinned into the receipt at stage
time, so a commit-then-replay names the generation the commit actually landed
and hits instead of missing as superseded — plus the incarnation id minted
beside that generation in the same atomic sidecar write), mirroring the
operation store's (host, kind, client operation ID, generation) scope plus the
same incarnation id. Including the post-commit generation plus incarnation id
keeps re-adds sharing a generation across incarnations from colliding or
authorizing work against the live entry. A replay matches only a retained
receipt of the same name, kind, and mutationId. One rule, three arms: the
first arm is the direct hit — same name, kind, mutationId, current generation,
AND current incarnation id — returning the recorded receipt; the second arm is
the superseded hit — the same key pinned to a superseded generation or a
different incarnation sharing the generation — returning the recorded outcome
for recovery only, never authorizing work (a lost-response `remove` retried
after a re-add recovers its outcome rather than tearing down the new
incarnation, including the shared-generation case: a same-key receipt pinned
to the open remnant's incarnation returns its outcome against a live entry
sharing the generation but carrying a different incarnation id, never a hit
authorizing work against the live entry); the third arm is the pruned refusal
— the same key whose receipt is gone with the count/TTL prune or dropped with
the tombstone purge (the purge persists a marker for the dropped same-key
receipts — §6) — refusing as `stale-entry`, never fresh-applying. A
`mutationId` colliding with a current-generation receipt of a different name or
kind is refused with the typed `conflicting-mutation-id` error — never a hit,
never a re-apply under the colliding key. A key with no current-generation
receipt commits fresh only when its `mutationId` matches no retained receipt
for that (name, kind) — a genuinely new mutation under a fresh key (a fresh
key after the purge commits fresh by the same rule). A post-purge same-key
replay refuses stale on the purge-persisted marker exactly like the
count/TTL-pruned case — the old key stays fenced against destructive re-apply
while new keys stay open, so the clean-slate path holds for new keys only.

A mutation sent without a key whose response is lost reconciles
read-after-unknown through `list` before any retry — `add` compares the listed
entry hash for the name, `update` compares the listed effective row only to
observe its intended row (a keyed `update` replay returns its receipt by the
superseded-receipt rule; a missing name or a `removed: true` row means the
keyed remove committed). The keyless-retry rule covers `add` only: a retry
that carries no `mutationId` never carries `expectedGeneration` either —
without it a concurrent update between the `list` read and the retry is
undetectable, so keyless `add` retries are non-retryable as guarded updates
and the stale-entry guarantee covers keyed retries only (`update` and `remove`
require all three fields server-side and take no keyless path; the UI always
sends `mutationId` with `expectedGeneration` and `expectedIncarnationId`,
required together — a keyed replay returns the receipt before the stale check;
a caller-constructed keyless `expectedGeneration` carries no receipt to recover
and is a validation refusal, never a guarded update). Read-after-unknown
compares only the effective `HostConfig` fields plus `generation`/`origin` —
never volatile live state (`attached`, `midEnsure`, `lastAttachError`) or age
counters (`installedVersionAgeSec`, `lastAttachErrorAgeSec`,
`escalationAgeSec`): once the intended effective row (for a lost-response
keyless `add`: the listed entry hash equals the intended entry; for `remove`:
a missing name or a `removed: true` row — a keyed `remove` retry returns its
receipt first) is observed, the retry treats the mutation as committed and
does not retry at all — any intervening change to an effective field breaks
the equality and forces the `stale-entry` path instead of guard omission. A
keyless `add` retry that has not yet observed its intended row re-reads `list`
and retries without `expectedGeneration` only while the mutation is still
uncommitted — the row still absent — so retries converge without
double-applying. `update` and `remove` take no keyless path at all: a
lost-response `update`/`remove` retry always carries its original key and
follows the single superseded-receipt rule, an `update`/`remove` carrying a
fresh key against a superseded generation or a superseded incarnation refuses
as `stale-entry`, and no unkeyed `update`/`remove` ever stages against a live
incarnation.

Processing order is fixed: receipt dedup by (mutationId, name, kind, current
generation, current incarnation id) runs first for every
`add`/`update`/`remove` — subject only to `update`'s and `remove`'s
parameter-presence gates (missing `mutationId`, `expectedGeneration`, or
`expectedIncarnationId` is a validation refusal before any dedup lookup). A
dedup match returns the recorded receipt with no `expectedGeneration`/
`expectedIncarnationId` value check, gate, or remnant validation; only a
non-replay proceeds into those checks. Guard-before-admission (§3) outranks
dedup-first: dedup-first applies only post-admission among handler stages.
## 6. Persistence: sidecar, commit, remnants, retention

Persistence target: the controller's `hub.toml` is hand-authored with
comments; a TOML re-marshal would strip them. The UI writes a managed sidecar
in the same config dir (e.g. `hub.hosts.json`), loaded after `hub.toml`. The
sidecar is the UI's only writable source. Config-path retention: the hub
supports `--config` paths (`cmd/evener-hub/main.go:186` —
`deps.loadConfig(opts.configPath)`), but neither the runtime `Config` nor
`WebConfig` retains the selected path, so 08a carries the canonical config
path (absolute, resolved at startup) through startup into the web
configuration; both the sidecar path (same dir as the selected `hub.toml`) and
the `hub.toml` fingerprint bytes (read from that same path at plan/mint,
deploy/validate, and mutation stage/final-check time) derive from it — UI
mutations against a `--config` hub land beside the selected file and
invalidate against the same file. File posture: the config dir's private state
holds mode `0600` for the sidecar and the operation store — temp files created
`0600`, atomic renames preserving the mode, and startup validation refusing to
load a sidecar/store readable beyond its owner (the bearer confirmation tokens
persist in the store). Every sidecar, receipt, remnant, cleared-marker, and
stash write is temp-file + file-fsync + rename + parent-dir-fsync — the temp
file is fsynced before the rename and the containing directory is fsynced
after it, so the canonical file always holds either the complete old or the
complete new bytes. An fsync failure before the rename aborts the write with
the old file intact (the mutation reports the failure and commits nothing); an
fsync failure on the directory after the rename is a crash-window boot
reconciliation (the rename landed but durability is unproven — boot re-reads
and schema-validates before serving, taking the same hard-startup-error path
as a corrupt file). The same pair covers the stash write, the stash restore
rename, the `pendingCompensation` record, the purged-row re-insert, and the
compensation-record clear — every durable step of the cross-file protocol (§8).

Merge rules: `add`/`update` refuse any name already declared in `hub.toml`
(the file is authoritative for its own names); `remove` deletes only sidecar
entries; a `hub.toml`-declared host can never be shadowed or UI-removed — its
`list` entry says "declared in hub.toml". The refuse rule is enforced at write
time AND the boot merge rejects the hand-edited collision. Every sidecar
mutation re-reads the current `hub.toml` bytes under the mutation lock and
validates the staged change against them — reusing the same `hub.toml`
content-hash fingerprint the plan/deploy path binds into confirmation tokens
(§8), or an equivalent fingerprint comparison: if the file changed since
startup (or since the last mutation), a staged name now colliding with a live
`hub.toml` entry is refused, and a collision already persisted into the
sidecar by an earlier racing edit is rebased (the sidecar live entry dropped,
the mutation retried against the re-read file) rather than committed as a
duplicate. Final check: between staging the new sidecar bytes and the atomic
rename — still under the same mutation lock — the commit re-reads the
`hub.toml` bytes once more and compares the content-hash fingerprint against
the validation read; if the file changed in between, the staged bytes are
discarded and the stage-validate sequence retries against the new file
(bounded retries, then the conflict-class envelope discriminator
`concurrent-edit` — data carries the `hub.toml` fingerprint the commit staged
against plus the fingerprint observed at refusal, so the client can re-read
and retry). The re-read plus rename is not an atomic compare-and-swap, so the
guarantee is reconciliation, not prevention: after the rename the commit
re-reads `hub.toml` once more, and if the file changed across the rename, the
mutation reconciles forward — a newly colliding live entry drops the committed
sidecar duplicate in a follow-up atomic write under the same lock AND
immediately rebuilds plus applies the merged runtime host set (registry,
manager bindings, sources, controllers — the step-(3) swap with the re-read
values, not a deferred pickup), so disk and live state agree before the
mutation returns; other changes are picked up by the fingerprint-bound
invalidation at the next mutation or token validation. When the post-rename
reconcile drops the just-committed sidecar entry as the colliding duplicate,
the mutation's finalized receipt and response describe the authoritative
`hub.toml` result, never the staged-then-dropped entry: the persisted receipt
carries the explicit `collision-dropped` outcome (the staged mutation
committed, then lost to the authoritative file in the same call — the receipt
names the winning `hub.toml` fingerprint plus the dropped staged entry), and a
replay carrying the same key returns that `collision-dropped` receipt, so a
suppressed retry can never read it as a live commit.

A name found in both `hub.toml` and the sidecar's live entries at load time is
a hard startup error naming both locations — UNLESS the collision is covered
by a managed sidecar reconcile marker the previous process left behind. The
reconcile write above records that marker (the re-read `hub.toml` fingerprint
the reconcile is running against plus the sidecar live entry it is about to
drop) in the same atomic sidecar write that stages the follow-up cleanup — the
re-read fingerprint is durable before the cleanup rename lands, never after
it — and the commit armed the intent one write earlier (the step-(2) staged
write carries the validation-read fingerprint — §5), so a crash between the
commit rename and the reconcile staging write is covered too. When boot sees
both live entries AND a reconcile marker whose fingerprint matches a re-read of
the on-disk `hub.toml`, boot completes the interrupted cleanup (the follow-up
drop plus runtime rebuild) instead of the hard error — and with no marker at
all, boot still completes the cleanup against the on-disk file (clearing the
armed intent in the same write) when the staged armed intent is present and
the on-disk fingerprint differs from the armed validation fingerprint (the
commit raced a `hub.toml` change across its rename; a concurrent hand edit
races identically and reconciles identically: `hub.toml` wins, the sidecar
duplicate drops). A collision with no marker and no armed intent, or with a
marker whose fingerprint no longer matches the on-disk file (a further hand
edit superseded the interrupted reconcile), or with an armed intent whose
validation fingerprint still matches the on-disk file (no race crossed the
commit — the duplicate is hand-made), stays the hard startup error — the
marker plus the armed intent scope the recovery to exactly the race the spec
reconciles, never a hand-created duplicate.

The sidecar file also carries the tombstone records (§15), so a removal's
entry-delete plus tombstone-write is one atomic write and the tombstone set
cannot diverge from the host set; a tombstone whose name matches a live host
at boot is discarded — the live host wins. A sidecar that fails schema
validation or is corrupt at boot is a hard startup error naming the file (the
same posture as the duplicate-name collision; recovery: fix or delete the
file; only sidecar state is lost). The sidecar file also carries the mutation
receipts and teardown-remnant records: `mutationReceipts` maps the scoped
receipt key (mutationId, host name, mutation kind, post-commit generation,
incarnation id — §5; a lookup compares all five) to `{outcome, row,
generation, incarnationId, committedAt, droppedEntry?, winningFingerprint?,
remnantId?, remnantResolvedAt?, bootRecovered?}`.
`droppedEntry` (the staged entry the post-rename reconcile dropped) and
`winningFingerprint` (the winning `hub.toml` fingerprint) are present exactly
on `collision-dropped` receipts — a replay renders the dropped arm from these
receipt fields, so a lost-response retry, even after restart, reconstructs
both what was dropped and which fingerprint won. `remnantId` is present
exactly when the commit staged a remnant, `remnantResolvedAt` exactly after
`teardown-retry` resolves it, `bootRecovered` (as `true`) exactly when boot
finalized a crash-window staged-receipt marker (§5). `prunedReceipts` maps the
full pruned scope key (mutationId, host name, mutation kind, pruned post-commit
generation, pruned incarnation id) to `{prunedAt}` — the bounded markers the
count/TTL compaction persists, riding the same atomic writes and the same
hard-startup-error posture. `teardownRemnants` maps the server-generated
opaque `remnantId` to `{host, kind, seam, pendingTeardown, generation,
incarnationId, mutationKey, committedAt, cleanupHandle}`; `cleanupHandle` is
the independently actionable ownership/remote-cleanup handle persisted at
commit, resolvable without any live in-process handle. A cleared remnant
persists as the cleared-remnant marker `remnantId → clearedAt` in the same
section, purged only by the name's next re-add or the retention-expiry prune.
`pendingTeardown` is the self-contained generation-scoped teardown target —
the staged supervisor/channel/fan-out teardown description pinned at commit,
resolvable without the live entry; a remnant never depends on the live
registry to execute. The commit, the `teardown-retry` clearance, and the
re-add purge are all single atomic writes with the same hard-startup-error
posture on corrupt or schema-invalid content.

Cleared-remnant markers carry their own bounded retention independent of
tombstones (owner-set cleared-marker TTL — a live host's repaired failures
create no tombstone, so without this the markers accumulate forever): every
boot and every sidecar mutation compacts cleared markers past the TTL in the
same atomic write, and lost-response retries past the TTL read as
`teardown-unknown-key` not-found instead of `already-cleared`.

Retention: receipts and remnants are per-host and per-generation — re-add
purges that name's superseded-generation receipts and — only after that name's
open remnants are resolved (remnant fence below) — its stale remnants are
dropped in the same atomic write that mints the new generation, so the
sections stay bounded by the live host set plus at most one superseded
generation per name for removed names. Active (live, never-removed) hosts
compact superseded receipts the same way: the same atomic sidecar write that
finalizes a mutation receipt drops that name's receipts pinned to earlier
generations — the live set keeps the current generation's receipts (at most
one finalized receipt per (mutationId, kind) at current generation) plus at
most 8 newest same-key superseded receipts per name (owner-adjustable count
bound; a same-key superseded receipt older than the owner-set superseded-receipt
TTL compacts the same way) — so repeated updates cannot grow the sidecar
without bound. The newest same-key superseded receipt per name is retained
until that name's tombstone expires (never pruned by the count/TTL bound while
the tombstone lives); a same-key `remove` retry naming a count/TTL-pruned
generation therefore never fresh-applies against the re-added incarnation —
the UI's (`expectedGeneration`, `expectedIncarnationId`) guard is the first
line, this refusal the backstop. Every count/TTL compaction that drops a
superseded receipt persists a bounded pruned marker keyed by the full receipt
scope (markers carry the scoped key only, no row bytes, so each stays small)
compacting under its own owner-set bound — at most 64 newest markers per live
name plus a marker TTL in the same owner-knob family as the superseded-receipt
TTL (every sidecar mutation and every boot compacts markers past either bound
in the same atomic write; defaults ship in the implementing PR). The backstop
marker's twin for a tombstoned name is exempt from that bound — that one
`remove`-retry backstop marker per tombstoned name persists until the tombstone
purge (re-add past the remnant fence, or retention expiry) — so no marker cap
drops the live backstop early; a marker dropped by the count/TTL bound for a
tombstone-less name removes only the oldest non-backstop entries. A replay
naming a marked key refuses as `stale-entry` (pruned-generation); a key with
neither a retained receipt nor a pruned marker commits fresh by the
commits-fresh clause. The tombstone purge is clean-slate: once the tombstone
(and that name's receipts with it) is gone, the purge still persists a bounded
pruned marker for that name's dropped newest same-key superseded receipts (the
same marker shape as the count/TTL compaction, keyed by the full receipt
scope), so a same-key replay after the purge refuses as `stale-entry`
(pruned-generation); a key with neither a retained receipt nor a live marker
commits fresh only when no marker survives for it, which after a purge holds
for fresh keys but never for the purged same-key replays the markers name; a
cross-name replay after the purge commits fresh. While a tombstone is retained
its name's receipts stay readable for post-remove forensics; purging the
tombstone drops that name's receipts and remnants with it. Retention expiry
never purges a tombstone whose name still holds an open remnant (§15).

Remnant fence: an open remnant is a host-wide fence for every lifecycle and
attach operation on the name until teardown succeeds — a new incarnation must
never start while the old lifecycle still owns supervisors, channels, or
fan-outs. Any non-replay mutation (a same-key replay matching a
current-generation receipt returns before this fence — §5) that would advance
or remove the affected name while it holds an open remnant — re-add, `update`
of the remnant's name, and `remove` of the remnant's name — is refused with the
typed `remnant-open` conflict refusal carrying the blocking `remnantId`. The
same open remnant fences every other lifecycle and attach path on the name:
`deploy`, `restart`, and `Ensure`-triggered work refuse with the same typed
`remnant-open` refusal (never the gate-busy form — the fence names the
blocking `remnantId`, not an in-flight operation), `plan` returns its no-token
shape with a `remnant-open` refusal naming the `remnantId` (never a minted
token bound to a lifecycle whose teardown is still pending), and attach
(`attachUnderGate` reattach/attach-first and the Connect path) refuses the
same way — the operator first resumes the named teardown through
`evener/host/teardown-retry` (which runs it to completion for that
generation), and only then does any fenced path proceed: re-add mints the new
generation and purges the cleared remnant. The remnant fence takes precedence
over the tombstone not-found rule (§4): a tombstone-only name WITH an open
remnant refuses `remnant-open`, never not-found — the not-found arm covers
remnant-free tombstones only. Open remnants pin their generation: while a
remnant is open for a name, no path advances that name past the remnant's
generation (the name stays tombstoned until the remnant resolves), and a
boot-merge collision involving a remnant-gated name leaves the open remnant
resumable by `remnantId`: the boot-collision above-mark bump is forbidden
while a remnant is open for that name — boot keeps the live entry at the
remnant's generation (no carve-out) until `teardown-retry` resolves it, so no
live incarnation is ever created over an open remnant and no mutation strands
a remnant by opening a second one for the same name.

Incarnation-scoped teardown: `teardown-retry` executes ONLY the remnant's
pinned teardown target — the staged supervisor/channel/fan-out teardown
description captured at commit, bound to the remnant's generation — and never
the name's live entry, live channel, or live supervisor set. Every lifecycle
handle the retry can touch carries a generation tag AND a persisted
incarnation identifier: the incarnation id is minted fresh on every
`add`/re-add in the same atomic sidecar write that mints the generation,
persisted per live entry alongside it (never derived from the generation,
never reused across incarnations even when generation numbers collide), and
copied into the remnant's pinned teardown target at commit; handles
(supervisor binding, channel handle, fan-out subscription) are tagged at bind
time with both values. Generations can collide across boot-merge, so handles
carry the never-reused incarnation id targeted by equality. The retry targets
handles by incarnation-id equality — never by generation alone and never by
name lookup: a name-based lookup resolving to a handle whose incarnation id
differs from the remnant's is refused, never executed, even when its
generation tag equals the remnant's. Post-crash rehydration: boot rehydrates
the remnant's actionable handle by loading the persisted `cleanupHandle` —
rehydration is record load, never live-handle resurrection (no in-process
handle survives restart — §7): for remote seams the handle is the remote
guard-file identity plus the orphan epoch's lease-entry ownership tokens, so
the retry kills and verifies through the lease wrapper without the dead
worker's handles; for local seams it is the durable local ownership-boundary
identity plus nonce (the same boundary-plus-nonce rule as orphan reaping —
§9), so boot reaps or the retry signals through the persisted boundary. The
retry re-resolves live bindings by (generation, incarnation-id) equality only
while the committer is still alive; after a crash it acts through the
`cleanupHandle` alone. A retry executing a boot-recovered remnant additionally
runs as a fenced remote operation: it persists a fresh fencing epoch in the
remnant record, kill/waits the superseded epoch's lease-tracked entries,
compare-and-advances the guard, and only then runs the pinned teardown through
the wrapper — so post-crash teardown repair can never mutate past a
still-running crashed-epoch orphan.

Staged commit (order matters): add/update/remove never mutate live state
incrementally. Holding the process-wide mutation lock only across the
transitions below — released across post-commit teardowns, re-acquired to
finalize (the same pattern as foreign-marker finalization and
`teardown-retry`, which never hold the lock across a teardown — the lock
serializes sidecar read-modify-write only, so an unrelated host's mutation
proceeds past a foreign-host marker, serializing only same-host
teardown/finalize work and the atomic file write): (1) stage the complete
change — new registry value, the *description* of the manager deltas
(including the planned supervisor/channel teardown for removals),
source-registry rows, host-admin-controller host set and its notification
fan-outs, web-config host view, and the new sidecar bytes; (2) persist the
sidecar first — stashing a durable copy of the prior sidecar bytes (including
the decision-source validation state — the archive/favorite host-set
acceptance rewired from the startup `RemoteHosts` snapshot to the live set,
preferably through a live-registry callback, so newly added hosts validate and
removed hosts stop validating as part of the same swap; same config dir —
same file posture as the sidecar: stash temps created `0600`, temp-file +
file-fsync + rename + parent-dir-fsync preserving the mode, startup refusing a
stash readable beyond its owner, and stray/expired stash files pruned or
ignored at boot) before the atomic rename, so the swap is compensable; (3)
persist the swap-started intent, then swap the runtime to the staged set — the
step-(2) staged sidecar write carries a durable `swapStarted` intent (false at
stage time, flipped to true in its own atomic sidecar write under the mutation
lock BEFORE the non-atomic runtime transition begins — a durable record now
exists on both sides of the transition) — then rebinding or wiring the live
handles to the new values (flipping the staged phase to `runtime-swapped` as
that transition lands), and only then executing the planned teardowns
(supervisor/channel stops, fan-out cancellations) as the post-commit rebind
phase (§7) with the lock released across the teardowns and re-acquired to
persist the committed receipt plus real remnant (verifying the claim's attempt
token still owns the marker); (4) if the swap itself fails, compensate fully
before responding: restore the prior sidecar bytes from the stash (atomic
rename), then revert the runtime to the previous set, then report exactly
which step failed — a typed swap-failure response is sent only after both
restores. The commit point is the start of the post-commit rebind phase: once
the first planned teardown executes, the mutation is committed and there is no
compensation path back — a failure at or after the commit point is reported as
a committed-with-teardown-failure with the seam named, and recovery is forward
(retry the teardown / re-apply), never a restore of the prior bytes — through
the teardown-repair operation, never a blind full-mutation retry (replaying
the original mutationId stays a no-op receipt return, so the API has a repair
path that is not the replay). Restoring bytes after a teardown cannot rebuild
destroyed handles, so compensation covers pre-commit failures only. The same
atomic sidecar write that persists the committed receipt also persists a
durable teardown-remnant record — the mutation's scoped receipt key plus the
in-progress remnant already pinned in the step-(2) marker — the named pending
teardown (handles, seam, generation, incarnation id, cleanup handle) plus a
server-generated opaque `remnantId` (non-empty, at most 128 bytes, unique per
remnant — never derived from the mutationId, so an ID-less failure still gets
a retry handle and a reused mutationId can never collide with an earlier
remnant) — and every committed-with-teardown-failure response carries that
`remnantId` alongside the committed receipt. A crash at any point of the
commit (including mid-compensation) is reconciled at boot — the disk wins —
while a typed failure never diverges from what a restart would apply. Within a
live process, staging failures change nothing. The stash is deleted once the
commit reaches either outcome; a stash left by a crash is ignored (safe to
prune) at boot — EXCEPT a stash named by a live `pendingCompensation` record's
stash reference (§8): that stash is the compensation's restore source, applied
in phase order (sidecar first, rows second) before any prune.

Per-host lifecycle handles: every live host owns cancellable handles — its
remote-source subscription, its host-admin fan-out (request forwarding plus
notification fan-out), and its supervisor/channel binding — started when the
host is added and cancelled/drained as part of the post-commit rebind phase on
update/remove, before the host's registry entry is considered gone. Rollback
(step (4) above) reverts to the prior handle set with the prior runtime, so a
compensated mutation leaves no server-lifetime goroutine retrying or emitting
for a removed host.

Hot-apply mechanics touch the seams built once at startup today:
`hostRegistryEntries(cfg)` → `hostreg.New` → `sshconn.New` → the
`RemoteHost*` fields in `hubcore.WebConfig` (`main.go:405-488`),
`newHubSourceRegistry`, and the host-admin controller (07a), whose per-host
request forwarding and notification fan-outs are initialized from the startup
snapshot — they must consume the live host set and start/stop fan-outs as part
of the staged commit (and its rollback — the stashed prior sidecar bytes
included). The live host-set surface (registry `Add`/`Update`/`Remove` with
the existing cycle and validation rules, manager add/update/remove safe
against in-flight `Ensure` and running supervisors) is 08a's core work.
Decision-source validation (the archive/favorite `validateDecisionSource`
host-set check, which today walks the startup `cfg.RemoteHosts` snapshot)
consumes the live host set as part of the swap — preferably through a
live-registry callback — so a newly added host is accepted and a removed host
is refused immediately after the commit; 08a tests pin both directions at that
boundary.

The new `evener/host/teardown-retry` mutation (params `{remnantId: string}`,
response the outcome union in §11) resumes ONLY that named teardown: it looks
up the remnant by the opaque `remnantId` (unknown or purged ID → typed
`teardown-unknown-key` not-found; a cleared-remnant marker still present
returns the `already-cleared` success arm, never not-found — the remnant
carries its own pinned teardown target, so lookup never requires a current
live entry and later mutations cannot strand it), try-acquires the host's
per-host gate (held → typed busy, same classes as `restart`), and holds the
process-wide mutation lock only for the marker/remnant/receipt state
transitions — never across the teardown itself (the same claim pattern as the
foreign-marker rule: claim under the lock with an attempt token, release
across the teardown, re-acquire to finalize). It runs the remnant's pinned
teardown to completion with no mutation lock held — through the persisted
`cleanupHandle` after a crash, under a fresh fencing epoch (including its
bounded kill/wait contexts; the retry's own teardown run carries a bounded
execution deadline of the same owner-set family: on timeout the retry reports
the terminal `committed-with-teardown-failure` outcome with the remnant still
open for a later retry — a stuck remote process therefore surfaces a terminal
outcome with a live retry handle, never an indefinitely held gate). The retry
validates against the remnant's OWN pinned identity, never against the
registry's current values: it re-resolves the pinned teardown target by the
remnant's recorded `(generation, incarnationId)` plus its persisted
`cleanupHandle` — the live entry for the name may be absent (post-remove) or a
newer incarnation (post-update) without blocking the retry — and refuses only
when the remnant's own pinned target fails to resolve through its own handle
(typed `teardown-unknown-key`), never because the registry moved on. It then
re-takes the mutation lock and clears the remnant in one atomic sidecar write
— the same write records the remnant resolution on the original mutation
receipt (outcome becomes `committed` with `remnantResolvedAt`) — and returns
the live row. A replay of the original mutationId after resolution returns the
resolved receipt, never the stale committed-with-teardown-failure. The retry
is idempotent by remnantId: a retry naming an already-cleared remnant returns
the already-cleared success arm, a receipt-returned no-op — never a second
teardown, never not-found. The response carries the stable
`committed-with-teardown-failure` outcome naming the seam AND the `remnantId`,
and the UI renders the committed host row with a teardown-retry affordance —
never a generic failure affordance, never a blind full-mutation retry.
## 7. Operation store

Deploy and restart take minutes and MUST NOT hold an AppWire RPC open. The
existing host-notification stream carries remote configuration notifications
and the existing jobs surface is session-scoped, so neither can carry durable
controller-side operation records; this component defines a controller-side
durable operation store.

Record: operation id (controller-assigned), client operation ID (opaque —
non-empty, at most 128 bytes, no required internal structure), host, kind
(`deploy`/`restart`), state (`pending`/`running`/`complete`/`failed`/
`interrupted`/`orphan-unverified`), the persisted orphan boundary identity —
the per-member `BoundaryEntry[]` array (§11; present exactly on
`orphan-unverified` records — persistence and wire carry the same array, never
a single object), progress entries (timestamped, bounded), terminal result,
timestamps, the pinned host generation (the incarnation the record ran against
— part of the dedup scope — plus the pinned incarnation id minted beside that
generation), and a `host-removed` mark (set when the host is removed — live at
remove time, or reconstructed from the sidecar tombstones at boot; §10 —
still-readable history that never matches a dedup lookup either way). Client
operation IDs deduplicate on (host, kind, client operation ID, host generation
at record creation, incarnation id at record creation), never the ID alone:
records pin the generation of the incarnation they ran against, and a dedup
lookup matches only records whose generation equals the registry's current
generation for the name AND whose incarnation id equals the live entry's — a
re-add starts its new generation with a clean dedup slate, so client
operation-ID reuse after a remove/re-add cycle opens a fresh operation instead
of colliding with the removed incarnation's history. Precedence is
generation-first: the dedup/conflict scan ignores records pinned to a
superseded generation entirely, and the `host-removed`-record conflict rule
applies only to records of the current (live) generation. A same-key replay
returns the existing record; an ID colliding with a current-generation record
of a different host or kind — or with a current-generation `host-removed`
record — is refused with the typed conflicting-operation-ID error, so a reused
ID can never hand back an unrelated operation's progress while the caller's
actual operation never starts; an ID whose only matches are
superseded-generation records (including `host-removed` records of a removed
incarnation) opens fresh. Pinning (generation, incarnation id) keeps
client-ID reuse after remove/re-add from returning an unrelated operation's
progress.

Durability: every operation-store write is atomic (temp + rename + fsync),
and token consumption and record creation are one such write (§8). A corrupt
or schema-invalid store file at boot quarantines (renamed aside with the boot
timestamp, never deleted) and the store starts empty with zero outstanding
tokens plus an operator-visible health signal naming the quarantined file —
and the quarantine mints a durable store epoch (a `quarantineEpoch` counter
persisted outside the quarantined file, in the sidecar-adjacent state meta
the replacement store reads at boot, advanced by exactly one per quarantine —
never reset, never derivable from the replacement store's own `compactSeq`,
which restarts at zero): the replacement store opens at epoch + 1 with reset
row IDs and `compactSeq` from zero, and cursor validation compares the
cursor's pinned store epoch against the live epoch first — a pre-quarantine
cursor (any epoch below the live one) is a typed `stale-entry` re-list
refusal, never an admission against the replacement store, so pre-quarantine
positions can neither skip nor misorder replacement-store rows. History is
loss-tolerable but bricking all hosts over bit-rot is not, so the posture is
quarantine-and-serve. File posture (bearer tokens persist in the store): the
store lives in a private state dir; the store file and its temp files carry
mode `0600` — replacements preserve the mode, and startup validation refuses
to load a store readable beyond its owner.

State-transition sequence: every atomic store write that moves a record into
a terminal state (`complete`/`failed`/`interrupted`, or the
`orphan-unverified`→`interrupted` resolution) also advances a durable
monotonic per-store state-transition sequence, persisted in the store file in
the same write, and stamps the transitioned record with the sequence value it
advanced to. The `plan`/deploy-step-(3)/`restart` race scans compare sequence
values only — the worker records the store's current sequence before its
gateless probe/resolution, and under the gate refuses `stale-entry`
(`concurrent-terminal-op`) when any record for the host carries a terminal
transition sequence above the recorded position — never a wall-clock
comparison against `updatedAt`, so a rollback cannot hide a concurrent
terminal operation. `createdAt`/`updatedAt` stay display-only timestamps and
never decide a race scan.

Retention and compaction: the store keeps at most 50 terminal records per host
(tunable owner knob; the default ships in the implementing PR) plus every
non-terminal record regardless of count; exceeding the cap compacts
oldest-terminal-first in the same atomic write that lands the new terminal
state, so a unique-operation-ID stream cannot grow the file without bound.
Compaction leaves a bounded dedup tombstone per compacted record — the client
operation ID with its (host, kind, generation, incarnation id) scope plus the
full replay fields — the controller-assigned record `id`, the bounded
`progress` entries, `createdAt`/`updatedAt`, the `hostRemoved` mark, the
recorded terminal outcome and `result`, and `compactedAt` — the marker IS the
compacted record's source, so no separate compacted-operation response shape
is needed — at most 50 tombstones per host (the same owner knob family as the
terminal-record cap; the default ships in the implementing PR), oldest-first
past the bound: a replay naming a tombstoned ID returns the full retained
record — controller `id`, client operation ID, host, pinned (generation,
incarnation id), kind, terminal state, retained `progress`, `result`,
`createdAt`/`updatedAt`, `hostRemoved` — with `compacted: true` instead of
opening a fresh operation, and only when the tombstone's pinned (generation,
incarnation id) still equals the registry's current pair for the name: a
tombstone pinned to a superseded generation or incarnation (a remove/re-add
cycle superseded it) never replays — the ID is reusable under the clean-slate
re-add rule above, which wins over the tombstone — and compaction never
touches `host-removed` marks of retained records. A lost-response retry
returns the completed record instead of silently starting a duplicate
destructive deploy/restart. Only past the tombstone bound — the documented,
owner-visible horizon of the lost-response retry contract — does a replay open
fresh. Every compacting write advances a durable monotonic compaction sequence
(`compactSeq`, persisted in the store file) — the pagination cursor pins it
(§11), so a mid-pagination compaction is detectable instead of silently
shifting later pages.

Store-wide serialization: atomic writes alone do not serialize
read-modify-write. Every operation-store read-modify-write path — token mint,
token validate-and-consume, record create/update, and any dedup lookup that
leads to a write — holds one store-wide mutex across the read and the atomic
write (or runs inside a single-writer transaction with the same span), so
concurrent `deploy`/`restart`/`plan` on different hosts cannot interleave
reads and lose records or token consumption. Per-host gates serialize
same-host operations; the store mutex serializes the shared file. Lock order
is fixed: the process-wide mutation lock is outermost, the store mutex
innermost — a path holding the mutation lock (e.g. `remove`'s token-row purge)
may acquire the store mutex, but no path holding the store mutex ever acquires
the mutation lock (gate holders' post-acquisition re-reads are lock-free
registry reads), so the two locks cannot deadlock.

Crash recovery: at startup, before the store serves any request, the boot pass
runs in this order — sidecar load first, then the local reap below, then the
interrupted transition, then the tombstone-derived `host-removed` pass, then
bidirectional generation-mirror reconciliation (§10) — and boot itself performs
no SSH, so an unreachable host cannot block startup and remote fencing lands
lazily at the next operation's guard advance, after the store already serves
`interrupted` records. Every record still in `pending`/`running` transitions
to `interrupted` (a terminal unknown outcome) with a note naming the crash; a
retry with the same operation ID gets the `interrupted` record back, and a new
operation ID starts a fresh operation. `orphan-unverified` is the one
exception: a durable per-record state (a persisted flag on the `pending-spawn`
record, never a log-only note). An abrupt crash can leave SSH subprocesses
(`Manager.preflight` one-shots, deploy pushes, remote deploy/restart commands)
or a remote deploy/restart running with no live worker to own them — "no live
handles survive restart" means no in-process handles, never that the OS or the
remote stopped too (teardown remnants carry their own persisted cleanup
handles, so post-crash teardown repair needs no resurrected in-process handle
— §6). A new operation ID on a host with an interrupted record starts only
after local reaping completes and under a fresh fencing epoch with the guard
advanced past kill/wait of the superseded epoch, so it never overlaps orphaned
local/remote work from the crashed incarnation.

After the sidecar loads AND after the interrupted transition, boot applies
every loaded tombstone (removed host name): it marks that host's records
`host-removed`, but ONLY records whose pinned (generation, incarnation id)
pair matches the tombstone's persisted (removed generation, removed
incarnation id) pair — the removed incarnation only, never a newer live one
and never a live incarnation sharing the removed generation with a different
incarnation id (generation-only matching would mark a new live incarnation's
records as removed). A tombstone colliding with a live re-add carrying a
different incarnation id matches no record, so the live stays unmarked; a
tombstone with no re-add matches only the removed incarnation's own records. A
crash between `remove`'s sidecar commit and its live mark recovers exactly the
removed incarnation's records the same way. A collision with a live `hub.toml`
host (or a live sidecar entry from a re-add) is treated as a new incarnation:
the live host is assigned a generation strictly above the tombstone
high-water mark BEFORE historical marks apply; the merge rule discards the
colliding tombstone, so pre-collision `host-removed` records stay at or below
the high-water mark while the live incarnation's current-generation records
sit strictly above, unmarked and out of the never-match rule. `remove`'s
sidecar commit and operation-store mark are two separate durable writes
(different files); a crash between them leaves the mark to be reconstructed
here — the never-match rule holds via the live mark OR boot reconciliation.

Cross-file generation mirror: every sidecar commit that also advances a
mirrored store-side generation writes a commit marker — the (name, sidecar
generation, store-mirror generation) triple — into the sidecar's atomic write.
Boot orders sidecar load first, then the `host-removed` pass above, then
bidirectional generation-mirror reconciliation before serving any request: a
store mirror newer than the sidecar mark for the same name with no matching
sidecar commit marker (a store write that survived its sidecar's compensation)
is rolled back to the sidecar mark before any token/record validation, so a
compensated mutation can never boot with an advanced generation invalidating
valid tokens and records — and a sidecar mark newer than the store mirror for
the same name (a crash after the sidecar write but before the mirror write) is
durably pushed forward into the store mirror in the same boot pass, so the
mirror is never left stale behind the sidecar. The rollback applies ONLY when
the sidecar carries a live entry or tombstone for the name with a valid marker
triple to roll back to: when the sidecar is missing or held no entry for the
name (deleted sidecar, compensated-away incarnation, or a name the sidecar
never knew), the store mirror is PRESERVED — never rolled back — and the
max-of-both restoration reads the surviving mirror as the high-water mark, so
discarding a corrupt sidecar can never discard the surviving high-water mark
and let a re-added host reuse an old generation. The discarded generation is
provably unreferenced by a stated and boot-enforced invariant — the mirror may
advance past the sidecar mark only through the compensable second phase of a
mutation whose sidecar restore revalidates exactly the purged rows: a
compensated-away generation therefore never owns an `OperationRecord` and
never backs a dedup key — and boot asserts exactly that durable half before
discarding (any record or dedup entry naming the discarded generation is a
hard startup error, never a silent drop). Cursors are opaque, stateless,
client-held wire values — boot cannot enumerate outstanding client cursors, so
the discarded generation is NOT provably unreferenced by cursors and boot
asserts nothing about cursor-pinned boundaries: a stale cursor is caught when
presented (§11), never at boot. The discarded generation is preserved as the
name's high-water mark (boot records the discarded number into the sidecar's
per-name high-water mark in the same atomic sidecar write that performs the
rollback — the mark outlives the rolled-back store mirror by construction), so
no later mutation ever reuses it and no recycled generation can resurrect a
stale cursor. Records reach the store only through the atomic consume-and-create
write, so no crash window can consume a token without leaving a recoverable
record. No record stays stuck forever.

Per-host operation gate: at most one deploy/restart per host at a time; the
gate is shared with `Ensure`-triggered deploys and supervisor activity so an
auto-deploy and a user deploy cannot interleave (the 04b `errControllerDirty`
posture applies across all of them). Acquisition is try-acquire — nothing waits
on a held gate: a new `deploy`/`restart`, a `plan` (which always try-acquires
before validation and mint, after its ungated refresh/probe), or an
`update`/`remove` that finds the gate held fails fast with the typed busy
error naming the in-flight operation. A try-acquire gate keeps a stuck holder
from wedging callers; the holder identity lets the UI offer open/wait versus
blind backoff. Holder classes: when the gate is held by a deploy/restart
operation the busy error names that operation (its operation id —
open/wait-able) — including an `Ensure`-triggered deploy, which is a durable
fenced operation holding its own op-store record (below), so its busy error
names the Ensure operation the same way; when it is held by `plan`'s
validation-plus-mint window — which holds no operation-store record — the busy
error is the typed transient form (`host busy (plan in progress)`) carrying no
operation reference, and the UI shows retry-with-backoff with no open/wait
affordance. Ensure-triggered mutations are durable fenced operations: the
Ensure path mints a server-side client operation ID, persists the op-store
record with its fencing epoch (controller boot id + per-host monotonic op
sequence) under the gate before launching the worker, and the worker runs the
same register/fence/perform guard advance — a crash mid-Ensure reaps and
fences exactly like a user deploy, and no Ensure remote mutation precedes its
persisted epoch/ownership record — and the Ensure path checks the remnant
fence before minting that record: an open remnant for the host refuses the
Ensure-triggered operation with typed `remnant-open` (naming the `remnantId`),
never a fresh epoch over a pending teardown. The gate pins the token-bound
host entry and resolved target for an operation's lifetime: while a
deploy/restart holds its host's gate, `update` and `remove` of that host fail
fast (and `add` cannot collide — the held host exists, so its name is refused
as a duplicate), so the "sees what will be installed where" binding cannot be
broken mid-push. Gate/mutation-lock ordering: `update`/`remove` check the gate
under the process-wide mutation lock and fail fast if it is held, and a gate
is not grantable to a new operation while the mutation lock is held — a
mutation that observes a free gate owns it through the staged commit, and no
operation can start under a mutation in flight. Post-acquisition re-read: an
operation resolves and validates against the host entry BEFORE acquiring the
gate, and a mutation can legally commit in that window (the gate is grantable
whenever the mutation lock is free — hence the re-read), so every gate holder
— `deploy`, `restart`, or `Ensure`-triggered work — re-reads the registry
entry immediately after acquisition and verifies its hash/generation still
matches what it resolved; a mismatch is a typed stale-entry refusal (`deploy`'s
token-binding check is this rule with the token's bindings as the reference;
`restart` refuses; `Ensure` re-resolves from the live registry) — never a
proceed on the superseded entry. Mutation rebind ordering: `update`/`remove`
rebind or cancel the supervisors and channels bound to the superseded entry as
part of the staged commit — the commit lands first (sidecar persist plus
runtime swap), the rebind/teardown completes, and the gate is released LAST,
so no gate waiter can acquire a half-rebound host and no supervisor reconnect
can race a half-swapped entry. This gate is the "safe against in-flight
`Ensure`" mechanism of the 08a manager surface.

Worker lifetime: the push/`waitHealthy` work runs on an async worker under a
controller-lifetime context, never the RPC context: the RPC context is used
only for admission and the atomic consume-and-create record write, and the RPC
returns once that write lands — a client disconnect cannot cancel a persisted
pending/running operation. A disconnect cannot cancel a persisted operation
and shutdown cannot leave gates or records dangling: only controller shutdown
cancels workers, transitioning the operation to `interrupted` (with a note
naming the shutdown) and releasing its host's gate. Post-operation facts
refresh: a deploy/restart worker that reaches terminal success first performs
a verified post-operation preflight (the same one-shot SSH preflight as
`plan`'s refresh, deadline-bounded) and re-probes the running build/health
over the attached channel, and publishes the refreshed facts to the
(generation, incarnation id)-scoped last-known store (keyed to the operation's
pinned pair, so a concurrent mutation cannot misattribute them) — and ONLY
THEN marks the operation `complete`. The probe confirmation is what the worker
verifies before marking `complete` (the refresh's preflight facts alone never
prove the live process caught up). A restart worker additionally confirms the
probe reports the post-restart build; a deploy worker whose plan said a
restart follows confirms the planned restart ran and the probe reports the new
version. Process-instance verification after restart is same-clock only: the
worker records the pre-restart `processStartTime` the pre-operation probe
returned; after restart the post-operation probe's `processStartTime` MUST
DIFFER from the pre-restart value (both read from the same remote clock, so no
cross-clock comparison — a changed value proves replacement, equal or absent
does not). Under a verifiable revision the worker additionally requires the
post-restart build to equal the deployed revision; under an unverifiable
revision (`dev` or dirty) the changed `processStartTime` alone is the success
signal — revision equality proves nothing there. A worker that cannot verify
the refresh or the probe — including an unchanged or absent post-restart
`processStartTime` — records the failure VERBATIM in the operation record
instead of marking clean success. Only the live channel probe on the same
remote clock proves the new process replaced the old one.

Progress is readable via `evener/host/operations` (guaranteed). If the
implementing session extends the host-notification stream with a
controller-originated best-effort event class, the UI MAY render from it but
MUST fall back to polling; polling-only is a fully valid implementation. The
UI renders progress through terminal state. Never a synchronous RPC.
## 8. Plan, deploy, restart

`evener/host/plan` is a mutation — it mints durable controller state, so it
is classified and admitted as one. Params `{name}`. It builds a fresh plan
from the current facts and configuration and returns the plan plus a
controller-minted confirmation token: single-use, expiring. Token TTL: 5
minutes by default, owner-adjustable, bounded by the plan freshness bound
below. Mint persists exactly one formula, `expiresAt = min(mintTime +
configuredTTL, factsCapturedAt + freshnessBound)` (both terms absolute
wall-clock timestamps — the first the post-mint liveness term, the second the
end-to-end facts-freshness term over `factsCapturedAt`, which is captured
BEFORE the ungated refresh-to-mint gap inside `plan` and therefore already
includes it): an owner-set TTL above the bound is clamped to the bound at
mint, never persisted past it; a slow probe ages the second term before mint
lands, so the minted `expiresAt` already reflects the refresh-to-mint interval
and no deploy-side recomputation re-derives it; lowering the bound below
outstanding TTLs shortens their effective expiry to the bound at validate
time, never past it. The effective deploy window is min(TTL, remaining
facts-freshness at mint) — at the default 5-minute TTL with a 5-minute bound
the window is the full TTL on freshly minted facts — and a token presented
past the freshness bound at deploy time is a `stale-entry` facts-age re-plan
refusal (the UI re-plans from fresh facts rather than retrying the token).
Deploy carries exactly two aligned checks on one formula: the deploy step-(3)
re-check compares the token-bound facts age (`now - factsCapturedAt`,
end-to-end over the same `factsCapturedAt` the formula's second term uses)
against the bound and refuses stale past it, and the step-(4) check compares
`now` against the persisted `expiresAt` verbatim — never recomputed, never
re-clamped — so the two checks agree on one interval pair by construction
(step (3) re-derives the freshness term from the bound source; step (4)
enforces the persisted minimum of both terms — step (3) stays reachable past
a slow probe because the formula's second term already aged through the
ungated window at mint). No second formula, no second arm.

The token binds (host name, the host's registry generation at mint time, a
hash of the resolved host entry, a fingerprint (content hash) of the
controller's `hub.toml` file bytes as they are on disk at plan time, the
preflight revision the plan was built from, the facts capture timestamp
(`factsCapturedAt` — the deploy facts-age re-check reads this binding, never
the plan text alone), the resolved target path, controller revision, the
probed running revision, the probed running-health flag (also bound — a health
change between plan and deploy invalidates the plan), the probed
`processStartTime` when the probe carried it (bound alongside the running
revision, so a process replacement between plan and deploy invalidates the
plan even when the revision is unverifiable), nonce). The effective freshness
bound in force at mint is a token binding alongside the rest above: the token
carries the bound it was minted under, and deploy step (3) compares the
token-bound facts age against the token-bound value, never a re-read owner
knob — so mint and execution agree on one bound by construction, and a
mid-flight owner change to the knob can neither extend nor shorten an
outstanding token's freshness term past its minted `expiresAt` (shortening
still lands through `expiresAt`, which the mint formula already clamped to
the minted bound). The `hub.toml` fingerprint is what makes a manual edit of
that file invalidate outstanding tokens at deploy time: the initial full load
of `hub.toml` is at startup, but the mutation staged-commit path re-reads the
file bytes on every mutation (validation read, final check, post-rename
reconcile — §6) and `plan`/`deploy` re-hash them at mint and execution time
(§8), so a hand edit is visible without a restart. Controller-minted,
entry/target/facts/running-state/`hub.toml`-bound single-use tokens force the
user to see exactly what will be installed where before any remote mutation.

Generation binding: the token is bound to the durable incarnation — the
per-name generation of §15 — and `deploy` validates the generation like every
other binding — AND to the incarnation id minted beside it: every `add`/re-add
mints a fresh incarnation id in the same atomic sidecar write that mints the
generation, the token binds both values at mint, and `deploy` refuses when
either differs from the registry's current pair — sharing a generation with
an open remnant therefore never shares validity. Live `remove` revokes every
outstanding token row for the name through the `pendingStoreSync` revocation
intent (never alongside the tombstone write — the two files share no atomic
commit), and re-add mints a new generation that starts with zero valid
tokens, so a token minted before a remove/re-add cycle can never validate
after it even if the re-added entry is byte-identical.

Outstanding-token rule: minting a new token for a host immediately supersedes
any earlier unconsumed token for that host. Superseded rows do not
accumulate: the same atomic store write that persists the new token deletes
the host's earlier unconsumed token rows — predecessors are already invalid by
the rule above, so mint replaces rather than accumulates; expired-token
reaping (lazy + boot, below) covers only tokens that expire unconsumed and
unsuperseded.

Cross-file commit intent: token rows live in the operation-store file while
tombstones and mutation receipts live in the sidecar — a shared mutex never
makes two files atomic. So `remove`'s staged commit (token-row purge plus
tombstone write — the tombstone write carrying a token-row revocation intent,
the purge applying only post-swap — and any token consume/delete that must
coincide with a sidecar commit) runs a durable two-phase intent: (1) the
sidecar's atomic write carries a `pendingStoreSync` intent (the exact store
rows to delete/invalidate plus the sidecar generation the intent belongs to);
(2) the store write applies it and the follow-up sidecar atomic write clears
the intent. Token deletion therefore lands only after the sidecar commit's
swap succeeds — the store purge runs as the post-swap step, never before it:
a failure before the purge leaves both files in the old state with nothing to
compensate; a post-swap failure after the purge (still before the commit
point) compensates the store purge alongside the sidecar restore — the purged
token rows are re-inserted alongside the stash restore, so compensation
resurrects exactly the tokens its own sidecar restore revalidates and the
compensated mutation leaves old sidecar bytes beside old store rows — through
a durable two-step protocol, never one cross-file atomic write. Two-phase
intents converge sidecar and store without a cross-file atomic write. The
committer first persists a `pendingCompensation` record holding the rows ABOUT
to be purged (plus the stash reference and the sidecar generation the purge
belongs to, in phase `compensating-armed`) into the operation-store file —
which the stash restore cannot touch, since the rename replaces sidecar bytes
only — in its own store-local write BEFORE the purge write, and the purge
write advances the record past `armed`; past the commit point the committer
clears the armed record in its own store write (the purge stands — the rows
were revoked, never lost). The preimage is persisted BEFORE the purge, never
after it. Only on the failure path does the restorer run — then restores the
prior sidecar bytes from the stash, then re-inserts the purged rows in a
second store write, then clears the compensation record. The
`pendingCompensation` record carries `phase` (`compensating-armed` →
`compensating-sidecar` → `compensating-rows` → `compensating-clear`) plus the
stash reference the sidecar restore must apply, and the restorer advances the
phase in its own store write per step: sidecar restore FIRST (stash bytes
back, phase to `compensating-rows`), THEN the row re-insert (phase to
`compensating-clear`), then the record clear. Boot reconciles a live
compensation record by phase, never by blind re-insert: a record still in
`compensating-armed` checks the purge first — a sidecar whose
`pendingStoreSync` intent is already cleared means the commit path passed the
commit point, so boot clears without resurrecting rows (the purge was
committed, never lost); intent still present with the rows still present means
the purge never landed, so boot restores the sidecar from the stash, leaves
the rows untouched, and clears; intent still present with the rows absent
means the purge landed before the crash, so boot advances to
`compensating-sidecar` and follows that arm; a record still in
`compensating-sidecar` restores the sidecar from the referenced stash before
touching any rows; a record in `compensating-rows` verifies the restored
sidecar is in place, then re-inserts exactly the rows the restored sidecar's
generation revalidates; a record in `compensating-clear` clears without
resurrecting rows (the rows are already converged — clearing the pending
intent without re-inserting is the roll-forward, never a second resurrect). A
crash at any point of compensation therefore still converges to the restored
sidecar's view with its tokens intact — the compensation record survives the
sidecar restoration by construction, and the restored sidecar carries no
`pendingStoreSync` intent for the generic rules below to misread. Boot
reconciles both directions before serving: an intent whose store rows are
still present is re-applied (the sidecar committed, the store lagged), and
store rows with no covering intent whose sidecar generation already advanced
past them are dropped (the store committed, the sidecar's compensation already
restored — the rows belong to the compensated-away incarnation) — and once
sidecar and store agree (the intent's store rows are already gone, including
the store-already-applied no-op where a re-applied intent finds nothing to
delete), the boot pass clears the stale intent in its own follow-up sidecar
atomic write, so no converged intent survives its boot. A crash between the
files therefore converges to exactly the committed sidecar's view, never a
restored sidecar beside a committed revocation — and compensation resurrects
only the tokens its own sidecar restore revalidates.

`plan` runs its two network round-trips with no gate held, then try-acquires
the host's per-host gate and fails fast with the typed busy error if held. The
gate hold is bounded to validation plus the durable mint — `plan` acquires the
gate only after the refresh and probe below complete, then re-checks
attachment, the registry generation of the resolved entry, and facts-freshness
against the refreshed facts — and scans the operation store (local read, no
network) for any operation on this host that reached a terminal state since
the ungated reads started, identified by the monotonic durable
state-transition sequence (§7) — the worker records its pre-read sequence
position before the gateless refresh and probe, and any terminal operation
with a higher transition sequence is a typed stale-entry re-plan refusal,
never a plan minted against the pre-operation process — so a detach, mutation,
facts advance, or completed operation landing between the ungated reads and
acquisition is a typed stale-entry refusal or a re-read, never a plan against
the superseded entry — and keeps the gate only through validation plus token
persistence (mint + durable write), releasing before returning — so a
fresh-facts plan cannot mint while a deploy/restart/`Ensure` holds the gate,
but no network wait ever holds it. The token lives behind the gate while the
gateless probe and refresh keep slow network reads from forcing spurious busy
refusals on unrelated mutations. `plan` always refreshes attached-host facts
before mint (unconditional: the plan freshness bound is 5 minutes by default,
owner-adjustable, and the refresh is what keeps every mint inside it): a
re-run of the same one-shot SSH preflight the deploy path already uses
(`Manager.preflight`, `sshconn/preflight.go` — the `uname`/env-probe/`id
-u`/`launch-check` command sequence executed over the manager's SSH transport),
exposed as a thin deadline-bounded entry point and run with no gate held,
bounded by the same attempt bound that caps the preflight phase of
`ensureOnce` — then builds the plan from the refreshed facts, so an online
(attached) host can always obtain a token without a re-attach/bootstrap
cycle. The refresh is an SSH command execution, not an attach: it never
initializes an AppWire channel, never starts or rebinds a supervisor, and
never enters the deploy/restart decision ladder — `ChannelIfAttached` is not
involved. If the host is not attached, `plan` returns no token and names the
staleness — the UI must Connect first (the attached precondition is deliberate:
`plan` must not become a hidden prober for never-connected hosts). If the host
IS attached and the refresh fails or times out, `plan` returns no token with
the distinct `refresh-failed` reason: the UI surfaces retryable diagnostics
and a refresh-retry affordance, never a Connect loop — reconnecting cannot fix
an independent preflight failure. A token is only ever minted from fresh
facts.

Running-state verification: `plan` mints only for an attached host, so with no
gate held it probes the running hub's identity and health through the attached
channel with the `evener/host/running` method (catalog entry + request/response
in §11) — a read-only request issued only through
`sshManager.ChannelIfAttached(name)` over the live channel (never a dial,
never preflight), deadline-bounded (explicit probe timeout, owner-adjustable,
default ships in the implementing PR — a hung remote holds no gate at all: on
timeout `plan` returns the no-token `probe-failed` refusal with nothing to
release) — and records the response's `buildRevision` as `runningVersion` and
its `healthy` flag as `runningHealthy`, both as `HostPlan` fields, with the
running revision bound into the confirmation token alongside the other
bindings above. The remote handler ships in the same 08b as `plan`: every hub
serves `evener/host/running` locally (its own revision from the same source as
the `controllerBuild` plan input, plus its own health) and admits it only over
an attached session peered by the #1603 handshake — the probe inherits the
channel's authentication and adds no new capability (classified as a read,
never forwarded onward, never entering the deploy ladder) — this is the
direction-scoped peer-probe exception named in §3: the remote's ingress admits
`evener/host/running` only over the attached session peered by the #1603
handshake, and rejects browser-origin and forwarded requests to it exactly
like every other `evener/host/*` request. A missing or unverified handshake is
an unauthenticated probe. `restartFollows` is computed from the fresh
preflight facts confirmed against the probed running version: the plan says a
restart follows only when the running hub is actually outdated — and a probed
unverifiable revision (`"dev"` or dirty — §11) always reads as outdated
(restart follows) — no timestamp comparison exempts it: cross-clock timestamp
comparison cannot prove the live process runs the desired code, so a probed
unverifiable revision never reads as current at plan time, the plan always
schedules the restart, and the deploy worker verifies the resulting process
with the same-clock instance rules (§7); a dev revision that merely equals the
controller build proves nothing by revision equality. If the probe read fails
or is unauthenticated — including a remote whose hub predates the handler
(named distinctly as handler-absent; such remotes take the one-time migration
path below) — `plan` mints nothing and names the failure in the same no-token
shape as the other refusals, with the discriminating `reason` field (§11) — a
plan never ships without verified running state. One-time migration for
handler-absent remotes: the first contact with a pre-handler remote takes an
explicit version-gated bootstrap plan — `plan` returns the handler-absent
no-token shape (never a token), and the UI offers the documented manual
upgrade step (install a probe-capable binary out-of-band, then re-run `plan`);
once the remote serves `evener/host/running`, the normal token-bound deploy
path delivers all later upgrades. There is no token-bound in-band upgrade of a
remote that cannot yet prove its running build. The handshake version, ping
liveness, and preflight on-disk facts feed the decision ladder but never
substitute for the probe: none of them proves which build the live process
runs. The token is generated by the controller, never constructed client-side
— this is the enforcement behind "the user always sees what will be installed
where".

Token format and validation: the token is an opaque base64url string of at
least 32 characters (≈192 bits); its nonce is drawn from a CSPRNG with at
least 128 bits of entropy and unique per mint; validate/consume compares the
presented token with constant-time equality and never logs it.

Token storage and lifecycle: minted tokens persist as rows in the
operation-store file itself (same atomic temp+rename+fsync writes as operation
records — a corrupt or schema-invalid store quarantines as in §7, so every
outstanding token drops with the quarantined file: zero valid tokens past the
restart, never a hard startup error). Expired tokens are reaped lazily (on any
token validate/consume pass for that host) and by the same boot pass that runs
the interrupted transition and the tombstone-derived `host-removed` marking:
any token past its TTL at boot is dropped, never revived, and any token whose
host resolves at boot to removed (tombstoned) is dropped with it — the
tombstone reconciliation runs before token revival is even considered, so no
pre-restart token survives for a host that no longer exists. After a
quarantine of the unvalidatable store file above, the store starts with zero
outstanding tokens; otherwise only unexpired, binding-intact tokens for live
hosts remain valid past the restart. Live removal revokes immediately:
`remove`'s staged commit drops every outstanding token row for the name
through the `pendingStoreSync` intent — the tombstone write carries the
revocation intent under the mutation lock and the store purge applies only
post-swap as the compensable second phase (never alongside the tombstone
write: the two files share no atomic commit) — and the re-added generation
starts with none — combined with the generation binding above, no pre-remove
token can validate after a remove/re-add cycle. Token validation additionally
requires the token's generation to equal the registry's current generation for
the name AND the token's incarnation id to equal the live entry's — sharing a
generation with an open remnant never shares validity.

Wall-clock rollback guard: every token validate/consume pass and every
facts-age check first compares now against the store's durable high-water
wall-clock (the greatest wall-clock value observed by any prior mint/facts-capture
write — validate/consume reads compare but never advance or persist the mark
— persisted in the store file in the same mint/capture write, never on a
validate read): a now reading more than 30 seconds behind the high-water mark
(owner-adjustable tolerance knob in the same family as the token TTL; the
default ships in the implementing PR — a backward step within tolerance reads
as clock jitter and invalidates nothing) is a detected rollback — only the
records whose own timestamps postdate `now` are affected (a token row whose
`expiresAt` is past `now` but was minted after `now` reads `token-expired`; a
token row minted at or before `now` is checked against an effective now of
`max(now, mark)` instead of the rolled-back `now` — never a fleet-wide drop:
while the rollback is active (`now < mark`) every `expiresAt` comparison and
every `now - factsCapturedAt` age check substitutes the mark for `now`, so a
pre-rollback token keeps exactly the real-time lifetime its persisted
`expiresAt` granted); a facts entry whose `factsCapturedAt` is past `now`
reads stale — its rendered age is unknown, so `status` marks the facts stale
and `plan` refreshes before minting rather than trusting the captured
timestamp) until wall-clock time again reaches the mark (`now >= mark` — a
post-rollback capture anchors at `max(now, mark)`, never by moving the mark
backward, so a fresh capture taken while `now` is still behind the mark cannot
re-anchor it). The high-water mark itself never moves backward, so a rollback
can only invalidate, never extend, a deadline (records whose timestamps
postdate `now` invalidate per-record above; all other records are checked
against `max(now, mark)` — their deadlines were already bounded by the
persisted `expiresAt`, never extended by the step). A forward jump past
outstanding TTLs simply expires them through the existing `expiresAt` check —
no special rule.

`evener/host/deploy` is a mutation. Processing order is fixed. (1) Dedup
first: if the client operation ID matches an existing durable operation record
of the same host, kind, current host generation, and current incarnation id (a
`host-removed` record never matches, and neither does a record pinned to a
superseded generation — §7), return that record — no token validation, no
consumption (this makes the single-use token and idempotent retry coexist: a
replay after a lost response succeeds without a fresh token). A client
operation ID colliding with a current-generation record of a different host or
kind, or with a current-generation `host-removed` record, is refused with a
typed conflicting-operation-ID error — never a hit, never a new operation
under the colliding ID. Only current-generation records participate: a
colliding ID whose only matches are superseded-generation records — including
`host-removed` records of a removed incarnation — is not a conflict and opens
a fresh operation. Dedup-before-gate lets idempotent replays succeed without
acquiring contested gates or reviving fenced work. (2) Otherwise provisionally
validate the token — missing, mismatched, superseded, or expired → refusal —
as a fail-fast readability check only: a concurrent `plan` can mint a newer
token and supersede the presented one before the gate is acquired, so nothing
is decided here. A fenced name never reaches the gate: an open teardown
remnant for the host refuses with typed `remnant-open` (naming the `remnantId`
— §6) before any probe or acquisition, past the step-(1) dedup check (a
same-key replay returns its record without consulting the fence). (3) Acquire
the host's per-host gate — but first probe gateless, then revalidate under the
gate: fail fast with the typed busy error if the gate is held at acquisition
time — probe the running build/health over the attached channel with no gate
held (same explicit probe timeout as `plan`'s probe), then acquire the gate
and under it re-resolve the target, re-read the host entry, and re-hash the
`hub.toml` fingerprint at execution time and reject if any differs from the
token's bindings — and re-validate the pre-acquisition probe result. Probe
first, gate second, so a slow probe never holds the gate busy against
`update`/`remove`/`plan`/concurrent `deploy`; a change to the entry, target,
or `hub.toml` fingerprint between probe and acquisition fails the
post-acquisition re-read above with the same refusals — but the
pre-acquisition probe's running-state result itself is NOT re-probed under the
gate (no second network read while holding it), so a concurrent
`deploy`/`restart` that completes in the probe→acquire window and changes the
live running revision or health is invisible to the re-read: under the gate,
the worker closes the window with a cheap local check — it scans the operation
store (local read, no network) for any operation on this host whose terminal
transition sequence (§7) is above the sequence position the worker records
before the gateless probe — and any such terminal operation is a `stale-entry`
re-plan refusal (token unconsumed, no record), never a deploy over a
possibly-replaced running process. The scan catches exactly the dangerous
case — a concurrent operation that finished (hence may have replaced the
process) inside the window — while an operation still in flight holds the
gate, so acquisition would have refused busy instead of reaching the scan. A
probe failure or timeout refuses with the typed `probe-failed` refusal — the
unavailable-class envelope discriminator `probe-failed` (data names the host
and whether the probe read failed, timed out, or was unauthenticated —
distinct from `plan`'s no-token `reason: "probe-failed"` union-arm value,
which is a union arm, never an envelope) — token unconsumed, no operation
record — distinct from a genuine running-version mismatch's `stale-entry`
refusal. The worker rejects if the probe differs from the token-bound running
revision or the token-bound running-health flag — and rejects if the re-probed
`processStartTime` differs from the token-bound one when the token bound one
(a process replacement between plan and deploy invalidates the plan even under
an unverifiable revision — both values are read from the same remote clock
across the two probes, never compared against controller wall-clock, so the
check orders process instances without a cross-clock comparison) — and
re-checks the token-bound preflight-facts freshness (`factsRevision` /
`factsCapturedAt` against the token-bound effective freshness bound — the
bound value carried in the token, never the owner's current knob): facts older
than the bound at deploy time are a `stale-entry` re-plan refusal (token
unconsumed, no record — the UI re-plans), so a token presented at minute 4
never deploys on 4-minute-old OS/arch/target-writability facts past the
token-bound value at deploy time. A sidecar edit, a manual `hub.toml` edit
(caught by the fingerprint, the only way a hand edit is visible without a
restart), a facts refresh, a target change, or a running-build or
running-health change between plan and deploy invalidates the plan; the UI
re-plans. This re-read is the gate protocol's post-acquisition check (§7).
(4) Revalidate and atomically consume the current nonce under the store mutex
— still holding the host's gate (gate-then-store-mutex, the same order as
`plan`'s validation-plus-mint window: the consume write is one more
store-mutex-serialized read-modify-write) — as one atomic store write:
re-read the host's current token row and compare-and-consume its nonce against
the presented token — and re-check `expiresAt` against the clock in that same
transaction: an `expiresAt` at or before now is a `token-expired` refusal with
no consumption and no operation created, even when the nonce still matches; a
changed nonce (a concurrent `plan` superseded it between step (2) and
acquisition) is a `token-superseded` refusal with no consumption and no
operation created. On a match the same write deletes the token row and creates
the pending operation record — consume is delete in that same write, never a
mark — so consumption and record creation are one event; return its id. A
consumed token presented again reads as `token-missing`: the row is gone, and
gone rows never validate — so the consumed-then-replayed-with-new-op-ID case
(a same-ID replay hits the step (1) dedup instead) pins to `token-missing`.
The operation holds its host's gate from record creation to terminal state,
pinning the validated entry and target for the push's lifetime —
`update`/`remove` on that host fail fast meanwhile (§7) — and the gate alone
does not pin the `hub.toml` file against external hand edits: the worker
re-hashes the on-disk `hub.toml` fingerprint immediately before each
irreversible step (before the push, and again before the planned restart when
the token-bound plan says one follows) and aborts with the typed `stale-entry`
(`hub.toml`-fingerprint) refusal, reconciling through the fingerprint-bound
invalidation path, on any drift from the token-bound fingerprint; `restart`
revalidates the same way before its irreversible step — otherwise the
guarantee narrows to the validation points named above, never the remote
mutation. Detached-during-deploy: if the channel is gone at the
pre-acquisition probe, or the post-acquisition revalidation finds the channel
dropped, `deploy` refuses with the typed `host-detached` error — never
consuming the token, never creating a record — directing the UI to Connect and
re-plan (`restart` keeps its operation-owned attach-first because it spends no
token; `deploy` spends a single-use token, so it must not consume it to open a
worker that then attach-firsts into an unvalidated channel — and the flow
above is probe-first, gate-second with no locked re-probe). Never blocks the
RPC on the push: the RPC returns the record id once the atomic
consume-and-create write lands, and the push runs on a worker under the
controller-lifetime context (§7), never the RPC context — a client disconnect
cannot cancel it. Deploy wraps the 04b deploy path (`Manager.deploy` /
`deployTarget` / `resolveDeployOrCreateTarget`) with its guards intact and
surfaced verbatim: source/revision verification (`verifyBuildSource`,
`verifyBuildRevision`), terminal dirty-controller refusal, push integrity,
resolved-target persistence. Planned-restart execution: when the token-bound
plan says a restart follows, the deploy worker performs that restart after the
push (the same 04b restart path `restart` wraps) and verifies it — the
post-operation facts refresh (§7) must report the new running version — before
marking `complete`. A plan that says no restart follows requires none. The
planned restart drops the channel exactly like a standalone `restart` and
follows the same operation-owned detach/restart/reattach sequence under the
same gate, so the post-operation probe always runs over a worker-owned
channel.

`evener/host/restart` is a mutation: same operation model ((host,
kind)-scoped operation-ID dedup first, generation-scoped like `deploy` — §7;
then the remnant fence (`remnant-open` naming the `remnantId` past dedup and
before any probe or acquisition — §6); busy-fail gate acquisition; atomic
record creation) — but no token: restart has no install step, no target, and
no plan to re-verify. Instead restart binds the current `hub.toml`
content-hash fingerprint (the same fingerprint `plan` binds into confirmation
tokens) at resolution and re-checks it under the gate alongside the
post-acquisition entry re-read — a manual file edit between resolution and
acquisition is a typed stale-entry refusal with a retry instruction, never a
restart under the superseded file; a mutation landing between restart's
resolution and its gate acquisition is likewise a typed stale-entry refusal,
never a restart of the superseded entry — and under the gate `restart` runs
the same terminal-operation scan as `deploy` step (3) above (any operation on
this host terminal since resolution started — identified by the same durable
state-transition sequence, never a wall-clock comparison — is a `stale-entry`
refusal, never a restart over a possibly-replaced process). It wraps the 04b
restart path (user vs system unit decision, `waitHealthy` proven replacement).
Restart drops the attached AppWire channel by construction, and no
supervisor/`Ensure` reattach can cover the worker — both need the gate the
operation holds through terminal verification — so the reattach is
operation-owned: the worker retains its host's gate across the drop and
re-runs the `evener/host/attach` dialing closure for the same
generation-pinned entry (no gate release, no gate handoff, no supervisor
involvement — both stay gated out while the gate is held) through the
manager's internal gate-aware attach primitive — `attachUnderGate` (08a ships
it alongside the live host-set surface): it accepts the already-held gate
instead of acquiring it (the normal `Manager.Ensure` path, which acquires the
non-reentrant gate, is never entered while holding it), and it suppresses
supervisor startup — the worker owns the channel until terminal verification,
so no supervisor can race it — then runs the channel re-probe the
post-operation refresh requires over the reattached channel, then starts (or
safely hands off to) a supervisor for the channel under the still-held gate
before releasing it — the suppress-supervisor scope of `attachUnderGate` ends
at verification, so the host keeps automatic reconnect after the restart. The
restart worker MUST use this primitive — re-running the normal attach path
while holding the gate is a deadlock (the gate is non-reentrant) and a
supervisor race — and every attach entry point (`attachUnderGate` for a caller
that already holds the operation gate, the normal `Manager.Ensure` attach
path, and the UI Connect action) checks the remnant fence before dialing
(gateless callers take the normal `Manager.Ensure` path, never this primitive
— a gateless caller through it would deadlock the non-reentrant gate):
an open remnant for the name refuses with typed `remnant-open` naming the
`remnantId` (§6), so no attach can rebind a channel or supervisor over a
pending teardown. Operation-owned `attachUnderGate` reattach avoids deadlock
on the non-reentrant gate and supervisor races while preserving automatic
reconnect after verification. A restart issued while the host has no attached
channel runs the same operation-owned attach first under the already-held gate
and names the attach-first path in the record, so verification always has a
channel and initially unattached hosts need no separate SSH verification path.

`evener/host/operations` is a read: list/detail of controller-side operations
(§11). Never dials. Polling this method is the guaranteed read path for
operation progress; the host-notification stream, where extended to carry
controller-originated events, is best-effort only.
## 9. Crash orphans, fencing, and orphan-resolve

Local reap runs at boot before the interrupted transition (§7), and remote
fencing is lazy — it lands at the next operation's guard advance, after the
store is already serving `interrupted` records. Boot itself performs no SSH:
an unreachable host never blocks startup.

Every worker SSH subprocess is spawned in its own process group with
parent-death cleanup (`Pdeathsig` / process-group kill on shutdown where the
platform supports it), and boot kills any residual group members only through
an ownership-verifiable handle — never the group id alone (a recycled id can
name unrelated work). Before spawning, the worker pre-creates a durable
ownership boundary — a Linux cgroup on Linux, a Darwin process-group-plus-session
boundary on Darwin (process group id plus the session id the worker's
`setsid`-detached launcher holds — the `linux || darwin` process-group seam in
`agent/execenv/process_group_unix.go` and the `Setsid` detached-launch seam in
`agent/execenv/detach_unix.go`; there is no Darwin cgroup or job-object
primitive, so the boundary is the (pgid, session id) pair, never a group id
alone) — plus a server-generated per-spawn nonce — and persists the boundary
identity plus the nonce in the operation-store file alongside the record (the
pre-spawn persist carries a `pending-spawn` intent holding the nonce; the
worker spawns every SSH subprocess directly into the pre-created boundary and
matches the intent post-spawn, so a not-yet-populated boundary can never
authorize a kill). On Linux the boundary is the pre-created cgroup (whose
membership itself is kernel-enforced) and the nonce binds to kernel-owned
per-process identity: cgroupfs is kernel-managed and exposes no app-created
marker files, so the worker never writes the nonce into the cgroup filesystem
— the pre-spawn persist records the nonce beside the boundary, and the
worker's launcher observes each spawned child's kernel-owned process start
time beside the same nonce post-spawn exactly like the Darwin launcher marker
below: boot enumerates current boundary members and signals only members whose
(pid, start time) still matches the launcher-observed pair — membership selects
the candidate set, the kernel-owned start time proves the instance, and the
persisted nonce ties both to the pre-spawn intent — so boot verifies ownership
by reading kernel-owned identity back from the process table and comparing it
against the persisted pair in the same enumeration that lists members. An
empty boundary is already clean; a boundary whose members match no persisted
(pid, start time) pair reads as already clean (reused ids naming different
processes), while a boundary whose launcher-observed pair was never persisted
(a crash between spawn and the post-spawn persist) is unverifiable, so boot
fails closed — reap nothing, keep the `pending-spawn` intent open, and mark
the record `orphan-unverified` — never a kill on membership alone. On Darwin
the reap runs process-table enumeration of the persisted (pgid, session id)
pair through a dedicated process-boundary API (a new `agent/execenv`
boundary-enumeration seam — NOT the `agent/envctx` probe seam:
`agent/envctx`'s `Probes` (`collector.go`) exposes only
`Now`/`GitBranch`/`Load`/`Memory`/`Disk`, with no process-enumeration entry —
so the implementing PR adds the boundary-enumeration function beside the
existing `process_group_unix.go`/`detach_unix.go` process-group seams, and
the Linux and Darwin arms stay asymmetric only because the kernels are: Linux
enforces membership in cgroupfs while Darwin has no cgroup or job-object
primitive, so Darwin enumerates the (pgid, session id) pair instead), and
every enumerated member is verified against a launcher-observed marker before
signaling: the worker's `setsid`-detached launcher persists the spawned
child's observed pid plus its process start time beside the nonce, and boot
signals only members whose (pid, start time) still matches the persisted pair
— a pid whose start time differs is a reused id naming a different process, so
the boundary reads as already clean and nothing is signaled. The (pid,
start-time) plus nonce binding names the exact process instance with
kernel-owned identity the nonce alone cannot provide. When that enumeration is
unavailable boot fails closed — reap nothing, keep the `pending-spawn` intent
open, and mark the affected records with the durable `orphan-unverified` state
instead of transitioning them to servable `interrupted` — so an unverifiable
Darwin orphan is never silently adopted as clean. A fail-closed
`orphan-unverified` keeps its host fenced until a human or a later enumeration
proves it gone. `orphan-unverified` is a durable per-record state (a persisted
flag on the `pending-spawn` record, never a log-only note — the one exception
to the boot rule that every other `pending`/`running` record becomes
`interrupted`): every subsequent boot retries the enumeration for local-reap
records only — whose boundary is local and boot-enumerable — and resolves the
record once it succeeds (boundary empty or start-time mismatch → drop the
intent and transition to `interrupted`; verified members reaped →
`interrupted`; a fencing-quarantine record's remote boundary is never
enumerated at boot — boot performs no SSH, so the enumeration is unavailable
and fails closed with the record kept open). A crash between the pre-spawn
persist and the spawn leaves a persisted-but-empty boundary: boot enumerates
it, finds no members, and drops the intent — never an orphan, never a reap of
unrelated work. A crash after the spawn but before the launcher-observed
pid/start-time marker is persisted beside the nonce leaves a live process with
no verifiable ownership metadata: boot treats a `pending-spawn` record whose
post-spawn marker is absent as `orphan-unverified` with the host fenced —
never as clean, never `interrupted` — until the operator resolves it through
`evener/host/orphan-resolve` (whose enumeration likewise refuses the transient
busy form on a marker-less boundary with members still present, and drops the
intent only on a demonstrably empty boundary).

The authenticated `evener/host/orphan-resolve` mutation (params `{id: string}`
— the controller-assigned record id — response the updated `OperationRecord`,
always the resolved record without `orphanBoundary`; exact shapes in §11)
re-runs the persisted-boundary enumeration for that record under the caller's
session authentication and, on a clean boundary under the persisted variant's
rule (empty; a marked local boundary with a pid/start-time mismatch on every
member; a markerless local boundary only when demonstrably empty — no instance
marker exists to mismatch against; a remote-fencing boundary only when no
persisted lease entry is still registered live AND every persisted lease entry
is enumerated and exit-confirmed against the live lease state under its stored
ownership identity — a live guard-file epoch no longer equal to the persisted
`guardEpoch` fences future actions but never proves already-running lease
commands exited, so the mismatch alone never reads clean — enumeration of all
persisted entries is mandatory on every resolve call regardless of the guard
comparison), drops the intent and transitions the record to `interrupted`; on
members still present it refuses with the transient busy form, never a
force-clear. The record's persisted tagged boundary (one `BoundaryEntry[]`
whose members carry the variant's ownership data under its discriminator —
marked local variants under `kind` (local Linux cgroup identity plus
launcher-observed pid/start time bound to the pre-spawn nonce, local Darwin
pgid plus session id plus launcher-observed pid/start time), the markerless
local variant carrying only the persisted pre-spawn boundary, remote-fencing
variants carrying the timed-out epoch's fencing epoch plus its guard-file
epoch plus lease-tracked entries each with their required ownership identity)
is visible through the `operations` detail filter, so the operator kills the
listed members (or confirms them gone) and calls `orphan-resolve` (a hub
restart re-runs the same enumeration at boot, and the post-recovery
enumeration resolves the record the same way). Admission is
session-authenticated like every other `evener/host/*` request; unknown `id`
is typed not-found, a non-unverified record is a typed validation refusal,
members still present get the transient busy form (never a force-clear). The
call never creates an operation record of its own and never advances the host
generation — it transitions the named record (`orphan-unverified` →
`interrupted` on a clean boundary) under the store mutex in one atomic write
— the call itself is never fenced by the orphan admission gate (it is the
gate's way out) — then the host admits new operations past admission again.
A newer operation ID meanwhile starts fresh only after the boundary is
resolved, never overlapping the unverified record.

While any `orphan-unverified` record is open for a host, that host admits no
new lifecycle or mutation call past admission: `plan`, `deploy`, `restart`,
`add`, `update`, `remove`, `teardown-retry`, `attach`, and `Ensure`-triggered
work all refuse with the transient busy form — scoped to that host's name only
(an `add` for a DIFFERENT name proceeds; one host's orphan never blocks
operations on unrelated hosts) — and local reaping stays incomplete — until
the boundary is verified (a later boot's enumeration resolves the record) or
the operator resolves it through `evener/host/orphan-resolve` above — only
then does a fresh operation start under a new epoch after local reap
completion. Only the read-only calls (`list`, `status`, `operations`,
`running`) and the `orphan-resolve` way out itself bypass the persistent
orphan fence. The fence's refusal on `teardown-retry` carries a distinct
discriminator (typed `orphan-fenced-busy` naming the blocking
`orphan-unverified` record id plus the `orphan-resolve` next step — never the
generic transient busy form — so the deploy/plan UI's `remnant-open`
teardown-retry affordance degrades to an orphan-must-resolve-first affordance
instead of directing the operator to a repair call the fence silently
refuses).

Remote fencing (at the next operation, not boot): the worker fences the remote
side per host with enforcement on both ends and inside every mutating step.
Each worker carries a durable fencing epoch (controller boot id + per-host
monotonic op sequence, persisted in the operation-store file alongside the
record before the worker launches), and the epoch is presented to the remote
on every SSH command the worker runs — corroborated by a remote-side epoch
guard file on the target host holding a totally ordered fencing sequence (the
guard file's own monotonic sequence, advanced only by remote compare-and-swap
— the guard-file sequence is the total order across controller restarts, never
the (boot id, per-host sequence) pair alone, which has no defined
cross-restart order). The worker's first commands under the new epoch run
through a remote lease wrapper: the wrapper atomically registers each command
in the per-host remote lease file BEFORE its side effects start, performs the
command's steps only through the wrapper, and holds an exclusive per-host
remote lease across the guard re-check and the irreversible action it guards
— the re-check and the side effect are one atomic helper-mediated operation,
never check-then-act as separable steps: the guard advance takes the same
exclusive lease, so a re-check-plus-act and a concurrent advance are mutually
exclusive — whichever holds the lease first wins and the loser observes the
winner (advance-first makes the check refuse server-side and abort the
operation; check-first lands the side effect before the advance) — register,
fence, and perform are one guarded unit per mutation, and a step whose
presented epoch no longer equals the guard refuses server-side and aborts the
operation. Atomic register-before-side-effects plus a lease-held re-check
closes the race where an old command passes the check then mutates after
losing the guard. The 08b fencing tests pin the interleaving (an old epoch's
check racing a new epoch's guard advance never lands a side effect past the
advance).

The wrapper's first command under the new epoch is a preemptive
fence-takeover — one atomic helper-mediated operation that revokes the
superseded epoch and takes over the exclusive per-host remote lease BEFORE any
kill/wait: the helper revokes the old epoch's lease ownership, installs the
new epoch as the lease holder, and records the fence state (fencing epoch,
superseded epoch, monotonic fence sequence) in the same atomic step, so the
old worker's lease is dead before it can reacquire and the new worker holds
the lease it kills under (takeover-before-kill, because the old worker may
hold the exclusive lease the new worker needs to fence it); only then,
holding the taken-over lease, does the new worker kill (bounded kill context)
and wait for exit (bounded wait context) the superseded epoch's
already-running commands (remote kill of the prior epoch's lease-tracked
entries with exit confirmation — each lease entry carries an ownership token:
the remote PID's start time, or a per-spawn nonce minted by the wrapper at
registration, or the remote cgroup/job-object membership where the platform
supports it — verified on the remote before signaling, mirroring the local
boundary-plus-nonce rule above, so a reused PID on the target host never kills
unrelated work), and only then advances the guard (compare-and-advance — an
older epoch never overwrites a newer one, so a late orphan cannot move the
guard backward — with the fence state held through the advance: the takeover's
fence record persists until the guard advance lands, so no window exists where
the lease is released but the guard has not advanced); every mutating remote
step after the advance re-presents the epoch and re-checks it against the
guard immediately before its irreversible action — a step whose presented
epoch no longer equals the guard refuses server-side and aborts the operation.
A superseded remote command already running is therefore killed before the new
operation mutates — never merely ignored controller-side while its remote side
effects land. Until the fenced guard advance succeeds the new operation
performs no mutating remote step (it may probe/stage locally, but the first
remote mutation follows kill/wait plus guard advance itself), so an orphan
surviving the crash cannot finish a mutation after the new operation started
and overwrite state.

Deadlines: the kill runs under its own bounded context (owner-set
fencing-kill deadline; the default ships in the implementing PR) and the exit
wait under a second bounded context of the same family. A stuck remote must
neither hold the gate forever nor overlap a possibly-live orphan. On kill/wait
timeout the new operation fails with the fencing-failure outcome — terminal
for the operation's result, while the record's state is `orphan-unverified`
(never `failed`: `orphan-unverified` is non-terminal with the boundary present
exactly on it) — and the timeout additionally persists a durable per-host
fencing-quarantine marker in the operation-store file in the same atomic write
that lands the fencing-timeout record in state `orphan-unverified`. The same
atomic store write that lands the `orphan-unverified` fencing-timeout record
also persists the timed-out epoch's remote boundary (the guard-file epoch plus
the lease-tracked entries of the superseded epoch — each lease entry WITH its
required ownership identity (remote PID plus start time, or the wrapper's
per-spawn nonce, or the remote cgroup/job-object membership — the identity the
takeover kill verifies before signaling; an entry without it fails closed at
resolve time), as a `remote-fencing` `BoundaryEntry` — the timed-out
operation's fencing epoch plus the guard-file epoch plus the superseded
epoch's ownership-carrying lease-tracked entries, the same `BoundaryEntry`
union `orphanBoundary` carries) on an `orphan-unverified`-class record for
the host, so the quarantine's record IS accepted by `orphan-resolve` (never a
`failed` record, which the admission refuses as non-unverified) — the call
re-runs the persisted boundary enumeration for that record — every persisted
lease entry, regardless of the guard-epoch comparison, exit-confirmed against
the live lease state under its stored ownership identity (§11) — and, on a
clean boundary under the variant's rule there (never a force-clear on members
still present), drops the intent, transitions the record to `interrupted`, and
clears the quarantine marker in the same atomic write; on members still
present it refuses with the transient busy form, never a force-clear.
`orphan-unverified` (not `failed`) on fencing timeout keeps the quarantine
record resolvable via `orphan-resolve`, which refuses non-unverified records.
The quarantine marker clears exactly one way — the operator confirming the old
remote command dead out-of-band and resolving through the authenticated
`evener/host/orphan-resolve` mutation (which verifies the persisted remote
boundary before clearing) — never through a later boot's boundary enumeration
(boot-enumeration clearing covers only local-reap `orphan-unverified` records,
whose boundary is local and boot-enumerable; a fencing-quarantine record's
boundary is remote, so boot skips it — its enumeration is unavailable at boot,
which fails closed and keeps the record open) — and the next `deploy`/`restart`
past the cleared marker runs its kill/wait + guard advance under a fresh epoch
before any mutating remote step, converging the fencing the timeout skipped. A
fencing timeout therefore leaves a fencing-failure outcome (terminal for the
operation's result) on an `orphan-unverified` record plus a closed host, never
an operable one — the host is operable again only after fencing is confirmed,
never merely after the gate released. While the marker is open for a host,
that host admits no new lifecycle or mutation call past admission: `plan`,
`deploy`, `restart`, `add`/`update`/`remove` for that name, `teardown-retry`,
`attach`, and `Ensure`-triggered work all refuse with the typed
fencing-failure form naming the quarantined host — scoped to that host's name
only, like the orphan-unverified fence above — and only the read-only calls
(`list`, `status`, `operations`, `running`) and the `orphan-resolve` way out
bypass it.

The remote lease wrapper is a versioned shell helper (`evener-fence`, version
1 — install path `~/.local/share/evener/fence`, version pinned in the fencing
epoch record) installed on the target host OUT-OF-BAND before the first fenced
operation (reusing the 04b bootstrap-on-bare-host seam for delivery only).
Every worker VERIFIES helper presence plus version string BEFORE the kill/wait
step with a READ-ONLY remote exec: helper absent → the operation refuses
fail-closed with typed `fencing-helper-absent` (conflict class, data names the
host plus the pinned helper version the operator must install out-of-band —
never `probe-failed`, which names only the deploy-step re-probe read failure,
so the client never mistakes the gate for a retryable probe failure) BEFORE
any remote mutation, never an unfenced push, never auto-install (the operator
installs the helper out-of-band through the one-time migration path below,
mirroring `handler-absent`); an older, incompatible, or explicitly untrusted
helper → refuses fail-closed with typed `fencing-helper-untrusted` (conflict
class, same data shape — host plus pinned version — naming the distrusted
version in place of the absent one) BEFORE any remote mutation, never an
in-band migration. NO direct-SSH kill/wait, NO empty-reads quiescence check,
NO guard advance over an untrusted helper — only the trusted wrapper enforces
the fence, so advancing over an untrusted helper cannot prove no untracked
work survives, and until such a primitive ships the host upgrades only when
the operator installs the pinned helper version out-of-band, exactly like the
helper-absent path. Only a remote that CANNOT run the helper at all (no POSIX
shell at the target path) refuses fail-closed until the operator upgrades the
remote itself out-of-band. Remotes first contacted by this component predate
the helper, so deploy/restart on them refuses fail-closed with
`fencing-helper-absent` until the operator installs the helper out-of-band
(the next operation's pre-fence verification runs a helper self-test
round-trip before kill/wait) before any mutating step; a remote whose platform
cannot run the helper (no POSIX shell at the target path) stays fail-closed
for deploy/restart with `fencing-helper-absent` — reads and `plan` still serve
— until the operator upgrades out-of-band. EVERY mutating SSH command the
controller issues to the host — deploy pushes, remote deploy/restart commands,
and 04b `Ensure`-triggered paths — runs through the wrapper under the
worker's epoch (04b paths under the Ensure operation's own persisted epoch
above, never a borrowed or unrecorded epoch); NO direct-SSH mutating path
survives alongside it (a command that cannot present an epoch is refused by
the guard-file rule above — the ONLY exception is the read-only pre-fence
verification above, taken before kill/wait with no remote state written). Any
unfenced mutator can outlive the guard advance, so every mutating command runs
through the wrapper. `Manager.preflight` one-shots (`uname`/env-probe/`id
-u`/`launch-check` over `runRemote` in
`cmd/evener-hub/internal/sshconn/preflight.go` — strictly read-only, no remote
state written) are EXEMPT from the wrapper and mutation fencing entirely:
gateless `plan` refresh runs them with NO worker epoch and NO operation
lifecycle (read-only probes write no remote state and `plan`'s gateless
refresh has no epoch to present). A host WITHOUT an installed helper therefore
cannot accept a remote mutation at all — the no-overlap guarantee holds
vacuously, never as an unfenced exception.

First-ever contact with a never-provisioned host is ONE EXEMPT delivery step
otherwise forbidden: the 04b `bootstrapHub` first-attach repair, starting a
stopped hub through supervisor/ad-hoc launch and shipping the deployed payload
with helper bytes inside, runs UNFENCED EXACTLY ONCE per never-provisioned
host (a host the controller NEVER fenced an epoch on AND whose sidecar entry
carries NO `helperInstalled` flag yet — NEVER a host with a prior fenced
epoch, a prior helper version record, or an interrupted record from a crashed
incarnation, where prior remote work may still run and an unfenced window
would overlap it) under the worker's persisted epoch. No other mutating step
shares the exemption. Before ANY remote side effect the worker persists a
durable bootstrap-attempt fence on the host's sidecar entry in its OWN atomic
sidecar write: the attempt fence lands BEFORE the first remote side effect, so
a crash before `helperInstalled` lands leaves the host attempt-fenced, never
never-provisioned again. Delivery is fenced against OTHER controllers' and
earlier unrecorded work, not only this controller's flags: delivery is
permitted ONLY after a host-side atomic bootstrap/fencing primitive proves the
target truly bare — unfenced delivery opens by ATOMICALLY claiming AND
quiescing through a PRE-EXISTING TRUSTED host-side primitive (a single atomic
claim-plus-quiesce naming this controller's fencing epoch — concurrent
claimants serialize on the host, exactly one wins, quiesce holds for the
ENTIRE delivery so no process/controller starting after the claim can overlap
it — NEVER an ordinary-SSH claim-then-check pair, whose check cannot cover
mid-delivery starts; where NO such host-side primitive exists delivery is
UNAVAILABLE — out-of-band helper provisioning required), verifying NO foreign
process or foreign guard holder live as part of the same atomic quiesce (no
running evener-managed process outside the claimed guard's ownership, no live
foreign guard claim), and only then delivering; a lost claim race, an
unavailable primitive, or any live foreign presence refuses fail-closed with
the typed `fencing-helper-absent` refusal — the exemption NEVER degrades to
overwrite — so the bare-host proof holds regardless of which controller last
touched the host. Ordinary-SSH claim-then-check cannot cover processes
starting mid-delivery and controller-local flags cannot see other
controllers' work, so only the atomic claim-plus-quiesce proves bareness.
Delivery converges the flag in the same step: the worker sets
`helperInstalled` on the host's sidecar entry in the SAME atomic sidecar write
finalizing bootstrap (or refuses finalize on failure). A host carrying the
attempt fence WITHOUT `helperInstalled` takes the FENCED path on every later
attempt — a bare host with NO pre-existing trusted host-side claim/quiesce
primitive is NOT bootstrapable through the UI at all (the operator provisions
the helper out-of-band through the one-time migration path; the §17 acceptance
criteria promise add-from-UI plus Connect only for helper-capable hosts, never
bare-metal first contact). Recovery first re-probes the remote and verifies no
bootstrapped process from the crashed attempt is live (or the operator repairs
out-of-band through the one-time migration path), and only then runs the next
mutation under the worker's persisted epoch. A retry racing the finalize
replays under dedup rather than running a second delivery.
## 10. Last-known store

The manager owns a per-host last-known store — the latest preflight facts with
their capture timestamp plus the latest attach error with its timestamp, plus
the last-known running revision/health/process-start-time snapshot and the
last plan-time refusal (the inputs behind `status`'s `restartFollows` and
`planRefusal` — without these a no-dial `status` read cannot produce those
fields), keyed by the host's registry (generation, incarnation id) pair and
updated on every successful preflight and every attach outcome. Pair-keying
keeps removed-incarnation facts from surfacing under a re-added entry that
reuses the generation. Every `plan` call publishes into it under the gate
before returning — except the pre-acquisition no-token refusals
(`unattached`, `refresh-failed`, `probe-failed`), which occur before any
acquisition and publish gateless, never by acquiring the gate to do so
(holding the gate across the SSH preflight refresh or the running channel
probe reintroduces the slow-probe busy-refusal bug): a refusal publishes its
`{terminal, message}` as the pair's plan-time refusal (a later success clears
it), and every completed probe — success or authenticated failure — publishes
the probed running revision/health plus the probed `processStartTime` when
carried (or their absence) as the pair's running-state snapshot, so `status`
after a plan refusal renders the refusal and the probed state instead of stale
data from an older pair (snapshots from a superseded (generation, incarnation
id) pair stay absent by the pair key). `list`/`status` read it read-only,
never dialing and never constructing facts from the channel. Removal clears
the outgoing generation's entry only after its replacement lifecycle handles
are drained (§6), and re-add starts its new generation with a cleared entry
alongside the name-keyed cache clearing (§15) — facts from the removed
incarnation can never surface under the new one. Successful deploy/restart
workers publish their verified post-operation refresh here — keyed to the
operation's pinned (generation, incarnation id) pair, so a concurrent mutation
cannot misattribute it — before marking `complete` (§7), so `status` reports
the new installed version once the operation reads `complete`.

## 11. Protocol types

Every new method gets its AppWire protocol catalog entry
(`appwire/protocol.go`, `ScopeHub`) plus request/response structs
(`appwire/types.go`) plus the regenerated TypeScript client — the hand-written
structs in the same PR as the handlers, the public catalog registration plus
the regenerated client in 08b with the union-registration generator work for
the union-shaped methods (§14; non-union 08a helpers register immediately).
`evener/host/attach` keeps its shipped shapes (`HostAttachParams{host}`,
`HostAttachResponse` identity fields) unchanged; every new type below follows
the same conventions (`name` names the host in every host-management request
field and in `HostRow` — distinct from `attach`'s shipped
`HostAttachParams{host}`, which is unchanged; `HostPlan.host`/
`OperationRecord.host` — the plan/token and operation-record bindings, not
request fields — keep `host`); lowerCamel JSON throughout, optional
facts/error fields absent — never null — when unknown (the absent-when-unknown
rule).

Generator mapping: the AppWire TypeScript generator emits Go structs as
interfaces and named string types as plain `string`, with no
discriminated-union or literal-union emission (`internal/appwirets/emit.go`
`typeExpr` — the only unions in `types.gen.ts` are the hardcoded
`ThreadItemEventKind` / `NavigationTargetKind` / name-catalog exceptions). The
literal string unions below (`origin`, `outcome`, `reason`, `kind`, `state`)
are wire value sets carried in the generated client as `string`, with the
exact value set pinned by the protocol-shapes test rather than a TS literal
union; the response unions below (the mutation-result arms, `plan`'s token vs
no-token shapes) are carried as per-arm interfaces selected at runtime by
their discriminator (`outcome`, `reason`), never as a generated TS union —
the 08b catalog defines one named Go struct per arm so each generates its own
interface field-for-field. No generator change is needed for TS
discriminated/literal-union emission — the contract's one-named-Go-struct-per-arm
rule (mutation-result arms, `plan`'s planned vs no-token arms,
`teardown-retry`'s six `outcome` x `hostKind` arms — the two success outcomes
(`teardown-complete`, `already-cleared`) plus the
`committed-with-teardown-failure` timeout arm, each crossed with `hostKind:
live | removed` — three outcomes times two host shapes is six declared arms)
exists only so each arm generates its own interface, with the 08b
protocol-shapes test pinning the wire shapes and the `outcome` discriminator
on both `plan` arms; the frontend selects arms at runtime on the
discriminator, never on a generated TS union. Arm registration: `EmitCatalog`
emits only each method's `Result`-named type plus types transitively reachable
from its fields (`internal/appwirets/emit.go` `registerTopLevel`/`discover` —
an anonymous struct `Result` panics the generator, and an `any`/`interface`
field emits `unknown`), so sibling arm structs are NOT emitted by being named
in prose. 08b therefore extends the generator with explicit union support
(generator work lands in 08b): the catalog declares one named Go struct per
arm plus a named union registration referencing every arm, and the generator
emits each arm as its own interface with the method's `MethodTypes` result
entry typed as the union over the arm names; the 08b protocol-shapes test pins
every arm field-for-field (including each arm's discriminator) — and the
union-shaped catalog/client changes for the 08a methods (the mutation-result
arms, `RemovedRow`, `teardown-retry`'s outcome arms) land in 08b with that
generator work, never in 08a: 08a ships the sidecar/commit behavior behind
hand-written request/response types plus the store-skeleton helpers, and the
regenerated client for the union-shaped 08a responses arrives with 08b. The
single-response-interface alternative is rejected: collapsing the arms would
force optional-ified `host`/`token`/`reason` fields the absent-when-unknown
rule cannot distinguish.

- `evener/host/list`: params `{}`; response `{hosts: HostRow[]}`. `HostRow` is
  the full effective `HostConfig` fields (`name`, `ssh`, `user`, `evenerPath`,
  `configPath`, `addr`, `roots`) plus live state — wire JSON is lowerCamel
  throughout (`evenerPath`, `configPath`); snake_case (`evener_path`,
  `config_path`) is the TOML-file spelling only (`config.go` TOML tags), never
  the wire: `attached: bool`, `installedVersion?: string`,
  `installedVersionAgeSec?: number`, `osArch?: string`,
  `lastAttachError?: string`, `lastAttachErrorAgeSec?: number`, `midEnsure:
  bool`, `origin: "hub.toml" | "sidecar"`, `removed: bool`, `retainedRows?:
  number` (tombstone rows only), `rowsTruncated?: bool` (present as `true`
  exactly on tombstone rows whose retained projection was truncated at the
  500-row/1 MiB persist bound — §15; absent everywhere else per the
  absent-when-unknown rule), `generation: number`, `incarnationId: string`
  (the live entry's current incarnation id — the second half of the
  guarded-mutation pair `update`/`remove` require as `expectedIncarnationId`
  alongside `expectedGeneration`; the UI echoes both values from the
  `list`/`status` row), `escalationAgeSec?: number` (present only on tombstone
  rows whose name holds an open remnant past the escalation bound — the
  escalation age the expiry-escalation rule promises; absent everywhere else
  per the absent-when-unknown rule). Tombstone values: a tombstone row renders
  from retained effective `HostConfig` with `attached: false`, `midEnsure:
  false`, and the removed entry's `origin`, `generation`, and
  `incarnationId`; `installedVersion?`, `installedVersionAgeSec?`, `osArch?`,
  `lastAttachError?`, and `lastAttachErrorAgeSec?` stay absent (never null)
  per the absent-when-unknown rule — every other `HostRow` field carries the
  explicit value above, so no non-optional field is left unknown.
- `evener/host/add`: params are one full host entry (all seven `HostConfig`
  fields; `name` required) plus optional `mutationId: string` (opaque,
  non-empty, at most 128 bytes — the idempotency key; §5); response is the
  mutation-result union below.
- `evener/host/update`: params `{name: string, entry: <the six non-name
  `HostConfig` fields>, mutationId: string, expectedGeneration: number,
  expectedIncarnationId: string}` — the idempotency key and the (generation,
  incarnation id) guard, all three required together, with the
  check-and-refusal semantics in §4 (presence of all three validated before
  the dedup check — missing any → validation refusal committing nothing;
  `expectedGeneration` AND `expectedIncarnationId` checked under the mutation
  lock against the target's current (generation, incarnation id) pair before
  staging — past the dedup check, so a replay never reaches it; mismatch on
  either → the same typed `stale-entry` refusal committing nothing — a keyed
  replay returns the recorded receipt before the stale check, so a
  lost-response retry recovers its outcome instead of refusing as stale; the
  UI retry path always sends all three); response is the mutation-result
  union below.
- `evener/host/remove`: params `{name: string, mutationId: string,
  expectedGeneration: number, expectedIncarnationId: string}` — the
  idempotency key and the (generation, incarnation id) guard, all three
  required together (unlike `add`, where `mutationId` stays optional —
  `update` requires all three fields identically), with the same
  check-and-refusal semantics as update; response is the mutation-result
  union below — the clean path returns `{outcome: "committed", host:
  RemovedRow}`, and only the union's failure arm describes the failed-rebind
  shape (`RemovedRow` is the dedicated removed-row arm — `{name, removed:
  true, retainedRows}` plus the tombstone's retained effective `HostConfig`
  fields, `attached: false`, `midEnsure: false`, and the removed entry's
  `origin`, `generation`, and `incarnationId` per `list` tombstone values,
  plus the same optional `escalationAgeSec?: number` as `HostRow` (present
  only when the row's name holds an escalated open remnant) and the same
  optional `rowsTruncated?: bool` as `HostRow` (present as `true` exactly when
  the tombstone's retained projection was truncated at the 500-row/1 MiB
  persist bound) — and is NOT a `HostRow`: the catalog + regenerated client
  carry it as its own interface, and both it and `HostRow` carry
  `incarnationId` in the generated protocol/client types, so the UI can always
  construct the guarded-mutation pair). A replay carrying a known key returns
  the recorded receipt without re-applying.
- Mutation-result union (add/update/remove): every add/update/remove handler
  returns either `{outcome: "committed", host: HostRow}` (remove's clean path
  returns `{outcome: "committed", host: RemovedRow}`) or the failure arm
  `{outcome: "committed-with-teardown-failure", seam: string, remnantId:
  string, host: HostRow}` (remove's failure arm carries `host: RemovedRow`
  for the same reason) or the dropped arm `{outcome: "collision-dropped",
  droppedEntry: HostConfig, winningFingerprint: string, host: HostRow}` (the
  dropped arm always carries the authoritative `HostRow` regardless of
  mutation kind — when the post-rename reconcile drops the just-committed
  sidecar entry, the authoritative result is the winning `hub.toml` live
  entry, never a tombstone, so a remove whose name was re-added through
  `hub.toml` mid-remove returns the live row — the arm names the staged entry
  the post-rename reconcile dropped plus the winning `hub.toml` fingerprint
  the receipt carries, so a replay returning it can never read as a live
  commit) — a normal result-union response, never an AppWire error-envelope
  throw (pre-commit failures throw typed error envelope codes; post-commit
  outcomes return through the union — the failure arm names a committed
  mutation whose teardown needs forward retry) — the `remnantId` is mandatory
  on the failure arm, `seam` names the failed rebind step, and the committed
  row is always present so the UI renders the row with a teardown-retry
  affordance. The catalog carries all three arms field-for-field and the
  regenerated client carries each arm as its own interface through the
  generator mapping above; a shape missing `remnantId` on the failure arm
  fails the protocol-shapes test.
- `evener/host/status`: params `{name: string}`; response is the host's
  `HostRow` plus the deploy plan inputs: `controllerBuild: string`,
  `resolvedTargetPath?: string`, `restartFollows?: bool` (absent — never null
  — when unknown: `status` never dials, so an offline or never-attached host
  may have no facts to compute it from — `restartFollows` and `planRefusal`
  render from the (generation, incarnation id)-scoped running-state/refusal
  snapshots in the manager store (§10), and stay absent when no snapshot
  exists for the current pair), `factsRevision?: string`, `factsAgeSec?:
  number`, `planRefusal?: {terminal: bool, message: string, reason:
  "unattached" | "refresh-failed" | "probe-failed" | "handler-absent" |
  "remnant-open" | "controller-dirty" | "target-unwritable" |
  "target-missing-prereq" | "target-unit-findings", remnantId?: string,
  attached: bool}` (`remnantId` present exactly on the `remnant-open` arm —
  the blocking remnant's id, mirroring `plan`'s no-token arm field-for-field;
  absent on every other arm per the absent-when-unknown rule — and `attached`
  naming the attach state `plan`'s no-token `staleFacts.attached` carries, so
  a `status`-only reader seeing `reason: "remnant-open"` can name the blocking
  remnant for the teardown-retry affordance). Only `plan` branches on the
  `reason` discriminator to mint-or-refuse; `status` stays display-only, but
  the cause travels with the display instead of stopping at the boolean.
- `evener/host/plan`: params `{name: string}`; response is either `{plan:
  HostPlan, token: string, outcome: "planned"}` or `{outcome: "no-token",
  staleFacts: {message: string, attached: bool, reason: "unattached" |
  "refresh-failed" | "probe-failed" | "handler-absent" | "remnant-open" |
  "controller-dirty" | "target-unwritable" | "target-missing-prereq" |
  "target-unit-findings"}, terminal: bool, remnantId?: string}` — `terminal`
  is `true` exactly on the `controller-dirty` / `target-unwritable` /
  `target-missing-prereq` / `target-unit-findings` arms (a dirty controller, a
  missing curl, an unwritable deploy target, 04b unit findings: retry cannot
  clear them, so the UI renders the terminal affordance, never
  retry-with-backoff) and `false` on the five retry arms; `remnantId` present
  exactly on the `remnant-open` arm (the remnant fence: an open remnant for
  the name refuses `plan` with no token minted — §6; absent on every other
  no-token arm per the absent-when-unknown rule) — `outcome: "no-token"` is
  the top-level discriminator on the no-token arm (the token arm carries
  `outcome: "planned"` alongside `plan`/`token`), so the generator mapping
  selects arms on `outcome` with no presence-based exception — the `reason`
  discriminates the failure: `unattached` (host not attached — Connect first),
  `refresh-failed` (attached, but the ungated preflight refresh failed or
  timed out — retryable diagnostics, never Connect-first), `probe-failed`
  (attached, but the `evener/host/running` probe read failed or was
  unauthenticated), `handler-absent` (attached, but the remote predates the
  handler — take the one-time migration path), `remnant-open` (an open
  teardown remnant fences the name — resume it through `teardown-retry`
  first; the `remnantId` field names the blocking remnant),
  `controller-dirty` (the controller is dirty — rebuild from a clean tree,
  never retry the plan), `target-unwritable` (the resolved deploy target is
  not writable — fix the target, never retry the plan),
  `target-missing-prereq` (a deploy prerequisite is missing on the target,
  e.g. no curl — install it first), `target-unit-findings` (the 04b unit
  decision reports findings blocking deploy — resolve them first). `HostPlan`
  is `{host, generation, targetPath, controllerRevision, restartFollows,
  factsRevision, hubTomlFingerprint, factsCapturedAt: string (RFC3339),
  factsAgeSec: number, runningVersion: string, runningHealthy: bool,
  runningProcessStartTime?: string (RFC3339)}` — `runningProcessStartTime`
  present exactly when the probe carried it (absent otherwise per the
  absent-when-unknown rule) — the token's bindings (including the bound
  `factsCapturedAt`) plus the server-generated facts capture timestamp and age
  rendered behind the deploy confirmation's freshness line. `plan`/`token` are
  absent — never null — on the no-token response, per the absent-when-unknown
  rule.
- `evener/host/running` (controller-side method, same-scope 08b — catalog
  entry + TypeScript client with the handler): params `{}`; response
  `{buildRevision: string, healthy: bool, processStartTime?: string
  (RFC3339)}` — `processStartTime` present exactly when the serving hub knows
  its own process start time (absent — never null — otherwise per the
  absent-when-unknown rule). An unverifiable revision (`"dev"` or a dirty
  `"<sha>-dirty"`, which name no code) never proves currency by revision
  equality: `plan` treats a probed unverifiable revision as outdated (restart
  follows) — no timestamp comparison exempts it (the processStartTime the
  probe may carry is bound into the token and checked same-clock
  probe-to-probe at deploy step (3) — §8 — never compared against controller
  wall-clock at plan time) — and deploy/restart verification fails closed on
  an unverifiable revision with no usable `processStartTime`; the
  process-instance discriminator keeps two different dev builds — or two
  processes running the same one — distinguishable, so a plan cannot omit a
  required restart while post-operation verification reports false success.
  Served locally by every hub — `buildRevision` from the same source as the
  `controllerBuild` plan input, `healthy` the hub's own health, computed
  authoritatively as follows (this paragraph is the definition — no other
  signal counts): the serving hub reports `healthy: true` exactly when all
  three hold — (1) its local liveness check passes (the hub process is serving
  this request, not mid-shutdown: the handler runs on the live request path,
  so reaching it proves it); (2) no restart-required condition is outstanding
  under the serving hub's dedicated local health predicate — the hub evaluates
  its own session/daemon ownership from its local controller roster directly
  (never by reusing the `restartRequiredDaemon` authenticated-probe path,
  which determines ownership from the probing controller's roster and cannot
  determine a remote hub's restart-required state): a daemon pid on an
  incompatible protocol, or any condition that would make the hub report
  `ThreadStatusRestartRequired`, forces `healthy: false`; (3) the hub's
  durable state roots are writable (state-root write probe — a real atomic
  temp-plus-rename probe inside the state dir (create uniquely named temp per
  probe — pid plus a per-process counter, never a shared probe name — fsync,
  rename to a DISTINCT probe target in the same dir (a second unique
  probe-prefixed name, never the temp's own name — a self-rename is a no-op
  and tests nothing), fsync the dir, then remove — never the live store or
  sidecar names — plus a free-space query against the state root; concurrent
  probes never share a rename target, so they neither race nor serialize each
  other; a probe temp orphaned by a crash carries the probe-name prefix and
  boot prunes prefix-matching strays before serving) — the probe exercises
  the write path deploy depends on without TOCTOU gaps, racing probes, or
  leaking crash temps — a read-only or full disk forces `healthy: false`,
  since the hub could neither persist an operation record nor a sidecar
  commit). Anything else — session counts, load, peer reachability, external
  dependency status — never feeds `healthy`. `healthy: false` is data, never
  a probe failure: each forced-false case returns `healthy: false` while the
  probe itself still succeeds — a responding-but-degraded hub binds
  `runningHealthy: false` into the token, and a health change between plan
  and deploy invalidates the plan exactly like a revision change. `GET
  /api/health` stays the human-readable surface (it carries no `healthy`
  field and is not the authority — `evener/host/running` is) — admitted only
  over an attached session peered by the #1603 handshake (read
  classification; never forwarded onward to a third hub; browser-origin and
  forwarded requests refused exactly like every other `evener/host/*` request
  — the direction-scoped peer-probe exception of §3 and §8). The `plan` probe
  calls it through `sshManager.ChannelIfAttached(name)`; its response fields
  are what `plan` records as `HostPlan.runningVersion` / `runningHealthy`.
- `evener/host/teardown-retry` (mutation): params `{remnantId: string}`;
  response six declared arms — three outcomes crossed with both host shapes:
  `{outcome: "teardown-complete" | "already-cleared" |
  "committed-with-teardown-failure", hostKind: "live" | "removed", host:
  HostRow | RemovedRow, remnantId: string, escalationAgeSec?: number}` — a
  retry whose bounded teardown run times out returns the timeout arm — the
  same failure outcome as the mutation-result union, carrying the still-open
  remnant's details for a later retry — and the outcome is explicitly not an
  error-envelope response. `escalationAgeSec` is present when the named
  remnant was past the escalation bound at execution — the escalation age the
  expiry-escalation rule promises on `teardown-retry` responses; absent
  otherwise per the absent-when-unknown rule. `hostKind: "live"` pairs with
  `host: HostRow`, `hostKind: "removed"` pairs with `host: RemovedRow` (the
  same tombstone shape `remove`'s clean path returns). An already-cleared ID
  returns `{outcome: "already-cleared", ...}` with the same `hostKind` pairing
  for idempotent lost-response retry — the cleared-remnant marker (remnantId
  → `clearedAt`) persists in the sidecar past the clearance so a later
  lost-response retry still returns `already-cleared`, and is purged only by
  the name's next re-add or retention-expiry prune (plus the cleared-marker
  TTL compaction in §6 — past the TTL the ID reads as
  `teardown-unknown-key`). When the remnant belongs to a `remove` there is no
  live row to return: `host` is the same `RemovedRow` `remove`'s clean path
  returns — same values, same tombstone rendering — with `hostKind:
  "removed"` (vs `"live"` for the `HostRow` arm). The retry validates against
  the remnant's pinned `(generation, incarnationId)` + `cleanupHandle` — the
  live entry may be absent or newer without blocking it — and never acts
  against the live entry.
- Mutation conflict: `conflicting-mutation-id` rides the same AppWire error
  envelope as `conflicting-operation-id` and is refused the same way.
- `evener/host/deploy`: params `{name: string, token: string, operationId:
  string}` (client operation ID: opaque, non-empty, at most 128 bytes);
  response `{id: string, clientOperationId: string, state: OperationState}`
  (`id` is the controller-assigned record id): the freshly created record
  reports `"pending"`, and a dedup hit returns the existing record's actual
  state (`pending`/`running`/`complete`/`failed`/`interrupted`/`orphan-unverified`)
  — the same `OperationRecord.state` the `operations` method returns.
- `evener/host/restart`: params `{name: string, operationId: string}`;
  response `{id: string, clientOperationId: string, state: OperationState}`
  (fresh create: `"pending"`; dedup hit: the existing record's state, as for
  `deploy`).
- `evener/host/orphan-resolve` (mutation, same-scope 08b — catalog entry +
  TypeScript client): params `{id: string}` (the controller-assigned record id
  of an `orphan-unverified`-class record — a local-reap `orphan-unverified`
  record or a fencing-quarantine record carrying the timed-out epoch's
  persisted remote boundary (§9); the quarantine marker clears in the same
  atomic write that resolves its record); response the updated
  `OperationRecord` — always the resolved record, without `orphanBoundary`
  (members still present never reach the response: they refuse transient-busy
  per §9). Admission is session-authenticated like every other
  `evener/host/*` request; unknown `id` → typed not-found, non-unverified
  record → typed validation refusal, members still present → the transient
  busy form (never a force-clear). The call never creates an operation record
  of its own and never advances the host generation — it transitions the named
  record (`orphan-unverified` → `interrupted` on a clean boundary) under the
  store mutex in one atomic write — the call itself is never fenced by the
  orphan admission gate (it is the gate's way out) — then the host admits new
  operations past admission again.
- `evener/host/operations`: params `{name?: string, operationId?: string,
  state?: OperationState, generation?: number, incarnationId?: string, id?:
  string, limit?: number, cursor?: string}` — `operationId` matches the
  client-supplied `clientOperationId` (never the controller-assigned `id`);
  `generation` selects the incarnation after client operation-ID reuse
  (omitted: the current generation); `incarnationId` narrows that selection to
  the exact incarnation — required alongside `generation` whenever the caller
  names a superseded pair, so the same-generation incarnation collision (a
  boot-merge live entry sharing an open remnant's generation with a different
  incarnation id) is addressable rather than ambiguous; `id` is the detail
  filter for the controller-assigned record id — the busy payload's
  open/wait-able reference resolves through it directly — (all optional
  filters plus pagination; empty params lists the first unfiltered page
  (cross-host, no generation pin — `hostBoundaries` returned; "current
  generation" is reserved for calls that name a host); response
  `{operations: OperationRecord[], generation?: number, incarnationId?:
  string, hostBoundaries?: {[host: string]: {generation: number,
  incarnationId: string, compactSeq: number, presenceEpoch: number} |
  "absent"}, nextCursor?: string}` — `generation`/`incarnationId` are present
  exactly on host-pinned pages (the single host named by the request — the
  effective pair listed, echoed back with `cursor` on later pages), and ABSENT
  on unfiltered cross-host pages spanning hosts and incarnations — no
  convention can name one pair for many — `hostBoundaries` is authoritative
  there instead, one `(generation, incarnationId, compactSeq, presenceEpoch)`
  boundary per every host in the query at cursor creation — hosts with no rows
  on the page encode as the `"absent"` marker, never by omission —
  `presenceEpoch` the per-host removal/presence epoch of §15, validated on
  every continuation — `limit` defaults to 50 and caps at 200; responses never
  exceed the cap, and an unfiltered call pages instead of returning the whole
  store. `OperationRecord` is `{id, clientOperationId, host, generation:
  number, incarnationId: string, kind: "deploy" | "restart", state: "pending"
  | "running" | "complete" | "failed" | "interrupted" | "orphan-unverified",
  orphanBoundary?: BoundaryEntry[], progress: ProgressEntry[], result?: {ok:
  bool, message: string}, createdAt: string, updatedAt: string, hostRemoved:
  bool, compacted?: true}` — `incarnationId` is the pinned incarnation the
  record ran against (the dedup scope's second half — §7 — without it the
  response cannot distinguish the colliding same-generation incarnations);
  `orphanBoundary` is present exactly on records whose `state` is
  `orphan-unverified` (absent on every other state per the absent-when-unknown
  rule) — `BoundaryEntry` is a four-variant union whose `kind` discriminator
  selects the boundary the record's open state requires, so every state
  recovery opens is representable:
  `{kind: "local-linux", cgroupId: string, nonce: string, pid: number,
  startTime: string} |
  {kind: "local-darwin", pgid: number, sessionId: number, pid: number, startTime: string} |
  {kind: "local-markerless", platform: "linux" | "darwin", cgroupId?: string, pgid?: number, sessionId?: number, nonce: string} |
  {kind: "remote-fencing", fencingEpoch: {bootId: string, opSeq: number}, guardEpoch: number, leaseEntries: {command: string, registeredAt: string, ownership: {pid: number, pidStartTime: string} | {nonce: string} | {cgroupId: string}}[]}` — every lease entry carries its required ownership identity (the remote PID plus its start time, or the wrapper's per-spawn nonce minted at registration, or the remote cgroup/job-object membership where the platform supports it — the same three tokens the fence-takeover kill verifies before signaling, so a reused PID never kills unrelated work: a lease entry persisted without its ownership identity fails closed at verify time — no kill, no clear) — and the field is an explicit per-member array of it — persistence, wire responses, and resolution all carry the same `BoundaryEntry[]`, an empty array meaning no spawned subprocess survived the crash and an absent field meaning a pre-spawn crash with no boundary to verify — the `kind` discriminator selects the ownership data the verifier needs (marked local Linux: the kernel-enforced cgroup identity plus the launcher-observed `pid`/`startTime` instance marker bound to the pre-spawn server nonce, carried ON THE WIRE — cgroupfs hosts no app-written marker file, so the nonce binds to kernel-owned process identity instead, and the pair is NOT derivable from `cgroupId`+`nonce`: the wire carries the persisted pair verbatim, so the verifier never reconstructs it — the kill requires BOTH cgroup membership AND a (pid, start time) matching the launcher-observed pair, and a member matching no persisted pair reads as already clean while a boundary whose pair was never persisted fails closed with no kill; marked local Darwin: the (pgid, session id) pair plus the launcher-observed (pid, start time) instance marker — a pid whose start time differs names a different process and reads as already clean — and where the boundary holds multiple SSH subprocesses the array holds one entry per spawned process, matched member-by-member at verify time) — the kill requires kernel-attested instance identity plus membership, never membership alone — markerless local (`local-markerless`, the persisted pre-spawn boundary only — platform plus the cgroup/pgid identity and the nonce, never a launcher-observed pair because the crash landed before the marker persist: boot and `orphan-resolve` apply no mismatch rule to it — a mismatch judgment needs a persisted pair to compare against — so enumeration of a markerless boundary refuses the transient busy form whenever members are still present and drops the intent only on a demonstrably empty boundary) — remote fencing (`remote-fencing`, the timed-out operation's fencing epoch plus the guard-file epoch plus the superseded epoch's lease-tracked entries — the guard epoch and lease ownership `orphan-resolve` needs — every lease entry carrying its required ownership identity above: on a resolve call the hub ALWAYS enumerates and verifies every persisted lease entry against the live lease state under the caller's session authentication, REGARDLESS of the guard-epoch comparison — a guard advance fences future actions only and never proves already-running lease commands exited, so a live guard epoch no longer equal to the persisted `guardEpoch` is never on its own proof of a clean boundary). The boundary reads clean only after that enumeration confirms exit for every persisted entry — each entry remote-verified against its stored ownership identity before any kill or clear (a PID verified against its stored start time, a nonce re-presented to the lease wrapper, a cgroup membership attested on the remote — a reused PID or an entry whose ownership no longer matches reads as already clean for that member only after the remote confirms no matching live holder, never by controller-side inference; an entry persisted without its ownership identity fails closed — no kill, no clear): a mismatched guard with every lease entry confirmed exited reads clean, while any persisted lease entry still registered live — under either a matching or a mismatched guard — refuses the transient busy form, never a force-clear, and the quarantine clears only after exit is confirmed for all of them — a record carries exactly one variant per persisted state (marked local entries for a local reap with markers, a single `local-markerless` entry for a marker-less local `pending-spawn` record, a single `remote-fencing` entry for a fencing-timeout quarantine record). `compacted` is present as `true` exactly on tombstone replays (absent on live records per the absent-when-unknown rule) — a replay naming a compacted ID returns the recorded terminal outcome with `compacted: true`, and the regenerated client carries both fields through the generator mapping above. `ProgressEntry` is `{ts: string (RFC3339), message: string}`, bounded per record. Pagination ordering and generation pinning: records sort AND resume by the controller-assigned `id` ascending — one ordering for both (the `id` is unique and monotonic per store, so the order is total: concurrent terminal writes can never skip or duplicate a row across pages — and a wall-clock rollback that stamps a later record with an earlier `createdAt` can never move it before the cursor, because `createdAt` is not part of the order at all); `createdAt`/`updatedAt` are stored UTC-normalized (`Z`-suffixed RFC3339 — a stored offset form is converted at write time, never compared lexicographically in its raw form), so timestamps stay comparable for display and freshness while never deciding page order; the cursor is opaque (encodes the last row's durable sequence position — the controller-assigned `id`, which never rolls back — never a bare offset — plus the pinned `generation` and a snapshot/retention boundary: the cursor encodes `(generation, incarnationId, compactSeq, presenceEpoch, lastId)`, where `compactSeq` is the store's monotonic compaction sequence minted in the same atomic write that compacts terminal records — §7). A host-pinned response carries the effective `generation` and `incarnationId` actually listed (the optional pair of the response shape above — absent on unfiltered cross-host pages, where `hostBoundaries` is authoritative), and host-pinned callers pass both values back with `cursor` for subsequent pages: the handler validates the cursor's `(generation, incarnationId)` pair against the request's `generation`/`incarnationId` filter (a mismatch is a typed `stale-entry` re-list refusal, never a mixed page — callers echo the pinned pair from the first-page response with `cursor` on every subsequent page, and an omitted filter on a later page reads as the pinned-cursor window, never as a fresh unpinned query: omitted means "continue the cursor's pin", so a generation advance between pages keeps later pages on the pinned incarnation instead of silently re-pinning to the new generation; a generation-pinned page requires `name` — a cursor minted for one pair validated against an unfiltered query is a typed `stale-entry` re-list refusal — and the map pins EVERY host in the query at cursor creation, not just the hosts present on the page: a host with no records on the page still contributes its current (generation, incarnation id, compactSeq, presenceEpoch) boundary — or its absent marker when the host holds no records at all — and a later page whose stored triple no longer matches the host's current boundary is a typed `stale-entry` re-list refusal, never a mixed page) — and its `compactSeq` against the store's current sequence — a compaction that removed rows at or before the cursor's position since the cursor was minted surfaces a typed cursor-invalidated refusal naming the compaction (the client restarts from the first page), so an advancing generation or a mid-pagination compaction changes nothing silently underfoot — later pages read the pinned incarnation, and a newer generation's records appear only on a fresh unpinned read. `limit` with no `cursor` starts the pinned first page. The cursor is a versioned base64url JSON envelope `{v: 2, pos: [id], bounds: {[host]: [generation, incarnationId, compactSeq, presenceEpoch] | "absent"}, storeEpoch: number}` — sort and resume are the monotonic `id`, so no timestamp is part of the resume position — and a cursor whose envelope version is not 2 is a typed `stale-entry` re-list refusal (fresh read required), never a best-effort decode — `storeEpoch` is the quarantine epoch (§7; pinned at cursor creation; a continuation whose pinned epoch no longer equals the live `quarantineEpoch` is a typed `stale-entry` re-list refusal before any boundary comparison — the stable `compactSeq`/row-ID checks below are meaningless across a quarantine reset whose replacement store restarts both at zero) — the absent marker encodes as the literal string `"absent"` — `presenceEpoch` is the per-host monotonic removal/presence epoch minted in the same atomic sidecar write as every `add`/`remove`/re-add (including tombstone re-add and expiry purge), so a removal that preserves the tombstone's generation and incarnation still advances the epoch — capped at 8 KiB encoded (a first page whose boundary map would exceed the cap refuses with typed `cursor-too-large` (its own envelope discriminator carrying `{capBytes: 8192}` — never `cursor-invalidated`, whose compaction data an over-cap first page cannot produce: no cursor was minted, so there is no `compactSeq` and no pinned pair to name) naming the cap, never a truncated cursor; the 63-host cap bounds the map, so the cap is reachable only with adversarial incarnation-id lengths, never in normal use); a host created after the cursor was minted has no stored triple to validate — later pages skip its records (a newer host's records appear only on a fresh unpinned read, never mid-pagination; a host removed after mint trips the same stored-triple mismatch rule — including its `presenceEpoch` fingerprint, which a removal always advances even when generation and incarnation are preserved, so later pages refuse `stale-entry`).

Typed errors ride the existing AppWire error envelope — the numeric `code`
(`appwire/errors.go`: `CodeInvalidParams` -32602, `CodeConflict` -32013,
`CodeUnavailable` -32014, `CodeInternalError` -32603, `CodeInvalidRequest`
-32600) with the stable discriminator in `data.evenerErrorInfo` plus the
error-specific data fields — and the regenerated TypeScript client branches on
the `evenerErrorInfo` discriminator (with data fields), never on the numeric
`code` alone; the catalog pins the exact numeric code per discriminator, and
the protocol-shapes test asserts the pair. Discriminators: `host-not-found`
(not-found class), `host-busy-operation` (busy class; data names the operation
id), `host-busy-transient` (busy class; no operation reference),
`orphan-fenced-busy` (busy class; the orphan-fence refusal of `teardown-retry`
— data names the blocking `orphan-unverified` record id plus the
`orphan-resolve` next step; every other fenced call keeps the generic
transient busy form, so only the UI-affordance path carries the discriminator
the UI branches on), `stale-entry` (conflict class; data names which of entry,
target, generation, `hub.toml`-fingerprint, running-version, running-health,
facts-age, pruned-generation, or concurrent-terminal-op mismatched or expired
— `entry` (the resolved host entry drifted), `target` (the resolved deploy
target drifted), `generation` (the registry generation advanced),
`hub.toml`-fingerprint (a hand edit landed between validation points),
`running-version` (deploy's re-probed running build differs from the
token-bound revision — §8 step (3)), `running-health` (the re-probed health
flag differs the same way), `facts-age` (the token-bound preflight facts aged
past the freshness bound at deploy time — the token-bound effective bound,
never the owner's current knob — the re-plan refusal), `pruned-generation` (a
same-key replay naming a pruned superseded receipt — §6),
`concurrent-terminal-op` (deploy step (3)'s — or `restart`'s —
post-acquisition scan found an operation on this host with a terminal
transition sequence above the pre-probe position, so the live running state
may have changed inside the probe→acquire window — the re-plan/retry refusal;
the 08b protocol-shapes test pins each value against the path that emits it)),
`cursor-invalidated` (conflict class; data names the compacting `compactSeq`
plus the cursor's pinned `(generation, incarnationId)` — the mid-pagination
compaction refusal above, distinct from `stale-entry`'s generation-mismatch
re-list refusal — and distinct from the over-cap first-page `cursor-too-large`
refusal above (own discriminator, data `{capBytes: 8192}`); the 08b
protocol-shapes test pins both discriminators with their data shapes),
`token-missing` / `token-mismatched` / `token-superseded` / `token-expired`
(conflict class) — a consumed-token replay presents a deleted row and reads as
`token-missing` (consume deletes the row in the same write that creates the
record — §8 step (4); no separate consumed discriminator exists),
`conflicting-operation-id` / `conflicting-mutation-id` (conflict class),
`too-many-hosts` (conflict class; the `ErrTooManyHosts` refusal),
`swap-failed` (internal class; data names the seam), `host-detached`
(unavailable class; deploy's channel-gone refusal — token unconsumed, no
record; UI Connects and re-plans), `probe-failed` (unavailable class; deploy
step (3)'s running re-probe read failed, timed out, or was unauthenticated —
data names the host plus which of read-failed, timed-out, or unauthenticated;
token unconsumed, no record — distinct from `plan`'s no-token `reason:
"probe-failed"` union-arm value, which is never an envelope),
`fencing-helper-absent` (conflict class; the pre-fence helper gate — helper
absent, remote unable to run the helper, or the bootstrap-guard claim
lost/unverifiable (§9): data names the host plus the pinned helper version the
operator must install out-of-band; the UI surfaces the one-time migration
step, never a probe retry), `fencing-helper-untrusted` (conflict class; the
same gate for an older, incompatible, or explicitly untrusted helper: same data
shape, naming the distrusted version — out-of-band install of the pinned
version, never an in-band migration), `concurrent-edit` (conflict class; the
sidecar final check's bounded stage-validate retries exhausted against a
racing `hub.toml` edit — data carries the staged `hub.toml` fingerprint plus
the observed fingerprint; the client re-reads and retries — §6),
`teardown-unknown-key` (not-found class; unknown or purged `remnantId` — never
a present-but-cleared remnant, which returns the `already-cleared` success
arm), `remnant-open` (conflict class; any remnant-fenced path refused (re-add/
`update`/`remove` on the remnant's name, plus `deploy`/`restart`/
`Ensure`-triggered work/`plan`/attach on the name — §6): data names the
blocking `remnantId`; resume it through `teardown-retry` first),
`fencing-failure` (conflict class; any fencing-quarantined path refused
(`plan`, `deploy`, `restart`, `add`/`update`/`remove` for the quarantined name,
`teardown-retry`, `attach`, and `Ensure`-triggered work — §9): data names the
quarantined host; the operator resolves through `evener/host/orphan-resolve`
once the persisted boundary verifies clean), `tombstone-capacity` (conflict
class; the global tombstone-cap persist refused because every eviction
candidate holds an open teardown remnant — data names the bound plus the
blocking remnant-gated names; resolve a remnant through `teardown-retry`
first, then retry the removal — §15), `cursor-too-large` (conflict class; the
over-cap first-page refusal above: data carries `{capBytes: 8192}`, never a
compacting `compactSeq` — no cursor was minted so there is no pinned pair to
name), `session-unavailable` (the #1603 attach classifier); `interrupted` is a
terminal record state (outcome unknown), not a thrown error.
`committed-with-teardown-failure` is NOT an error-envelope code — it is the
mutation-result union's failure arm (a normal result response).
## 12. Error handling

Attach failures: typed `SessionUnavailable` via the #1603 classifier; the
caller-context error stays raw; the UI renders the classification, not the raw
chain. Deploy: dedup-first processing order is fixed (§8); conflicting
operation-ID reuse → typed refusal; invalid/expired/superseded tokens →
refusal, always; detached channel at re-probe time → typed `host-detached`
refusal (token unconsumed, no record; UI Connects and re-plans — matching
`restart` is deliberately NOT the shape: restart attach-firsts because it
spends no token); execution-time re-resolution mismatch — entry, target,
generation, or `hub.toml` fingerprint, on `deploy` or `restart` → typed
stale-entry refusal with a re-plan/retry instruction; a held per-host gate →
typed busy refusal — `host-busy-operation` naming the in-flight operation when
a deploy/restart record holds the gate, including an Ensure-triggered deploy
(a durable fenced op-store record — its busy error names the Ensure operation
exactly like a user deploy, open/wait-able), and transient `host busy (plan in
progress)` with no operation reference only for `plan`'s validation-plus-mint
window, which holds no op-store record; plan-time refusals surface verbatim
(`errControllerDirty` is terminal — the UI must not offer retry-anything;
unmet prerequisites shown before confirmation); runtime failures land in the
operation record verbatim; `interrupted` records tell the user the outcome is
unknown and a new operation may be started. Host busy: a held per-host gate
fails `update`, `remove`, `plan`, and any new `deploy`/`restart` fast with a
typed busy error: the operation-held form names the in-flight operation, and
the UI surfaces "operation in progress" with open-or-wait — an Ensure-held
gate takes this form too, naming the Ensure operation's record; the transient
plan-held form carries no operation reference, and the UI shows "host busy
(plan in progress)" with retry and no open/wait affordance. A refused mutation
leaves the running operation untouched — it finishes and records its normal
terminal state. Hot-apply failures: staging failures change nothing; a failed
swap compensates by restoring the prior sidecar bytes and reverting the
runtime (both before the response), and the error carries which seam failed
(registry / manager / source / admin controller / web view / persistence /
decision-source validation). `evener/host/*` on an unknown host name: typed
not-found.

## 13. UI

Hosts settings section (new `panes/settings/sections/hosts/*`, plus its
registration in the settings section map): host rows with state chip (online /
offline / connecting / removed-retained), installed version + controller
version, OS/arch, origin marker, actions: Connect, Deploy, Restart, Edit,
Remove (each with the confirm pattern used elsewhere in settings), and the Add
button.

Add / Edit dialog: fields exactly the `HostConfig` schema — `name`, `ssh`,
`user`, `evener_path`, `config_path`, `addr`, `roots` (multi-line;
`config.go:32-46` — TOML-file spellings; the wire carries `evenerPath` /
`configPath` per §11) — all seven fields, none invented, none hidden; each
with its validation message mapped from the backend response. Edit does not
offer `name` (immutable).

Connect state machine: offline → connecting (in-flight, driven by the
`attach` response or the host-notification stream) → online (the sources
manifest's online flag flips; the rail and picker update through the existing
06b/07b paths) or failed (typed error shown, retry). The spawn-picker Connect
trigger shipped with #1603 stays as-is; this section is the management view of
the same action.

Deploy confirmation: opening the dialog calls `evener/host/plan` and renders
the confirmation from `plan`'s response — the controller-minted, token-bound
plan: target host, controller revision, resolved remote target path, whether a
restart follows, the running build, facts freshness (from the plan's
server-generated `factsCapturedAt`/`factsAgeSec`), and any plan-time refusal
(terminal ones disable the button). The deploy confirmation submits exactly
the displayed plan: `evener/host/deploy` with that plan's token and a client
operation ID — never a plan the user has not seen. If `deploy` rejects the
token as stale (expired, superseded, or binding-mismatched), the UI re-plans
and re-renders the confirmation from the new response before any retry.
No-token responses from `plan` branch on the `reason` field: `unattached`
directs the UI to Connect first; `refresh-failed` on an attached host surfaces
the refresh failure with retryable diagnostics and a refresh-retry affordance
(never Connect); `probe-failed` on an attached host surfaces the probe failure
with retry (never a Connect loop — and never the helper-gate step, which rides
`fencing-helper-absent`/`fencing-helper-untrusted` instead); `handler-absent`
on an attached host surfaces the one-time migration step (never
Connect-first); `remnant-open` surfaces the blocking `remnantId` with a
teardown-retry affordance (never Connect, never re-plan — a re-plan mints
nothing while the remnant is open). `status` stays the read-only informational
surface behind the host row — it mints nothing and is never the confirmation's
source. Progress renders from `operations` polling (with best-effort stream
events if implemented); terminal failure surfaces the verbatim 04b error; a
retry after a lost response reuses the same operation ID — a replay past
compaction returns the tombstoned terminal result, never a fresh operation.

Stores: follow the 07b host-store pattern (`hostInstancesStore` / per-host
request sequences / connection-generation guards as fixed in #1605). No shared
mutable module state.

## 14. Implementation approach (files/packages, cited seams)

- `cmd/evener-hub/internal/hostreg` — live registry: `Add` exists; add
  `Update`/`Remove` with the same cycle/validation rules (including the
  63-remote-host cap — over-cap `Add`/`Update` fail `ErrTooManyHosts`), safe
  for concurrent use.
- `cmd/evener-hub/internal/sshconn` — `Manager` gains the live host-set
  surface (add/update/remove entry, each safe against in-flight `Ensure` and
  running supervisors) and thin exported entry points for deploy/restart
  reusing the 04b internals (`deploy`, `ensureDecision`, `waitHealthy`,
  `bootstrapHub`), plus an exported deadline-bounded preflight re-read (the
  existing one-shot SSH command sequence re-run on demand, no channel
  involvement — `plan`'s facts-refresh mechanism; §8); plus the internal
  gate-aware `attachUnderGate` primitive (accepts an already-held per-host
  gate, suppresses supervisor startup until the worker's post-verification
  handoff — the restart worker's reattach path, §8); `Ensure`/`Attached`/
  `ChannelIfAttached`/facts unchanged.
- `cmd/evener-hub/config.go` — sidecar load/merge (hard-error duplicate rule)
  + atomic write.
- `cmd/evener-hub/app_host_manage.go` (new) — the handlers, router
  registration, mutation classification (`plan` is a mutation — it mints
  durable state; `list`/`status`/`operations` are reads), guarded by the
  shared origin guard — landed by #1603 at the dial + dispatch seams,
  extended by 08a to the common request-ingress/router boundary as a
  pre-admission hook running before admission (§3) rather than wrapping
  handlers one by one, with ordering tests proving guard-before-admission
  (dedup/token orderings asserted in 08b where they ship) — explicitly NOT in
  `remoteHostAdminMethods` (negative assertion in the allow-list tests).
- AppWire protocol catalog entries for every new method + request/response
  types (hand-written Go request/response structs in the same PR as the
  handlers, so the backend contract is reviewable), with the shapes in §11
  (the wire contract field-for-field; the generated client through the
  generator mapping named there) — but public AppWire catalog registration
  plus the regenerated TypeScript client for the union-shaped methods land in
  08b with the union-registration generator work (§11), never in 08a: 08a
  ships no catalog entry and no regenerated client for a union-shaped
  response, so the "each handler ships with catalog + types + regenerated
  client" contract is satisfied per-PR-sequence — hand-written types in 08a,
  public registration plus generated client in 08b — never by undocumented
  provisional types.
- The operation store: a small durable store (records keyed by operation id,
  dedup index on client operation ID scoped by (host, kind, host generation,
  incarnation id), with the typed conflicting-reuse refusal, the
  `host-removed` never-match rule, and bounded compacted-ID tombstones
  returning the expired/compacted result — §7; atomic temp+rename writes,
  startup reconciliation to `interrupted` plus the tombstone-derived
  `host-removed` pass) + optional host-notification publisher extension. Where
  it lives on disk follows the hub's existing durable-record conventions (the
  implementing session picks the closest existing store pattern and names it
  in the PR).
- `cmd/evener-hub/main.go` — replace the startup snapshot of host entries
  with the live view seam (registry, manager, sources, web config, and the
  host-admin controller's host set/fan-outs); keep every existing
  `RemoteHost*` wiring.
- Frontend: `settings/sections/hosts/*` + settings registration, host-manager
  store, `operations` polling, notification subscription reuse. The spawn
  picker is untouched (its Connect trigger ships with #1603).

## 15. Data flow (remove + tombstone)

`refreshRemoteThreadSnapshot` enumerates the registered sources; removing one
would otherwise drop its rows from the next snapshot. Removal instead writes
an explicit tombstone record for the source (name, the removed entry's
effective `HostConfig` — all seven fields `HostRow` requires, so `list` can
render the removed row without a live entry — last-known-good rows, removal
timestamp, the removed incarnation's id persisted alongside the generation
high-water mark — boot reconciles on the exact persisted (generation,
incarnation id) pair, so a live incarnation sharing the generation but
carrying a different incarnation id never matches) — with a hard bound: at
most 500 retained rows per tombstone and at most 1 MiB of serialized row bytes
per tombstone (owner-adjustable knobs in the same family as the cleared-marker
TTL; the defaults ship in the implementing PR) — plus a GLOBAL cap across all
tombstones in the sidecar/state dir: at most 64 tombstones and at most 16 MiB
of total serialized tombstone bytes (same knob family; defaults ship in the
implementing PR) — enforced deterministically newest-first by removal
timestamp (with the name as tie-break) on every tombstone persist: a persist
that would exceed either global bound first drops the oldest tombstones (with
their receipts and remnants, per the retention rule — §6) until the new
tombstone fits — but open-remnant tombstones are NEVER eviction candidates (a
tombstone whose name still holds an open teardown remnant is skipped by the
eviction scan exactly like the expiry prune below skips remnant-gated names:
evicting it would purge the remnant the gate keeps alive. When every candidate
but the incoming tombstone holds an open remnant and the persist would still
exceed a global bound, the persist refuses with the typed `tombstone-capacity`
conflict error (data names the bound plus the blocking remnant-gated names)
instead of evicting or growing unbounded — the operator resolves a remnant
through `teardown-retry` first, then retries the removal), so repeated
add/remove churn over distinct names within the 7-day window converges to the
newest 64 instead of accumulating unbounded growth that exhausts disk or fails
atomic renames. A per-tombstone bound alone lets distinct-name churn grow the
sidecar without limit, so the global cap holds it. Removal persists a bounded
projection (newest-first by each row's last-updated timestamp with a total
tie-break — (lastUpdated, row id) lexicographic, rows with a missing timestamp
sorting oldest (a missing timestamp never outranks a present one), applied
deterministically on every persist so truncation is stable across retries —
never the snapshot's row order, which is tree order, not chronological, so
truncating by it would keep stale rows and drop fresh ones while
`retainedRows`/`rowsTruncated` misreported completeness — truncated past the
bound) plus an explicit `rowsTruncated: bool` on the tombstone, surfaced in
the `list` tombstone row's `retainedRows` count and the tree merge below
(truncated projections render their stale rows with the truncation indicator,
never as a complete set): the rows stay in the navigation tree marked
**stale**, and the tombstone is **consumed by the tree and the action-capability
logic** — removed-host rows must be visibly non-actionable (markers in the
tree/rail; actions disabled), not merely `Complete == false`. The tree
predicate treats active status as live (`appThreadTreeLive(thread) &&
s.sourceOnline(thread.Source)` in `cmd/evener-hub/web_api_tree.go`, and
`sourceOnline` in `cmd/evener-hub/web.go` deliberately fail-opens unknown
source IDs as online): without a tombstone marker carried into tree/action
logic, a removed host's active rows still satisfied the live predicate and
stayed live and actionable. The merge carries each re-applied row's tombstone
identity through the navigation snapshot (the publication tags every
tombstone-sourced row with its tombstone name, never as an ordinary
live-source row), and the tree/action logic consumes it: tombstone-tagged rows
are forced non-live with capabilities disabled — never through the
`appThreadTreeLive` + `sourceOnline` predicate — while their metadata (labels,
timestamps, truncation indicator) is retained for display. A tombstone-tagged
row therefore renders stale and non-actionable even when its retained status
is active, and deregistration keeps the source-unknown fail-open from ever
promoting it back to live. Tombstoned hosts are not manifest sources: removal
deletes the source-registry entry, so the navigation manifest's `sources`
array drops the removed host on its next read — the manifest element schema is
exactly `{id,label,kind,online}` (`hubapi.Source`, `hubapi/types.go`; the
component-06 read contract pins those four fields and its enumeration is the
registered-source set), with no `removed`/`stale` field to carry a tombstone,
so no schema change is in scope. The tombstone's surviving surfaces are the
stale tree rows and the `list` row (`removed: true`) — the Hosts list reads
tombstones from the controller, never from the sources manifest. This
supersedes the earlier source-lifecycle contracts — the component-03 rule that
the registry is built once at startup with one source per configured host and
nothing added or removed on attach/detach (`sources.All()` always equals the
configured list), the component-04 rule that the manager never adds or removes
a source and no register/unregister path exists, and the component-06 rule
that the registry always carries the full `[[hosts]]` list with detach never
reading as removal: those rules hold only for the pre-08 lifecycle, where
removal was impossible. Under this spec the implementing PR adds the registry
removal the predecessors lacked — a source-registry `Remove(name)` (or
equivalent deregistration) invoked as part of the staged commit's runtime swap
(and its step-(4) rollback, which re-registers the prior set), plus the
fan-out/consumer changes that follow from it: the component-05
`Online()`/broker rebind and the component-06 navigation poke consume the live
host set (removed names leave every fan-out, never linger as offline-present),
and `refreshRemoteThreadSnapshot` enumerates the registered sources
post-removal. A removed host therefore leaves navigation and fan-outs by
deregistration, never as a present-but-offline source. Snapshot merge: every
`refreshRemoteThreadSnapshot` publication merges the retained tombstone rows
back in AFTER enumerating the registered sources — the refresh replaces only
live-source rows, then re-applies each unexpired tombstone's last-known-good
rows (marked stale, non-actionable, plus the tombstone's `rowsTruncated`
indicator when the projection was truncated at persist time), so a removed
host's rows survive every subsequent refresh until its tombstone is re-added
or pruned; a concurrent re-add wins by the generation rule below (its
new-generation publication supersedes the tombstone merge for that name). The
tombstone is purged when the same host name is re-added, or after the
tombstone retention period (owner-set `tombstoneRetention` knob, default 7
days). Expiry mechanism (explicit, no background timer): the controller
evaluates expiry lazily — `list` filters in memory with no lock (§4), and
every sidecar mutation prunes durably in its atomic write under the mutation
lock, and every boot prunes durably in the same atomic-write posture before
serving requests (lazy + mutation-path + boot — no background timer, so a
read-only workload still converges at the next restart; disk growth between
the last mutation and the next boot is bounded by one tombstone plus its
receipts and remnants per removed host) — and drops each tombstone whose
`removal timestamp + retention period` has passed: the read path omits it from
the response without taking the lock, and the next mutation-path atomic
sidecar write under the lock prunes it durably (plus that name's receipts and
remnants, per the retention rule — §6). Expiry never purges a tombstone whose
name still holds an open teardown remnant — the mutation-path prune skips
remnant-gated names, and the in-memory filter keeps rendering them — so the
failed teardown's generation-specific handles are completed through
`teardown-retry` before the gate releases (§6) — with a bounded
operator-escalation backstop: an open remnant older than the owner-set
remnant-escalation bound (a multiple of the cleared-marker TTL, default ships
in the implementing PR) surfaces an operator-escalation signal on the remnant
(`teardown-retry` responses and `list` tombstone rows carry the escalation
age), and the operator resolves it out-of-band (manual teardown of the pinned
target, then a forced clearance through the same `teardown-retry` path); the
gate still never auto-purges an open remnant — the bound escalates, never
silently drops, so storage cannot pin forever without a visible operator
action.

Tombstones are durable: they persist as a section of the managed sidecar
itself (same file, same atomic writes — §6), so a removal's delete-entry +
write-tombstone is one write, boot restores them alongside the host entries,
and they survive controller restarts; re-add and retention-expiry purges
rewrite the same file atomically under the mutation lock.

Host generations: every `add` (including re-add) mints, and every `update`
advances, a per-name generation that is durable: persisted in the managed
sidecar alongside the host entries (including the per-name high-water mark for
removed names) in the same atomic write as the entry mutation — `add` mints
generation 1 for a never-seen name, `update` advances the live generation by
one, and re-add mints a generation strictly greater than any generation that
name has ever carried, so a re-added byte-identical entry still invalidates
every pre-remove token. Every `add`/re-add mints a fresh incarnation id in
that same atomic write (opaque, unique per mint, persisted per live entry next
to the generation — the teardown-targeting identity `teardown-retry` selects
on; §6). The boot load restores persisted generations before the store serves
any request, so the generation check — the token's generation must equal the
registry's current generation — survives restarts: an update-then-restart
keeps outstanding tokens valid (the bumped generation is on disk), and no
restart silently invalidates or re-validates anything. Store-side mirror: the
per-name generation high-water mark is mirrored into the operation store
itself (same atomic store writes as records), and boot takes the maximum of
the sidecar mark and the store mirror as the name's restored generation — with
the missing-sidecar preservation: a name with no sidecar mark contributes no
mark to the maximum (the surviving mirror alone is the high-water mark),
never a zero that drags it down — so deleting a corrupt sidecar and re-adding
an identical host cannot restart its generation at 1 and adopt the old
incarnation's records or tokens: the re-add mints above the mirrored
high-water mark instead. Deleting both durable files is the only clean-slate
path, and the spec names it as such — there is no silent history adoption
either way. Boot-merge collision: when a retained tombstone collides at boot
with a newly live `hub.toml` host (or a live sidecar entry from a re-add), the
live host is treated as a new incarnation: its restored generation is set
strictly above the tombstone's high-water mark before any historical
receipts/remnants apply, so old `host-removed` records stay at or below the
mark and live records stay unmarked — never a promotion of a pre-collision
record into the current generation, never a block on valid operation-ID reuse
— unless the colliding name holds an open teardown remnant, in which case the
bump is forbidden and the live entry stays at the remnant's generation until
`teardown-retry` resolves it (§6 — never a live incarnation over an open
remnant). `hub.toml`-declared hosts carry a stable generation as long as their
effective entry is unchanged — the sidecar persists each declared host's
effective-entry fingerprint (content hash of the resolved entry) alongside the
generations, and boot compares the freshly read entry against it before
serving requests: a mismatch advances that host's generation and clears or
rebinds its name-keyed cached state, so an edit made while the controller was
stopped can never restore the old generation with old records and cached state
reading as current — initial assignment at boot is generation 1 for every
`hub.toml`-declared name with no persisted high-water mark, AND boot mints a
fresh incarnation id for every `hub.toml`-declared name with no persisted
incarnation in that same atomic sidecar write (so token generation checks and
every incarnation-scoped read have a defined pair on both sides; a declared
host whose effective entry changed while stopped still advances its generation
AND mints a fresh incarnation in the same write, and a boot-merge collision on
a declared name takes the same above-mark bump plus fresh incarnation as any
other collision, never a stable generation with an undefined incarnation).
Collision precedence: the boot-merge collision bump above overrides stable
generation for that boot — the live host's above-mark generation replaces the
stable one, and tokens bound to the pre-bump generation are invalidated (their
generation no longer equals current) — since the sibling reconciling commit
detects external `hub.toml` edits after the fact, any detected change to a
`hub.toml`-declared host's effective entry — detected by the sidecar staged
commit's `hub.toml` re-reads (validation, final check, post-rename reconcile,
all under the mutation lock), by a `plan`/`deploy` fingerprint check, or by
the live external-reconciliation path below (never by an unsynchronized
background watcher) — advances that host's generation and clears or rebinds
its name-keyed cached state — the detecting path adopts the re-read entry into
the running registry first, then bumps the generation, then clears or rebinds:
resolved deploy targets, deployment state, last-known facts entries,
supervisor bindings, channel handles, and outstanding tokens — so a stale
snapshot or preflight facts read from the old configuration can never pass a
generation check against the new one. Live external reconciliation: every
`evener/host/*` admission — mutations and `plan`/`deploy` alike, plus
`list`/`status` at most once per admission (cached fingerprint check only —
§4) — and every boot compares the on-disk `hub.toml` fingerprint against the
in-memory fingerprint in a pre-handler step before serving the call (the
`list`/`status` lock-free read itself stays lock-free and serves the last
published snapshot on mismatch — §4): on a fingerprint match the call proceeds
on the current snapshot with no lock; on mismatch a mutation/`plan`/`deploy`
admission takes the mutation lock synchronously and runs the same
adopt-then-bump-then-clear sequence as the sibling reconciling commit (re-read
the file, adopt added/changed declared entries into the running registry, drop
declared hosts deleted from the file out of the registry/manifest/admin host
set and fan-outs — a deleted declared host reads as not-found on all
`evener/host/*` methods until re-declared, and its name-keyed caches clear —
then bump affected generations and clear or rebind every name-keyed cache
above — and the adopt-then-bump-then-clear sequence coordinates with in-flight
operations exactly like `update`/`remove`: before adopting a changed entry,
dropping a declared host, or clearing its manager bindings the reconcile
try-acquires that host's per-host gate; a held gate defers the
adopt/drop-and-clear for that host until the in-flight operation reaches
terminal state — the admitted call proceeds on the pre-reconcile snapshot for
that host meanwhile — while purely added entries and uncontended transitions
(gate free at try-acquire) apply immediately under the mutation lock; a
deferred transition re-runs the same generation bump and cache clear when the
gate releases, so no external edit survives past the in-flight operation's
completion — a changed entry never rebinds the registry entry, generation,
channel, or supervisor under an in-flight deploy/restart still operating on
the pinned old configuration), so no external edit — including a
declared-host removal — survives past the next mutation/`plan`/`deploy`
admission (a `list`/`status` admission serves the last published snapshot
immediately and schedules the same sequence asynchronously, debounced — §4):
the step publishes a new snapshot under the lock and the admitted
mutation-path call then serves from it — but only after the reconciled merged
set passes the complete merged-config validation first (the reconcile
validates the merged post-adopt live set against the full component-03 rules
plus the 63-host cap under the mutation lock BEFORE publishing: valid sets
publish exactly as above, while a failed validation publishes nothing — the
last-good snapshot stays live, the admitted call serves from it, and the
failure surfaces as the typed `too-many-hosts` configuration error (over-cap)
or `concurrent-edit` (other merged-config drift), naming both sources and
their counts — an external edit adding declared hosts cannot publish an
over-cap live registry no mutation path could have committed — never a
silently over-cap registry), and all live consumers (registry, manager
bindings, sources, manifest, host-admin controller fan-outs, web-config view)
update atomically under that lock before the admitted call proceeds (a
`list`/`status` read arriving while the reconcile holds the lock serves the
file-filtered snapshot (§4 — names filtered against the current file bytes
synchronously) lock-free instead of waiting — reads never fail busy for a
fingerprint reason). A stale snapshot or preflight facts read from the old
configuration can never pass a generation check against the new one, and a
reconcile never rebinds the registry, channel, or supervisor under a pinned
in-flight operation. Navigation, source, and host-admin consumers outside
`evener/host/*` do not run the admission step themselves — they reach those
methods' snapshots only through the hub's common ingress or the live registry
callback: every read of the live host set — registry, manager bindings,
sources, manifest, host-admin controller fan-outs, web-config view — goes
through that callback, which runs the same fingerprint-compare-then-reconcile
pre-handler step with the same filtered-serve-and-reconcile-async read posture
(§4 — the callback filters names synchronously against the current file bytes
and never blocks a read on the mutation lock), so a deleted or changed
declared host is invisible to every live consumer by its next read — a deleted
host absent, a content-changed host unavailable (never the old SSH/path
config) until the reconcile publishes the new runtime snapshot — not only
after the next host-management request. The UI cannot remove or re-add these
hosts; only the entry-change rule moves their generation. Token bindings
reference these generations (§8): validation requires the token's generation
to equal the registry's current generation for the name. Snapshot publications
carry the generation of the source they were read from, and a publication
whose generation no longer matches the registry's current generation for that
name is rejected; re-add also clears the name-keyed caches (snapshot rows and
last-known preflight facts) as part of its staged commit — an in-flight
refresh from the removed incarnation or a stale name-keyed cache entry can
never republish rows for the new host.
## 16. Testing

- 08a: registry live-update tests (including the add-time cycle rules),
  manager add/remove-vs-supervisor tests (removing an attached host stops its
  supervisor, closes its channel, and drains its per-host lifecycle handles
  before the entry is gone), sidecar merge/atomicity tests including the
  refuse rules, the 63-remote-host cap (63 live remotes add; the 64th fails
  `ErrTooManyHosts` — and a live external `hub.toml` edit pushing the merged
  set over the cap fails the reconcile validation instead of publishing: the
  last-good snapshot stays live and the failure surfaces as the typed
  `too-many-hosts` configuration error), the boot-time hard-error duplicate,
  swap-failure compensation (prior sidecar bytes restored, runtime reverted,
  retry re-applies cleanly), and the corrupt/schema-invalid sidecar boot hard
  error, the host-admin controller live-set tests (forwarded requests reach
  newly added hosts; removed hosts' fan-outs stop), update-rebind tests (a
  running supervisor's next reconnect uses the updated entry; the rebind
  completes before the gate is released), tombstone tests (list attached →
  remove → rows persist and are marked stale/non-actionable in tree +
  capabilities while the manifest's `sources` array drops the host;
  tombstones survive a controller restart; the persist bound holds (a removal
  over 500 rows / 1 MiB persists the newest-first projection with
  `rowsTruncated: true` on the tombstone and the `list` row (newest-first by
  row last-updated timestamp, never snapshot order), and the merge renders the
  indicator — never the full set silently); re-add purges, clears the
  name-keyed caches, and mints a new generation — publication from an obsolete
  generation is rejected; a refresh after remove re-merges the tombstone's
  retained rows (never drops them); boot prunes expired tombstones durably
  even with no mutation since expiry (pinned with controllable removal
  timestamps against the 7-day default — §15)), decision-source live-set
  tests (a newly added host is accepted by archive/favorite validation
  immediately after the commit; a removed host is refused; validation reads
  the live set, never the startup snapshot), ingress-boundary ordering tests
  (a remote-originated request is refused before admission — proven by
  ordering tests on the 08a surface, not by handler wrapping; the
  before-dedup / before-token-validation orderings are asserted in 08b where
  dedup and token validation ship — the tests present the cooperative bridge
  marker (the guard's only signal) and pin refusal of honestly-marked
  peer-forwarded requests, never spoof-resistance: a markerless request is
  local-originated by construction, §3), update-generation tests (update
  advances the generation and clears/generation-keys resolved targets, facts,
  and bindings before rebinding; (`expectedGeneration`,
  `expectedIncarnationId`) present-and-current commits, present-and-stale on
  either is a `stale-entry` refusal committing nothing (`update` and `remove`
  both require all three fields — missing any is a validation refusal
  committing nothing — with the same check-and-refusal once present); a
  lost-response `remove` retried after a re-add never tears down the new
  incarnation when the generations differ — a retained same-key receipt
  returns the recorded `committed` receipt for outcome recovery, and a pruned
  or tombstone-purged same-key receipt is the typed `stale-entry`
  pruned-generation refusal on the surviving marker (the single
  superseded-receipt rule — never a fresh apply; `stale-entry` fires exactly
  when no retained receipt survives for the key); `expectedGeneration`
  requires `mutationId` on the UI path and keyless `add` retries follow the
  effective-fields re-read rule (never an `expectedGeneration` without a key;
  uncommitted `add` retries while the row is still absent, `stale-entry` once
  an effective field changed; `update` takes no keyless path — missing either
  field is a validation refusal)), remote-origin rejection for
  `list`/`add`/`update`/`remove` (the #1603 origin guard refuses
  honestly-marked peer-forwarded requests before admission — the 08a surface
  only — including `teardown-retry`'s origin rejection alongside its 08a
  commit-point tests), and a wiring test mirroring the 05a registration
  tests. Mutation-idempotency tests (the operation store's cross-name rule,
  applied to mutations): cross-name/cross-kind `mutationId` replay is the
  typed `conflicting-mutation-id` refusal; same-key replay after remove/
  re-add never fresh-applies against the new incarnation (a retained same-key
  superseded-generation receipt is always a hit for outcome recovery, never a
  conflict and never a fresh destructive apply; a pruned receipt — past the
  count/TTL prune, or dropped with its tombstone — is the typed `stale-entry`
  pruned-generation refusal on the surviving marker, never a fresh apply
  either; a genuinely new mutation mints a new `mutationId`);
  commit-then-replay of an `update` returns the receipt pinned to the
  post-bump generation without rebumping. Commit-point tests: a
  committed-with-teardown-failure persists the remnant with its opaque
  `remnantId` in the response, replaying the original `mutationId` stays a
  no-op receipt return, and `teardown-retry` completes only the named teardown
  (unknown/purged ID → `teardown-unknown-key` not-found; present-but-cleared
  ID → the `already-cleared` success arm; retrying the original `mutationId`
  after resolution returns the resolved receipt; any remnant-fenced path —
  re-add, `update`, or `remove` on the remnant's name, plus `deploy`/`restart`/
  `Ensure`-triggered work/`plan`/attach on the name — refused with
  `remnant-open` until the retry completes; the persisted receipt carries
  exactly `{outcome, row, generation, incarnationId, committedAt,
  droppedEntry?, winningFingerprint?, remnantId?, remnantResolvedAt?,
  bootRecovered?}` (`incarnationId` the pinned incarnation — the commit-point
  test pins the full five-part scoped key, so a shared-generation boot-merge
  looks the receipt up under the right incarnation; `remnantId` while the
  remnant is open, `remnantResolvedAt` after resolution, `bootRecovered`
  exactly on boot-recovered receipts) and the cleared marker is `remnantId →
  clearedAt` in `teardownRemnants`; a post-`remove` retry returns the tombstone
  removed-row shape, a post-add/update retry the live row; the retry validates
  against the remnant's pinned `(generation, incarnationId)` + `cleanupHandle`
  — the live entry may be absent or newer without blocking it — and never acts
  against the live entry). Pending-marker tests: a post-commit receipt write
  lost while the process stays alive leaves the staged-receipt marker staged
  — a replay finalizes carrying the pre-minted `remnantId` and the pinned
  teardown target — a replay by phase (a staged/unswapped marker finalizes as
  `committed` with no remnant and no teardown run; a swapped or ambiguous
  marker re-runs the pinned teardown and finalizes the observed outcome — a
  real teardown failure surfaces `committed-with-teardown-failure` with the
  remnant; a clean re-run returns `committed` with no remnant — the staged
  provisional outcome is never returned as-is), and the next mutation-path
  write finalizes a foreign marker for its own host before its own stage,
  while a marker for another host never blocks it (per-host markers;
  foreign-host entries ride along untouched in the same atomic writes).
  Lock-free `list` tests: `list` takes no mutation lock and prunes nothing
  durably; expiry filtering is in-memory only and the durable prune lands on
  the next mutation-path write — the `hub.toml`-fingerprint pre-handler check
  never takes the lock on the read path (mismatch serves the last published
  snapshot lock-free and schedules the reconcile asynchronously, debounced; no
  read ever fails busy for a fingerprint reason; the reconcile re-compares
  fingerprints on completion and reschedules on a still-present mismatch, so a
  coalesced mid-flight edit is never silently lost). Remnant-gate tests:
  re-add and retention expiry skip names with open remnants; while a remnant
  is open for a name, `deploy`, `restart`, `Ensure`-triggered work, `plan`
  (no-token `remnant-open` arm with `remnantId`, never a minted token), and
  attach all refuse with `remnant-open` naming the blocking `remnantId` — the
  fence is host-wide, never mutation-only. Config-path tests: a `--config`
  startup carries the canonical path into the web config and the sidecar +
  fingerprint derive from it. Collision tests: a tombstone/live collision
  restores the live generation strictly above the high-water mark with
  pre-collision records marked and live records unmarked. Crash-window test: a
  `hub.toml`/sidecar live-entry collision covered by a pending reconcile
  marker whose fingerprint matches the on-disk file boots into the completed
  cleanup (no hard error) — the step-(2) write arms the validation fingerprint
  and the pending marker rides the staging write, so a crash between the
  commit rename and the staging write, or between staging and the cleanup
  rename, still boots covered, never unmarked (while an armed intent whose
  fingerprint still matches the on-disk file with a duplicate present still
  boots hard-error — hand-made); the same collision with no marker and no
  armed intent, or with a marker whose fingerprint no longer matches, boots
  into the hard startup error. Swap-window tests: a marker with `swapStarted:
  false` and `teardownStarted: false` finalizes without a remnant only after
  the staged runtime set is re-applied to the live handles (the sidecar
  already holds the new config — a finalize that skips the swap diverges the
  live process from durable state), while a marker with the intent written
  (`swapStarted: true`) but the swap incomplete recovers by re-applying the
  staged runtime set first and then recovering conservatively with the pinned
  remnant — never a teardown-first mismatch, never without one. File-posture
  tests: sidecar and store temp files are `0600`, renames preserve the mode,
  and startup refuses a file readable beyond its owner, and the stash gets the
  same coverage (stash temps `0600`, mode-preserving rename, owner-only
  startup refusal, stray-stash prune/ignore).
- 08b: handler tests per method (validation, admission, classification incl.
  `plan`-as-mutation, remote-origin rejection for
  `status`/`plan`/`deploy`/`restart`/`operations`/`running`/`orphan-resolve`
  (`teardown-retry`'s origin rejection is asserted in 08a with its
  commit-point tests), the token matrix:
  missing/mismatched/expired/superseded/consumed-then-replayed-with-new-op-ID
  (pins to `token-missing`: consume deletes the row — §8 step (4)),
  expiry-across-the-wait (a token valid at the step-(2) provisional pass but
  past its TTL at the step-(4) consume is a `token-expired` refusal with no
  record — expiry is re-checked inside the consume transaction, never decided
  at provisional validation alone), clock-rollback (a wall-clock now reading
  behind the store's durable high-water mark by more than the 30-second
  tolerance drops only the records whose own timestamps postdate `now`
  (later-minted token rows as `token-expired`, later-captured facts entries as
  stale) until wall-clock time again reaches the mark (post-rollback captures
  anchor at `max(now, mark)` — §8 — and a within-tolerance step invalidates
  nothing; all other expiry and age checks run against `max(now, mark)` while
  the rollback is active, so a pre-rollback token keeps exactly its persisted
  real-time lifetime, never an extended one) — a backward step invalidates,
  never extends, a deadline), supersede-between-validate-and-consume (a `plan`
  mint landing after `deploy`'s step (2) provisional pass but before its step
  (4) compare-and-consume is a `token-superseded` refusal with no record —
  §8 step (4)), generation-bound (a token minted under generation N is refused
  after remove/re-add even for a byte-identical entry; live remove drops
  outstanding tokens; re-add starts with none), token persistence (mint is a
  durable store write held under the gate; expiry reaped lazily and at boot;
  tombstoned hosts' tokens dropped), post-operation refresh (a completed
  deploy/restart publishes verified fresh facts to the (generation,
  incarnation id)-scoped last-known store; `status` reports the new version),
  busy-holder classes (an operation-held gate names the operation — including
  an Ensure-triggered deploy, which holds its own record; a plan-held gate
  returns the transient form with no operation reference and the UI shows
  retry, not open/wait), protocol shapes (catalog entries and the regenerated
  client match §11 field-for-field — including `incarnationId` on
  `OperationRecord` and the `operations` request/response/cursor,
  `compacted: true` exactly on tombstone replays, every `stale-entry` data
  value against its emitting path, the `cursor-invalidated` catalog entry, and
  the `fencing-failure` + `fencing-helper-absent` + `fencing-helper-untrusted`
  + `cursor-too-large` catalog entries with their data shapes — plus the
  `collision-dropped` mutation-result arm (dropped-entry + winning-fingerprint
  payload), the `hostBoundaries` `{...} | "absent"` value union, the
  `orphanBoundary` per-member array (`BoundaryEntry[]` — the `kind`
  discriminator on every entry, the local-linux arm's `pid`/`startTime`
  fields, the `local-markerless` and `remote-fencing` variants (the latter
  with per-entry `ownership`), the empty/absent cases, and a multi-member
  case), and `status.planRefusal`'s `remnantId?` + `attached` parity fields),
  operations incarnation scope (a `generation` + `incarnationId` filter pair
  addresses the colliding same-generation incarnation; the response echoes the
  listed pair; a cursor minted under one pair never lists the other),
  unconditional plan refresh (an attached `plan` refreshes even when the known
  facts are fresh — the mint's facts are never older than the refresh it just
  ran — and the 5-minute token TTL is the deploy window), Ensure busy names
  its operation (an Ensure-held gate returns `host-busy-operation` with the
  Ensure record's id — open/wait-able; the transient form fires only for
  `plan`'s validation-plus-mint window), cursor-invalidated (a mid-pagination
  compaction past the cursor refuses typed `cursor-invalidated` with the
  compacting `compactSeq` — the client restarts from page one; an over-cap
  first page refuses the distinct `cursor-too-large` discriminator with
  `{capBytes: 8192}` — both shapes pinned, and both catalog entries carry the
  regenerated client through the generator mapping — §11),
  teardown-retry timeout arm (a retry whose bounded teardown run times out
  returns the declared `committed-with-teardown-failure` arm with the
  still-open remnant's details — pinned alongside the two success arms), the
  running probe (`evener/host/running` handler: local revision + health +
  optional `processStartTime`, attached-session admission only —
  unauthenticated probe refusal; browser/forwarded requests refused; never
  forwarded onward (no A→B→A chain); the ungated `plan` probe call with its
  explicit deadline (timeout → no-token `probe-failed` refusal with no gate
  ever held, never an extended busy hold); `HostPlan.runningVersion` /
  `runningHealthy` placement; handler-absent named for pre-handler remotes
  with the one-time manual-upgrade migration path; no-token `reason`
  discriminates `unattached` | `refresh-failed` | `probe-failed` |
  `handler-absent` | `remnant-open` | `controller-dirty` |
  `target-unwritable` | `target-missing-prereq` | `target-unit-findings`
  (with `remnantId` naming the blocking remnant — §6 — and `terminal: true`
  exactly on the four terminal arms) and the UI branches on it — no Connect
  loop for attached probe failures; an unverifiable probed revision always
  reads as outdated (restart follows) — no timestamp comparison exempts it),
  restart reattach (the worker retains the gate across the channel drop,
  reattaches through `attachUnderGate` for the pinned entry — never the normal
  attach path, no supervisor start until the post-verification handoff — and
  re-probes over the reattached channel; an initially unattached restart
  attach-firsts under the same gate and names it in the record; a
  reconnect-after-restart test pins that the supervisor owns the channel again
  once the gate releases), the ungated refresh: an attached host refreshes via
  the bounded one-shot SSH preflight (no channel initialization, no
  supervisor, no attach state machine, no gate held) then acquires the gate,
  re-checks, and mints; an unattached host gets the no-token refusal naming
  Connect; the deploy/restart worker's post-operation preflight is
  channel-free under the same pin, execution-time re-resolution mismatch
  including the `hub.toml` fingerprint (a manual edit between plan and deploy
  refuses; a manual edit between restart's resolution and its gate acquisition
  refuses) and the post-acquisition entry re-read (`restart` and
  Ensure-triggered work refuse or re-resolve when a mutation lands between
  resolution and gate acquisition), (host, kind, generation, incarnation
  id)-scoped operation-ID dedup including the interrupted-record path, the
  compacted-ID tombstone path (replay returns the tombstoned terminal result,
  never a fresh operation), the clean-slate re-add path (op-ID reuse after
  remove/re-add opens fresh), and the conflicting-reuse refusals: same ID
  different host, deploy-vs-restart, and a current-generation `host-removed`
  record (IDs used up only by a removed incarnation are cleanly reusable),
  per-host gate serialization vs an in-flight `Ensure` deploy, worker lifetime
  (a client disconnect after record creation leaves the operation running;
  controller shutdown records `interrupted` and releases the gate), the busy
  refusals (`update`/`remove`/`plan`/new-deploy on a held gate) and the atomic
  consume-and-create write (a replayed deploy after a crash in that window
  finds a record, never a silently consumed token), startup reconciliation of
  pending/running → interrupted plus the tombstone-derived `host-removed` pass
  (a crash between a remove's sidecar commit and its live mark still
  never-matches at boot), the corrupt operation-store boot quarantine (store
  quarantined aside, boot serves empty with zero outstanding tokens plus the
  operator-visible health signal), orphan fencing (a crashed worker's local
  process group is reaped at boot only through its persisted
  boundary-plus-kernel-attested-identity (Linux: cgroup membership bound to
  the launcher-observed (pid, start time) pair; Darwin: (pgid, session id)
  plus the same launcher marker — a persisted-but-empty boundary after a
  pre-spawn crash reaps nothing and drops the intent — and a fresh operation
  runs under a new fencing epoch through the remote lease wrapper (atomic
  register+fence+perform per mutation; lease ownership token verified remotely
  before any kill) — no overlap with orphaned local or remote work; a
  helper-absent or older/untrusted-helper host refuses fail-closed
  (`fencing-helper-absent` / `fencing-helper-untrusted`) before any remote
  mutation with no auto-install and no in-band migration — out-of-band install
  only — and first-ever-contact bootstrap persists its attempt fence before
  any remote side effect and converges its `helperInstalled` flag in the
  finalizing write, so a crash between them leaves the host attempt-fenced on
  the fenced recovery path, never open for a second unfenced delivery), the
  cross-file intent (a crash between the sidecar commit and the store sync
  converges to the committed sidecar's view in both directions; the store
  purge lands only after swap success, and swap-failure compensation re-inserts
  exactly the purged token rows its sidecar restore revalidates), plan publish
  (every `plan` refusal and every completed probe lands in the (generation,
  incarnation id)-scoped running-state/refusal snapshot — `status` after a
  plan refusal renders the refusal, never stale data), the running-health
  definition (each forced-false condition returns `healthy: false` as data
  while the probe itself succeeds — evaluated by the serving hub's local
  predicate, never the `restartRequiredDaemon` probe path), pinned pagination
  (stable `id`-ascending order for both sort and resume across concurrent
  terminal writes (`createdAt` display-only — a post-cursor record stamped
  pre-cursor by clock rollback still lists, and backward timestamps never
  reorder a page — pinned); mid-pagination generation advance keeps later
  pages on the pinned incarnation; cursor carries `(generation,
  incarnationId, compactSeq, presenceEpoch, lastId)` in the `v: 2` envelope
  with pair-mismatch (`stale-entry` re-list) and post-cursor compaction
  (`cursor-invalidated`) both surfaced as refusals, never silent page shifts;
  host-pinned pages carry the top-level pair while unfiltered cross-host pages
  omit it (`hostBoundaries` authoritative — the shapes test pins the absence);
  the cursor pins every host in the query at creation, including absent ones
  (absent encodes as the literal `"absent"` string, pinned field-for-field) —
  a host advancing generations between pages is a `stale-entry` re-list
  refusal, and a host removed after mint (presence epoch advanced, triple
  preserved) is a `stale-entry` re-list refusal the same way; a host created
  after mint is skipped on later pages (fresh read only); the versioned
  base64url envelope is capped at 8 KiB encoded (over-cap first page refuses
  `cursor-too-large` with `{capBytes: 8192}`, pinned as its own
  discriminator)), Orphan ownership (Linux reaps through the cgroup boundary
  plus the launcher-observed (pid, start time) marker bound to the pre-spawn
  nonce — never a cgroupfs marker file, never the group id alone; Darwin reaps
  through the (pgid, session id) boundary plus the same launcher marker — with
  a pid whose start time differs reading as already clean (never signaled),
  and fails closed with durable `orphan-unverified` (resolved by retry at a
  later boot or by the authenticated `evener/host/orphan-resolve` call — the
  record carries the `orphanBoundary` per-member array, the shapes test pins
  the `orphan-unverified` state, and the detail filter still resolves through
  `id`) when enumeration is unavailable — and while the record is open the
  host admits no new operation past admission (transient busy until verified
  or resolved through `orphan-resolve`; the fresh operation starts only after
  local reap completion); fencing kill/wait run under bounded contexts
  (kill/wait timeout → terminal fencing-failure outcome plus a durable
  per-host quarantine carrying the timed-out epoch's persisted remote boundary
  on an `orphan-unverified`-class record — the host admits no new mutation
  until the operator resolves through `orphan-resolve` (which verifies the
  persisted boundary before clearing), and the next `deploy`/`restart` past
  the cleared marker converges the fencing with its kill/wait + guard advance
  — never a stuck host and never an operable-but-unfenced one)), and the
  not-in-forwarded-allow-list assertion.
- 08c: `make test-web`, browser gate (`env -u DBUS_SESSION_BUS_ADDRESS make
  test-web-browser`), `make lint-generated`; per-flow tests in the section's
  test files; the never-dial invariant of `list`/`status`/`operations`
  test-pinned, with `list`/`status` serving last-known facts, ages, and attach
  errors from the manager store for offline hosts.
- Live E2E (strongly recommended, never yet exercised): the campaign landed
  04b without a live deploy against a real host. 08b/08c should run one add →
  connect → deploy → restart → spawn-remote cycle against a disposable host
  before declaring the component done; that requires a host Jesse designates.

## 17. Acceptance criteria

1. A user with zero hosts configured adds one from the UI, sees it connect,
   and the spawn picker appears with the host selectable — no file editing, no
   controller restart (for a helper-capable host: a bare host with no
   pre-existing trusted host-side claim/quiesce primitive requires out-of-band
   helper provisioning first — §9 — and is not bootstrapable through the UI).
2. An offline configured host has a working Connect action in the Hosts
   section (the picker's trigger shipped with #1603); success flips its online
   state everywhere (rail, picker, manifest) through the existing paths.
3. Deploy from the UI: the confirmation renders the controller-minted plan
   (controller revision + resolved remote target path) and cannot proceed
   without a fresh token; an edit between plan and deploy is rejected and
   re-planned — including a manual `hub.toml` edit, caught by the
   fingerprint; an edit attempted while the deploy runs is refused with a busy
   error and the push is unaffected; after confirm, the host reports the new
   version in `status` via the worker's required post-operation facts refresh
   (published to the (generation, incarnation id)-scoped last-known store
   before the operation marks `complete`), and the version-skew signal (facts
   vs controller build) is truthful; a replayed deploy with the same operation
   ID returns the same record; a controller crash mid-deploy surfaces as
   `interrupted`, never a stuck operation.
4. Hand-edited `hub.toml` hosts keep working exactly as today; UI add/update
   refuse their names; the origin marker shows which file owns each entry; a
   hand-created duplicate name across files is a hard startup error.
5. Removing a sidecar host retains its last-known-good rows as an explicitly
   stale, non-actionable tombstone (tree + action capabilities, test-pinned)
   while the manifest's `sources` array drops the host; the tombstone survives
   a controller restart; re-adding the same name purges it, clears its
   name-keyed caches, and mints a new generation, so stale rows from the old
   incarnation cannot republish; `list` shows it with `removed: true`.
6. No lazy-attachment regression: with no attach call, no explicit source, no
   read dials anything — `list`/`status`/`operations` never attach
   (test-pinned) — and `plan`'s facts refresh and the deploy/restart worker's
   post-operation preflight, the surface's two deliberate non-attach SSH uses,
   are channel-free by construction (no initialize, no supervisor, no attach
   state machine; test-pinned).
7. Standard gates green on every PR (go/build/vet, package races, full hub
   suite, module-lint; web + browser + lint-generated for 08c).

## 18. PR size estimate (LOC)

- 08a: ~800–1100 (registry/manager/config/admin-controller/wiring/catalog/
  client) + tests.
- 08b: ~700–1000 (handlers + token + operation store + reconciliation) +
  tests.
- 08c: ~800–1100 (section, dialogs, stores, polling) + tests.

## 19. Open questions

1. **Sidecar vs rewrite of `hub.toml`** — spec'd as sidecar with the refuse
   rules and hard-error duplicate; the owner may still prefer in-place
   `hub.toml` rewrite accepting comment loss.
2. **Tombstone retention period** — decided: re-add purges by name; the
   default retention for never-re-added hosts is 7 days (owner-set
   `tombstoneRetention` knob).
3. **Who may add hosts** — reuse the settings-mutation admission as-is, or a
   distinct grant? Spec assumes as-is.
4. **Per-host detach** — out of scope here; natural follow-up once remove
   semantics settle.
5. **`ServerInfo.Version` constant** — owner follow-up from #1603 r2; does not
   block this component (version display reads preflight facts).

## 20. Changelog

- 2026-09-16: initial component 08 spec — host management UI (add-host
  dialog, Connect, deploy/restart surface).
- 2026-09-17: round 1 — token contract, staged atomic apply, operation store,
  precedence and tombstone rules.
- 2026-09-17: round 2 — token freshness and binding, dedup-first order,
  corrected update rule, boot merge hard error, admin-controller fan-outs,
  crash recovery.
- 2026-09-17: round 3 — origin-guarded handlers, compensable sidecar swap,
  gate-pinned deploys, scoped dedup, durable tombstones, bounded tokens.
- 2026-09-17: round 4 — SSH-preflight plan refresh, post-acquisition gate
  re-read, boot tombstone reconciliation, live-only duplicate refusal,
  plan-rendered confirmation, hub.toml fingerprint.
- 2026-09-17: round 5 — always-acquire plan gate, hub.toml re-read on
  mutations, mark-before-discard boot order, live-only not-found, token
  lifecycle.
- 2026-09-17: round 6 — worker lifetime, restart fingerprint, 63-host cap,
  last-known facts, lifecycle handles, commit ordering, token-boot wording.
- 2026-09-17: round 7 — generation-bound tokens, update invalidation,
  post-op refresh, decision-source live set, ingress guard, protocol types,
  busy classes.
- 2026-09-17: round 8 — persisted generations, store mutex, running-state
  plan, commit point, generation-scoped dedup, hub.toml final check, ingress
  provenance, protocol shapes.
- 2026-09-17: round 9 — probe, store generations, reconciliation, tombstones,
  receipts, dedup precedence.
- 2026-09-17: round 10 — probe method, restart reattach, receipt scoping,
  teardown repair, tombstone values, plan timestamp, expiry, token prune,
  hub.toml owners.
- 2026-09-17: round 11 — peer-probe exception, remnant IDs, re-add gate,
  lock-free list, config path, incarnation, plan reason, migration, perms,
  params, counts, SSH uses.
- 2026-09-17: round 12 — fixes for review verdict (15 findings).
- 2026-09-17: round 13 — fixes for review verdict (7 findings).
- 2026-09-17: round 14 — review fixes.
- 2026-09-17: round 15 — review (6 Medium + 2 Low).
- 2026-09-17: round 16 — review (2 High + 6 Medium + 2 Low).
- 2026-09-17: round 17 — review (2 High + 7 Medium + 1 Low).
- 2026-09-17: round 18 — review (2 High + 9 Medium + 4 Low).
- 2026-09-17: round 19 — review (4 High + 5 Medium + 3 Low).
- 2026-09-17: round 20 — review (5 High + 7 Medium + 1 Low).
- 2026-09-17: round 21 — combined review (10 findings).
- 2026-09-17: round 22 — combined review (11 findings).
- 2026-09-17: round 23 — review (teardown-retry identity, Darwin reap,
  op-record incarnation, compacted marker, tombstone bound, live hub.toml
  reconcile, Ensure busy class, unconditional plan refresh, stale-entry enum,
  cursor-invalidated, remove presence ordering).
- 2026-09-17: round 24 — review (fencing lease, Darwin pid+start-time,
  pre-handler reconcile, single receipt rule, staged phases, orphan-unverified,
  per-host cursor, common-ingress reconcile, preflight exemption, timestamp
  truncation, probe-before-acquire, direct-SSH reinstall, writability
  metadata).
- 2026-09-17: round 25 — review (pruned-receipt markers, tombstone
  incarnation, swap intent, orphan gate, receipt key, compacted fields,
  unfiltered pair, cursor pinning, fencing deadlines, lock-free reads, update
  key, TTL clamp, real probe, truncation tie-break, facts binding,
  RemovedRow truncation).
- 2026-09-17: round 26 — review findings.
- 2026-09-17: round 27 — review findings.
- 2026-09-17: round 28 — review findings.
- 2026-09-17: round 29 — review findings.
- 2026-09-17: round 30 — review findings.
- 2026-09-17: round 31 — review findings.
- 2026-09-17: round 32 — review findings.
- 2026-09-17: round 33 — review findings.
- 2026-09-17: round 34 — review findings.
- 2026-09-17: round 35 — review findings.
- 2026-09-17: round 36 — review findings.
- 2026-09-17: full rewrite — glossary, single PR table, single-statement
  rules with section cites, history appendix, upfront scope list, known
  defects resolved.
