# Slice-0 audit: client slow-read sweep + ctx-binding cancellation traces

Audited: origin/main @ `173411a6add72b9d05165694bfe875871e3aed4c`
Spec: `e3444feda:docs/superpowers/specs/2026-08-30-appserver-serial-worker-dispatch-design.md`
Auditor scope: spec's client-side sweep (validates `slowReadDispatchCap = 4`) and the
deep ctx-binding cancellation analysis. Read-only; no repo files modified.

---

## Section 1 — web/TUI slow-read call-site sweep

Method set swept: `thread/read`, `thread/turns/list`, `evener/subagentPreview`.
Connection model verified: one browser tab = one `AppwireClient` = one WebSocket to
the hub; one TUI process = one `appwire.Client` connection. Both multiplex concurrent
requests over the pending-request map (`appwire/client.go`). Bubbletea's `tea.Batch`
runs each Cmd in its own goroutine, so a batch of N commands is N concurrent
in-flight requests on the one connection.

Concentration check (not in scope but load-bearing for the cap's meaning): the hub's
relay to the daemon does NOT concentrate client fan-out onto one hub→daemon
connection. `LocalDaemonSource.withClientCallMapper`
(`cmd/evener-hub/internal/appsource/local_daemon.go:578`) dials a fresh connection
per call, and subscribe-reads go through per-thread relay sessions. The cap
therefore binds exactly on the client↔hub connections swept below (and on any
per-thread relay connection, where fan-out is ~1).

### Call-site table

| # | Site | Flow that triggers it | Max plausible in-flight fan-out per connection | Bound mechanism |
|---|------|----------------------|------------------------------------------------|-----------------|
| W1 | `cmd/evener-hub/frontend/src/stores/threads.ts:933` (`hydrateAndSubscribe`, `thread/read` IncludeTurns:true) | Pane mount: `ensureThread` from `panes/session/Session.tsx:134`, `panes/transcript/Transcript.tsx:71`, `panes/sessionPanels/SessionPanelPane.tsx:40` | 1 per distinct ref; total = # of distinct thread refs across mounted panes. Dockview workspace (`shell/workspace.ts`) has no pane cap; opening a session plus several child transcripts beside it is a supported flow (`openBeside`, delegate row "open ⤢") | `inflightHydrates` map dedups per ref (`threads.ts:235` comment); pane count is user-controlled, uncapped |
| W2 | `cmd/evener-hub/frontend/src/stores/threads.ts:981` (`hydrateAndSubscribeWatch`, `thread/read`) | Every rendered delegate card: `SubagentCard` (`panes/session/transcript/tools/subagentModule.tsx:188`) calls `watchThread(ref, {includeTurns: true})` on mount — a FULL child read — and the delegate tool renderer is `autoExpand: () => true` (`subagentModule.tsx:401`), so every delegate row in the visible transcript mounts its card expanded and takes the watch | **N = delegate rows rendered at once.** A turn with 4–8 parallel delegates renders 4–8 cards in one commit → 4–8 concurrent full `thread/read`s. Parallel-delegate turns of ≥4 are routine (this audit itself is 4 parallel subagents) | Per-ref dedup (`inflightWatchHydrates`); N bounded only by delegate rows in the rendered viewport — no client-side concurrency limit |
| W3 | `cmd/evener-hub/frontend/src/stores/threads.ts:2337` (`loadOlderTurns`, `thread/turns/list`) | Scroll-to-top paging in a transcript | 1 per transcript pane | `inFlightRef` re-entrancy guard (`panes/session/transcript/useTranscript.ts:41`) |
| W4 | `cmd/evener-hub/frontend/src/stores/threads.ts:1809` (`handleReady` resync — issues W1+W2 in bulk) | Reconnect / client-ready transition: `Promise.all` over **all** tracked refs ∪ pending hydrations ∪ pinned mutation refs, plus **all** watched refs, simultaneously; discovered pinned refs then recurse into `handleReady` per ref | **Widest single burst in the codebase: panes + watched delegate cards + pinned refs, all at the same instant.** One session pane with 6 delegate cards = 7 concurrent `thread/read`s minimum | None. `Promise.all` with zero concurrency limiting; per-ref dedup only |
| W5 | `evener/subagentPreview` | — | **0. No call sites** in `cmd/evener-hub/frontend/src/` (only the generated catalog `protocol/types.gen.ts`) and none in `cmd/evener-tui/`. The legacy `renderer.js` that presumably used it is gone from `cmd/evener-hub/assets/` | Handler (`cmd/evener-hub/app_rpc.go:357`) is currently client-orphaned |
| T1 | `cmd/evener-tui/hub_commands.go:210` (`fetchHubSessionRead`, `thread/read` full) | Session entry (`hub_keys.go:208,255`, `hub_update.go:387,423,444`), status refresh (`hub_notifications.go:82`), resync (`hub_notifications.go:132`) | 1 | Single command per event |
| T2 | `cmd/evener-tui/hub_commands.go:224` (`subscribeChildActivity`, `thread/read` lean, Subscribe:true) | `subscribeNewChildren` (`hub_notifications.go:519`) returns `tea.Batch` of one per running, unwatched subagent child. Fires on session entry (`hub_update.go:141`), on job/delegate notifications (`hub_notifications.go:156,166`), and on reconnect (`hub_reconnect.go:93`, after `watchedChildRefs = nil` reset — so the full N re-fires) | **N = running subagent children of the viewed session**, all concurrent (tea.Batch goroutines). Reconnect adds `resyncHubSession`'s own full read → **N+1** | `watchedChildRefs` dedup per ref; N bounded only by how many subagents the session runs in parallel |
| T3 | `cmd/evener-tui/hub_commands.go:231,254` (`fetchHubStatus`, `fetchHubTranscript`, `thread/read` full) | Status panel, transcript picker | 1 each | Single command per event |
| T4 | `thread/turns/list` in TUI | — | 0 — the TUI never calls `ThreadTurnsList` | n/a |

### Cap verdict

**Legitimate flows exceed 4 concurrent slow reads on one connection. The spec's
revision clause fires.**

- **Web, delegate-card mount (W2):** a single turn with ≥4 parallel delegates puts
  ≥4 full `thread/read`s in flight in one React commit; the session's own hydrate
  (W1) can overlap it. ≥5 concurrent is a routine render, not an abuse pattern.
- **Web, reconnect resync (W4):** `handleReady`'s unbounded `Promise.all` is the
  worst case — every open pane + every watched delegate card + pinned refs at
  once. 7–15 concurrent `thread/read`s from one busy session view is realistic.
- **TUI, session entry / reconnect (T2):** N+1 concurrent `thread/read`s for N
  running subagent children; N≥4 is a normal parallel-fan-out session.

Consequence under `slowReadDispatchCap = 4` as specified: no failure — the 5th
read parks the worker in the acquire and head-of-line blocks every queued request
(including `turn/interrupt`) until a slot frees. Because all these reads are
answered from in-memory daemon snapshots (`appThreadReadSnapshot`,
`server/appwire_runtime.go:1024` — "answers entirely from memory") or the hub's
equivalents, slots normally free in milliseconds, so the practical cost is
latency, and reconnect hydration serializing 4-at-a-time. But the cap WILL be
routinely saturated by normal UI behavior, which also makes the one-shot
"first blocked acquire" advisory fire in ordinary operation, degrading its
signal value.

Recommendation with evidence attached: raise the constant to **8** (covers the
common 4–8 parallel-delegate case and TUI N+1) or **16** (covers reconnect
bursts of a heavily-split workspace); alternatively bound the client side
(a small p-limit in `handleReady`, and lean child watches in W2 like the TUI's
IncludeTurns:false). Either resolution satisfies the spec's stated rule that
the constant is revised where the evidence lands; 4 as-is means the head-of-line
scheduling change is exercised constantly rather than in a rare corner.

Secondary observation for the disposition table: `evener/subagentPreview` (W5)
has zero client callers on origin/main — the concurrent set's third member is
dead client-side. Nothing to change in the design (the method-set test pins the
catalog), but no fan-out analysis needs to account for it.

---

## Section 2 — ctx-binding cancellation traces

Contract under test (spec, "Admitted-request semantics on disconnect"): an admitted
request may observe connection cancellation at any await point; its side effects
are whatever it completed before observing it. A handler is UNSAFE if mid-flight
cancellation can leave inconsistent durable state — a partial write without
cleanup or idempotent retry.

### Daemon: the six ctx-binding handlers (`server/appwire_runtime.go`)

**`thread/read` (`handleAppThreadRead`, :978) — SAFE.**
Non-subscribe path is a pure in-memory struct copy (`appThreadReadSnapshot`).
Subscribe path runs `appserver.CaptureSubscription`, which verifies
`server.conns[conn.id] == conn` under the projection gate — cancellation racing
teardown either completes the capture before the unregister sweep (and is swept)
or observes unregistration and aborts without registering. No durable writes
anywhere. Await points: none that touch durable state.

**`model/list` (`handleAppModelList`, :1886) — SAFE.**
Read-only: `listModelsFunc(ctx)` fails cleanly on cancellation, error propagates,
nothing written.

**`thread/unsubscribe` (`handleAppThreadUnsubscribe`, :1047) — SAFE.**
Only calls `appserver.Unsubscribe` (idempotent, identity-checked under
`server.mu`, deliberately no projection gate). Connection-owned state, not
durable state; an unsubscribe after unregistration is a no-op by the seam fence.

**`turn/interrupt` (`handleAppTurnInterrupt`, :1299 → `Session.InterruptClientMutation`, `agent/session_client_mutation.go:503`) — SAFE.**
Two paths:
- *Joined waiter:* `select { <-lookup.OwnerDone; <-ctx.Done() }` — cancellation
  returns `ctx.Err()` having written nothing; the owner continues independently.
  A reconnect retry with the same `ClientMutationID` replays or re-joins.
- *Owner:* after `reservePrepared` persists the interrupt fence, the only
  subsequent await is `cancelAndWait()` (`cancelAndWaitMutationRunner` in
  `cmd/evener/serve.go`), which takes no ctx and runs to completion regardless of
  cancellation. There is no ctx-sensitive await between the durable fence write
  and finalization, so the owner cannot be abandoned mid-way; worst case it parks
  the worker (the cooperative-cancellation bound the spec already accepts). The
  doc comment's recovery path ("if the runner does not finalize the fence,
  recovery finalizes it after the wait returns") covers the crash shape.

**`thread/clear` (`handleAppThreadClear`, :1479) — SAFE, and stronger than the spec states.**
The handler's one await between durable writes is `fn(ctx, params)` — but the
installed callback (`srv.SetClearFunc`, `cmd/evener/serve.go:932`) **binds ctx and
never uses it** (verified: the closure's only mention of `ctx` is its signature;
provisionSandbox / newClearSession / prepareAppIdentity / updateSessionID /
ReplaceAppIdentity all take no ctx). Prompt connection cancellation therefore
cannot interrupt a clear in flight: the callback runs to completion, the applied
record persists (`persistThreadClearJournal`, atomic rewrite), and a reconnect
retry with the same `ClientMutationID` replays the receipt — including through
the crash-recovery branch when the process died between replacement and receipt.
The journal reserve/rollback machinery (beforeReservation / reserved snapshots)
handles every fallible edge. Disposition-table note: the spec's
"failure path releases its gate and admits a retry" describes callback *errors*;
cancellation can never reach that path because the callback is ctx-deaf. Nobody
should "fix" clear for a cancellation it cannot observe.

**`thread/compact/start` (`handleAppThreadCompactStart`, :1448 → `Session.Compact`, `agent/session_compaction.go:20` → `contextmgr.ForceCompact`, `agent/internal/contextmgr/context_manager.go:437`) — SAFE.**
Cancellation trace through ForceCompact's layers:
- Layer 1 (checkpoint) is deterministic local computation — always completes,
  produces a valid history state.
- Layer 2 (`summarizeWithLLMSteered(ctx, …)`) on a canceled ctx returns an error;
  ForceCompact emits a warning and **keeps the checkpointed history** — the same
  degraded-but-valid state any LLM failure produces.
- Back in `Compact`: `s.history = histCopy` installs the whole (valid) history and
  `maybeAutoSave()` persists a complete consistent snapshot. There is no partial
  durable write at any interleaving; a canceled compact is a checkpoint-only
  compact. Re-issuing compact later is safe (idempotent-ish; worst case a second
  compaction pass).
- Pre-compact plugin hooks (`runPreCompactHook(ctx, …)`) failing under
  cancellation skip their steering injection; in-memory only until the same
  whole-state save.

### Hub: marketplace/plugin family (`cmd/evener-hub/app_rpc.go:741-770`, `app_plugin_autoupgrade.go:114` → `internal/plugins.Manager`) and `device/poll`

Shared mechanics verified first:
- Registry and marketplace metadata writes are atomic (`atomicWriteFile`:
  O_EXCL temp + fsync + rename + parent-dir fsync — `internal/plugins/atomic.go:32`),
  and in every flow the metadata write is the LAST step, after all fallible work.
- `acquireLock` (`internal/plugins/locks.go:27`) is timeout-based (30s flock spin)
  and does **not** select on ctx: a canceled handler still parks the worker up to
  30s waiting for the lock. Not a durable-state issue, but a named 30s floor on
  the cooperative-cancellation bound the spec's liveness narrative should carry.
- `git` runs via `exec.CommandContext` with **no `cmd.Cancel` override and no
  `WaitDelay`** (`internal/plugins/git.go:27`), so ctx cancellation SIGKILLs git
  mid-operation — git gets no chance to remove its own lock files.

**`evener/marketplace/browse` (`Manager.Browse`, `internal/plugins/catalog.go:131`) — SAFE.**
Lazy fetch path: `ensureFetched` (`marketplaces.go:107`) clones into the final
`marketplaceDir(name)` — cancellation mid-clone leaves a partial directory there,
BUT `InstallLocation` is only persisted to the marketplaces file after a
successful clone, so the marketplace still reads as unfetched, and the next
attempt begins with `marketplaceRemoveAll(installLoc)` before re-cloning.
Self-healing by retry; no reader consults the directory except through the
persisted `InstallLocation`.

**`evener/plugin/install` (`Manager.Install`, `internal/plugins/install.go:97`) — SAFE.**
ctx is live only inside `ensureFetched` (covered above) and
`fetchPluginSource(ctx, …, staging)`; a canceled fetch removes the staging dir
(`stagePlugin`'s error path, and the next attempt's unconditional
`installRemoveAll(staging)`). Everything after the fetch — `commitStaged`
(rename), manifest fallback, validation (each with cleanup on error), registry
load + atomic save — is ctx-deaf local work that runs to completion. The
registry write is last; a canceled install leaves at worst an orphaned cache
dir that the gc sweep reclaims.

**`evener/plugin/upgrade` (`Manager.Upgrade` → `upgradeLocked`, `install.go:173,203`) and `evener/plugin/checkNow` (`runPluginAutoUpgradeTick` → `UpdateAutoUpgrade` → `upgradeAuto`, `internal/plugins/autoupgrade.go`) — SAFE.**
Same shape as Install: fetch (cancelable, staging cleaned) → local commit/validate
(with `installRemoveAll(final)` on error) → registry repoint last, atomic. The
old install dir is never deleted (gc reclaims), so a canceled upgrade leaves the
previous version fully live. checkNow's sweep is failure-isolated per plugin;
cancellation mid-sweep errors the remaining plugins without touching them, and
eligibility/sha checks are re-done fresh under the lock on retry.

**`evener/marketplace/refresh` (`Manager.RefreshMarketplace`, `internal/plugins/marketplaces.go:225`) — UNSAFE. Spec gate.**
Two branches:
- Never-fetched: wipe-and-clone into final loc — self-healing exactly like
  Browse. Safe.
- **Already-fetched: `marketplaceGitPull(ctx, ref.InstallLocation)` runs
  `git pull --ff-only` inside the persistent marketplace clone.** On ctx
  cancellation, `exec.CommandContext` SIGKILLs git mid-pull, which can strand
  git's own lock files (`.git/index.lock`, `FETCH_HEAD` lock, etc.) in the
  clone. From then on **every subsequent RefreshMarketplace of that marketplace
  fails at the same pull, and nothing heals it**: the error return leaves
  `InstallLocation` set, so the retry takes the pull branch again (never the
  wipe-and-reclone branch); `ensureFetched` sees a fetched marketplace and does
  nothing; `doctor.go` has no stale-git-lock finding or remediation. The
  marketplaces metadata file itself stays consistent (saved only after success,
  atomically) — the inconsistent durable state is the clone working directory,
  wedged until a human removes the lock file or removes/re-adds the
  marketplace. This is precisely "partial write without cleanup/idempotent
  retry". It is reachable today under PR #667 (send-path failure or shutdown
  canceling the shared ctx mid-pull), but the worker's prompt
  disconnect-cancellation makes it an everyday path: closing the tab during a
  marketplace refresh kills git mid-pull.
  Remediation shapes (any one suffices, smallest first):
  1. On pull failure in RefreshMarketplace, fall back to wipe-and-reclone —
     the never-fetched branch is already exactly that code.
  2. Give `git()` a graceful `cmd.Cancel` (SIGTERM) + `WaitDelay` so git can
     clean its locks on cancellation.
  3. Detach the pull from connection ctx (run under a server-lifetime context).
  Per the spec's slice-0 rule, this fix (with a test pinning
  canceled-pull-then-successful-retry) must land before or with the worker.

