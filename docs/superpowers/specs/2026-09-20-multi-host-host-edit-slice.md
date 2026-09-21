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
  **every update advances the registry generation**, and the staged commit
  clears or re-keys name-keyed resolved state before rebinding; commit first,
  then rebind or tear down, gate released last.
- §13 — the Add/Edit dialog covers **all seven** `HostConfig` fields, none
  invented and none hidden, each with its validation message mapped from the
  backend's refusal; Edit does not offer `name`; the row renders the host's
  installed version beside the controller's.

It is *reduced* against some of §4's rules, by decision: §6 lists every
deviation and what the pipeline slice inherits.

## 3. What this slice adds

### 3.1 Wire (`appwire/types.go`, then the regenerated client and protocol doc)

- `HostAddParams` grows from `{name, address, keyPath}` to the full entry:
  `name`, `ssh`, `user`, `evenerPath`, `configPath`, `addr`, `roots`.
- `HostUpdateParams` is `{name}` plus the six mutable fields. The wire keeps
  slice 1's contextual spellings — `address` for the ssh destination,
  `keyPath` for the key path — and adds `user`, `evenerPath`, `configPath`,
  `addr`, `roots`. `HostUpdateResponse` is `{host: HostRow}`, mirroring
  remove's shape: the updated row, which the dialog's caller re-reads like
  every other mutation's result.
- `HostRow` grows to the effective entry *plus* the live state it already
  carries, so the dialog prefills from a row and `list`/`status` stay the one
  source of truth for what a host currently is. The optionals stay absent when
  unknown, as slice 1 renders them.

### 3.2 `hostreg.Registry.Update(entry Host) error` (`internal/hostreg`)

- Normalizes and validates with the same rules `AddWithUpstreams` runs
  (`validateEntry`, the name grammar, the reserved name, the host-count cap,
  the cycle check), preserving the name's existing upstream edges.
- Replaces the entry in place and assigns a fresh generation from the
  registry-wide counter. That generation is the invalidation mechanism: the
  manager's channel fence, `SameRegistration`, the remote-thread cache's
  per-source generations and the last-good retention are all keyed on it, so
  the update invalidates them by construction rather than by a list of things
  to clear.
- Refusals reuse the existing sentinels (`ErrUnknownHost`, `ErrInvalidName`,
  `ErrReservedName`, `ErrMissingSSH`, `ErrAmbiguousSSHUser`, `ErrEmptyRoot`,
  `ErrHostCycle`, the cap error).

### 3.3 `sshconn.Manager.UpdateHost(entry hostreg.Host) error`

- One hold of the per-host gate: the registry `Update`, then — when a
  dial-relevant field changed — the same teardown body `RemoveHost` and
  `DetachHost` already share (stop the supervisor, drop the channel, clear the
  per-host caches), with the gate released last.
- *Dial-relevant* means the fields the dial, preflight, deploy or health probe
  read: `ssh`, `user`, `keyPath`, `evenerPath`, `configPath`, `addr`. `roots`
  is not dial-relevant; neither is the name, which cannot change.
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
  found. Persist durable-first with one atomic sidecar write carrying the
  entry replaced (the store's `without(name)` plus the new entry); a failure
  whose rename already committed compensates back to the live contents exactly
  as add and remove do. Replace the store row in the same critical section,
  set the mark, release the mutex.
- **Live, mutex-free.** `manager.UpdateHost(entry)` when a manager is wired;
  otherwise the registry's own `Update`, mirroring how remove falls back when
  no manager is wired (tests, embedders).
- **Finish, under the mutex.** Clear the mark. On success, re-register the
  remote source when `roots` changed (`sources.Remove` then the existing
  `registerSource`), drop the name-keyed retained state the new generation no
  longer owns — the remote-thread cache's source entry and the last-good
  retention, exactly as remove drops them — reset the host's attach record so
  an edited host does not inherit its predecessor's attach state, and return
  the updated row. On failure, roll the sidecar back to the live set and return
  the error, so a host is never durable-but-unlive.

### 3.5 Frontend

- `stores/hosts.ts`: an `update(params)` call that issues `evener/host/update`
  and then the existing quiet re-read (`reReadAfterMutation`); `add` extends to
  the seven fields. Errors keep remove's posture — thrown to the caller, shown
  by the dialog, never silently swallowed.
- One `HostEntryDialog` serves Add and Edit: the seven fields, `name` rendered
  read-only on edit (per §13, Edit does not offer it), submit disabled while in
  flight, and each field's message placed inline (§5).
- Row actions: **Edit** for sidecar, non-removed rows. `hub.toml` rows keep the
  read-only explanation the pane's help text already gives; removed rows keep
  their removed posture.
- **Skew marker.** The row compares the host's reported build (`serverVersion`,
  falling back to `hubVersion`) against the controller's own
  `serverInfo.version` from the connection store and marks a difference. No
  backend change: the connection already carries the controller's version.

