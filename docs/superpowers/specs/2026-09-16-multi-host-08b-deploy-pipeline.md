# Component spec 08b — Deploy pipeline (plan/deploy/restart as durable idempotent operations)

Status: not started. This spec is the hand-off for the implementing session.

Depends on: the registry spec (registry, `hub.toml`, receipts, remnants, tombstones, generations, the
`attachUnderGate` primitive, and the operation-store skeleton helpers). The pipeline stacks on
the registry; fencing stacks on the pipeline. Hand-written Go request/response structs for pipeline-owned methods land in the same PR as
their handlers (union-shaped private types authored in the registry PR stay unregistered until this PR registers them — registry spec §2). Public catalog registration plus the regenerated client for union-shaped
responses arrive with the pipeline PR, as do the union catalog/protocol-shape tests.

## 1. Terms

Every later section uses these terms with exactly these meanings.

- **MutationId.** The client-supplied idempotency key on `add`/`update`/`remove`: opaque, non-empty, at most 128 bytes, no required structure. A replay is a call repeating a previously used key.
- **OperationId (client operation ID).** The client-supplied operation ID on `deploy`/`restart`: opaque, non-empty, at most 128 bytes, no required structure. `deploy`/`restart` responses carry `id` (the controller-assigned record id) and `clientOperationId` (echoing the caller's value).
- **Generation.** The per-name monotonic counter minted by `add`/re-add and advanced by `update`, persisted in `hub.toml`. Re-add mints strictly above every retained high-water mark for the name; a name with no surviving mark and no live history re-adds clean at generation 1 with a fresh incarnation id plus an advanced presence epoch, so it cannot adopt the old history (registry spec §15). A token, receipt, or record pins the generation it ran under; validation requires equality with the registry's current generation for the name.
- **Incarnation id.** The opaque server-generated string minted beside the generation on every `add`/re-add, never derived from it and never reused: at most 128 bytes, and the generator pins its output to 36 bytes (canonical UUID text), so the 8 KiB cursor-cap bound in §8 holds by construction. The pair (generation, incarnation id) is the guarded-mutation and dedup identity everywhere.
- **Receipt.** The durable finalized outcome of a host mutation, keyed by (mutationId, host name, mutation kind, post-commit generation, incarnation id).
- **Remnant.** The durable in-progress teardown record of a committed-with-teardown-failure mutation, addressed by its opaque server-generated `remnantId`. An open remnant fences every lifecycle and attach path on its name until `teardown-retry` resolves it or escalated `teardown-recover` clears it.
- **Tombstone.** The durable removed-host record carrying retained rows, persisted in `hub.toml`. Tombstone-only names render in `list` as `removed: true` rows and accept only re-`add`.
- **Per-host gate.** The try-acquire (never wait) mutex serializing deploy/restart/`plan`/teardown work for one host name. A held gate fails new work fast with the typed busy error.
- **Mutation lock.** The process-wide lock serializing `hub.toml` read-modify-write only, never across a teardown. Outermost among the durable-write locks (mutation lock → store mutex); the per-host gate precedes all of them (§5).
- **Store mutex.** The lock serializing operation-store read-modify-write. No path holding the store mutex ever acquires the mutation lock.
- **Origin guard.** The shared pre-admission hook refusing honestly-marked remote-originated, peer-forwarded requests before admission. An honest-peer recursion terminator, not a security boundary.
- **Confirmation token.** The controller-minted, single-use, expiring opaque bearer `plan` returns beside its plan, bound to the host entry, generation, incarnation id, `hub.toml` fingerprint, facts, and running state it was minted from.
- **Operation record.** The durable controller-side record of one `deploy`/`restart`, keyed by controller-assigned id, deduplicated on (host, kind, client operation ID, pinned generation, pinned incarnation id).
- **Boundary.** A persisted ownership description a verifier checks before signaling a possibly-live process: `local-linux`, `local-darwin`, `local-markerless`, or `remote-fencing` (defined in the crash-fencing spec §9), plus the `boundary-unavailable` custody sentinel a corrupt-store custody import carries when corruption destroyed the boundary (crash-fencing spec §9).
- **Fencing epoch.** A worker's durable (controller boot id, per-host monotonic op sequence) presented on every SSH command it runs.
- **Presence epoch.** The per-host monotonic removal/presence counter the file advances on every add, remove, re-add, and expiry purge. It persists in `hub.toml` per live entry and per tombstone; every `hub.toml` write that adds, removes, re-adds, or expiry-prunes the name advances it in that same atomic write. The store mirrors it into the per-host boundary record on the same writes that mirror the generation (§4); cursor validation reads the mirrored value (§8).
- **Facts revision (`factsRevision`).** The canonical digest of every preflight field planning or deploy decides on: OS/arch, home directory, resolved roots, UID, installed version, protocol version, and launch flags. `plan` computes it over the refreshed facts at mint (§3) and `HostPlan.factsRevision` carries it as the mint-time reference; deploy checks the token's facts freshness rather than recomputing the digest (§6 step 3).

Pipeline-specific terms:

- **Freshness bound.** The owner-adjustable cap on preflight-facts age a token may deploy
  under. Default 5 minutes. Minted into the token; deploy enforces the minted value.
- **State-transition sequence.** The durable monotonic per-store counter advanced by every
  atomic store write that moves a record into a terminal state. Race scans compare sequence
  values, never wall-clock timestamps.
- **Compaction sequence (`compactSeq`).** The durable monotonic counter advanced by every
  compacting write. Pagination cursors pin it; a mid-pagination compaction is detectable.
- **Store dedup tombstone.** The bounded retained replay source a compacted terminal record
  leaves behind: client operation ID with its (host, kind, generation, incarnation id)
  scope plus the full replay fields. Distinct from the registry spec §5 pruned receipt
  markers, which cover mutation receipts.
- **Quarantine epoch (`quarantineEpoch`).** The durable counter advanced once per
  corrupt-store quarantine, persisted outside the quarantined file. Cursors pin it; a
  pre-quarantine cursor never admits against the replacement store.
- **Wall-clock high-water mark.** The greatest wall-clock value observed by any token
  mint or facts-capture write, persisted in the store file. The rollback guard compares
  `now` against it. It never moves backward.

## 2. Scope

This spec creates the deploy pipeline: `plan` mint, `deploy`/`restart` execution,
idempotency receipts and dedup, the operation store, operations pagination, and boot
recovery of pipeline state.

This spec owns `evener/host/plan`, `evener/host/deploy`, `evener/host/restart`,
`evener/host/operations`, the `evener/host/running` probe handler, confirmation tokens,
operation records, the per-host gate protocol, the cross-file commit intents, the
union-registration generator work, and the `teardown-retry`/`teardown-recover` handler
registration with their union catalog entry and the `teardown-unknown-key`
discriminator (the registry spec §2 table assigns that registration here). It mirrors
generations in the store. It consumes
`attachUnderGate` for restart reattach.

This spec does not own mutations, mutation receipts, `hub.toml` commits, remnants,
tombstones, or generations. Those are defined in the registry spec (§4 mutations, §5
receipts, §6 remnant records, §15 tombstones-generations). The last-known store schema
is defined in the registry spec §10; this spec defines only the publish side. Fencing,
orphan reaping, quarantine, and `orphan-resolve` are defined in the crash-fencing spec;
this spec cites them and never restates them.

## 3. Confirmation tokens

`plan` mints the token. This is seam (a): what `plan` mints is the token plus its full
binding list below.

Mint runs gated. `plan` refreshes facts with no gate held, then try-acquires the
host gate once and holds it through the running-state probe, validation, the durable
mint, publication, and return. The gate hold covers the probe window plus validation
plus the durable mint write plus the last-known-store publication (§6). `plan`
releases the gate before returning. Mint under
the gate replaces: the same atomic store write that persists the new token deletes the
host's earlier unconsumed token rows.

TTL and freshness follow one formula. `expiresAt = min(mintTime + configuredTTL,
factsCapturedAt + freshnessBound)`. Both terms are absolute wall-clock timestamps. The
default TTL is 5 minutes, owner-adjustable. The default freshness bound is 5 minutes,
owner-adjustable. An owner-set TTL above the bound clamps to the bound at mint. The
minted `expiresAt` already reflects the refresh-to-mint interval, because
`factsCapturedAt` predates it. When the remaining freshness is exhausted at
mint time (`factsCapturedAt + freshnessBound` at or before now) mint refuses
without minting — the no-token `refresh-failed` arm (stale facts at mint read
as a refresh failure, and re-planning refreshes them), never an
already-expired token. Deploy never recomputes `expiresAt`. Freshness is
immutable for the token lifetime: deploy enforces the minted `expiresAt`
only, never the live owner knob. Lowering the bound affects only tokens
minted after the change.

The token binds the host name, the registry generation at mint, a hash of the resolved
host entry, the `hub.toml` content-hash fingerprint as on disk at plan time, the
`factsRevision` digest (§1) of the refreshed preflight facts the plan was built from, `factsCapturedAt`, the resolved target path,
the controller revision, the probed running revision, the probed running-health flag, the
probed `processStartTime` when the probe carried it, the effective freshness bound in
force at mint, and a nonce. Deploy step (3) compares the token-bound facts age against
the token-bound bound value, never a re-read owner knob. A mid-flight knob change can
neither extend nor shorten an outstanding token's freshness term past its minted
`expiresAt`.

Generation and incarnation bind like every other binding. The token binds both the
generation and the incarnation id minted beside it. Deploy refuses when either differs
from the registry's current pair. Sharing a generation with an open remnant never shares
validity. Live `remove` revokes every outstanding token row for the name through the
`pendingStoreSync` revocation intent (§9). Re-add starts with zero valid tokens.

The outstanding-token rule is supersede-on-mint. Minting a new token for a host
immediately supersedes any earlier unconsumed token for that host. Superseded rows do
not accumulate: the mint write deletes them. Expired-token reaping covers only tokens
that expire unconsumed and unsuperseded.

Format and validation: the token is an opaque base64url string of at least 32 characters
(≈192 bits). Its nonce comes from a CSPRNG with at least 128 bits of entropy, unique per
mint. Validate and consume compare with constant-time equality. The token is never
logged.

Storage and lifecycle: minted tokens persist as rows in the operation-store file, under
the same atomic temp-plus-rename-plus-fsync writes as operation records. Expired tokens
reap lazily on any validate/consume pass for that host, and at boot (§7). A quarantined
store drops every outstanding token (§4). Only unexpired, binding-intact tokens for live
hosts survive a restart. A consumed token presented again reads as `token-missing`: the
row is gone, and gone rows never validate.

Wall-clock rollback guard: every validate/consume pass and every facts-age check first
compares `now` against the store's durable high-water wall clock. A `now` more than 30
seconds behind the mark (owner-adjustable tolerance; within tolerance reads as jitter
and invalidates nothing) is a detected rollback. The comparison boundary is the mark, never `now` — one algorithm everywhere: a record invalidates only when its own timestamp postdates the mark (impossible for honest captures, since the mark is the greatest observed wall-clock value — the arm covers corrupt rows only, which read `token-expired` for tokens and stale for facts). A pre-rollback record never shortens its deadline merely because the clock moved backward. Token TTL is monotonic
elapsed time, evaluated against `max(now, mark)`: every `expiresAt` comparison and every
`now - factsCapturedAt` age check substitutes the mark for `now` while the rollback is
active (`now < mark`), and a post-rollback capture anchors at `max(now, mark)`. A
pre-rollback token therefore keeps exactly the real-time lifetime its persisted
`expiresAt` granted — a token already expired before the rollback stays expired, never
valid again — expiring only when elapsed time since mint passes the minted TTL
(equivalently when wall-clock time again reaches `expiresAt`). The mark never moves backward. A
rollback can only invalidate, never extend, a deadline. A forward jump past outstanding
TTLs expires them through the existing `expiresAt` check. No special rule covers forward
jumps.

## 4. Operation store

Deploy and restart take minutes and must not hold an AppWire RPC open. The operation
store is the controller-side durable record of those operations.

Record schema: controller-assigned id, client operation ID, host, kind
(`deploy`/`restart`), state (`pending`/`running`/`complete`/`failed`/`interrupted`/
`orphan-unverified`), progress entries (timestamped, bounded), terminal result,
timestamps, the pinned host generation plus the pinned incarnation id, the worker's fencing epoch (persisted before the first `running` probe per §6; its shape is defined in the crash-fencing spec and never restated here), and a
`host-removed` mark. The `orphan-unverified` variant carries the per-member
`BoundaryEntry[]` array; its shape and verification are defined in the crash-fencing
spec §9 and never restated here. The wire shape is pinned field-for-field in §10.
`createdAt`/`updatedAt` are display-only. They never decide a race.

Dedup scope is (host, kind, client operation ID, pinned generation, pinned incarnation
id), never the ID alone. A dedup lookup matches only records whose pinned pair equals
the registry's current pair for the name. Generation-first precedence: a same-key replay returns the existing record; an
ID colliding with a current-generation record of a different host or kind, or with a
current-generation `host-removed` record, is refused with `conflicting-operation-id`; an
ID whose only current-generation matches are none but whose retained superseded-generation
records exist for the same (host, kind) returns the newest retained superseded record
when the request names no newer intended pair, and refuses `stale-entry` (pruned-generation
value) when the request's intended pair is older than the registry's current pair —
a lost-response retry after an intervening update never silently opens a fresh operation.
No fresh operation ever opens on a superseded pair: a request naming a superseded
(generation, incarnation id) pair replays the retained record when one matches,
or refuses `stale-entry` when none does — never a fresh run against a stale
configuration. The (generation, incarnation id) filter pair in §10 is query-only:
it selects which retained record the `operations` read returns, never which pair
a fresh operation runs under. Re-add starts its
new generation with a clean dedup slate. A `host-removed` record never matches a dedup
lookup. History stays readable either way.

Durability: every operation-store write is atomic (temp-file fsync plus rename plus
parent-directory fsync — the temp file is fsynced before the rename and the containing
directory fsynced after it, matching the `hub.toml` write protocol in the registry spec §6).
Token consumption and record creation are one such write (§6 step 4). The store file and
its temp files carry mode `0600`. Replacements preserve the mode. Startup refuses to
load a store readable beyond its owner. A corrupt or schema-invalid store file at boot
quarantines in custody-first order: boot first persists the quarantine-intent plus
quarantine-custody file beside the store (same atomic temp-file-fsync plus rename
plus parent-directory-fsync write, mode `0600`, never inside the replaceable
store file), and only then renames the corrupt file aside with the boot
timestamp, never deleting it. The intent names the corrupt file plus the custody
file plus the boot timestamp before either rename lands, so a crash between the
custody write and the rename, or between the rename and the replacement-store
open, still boots covered: an intent with no matching aside file re-runs the
rename; an aside file with no complete custody fails startup, never serves. Before the
replacement store serves, that custody file snapshots the safety-critical fences the
quarantined file can no longer prove — per-host fencing quarantines, open
`orphan-unverified` records with their persisted boundaries, per-record ids plus
the allocator high-water mark, and per-name
ownership (generation high-water marks plus incarnation ids). The custody file
schema is `{quarantineEpoch: number, quarantinedFile: string,
custodiedAt: string (RFC3339), recordIds: {recordId: string, host: string}[],
allocatorHighWaterMark: number,
fences: {recordId: string, host: string, kind: "deploy" | "restart", clientOperationId: string, generation: number, incarnationId: string, quarantine: bool,
boundary: BoundaryEntry[]}[], ownership: {quarantineRecordId: string, host: string, kind: "deploy" | "restart", clientOperationId: string, generation: number, highWaterMark: number,
incarnationId: string}[]}` — `recordIds` carries every imported record's original
controller-assigned id verbatim so the replacement store imports by id, and
`allocatorHighWaterMark` carries the pre-quarantine maximum so the replacement
allocator starts above it; one fence entry per fenced host carrying the
quarantined record's persisted boundary verbatim (element type in the
crash-fencing spec §9) plus the full record identity that entry imports under —
`recordId`, host, kind, client operation ID, and the pinned (generation,
incarnation id) pair — so a fence import builds its `OperationRecord` from the
entry itself and never joins an ambiguous per-host `recordIds` row; one
ownership entry per name the corrupt file
yielded. Each ownership entry carries a stable `quarantineRecordId` (server-generated, unique in the custody file) plus the full record identity a replacement `OperationRecord` requires: host, kind (`restart`; custody never invents a `deploy` plan the corrupt file did not hold), client operation ID (server-minted `quarantine-<name>` when the corrupt file yields none), and the pinned (generation, incarnation id) pair with the generation high-water mark. Every custody entry is resolvable: boot imports each fence entry as
an `orphan-unverified` record carrying the custodial boundary under its original
record id, and each
ownership-only entry as an `orphan-unverified` record of the carried kind under its stable `quarantineRecordId`, carrying the single
`boundary-unavailable` entry (boundary lost to the corruption, never verified
empty — the operator attests the name idle out-of-band before resolving, per the
crash-fencing spec §5 attestation rule), in the replacement
store under the same id scope as §4 records — so `orphan-resolve`
(crash-fencing spec §§4–5) and the `operations` detail filter address every
closed name by record id like any other unverified record, and no name stays
permanently blocked for want of an id. When the corrupt file cannot
yield a complete custody snapshot, boot fails startup rather than serving hosts
past an unprovable fence. Completeness is all-or-nothing, never best-effort: the
snapshot is complete only when the corrupt file parses whole — every record it
carries parses and validates, no region of the file is left unparsed or
discarded, and the parsed record ids, against the file's own allocator
high-water mark, account for the file's whole record set with no gap or residue.
Unparseable fences, a boundary that fails schema validation, and ownership
missing for a fenced name are each incomplete — and so is any other shortfall,
including a truncation that merely omits a fenced name's record (a gap below the
high-water mark, not a parse error). The
replacement
store opens at epoch + 1 with its row-ID allocator starting above the custodial
`allocatorHighWaterMark` (so no fresh operation reuses an imported record's id) and
`compactSeq` from zero, and
every name the custody file names stays closed — no new lifecycle or mutation
call past admission — until the operator resolves its quarantined state
explicitly through `orphan-resolve`, which verifies
the custodial boundary before clearing. The store starts otherwise empty with
zero outstanding tokens plus an operator-visible health signal naming the
quarantined file. The quarantine advances the durable `quarantineEpoch` by exactly one,
persisted outside the quarantined file. Cursor validation compares the cursor's pinned
store epoch against the live epoch first: a pre-quarantine cursor is a typed
`stale-entry` re-list refusal, never an admission against the replacement store. History
is loss-tolerable; bricking all hosts over bit-rot is not — but no host
reopens past a fence the quarantine can no longer prove.

State-transition sequence: every atomic store write that moves a record into a terminal
state (`complete`/`failed`/`interrupted`, or the `orphan-unverified`→`interrupted`
resolution) advances a durable monotonic per-store sequence, persisted in the store file
in the same write, and stamps the transitioned record with the value it advanced to.
The `plan` and deploy-step-(3) and `restart` race scans compare sequence values only.
`createdAt`/`updatedAt` never decide a race scan.

Retention and compaction: the store keeps at most 50 terminal records per host (tunable
owner knob; default ships in the implementing PR) plus every non-terminal record
regardless of count, within global bounds across all hosts: at most 500 terminal
records store-wide, at most 64 MiB of serialized store bytes, and at most 30 days
of terminal-record age (same owner-knob family; defaults ship in the implementing
PR). A write that would exceed a global bound first compacts oldest-terminal-first
across hosts until the new record fits; removed-host history compacts first once
its replay horizon expires (past the `tombstoneRetention` horizon — 7-day default —
and, where the registry tombstone already expired at 7 days, the historical (generation, incarnation id, presenceEpoch) boundary persists in the store's per-host boundary record until the host's last record compacts, so pagination never meets retained records with no boundary to validate against —
defined in the registry spec §15, the documented owner-visible horizon of the
lost-response retry contract — a removed host's
terminal records and tombstones compact before any live host's). Exceeding the cap compacts oldest-terminal-first in the same atomic
write that lands the new terminal state. Compaction leaves a bounded store dedup
tombstone per compacted record: the client operation ID with its (host, kind,
generation, incarnation id) scope plus the full replay fields (controller-assigned id,
bounded progress, `createdAt`/`updatedAt`, `hostRemoved`, terminal outcome and result,
`compactedAt`). At most 50 tombstones per host (same owner-knob family), oldest-first
past the bound. A replay naming a tombstoned ID returns the full retained record with
`compacted: true` instead of opening a fresh operation, but only while the tombstone's
pinned pair still equals the comparison pair for the name: the registry's current pair for a live host, the tombstone's own removed pair for a removed host (which has no live current pair). A tombstone pinned to
a superseded pair on a live host never replays; the clean-slate re-add rule wins over the tombstone.
Compaction never touches `host-removed` marks of retained records. Safe compaction
of removed-host history: once a removed host's records sit past the `tombstoneRetention`
horizon (registry spec §15) its terminal records compact (leaving the bounded tombstones
in §4, which
replay with `compacted: true` while their pinned pair still equals current) and its
older tombstones drop oldest-first. Only past that
horizon — the documented, owner-visible horizon of the lost-response retry
contract — does a replay open fresh. Every compacting write advances the durable
`compactSeq`, persisted in the store file. Pagination cursors pin it (§8, §10).

Store mutex and lock order: every operation-store read-modify-write path — token mint,
token validate-and-consume, record create and update, and any dedup lookup that leads to
a write — holds one store-wide mutex across the read and the atomic write (or runs
inside a single-writer transaction with the same span). Lock order is fixed: the
process-wide mutation lock is outermost, the store mutex innermost. A path holding the
mutation lock (for example `remove`'s token-row purge) may acquire the store mutex. No
path holding the store mutex ever acquires the mutation lock. Gate holders'
post-acquisition re-reads are lock-free registry reads.

Probe epochs are ephemeral non-listed rows: they carry no token, no worker, and no `deploy`/`restart` kind, never appear in `operations` reads, and boot reaps them silently (deleted, never transitioned to `interrupted`). Persisted-before-launch: no controller-authorized remote mutation precedes the durable controller-side record carrying its fencing epoch. A probe epoch is itself that durable record: it is written in its own atomic store write before the probe's remote write half, and it authorizes only that bounded probe mutation (the `running` read plus the crash-fencing takeover, bounded kill/wait, and guard advance). A consumed `deploy`/`restart` additionally persists its ownership record, the `pending` operation record, in the same atomic write that consumes the token, and `Ensure`/`restart` persist their record before launching. A crash between the probe epoch's persist and the probe's remote write leaves an epoch-only row that boot deletes silently: the remote was never touched and nothing was owned. A crash after the probe's remote write but before the consume deletes the row the same way: the remote guard names the probe epoch, no worker was ever launched for it, and the next operation's guard advance fences the abandoned epoch forward (its kill/wait finds an empty lease, so nothing is signaled). Only a consumed operation has an owner, so only a `pending`/`running` operation record transitions to `interrupted`; an epoch-only probe row never does. Crash recovery of records: at startup, before the store serves any request, every record
still in `pending`/`running` transitions to `interrupted` (a terminal unknown outcome)
with a note naming the crash. `orphan-unverified` is the one exception: a durable
per-record state resolved only through the fencing paths. A retry with the same
operation ID gets the `interrupted` record back. A new operation ID starts a fresh
operation, but only after local reaping completes and under a fresh fencing epoch with
the guard advanced past kill/wait of the superseded epoch. Boot performs no SSH. An
unreachable host cannot block startup. Remote fencing lands lazily at the next
operation's guard advance, after the store already serves `interrupted` records.

Host-removed pass: after `hub.toml` loads (the one-time legacy-sidecar migration
included) and after the interrupted transition, boot
applies every loaded tombstone. It marks that host's records `host-removed`, but only
records whose pinned (generation, incarnation id) pair matches the tombstone's persisted
(removed generation, removed incarnation id) pair. Generation-only matching would mark a
new live incarnation's records as removed. A tombstone colliding with a live re-add
carrying a different incarnation id matches no record. A crash between `remove`'s
`hub.toml` commit and its live mark recovers exactly the removed incarnation's records the
same way. `remove`'s `hub.toml` commit and operation-store mark are two separate durable
writes in different files; a crash between them leaves the mark to this reconciliation.
Tombstone contents and retention are defined in the registry spec §15.

Generation mirror: every `hub.toml` commit that also advances a mirrored store-side
generation writes a commit marker — the (name, `hub.toml` generation, store-mirror
generation) triple — into `hub.toml`'s atomic write. Boot runs bidirectional
reconciliation before serving any request. A store mirror newer than the `hub.toml` mark
for the same name with no matching `hub.toml` commit marker rolls back to the `hub.toml` mark
before any token or record validation. A `hub.toml` mark newer than the store mirror
pushes forward into the store mirror in the same boot pass. The rollback applies only
when `hub.toml` carries a live entry or tombstone for the name with a valid marker
triple to roll back to. When `hub.toml` is missing or held no entry for the name, the
store mirror is preserved, never rolled back, and the max-of-both restoration reads the
surviving mirror as the high-water mark. The discarded generation is preserved as the
name's high-water mark in the same atomic `hub.toml` write that performs the rollback, so
no later mutation reuses it. Boot recovers any record or dedup entry naming the discarded generation instead of refusing startup: records naming it transition to `interrupted` with a note naming the torn write (their generation was discarded, so their outcome is unknown), dedup tombstones naming it drop in the same write with same-key replays refusing `stale-entry` (pruned-generation value — §11), and the boot serves once the high-water mark lands. No torn-write split refuses startup. This section owns the mirrored-generation
boot rule; the registry spec cites it and states no independent maximum. Cursors are opaque client-held wire values, so boot
asserts nothing about cursor-pinned boundaries; a stale cursor is caught when presented
(§8). Records reach the store only through the atomic consume-and-create write, so no
crash window can consume a token without leaving a recoverable record.

## 5. Gates and serialization

Per-host gate: at most one deploy/restart per host at a time. The gate is shared with
`Ensure`-triggered deploys and supervisor activity, so an auto-deploy and a user deploy
cannot interleave. Acquisition is try-acquire. Nothing waits on a held gate. A new
`deploy`/`restart`, a `plan` (which always try-acquires after its ungated facts
refresh and before the running-state probe, holding the gate through the probe
window plus validation and mint), or an `update`/`remove` that finds the gate held
fails fast with the typed busy error naming the in-flight operation.

Holder classes: when a deploy/restart operation holds the gate, the busy error names
that operation (its operation id — open/wait-able), including an `Ensure`-triggered
deploy, which holds its own operation-store record. When `plan`'s validation-plus-mint
window holds the gate — which holds no operation-store record — the busy error is the
typed transient form (`host busy (plan in progress)`) carrying no operation reference.
The UI shows retry-with-backoff with no open/wait affordance for the transient form.

Gate and mutation-lock ordering: one order everywhere — the host gate first,
the mutation lock second, never the reverse. (The mutation lock is outermost only among the durable-write locks — mutation lock → store mutex per §4 — while the gate precedes all of them.) A mutation reserves its host's
gate (try-acquire, fail fast with the typed busy error if held) BEFORE taking
the process-wide mutation lock, and holds that reservation through the staged
commit; `update`/`remove` therefore never check the gate under the lock —
they arrive already holding the reservation. A gate is not grantable to a new
operation while the mutation lock is held, so no operation can start under a
mutation in flight: `plan`/`deploy`/`restart`/`Ensure` try-acquire only with
no mutation in flight, and a mutation that reserved a free gate owns it
through the commit. While a deploy/restart holds its host's gate, `update`
and `remove` of that host fail fast at the reservation step (and `add` cannot
collide — the held host exists, so its name refuses as a duplicate). The
gate pins the token-bound host entry and resolved target for the operation's lifetime.
Mutation rebind ordering: `update`/`remove` rebind or cancel the supervisors and
channels bound to the superseded entry as part of the staged commit, and the gate
releases last. No gate waiter can acquire a half-rebound host.

Post-acquisition re-read: an operation resolves and validates against the host entry
before acquiring the gate, and a mutation can legally commit in that window. Every gate
holder re-reads the registry entry immediately after acquisition and verifies its
hash and generation still match what it resolved. A mismatch is a typed `stale-entry`
refusal, never a proceed on the superseded entry. `deploy`'s token-binding check is
this rule with the token's bindings as the reference. `restart` refuses the same way.
`Ensure` re-resolves from the live registry.

## 6. Plan, deploy, restart

`plan` is a mutation: it mints durable controller state, so it is classified and
admitted as one. Params `{name}`. It builds a fresh plan from current facts and
configuration and returns the plan plus a confirmation token (§3), or the no-token shape
(§10).

`plan` runs its first network round-trip with no gate held, its second under the gate.
First the unconditional
facts refresh: a re-run of the same one-shot SSH preflight the deploy path uses,
deadline-bounded, run with no gate held. The refresh is an SSH command execution, not
an attach: it never initializes a channel, never starts or rebinds a supervisor, and
never enters the deploy/restart decision ladder. If the host is not attached, `plan`
returns no token with reason `unattached`: the UI must Connect first. If the host is
attached and the refresh fails or times out, `plan` returns no token with reason
`refresh-failed`: retryable diagnostics, never a Connect loop. A token is only ever
minted from fresh facts.

Second, `plan` try-acquires the host gate once (gate first per §5 — `plan` holds no mutation
lock, so no order inversion is possible), failing fast with the typed busy error
if held. Holding the gate but no durable epoch yet, `plan` persists a durable probe epoch first: a fencing-epoch-shaped (boot id, per-host op sequence) probe record in the operation-store file in its own atomic store write, bound to the host's current (generation, incarnation id) pair and superseded by the eventual token mint. The probe epoch exists before the first `running` call, so the remote write half is recoverable under crash-fencing's persisted-before-launch rule. The probe's remote write half then runs the complete fencing protocol for the fresh epoch — takeover, bounded kill/wait of the superseded epoch, guard advance — per the crash-fencing spec §4 before the write lands. A probe-epoch write failure refuses `probe-failed` with nothing launched; a crash after the epoch persists but before the mint leaves an epoch-only record that boot deletes silently (§4, §7) with no token and no worker (probe epochs never launch workers and never outlive their `plan` call — the mint supersedes them or the call's failure path deletes them). `plan` holds the gate through the running-state probe, validation, the durable
mint, publication, and return. Then the running-state probe: `evener/host/running` through
`sshManager.ChannelIfAttached(name)` over the live channel (never a dial, never
preflight), deadline-bounded with an explicit owner-adjustable probe timeout, presenting the persisted probe epoch (never a default, never absent — §10). The
probe's remote write half runs classified under the fencing epoch and gate (§10):
the probe call runs through the gate-aware probe primitive, which inherits the
already-held gate and presents the worker epoch instead of try-acquiring the
non-reentrant gate a second time. On
timeout `plan` returns the no-token `probe-failed` refusal with nothing left held: a
hung remote holds no gate past the probe window. The probe records the response's `buildRevision` as
`runningVersion` and its `healthy` flag as `runningHealthy`, both as `HostPlan` fields,
with the running revision bound into the token. A probed unverifiable revision (`dev`
or dirty) always reads as outdated: restart follows. No timestamp comparison exempts
it. If the probe read fails or is unauthenticated — including a remote whose hub
predates the handler (named distinctly as `handler-absent`; such remotes take the
one-time manual-upgrade migration path) — `plan` mints nothing and names the failure
with the discriminating `reason` field. The handshake version, ping liveness, and
preflight on-disk facts feed the decision ladder but never substitute for the probe.
None of them proves which build the live process runs.

Still holding the gate, `plan` re-checks attachment, the registry generation of the resolved entry,
and facts-freshness against the refreshed facts. It scans the operation store (local
read, no network) for any operation on this host that reached a terminal state since
the ungated refresh started, identified by the state-transition sequence (§4): the worker
records its pre-read sequence position before the gateless refresh, and any
terminal operation with a higher transition sequence is a typed `stale-entry` re-plan
refusal. A detach, mutation, facts advance, or completed operation landing between the
ungated refresh and acquisition is a typed refusal or a re-read, never a plan against the
superseded entry.

Every `plan` call publishes into the last-known store. This is seam (c), first half:
`status` reads the last-known store inputs (running-state and refusal snapshots)
without dialing, and `plan` publishes them. Every `plan` publishes under the gate before
returning, except the pre-acquisition no-token refusals (`unattached`,
`refresh-failed`, `remnant-open`), which
publish pair-scoped gateless and never acquire the gate to do so. The gated-probe
refusals (`probe-failed`, `handler-absent`) publish under the probe-window gate
before releasing it, matching the registry spec §10. A refusal publishes its `{terminal, message}` as the pair's plan-time refusal; a
later success clears it. Every completed probe — success or authenticated failure —
publishes the probed running revision and health plus the probed `processStartTime`
when carried. The snapshots are keyed by the host's (generation, incarnation id) pair,
so `status` after a plan refusal renders the refusal and the probed state instead of
stale data from an older pair. The store schema is defined in the registry spec §10.

`deploy` is a mutation. This is seam (b): `deploy` consumes the token plus the client
operation ID, dedup-first, with consume-and-create atomicity. Processing order is fixed.

(1) Dedup first: if the client operation ID matches an existing durable operation
record of the same host, kind, current host generation, and current incarnation id (a
`host-removed` record never matches),
return that record. No token validation, no consumption. A lost-response replay succeeds
without a fresh token. A client operation ID colliding with a current-generation record
of a different host or kind, or with a current-generation `host-removed` record, is
refused with `conflicting-operation-id`. A colliding ID whose only matches are retained
superseded-generation records of the same (host, kind) returns the newest retained
record, or refuses `stale-entry` when the request's intended pair is stale — per the
dedup rule in §4, never a silent fresh operation. Dedup-before-gate lets idempotent replays
succeed without acquiring contested gates or reviving fenced work. Guard-before-admission
outranks dedup-first: dedup-first applies only post-admission among handler stages.

(2) Provisionally validate the token — missing, mismatched, superseded, or expired
refuses — as a fail-fast readability check only. A concurrent `plan` can mint a newer
token and supersede the presented one before the gate is acquired. Nothing is decided
here. A fenced name never reaches the gate: an open teardown remnant refuses with typed
`remnant-open` (naming the `remnantId`) before any probe or acquisition, past the
step-(1) dedup check. Remnant semantics are defined in the registry spec §6.

(3) Persist the probe epoch, then probe under the gate, then revalidate under the same gate. On a fresh (non-dedup-hit) operation the worker persists a durable probe epoch first — a fencing-epoch-shaped (boot id, per-host op sequence) probe record in the same atomic store write posture as step (4), bound to the host's current (generation, incarnation id) pair and superseded by the step-(4) consume — before the first `running` probe, so no remote write probe precedes its durable controller-side epoch. The first probe's remote write half runs the complete fencing protocol — takeover, bounded kill/wait, guard advance per the crash-fencing spec §4 — before the write, so a crashed epoch's orphan is fenced before the probe mutates. A crash between the probe-epoch write and the consume leaves an epoch-only record that boot deletes silently (§4, §7), never an unrecoverable probe. Probe the running build and health
over the attached channel holding the try-acquired host gate for the probe window (same explicit probe timeout as `plan`'s
probe, presenting the persisted probe epoch — never a default, never absent; a held gate fails fast with the typed busy error before any probe write). Still holding
that gate, the worker
re-resolves the target, re-reads the host entry, re-hashes the `hub.toml` fingerprint at
execution time, and re-validates the probe result taken under the same gate. Reject on any drift
from the token's bindings. The probe result is not
re-probed a second time. Instead the worker closes the probe window with a
local scan: any operation on this host whose terminal transition sequence (§4) sits
above the sequence position recorded before the gated probe is a `stale-entry`
re-plan refusal. Token unconsumed, no record. A probe failure or timeout refuses with
the typed `probe-failed` envelope (data names the host and whether the read failed,
timed out, or was unauthenticated) — token unconsumed, no record. The worker rejects if
the probe differs from the token-bound running revision or running-health flag, and
rejects if the re-probed `processStartTime` differs from the token-bound one when the
token bound one. Both values come from the same remote clock across the two probes,
never from controller wall-clock. Deploy runs no fresh preflight of its own — the
on-gate re-read is the local resolution above plus the channel probe — so it
validates the token's stored `factsRevision`/`factsCapturedAt` rather than
recomputing the digest over new facts. The worker re-checks token-bound preflight-facts
freshness (`factsCapturedAt` against the token-bound bound): facts
older than the bound at deploy time are a `stale-entry` re-plan refusal. This re-read is
the gate protocol's post-acquisition check (§5).

(4) Revalidate and atomically consume the current nonce under the store mutex, still
holding the host's gate (gate-then-store-mutex, the same order as `plan`'s
validation-plus-mint window; deploy never holds the mutation lock, so the §5
gate-first order holds), as one atomic store write. Re-read the host's current
token row and compare-and-consume its nonce against the presented token, and re-check
`expiresAt` against the clock in that same transaction. An `expiresAt` at or before now
is a `token-expired` refusal with no consumption and no record, even when the nonce
still matches. A changed nonce is a `token-superseded` refusal with no consumption and
no record. On a match the same write deletes the token row and promotes the probe-epoch record to the pending
operation record carrying the worker's fencing epoch. Consume is delete in that same write, never a mark. Return its id.
The operation holds its host's gate from record creation to terminal state. The worker
re-hashes the on-disk `hub.toml` fingerprint immediately before each irreversible step
(before the push, and again before the planned restart when the token-bound plan says
one follows) and aborts with typed `stale-entry` on any drift from the token-bound
fingerprint. The gate alone does not pin the file against external hand edits: the
final fingerprint read is a check, not an atomic compare-and-swap with external
writers. An edit landing after the check but before the push/restart still drives
the action; the post-operation refresh (§6) detects the drift and the next
token validation refuses on the new fingerprint, so post-action drift converges
explicitly instead of silently standing.

Detached-during-deploy: if the channel is gone at the gated probe, or a
revalidation under the same gate finds the channel dropped, `deploy` refuses with typed
`host-detached`. Token unconsumed, no record. The UI Connects and re-plans. `deploy`
spends a single-use token, so it must not consume it to open a worker that then
attach-firsts into an unvalidated channel.

The RPC never blocks on the push. The RPC returns the record id once the atomic
consume-and-create write lands. The push runs on a worker under the controller-lifetime
context, never the RPC context. A client disconnect cannot cancel a persisted
pending/running operation. Deploy wraps the 04b deploy path with its guards intact and
surfaced verbatim: source and revision verification, terminal dirty-controller refusal,
push integrity, resolved-target persistence. When the token-bound plan says a restart
follows, the deploy worker performs that restart after the push (the same 04b restart
path `restart` wraps) and verifies it before marking `complete`. The planned restart
drops the channel exactly like a standalone `restart` and follows the same
operation-owned detach/restart/reattach sequence under the same gate.

`restart` is a mutation with the same operation model: (host, kind)-scoped operation-ID
dedup first (generation-scoped like `deploy`), then the remnant fence (`remnant-open`
naming the `remnantId`, past dedup and before any probe or acquisition), then busy-fail
gate acquisition, then atomic record creation. The request carries the intended (generation, incarnation id) pair (§10); a lost-response retry repeats the old pair and replays, while a reuse of the same operation ID for a new incarnation names the new pair and opens fresh. No token: restart has no install step, no
target, and no plan to re-verify. Instead restart binds the current `hub.toml`
content-hash fingerprint at resolution and re-checks it under the gate alongside the
post-acquisition entry re-read. A manual file edit between resolution and acquisition is
a typed `stale-entry` refusal with a retry instruction. A mutation landing between
resolution and gate acquisition is likewise a typed refusal. Under the gate `restart`
runs the same terminal-operation scan as deploy step (3). It wraps the 04b restart path
(user versus system unit decision, `waitHealthy` proven replacement). Immediately before
the irreversible restart — after the scan, still holding the gate, before signaling
or restarting — the worker re-reads the on-disk `hub.toml` fingerprint and compares
it against the bound fingerprint; any drift aborts with typed `stale-entry` and no
restart. The read is a check, not an atomic compare-and-swap with external writers:
an edit landing after it still drives the restart, and the post-operation refresh
detects the drift explicitly per the deploy rule above.

Restart drops the attached channel by construction, and no supervisor or `Ensure`
reattach can cover the worker: both need the gate the operation holds through terminal
verification. The reattach is operation-owned. This is seam (d): the
`attachUnderGate` consumption contract takes the already-held gate in and starts no
supervisor until the post-verification handoff. The worker retains its host's gate
across the drop and re-runs the attach dialing closure for the same generation-pinned
entry through the manager's internal gate-aware attach primitive — `attachUnderGate`,
shipped by the registry spec — which accepts the already-held gate instead of acquiring
it and suppresses supervisor startup. The worker owns the channel until terminal
verification. Then it runs the channel re-probe the post-operation refresh requires
over the reattached channel, then starts (or safely hands off to) a supervisor for the
channel under the still-held gate before releasing it. The suppress-supervisor scope
ends at verification, so the host keeps automatic reconnect after the restart.
Re-running the normal attach path while holding the gate is a deadlock: the gate is
non-reentrant. A restart issued while the host has no attached channel runs the same
operation-owned attach first under the already-held gate and names the attach-first
path in the record. Every attach entry point checks the remnant fence before dialing.

`Ensure`-triggered operations are durable fenced operations. The Ensure path mints a
server-side client operation ID, persists the operation-store record with its fencing
epoch (controller boot id plus per-host monotonic op sequence) under the gate before
launching the worker, and the worker runs the same register/fence/perform guard
advance. A crash mid-Ensure reaps and fences exactly like a user deploy. No Ensure
remote mutation precedes its persisted epoch and ownership record (§4,
persisted-before-launch). The Ensure path
checks the remnant fence before minting: an open remnant refuses the Ensure-triggered
operation with typed `remnant-open`. An Ensure-held gate returns `host-busy-operation`
with the Ensure record's id — open/wait-able. Epoch, lease, and guard semantics are
defined in the crash-fencing spec; this spec defines only the record, the gate, and
the fence check.

Worker lifetime: the push and `waitHealthy` work run on an async worker under a
controller-lifetime context, never the RPC context. The RPC context serves only
admission and the atomic consume-and-create record write. Only controller shutdown
cancels workers, transitioning the operation to `interrupted` (with a note naming the
shutdown) and releasing its host's gate.

Post-operation refresh: a deploy/restart worker that reaches terminal success first
performs a verified post-operation preflight (the same one-shot SSH preflight as
`plan`'s refresh, deadline-bounded) and re-probes the running build and health over
the attached channel. It publishes the refreshed facts to the (generation,
incarnation id)-scoped last-known store, keyed to the operation's pinned pair so a
concurrent mutation cannot misattribute them. This is seam (c), second half. Only then
does the worker mark the operation `complete`. The probe confirmation is what the
worker verifies before marking `complete`. A restart worker additionally confirms the
probe reports the post-restart build. A deploy worker whose plan said a restart follows
confirms the planned restart ran and the probe reports the new version.
Process-instance verification after restart is same-clock only: the post-operation
probe's `processStartTime` must differ from the pre-restart value the pre-operation
probe returned. Both come from the same remote clock. Under a verifiable revision the
worker additionally requires the post-restart build to equal the deployed revision.
Under an unverifiable revision (`dev` or dirty) the changed `processStartTime` alone is
the success signal. A worker that cannot verify the refresh or the probe — including an
unchanged or absent post-restart `processStartTime` — records the failure verbatim in
the operation record instead of marking clean success. Only the live channel probe on
the same remote clock proves the new process replaced the old one. The deploy and
restart worker's post-operation preflight is channel-free like `plan`'s refresh: no
channel initialization, no supervisor, no attach state machine.

`operations` is a read: list and detail of controller-side operations (§10). It never
dials. Polling this method is the guaranteed read path for operation progress. A
controller-originated host-notification event class, where extended, is best-effort
only. The UI renders progress through terminal state. Never a synchronous RPC.

## 7. Boot recovery of pipeline state

The boot pass runs in this order, before the store serves any request: operation-store
load plus the safety-critical local reap of its local orphan boundary first (defined in the
crash-fencing spec §3), then `hub.toml` load (defined in the registry spec §6, the one-time legacy-sidecar
migration included), then the
interrupted transition, then the tombstone-derived
`host-removed` pass, then bidirectional generation-mirror reconciliation, then the
cross-file intent reconciliation (§9). A corrupt `hub.toml` is still a hard startup error,
but only after the operation store's local reap has run: a valid operation store is
never left unreaped because an unrelated `hub.toml` failed validation. Boot performs no SSH. An unreachable host cannot
block startup. Remote fencing lands lazily at the next operation's guard advance, after
the store already serves `interrupted` records.

Interrupted transition: every record still in `pending`/`running` transitions to
`interrupted` with a note naming the crash. `orphan-unverified` is the one exception. An epoch-only probe record (persisted probe epoch with no token consumed and no worker launched — §6) is an ephemeral non-listed row: boot deletes it silently, never transitions it to `interrupted`, and never revives a token or a worker; a lost-response `plan` retry after the crash re-plans fresh under a new probe epoch.
A retry with the same operation ID gets the `interrupted` record back. A new operation
ID starts fresh, but only after local reaping completes and under a fresh fencing epoch
with the guard advanced past kill/wait of the superseded epoch.

Token reaping: any token past its TTL at boot is dropped, never revived. Any token
whose host resolves at boot to removed (tombstoned) is dropped with it. The tombstone
reconciliation runs before token revival is even considered. No pre-restart token
survives for a host that no longer exists. After a quarantine of the unvalidatable
store file, the store starts with zero outstanding tokens. Otherwise only unexpired,
binding-intact tokens for live hosts remain valid past the restart.

Generation-mirror reconciliation runs before serving any request, per §4: roll back a
store mirror newer than the `hub.toml` mark with no matching commit marker; push forward a
`hub.toml` mark newer than the store mirror; preserve the mirror when `hub.toml` carries
no entry for the name; record the discarded number as the high-water mark; recover records or dedup entries naming the discarded generation per §4 (interrupted transition, never a startup refusal).

## 8. Operations pagination and cursors

Ordering: records sort and resume by the controller-assigned `id` ascending. One
ordering for both. The `id` is unique and monotonic per store, so the order is total.
`createdAt`/`updatedAt` are stored UTC-normalized (`Z`-suffixed RFC3339; a stored offset
form converts at write time) and stay display-only. A wall-clock rollback that stamps a
later record with an earlier `createdAt` can never move it before the cursor, because
`createdAt` is not part of the order.

Cursor envelope: the cursor is a versioned base64url JSON envelope `{v: 2, pos: id,
compactSeq: number,
bounds: {[host]: [generation, incarnationId, presenceEpoch] | "absent"},
quarantineEpoch: number}`. It encodes the last row's durable sequence position
(`pos`, the controller-assigned `id`, which never rolls back — never a bare
offset), plus the global compaction position (`compactSeq`, carried once in the
envelope — never per-host, never part of any per-host boundary comparison), plus
a snapshot and retention boundary per host (`bounds`), plus the
quarantine epoch (`quarantineEpoch`, the §4 counter). A cursor whose envelope version is not 2 is a typed `stale-entry` re-list
refusal, never a best-effort decode. A continuation whose pinned epoch no longer equals
the live `quarantineEpoch` is a typed `stale-entry` re-list refusal before any boundary
comparison. Sort and resume are the monotonic `id`; no timestamp is part of the resume
position.

Boundaries: a host-pinned response carries the effective `generation` and
`incarnationId` actually listed. Host-pinned callers pass both values back with
`cursor` for subsequent pages. The handler validates each host's `bounds`
entry — its `[generation, incarnationId, presenceEpoch]` tuple, or
the `"absent"` marker — against the request's filters; a mismatch is a typed
`stale-entry` re-list refusal, never a mixed page. The global `compactSeq` is
compared once against the envelope value, never per-host: an unrelated host's
compaction never trips a per-host boundary mismatch. An omitted filter on a later page reads as the
pinned-cursor window, never as a fresh unpinned query. A generation or presence
advance between pages rejects the continuation: later pages never serve the
pinned incarnation past a boundary change; a newer generation's records appear
only on a fresh unpinned read. A generation-pinned page requires `name`: a cursor
minted for one pair validated against an unfiltered query is a typed `stale-entry`
re-list refusal. The map pins every host in the query at cursor creation, not just the
hosts on the page: a host with no records on the page still contributes its current
(generation, incarnation id, `presenceEpoch`) boundary, or its absent
marker when the host holds no records at all — and, where the registry tombstone expired while records remain (§4), the store's persisted historical (generation, incarnation id, presenceEpoch) boundary triple, which validates like a live boundary until the host's last record compacts. The absent marker encodes as the literal
string `"absent"`. `presenceEpoch` is the per-host removal/presence counter defined in
the registry spec §1 glossary (advanced on every add, remove, re-add, and
expiry purge); a removal advances it even when generation and incarnation are
preserved. A host created after the cursor was minted has no stored triple to validate:
later pages skip its records. A newer host's records appear only on a fresh unpinned
read, never mid-pagination. A host removed after mint trips the stored-triple mismatch
rule the same way.

Refusals: a later page whose stored `bounds` entry no longer matches the host's
current boundary is a typed `stale-entry` re-list refusal, never a mixed page.
A compaction that removed rows at or before the cursor's `pos` since the cursor
was minted surfaces a typed `cursor-invalidated` refusal naming the compacting
`compactSeq` (the envelope-global value) plus the affected host's `bounds` entry
(`[generation, incarnationId, presenceEpoch]` as stored at mint, or `"absent"`);
the client restarts from the first page. A host-pinned page names the single
listed host's entry; an unfiltered cross-host page names the compacted host's
entry. A first page whose
boundary map would exceed the 8 KiB encoded cap refuses with typed `cursor-too-large`
(data carries `{capBytes: 8192}`), never a truncated cursor. No cursor was minted, so
there is no `compactSeq` and no stored `bounds` entry to name. With the host-count
cap withdrawn (registry spec §4; component 03 §Scope), the map is bounded only by
the operator's host list, so `cursor-too-large` is reachable from a
sufficiently large configured host set: incarnation ids are server-generated and
36 bytes by construction (§1), so the encoded cursor grows only with host count
and never with id length.
`limit` defaults to 50 and caps at 200. Responses never exceed the cap. `limit` with no
`cursor` starts the pinned first page. An unfiltered call pages instead of returning
the whole store.

## 9. Cross-file commit intents

Token rows live in the operation-store file while tombstones and mutation receipts live
in `hub.toml`. No shared mutex makes `hub.toml` and the operation-store file atomic. `remove`'s staged commit (the
token-row purge plus the tombstone write) and any token consume or delete that must
coincide with a `hub.toml` commit run a durable two-phase intent. Two-phase intents
converge `hub.toml` and store without a cross-file atomic write.

`pendingStoreSync`: `hub.toml`'s atomic write carries the intent (the exact store rows
to delete or invalidate plus the `hub.toml` generation the intent belongs to). The store
write applies it. A follow-up `hub.toml` atomic write clears the intent. Token deletion
lands only after the `hub.toml` commit's swap succeeds: the store purge runs as the
post-swap step, never before it. A failure before the purge with the swap already
landed leaves `hub.toml` new and the store old: boot re-applies the intent's
purge, then clears the intent in its follow-up `hub.toml` write — the §9 boot
reconciliation below owns this ordering, never a no-op. A failure before the swap
leaves both files old with nothing to compensate. A post-swap failure after the purge (still before
the commit point) compensates the store purge alongside the `hub.toml` restore: the purged
rows are re-inserted alongside the stash restore, so compensation resurrects exactly
the tokens its own `hub.toml` restore revalidates, and the compensated mutation leaves old
`hub.toml` bytes beside old store rows. Boot and live recovery cover every ordering
of the four steps (swap, purge, intent-clear, compensation): swap-landed/purge-missing
re-applies the purge; purge-landed/intent-present converges to the committed
`hub.toml`'s view and clears; compensation open follows the `pendingCompensation`
phase arms; converged (rows gone, intent cleared) clears the stale intent with no
further write.

