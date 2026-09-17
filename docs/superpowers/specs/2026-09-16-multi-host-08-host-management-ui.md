# Component spec 08 — Host management UI (add-host dialog, Connect, deploy/restart surface)

Status: not started. This spec is the hand-off for the implementing session.

Depends on: PR #1603 (the attached-only seams). The symbols this spec cites that
do not yet exist on main — `sshManager.ChannelIfAttached`, the
`RemoteHostClientIfAttached`/`RemoteHostHandshake` seams, and the attach
classifier — land with #1603 and MUST be on main before this component starts.
`evener/host/attach` itself and the spawn picker's Connect trigger also ship in
#1603 (round three); 08b extends that method with the plan/deploy/restart
surface and 08c adds the settings section. Do not start this component until
#1603 is landed.

Related: `2026-09-14-multi-host-evener-design.md` (topology), `…-03-host-config.md`
(host registry), `…-04-ssh-connection-manager.md` (attach + deploy/restart
machinery), `…-06-fleet-view.md` (picker, rail badges, online flags),
`…-07-remote-admin.md` (admin proxy). Components 01–07 are landed on main; this
component adds the user-facing management surface they deliberately left out.

## Purpose

Three user stories, all impossible today:

1. **Add a remote server from the UI.** Hosts are declared only as `[[hosts]]`
   entries in the controller's `hub.toml`, read once at startup
   (`cmd/evener-hub/config.go:62`, `Hosts []HostConfig`). There is no
   config-write path anywhere in the hub, no add/remove/update methods, and no
   hot-apply: today "add a host" means edit a file and restart the controller.
2. **Connect on demand.** Attachment is lazy (component 05/06 ruling: first use
   dials), and #1603's round three adds the `evener/host/attach` method plus a
   spawn-picker trigger so the federation is operable. This component adds the
   management surface: a Hosts section with per-host state and a Connect
   action, and the deploy/restart controls.
3. **Deploy / restart from the UI.** Component 04b's machinery (version
   auto-match, deploy, restart, bootstrap-on-bare-host) exists inside
   `sshconn.Manager` but is only reachable as a side effect of `Ensure`. No
   method triggers it, no surface reports it.

The spawn host picker (06b) is hidden while zero remote hosts are configured, so
a user with no `[[hosts]]` entry sees no federation UI at all. This component is
what makes the federation fully operable without a terminal.

## Scope

- Controller-side hub methods: `evener/host/list`, `evener/host/add`,
  `evener/host/update`, `evener/host/remove`, `evener/host/status`,
  `evener/host/plan`, `evener/host/deploy`, `evener/host/restart`, and
  `evener/host/operations`. `evener/host/attach` ships with #1603 and is
  extended here, not re-specified.
- Durable persistence of host entries with a hot-apply path: the running hub
  picks up add/update/remove without a restart (registry, sshconn manager,
  source registry, web config, host-admin controller, navigation manifest).
- A controller-side **operation store** for deploy/restart: durable records,
  idempotency, per-host serialization, crash recovery, progress, and an
  operations read API (polling is the guaranteed read path).
- A new **Hosts** settings section in the hub web UI: host list with live
  state, add/edit dialog, remove-with-confirm, Connect / Deploy / Restart
  actions, deploy confirmation showing what will be installed where.
- AppWire protocol catalog entries for every new method/type, the regenerated
  TypeScript client, and frontend settings-section registration — the methods
  and the section are unreachable without these, so they are in scope of the
  implementing PRs, not an afterthought.

## Non-scope

- The remote-admin proxy (`app_host_admin.go`) and its forwarded allow-list are
  unchanged in *interface*: the methods above are **controller-local** — they
  act on the controller's config and its sshconn manager, not on a remote
  hub's surface. They must NOT be added to `remoteHostAdminMethods` (negative
  test required). The proxy's *host set* does become live (see data flow).
