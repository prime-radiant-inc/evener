# Component spec 08 — Host management UI (add-host dialog, Connect, deploy/restart surface)

Status: not started. This spec is the hand-off for the implementing session.

Depends on: PR #1603 (the attached-only seams and the shared origin guard).
The symbols this spec cites that do not yet exist on main —
`sshManager.ChannelIfAttached`, the `RemoteHostClientIfAttached`/
`RemoteHostHandshake` seams, the attach classifier, **and the shared origin
guard** (bridge origin metadata plus the rule that remote-originated,
peer-forwarded requests never reach controller-side dialing or SSH-affecting
handlers) — land with #1603 and MUST be on main before this component starts.
`evener/host/attach` itself and the spawn picker's Connect trigger also ship in
#1603 (round three); this component does not extend that method. 08a extends
the shipped picker/Connect surface with the live host set (persistence +
hot-apply + `list`/`add`/`update`/`remove`); 08b adds the separate
`status`/`plan`/`deploy`/`restart`/`operations` methods; 08c adds the Hosts
settings section. Do not start this component until #1603 is landed.

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
  `evener/host/operations`, the local `evener/host/running` probe
  handler, the `evener/host/teardown-retry` repair mutation, and the
  `evener/host/orphan-resolve` crash-recovery mutation (thirteen methods
  total — twelve besides `attach`).
  `evener/host/attach` ships with #1603 and is
  unchanged here — relied on, not re-specified.
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
  test required) — **and the allow-list is not the security boundary**: the
  true security boundary is edge admission/auth (the hub's capability-token
  auth gating every non-exempt route, plus the existing admin-mutation
  admission the methods ride — see Non-scope "Multi-tenancy / auth changes"):
  any holder of the capability token is fully authorized, and a hostile peer
  holding it can omit the cooperative bridge marker (see below) and present as
  local. The origin guard is therefore an **honest-peer recursion terminator,
  not a security boundary** — it terminates an honest A→B→A forwarding cycle
  past depth 1, and the v1 trust assumption is that peer hubs are cooperative
  (the marker is client-asserted and carries no secret — #1603
  `host_routing_origin.go`: "any token holder can set or omit it — so it is
  not a boundary against a hostile peer"). Residual risk: a compromised or
  hostile token holder can invoke controller-local mutations or SSH-affecting
  methods by omitting the marker; that threat is contained by token secrecy
  (0600 token file, loopback-only bridge dial, no-proxy handshake), not by the
  guard. Every
 `evener/host/*` request (all thirteen methods, `attach` included) passes
  through the shared origin guard — **landed by #1603 at the dial seam
  (`guardRemoteHostDial`) and the remote-client dispatch seam
  (`guardRemoteDispatch`), and extended by 08a to the common
  request-ingress/router boundary as a pre-admission hook — remote-originated,
  peer-forwarded requests are refused before admission** (before dedup and
 before token validation on the 08b phases where those stages exist — 08a
 asserts the guard-before-admission ordering on its own surface), before any handler logic runs. The mutating handlers (`attach`,
  `plan`, `deploy`, `restart`, `add`, `update`, `remove`, `teardown-retry`,
  `orphan-resolve`) are unreachable
  to honestly-marked peer-forwarded requests — refused before admission for
  honestly-marked remote-origin requests (a markerless request is
  local-originated by construction and takes the full admission path — the
  guard is an honest-peer recursion terminator, not spoof-resistance, see the
  residual-risk paragraph above); the reads (`list`/`status`/`operations`/`running`) are guarded
  equally because they disclose the controller's topology and operation
  state. (`attach`'s guard routing ships and is test-pinned with #1603; this
 component pins the other twelve.) **The one direction-scoped exception is
 the `evener/host/running` peer probe: a controller-originated request issued
 only through `sshManager.ChannelIfAttached(name)` over a live channel peered
 by the #1603 handshake, admitted on the remote only over that same attached
 session (see `plan`). The exception never admits a browser- or
 forwarded-origin request, the probe is never forwarded onward to a third hub,
 and a missing or unverified handshake stays an unauthenticated-probe refusal
 — so the A→B→A chaining the #1603 guard closes stays closed.** The proxy's
 *host set* does become live
  (see data flow).
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
  test-pinned — and `evener/host/attach` remains the only user-facing method trigger of the
  attach/bootstrap cycle besides an explicit `SourceID` — the operation-owned
  `attachUnderGate` attach/reattach (restart's reattach and attach-first, plus
  deploy's planned restart, see `evener/host/restart` and `deploy`) is the
  sanctioned non-user path (extending the round-fourteen `attachUnderGate` and
  round-eleven wording) (`plan`'s gated
  preflight refresh is one of the two deliberate non-attach SSH uses (run with
  no gate held, the gate acquired only after it completes — see `plan`) — the other is the
 deploy/restart worker's post-operation preflight, the same one-shot preflight
 (see worker lifetime): bounded one-shot preflight
  command executions over the manager's transport that never initialize a
  channel or touch a supervisor; see `plan`).

## Contract / interfaces

### Method surface

All are hub-side handlers registered like every other hub method (the
router-vs-catalog test pins registration), classified as mutations where they
mutate, admission-gated like the hub's other settings mutations, **and
origin-guarded — the shared origin guard #1603 lands at the dial/dispatch
seams, extended by 08a to the common request-ingress/router boundary as a
pre-admission hook, so honestly-marked remote-originated requests are refused before
admission (before dedup and before token validation where those stages
exist — 08a asserts guard-before-admission; the fuller orderings land in
08b), before any handler runs (see Non-scope)** — and each gets
its AppWire protocol catalog entry + regenerated TypeScript client with the
exact shapes in Protocol types, below.

**Host mutations are serialized by one process-wide lock** (all of
`add`/`update`/`remove` hold it only across stage, persist, and swap plus the
remnant/receipt state transitions — released across post-commit teardowns and
re-acquired to finalize — and so does every other sidecar read-modify-write:
`teardown-retry`'s remnant clearance, retention-expiry pruning, and marker
finalization all run under the same lock for their transitions only, never
across a teardown (extending the round-eight serialization and the
round-nineteen foreign-marker pattern — r19 held the normal-commit lock
across post-commit teardowns while foreign finalization and `teardown-retry`
ran teardowns outside it, blocking every host mutation through slow teardown;
the fixed lock order with the store mutex lands in the operation store
below — the mutation lock is outermost, the store mutex innermost), so
concurrent read-modify-write on the sidecar cannot lose updates.
**Mutation idempotency:** `add` accepts an optional
opaque `mutationId` (non-empty, at most 128 bytes, no required structure —
the same rules as client operation IDs); `update`/`remove` require `mutationId` with
`expectedGeneration` (see below) — a keyless `update`/`remove` never stages. The staged commit persists a durable
mutation receipt in two writes — the single explicit receipt write point (extending
the round-ten receipt): the step-(2) sidecar write carries a transient
`pendingMutation` marker (the scoped key plus `stagedAt`, a `swapStarted`
intent (false at stage time, flipped true in its own atomic sidecar write
under the mutation lock before the runtime transition begins — see the staged
commit below; a `finalizingMutation` claim carries it as true), plus a
`teardownStarted` flag (false at stage time, flipped true in its own atomic
write after the swap and before the first teardown — see crash-window
recovery below; a `finalizingMutation` claim carries the flag as true),
plus the collision-reconcile armed intent (the validation-read `hub.toml`
fingerprint the commit staged against — see the final check in
persistence; the post-commit write replaces the marker with the finalized
receipt, clearing the armed intent with it, and the reconcile staging below
supersedes it with the re-read fingerprint when a race is found)
plus the staged provisional payload — explicitly provisional outcome, row,
generation, a pre-minted `remnantId`, and the pinned teardown target (the
in-progress remnant: the staged supervisor/channel/fan-out teardown
description for this commit, resolvable without the live entry — the step-(2)
write lands before the post-commit rebind executes the first teardown, so
the target and its remnant are durable before any irreversible teardown
destroys a handle);
a map keyed by host name — at most one staged entry per host, so a pending
marker for host A never blocks a mutation on host B (extending the
round-twenty-one global marker — r21's single global marker forced every
mutation on host B to finalize host A's marker first, so while A's teardown
held A's gate, B returned busy despite the process-wide lock being released
for unrelated hosts), and every marker write preserves other hosts' entries
verbatim), and the
post-commit write replaces the marker with the finalized mutation receipt —
the scoped key, the outcome, the resulting row, and the resulting generation
(row schema in Persistence + hot-apply, below) — and a replay carrying a
known key returns the recorded finalized receipt without re-applying: a retried `add`
cannot duplicate, a retried `remove` cannot fail not-found, and a retried
`update` cannot double-apply or double-bump the generation. A replay naming
a still-`pendingMutation` key never re-applies: while the original commit
holds the mutation lock the replay fails fast with the transient busy form
(retry with backoff); once the lock is free and the marker persists — the
post-commit write failed or its response was lost while the process stayed
alive — the replay re-runs the marker's pinned teardown to completion (the
pinned target is idempotent, so a teardown that already ran is a no-op and a
teardown interrupted by the lost write is completed) to derive the real
outcome and remnant, then finalizes the receipt from that observed result in
one atomic sidecar write and returns the finalized receipt (`committed`, or
`committed-with-teardown-failure` with the pre-minted `remnantId` when the
re-run actually failed — never the staged provisional outcome on its own),
so retries converge instead of reporting busy
forever. Any mutation-path write that finds a marker it did not stage for its own host
finalizes that marker first — a marker for a different host rides along untouched in the same atomic writes and never forces finalization, so unrelated-host mutations proceed while serializing only same-host teardown/finalize work plus the sidecar write itself — with the pinned-teardown re-run above executed
OUTSIDE the mutation lock (extending the round-eighteen marker work and the
round-fifteen lock work — r18 finalized the foreign marker, pinned teardown
included, while holding the process-wide lock, blocking every host mutation
through a potentially slow supervisor/channel teardown): the finder first
atomically claims the marker in one sidecar write under the lock — replacing
the `pendingMutation` marker with a `finalizingMutation` claim carrying the
same scoped key plus a server-generated opaque attempt token (extending the
round-nineteen lock work — r19 released the lock across the teardown with no
claim state, so a concurrent mutation or `teardown-retry` could finalize the
same marker twice with conflicting receipts/remnants): any other path finding
a `finalizingMutation` claim waits for or recovers the claim instead of
re-finalizing — a live claimant re-runs to completion before responding busy,
a dead claimant's claim (crashed or vanished holder) is adopted by re-running
the pinned teardown under the same generation/incarnation guards and
 finalizing under the claimant's attempt token (extending the round-twenty
 claim work — r20 claimed under the mutation lock but ran the pinned
 teardown with no per-host gate, while the original committer releases the
 lock across teardowns yet holds the gate until last, so a different-host
 mutation or replay could run the same supervisor/channel teardown
 concurrently with the live committer): the finder try-acquires the
 remnant's host gate after claiming and before running the teardown — a
 held gate means a live committer still owns the host, so the finder
 releases the claim back to `pendingMutation`, responds busy, and finalizes
 nothing; only a dead claim on a gate-free host is adopted — then releases the lock, runs
the pinned teardown with no lock held under the marker's
generation/incarnation guards, and re-takes the lock to persist the finalized
receipt plus real remnant (verifying the attempt token still owns the claim),
so the foreign commit is durably recoverable (receipt plus real
remnant) before the new mutation stages — before its own stage — a live
process never accumulates an orphaned marker — and `list` already shows the
committed row (disk holds the entry), so read-after-unknown converges even
before finalization lands. **Crash-window recovery (extending the
round-nineteen marker recovery — r19 finalized every pending marker as
committed-without-remnant even when the crash landed after teardown began,
losing the pinned repair target and leaving partially-torn-down resources
behind): the step-(2) write persists a `teardownStarted` flag with the marker
(false at stage time), and the commit flips it to true in its own atomic
sidecar write after the swap and before the first teardown executes — under
the mutation lock, before the mutation lock is released across post-commit
teardowns — so the
first teardown runs only after that flip is durable. The staged sidecar write
additionally persists a runtime phase with the marker (`staged` at stage
time, flipped to `runtime-swapped` in the same atomic write that flips
`teardownStarted` after a successful swap — extending the round-twenty-three
marker, which persisted the teardown flag only after the swap, so a crash
after the swap/rebind but before the flag write left the marker false and
boot finalized without a remnant though the runtime transition had partially
applied: what changes is the explicit phase persisted on both sides of the
transition): a marker found in phase `runtime-swapped` (or in an unknown
phase that follows the swap) recovers with the pinned teardown target even
  when `teardownStarted` reads false — the swap may have applied — while
  only a phase-`staged` marker with `teardownStarted: false` AND
  `swapStarted: false` is the finalize-without-remnant case (extending the
  round-twenty-four recovery, which finalized every `staged`-with-false
  marker without a remnant: a crash between the swap-intent write and the
  non-atomic runtime transition is ambiguous — the swap may or may not have
  applied — so boot recovers every ambiguous post-intent marker
  conservatively with the pinned remnant, never without one). Boot finalizes each host entry by the
flag (a leftover `finalizingMutation` claim counts as teardown-started: the
claim write sets the flag true, since a live claimant is about to run the
torn-down under the claim): a
  marker with `teardownStarted: false` AND `swapStarted: false` finalizes as
  committed with no remnant
  (no swap ran — only then is finalize-without-repair sound — and the
receipt records `bootRecovered: true` (the optional receipt field in
Persistence + hot-apply, below)); a marker with `teardownStarted: true`
recovers as a durable teardown remnant — the pinned target plus the
pre-minted `remnantId` become the remnant record, the receipt records
`committed-with-teardown-failure` with that `remnantId` plus `bootRecovered:
true`, and the operator resumes through `evener/host/teardown-retry` — so a
lost-response retry after the crash returns the recovered
receipt or the retry handle instead of re-applying, and a crash after
teardown began never loses its repair target.** Compensation's stash-restore removes the
marker with the prior bytes, so a compensated mutation leaves neither marker
nor receipt (pre-commit only: once the first teardown executes, the commit-point
rule below applies and the in-progress remnant is the forward-repair handle,
never a stash restore). **Receipt scope
— extending the round-nine receipt with the operation store's scoping:** the
dedup key is (mutationId, host name, mutation kind, host generation at
commit — the resulting post-commit generation: for `update`, the post-bump
value the same commit advances to, pinned into the receipt at stage time, so
a commit-then-replay names the generation the commit actually landed and hits
 instead of missing as superseded — plus the incarnation id minted beside
 that generation in the same atomic sidecar write), mirroring the operation
 store's (host, kind, client operation ID, generation) scope plus the same
 incarnation id (extending the round-twenty generation-only scope — r20 let
 a boot-merge collision that shares a generation with an open remnant also
 share dedup and receipts across incarnations). A replay matches only a
  retained receipt of the same name, kind, and mutationId (one rule, three
  arms — extending the round-twenty-three superseded clauses, which let the
  "matches only current" sentence read as if a superseded receipt could never
  hit: the first arm is the direct hit — same name, kind, mutationId, current
  generation, AND current incarnation id — returning the recorded receipt;
  the second arm is the superseded hit — the same key pinned to a superseded
  generation or a different incarnation sharing the generation — returning the
  recorded outcome for recovery only, never authorizing work; the third arm is
  the pruned refusal — the same key whose receipt is gone with the count/TTL
  prune (never the purge — the purge is clean-slate commits-fresh, see
  below) — refusing as `stale-entry`, never fresh-applying). A mutationId colliding
  with a current-generation
receipt of a different name or kind is refused with the typed
`conflicting-mutation-id` error — never a hit, never a re-apply under the
colliding key (the operation store's conflicting-operation-ID rule, applied
to mutations). A key with no current-generation receipt commits fresh only when its `mutationId` matches no retained receipt for that (name, kind) — a genuinely new mutation under a fresh key (cross-name replay after the purge commits fresh by the same rule, see retention below). A same-key replay whose superseded receipt was pruned refuses as `stale-entry` (pruned-generation), never fresh-applies — see the single superseded-receipt rule below. **A same-`(mutationId, name, kind)`
receipt pinned to a superseded generation (visible because the re-add purge
below has not yet run for that name, or because the superseded receipt is
still retained): that replay returns the recorded `committed` receipt instead
of a fresh destructive apply, so a lost-response `remove` retried after a
re-add recovers its outcome rather than tearing down the new incarnation**
(extending the round-sixteen dedup ordering and the round-fifteen receipt
scoping — the generation scope still decides *conflict* vs *hit*, but a
pinned same-key receipt is always a hit for outcome recovery, never a fresh
 destructive apply — including the shared-generation case: a same-key
 receipt pinned to the open remnant's incarnation returns its outcome
 against a live entry sharing the generation but carrying a different
 incarnation id, never a hit authorizing work against the live entry). A
 same-key replay whose superseded receipt was pruned by the count/TTL
 compaction bound is refused with the typed `stale-entry`
 (pruned-generation) refusal instead of fresh-applying — retained receipt
 present ⇒ return it, count/TTL-pruned ⇒ stale-entry refusal (extending the
 round-twenty-one superseded-replay clauses and the round-twenty receipt work
 — r21 let the commits-fresh clause, the return-recorded-receipt clause, and
 the pruned-generation refusal overlap with no single winner; commits-fresh
 is scoped to different-key new mutations only and the superseded
 commits-fresh clause is deleted). Purging the tombstone is the clean-slate
 path (see retention below): the purge drops that name's receipts with the
 tombstone, and a same-key replay after the purge commits fresh — there is no
 retained tombstone left to distinguish it from a fresh key, so no stale-entry
 arm survives the purge (extending the round-twenty-three prune rule, which
 kept a post-purge pruned-refusal arm with no retained state to enforce it —
 one rule survives: count/TTL-prune refuses stale, purge commits fresh).
 A mutation sent without a
key whose response is lost reconciles read-after-unknown through `list`
before any retry — `add` compares the listed entry hash for the name,
  `update` compares the listed effective row only to observe its intended row (a keyed `update` replay returns
  its receipt by the superseded-receipt rule; a missing name or a `removed:
  true` row means the keyed remove committed). **Keyless-retry rule (extending the
  round-nineteen keyless rule — r19 compared every `HostRow` field including
  volatile live state, so equality never held stably, and let a keyless retry
  omit `expectedGeneration` with no concurrent-update detection, so the claimed
  stale-entry protection was unimplementable — and extending the round-twenty-four rule, which kept the keyless path for `update` with unconditionally-committing retries: what changes is that `update` joins `remove` in the required-key shape, see `evener/host/update`): a retry that carries no
  `mutationId` never carries `expectedGeneration` either — without it a
  concurrent update between the `list` read and the retry is undetectable, so
  keyless `add` retries are non-retryable as guarded updates and the stale-entry
  guarantee covers keyed retries only (`update` and `remove` require both fields server-side
  and takes no keyless path — see below; the UI always sends `mutationId` with
  `expectedGeneration`, required together — a keyed replay returns the receipt
  before the stale check, see below; a caller-constructed keyless
  `expectedGeneration` carries no receipt to recover and is a validation
  refusal, never a guarded update). Read-after-unknown compares only the effective `HostConfig`
  fields plus `generation`/`origin` — never volatile live state (`attached`,
  `midEnsure`, `lastAttachError`) or age counters (`installedVersionAgeSec`,
  `lastAttachErrorAgeSec`, `escalationAgeSec`): once the intended effective
  row (for a lost-response keyless `add`: the listed entry hash equals the
  intended entry; for `remove`: a missing name or a `removed: true` row — a
  keyed `remove` retry returns its receipt first) is observed,
  the retry treats the mutation as committed and does not retry at all — any
  intervening change to an effective field
  breaks the equality and forces the `stale-entry` path instead of guard
  omission. A keyless `add` retry that has not yet observed its intended row
  re-reads `list` and retries without `expectedGeneration` only while the
  mutation is still uncommitted — the row still absent — so retries converge
  without double-applying. `update` and `remove` take no keyless path at all —
  both `mutationId` and `expectedGeneration` are required on each (extending
  the round-twenty-one unguarded-remove rule — r21 kept both fields optional
  and enforced the incarnation guard through a server-side unguarded-retry
  refusal, but optional fields let a first-time keyless call and a lost-response
  retry after re-add arrive identically, so no server-side refusal can tell them
  apart: optional is unworkable either way, and the chosen shape is
  required-key, not server-issued request identity): an `update` or `remove`
  missing either field is a validation refusal committing nothing (surfaced
  inline like any validation error; the UI always sends both, re-reading `list`
  first when the generation is unknown). The keyless-retry rule above therefore
  covers `add` only — a lost-response `update`/`remove` retry always carries its
  original key and follows the single superseded-receipt rule (returns the
  recorded `committed` receipt, never a fresh destructive apply), an
  `update`/`remove` carrying a fresh key against a superseded generation refuses
  as `stale-entry`, and no unkeyed `update`/`remove` ever stages against a live
  incarnation.**
**Processing order is fixed — mirroring
`deploy` step (1) (extending the round-fourteen receipt and round-twelve
scoping work, and the round-twenty-three presence rule — "FIRST" never meant
before parameter presence):** receipt dedup by (mutationId, name, kind, current
generation, current incarnation id) runs FIRST for every `add`/`update`/`remove` — subject only to
`update`'s and `remove`'s parameter-presence gates (missing `mutationId` or
`expectedGeneration` → validation refusal before any dedup lookup, see
`update`/`remove`) — a dedup match returns
the recorded receipt with no `expectedGeneration` VALUE check, gate, or remnant
validation; only a non-replay proceeds into those checks (extending the
round-twenty-five processing-order paragraph, which keyed the lookup on the
4-tuple without the incarnation id, so two incarnations sharing a generation —
explicitly allowed elsewhere — collided on the same key: a lookup carrying
only (mutationId, name, kind, generation) never hits; the incarnation id is
always compared, pinned to the live entry's current incarnation id).

- `evener/host/list` (read, never dials, lock-free): every configured host — the full
  effective `HostConfig` fields plus live state: `attached`
  (`sshManager.ChannelIfAttached(name)`), preflight facts when known (installed
  version, OS/arch), last attach error, whether the manager is currently
  mid-`Ensure`, and the **origin marker** (declared in `hub.toml` vs declared
  in the sidecar — the effective source after merge). **Last-known facts,
  their ages, and attach errors come from a manager-owned store keyed by
  the (generation, incarnation id) pair (see `status`): the `ChannelIfAttached` lookup reports
  only the current channel, so offline rows would otherwise go blank.**
 **`list` never takes the mutation lock and never prunes durably: expiry is
 in-memory filtering only (expired tombstones are omitted from the response;
 see the expiry mechanism in data flow). Durable pruning of expired
 tombstones happens on the mutation path — every sidecar mutation prunes
 expired entries in its atomic write under the mutation lock — never on the
 read path, so the read-only contract holds and `list` cannot contend with
 `add`/`update`/`remove`. The `hub.toml` fingerprint reconciliation below is
 the one exception, and it is a separate pre-handler step, not part of the
 read: before serving any admitted call — `list`/`status` included — the hub
 compares the on-disk `hub.toml` fingerprint against a cached fingerprint
 lock-free; on a match the read serves immediately from the last published
 snapshot as specified here, while on a mismatch the read still serves
 immediately from that same snapshot and the reconcile is scheduled
 asynchronously (debounced — at most one reconcile in flight, coalescing
 rapid successive edits — extending the round-twenty-four reconciliation,
 which ran the adopt-then-bump-then-clear sequence synchronously in the
 pre-handler step on every mismatch, so every `list`/`status` admission and
 every live-registry callback paid file I/O plus the mutation lock on the
 hot path and a slow disk or frequent edits stalled reads into spurious
 busy: what changes is that the lock-free fast path is authoritative for
 reads — reconcile runs async or on the mutation/`plan`/`deploy` paths only
 — while a read arriving while a reconcile holds the lock serves the last
 published snapshot instead of waiting with busy semantics, never failing
 busy for a fingerprint reason — and the async reconcile re-compares
 fingerprints on completion: when it finishes it re-reads the on-disk
 fingerprint against the fingerprint it just reconciled, and a still-present
 mismatch reschedules another pass (same debounce, coalescing further edits)
 instead of settling — an edit landing mid-reconcile and coalesced away
 can never leave live consumers on the deleted/changed host until the next
 mutation or token path (extending the round-twenty-five async reconcile,
 which scheduled with no trailing check, so a coalesced mid-flight edit was
 silently lost until unrelated work re-triggered it).**
 **Read isolation:** "no mutation lock" does not mean "no synchronization"
 (extending the round-eleven lock-free `list`). Writers publish two
 immutable snapshots through atomic pointers: under the mutation lock every
 staged commit publishes a new registry+tombstone snapshot (entries,
 tombstones, generations, high-water marks); under a small store mutex every
 last-known-store update (facts refresh, attach outcome, worker publish)
 publishes a new copy-on-write facts snapshot. `list` loads both pointers
 lock-free and joins them in memory — each half is internally consistent, so
 a concurrent swap can never tear a row — and cross-half skew is resolved by
 the existing (generation, incarnation id) key (facts whose pair no longer matches the
 snapshot's current pair for the name render absent, exactly as after
 an update). `status` reads the same two snapshots for its single row.
  **Removed-but-retained
  hosts (tombstones) appear as `list` rows with `removed: true`** and their
retained-row count — one surface, no separate retained view. Removed rows are
rendered from the tombstone's retained effective `HostConfig` (all seven
fields `HostRow` requires) plus the explicit tombstone values
(`attached: false`, `midEnsure: false`, the removed entry's origin and
generation — see Protocol types); only the facts/error optionals stay absent
per the absent-when-unknown rule.
- `evener/host/add` (mutation): body = one host entry. Validate with the
  component-03 rules (name validation, the `[[hosts]]` field validation at
  `cmd/evener-hub/config.go:177`). **Enforces the component-03 host-count
  limit: at most 63 remote hosts (the `local` entry is the 64th manifest
  source — one host over the cap fails navigation for the entire hub, not
  just the extra host). The staged commit validates the merged post-change
  live set against the cap under the mutation lock before the sidecar
  persist, and the same check runs on the boot load and the registry
  `Add`/`Update` paths, so no sidecar or runtime commit can push the
 registry over it; over-cap is refused with the typed `ErrTooManyHosts` —
 and boot over-cap is a hard startup error, not a refusal (boot returns
 nothing: a merged `hub.toml` + sidecar live set over 63 remote hosts names
 both sources and their counts and refuses to serve — the same posture as
 the boot-time hard-error duplicate and the corrupt/schema-invalid
 sidecar).**
  **Refuses any name already declared in
  `hub.toml` or held as a live sidecar entry — the duplicate refusal applies
  to live entries only: a name present solely as a tombstone is accepted
 (re-add) past the remnant gate — a re-add naming a host with an open
 teardown remnant is refused until `teardown-retry` completes that
 generation's teardown (see the remnant gate in persistence) — and the same
 staged commit purges that tombstone in its atomic
 sidecar write (see data flow)** — no duplicate live entry can exist (the
  boot-time behavior for a hand-created duplicate is a hard startup error;
  see persistence). Persist, hot-apply. Surfaced validation errors are the
  dialog's inline errors.
- `evener/host/update` (mutation): **requires the target name to be held as a
  live sidecar entry**; a `hub.toml`-declared name is refused with the "edit the file"
  explanation; a name absent from both — or present solely as a tombstone —
  is refused as not-found. **Refused
  with the typed busy error while the host's per-host gate is held** (past the
  dedup check above — a replay returns its receipt without consulting the
  gate) by an
  in-flight deploy/restart/`Ensure` (see the operation store). Mutates every
  field except `name` — **names are immutable** (they key source IDs, cached
  rows, manager state, and file entries; renaming is remove + add, documented
  in the UI). Update never inserts a new name; only `add` can. **An update
 requires `mutationId` and `expectedGeneration` together (like `remove` —
 params `{name, entry, mutationId, expectedGeneration}`, see Protocol types;
 extending the round-twenty-four update contract, which left
 `expectedGeneration` optional — see the required-key protocol change):
 presence of both is validated before the dedup check (missing either →
 validation refusal committing nothing), and `expectedGeneration` is checked
 under the mutation lock against the target's current generation before
 staging, past the dedup check (a replay never reaches it) — a mismatch is a
 typed `stale-entry` refusal that commits nothing (the UI re-reads the row
 and retries against the current generation). A delayed retry of an `update` that carries no matching
  current-generation receipt and lands after an intervening `update` advanced
  the generation carries the pre-bump generation and is refused the same way,
  so it can never overwrite fields the intervening update changed — while an
  `add`-minted new generation after a remove/re-add cycle is NOT a stale
  retry but a new incarnation, and matches the recorded receipt only under
  the same-generation scoping rule (see mutation idempotency): a same-key
  replay naming a superseded generation follows the single superseded-receipt
  rule (see mutation idempotency): a replay carrying a different `mutationId`
  than any recorded same-`(mutationId, name, kind)` receipt commits fresh,
  never re-applies — extending the round-nine/round-ten receipt scoping
  rather than adding a second incarnation rule — while a same-key replay
  pinned to a superseded generation is a hit returning the recorded
  finalized receipt, never a fresh apply and never `stale-entry`.** **An update
  that changes what a live supervisor or channel is bound to rebinds them to
  the new entry (or tears them down) as part of its staged commit — commit
  first, then rebind/teardown, gate released last (see the operation store).**
  **Every update advances the host's registry generation and, as part of the
  same staged commit, clears or (generation, incarnation id)-keys all name-keyed resolved state
  — resolved deploy targets, deployment state, last-known facts entries,
  supervisor bindings, and channel handles — before rebinding, so the next
  operation can never serve the pre-update configuration.**
- `evener/host/remove` (mutation): **live sidecar entries only** (same
  refusal for `hub.toml` names as update; a name present solely as a
  tombstone is refused as not-found, never re-removed). **Refused with the same typed
  busy error while the host's per-host gate is held (past the dedup check —
  a replay returns its receipt without consulting the gate) — remove never waits
  and never cancels**: it proceeds only on a free gate, so its staged
  supervisor/channel teardown cannot race a push or a `waitHealthy`, and a
  refused remove leaves the in-flight operation to finish and record its
  normal terminal state; a successful remove marks that host's operation
  records `host-removed` (readable history, never a dedup match; the mark
  lands with 08b's store — and a crash between the sidecar commit and the
  mark has the mark reconstructed from the tombstone at boot; see the
  operation store). Persist + hot-apply. Removing an attached host
  stops its supervisor and detaches. The removed host's cached snapshot rows
  are **not** silently dropped: the source's last-known-good snapshot is
  retained as an explicit **tombstone** (see data flow), and the UI warns
  when the host has live remote threads (host data on the remote is
  untouched). **Remove requires both `mutationId` and `expectedGeneration`
 together (params `{name, mutationId, expectedGeneration}` — both required,
 see Protocol types): presence of both is validated before the dedup check
 (missing either → validation refusal committing nothing), and
 `expectedGeneration` is checked under the mutation lock before staging,
 past the dedup check
 (a replay never reaches it) — a mismatch is the same typed `stale-entry`
  refusal committing nothing — and the UI retry path always sends both
  (re-read from `list` first when the generation is unknown). A lost-
  response `remove` retry that lands after a re-add minted a new generation
  and carries no recorded same-key receipt (a fresh key) therefore refuses as stale instead of tearing
 down the new incarnation — enforced server-side under the mutation lock
 (a `remove` carrying a fresh key against a superseded generation refuses as
 `stale-entry`: a lost response plus a re-add leaves a retry with no recorded
 same-key receipt holding no incarnation handle, and no client re-read can
 recover one — extending the round-twenty-one
 keyless rules, which kept the fields optional and guarded only the unguarded
 retry; what changes is the enforcement point, from refusing unguarded retries
 to requiring the key, so a retry always names its incarnation) — while a
 retry carrying the original key follows
  the single superseded-receipt rule above (returns the recorded `committed`
  receipt, never a fresh destructive apply) — and keyless retries are
  non-retryable as guarded updates for `add` only (`update` and `remove`
  both require the key — there is no keyless `update` path and no
  unconditionally-committing arm — see the keyless-retry rule in mutation
  idempotency, above; the UI always sends the key).**