`pendingCompensation`: the committer first persists a record holding the rows about to
be purged (plus the stash reference and the `hub.toml` generation the purge belongs to, in
phase `compensating-armed`) into the operation-store file — which the stash restore
cannot touch, so a `hub.toml` restore that overwrites the restorable `hub.toml` bytes leaves
this record intact — in its own store-local write before the purge write. Boot resumes
the rollback plus stash cleanup from this record (never from bytes inside the restorable
`hub.toml`): a record still open means the swap compensation has not converged, and boot
re-runs it by phase per the reconciliation arms below. The purge write
advances the record past `armed`. The preimage persists before the purge, never after
it. Past the commit point the committer clears the armed record in its own store write:
the purge stands. Only on the failure path does the restorer run. The record carries
`phase` (`compensating-armed` → `compensating-hubtoml` → `compensating-rows` →
`compensating-runtime` → `compensating-clear`) plus the stash reference the `hub.toml` restore must apply. The middle literal was renamed from `compensating-sidecar` when the storage decision retired the sidecar; a persisted record may still carry the old spelling, so boot treats `compensating-sidecar` as an alias for `compensating-hubtoml` (same arm, same restore), never as an unknown phase. The
restorer advances the phase in its own store write per step: `hub.toml` restore first
(stash bytes back, phase to `compensating-rows`), then the row re-insert (phase to
`compensating-runtime`), then the runtime revert (phase to
`compensating-clear`), then the record clear. The marker plus its stash
reference persist until the runtime revert succeeds: a record in
`compensating-runtime` re-applies the restored `hub.toml`'s runtime set first,
then clears. A runtime-revert failure leaves the record in
`compensating-runtime` with the stash reference intact, never a cleared
compensation beside a diverged runtime.

