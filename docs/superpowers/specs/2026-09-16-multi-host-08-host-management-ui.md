# Component spec 08 — Host management UI (add-host dialog, Connect, deploy/restart surface)

Status: not started. This spec is the hand-off for the implementing session.

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
2. **Connect on demand.** Attachment is purely lazy (component 05/06 ruling:
   first use dials). An offline host shows as disabled in the spawn picker with
   no way to attach it. The deferred `evener/host/attach` action is the missing
   seam.
3. **Deploy / restart from the UI.** Component 04b's machinery (version
   auto-match, deploy, restart, bootstrap-on-bare-host) exists inside
   `sshconn.Manager` but is only reachable as a side effect of `Ensure`. No
   method triggers it, no surface reports it.

The spawn host picker (06b) is hidden while zero remote hosts are configured, so
a user with no `[[hosts]]` entry sees no federation UI at all. This component is
what makes the federation operable without a terminal.

## Scope

- Controller-side hub methods for host management: `evener/host/list`,
  `evener/host/add`, `evener/host/update`, `evener/host/remove`,
  `evener/host/attach`, `evener/host/status`, `evener/host/deploy`,
  `evener/host/restart`.
- Durable persistence of host entries with a hot-apply path: the running hub
  picks up add/update/remove without a restart (registry, sshconn manager,
  source registry, web config, navigation manifest).
- A new **Hosts** settings section in the hub web UI: host list with live
  state, add/edit dialog, remove-with-confirm, Connect / Deploy / Restart
  actions, deploy confirmation showing what will be installed where.
- A Connect affordance on offline rows in the spawn host picker.

## Non-scope

- The remote-admin proxy (`app_host_admin.go`) and its forwarded allow-list are
  unchanged: the methods above are **controller-local** — they act on the
  controller's config and its sshconn manager, not on a remote hub's surface.
  They must NOT be added to `remoteHostAdminMethods`.
- Detach/disconnect of an individual host (no `evener/host/detach`; `Manager.Close`
  remains whole-manager). See open questions.
- Multi-tenancy / auth changes: the methods ride the hub's existing
  admin-mutation admission; no new capability type.
- The native client; hub web only.
- Fixing the hub's `ServerInfo.Version` constant (`"0.1.0"` at
  `cmd/evener-hub/main.go:46`, recorded follow-up from PR #1603 round two):
  version display in this component reads the preflight facts, which already
  report the remote's real build; the constant is a separate owner decision.
- Any change to lazy attachment semantics: the attached-only seams (PR #1603)
  stay exactly as landed. `evener/host/attach` is the one place that may dial,
  alongside an explicit `SourceID`.

## Contract / interfaces

### Method surface

All eight are hub-side handlers registered like every other hub method (the
router-vs-catalog test pins registration), classified as mutations where they
mutate, and admission-gated like the hub's other settings mutations.

- `evener/host/list` (read): every configured host — the full
  `HostConfig`/`hostreg.Host` fields plus live state: `attached`
  (`sshManager.ChannelIfAttached(name)`), preflight facts when known
  (installed version, OS/arch), last attach error, and whether the manager is
  currently mid-`Ensure` for it. This is the settings section's data source; it
  must include offline and unattached hosts (the navigation manifest's
  `sources` deliberately carries only registered online-visible rows).
- `evener/host/add` (mutation): body = one host entry. Validate with the
  component-03 rules (`hostreg.ValidateName`, the `[[hosts]]` field validation
  at `cmd/evener-hub/config.go:177`), refuse duplicates, persist, hot-apply.
  Surfaced validation errors are the dialog's inline errors.
- `evener/host/update` (mutation): same entry keyed by name; the manager must
  reconcile an attached host whose entry changed (detach the old channel on
  next use; see data flow).
- `evener/host/remove` (mutation): persist + hot-apply. Removing an attached
  host stops its supervisor and detaches; the UI warns when the host has
  live remote threads in the tree (rows persist as stale last-known-good; the
  removal does not delete thread data on the host).
- `evener/host/attach` (mutation): wraps `sshManager.Ensure(ctx, name)` with
  the explicit-attach semantics already exported. Errors are classified
  through the attached-only classifier chain landed in #1603 (the
  `MapAttachError`/`attachErrorClassifier` seam): an ssh-start or
  deadline chain returns typed `SessionUnavailable`, the caller's own context
  error stays raw. Returns final state (attached + facts) or the typed error.
  Idempotent: attaching an attached host returns its state.
- `evener/host/status` (read): one host's row from `list`, plus the deploy
  decision inputs: controller build (`buildinfo`), remote installed version,
  `deployRequired` decision, target path when resolvable.