- Detach/disconnect of an individual host (no `evener/host/detach`; `Manager.Close`
  remains whole-manager). See open questions.
- Multi-tenancy / auth changes: the methods ride the hub's existing
  admin-mutation admission; no new capability type.
- The native client; hub web only.
- Fixing the hub's `ServerInfo.Version` constant (`"0.1.0"` at
  `cmd/evener-hub/main.go:46`, recorded follow-up from PR #1603 round two):
  version display in this component reads the preflight facts, which already
  report the remote's real build; the constant is a separate owner decision.
- Any change to lazy attachment semantics: the attached-only seams (#1603) stay
  exactly as landed. `evener/host/list`, `status`, and `operations` never dial —
  test-pinned — and `evener/host/attach` remains the only dialing trigger
  besides an explicit `SourceID`.

## Contract / interfaces

### Method surface

All are hub-side handlers registered like every other hub method (the
router-vs-catalog test pins registration), classified as mutations where they
mutate, admission-gated like the hub's other settings mutations, and each gets
its AppWire protocol catalog entry + regenerated TypeScript client.

**Host mutations are serialized by one process-wide lock** (all of
`add`/`update`/`remove` take it for their entire staged commit), so concurrent
read-modify-write on the sidecar cannot lose updates.

- `evener/host/list` (read, never dials): every configured host — the full
  effective `HostConfig` fields plus live state: `attached`
  (`sshManager.ChannelIfAttached(name)`), preflight facts when known (installed
  version, OS/arch), last attach error, whether the manager is currently
  mid-`Ensure`, and the **origin marker** (declared in `hub.toml` vs declared
  in the sidecar — the effective source after merge). **Removed-but-retained
  hosts (tombstones) appear as `list` rows with `removed: true`** and their
  retained-row count — one surface, no separate retained view.
- `evener/host/add` (mutation): body = one host entry. Validate with the
  component-03 rules (name validation, the `[[hosts]]` field validation at
  `cmd/evener-hub/config.go:177`). **Refuses any name already declared in
  `hub.toml` or in the sidecar** — no duplicate can exist (the boot-time
  behavior for a hand-created duplicate is a hard startup error; see
  persistence). Persist, hot-apply. Surfaced validation errors are the
  dialog's inline errors.
- `evener/host/update` (mutation): **requires the target name to exist in the
  sidecar**; a `hub.toml`-declared name is refused with the "edit the file"
  explanation; a name absent from both is refused as not-found. Mutates every
  field except `name` — **names are immutable** (they key source IDs, cached
  rows, manager state, and file entries; renaming is remove + add, documented
  in the UI). Update never inserts a new name; only `add` can.
- `evener/host/remove` (mutation): **sidecar-declared hosts only** (same
  refusal for `hub.toml` names as update). Persist + hot-apply. Removing an
  attached host stops its supervisor and detaches. The removed host's cached
  snapshot rows are **not** silently dropped: the source's last-known-good
  snapshot is retained as an explicit **tombstone** (see data flow), and the UI
  warns when the host has live remote threads (host data on the remote is
  untouched).
- `evener/host/attach` (mutation): **shipped with #1603 round three** (wraps
  the dialing closure, errors classified through the #1603 attach classifier:
  ssh-start/deadline chains → typed `SessionUnavailable`, caller-context
  cancellation stays raw; idempotent). Unchanged by this component.
- `evener/host/status` (read, never dials, mints nothing): one host's `list`
  row plus the **deploy plan inputs**: controller build (`buildinfo`), remote
  installed version (from the last known preflight facts — **stale facts are
  marked with their age**), the deploy decision inputs, the resolved remote
  target path (as of those facts), whether a restart follows, and any
  plan-time refusal known from the last preflight (`errControllerDirty` is
  terminal; missing curl, unwritable target, unit findings per 04b). This is
  the pure read the confirmation dialog renders.