Boot reconciles a live compensation record by phase, never by blind re-insert. A record
still in `compensating-armed` checks the purge first: a `hub.toml` whose
`pendingStoreSync` intent is already cleared means the commit path passed the commit
point, so boot clears without resurrecting rows; intent still present with the rows
still present means the purge never landed, so boot restores `hub.toml` from the
stash, leaves the rows untouched, and clears; intent still present with the rows absent
means the purge landed before the crash, so boot advances to `compensating-hubtoml`
and follows that arm. A record in `compensating-hubtoml` restores `hub.toml` from the
referenced stash before touching any rows. A record in `compensating-rows` verifies the
restored `hub.toml` is in place, then re-inserts exactly the rows the restored `hub.toml`'s
generation revalidates, then advances to `compensating-runtime`. A record in
`compensating-runtime` re-applies the restored `hub.toml`'s runtime set to the
live handles first, then clears. A record in `compensating-clear` clears without resurrecting
rows: the rows are already converged. A crash at any point of compensation still
converges to the restored `hub.toml`'s view with its tokens intact. The compensation
record survives `hub.toml` restoration by construction, and the restored `hub.toml`
carries no `pendingStoreSync` intent for the generic rules below to misread.

Boot reconciles both directions before serving. An intent whose store rows are still
present is re-applied: the `hub.toml` committed, the store lagged. Store rows with no
covering intent whose `hub.toml` generation already advanced past them are dropped: the
store committed, `hub.toml`'s compensation already restored. Once `hub.toml` and store
agree (the intent's store rows are already gone, including the store-already-applied
no-op where a re-applied intent finds nothing to delete), the boot pass clears the
stale intent in its own follow-up `hub.toml` atomic write. No converged intent survives
its boot. A crash between the files converges to exactly the committed `hub.toml`'s view,
never a restored `hub.toml` beside a committed revocation. Compensation resurrects only
the tokens its own `hub.toml` restore revalidates.

