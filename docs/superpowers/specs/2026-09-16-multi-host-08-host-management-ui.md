# Component spec 08 — Host management UI (add-host dialog, Connect, deploy/restart surface)

Status: not started. This spec is the hand-off for the implementing session.

Depends on: PR #1603 (the attached-only seams). The symbols this spec cites that
do not yet exist on main — `sshManager.ChannelIfAttached`, the
`RemoteHostClientIfAttached`/`RemoteHostHandshake` seams, and the attach
classifier — land with #1603 and MUST be on main before this component starts.
`evener/host/attach` itself and the spawn picker's Connect trigger also ship in
#1603 (round three); 08b extends that method with status/deploy/restart and
08c adds the settings section. Do not start this component until #1603 is
landed.

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
  `evener/host/deploy`, `evener/host/restart`, plus the operation-reading
  surface (`evener/host/operations`). `evener/host/attach` ships with #1603
  and is extended here, not re-specified.
- Durable persistence of host entries with a hot-apply path: the running hub
  picks up add/update/remove without a restart (registry, sshconn manager,
  source registry, web config, navigation manifest).
- A controller-side **operation store** for deploy/restart: durable records,
  idempotency, per-host serialization, progress, and an operations read API.
- A new **Hosts** settings section in the hub web UI: host list with live
  state, add/edit dialog, remove-with-confirm, Connect / Deploy / Restart
  actions, deploy confirmation showing what will be installed where.
- AppWire protocol catalog entries for every new method/type, the regenerated
  TypeScript client, and frontend settings-section registration — the methods
  and the section are unreachable without these, so they are in scope of the
  implementing PRs, not an afterthought.

## Non-scope

- The remote-admin proxy (`app_host_admin.go`) and its forwarded allow-list are
  unchanged: the methods above are **controller-local** — they act on the
  controller's config and its sshconn manager, not on a remote hub's surface.
  They must NOT be added to `remoteHostAdminMethods` (negative test required).
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
  exactly as landed. `evener/host/list` and `evener/host/status` never dial —
  test-pinned — and `evener/host/attach` remains the only dialing trigger
  besides an explicit `SourceID`.

## Contract / interfaces

### Method surface

All are hub-side handlers registered like every other hub method (the
router-vs-catalog test pins registration), classified as mutations where they
mutate, admission-gated like the hub's other settings mutations, and each gets
its AppWire protocol catalog entry + regenerated TypeScript client.

- `evener/host/list` (read): every configured host — the full effective
  `HostConfig` fields plus live state: `attached`
  (`sshManager.ChannelIfAttached(name)`), preflight facts when known (installed
  version, OS/arch), last attach error, whether the manager is currently
  mid-`Ensure`, and the **origin marker** (declared in `hub.toml` vs declared
  in the sidecar — the effective source after merge). Never dials.
- `evener/host/add` (mutation): body = one host entry. Validate with the
  component-03 rules (`hostref` name validation, the `[[hosts]]` field
  validation at `cmd/evener-hub/config.go:177`). **Refuses any name already
  declared in `hub.toml` or the sidecar** — no shadowing exists by construction
  (see persistence). Persist, hot-apply. Surfaced validation errors are the
  dialog's inline errors.
- `evener/host/update` (mutation): every field except `name` — **names are
  immutable** (they key source IDs, cached rows, manager state, and
  `hub.toml`/sidecar entries; renaming is remove + add, documented in the UI).
  Same refuse rule on conflict, same validation.
- `evener/host/remove` (mutation): **sidecar-declared hosts only**; a
  `hub.toml`-declared host is refused with the "edit the file" explanation.
  Persist + hot-apply. Removing an attached host stops its supervisor and
  detaches. The removed host's cached snapshot rows are **not** silently
  dropped: the source's last-known-good snapshot is retained as a stale
  tombstone (see data flow), and the UI warns when the host has live remote
  threads (rows persist as stale; host data on the remote is untouched).
- `evener/host/attach` (mutation): **shipped with #1603 round three** (wraps
  the dialing closure, errors classified through the #1603 attach classifier:
  ssh-start/deadline chains → typed `SessionUnavailable`, caller-context
  cancellation stays raw; idempotent). 08b extends only its response with the
  plan fields the deploy confirmation reuses.
- `evener/host/status` (read, never dials): one host's `list` row plus the
  **deploy plan and confirmation token**: controller build (`buildinfo`),
  remote installed version (from the last known preflight facts — stale facts
  are marked), the deploy decision inputs, the resolved remote target path,
  whether a restart follows, any plan-time refusal (`errControllerDirty` is
  terminal; missing curl, unwritable target, unit findings per 04b), and a
  **controller-minted confirmation token**: expiring, single-use, bound to
  (host name, resolved target path, controller revision, nonce). The token is
  generated by the controller when the plan is built. It is never minted or
  guessed client-side — this is the enforcement behind "the user always sees
  what will be installed where".