- `evener/host/plan` (mutation — it mints durable controller state, so it is
  classified and admitted as one): body `{name}`. Builds a **fresh plan from
  the current facts and configuration** and returns the plan plus a
  **controller-minted confirmation token**: expiring, single-use, bound to
  (host name, a hash of the resolved host entry, the preflight revision the
  plan was built from, the resolved target path, controller revision, nonce).
  **Outstanding-token rule: minting a new token for a host immediately
  supersedes any earlier unconsumed token for that host.** If the known
  preflight facts are stale (older than the plan freshness bound), `plan`
  returns **no token** and names the staleness — the UI must attach (or
  otherwise refresh facts) first; a token is only minted from fresh facts.
  The token is generated by the controller, never constructed client-side —
  this is the enforcement behind "the user always sees what will be installed
  where".
- `evener/host/deploy` (mutation): **processing order is fixed.** (1) If the
  client operation ID matches an existing durable operation record, return
  that record — no token validation, no consumption (this makes the
  single-use token and idempotent retry coexist: a replay after a lost
  response succeeds without a fresh token). (2) Otherwise validate the
  token: **missing, mismatched, superseded, or expired → refusal**; then
  **re-resolve the target and re-read the host entry at execution time and
  reject if either differs from the token's bindings** (a `hub.toml`/sidecar
  edit, a facts refresh, or a target change between plan and deploy invalidates
  the plan; the UI re-plans). (3) Consume the token, create the operation
  record, return its id. Never blocks the RPC on the push. Wraps the 04b
  deploy path (`Manager.deploy` / `deployTarget` /
  `resolveDeployOrCreateTarget`) with its guards intact and surfaced
  verbatim: source/revision verification (`verifyBuildSource`,
  `verifyBuildRevision`), terminal dirty-controller refusal, push integrity,
  resolved-target persistence.
- `evener/host/restart` (mutation): same operation model (operation ID dedup
  first; no token — restart has no install step), wraps the 04b restart path
  (user vs system unit decision, `waitHealthy` proven replacement).
- `evener/host/operations` (read): list/detail of controller-side operations
  (see below). Never dials. **Polling this method is the guaranteed read path
  for operation progress**; the host-notification stream, where extended to
  carry controller-originated events, is best-effort only.

### Long-running operations (the operation store)

Deploy and restart take minutes and must not hold an AppWire RPC open. The
existing host-notification stream carries remote configuration notifications
and the existing jobs surface is session-scoped, so neither can carry durable
controller-side operation records; this component defines a
**controller-side durable operation store**:

- Record: operation id (controller-assigned), client operation ID (durable dedup
  index — a replay with a known op ID returns the existing record), host, kind
  (deploy/restart), state (`pending`/`running`/`complete`/`failed`/
  `interrupted`), progress entries (timestamped, bounded), terminal result,
  timestamps.
- **Crash recovery:** at startup, before the store serves any request, every
  record still in `pending`/`running` transitions to `interrupted` (a
  terminal unknown outcome) with a note naming the crash; a retry with the
  same operation ID gets the `interrupted` record back, and a new operation
  ID starts a fresh operation. No record can stay stuck forever.
- **Per-host operation gate:** at most one deploy/restart per host at a time;
  the gate is shared with `Ensure`-triggered deploys and supervisor activity
  so an auto-deploy and a user deploy cannot interleave (the 04b
  `errControllerDirty` posture applies across all of them).
- Progress is readable via `evener/host/operations` (guaranteed). If the
  implementing session extends the host-notification stream with a
  controller-originated best-effort event class, the UI may render from it
  but MUST fall back to polling; polling-only is a fully valid implementation.
- The UI renders progress → terminal state. Never a synchronous RPC.

### Persistence + hot-apply