## 10. Protocol types

Every new method gets its AppWire protocol catalog entry (`appwire/protocol.go`,
`ScopeHub`) plus request/response structs (`appwire/types.go`) plus the regenerated
TypeScript client. Hand-written structs for pipeline-owned methods land in the same PR as the handlers. Public
catalog registration plus the regenerated client land in the pipeline PR with the union-registration
generator work, with the union catalog/protocol-shape tests. LowerCamel JSON throughout. Optional facts and error fields stay
absent — never null — when unknown (the absent-when-unknown rule).

Generator mapping: the AppWire TypeScript generator emits Go structs as interfaces and
named string types as plain `string`, with no discriminated-union or literal-union
emission. The literal string unions below are wire value sets carried in the generated
client as `string`, with the exact value set pinned by the protocol-shapes test. The
response unions below are carried as per-arm interfaces selected at runtime by their
discriminator (`outcome`, `reason`): the pipeline catalog
defines one named Go struct per arm so each generates its own interface. The pipeline PR extends the
generator with explicit union support: the catalog declares one named Go struct per arm
plus a named union registration referencing every arm, and the generator emits each arm
as its own interface with the method's `MethodTypes` result entry typed as the union
over the arm names. The pipeline protocol-shapes test pins every arm field-for-field,
including each arm's discriminator.