**`evener/auth/device/poll` (`hubAuthController.DevicePoll`, `cmd/evener-hub/app_auth.go:453`) — SAFE for durable state.**
Await-point trace:
1. `pollDeviceOnce(ctx, …)` canceled → error return; no writes; the in-memory
   `deviceFlows` record survives; a reconnect retry polls again. Safe.
2. `exchangeDevice(ctx, …)` canceled → error return; no local writes. Worst
   case the provider consumed the single-use authorization code without the hub
   receiving tokens, so the retry's exchange fails and the user restarts login
   (`device/start`). A burned login attempt is a UX cost, not inconsistent
   durable state — `saveAuth` (the only durable write) was never reached.
3. `saveAuth` is not ctx-gated and completes atomically once entered; the flow
   record deletion after it is in-memory. A disconnect after saveAuth loses only
   the response — the next `auth/status` reports authorized. Consistent.

**Bonus row — `evener/git/head` (`cmd/evener-hub/app_git_head.go`) — SAFE.**
Read-only `git rev-parse` via `exec.CommandContext`; cancellation kills a
read-only process, no repository state touched.

### Verdict summary

| Handler | Verdict |
|---|---|
| daemon `thread/read` | SAFE |
| daemon `model/list` | SAFE |
| daemon `thread/unsubscribe` | SAFE |
| daemon `turn/interrupt` | SAFE (owner path has no ctx-sensitive await after the fence) |
| daemon `thread/clear` | SAFE (callback is ctx-deaf; cancellation unobservable mid-clear) |
| daemon `thread/compact/start` | SAFE (degrades to checkpoint-only; whole-state save) |
| hub `evener/marketplace/browse` | SAFE (retry wipes and re-clones) |
| hub `evener/plugin/install` | SAFE (staging cleanup; registry write last, atomic) |
| hub `evener/plugin/upgrade` | SAFE (same; old dir preserved) |
| hub `evener/plugin/checkNow` | SAFE (failure-isolated sweep of the same safe path) |
| hub `evener/marketplace/refresh` | **UNSAFE — SIGKILLed `git pull` can wedge the persistent clone; retry does not heal; remediation gates slice 1** |
| hub `evener/auth/device/poll` | SAFE (worst case: burned single-use auth code, fresh login; no partial durable write) |
| hub `evener/git/head` | SAFE (read-only) |