- **Persistence target:** the controller's `hub.toml` is hand-authored with
  comments; a TOML re-marshal would strip them. The UI writes a managed sidecar
  in the same config dir (e.g. `hub.hosts.json`), loaded after `hub.toml`.
  **Merge rules:** `add`/`update` refuse any name already declared in
  `hub.toml` (the file is authoritative for its own names); `remove` deletes
  only sidecar entries; a `hub.toml`-declared host can never be shadowed or
  UI-removed — its `list` entry says "declared in hub.toml". The sidecar is
  the UI's only writable source. **A name found in both `hub.toml` and the
  sidecar at load time is a hard startup error naming both locations** — the
  refuse rule is enforced at write time AND the boot merge rejects the
  hand-edited collision. Writes are atomic (temp + rename).
- **Staged commit (order matters):** add/update/remove never mutate live
  state incrementally. Under the process-wide mutation lock: (1) stage the
  complete change — new registry value, manager deltas (including
  supervisor/channel teardown for removals), source-registry rows,
  host-admin-controller host set and its notification fan-outs, web-config
  host view, and the new sidecar bytes; (2) **persist the sidecar first**
  (atomic rename); (3) **swap the runtime to the staged set**; (4) if the
  swap itself fails, compensate by reverting the runtime to the previous set
  and reporting exactly which step failed. A crash between (2) and (3) is
  reconciled at boot: **the disk wins** — boot always builds live state from
  what was persisted, so a mid-commit crash converges to the persisted new
  state on restart. Within a live process, staging failures change nothing.
- Hot-apply mechanics touch the seams built once at startup today:
  `hostRegistryEntries(cfg)` → `hostreg.New` → `sshconn.New` → the
  `RemoteHost*` fields in `hubcore.WebConfig` (`main.go:405-488`),
  `newHubSourceRegistry`, **and the host-admin controller (07a), whose
  per-host request forwarding and notification fan-outs are initialized from
  the startup snapshot — they must consume the live host set and start/stop
  fan-outs as part of the staged commit (and its rollback)**. The live
  host-set surface (registry `Add`/`Update`/`Remove` with the existing cycle
  and validation rules, manager add/update/remove safe against in-flight
  `Ensure` and running supervisors) is 08a's core work.

### UI

- **Hosts settings section** (new `panes/settings/sections/hosts/*`, plus its
  registration in the settings section map): host rows with state chip (online
  / offline / connecting / removed-retained), installed version + controller
  version, OS/arch, origin marker, actions: Connect, Deploy, Restart, Edit,
  Remove (each with the confirm pattern used elsewhere in settings), and the
  Add button.
- **Add / Edit dialog:** fields exactly the `HostConfig` schema — `name`,
  `ssh`, `user`, `evener_path`, `config_path`, `addr`, `roots` (multi-line;
  `config.go:32-46`) — all seven fields, none invented, none hidden; each with
  its validation message mapped from the backend response. Edit does not
  offer `name` (immutable).
- **Connect state machine:** offline → connecting (in-flight, driven by the
  `attach` response or the host-notification stream) → online (the sources
  manifest's online flag flips; the rail and picker update through the
  existing 06b/07b paths) or failed (typed error shown, retry). The
  spawn-picker Connect trigger shipped with #1603 stays as-is; this section is
  the management view of the same action.
- **Deploy confirmation:** the dialog first renders the `status` plan —
  target host, controller revision, resolved remote target path, whether a
  restart follows, facts freshness, and any plan-time refusal (terminal ones
  disable the button). Confirm calls `evener/host/plan` (fresh token) and
  then `evener/host/deploy` with that token and a client operation ID;
  stale-facts responses from `plan` direct the UI to Connect first. Progress
  renders from `operations` polling (with best-effort stream events if
  implemented); terminal failure surfaces the verbatim 04b error; a retry
  after a lost response reuses the same operation ID.