`orphan-resolve` and `BoundaryEntry` belong to the fencing spec. This section never
restates them. The `orphanBoundary` field below cites the fencing spec §9 for its
element type.

- `evener/host/plan`: params `{name: string}`; response is either `{plan: HostPlan,
  token: string, outcome: "planned"}` or `{outcome: "no-token", staleFacts: {message:
  string, attached: bool, reason: "unattached" | "refresh-failed" | "probe-failed" |
  "handler-absent" | "remnant-open" | "controller-dirty" | "target-unwritable" |
  "target-missing-prereq" | "target-unit-findings"}, terminal: bool, remnantId?:
  string}`. `terminal` is `true` exactly on the `controller-dirty` /
  `target-unwritable` / `target-missing-prereq` / `target-unit-findings` arms and
  `false` on the five retry arms. `remnantId` is present exactly on the
  `remnant-open` arm (absent on every other no-token arm per the absent-when-unknown
  rule). `outcome: "no-token"` is the top-level discriminator on the no-token arm; the
  token arm carries `outcome: "planned"` alongside `plan`/`token`. The `reason`
  discriminates the failure: `unattached` (host not attached — Connect first),
  `refresh-failed` (attached, but the ungated preflight refresh failed or timed
  out, or the refreshed facts' freshness bound elapsed before mint — both
  re-plan from fresh facts),
  `probe-failed` (attached, but the `evener/host/running` probe read failed or was
  unauthenticated), `handler-absent` (attached, but the remote predates the handler —
  take the one-time migration path), `remnant-open` (an open teardown remnant fences
  the name — resume it through `teardown-retry` first), `controller-dirty` (the
  controller is dirty — rebuild from a clean tree), `target-unwritable` (the resolved
  deploy target is not writable), `target-missing-prereq` (a deploy prerequisite is
  missing on the target), `target-unit-findings` (the 04b unit decision reports
  findings blocking deploy). `HostPlan` is `{host, generation, targetPath,
  controllerRevision, restartFollows, factsRevision, hubTomlFingerprint,
  factsCapturedAt: string (RFC3339), factsAgeSec: number, runningVersion: string,
  runningHealthy: bool, runningProcessStartTime?: string (RFC3339)}` —
  `runningProcessStartTime` present exactly when the probe carried it. `plan`/`token`
  are absent — never null — on the no-token response.