- `evener/host/deploy` (mutation, long-running): wraps the 04b deploy path
  (`Manager.deploy` / `deployTarget` / `resolveDeployOrCreateTarget`) with its
  guards intact and surfaced verbatim: source/revision verification
  (`verifyBuildSource`, `verifyBuildRevision`), the terminal dirty-controller
  refusal (`errControllerDirty`), push integrity, resolved-target persistence.
  The request must carry an explicit confirmation token the UI can only mint
  from a rendered plan (see UI: deploy confirmation). Returns the durable job
  id (see long-running ops) — never blocks the RPC on the push.
- `evener/host/restart` (mutation, long-running): wraps the 04b restart path
  (user vs system unit decision, `waitHealthy` proven-replacement) and reports
  through the same long-op channel.

### Long-running operations

Deploy and restart take minutes (build, push, service restart). They must not
hold an AppWire RPC open. Shape (decide in 08b, this is the pinned constraint):
run as controller-side jobs on the existing hub jobs surface, with progress
arriving over the existing host-notification stream (07a
`SubscribeHostNotifications` + `publishHostNotification`). The UI subscribes
per host and renders progress → terminal state. If the jobs surface cannot
carry these, fall back to a manager-owned operation record polled by
`evener/host/status`; do NOT fall back to a synchronous RPC.

### Persistence + hot-apply

- **Persistence target:** the controller's `hub.toml` is hand-authored with
  comments; a TOML re-marshal would strip them. Recommended: a managed
  sidecar in the same config dir (e.g. `hub.hosts.json`), loaded after
  `hub.toml` and merged (sidecar entries win by name; `hub.toml` entries
  remain authoritative when absent from the sidecar). UI writes go to the
  sidecar only. This keeps hand-editing of `hub.toml` fully supported and the
  write path trivially atomic (temp + rename). The alternative (surgical
  rewrite of `hub.toml`, accepting comment loss) is rejected unless the owner
  rules otherwise — see open questions.
- **Hot-apply mechanics:** today `main.go` builds everything once at startup:
  `hostRegistryEntries(cfg)` → `hostreg.New` → `sshconn.New` →
  `RemoteHosts`/`RemoteHostClient`/`RemoteHostFacts`/`RemoteHostOnline` in
  `hubcore.WebConfig` (`main.go:405-488`), and `newHubSourceRegistry`
  registers one `RemoteHubSource` per host. Add/update/remove must
  transactionally: build the new registry, apply it to the sshconn manager
  (new `Manager` surface for live host-set changes, with supervisor
  lifecycle — removing an attached host stops its supervisor and closes its
  channel), swap the source registry rows, and update the web config's host
  list view (a live view, not the startup snapshot). On any apply failure the
  previous set stays; the persistence write happens only after a successful
  apply, and a boot-time load failure of the sidecar is a hard startup error.
- `hub.toml`-declared hosts and sidecar hosts behave identically once loaded;
  the `list` response marks which file each entry came from so the UI can
  show "declared in hub.toml (remove by editing the file)" vs removable.

### UI

- **Hosts settings section** (new `panes/settings/sections/hosts/*`, following
  the section pattern): host rows with state chip (online / offline /
  connecting), installed version + controller version, OS/arch, actions:
  Connect, Deploy, Restart, Edit, Remove (each with the confirm pattern used
  elsewhere in settings), and the Add button.
- **Add / Edit dialog:** fields exactly the `HostConfig` schema — `name`,
  `ssh`, `user`, `evener_path`, `config_path`, `addr` (config.go:32-44),
  each with its validation message mapped from the backend response; no
  invented fields, no fields hidden.
- **Connect state machine:** offline → connecting (in-flight, driven by the
  method response or the host-notification stream) → online (the sources
  manifest's online flag flips, the rail and picker update through the
  existing 06b/07b paths) or failed (typed error shown, retry). A Connect
  affordance also appears on offline rows in the spawn host picker; disabled
  rows stay disabled until attached — this only adds the action, it does not
  change picker semantics.
- **Deploy confirmation:** the dialog must show the plan before the user can
  confirm — target host, the controller build's revision (from `buildinfo`),
  the resolved remote target path, whether a restart follows, and the dirty-
  controller refusal reason when applicable. The confirm token mints from
  this rendered plan. This is the 04b supply-chain posture made visible:
  the user always sees what will be installed where before it runs.