- `evener/host/attach` (mutation): **shipped with #1603 round three** (wraps
  the dialing closure, errors classified through the #1603 attach classifier:
  ssh-start/deadline chains → typed `SessionUnavailable`, caller-context
  cancellation stays raw; idempotent). Unchanged by this component.
- `evener/host/status` (read, never dials, mints nothing): one host's `list`
  row plus the **deploy plan inputs**: controller build (`buildinfo`), remote
  installed version (from the last-known preflight facts — **stale facts are
  marked with their age**), the deploy decision inputs, the resolved remote
  target path (as of those facts), whether a restart follows, and any
  plan-time refusal known from the last preflight (`errControllerDirty` is
  terminal; missing curl, unwritable target, unit findings per 04b — the
  same four terminal arms `plan` returns as `controller-dirty` /
  `target-unwritable` / `target-missing-prereq` / `target-unit-findings`
  with `terminal: true` in the no-token response below, so `status` and
  `plan` name the same terminal conditions in the same words — and `status`
  carries the same `reason` discriminator as `plan`'s no-token arm (see
  Protocol types), so the parity is field-for-field, never words-only).
  **Last-known snapshot:** the manager owns a per-host last-known store —
  the latest preflight facts with their capture timestamp plus the latest
attach error with its timestamp, plus the last-known running
revision/health/process-start-time snapshot and the last plan-time refusal
(the inputs behind `status`'s `restartFollows` and `planRefusal` — without these a no-dial `status` read
cannot produce those fields), keyed by the host's registry (generation,
  incarnation id) pair
  and updated on every successful preflight and every attach outcome — **and
  every `plan` call publishes into it under the gate before returning: a
 (gated `plan` calls publish holding the gate; the pre-acquisition no-token
 refusals — `unattached`, `refresh-failed`, `probe-failed` — occur before
 any acquisition and publish gateless, never by acquiring the gate to do
 so: holding the gate across the SSH preflight refresh or the running
 channel probe reintroduces the slow-probe busy-refusal bug): a
  refusal publishes its `{terminal, message}` as the pair's plan-time
  refusal (a later success clears it), and every completed probe — success or
  authenticated failure — publishes the probed running revision/health plus
  the probed `processStartTime` when carried (or their absence) as the
  pair's running-state snapshot, so `status` after a plan refusal renders
  the refusal and the probed state instead of stale data from an older pair
  (snapshots from a superseded (generation, incarnation id) pair stay absent
  by the pair key above)** (extending the
  round-twenty-one generation-only store — r21 keyed facts, running-state, and
  refusal snapshots by generation alone, so the forbidden-bump rule's live entry
  at an open remnant's generation let old-incarnation facts match the new live
  generation and surface under it: pair-keying keeps them absent, preserving the
  re-add cache-clearing guarantee) —
  and `list`/`status` read it read-only, never dialing and never
  constructing facts from the channel. Removal clears the outgoing
  generation's entry only after its replacement lifecycle handles are
  drained (see hot-apply), and re-add starts its new generation with a
  cleared entry alongside the name-keyed cache clearing (see data flow) —
  facts from the removed incarnation can never surface under the new one.
  Successful deploy/restart workers publish their verified post-operation
  refresh here — keyed to the operation's pinned (generation, incarnation id) pair, so a concurrent
  mutation cannot misattribute it — before marking `complete` (see worker
  lifetime), so `status` reports the new installed version once the operation
  reads `complete`.
  This is the pure read behind the host row's informational detail —
  the deploy confirmation renders `plan`'s minted response, never `status`
  (see UI).
- `evener/host/plan` (mutation — it mints durable controller state, so it is
  classified and admitted as one): body `{name}`. Builds a **fresh plan from
  the current facts and configuration** and returns the plan plus a
  **controller-minted confirmation token**: single-use, **expiring (token
  TTL: 5 minutes by default, owner-adjustable — bounded by the plan
  freshness bound below — mint persists `expiresAt = min(configured TTL,
  freshness bound at mint)`: an owner-set TTL above the bound is clamped to
  the bound at mint, never persisted past it; lowering the bound below
  outstanding TTLs shortens their effective expiry to the bound at validate
  time, never past it — extending the
  round-twenty-four TTL work, which stated the never-above invariant with no
  enforcement when the owner sets TTL above the bound or lowers the bound
  below outstanding TTLs: what changes is the mechanism — mint clamps, and
  validate shortens outstanding TTLs to a lowered bound — extending the round-eighteen TTL work, which left the 10-
  minute default above the 5-minute deploy freshness re-check so any token
  older than 5 minutes always failed stale and the advertised window was
  unreachable: the effective deploy window is min(TTL, remaining
  facts-freshness at mint) — at the default 5-minute TTL with a 5-minute
  bound the window is the full TTL on freshly minted facts), and a token
  whose bound facts aged past the freshness bound at deploy time is a
  `stale-entry` facts-age re-plan refusal (the UI re-plans from fresh facts
  rather than retrying the token — extending the round-twenty-five window
  text, which reached this arm through an unclamped above-bound TTL while
  mint claimed to clamp: the arm is a second, independent guard checked
  before the step-(4) `expiresAt` re-check, and it fires only when the bound
  was lowered below outstanding TTLs after mint — at an unchanged bound the
  unconditional refresh plus the mint clamp keep facts age below the bound
  whenever the token is live, so expiry alone governs and the arm is
  unreachable by construction, never by contradiction))**, bound to
  (host name, the host's registry generation at mint time, a hash of the resolved host entry, **a fingerprint (content
  hash) of the controller's `hub.toml` file bytes as they are on disk at
  plan time**, the preflight revision the plan was built from, the facts capture timestamp
  (`factsCapturedAt` — the deploy facts-age re-check reads this binding, never
  the plan text alone; extending the round-twenty-four bindings, which listed
  only the preflight revision while deploy re-checked `factsCapturedAt` /
  `factsAgeSec` freshness against it — the value had no bound source, so the
  re-check was unimplementable), the resolved
  target path, controller revision, the probed running revision (see the
  running-state rule below), the probed running-health flag (also bound — a
  health change between plan and deploy invalidates the plan; see the
  running-state rule and `deploy`), the probed `processStartTime` when the
  probe carried it (bound alongside the running revision, so a process
  replacement between plan and deploy invalidates the plan even when the
  revision is unverifiable), nonce). The `hub.toml` fingerprint is
  what makes a manual edit of that file invalidate outstanding tokens at
  deploy time: the initial full load of `hub.toml` is at startup, but the
  mutation staged-commit path re-reads the file bytes on every mutation
  (validation read, final check, post-rename reconcile — see persistence)
  and `plan`/`deploy` re-hash them at mint and execution time (see
  `deploy`), so a hand edit is visible without a restart.
  **Generation binding:** the token is bound to the durable incarnation — the
  per-name generation of Host generations, below — and `deploy` validates the
 generation like every other binding — AND to the incarnation id minted
 beside it (extending the round-twenty generation-only binding — r20 scoped
 tokens, operation dedup, and receipts by generation alone, so a boot-merge
 collision that keeps the live entry at an open remnant's generation lets
 old tokens and records validate against the new live entry): every
 `add`/re-add mints a fresh incarnation id in the same atomic sidecar write
 that mints the generation, the token binds both values at mint, and
 `deploy` refuses when either differs from the registry's current pair —
 sharing a generation with an open remnant therefore never shares
 validity. Live `remove` revokes every outstanding
 token row for the name through the `pendingStoreSync` revocation intent
 (never alongside the tombstone write — the two files share no atomic
 commit), and re-add mints a new
 generation that starts with zero valid tokens, so a token minted before a
 remove/re-add cycle can never validate after it even if the re-added entry
 is byte-identical.
  **Outstanding-token rule: minting a new token for a host immediately
  supersedes any earlier unconsumed token for that host. Superseded rows do
  not accumulate:** the same atomic store write that persists the new token
  deletes the host's earlier unconsumed token rows — predecessors are already
  invalid by the rule above, so mint replaces rather than accumulates;
  expired-token reaping (lazy + boot, below) covers only tokens that expire
  unconsumed and unsuperseded. **Cross-file commit intent (extending the
  round-sixteen commit marker): token rows live in the operation-store file
  while tombstones and mutation receipts live in the sidecar — a shared mutex
  never makes two files atomic. So `remove`'s staged commit (token-row purge +
 tombstone write — the tombstone write carrying a token-row revocation
 intent, the purge applying only post-swap — and any token consume/delete that must coincide with a
  sidecar commit run a durable two-phase intent: (1) the sidecar's atomic
  write carries a `pendingStoreSync` intent (the exact store rows to
  delete/invalidate plus the sidecar generation the intent belongs to);
  (2) the store write applies it and the follow-up sidecar atomic write
  clears the intent. Token deletion therefore lands only after the sidecar
  commit's swap succeeds — the store purge runs as the post-swap step, never
  before it (extending the round-nineteen intent — r19 purged tokens
  post-sidecar-commit but pre-swap, so a swap-failure compensation restored
  sidecar bytes only and left a live host with purged tokens, violating the
  pre-commit leave-old-state promise): a swap failure before the purge leaves
  both files in the old state with nothing to compensate; a swap failure
  after the purge compensates the store purge alongside the sidecar restore —
  the purged token rows are re-inserted in the same atomic store write that
  accompanies the stash restore, so compensation resurrects exactly the
 tokens its own sidecar restore revalidates and the compensated mutation
 leaves old sidecar bytes beside old store rows — through a durable two-step
 protocol, never one cross-file atomic write (extending the round-twenty
 compensation — r20 re-inserted purged rows in "the same atomic store write
 that accompanies the stash restore", but no single write spans the sidecar
 rename and the store file, so a crash after the stash restore and before
 the reinsert left the restored sidecar without its `pendingStoreSync`
 intent and boot could not detect the lost store rows): the committer first
 persists a `pendingCompensation` record holding the purged rows into the
 operation-store file — which the stash restore cannot touch, since the
 rename replaces sidecar bytes only — then restores the prior sidecar bytes
 from the stash, then re-inserts the purged rows in a second store write,
 then clears the compensation record. Boot reconciles live compensation
 records before serving (re-inserting their rows, then clearing), so a crash
 at any point of compensation still converges to the restored sidecar's view
 with its tokens intact — the compensation record survives the sidecar
 restoration by construction, and the restored sidecar carries no
 `pendingStoreSync` intent for the generic rules below to misread. Boot reconciles both
  directions before serving
  (extending the round-eighteen intent work — r18 re-applied and dropped but
  never cleared a stale intent after convergence, so intents lingered across
  restarts):
  an intent whose store rows are still present is re-applied (the sidecar
  committed, the store lagged), and store rows with no covering intent whose
  sidecar generation already advanced past them are dropped (the store
  committed, the sidecar's compensation already restored — the rows belong to
  the compensated-away incarnation) — and once sidecar and store agree (the
  intent's store rows are already gone, including the store-already-applied
  no-op where a re-applied intent finds nothing to delete), the boot pass
  clears the stale intent in its own follow-up sidecar atomic write, so no
  converged intent survives its boot. A crash between the files therefore
  converges to exactly the committed sidecar's view, never a restored sidecar
  beside a committed revocation — and compensation resurrects only the tokens
  its own sidecar restore revalidates (the never-resurrects rule survives for
  compensated-away incarnations, never for a compensated mutation whose
  restore keeps the host live).** `plan`
  **runs its two network round-trips with no gate held, then try-acquires the
  host's per-host gate and fails fast with the typed busy error if held**
  (extending the round-fifteen gate and round-nineteen consume ordering —
  r19 held the gate across the SSH preflight refresh plus the running
  channel probe through mint, so a slow probe forced spurious busy refusals
  on `update`/`remove`/`deploy`/`restart`): the gate hold is bounded to
  validation plus the durable mint — `plan` acquires the gate only after the
  refresh and probe below complete, then re-checks attachment, the registry
  generation of the resolved entry, and facts-freshness against the refreshed
  facts (a detach, mutation, or facts advance landing between the ungated
  reads and acquisition is a typed stale-entry refusal or a re-read, never
  a plan against the superseded entry), and keeps the gate only through
  validation plus token persistence (mint + durable write), releasing before
  returning — so a fresh-facts plan cannot mint while a deploy/restart/
  `Ensure` holds the gate, but no network wait ever holds it. `plan`
  **always refreshes attached-host facts before mint (unconditional —
  extending the round-twenty-three refresh rule, which deleted the stale-only
  conditional: a plan minted from ~4:30-old facts without a refresh would die
  at deploy ~30s later, leaving the advertised TTL window unreachable — the
  plan freshness bound is 5 minutes by default, owner-adjustable, and the
  refresh is what keeps every mint inside it): a re-run of the same one-shot
  SSH preflight the deploy
  path already uses (`Manager.preflight`, `sshconn/preflight.go` — the
  `uname`/env-probe/`id -u`/`launch-check` command sequence executed over
  the manager's SSH transport), exposed as a thin deadline-bounded entry
  point and run with no gate held, bounded by the same attempt
  bound that caps the preflight phase of `ensureOnce`** — then builds the
  plan from the refreshed facts, so an online (attached) host can always
  obtain a token without a re-attach/bootstrap cycle. The
  refresh is an SSH command execution, not an attach: it never initializes
  an AppWire channel, never starts or rebinds a supervisor, and never enters
  the deploy/restart decision ladder — `ChannelIfAttached` is not involved
  (an attached channel cannot run SSH shell commands, and the channel's
  captured preflight is a cache of attach-time facts, not a fresh read). If
  the host is not attached, `plan` returns **no token** and names the
  staleness — the UI must Connect first (the attached precondition is
  deliberate: `plan` must not become a hidden prober for never-connected
  hosts). If the host IS attached and the refresh fails or times out, `plan`
  returns **no token** with the distinct `refresh-failed` reason (extending
  the round-eleven reason field): the UI surfaces retryable diagnostics and a refresh-retry
  affordance, never a Connect loop — reconnecting cannot fix an independent
  preflight failure. A token is only ever minted from fresh
  facts. **Running-state verification:** `plan` mints only for an attached
  host, so with no gate held it probes the running hub's identity
  and health through the attached channel with the `evener/host/running`
  method (catalog entry + request/response in Protocol types, below) — a
  read-only request issued only through `sshManager.ChannelIfAttached(name)`
  over the live channel (never a dial, never preflight), **deadline-bounded
  (explicit probe timeout, owner-adjustable, default ships in the
  implementing PR — a hung remote holds no gate at all: on timeout
  `plan` returns the no-token `probe-failed` refusal with nothing to release,
  extending the round-eleven reason field and the round-thirteen running
  probe)** — and records the
  response's `buildRevision` as `runningVersion` and its `healthy` flag as
  `runningHealthy`, both as `HostPlan` fields, with the running revision
  bound into the confirmation token alongside the other bindings above.
  The remote handler ships in the same 08b as `plan`: every hub serves
  `evener/host/running` locally (its own revision from the same source as
  the `controllerBuild` plan input, plus its own health) and admits it only
  over an attached session peered by the #1603 handshake — the probe
  inherits the channel's authentication and adds no new capability
  (classified as a read, never forwarded onward, never entering the deploy
  ladder) — this is the direction-scoped peer-probe exception to the r7-M4
  ingress guard named in Non-scope: the remote's ingress admits
  `evener/host/running` only over the attached session peered by the #1603
  handshake (r8-M4 provenance — controller-originated over the live channel),
  and rejects browser-origin and forwarded requests to it exactly like every
  other `evener/host/*` request. A missing or unverified handshake is an unauthenticated probe.
  `restartFollows` is computed from the fresh preflight facts confirmed
  against the probed running version: the plan says a restart follows only
  when the running hub is actually outdated — and a probed unverifiable
  revision (`"dev"` or dirty, per `evener/host/running` above) always reads
 as outdated (restart follows) — no timestamp comparison exempts it
 (extending the round-twenty-eight dev-identity work, which exempted an
 unverifiable revision whose remote `processStartTime` postdated the
 controller's `factsCapturedAt`: those timestamps come from different
 clocks with no skew bound, normalization, or comparison rule, so the
 comparison cannot prove the live process runs the desired code and a
 required restart could be skipped while an old binary keeps running.
 What changes is the removal of the cross-clock exemption — a probed
 unverifiable revision never reads as current at plan time, so the plan
 always schedules the restart and the deploy worker verifies the resulting
 process with the same-clock instance rules below; a dev revision that
 merely equals the controller build proves nothing by revision equality —
 same rule the 04b restart wait applies). If the probe read fails or is
  unauthenticated — including a remote whose hub predates the handler
  (named distinctly as handler-absent; such remotes take the one-time
  migration path below, since the token-bound deploy path cannot itself
  deliver the first probe-capable binary) — `plan` mints nothing and names
  the failure in the same no-token shape as the other refusals, with the
  discriminating `reason` field (see Protocol types) — a plan never ships
  without verified running state. **One-time migration for handler-absent
  remotes:** the first contact with a pre-handler remote takes an explicit
  version-gated bootstrap plan — `plan` returns the handler-absent no-token
  shape (never a token), and the UI offers the documented manual upgrade step
  (install a probe-capable binary out-of-band, then re-run `plan`); once the
  remote serves `evener/host/running`, the normal token-bound deploy path
  delivers all later upgrades. There is no token-bound in-band upgrade of a
  remote that cannot yet prove its running build.** The handshake version, ping liveness, and
  preflight on-disk facts feed the decision ladder but never substitute for
  the probe: none of them proves which build the live process runs.
  The token is generated by the controller, never constructed client-side —
  this is the enforcement behind "the user always sees what will be installed
  where". **Token format and validation (extending the round-eleven 0600
  file posture — randomness is this paragraph's addition, not a second file
  rule):** the token is an opaque base64url string of at least 32
  characters (≈192 bits); its nonce is drawn from a CSPRNG with at least 128
  bits of entropy and unique per mint; validate/consume compares the
  presented token with constant-time equality and never logs it. **Token
  storage and lifecycle:** minted tokens persist as rows in
  the operation-store file itself (same atomic temp+rename+fsync writes as
  operation records, same schema-validation posture at boot — a corrupt or
  schema-invalid store invalidates every outstanding token via the same hard
  startup error). Expired tokens are reaped lazily (on any token
  validate/consume pass for that host) and by the same boot pass that runs
  the interrupted transition and the tombstone-derived `host-removed`
  marking: any token past its TTL at boot is dropped, never revived, and any
  token whose host resolves at boot to removed (tombstoned) is dropped with
  it — the tombstone reconciliation runs before token revival is even
  considered, so no pre-restart token survives for a host that no longer
  exists. After the operator deletes the unvalidatable store file (the
  recovery path for the hard startup error above), the store starts with
  zero outstanding tokens; otherwise only unexpired,
  binding-intact tokens for live hosts remain valid past the restart.
  **Live removal revokes immediately:** `remove`'s staged commit drops every
 outstanding token row for the name through the `pendingStoreSync` intent —
 the tombstone write carries the revocation intent under the mutation lock
 and the store purge applies only post-swap as the compensable second phase
 (never alongside the tombstone write: the two files share no atomic commit,
 see the cross-file intent above), and the re-added generation starts with none —
  combined with the generation binding above, no pre-remove token can validate
  after a remove/re-add cycle. Token validation additionally requires the
 token's generation to equal the registry's current generation for the name
 AND the token's incarnation id to equal the live entry's — sharing a
 generation with an open remnant never shares validity (see the generation
 binding above).