- `evener/host/running` (controller-side mutation — catalog entry plus TypeScript client
  with the handler): params `{fencingEpoch: {bootId: string, opSeq:
  number}}` (required on the wire; the `plan`/`deploy` probe path always
  presents the caller's fencing epoch — the persisted epoch the calling worker
  minted before launch — and the generated client carries the field, so no
  well-formed client call omits it); response `{buildRevision: string, healthy: bool,
  processStartTime?: string (RFC3339)}` — `processStartTime` present exactly when the
  serving hub knows its own process start time. An unverifiable revision (`"dev"` or a
  dirty `"<sha>-dirty"`) never proves currency by revision equality. Served locally by
  every hub: `buildRevision` from the same source as the `controllerBuild` plan input,
  `healthy` the hub's own health, computed authoritatively as follows (this paragraph
  is the definition — no other signal counts): the serving hub reports `healthy: true`
  exactly when all three hold — (1) its local liveness check passes (the handler runs
  on the live request path, so reaching it proves it); (2) no restart-required
  condition is outstanding under the serving hub's dedicated local health predicate
  (evaluated from its local controller roster directly, never through the
  `restartRequiredDaemon` authenticated-probe path); (3) the hub's durable state roots
  are writable and not critically full: the predicate first runs a free-space query
  against the state root and reports `healthy: false` when free space falls below the
  owner-set minimum-free-space knob (default ships in the implementing PR); only above
  that threshold does it run the state-root write probe — a real atomic temp-plus-rename
  probe inside the state dir with a uniquely named temp per probe, rename to a distinct
  probe target in the same dir, fsync the dir, then remove. The probe is a fully
  fenced mutating step running the complete fencing protocol (takeover, bounded kill/wait, guard advance per the crash-fencing spec §4) before its write half, never a read and never a gateless bypass: the calling
  side issues it only while holding the host gate through the gate-aware probe
  primitive in §6 (which inherits the already-held gate instead of re-acquiring
  it), presenting the worker's persisted fencing epoch; the serving hub persists
  the presented epoch per calling host before the write half runs, validates it
  against the host's current fencing epoch, and refuses stale epochs without
  probing. It never runs gateless and its epoch never defaults. A call with the
  epoch absent is refused with typed `probe-failed` (no epoch presented), never
  served as an unfenced write; a
  probe temp orphaned by a crash carries the probe-name prefix and
  boot prunes prefix-matching strays before serving. No probe temp survives the probe
  window past its remove except a crash orphan the boot prune owns. The orphan
  fence gates it like every other mutating call: it never bypasses an open
  `orphan-unverified` record or quarantine — only the read-only calls
  (`list`, `status`, `operations`) plus the `orphan-resolve` way out bypass the
  fence (crash-fencing spec §8). Anything else — session counts,
  load, peer reachability, external dependency status — never feeds `healthy`.
  `healthy: false` is data, never a probe failure: each forced-false case returns
  `healthy: false` while the probe itself still succeeds. Admitted only over an
  attached session peered by the #1603 handshake (mutation classification; never forwarded
  onward to a third hub; browser-origin and forwarded requests refused exactly like
  every other `evener/host/*` request). The `plan` probe calls it through
  `sshManager.ChannelIfAttached(name)`; its response fields are what `plan` records as
  `HostPlan.runningVersion` / `runningHealthy`.
- `evener/host/deploy`: params `{name: string, token: string, operationId: string}`
  (client operation ID: opaque, non-empty, at most 128 bytes); response `{id: string,
  clientOperationId: string, state: OperationState}` (`id` is the controller-assigned
  record id). The freshly created record reports `"pending"`. A dedup hit returns the
  existing record's actual state.