- **Stores:** follow the 07b host-store pattern (`hostInstancesStore` /
  per-host request sequences / connection-generation guards as fixed in
  #1605). No shared mutable module state.

## Implementation approach (files/packages, cited seams)

- `cmd/evener-hub/internal/hostreg` — live registry: `Add` exists; add
  `Remove`/`Update` with the same cycle/validation rules, safe for
  concurrent use.
- `cmd/evener-hub/internal/sshconn` — `Manager` gains the live host-set
  surface (add/update/remove entry, each safe against in-flight `Ensure` and
  running supervisors) and thin exported entry points for deploy/restart
  reusing the 04b internals (`deploy`, `ensureDecision`, `waitHealthy`,
  `bootstrapHub`); `Ensure`/`Attached`/`ChannelIfAttached`/facts unchanged.
- `cmd/evener-hub/config.go` — sidecar load/merge + atomic write.
- `cmd/evener-hub/app_host_manage.go` (new) — the eight handlers, router
  registration, mutation classification; explicitly NOT in
  `remoteHostAdminMethods` (assert this in a test, mirroring the exact-name
  allow-list tests in `app_host_admin.go`).
- `cmd/evener-hub/main.go` — replace the startup snapshot of host entries
  with the live view seam; keep every existing `RemoteHost*` wiring.
- Frontend: `settings/sections/hosts/*`, picker Connect affordance
  (`Spawn.tsx` host rows), a host-manager store, notification subscription
  reuse.
- PR sequence: **08a** persistence + hot-apply + registry/manager surface +
  `list/add/update/remove`; **08b** `attach/status/deploy/restart` + the
  long-op channel; **08c** the frontend (settings section, dialogs, picker
  affordance). 08b stacks on 08a; 08c stacks on 08b.

## Error handling

- Attach failures: typed `SessionUnavailable` via the #1603 classifier; the
  caller-context error stays raw; the UI renders the classification, not the
  raw chain.
- Deploy refusals surface verbatim: `errControllerDirty` is terminal (the UI
  must not offer retry-anything); unmet prerequisites (curl missing, no
  writable target, user-unit vs system-unit findings per 04b) are plan-time
  errors shown before confirmation.
- Hot-apply failures: previous state stays live; the error carries which seam
  failed (registry / manager / source / web view).
- `evener/host/*` on an unknown host name: typed not-found, not a generic
  panic path.

## Testing

- 08a: registry live-update tests (including the add-time cycle rules),
  manager add/remove-vs-supervisor tests (removing an attached host stops its
  supervisor and closes the channel), sidecar merge/atomicity tests, and a
  nil-roster style wiring test mirroring the 05a registration tests.
- 08b: handler tests per method (validation, classification, confirmation
  token, plan rendering), long-op job tests, and the not-in-forwarded-allow-
  list assertion.
- 08c: `make test-web`, browser gate (`env -u DBUS_SESSION_BUS_ADDRESS make
  test-web-browser`), `make lint-generated`; per-flow tests in the section's
  test files.
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
   section and the spawn picker; success flips its online state everywhere
   (rail, picker, manifest) through the existing paths.
3. Deploy from the UI: the confirmation shows controller revision + remote
   target path; after confirm, the host reports the new version in
   `status`, and the version-skew signal (facts vs controller build) is
   truthful.
4. Hand-edited `hub.toml` hosts keep working exactly as today (merge
   semantics documented and tested).
5. No lazy-attachment regression: with no Connect click and no explicit
   source, nothing dials (the #1603 attached-only seams stay intact; add a
   test pinning that `list`/`status` never attach).
6. Standard gates green on every PR (go/build/vet, package races, full hub
   suite, module-lint; web + browser + lint-generated for 08c).

## PR size estimate (LOC)

- 08a: ~700–1000 (registry/manager/config/wiring) + tests.
- 08b: ~400–600 (handlers + long-op channel) + tests.
- 08c: ~900–1300 (section, dialogs, stores, picker affordance) + tests.

## Open questions

1. **Sidecar vs rewrite of `hub.toml`** — recommended: managed sidecar; the
   owner may prefer in-place `hub.toml` rewrite accepting comment loss.
2. **Long-op channel** — durable jobs vs a manager-owned operation record;
   decide in 08b against the actual jobs-surface constraints.
3. **Remove with live remote threads** — allowed with warning (rows go stale,
   host data untouched), or blocked until the host has no live rows? Current
   spec: warn + allow.
4. **Per-host detach** — out of scope here; natural follow-up once remove
   semantics settle.
5. **Who may add hosts** — reuse the settings-mutation admission as-is, or a
   distinct grant? Spec assumes as-is.
6. **`ServerInfo.Version` constant** — owner follow-up from #1603 r2; does not
   block this component (version display reads preflight facts).