### Spec-assumption notes for the assembler

1. **Cap evidence (Section 1): legitimate fan-out exceeds 4** — web reconnect
   resync (unbounded `Promise.all`), auto-expanded delegate-card watches
   (`includeTurns: true`, N = parallel delegates), TUI `subscribeNewChildren`
   batch (N+1 on reconnect). The spec's own revision clause applies; 4 will be
   saturated by routine UI behavior and the one-shot cap advisory will fire in
   normal operation.
2. **`evener/subagentPreview` has zero client call sites** on origin/main (web
   and TUI both) — the concurrent set's third member is client-orphaned.
3. **`thread/clear`'s cancellation tolerance is by ctx-deafness, not by its
   gate-release/retry shape** — record this in the disposition table so the
   remediation pass doesn't chase a cancellation path the callback cannot
   observe.
4. **Plugin-family lock acquires (30s flock spin) do not select on ctx** — a
   canceled marketplace/plugin request can park the worker up to 30s before its
   handler even starts its cancelable work. Within the spec's accepted
   cooperative-cancellation bound, but it deserves the named floor.
5. **`git()` has no graceful-cancel configuration** (`exec.CommandContext`
   default SIGKILL, no `WaitDelay`) — this is the root cause behind finding #
   (marketplace/refresh) and would harden every git call site if fixed once at
   `internal/plugins/git.go:27`.