- `evener/host/deploy` (mutation): requires the confirmation token from
  `status`; **rejects missing, mismatched, or expired tokens** (a direct RPC
  caller cannot deploy without having obtained a plan). Carries a client
  **operation ID** for idempotency. Returns the operation record id.
  Never blocks the RPC on the push. Wraps the 04b deploy path
  (`Manager.deploy` / `deployTarget` / `resolveDeployOrCreateTarget`) with its
  guards intact and surfaced verbatim: source/revision verification
  (`verifyBuildSource`, `verifyBuildRevision`), terminal dirty-controller
  refusal, push integrity, resolved-target persistence.
- `evener/host/restart` (mutation): same operation model (operation ID, token
  not required — restart has no install step), wraps the 04b restart path
  (user vs system unit decision, `waitHealthy` proven replacement).
- `evener/host/operations` (read): list/detail of controller-side operations
  (see below). Never dials.

### Long-running operations (the operation store)

Deploy and restart take minutes and must not hold an AppWire RPC open. The
existing host-notification stream carries remote configuration notifications
and the existing jobs surface is session-scoped, so neither can carry these;
this component defines a **controller-side durable operation store**:

- Record: operation id (controller-assigned), client operation ID (for
  idempotent retries after a lost response — dedup keyed on it, durable),
  host, kind (deploy/restart), state (pending/running/complete/failed),
  progress entries (timestamped, bounded), terminal result, timestamps.
- **Per-host operation gate**: at most one deploy/restart per host at a time;
  the gate is shared with `Ensure`-triggered deploys and supervisor activity
  so an auto-deploy and a user deploy cannot interleave (the 04b
  `errControllerDirty` posture applies across all of them).
- Progress is published on the existing host-notification stream as a new
  controller-originated event class (host-scoped, so existing subscribers
  filter naturally), and the full record is readable via
  `evener/host/operations` for polling clients.
- The UI subscribes per host and renders progress → terminal state. If the
  implementing session finds the host-notification stream structurally wrong
  for controller-originated events, fall back to polling
  `evener/host/operations` only — never a synchronous RPC.

### Persistence + hot-apply

- **Persistence target:** the controller's `hub.toml` is hand-authored with
  comments; a TOML re-marshal would strip them. The UI writes a managed sidecar
  in the same config dir (e.g. `hub.hosts.json`), loaded after `hub.toml`.
  **Precedence without shadowing:** `add`/`update` REFUSE any name already
  declared in `hub.toml` (the file is authoritative for its own names);
  `remove` deletes only sidecar entries; a `hub.toml`-declared host can never
  be shadowed, silently re-exposed by a sidecar delete, or UI-removed — its
  `list` entry says "declared in hub.toml". The sidecar is the UI's only
  writable source. Writes are atomic (temp + rename), and a boot-time parse
  failure of the sidecar is a hard startup error.
- **Staged atomic apply:** add/update/remove never mutate live state
  incrementally. Stage the complete change first: the new registry value, the
  manager deltas (including supervisor/channel teardown for removals), the
  source-registry rows, the web-config host view, and the persistence bytes.
  Commit in one step: swap the runtime to the staged set and atomically
  rename the sidecar. If staging fails, nothing changed. If the commit itself
  fails partway, compensate by reverting the runtime to the previous set and
  reporting exactly which step failed — the durable state is either fully old
  or fully new because the persistence write is a single atomic rename.
- Hot-apply mechanics touch the seams built once at startup today:
  `hostRegistryEntries(cfg)` → `hostreg.New` → `sshconn.New` → the
  `RemoteHost*` fields in `hubcore.WebConfig` (`main.go:405-488`) and
  `newHubSourceRegistry`. The live host-set surface (registry
  `Add`/`Update`/`Remove` with the existing cycle and validation rules,
  manager add/update/remove safe against in-flight `Ensure` and running
  supervisors) is 08a's core work.

### UI

- **Hosts settings section** (new `panes/settings/sections/hosts/*`, plus its
  registration in the settings section map): host rows with state chip (online
  / offline / connecting / stale-tombstone), installed version + controller
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
- **Deploy confirmation:** the dialog renders the `status` plan first — target
  host, controller revision, resolved remote target path, whether a restart
  follows, and any plan-time refusal (terminal ones disable the button) — and
  only then can the user confirm; the confirm sends the controller-minted
  token. Progress comes from the operation record/notification stream;
  terminal failure surfaces the verbatim 04b error.
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
- `cmd/evener-hub/config.go` — sidecar load/merge + atomic write with the
  refuse-shadowing rule.
- `cmd/evener-hub/app_host_manage.go` (new) — the handlers, router
  registration, mutation classification; explicitly NOT in
  `remoteHostAdminMethods` (negative assertion in the allow-list tests).
