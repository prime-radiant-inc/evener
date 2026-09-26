# Component spec 08 — Host management registry (mutations, sidecar, reads, UI)

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

This document is one of three. It owns the registry surface: the host
mutations, the sidecar and its receipts/remnants/tombstones, the
`hub.toml` reconcile, the list/status reads, and the Hosts UI. The
deploy pipeline (plan/deploy/restart, tokens, gates, the operation store)
is defined in `2026-09-16-multi-host-08b-deploy-pipeline.md`. Crash
orphans, fencing, and orphan-resolve are defined in
`2026-09-16-multi-host-08c-crash-fencing.md`. A rule named here is not
restated there, and vice versa: cross-document references are section
pointers, never copies.

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

Every later section uses these terms with exactly these meanings. The
sixteen shared terms below are identical in all three documents.

**MutationId.** The client-supplied idempotency key on `add`/`update`/`remove`: opaque, non-empty, at most 128 bytes, no required structure. A replay is a call repeating a previously used key.

**OperationId (client operation ID).** The client-supplied operation ID on `deploy`/`restart`: opaque, non-empty, at most 128 bytes, no required structure. `deploy`/`restart` responses carry `id` (the controller-assigned record id) and `clientOperationId` (echoing the caller's value).

**Generation.** The per-name monotonic counter minted by `add`/re-add and advanced by `update`, persisted in the sidecar. Re-add mints strictly above every retained high-water mark for the name; a name with no surviving mark and no live history re-adds clean at generation 1 with a fresh incarnation id plus an advanced presence epoch, so it cannot adopt the old history (§15). A token, receipt, or record pins the generation it ran under; validation requires equality with the registry's current generation for the name.

**Incarnation id.** The opaque server-generated string minted beside the generation on every `add`/re-add, never derived from it and never reused: at most 128 bytes, and the generator pins its output to 36 bytes (canonical UUID text), so the 8 KiB cursor-cap bound in the deploy-pipeline spec §8 holds by construction. The pair (generation, incarnation id) is the guarded-mutation and dedup identity everywhere.

**Receipt.** The durable finalized outcome of a host mutation, keyed by (mutationId, host name, mutation kind, post-commit generation, incarnation id).

**Remnant.** The durable in-progress teardown record of a committed-with-teardown-failure mutation, addressed by its opaque server-generated `remnantId`. An open remnant fences every lifecycle and attach path on its name until `teardown-retry` resolves it or escalated `teardown-recover` clears it.

**Tombstone.** The durable removed-host record carrying retained rows, persisted in the sidecar. Tombstone-only names render in `list` as `removed: true` rows and accept only re-`add`.

**Per-host gate.** The try-acquire (never wait) mutex serializing deploy/restart/`plan`/teardown work for one host name. A held gate fails new work fast with the typed busy error.

**Mutation lock.** The process-wide lock serializing sidecar read-modify-write only, never across a teardown. Outermost among the durable-write locks (mutation lock → store mutex); the per-host gate precedes all of them (deploy-pipeline spec §5).

**Store mutex.** The lock serializing operation-store read-modify-write. No path holding the store mutex ever acquires the mutation lock.

**Origin guard.** The shared pre-admission hook refusing honestly-marked remote-originated, peer-forwarded requests before admission. An honest-peer recursion terminator, not a security boundary.

**Confirmation token.** The controller-minted, single-use, expiring opaque bearer `plan` returns beside its plan, bound to the host entry, generation, incarnation id, `hub.toml` fingerprint, facts, and running state it was minted from.

**Operation record.** The durable controller-side record of one `deploy`/`restart`, keyed by controller-assigned id, deduplicated on (host, kind, client operation ID, pinned generation, pinned incarnation id).

**Boundary.** A persisted ownership description a verifier checks before signaling a possibly-live process: `local-linux`, `local-darwin`, `local-markerless`, or `remote-fencing` (defined in the crash-fencing spec §9).

**Fencing epoch.** A worker's durable (controller boot id, per-host monotonic op sequence) presented on every SSH command it runs.

**Presence epoch.** The per-host monotonic removal/presence counter the sidecar advances on every add, remove, re-add, and expiry purge. It persists in the sidecar per live entry and per tombstone; every sidecar write that adds, removes, re-adds, or expiry-prunes the name advances it in that same atomic write. The store mirrors it into the per-host boundary record on the same writes that mirror the generation (§7 defines the record schema; deploy-pipeline spec §4 cites it); cursor validation reads the mirrored value (§8 there).

**Intent.** A durable record the controller writes before acting, so a crash
leaves recovery instructions on disk. The registry intents are the
`swapStarted`/`teardownStarted` flags plus runtime phase on a staged mutation
marker (§5) and the reconcile marker (§6). The `pendingStoreSync` revocation
intent and the `pendingCompensation` phase record are defined in the
deploy-pipeline spec §9. An in-memory plan (a `HostPlan` the UI
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
- Cleared-remnant marker (a typed resolved-remnant record in `teardownRemnants`,
  keyed by `remnantId`): the proof a remnant already cleared, so a lost-response
  retry returns `already-cleared` or `recovered-cleared` (§6). Each record carries
  the replay payload — `clearedAt`, the resolution kind (`retry` | `recover`), the
  host kind (`live` | `removed`), the pinned host/name payload, and the recovery
  attestation when the kind is `recover` — or the receipt reference holding them,
  plus all replay metadata until expiry.

**High-water mark.** The greatest generation a name ever carried, surviving
removals, persisted in the sidecar alongside the tombstone (§15) as the
(generation, incarnationId, presenceEpoch) triple — the incarnation id covers
the historical case, and an expiry prune that deletes the tombstone still
advances the presence epoch into the entry. Re-add mints strictly above every
retained mark; a name with no surviving mark and no live history re-adds clean
at generation 1 with a fresh incarnation id plus an advanced presence epoch
(§15). The operation store mirrors the triple (deploy-pipeline
spec §4), which owns the mirrored-generation rule: on a store-newer/sidecar-older split with no matching commit marker boot rolls back to the sidecar mark and keeps the discarded value only as the high-water mark (deploy-pipeline spec §4). This document states no independent maximum rule.

## 2. PR sequence

This table is the only place the three-document split is defined.

| Method / artifact | Registry (this doc) | Pipeline (08b) | Fencing (08c) |
|---|---|---|---|
| `evener/host/list` handler + catalog + regenerated client | ships (handler + catalog + client) | consumes | — |
| `evener/host/add` behavior + private hand-written types | ships behavior only (no handler registration) | handler + union catalog + regenerated client | — |
| `evener/host/update` behavior + private hand-written types | ships behavior only (no handler registration) | handler + union catalog + regenerated client | — |
| `evener/host/remove` behavior + private hand-written types | ships behavior only (no handler registration) | handler + union catalog + regenerated client | — |
| `evener/host/teardown-retry` behavior + private hand-written types | ships behavior only (no handler registration) | handler + union catalog + regenerated client | — (fenced: crash-fencing spec §8) |
| `evener/host/teardown-recover` behavior + private hand-written types | ships behavior only (no handler registration) | handler + union catalog + regenerated client | — (fenced: crash-fencing spec §8) |
| `evener/host/status` handler + catalog + regenerated client | ships (handler + catalog + client) | consumes | consumes (refusal snapshots) |
| `evener/host/plan` + token mint | — | ships (handler + catalog + client) | consumes |
| `evener/host/deploy` | — | ships (handler + catalog + client) | consumes |
| `evener/host/restart` | — | ships (handler + catalog + client) | consumes |
| `evener/host/operations` + pagination | — | ships (handler + catalog + client) | consumes (polling) |
| `evener/host/running` probe | — | ships (handler + catalog + client) | — |
| `evener/host/orphan-resolve` | — | — | ships (handler + catalog + client) |
| `attachUnderGate` primitive + live host-set surface (registry/manager) | ships | uses (restart reattach) | — |
| Sidecar, staged commit, receipts, remnants, tombstones, generations | ships | mirrors generations in store | — |
| Operation-store skeleton `remove` depends on (`host-removed` mark path, outstanding-token-row purge path, atomic writes, no pipeline behavior behind them) | ships as store-owned helpers | full store (records, dedup, tokens, reconciliation) | — |
| Union-registration generator work (`internal/appwirets/emit.go`) | — | ships | — |
| Origin guard pre-admission hook at request ingress | ships (hook + registry-surface orderings: `list`/`status` here; union-surface orderings in the pipeline PR where those handlers ship) | guard-before-admission ordered on the pipeline surface; dedup/token orderings asserted where they ship | — |
| Catalog registration + regenerated TypeScript client for union-shaped methods | — (this doc registers no handler and ships no catalog entry and no regenerated client for a union-shaped response) | ships | — |
| Non-union registry helpers | register immediately | — | — |
| Hosts settings section, dialogs, stores, polling, deploy confirmation | shipped (the add/edit/connect/remove section, dialogs, and `stores/hosts.ts` — #1784 and the edit slice #2111) | ships (the Deploy/Restart actions, plan confirmation, and `operations` polling added to that section, with the regenerated client) | consumes (resolve affordance) |
| Registry tests (§16) | ships (list/status handler-level tests ship here with the registered handlers; union-handler ingress/origin-rejection/wiring tests plus union catalog/protocol-shape tests ship in the pipeline PR where those handlers register) | — | — |
| Pipeline tests (§12 of the pipeline spec) | — | ships | — |
| Fencing tests (§10 of the fencing spec) | — | — | ships |

The pipeline stacks on the registry; fencing stacks on the pipeline.
The router-vs-catalog test pins registration both ways
(`cmd/evener-hub/appwire_catalog_test.go` — routed-but-uncataloged fails
exactly like cataloged-but-unrouted), so a union-returning handler cannot
land before its catalog entry and generated client. Hand-written Go
request/response structs land in the registry PR as private (unexported,
unregistered) types beside the behavior they describe, so the backend
contract is reviewable with no router or catalog wiring; handler
registration plus the public catalog entries plus the regenerated client
for those handlers arrive together with the pipeline PR's generator work.
No undocumented provisional types ship in any PR.

## 3. Scope and non-scope

Scope: the seven controller-side methods besides `attach` — `evener/host/list`,
`add`, `update`, `remove`, `status`, the `evener/host/teardown-retry`
repair mutation, and the `evener/host/teardown-recover` recovery mutation (§6) —
plus durable persistence of host entries with hot-apply,
the Hosts settings contract (§13; the add/edit/connect/remove section already
shipped, and only the Deploy/Restart/polling files land in the pipeline PR per
the §2 table), and the catalog/client/registration work
per the §2 table. `evener/host/plan`, `deploy`, `restart`, `operations`,
the local `evener/host/running` probe handler, and the controller-side
operation store are defined in the deploy-pipeline spec. The
`evener/host/orphan-resolve` crash-recovery mutation, fencing, and boot
reaping are defined in the crash-fencing spec. `evener/host/attach` ships
with #1603 and is relied on, not re-specified.

Cross-component effects (each owned by the cited section, none introduced
elsewhere): component-03 validation rules enforced on mutations, boot load,
registry paths, and live reconcile (§4, §14, §15), with the 63-remote-host cap
withdrawn by decision (Jesse, 2026-09-26; component 03 §Scope, design §2);
component-04b deploy/restart internals reused verbatim under the operation
model (deploy-pipeline spec §6); component-05 `Online()`/broker rebind consuming the live host set
(§15); component-06 navigation poke consuming the live host set, manifest
`sources` enumeration as the registered-source set, and the picker staying
hidden at zero remotes (§13, §15); the component-07a host-admin controller's
per-host forwarding and notification fan-outs starting and stopping with the
staged commit and its rollback (§6, §14); decision-source (archive/favorite)
validation rewired from the startup `RemoteHosts` snapshot to the live set
(§6); `GET /api/health` demoted to the human-readable surface carrying no
`healthy` field — `evener/host/running` is the authority (deploy-pipeline spec §6); source-registry
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

Every `evener/host/*` request (all fourteen methods, `attach` included) passes
through the shared origin guard: landed by #1603 at the dial seam
(`guardRemoteHostDial`) and the remote-client dispatch seam
(`guardRemoteDispatch`), and extended by the registry PR to the common
request-ingress/router boundary as a pre-admission hook. Honestly-marked
remote-originated, peer-forwarded requests are refused before admission —
before dedup and before token validation on the phases where those stages
exist — and before any handler runs. The mutating handlers (`attach`, `plan`,
`deploy`, `restart`, `add`, `update`, `remove`, `teardown-retry`,
`teardown-recover`, `orphan-resolve`, `running`) are unreachable to honestly-marked peer-forwarded requests; a
markerless request is local-originated by construction and takes the full
admission path. The reads (`list`/`status`/`operations`) are guarded
equally because they disclose the controller's topology and operation state.
(`attach`'s guard routing ships and is test-pinned with #1603; this document
pins the guard orderings for its two registered handlers (`list`/`status`) and
states the behavior for the five union handlers (`add`/`update`/`remove`/`teardown-retry`/`teardown-recover`) whose orderings pin in the
pipeline PR where they register (with `teardown-retry`/`teardown-recover` origin-rejection tests in that PR), the pipeline document its five, the fencing document
its one (with `orphan-resolve` orderings in the fencing spec where that handler
registers) — thirteen new methods plus `attach` is fourteen.) The one direction-scoped exception is the
`evener/host/running` peer probe: a controller-originated request issued only
through `sshManager.ChannelIfAttached(name)` over a live channel peered by the
#1603 handshake, admitted on the remote only over that same attached session
(deploy-pipeline spec §10). The exception never admits a browser- or forwarded-origin request, the
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
deploy's planned restart — §4; the primitive ships here, the pipeline consumes
it) and the two deliberate non-attach SSH uses, both defined in the
deploy-pipeline spec §6: `plan`'s gated preflight refresh (run with no gate
held, the gate acquired only after it completes) and the deploy/restart
worker's post-operation preflight (the same one-shot preflight). Both are bounded one-shot
preflight command executions over the manager's transport that never initialize
a channel or touch a supervisor.
## 4. Host mutations: list, add, update, remove

`evener/host/list` and `evener/host/status` are hub-side handlers registered
like every other hub method (the router-vs-catalog test pins registration);
`add`/`update`/`remove`/`teardown-retry`/`teardown-recover` behavior ships
here behind private hand-written request/response types with no router or
catalog registration, with their handlers registering in the pipeline PR. All seven are
classified as mutations where they mutate, admission-gated like the hub's other
settings mutations, origin-guarded per §3, with catalog entries and regenerated
clients per the §2 table and the exact shapes in §11.

`evener/host/list` is a read: params `{}`; response `{hosts: HostRow[]}`. It
never dials and never takes the mutation lock. Every configured host renders
its full effective `HostConfig` fields plus live state: `attached` (via
`sshManager.ChannelIfAttached(name)`), preflight facts when known (installed
version, OS/arch), last attach error, whether the manager is mid-`Ensure`, and
the origin marker (`hub.toml` vs sidecar — the effective source after merge).
The registry extends the manager to expose the channel's pinned (generation,
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
on-disk file state. The filter runs one bounded synchronous read: the hub reads
the file bytes ONCE per call into a single snapshot and derives both the host
set and the per-entry content hashes from those same bytes — never a host-set
re-read plus a separate content-hash parse, so no inter-read edit can make a
changed entry look unchanged or deleted. A fingerprint re-validation after
filtering covers the residual race: if the file changed across the single read,
the read falls back to unavailable instead of serving the filtered view. A
name whose content changed renders unavailable — its row omitted
from `list`, its `status` a typed changed-entry refusal (conflict class, discriminator
`changed-entry`, data carrying the changed name; §11 pins the arm and §12
the envelope pair, asserted by the protocol-shapes test) directing the caller
to wait for the reconcile, never the old SSH/path config served as current),
and
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
connection data; the full reconcile runs async so reads never take the
mutation lock — the only synchronous file read on the path is the bounded
host-set re-read above.

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
at `cmd/evener-hub/config.go:177`). The component-03 host-count cap was
withdrawn by decision (Jesse, 2026-09-26; component 03 §Scope, design §2
"Host-count cap: withdrawn"): no add, boot, or registry path enforces it, and
`ErrTooManyHosts` is not a sentinel. A `[[hosts]]` list larger than the
navigation manifest's 64-source limit is therefore accepted, and such a config
fails navigation for the entire hub until it shrinks. The staged commit still
validates the merged post-change live set under the mutation lock before the
sidecar persist, and boot load and the registry `Add`/`Update` paths run the
same validation, with no count rule among them. `add` refuses any name already
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
commit first, then rebind/teardown, gate released last (deploy-pipeline spec §5). Every update
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
mark lands with the pipeline store — and a crash between the sidecar commit and the
mark has the mark reconstructed from the tombstone at boot (deploy-pipeline spec §4)). Removing an
attached host stops its supervisor and detaches. The removed host's cached
snapshot rows are not silently dropped: the source's last-known-good snapshot
is retained as an explicit tombstone (§15), and the UI warns when the host has
live remote threads (host data on the remote is untouched). A lost-response
`remove` retry landing after a re-add minted a new generation and carrying a
fresh key refuses as stale instead of tearing down the new incarnation; a
retry carrying the original key follows the superseded-receipt rule (§5) and
returns the recorded `committed` receipt — the re-add purge retains that
name's newest same-key superseded `remove` receipts (plus the purge-persisted
pruned markers, §6) so the superseded arm stays satisfiable after the purge.

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
teardown never blocks unrelated hosts. Lock order is fixed: the host gate first, then the process-wide
mutation lock, then the store mutex innermost (deploy-pipeline spec §§4–5). Concurrent
read-modify-write on the sidecar cannot lose updates.