- **Stores:** follow the 07b host-store pattern (`hostInstancesStore` /
  per-host request sequences / connection-generation guards as fixed in
  #1605). No shared mutable module state.

## Implementation approach (files/packages, cited seams)

- `cmd/evener-hub/internal/hostreg` — live registry: `Add` exists; add
  `Update`/`Remove` with the same cycle/validation rules, safe for concurrent
  use.
- `cmd/evener-hub/internal/sshconn` — `Manager` gains the live host-set
  surface (add/update/remove entry, each safe against in-flight `Ensure` and
  running supervisors) and thin exported entry points for deploy/restart
  reusing the 04b internals (`deploy`, `ensureDecision`, `waitHealthy`,
  `bootstrapHub`); `Ensure`/`Attached`/`ChannelIfAttached`/facts unchanged.
- `cmd/evener-hub/config.go` — sidecar load/merge (hard-error duplicate rule)
  + atomic write.
- `cmd/evener-hub/app_host_manage.go` (new) — the handlers, router
  registration, mutation classification (`plan` is a mutation — it mints
  durable state; `list`/`status`/`operations` are reads), explicitly NOT in
  `remoteHostAdminMethods` (negative assertion in the allow-list tests).
- **AppWire protocol catalog** entries for every new method + request/response
  types, and the **regenerated TypeScript client** — both in the same PR as
  the handlers, so the frontend can consume them.
- The **operation store**: a small durable store (records keyed by operation
  id, dedup index on client operation ID, startup reconciliation to
  `interrupted`) + optional host-notification publisher extension. Where it
  lives on disk follows the hub's existing durable-record conventions (the
  implementing session picks the closest existing store pattern and names it
  in the PR).