- `evener/host/restart`: params `{name: string, operationId: string, generation: number, incarnationId: string}` (the intended pair: a retry repeats the old pair and replays the retained record; a reuse for a new incarnation names the new pair and opens fresh — a pair older than current refuses `stale-entry`, per §4); response `{id:
  string, clientOperationId: string, state: OperationState}` (fresh create:
  `"pending"`; dedup hit: the existing record's state, as for `deploy`).
- `evener/host/operations`: params `{name?: string, operationId?: string, state?:
  OperationState, generation?: number, incarnationId?: string, id?: string, limit?:
  number, cursor?: string}` — `operationId` matches the client-supplied
  `clientOperationId` (never the controller-assigned `id`); `generation` selects the
  incarnation after client operation-ID reuse (omitted: the current generation);
  `incarnationId` narrows that selection to the exact incarnation — required alongside
  `generation` whenever the caller names a superseded pair; `id` is the detail filter
  for the controller-assigned record id (the busy payload's open/wait-able reference
  resolves through it directly); response `{operations: OperationRecord[],
  generation?: number, incarnationId?: string, hostBoundaries?: {[host: string]:
  {generation: number, incarnationId: string, presenceEpoch:
  number} | "absent"}, nextCursor?: string}` — `generation`/`incarnationId` are
  present exactly on host-pinned pages (the single host named by the request) and
  absent on unfiltered cross-host pages, where `hostBoundaries` is authoritative
  instead (one boundary per every host in the query at cursor creation; `"absent"` encodes only hosts with no records in the query — hosts whose records land on later pages carry their current (generation, incarnationId, presenceEpoch) triple, never by omission).
  `OperationRecord` is `{id, clientOperationId, host, generation: number,
  incarnationId: string, kind: "deploy" | "restart", state: "pending" | "running" |
  "complete" | "failed" | "interrupted" | "orphan-unverified", orphanBoundary?:
  BoundaryEntry[], progress: ProgressEntry[], result?: {ok: bool, message: string},
  createdAt: string, updatedAt: string, hostRemoved: bool, compacted?: true,
  orphanResolved?: true,
  attestation?: {operator: string, statement: "orphan-verified-absent",
  recordId: string, boundaryRef: string, observedAt: string}}` —
  `incarnationId` is the pinned incarnation the record ran against; `orphanBoundary`
  is present exactly on records whose `state` is `orphan-unverified` (absent on every
  other state per the absent-when-unknown rule) — its element type is defined in the
  fencing spec §9; `orphanResolved?: true` is present exactly on records resolved
  through `orphan-resolve` (absent on every other record including ordinary
  boot-transitioned `interrupted` records, per the absent-when-unknown rule — the
  marker is defined in the fencing spec §5 and cited here, never restated);
  `attestation` is present exactly on a record resolved through `orphan-resolve`
  with an attestation, carrying the persisted `{operator, statement, recordId,
  boundaryRef, observedAt}` (fencing spec §§5, 9), and absent on every other
  record;
  `compacted` is present as `true` exactly on tombstone replays
  (absent on live records). `ProgressEntry` is `{ts: string (RFC3339), message:
  string}`, bounded per record. `limit` defaults to 50 and caps at 200; responses
  never exceed the cap. An unfiltered call pages instead of returning the whole
  store. Empty params list the first unfiltered page (cross-host, no generation pin —
  `hostBoundaries` returned).

## 11. Error discriminators this spec's paths emit

Typed errors ride the existing AppWire error envelope: the numeric `code`
(`appwire/errors.go`: `CodeInvalidParams` -32602, `CodeConflict` -32013,
`CodeUnavailable` -32014, `CodeInternalError` -32603, `CodeInvalidRequest` -32600)
with the stable discriminator in `data.evenerErrorInfo` plus the error-specific data
fields. The regenerated TypeScript client branches on the `evenerErrorInfo`
discriminator (with data fields), never on the numeric `code` alone. The catalog pins
the exact numeric code per discriminator. The protocol-shapes test asserts the pair.

This spec's paths emit:

- `token-missing` / `token-mismatched` / `token-superseded` / `token-expired`
  (conflict class). A consumed-token replay presents a deleted row and reads as
  `token-missing`. No separate consumed discriminator exists.
- `conflicting-operation-id` (conflict class). Same-key replay is a hit; cross-host,
  cross-kind, or current-generation `host-removed` collision is this refusal.
- `stale-entry` (conflict class) with the values this spec's paths emit: `entry` (the
  resolved host entry drifted), `target` (the resolved deploy target drifted),
  `generation` (the registry generation advanced), `hub.toml`-fingerprint (a hand edit
  landed between validation points), `running-version` (deploy's re-probed running
  build differs from the token-bound revision — §6 step 3), `running-health` (the
  re-probed health flag differs the same way), `facts-age` (the token-bound preflight
  facts aged past the token-bound freshness bound at deploy time — the re-plan
  refusal), `concurrent-terminal-op` (the post-acquisition scan found an operation on
  this host with a terminal transition sequence above the pre-probe position), `pruned-generation` (the request's intended pair names a pruned generation: emitted only on the §4 operation-store dedup path when a same-key replay finds only superseded-generation records and the intended pair is older than current — the operation-store counterpart of the registry receipt path's `pruned-generation` value, defined in the registry spec §5). Data
  names which value fired.
- `host-busy-operation` (busy class; data names the operation id) when a
  deploy/restart record — including an Ensure-triggered deploy — holds the gate.
- `host-busy-transient` (busy class; no operation reference) for `plan`'s
  validation-plus-mint window, which holds no operation-store record, and for an
  open `orphan-unverified` fence's non-teardown refusals (crash-fencing spec §8;
  `teardown-retry`/`teardown-recover` carry `orphan-fenced-busy` instead).
- `host-detached` (unavailable class). Deploy's channel-gone refusal. Token
  unconsumed, no record. The UI Connects and re-plans.