`add` accepts an optional opaque `mutationId`; `update`/`remove` require
`mutationId` with `expectedGeneration` plus `expectedIncarnationId` (§4) — a
keyless `update`/`remove` never stages. The staged commit persists a durable
mutation receipt in two writes — the single explicit receipt write point. The
step-(2) sidecar write carries a transient staged-receipt marker: the scoped
key plus `stagedAt`, a `swapStarted` intent (false at stage time, flipped true
in its own atomic sidecar write under the mutation lock before the runtime
transition begins; a finalizing claim preserves the marker's persisted value,
never forcing it true), a `teardownStarted`
flag (false at stage time, flipped true in its own atomic write after the swap
and before the first teardown; a finalizing claim preserves the persisted flag
the same way),
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
- Phase `staged` with `swapStarted: false`: re-apply the staged runtime set
  to the live handles first (the sidecar already holds the new config, so the
  live runtime must converge to it), then re-run the marker's pinned teardown
  to completion, then finalize the receipt from that observed result
  (`committed`, or `committed-with-teardown-failure` with the pre-minted
  `remnantId` when the re-run actually failed — never the staged provisional
  outcome on its own). No phase finalizes `committed` while old lifecycle
  handles remain: for a `remove` or binding-changing `update` the pinned
  teardown destroys the superseded supervisor/channel/fan-out, and where the
  pinned target is empty the re-run is a no-op.
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
marker's persisted phase exactly like a live replay above (every phase
re-applies the staged runtime set first, then re-runs the pinned teardown to
completion and finalizes from the observed result — no marker finalizes
`committed` while old lifecycle handles remain). A dead claimant's claim (crashed or vanished holder) is
adopted by running the same phase-aware recovery under the same
generation/incarnation guards and finalizing under the claimant's attempt
token. The finder releases the mutation lock after the atomic claim lands, then try-acquires the remnant's host gate before running the teardown — gate after lock-release, never gate-while-holding-lock, so the §5 gate-first order holds — then re-takes the lock to verify the claim before finalizing. A held gate means a live committer still owns
the host, so the finder releases the claim back to the staged-receipt marker,
responds busy, and finalizes nothing — except when the finder already holds that host's gate reservation: a mutation reserves its host's gate before taking the mutation lock, so a same-host foreign marker always meets a self-held gate and the non-reentrant try-acquire would fail against the caller's own reservation. A self-held gate already proves no other live committer owns the host, so the finder reuses the held reservation and adopts the marker without re-acquiring. Only a dead claim on a gate-free host — or a foreign marker on a self-gated host — is
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
reads false — the swap may have applied — and a phase-`staged` marker with
`teardownStarted: false` AND `swapStarted: false` re-applies the staged
runtime set first and then re-runs the pinned teardown like every other phase,
finalizing from the observed result; no phase finalizes `committed` while old
lifecycle handles remain. Boot
finalizes each host entry by the persisted phase and flags (a leftover finalizing
claim preserves both: the claim write carries the marker's `teardownStarted`
value and runtime phase over unchanged, and boot recovery decides by them —
never by treating every claim as teardown-started): a marker with `teardownStarted:
false` AND `swapStarted: false` re-applies the staged runtime set to the live handles first (the sidecar
already holds the new config, so the live runtime must converge to it before
the receipt finalizes), then re-runs the pinned teardown to completion and
finalizes from the observed result — and the receipt records `bootRecovered: true` (the
optional receipt field in §6); a marker with `teardownStarted: true` re-runs the pinned teardown to completion like every other phase and finalizes from the observed result — a clean re-run returns `committed` with no remnant, and only a re-run that actually fails persists the pinned target plus the pre-minted
`remnantId` as the remnant record with
`committed-with-teardown-failure` plus `bootRecovered:
true` for operator resume through `evener/host/teardown-retry` — so a
lost-response retry after the crash returns the recovered receipt or the retry
handle instead of re-applying, and a crash after teardown began never loses its
repair target. Every phase re-runs the pinned teardown to completion before
finalizing, so post-teardown crashes never lose the handle. Compensation's stash-restore removes the marker
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
same incarnation id. The (generation, incarnation id) pair is unique and the incarnation id is never reused: generations are strictly monotonic per the §1 glossary —
no live re-add path reuses a retained generation — so no two incarnations share a pair. A replay matches only a retained
receipt of the same name, kind, and mutationId. One rule, three arms: the
first arm is the direct hit — same name, kind, mutationId, current generation,
AND current incarnation id — returning the recorded receipt; the second arm is
the superseded hit — the same key pinned to a superseded generation or a
different incarnation — returning the recorded outcome
for recovery only, never authorizing work (a lost-response `remove` retried
after a re-add recovers its outcome rather than tearing down the new
incarnation; the different-incarnation case covers only a crash-torn write —
a receipt whose pinned pair survived on disk while the live registry already
moved on — and returns its outcome against the live entry for recovery only,
never a hit authorizing work against the live entry); the third arm is the pruned refusal
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
`escalationAgeSec`): a keyed retry that observes its intended effective row
(for a lost-response keyless `add`: the listed entry hash equals the intended
entry; for `remove`: a missing name or a `removed: true` row — a keyed
`remove` retry returns its receipt first) returns the recorded receipt, never a
fresh apply. A keyless `add` retry that observes a matching listed row returns
the explicit ambiguous outcome — the row may be the caller's committed
mutation, a pre-existing identical row, or another client's remove/re-add, and
the keyless retry cannot distinguish them — instead of claiming the mutation
committed. Any intervening change to an effective field breaks
the equality and forces the `stale-entry` path instead of guard omission. A
keyless `add` retry that has not yet observed its intended row never re-applies keyless: an absent row cannot prove the original uncommitted — a committed `add` later removed reads identically absent — so the retry mints a fresh `mutationId` and commits as a new keyed mutation (dedup-safe under the new key), never as a keyless continuation; only a retry carrying the original `mutationId` may replay-or-claim under the stale-entry guarantee. `update` and `remove` take no keyless path at all: a
lost-response `update`/`remove` retry always carries its original key and
follows the single superseded-receipt rule, an `update`/`remove` carrying a
fresh key against a superseded generation or a superseded incarnation refuses
as `stale-entry`, and no unkeyed `update`/`remove` ever stages against a live
incarnation.