- `cmd/evener-hub/main.go` — replace the startup snapshot of host entries
  with the live view seam (registry, manager, sources, web config, **and the
  host-admin controller's host set/fan-outs**); keep every existing
  `RemoteHost*` wiring.
- Frontend: `settings/sections/hosts/*` + settings registration, host-manager
  store, `operations` polling, notification subscription reuse. The spawn
  picker is untouched (its Connect trigger ships with #1603).
- PR sequence: **08a** persistence + hot-apply + registry/manager/admin-
  controller surface + `list/add/update/remove` (with catalog + client
  regeneration); **08b** `status`/`plan` (token) / `deploy` / `restart` /
  `operations` + the operation store; **08c** the frontend (settings section,
  dialogs, deploy confirmation). 08b stacks on 08a; 08c stacks on 08b.

## Data flow (remove + tombstone)

`refreshRemoteThreadSnapshot` enumerates the registered sources; removing one
would otherwise drop its rows from the next snapshot. Removal instead writes
an **explicit tombstone record for the source** (name, last-known-good rows,
removal timestamp): the rows stay in the navigation tree marked **stale**,
and the tombstone is **consumed by the tree, the manifest, and the
action-capability logic** — removed-host rows must be visibly non-actionable
(markers in the tree/rail; actions disabled), not merely `Complete == false`,
because liveness alone does not make an unknown source's rows stale in the
current navigation projection. The tombstone is purged when the same host
name is re-added, or after a retention period (owner-set; open question).
Tombstoned hosts appear in `list` with `removed: true`.

## Error handling

- Attach failures: typed `SessionUnavailable` via the #1603 classifier; the
  caller-context error stays raw; the UI renders the classification, not the
  raw chain.
- Deploy: dedup-first processing order is fixed; invalid/expired/superseded
  tokens → refusal, always; **execution-time re-resolution mismatch →
  refusal with a re-plan instruction**; plan-time refusals surface verbatim
  (`errControllerDirty` is terminal — the UI must not offer retry-anything;
  unmet prerequisites shown before confirmation); runtime failures land in
  the operation record verbatim; `interrupted` records tell the user the
  outcome is unknown and a new operation may be started.
- Hot-apply failures: staging failures change nothing; a failed swap
  compensates by reverting the runtime, and the error carries which seam
  failed (registry / manager / source / admin controller / web view /
  persistence).
- `evener/host/*` on an unknown host name: typed not-found.

## Testing

- 08a: registry live-update tests (including the add-time cycle rules),
  manager add/remove-vs-supervisor tests (removing an attached host stops its
  supervisor and closes its channel), sidecar merge/atomicity tests including
  the refuse rules and the boot-time hard-error duplicate, the host-admin
  controller live-set tests (forwarded requests reach newly added hosts;
  removed hosts' fan-outs stop), tombstone retention tests (list attached →
  remove → rows persist and are marked stale/non-actionable in tree +
  manifest + capabilities), and a wiring test mirroring the 05a registration
  tests.
- 08b: handler tests per method (validation, admission, classification incl.
  `plan`-as-mutation, the token matrix: missing/mismatched/expired/superseded/
  consumed-then-replayed-with-new-op-ID, execution-time re-resolution
  mismatch, operation-ID dedup including the interrupted-record path, per-host
  gate serialization vs an in-flight `Ensure` deploy, startup reconciliation
  of pending/running → interrupted), and the not-in-forwarded-allow-list
  assertion.
- 08c: `make test-web`, browser gate (`env -u DBUS_SESSION_BUS_ADDRESS make
  test-web-browser`), `make lint-generated`; per-flow tests in the section's
  test files; the never-dial invariant of `list`/`status`/`operations`
  test-pinned.
- **Live E2E (strongly recommended, never yet exercised):** the campaign
  landed 04b without a live deploy against a real host. 08b/08c should run
  one add → connect → deploy → restart → spawn-remote cycle against a
  disposable host before declaring the component done; that requires a host
  Jesse designates.

## Acceptance criteria

1. A user with zero hosts configured adds one from the UI, sees it connect,
   and the spawn picker appears with the host selectable — no file editing,
   no controller restart.
2. An offline configured host has a working Connect action in the Hosts
   section (the picker's trigger shipped with #1603); success flips its
   online state everywhere (rail, picker, manifest) through the existing
   paths.
3. Deploy from the UI: the confirmation renders the controller-minted plan
   (controller revision + resolved remote target path) and cannot proceed
   without a fresh token; an edit between plan and deploy is rejected and
   re-planned; after confirm, the host reports the new version in `status`,
   and the version-skew signal (facts vs controller build) is truthful; a
   replayed deploy with the same operation ID returns the same record; a
   controller crash mid-deploy surfaces as `interrupted`, never a stuck
   operation.
4. Hand-edited `hub.toml` hosts keep working exactly as today; UI add/update
   refuse their names; the origin marker shows which file owns each entry; a
   hand-created duplicate name across files is a hard startup error.
5. Removing a sidecar host retains its last-known-good rows as an explicitly
   stale, non-actionable tombstone (tree + manifest + capabilities,
   test-pinned); re-adding the same name purges it; `list` shows it with
   `removed: true`.
6. No lazy-attachment regression: with no attach call, no explicit source,
   nothing dials — `list`/`status`/`operations` never attach (test-pinned).
7. Standard gates green on every PR (go/build/vet, package races, full hub
   suite, module-lint; web + browser + lint-generated for 08c).

## PR size estimate (LOC)

- 08a: ~800–1100 (registry/manager/config/admin-controller/wiring/catalog/
  client) + tests.
- 08b: ~700–1000 (handlers + token + operation store + reconciliation) +
  tests.
- 08c: ~800–1100 (section, dialogs, stores, polling) + tests.

## Open questions

1. **Sidecar vs rewrite of `hub.toml`** — spec'd as sidecar with the refuse
   rules and hard-error duplicate; the owner may still prefer in-place
   `hub.toml` rewrite accepting comment loss.
2. **Tombstone retention period** — re-add purges by name; what is the
   default retention for never-re-added hosts (e.g. 7 days)?
3. **Who may add hosts** — reuse the settings-mutation admission as-is, or a
   distinct grant? Spec assumes as-is.
4. **Per-host detach** — out of scope here; natural follow-up once remove
   semantics settle.
5. **`ServerInfo.Version` constant** — owner follow-up from #1603 r2; does not
   block this component (version display reads preflight facts).