- `probe-failed` (unavailable class). Deploy step (3)'s running re-probe read failed,
  timed out, or was unauthenticated. Data names the host plus which of the three.
  Token unconsumed, no record. Distinct from `plan`'s no-token `reason:
  "probe-failed"` union-arm value, which is never an envelope.
- `cursor-invalidated` (conflict class). The mid-pagination compaction refusal. Data
  names the compacting `compactSeq` (envelope-global) plus the affected host's `bounds`
  entry as stored at mint (`[generation, incarnationId, presenceEpoch]`, or
  `"absent"`). Distinct from `stale-entry`'s generation-mismatch re-list refusal.
- `cursor-too-large` (conflict class). The over-cap first-page refusal. Data carries
  `{capBytes: 8192}`, never a compacting `compactSeq`.

Not emitted here, cited only: `concurrent-edit` belongs to the `hub.toml` final check
(defined in the registry spec §6). `fencing-failure`, `fencing-helper-absent`,
`fencing-helper-untrusted`, and `orphan-fenced-busy` belong to the fencing spec. The
`orphanBoundary` element shapes belong to the fencing spec §9.

`remnant-open` (conflict class): emitted by `deploy`/`restart`/`Ensure`
(§6 step 2) past the dedup check and before any probe or acquisition. The
conflict class is pinned in the registry spec §11; the code-per-discriminator
pair is pinned here. Data carries `{remnantId: string}` naming the blocking
remnant, mirroring the `plan` no-token `remnant-open` arm's `remnantId`
field-for-field.

`committed-with-teardown-failure` is not an error-envelope code. It is the
mutation-result union's failure arm (a normal result response, defined in the registry
spec). `interrupted` is a terminal record state (outcome unknown), not a thrown error.

## 12. Testing

- Handler tests per method: validation, admission, classification including
  `plan`-as-mutation, and remote-origin rejection for `plan`/`deploy`/
  `restart`/`operations`/`running` (the fencing mutation's
  guard-before-admission ordering is asserted in the fencing spec where
  `orphan-resolve` registers — guard-before-admission outranks dedup-first; the
  before-dedup and before-token-validation orderings are asserted where they ship).
- The token matrix: missing, mismatched, expired, superseded, consumed-then-replayed
  with a new operation ID (pins to `token-missing`: consume deletes the row — §6 step
  4), expiry-across-the-wait (a token valid at the step-(2) provisional pass but past
  its TTL at the step-(4) consume is a `token-expired` refusal with no record),
  clock-rollback (a `now` behind the high-water mark by more than the 30-second
  tolerance drops only corrupt rows whose own timestamps postdate the mark; a pre-rollback
  token keeps its minted real-time lifetime and expires only on elapsed TTL, never by
  comparison against the later mark — and a token already expired before the rollback
  stays expired past it; post-rollback captures anchor at `max(now,
  mark)`; a within-tolerance step invalidates nothing; facts-age
  checks and `expiresAt` comparisons run against `max(now, mark)` while the rollback
  is active), and
  supersede-between-validate-and-consume (a `plan` mint landing after step (2) but
  before step (4) is a `token-superseded` refusal with no record).
- Generation-bound tokens: a token minted under generation N refuses after
  remove/re-add even for a byte-identical entry. Live remove drops outstanding
  tokens. Re-add starts with none.
- Token persistence: mint is a durable store write held under the gate. Expiry reaps
  lazily and at boot. Tombstoned hosts' tokens drop.
- Post-operation refresh: a completed deploy/restart publishes verified fresh facts
  to the (generation, incarnation id)-scoped last-known store. `status` reports the
  new version.
- Busy-holder classes: an operation-held gate names the operation, including an
  Ensure-triggered deploy, which holds its own record. A plan-held gate returns the
  transient form with no operation reference. The UI shows retry, not open/wait.
- Protocol shapes: catalog entries and the regenerated client match §10
  field-for-field — including the union-handler catalog entries, the `teardown-unknown-key` entry, the four-arm mutation-result union with the `ambiguous` arm, and the `teardown-retry`/`teardown-recover` request/response shapes (moved from the registry spec — registry spec §2; the registry PR ships no catalog entry for a union-shaped response), plus the `running` mutation's required
  `{fencingEpoch: {bootId, opSeq}}` params, `incarnationId` on `OperationRecord` and the
  `operations` request/response/cursor, `compacted: true` exactly on tombstone
  replays, every `stale-entry` data value against its emitting path, the
  `cursor-invalidated` plus `cursor-too-large` catalog entries with their data
  shapes, and the `hostBoundaries` `{...} | "absent"` value union.
- Operations incarnation scope: a `generation` plus `incarnationId` filter pair
  addresses the colliding same-generation incarnation. The response echoes the listed
  pair. A cursor minted under one pair never lists the other.
- Unconditional plan refresh: an attached `plan` refreshes even when the known facts
  are fresh. The mint's facts are never older than the refresh it just ran. The
  5-minute token TTL is the deploy window.
- Ensure busy names its operation: an Ensure-held gate returns
  `host-busy-operation` with the Ensure record's id — open/wait-able. The transient
  form fires only for `plan`'s validation-plus-mint window.
- Cursor-invalidated: a mid-pagination compaction past the cursor refuses typed
  `cursor-invalidated` with the compacting `compactSeq`. The client restarts from
  page one. An over-cap first page refuses the distinct `cursor-too-large`
  discriminator with `{capBytes: 8192}`. Both shapes pinned.
- Durable probe epoch: no `running` probe precedes its durable controller-side epoch — `plan` persists its probe epoch before the first probe call and binds it to the eventual token's (generation, incarnation id) pair; `deploy` persists its probe epoch before probing and promotes it at the step-(4) consume. Probe-epoch write failure is `probe-failed` with nothing launched; a crash between epoch and mint/consume boots to silent deletion of the epoch-only row with no token and no worker (probe epochs are ephemeral non-listed rows, never `operations`-visible); the mint supersedes a `plan` probe epoch and step (4) promotes a `deploy` one.
- Fenced probe ordering: the first mutating probe after a crashed epoch runs takeover, bounded kill/wait, and guard advance before its write half (crash-fencing spec §4); the ordering test observes the write landing only after the guard advanced past the superseded epoch.
- `factsRevision` freshness: a token whose preflight facts are older than the token-bound bound at deploy time refuses `stale-entry`; deploy runs no fresh preflight, so the check is on the stored `factsRevision`/`factsCapturedAt`, not a re-read, and the mint-time digest remains the pinned reference for the facts the token was minted from.
- `restart` incarnation pair: a lost-response retry repeating the old pair replays the retained record; the same operation ID naming the new pair after remove/re-add opens fresh; a pair older than current refuses `stale-entry`.
- Torn-write recovery: a store-newer/`hub.toml`-older split with no commit marker transitions affected records to `interrupted` and serves; startup is never refused for this split.
- The running probe (`evener/host/running` handler): local revision plus health plus
  optional `processStartTime`; mutation classification with the required
  `{fencingEpoch: {bootId, opSeq}}` params (the generated client carries the
  field — an epoch-absent call refuses `probe-failed`, never served); attached-session admission only; unauthenticated probe
  refusal; browser and forwarded requests refused; never forwarded onward (no A→B→A
  chain); orphan-fenced like every other mutating call (no read bypass — an open
  `orphan-unverified` record or quarantine refuses it past admission); the serving
  hub persists the presented epoch before the write half and refuses stale epochs
  without probing; the gated `plan` probe call with its explicit deadline (timeout yields
  the no-token `probe-failed` refusal with nothing left held past the probe window); `HostPlan`
  `runningVersion` / `runningHealthy` placement; `handler-absent` named for
  pre-handler remotes with the one-time manual-upgrade migration path; the no-token
  `reason` discriminates all nine values with `terminal: true` exactly on the four
  terminal arms and the UI branching on it; an unverifiable probed revision always
  reads as outdated (restart follows).
- Restart reattach: the worker retains the gate across the channel drop, reattaches
  through `attachUnderGate` for the pinned entry (never the normal attach path, no
  supervisor start until the post-verification handoff), and re-probes over the
  reattached channel. An initially unattached restart attach-firsts under the same
  gate and names it in the record. A reconnect-after-restart test pins that the
  supervisor owns the channel again once the gate releases.
- The ungated refresh: an attached host refreshes via the bounded one-shot SSH
  preflight (no channel initialization, no supervisor, no attach state machine, no
  gate held), then acquires the gate, re-checks, and mints. An unattached host gets
  the no-token refusal naming Connect. The deploy/restart worker's post-operation
  preflight is channel-free under the same pin.
- Execution-time re-resolution mismatch including the `hub.toml` fingerprint: a
  manual edit between plan and deploy refuses; a manual edit between restart's
  resolution and its gate acquisition refuses; a manual edit after the under-gate
  check refuses at the final pre-restart fingerprint re-read with no restart.
  Post-acquisition entry re-read:
  `restart` and Ensure-triggered work refuse or re-resolve when a mutation lands
  between resolution and gate acquisition.
- (Host, kind, generation, incarnation id)-scoped operation-ID dedup including the
  interrupted-record path; the superseded-generation path (a same-key replay whose
  only matches are retained superseded-generation records returns the newest retained
  record, or refuses `stale-entry` on a stale intended pair — never a silent fresh
  operation); the compacted-ID tombstone path (replay returns the
  tombstoned terminal result, never a fresh operation); the clean-slate re-add path
  (operation-ID reuse after remove/re-add opens fresh); the conflicting-reuse
  refusals (same ID on a different host, deploy-versus-restart, and a
  current-generation `host-removed` record — IDs used up only by a removed
  incarnation stay cleanly reusable).
- Per-host gate serialization versus an in-flight `Ensure` deploy.
- Worker lifetime: a client disconnect after record creation leaves the operation
  running. Controller shutdown records `interrupted` and releases the gate.
- The busy refusals (`update`/`remove`/`plan`/new-deploy on a held gate) and the
  atomic consume-and-create write (a replayed deploy after a crash in that window
  finds a record, never a silently consumed token).
- Startup reconciliation of pending/running to `interrupted` plus the
  tombstone-derived `host-removed` pass (a crash between a remove's `hub.toml` commit
  and its live mark still never-matches at boot).
- The corrupt operation-store boot quarantine (store quarantined aside; boot serves
  empty with zero outstanding tokens plus the operator-visible health signal;
  custody-intent persisted before the rename plus the custody-file schema pinned
  field-for-field (original record ids, allocator high-water mark, fences,
  ownership); every custody fence imported as an
  `orphan-unverified` record under its original id resolvable through `orphan-resolve`;
  ownership-only entries imported with the `boundary-unavailable` entry and
  attested resolve only; a crash between custody write and rename, or an aside
  file with incomplete custody, fails closed; truncated and
  corrupt stores covered end-to-end — including the fail-startup posture when
  custody is incomplete).
- The cross-file intent (a crash between the `hub.toml` commit and the store sync
  converges to the committed `hub.toml`'s view in both directions; the store purge lands
  only after swap success; swap-failure compensation re-inserts exactly the purged
  token rows its `hub.toml` restore revalidates).
- Plan publish: every `plan` refusal and every completed probe lands in the
  (generation, incarnation id)-scoped running-state/refusal snapshot. `status` after
  a plan refusal renders the refusal, never stale data.
- The running-health definition: each forced-false condition returns `healthy: false`
  as data while the probe itself succeeds — evaluated by the serving hub's local
  predicate, never the `restartRequiredDaemon` probe path; the free-space floor is the
  owner-set minimum-free-space knob, and the write-probe half runs classified under the
  host gate and fencing epoch (§10), never as a gateless bypass.
- Pinned pagination: stable `id`-ascending order for both sort and resume across
  concurrent terminal writes (`createdAt` display-only); a mid-pagination
  generation or presence advance rejects the continuation (`stale-entry`
  re-list); cursor carries the `v: 2` envelope `{v, pos, compactSeq, bounds,
  quarantineEpoch}` per §8 (`pos` the last row's controller-assigned `id`;
  `compactSeq` once globally; `bounds` one `[generation, incarnationId,
  presenceEpoch]` tuple per host in the query, or `"absent"`) with entry-mismatch
  (`stale-entry` re-list) and post-cursor compaction (`cursor-invalidated` naming
  the envelope-global compacting `compactSeq` plus the affected host's stored
  `bounds` entry) both surfaced as refusals;
  host-pinned pages carry the top-level pair while unfiltered cross-host pages
  omit it (`hostBoundaries` authoritative — the shapes test pins the absence);
  the cursor pins every host in the query at creation, including absent ones
  (absent encodes as the literal `"absent"` string); a host advancing
  generations between pages is a `stale-entry` re-list refusal; a host removed
  after mint (presence epoch advanced, tuple preserved) is a `stale-entry`
  re-list refusal the same way; a host created after mint is skipped on later
  pages; the versioned base64url envelope is capped at 8 KiB encoded (over-cap
  first page refuses `cursor-too-large` with `{capBytes: 8192}`, pinned as its
  own discriminator).
- The not-in-forwarded-allow-list assertion.
- Live E2E (strongly recommended, never yet exercised): one add → connect → deploy →
  restart → spawn-remote cycle against a disposable host before declaring the
  component done. That requires a host Jesse designates.

Orphan fencing, fencing-quarantine, helper-gate, and `orphan-resolve` tests belong to
the fencing spec. This spec's tests pin only their seams: the `remnant-open` fence
before any probe or acquisition, the `orphanBoundary` wire presence rule (§10), and
the `orphan-unverified` state surviving the interrupted transition.

## 13. UI (Hosts settings section) and acceptance criteria

The Hosts settings section already shipped — `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx` (one file, not `hosts/*`), its settings section-map registration, and the host-manager store `stores/hosts.ts`, carrying the add/edit/connect/remove rows and dialogs (#1784, and the edit slice #2111). This spec owns only the new UI added to that section: the Deploy/Restart actions, the plan confirmation, the `operations` polling, and the host-ops store carrying them, landing in the pipeline
PR with the regenerated client they consume (registry spec §2). The section
contract (rows, dialogs, Connect state machine, confirmation sourcing, stores)
is stated in the registry spec §13 and cited here, never restated.

1. Deploy from the UI: the confirmation renders the controller-minted plan
   (controller revision plus resolved remote target path) and cannot proceed without
   a fresh token. An edit between plan and deploy is rejected and re-planned —
   including a manual `hub.toml` edit, caught by the fingerprint. An edit attempted
   while the deploy runs is refused with a busy error and the push is unaffected.
   After confirm, the host reports the new version in `status` via the worker's
   required post-operation facts refresh (published to the (generation, incarnation
   id)-scoped last-known store before the operation marks `complete`), and the
   version-skew signal (facts versus controller build) is truthful. A replayed
   deploy with the same operation ID returns the same record. A controller crash
   mid-deploy surfaces as `interrupted`, never a stuck operation.
2. No lazy-attachment regression: with no attach call, no explicit source, no read
   dials anything — `list`/`status`/`operations` never attach (test-pinned) — and
   `plan`'s facts refresh and the deploy/restart worker's post-operation preflight,
   the surface's two deliberate non-attach SSH uses, are channel-free by
   construction (no initialize, no supervisor, no attach state machine;
   test-pinned).
3. Standard gates green on every PR (go/build/vet, package races, full hub suite,
   module-lint).

## 14. Open questions

1. **Who may add hosts** — reuse the settings-mutation admission as-is, or a
   distinct grant? This spec assumes as-is for the pipeline handlers (`plan`,
   `deploy`, `restart` ride the existing admin-mutation admission; no new
   capability type).
2. **`ServerInfo.Version` constant** — owner follow-up from #1603 r2; does not
   block this component (version display reads preflight facts, and the running
   probe reads the remote's real build).

The sidecar-versus-`hub.toml` rewrite question is decided (registry spec §19:
in-place `hub.toml` rewrite, machine-managed, comments not preserved); tombstone
retention period and per-host detach
belong to the sibling specs and are not reopened here.
