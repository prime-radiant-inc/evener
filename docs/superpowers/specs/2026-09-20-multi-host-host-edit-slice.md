# Component 08 slice 2 — host editing and the host schema surface

Status: design, pre-implementation. Awaiting adversarial review, then the
implementation plan.

This slice implements the edit half of component 08 — the registry spec,
`2026-09-16-multi-host-08-host-management-ui.md` (open on PR #1622, head
`56989a29d7`) — against the code slice 1 merged (PR #1784, `d66d970556`), and
records what it defers to the pipeline slice.

## 1. Purpose

Today a host can be added, connected and removed from Settings → Hosts, but not
changed. `HostAddParams` carries name, address and key path
(`appwire/types.go`); `evener/host/update` does not exist; `HostRow` carries
neither the remaining entry fields nor the versions a row needs to be edited
from. The Add dialog offers three of the seven `HostConfig` fields.

An operator who mistypes an address, moves a host's `hub.toml`, changes the SSH
user, sets an `evener_path`, or adds a root must remove and re-add the host.
That is not the same thing: removal drops the entry, its retained rows and its
attach record, and (with the generation fences slice 1 shipped) mints a new
identity. This slice makes editing the supported path.

## 2. The contract this slice implements

The registry spec defines update (§4) and the UI (§13). This slice implements:

- §4 — `evener/host/update` is a mutation; the target must be a **live sidecar
  entry**; a `hub.toml`-declared name is refused with the "edit the file"
  explanation; a name absent from both files, or present solely as a tombstone,
  is refused as not found; every field except `name` is mutable; `name` is
  immutable — it keys source IDs, cached rows, manager state and file entries;
  **every update advances the registry generation**, and the staged update
  rebinds before it clears or re-keys name-keyed resolved state, so the retiring
  identity is fenced out first; commit first, then rebind or tear down, gate
  released last.
- §13 — the Add/Edit dialog covers **all seven** `HostConfig` fields, none
  invented and none hidden, each with its validation message mapped from the
  backend's refusal; Edit does not offer `name`; the row renders the host's
  installed version beside the controller's.

It is *reduced* against some of §4's rules, by decision: §6 lists every
deviation and what the pipeline slice inherits.

## 3. What this slice adds

### 3.1 Wire (`appwire/types.go`, then the regenerated client and protocol doc)

- `HostAddParams` becomes `{entry}` and `HostUpdateParams` becomes
  `{name, entry}` — the design record's own shape (§11), so the pipeline slice
  adds its guard fields to a wire this slice already matches instead of
  reshaping it a second time. This revises slice 1's flat
  `{name, address, keyPath}` params, and the regenerated client and the pane's
  store follow.
- `entry` carries the record's six mutable `HostConfig` fields under slice 1's
  wire spellings — `address` (schema `ssh`), `user`, `evenerPath` (schema
  `evener_path`), `configPath` (schema `config_path`), `addr`, `roots` — plus
  the one field slice 1 added that the schema does not have: `keyPath`.
  `keyPath` is **not** a `HostConfig` field: `config.go`'s schema has no key,
  `hostreg`'s own comment says hub.toml has no key field, and the record's
  update entry is "the six non-name fields". Slice 1 invented the wire field
  because the dial needs one, and this slice keeps it and round-trips it —
  dropping it would zero a stored key path on every edit, which the
  no-regression criterion forbids. §6 records the extension.
- `add`'s entry also carries `name`, which is required there and absent from
  the update entry because the update's target name is the request's own
  field.
- The dialog carries the record's seven inputs plus the Key path control slice
  1 shipped — `name`, `address` (the schema's `ssh`), `user`, `keyPath`,
  `evenerPath`, `configPath`, `addr`, `roots` — eight on **add**, seven on
  **edit**: per §13 the edit dialog offers no `name` input, and the host being
  edited is named in the dialog's title instead. The dialog's inputs and a
  refusal's `field` use these wire spellings, so mapping a refusal to an input
  is the same string on both sides (§5), and the schema spelling appears only
  where it is the schema's.
- `HostUpdateParams.name` is the immutable target: it identifies which host to
  edit, and nothing in the request can rename one.
- `HostUpdateResponse` is `{host: HostRow}`, mirroring remove's shape: the
  updated row, which the dialog's caller re-reads like every other mutation's
  result. §11's mutation-result union arrives with the guards that give it its
  arms (§6).
- `HostRow` grows to the effective entry *plus* the live state it already
  carries, so the dialog prefills from a row and `list`/`status` stay the one
  source of truth for what a host currently is. The optionals stay absent when
  unknown, as slice 1 renders them.

### 3.2 `hostreg.Registry.Update(entry Host) error` (`internal/hostreg`)

- Normalizes (`hostreg.Normalize`) and validates with the same rules
  `AddWithUpstreams` runs (`validateEntry`, the name grammar, the reserved
  name, the cycle check), preserving the name's existing upstream edges.
- It runs **no host-count cap**, and neither does anything else on this path:
  the registry has no count check, config load has none, and the only 64-source
  limit in the tree today lives in the navigation projection. The record's §4
  and §14 do assign the cap to the registry's `Add`/`Update` (over-cap failing
  `ErrTooManyHosts`), §16 requires its test, and the design document's
  follow-up ledger carried it as its [03] item — now **withdrawn by decision**
  (Jesse, 2026-09-26: design §2 "Host-count cap: withdrawn"; component 03
  §Scope). This slice records the drop instead of pretending to satisfy it, and
  states the consequence plainly: a live set pushed past 63 sources fails
  navigation for the whole hub, not just the extra host.
- Replaces the entry in place and assigns a fresh generation from the
  registry-wide counter. That generation is the identity fence: `SameRegistration`
  and the manager's channel fence read it, so a parked `Ensure` that captured the
  pre-update entry refuses once the update lands.
- It does **not** reach the derived stores by itself. The remote-thread cache
  owns its own per-source generations, assigned by `RemoteThreadCache.RegisterSource`
  from the cache's own counter, and the last-good retention is keyed by source
  id, not by any generation. A registry update therefore leaves both exactly as
  they were; §3.4 handles them explicitly instead of relying on the new
  generation.
- Refusals reuse the existing sentinels (`ErrUnknownHost`, `ErrInvalidName`,
  `ErrReservedName`, `ErrMissingSSH`, `ErrAmbiguousSSHUser`, `ErrEmptyRoot`,
  `ErrHostCycle`).

### 3.3 `sshconn.Manager.UpdateHost(entry hostreg.Host, onRetire func(retired hostreg.Host)) error`

- One hold of the per-host gate, in a fixed order: **validate** the entry
  (`hostreg.ValidateEntry`, the side-effect-free half of the pass the registry's
  own `Update` runs), then **tear the retired identity's channel down** through
  the same teardown body `RemoveHost` and `DetachHost` already share (stop the
  supervisor, drop the channel, clear the per-host caches), then **commit the
  registry `Update`**, with the gate released last. The teardown must precede
  the swap: the gate-free row build behind `host/list` and `host/status` takes
  no host gate, snapshots the registry, and then resolves the channel **by name
  alone**, so a swap that became visible first would let a row pair the new
  entry with the still-mapped retired channel, read that channel's
  handshake/facts, and record them under the new generation — above the
  generation fence the swap itself advances — so the new identity would render
  the retired host's server facts until the next attach. Unmapping the channel
  before the new generation is visible makes that pairing impossible; the
  reverse window (the old entry seen with no channel) only makes the row render
  offline, which the retire leaves clean. Validation likewise precedes the
  teardown: the caller's contract is that an error means nothing live changed,
  so a refused entry returns before the channel is touched.
- `onRetire`, when non-nil, runs **inside that same gate hold**, after the
  registry swap and the retired identity's teardown (its `Detached` included)
  and immediately before the gate is released — never when the call refuses.
  The placement is the contract: every lifecycle event is delivered
  synchronously with the gate held, so a concurrent attach can start only for
  the new generation, after the gate is free; a caller's per-identity state
  retired from the hook therefore cannot have a new-generation event interleave
  between the swap and the retirement and be erased by it. The hook receives
  **the entry the swap actually retired** — the pre-swap capture, its
  `Generation` and all its pre-edit fields — so a caller can retire per-identity
  state **by generation rather than by timing**: it can mark every row built
  from that identity (or an earlier one) as retired, which is what makes the
  retirement immune to a late write from a row that captured the old entry
  before the swap but only reaches its state write after the hook. The hook must
  not call back into `Manager` (the gate is non-reentrant) and must not take a
  lock another goroutine may hold while parked on this gate.
- **Every update tears the channel down**, not only the edits that change a
  field the dial reads. The reason is the fence, not the dial: a supervisor
  captures the entry it supervises when it starts, and `reconnectOnce` refuses
  to publish a replacement when `SameRegistration` no longer matches that
  capture — so an update that advanced the generation while leaving the channel
  up would leave its supervisor unable to reconnect it, and the host would go
  dark on the next link drop with nothing to bring it back. Retiring the
  channel with the identity keeps one rule instead of a rebind path. The record
  allows either ("rebinds them to the new entry (or tears them down)"); §6
  records that this slice always tears down.
- A parked `Ensure` that captured the pre-edit entry fails its
  `SameRegistration` recheck and refuses; a parked update and a parked remove
  cannot both win, because they take the same gate. Slice 1's fences supply
  those outcomes; this slice adds no new fence.
- A nil registry is an error, mirroring `AddHost` and `RemoveHost`.

### 3.4 The hub's update flow (`cmd/evener-hub/app_host_manage.go`)

Mirrors `Remove`'s three phases, which is what keeps the durable-first order
and the compensation paths in one shape:

- **Commit, under the mutation mutex.** Refuse a name with a mutation already
  in flight — the existing in-flight mark, generalised from removals to any
  mutation on the name. Require a live sidecar entry: a `hub.toml` name gets
  the edit-the-file refusal, a name that is gone or tombstone-only gets not
  found. **Validate the entry here, before anything is written** — the same
  `hostreg.ValidateEntry` call the add flow runs for exactly this reason — so a
  refusal commits nothing and an entry the registry would reject never reaches
  the file. Then persist durable-first with one atomic sidecar write that
  replaces the entry **in place**, through a store-level replace: the file
  keeps the order it already had, so an edit is a minimal change to it rather
  than a reordering nothing asked for. (The rendered list is name-sorted
  regardless — this is about the file's own order, not the list's.) A failure whose
  rename already committed compensates back to the live contents exactly as add
  and remove do. Replace the store row in the same critical section, set the
  mark, release the mutex.
- **Live, mutex-free, in this order.** Call `manager.UpdateHost(entry, onRetire)`
  when a manager is wired, handing it an `onRetire` that **retires the name's
  attach record by the generation the swap replaced** — `state.retire(name,
  retired.Generation)`, the hook's argument being the entry the swap actually
  retired; otherwise call the registry's own `Update` and retire inline after a
  successful swap using the pre-swap entry's generation read immediately before
  it, mirroring how remove falls back when no manager is wired (tests,
  embedders) and safe because with no manager no lifecycle event can exist (but
  a stale row can still be racing the clear, so the mark is what fences it).
  The registry re-runs the same validation under its own lock; the commit
  phase's check is what keeps an invalid or unnormalized entry out of the file,
  and is not a substitute for it. The manager's swap is atomic under the host
  gate — the registry entry is replaced and the channel retired, or neither —
  so an error from it means nothing live changed, and the hook never runs on a
  refusal, so the still-live identity's record survives one. §4 requires the
  name-keyed resolved state to be cleared wholesale on every update, and the
  record is exactly that: the retiring identity's last-known facts and attach
  error, which an edit otherwise leaves lying about an entry that no longer
  exists (most visibly on an offline host whose address changed, where no
  channel exists to tear down).

  The retirement is **generation-scoped**, which is what makes the clear immune
  to a pre-swap row. The row build runs without the mutation mutex (round-7 M4,
  so a parked facts read holds up no commit), so a row can pass
  `hostEntryCurrent` before the swap and only reach its `apply`/`recordKnown`
  after the hook has run. The retire marks the name's record with the retired
  entry's generation and keeps that mark (`retiredThrough`, monotonic — the max
  of the existing and the new generation); `apply` and `recordKnown` are fenced
  on it, so a row whose entry generation is at or below the mark folds nothing
  and writes nothing, while a row for the new, higher generation is served and
  records normally. A bare delete could not hold: the stale row's late
  `recordKnown` would recreate the record from the retired identity, and the
  edited host's later offline rows would render the retired facts and attach
  error as their own. The mark is what defeats that late write, and it also
  leaves the post-swap new-identity lifecycle event (delivered only after the
  hook cleared the record) free to record the new identity's own state.

  Dropping a record wholesale — `remove` — remains the clean-slate path
  (Remove, Add's clean re-add, the two vanished arms): it deletes the record
  and its mark together, so a re-add starts clean.
- **Finish, under the mutex.** Clear the mark. On success:
  - when `roots` changed, retire both old identities first — the remote-thread
    cache's source entry and the source registration — then clear the last-good
    retention, and only then re-register the source, which assigns a fresh cache
    generation. The order is load-bearing: retiring the identities first makes
    an in-flight old-source walk fail its cache-generation fence or its
    source-instance ownership check, so it cannot re-store obsolete rows after
    the retention clear. The cache drop still precedes re-registration, so it
    cannot delete the generation the registration just minted.
  - when `roots` did not change, leave both alone: it is the same source, its
    cache generation still owns its rows, and the retained list is still this
    host's. An edit that changes only the SSH address or a path must not blank
    the host's sessions in the tree.
  On failure, roll the sidecar back to the live set — one atomic write of the
  live set as it stands now, not a pre-commit copy: a concurrent add or removal
  that committed in this window must survive — which puts the file and the
  in-memory sidecar store back on the same entries in the same order, clear the
  mark, and return the error. A live-phase refusal happens before the swap and
  leaves the retiring identity's attach record intact. When the live set no
  longer holds the name — only a directly driven registry can drop it while the
  live phase runs, because the mark fences this manager's own paths — the
  un-commit is a **removal**: drop the committed store row before taking the
  snapshot, so the rollback writes the live set without the name and the file
  does not keep an edit for a name that is not live, which a later add would
  duplicate and the next sidecar load would reject.
  If the live phase instead succeeded and a directly driven registry drops the
  entry before the finish-phase reread, the name is likewise removed: drop the
  committed store row and write that live snapshot, then retire the derived
  state exactly as `Remove`'s finish phase does and in that same order — the
  source registration, the name-keyed attach record, the remote-thread cache
  entry, and the retained last-known-good list. The order is load-bearing for
  the same reason the roots case gives: an in-flight walk must fail its
  cache-generation sweep or its source-ownership check, so it cannot re-store
  obsolete rows after the cache drop. Removing the row alone would leave the
  source, the cache and the retention rendering sessions for a name the live
  registry no longer has.
  The caller may retry, because nothing is half-applied: the entry, the file,
  the store row and the live registry agree on the old entry, the new one, or
  its absence.

### 3.5 Frontend

- `stores/hosts.ts`: an `update(params)` call that issues `evener/host/update`
  and then the existing quiet re-read (`reReadAfterMutation`); `add` takes the
  same entry. Errors keep remove's posture — thrown to the caller, shown by the
  dialog, never silently swallowed.
- One `HostEntryDialog` serves Add and Edit: the record's seven fields plus
  slice 1's Key path control, on edit without the `name` input (the host's name
  is in the dialog's title, per §13), submit disabled while in flight, and each
  field's message placed inline (§5).
- Row actions: **Edit** for sidecar, non-removed rows. `hub.toml` rows keep the
  read-only explanation the pane's help text already gives; removed rows keep
  their removed posture.
- **No skew marker in this slice.** §13 wants the row to show the host's
  installed build beside the controller's, and this slice cannot honestly build
  it: the row's `hubVersion` is `buildinfo.Version()` *of the controller*, by
  design — the attach ladder redeploys the controller's build on a mismatch, so
  a successful attach means the host runs the controller's build, and the facts
  say so deliberately. Comparing that with the controller's own health version
  compares one string with itself. The record puts the verified post-operation
  facts refresh in the pipeline slice, which is where a truthful build signal
  belongs; §6 and §8 record the deferral rather than pinning a marker that
  cannot fire.
- `hostRowEqual` gains the new fields. The publish guard skips its `setState`
  when every row compares equal, and the comparator's field list is slice 1's:
  without extending it, an edit that changes only `user`, `evenerPath`,
  `configPath`, `addr` or `roots` is swallowed by the post-mutation quiet
  re-read and the pane keeps rendering the old row.

### 3.6 The generation-aware attached-client lookup (`cmd/evener-hub/app_host_manage.go`)

`hostRow` snapshots an entry and then resolves the host's attached client and
live facts; the lookup pairs the channel with the entry the row is rendering,
not merely with the name. The hub-side `attachedClient` helper reads the
manager's `ChannelIfAttached` once — one lookup, one generation, the
`remoteHostFactsForChannel` idiom at `main.go` — and hands back the channel's
client only while the registration the channel was published for is the entry
being rendered — `sshconn.Channel.MatchesRegistration` compares that
publish-time registration through `hostreg.SameRegistration`, content and
generation both, the same captured-registration predicate
`hostreg.Registry.SameRegistration` implements
and `hostEntryCurrent` applies to the retained-state fold, compared between the
two captures rather than through the registry, so a swap cannot slip between a
read of the entry and a read of the channel and let a mismatched pair pass. A
mismatch renders the row offline; with no manager wired (tests, embedders)
there is no channel registration to compare, and the row falls back to the
name-only `RemoteHostClientIfAttached` seam whose callers own their pairing.

The update path's fences close every window where a mismatch could *persist* —
the channel is unmapped before the swap becomes visible, and a name with a
mutation in flight folds no retained state — but one window remained: a row
that snapshots the pre-swap entry can still resolve the channel a later attach
published for the new generation, so without the pairing that one render would
pair the old configuration with the new channel's attached state and server
facts, and the reverse pairing — a new entry reading a still-mapped retired
channel — was equally possible. Nothing persisted (the generation fence still
refuses the retained-state write, and the next poll rebuilds the row), which is
why the review that raised it — one of three reviewers, describing the finding
as a race window rather than a reproducible defect — did not hold the slice,
but the render was wrong. The guard closes it: `hostRow` reaches its live
lookups outside the mutation mutex, so it now checks the channel's registration
against the entry it holds before adopting the channel. Pinned by
`TestHostRowPairsTheChannelWithTheEntryItRenders` (a matching registration is
still adopted; an advanced generation or changed content is not) and
`TestHostRowAcrossTheUpdateWindowDoesNotAdoptTheNewGenerationsChannel` (the
pre-swap-entry row renders offline while the new generation's channel is
installed, and the new identity's own row still adopts it).

## 4. Semantics, precisely

- Update targets a live sidecar entry. `name` is immutable. The entry's
  effective values are what the row, the source and the attach path read: there
  is no second copy to drift.
- **The attached-edit rule.** Any edit to a host that is attached lands, drops
  the channel, and leaves the row offline with Connect available: the identity
  changed, so the channel goes with it. This is simpler than the dial-relevant
  split it replaces, and §3.3 gives the reason that split was unsafe — a
  supervisor's capture is generation-fenced, so a channel kept across an update
  could not be reconnected by the supervisor that owns it. §7 asserts the drop
  and the re-attach for an attached host and for an offline one.
- An edit never dials, never deploys, never attaches and never starts a
  session. Its live effects are exactly: the registry replacement, the
  conditional teardown, and — only when `roots` changed — the drop of the
  host's derived rows followed by the source re-registration.
- `evener/host/attach` is unchanged by this slice, exactly as §4 states: an
  edit that leaves the host offline is connected again with the same Connect
  affordance, not with a new attach path.
- At most one mutation per name: a name mid-mutation refuses add, remove and
  update with the same Conflict a removal in flight produces today.

## 5. Error handling

- Validation refusals gain a field-carrying shape so a dialog can place them
  without parsing prose: the error data names the `field` beside the message,
  and it names the field with the **wire** spelling the dialog's input is
  named for, not the schema's — so a mapping never sends the client looking
  for an input that does not exist. `ErrMissingSSH` → `address`;
  `ErrAmbiguousSSHUser` → `user` (the field that made the destination
  ambiguous, and the message says so); `ErrEmptyRoot` → `roots`;
  `ErrInvalidName` and `ErrReservedName` → `name` (add only); `ErrHostCycle` →
  the entry as a whole. Anything else is a form-level message.
- Refusals commit nothing. A `hub.toml` name, an unknown or tombstone-only
  name, a validation refusal and a failed durable write all leave the file and
  the live set exactly as they were.
- A failed live phase rolls the sidecar back to the live snapshot and returns
  the error.

## 6. Deviations from the trio, and what the pipeline slice inherits

Each of these is a decision, not an omission; the pipeline slice owns the
reversal.

1. **No guards.** `mutationId`, `expectedGeneration`, `expectedIncarnationId`
   and the receipt store are deferred. They need the operation store the
   pipeline slice owns, and slice 1 shipped add and remove without them, so
   update matches its siblings rather than leading them.
2. **No busy refusal.** Update waits for the per-host gate, as add and remove
   do today. §4's typed busy refusal arrives with the guards.
3. **`status` keeps its single-row response**, and this slice ships no build
   signal on the row: `controllerBuild` and the plan inputs stay with the
   pipeline slice, and the row's `hubVersion` is the controller's own build by
   construction, so a marker built from it would compare a string with itself
   (§3.5, §6.11).
4. **Refusal classes follow slice 1's pattern** — `InvalidParams` with the host
   named — rather than §12's final discriminator vocabulary. The field-carrying
   validation refusal is the seed the pipeline slice aligns to §12.
5. **No host-count cap.** The registry has no count check, config load has
   none, and this slice adds none; the only 64-source limit in the tree today
   is the navigation projection's. The record assigns the cap to the registry's
   `Add`/`Update` (§4, §14, with §16's test) and the design document's
   follow-up ledger tracked it as its [03] item, now withdrawn by decision
   (Jesse, 2026-09-26). This slice records the drop and its consequence — a live
   set pushed past 63 sources fails navigation for the whole hub — rather than
   claiming a check it does not run.
6. **The handler, catalog row and regenerated client land here.** §2's table
   places them in the pipeline PR, but slice 1 already registered
   `add`/`remove`/`list`/`status`, and a mutation whose method is not routed is
   not a feature. This slice continues slice 1's practice.
7. **The response is `{host: HostRow}`**, not §11's mutation-result union: the
   union's arms are the receipt and dedup outcomes the guards own, so it
   arrives with them.
8. **The params adopt §11's nested `entry`** and revise slice 1's flat add
   params, so the guards can be added later without a second wire change.
9. **`keyPath` is a slice-1 extension, not a schema field.** The record's
   `HostConfig` has no key field and §13's dialog is its seven inputs; slice 1
   shipped the wire field and the Key path control because the dial needs one.
   This slice keeps both, round-trips the field, and records the extension here
   instead of hiding a schema field or dropping the control.
10. **Every update tears the channel down** (§3.3). The record allows either
    rebinding live resources to the new entry or tearing them down; this slice
    always tears down, because a supervisor's capture is generation-fenced and
    a channel kept across an update could not be reconnected by the supervisor
    that owns it.
11. **No build signal on the row** (§3.5). The value slice 1's row carries is
    the controller's own build, so the installed-versus-controller comparison
    §13 implies cannot be built from it. It belongs with the pipeline slice's
    verified post-operation facts refresh.
12. **The wire says `address` where the schema and the record's dialog say
    `ssh`.** Slice 1 shipped that spelling, and the refusal mapping follows it,
    so the dialog's inputs and the refusals agree with each other; the record's
    §13 names the field after the config schema instead. The label is
    load-bearing on this side — an input and a refusal have to match — so
    aligning it with the record later is a rename across the wire, the
    generated client and the pane, not a shape change.
13. **The reshaped `HostAddParams` keeps `ProtocolVersion` v5** (§3.1), even
    though it changes the wire shape from flat fields to a nested `entry`. The
    constant is compared exactly at the handshake so a mixed pair of released
    binaries fails once, loudly, at initialize — the flagday rule the constant's
    own comment in `appwire/types.go` states — and a shape change under a shared
    version is exactly what that rule forbids. No bump is needed here because v5
    was never shipped to customers, so there is no mixed pair of released
    binaries to protect; a spoke/released pair that had to agree would need one.

## 7. Tests and acceptance criteria

Every item is pinned by a test in this slice's PR.

1. Each mutable field round-trips: edit it, and `list`/`status` returns the new
   value, the row prefills the dialog, and the pane repaints — the row
   comparator carries the new fields, so the change is not swallowed by the
   publish guard.
2. The sidecar is the durable record: after an edit and a sidecar reload, the
   edited values are the effective entry, and no `hub.toml` entry was written.
3. `name` cannot change: the update request carries `name` only as the target
   it addresses, the edit dialog offers no name input (it names the host in its
   title), and no mutable field can rename a host.
4. A `hub.toml`-declared name is refused with the edit-the-file message;
   nothing changes on disk or live.
5. An unknown name is refused as not found and nothing is written. (The
   tombstone-only arm of §4's rule is not constructible yet: slice 1's removal
   deletes the entry outright and tombstones arrive with the remnant
   machinery, so this slice pins the arm it can build and leaves that one to
   the slice that introduces it.)
6. Every update advances the generation — two successive updates give strictly
   increasing generations — and a captured pre-edit entry stops matching
   `SameRegistration`.
7. An attached host edited: the edit lands, the channel drops, the row reads
   offline, and a following Connect attaches the edited entry. Pinned with the
   gate-contention technique slice 1's tests use, not a sleep.
8. Every edit drops the channel, attached or offline. For an attached host the
   row reads offline with Connect available and a following Connect re-attaches
   the edited entry; for an offline host whose address changed, the stale attach
   error and last-known facts are gone with the record.
9. A parked `Ensure` refuses across an update swap; a parked update and a
   parked remove cannot both commit.
10. A `roots` edit drops the host's retained rows and the cache entry, then
    re-registers the source under the new roots; a non-`roots` edit leaves both
    untouched and the host's sessions keep rendering.
11. Per-field validation lands on the right field, in wire spelling: a missing
    ssh destination on `address`, an empty root on `roots`, a malformed name on
    `name` (add).
12. An invalid entry never reaches the sidecar: the refusal leaves the file
    bytes unchanged, a reload of the sidecar is clean, and a padded input is
    stored trimmed — the file and the live set cannot drift.
13. No regression to slice 1: `list`/`status` still never dial, Connect and
    remove behave as they did, an edit round-trips a stored `keyPath` rather
    than clearing it, and the add handler's own tests move with the params
    reshape instead of being left asserting the old flat shape.
14. A name mid-update refuses `add`, `remove` and a second `update` with the
    same conflict a removal in flight produces today — the mark's add arm
    included, since the mark now guards every mutation on the name.
15. An edit neither dials, deploys nor attaches: no new SSH dial appears during
    an update, and the row's live state changes only as the teardown's own
    events describe — its configured values come from the edited entry, which
    is what criterion 1 asserts.
16. A failed live phase leaves the file, the in-memory store row and the live
    registry all describing the old entry, and leaves the still-live identity's
    attach-derived facts intact — the clear is gated on a successful swap, so a
    refused live phase never touches the record — and a retry of the same edit
    then succeeds.
17. `Registry.Update` preserves the name's upstream edges, and the file's order
    is unchanged: the edit replaced one entry rather than moving it.

**Verification, not new code:** adding a host, connecting it, and starting a
session on it makes no discovery call that bypasses `evener/host/request`.

## 8. Out of scope

Plan/deploy/restart, the operation store and progress, remnants and the repair
affordances, crash fencing, per-host administration pages, credential push, and
the wider source-cap and cycle work.

**Pinned: deploying the evener executable.** Getting the binary onto a host from
the controller is not in this slice, and it is the next thing the deploy
pipeline owns. The concrete failure that surfaced it: connecting to a host
failed because the controller could not produce an executable the remote would
run, plausibly because the remote is a different OS or architecture and the
controller has no matching build to push. The ladder the Connect path already
runs decides a deploy internally and pushes a matching binary; what is missing
is the user-facing path — a plan the operator sees, a deploy and restart they
confirm, and an honest report of what landed — plus an answer for the host
whose platform the controller has no binary for. That belongs with the
pipeline slice's plan/deploy/restart work, and this pin is here so it is not
lost. The same work owns the build signal §13 wants on the row: the verified
post-operation facts refresh is what makes "the host is running the build I
deployed" a fact rather than a restatement of the controller's own version
(§6.11).

## 9. Files touched

- `appwire/types.go`, `appwire/protocol.go`, and the regenerated
  `appwire-client/typescript/types.gen.ts` and `docs/appwire-protocol.md`.
- `cmd/evener-hub/internal/hostreg/hostreg.go` and its tests.
- `cmd/evener-hub/internal/sshconn/manager.go` and its tests.
- `cmd/evener-hub/app_host_manage.go`, `cmd/evener-hub/app_rpc.go` (handler
  registration) and their tests.
- `cmd/evener-hub/frontend/src/stores/hosts.ts`,
  `cmd/evener-hub/frontend/src/panes/settings/sections/hosts.tsx`,
  `.../hosts.module.css`, and their tests.

## 10. Open questions

None. The two decisions this slice needed — the attached-edit rule and the
reduced guard contract — are recorded in §4 and §6.