- **AppWire protocol catalog** entries for every new method + request/response
  types, and the **regenerated TypeScript client** — both in the same PR as
  the handlers, so the frontend can consume them.
- The **operation store**: a small durable store (records keyed by operation
  id, dedup index on client operation ID) + the host-notification publisher
  extension. Where it lives on disk follows the hub's existing durable-record
  conventions (the implementing session picks the closest existing store
  pattern and names it in the PR).
- `cmd/evener-hub/main.go` — replace the startup snapshot of host entries
  with the live view seam; keep every existing `RemoteHost*` wiring.
- Frontend: `settings/sections/hosts/*` + settings registration, host-manager
  store, notification subscription reuse. The spawn picker is untouched
  (its Connect trigger ships with #1603).
- PR sequence: **08a** persistence + hot-apply + registry/manager surface +
  `list/add/update/remove` (with catalog + client regeneration); **08b**
  `status` (plan + token) / `deploy` / `restart` / `operations` + the
  operation store; **08c** the frontend (settings section, dialogs, deploy
  confirmation). 08b stacks on 08a; 08c stacks on 08b.

## Data flow (remove + tombstone)

`refreshRemoteThreadSnapshot` enumerates the registered sources; removing one
would otherwise drop its rows from the next snapshot. Removal instead marks
the source's cached snapshot **stale (tombstoned)**: the rows stay in the
navigation tree as last-known-good with `Complete == false`, exactly like the
detached-but-configured case the #1603 snapshot gate already pins. The
tombstone is purged when the same host name is re-added, or after a retention
period (owner-set; open question). `list`/`status` show tombstoned hosts as
removed-but-retained.

## Error handling

- Attach failures: typed `SessionUnavailable` via the #1603 classifier; the
  caller-context error stays raw; the UI renders the classification, not the
  raw chain.
- Deploy: invalid/expired/mismatched confirmation token → refusal, always;
  plan-time refusals surface verbatim (`errControllerDirty` is terminal — the
  UI must not offer retry-anything; unmet prerequisites shown before
  confirmation); runtime failures land in the operation record verbatim.
- Hot-apply failures: previous state stays live; the staged-commit error
  carries which seam failed (registry / manager / source / web view /
  persistence).
- `evener/host/*` on an unknown host name: typed not-found.

## Testing

- 08a: registry live-update tests (including the add-time cycle rules),
  manager add/remove-vs-supervisor tests (removing an attached host stops its
  supervisor and closes its channel), sidecar merge/atomicity tests including
  the refuse-shadowing rule, tombstone retention tests (list attached →
  remove → rows persist in `fetch.threads` + `fetch.sources[id].Threads`,
  `Complete == false`), and a wiring test mirroring the 05a registration
  tests.
- 08b: handler tests per method (validation, admission, classification,
  token rejection matrix: missing/mismatched/expired/consumed, operation-ID
  dedup, per-host gate serialization vs an in-flight `Ensure` deploy), and
  the not-in-forwarded-allow-list assertion.
- 08c: `make test-web`, browser gate (`env -u DBUS_SESSION_BUS_ADDRESS make
  test-web-browser`), `make lint-generated`; per-flow tests in the section's
  test files; the never-dial invariant of `list`/`status` test-pinned.
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
   without the token; after confirm, the host reports the new version in
   `status`, and the version-skew signal (facts vs controller build) is
   truthful; a replayed deploy with the same operation ID is deduplicated.
4. Hand-edited `hub.toml` hosts keep working exactly as today; UI add/update
   refuse their names; the origin marker shows which file owns each entry.
5. Removing a sidecar host retains its last-known-good rows as stale
   (test-pinned); re-adding the same name purges the tombstone.
6. No lazy-attachment regression: with no attach call, no explicit source,
   nothing dials — `list`/`status` never attach (test-pinned).
7. Standard gates green on every PR (go/build/vet, package races, full hub
   suite, module-lint; web + browser + lint-generated for 08c).

## PR size estimate (LOC)

- 08a: ~700–1000 (registry/manager/config/wiring/catalog/client) + tests.
- 08b: ~600–900 (handlers + token + operation store) + tests.
- 08c: ~800–1100 (section, dialogs, stores) + tests.

## Open questions

1. **Sidecar vs rewrite of `hub.toml`** — spec'd as sidecar with the
   refuse-shadowing rule; the owner may still prefer in-place `hub.toml`
   rewrite accepting comment loss.
2. **Tombstone retention period** — re-add purges by name; what is the
   default retention for never-re-added hosts (e.g. 7 days)?
3. **Who may add hosts** — reuse the settings-mutation admission as-is, or a
   distinct grant? Spec assumes as-is.
4. **Per-host detach** — out of scope here; natural follow-up once remove
   semantics settle.
5. **`ServerInfo.Version` constant** — owner follow-up from #1603 r2; does not
   block this component (version display reads preflight facts).
