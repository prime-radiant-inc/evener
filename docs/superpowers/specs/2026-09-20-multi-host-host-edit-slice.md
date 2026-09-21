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

- `HostAddParams` becomes `{entry}` and `HostUpdateParams` becomes
  `{name, entry}` — the design record's own shape (§11), so the pipeline slice
  adds its guard fields to a wire this slice already matches instead of
  reshaping it a second time. This revises slice 1's flat
  `{name, address, keyPath}` params, and the regenerated client and the pane's
  store follow.
- `entry` carries the six mutable `HostConfig` fields under slice 1's wire
  spellings, with the schema mapping stated so nobody has to guess: `address`
  (schema `ssh`), `user`, `evenerPath` (schema `evener_path`), `configPath`
  (schema `config_path`), `addr`, `roots`, and `keyPath` (schema `key_path`).
  The schema has seven fields; six are mutable. `add`'s entry also carries
  `name`, which is required there and absent from the update entry because the
  update's target name is the request's own field.
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

- Normalizes and validates with the same rules `AddWithUpstreams` runs
  (`validateEntry`, the name grammar, the reserved name, the cycle check),
  preserving the name's existing upstream edges. It runs **no host-count cap**:
  the registry enforces no cap today — the cap lives only at config load, and
  moving it onto `New`/`Add`/`Update` is the design record's outstanding [03]
  item, not this slice's (§6).
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
  found. **Validate the entry here, before anything is written** — the same
  `hostreg.ValidateEntry` call the add flow runs for exactly this reason — so a
  refusal commits nothing and an entry the registry would reject never reaches
  the file. Then persist durable-first with one atomic sidecar write that
  replaces the entry **in place**, through a store-level replace: the sidecar
  keeps its add order, and composing `without(name)` with an append would
  silently move the edited host to the end of every later list. A failure whose
  rename already committed compensates back to the live contents exactly as add
  and remove do. Replace the store row in the same critical section, set the
  mark, release the mutex.
- **Live, mutex-free.** `manager.UpdateHost(entry)` when a manager is wired;
  otherwise the registry's own `Update`, mirroring how remove falls back when
  no manager is wired (tests, embedders). The registry re-runs the same
  validation under its own lock; the commit phase's check is what keeps an
  invalid entry out of the file, and is not a substitute for it.
- **Finish, under the mutex.** Clear the mark. On success:
  - when `roots` changed, drop the host's derived rows **first** — the
    remote-thread cache's source entry and the last-good retention, the two
    things the source's identity owns — and then re-register the source
    (`sources.Remove`, then the existing `registerSource`), which assigns a
    fresh cache generation. The order is load-bearing: dropping after
    re-registering would delete the generation the registration just minted.
  - when `roots` did not change, leave both alone: it is the same source, its
    cache generation still owns its rows, and the retained list is still this
    host's. An edit that changes only the SSH address or a path must not blank
    the host's sessions in the tree.
  - reset the host's attach record **only when the edit is what invalidated
    it** — when a dial-relevant change tore the channel down in the live phase.
    Resetting unconditionally would erase the record an attach that landed
    during the live phase just wrote; the entry stays visible throughout an
    update, so unlike `add` there is no pre-visibility window to reset inside.
  On failure, roll the sidecar back to the live set and return the error, so a
  host is never durable-but-unlive.

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
- **Skew marker.** The row compares the host's real installed build with the
  controller's. The host side is the facts-owned `hubVersion` — the remote
  hub's own build, from the preflight facts — and never `serverVersion`, which
  is the attach handshake's constant: the controller reports the same constant
  about itself, so comparing those two would agree with itself in production
  and the marker would never fire. The controller side is the build the shell
  already polls from `/api/health` (`stores/hubUpdate.ts`), which is the hub's
  real `buildinfo.Version()`, again not the connection's `serverInfo`. Both
  values already reach the browser, so this needs no wire change.
- `hostRowEqual` gains the new fields. The publish guard skips its `setState`
  when every row compares equal, and the comparator's field list is slice 1's:
  without extending it, an edit that changes only `user`, `evenerPath`,
  `configPath`, `addr` or `roots` is swallowed by the post-mutation quiet
  re-read and the pane keeps rendering the old row.

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
3. **`status` keeps its single-row response.** `controllerBuild` and the plan
   inputs stay in the pipeline slice; this slice's skew signal reads the
   health endpoint and the row's facts instead (§3.5).
4. **Refusal classes follow slice 1's pattern** — `InvalidParams` with the host
   named — rather than §12's final discriminator vocabulary. The field-carrying
   validation refusal is the seed the pipeline slice aligns to §12.
5. **No host-count cap.** The registry enforces none today — the cap sits at
   config load — so this slice neither adds one nor claims one. Moving it onto
   `New`/`Add`/`Update` is the design record's outstanding [03] item, and it
   lands with that work rather than here.
6. **The handler, catalog row and regenerated client land here.** §2's table
   places them in the pipeline PR, but slice 1 already registered
   `add`/`remove`/`list`/`status`, and a mutation whose method is not routed is
   not a feature. This slice continues slice 1's practice.
7. **The response is `{host: HostRow}`**, not §11's mutation-result union: the
   union's arms are the receipt and dedup outcomes the guards own, so it
   arrives with them.
8. **The params adopt §11's nested `entry`** and revise slice 1's flat add
   params, so the guards can be added later without a second wire change.

## 7. Tests and acceptance criteria

Every item is pinned by a test in this slice's PR.

1. Each mutable field round-trips: edit it, and `list`/`status` returns the new
   value, the row prefills the dialog, and the pane repaints — the row
   comparator carries the new fields, so the change is not swallowed by the
   publish guard.
2. The sidecar is the durable record: after an edit and a sidecar reload, the
   edited values are the effective entry, and no `hub.toml` entry was written.
3. `name` cannot change: the update request carries `name` only as the target
   it addresses, the dialog renders it read-only, and no mutable field can
   rename a host.
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
7. Attached plus a dial-relevant edit: the edit lands, the channel drops, the
   row reads offline, and a following Connect attaches the new address. Pinned
   with the gate-contention technique slice 1's tests use, not a sleep.
8. Attached plus a non-dial edit (a `roots` change): the channel survives.
9. A parked `Ensure` refuses across an update swap; a parked update and a
   parked remove cannot both commit.
10. A `roots` edit drops the host's retained rows and the cache entry, then
    re-registers the source under the new roots; a non-`roots` edit leaves both
    untouched and the host's sessions keep rendering.
11. Per-field validation lands on the right field, in wire spelling: a missing
    ssh destination on `address`, an empty root on `roots`, a malformed name on
    `name` (add).
12. The row's skew marker renders when the host's facts-reported build and the
    controller's health-reported build disagree, and stays quiet when they
    match — both sides fixtured, since neither is a constant in the test.
13. An invalid entry never reaches the sidecar: the refusal leaves the file
    bytes unchanged, and a reload of the sidecar is clean.
14. No regression to slice 1: the existing add/Connect/remove tests pass
    unchanged, and `list`/`status` still never dial.

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
lost.

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