Processing order is fixed: receipt dedup by (mutationId, name, kind, current
generation, current incarnation id) runs first for every keyed
`add`/`update`/`remove` — dedup-first applies only when `mutationId` is present; a keyless `add` carries no key to look up, so it skips the receipt lookup and routes directly to the list-reconciliation path in this section — subject only to `update`'s and `remove`'s
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
`WebConfig` retains the selected path, so the registry carries the canonical config
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
compensation-record clear — every durable step of the cross-file protocol (deploy-pipeline spec §9).

Merge rules: `add`/`update` refuse any name already declared in `hub.toml`
(the file is authoritative for its own names); `remove` deletes only sidecar
entries; a `hub.toml`-declared host can never be shadowed or UI-removed — its
`list` entry says "declared in hub.toml". The refuse rule is enforced at write
time AND the boot merge rejects the hand-edited collision. Every sidecar
mutation re-reads the current `hub.toml` bytes under the mutation lock and
validates the staged change against them — reusing the same `hub.toml`
content-hash fingerprint the plan/deploy path binds into confirmation tokens
(deploy-pipeline spec §3), or an equivalent fingerprint comparison: if the file changed since
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
mutation returns; the runtime-apply half of that reconcile is a persisted
sidecar phase on the reconcile marker (`reconcile-applied: false` at reconcile
stage time, flipped true in the same atomic sidecar write that publishes the
dropped-then-rebuilt set, before the receipt finalizes): a crash or
runtime-apply error between the follow-up drop and the rebuild leaves the
marker open, a lost-response retry re-runs the pending runtime-apply under the
same marker before finalizing the `collision-dropped` receipt, and boot
completes an open marker the same way it completes the drop (reconcile marker
with a matching fingerprint finishes the rebuild, never the hard error); other
changes are picked up by the fingerprint-bound
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
edit superseded the interrupted reconcile), stays the hard startup error. An
armed intent whose validation fingerprint still matches the on-disk file while
a staged sidecar duplicate is present is likewise a hard startup error naming
both locations and the required explicit recovery (drop the staged duplicate
or re-stage the mutation): the fingerprint equality proves no race crossed the
commit, so the duplicate is either a post-crash manual edit or an ambiguous
crash state the spec refuses to auto-resolve — the marker plus a mismatching
armed intent scope the silent recovery to exactly the race the spec
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
remnantId?, remnantResolvedAt?, recoveryAttestation?, bootRecovered?}` —
`recoveryAttestation` (`{operator, statement, observedAt}`) is present exactly
on receipts resolved through `teardown-recover`, absent otherwise.
`droppedEntry` (the staged entry the post-rename reconcile dropped, persisted
in the same lowerCamel effective-config shape as `HostRow`'s config fields —
§11) and
`winningFingerprint` (the winning `hub.toml` fingerprint) are present exactly
on `collision-dropped` receipts — a replay renders the dropped arm from these
receipt fields, so a lost-response retry, even after restart, reconstructs
both what was dropped and which fingerprint won. `remnantId` is present
exactly when the commit staged a remnant, `remnantResolvedAt` exactly after
`teardown-retry` or `teardown-recover` resolves it, `recoveryAttestation?`
exactly on receipts resolved through `teardown-recover` (the `{operator,
statement, observedAt}` attestation the recovery call validated — the audited
recovery contract's durable record, pinned field-for-field by the
protocol-shape test), `bootRecovered` (as `true`) exactly when boot
finalized a crash-window staged-receipt marker (§5). `prunedReceipts` maps the
full pruned scope key (mutationId, host name, mutation kind, pruned post-commit
generation, pruned incarnation id) to `{prunedAt}` — the bounded markers the
count/TTL compaction persists, riding the same atomic writes and the same
hard-startup-error posture. `teardownRemnants` maps the server-generated
opaque `remnantId` to `{host, kind, seam, pendingTeardown, generation,
incarnationId, mutationKey, committedAt, cleanupHandle}`; `cleanupHandle` is
the independently actionable ownership/remote-cleanup handle persisted at
commit, resolvable without any live in-process handle. A cleared remnant
persists as a typed resolved-remnant record in the same section, keyed by
`remnantId` and carrying `{clearedAt, resolutionKind: "retry" | "recover",
hostKind: "live" | "removed", host, name, attestation?}` plus the receipt
reference the clearance recorded — the full replay payload a lost-response
retry renders (`already-cleared` with its `hostKind` pairing, `recovered-cleared`
with `clearedName`/`clearedAt`), reconstructable after restart without the live
entry — and the record is purged only by the name's next re-add or the
retention-expiry prune. `attestation` is present exactly on `recover` records.
`pendingTeardown` is the self-contained generation-scoped teardown target —
the staged supervisor/channel/fan-out teardown description pinned at commit,
resolvable without the live entry; a remnant never depends on the live
registry to execute. The commit, the `teardown-retry` clearance, and the
re-add purge are all single atomic writes with the same hard-startup-error
posture on corrupt or schema-invalid content.

Cleared-remnant records carry their own bounded retention independent of
tombstones (owner-set cleared-marker TTL plus an at-most-64-newest-per-name
count bound in the same owner-knob family — a live host's repaired failures
create no tombstone, so without both bounds repeated recovered-update failures
grow the sidecar without limit): every
boot and every sidecar mutation compacts retry records past either bound in the
same atomic write, and lost-response retries past the bounds read as
`teardown-unknown-key` not-found instead of `already-cleared`. Recovery
(`recover`-kind) records compact under the same dual bound — their TTL and
count knobs ship in the same family with their own defaults — and compact only
otherwise on the name's re-add or the retention-expiry purge.

Retention: receipts and remnants are per-host and per-generation — re-add
purges that name's superseded-generation receipts — except that name's newest
same-key superseded `remove` receipts, which the purge retains so a
lost-response `remove` retry after the re-add still returns its superseded
receipt per §4 (a surviving pruned marker refuses `stale-entry` where no
retained receipt survives) — and — only after that name's
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
while a remnant is open for that name — boot fails startup on a sidecar-held
colliding live entry, or excludes a `hub.toml`-declared colliding entry from
the live set as `blocked-pending-teardown` (never published live) until
`teardown-retry` resolves it, so no
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
crash-fencing spec §3), so boot reaps or the retry signals through the persisted boundary. The
retry re-resolves live bindings by (generation, incarnation-id) equality only
while the committer is still alive; after a crash it acts through the
`cleanupHandle` alone. A retry executing a boot-recovered remnant additionally
runs as a fenced remote operation (crash-fencing spec §4): it persists a fresh fencing epoch in the
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
phase (deploy-pipeline spec §5) with the lock released across the teardowns and re-acquired to
persist the committed receipt plus real remnant (verifying the claim's attempt
token still owns the marker); (4) if the swap itself fails, compensate fully
before responding: restore the prior sidecar bytes from the stash (atomic
rename), then revert the runtime to the previous set, then report exactly
which step failed. Compensation runs in persisted phases, tracked in the
store-side compensation record (deploy-pipeline spec §9 pins the phase field)
— which the sidecar restore cannot touch — never on the sidecar
staged-receipt marker: the committer persists that record with the stash
reference before the sidecar restore lands, the
sidecar restore lands first, then the runtime revert, and the store-side record
plus its stash reference persist until the runtime revert succeeds — so a
runtime-revert failure or a crash between the two restores still names its
restore source, and boot resumes the rollback plus stash cleanup from that
durable record instead of reporting a diverged swap as compensated. A typed swap-failure response is
sent only after both restores. The commit point is the start of the post-commit rebind phase: once
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
stash reference (deploy-pipeline spec §9): that record is the compensation's
sole durable authority, applied in phase order (sidecar first, rows second) before
any prune. An open staged-receipt marker names the in-progress commit, never the
compensation source. Boot reconciles the compensation record before pruning any
stash: a live compensation record naming the stash preserves it, and only a
stash named by no live record — the compensation record durably cleared — is
prunable, so a registry-swap crash can never lose the rollback bytes an unfinished
compensation still needs.

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
against in-flight `Ensure` and running supervisors) is the registry's core work.
Decision-source validation (the archive/favorite `validateDecisionSource`
host-set check, which today walks the startup `cfg.RemoteHosts` snapshot)
consumes the live host set as part of the swap — preferably through a
live-registry callback — so a newly added host is accepted and a removed host
is refused immediately after the commit; the registry tests pin both directions at that
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
execution deadline of the same owner-set family. The retry persists each attempt as a durable attempt record (server-generated attempt id, fencing epoch, start time, `open` state) in the same atomic sidecar write that claims the remnant, and holds the host gate only while its attempt is live: on timeout the retry releases the host gate in the same atomic sidecar write that marks its attempt record timed-out-but-open, then reports
the terminal `committed-with-teardown-failure` outcome with the remnant still
open plus its attempt record open for fencing — a stuck remote process therefore surfaces a terminal
outcome with a live retry handle, never an indefinitely held gate. The open attempt record is an attempt fence: while one stands, every lifecycle path on the name refuses except a later `teardown-retry` naming the same remnant (a live attempt still holding the gate refuses even that retry with the typed busy error — the gate holder owns the attempt). A later retry try-acquires the freed gate, then fences the timed-out attempt first: it takes over the prior attempt's fencing epoch (kill/wait plus guard advance per the crash-fencing spec §4), marks the prior attempt record fenced-closed in the same atomic sidecar write that claims the remnant under a fresh attempt record, and only then runs the pinned teardown again, so two retries never execute the same cleanup concurrently and a wedged gate never blocks repair. The gate is therefore never held past a returned response: live attempt → gate held, later retries busy-fail; timed-out attempt → gate free, later retry adopts and fences the open attempt record). The retry
validates against the remnant's OWN pinned identity, never against the
registry's current values: it re-resolves the pinned teardown target by the
remnant's recorded `(generation, incarnationId)` plus its persisted
`cleanupHandle` — the live entry for the name may be absent (post-remove) or a
newer incarnation (post-update) without blocking the retry — and refuses only
when the remnant's own pinned target fails to resolve through its own handle
(typed `teardown-unknown-key`), never because the registry moved on. An open remnant whose `cleanupHandle` cannot be resolved (backend reports the
pinned target unresolvable) is recoverable only through the authenticated
auditable `evener/host/teardown-recover` recovery mutation — a mutation
admitted like every other `evener/host/*` request (origin-guarded per §3,
never in `remoteHostAdminMethods` per §3, fenced by the remnant and orphan
fences per the crash-fencing spec §8: an open orphan fence on the name
refuses it with `orphan-fenced-busy`, never a clearance past an unverified
orphan) — (params
`{remnantId, attestation: {operator: string, statement: "teardown-verified-absent", observedAt: string (RFC3339)}}`;
response the outcome union in §11 with `outcome: "recovered-cleared"` plus the
cleared `remnantId`): the call try-acquires the host's per-host gate first — the
same gate a live `teardown-retry` attempt holds — failing fast with the typed busy error when a
retry attempt is live, and holds it through the clearance. When a timed-out-but-open attempt record stands (gate free, attempt fence open), the recover fences it first — takeover of its fencing epoch (kill/wait plus guard advance per the crash-fencing spec §4) and a fenced-closed mark in the same atomic sidecar write as the remnant claim — before the safety checks below, so the clearance never lands past possibly-live cleanup. Under the gate it claims
the remnant atomically (claim under the mutation lock with an attempt token, so a
concurrent retry racing the claim loses exactly one of the two), then verifies
the operator attestation is present and well-formed, re-runs the safety checks
(no live handle tagged with the remnant's `(generation, incarnationId)` pair
exists, no supervisor or channel binding names the remnant's pinned target),
re-checks the safety conditions immediately before the clearing write, and only
then clears the remnant in one atomic sidecar write — recording the attestation
(operator, statement, observedAt) on the original mutation receipt beside
`remnantResolvedAt` (outcome becomes `committed` with `remnantResolvedAt`) — so
the forced clearance is an explicit audited operator decision, never a silent
drop, and a concurrent retry can neither start inside the check nor have its
in-progress cleanup marker cleared. The claimed attestation `operator` must equal the session's authenticated identity (crash-fencing spec §5); a mismatch refuses validation before any clearance. An attestation that fails validation or a
safety check that still finds live state refuses without clearing, naming the
blocking check, and releases the claim plus the gate. A replay of the original
mutationId after resolution returns the
resolved receipt, never the stale committed-with-teardown-failure. The retry
is idempotent by remnantId: a retry naming an already-cleared remnant returns
the already-cleared success arm, a receipt-returned no-op — never a second
teardown, never not-found. The response carries the stable
`committed-with-teardown-failure` outcome naming the seam AND the `remnantId`,
and the UI renders the committed host row with a teardown-retry affordance —
never a generic failure affordance, never a blind full-mutation retry.
## 7. Operation store

Defined in the deploy-pipeline spec §§4–5 and §7: records, dedup scope,
retention and compaction, the store mutex and lock order, gates, and boot
recovery. This document cites that store for the `host-removed` mark path,
the outstanding-token-row purge path, and the mirrored generations.
The registry owns the per-host boundary record the store mirrors: one record
per host name — `{generation: number, incarnationId: string, presenceEpoch:
number}` — written in the same atomic store writes that mirror the
generation, reconciled by the same boot rule, and rolled back by the same
rollback rule (deploy-pipeline spec §§4, 8 cite this schema, never restate
it). A tombstone-expiry prune that deletes the name's last sidecar trace
still advances the presence epoch into the high-water entry, so the next
clean-slate re-add carries the advanced epoch.

## 8. Plan, deploy, restart

Defined in the deploy-pipeline spec §§3, 5–6, and 9: confirmation tokens,
gates, the plan/deploy/restart workers, and the cross-file commit intents.
This document cites that pipeline for token bindings, dedup-first order,
consume-and-create atomicity, and the `attachUnderGate` consumption
contract (§4 ships the primitive).

## 9. Crash orphans, fencing, and orphan-resolve

Defined in the crash-fencing spec §§3–8: local reap, fencing epochs and
leases, quarantine, `orphan-resolve`, the remote helper, and boot reaping.
This document cites that spec for the orphan fence, the fencing quarantine,
and what a fenced mutation requires.

## 10. Last-known store

The manager owns a per-host last-known store — the latest preflight facts with
their capture timestamp plus the latest attach error with its timestamp, plus
the last-known running revision/health/process-start-time snapshot and the
last plan-time refusal (the inputs behind `status`'s `restartFollows` and
`planRefusal` — without these a no-dial `status` read cannot produce those
fields), keyed by the host's registry (generation, incarnation id) pair and
updated on every successful preflight and every attach outcome. Pair-keying
keeps a crash-torn snapshot from surfacing under the live entry: generations
are strictly monotonic (§1), so no live re-add reuses a generation, and the
pair key additionally fences a stale write that survived on disk past the
registry's advance. Every `plan` call publishes into it under the gate
before returning — except the pre-mint no-token refusals
(`unattached`, `refresh-failed`, `probe-failed`, `remnant-open`,
`handler-absent`): `unattached`, `refresh-failed`, and `remnant-open` occur
before any acquisition and publish gateless, never by acquiring the gate to do
so (holding the gate across the SSH preflight refresh reintroduces the
slow-probe busy-refusal bug), while a gated-probe `probe-failed` or
`handler-absent` publishes under the probe-window gate before releasing it:
a refusal publishes its
`{terminal, message}` as the pair's plan-time refusal (a later success clears
it), and the `remnant-open` check runs before any gate acquisition — a
remnant-fenced name refuses without ever try-acquiring the gate, so the remnant
refusal takes precedence over a held-gate busy — and every completed probe
— success or authenticated failure — publishes
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
cannot misattribute it — before marking `complete` (deploy-pipeline spec §6), so `status` reports
the new installed version once the operation reads `complete`.

## 11. Protocol types

The registered `list`/`status` handlers get their AppWire protocol catalog
entries (`appwire/protocol.go`, `ScopeHub`) plus request/response structs
(`appwire/types.go`) plus the regenerated TypeScript client in the registry PR;
the union-shaped methods get hand-written structs here with public catalog
registration plus the regenerated client in the pipeline PR with the
union-registration generator work (§14; non-union registry helpers register
immediately).
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
their discriminator (`outcome`, `reason`) —
the pipeline catalog defines one named Go struct per arm so each generates its own
interface field-for-field, and the pipeline PR extends the generator with explicit union support so each method's result is typed as the union over the arm names: the union registration lists arms in discriminator order, the generator emits the method result as a TypeScript union of the arm interface names in that order plus a discriminator-narrowing guard per arm, an unknown discriminator value stays a typed unknown-arm refusal (never a silent first-arm cast), a new arm is additive only (existing arms keep their names, fields, and discriminator values — the protocol-shapes test fails on any rename, removal, or field-type change), and the pipeline protocol-shapes test pins every arm field-for-field including each arm's discriminator. The contract's one-named-Go-struct-per-arm
rule (mutation-result arms, `plan`'s planned vs no-token arms,
`teardown-retry`'s six `outcome` x `hostKind` arms — the two success outcomes
(`teardown-complete`, `already-cleared`) plus the
`committed-with-teardown-failure` timeout arm, each crossed with `hostKind:
live | removed` — three outcomes times two host shapes is six declared arms)
exists so each arm generates its own interface plus the union-typed result, with the pipeline
protocol-shapes test pinning the wire shapes and the `outcome` discriminator
on both `plan` arms; the frontend selects arms at runtime on the
discriminator. Arm registration: `EmitCatalog`
emits only each method's `Result`-named type plus types transitively reachable
from its fields (`internal/appwirets/emit.go` `registerTopLevel`/`discover` —
an anonymous struct `Result` panics the generator, and an `any`/`interface`
field emits `unknown`), so sibling arm structs are NOT emitted by being named
in prose. The pipeline PR therefore extends the generator with explicit union support
(generator work lands in the pipeline PR): the catalog declares one named Go struct per
arm plus a named union registration referencing every arm, and the generator
emits each arm as its own interface with the method's `MethodTypes` result
entry typed as the union over the arm names; the pipeline protocol-shapes test pins
every arm field-for-field (including each arm's discriminator) — and the
union-shaped catalog/client changes for the registry methods (the mutation-result
arms, `RemovedRow`, `teardown-retry`'s outcome arms) land in the pipeline PR with that
generator work, never in the registry PR: the union-returning handlers register
in the pipeline PR with them (§2 — the router-vs-catalog test rejects routed-
but-uncataloged methods), the registry ships the sidecar/commit behavior behind
hand-written request/response types plus the store-skeleton helpers, and the
regenerated client for the union-shaped registry responses arrives with the pipeline PR. The
single-response-interface alternative is rejected: collapsing the arms would
force optional-ified `host`/`token`/`reason` fields the absent-when-unknown
rule cannot distinguish.


Registry surface shapes (pipeline and fencing shapes live in their own
documents and are cited, never restated):

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
  `list`/`status` row), `openRemnantId?: string` (present exactly on rows — live
  or tombstone — whose name holds an open remnant; the blocking remnant's id),
  `escalationAgeSec?: number` (present only on rows — live or tombstone — whose
  name holds an open remnant past the escalation bound — the escalation age
  the expiry-escalation rule promises; absent everywhere else per the
  absent-when-unknown rule). Tombstone values: a tombstone row renders
  from retained effective `HostConfig` with `attached: false`, `midEnsure:
  false`, and the removed entry's `origin`, `generation`, and
  `incarnationId`; `installedVersion?`, `installedVersionAgeSec?`, `osArch?`,
  `lastAttachError?`, and `lastAttachErrorAgeSec?` stay absent (never null)
  per the absent-when-unknown rule — every other `HostRow` field carries the
  explicit value above, so no non-optional field is left unknown.
- `evener/host/add`: params are one full host entry (all seven `HostConfig`
  fields; `name` required) plus optional `mutationId: string` (opaque,
  non-empty, at most 128 bytes — the idempotency key; a keyless `add` skips dedup and is non-retryable as a continuation — §5 — and commits a keyless-add audit record under a server-generated internal key with no client idempotency semantics, so the crash/audit trail has no keyless gap (audit records compact under the same dual bound as receipts — at most 64 newest per name plus an owner-set audit TTL in the same knob family, every sidecar mutation and every boot compacting past either bound in the same atomic write, and riding the 64-tombstone / 16 MiB global sidecar cap in §15 — so keyless-add spam cannot grow the sidecar without limit); response is the
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
  droppedEntry: <the staged effective config in the same lowerCamel shape as
  `HostRow`'s config fields — never a literal `HostConfig`, whose `toml`-only
  tags would generate `Name`/`SSH`/`EvenerPath`/… instead of
  `name`/`ssh`/`evenerPath` —, winningFingerprint: string, host: HostRow}` (the
  dropped arm always carries the authoritative `HostRow` regardless of
  mutation kind — when the post-rename reconcile drops the just-committed
  sidecar entry, the authoritative result is the winning `hub.toml` live
  entry, never a tombstone, so a remove whose name was re-added through
  `hub.toml` mid-remove returns the live row — the arm names the staged entry
  the post-rename reconcile dropped plus the winning `hub.toml` fingerprint
  the receipt carries, so a replay returning it can never read as a live
  commit) or the keyless-ambiguous arm `{outcome: "ambiguous", observedRow:
  HostRow}` (returned only by a keyless `add` retry that observes its intended
  row — the row may be the caller's committed mutation, a pre-existing
  identical row, or another client's remove/re-add, so the response claims no
  commit and carries no receipt semantics; §5) — a normal result-union response,
  never an AppWire error-envelope
  throw (pre-commit failures throw typed error envelope codes; post-commit
  outcomes return through the union — the failure arm names a committed
  mutation whose teardown needs forward retry) — the `remnantId` is mandatory
  on the failure arm, `seam` names the failed rebind step, and the committed
  row is always present so the UI renders the row with a teardown-retry
  affordance. The catalog carries all four arms field-for-field and the
  regenerated client carries each arm as its own interface through the
  generator mapping above; a shape missing `remnantId` on the failure arm
  fails the protocol-shapes test, as does a missing `ambiguous` arm.
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

The `planRefusal` reason values name the refusal the deploy-pipeline spec
§6 defines; `status` renders them display-only and never branches on them.

- `evener/host/teardown-retry` (mutation): params `{remnantId: string}`;
  response six declared arms — three outcomes crossed with both host shapes:
  `{outcome: "teardown-complete" | "already-cleared" |
  "committed-with-teardown-failure", hostKind: "live" | "removed", host:
  HostRow | RemovedRow, remnantId: string, escalationAgeSec?: number,
  seam?: string}` — `seam` present exactly on the
  `committed-with-teardown-failure` arms, naming the failed seam — a
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
  for idempotent lost-response retry — the typed resolved-remnant record persists
  in the sidecar past the clearance carrying the replay payload (`clearedAt`,
  `retry` kind, host kind, host/name payload) so a later lost-response retry,
  even after restart, still returns `already-cleared`, and is purged only by
  the name's next re-add or retention-expiry prune (plus the cleared-marker
  TTL compaction in §6 — past the TTL the ID reads as
  `teardown-unknown-key`). When the remnant belongs to a `remove` there is no
  live row to return: `host` is the same `RemovedRow` `remove`'s clean path
  returns — same values, same tombstone rendering — with `hostKind:
  "removed"` (vs `"live"` for the `HostRow` arm). The retry validates against
  the remnant's pinned `(generation, incarnationId)` + `cleanupHandle` — the
  live entry may be absent or newer without blocking it — and never acts
  against the live entry.

- `evener/host/teardown-recover` (mutation): params `{remnantId: string,
  attestation: {operator: string, statement: "teardown-verified-absent",
  observedAt: string (RFC3339)}}`; response `{outcome: "recovered-cleared",
  remnantId: string, clearedName: string, clearedAt: string (RFC3339),
  hostKind: "live" | "removed"}` — `clearedName` is the remnant's pinned name
  and `hostKind` names which row shape the cleared generation had (`"removed"`
  when the remnant belonged to a `remove`, `"live"` otherwise); the response
  carries no live row because the recover clears a remnant whose teardown never
  produced one. The call persists a typed resolved-remnant record (the
  recovery marker — `remnantId` → the replay payload in §6, same record shape
  as the retry's cleared-remnant record)
  in the same atomic sidecar write that clears the remnant, so a retry naming
  an already-recovered ID replays `{outcome: "recovered-cleared", remnantId,
  clearedName, clearedAt, hostKind}` from the record — `hostKind` required on
  the initial response and on every replay, pinned field-for-field by the
  protocol-shape test — never a second clearance, never
  not-found. Recovery markers compact under their own bounded retention — at most
64 newest recovery records per name plus a recovery-marker TTL in the same
owner-knob family as the cleared-marker TTL (every sidecar mutation and every
boot compacts markers past either bound in the same atomic write; defaults ship
in the implementing PR) — so a live host's recovered clearances stay bounded
exactly like its retry markers. A `recovered-cleared` replay past the bound
reads as `teardown-unknown-key`, never a second clearance. The attestation is validated before admission completes and the
  safety checks in §6 run before the clearing write; any failure refuses
  without clearing, naming the blocking check. The catalog pins the mutation
  classification plus the request/response shapes field-for-field.
- Mutation `concurrent-edit` refusal: conflict class, discriminator
  `concurrent-edit`, data `{stagedFingerprint: string, observedFingerprint:
  string}`. It fires on two paths: (1) the sidecar commit's final check (§6) finds
  the `hub.toml` fingerprint moved between the validation read and the final
  check after bounded retries; (2) the live-external reconcile validation (§15) rejects merged-config drift — data on that path is `{firstSource: string, firstCount: number, secondSource: string, secondCount: number}` naming both merged sources and their host counts. The protocol-shapes test asserts the
  code-plus-discriminator pair plus the data shape per path.
- Mutation `tombstone-capacity` refusal: conflict class, discriminator
  `tombstone-capacity`, data `{bound: string, blockingNames: string[]}`. It
  fires exactly when a tombstone persist fits only by evicting a remnant-gated
  tombstone (§15). The protocol-shapes test asserts the
  code-plus-discriminator pair plus the data shape.
- `evener/host/teardown-retry` `teardown-unknown-key` refusal: not-found class,
  discriminator `teardown-unknown-key`, data `{remnantId: string}`. It fires
  exactly when the named remnant id is unknown or purged (§6: unknown/purged ID
  reads as not-found; a cleared-remnant marker still present returns the
  `already-cleared` arm instead). The protocol-shapes test asserts the
  code-plus-discriminator pair plus the data shape.
- `evener/host/status` changed-entry refusal: conflict class, discriminator
  `changed-entry`, data `{name: string}`. It fires exactly when the named
  entry's on-disk content changed under the cached fingerprint (§4); the
  response arm is otherwise the host's `HostRow` shape.
- Mutation conflict: `conflicting-mutation-id` rides the same AppWire error
  envelope as `conflicting-operation-id` and is refused the same way.
- `remnant-open` (conflict class): the remnant fence on every lifecycle and
  attach path for the fenced name. Data carries `{remnantId: string}` naming
  the blocking remnant. The envelope mechanics (numeric code, discriminator
  wiring) are defined in the deploy-pipeline spec §11, which cites this
  classification and never restates it.

Pipeline shapes (`plan`, `deploy`, `restart`, `operations`, `running`) are
defined in the deploy-pipeline spec §10. `orphan-resolve` and the
`BoundaryEntry` union are defined in the crash-fencing spec §9.

## 12. Error handling

Attach failures: typed `SessionUnavailable` via the #1603 classifier; the
caller-context error stays raw; the UI renders the classification, not the raw
chain. Deploy, restart, and plan error paths are defined alongside their
handlers in the deploy-pipeline spec §11; fencing and orphan error paths in
the crash-fencing spec §8. Host busy: a held per-host gate
fails `update` and `remove` fast with a typed busy error (deploy-pipeline
spec §5 for the holder classes). A refused mutation
leaves the running operation untouched — it finishes and records its normal
terminal state. Hot-apply failures: staging failures change nothing; a failed
swap compensates by restoring the prior sidecar bytes and reverting the
runtime (both before the response), and the error carries which seam failed
(registry / manager / source / admin controller / web view / persistence /
decision-source validation). `evener/host/*` on an unknown host name: typed
not-found. `changed-entry` rides the conflict class
envelope and `teardown-unknown-key` the not-found class, each with the
`evenerErrorInfo` discriminator plus the data in §11; the
protocol-shapes test asserts the code-plus-discriminator pair for each.

## 13. UI

Hosts settings section (the add/edit/connect/remove section, dialog, and `stores/hosts.ts` shipped with #1784 and the edit slice #2111 at `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx`; the pipeline PR adds only the Deploy/Restart actions, plan confirmation, and `operations` polling to it, with the regenerated client, per the §2 table; this section states the contract only): host rows with state chip (online /
offline / connecting / removed-retained), installed version + controller
version, OS/arch, origin marker, actions: Connect, Deploy, Restart, Edit,
Remove (each with the confirm pattern used elsewhere in settings), and the Add
button. Rows with `removed: true` expose re-add plus remnant repair: Connect,
Deploy, Restart, Edit, and Remove render disabled (never firing) on tombstone
rows, while a tombstone row whose name holds an open remnant carries a
teardown-retry affordance and, past the escalation bound (§6, §15), a
teardown-recover escalation affordance naming the attestation it requires.
Live rows whose committed outcome left an open remnant carry the same
teardown-retry affordance (escalating to teardown-recover past the bound)
beside the rendered row. While an orphan fence is open on the name the repair
affordance degrades to an orphan-must-resolve-first affordance naming the
`orphan-resolve` next step (crash-fencing spec §8): the fence refuses the
repair call, so the UI never directs the operator into a silently refused
repair. The section's UI tests pin a removed row showing the re-add
affordance with every live-host action disabled, a remnant-gated live row
showing the retry affordance, an escalated row showing the recover
affordance, and an orphan-fenced row showing the resolve-first affordance.

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
plan (token semantics in the deploy-pipeline spec §3): target host, controller revision, resolved remote target path, whether a
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
  `Update`/`Remove` with the same cycle/validation rules (the host-count cap is
  withdrawn — component 03 §Scope), safe for concurrent use.
- `cmd/evener-hub/internal/sshconn` — `Manager` gains the live host-set
  surface (add/update/remove entry, each safe against in-flight `Ensure` and
  running supervisors) and thin exported entry points for deploy/restart
  reusing the 04b internals (`deploy`, `ensureDecision`, `waitHealthy`,
  `bootstrapHub`), plus an exported deadline-bounded preflight re-read (the
  existing one-shot SSH command sequence re-run on demand, no channel
  involvement — `plan`'s facts-refresh mechanism, deploy-pipeline spec
  §6); plus the internal gate-aware `attachUnderGate` primitive (accepts
  an already-held per-host gate, suppresses supervisor startup until the
  worker's post-verification handoff — the restart worker's reattach path,
  consumed in the deploy-pipeline spec §6); `Ensure`/`Attached`/
  `ChannelIfAttached`/facts unchanged.
- `cmd/evener-hub/config.go` — sidecar load/merge (hard-error duplicate rule)
  + atomic write.
- `cmd/evener-hub/app_host_manage.go` (new) — the `list`/`status` handlers
  with router registration, catalog entries, and regenerated client, plus the
  `add`/`update`/`remove`/`teardown-retry`/`teardown-recover` behavior behind
  private hand-written request/response types with no router or catalog
  registration (their handlers register in the pipeline PR),
  mutation classification (`plan` is a mutation — it mints
  durable state; `list`/`status`/`operations` are reads), guarded by the
  shared origin guard — landed by #1603 at the dial + dispatch seams,
  extended by the registry PR to the common request-ingress/router boundary as a
  pre-admission hook running before admission (§3) rather than wrapping
  handlers one by one. The registry PR ships the hook plus the registry-surface
  (`list`/`status`) ingress/origin-rejection/wiring tests; the union-returning
  handlers register in the
  pipeline PR with the union catalog and regenerated client (§2), which
  asserts guard-before-admission on the pipeline surface plus the dedup/token
  orderings where they ship — explicitly NOT in
  `remoteHostAdminMethods` (negative assertion in the allow-list tests).
- AppWire protocol catalog entries for the registered methods + request/response
  types (private hand-written Go request/response structs land in the registry
  PR beside their behavior with no registration, so the backend contract is
  reviewable), with the shapes in §11
  (the wire contract field-for-field; the generated client through the
  generator mapping named there) — `list`/`status` register with catalog
  plus regenerated client in the registry PR, while public AppWire catalog
  registration plus the regenerated TypeScript client for the union-shaped
  methods land in the pipeline PR with the union-registration generator work
  (§11), never in the registry PR: the registry
  ships no catalog entry and no regenerated client for a union-shaped
  response, so the "each handler ships with catalog + types + regenerated
  client" contract is satisfied per-PR-sequence — hand-written types in the registry PR,
  public registration plus generated client in the pipeline PR — never by undocumented
  provisional types.
- The operation store: defined in the deploy-pipeline spec §§4–5. The
  registry PR ships only the store-owned helpers `remove` depends on
  (`host-removed` mark path, outstanding-token-row purge path, atomic
  writes — no pipeline behavior behind them). Where the store lives on disk
  follows the hub's existing durable-record conventions (the
  implementing session picks the closest existing store pattern and names it
  in the PR).
- `cmd/evener-hub/main.go` — replace the startup snapshot of host entries
  with the live view seam (registry, manager, sources, web config, and the
  host-admin controller's host set/fan-outs); keep every existing
  `RemoteHost*` wiring.
- Frontend: the section, dialogs, and `stores/hosts.ts` shipped with #1784 and
  the edit slice #2111; the pipeline PR owns only the Deploy/Restart actions,
  plan confirmation, and `operations` polling added to them per the §2
  table. The spawn
  picker is untouched (its Connect trigger ships with #1603).

## 15. Data flow (remove + tombstone)

`refreshRemoteThreadSnapshot` enumerates the registered sources; removing one
would otherwise drop its rows from the next snapshot. Removal instead writes
an explicit tombstone record for the source (name, the removed entry's
effective `HostConfig` — all seven fields `HostRow` requires, so `list` can
render the removed row without a live entry — last-known-good rows, removal
timestamp, the removed incarnation's id persisted alongside the generation
high-water mark — boot reconciles on the exact persisted (generation,
incarnation id) pair (generations are strictly monotonic per §1, so the pair
check is defense-in-depth against a crash-torn tombstone, never a live
re-add reusing a generation) — with a hard bound: at
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
remnants, per the retention rule — §6). The prune advances that name's
presence epoch in the same atomic write and persists the advanced value in
the name's high-water entry (§1), so a later clean-slate re-add carries the
advanced epoch. Expiry never purges a tombstone whose
name still holds an open teardown remnant — the mutation-path prune skips
remnant-gated names, and the in-memory filter keeps rendering them — so the
failed teardown's generation-specific handles are completed through
`teardown-retry` before the gate releases (§6) — with a bounded
operator-escalation backstop: an open remnant older than the owner-set
remnant-escalation bound (a multiple of the cleared-marker TTL, default ships
in the implementing PR) surfaces an operator-escalation signal on the remnant
(`teardown-retry` responses and `list` tombstone rows carry the escalation
age), and the operator resolves it out-of-band (manual teardown of the pinned
target, then a forced clearance through the authenticated `evener/host/teardown-recover` recovery mutation (§6); the
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
removed names — bounded below) in the same atomic write as the entry mutation — `add` mints
generation 1 for a never-seen name, `update` advances the live generation by
one, and re-add mints a generation strictly above every retained high-water
mark for the name — generation 1 only when no mark survives and no live
history remains — so a re-added byte-identical entry still invalidates every
pre-remove token while no surviving referrer keeps the old history alive. Every `add`/re-add mints a fresh incarnation id in
that same atomic write (opaque, unique per mint, persisted per live entry next
to the generation — the teardown-targeting identity `teardown-retry` selects
on; §6). The boot load restores persisted generations before the store serves
any request, so the generation check — the token's generation must equal the
registry's current generation — survives restarts: an update-then-restart
keeps outstanding tokens valid (the bumped generation is on disk), and no
restart silently invalidates or re-validates anything. Store-side mirror: the
per-name generation high-water mark is mirrored into the operation store
itself (same atomic store writes as records). The mirrored-generation boot
rule is owned by the deploy-pipeline spec §4 (marker-gated rollback: a store
mirror newer than the sidecar mark with no matching commit marker rolls back
to the sidecar mark, the discarded value kept only as the high-water mark) and
is cited here, never restated — with
the missing-sidecar preservation: a name with no sidecar mark contributes no
mark (the surviving mirror alone is the high-water mark),
never a zero that drags it down — so deleting a corrupt sidecar and re-adding
an identical host cannot restart its generation at 1 and adopt the old
incarnation's records or tokens: the re-add mints above the mirrored
high-water mark instead. Deleting both durable files is the only clean-slate
path, and the spec names it as such — there is no silent history adoption
either way. High-water marks are bounded: a per-name high-water entry survives
only while a live entry, tombstone, retained receipt, pruned marker, open
remnant, resolved-remnant record, mirrored store generation, or outstanding
token still names that generation; every sidecar mutation and every boot
compacts entries with no surviving referrer in the same atomic write. Distinct
names that churned and fully expired therefore leave no durable trace —
re-add mints generation 1 for a name with no surviving mark and no live
history — while any surviving token or replay record keeps its entry alive
until it too expires. Boot-merge collision: when a retained tombstone collides at boot
with a newly live `hub.toml` host (or a live sidecar entry from a re-add), the
live host is treated as a new incarnation: its restored generation is set
strictly above the tombstone's high-water mark before any historical
receipts/remnants apply, so old `host-removed` records stay at or below the
mark and live records stay unmarked — never a promotion of a pre-collision
record into the current generation, never a block on valid operation-ID reuse
— unless the colliding name holds an open teardown remnant, in which case
boot fails startup when the colliding live entry comes from the on-disk sidecar
itself, or excludes the colliding declared entry from the live set (fenced
`blocked-pending-teardown`, never published live, stale handles and tokens for
the name observe nothing new) when it comes from a `hub.toml` declaration, and
represents the declaration as blocked until `teardown-retry` resolves the open
remnant; only then does the declaration mint a new generation and incarnation
above the high-water mark (§6 — never a live incarnation over an open
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
`list`/`status` lock-free read stays lock-free and serves the file-filtered
view on mismatch — §4): on a fingerprint match the call proceeds
on the current snapshot with no lock; on mismatch a mutation/`plan`/`deploy`
admission takes the mutation lock synchronously and runs the same
adopt-then-bump-then-clear sequence as the sibling reconciling commit (re-read
the file, adopt added/changed declared entries into the running registry, drop
declared hosts deleted from the file out of the registry/manifest/admin host
set and fan-outs — a deleted declared host reads as not-found on all
`evener/host/*` methods until re-declared, and its name-keyed caches clear —
then bump affected generations and clear or rebind every name-keyed cache
above — and the adopt-then-bump-then-clear sequence coordinates with in-flight
operations exactly like `update`/`remove` under the same gate-first order
(deploy-pipeline spec §5): before taking the mutation lock the reconcile
try-acquires every affected host's per-host gate; a held gate defers the
whole reconcile — the admitted call proceeds on the pre-reconcile snapshot
meanwhile — while a free gate set lets the reconcile take the mutation lock
holding those reservations and apply immediately; a deferred reconcile
re-runs the same generation bump and cache clear when the gates release, so
no external edit survives past the in-flight operations' completion — a
changed entry never rebinds the registry entry, generation,
channel, or supervisor under an in-flight deploy/restart still operating on
the pinned old configuration), so no external edit — including a
declared-host removal — survives past the next mutation/`plan`/`deploy`
admission (a `list`/`status` admission serves the file-filtered view
immediately — §4: deleted names absent, changed names unavailable — and
schedules the same sequence asynchronously, debounced):
the step publishes a new snapshot under the lock and the admitted
mutation-path call then serves from it — but only after the reconciled merged
set passes the complete merged-config validation first (the reconcile
validates the merged post-adopt live set against the full component-03 rules
under the mutation lock BEFORE publishing: valid sets
publish exactly as above, while a failed validation publishes nothing — the
last-good snapshot stays live, the admitted call serves its file-filtered
view from it, and the
failure surfaces as the typed `concurrent-edit` configuration error (merged-config drift), naming both sources and
their counts), and all live consumers (registry, manager
bindings, sources, manifest, host-admin controller fan-outs, web-config view)
update atomically under that lock before the admitted call proceeds (a
`list`/`status` read arriving while the reconcile holds the lock serves the
file-filtered snapshot (§4 — the single-snapshot filter against the current file
bytes) lock-free instead of waiting — reads never fail busy for a
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
in §4 (the callback applies the same bounded synchronous host-set filter and
never takes the mutation lock on the read path), so a deleted or changed
declared host is invisible to every live consumer by its next read — a deleted
host absent, a content-changed host unavailable (never the old SSH/path
config) until the reconcile publishes the new runtime snapshot — not only
after the next host-management request. The UI cannot remove or re-add these
hosts; only the entry-change rule moves their generation. Token bindings
reference these generations (deploy-pipeline spec §3): validation requires the token's generation
to equal the registry's current generation for the name. Snapshot publications
carry the generation of the source they were read from, and a publication
whose generation no longer matches the registry's current generation for that
name is rejected; re-add also clears the name-keyed caches (snapshot rows and
last-known preflight facts) as part of its staged commit — an in-flight
refresh from the removed incarnation or a stale name-keyed cache entry can
never republish rows for the new host.
## 16. Testing

Registry tests (all bullets in this section ship with the registry PR, except the union-handler catalog/protocol-shape pins named below plus the pipeline-handler halves named in the remnant-gate and status-after-refusal bullets, which ship in the pipeline PR where those handlers register — §2):

- Registry live-update tests (including the add-time cycle rules),
  manager add/remove-vs-supervisor tests (removing an attached host stops its
  supervisor, closes its channel, and drains its per-host lifecycle handles
  before the entry is gone), sidecar merge/atomicity tests including the
  refuse rules (a live external `hub.toml` edit that fails the merged-config
  validation fails the reconcile instead of publishing: the
  last-good snapshot stays live and the failure surfaces as the typed
  `concurrent-edit` configuration error), the boot-time hard-error duplicate,
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
  indicator — never the full set silently); the global cap holds (churn over
  distinct names converges newest-first by removal timestamp to the newest 64
  tombstones / 16 MiB, remnant-gated tombstones never eviction candidates, and
  a persist that fits only by evicting a remnant-gated tombstone refuses typed
  `tombstone-capacity` naming the bound plus the blocking names); re-add purges, clears the
  name-keyed caches, and mints a new generation — publication from an obsolete
  generation is rejected; a refresh after remove re-merges the tombstone's
  retained rows (never drops them); boot prunes expired tombstones durably
  even with no mutation since expiry (pinned with controllable removal
  timestamps against the 7-day default — §15)), decision-source live-set
  tests (a newly added host is accepted by archive/favorite validation
  immediately after the commit; a removed host is refused; validation reads
  the live set, never the startup snapshot), ingress-boundary ordering tests
  (a remote-originated request is refused before admission — proven by
  ordering tests on the registry surface (`list`/`status`, registered here),
  not by handler wrapping; the
  before-dedup / before-token-validation orderings are asserted in the pipeline PR where
  dedup and token validation ship, as are the union-handler ingress/origin-rejection/wiring
  tests for the handlers that register there — the tests present the cooperative bridge
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
  absent-row keyless `add` retries minting a fresh `mutationId` and committing as a new keyed mutation (never a keyless re-apply — §5), the explicit ambiguous
  outcome once a matching row is observed, `stale-entry` once
  an effective field changed; `update` takes no keyless path — missing either
  field is a validation refusal; a keyless `add` commit persists the server-keyed audit record with no client idempotency semantics, and audit compaction holds (repeated keyless adds converge to the newest 64 per name under the audit TTL — §11)), remote-origin rejection for
  `list`/`status` (the #1603 origin guard refuses
  honestly-marked peer-forwarded requests before admission — the registry surface
  only: `list`/`status` here, `add`/`update`/`remove`/`teardown-retry`/
  `teardown-recover` origin rejection alongside the pipeline-surface
  rejections where those handlers register; `status` rejection is asserted
  here, never in the
  pipeline spec), and a wiring test mirroring the 05a registration
  tests for the registered `list`/`status` handlers. Mutation-idempotency tests (the operation store's cross-name rule,
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
  recoveryAttestation?, bootRecovered?}` (`incarnationId` the pinned incarnation — the commit-point
  test pins the full five-part scoped key, so a crash-torn boot-merge
  looks the receipt up under the right incarnation (strict monotonicity per
  §1 means no live path produces a shared generation); `remnantId` while the
  remnant is open, `remnantResolvedAt` after resolution, `recoveryAttestation`
  exactly on `teardown-recover`-resolved receipts, `bootRecovered`
  exactly on boot-recovered receipts) and the cleared marker is the typed
  resolved-remnant record (`remnantId` → replay payload) in `teardownRemnants`;
  a post-`remove` retry returns the tombstone
  removed-row shape, a post-add/update retry the live row; the retry validates
  against the remnant's pinned `(generation, incarnationId)` + `cleanupHandle`
  — the live entry may be absent or newer without blocking it — and never acts
against the live entry; a bounded-run timeout against a fake teardown
dependency returns the `committed-with-teardown-failure` arm with `seam`
naming the failed seam and the remnant still open for a later retry — the timed-out retry releases the gate while its open attempt record fences the name (a concurrent live attempt still busy-fails), and the later retry try-acquires the freed gate, fences the timed-out attempt (takeover, kill/wait, guard advance, fenced-closed mark) before re-running the teardown, so the same cleanup never runs concurrently and repair never wedges on a held gate;
protocol-shape tests pin the `changed-entry` refusal arm, the `concurrent-edit`,
`tombstone-capacity` catalog entries with their envelope
code-plus-discriminator pairs here; the `teardown-unknown-key` entry, the four-arm mutation-result union (including the
`ambiguous` arm), plus the
`teardown-retry`/`teardown-recover` request/response shapes pin field-for-field in the pipeline PR where those handlers register (§2);
an unresolvable-`cleanupHandle` remnant refuses `teardown-unknown-key` on the
retry path and clears only through `teardown-recover` — attestation
validation, safety-check refusals naming the blocking check, the audited
`recovered-cleared` receipt (the protocol-shape test pins the
`recoveryAttestation` receipt fields field-for-field), and replay-after-recovery returning the same
`recovered-cleared` response (`remnantId`, `clearedName`, `clearedAt`) from
the persisted recovery marker, never not-found).
Pending-marker tests: a post-commit receipt write
  lost while the process stays alive leaves the staged-receipt marker staged
  — a replay finalizes carrying the pre-minted `remnantId` and the pinned
  teardown target — a replay by phase (every phase re-applies the
  staged runtime set first, then re-runs the pinned teardown to completion and
  finalizes the observed outcome — a real teardown failure surfaces
  `committed-with-teardown-failure` with the remnant; a clean re-run returns
  `committed` with no remnant — the staged provisional outcome is never
  returned as-is), and the next mutation-path
  write finalizes a foreign marker for its own host before its own stage,
  while a marker for another host never blocks it (per-host markers;
  foreign-host entries ride along untouched in the same atomic writes).
  Lock-free `list` tests: `list` takes no mutation lock and prunes nothing
  durably; expiry filtering is in-memory only and the durable prune lands on
  the next mutation-path write — the `hub.toml`-fingerprint pre-handler check
never takes the lock on the read path (mismatch serves the file-filtered
view lock-free — §4: a name deleted on disk is absent from the response and
a name whose content changed renders unavailable through the changed-entry
refusal, never the old SSH/path config — and schedules the reconcile
asynchronously, debounced; no read ever fails busy for a fingerprint reason;
the reconcile re-compares fingerprints on completion and reschedules on a
still-present mismatch, so a coalesced mid-flight edit is never silently
lost). Remnant-gate tests:
  re-add and retention expiry skip names with open remnants; while a remnant
  is open for a name, `deploy`, `restart`, `Ensure`-triggered work, `plan`
  (no-token `remnant-open` arm with `remnantId`, never a minted token), and
  attach all refuse with `remnant-open` naming the blocking `remnantId` — the
  fence is host-wide, never mutation-only; the `deploy`/`restart`/`plan`
  handler-integration halves of this bullet ship in the pipeline PR where those
  handlers register, and the registry PR keeps only the registry-level
  store/fence halves (re-add/update/remove refusal, retention-expiry skip,
  attach refusal) against fake publishers. Status-after-refusal tests: `status`
  renders the pair-scoped refusal after each of the nine no-token refusal
  reasons (`unattached`, `refresh-failed`, `probe-failed`, `remnant-open`,
  `handler-absent`, plus the four terminal validation refusals
  `controller-dirty`, `target-unwritable`, `target-missing-prereq`,
  `target-unit-findings`) — one case per reason, each asserting pair-scoped
  publication, `terminal: true` on the four terminal arms and `terminal: false`
  on the five retry arms, `status` rendering of the refusal (never stale data
  from a superseded pair), and clearing of the refusal by a later success;
  the end-to-end plan-publication halves ship in the pipeline PR where `plan`
  registers, and the registry PR keeps only status-rendering of pair-scoped
  refusals published via fake publishers.
  Config-path tests: a `--config`
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
  validation fingerprint still matches the on-disk file with a staged duplicate
  present boots hard-error naming both locations — the equality proves no race
  crossed the commit, so the duplicate is hand-made or ambiguous and requires
  explicit recovery); the same collision with no marker and no
  armed intent, or with a marker whose fingerprint no longer matches, boots
  into the hard startup error. Swap-window tests: a marker with `swapStarted:
  false` and `teardownStarted: false` re-applies the staged runtime set to
  the live handles first (the sidecar already holds the new config — a
  finalize that skips the swap diverges the live process from durable state),
  then re-runs the pinned teardown to completion and finalizes from the
  observed outcome (a clean run returns `committed` with no remnant; a failed
  run surfaces `committed-with-teardown-failure` with the remnant) — never
  `committed` while old lifecycle handles remain — while a marker with the
  intent written (`swapStarted: true`) but the swap incomplete recovers by
  re-applying the staged runtime set first, then re-running the pinned teardown to completion and finalizing from the observed result while keeping the pinned remnant — never a teardown-first mismatch, never without
  one. A leftover finalizing claim preserves the marker's `teardownStarted`
  value and runtime phase, and boot decides by the persisted phase — never a
  blanket teardown-started claim. File-posture
  tests: sidecar and store temp files are `0600`, renames preserve the mode,
  and startup refuses a file readable beyond its owner, and the stash gets the
same coverage (stash temps `0600`, mode-preserving rename, owner-only
readability refusal).
## 17. Acceptance criteria

1. A user with zero hosts configured adds one from the UI, sees it connect,
   and the spawn picker appears with the host selectable — no file editing, no
   controller restart (helper-gated hosts take the fencing spec's migration
   path first — crash-fencing spec §6).
2. An offline configured host has a working Connect action in the Hosts
   section (the picker's trigger shipped with #1603); success flips its online
   state everywhere (rail, picker, manifest) through the existing paths.
3. Deploy from the UI: defined in the deploy-pipeline spec §13. The registry
   half of the promise: after confirm, the host reports the new
   version in `status` via the worker's required post-operation facts refresh
   (published to the (generation, incarnation id)-scoped last-known store
   before the operation marks `complete`), and the version-skew signal (facts
   vs controller build) is truthful.
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
   read dials anything — `list`/`status` never attach
   (test-pinned) — and the pipeline's two deliberate non-attach SSH uses
   (deploy-pipeline spec §6) are channel-free by construction (no initialize,
   no supervisor, no attach state machine; test-pinned).
7. Standard gates green on every PR (go/build/vet, package races, full hub
   suite, module-lint; web + browser + lint-generated for the UI PR).

## 18. PR size estimate (LOC)

- Registry: ~800–1100 (registry/manager/config/admin-controller/wiring/catalog/
  client for the registered `list`/`status` surface) + tests.
- Pipeline: ~700–1000 (handlers + token + operation store + reconciliation) +
  tests (deploy-pipeline spec), plus the Hosts settings section, dialogs,
  stores, and polling (moved from §13 per the §2 table).
- Fencing: crash-fencing spec + tests.

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

- 2026-09-17: three-way split of the 08 host-management spec. This document
  keeps the registry surface; the deploy pipeline moves to
  `2026-09-16-multi-host-08b-deploy-pipeline.md`; crash orphans and fencing
  move to `2026-09-16-multi-host-08c-crash-fencing.md`.