## 4. Semantics, precisely

- Update targets a live sidecar entry. `name` is immutable. The entry's
  effective values are what the row, the source and the attach path read: there
  is no second copy to drift.
- **The attached-edit rule.** An edit that changes a dial-relevant field while
  the host is attached lands, drops the channel, and leaves the row offline
  with Connect available. An edit that touches no dial-relevant field leaves
  the channel up. Both halves are asserted (§7).
- An edit never dials, never deploys, never attaches and never starts a
  session. Its live effects are exactly: the registry replacement, the
  conditional teardown, the source re-registration on a `roots` change, and the
  retained-state drop.
- `evener/host/attach` is unchanged by this slice, exactly as §4 states: an
  edit that leaves the host offline is connected again with the same Connect
  affordance, not with a new attach path.
- At most one mutation per name: a name mid-mutation refuses add, remove and
  update with the same Conflict a removal in flight produces today.

## 5. Error handling

- Validation refusals gain a field-carrying shape so a dialog can place them
  without parsing prose: the error data names the `field` beside the message.
  The mapping is from the existing sentinels — `ErrMissingSSH` → `ssh`,
  `ErrAmbiguousSSHUser` → `user` (the field that made the destination
  ambiguous), `ErrEmptyRoot` → `roots`, `ErrInvalidName` / `ErrReservedName` →
  `name`, `ErrHostCycle` → the entry as a whole. Anything else is a form-level
  message.
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
3. **`status` keeps its single-row response.** `controllerBuild` and the plan
   inputs stay in the pipeline slice; this slice's skew signal reads the
   connection's own reported version instead.
4. **Refusal classes follow slice 1's pattern** — `InvalidParams` with the host
   named — rather than §12's final discriminator vocabulary. The field-carrying
   validation refusal is the seed the pipeline slice aligns to §12.
5. **The host-count cap** is enforced on this path (`Update` runs the same
   check `Add` runs) but the wider enforcement the ledger's [03] item describes
   stays in the ledger.

## 7. Tests and acceptance criteria

Every item is pinned by a test in this slice's PR.

1. Each mutable field round-trips: edit it, and `list`/`status` returns the new
   value and the row prefills the dialog.
2. The sidecar is the durable record: after an edit and a sidecar reload, the
   edited values are the effective entry, and no `hub.toml` entry was written.
3. `name` cannot change: the wire type carries no name on update, and the
   dialog renders it read-only.
4. A `hub.toml`-declared name is refused with the edit-the-file message;
   nothing changes on disk or live.
5. A tombstone-only or unknown name is refused as not found; nothing is
   written.
6. Every update advances the generation — two successive updates give strictly
   increasing generations — and a captured pre-edit entry stops matching
   `SameRegistration`.
7. Attached plus a dial-relevant edit: the edit lands, the channel drops, the
   row reads offline, and a following Connect attaches the new address. Pinned
   with the gate-contention technique slice 1's tests use, not a sleep.
8. Attached plus a non-dial edit (a `roots` change): the channel survives.
9. A parked `Ensure` refuses across an update swap; a parked update and a
   parked remove cannot both commit.
10. A `roots` edit re-registers the source with the new roots, and the previous
    generation's retained rows stop publishing.
11. Per-field validation lands on the right field: a missing ssh on `ssh`, an
    empty root on `roots`, a malformed name on `name` (add).
12. The row's skew marker renders when the host's build and the controller's
    disagree, and stays quiet when they match.
13. No regression to slice 1: the existing add/Connect/remove tests pass
    unchanged, and `list`/`status` still never dial.

**Verification, not new code:** adding a host, connecting it, and starting a
session on it makes no discovery call that bypasses `evener/host/request`.

## 8. Out of scope

Plan/deploy/restart, the operation store and progress, remnants and the repair
affordances, crash fencing, per-host administration pages, credential push, and
the wider source-cap and cycle work.

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