- `evener/host/deploy` (mutation): **processing order is fixed.** (1) Dedup
  first: if the client operation ID matches an existing durable operation
  record **of the same host, kind, current host generation, and current
  incarnation id** (a
  `host-removed` record never matches, and neither does a record pinned to
  a superseded generation — see the operation store), return that record —
  no token validation, no consumption (this
  makes the single-use token and idempotent retry coexist: a replay after a
  lost response succeeds without a fresh token). **A client operation ID
  colliding with a current-generation record of a different host or kind,
  or with a current-generation `host-removed` record, is refused with a
  typed conflicting-operation-ID error — never a hit, never a new operation
  under the colliding ID. Only current-generation records participate: a
  colliding ID whose only matches are superseded-generation records —
  including `host-removed` records of a removed incarnation — is not a
  conflict and opens a fresh operation.**
  (2) Otherwise provisionally validate the token — **missing,
  mismatched, superseded, or expired → refusal** — as a fail-fast
  readability check only: a concurrent `plan` can mint a newer token and
  supersede the presented one before the gate is acquired, so nothing is
  decided here. A fenced name never reaches the gate: an open teardown
  remnant for the host refuses with typed `remnant-open` (naming the
  `remnantId` — see the remnant gate) before any probe or acquisition,
  past the step-(1) dedup check (a same-key replay returns its record without
  consulting the fence). (3) Acquire the host's
  per-host gate — but first probe gateless, then revalidate under the gate:
  **fail fast with the typed busy error if the gate is held at acquisition
  time** — **probe the running
  build/health over the attached channel with no gate held (same explicit
  probe timeout as `plan`'s probe), then acquire the gate and under it
  re-resolve the target, re-read the host entry, and re-hash the
  `hub.toml` fingerprint at execution time and reject if any differs from
  the token's bindings — and re-validate the pre-acquisition probe result
  (probe first, gate second —
  extending the round-twenty-three deploy step, which probed while holding the
  gate and reintroduced the same held-gate-across-network stall `plan` was
  fixed to avoid: what changes is the probe-before-acquisition ordering, so a
  slow probe never holds the gate busy against `update`/`remove`/`plan`/
  concurrent `deploy`; a change to the entry, target, or `hub.toml`
  fingerprint between probe and acquisition fails the post-acquisition
  re-read above with the same refusals — but the pre-acquisition probe's
  running-state result itself is NOT re-probed under the gate (no second
  network read while holding it), so a concurrent `deploy`/`restart` that
  completes in the probe→acquire window and changes the live running
  revision or health is invisible to the re-read — extending the
  round-twenty-eight deploy step, which claimed that window "fails the
  post-acquisition revalidation" while the re-read covered only
  entry/target/fingerprint. What changes is the cheap local close of the
  window plus the corrected claim: under the gate, the worker scans the
  operation store (local read, no network) for any operation on this host
  that reached a terminal state since the probe started — identified by
  the probe-start timestamp the worker records before the gateless probe
  against the store's durable `updatedAt` — and any such terminal
  operation is a `stale-entry` re-plan refusal (token unconsumed, no
  record), never a deploy over a possibly-replaced running process. The
  scan catches exactly the dangerous case — a concurrent operation that
  finished (hence may have replaced the process) inside the window — while
  an operation still in flight holds the gate, so acquisition would have
  refused busy instead of reaching the scan) — a probe failure or
  timeout refuses with the typed `probe-failed` refusal — the
  unavailable-class envelope discriminator
  `probe-failed` (data names the host and whether the probe read failed, timed
  out, or was unauthenticated; see the typed-error catalog below; distinct
  from `plan`'s no-token `reason: "probe-failed"` value, which is a union arm,
  never an envelope) — (token unconsumed, no operation record), distinct
  from a genuine running-version mismatch's `stale-entry` refusal (extending
  the round-fourteen probe deadline) — and reject if it differs from the
  token-bound running revision or the token-bound running-health flag —
  and reject if the re-probed `processStartTime` differs from the
  token-bound one when the token bound one (a process replacement between
  plan and deploy invalidates the plan even under an unverifiable revision
  — both values are read from the same remote clock across the two probes,
  never compared against controller wall-clock, so the check orders process
  instances without a cross-clock comparison) —
  and re-check the token-bound preflight-facts freshness (`factsRevision` /
  `factsCapturedAt` against the same 5-minute plan freshness bound): facts
  older than the bound at deploy time are a `stale-entry` re-plan refusal
  (token unconsumed, no record — the UI re-plans), so a token presented at
  minute 4 never deploys on 4-minute-old OS/arch/target-writability facts
  past the 5-minute bound at deploy time (mint clamps `expiresAt` to the
  bound, so an unchanged bound governs through expiry alone — this arm fires
  only when the bound was lowered below the minted TTL after mint, shortening
  effective expiry at validate time — see the TTL above)**
  (a sidecar edit, a manual `hub.toml` edit — caught
  by the fingerprint, the only way a hand edit is visible without a restart
  — a facts refresh, a target change, a running-build or running-health
  change between plan and deploy invalidates the plan; the UI re-plans). This re-read is the gate protocol's
  post-acquisition check (see the operation store). (4) **Revalidate and
  atomically consume the current nonce under the store mutex — still holding
  the host's gate (gate-then-store-mutex, the same order as `plan`'s
  validation-plus-mint window, extending the round-eight store mutex and the round-thirteen marker
  finalization: the consume write is one more store-mutex-serialized
  read-modify-write) — as one atomic store write: re-read the host's
  current token row and compare-and-consume its nonce against the presented
  token — and re-check `expiresAt` against the clock in that same transaction
  (extending the round-nineteen consume — r19 checked expiry only at the
  step-(2) provisional pass, so a token expiring across the gate/probe wait
  created an operation): an `expiresAt` at or before now is a `token-expired`
  refusal with no consumption and no operation created, even when the nonce
  still matches; a changed nonce (a concurrent `plan` superseded it between
  step (2) and acquisition) is a `token-superseded` refusal with no
  consumption and no operation created. On a match the same write deletes the token row and
  creates the pending operation record — consume is delete in that same
  write, never a mark — so consumption and record creation are one
  event**; return its id. **A consumed token presented again reads as
  `token-missing`: the row is gone, and gone rows never validate — so the
  consumed-then-replayed-with-new-op-ID case (a same-ID replay hits the
  step (1) dedup instead) pins to `token-missing`.** **The operation holds its host's gate from record
  creation to terminal state, pinning the validated entry and target for
  the push's lifetime** — `update`/`remove` on that host fail fast
  meanwhile (see the operation store). **Detached-during-deploy (extending
  the round-ten reattach — the matching shape is typed refusal, not
  operation-owned attach-first: `deploy` spends a single-use token, so it
  must not consume it to open a worker that then attach-firsts into an
  unvalidated channel — and the flow above is probe-first, gate-second
  (step (3): probe gateless, then acquire the gate and re-validate — there
  is no locked re-probe):** if the channel is gone at the pre-acquisition
  probe, or the post-acquisition revalidation finds the channel dropped,
  `deploy` refuses with the typed `host-detached` error — never consuming
  the token, never creating a record — directing the UI to Connect and
  re-plan (`restart` keeps its operation-owned attach-first because it
  spends no token). Never blocks the RPC on the push:
  the RPC returns the record id once the atomic consume-and-create write
  lands, and the push runs on a worker under the controller-lifetime
  context (see the operation store), never the RPC context — a client
  disconnect cannot cancel it.
  Wraps the 04b
  deploy path (`Manager.deploy` / `deployTarget` /
  `resolveDeployOrCreateTarget`) with its guards intact and surfaced
  verbatim: source/revision verification (`verifyBuildSource`,
  `verifyBuildRevision`), terminal dirty-controller refusal, push integrity,
  resolved-target persistence. **Planned-restart execution:** when the
  token-bound plan says a restart follows, the deploy worker performs that
  restart after the push (the same 04b restart path `restart` wraps) and
  verifies it — the post-operation facts refresh below must report the new
  running version — before marking `complete`. A plan that says no restart
  follows requires none. This is the execution half of the round-seven
  post-operation facts refresh: the refresh stays required, and it now has a
  precondition — the planned restart must actually have run — so `deploy`
  can no longer report success with the running hub still outdated. The
  planned restart drops the channel exactly like a standalone `restart` and
  follows the same operation-owned detach/restart/reattach sequence under
  the same gate (see `restart`), so the post-operation probe always runs
  over a worker-owned channel.
- `evener/host/restart` (mutation): same operation model (**(host, kind)**-
  scoped operation-ID dedup first (generation-scoped like `deploy` — see the
  operation store), then the remnant fence (`remnant-open` naming the
  `remnantId` past dedup and before any probe or acquisition — see the
  remnant gate), busy-fail gate acquisition, atomic record
  creation — but no token: restart has no install step, no target, and no
  plan to re-verify; instead restart binds the current `hub.toml`
  content-hash fingerprint (the same fingerprint `plan` binds into
  confirmation tokens — see `plan`) at resolution and re-checks it under the
  gate alongside the post-acquisition entry re-read — a manual file edit
  between resolution and acquisition is a typed stale-entry refusal with a
  retry instruction, never a restart under the superseded file; a mutation
  landing between restart's resolution and its gate acquisition is likewise
  a typed stale-entry refusal, never a restart of the superseded entry — and
  under the gate `restart` runs the same terminal-operation scan as
  `deploy` step (3) above (any operation on this host terminal since
  resolution started is a `stale-entry` refusal, never a restart over a
  possibly-replaced process)),
  wraps the 04b restart path (user vs system unit decision,
  `waitHealthy` proven replacement). Restart drops the attached AppWire
  channel by construction, and no supervisor/`Ensure` reattach can cover the
  worker — both need the gate the operation holds through terminal
  verification — so the reattach is operation-owned: the worker retains its
  host's gate across the drop and re-runs the `evener/host/attach` dialing
  closure for the same generation-pinned entry (no gate release, no gate
  handoff, no supervisor involvement — both stay gated out while the gate
  is held) **through the manager's internal gate-aware attach primitive —
  `attachUnderGate` (08a ships it alongside the live host-set surface): it
  accepts the already-held gate instead of acquiring it (the normal
  `Manager.Ensure` path, which acquires the non-reentrant gate, is never
  entered while holding it), and it suppresses supervisor startup — the
  worker owns the channel until terminal verification, so no supervisor can
  race it; this is the enforcement behind "no supervisor involvement" —
  then runs the channel re-probe the post-operation refresh
  requires over the reattached channel, then starts (or safely hands off to)
  a supervisor for the channel under the still-held gate before releasing it
  — the suppress-supervisor scope of `attachUnderGate` ends at verification,
  so the host keeps automatic reconnect after the restart (extending the
  round-fourteen `attachUnderGate` work). The restart worker MUST use this
  primitive — re-running the normal attach path while holding the gate is a
  deadlock (the gate is non-reentrant) and a supervisor race (extending the
  round-ten reattach work) — and every attach entry point (`attachUnderGate`
  for a caller that does not already hold the operation gate, the normal
  `Manager.Ensure` attach path, and the UI Connect action) checks the remnant
  fence before dialing: an open remnant for the name refuses with typed
  `remnant-open` naming the `remnantId` (see the remnant gate), so no attach
  can rebind a channel or supervisor over a pending teardown.** A restart issued while the host has
  no attached channel runs the same operation-owned attach first under the
  already-held gate and names the attach-first path in the record, so
  verification always has a channel and initially unattached hosts need no
  separate SSH verification path.
- `evener/host/plan`/`deploy`/`restart`/`status` on a name present solely as
  a tombstone — or absent from both files — is the same typed not-found as
  `update`/`remove`: only `list` surfaces tombstone-only names (as
  `removed: true` rows) and only the `add` re-add path accepts them.
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

- Record: operation id (controller-assigned), client operation ID, host, kind
  (deploy/restart), state (`pending`/`running`/`complete`/`failed`/
  `interrupted`/`orphan-unverified` — extending the round-twenty-five
  record, which enumerated five states while `orphan-unverified` persisted as
  a durable per-record state: the enum now names all six), the persisted
  orphan boundary identity — the tagged platform-specific boundary below
  (Linux: cgroup identity + nonce; Darwin: process-group/session + launcher-
  observed pid/start time — present exactly on `orphan-unverified` records,
  exposed as `orphanBoundary` on the wire record), progress entries
  (timestamped, bounded), terminal result,
timestamps, the **pinned host generation (the incarnation the record ran
against — part of the dedup scope, so polling after client operation-ID reuse
returns distinguishable records and the current incarnation is selected by
 generation match — plus the pinned incarnation id minted beside that
 generation, so a boot-merge collision sharing a generation with an open
 remnant never shares dedup either)**, and a **`host-removed` mark** (set when the host is removed —
  live at remove time, or reconstructed from the sidecar tombstones at boot
  (see crash recovery); still-readable history that never matches a dedup
  lookup either way). **Client
  operation IDs are opaque — non-empty, at most 128 bytes, no required
  internal structure — and deduplication is keyed by (host, kind, client
 operation ID, host generation at record creation, incarnation id at record
 creation), never the ID alone:**
  records pin the generation of the incarnation they ran against, and a
  dedup lookup matches only records whose generation equals the registry's
  current generation for the name AND whose incarnation id equals the live
  entry's — a re-add starts its new generation with
  a clean dedup slate, so client operation-ID reuse after a remove/re-add
  cycle opens a fresh operation instead of colliding with the removed
  incarnation's history. **Precedence is generation-first — extending the
  round-eight generation-aware dedup: the dedup/conflict scan ignores
  records pinned to a superseded generation entirely, and the
  `host-removed`-record conflict rule applies only to records of the
  current (live) generation.** A same-key replay returns the
  existing record; an ID colliding with a current-generation record of a
  different host or kind — or with a current-generation `host-removed`
  record — is refused with the
  typed conflicting-operation-ID error, so a reused ID can never hand back
  an unrelated operation's progress while the caller's actual operation
  never starts; an ID whose only matches are superseded-generation records
  (including `host-removed` records of a removed incarnation) opens fresh.
- **Durability:** every operation-store write is atomic (temp + rename +
  fsync), and token consumption and record creation are one such write (see
  `deploy`). **A corrupt or schema-invalid store file at boot is a hard
  startup error naming the file** (recovery is deleting it: operation
  history is lost, nothing else is). **File posture (bearer tokens persist in
  the store): the store lives in a private state dir; the store file and its
  temp files carry mode `0600` — replacements preserve the mode, and startup
  validation refuses to load a store readable beyond its owner.**
  **Retention and compaction:** the store keeps at most 50 terminal records
  per host (tunable owner knob; the default ships in the implementing PR)
  plus every non-terminal record regardless of count; exceeding the cap
  compacts oldest-terminal-first in the same atomic write that lands the new
  terminal state, so a unique-operation-ID stream cannot grow the file
  without bound. Compaction leaves a bounded dedup tombstone per compacted
  record — the client operation ID with its (host, kind, generation,
  incarnation id) scope plus the full replay fields — the controller-assigned
  record `id`, the bounded `progress` entries, `createdAt`/`updatedAt`, the
  `hostRemoved` mark, the recorded terminal outcome and `result`, and
  `compactedAt` (extending the round-twenty-four compaction marker, which
  retained the client ID, scope, terminal outcome, and compaction time only,
  so the promised replay as a full `OperationRecord` — controller ID,
  progress, timestamps, `hostRemoved` — had no retained source: what changes
  is the retained fields, so no separate compacted-operation response shape
  is needed; the marker IS the compacted record's source)
  — at most 50 tombstones per host (the same owner knob family as the
  terminal-record cap; the default ships in the implementing PR), oldest-first
  past the bound: a replay naming a tombstoned ID returns the full retained
  record — controller `id`, client operation ID, host, pinned (generation,
  incarnation id), kind, terminal state, retained `progress`, `result`,
  `createdAt`/`updatedAt`, `hostRemoved` — with `compacted: true` instead of
  opening a fresh operation (extending the round-twenty-one compaction work —
  r21 compacted the record away, so a replaying client operation ID silently
  started a new deploy/restart, contradicting the UI lost-response retry
  contract and risking a duplicate destructive operation) — and compaction
  never touches
  `host-removed` marks of retained records. Only past the tombstone bound —
  the documented, owner-visible horizon of the lost-response retry contract —
  does a replay open fresh. Every compacting write advances
  a durable monotonic compaction sequence (`compactSeq`, persisted in the
  store file) — the pagination cursor pins it (see `operations`), so a
  mid-pagination compaction is detectable instead of silently shifting
  later pages.
  **Store-wide serialization:** atomic writes alone do not serialize
  read-modify-write. Every operation-store read-modify-write path — token
  mint, token validate-and-consume, record create/update, and any dedup
  lookup that leads to a write — holds **one store-wide mutex across the
  read and the atomic write** (or runs inside a single-writer transaction
  with the same span), so concurrent `deploy`/`restart`/`plan` on different
  hosts cannot interleave reads and lose records or token consumption.
  Per-host gates serialize same-host operations; the store mutex serializes
  the shared file. **Lock order is fixed: the process-wide mutation lock is
  outermost, the store mutex innermost — a path holding the mutation lock
  (e.g. `remove`'s token-row purge) may acquire the store mutex, but no
  path holding the store mutex ever acquires the mutation lock** (gate
  holders' post-acquisition re-reads are lock-free registry reads), so the
  two locks cannot deadlock.
- **Crash recovery:** at startup, before the store serves any request, every
  record still in `pending`/`running` transitions to `interrupted` (a
  terminal unknown outcome) with a note naming the crash; a retry with the
  same operation ID gets the `interrupted` record back, and a new operation
  ID starts a fresh operation. **Orphan cleanup + remote fencing (extending the round-sixteen interrupted
  transition): an abrupt crash can leave SSH subprocesses (`Manager.preflight`
  one-shots, deploy pushes, remote deploy/restart commands) or a remote
  deploy/restart running with no live worker to own them — "no live handles
  survive restart" means no *in-process* handles, never that the OS or the
  remote stopped too (teardown remnants carry their own persisted cleanup
  handles, so post-crash teardown repair needs no resurrected in-process
  handle — see incarnation-scoped teardown). Boot itself performs no SSH: the local reap below runs
  at boot before the interrupted transition, and remote fencing is lazy — it
  lands at the next operation's guard advance, after the store is already
  serving `interrupted` records (extending the round-eighteen fencing timing
  — r18 left boot-time vs next-op fencing ambiguous; boot never blocks on
  unreachable hosts). So before the interrupted transition the boot pass (1)
  reaps local orphans: every worker SSH subprocess is spawned in its own
  process group with parent-death cleanup (`Pdeathsig` / process-group kill on
  shutdown where the platform supports it), and boot kills any residual group
  members only through an ownership-verifiable handle — never the group id
  alone (extending the round-eighteen group-id reap and superseding the
  round-nineteen pidfd persist — r18 reaped by the recorded group id, which
  is reusable by unrelated work after the original group exits, and r19
  persisted a pidfd before spawn, which is unimplementable: a pidfd cannot
  exist before its process is spawned and cannot be reused after a restart):
  before spawning, the worker pre-creates a durable ownership boundary — a
  Linux cgroup on Linux, a Darwin process-group-plus-session boundary on
  Darwin (process group id plus the session id the worker's `setsid`-detached
  launcher holds — the `linux || darwin` process-group seam in
  `agent/execenv/process_group_unix.go` and the `Setsid` detached-launch seam
  in `agent/execenv/detach_unix.go` — there is no Darwin cgroup or job-object
  primitive, so the boundary is the (pgid, session id) pair, never a group id
  alone) — plus a
  server-generated per-spawn nonce — and persists the boundary identity plus
  the nonce in the operation-store file alongside the record (the pre-spawn
  persist carries a `pending-spawn` intent holding the nonce; the worker
  spawns every SSH subprocess directly into the pre-created boundary and
  matches the intent post-spawn, so a not-yet-populated boundary can never
  authorize a kill); boot reaps by enumerating current boundary members and
  signaling only members of the persisted boundary whose nonce still matches
  — an empty boundary, or members failing the nonce check, are already clean
  — never an error, never a kill of unrelated work. On Darwin the reap runs
  process-table enumeration of the persisted (pgid, session id) pair through
  the same `agent/envctx` Darwin probe seam as `probes_darwin.go`, and every
  enumerated member is verified against a launcher-observed marker before
  signaling: the worker's `setsid`-detached launcher persists the spawned
  child's observed pid plus its process start time beside the nonce, and boot
  signals only members whose (pid, start time) still matches the persisted
  pair — a pid whose start time differs is a reused id naming a different
  process, so the boundary reads as already clean and nothing is signaled
  (extending the round-twenty-three Darwin boundary — r23 matched the (pgid,
  session id) pair plus a nonce, but the nonce has no process-table
  association, so a pair match after id reuse could not prove ownership and
  "never kill unrelated work" was unimplementable: what changes is the
  pid-plus-start-time comparison, which names the exact process instance —
  the nonce stays as defense-in-depth on the Linux cgroup path, where
  membership itself is kernel-enforced). When that enumeration is unavailable
  boot fails closed — reap nothing, keep the `pending-spawn` intent open,
  and mark the affected records with the durable `orphan-unverified` state
  instead of transitioning them to servable `interrupted` — so an
  unverifiable Darwin orphan is never silently adopted as clean.
  `orphan-unverified` is a durable per-record state (a persisted flag on the
  `pending-spawn` record, never a log-only note — the one exception to the
  boot rule above that every other `pending`/`running` record becomes
  `interrupted`): every subsequent boot retries the enumeration and resolves
  the record once it succeeds (boundary empty or start-time mismatch → drop
  the intent and transition to `interrupted`; verified members reaped →
  `interrupted`), and the operator resolves it explicitly through the
  authenticated `evener/host/orphan-resolve` mutation below (params
  `{id: string}` — the controller-assigned record id — response the
  updated `OperationRecord`: the call re-runs the persisted-boundary
  enumeration for that record under the caller's session authentication and,
  on a clean boundary (empty, or pid/start-time mismatch on every member),
  drops the intent and transitions the record to `interrupted`; on members
  still present it refuses with the transient busy form, never a force-clear)
  — the record's persisted tagged boundary (platform discriminator plus the
  platform's ownership data — Linux cgroup identity + nonce, Darwin pgid +
  session id + launcher-observed pid/start time) is visible through the
  `operations` detail filter, so the
  operator kills the listed members (or confirms them gone) and calls
  `orphan-resolve` (a hub restart re-runs the same enumeration at boot, and
  the post-recovery enumeration resolves the record the same way) — a newer
  operation ID meanwhile starts fresh only after the boundary is resolved,
  never overlapping the unverified record (extending the round-twenty-four
  orphan-unverified state, which marked the record but let a newer operation
  start "meanwhile" — overlapping an unverified crashed SSH subprocess that
  might still be running, contradicting the no-overlap rule below: what
  changes is the gate — while any `orphan-unverified` record is open for a
  host, that host admits no new lifecycle or mutation call past admission:
  `plan`, `deploy`, `restart`, `add`, `update`, `remove`,
  `teardown-retry`, `attach`, and `Ensure`-triggered work all refuse with
  the transient busy form — scoped to that host's name only (extending the
  round-twenty-seven fence, which listed `add` alongside per-host calls
  without scoping: read as global, one host's orphan blocked `add` for an
  unrelated name; read as same-name-only, the `add` arm looked redundant
  with duplicate refusal. The scope is same-name: the fence refuses
  same-name `add`/`update`/`remove`/`attach`/`plan`/`deploy`/`restart`/
  `teardown-retry` plus `Ensure`-triggered work for that host only — an
  `add` for a DIFFERENT name proceeds — and local reaping stays incomplete
  — until the boundary is verified (a later boot's enumeration resolves
  the record) or the operator resolves it through
  `evener/host/orphan-resolve` above — only then does a fresh
  operation start under a new epoch after local reap completion — only the
  read-only calls (`list`, `status`, `operations`, `running`) and the
  `orphan-resolve` way out itself bypass the persistent orphan fence). A crash between the
  pre-spawn persist and the spawn leaves a persisted-but-empty boundary: boot
  enumerates it, finds no members, and drops the intent — never an orphan,
  never a reap of unrelated work. A crash after the spawn but before the
  launcher-observed pid/start-time marker is persisted beside the nonce
  leaves a live process with no verifiable ownership metadata (extending the
  round-twenty-seven Darwin reap, which resolved any boundary whose members
  failed the pid/start-time check as clean: a record with a persisted
  boundary but no persisted post-spawn marker is indistinguishable from a
  clean boundary and would have released the host): boot treats a
  `pending-spawn` record whose post-spawn marker is absent as
  `orphan-unverified` with the host fenced — never as clean, never
  `interrupted` — until the operator resolves it through
  `evener/host/orphan-resolve` (whose enumeration likewise refuses the
  transient busy form on a marker-less boundary with members still present,
  and drops the intent only on a demonstrably empty boundary). The boot pass
  ends here — it lists only the local reap above, and remote fencing is not
  a boot step: it lands at the next operation's guard advance, never at
  startup (the "(2) fences the remote side" item the round-twenty-eight
  text numbered as part of the boot pass described the per-worker fencing
  mechanism below, not a boot-pass step — an implementer following the
  old numbering issued SSH fencing at startup, contradicting the
  boot-performs-no-SSH rule two paragraphs up and reintroducing the
  boot-time-vs-next-op ambiguity r18 settled).
  **Remote fencing (at the next operation, not boot):** the worker
  fences the remote side per host with enforcement on both ends and inside
  every mutating step (extending
  the round-seventeen epoch and the round-eighteen start-gating — r17 defined
  the epoch but neither its remote-side enforcement nor the durable local
  ownership handle, and r18 gated only command start plus terminal tag
  filtering, which cannot stop an already-running orphan from finishing a
  mutation after the new operation started): each worker
  carries a durable fencing epoch (controller boot id + per-host monotonic
  op sequence, persisted in the operation-store file alongside the record
  before the worker launches), and the epoch is presented to the remote on
  every SSH command the worker runs — corroborated by a remote-side epoch
  guard file on the target host holding a totally ordered fencing sequence
  (the guard file's own monotonic sequence, advanced only by remote
  compare-and-swap — the guard-file sequence is the total order across
  controller restarts, never the (boot id, per-host sequence) pair alone,
  which has no defined cross-restart order): the worker's first commands
  under the new epoch run through a remote lease wrapper (extending the
  round-nineteen lease file — r19 tracked prior commands but defined neither
  atomic registration before side effects nor an atomic guard check per
  irreversible action, so an untracked or already-checked old command could
  survive the guard advance and mutate afterward): the wrapper atomically
  registers each command in the per-host remote lease file BEFORE its side
  effects start, performs the command's steps only through the wrapper, and
  holds an exclusive per-host remote lease across the guard re-check and the
  irreversible action it guards — the re-check and the side effect are one
  atomic helper-mediated operation, never check-then-act as separable steps
  (extending the round-twenty-three recheck — r23 re-checked immediately
  before the action but held no lease across the two, so an old command could
  pass the check, lose the guard to a new operation's advance, then still
  perform its side effect): the guard advance takes the same exclusive lease,
  so a re-check-plus-act and a concurrent advance are mutually exclusive —
  whichever holds the lease first wins and the loser observes the winner
  (advance-first makes the check refuse server-side and abort the operation;
  check-first lands the side effect before the advance) — register, fence,
  and perform are one guarded unit per mutation, and a step whose presented
  epoch no longer equals the guard refuses server-side and aborts the
  operation. The 08b fencing tests pin the interleaving (an old epoch's
  check racing a new epoch's guard advance never lands a side effect past
  the advance). **Deadlines (extending the round-twenty-four fencing, which
  ran the kill/wait under the controller-lifetime context only, so a stuck
  remote process held the host gate and the operation indefinitely,
  contradicting the no-record-stays-stuck rule: what changes is the bound —
  the kill runs under its own bounded context (owner-set fencing-kill
  deadline; the default ships in the implementing PR) and the exit wait
  under a second bounded context of the same family: on kill/wait timeout
  the new operation fails with the terminal fencing-failure outcome — and a
  kill/wait timeout additionally persists a durable per-host
  fencing-quarantine marker in the operation-store file in the same atomic
  write that lands the terminal record (extending the round-twenty-eight
  fencing deadlines, which released the host gate on timeout with the host
  operable again: an old remote command may still be running past the
  timeout, so releasing the gate with no admission rule let a subsequent
  mutation overlap it — contradicting "never a new mutation until fencing
  is confirmed". What changes is the quarantine: the terminal record no
  longer re-opens the host — while the marker is open for a host, that host
  admits no new lifecycle or mutation call past admission: `plan`,
  `deploy`, `restart`, `add`/`update`/`remove` for that name,
  `teardown-retry`, `attach`, and `Ensure`-triggered work all refuse with
  the typed fencing-failure form naming the quarantined host — scoped to
  that host's name only, like the orphan-unverified fence in (1) above —
  and only the read-only calls (`list`, `status`, `operations`,
  `running`), the `orphan-resolve` way out, and the next operation's own
  kill/wait + guard advance (which is fenced work converging the quarantine,
  never a new mutation over it) bypass it. The marker clears only when a
  subsequent operation's kill/wait + guard advance succeeds — the fencing
  the timeout skipped is confirmed then, and the success clears the marker
  in the same atomic store write that advances the guard — or when the
  operator confirms the old remote command dead out-of-band and resolves
  through the authenticated `evener/host/orphan-resolve` mutation (which
  likewise verifies the persisted boundary before clearing). A fencing
  timeout therefore leaves a terminal record plus a closed host, never a
  terminal record plus an operable one — the host is operable again only
  after fencing is confirmed, never merely after the gate released).** The
  wrapper's first commands
  under the new epoch kill (bounded kill context) and wait for exit (bounded
  wait context) of the superseded epoch's
  already-running commands (remote kill of the prior epoch's lease-tracked
  entries with exit confirmation — each lease entry carries an ownership
  token: the remote PID's start time, or a per-spawn nonce minted by the
  wrapper at registration, or the remote cgroup/job-object membership where
  the platform supports it — verified on the remote before signaling, mirroring
  the local boundary-plus-nonce rule above, so a reused PID on the target
  host never kills unrelated work) and only then
  advance the guard (compare-and-advance — an older epoch never
  overwrites a newer one, so a
  late orphan cannot move the guard backward); every mutating remote step
  after the advance re-presents the epoch and re-checks it against the guard
  immediately before its irreversible action — a step whose presented epoch
  no longer equals the guard refuses server-side and aborts the operation.
  A superseded remote command already running is therefore killed before the
  new operation mutates — never merely ignored controller-side while its
  remote side effects land: terminal verification reading only results
  tagged with the current epoch is the backstop, not the fence. Until the
  fenced guard advance succeeds the new
  operation performs no mutating remote step (it may probe and stage locally,
  but the first remote mutation follows the kill/wait + guard advance itself), so an orphan
  that survived the crash cannot finish a mutation after the new operation
  started and overwrite its state. A new operation ID on a host with an
  interrupted record therefore starts only after local reaping completes and
  (never while an `orphan-unverified` record is open for the host — see (1)
  above) and under a fresh fencing epoch with the guard advanced past a
  kill/wait of the superseded epoch, so it can never
 overlap orphaned local or remote work from the crashed incarnation.
 **Fencing helper, install, and version gate (extending the round-twenty
 fencing machinery — r20 defined the wrapper's register/fence/perform
 semantics but neither how the wrapper reaches the remote nor what the
 deploy/restart paths that predate it must do, so a host without the
 wrapper kept every old mutating SSH command outside the fence): the remote
 lease wrapper is a versioned shell helper (`evener-fence`, version 1 —
 install path `~/.local/share/evener/fence`, version pinned in the fencing
 epoch record) installed on the target host out-of-band before the first
 fenced operation (reusing the 04b bootstrap-on-bare-host
  seam for delivery only — and first-ever contact with a never-provisioned
  host is the one exempt delivery step this paragraph otherwise forbids
  (extending the round-twenty-seven helper-absent rule, which refused
  fail-closed before any remote mutation with never an auto-install while
  routing every mutating SSH command — including the 04b
  `Ensure`-triggered bootstrap — through the wrapper: on a bare host the
  fail-closed check blocked the very bootstrap that delivers the helper, so
  a newly added bare host could never be provisioned from the UI. What
  changes is the named exemption: the first-ever-contact delivery — the 04b
  `bootstrapHub` first-attach repair, which starts a stopped hub through the
  supervisor/ad-hoc launch and ships the deployed payload with the helper
  bytes inside it — runs unfenced exactly once per never-provisioned host
  (a host the controller has never fenced an epoch on and whose sidecar
  entry carries no `helperInstalled` marker yet — never a host with a prior
  fenced epoch, a prior helper version record, or an interrupted record
  from the crashed incarnation, where prior remote work may still run and
  the unfenced window would overlap it) under the worker's persisted epoch,
  and the delivery converges the marker in the same step: the worker sets
  `helperInstalled` on the host's sidecar entry in the same atomic sidecar
  write that finalizes the bootstrap (or refuses the finalize on failure),
  so a crash before the marker lands leaves the host still never-
  provisioned (the exemption stays available and the next attempt retries
  it), a crash after the marker lands leaves the exemption permanently
  closed (the next attempt takes the fenced path), and a retry racing the
  finalize replays under dedup rather than running a second unfenced
  delivery. No other mutating step shares the exemption): verification is a
  read-only pre-fence check and
  installation is never an exempt pre-mutation step otherwise (extending the
 round-twenty-one helper work — r21 ran install-or-verify as an unfenced
 remote write before the guard advance while calling it "never a mutation",
 so the first operation on a helper-less host performed an unfenced remote
 mutation while a crashed-epoch orphan might still run); every worker verifies
 the helper's presence and version string before the kill/wait step with
 read-only remote exec — helper absent → the operation refuses fail-closed
 with the typed `probe-failed`-class fencing refusal before any remote
 mutation, never an unfenced push and never an auto-install (the operator
 installs the helper out-of-band through the one-time migration path below,
 mirroring `handler-absent`); older, incompatible, or explicitly untrusted
 helper → the operation refuses fail-closed with the same typed
 fencing refusal before any remote mutation, never an in-band migration
 (extending the round-twenty-eight migration primitive, which ran the
 superseded-epoch kill/wait as direct-SSH reads plus a two-consecutive-
 empty-reads quiescence check before the guard advance: an older or
 explicitly untrusted helper may hold untracked commands, its lease listing
 cannot be trusted complete, and a command can start after the final read
 and before the guard advance — two empty reads are not an atomic quiesce,
 so the advance could still overlap unverified remote work. What changes is
 the deletion of the in-band path — no direct-SSH kill/wait, no empty-reads
 quiescence check, no guard advance over an untrusted helper — because only
 a trusted atomic remote quiesce/lock primitive held by the current helper
 could close the read-to-advance window, and the old helper is definitionally
 not that primitive: until such a primitive ships, the host upgrades only when
 the operator installs the pinned helper version out-of-band, exactly like
 the helper-absent path).
 Only a remote that cannot run the helper at all (no
 POSIX shell at the target path) refuses fail-closed until the operator
 upgrades the remote itself out-of-band.
Migration: remotes first contacted by this
 component predate the helper, so deploy/restart on them refuses fail-closed
 until the operator installs the helper out-of-band (the next operation's
 pre-fence verification runs a helper self-test
 round-trip before the kill/wait) before any mutating step; a remote whose platform cannot run
 the helper (no POSIX shell at the target path) stays fail-closed for
 deploy/restart — reads and `plan` still serve — until the operator
 upgrades it out-of-band. An older, incompatible, or explicitly untrusted
 helper is likewise an out-of-band case — deploy/restart on it refuses
 fail-closed with the same typed fencing refusal until the operator installs
 the pinned helper version out-of-band, so the host upgrades and
 deploy/restart proceed only through the trusted wrapper, never through an
 in-band advance over an untrusted one. Routing: EVERY mutating SSH command the
 controller issues to the host — deploy pushes, remote deploy/restart
commands, and the 04b `Ensure`-triggered
 paths — runs through the wrapper under the worker's epoch (the 04b paths
 under the Ensure operation's own persisted epoch above, never a borrowed or
 unrecorded epoch); no direct-SSH
 mutating path survives alongside it (a command that cannot present an
 epoch is refused by the guard file rule above — the only exception is the
 read-only pre-fence verification above, taken before the kill/wait with no
 remote state written).
`Manager.preflight` one-shots (`uname`/env-probe/`id -u`/`launch-check` over
`runRemote` in `cmd/evener-hub/internal/sshconn/preflight.go` — strictly
read-only, no remote state written) are exempt from the wrapper and from
mutation fencing entirely: the gateless `plan` refresh runs them with no
worker epoch and no operation lifecycle (extending the round-four preflight
mechanism and the round-twenty-three fencing, which classified every
preflight one-shot as a mutating SSH command requiring a worker epoch — that
classification left `plan`'s gateless preflight with no valid execution
model). A host without the
 installed helper therefore cannot accept a remote mutation at all — the
 no-overlap guarantee holds vacuously, never as an unfenced exception.**
  **The same boot pass, after the sidecar is
  loaded and after the interrupted transition, applies every loaded
  tombstone to the store: each tombstone (removed host name) marks that
  host's records `host-removed`, but only records whose pinned
  (generation, incarnation id) pair matches the tombstone's persisted
  (removed generation, removed incarnation id) pair — i.e. records of the
  removed incarnation, never a newer live one and never a live incarnation
  sharing the removed generation with a different incarnation id (the
  allowed same-generation collision with an open remnant — generation-only
  matching would mark the new live incarnation's records as removed).
  (For a tombstone colliding with a live re-add carrying a different
  incarnation id no record matches, so the live incarnation stays unmarked;
  for a tombstone with no re-add only the removed incarnation's own
  records match — and a crash between `remove`'s sidecar commit and its
  live mark recovers exactly the removed incarnation's records the same
  way.) Tombstones colliding with a
  live `hub.toml` host (or a live sidecar entry from a re-add) still keep
 their mark for the pre-collision records; **the collision is then treated as
 a new incarnation — the live host is assigned a generation strictly above
 the tombstone high-water mark before the historical marks apply — and the
 merge rule discards
 the colliding tombstone, so the pre-collision `host-removed` records stay at
 or below the high-water mark while the live incarnation's current-generation
 records sit strictly above it, unmarked and out of the never-match rule
 (live records stay unmarked, and operation-ID reuse against the live
 generation opens fresh).** This replaces the
  round-5 mark-before-discard order: mark-before-discard mislabeled the
  live name's history and made op-ID reuse a conflicting-reuse refusal, so
  the mark is now generation-scoped — consistent with the round-7
  generation-bound tokens, which use the same per-name generations.** A remove's
  sidecar commit and its
  operation-store mark are two separate durable writes (different files), so
  a crash between them leaves the mark to be reconstructed here — the
  never-match rule holds via the live mark OR this boot reconciliation.
  **Cross-file commit marker (extending the round-fifteen boot fingerprint
  and the round-eighteen mirror preservation — r18 scoped the store-newer
  rollback but left the sidecar-newer direction unsynchronized, so a crash
  after the sidecar write and before the mirror write booted with a stale
  mirror and a later sidecar delete let a re-add reuse an old generation):
  every sidecar commit that also advances a mirrored store-side generation
  writes a commit marker — the (name, sidecar generation, store-mirror
  generation) triple — into the sidecar's atomic write; boot recovery orders
  sidecar load first, then the tombstone-derived `host-removed` pass above,
  then bidirectional generation-mirror reconciliation before serving any
  request: a store mirror newer than the sidecar
  mark for the same name with no matching sidecar commit marker (a store write
  that survived its sidecar's compensation) is rolled back to the sidecar mark
  before any token/record validation, so a compensated mutation can never boot
  with an advanced generation invalidating valid tokens and records — and a
  sidecar mark newer than the store mirror for the same name (a crash after
  the sidecar write but before the mirror write) is durably pushed forward
  into the store mirror in the same boot pass, so the mirror is never left
  stale behind the sidecar — and the
  rollback applies ONLY when the sidecar carries a live entry or tombstone
  for the name with a valid marker triple to roll back to (extending the
  round-sixteen commit marker and the round-seventeen cross-file work — r16
  defined the marker without scoping the rollback, and r17 left the
  max-of-both restoration unconditioned): when the sidecar is missing or held
  no entry for the name (deleted sidecar, compensated-away incarnation, or a
  name the sidecar never knew), the store mirror is PRESERVED — never rolled
  back, and the max-of-both restoration below reads the surviving mirror as
  the high-water mark — so discarding a corrupt sidecar can never discard the
  surviving high-water mark and let a re-added host reuse an old generation.
  The two branches therefore converge differently before the store serves:
  a name with a valid sidecar marker triple rolls back to the sidecar mark
  (never the maximum — the mirror's newer compensated-away generation is
  discarded — and the discarded generation is provably unreferenced
  (extending the round-twenty-seven commit marker, which discarded the
  mirror's newer generation with no invariant on who references it: a future
  mutation could have reused a generation pinned by dedup or pagination.
  What changes is the stated and boot-enforced invariant — the mirror may
  advance past the sidecar mark only through the compensable second phase
  of a mutation whose sidecar restore revalidates exactly the purged rows:
  a compensated-away generation therefore never owns an `OperationRecord`,
  and never backs a dedup key — and
  boot asserts exactly that durable half before discarding (any record or
  dedup entry naming the discarded generation is a hard startup
  error, never a silent drop). Cursors are opaque, stateless, client-held
  wire values — boot cannot enumerate outstanding client cursors, so the
  discarded generation is NOT provably unreferenced by cursors and boot
  asserts nothing about cursor-pinned boundaries (extending the
  round-twenty-eight commit marker, which asserted exactly that: a stale
  cursor naming the discarded generation is unobservable at boot, and a
  later update could reuse the discarded number and make the old cursor
  look valid again. What changes is the lazy rule — a stale cursor is
  caught when presented, never at boot: the handler's existing
  bounds/`presenceEpoch`/`compactSeq` validation refuses it with the
  established `stale-entry`/`cursor-invalidated` refusals — and the
  high-water floor: the discarded generation is preserved as the name's
 high-water mark (boot records the discarded number into the sidecar's
 per-name high-water mark in the same atomic sidecar write that performs
 the rollback — the mark outlives the rolled-back store mirror by
 construction), so no later mutation ever reuses it and no recycled
 generation can resurrect a stale cursor), so no later mutation reuses a
 referenced
 generation); a name with no sidecar mark preserves the surviving mirror,
  and the max-of-both restoration below reads that mirror alone as the
  high-water mark.**
  Records reach the store only through the
  atomic consume-and-create write, so no crash window can consume a token
  without leaving a recoverable record. No record can stay stuck forever.
- **Per-host operation gate:** at most one deploy/restart per host at a time;
  the gate is shared with `Ensure`-triggered deploys and supervisor activity
  so an auto-deploy and a user deploy cannot interleave (the 04b
  `errControllerDirty` posture applies across all of them). **Acquisition is
  try-acquire — nothing waits on a held gate: a new `deploy`/`restart`, a
 `plan` (which always try-acquires before validation and mint, after its
 ungated refresh/probe), or an
  `update`/`remove` that finds the gate held fails fast with the typed busy
  error naming the in-flight operation.**
  **Holder classes:** when the gate is held by a deploy/restart operation the
  busy error names that operation (its operation id — open/wait-able) —
  including an `Ensure`-triggered deploy, which is a durable fenced operation
  holding its own op-store record (see below), so its busy error names the
  Ensure operation the same way; when it
  is held by `plan`'s validation-plus-mint window — which holds
  no operation-store record — the busy error is the typed transient form
  (`host busy (plan in progress)`) carrying no operation reference, and
  the UI shows retry-with-backoff with no open/wait affordance. **Ensure-triggered
  mutations are durable fenced operations (extending the round-twenty-one
  fencing work — r21 required every Ensure mutation to run under a durable
  fencing epoch while Ensure work held no operation-store record, so it had no
  persisted epoch or ownership boundary for crash recovery): the Ensure path
  mints a server-side client operation ID, persists the op-store record with
  its fencing epoch (controller boot id + per-host monotonic op sequence)
  under the gate before launching the worker, and the worker runs the same
  register/fence/perform guard advance — a crash mid-Ensure reaps and fences
  exactly like a user deploy, and no Ensure remote mutation precedes its
  persisted epoch/ownership record — and the Ensure path checks the remnant
  fence before minting that record: an open remnant for the host refuses the
  Ensure-triggered operation with typed `remnant-open` (naming the
  `remnantId`), never a fresh epoch over a pending teardown.**
  **The gate pins the token-bound host entry and resolved target for an
  operation's lifetime: while a deploy/restart holds its host's gate,
  `update` and `remove` of that host fail fast** (and `add` cannot collide —
  the held host exists, so its name is refused as a duplicate), so the "sees
  what will be installed where" binding cannot be broken mid-push.
  **Gate/mutation-lock ordering:** `update`/`remove` check the gate under
  the process-wide mutation lock and fail fast if it is held, and a gate is
  not grantable to a new operation while the mutation lock is held — a
  mutation that observes a free gate owns it through the staged commit, and
  no operation can start under a mutation in flight. **Post-acquisition
  re-read:** an operation resolves and validates against the host entry
  *before* acquiring the gate, and a mutation can legally commit in that
  window (the gate is grantable whenever the mutation lock is free), so
  every gate holder — `deploy`, `restart`, or `Ensure`-triggered work —
  re-reads the registry entry immediately after acquisition and verifies
  its hash/generation still matches what it resolved; a mismatch is a typed
  stale-entry refusal (`deploy`'s token-binding check is this rule with the
  token's bindings as the reference; `restart` refuses; `Ensure` re-resolves
  from the live registry) — never a proceed on the superseded entry.
  **Mutation rebind ordering:** `update`/`remove` rebind or cancel the
  supervisors and channels bound to the superseded entry as part of the
  staged commit — the commit lands first (sidecar persist + runtime swap),
  the rebind/teardown completes, and the gate is released LAST, so no gate
  waiter can acquire a half-rebound host and no supervisor reconnect can
  race a half-swapped entry. This gate is the
  "safe against in-flight `Ensure`" mechanism of the 08a manager surface.
- **Worker lifetime:** the push/`waitHealthy` work runs on an async worker
  under a controller-lifetime context, never the RPC context: the RPC
  context is used only for admission and the atomic consume-and-create
  record write, and the RPC returns once that write lands — a client
  disconnect cannot cancel a persisted pending/running operation. Only
  controller shutdown cancels workers: shutdown cancellation transitions
  the operation to `interrupted` (with a note naming the shutdown) and
  releases its host's gate, so no gate or record is left unresolved.
  **Post-operation facts refresh:** a deploy/restart worker that reaches
  terminal success first performs a verified post-operation preflight (the
  same one-shot SSH preflight as `plan`'s refresh, deadline-bounded) and
  re-probes the running build/health over the attached channel —
  extending the round-seven post-operation facts refresh and the round-eight
  running-state rule: the refresh's preflight facts alone never prove the
  live process caught up, so the probe confirmation is what the worker
  verifies before marking `complete` — and publishes the refreshed facts to
  the (generation, incarnation id)-scoped last-known store
  (see `status`) — keyed to the operation's pinned (generation, incarnation id) pair, so a
  concurrent mutation cannot misattribute them — and only then marks the
  operation `complete`. A restart worker additionally confirms the probe
  reports the post-restart build; a deploy worker whose plan said a restart
  follows confirms the planned restart ran and the probe reports the new
  version. **Process-instance verification after a restart is same-clock only
  (extending the round-twenty-eight `processStartTime` binding, which
  compared a remote wall-clock timestamp against the controller's
  `factsCapturedAt` with no skew bound — the H1 cross-clock rule removed from
  `plan` above): the worker records the pre-restart `processStartTime` the
  pre-operation probe returned, and after the restart the post-operation
  probe's `processStartTime` must differ from that pre-restart value (both
  read from the same remote clock, so no cross-clock comparison is involved —
  a changed value proves process replacement, an equal or absent value does
  not). Under a verifiable revision the worker additionally requires the
  post-restart build to equal the deployed revision; under an unverifiable
  revision (`"dev"` or dirty) the changed `processStartTime` alone is the
  success signal — revision equality proves nothing there. A worker that
  cannot verify the refresh or the probe — including an unchanged or absent
  post-restart `processStartTime` — records the
  failure verbatim in the operation record instead of marking clean success.**
- Progress is readable via `evener/host/operations` (guaranteed). If the
  implementing session extends the host-notification stream with a
  controller-originated best-effort event class, the UI may render from it
  but MUST fall back to polling; polling-only is a fully valid implementation.
- The UI renders progress → terminal state. Never a synchronous RPC.

### Persistence + hot-apply

- **Persistence target:** the controller's `hub.toml` is hand-authored with
  comments; a TOML re-marshal would strip them. The UI writes a managed sidecar
  in the same config dir (e.g. `hub.hosts.json`), loaded after `hub.toml`.
  **Config-path retention:** the hub supports `--config` paths
  (`cmd/evener-hub/main.go:186` — `deps.loadConfig(opts.configPath)`), but
  neither the runtime `Config` nor `WebConfig` retains the selected path, so
  08a carries the canonical config path (absolute, resolved at startup) through
  startup into the web configuration; both the sidecar path (same dir as the
  selected `hub.toml`) and the `hub.toml` fingerprint bytes (read from that
  same path at plan/mint, deploy/validate, and mutation stage/final-check
  time) derive from it — UI mutations against a `--config` hub land beside
  the selected file and invalidate against the same file. **File posture:**
  the config dir's private state holds mode `0600` for the sidecar and the
  operation store — temp files created `0600`, atomic renames preserving the
  mode, and startup validation refusing to load a sidecar/store readable
  beyond its owner (the bearer confirmation tokens persist in the store).
  **Merge rules:** `add`/`update` refuse any name already declared in
  `hub.toml` (the file is authoritative for its own names); `remove` deletes
  only sidecar entries; a `hub.toml`-declared host can never be shadowed or
  UI-removed — its `list` entry says "declared in hub.toml". The sidecar is
  the UI's only writable source. **Every sidecar mutation re-reads the
  current `hub.toml` bytes under the mutation lock and validates the staged
  change against them — reusing the same `hub.toml` content-hash fingerprint
  the plan/deploy path binds into confirmation tokens (see `plan`), or an
  equivalent fingerprint comparison: if the file changed since startup (or
  since the last mutation), a staged name now colliding with a live
  `hub.toml` entry is refused, and a collision already persisted into the
  sidecar by an earlier racing edit is rebased (the sidecar live entry
  dropped, the mutation retried against the re-read file) rather than
  committed as a duplicate. **Final check:** between staging the new sidecar
  bytes and the atomic rename — still under the same mutation lock — the
  commit re-reads the `hub.toml` bytes once more and compares the content-hash
  fingerprint against the validation read; if the file changed in between
  (an external editor landing between validation and rename), the staged
  bytes are discarded and the stage-validate sequence retries against the
  new file (bounded retries, then the conflict-class envelope discriminator
  `concurrent-edit` — data carries the `hub.toml` fingerprint the commit
  staged against plus the fingerprint observed at refusal, so the client can
  re-read and retry; see the typed-error catalog below) — no
  sidecar commit is *intended* to land against a superseded file read —
  but the re-read + rename is not an atomic compare-and-swap, so the
  guarantee is reconciliation, not prevention: after the rename the commit
  re-reads `hub.toml` once more, and if the file changed across the rename,
  the mutation reconciles forward — extending the round-eleven collision and
  the round-nine hub.toml-authority work: a newly colliding live entry drops
  the committed sidecar duplicate in a follow-up atomic write under the same
  lock AND immediately rebuilds + applies the merged runtime host set
  (registry, manager bindings, sources, controllers — the step-(3) swap with
  the re-read values, not a deferred pickup), so disk and live state agree
  before the mutation returns; other changes are picked up by the
  fingerprint-bound invalidation at the next mutation or token validation —
  rather than claiming no commit
  against a superseded read ever lands. This extends the round-4/5
  fingerprint mechanism and narrows the round-eight final-check guarantee:
  the same content hash that `plan` binds into confirmation tokens still
  gates the commit, but the commit point can no longer promise the file was
  untouched — only that any race is detected and reconciled.** **A name found in
  both `hub.toml` and the
  sidecar's live entries at load time is a hard startup error naming both
  locations — UNLESS the collision is covered by a managed sidecar marker
  the previous process left behind:** the reconcile write above records a
  managed pending marker (the re-read `hub.toml` fingerprint the reconcile
  is running against plus the sidecar live entry it is about to drop) in the
  same atomic sidecar write that stages the follow-up cleanup — the re-read
  fingerprint is durable before the cleanup rename lands, never after it
  (extending the round-twenty-five reconcile, which persisted the pending
  marker only after the cleanup rename, so a crash or write failure in
  between left a legitimate duplicate unmarked and boot hard-failed it) —
  and the commit armed the intent one write earlier (the step-(2) staged write
  carries the validation-read fingerprint — see mutation idempotency), so a
  crash between the commit rename and the reconcile staging write is covered
  too: when boot sees both live entries AND a pending marker whose fingerprint
  matches a re-read of the on-disk `hub.toml`, boot completes the interrupted
  cleanup (the follow-up drop + runtime rebuild) instead of the hard error —
  and with no marker at all, boot still completes the cleanup against the
  on-disk file (clearing the armed intent in the same write) when the staged
  armed intent is present and the on-disk fingerprint differs from the armed
  validation fingerprint — the commit raced a `hub.toml` change across its
  rename (a concurrent hand edit races identically and reconciles identically:
  `hub.toml` wins, the sidecar duplicate drops) — the race the spec
  reconciles is recoverable across the whole crash window, never just past
  the staging write. A collision with no marker and no armed intent, or with
  a marker whose fingerprint no longer matches the on-disk file (a further
  hand edit superseded the interrupted reconcile), or with an armed intent
  whose validation fingerprint still matches the on-disk file (no race crossed
  the commit — the duplicate is hand-made), stays the hard startup error —
  the marker plus the armed intent scope the recovery to exactly the race the
  spec can reconcile, never a hand-created duplicate (extending the
  round-thirteen hub.toml-authority work; crash-window test in Testing).**
  The
  refuse rule is enforced at write time AND the boot merge rejects the
 hand-edited collision. Writes are atomic AND durable: every sidecar,
 receipt, remnant, cleared-marker, and stash write is temp-file +
 file-fsync + rename + parent-dir-fsync — the temp file is fsynced before
 the rename and the containing directory is fsynced after it (the same
 posture the operation-store Durability paragraph states as "temp + rename
 + fsync" — verified: the op-store rule already carries the fsync, so this
 extends that posture to the sidecar family rather than inventing it —
 extending the round-twenty-eight sidecar work, which required temp-file
 plus rename only: rename alone orders the name, never the content, so a
 crash could boot with a truncated sidecar while recovery assumed the
 renamed bytes were complete. What changes is the fsync pair: an fsync
 failure before the rename aborts the write with the old file intact (the
 mutation reports the failure and commits nothing), an fsync failure on
 the directory after the rename is a crash-window boot reconciliation
 (the rename landed but durability is unproven — boot re-reads and
 schema-validates before serving, taking the same hard-startup-error path
 as a corrupt file), and the same pair covers the stash write, the stash
 restore rename, the `pendingCompensation` record, the purged-row
 re-insert, and the compensation-record clear — every durable step of the
 cross-file protocol, never just the sidecar commits). **The sidecar
  file also carries the tombstone records (see data flow), so a removal's
  entry-delete + tombstone-write is one atomic write and the tombstone set
  cannot diverge from the host set; a tombstone whose name matches a live
  host at boot is discarded — the live host wins. A sidecar that fails
  schema validation or is corrupt at boot is a hard startup error naming
  the file — the same posture as the duplicate-name collision (recovery:
  fix or delete the file; only sidecar state is lost).** **The sidecar file
  also carries the mutation receipts and teardown-remnant records (sibling
  of the idempotency receipts): `mutationReceipts` maps the scoped receipt
key (mutationId, host name, mutation kind, resulting post-commit generation
  — see Receipt scope, above — plus the incarnation id minted beside that
  generation in the same atomic sidecar write (extending the round-
  twenty-four persisted map, which keyed and valued generation-only, so a
  boot-merge live entry sharing an open remnant's generation could neither
  match its own receipt nor collide safely with the remnant incarnation's:
  the persisted key now enumerates the full dedup key — (mutationId, host
  name, mutation kind, post-commit generation, incarnation id) — and a lookup
  compares all five) to
  `{outcome, row, generation, incarnationId, committedAt, remnantId?, remnantResolvedAt?, bootRecovered?}` —
  `incarnationId` is the incarnation the receipt was pinned to at commit
  (mirroring the `incarnationId` on every `OperationRecord` — a receipt for
  a superseded incarnation stays addressable after the shared-generation
  boot-merge, and two incarnations sharing a generation never collide on
  the same key) —
 `remnantId` present exactly when the commit staged a remnant (see the commit
 point), `remnantResolvedAt` present exactly after `teardown-retry` resolves
 it (see the commit point), `bootRecovered` present (as `true`) exactly when
 boot finalized a crash-window `pendingMutation` marker (see crash-window
 recovery above); `prunedReceipts` maps the full pruned scope key
 (mutationId, host name, mutation kind, pruned post-commit generation,
 pruned incarnation id) to `{prunedAt}` — the bounded markers the
 count/TTL compaction persists (see retention below), riding the same atomic
 temp+rename writes and the same hard-startup-error posture; `teardownRemnants` maps the
 server-generated opaque `remnantId` to `{host, kind, seam, pendingTeardown,
 generation, incarnationId, mutationKey, committedAt, cleanupHandle}` —
 `cleanupHandle` is the independently actionable ownership/remote-cleanup
 handle persisted at commit (see incarnation-scoped teardown below),
 resolvable without any live in-process handle —, and a cleared remnant persists as
 the cleared-remnant marker `remnantId → clearedAt` in the same section
 (see `teardown-retry`), purged only by the name's next re-add or the
retention-expiry prune. **Cleared-remnant markers carry their own bounded
retention independent of tombstones (owner-set cleared-marker TTL — a live
host's repaired failures create no tombstone, so without this the markers
accumulate forever): every boot and every sidecar mutation compacts cleared
markers past the TTL in the same atomic write, and lost-response retries past
the TTL read as `teardown-unknown-key` not-found instead of
`already-cleared`.** The 08a commit-point tests assert these exact
 fields (including the `prunedReceipts` scoped key and the receipt
 `incarnationId` — the receipt test pins the full five-part key). Both sections
 (`pendingTeardown` is the self-contained generation-scoped teardown target —
 the staged supervisor/channel/fan-out teardown description pinned at commit,
 resolvable without the live entry — extending the round-eleven remnant; a
 remnant never depends on the live registry to execute). Both sections
  ride the same atomic temp-file + file-fsync + rename + parent-dir-fsync writes as entries and tombstones (the
  commit, the `teardown-retry` clearance, and the re-add purge below are all
  single writes), and the same hard-startup-error posture on corrupt or
  schema-invalid content. Retention:** receipts and remnants are per-host and
  per-generation — re-add purges that name's superseded-generation receipts
 and — only after that name's open remnants are resolved (see the remnant
 gate below) — its stale
 remnants are dropped in the same atomic write that mints the new generation, so the
  sections stay bounded by the live host set plus at most one superseded
  generation per name for removed names; active (live, never-removed) hosts
  compact superseded receipts the same way (extending the round-fourteen
  receipt schema and the round-eighteen per-key bound — r14 defined the
  receipt but no active-host rule, r16
  covered cleared markers only, and r18 kept one superseded receipt per
  unique mutationId with no bound, so an active host's repeated updates grew
  the sidecar without bound): the same atomic sidecar write that finalizes
  a mutation receipt drops that name's receipts pinned to earlier generations
  — the live set keeps the current generation's receipts (at most one
  finalized receipt per (mutationId, kind) at current generation) plus at
  most 8 newest same-key superseded receipts per name (owner-adjustable
  count bound; a same-key superseded receipt older than the owner-set
  superseded-receipt TTL compacts the same way), never the full per-
  generation history, so repeated updates cannot grow the sidecar without
  bound. A replay naming a pruned receipt is not a hit — EXCEPT a replay
  naming a pruned same-key superseded generation is refused with the typed
  `stale-entry` (pruned-generation) refusal instead of fresh-applying
  (extending the round-eighteen prune rule and the round-seventeen receipt
  pruning — r17 pruned, and r18 fresh-applied past the prune, so a
  `remove` retry past the prune destroyed the re-added
  incarnation): the newest same-key superseded receipt per name is retained
  until that name's tombstone expires (never pruned by the count/TTL bound
  above while the tombstone lives); a same-key `remove` retry naming a
  count/TTL-pruned generation therefore never fresh-applies against the
  re-added incarnation — the UI's `expectedGeneration` guard is the first
  line, this refusal the backstop — and a keyed replay naming any pruned
  generation likewise refuses as stale rather than committing fresh. The
  pruned-generation refusal is enforceable because every count/TTL
  compaction that drops a superseded receipt persists a bounded pruned
  marker keyed by the full receipt scope (mutationId, host name, mutation
  kind, pruned post-commit generation, pruned incarnation id — extending
  the round-twenty-four prune rule, which dropped pruned receipts with no
  retained marker, so a retry naming a pruned key was indistinguishable
  from a never-seen key and fresh-applied past the prune, contradicting
  the stale-entry rule above): markers carry the scoped key only (no row
  bytes, so each stays small) and compact under their own owner-set bound —
  at most 64 newest markers per live name plus a marker TTL in the same
  owner-knob family as the superseded-receipt TTL (every sidecar mutation
  and every boot compacts markers past either bound in the same atomic
  write; defaults ship in the implementing PR) — extending the
  round-twenty-five marker rule, which removed markers only by the tombstone
  purge, so a never-removed live host minted one marker per unique
  mutationId forever and the marker map grew monotonically against the
  stated storage bound: what changes is that tombstone-less names compact
  markers by count/TTL exactly like receipts, so the map stays bounded for
  live hosts too. The newest same-key superseded receipt per name is still
  retained until that name's tombstone expires (never pruned by the
  receipt count/TTL bound above while the tombstone lives), and its marker
  twin is exempt from the marker count/TTL bound the same way — that one
  `remove`-retry backstop marker per tombstoned name persists until the
  tombstone purge (re-add past the remnant gate, or retention expiry — the
  explicitly defined clean-slate paths), so no marker cap can drop the live
  backstop early and reopen the hole. A marker dropped by the count/TTL
  bound for a tombstone-less name removes only the oldest non-backstop
  entries, and a key with neither a retained receipt nor a live marker
  commits fresh by the commits-fresh clause — the backstop exemption is the
  one case where retention, not recency, decides. A replay naming a marked
  key refuses as `stale-entry` (pruned-generation); a key with neither a
  retained receipt nor a pruned marker commits fresh by the commits-fresh
  clause. The
  tombstone purge is clean-slate: once the tombstone (and that name's
  receipts with it) is gone, a same-key replay commits fresh by the
  commits-fresh clause in mutation idempotency, above — see the single
  superseded-receipt rule there.**
 **Remnant gate (extending the round-eight commit point, and the
 round-twenty-five remnant gate, which fenced configuration mutations only
 — re-add, `update`, and `remove` on the remnant's name — so `deploy`,
 `restart`, `Ensure`-triggered work, `plan`, and attach could run against a
 newer lifecycle while the prior supervisor, channels, or fan-outs were still
 pending teardown: what changes is that the open remnant is a host-wide fence
 for every lifecycle and attach operation on the name until teardown
 succeeds): any non-replay mutation (a same-key replay matching a
 current-generation receipt returns before this gate — see the fixed ordering
 in mutation idempotency) that would advance or remove the affected name while it
 holds an open remnant — re-add, `update` of the remnant's name, and `remove`
 of the remnant's name — is refused with the typed `remnant-open` conflict refusal
 (carrying the blocking `remnantId` — extending the round-eleven re-add gate:
 the gate refusal finally has its own contract value rather than borrowing
 `teardown-unknown-key`'s shape) — and the same open remnant fences every
 other lifecycle and attach path on the name: `deploy`, `restart`, and
 `Ensure`-triggered work refuse with the same typed `remnant-open` refusal
 (never the gate-busy form — the fence names the blocking `remnantId`, not
 an in-flight operation), `plan` returns its no-token shape with a
 `remnant-open` refusal naming the `remnantId` (never a minted token bound to
 a lifecycle whose teardown is still pending), and attach (`attachUnderGate`
 reattach/attach-first and the Connect path) refuses the same way — the
 operator first resumes the named teardown through `evener/host/teardown-retry`
 (which runs it to completion for that generation), and only then does any
 fenced path proceed: re-add mints the new generation and purges the cleared
 remnant — so a new incarnation can never start, and no
 deploy/restart/`Ensure`/`plan`/attach can run, while the old lifecycle still
 owns supervisors, channels, or fan-outs. **Open remnants pin their generation:** while a remnant is open
 for a name, no path advances that name past the remnant's generation —
 re-add, `update`, and `remove` on that name all stay refused by the gate
 above (the name stays tombstoned until the remnant resolves — the
 post-`remove` tombstone is the tombstone; the pre-re-add refusal for the
 add/update/remove paths above IS the tombstoning, extending the
 round-twelve and round-thirteen remnant gates), and a boot-merge collision
 involving a remnant-gated name leaves the
 open remnant resumable by `remnantId`: **the boot-collision above-mark bump
 is forbidden while a remnant is open for that name — boot keeps the live
 entry at the remnant's generation (no carve-out) until `teardown-retry`
 resolves it, so no live incarnation is ever created over an open remnant
 (extending the round-sixteen boot-collision wording — the bump applies only
 to remnant-free names)** — so no
 mutation can strand a remnant by opening a second one for the same name.
 **Incarnation-scoped teardown:** `teardown-retry` executes ONLY the
 remnant's pinned teardown target — the staged supervisor/channel/fan-out
 teardown description captured at commit, bound to the remnant's generation
 — and never the name's live entry, live channel, or live supervisor set:
 every lifecycle handle the retry can touch carries a generation tag AND a
 persisted incarnation identifier (extending the round-seventeen
 generation-only tagging — r17 tagged handles by generation alone, so a new
 live incarnation sharing the remnant's generation was indistinguishable
 from it): the incarnation id is a server-generated opaque string minted
 fresh on every `add`/re-add in the same atomic sidecar write that mints
 the generation, persisted per live entry alongside it (never derived from
 the generation, never reused across incarnations even when generation
 numbers collide), and copied into the remnant's pinned teardown target at
 commit; handles (supervisor binding, channel handle, fan-out subscription)
 are tagged at bind time with both values. The retry targets handles by
 incarnation-id equality — never by generation alone and never by name
 lookup: a name-based lookup resolving to a handle whose incarnation id
 differs from the remnant's is refused, never executed, even when its
 generation tag equals the remnant's. **Post-crash rehydration (extending the
 round-twenty-one remnant work — r21 persisted generation/incarnation tags plus
 the teardown description, which does not define how a removed supervisor,
 channel, or fan-out is rehydrated or terminated after the original process
 exited): boot rehydrates the remnant's actionable handle by loading the
 persisted `cleanupHandle` — rehydration is record load, never live-handle
 resurrection (no in-process handle survives restart, see crash recovery): for
 remote seams the handle is the remote guard-file identity plus the orphan
 epoch's lease-entry ownership tokens, so the retry kills and verifies through
 the lease wrapper without the dead worker's handles; for local seams it is
 the durable local ownership-boundary identity plus nonce (the same
 boundary-plus-nonce rule as orphan reaping above), so boot reaps or the retry
 signals through the persisted boundary. The retry re-resolves live bindings
 by (generation, incarnation-id) equality only while the committer is still
 alive; after a crash it acts through the `cleanupHandle` alone. A retry
 executing a boot-recovered remnant additionally runs as a fenced remote
 operation: it persists a fresh fencing epoch in the remnant record,
 kill/waits the superseded epoch's lease-tracked entries, compare-and-advances
 the guard, and only then runs the pinned teardown through the wrapper — so
 post-crash teardown repair can never mutate past a still-running
 crashed-epoch orphan.** With the forbidden-bump rule above a
 mismatch reaches the retry only through a hand-edited collision the gate
 could not refuse, and even then the incarnation-id targeting keeps the
 live incarnation provably untouched.
 Retention expiry likewise never purges a tombstone whose name still holds an
 open remnant (see the expiry mechanism in data flow).** Tombstone-purge semantics decide the rest (see data flow):
  while a tombstone is retained its name's receipts stay readable for
  post-remove forensics; purging the tombstone drops that name's receipts and
  remnants with it — cross-name replay after the purge commits fresh and
  overwrites by the scoping rule above.
- **Staged commit (order matters):** add/update/remove never mutate live
  state incrementally. Holding the process-wide mutation lock only across the
  transitions below — released across post-commit teardowns, re-acquired to
  finalize (the same pattern as foreign-marker finalization and
  `teardown-retry`, which never hold the lock across a teardown — the lock
  serializes sidecar read-modify-write only, so an unrelated host's mutation
  proceeds past a foreign-host marker (see mutation idempotency), serializing
  only same-host teardown/finalize work and the atomic file write): (1) stage the
  complete change — new registry value, the *description* of the manager
  deltas (including the planned supervisor/channel teardown for removals),
  source-registry rows, host-admin-controller host set and its notification
  fan-outs, web-config host view, and the new sidecar bytes; (2) **persist
  the sidecar first** — stashing a durable copy of the prior sidecar bytes
  (including the decision-source validation state — the archive/favorite
  host-set acceptance rewired from the startup `RemoteHosts` snapshot to the
  live set, preferably through a live-registry callback, so newly added hosts
  validate and removed hosts stop validating as part of the same swap),
(same config dir — same file posture as the sidecar: stash temps created
`0600`, temp-file + file-fsync + rename + parent-dir-fsync preserving the mode, startup refusing a stash readable
beyond its owner, and stray/expired stash files pruned or ignored at boot)
before the atomic rename, so the swap is compensable;
  (3) **persist the swap-started intent, then swap the runtime to the staged
  set** — the step-(2) staged sidecar write carries a durable `swapStarted`
  intent (false at stage time, flipped to true in its own atomic sidecar
  write under the mutation lock BEFORE the non-atomic runtime transition
  begins — extending the round-twenty-four staged phases, which persisted
  `runtime-swapped` plus `teardownStarted` only AFTER the swap, so a crash
  between the swap and that write left phase `staged` with
  `teardownStarted: false` and boot finalized without a remnant though the
  runtime transition had partially applied: a durable marker now exists on
  both sides of the transition) — then rebinding or wiring the
  live handles to the new values (flipping the staged phase to
  `runtime-swapped` as that transition lands), and only then executing the planned
  teardowns (supervisor/channel stops, fan-out cancellations) as the
  post-commit rebind phase (see the mutation rebind ordering in the
  operation store) with the lock released across the teardowns and re-acquired
  to persist the committed receipt plus real remnant (verifying the claim's
  attempt token still owns the marker); (4) if the swap itself fails, **compensate
  fully before responding: restore the prior sidecar bytes from the stash
  (atomic rename), then revert the runtime to the previous set**, then
  report exactly which step failed — a typed swap-failure response is sent
  only after both restores. **The commit point is the start of the
  post-commit rebind phase: once the first planned teardown executes, the
  mutation is committed and there is no compensation path back — a failure
  at or after the commit point is reported as a committed-with-
  teardown-failure with the seam named, and recovery is forward (retry the
  teardown / re-apply), never a restore of the prior bytes — through the
  teardown-repair operation, never a blind full-mutation retry (replaying
  the original mutationId stays a no-op receipt return, so the API has a
  repair path that is not the replay): the same atomic sidecar write that
  persists the committed receipt also persists a durable teardown-remnant
record — the mutation's scoped receipt key plus the in-progress remnant
already pinned in the step-(2) marker (see mutation idempotency, above) —
the named pending teardown
 (handles, seam, generation, incarnation id, cleanup handle) **plus a server-generated opaque `remnantId`
 (non-empty, at most 128 bytes, unique per remnant — never derived from the
 mutationId, so an ID-less failure still gets a retry handle and a reused
 mutationId can never collide with an earlier remnant)** — and every
 committed-with-teardown-failure response carries that `remnantId` alongside
 the committed receipt — and the new `evener/host/teardown-retry`
mutation (params `{remnantId: string}`, response the outcome union in
Protocol types)
 resumes ONLY that named teardown: it looks up the remnant by the opaque
 `remnantId` (unknown or purged ID → typed `teardown-unknown-key` not-found;
 a cleared-remnant marker still present returns the `already-cleared` success
 arm, never not-found — the remnant carries its own pinned teardown target,
 so lookup never requires a current live entry and later mutations cannot
 strand it (see the remnant schema above)), try-acquires
  the host's per-host gate (held → typed busy, same classes as `restart`) —
 and the clearance holds the process-wide mutation lock only for the
 marker/remnant/receipt state transitions (the same lock as
 `add`/`update`/`remove`, so a concurrent mutation cannot interleave with
 them) — never across the teardown itself (the same claim pattern as the
 foreign-marker rule above — claim under the lock with an attempt token,
 release across the teardown, re-acquire to finalize):
  runs the remnant's pinned teardown to completion with no mutation lock held — through the persisted `cleanupHandle` after a crash, under a fresh fencing epoch (see incarnation-scoped teardown above — including its bounded kill/wait contexts, and the retry's own teardown run carries a bounded execution deadline of the same owner-set family: on timeout the retry reports the terminal `committed-with-teardown-failure` outcome with the remnant still open for a later retry — a stuck remote process therefore surfaces a terminal outcome with a live retry handle, never an indefinitely held gate) — (never against a live or
  re-added entry — the retry validates against the remnant's OWN pinned
  identity, never against the registry's current values (extending the
  round-twenty-three remnant-identity work — the equality re-check against
  the registry's current generation/incarnation made the retry unexecutable:
  update/remove remnants intentionally belong to a superseded incarnation,
  and removes leave no live entry at all, so the check could never pass): the
  retry re-resolves the pinned teardown target by the remnant's recorded
  `(generation, incarnationId)` plus its persisted `cleanupHandle` — the live
  entry for the name may be absent (post-remove) or a newer incarnation
  (post-update) without blocking the retry — and refuses only when the
  remnant's own pinned target fails to resolve through its own handle
  (typed `teardown-unknown-key`), never because the registry moved on; the
  open-remnant gate above is what keeps a newer incarnation from existing
  while the remnant is open, not this re-check), then re-takes the mutation lock and clears the
  remnant in one atomic sidecar write — the same write records the
  remnant resolution on the original mutation receipt (outcome becomes
  `committed` with `remnantResolvedAt`, extending the round-eleven
  receipt/remnant work: a replay of the original mutationId after resolution
  returns the resolved receipt, never the stale
  committed-with-teardown-failure) — and returns the live row. The retry is
  idempotent by remnantId: a retry naming an already-cleared remnant returns
  the already-cleared success arm (see Protocol types), a receipt-returned
  no-op — never a second teardown, never not-found. Extending the
  round-eight commit-point work with a stable representation: the response
  carries the stable `committed-with-teardown-failure` outcome naming the
 seam **and the `remnantId`**, the mutation's durable receipt (see mutation idempotency, above) is
 persisted as committed with the pending teardown named, and the UI renders
  the committed host row with a teardown-retry affordance — never a generic
  failure affordance, never a blind full-mutation retry. The one
  sentence that changes from rounds 3–7's settled text is the scope of the
  guarantee above: the "failed mutations leave disk+runtime in the old
  state" promise now covers pre-commit failures only** (no teardown runs
  before the commit point, so compensation never has to recreate a canceled
  lifecycle handle or a drained channel — restoring sidecar bytes after a
  teardown cannot rebuild the destroyed handles)
  and a retry behaves as a fresh mutation. **Failure and crash rules
  agree:** every sidecar write in the
 sequence is a temp-file + file-fsync + rename + parent-dir-fsync, so the canonical file always holds either the
  complete old or the complete new bytes; a crash at any point of the
  commit (including mid-compensation) is reconciled at boot — **the disk
  wins** — while a typed failure never diverges from what a restart would
  apply. Within a live process, staging failures change nothing. The stash
  is deleted once the commit reaches either outcome; a stash left by a
  crash is ignored (safe to prune) at boot.
- **Per-host lifecycle handles:** every live host owns cancellable handles —
  its remote-source subscription, its host-admin fan-out (request
  forwarding plus notification fan-out), and its supervisor/channel binding
  — started when the host is added and cancelled/drained as part of the
  post-commit rebind phase on update/remove, before the host's registry
  entry is considered gone. Rollback (step (4) above) reverts to the prior
  handle set with the prior runtime, so a compensated mutation leaves no
  server-lifetime goroutine retrying or emitting for a removed host.
- Hot-apply mechanics touch the seams built once at startup today:
  `hostRegistryEntries(cfg)` → `hostreg.New` → `sshconn.New` → the
  `RemoteHost*` fields in `hubcore.WebConfig` (`main.go:405-488`),
  `newHubSourceRegistry`, **and the host-admin controller (07a), whose
  per-host request forwarding and notification fan-outs are initialized from
  the startup snapshot — they must consume the live host set and start/stop
  fan-outs as part of the staged commit (and its rollback — the stashed
  prior sidecar bytes included; see the staged commit)**. The live
  host-set surface (registry `Add`/`Update`/`Remove` with the existing cycle
  and validation rules, manager add/update/remove safe against in-flight
  `Ensure` and running supervisors) is 08a's core work.
  **Decision-source validation** (the archive/favorite
  `validateDecisionSource` host-set check, which today walks the startup
  `cfg.RemoteHosts` snapshot) consumes the live host set as part of the swap
  — preferably through a live-registry callback — so a newly added host is
  accepted and a removed host is refused immediately after the commit; 08a
  tests pin both directions at that boundary.

### UI

- **Hosts settings section** (new `panes/settings/sections/hosts/*`, plus its
  registration in the settings section map): host rows with state chip (online
  / offline / connecting / removed-retained), installed version + controller
  version, OS/arch, origin marker, actions: Connect, Deploy, Restart, Edit,
  Remove (each with the confirm pattern used elsewhere in settings), and the
  Add button.
- **Add / Edit dialog:** fields exactly the `HostConfig` schema — `name`,
  `ssh`, `user`, `evener_path`, `config_path`, `addr`, `roots` (multi-line;
  `config.go:32-46` — TOML-file spellings; the wire carries `evenerPath` /
  `configPath` per Protocol types) — all seven fields, none invented, none
  hidden; each with its validation message mapped from the backend response.
  Edit does not offer `name` (immutable).
- **Connect state machine:** offline → connecting (in-flight, driven by the
  `attach` response or the host-notification stream) → online (the sources
  manifest's online flag flips; the rail and picker update through the
  existing 06b/07b paths) or failed (typed error shown, retry). The
  spawn-picker Connect trigger shipped with #1603 stays as-is; this section is
  the management view of the same action.
- **Deploy confirmation:** opening the dialog calls `evener/host/plan` and
  renders the confirmation from **`plan`'s response — the controller-minted,
  token-bound plan**: target host, controller revision, resolved remote
  target path, whether a restart follows, the running build, facts freshness
  (from the plan's server-generated `factsCapturedAt`/`factsAgeSec`),
  and any plan-time
  refusal (terminal ones disable the button). The deploy confirmation
  submits exactly the displayed plan: `evener/host/deploy` with that plan's
  token and a client operation ID — never a plan the user has not seen. If
  `deploy` rejects the token as stale (expired, superseded, or
  binding-mismatched), the UI re-plans and re-renders the confirmation from
 the new response before any retry. No-token responses from `plan` branch on
 the `reason` field: `unattached` directs the UI to Connect
 first; `refresh-failed` on an attached host surfaces the refresh failure
with retryable diagnostics and a refresh-retry affordance (never Connect);
`probe-failed` on an attached host surfaces the probe failure with
 retry (never a Connect loop); `handler-absent` on an attached host surfaces
 the one-time migration step (never Connect-first); `remnant-open` surfaces
 the blocking `remnantId` with a teardown-retry affordance (never Connect,
 never re-plan — a re-plan mints nothing while the remnant is open).
 `status` stays the read-only informational surface
  behind the host row — it mints nothing and is never the confirmation's
  source. Progress renders from `operations` polling (with best-effort
  stream events if implemented); terminal failure surfaces the verbatim 04b
  error; a retry after a lost response reuses the same operation ID — a
  replay past compaction returns the tombstoned terminal result, never a
  fresh operation.
- **Stores:** follow the 07b host-store pattern (`hostInstancesStore` /
  per-host request sequences / connection-generation guards as fixed in
  #1605). No shared mutable module state.

## Implementation approach (files/packages, cited seams)

- `cmd/evener-hub/internal/hostreg` — live registry: `Add` exists; add
  `Update`/`Remove` with the same cycle/validation rules (including the
  63-remote-host cap — over-cap `Add`/`Update` fail `ErrTooManyHosts`),
  safe for concurrent use.
- `cmd/evener-hub/internal/sshconn` — `Manager` gains the live host-set
  surface (add/update/remove entry, each safe against in-flight `Ensure` and
  running supervisors) and thin exported entry points for deploy/restart
  reusing the 04b internals (`deploy`, `ensureDecision`, `waitHealthy`,
  `bootstrapHub`), plus an exported deadline-bounded preflight re-read (the
  existing one-shot SSH command sequence re-run on demand, no channel
  involvement — `plan`'s facts-refresh mechanism; see `plan`);
  plus the internal gate-aware `attachUnderGate` primitive (accepts an
  already-held per-host gate, suppresses supervisor startup until the
  worker's post-verification handoff — the restart
  worker's reattach path, see `evener/host/restart`);
  `Ensure`/`Attached`/`ChannelIfAttached`/facts unchanged.
- `cmd/evener-hub/config.go` — sidecar load/merge (hard-error duplicate rule)
  + atomic write.
- `cmd/evener-hub/app_host_manage.go` (new) — the handlers, router
  registration, mutation classification (`plan` is a mutation — it mints
  durable state; `list`/`status`/`operations` are reads), **guarded by the
  shared origin guard — landed by #1603 at the dial + dispatch seams,
  extended by 08a to the common request-ingress/router boundary as a
  pre-admission hook running before admission (see Non-scope) rather than
  wrapping handlers one by one, with ordering tests proving
  guard-before-admission (dedup/token orderings asserted in 08b where they
  ship))**, explicitly NOT in
  `remoteHostAdminMethods` (negative assertion in the allow-list tests).
- **AppWire protocol catalog** entries for every new method + request/response
  types, and the **regenerated TypeScript client** — both in the same PR as
  the handlers, so the frontend can consume them — with the shapes in
  Protocol types, below (the wire contract field-for-field; the generated
  client through the generator mapping named there), or the client drifts
  from the contract.
### Protocol types

Every new method gets its AppWire protocol catalog entry
(`appwire/protocol.go`, `ScopeHub`) plus request/response structs
(`appwire/types.go`) and the regenerated TypeScript client — in the same PR
as the handlers. `evener/host/attach` keeps its shipped shapes
(`HostAttachParams{host}`, `HostAttachResponse` identity fields) unchanged;
every new type below follows the same conventions (`name` names the host in
every host-management request field and in `HostRow` — distinct from
`attach`'s shipped `HostAttachParams{host}`, which is unchanged;
lowerCamel JSON, optional facts/error fields absent — never null — when
unknown). (`HostPlan.host`/`OperationRecord.host` — the plan/token and
 operation-record bindings, not request fields — keep `host`.)
- **Generator mapping (revising the contract to the generator's supported
  shapes — generator work is out of this spec PR's scope):** the AppWire
  TypeScript generator emits Go structs as interfaces and named string types
  as plain `string`, with no discriminated-union or literal-union emission
  (`internal/appwirets/emit.go` `typeExpr` — the only unions in
  `types.gen.ts` are the hardcoded `ThreadItemEventKind` /
  `NavigationTargetKind` / name-catalog exceptions). The literal string
  unions below (`origin`, `outcome`, `reason`, `kind`, `state`) are wire
  value sets carried in the generated client as `string`, with the exact
  value set pinned by the protocol-shapes test rather than a TS literal
  union; the response unions below (the mutation-result arms, `plan`'s
  token vs no-token shapes) are carried as per-arm interfaces selected at
  runtime by their discriminator (`outcome`, `reason`), never as a
  generated TS union — the 08b catalog defines one named Go struct per arm
  so each generates its own interface field-for-field. **That is the
  round-seventeen answer to the union-emission question (extending the
  round-fifteen mapping and the round-sixteen `outcome`-discriminator /
  `RemovedRow` work, and narrowing the round-eighteen wording — r18's "no
  generator change is needed" read as covering registration too, which the
  next paragraph contradicts): no generator change is needed for TS
  discriminated/literal-union emission — the contract's one-named-Go-struct-
  per-arm rule (mutation-result arms, `plan`'s planned vs
  no-token arms, `teardown-retry`'s six `outcome` x `hostKind` arms — the two
  success outcomes (`teardown-complete`, `already-cleared`) plus the
  `committed-with-teardown-failure` timeout arm, each crossed with
  `hostKind: live | removed (extending the round-twenty-eight generator
  mapping, which still counted four — two outcomes times two host kinds —
  after round twenty-seven declared the third timeout arm, so an
  implementer taking the count literally generated interfaces for the two
  success outcomes only and left the timeout arm unrepresented)) exists
  only so each arm generates its own interface, with the 08b
  protocol-shapes test pinning the wire shapes
  and the `outcome` discriminator on both `plan` arms; the frontend selects
  arms at runtime on the discriminator (see `plan` and the mutation-result
  union), never on a generated TS union — while union *registration* (how
  those arm structs reach the generated client at all) is the Arm
  registration paragraph below, which 08b changes. **Arm registration (extending the
  round-seventeen answer with the new per-arm specific — r17 settled that no
  TS union emission is needed; this settles how all N arm structs reach the
  generated client when the catalog holds exactly one `Result` value per
  method): `EmitCatalog` emits only each method's `Result`-named type plus
  types transitively reachable from its fields (`internal/appwirets/emit.go`
  `registerTopLevel`/`discover` — an anonymous struct `Result` panics the
  generator, and an `any`/`interface` field emits `unknown`), so sibling arm
  structs are NOT emitted by being named in prose. 08b therefore extends the
  generator with explicit union support (generator work lands in 08b, never
  in this spec PR — extending the round-nineteen sequencing, which required
  regenerated 08a clients for union-shaped responses while deferring the
  union-registration generator change to 08b, so the 08a client could not be
  generated field-for-field): the catalog declares one named Go struct per
  arm plus a named union registration referencing every arm, and the
  generator emits each arm as its own interface with the method's
  `MethodTypes` result entry typed as the union over the arm names; the 08b
  protocol-shapes test pins every arm field-for-field (including each arm's
  discriminator) — and the union-shaped catalog/client changes for the 08a
  methods (the mutation-result arms, `RemovedRow`, `teardown-retry`'s
  outcome arms) land in 08b with that generator work, never in 08a: 08a
  ships the sidecar/commit behavior behind hand-written request/response
  types plus the store-skeleton helpers, and the regenerated client for the
  union-shaped 08a responses arrives with 08b. The
  single-response-interface alternative is rejected: collapsing the arms
  would force optional-ified `host`/`token`/`reason` fields the
  absent-when-unknown rule cannot distinguish.**

- `evener/host/list`: params `{}`; response `{hosts: HostRow[]}`. `HostRow`
  is the full effective `HostConfig` fields (`name`, `ssh`, `user`,
  `evenerPath`, `configPath`, `addr`, `roots`) plus live state — wire JSON is
 lowerCamel throughout (`evenerPath`, `configPath`); snake_case
 (`evener_path`, `config_path`) is the TOML-file spelling only (`config.go`
 TOML tags), never the wire:
  `attached: bool`, `installedVersion?: string`,
  `installedVersionAgeSec?: number`, `osArch?: string`,
  `lastAttachError?: string`, `lastAttachErrorAgeSec?: number`,
  `midEnsure: bool`, `origin: "hub.toml" | "sidecar"`, `removed: bool`,
  `retainedRows?: number` (tombstone rows only), `rowsTruncated?: bool`
  (present as `true` exactly on tombstone rows whose retained projection was
  truncated at the 500-row/1 MiB persist bound above; absent everywhere else per the
  absent-when-unknown rule — the 08a tombstone tests pin both the bound and
  the indicator), `generation: number`,
  `escalationAgeSec?: number` (present only on tombstone rows whose name
  holds an open remnant past the escalation bound — the escalation age the
  expiry-escalation rule promises; absent everywhere else per the
  absent-when-unknown rule — extending the round-eighteen escalation work,
  which promised the surfacing without defining the field).
  **Tombstone values:** a tombstone row renders from retained effective
  `HostConfig` with `attached: false`, `midEnsure: false`, and the removed
  entry's `origin` and `generation`; `installedVersion?`,
  `installedVersionAgeSec?`, `osArch?`, `lastAttachError?`, and
  `lastAttachErrorAgeSec?` stay absent (never null) per the absent-when-unknown
  rule — every other `HostRow` field carries the explicit value above, so no
  non-optional field is left unknown.
- `evener/host/add`: params are one full host entry (all seven `HostConfig`
  fields; `name` required) plus optional `mutationId: string` (opaque,
  non-empty, at most 128 bytes — the idempotency key; see the mutation
  idempotency rule above); response is the mutation-result union below.
- `evener/host/update`: params `{name: string, entry: <the six non-name
  `HostConfig` fields>, mutationId: string, expectedGeneration: number}` —
  the idempotency key and the generation guard, both required together (like
  `remove` — extending the round-twenty-four update contract, which left
  `expectedGeneration` optional with keyless retries unconditionally
  committing, so a stale or buggy client could overwrite an intervening
  update with no generation check: what changes is the enforcement,
  matching `remove`'s required-key shape — a first-time `update` and a
  lost-response retry after an intervening update are no longer
  indistinguishable) — with update-identical check-and-refusal semantics
  once present (presence of both validated before the dedup check — missing
  either → validation refusal committing nothing; `expectedGeneration`
  checked under the mutation lock against the target's current generation
  before staging — past the dedup check, so a replay never reaches it;
  mismatch → the same typed `stale-entry` refusal committing nothing — a
  keyed replay returns the recorded receipt before the stale check, so a
  lost-response retry recovers its outcome instead of refusing as stale;
  see `evener/host/update` above; the UI retry path always sends both, and
  the 08b catalog, regenerated client, and update-required-key behavioral
  tests pin the four-field shape with both fields required); response is the
 mutation-result union below.
- `evener/host/remove`: params `{name: string, mutationId: string,
 expectedGeneration: number}` — the idempotency key and the generation guard,
 both required together (unlike `add`, where `mutationId` stays optional —
 `update` requires both fields identically, see above),
 with update-identical check-and-refusal semantics once present (presence of
 both validated before the dedup check — missing either → validation refusal
 committing nothing; `expectedGeneration` checked under the
 mutation lock against the target's current generation before staging —
 past the dedup check, so a replay never reaches it; mismatch → the same
 typed `stale-entry` refusal committing nothing; see `evener/host/remove`
 above; the UI retry path always sends both, and the 08b catalog, regenerated
 client, and remove-required-key behavioral tests pin the three-field shape
 with both fields required);
 response the
 mutation-result union below — the clean path returns `{outcome: "committed",
host: RemovedRow}`, and only the union's failure arm
  describes the failed-rebind shape (`RemovedRow` is the dedicated removed-row
  arm — `{name, removed: true, retainedRows}` plus the tombstone's retained
effective `HostConfig` fields, `attached: false`, `midEnsure: false`, and the
  removed entry's `origin` and `generation` per `list` tombstone values, plus
  the same optional `escalationAgeSec?: number` as `HostRow` (present only
  when the row's name holds an escalated open remnant) and the same optional
  `rowsTruncated?: bool` as `HostRow` (present as `true` exactly when the
  tombstone's retained projection was truncated at the 500-row/1 MiB persist
  bound — extending the round-twenty-four `RemovedRow`, which omitted it, so
  the remove clean/failure arms and the teardown-retry `removed` arm could not
  report truncation the `list` row reports: the 08b protocol-shapes test pins
  the field on both arms) — and is
NOT a `HostRow`: the catalog + regenerated client carry it as its own
interface). A replay carrying a known
  key returns the recorded receipt without re-applying.
**Mutation-result union (add/update/remove — the round-seven protocol-types
contract is authoritative for codegen):** every add/update/remove handler
returns either `{outcome: "committed", host: HostRow}` (remove's clean path
returns `{outcome: "committed", host: RemovedRow}`) or
the failure arm `{outcome: "committed-with-teardown-failure", seam:
string, remnantId: string, host: HostRow}` (remove's failure arm carries
`host: RemovedRow` for the same reason) — a normal result-union response,
never an AppWire error-envelope throw (pre-commit failures throw typed error
envelope codes; post-commit outcomes return through the union — the failure
arm names a committed mutation whose teardown needs forward retry) — the `remnantId` is mandatory on
the failure arm, `seam` names the failed rebind step, and the committed
`HostRow` is always present so the UI renders the row with a teardown-retry
affordance. The catalog carries both arms field-for-field and the regenerated
client carries each arm as its own interface through the generator mapping
above (see Implementation approach); a shape missing `remnantId`
on the failure arm fails the protocol-shapes test.
- `evener/host/status`: params `{name: string}`; response is the host's
  `HostRow` plus the deploy plan inputs: `controllerBuild: string`,
  `resolvedTargetPath?: string`, `restartFollows?: bool` (absent — never
  null — when unknown: `status` never dials, so an offline or never-attached
host may have no facts to compute it from — `restartFollows` and
`planRefusal` render from the (generation, incarnation id)-scoped running-state/refusal
snapshots in the manager store above, and stay absent when no snapshot
exists for the current pair),
  `factsRevision?: string`, `factsAgeSec?: number`, `planRefusal?:
  {terminal: bool, message: string, reason: "unattached" | "refresh-failed" |
  "probe-failed" | "handler-absent" | "remnant-open" | "controller-dirty" |
  "target-unwritable" | "target-missing-prereq" | "target-unit-findings"}`
  (the same `reason` discriminator `plan`'s no-token arm carries below, with
  the same value set and the same terminal-four rule — extending the
  round-twenty-eight status shape, which carried only `{terminal, message}`
  while claiming `status` and `plan` name the same terminal conditions in
  the same words: a `status`-only reader could not distinguish the four
  terminal causes. What changes is the discriminator — only `plan` branches
  on it to mint-or-refuse, `status` stays display-only, but the cause now
  travels with the display instead of stopping at the boolean).
- `evener/host/plan`: params `{name: string}`; response is either `{plan:
HostPlan, token: string, outcome: "planned"}` or `{outcome: "no-token", staleFacts:
  {message: string, attached: bool, reason: "unattached" | "refresh-failed" |
"probe-failed" | "handler-absent" | "remnant-open" | "controller-dirty" |
"target-unwritable" | "target-missing-prereq" | "target-unit-findings"},
  terminal: bool, remnantId?: string}` — `terminal` is `true` exactly on the
  `controller-dirty` / `target-unwritable` / `target-missing-prereq` /
  `target-unit-findings` arms (the `status` terminal refusals — a dirty
  controller, a missing curl, an unwritable deploy target, 04b unit
  findings: retry cannot clear them, so the UI renders the terminal
  affordance, never retry-with-backoff; extending the round-twenty-seven
  reason field, which defined only the five retry-oriented reasons while
  `status` and the UI required terminal plan refusals) and `false` on the
  five retry arms; `remnantId` present exactly on the `remnant-open` arm
  (the remnant fence: an open remnant for the name refuses `plan` with no
  token minted — see the remnant gate; absent on every other no-token arm
  per the absent-when-unknown rule) — `outcome: "no-token"` is the top-level
discriminator on the no-token arm (the token arm carries
`outcome: "planned"` alongside `plan`/`token`), so the generator mapping
selects arms on `outcome` with no presence-based exception — the `reason`
discriminates the
  failure (`stale-facts` deleted — `plan` always refreshes attached hosts, so
  no path yields it; see `plan`): `unattached` (host not attached — Connect first),
  `refresh-failed` (attached, but the ungated preflight refresh failed or timed
  out — retryable diagnostics, never Connect-first),
  `probe-failed` (attached, but the `evener/host/running` probe read failed or
  was unauthenticated), `handler-absent` (attached, but the remote predates the
handler — take the one-time migration path), `remnant-open` (an open teardown
remnant fences the name — resume it through `teardown-retry` first; the
`remnantId` field names the blocking remnant), and the 08b protocol-shapes test
`controller-dirty` (the controller is dirty — rebuild from a clean tree,
never retry the plan), `target-unwritable` (the resolved deploy target is
not writable — fix the target, never retry the plan),
`target-missing-prereq` (a deploy prerequisite is missing on the target,
e.g. no curl — install it first), `target-unit-findings` (the 04b unit
decision reports findings blocking deploy — resolve them first), and the
08b protocol-shapes test
pins the `outcome` discriminator on both arms (including the `remnant-open`
arm with its `remnantId`, and the `terminal` flag true exactly on the four
terminal arms). `HostPlan` is `{host, generation,
  targetPath, controllerRevision, restartFollows, factsRevision,
  hubTomlFingerprint, factsCapturedAt: string (RFC3339), factsAgeSec: number,
  runningVersion: string, runningHealthy: bool, runningProcessStartTime?:
  string (RFC3339)}` — `runningProcessStartTime` present exactly when the
  probe carried it (absent otherwise per the absent-when-unknown rule) —
  the token's bindings (including the bound `factsCapturedAt` above) plus
  the server-generated facts capture timestamp and age rendered behind the
  deploy confirmation's
  freshness line. `plan`/`token`
  are absent — never null — on the no-token response, per the absent-
  when-unknown rule above.
- `evener/host/running` (controller-side method, same-scope 08b — catalog
  entry + TypeScript client with the handler): params `{}`; response
  `{buildRevision: string, healthy: bool, processStartTime?: string
  (RFC3339)}` — `processStartTime` present exactly when the serving hub
  knows its own process start time (absent — never null — otherwise per the
  absent-when-unknown rule; extending the round-twenty-seven `running`
  shape, which carried only `buildRevision` + `healthy`: every unstamped
  binary reports `buildRevision: "dev"` from the same source as the
  `controllerBuild` plan input, so two different dev builds — or two
  processes running the same one — were indistinguishable, and a plan could
  omit a required restart while post-operation verification reported false
  success. What changes is the process-instance discriminator: an
  unverifiable revision (`"dev"` or a dirty `"<sha>-dirty"`, which name no
  code — see the 04b restart identity rule) never proves currency by
  revision equality: `plan` treats a probed unverifiable revision as
  outdated (restart follows) unless the probe also carries a
  `processStartTime` proving the live process is the just-deployed one, and
  deploy/restart verification fails closed on an unverifiable revision with
  no usable `processStartTime`). Served locally by every hub —
  `buildRevision` from the same source as the `controllerBuild` plan input,
  `healthy` the hub's own health, computed authoritatively as follows (this
  paragraph is the definition — no other signal counts): the serving hub
  reports `healthy: true` exactly when all three hold — (1) its local
  liveness check passes (the hub process is serving this request, not
  mid-shutdown: the handler runs on the live request path, so reaching it
  proves it); (2) no restart-required condition is outstanding under the
  serving hub's dedicated local health predicate — the hub evaluates its own
  session/daemon ownership from its local controller roster directly (never by
  reusing the `restartRequiredDaemon` authenticated-probe path, which
  determines ownership from the probing controller's roster and cannot
  determine a remote hub's restart-required state — extending the
  round-twenty-one probe-field work, which bound `healthy` to that probe's
  verdict):
  a daemon pid on an incompatible protocol, or any condition that would make
  the hub report `ThreadStatusRestartRequired`, forces `healthy: false`);
  (3) the hub's durable state roots are writable (state-root write probe —
  a real atomic temp-plus-rename probe inside the state dir (create uniquely
  named temp per probe — pid plus a per-process counter, never a shared
  probe name — fsync, rename to a DISTINCT probe target in the same dir
  (a second unique probe-prefixed name, never the temp's own name — a
  self-rename is a no-op and tests nothing), fsync the dir, then remove —
  never the live store or sidecar names — plus a free-space query against
  the state root; concurrent probes never share a rename target, so they
  neither race nor serialize each other; a probe temp orphaned by a crash
  carries the probe-name prefix and boot prunes prefix-matching strays before
  serving; extending the round-twenty-four metadata-only check, which ran an
  `access(W_OK)`-class probe with no file creation while `runningHealthy`
  bound into deploy invalidation, so the signal was TOCTOU-vulnerable exactly
  where it gated work, and the round-twenty-five probe, which used one shared
  probe name with no crash-stray rule: what changes is the real mutation with
  per-probe unique names plus crash-tolerant cleanup, so the probe exercises
  the write path deploy depends on without racing concurrent probes or
  leaking crash temps) —
  a read-only or full disk forces `healthy: false`, since the hub could
  neither persist an operation record nor a sidecar commit). Anything else —
  session counts, load, peer reachability, external dependency status —
  never feeds `healthy`. The 08b protocol-shapes test pins this three-
  condition computation (each forced-false case returns `healthy: false`
  while the probe itself still succeeds — `healthy: false` is data, never a
  probe failure; concurrent probes return independently with no shared rename
  target, and a crash-orphaned probe temp is pruned at boot, never served),
  so an implementation cannot reduce health to "the probe
  responded": a responding-but-degraded hub binds `runningHealthy: false`
  into the token, and a health change between plan and deploy invalidates the
  plan exactly like a revision change. `GET /api/health` stays the
  human-readable surface (it carries no `healthy` field and is not the
  authority — `evener/host/running` is) — admitted only over an attached session
 peered by the #1603 handshake (read classification; never forwarded onward
 to a third hub; browser-origin and forwarded requests refused exactly like
 every other `evener/host/*` request — the direction-scoped peer-probe
 exception of Non-scope and `plan`). The
 `plan` probe calls it through `sshManager.ChannelIfAttached(name)`; its
 response fields are what `plan` records as `HostPlan.runningVersion` /
 `runningHealthy`.
- `evener/host/teardown-retry` (mutation): params `{remnantId: string}`;
  response `{outcome: "teardown-complete" | "already-cleared", hostKind:
  "live" | "removed", host: HostRow | RemovedRow, remnantId: string,
  escalationAgeSec?: number}` — plus the declared timeout arm `{outcome:
  "committed-with-teardown-failure", hostKind: "live" | "removed", host:
  HostRow | RemovedRow, remnantId: string, escalationAgeSec?: number}`
  (extending the round-twenty-seven timeout rule, which required returning
  the terminal `committed-with-teardown-failure` outcome on retry timeout
  while the declared response named only the two success arms — the timeout
  path had no declared arm and the outcome is explicitly not an
  error-envelope response. What changes is the third declared arm: a retry
  whose bounded teardown run times out returns the timeout arm — the same
  failure outcome as the mutation-result union, carrying the still-open
  remnant's details for a later retry — and the 08b protocol-shapes test
  pins all three arms field-for-field) — `escalationAgeSec` (present when the named remnant was past the
  escalation bound at execution — the escalation age the expiry-escalation
  rule promises on `teardown-retry` responses; absent otherwise per the
  absent-when-unknown rule) —
  resumes and clears the named remnant, or
  typed not-found / busy (never stale-entry: the remnant executes its pinned
  target, see the commit point); `hostKind: "live"` pairs with `host:
  HostRow`, `hostKind: "removed"` pairs with `host: RemovedRow` (the same
  tombstone shape `remove`'s clean path returns), so each of the six
  combinations (three outcomes crossed with both host shapes) is a declared
  arm with the catalog + regenerated client
  carrying all three outcomes through the generator mapping above — and the
  union registration plus the shapes test cover all six, never just the
  four success-outcome combinations; an
  already-cleared ID returns
  `{outcome: "already-cleared", ...}` with the same `hostKind` pairing for idempotent lost-response retry
  — the cleared-remnant marker (remnantId → `clearedAt`) persists in the
  sidecar past the clearance so a later lost-response retry still returns
  `already-cleared`, and is purged only by the name's next re-add or
retention-expiry prune (plus the cleared-marker TTL compaction in
Persistence + hot-apply, above — past the TTL the ID reads as
`teardown-unknown-key`). **When the remnant belongs to a `remove` there is
no live row to return: `host` is the same `RemovedRow` `remove`'s clean path
returns — same values, same tombstone rendering
(see `list` tombstone values) — with `hostKind: "removed"` (vs `"live"` for
the `HostRow` arm above). The catalog +
regenerated client carry this arm as its own interface, and the 08a commit-point tests pin it
  (post-`remove` retry returns the tombstone shape, post-add/update retry
  returns the live row).**
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
  (fresh create: `"pending"`; dedup hit: the existing record's state, as
  for `deploy`).
- `evener/host/orphan-resolve` (mutation, same-scope 08b — catalog entry +
  TypeScript client): params `{id: string}` (the controller-assigned record
  id of an `orphan-unverified` record); response the updated
  `OperationRecord` (with `orphanBoundary` while still unverified, without it
  once resolved). Admission is session-authenticated like every other
  `evener/host/*` request; unknown `id` → typed not-found, non-unverified
  record → typed validation refusal, members still present → the
  transient busy form (never a force-clear). The call never creates an
  operation record of its own and never advances the host generation — it
  transitions the named record (`orphan-unverified` → `interrupted` on a
  clean boundary) under the store mutex in one atomic write — the call itself
  is never fenced by the orphan admission gate (it is the gate's way out),
  then the host
  admits new operations past admission again. The 08b protocol-shapes test
  pins the params/response plus the `orphan-unverified` state and the
  `orphanBoundary` field.
- `evener/host/operations`: params `{name?: string, operationId?: string,
state?: OperationState, generation?: number, incarnationId?: string, id?: string, limit?: number,
cursor?: string}` — `operationId`
matches the client-supplied `clientOperationId` (never the
controller-assigned `id`); `generation` selects the incarnation after client
operation-ID reuse (omitted: the current generation); `incarnationId` narrows
that selection to the exact incarnation — required alongside `generation`
whenever the caller names a superseded pair, so the documented
same-generation incarnation collision (a boot-merge live entry sharing an
open remnant's generation with a different incarnation id) is addressable
rather than ambiguous (extending the round-twenty-three incarnation record
work — the record below already pinned both values for dedup, but the
filter/response exposed only `generation`, so the colliding incarnation was
unrepresentable and unqueryable); `id` is the detail filter
for the controller-assigned record id — the busy payload's open/wait-able
reference resolves through it directly — (all optional
filters plus pagination; empty params lists the first unfiltered page
(cross-host, no generation pin — `hostBoundaries` returned; "current
generation" is reserved for calls that name a host — extending the
round-twenty-five params text, which promised the first page "of the current
generation" for a multi-host page that has no single current generation);
response
  `{operations: OperationRecord[], generation?: number, incarnationId?: string, hostBoundaries?: {[host: string]: {generation: number, incarnationId: string, compactSeq: number, presenceEpoch: number}}, nextCursor?: string}` — `generation`/`incarnationId` are present exactly on host-pinned pages (the single host named by the request — the effective pair listed, echoed back with `cursor` on later pages), and ABSENT on unfiltered cross-host pages spanning hosts and incarnations (extending the round-twenty-four response shape, which declared the pair required top-level as "the effective" pair with no defined value for a mixed page — no convention can name one pair for many: `hostBoundaries` is authoritative there instead, one `(generation, incarnationId, compactSeq, presenceEpoch)` boundary per host present on the page — `presenceEpoch` the per-host removal/presence epoch below, validated on every continuation) — `limit` defaults
  to 50 and caps at 200; responses never exceed the cap, and an unfiltered
  call pages instead of returning the whole store. `OperationRecord` is `{id,
clientOperationId, host, generation: number, incarnationId: string, kind: "deploy" | "restart", state:
  "pending" | "running" | "complete" | "failed" | "interrupted" | "orphan-unverified", orphanBoundary?: {platform: "linux", cgroupId: string, nonce: string} | {platform: "darwin", pgid: number, sessionId: number, pid: number, startTime: string}, progress:
  ProgressEntry[], result?: {ok: bool, message: string}, createdAt: string,
  updatedAt: string, hostRemoved: bool, compacted?: true}` — `incarnationId`
  is the pinned incarnation the record ran against (the dedup scope's second
  half, see the operation store — without it the response cannot distinguish
  the colliding same-generation incarnations); `orphanBoundary` is present
  exactly on records whose `state` is `orphan-unverified` (absent on every
  other state per the absent-when-unknown rule — the `platform`
  discriminator selects the ownership data the verifier needs (Linux:
  kernel-enforced cgroup identity plus the pre-spawn server nonce —
  the kill requires BOTH cgroup membership AND a matching nonce (extending
  the round-twenty-seven wire shape, which exposed the nonce on the wire
  while stating membership alone authorizes the kill — both could not hold:
  the nonce was either unchecked authorization data or redundant exposure
  to every `operations` reader. What changes is the single stated rule —
  nonce plus membership, never membership alone — so the wire nonce is the
  authorization check the verifier performs, not redundant data); Darwin:
  the (pgid, session id) pair
  plus the launcher-observed (pid, start time) instance marker — a pid whose
  start time differs names a different process and reads as already clean) —
  extending the
  round-twenty-five `operations` detail filter, which exposed the persisted
  boundary identity only as prose with no wire field, and the round-twenty-six
  wire shape, which exposed only the Darwin fields (pgid, session id, pid,
  start time) with no platform discriminator, no cgroup identity, and no
  nonce — so a Linux boundary was unrepresentable and no verifier could tell
  which ownership rule applied: the filter still resolves through `id`,
  and the record now carries the boundary the operator must verify before
  calling `orphan-resolve`); `compacted` is present as
  `true` exactly on tombstone replays (absent on live records per the
  absent-when-unknown rule — extending the round-twenty-three compacted-marker
  work: compacted replays already had to return `compacted: true`, but the
  field was absent from this wire shape and the generated client, so the
  contract was unimplementable) — a replay naming a compacted ID returns the
  recorded terminal outcome with `compacted: true`, and the regenerated
  client carries both fields through the generator mapping above (the 08b
  protocol-shapes test pins them field-for-field). `ProgressEntry` is `{ts: string
  (RFC3339), message: string}`, bounded per record. **Pagination ordering and
  generation pinning (extending the round-fourteen / round-twelve pagination
 work): records sort AND resume by the controller-assigned `id` ascending —
 one ordering for both (the `id` is unique and monotonic per store, so the
 order is total: concurrent terminal writes can never skip or duplicate a
 row across pages — and a wall-clock rollback that stamps a later record
 with an earlier `createdAt` can never move it before the cursor, because
 `createdAt` is not part of the order at all (extending the
 round-twenty-eight ordering, which sorted by `(createdAt, id)` but resumed
 by monotonic `id` alone: a record created after the cursor but stamped
 with an earlier timestamp sorted before it while the cursor had already
 passed it — page order and resume key diverged, so the stated sort
 contract was unimplementable as written. What changes is the sort key —
 the monotonic sequence is now both the page order and the cursor position,
 while `createdAt` stays display-only — and the 08b pinned-pagination
 test pins both the rollback case (a post-cursor record with a pre-cursor
 timestamp still lists on the next page) and the divergence case (pages
 arrive in ascending `id` even when timestamps run backward));
 `createdAt`/`updatedAt` are stored UTC-normalized (`Z`-suffixed RFC3339 —
 a stored offset form is converted at write time, never compared
 lexicographically in its raw form), so timestamps stay comparable for
 display and freshness while never deciding page order; the
 cursor is opaque (encodes the last row's durable sequence position — the
 controller-assigned `id`, which never rolls back — never a bare
 offset — plus the pinned `generation` and a snapshot/retention boundary:
 the cursor encodes `(generation, incarnationId, compactSeq, lastId)`, where
 `compactSeq` is the store's monotonic compaction sequence minted in the
 same atomic write that compacts terminal records (see Retention and
 compaction, above)). A host-pinned response carries the effective
  `generation` and `incarnationId` actually listed (the optional pair of the
  response shape above, pinned in the 08b catalog entry — absent on
  unfiltered cross-host pages, where `hostBoundaries` is authoritative,
  extending the round-twenty-four cursor text, which had the cursor validate
  against a top-level reference pair the unfiltered shape could not supply:
  the catalog and the shapes test pin the pair on host-pinned pages and its
  absence on unfiltered ones), and host-pinned callers pass both values back
  with `cursor` for subsequent pages (extending the round-nineteen pagination
  — r19 excluded generation from the cursor while generation stayed optional
  and let terminal-record compaction delete rows from the pinned generation
  between pages, so later pages could change underfoot): the handler
  validates the cursor's `(generation, incarnationId)` pair against the request's
  `generation`/`incarnationId`
  filter (a mismatch is a typed `stale-entry` re-list refusal, never a mixed
  page — callers echo the pinned pair from the first-page response with
  `cursor` on every subsequent page, and an omitted filter on a later page
  reads as the pinned-cursor window, never as a fresh unpinned query: omitted
  means "continue the cursor's pin", so a generation advance between pages
  keeps later pages on the pinned incarnation instead of silently re-pinning
  to the new generation; a generation-pinned page requires `name` — a
  host-pinned first page returns the pinned pair for the single host named by
  the request, while an unfiltered response carries a per-host boundary map (one
  `(generation, incarnationId, compactSeq, presenceEpoch)` boundary per host
  in the query — every host in the query at cursor creation, not just the
  hosts present on the page (correcting the stale parenthetical above, which
  described the already-fixed round-twenty-four bug that recorded boundaries
  only for present hosts: an implementer following "present hosts only"
  would reintroduce wrong-incarnation cross-page listing) — and the cursor
  encodes that map alongside the last row's `id`
  (the cursor is a versioned base64url JSON envelope `{v: 2, pos: [id],
  bounds: {[host]: [generation, incarnationId, compactSeq, presenceEpoch] | "absent"}}`
  — the version bump from the round-twenty-eight `{v: 1, pos: [createdAt,
  id]}` envelope is the M1 ordering change itself: sort and resume are now
  the monotonic `id`, so the timestamp leaves the resume position — and a
  cursor whose envelope version is not 2 is a typed `stale-entry` re-list
  refusal (fresh read required), never a best-effort decode — the absent
  marker encodes as the literal string `"absent"`, pinned in
  the 08b protocol-shapes test; `presenceEpoch` is a per-host monotonic
  removal/presence epoch minted in the same atomic sidecar write as every
  `add`/`remove`/re-add (including tombstone re-add and expiry purge), so a
  removal that preserves the tombstone's generation and incarnation still
  advances the epoch — extending the round-twenty-six cursor, which carried
  only the (generation, incarnationId, compactSeq) triple with no
  removal-epoch signal, so a removal preserving the triple listed
  removed-host records on later pages instead of refusing — capped at 8 KiB encoded (a first page
  whose boundary map would exceed the cap refuses with typed
  `cursor-too-large` (its own envelope discriminator carrying `{capBytes:
  8192}` — never `cursor-invalidated`, whose compaction data an over-cap
  first page cannot produce: no cursor was minted, so there is no
  `compactSeq` and no pinned pair to name; extending the round-twenty-seven
  cursor text, which reused `cursor-invalidated` for both emitting paths
  under one data contract) naming the cap, never a truncated cursor; the
  63-host cap bounds the map, so the cap is reachable only with adversarial
  incarnation-id lengths, never in normal use); a host created after the
  cursor was minted has no stored triple to validate — later pages skip
  its records (a newer host's records appear only on a fresh unpinned read,
  never mid-pagination; a host removed after mint trips the same stored-triple
  mismatch rule — including its `presenceEpoch` fingerprint, which a removal
  always advances even when generation and incarnation are preserved, so
  later pages refuse `stale-entry` — see below)
  (extending the
  round-twenty-five cursor, which left post-creation hosts undefined and
  defined no encoding or size bound, so a new host between pages had no
  specified handling and an unbounded map risked oversized responses)
  (extending the round-twenty-three cursor, which carried a single pair even
  for unfiltered cross-host queries, so later pages could not pin or validate
  mixed-host results — a cursor minted for one pair validated against an
  unfiltered query is a typed `stale-entry` re-list refusal — and the map
  pins EVERY host in the query at cursor creation, not just the hosts present
  on the page (extending the round-twenty-four boundary map, which recorded
  boundaries only for present hosts, so a host absent from page one could
  advance generations before page two and list wrong-incarnation results
  under the pin: a host with no records on the page still contributes its
  current (generation, incarnation id, compactSeq, presenceEpoch) boundary — or its absent
  marker when the host holds no records at all — and a later page whose
  stored triple no longer matches the host's current boundary is a typed
  `stale-entry` re-list refusal, never a mixed page) — and its
  `compactSeq` against the store's current sequence — a
  compaction that removed rows at or before the cursor's position since the
  cursor was minted surfaces a typed cursor-invalidated refusal naming the
  compaction (the client restarts from the first page), so an advancing
  generation or a mid-pagination compaction changes nothing silently
  underfoot — later pages read the pinned incarnation, and a newer
  generation's records appear only on a fresh unpinned read. `limit` with
  no `cursor` starts the pinned first page.**
- Typed errors ride the existing AppWire error envelope — the numeric `code`
  (`appwire/errors.go`: `CodeInvalidParams` -32602, `CodeConflict` -32013,
  `CodeUnavailable` -32014, `CodeInternalError` -32603, `CodeInvalidRequest`
  -32600) with the stable discriminator in `data.evenerErrorInfo` plus the
  error-specific data fields — and the regenerated TypeScript client branches
  on the `evenerErrorInfo` discriminator (with data fields), never on the
  numeric `code` alone; the catalog pins the exact numeric code per
  discriminator, and the protocol-shapes test asserts the pair.
  Discriminators: `host-not-found` (not-found class),
  `host-busy-operation` (busy class; data names the operation id),
  `host-busy-transient` (busy class; no operation reference),
  `stale-entry` (conflict class; data names which of entry, target,
  generation, `hub.toml`-fingerprint, running-version, running-health,
  facts-age, or pruned-generation mismatched or expired — `entry` (the
  resolved host entry drifted), `target` (the resolved deploy target drifted),
  `generation` (the registry generation advanced), `hub.toml`-fingerprint (a
  hand edit landed between validation points), `running-version` (deploy's
  re-probed running build differs from the token-bound revision — see deploy
  step (3)), `running-health` (the re-probed health flag differs the same
  way), `facts-age` (the token-bound preflight facts aged past the freshness
  bound at deploy time — the re-plan refusal), `pruned-generation` (a
  same-key replay naming a pruned superseded receipt — see retention;
  `concurrent-terminal-op` (deploy step (3)'s — or `restart`'s — post-
  acquisition scan found an operation on this host terminal since the
  gateless probe/resolution started, so the live running state may have
  changed inside the probe→acquire window — the re-plan/retry refusal;
  the 08b protocol-shapes test pins each value against the path that emits it) —
  extending the round-twenty-three stale-entry work, which routed all three
  through `stale-entry` while the catalog named only four values, so the
  client could not branch and the shapes test could not pin the pair),
  `cursor-invalidated` (conflict class; data names the compacting
  `compactSeq` plus the cursor's pinned `(generation, incarnationId)` — the
  mid-pagination compaction refusal above, distinct from `stale-entry`'s
  generation-mismatch re-list refusal — and distinct from the over-cap
  first-page `cursor-too-large` refusal above (own discriminator, data
  `{capBytes: 8192}`); the 08b protocol-shapes test pins both discriminators
  with their data shapes — extending the round-twenty-three catalog work,
  which promised the typed refusal at the cursor paragraph but left this
  list without it),
  `token-missing` /
  `token-mismatched` / `token-superseded` / `token-expired` (conflict class),
  **(a consumed-token replay presents a deleted row and reads as
  `token-missing` — consume deletes the row in the same write that creates
  the record, see `deploy` step (4); no separate consumed discriminator
  exists),**
  `conflicting-operation-id` / `conflicting-mutation-id` (conflict class),
  `too-many-hosts` (conflict class; the `ErrTooManyHosts` refusal),
  `swap-failed` (internal class; data names the seam), `host-detached`
  (unavailable class; deploy's channel-gone refusal — token unconsumed, no
  record; UI Connects and re-plans), `probe-failed` (unavailable class;
  deploy step (3)'s running re-probe read failed, timed out, or was
  unauthenticated — data names the host plus which of read-failed, timed-out,
  or unauthenticated; token unconsumed, no record — distinct from `plan`'s
  no-token `reason: "probe-failed"` union-arm value, which is never an
  envelope), `concurrent-edit` (conflict class; the sidecar final check's
  bounded stage-validate retries exhausted against a racing `hub.toml` edit —
  data carries the staged `hub.toml` fingerprint plus the observed
  fingerprint; the client re-reads and retries), `teardown-unknown-key` (not-found class;
  unknown or purged `remnantId` — never a present-but-cleared remnant, which
  returns the `already-cleared` success arm), `remnant-open` (conflict class;
  any remnant-fenced path refused (re-add/`update`/`remove` on the remnant's
  name, plus `deploy`/`restart`/`Ensure`-triggered work/`plan`/attach on the
  name — see the remnant gate): data names the blocking `remnantId`;
  resume it through `teardown-retry` first), `session-unavailable` (the #1603
  attach classifier); `interrupted` is a terminal record state (outcome
  unknown), not a thrown error. `committed-with-teardown-failure` is NOT an
  error-envelope code — it is the mutation-result union's failure arm (a
  normal result response; see Protocol types).
- The **operation store**: a small durable store (records keyed by operation
  id, **dedup index on client operation ID scoped by (host, kind, host
  generation, incarnation id), with the
  typed conflicting-reuse refusal, the `host-removed` never-match rule, and
  bounded compacted-ID tombstones returning the expired/compacted result**,
  atomic temp+rename writes, startup reconciliation to `interrupted` plus
  the tombstone-derived `host-removed` pass) + optional host-notification
  publisher extension. Where it
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
  controller surface (including the per-host gate shared with `Ensure`) +
`list/add/update/remove` + `teardown-retry` (the repair mutation for the 08a
sidecar's own commit-point remnants and receipts — its commit-point tests are
the 08a tests below) (with hand-written request/response types — the
  union-shaped catalog + regenerated-client changes for these methods land
  in 08b with the union-registration generator work, see the Arm
  registration paragraph above — including the minimal store skeleton
  `remove` depends on:
  the `host-removed` mark path and the outstanding-token-row purge path with
  their atomic writes, shipped as store-owned helpers with no 08b behavior
  behind them); **08b** the union-registration generator change plus the
  union-shaped catalog + regenerated client for the 08a methods alongside
  `status`/`plan` (token) / `deploy` / `restart` / `running` /
  `orphan-resolve` / `operations` + the full operation store (records,
  dedup, tokens, reconciliation); **08c** the frontend (settings section,
  dialogs, deploy confirmation). 08b stacks on 08a; 08c stacks on 08b.
  (Extending the round-sixteen/round-seventeen sequence — r16 placed
  `remove`/`teardown-retry` in 08a with the store in 08b, and r17 kept the
  split: 08a meanwhile performs the mark and purge through the skeleton
  helpers writing the same store-file rows 08b owns, so 08a satisfies its own
  `remove`/commit-point spec without pulling the full store forward.)

## Data flow (remove + tombstone)

`refreshRemoteThreadSnapshot` enumerates the registered sources; removing one
would otherwise drop its rows from the next snapshot. Removal instead writes
an **explicit tombstone record for the source** (name, the removed entry's
effective `HostConfig` — all seven fields `HostRow` requires, so `list` can
render the removed row without a live entry — last-known-good rows,
removal timestamp, the removed incarnation's id (persisted alongside the
generation high-water mark — extending the round-twenty-four tombstone,
which persisted the generation but not the removed incarnation id, so the
boot `host-removed` pass below marked on generation alone and — with the
explicitly allowed same-generation collision holding an open remnant — a
new live incarnation sharing the removed generation was marked as removed:
what changes is boot reconciling on the exact persisted
(generation, incarnation id) pair, so a live incarnation sharing the
generation but carrying a different incarnation id never matches) — with a hard bound: at most 500 retained rows per
tombstone and at most 1 MiB of serialized row bytes per tombstone
(owner-adjustable knobs in the same family as the cleared-marker TTL; the
defaults ship in the implementing PR) — removal persists a bounded projection
(newest-first by each row's last-updated timestamp with a total tie-break —
(lastUpdated, row id) lexicographic, rows with a missing timestamp sorting
oldest (a missing timestamp never outranks a present one), applied
deterministically on every persist so truncation is stable across retries
(extending the round-twenty-four timestamp order, which left missing/equal
timestamps undefined and let retries keep nondeterministic subsets, flaking
newest-first tests: what changes is the total order plus the missing rule,
so the same row set always truncates to the same subset) — never the snapshot's row
order, which is tree order, not chronological, so truncating by it would keep
stale rows and drop fresh ones while `retainedRows`/`rowsTruncated`
misreported completeness (extending the round-twenty-three persist bound,
which truncated by snapshot order) — truncated past the bound) plus an
explicit `rowsTruncated: bool` on the tombstone, surfaced in the `list`
tombstone row's `retainedRows` count and the tree merge below (truncated
projections render their stale rows with the truncation indicator, never as a
complete set — extending the round-twenty-three retention work, which kept
every last-known-good row until tombstone expiry with no per-host, global, or
byte bound, so a large remote thread set could exhaust the sidecar/state dir
and fail atomic writes or startup)): the rows stay in the navigation tree marked **stale**,
and the tombstone is **consumed by the tree and the action-capability
logic** — removed-host rows must be visibly non-actionable
(markers in the tree/rail; actions disabled), not merely `Complete == false`,
because liveness alone does not make an unknown source's rows stale in the
current navigation projection. **Tombstoned hosts are not manifest
sources:** removal deletes the source-registry entry, so the navigation
manifest's `sources` array drops the removed host on its next read — the
manifest element schema is exactly `{id,label,kind,online}`
(`hubapi.Source`, `hubapi/types.go`; the component-06 read contract pins
those four fields and its enumeration is the registered-source set), with
no `removed`/`stale` field to carry a tombstone, so no schema change is in
scope. The tombstone's surviving surfaces are the stale tree rows and the
`list` row (`removed: true`) — the Hosts list reads tombstones from the
controller, never from the sources manifest. **Snapshot merge:** every
`refreshRemoteThreadSnapshot` publication merges the retained tombstone rows
back in AFTER enumerating the registered sources — the refresh replaces only
live-source rows, then re-applies each unexpired tombstone's last-known-good
rows (marked stale, non-actionable — see the tree/capability consumption
above — plus the tombstone's `rowsTruncated` indicator when the projection
was truncated at persist time), so a removed host's rows survive every subsequent refresh until its
tombstone is re-added or pruned; a concurrent re-add wins by the generation
rule below (its new-generation publication supersedes the tombstone merge
for that name).** The tombstone is purged when
the same host name is re-added, or after a retention period (owner-set;
open question 2 — **the owner knob is the retention period**). **Expiry
mechanism (explicit, no background timer):** the controller evaluates expiry
 lazily — `list` filters in memory with no lock (see `list`), and every
 sidecar mutation prunes durably in its atomic write under the
 mutation lock, **and every boot prunes durably in the same atomic-write
 posture before serving requests (extending the round-ten/round-eleven
 expiry: lazy + mutation-path + boot — no background timer, so a read-only
 workload still converges at the next restart; disk growth between the last
 mutation and the next boot is bounded by one tombstone plus its receipts
 and remnants per removed host)** — and drops each tombstone whose `removal timestamp + retention
 period` has passed: the read path omits it from the response without taking
 the lock, and the next mutation-path
 atomic sidecar write under the lock prunes it durably (plus that name's
 receipts and remnants, per the retention rule in persistence). **Expiry
 never purges a tombstone whose name still holds an open teardown remnant —
 the mutation-path prune skips remnant-gated names, and the in-memory filter
 keeps rendering them — so the failed teardown's generation-specific handles
 are completed through `teardown-retry` before the gate releases (see the
 remnant gate in persistence) — with a bounded operator-escalation backstop
 (extending the round-sixteen cleared-marker TTL and the round-seventeen
 re-add gate — r16 bounded cleared markers, r17 gated re-add, but neither
 bounded an open remnant itself): an open remnant older than the owner-set
 remnant-escalation bound (a multiple of the cleared-marker TTL, default
 ships in the implementing PR) surfaces an operator-escalation signal on the
 remnant (`teardown-retry` responses and `list` tombstone rows carry the
 escalation age), and the operator resolves it out-of-band (manual teardown
 of the pinned target, then a forced clearance through the same
 `teardown-retry` path); the gate still never auto-purges an open remnant —
 the bound escalates, never silently drops, so storage cannot pin forever
 without a visible operator action.** The data-flow
 purge sentence stands — re-add (past the remnant gate) and retention-expiry
 (past the same gate) both prune — and open
question 2 now asks only for the default value, not the mechanism.

**Tombstones are durable:** they persist as a section of the managed sidecar
itself (same file, same atomic writes — see persistence), so a removal's
delete-entry + write-tombstone is one write, boot restores them alongside the
host entries, and they survive controller restarts; re-add and
retention-expiry purges rewrite the same file atomically under the mutation
lock.

**Host generations:** every `add` (including re-add) mints, and every
`update` advances, a per-name
generation that is **durable: persisted in the managed sidecar alongside the
host entries (including the per-name high-water mark for removed names) in
the same atomic write as the entry mutation** — `add` mints generation 1 for
a never-seen name, `update` advances the live generation by one, and re-add
mints a generation strictly greater than any generation that name has ever
carried, so a re-added byte-identical entry still invalidates every
pre-remove token. **Every `add`/re-add mints a fresh incarnation id in that
same atomic write (opaque, unique per mint, persisted per live entry next to
the generation — the teardown-targeting identity `teardown-retry` selects on;
see incarnation-scoped teardown in persistence).** The boot load restores persisted generations before the
store serves any request, so the generation check — the token's generation
must equal the registry's current generation — survives restarts: an
update-then-restart keeps outstanding tokens valid (the bumped generation is
on disk), and no restart silently invalidates or re-validates anything.
**Store-side mirror — extending the round-eight persisted sidecar
generations:** the per-name generation high-water mark is mirrored into the
operation store itself (same atomic store writes as records), and boot takes
the maximum of the sidecar mark and the store mirror as the name's restored
generation — with the missing-sidecar preservation above: a name with no
sidecar mark contributes no mark to the maximum (the surviving mirror alone
is the high-water mark), never a zero that drags it down — so deleting a corrupt sidecar and re-adding an identical host
cannot restart its generation at 1 and adopt the old incarnation's records
or tokens: the re-add mints above the mirrored high-water mark instead.
Deleting both durable files is the only clean-slate path, and the spec
names it as such — there is no silent history adoption either way.
**Boot-merge collision (extending the round-ten wording): when a retained
tombstone collides at boot with a newly live `hub.toml` host (or a live
sidecar entry from a re-add), the live host is treated as a new incarnation:
its restored generation is set strictly above the tombstone's high-water mark
before any historical receipts/remnants apply, so old `host-removed` records
stay at or below the mark and live records stay unmarked — never a promotion
of a pre-collision record into the current generation, never a block on valid
operation-ID reuse — unless the colliding name holds an open teardown remnant,
in which case the bump is forbidden and the live entry stays at the remnant's
generation until `teardown-retry` resolves it (see the remnant gate in
persistence — never a live incarnation over an open remnant).**
`hub.toml`-declared hosts carry a
stable generation as long as their effective entry is unchanged — the sidecar
persists each declared host's effective-entry fingerprint (content hash of the
resolved entry) alongside the generations, and boot compares the freshly read
entry against it before serving requests: a mismatch advances that host's
generation and clears or rebinds its name-keyed cached state (the same
invalidation the sibling rule below applies), so an edit made while the
controller was stopped can never restore the old generation with old records
and cached state reading as current (extending the round-thirteen /
round-fourteen fingerprint work) — initial
assignment at boot is generation 1 for every `hub.toml`-declared name with no
  persisted high-water mark, AND boot mints a fresh incarnation id for every
  `hub.toml`-declared name with no persisted incarnation in that same atomic
  sidecar write (extending the round-twenty-seven incarnation work, which
  minted incarnation ids on `add`/re-add only: initial hosts carried
  generations plus fingerprints but no incarnation id, so their required
  (generation, incarnationId) identity was undefined across restarts. What
  changes is the defined baseline on both halves — generation 1 plus a
  persisted incarnation — so token generation checks and every
  incarnation-scoped read have a defined pair on both sides; a declared
  host whose effective entry changed while stopped still advances its
  generation AND mints a fresh incarnation in the same write, and a
  boot-merge collision on a declared name takes the same above-mark bump
  plus fresh incarnation as any other collision, never a stable generation
  with an undefined incarnation). Collision
precedence: the boot-merge collision bump above overrides stable generation
for that boot — the live host's above-mark generation replaces the stable
one, and tokens bound to the pre-bump generation are invalidated (their
generation no longer equals current) — since the
sibling reconciling commit above detects external `hub.toml` edits after the
fact, any detected change to a `hub.toml`-declared host's effective entry
— detected by the sidecar staged commit's `hub.toml` re-reads (validation,
final check, post-rename reconcile, all under the mutation lock), by a
`plan`/`deploy` fingerprint check, or by the live external-reconciliation
path below (never by an unsynchronized background watcher) —
advances that host's generation and clears or
rebinds its name-keyed cached state — the detecting path adopts the re-read
entry into the running registry first, then bumps the generation, then
clears or rebinds: resolved deploy targets, deployment
state, last-known facts entries, supervisor bindings, channel handles, and
outstanding tokens — so a stale snapshot or preflight facts read from the
old configuration can never pass a generation check against the new one.
**Live external reconciliation (extending the round-twenty-three hub.toml
work — the mutation re-reads and the plan/deploy fingerprint checks above
fired only on those paths, so removing a declared host from `hub.toml`
while the hub ran left it active in the registry, manifest, and admin
fan-outs indefinitely): every `evener/host/*` admission — mutations and
`plan`/`deploy` alike, plus `list`/`status` at most once per admission (cached
fingerprint check only — see `list`) — and
every boot compares the on-disk `hub.toml` fingerprint against the
in-memory fingerprint in a pre-handler step before serving the call (the
`list`/`status` lock-free read itself stays lock-free and serves the last
published snapshot on mismatch — see `list`): on a
fingerprint match the call proceeds on the current snapshot with no lock; on
mismatch a mutation/`plan`/`deploy` admission takes the
mutation lock synchronously and runs the same adopt-then-bump-then-clear sequence as the
sibling reconciling commit (re-read the file, adopt added/changed declared
entries into the running registry, drop declared hosts deleted from the file
out of the registry/manifest/admin host set and fan-outs — a deleted
declared host reads as not-found on all `evener/host/*` methods until
re-declared, and its name-keyed caches clear — then bump affected
generations and clear or rebind every name-keyed cache above — and the
adopt-then-bump-then-clear sequence coordinates with in-flight operations
exactly like `update`/`remove` (extending the round-twenty-seven
reconciliation, which removed hosts and cleared manager bindings with no
in-flight coordination while `update`/`remove` fail fast on a held gate: a
reconciliation landing mid-operation could strand the worker on a host
whose bindings were already cleared. What changes is the gate ordering —
before dropping a declared host or clearing its manager bindings the
reconcile try-acquires that host's per-host gate; a held gate defers the
drop-and-clear for that host until the in-flight operation reaches terminal
state — the admitted call proceeds on the pre-reconcile snapshot for that
host meanwhile — while added/changed entries and uncontended drops apply
immediately under the mutation lock; a deferred drop re-runs the same
generation bump and cache clear when the gate releases, so no external edit
survives past the in-flight operation's completion), so no
external edit — including a declared-host removal — survives past the next
mutation/`plan`/`deploy` admission (a `list`/`status` admission serves the
last published snapshot immediately and schedules the same sequence
asynchronously, debounced — see `list`): the step publishes a new snapshot under the lock and
the admitted mutation-path call then serves from it, and all live consumers (registry,
manager
bindings, sources, manifest, host-admin controller fan-outs, web-config
view) update atomically under that lock before the admitted call proceeds
(a `list`/`status` read arriving while the reconcile holds the lock serves
the last published snapshot lock-free instead of waiting — reads never fail
busy for a fingerprint reason).**
Navigation, source, and host-admin consumers outside `evener/host/*` do not
run the admission step themselves — they reach those methods' snapshots only
through the hub's common ingress or the live registry callback (see the
staged sidecar's live-registry rewiring in `add`): every read of the live
host set — registry, manager bindings, sources, manifest, host-admin
controller fan-outs, web-config view — goes through that callback, which runs
the same fingerprint-compare-then-reconcile pre-handler step with the same
serve-from-snapshot-and-reconcile-async read posture (see `list` — the
callback never blocks a read on file I/O or the mutation lock),
so a deleted or changed declared host is invisible to every live consumer by
its next read, not only after the next host-management request (extending the
round-twenty-three reconciliation, which reconciled only on admitted
`evener/host/*` methods while claiming all live consumers update on the next
admission — navigation/source/host-admin consumers kept using deleted hosts
until a host-management request arrived).
The UI cannot remove or re-add these hosts; only the entry-change rule moves
their generation. Token bindings
reference these generations (see `plan`): validation requires the token's
generation to equal the registry's current generation for the name. Snapshot
publications carry the generation of the source they were read from, and a
publication whose generation no longer matches the registry's current
generation for that name is rejected; **re-add also clears the name-keyed
caches** (snapshot rows and last-known preflight facts) as part of its staged
commit — an in-flight refresh from the removed incarnation or a stale
name-keyed cache entry can never republish rows for the new host.

## Error handling

- Attach failures: typed `SessionUnavailable` via the #1603 classifier; the
  caller-context error stays raw; the UI renders the classification, not the
  raw chain.
- Deploy: dedup-first processing order is fixed; **conflicting operation-ID
  reuse → typed refusal**; invalid/expired/superseded
  tokens → refusal, always; **detached channel at re-probe time → typed
  `host-detached` refusal (token unconsumed, no record; UI Connects and
  re-plans — matching `restart` is deliberately NOT the shape: restart
  attach-firsts because it spends no token)**; **execution-time re-resolution mismatch — entry,
  target, generation, or `hub.toml` fingerprint, on `deploy` or `restart` → typed
  stale-entry refusal with a re-plan/retry instruction**; **a held per-host
  gate → typed busy refusal — `host-busy-operation` naming the in-flight
  operation when a deploy/restart record holds the gate, including an
  Ensure-triggered deploy (a durable fenced op-store record — its busy error
  names the Ensure operation exactly like a user deploy, open/wait-able; see
  the holder classes in the operation store and the 08b busy-holder tests),
  and transient `host busy (plan in progress)` with no operation reference
  only for `plan`'s validation-plus-mint window, which holds no op-store
  record**; plan-time refusals surface verbatim
  (`errControllerDirty` is terminal — the UI must not offer retry-anything;
  unmet prerequisites shown before confirmation); runtime failures land in
  the operation record verbatim; `interrupted` records tell the user the
  outcome is unknown and a new operation may be started.
- Host busy: a held per-host gate fails `update`, `remove`, `plan`, and any
  new `deploy`/`restart` fast with a typed busy error: the
  operation-held form names the in-flight operation, and the UI surfaces
  "operation in progress" with open-or-wait — an Ensure-held gate takes this
  form too, naming the Ensure operation's record (extending the
  round-twenty-three busy-class rule — this paragraph lumped Ensure with the
  transient form while the holder classes above already made Ensure-triggered
  work a record-holding operation, so Error handling hid the open/wait
  affordance for a wait-able op); the transient
  plan-held form carries no operation reference, and the UI shows
  "host busy (plan in progress)" with retry and no open/wait
  affordance. A refused mutation leaves the running
  operation untouched — it finishes and records its normal terminal state.
- Hot-apply failures: staging failures change nothing; a failed swap
  compensates by restoring the prior sidecar bytes and reverting the runtime
  (both before the response), and the error carries which seam
  failed (registry / manager / source / admin controller / web view /
  persistence / decision-source validation).
- `evener/host/*` on an unknown host name: typed not-found.

## Testing

- 08a: registry live-update tests (including the add-time cycle rules),
  manager add/remove-vs-supervisor tests (removing an attached host stops
  its supervisor, closes its channel, and drains its per-host lifecycle
  handles before the entry is gone), sidecar merge/atomicity tests
  including the refuse rules, **the 63-remote-host cap (63 live remotes
  add; the 64th fails `ErrTooManyHosts`)**, the boot-time hard-error duplicate, **swap-failure
  compensation (prior sidecar bytes restored, runtime reverted, retry
  re-applies cleanly)**, and **the corrupt/schema-invalid sidecar boot hard
  error**, the host-admin controller live-set tests (forwarded requests reach
  newly added hosts; removed hosts' fan-outs stop), **update-rebind tests (a
  running supervisor's next reconnect uses the updated entry; the rebind
  completes before the gate is released)**, tombstone tests (list
  attached → remove → rows persist and are marked stale/non-actionable in
  tree + capabilities while the manifest's `sources` array drops the host;
  **tombstones survive a controller restart; the persist bound holds (a
  removal over 500 rows / 1 MiB persists the newest-first projection with
  `rowsTruncated: true` on the tombstone and the `list` row (newest-first by
  row last-updated timestamp, never snapshot order), and the merge
  renders the indicator — never the full set silently);
  re-add purges, clears the name-keyed caches, and mints a new generation —
 publication from an obsolete generation is rejected**; **a refresh after
 remove re-merges the tombstone's retained rows (never drops them); boot
 prunes expired tombstones durably even with no mutation since expiry**),
  **decision-source live-set tests (a newly added host is accepted by
  archive/favorite validation immediately after the commit; a removed host is
  refused; validation reads the live set, never the startup snapshot)**,
  **ingress-boundary ordering tests (a remote-originated request is refused
  before admission — proven by ordering tests on the 08a surface, not by
  handler wrapping; the before-dedup / before-token-validation orderings are
  asserted in 08b where dedup and token validation ship — the tests present the
  cooperative bridge marker (the guard's only signal) and pin refusal of
  honestly-marked peer-forwarded requests, never spoof-resistance: a markerless
  request is local-originated by construction, see Non-scope)**,
  **update-generation tests (update advances the generation and
  clears/generation-keys resolved targets, facts, and bindings before
  rebinding; `expectedGeneration` present-and-current commits, present-and-
  stale is a `stale-entry` refusal committing nothing (`update` and `remove`
  both require both fields — missing either is a validation refusal committing
  nothing — with the same check-and-refusal once present); a lost-response `remove` retried after a
  re-add with no recorded same-key receipt refuses as stale (never tears down
  the new incarnation) when the generations differ, and a same-key replay
  pinned to a superseded generation returns the recorded `committed` receipt
  instead of a fresh destructive apply (the single superseded-receipt rule —
  never a fresh apply, never `stale-entry` for the same key);
  `expectedGeneration` requires `mutationId` on the UI path and keyless
  `add` retries follow the effective-fields re-read rule (never an
  `expectedGeneration` without a key; uncommitted `add` retries while the row
  is still absent, `stale-entry` once an effective field changed; `update`
  takes no keyless path — missing either field is a validation refusal))**,
  **remote-origin rejection for `list`/`add`/`update`/`remove`** (the #1603
  origin guard refuses honestly-marked peer-forwarded requests before admission — the 08a
surface only — including `teardown-retry`'s origin rejection alongside its
08a commit-point tests — `running` asserted in 08b), and a
  wiring test mirroring the 05a registration tests.
  **Mutation-idempotency tests (the operation store's cross-name rule,
  applied to mutations): cross-name/cross-kind `mutationId` replay is the
 typed `conflicting-mutation-id` refusal; same-key replay after remove/
 re-add returns the retained receipt's original result — never a fresh apply
 against the new incarnation (a retained same-key superseded-generation
 receipt is always a hit for outcome recovery, never a conflict and never a
 fresh destructive apply; a pruned receipt — past the count/TTL prune, or
 gone with its tombstone — is the typed `stale-entry` pruned-generation
 refusal, never a fresh apply either; a genuinely new mutation mints a new
 `mutationId` — extending the round-twenty testing text, which said
 superseded receipts "open fresh" while the contract replays them);
commit-then-replay of an `update` returns the receipt pinned to the
post-bump generation without rebumping.**
 **Commit-point tests: a committed-with-teardown-failure persists the
 remnant with its opaque `remnantId` in the response, replaying the original
 `mutationId` stays a no-op receipt return, and `teardown-retry` completes
 only the named teardown (unknown/purged ID → `teardown-unknown-key`
 not-found; present-but-cleared ID → the `already-cleared` success arm;
 retrying the original `mutationId` after resolution returns the resolved
 receipt; any remnant-fenced path — re-add, `update`, or `remove` on the
 remnant's name, plus `deploy`/`restart`/`Ensure`-triggered work/`plan`/attach
 on the name — refused with `remnant-open` until the retry completes;
 the persisted receipt carries exactly `{outcome, row, generation,
 incarnationId, committedAt, remnantId?, remnantResolvedAt?, bootRecovered?}` (`incarnationId` the pinned incarnation — the commit-point test pins the full five-part scoped key, so a shared-generation boot-merge looks the receipt up under the right incarnation; `remnantId`
 while the remnant is open, `remnantResolvedAt` after resolution,
 `bootRecovered` exactly on boot-recovered receipts) and the cleared marker is
 `remnantId → clearedAt` in `teardownRemnants`; a post-`remove` retry
 returns the tombstone removed-row shape, a post-add/update retry the live
  row; the retry validates against the remnant's pinned `(generation,
  incarnationId)` + `cleanupHandle` — the live entry may be absent or newer
  without blocking it — and never acts against the live entry).
 Pending-marker tests: a post-commit receipt write lost while the process
 stays alive leaves the `pendingMutation` marker staged — a replay finalizes
carrying the pre-minted `remnantId` and the pinned teardown target — a replay
re-runs the pinned teardown and finalizes the observed outcome (a real
teardown failure surfaces `committed-with-teardown-failure` with the remnant;
a clean re-run returns `committed` with no remnant — the staged provisional
outcome is never returned as-is), and the next
 mutation-path write finalizes a foreign marker for its own host before its
 own stage, while a marker for another host never blocks it (per-host
 markers; foreign-host entries ride along untouched in the same atomic
 writes).**
 **Lock-free `list` tests: `list` takes no mutation lock and prunes nothing
durably; expiry filtering is in-memory only and the durable prune lands on
the next mutation-path write — the `hub.toml`-fingerprint pre-handler check
never takes the lock on the read path (mismatch serves the last published
snapshot lock-free and schedules the reconcile asynchronously, debounced;
no read ever fails busy for a fingerprint reason; the reconcile re-compares
fingerprints on completion and reschedules on a still-present mismatch, so a
coalesced mid-flight edit is never silently lost).
Remnant-gate tests: re-add and retention expiry skip names with open
 remnants; while a remnant is open for a name, `deploy`, `restart`,
 `Ensure`-triggered work, `plan` (no-token `remnant-open` arm with
 `remnantId`, never a minted token), and attach all refuse with
 `remnant-open` naming the blocking `remnantId` — the fence is host-wide,
 never mutation-only. Config-path tests: a `--config` startup
 carries the canonical path into the web config and the sidecar + fingerprint
 derive from it. Collision tests: a tombstone/live collision restores the
 live generation strictly above the high-water mark with pre-collision
 records marked and live records unmarked. **Crash-window test: a
 `hub.toml`/sidecar live-entry collision covered by a pending reconcile
 marker whose fingerprint matches the on-disk file boots into the completed
 cleanup (no hard error) — the step-(2) write arms the validation
 fingerprint and the pending marker rides the staging write, so a crash
 between the commit rename and the staging write, or between staging and the
 cleanup rename, still boots covered, never unmarked (while an armed intent
 whose fingerprint still matches the on-disk file with a duplicate present
 still boots hard-error — hand-made); the same collision with no marker
 and no armed intent, or with a
 marker whose fingerprint no longer matches, boots into the hard startup
 error. Swap-window tests: a marker with `swapStarted: false` and
 `teardownStarted: false` finalizes without a remnant, while a marker with
 the intent written (`swapStarted: true`) but the swap incomplete recovers
 conservatively with the pinned remnant — never without one.** File-posture tests: sidecar and
 store temp files are `0600`, renames preserve the mode, and startup refuses
a file readable beyond its owner, and the stash gets the same coverage
(stash temps `0600`, mode-preserving rename, owner-only startup refusal,
stray-stash prune/ignore).**
- 08b: handler tests per method (validation, admission, classification incl.
  `plan`-as-mutation, **remote-origin rejection for
`status`/`plan`/`deploy`/`restart`/`operations`/`running`/`orphan-resolve`** (`teardown-retry`'s
origin rejection is asserted in 08a with its commit-point tests),
  the token matrix:
  missing/mismatched/expired/superseded/consumed-then-replayed-with-new-op-ID
  (pins to `token-missing`: consume deletes the row — see `deploy` step (4)),
  **expiry-across-the-wait (a token valid at the step-(2) provisional pass
  but past its TTL at the step-(4) consume is a `token-expired` refusal with
  no record — expiry is re-checked inside the consume transaction, never
  decided at provisional validation alone),**
  **supersede-between-validate-and-consume (a `plan` mint landing after
  `deploy`'s step (2) provisional pass but before its step (4) compare-and-
  consume is a `token-superseded` refusal with no record — see `deploy`
  step (4)),**
  **generation-bound (a token minted under generation N is refused after
  remove/re-add even for a byte-identical entry; live remove drops
  outstanding tokens; re-add starts with none)**,
  **token persistence (mint is a durable store write held under the gate;
  expiry reaped lazily and at boot; tombstoned hosts' tokens dropped),**
  **post-operation refresh (a completed deploy/restart publishes verified
  fresh facts to the (generation, incarnation id)-scoped last-known store; `status` reports the
  new version)**,
  **busy-holder classes (an operation-held gate names the operation —
  including an Ensure-triggered deploy, which holds its own record; a
  plan-held gate returns the transient form with no operation
  reference and the UI shows retry, not open/wait)**,
  **protocol shapes (catalog entries and the regenerated client match the
  Protocol types section field-for-field — including `incarnationId` on
  `OperationRecord` and the `operations` request/response/cursor,
  `compacted: true` exactly on tombstone replays, every `stale-entry` data
  value against its emitting path, and the `cursor-invalidated` catalog
  entry)**,
  **operations incarnation scope (a `generation` + `incarnationId` filter
  pair addresses the colliding same-generation incarnation; the response
  echoes the listed pair; a cursor minted under one pair never lists the
  other)**,
  **unconditional plan refresh (an attached `plan` refreshes even when the
  known facts are fresh — the mint's facts are never older than the refresh
  it just ran — and the 5-minute token TTL is the deploy window)**,
  **Ensure busy names its operation (an Ensure-held gate returns
  `host-busy-operation` with the Ensure record's id — open/wait-able; the
  transient form fires only for `plan`'s validation-plus-mint window)**,
  **cursor-invalidated (a mid-pagination compaction past the cursor refuses
  typed `cursor-invalidated` with the compacting `compactSeq` — the client
  restarts from page one; an over-cap first page refuses the distinct
  `cursor-too-large` discriminator with `{capBytes: 8192}` — both shapes
  pinned)**,
  **teardown-retry timeout arm (a retry whose bounded teardown run times
  out returns the declared `committed-with-teardown-failure` arm with the
  still-open remnant's details — pinned alongside the two success arms)**,
  **the running probe (`evener/host/running` handler: local revision +
  health + optional `processStartTime`, attached-session admission only —
  unauthenticated probe refusal;
 browser/forwarded requests refused; never forwarded onward (no A→B→A chain);
  the ungated `plan` probe call with its explicit deadline (timeout →
  no-token `probe-failed` refusal with no gate ever held, never an extended
  busy hold); `HostPlan.runningVersion` /
  `runningHealthy` placement; handler-absent named for pre-handler
 remotes with the one-time manual-upgrade migration path; no-token `reason`
 discriminates `unattached` | `refresh-failed` | `probe-failed` |
  `handler-absent` | `remnant-open` | `controller-dirty` |
  `target-unwritable` | `target-missing-prereq` | `target-unit-findings`
  (with `remnantId` naming the blocking remnant — see the remnant gate —
  and `terminal: true` exactly on the four terminal arms) and the UI
  branches on it — no Connect loop for attached probe failures; an
  unverifiable probed revision reads as outdated (restart follows) unless
  `processStartTime` proves the live process)**,
  **restart reattach (the worker retains the gate across the channel drop,
  reattaches through `attachUnderGate` for the pinned entry — never the
  normal attach path, no supervisor start until the post-verification
  handoff — and re-probes over the
  reattached channel; an initially unattached restart attach-firsts under
  the same gate and names it in the record; a reconnect-after-restart test
  pins that the supervisor owns the channel again once the gate releases)**,
  **the ungated refresh: an attached host refreshes via the
  bounded one-shot SSH preflight (no channel initialization, no supervisor,
  no attach state machine, no gate held) then acquires the gate, re-checks,
  and mints; an unattached host gets the
 no-token refusal naming Connect; the deploy/restart worker's post-operation
 preflight is channel-free under the same pin**, execution-time re-resolution mismatch
  **including the `hub.toml` fingerprint (a manual edit between plan and
  deploy refuses; a manual edit between restart's resolution and its gate
  acquisition refuses) and the post-acquisition entry re-read (`restart`
  and `Ensure`-triggered work refuse or re-resolve when a mutation lands
  between resolution and gate acquisition)**, **(host, kind, generation,
  incarnation id)-scoped operation-ID dedup including the
  interrupted-record path, the compacted-ID tombstone path (replay returns the
  tombstoned terminal result, never a fresh operation), the
  clean-slate re-add path (op-ID reuse after remove/re-add opens fresh),
  and the conflicting-reuse refusals: same ID different host,
  deploy-vs-restart, and a current-generation `host-removed` record
  (IDs used up only by a removed incarnation are cleanly reusable — the
  re-add path above)**, per-host
  gate serialization
  vs an in-flight `Ensure` deploy, **worker lifetime (a client disconnect
  after record creation leaves the operation running; controller shutdown
  records `interrupted` and releases the gate)**, **the busy refusals
  (`update`/`remove`/`plan`/new-deploy on a held gate) and the atomic
  consume-and-create write (a replayed deploy after a crash in that window
  finds a record, never a silently consumed token)**, startup reconciliation
  of pending/running → interrupted **plus the tombstone-derived
  `host-removed` pass (a crash between a remove's sidecar commit and its
  live mark still never-matches at boot)**, **the corrupt operation-store
  boot hard
  error**, **orphan fencing (a crashed worker's local process group is reaped
  at boot only through its persisted boundary-plus-nonce — a persisted-but-
  empty boundary after a pre-spawn crash reaps nothing and drops the intent —
  and a fresh operation runs under a new fencing epoch through the remote
  lease wrapper (atomic register+fence+perform per mutation; lease ownership
  token verified remotely before any kill) — no overlap with orphaned local
  or remote work; a helper-absent or older/untrusted-helper host refuses
  fail-closed before any remote mutation with no auto-install and no
  in-band migration — out-of-band install only — and first-ever-contact
  bootstrap converges its marker in the finalizing write)**, **the cross-file intent (a crash
  between the sidecar commit and the store sync converges to the committed
  sidecar's view in both directions; the store purge lands only after swap
  success, and swap-failure compensation re-inserts exactly the purged token
  rows its sidecar restore revalidates)**, **plan publish (every `plan`
  refusal and every completed probe lands in the (generation, incarnation id)-
  scoped running-state/refusal snapshot — `status` after a plan refusal
  renders the refusal, never stale data)**, **the running-health definition (each
  forced-false condition returns `healthy: false` as data while the probe
  itself succeeds — evaluated by the serving hub's local predicate, never the
  `restartRequiredDaemon` probe path)**, **pinned pagination (stable
  `id`-ascending order for both sort and resume across concurrent terminal
  writes (`createdAt` display-only — a post-cursor record stamped
  pre-cursor by clock rollback still lists, and backward timestamps never
  reorder a page — pinned); mid-pagination generation advance keeps
  later pages on the pinned incarnation; cursor carries `(generation,
  incarnationId, compactSeq, presenceEpoch, lastId)` in the `v: 2` envelope with pair-mismatch (`stale-entry`
  re-list) and post-cursor compaction (`cursor-invalidated`) both surfaced as
  refusals, never silent page shifts; host-pinned pages carry the top-level
  pair while unfiltered cross-host pages omit it (`hostBoundaries`
  authoritative — the shapes test pins the absence); the cursor pins every
  host in the query at creation, including absent ones (absent encodes as the
  literal `"absent"` string, pinned field-for-field) — a host advancing
  generations between pages is a `stale-entry` re-list refusal, and a host
  removed after mint (presence epoch advanced, triple preserved) is a
  `stale-entry` re-list refusal the same way; a host created
  after mint is skipped on later pages (fresh read only); the versioned
  base64url envelope is capped at 8 KiB encoded (over-cap first page refuses
  `cursor-too-large` with `{capBytes: 8192}`, pinned as its own
  discriminator))**,
  **Darwin orphan ownership (Linux reaps through the cgroup boundary; Darwin
  reaps through the (pgid, session id) boundary plus the launcher-observed
  (pid, start time) marker — never the group id alone — with a pid whose
  start time differs reading as already clean (never signaled), and fails
  closed with durable `orphan-unverified` (resolved by retry at a later boot
  or by the authenticated `evener/host/orphan-resolve` call — the record
  carries `orphanBoundary`, the shapes test pins the `orphan-unverified` state,
  and the detail filter still resolves through `id`) when
  enumeration is unavailable — and while the record is open the host admits
  no new operation past admission (transient busy until verified or resolved
  through `orphan-resolve`; the fresh operation starts only after local reap completion);
  fencing kill/wait run under bounded contexts (kill/wait timeout → terminal
  fencing-failure outcome plus a durable per-host quarantine — the host
  admits no new mutation until a later guard advance succeeds or the
  operator resolves through `orphan-resolve` — never a stuck host and never
  an operable-but-unfenced one)**), and
  the not-in-forwarded-allow-list
  assertion.
- 08c: `make test-web`, browser gate (`env -u DBUS_SESSION_BUS_ADDRESS make
  test-web-browser`), `make lint-generated`; per-flow tests in the section's
  test files; the never-dial invariant of `list`/`status`/`operations`
  test-pinned, with `list`/`status` serving last-known facts, ages, and
  attach errors from the manager store for offline hosts.
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
   re-planned — including a manual `hub.toml` edit, caught by the
   fingerprint; an edit attempted while the deploy runs is refused with a
   busy error and the push is unaffected; after confirm, the host reports
   the new version in `status` via the worker's required post-operation facts
   refresh (published to the (generation, incarnation id)-scoped last-known store before the
   operation marks `complete`), and the version-skew signal (facts vs
   controller build) is truthful; a replayed deploy with the same operation
   ID returns the same record; a controller crash mid-deploy surfaces as
   `interrupted`, never a stuck operation.
4. Hand-edited `hub.toml` hosts keep working exactly as today; UI add/update
   refuse their names; the origin marker shows which file owns each entry; a
   hand-created duplicate name across files is a hard startup error.
5. Removing a sidecar host retains its last-known-good rows as an explicitly
   stale, non-actionable tombstone (tree + action capabilities, test-pinned)
   while the manifest's `sources` array drops the host; the tombstone
   survives a controller restart; re-adding the same name
   purges it, clears its name-keyed caches, and mints a new generation, so
   stale rows from the old incarnation cannot republish; `list` shows it
   with `removed: true`.
6. No lazy-attachment regression: with no attach call, no explicit source,
   no read dials anything — `list`/`status`/`operations` never attach
   (test-pinned) — and `plan`'s facts refresh and the deploy/restart worker's
   post-operation preflight, the surface's two deliberate
   non-attach SSH uses, are channel-free by construction (no initialize, no
   supervisor, no attach state machine; test-pinned).
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
