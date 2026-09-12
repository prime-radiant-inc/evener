# AppServer serial worker dispatch — slice-0 handler disposition audit

Date: 2026-08-30
Status: complete; gates evaluated and resolved (Jesse, 2026-08-30)
Spec: `docs/superpowers/specs/2026-08-30-appserver-serial-worker-dispatch-design.md`
(PR #679, audited against spec tip `e3444feda`). The spec asks for this table to be
appended to it; the spec lives on `review/serial-worker-dispatch-design`, so the
table lands here as a companion document and travels with the implementation PR.
Audited tree: `origin/main` = `173411a6add72b9d05165694bfe875871e3aed4c`

Method: four parallel auditors produced the shards below (A: `app_rpc.go`
registrations 1–35; B: `app_rpc.go` 36–70; C: the 12 non-`app_rpc.go` hub
registrations, the 2 raw `Router().Handle` registrations, and the 24 daemon
registrations; D: the web/TUI slow-read call-site sweep plus the deep
ctx-binding cancellation traces). The assembler verified the counting
convention against a fresh sweep (70 + 12 + 2 + 24 = 108 = 82 hub HandleTyped +
2 raw + 24 daemon — the spec's checksum), spot-checked registration sites and
the load-bearing code claims (RefreshMarketplace's pull-in-live-clone branch,
`internal/plugins/git.go`'s SIGKILL-on-cancel exec configuration, the
delegate-card `watchThread(includeTurns: true)` + `autoExpand` pair, and
`handleReady`'s unbounded `Promise.all`), and cross-checked the daemon and hub
tables against two independently produced enumerations; the daemon 18/6 ctx
split and the identity of all six ctx-binding daemon handlers were confirmed by
both sweeps.

## Gate outcomes (decided: Jesse, 2026-08-30)

The audit tripped both gates the spec names. Neither blocks the worker; both
resolutions are recorded here and supersede the shard-level dispositions below.

1. **Cancellation-safety gate — tripped by `evener/marketplace/refresh`,
   with `evener/plugin/checkNow` sharing the root cause and `thread/start`
   flagged for its no-dedup abandonment residue.** A ctx-canceled
   `git pull --ff-only` inside the persistent marketplace clone is SIGKILLed
   (`exec.CommandContext` with no `Cancel`/`WaitDelay` override) and can strand
   `.git` lock state that no code path repairs — the already-fetched branch
   never falls back to reclone. Resolution: remediated in the standalone PR
   `review/slice0-audit-remediations` (staged-reclone self-heal on pull
   failure, SIGTERM + `WaitDelay` on `git()`, ctx-aware flock acquisition),
   which merges before the worker PR; `thread/start`'s admitted sequence is
   shielded there with `context.WithoutCancel`, and its retry-duplicate residue
   (a client retry after abandonment spawns a second session — a pre-existing
   ambiguity, not corruption) is tolerated. The worker's prompt-cancellation
   behavior therefore does not deploy ahead of a handler that cannot take it.
2. **Slow-read cap gate — tripped: routine client flows exceed 4 concurrent
   slow reads on one connection.** Evidence (shard D): the web delegate rail
   mounts every visible delegate card auto-expanded with a full
   `thread/read` watch (N = parallel delegates, 4–8 routine); web reconnect
   (`handleReady`) fires an unbounded `Promise.all` over every tracked +
   watched + pinned ref (7–15 realistic); the TUI's `subscribeNewChildren`
   batch is N+1 concurrent reads on session entry/reconnect. Resolution:
   `slowReadDispatchCap = 16` (the spec's revision clause, exercised with this
   evidence; the spec doc is amended in parallel). The cap's saturation
   *behavior* — blocking, advisories, recovery — is unchanged.

## Assembler dispositions of the shard flags

| flag | row | disposition |
|---|---|---|
| A/F1 `thread/start` | shard A row 6 | remediated via `review/slice0-audit-remediations` (`context.WithoutCancel` shield on the admitted sequence); duplicate-on-retry residue tolerated, pre-existing |
| B/F1 + D `evener/marketplace/refresh` | shard B row 46 | **the audit's one UNSAFE row**; remediated via `review/slice0-audit-remediations` (reclone self-heal + graceful git cancel) |
| C/F1 `evener/plugin/checkNow` | shard C hub row 8 | conditionally safe; same git root cause, covered by the same remediation PR |
| B/F2 spec location error | — | confirmed: `evener/plugin/checkNow` is registered in `app_plugin_autoupgrade.go:114`, not `app_rpc.go`; spec text amended in parallel |
| B/F3 + D flock promptness | family-wide | 30s non-ctx flock is a promptness floor, not a durability bug; ctx-aware flock lands in the remediation PR |
| D `thread/clear` | shard C daemon row | safe **by ctx-deafness**: `SetClearFunc`'s closure binds ctx and never uses it, so mid-clear cancellation is unobservable — recorded so nobody "remediates" an unreachable path |
| D `model/list` | shard C daemon row | safe; honest label is "serial outbound network read" — exactly the latency class the worker absorbs |
| D `evener/subagentPreview` | — | zero client call sites on origin/main (web and TUI); stays in the concurrent set per spec, no fan-out analysis needed |

Everything below is the auditors' work, verbatim except three assembler
corrections from per-commit review: shard A rows 4–5 normalized to the exact
registered wire names (`thread/turns/list`, `evener/subagentPreview`), and the
raw transcriptDisplay/patch row's retry description corrected (an abandoned
pre-execution patch retries successfully; the revision conflict answers a
retry of a patch that already committed).

# Slice-0 handler disposition audit — shard A (registrations 1–35)

Audited commit: origin/main = `173411a6add72b9d05165694bfe875871e3aed4c`
Rows: 35 (the first 35 `appserver.HandleTyped` registrations in `cmd/evener-hub/app_rpc.go`, file order; auditor B covers 36–70)

Column key — ctx behavior: `ignores` = registration binds `_` or never uses ctx; `binds-unused` = named ctx parameter never passed onward; `passes-through` = ctx forwarded into the delegate chain. Disposition: `safe` (pure read, or dedup/idempotent/convergent under mid-flight abandonment), `safe-note` (safe with a named residual), `FLAG` (gate input for the assembler). All line numbers are from origin/main.

| # | method | file:line | ctx behavior | side-effect shape | mid-flight-abandonment disposition | evidence note |
|---|--------|-----------|--------------|-------------------|------------------------------------|---------------|
| 1 | thread/list | cmd/evener-hub/app_rpc.go:227 | passes-through | pure read (roster + past-index aggregation) | safe | delegates to `hubThreadList` (cmd/evener-hub/app_threadlist.go:17); no durable writes |
| 2 | thread/read | cmd/evener-hub/app_rpc.go:230 | passes-through | read; may launch managed codex source as a side effect; subscription mutation via seam-fenced Subscribe/CaptureSubscription; relay goroutine | safe | managed launch (`EnsureSource`, cmd/evener-hub/internal/codexlaunch/codex_launch.go:195) uses `exec.Command` NOT CommandContext (line ~338 comment) and kills the child on canceled ready-wait (line ~430 `process.Kill()`); relay runs under `context.WithoutCancel(ctx)` (cmd/evener-hub/app_relay.go:605), so request cancel never orphans a half-bound relay; subscription paths are the spec's fenced seam; idle relays reaped by `retireIfIdle` |
| 3 | thread/unsubscribe | cmd/evener-hub/app_rpc.go:298 | passes-through | conn-owned subscription removal via `appserver.Unsubscribe` | safe | exactly the class the spec names: conn-scoped, idempotent, seam-fenced, connection-close cleanup reaps a missed key |
| 4 | thread/turns/list | cmd/evener-hub/app_rpc.go:318 | passes-through | read (live source or paged past transcript); may launch managed codex source | safe | same managed-launch shape as row 2; no durable writes of its own |
| 5 | evener/subagentPreview | cmd/evener-hub/app_rpc.go:357 | passes-through | read (live ReadThread or past transcript); may launch managed codex source | safe | same as row 4 |
| 6 | thread/start | cmd/evener-hub/app_rpc.go:389 | passes-through | multi-step mutation: launch-config resolve (reads) → `Spawner.Spawn` starts a **detached** daemon → roster refresh → `ReadThread` → optional initial `StartTurn` with a **server-generated** ClientMutationID → relay attach + Notify-on-failure | **FLAG** | `spawnDaemon` (cmd/evener-hub/spawn.go:332-341) deliberately uses `exec.Command` NOT CommandContext — cancel cannot kill or corrupt the child; ctx scopes only the rendezvous wait, and `rendezvousWaitError` (spawn.go:507-522) already documents the websocket-ctx-cancel case. But this is a ctx-binding serial mutation with awaits **between durable side effects** and NO dedup: thread/start is not in the ValidateMutationParams 8, and the initial turn's ClientMutationID is minted fresh per attempt (`identifier.NewClientMutationID()`, app_threadlifecycle.go:210-212), so a retry can never dedup. Abandonment after Spawn leaves a live orphan session the client never learned the ID of; the client's natural retry spawns a second session; if `params.Input` was set, the initial input may have been delivered to a session the client cannot correlate. Not corrupting (orphan is visible in thread/list) and the ambiguity predates the worker (undeliverable response under PR #667 had the same effect), but the worker makes abandonment strictly more common and no retry contract exists. |
| 7 | thread/resume | cmd/evener-hub/app_rpc.go:405 | passes-through | resumes a detached daemon; relay attach | safe | convergent by construction: per-session `ResumeLocks` plus roster double-check under the lock (app_threadlifecycle.go:303-338) means an abandoned resume's daemon is found and reused by the retry instead of double-spawned; `ResumeDaemon` is detached from ctx exactly like Spawn (spawn.go:423-431) |
| 8 | thread/fork | cmd/evener-hub/app_rpc.go:415 | passes-through | durable fork: local path writes a child session dir (`hubForkSession`/`hubAsideSession`) + `Past.Rebuild()`; remote path is one forwarded `ForkThread` | safe-note | single durable step, no await between durable writes, no partial state. Residual: no ClientMutationID (not in the 8), so an abandonment after the fork applied + client retry creates a **second child session**. Pre-existing ambiguity (undeliverable response had the same shape); promptness makes it more common. Duplicate children are inert sessions, not corruption. |
| 9 | turn/start | cmd/evener-hub/app_rpc.go:418 | passes-through | resolve source (may resume/spawn detached daemon) → `relays.startTurn` → prepareRelay (seam-fenced) → `source.StartTurn`; resume-then-retry on session-unavailable | safe | ClientMutationID required (one of the 8, appwire/protocol.go:189); daemon-side durable dedup answers a reconnect retry with the original outcome; abandonment between resume and StartTurn = never-started mutation, retries as fresh application; resume leg is convergent per row 7 |
| 10 | turn/steer | cmd/evener-hub/app_rpc.go:462 | passes-through | single forwarded `SteerTurn` under deletion fence | safe | ClientMutationID dedup (protocol.go:190) |
| 11 | turn/interrupt | cmd/evener-hub/app_rpc.go:477 | passes-through | single forwarded `InterruptTurn` under deletion fence | safe | ClientMutationID dedup (protocol.go:191); spec explicitly keeps interrupt in the ordered queue |
| 12 | evener/sandboxEscalation/resolve | cmd/evener-hub/app_rpc.go:489 | passes-through | single forwarded `ResolveSandboxEscalation`; NO ClientMutationID | safe-note | at most one forwarded call, no hub-side durable state, no awaits between durable writes. Residual: abandonment leaves applied-vs-not ambiguous with no dedup; recovery is user-driven (the escalation prompt either cleared or it didn't, and re-resolving is the natural retry). |
| 13 | turn/queue | cmd/evener-hub/app_rpc.go:498 | passes-through | single forwarded `QueueTurn` under deletion fence | safe | ClientMutationID (protocol.go:192); queue fences per spec |
| 14 | turn/drainAsSteer | cmd/evener-hub/app_rpc.go:513 | passes-through | single forwarded `DrainAsSteer` | safe | ClientMutationID + expectedQueueRevision (protocol.go:193) |
| 15 | turn/promoteQueuedAsSteer | cmd/evener-hub/app_rpc.go:528 | passes-through | single forwarded `PromoteQueuedAsSteer` | safe | ClientMutationID + expectedEntryId (protocol.go:194) |
| 16 | turn/cancelQueued | cmd/evener-hub/app_rpc.go:546 | passes-through | single forwarded `CancelQueued` | safe | ClientMutationID + expectedEntryId (protocol.go:195) |
| 17 | thread/clear | cmd/evener-hub/app_rpc.go:564 | passes-through | `withSessionResume` (resume-then-retry, convergent per row 7) → capability read → single forwarded `ClearThread` | safe | ClientMutationID (protocol.go:196); daemon side has the spec's named gate-release/retry shape; hub side forwards ClientMutationID into the deletion fence too (app_session_resume.go:21-48) |
| 18 | thread/compact/start | cmd/evener-hub/app_rpc.go:573 | passes-through | capability read → single forwarded `CompactThread`; resume-then-retry on session-unavailable; NO ClientMutationID | safe-note | no hub-side durable writes; the only durable effect is the daemon's compaction, launched by one forwarded call (spec names the daemon side: ctx forwarded into a single callback). Residual: applied-vs-not ambiguity on abandonment, recovered by re-compacting (idempotent-in-effect). |
| 19 | thread/shutdown | cmd/evener-hub/app_rpc.go:576 | passes-through | capability read → single forwarded `ShutdownThread`; already-exited treated as success | safe | goal-idempotent: `shutdownThreadTolerateExited` (app_session_resume.go:54) — retrying a shutdown that already applied returns success |
| 20 | thread/model/set | cmd/evener-hub/app_rpc.go:579 | passes-through | capability read → single forwarded `SetThreadModel`; resume-then-retry | safe | set-to-value: retry after abandonment converges to the same state (cmd/evener-hub/app_model.go:11-40); no ClientMutationID needed |
| 21 | thread/visionModel/set | cmd/evener-hub/app_rpc.go:582 | passes-through | same shape as row 20 | safe | cmd/evener-hub/app_vision_model.go:11-40 |
| 22 | thread/reasoningEffort/set | cmd/evener-hub/app_rpc.go:585 | passes-through | single forwarded `SetThreadReasoningEffort` under deletion fence | safe | set-to-value idempotent; no capability gate by design (comment at registration) |
| 23 | goal/set | cmd/evener-hub/app_rpc.go:597 | passes-through | `withSessionResume` → capability read → single forwarded `GoalSet`; NO ClientMutationID | safe | set-to-value idempotent on retry (app_session_resume.go:71) |
| 24 | evener/auth/status | cmd/evener-hub/app_rpc.go:605 | ignores (`_`) | pure read of credential state | safe | runs to completion regardless of cancel; parks worker only trivially |
| 25 | evener/auth/test | cmd/evener-hub/app_rpc.go:608 | passes-through | read-only network credential probe; in-memory single-flight cache only | safe | `TestCredentials` (cmd/evener-hub/app_credentials.go:52) mutates only the in-memory `credentialTests` map; cancel aborts the probe, retry re-probes |
| 26 | evener/auth/login/start | cmd/evener-hub/app_rpc.go:611 | ignores (`_`) | in-memory PKCE flow-map write; builds authorize URL | safe | app_auth.go:195-234; an abandoned flow entry is inert in memory |
| 27 | evener/auth/login/complete | cmd/evener-hub/app_rpc.go:614 | passes-through | IdP token exchange over network (consumes a one-time authorization code — a durable *external* effect) → `saveAuth` durable credential file write → flow delete → broadcast | safe-note | app_auth.go:235-292. No await between the exchange result and `saveAuth` (file write does not observe ctx), so no partial local state. Residual: cancel mid-exchange can leave the one-time code redeemed at the IdP with tokens lost; retry with the same redirect URL then fails permanently and the user must restart the login flow. No dedup, but recovery path exists and no state corrupts. Broadcast lands on the sendClosed-fenced seam. |
| 28 | evener/auth/logout | cmd/evener-hub/app_rpc.go:621 | binds-unused | durable credential delete (OAuth record and/or file key) | safe | `Logout(params)` never receives ctx (app_auth.go:294) — runs to completion under cancel; delete is idempotent on retry |
| 29 | evener/auth/list | cmd/evener-hub/app_rpc.go:628 | ignores (`_`) | pure read | safe | app_auth.go:336 |
| 30 | evener/auth/apiKey/set | cmd/evener-hub/app_rpc.go:631 | binds-unused | durable credential write | safe | `ApiKeySet(params)` never receives ctx (app_auth.go:360) — runs to completion; set-to-value idempotent on retry; broadcast seam-fenced |
| 31 | evener/auth/device/start | cmd/evener-hub/app_rpc.go:638 | passes-through | network device-code request → in-memory device-flow map write | safe | app_auth.go:422-452; abandoned flow expires after 15 minutes (DevicePoll's staleness check) |
| 32 | evener/auth/device/poll | cmd/evener-hub/app_rpc.go:641 | passes-through | network poll; on authorized: token exchange → `saveAuth` durable write → flow delete → broadcast | safe | app_auth.go:453-503. Poll is retried by the client by design; the flow entry survives an abandoned poll, so the retry re-polls and re-drives the exchange; exchange→saveAuth has no await between them. Convergent. |
| 33 | evener/instance/list | cmd/evener-hub/app_rpc.go:659 | ignores (`_`) | pure read (fresh providers.toml load) | safe | app_instances.go:52; providers.toml is the single source of truth, loaded fresh |
| 34 | evener/instance/create | cmd/evener-hub/app_rpc.go:662 | ignores (`_`) | atomic providers.toml write under controller mutex → broadcast → fresh List | safe | ctx-ignoring: cancellation cannot abandon it mid-write, it runs to completion and parks the worker for a local file write only (app_instances.go:36-40, WriteFile atomic per struct doc comment) |
| 35 | evener/instance/edit | cmd/evener-hub/app_rpc.go:669 | ignores (`_`) | atomic providers.toml write under controller mutex → broadcast → fresh List | safe | same shape as row 34; edit-to-value idempotent on client retry |

## Shard-level findings for the assembler

- **FLAG — thread/start (row 6):** the one handler in this shard that meets the spec's gate criterion head-on: ctx-binding serial mutation, awaits between durable side effects (detached Spawn → ReadThread → initial StartTurn), no retry dedup (not in the ValidateMutationParams 8, and the initial turn's ClientMutationID is server-generated per attempt so dedup is structurally impossible). Abandonment outcome is orphan-session + possible uncorrelated initial input, duplicated on retry. Pre-existing under PR #667 but frequency-amplified by prompt cancellation.
- **Notes (safe but named residuals):** thread/fork duplicate-child-on-retry (row 8); sandboxEscalation/resolve applied-vs-not ambiguity with user-driven recovery (row 12); thread/compact/start no-dedup idempotent-in-effect (row 18); auth/login/complete one-time-code burn on cancel mid-exchange, recovered by restarting the flow (row 27).
- **Spec assumptions verified:** ValidateMutationParams enforces clientMutationId on exactly the 8 methods the spec names (appwire/protocol.go:183-197 — turn family + thread/clear), and all 8 are registered in this shard (rows 9-11, 13-17). The spawn/resume/codex-launch paths all detach the child process from the request ctx (`exec.Command`, explicit "NOT CommandContext" comments) and already document websocket-ctx cancellation (spawn.go:507-522, codex_launch.go:444-463), matching the spec's claim that the system anticipates mid-flight cancellation. Subscription mutations in this shard go only through the seam-fenced entry points the spec enumerates.
- **No spec contradictions found in this shard.**

---

# Slice-0 handler disposition audit — Shard B (registrations 36–70 of `cmd/evener-hub/app_rpc.go`)

**origin/main SHA audited:** `173411a6add72b9d05165694bfe875871e3aed4c`
**First method in shard:** `evener/instance/remove` (registration 36, `app_rpc.go:676`; auditor A's shard ends at registration 35, `evener/instance/edit`, `app_rpc.go:669`)
**Row count:** 35 (registrations 36–70; `app_rpc.go` contains exactly 70 `appserver.HandleTyped` registrations, so this shard is the file's tail)

Column key — ctx behavior: `ignores` = handler signature discards ctx (`_` or unnamed); `passes-through (unobserved)` = ctx forwarded into the controller but no callee ever selects on it (no ctx-aware I/O anywhere downstream); `binds` = ctx reaches ctx-aware I/O (`exec.CommandContext`, `http.NewRequestWithContext`, daemon RPC) so mid-flight cancellation is actually observable.

| # | method | file:line | ctx behavior | side-effect shape | mid-flight-abandonment disposition | evidence note |
|---|--------|-----------|--------------|-------------------|------------------------------------|---------------|
| 36 | evener/instance/remove | cmd/evener-hub/app_rpc.go:676 | ignores (`_`) | durable mutation: providers.toml rewrite (atomic WriteFile) or delete, plus credential clear + OAuth state-file delete, under in-proc mutex; broadcasts evener/auth/updated on success | **safe** — never observes ctx, so prompt cancellation is a no-op: runs to completion, only the response is undeliverable; retry is idempotent (RemoveInstance of an absent name is a no-op rewrite) | `app_instances.go:197` Remove; validation-then-write under `c.mu`; broadcast is post-success and not ctx-tied |
| 37 | evener/instance/setDefault | cmd/evener-hub/app_rpc.go:683 | ignores (`_`) | durable mutation: single providers.toml atomic write under mutex; broadcast on success | **safe** — ctx-ignoring, runs to completion; retry idempotent (same default rewritten) | `app_instances.go:261` SetDefault |
| 38 | evener/launch/resolve | cmd/evener-hub/app_rpc.go:695 | passes-through (unobserved) | pure read: canonicalize dir, load launch-config layers from disk | **safe** — pure read; no callee observes ctx | `app_launch.go:79` Resolve → launchconfig.Resolve (file reads only) |
| 39 | evener/launch/schema | cmd/evener-hub/app_rpc.go:698 | passes-through (unobserved) | pure in-memory read (static schema marshal) | **safe** — pure read | `app_launch.go:41` Schema |
| 40 | evener/launch/getLayer | cmd/evener-hub/app_rpc.go:701 | passes-through (unobserved) | pure read: load one layer file | **safe** — pure read | `app_launch.go:107` GetLayer |
| 41 | evener/launch/setLayer | cmd/evener-hub/app_rpc.go:704 | passes-through (unobserved) | durable mutation: single `launchconfig.SaveLayer` file write, then re-resolve (reads); broadcasts evener/launch/updated on success | **safe** — no ctx observation anywhere, so uncancelable mid-flight; single durable write, no awaits between writes; retry is last-writer-wins idempotent | `app_launch.go:136` SetLayer; post-write resolve is read-only, so no canceled-after-durable-write false failure |
| 42 | evener/launch/trustRepo | cmd/evener-hub/app_rpc.go:711 | passes-through (unobserved) | durable mutation: single `SaveMeta` write appending the hash to the trusted set; broadcast on success | **safe** — uncancelable (no ctx observation); retry idempotent (`HashInSet` dedups the append) and the `resolved.Repo.Hash != params.Hash` precondition fences a stale retry | `app_launch.go:176` TrustRepo |
| 43 | evener/marketplace/list | cmd/evener-hub/app_rpc.go:724 | ignores (`_`) | pure read of marketplaces.json | **safe** — pure read | `app_plugins.go:202` → `marketplaces.go` loadMarketplaces |
| 44 | evener/marketplace/add | cmd/evener-hub/app_rpc.go:727 | binds (git clone via `exec.CommandContext`) | flock (30s, **not ctx-aware**) → network clone into `.staging` → rename to final → atomic saveMarketplaces; broadcast on success | **safe** — every ctx-observing await (clone) precedes any durable commit; the failure path (including ctx-canceled) removes staging; the rename + registry write that follow the last await never observe ctx, so they cannot be split; retry after reconnect converges (RemoveAll + re-clone + map overwrite) | `marketplaces.go:AddMarketplace` (staging comment: "a bad marketplace never half-registers"); `git.go:gitClone` ctx-aware |
| 45 | evener/marketplace/remove | cmd/evener-hub/app_rpc.go:734 | ignores (`_`) | flock → RemoveAll of clone dir + saveMarketplaces | **safe** — ctx-ignoring, runs to completion; retry returns ErrMarketplaceNotFound (benign) | `marketplaces.go:RemoveMarketplace` |
| 46 | evener/marketplace/refresh | cmd/evener-hub/app_rpc.go:741 | binds (git pull / clone via `exec.CommandContext`) | flock → `git pull --ff-only` on the existing clone (or first-fetch clone) → saveMarketplaces LastUpdated | **FLAGGED (moderate)** — cancel mid-`git pull` SIGKILLs git inside the *live* clone working tree; unlike every clone path there is no discard-and-retry staging: a killed pull can leave `.git/index.lock`/partial checkout, after which every subsequent refresh errors until the marketplace is removed and re-added. Registry state stays consistent (LastUpdated only written after success) and the failure is loud, not silent — but the handler does not itself tolerate mid-flight cancellation; recovery is manual. See flag F1 below | `marketplaces.go:RefreshMarketplace`; `git.go:gitPull`; `exec.CommandContext` default Cancel is Kill |
| 47 | evener/marketplace/browse | cmd/evener-hub/app_rpc.go:748 | binds (lazy clone) | read + lazy `ensureFetched` (clone + saveMarketplaces InstallLocation backfill) under flock | **safe** — cancel mid-clone returns error with `InstallLocation` still `""`; the partial clone dir is orphaned but the next fetch attempt `RemoveAll`s it first (self-healing); the backfill write happens only after a completed clone with no intervening await | `catalog.go:131` Browse; `marketplaces.go:ensureFetched` (`_ = marketplaceRemoveAll(installLoc)` before every fetch) |
| 48 | evener/plugin/list | cmd/evener-hub/app_rpc.go:751 | ignores (`_`) | pure read of plugin registry | **safe** — pure read | `app_plugins.go:280` → registry load |
| 49 | evener/plugin/preview | cmd/evener-hub/app_rpc.go:754 | binds nominally, never observed (`_ = ctx` in controller) | read-only launch resolve; for a not-yet-existing CWD creates a temp resolver dir removed by deferred cleanup | **safe** — read; the temp dir is cleaned by defer even on error | `app_plugins.go:56` Preview (comment: "only reads manifests and registry state") |
| 50 | evener/plugin/install | cmd/evener-hub/app_rpc.go:757 | binds (network git clone) | flock (30s, not ctx-aware) → lazy marketplace fetch (**durable**: clone + saveMarketplaces) → **await** (plugin source clone into `.staging`, ctx-aware) → rename to sha-keyed dir + registry write (**durable**, no ctx observation); broadcast on success | **safe, with a note** — this is exactly the spec's named "await between durable side effects" shape, but the only cancel-observable windows are inside staged fetches whose failure paths discard staging. Cancel between the two durable writes leaves marketplace-fetched/plugin-absent — a consistent intermediate state identical to `marketplace add` followed by a failed install; a reconnect retry converges idempotently (same sha-keyed cache dir, registry map overwrite), so no ClientMutationID dedup is needed. The commit sequence after the last await (rename → LoadRegistry → SaveRegistry) never observes ctx and cannot be split | `install.go:Install`, `stagePlugin` (removes staging on error), `commitStaged`, `ensureFetched` |
| 51 | evener/plugin/upgrade | cmd/evener-hub/app_rpc.go:764 | binds (network git clone) | same shape as install; on sha change commits a NEW sha dir and repoints the registry, never deleting the old dir (gc reclaims) | **safe** — same staged-fetch reasoning; the old install dir survives by design, so an abandoned upgrade leaves the previous version fully live; a validation-failed final dir is removed, and an orphaned sha dir is gc's job; retry converges | `install.go:upgradeLocked` (doc: "never deleting the old dir … so live sessions keep working") |
| 52 | evener/plugin/remove | cmd/evener-hub/app_rpc.go:771 | ignores (`_`) | flock → registry write + cache-dir RemoveAll | **safe** — ctx-ignoring, runs to completion; retry → ErrNotInstalled (benign) | `install.go` Remove/mutate path |
| 53 | evener/plugin/enable | cmd/evener-hub/app_rpc.go:778 | ignores (`_`) | flock → single registry flag write | **safe** — ctx-ignoring; retry idempotent | `install.go:mutateEntry`/`SetEnabled` |
| 54 | evener/plugin/disable | cmd/evener-hub/app_rpc.go:785 | ignores (`_`) | flock → single registry flag write | **safe** — same as enable | `install.go:SetEnabled(false)` |
| 55 | evener/plugin/setAutoUpgrade | cmd/evener-hub/app_rpc.go:792 | ignores (`_`) | flock → single registry flag write | **safe** — ctx-ignoring; retry idempotent; the auto-upgrade daemon re-reads the flag under the same lock before acting, so a flipped flag is honored regardless of request fate | `install.go:SetAutoUpgrade`; `upgradeLocked` requireAutoUpgrade re-check |
| 56 | evener/upgrade | cmd/evener-hub/app_rpc.go:820 | binds (HTTP download via `http.NewRequestWithContext`) | self-update: MkdirTemp → network download → extract → install binaries (copy + rename); temp dir removed by defer | **safe** — the only ctx-observing await is the download, which precedes every durable write; cancel mid-download leaves nothing (deferred RemoveAll); extract/install never observe ctx so the multi-binary install cannot be split by cancellation; retry is a fresh idempotent upgrade | `internal/selfupdate/update.go:81` Upgrade, `:171` download (ctx request), `:262` installExtractedBinaries (no ctx) |
| 57 | evener/search | cmd/evener-hub/app_rpc.go:821 | ignores (`_`) | pure in-memory read (roster + past index) | **safe** — pure read | `app_search.go:14` hubSearch |
| 58 | model/list | cmd/evener-hub/app_rpc.go:824 | binds (daemon RPC / codex source calls) | read; may lazily `EnsureSource`-launch a managed codex app-server (shared infrastructure, process spawned under `context.Background()`, request ctx governs only the readiness wait and the process is killed on launch failure/cancel) | **safe** — read with cancel-clean launch: cancel mid-launch kills the spawned process and errors; a successful launch is shared state a retry reuses | `app_models.go:27`; `codexlaunch/codex_launch.go:195` EnsureSource, launch wait selects `ctx.Done()` and Kills on failure |
| 59 | evener/tasks/list | cmd/evener-hub/app_rpc.go:827 | binds | read: proxy to live daemon source (may managed-launch, see #58) with persisted-file fallback for dead sessions | **safe** — pure read both paths | `app_tasks.go:30` hubTasksList |
| 60 | evener/jobs/list | cmd/evener-hub/app_rpc.go:830 | binds | read: same live-first/dead-fallback shape | **safe** — pure read | `app_jobs.go:17` hubJobsList |
| 61 | evener/jobs/output | cmd/evener-hub/app_rpc.go:833 | binds | read: same shape, tails persisted job output on fallback | **safe** — pure read | `app_jobs.go:60` hubJobsOutput |
| 62 | evener/thread/transcripts/list | cmd/evener-hub/app_rpc.go:836 | binds | read: resolves root thread via sources, enumerates subagent transcripts | **safe** — pure read | `app_transcripts.go:15` hubThreadTranscriptList |
| 63 | evener/paths/complete | cmd/evener-hub/app_rpc.go:839 | ignores (`_`) | pure filesystem read (ReadDir/Stat) | **safe** — pure read; errors coerced to empty suggestions | `internal/fspaths/app_paths.go:14` CompletePaths |
| 64 | evener/dirs/create | cmd/evener-hub/app_rpc.go:842 | ignores (`_`) | durable mutation: single `MkdirAll` | **safe** — ctx-ignoring; naturally idempotent (existing dir → `Created:false`) | `app_dirs.go:17` hubDirsCreate |
| 65 | evener/projects/recent | cmd/evener-hub/app_rpc.go:845 | ignores (`_`) | pure in-memory read (past index) | **safe** — pure read | inline in `app_rpc.go:845` |
| 66 | evener/path/validate | cmd/evener-hub/app_rpc.go:859 | ignores (`_`) | pure read (Stat/LookPath) | **safe** — pure read | `internal/fspaths/app_paths.go:150` ValidateLaunchPath |
| 67 | evener/git/head | cmd/evener-hub/app_rpc.go:862 | binds (`exec.CommandContext`) | read: forks `git rev-parse` up to twice | **safe** — read-only; cancel kills git and the handler returns empty HEAD (best-effort display metadata by design); `rev-parse` is read-only so a killed fork cannot wedge the repo | `app_git_head.go:18`; matches spec's exposure description |
| 68 | evener/harnesses/list | cmd/evener-hub/app_rpc.go:865 | ignores (unnamed param) | pure in-memory read of configured harnesses | **safe** — pure read | `app_models.go:452` launchHarnessDescriptors |
| 69 | evener/command/list | cmd/evener-hub/app_rpc.go:868 | ignores (unnamed param) | read: plugin resolution (`ResolveForLaunch`, registry + manifest reads, no lock, no writes) + fail-soft command discovery | **safe** — pure read | `app_rpc.go` hubCommandList body (below registrations) |
| 70 | evener/settings/overview | cmd/evener-hub/app_rpc.go:871 | binds | read: settings field bag; probes configured MCP servers in parallel under per-probe bounded timeouts (transient probe subprocesses/connections only) | **safe** — read; probe side effects are transient and ctx/timeout-bounded | `app_rpc_settings_overview.go:24`, `:110` settingsMCPOverview |

## Flags for the assembler (gate inputs)

**F1 — `evener/marketplace/refresh` is the shard's one gate-relevant finding.** Its `git pull --ff-only` runs ctx-aware in the *live* marketplace clone with no staging: prompt cancellation SIGKILLs git mid-pull and can leave the clone wedged (`.git/index.lock`, partial checkout), after which every refresh of that marketplace fails until it is removed and re-added (or the clone dir is manually cleaned). Registry JSON stays consistent and the failure is loud, so this is degraded-but-recoverable rather than corrupting — but per the spec's gate ("any member that does not already tolerate mid-flight cancellation … gets its fix"), it does not have the gate-release/retry shape. Cheap remediations: on pull failure, fall back to RemoveAll + re-clone (the never-fetched branch's own code path, ~5 lines in `RefreshMarketplace`); or detach the pull from connection ctx. Note the same exposure already exists today via send-path cancellation/shutdown per the spec's own "it already happens" argument — the worker only makes it common.

**F2 — spec location error for `evener/plugin/checkNow`, and a shard-coverage hazard.** The spec's exposure section lists `evener/plugin/checkNow` among the marketplace/plugin family "(`cmd/evener-hub/app_rpc.go`)". It is actually registered in `cmd/evener-hub/app_plugin_autoupgrade.go:114`. It is therefore in NONE of `app_rpc.go`'s 70 registrations — an audit sharded purely by `app_rpc.go` row ranges will silently miss it (and it is ctx-binding: it runs an auto-upgrade tick, i.e. the same clone-then-registry-write machinery as #51, though `upgradeLocked`'s under-lock flag re-check and never-delete-old-dir discipline make it the same "safe" disposition). Whoever holds the 12 non-app_rpc.go hub registrations must own its row; the spec text should be corrected.

**F3 — flock acquisition is not cancellation-aware (whole marketplace/plugin family, #44–47, 50–55, and checkNow).** `acquireLock` (`internal/plugins/locks.go`) takes no ctx: it spins flock-with-backoff for up to 30 seconds. A canceled (disconnected) request blocked there cannot observe cancellation until the lock is won or the timeout expires — so "prompt cancellation" of these handlers can be up to 30s + the flock holder's remaining runtime before the serial worker goroutine actually returns. Not a durability bug (nothing has happened yet), but it undercuts the spec's promptness story for this family and holds the per-connection worker's teardown open; worth a sentence in the spec or a ctx parameter on acquireLock.

**F4 — none of this shard's mutations carry ClientMutationID dedup, by design.** All 8 `ValidateMutationParams` methods (`appwire/protocol.go:189-196`: turn family + thread/clear) fall in auditor A's shard. Every mutation in shard B is either ctx-ignoring (uncancelable mid-flight) or convergent-on-retry as evidenced per row, so the absence of the dedup machinery is consistent with the spec's admitted-request contract ("an abandoned mutation retries as a fresh application") — no contradiction, but the assembler should record that the retry contract for rows 36–70 rests on idempotence, not dedup.

**Count verification for the assembler:** `app_rpc.go` on `173411a6a` has exactly 70 `HandleTyped` registrations; cmd/evener-hub non-test total is 82 (70 + archive 1, favorite 1, mobile 1, pin_section 4, plugin_autoupgrade 1, project_delete 1, rename 1, rpc_transcript_display 1, session_delete 1), and exactly 2 raw `Router().Handle` registrations exist (`app_navigation.go:16`, `app_rpc_transcript_display.go:17`) — matching the spec's 82-hub + 2-raw counting convention.

---

# Slice-0 handler disposition audit — shard C

Audited at: `origin/main` = `173411a6add72b9d05165694bfe875871e3aed4c`
Row count: **38** (12 hub `HandleTyped` outside app_rpc.go + 2 raw `Router().Handle` + 24 daemon `HandleTyped`)
Columns: method | file:line (registration) | ctx behavior | side-effect shape | mid-flight-abandonment disposition | evidence note.
Dispatch class: every row is **serial** except the two marked concurrent (`thread/read`, `thread/turns/list` are members of `concurrentDispatchMethod`).

## Hub `appserver.HandleTyped` (non-app_rpc.go) — 12 rows

| method | file:line | ctx behavior | side-effect shape | mid-flight-abandonment disposition | evidence note |
|---|---|---|---|---|---|
| `evener/archive/set` | cmd/evener-hub/app_archive.go:14 | binds; used only for `navigation.Refresh(ctx)` after the write | mutation: one atomic SQLite upsert (`ArchiveStore.Set`, ctx-free) + nav refresh + attention poke | **safe** — idempotent-by-value write; cancel can only skip the post-write refresh, whose build flight is service-owned ("Caller cancellation stops only that caller's wait", navigation_service.go:379) and whose invalidation still broadcasts; retry converges | store write at archive.go:88 is not cancelable; no awaits between durable writes (only one) |
| `evener/favorite/set` | cmd/evener-hub/app_favorite.go:13 | binds; only for post-write `navigation.Refresh(ctx)` | mutation: one atomic SQLite upsert (`FavoriteStore.Set`, ctx-free) + nav refresh | **safe** — same shape as archive/set; idempotent-by-value, retry converges | favorite.go:65 |
| `evener/mobile/pairing` | cmd/evener-hub/app_mobile.go:21 | ignores (`_`) | pure computation over cfg (origin validation, URL build); zero side effects | **safe** — pure read, cannot observe cancel | mobilePairing at app_mobile.go:136 does no I/O |
| `evener/pin-section/rename` | cmd/evener-hub/app_pin_section.go:18 | binds; only for post-write `commitPinNavigation(ctx)` | mutation: one SQLite tx (`PinSectionStore.Rename`, ctx-free) + nav refresh | **safe** — single atomic tx; rename retry with same name is idempotent (`changed=false`); nav propagation survives caller cancel | pin_section.go:345 |
| `evener/pin-section/delete` | cmd/evener-hub/app_pin_section.go:33 | binds; only for post-write nav refresh | mutation: one SQLite tx (`DeleteSection`) + nav refresh | **safe** — atomic; applied-then-abandoned retry gets `ResourceNotFound` (distinguishable, never double-applies) | pin_section.go:450; pinSectionAppWireError maps ErrPinSectionNotFound |
| `evener/session-pin/assign` | cmd/evener-hub/app_pin_section.go:48 | binds; resolver read (`resolvePinSession(ctx)`) before write, nav refresh after | mutation: one SQLite tx (`Assign` / `CreateOrReuseAndAssign`) + nav refresh | **safe** — ctx touches only reads on either side of one atomic tx; assign retry idempotent, CreateOrReuse reuses by name key | pin_section.go:200,271 |
| `evener/session-pin/unpin` | cmd/evener-hub/app_pin_section.go:80 | binds; resolver read before, nav refresh after | mutation: one SQLite tx (`Unpin`) + nav refresh | **safe** — idempotent (`changed=false` on repeat) | pin_section.go:340 |
| `evener/plugin/checkNow` | cmd/evener-hub/app_plugin_autoupgrade.go:114 | **binds; ctx flows into git subprocesses** (`exec.CommandContext` in internal/plugins/git.go:27) and marketplace/plugin loops | mutation with **awaits between durable side effects**: per-marketplace `git pull`/clone + marketplaces.json write, then per-plugin clone→stage→validate→rename→registry write, each under a 30s file lock; broadcast on success | **FLAG — conditionally safe; remediation candidate** (see finding F1 below). Registry side is cancel-safe: stage-and-commit with cleanup on error (install.go:56–74) and registry write last (install.go:284); retry (next tick or repeat call) converges; per-plugin failure isolation (autoupgrade.go:73–85). Residual: ctx cancel SIGKILLs a mid-`git pull` child, which can strand `.git` lock files in a marketplace clone with **no self-heal path** (RefreshMarketplace only remove-and-reclones when `InstallLocation==""`, marketplaces.go:240–248); also `installAcquireLock`/`marketplaceAcquireLock` (30s) do not select on ctx, so a canceled handler still parks the worker up to 30s per acquisition (promptness, not safety) | no ClientMutationID (not in the 8 `ValidateMutationParams` methods); spec explicitly names this family as slice-0 scope |
| `evener/project/delete` | cmd/evener-hub/app_project_delete.go:9 → project_delete.go:60 | binds; used for `memoTree(ctx)` read (pre-write) and `projectDeleteResult→navigation.Refresh(ctx)` (post-write) | destructive mutation: durable deletion fence (`DeletionStore.BeginProject`), file/dir removal, past-index rebuild — **all ctx-free** | **safe** — the destructive core takes no ctx, so cancel cannot interrupt between fence and cleanup; the persisted deletion record makes a crashed/abandoned delete resumable on retry (the `DeletingProject` branch, project_delete.go:88–107); post-write nav refresh loss converges via invalidation | this is the gate-fence/resume shape the spec asks for, already built |
| `evener/thread/name/set` (hub) | cmd/evener-hub/app_rename.go:35 | binds; live path passes ctx to source resolution (possible managed-daemon `EnsureSource(ctx)` launch) and the proxied daemon RPC `source.SetThreadName(ctx)`; past path is ctx-free (meta load/save + `Past.UpdateMeta`) | mutation: either one atomic session-meta file save, or one forwarded daemon RPC; then ctx-free `navigation.Invalidate` (async, comment at app_rename.go:132 states the retry contract) | **safe** — past path: single ctx-free durable write. Live path: cancel mid-RPC leaves apply-or-not on the daemon, but rename to the same name is idempotent and the daemon's own rename event converges hub state via the relay; a canceled `EnsureSource` launch is owned by the launcher/roster, not this request | app_rename.go:61–121 |
| `evener/settings/transcriptDisplay/get` | cmd/evener-hub/app_rpc_transcript_display.go:13 | ignores (unnamed param) | pure read: in-memory `store.Snapshot()` | **safe** — pure read | transcript_display_store.go:72 |
| `evener/session/delete` | cmd/evener-hub/app_session_delete.go:18 → sessionDelete at app_session_delete.go:206 | binds; only for post-write `sessionDeleteResponse→navigation.Refresh(ctx)` | destructive mutation: ownership acquire, artifact removal, decision scrub, past rebuild — all ctx-free (reuses project-deletion machinery) | **safe** — same shape as project/delete: ctx-free destructive core, resumable via the shared deletion-record contract; retry after applied finds no entry → scrub-and-empty-response (converges) | app_session_delete.go:221–274 |

## Raw `Router().Handle` — 2 rows (bypass HandleTyped's envelope)

| method | file:line | ctx behavior | side-effect shape | mid-flight-abandonment disposition | evidence note |
|---|---|---|---|---|---|
| `evener/navigation/read` | cmd/evener-hub/app_navigation.go:16 | binds; explicit `ctx.Err()` pre-check, then `navigation.Representation(ctx)` | pure read (cached, versioned projection; gzip encode) | **safe** — read-only; ctx cancel maps to `Unavailable` (navigationReadError); the cache's build flight is service-owned so an abandoned caller wastes nothing | raw registration decodes params itself (decodeNavigationReadParams) — no HandleTyped envelope, but also no `ValidateMutationParams` exposure since it mutates nothing |
| `evener/settings/transcriptDisplay/patch` | cmd/evener-hub/app_rpc_transcript_display.go:17 | **ignores** (`_ context.Context`) — zero cancellation points | mutation: one durable atomic-rename file write guarded by an `ExpectedRevision` optimistic fence (transcript_display_store.go:84–124), then `server.BroadcastAll` | **safe** — cannot be canceled mid-flight at all (never observes ctx); a request abandoned pre-execution left nothing committed, so its retry simply succeeds; a patch that committed before its response was delivered answers the retry with a revision-conflict error carrying current state — a defined retry contract either way | raw registration exists to hand-decode the patch params; the fence is one of the spec's named queue-mutation-fence analogues. Note: bypassing HandleTyped means no typed-envelope validation, but decode errors are answered `InvalidParams` explicitly |

## Daemon `appserver.HandleTyped` — server/appwire_runtime.go, 24 rows

Registrations at appwire_runtime.go:934–957. Ctx split measured fresh: **6 bind (named), 18 ignore (16 `_` + 2 unnamed)** — matches the spec's claim exactly, including the identity of all six binders.

| method | file:line | ctx behavior | side-effect shape | mid-flight-abandonment disposition | evidence note |
|---|---|---|---|---|---|
| `thread/list` | appwire_runtime.go:934 (body :960) | ignores (unnamed) | pure read: in-memory snapshot under RLock | **safe** — pure read, uncancelable | |
| `thread/read` | :935 (body :978) | **binds**; passes ctx to `appserver.CaptureSubscription` | read + connection-owned subscription registration; snapshot answered entirely from memory | **safe** — concurrent slow read; already experiences prompt disconnect cancel today; subscription capture is seam-fenced (registration identity check under projection gate) | spec's named slow read; **concurrent** dispatch class |
| `thread/unsubscribe` | :936 (body :1047) | **binds**; ctx passed to `appserver.Unsubscribe` | connection-owned subscription-state mutation (not durable state) | **safe** — `Unsubscribe` is idempotent and identity-checked at the seam; post-unregistration call is a no-op; abandonment leaves at worst a subscription that connection close purges anyway | spec's characterization confirmed verbatim |
| `turn/start` | :937 (body :1211) | ignores (`_`) | durable mutation intent accept + receipt under `lockRetrySafeMutation`; turn executes under session-lifetime ctx in the serve loop; `refreshFacets` (in-memory) | **safe** — cannot observe cancel; ClientMutationID durable dedup answers retry with original outcome | archetype per spec; `ValidateMutationParams` enforces clientMutationId+expectedInstanceId |
| `turn/steer` | :938 (body :1249) | ignores (`_`) | same accept-intent shape | **safe** — same as turn/start | |
| `evener/sandbox/escalation/resolve` | :939 (body :1276) | ignores (`_`) | in-memory escalation resolution via callback; unknown/already-resolved → `Conflict` | **safe** — uncancelable; double-delivery answered by the Conflict path (client drops the card) | no ClientMutationID, but the Conflict-on-replay contract covers abandonment |
| `turn/interrupt` | :940 (body :1299) | **binds**; ctx forwarded to `retrySafeTurns.Interrupt` → `Session.InterruptClientMutation` | durable interrupt fence persisted first (`reservePrepared`), queue/steering parked at accept, then cancel-and-wait | **safe** — ctx is consulted only in the joined-waiter select (session_client_mutation.go:576–583); the fence and receipt are durable before any await, so an abandoned interrupt replays/joins on retry with the same ClientMutationID. Promptness note: `cancelAndWait()` itself does not observe ctx — a canceled connection's worker still waits for the runner to unwind (parking, not corruption) | spec's characterization confirmed |
| `turn/queue` | :941 (body :1318) | ignores (`_`) | accept-intent + durable dedup | **safe** | |
| `turn/drainAsSteer` | :942 (body :1345) | ignores (`_`) | accept-intent + durable dedup + `expectedQueueRevision` fence | **safe** | |
| `turn/promoteQueuedAsSteer` | :943 (body :1371) | ignores (`_`) | accept-intent + durable dedup + `expectedEntryId` fence | **safe** | |
| `turn/cancelQueued` | :944 (body :1398) | ignores (`_`) | accept-intent + durable dedup + `expectedEntryId` fence | **safe** | |
| `goal/set` | :945 (body :1428) | ignores (`_`) | goal store mutation via callback; event emitted by callback after commit | **safe** — uncancelable; set/clear idempotent by value | |
| `thread/compact/start` | :946 (body :1448) | **binds**; ctx forwarded into single callback `compactFunc(ctx)` → `Session.Compact(ctx)` | compaction: deterministic checkpoint layer, then ctx-bound LLM summarization, then history swap + `maybeAutoSave` | **safe** — cancel mid-summarize degrades, never corrupts: `ForceCompact` keeps the checkpoint-compacted history and only emits a warning when the LLM call errors (context_manager.go:474–479); the saved history is coherent at every stage; operation is re-invokable | spec's "forwards ctx into a single callback" confirmed; no ClientMutationID needed — best-effort, convergent |
| `thread/shutdown` | :947 (body :1461) | ignores (`_`) | fires `go fn()` — shutdown detached to its own goroutine | **safe** — the effect is explicitly detached from the request before the handler returns | |
| `thread/clear` | :948 (body :1479) | **binds**; ctx forwarded to `clearFunc(ctx, params)` (session replacement) | journaled identity transition: durable reservation → callback → durable applied receipt; rollback restores pre-reservation journal on failure; crash-recovery branch replays a reserved-but-replaced identity | **safe** — this is the spec's named gate-release/retry shape, verified line-by-line: cancel before install → rollback + `MutationNotAccepted`, retry re-runs; cancel after install → reserved record + recovery branch reconstructs and replays the receipt (appwire_runtime.go:1531–1560) | the most cancellation-hardened handler in the shard |
| `thread/model/set` | :949 (body :1672) | ignores (`_`) | in-memory/session profile mutation via callback + synchronous session-info refresh | **safe** — uncancelable; idempotent by value; guarded by processing/reserved-turn Conflict | |
| `thread/vision-model/set` | :950 (body :1709) | ignores (`_`) | same shape as model/set | **safe** | |
| `evener/thread/name/set` (daemon) | :951 (body :1737) | ignores (`_`) | `nameFunc(name)` → `Session.Rename` (meta write via session) | **safe** — uncancelable; idempotent by value | |
| `thread/reasoning-effort/set` | :952 (body :1755) | ignores (`_`) | session setting mutation via callback after vocabulary validation | **safe** — uncancelable; idempotent by value | |
| `evener/tasks/list` | :953 (body :1845) | ignores (unnamed) | pure read via callback | **safe** | |
| `evener/jobs/list` | :954 (body :1855) | ignores (`_`) | pure read (job activity tree) | **safe** | |
| `evener/jobs/output` | :955 (body :1869) | ignores (`_`) | pure read (output tail) | **safe** | |
| `model/list` | :956 (body :1886) | **binds**; ctx into `listModelsFunc(ctx)` → `llm.Client.ListModels` | pure read — but **outbound HTTP to the LLM provider** (cmdutil.go:303–315) | **safe** for the cancellation gate (read-only; cancel just fails the call). Latency note for the assembler: this is a serial-class handler doing a network round trip — same structural shape as the spec's `DevicePoll` exposure; under the worker that is exactly the latency the design absorbs, so no remediation needed | spec calls it "one benign read" — true for abandonment, but "network read" is the honest label |
| `thread/turns/list` | :957 (body :1203) | ignores (`_`) | pure read: in-memory turn paging | **safe** — spec's "ignores its context outright" confirmed | **concurrent** dispatch class |

## Findings and gate flags

**F1 (the shard's one gate-relevant finding) — `evener/plugin/checkNow` (app_plugin_autoupgrade.go:114): conditionally safe; named remediation candidate.**
It is the only handler in this shard that is a ctx-binding mutation with awaits between durable side effects (per-marketplace: git network op → marketplaces.json write; per-plugin: clone → registry write). The durable-state discipline is genuinely cancel-tolerant — staging cleanup on error, registry write last, old install dirs never deleted, per-plugin failure isolation, convergent retry on the next tick. The intolerance is one level down: ctx cancellation SIGKILLs the git child (`exec.CommandContext` with no `Cancel`/`WaitDelay` override, internal/plugins/git.go:27), and a killed `git pull` inside an already-fetched marketplace clone can strand `.git` lock files / partial merge state that **no code path repairs** — `RefreshMarketplace` only remove-and-reclones when the marketplace was never fetched. Under the worker, an ordinary tab close during checkNow becomes a routine trigger for this, where today it needs shutdown or a send-path failure. Cheapest fixes, either satisfying the spec's gate: (a) detach the tick from connection cancel — run it under the hub-lifetime ctx exactly as `startPluginAutoUpgradeDaemon` already does, with the RPC just awaiting it; or (b) make `RefreshMarketplace` treat a pull failure as remove-and-reclone. Secondary: the 30s `installAcquireLock`/`marketplaceAcquireLock` acquisitions ignore ctx (promptness-only; worker parks up to 30s per lock after cancel).

**Spec verification results:**
- **Daemon 18/6 ctx split: HOLDS exactly.** 16 handlers bind ctx as `_`, 2 leave it unnamed (`thread/list`, `evener/tasks/list`), 6 bind named ctx — and the six are precisely the spec's list: `thread/read`, `model/list`, `thread/unsubscribe`, `turn/interrupt`, `thread/clear`, `thread/compact/start`. Daemon `HandleTyped` count is exactly 24 (appwire_runtime.go:934–957).
- **The 8 `ValidateMutationParams` methods confirmed** (appwire/protocol.go:189–196): turn/start, turn/steer, turn/interrupt, turn/queue, turn/drainAsSteer (+expectedQueueRevision), turn/promoteQueuedAsSteer (+expectedEntryId), turn/cancelQueued (+expectedEntryId), thread/clear.
- **Raw registrations confirmed** at app_navigation.go:16 and app_rpc_transcript_display.go:17; both are benign under the gate (pure read; ctx-ignoring fenced write).
- **One minor spec inaccuracy:** the spec's "Current state" section attributes `evener/plugin/checkNow` to `cmd/evener-hub/app_rpc.go`; its registration actually lives in `cmd/evener-hub/app_plugin_autoupgrade.go:114`. Family analysis (shared `internal/plugins.Manager`, git clones, 30s lock) is accurate; only the file attribution is off. Doesn't change any conclusion, but the disposition table's file:line column is the checksum, so recording it.

---

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
