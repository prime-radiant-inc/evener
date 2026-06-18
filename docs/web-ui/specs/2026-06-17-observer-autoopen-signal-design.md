# Observer-Subagent Auto-Open — Signal Surfacing Feasibility + Design Spec

Date: 2026-06-17
Status: Draft for Jesse's review. **No implementation** — this is a design/feasibility doc.
Scope: the agent runtime (`agent/`), the appwire wire types (`appwire/`), the Serf web hub
(`cmd/serf-hub/`), and the multi-pane workspace (`assets/panes.js`, `assets/renderer.js`).
Companion to `docs/web-ui/specs/2026-06-17-multi-pane-workspace-design.md` (the multi-pane MVP this
extends).

## What we're trying to do

The multi-pane MVP just shipped: a user can manually "open beside" a subagent's session as an
`<iframe>` pane (`renderer.js` ⇲ button → `SerfPanes.open("/s/<id>", label)`). Jesse's ask:

> When the session you're viewing has an **observer subagent** running, that observer's pane should
> **auto-open** beside it.

The blocker found during planning: **the web has no way to identify which subagent is "the
observer."** "Observer" is a runtime `job_watch` concept (`agent/job_watch.go`), not a flag that
reaches the hub. Subagents carry only `IsSubagent`/`ParentSessionID`
(`agent/schema/snapshot.go:52-54`); `AgentRole` (`appwire/types.go:152`) is a codex-only field
(populated solely in `cmd/serf-hub/internal/appsource/codex_source.go:638`, never for local
sessions). This doc defines what it takes to surface the observer signal, grounds every claim in
code (file:line), and recommends an approach.

---

## TL;DR (executive summary)

- **The watched↔observer relationship IS knowable and durably persisted server-side today** — but
  not as a flag, and not anywhere the hub currently reads. It lives in the **watching session's
  jobstore** as `watch_read_grant` events (`agent/internal/jobstore/event.go:19,71`), folded by
  `FoldGrants` into `map[observerSessionID]map[watchedJobID]bool`
  (`agent/internal/jobstore/fold.go:104-126`). The hub reads **only** session `.meta.json` files
  (`agent/schema/snapshot.go:139-150`, via `hubcore.PastIndex`), **never** the jobstore — a grep of
  `cmd/serf-hub` for `jobstore`/`jobs.jsonl`/`LoadGrants` returns zero hits. So the data exists; the
  plumbing to surface it does not.
- **Recommended approach: persist the observer link on the *watched worker's* `SessionMeta` at
  watch-install time, then carry it through the existing lineage seam to a data-attribute the
  auto-open JS reads.** Concretely: stamp `ObservedBy []string` (observer session ids) onto the
  watched subagent's meta when `job_watch` mints the read grant
  (`agent/job_watch.go:2016-2041`); the hub already reads that meta
  (`web_workspace.go:243,321` → `fillSubagentLineage` `web_workspace.go:381-396`), so add
  `ObserverRouteIDs` to `WorkspaceData` (`cmd/serf-hub/web_types.go:161-199`) and render it as a
  `data-observers` attribute on `#conversation` (`templates/partials/workspace.html:37-43`). The JS
  reads it in `SerfRenderer.init()` where `this.sessionId` is already set (`renderer.js:97`) and
  calls `SerfPanes.open` for each live observer.
- **Rough effort: ~150–280 LOC**, mostly small additive changes across: meta field (~5), watch-time
  stamp (~30–60), meta→WorkspaceData→template wiring (~30–50), the JS auto-open rule (~50–90),
  tests (moderate). No CSP, transport, or renderer-singleton work — the multi-pane MVP already paid
  those costs.
- **Local-only for v1.** The signal can be computed for **local** sessions (the hub has filesystem
  access to their state dir — `PastEntry.StateDir`, `cmd/serf-hub/internal/hubcore/past.go:17-20`).
  For **remote** sources (codex), the hub has only the appwire `Thread` snapshot
  (`appwire/types.go:136-157`) and no jobstore access; remote observer auto-open needs the remote
  daemon to emit the field over appwire — deferred.

**Top 2 open questions for Jesse** (full list at the end):
1. **Auto-open on every load, or once?** `SerfPanes` persists *open* panes but does **not** track a
   *user-closed* pane (`panes.js` `persist()` writes only the open set), so a naive auto-open would
   re-open a pane the user just dismissed on the next navigation/reload. Do we add a
   "suppressed-observers" memory, or accept re-open?
2. **What counts as "the session you're viewing"?** An observer watches a **worker** job, and both
   the worker and the observer are delegate children of a **parent** that ran `job_watch`. Should
   auto-open fire when viewing the **worker** (pair them side-by-side), the **parent**, or any of
   the three? (Recommendation: the **worker** — that's the "watch this run live" intent.)

---

## Key finding: is the watched↔observer relationship knowable / persisted today?

**Yes — it is durably persisted in the watching session's jobstore, and it is reconstructable
server-side. It is simply not a flag and not read by the hub.**

### What an observer IS, precisely (trace through `agent/job_watch.go`)

An "observer" is conferred at **`job_watch` install time**, not at spawn. The shape:

- A **parent** session runs `job_watch` with a concrete job `target` (the **watched worker** — a
  running delegate job) and `send.to = <observer-job>` (a second delegate job). This is the
  "sidecar watch" (`configureWatch`, `agent/job_watch.go:373-559`; the sidecar grant path at
  `:444-454`).
- Installing that watch **mints a durable read grant** so the observer's child session may
  `job_read_output` the watched job: `mintWatchCreateReadGrant`
  (`agent/job_watch.go:2016-2041`) resolves `send.to` → the observer's child **session** id via
  `watchReadGrantObserver` (`:1939-1958`, job record → `transcript_ref` → session id) and appends
  `appendWatchReadGrant(observerSessionID, cfg.target)` (`:1922-1929`). Wildcard/`watched`-target
  watches mint the same grant per-fire (`mintWatchSendReadGrant`, `:2054-2084`).
- The grant is an append-only `EventWatchReadGrant` event with `ObserverSessionID` +
  `JobID`=watched job (`agent/internal/jobstore/event.go:19,71`). `FoldGrants`
  (`agent/internal/jobstore/fold.go:104-126`) and `Store.LoadGrants`
  (`agent/internal/jobstore/store.go:123-136`) reconstruct
  `map[observerSessionID]map[watchedJobID]bool` from the durable log; the fold is order-insensitive
  and dedups.
- The grant keys on the observer's **session** id (not its job id) deliberately, because a fired
  frame resumes an idle observer under a NEW job id — the session id is the stable handle
  (`agent/job_watch.go:2008-2011`). Per spec §5.1, grants are **never revoked** (`:1917-1921`); they
  outlive watch clear/expiry.

### Why this is per-session and reachable on disk for local sessions

The jobstore is **per-session**: `<stateDir>/sessions/<sessionID>/jobs.jsonl`
(`agent/jobs.go:233` builds `sessions/<id>`, `:244` opens `jobs.jsonl`). The grant is appended to
the **watching session's** store (`jm.store` belongs to the session that ran `job_watch`). The
**watched job** (`cfg.target`) is a delegate job in that same watching session; its job record's
`TranscriptRef` decodes to the **worker subagent's session id** (`decodeRef`, used by
`watchReadGrantObserver` at `:1951`).

So the full server-side chain is:

```
watching session's jobs.jsonl
  └─ EventWatchReadGrant{ ObserverSessionID, JobID = watchedJobID }
        ├─ ObserverSessionID  → the observer subagent's session id   (stable)
        └─ watchedJobID       → watched delegate job
                                  └─ JobRecord.TranscriptRef → worker subagent's session id
```

**The relationship (worker session ↔ observer session) is fully reconstructable from durable
on-disk state for local sessions.** The hub's `PastEntry` already carries the project `StateDir`
(parent of `sessions/`, `cmd/serf-hub/internal/hubcore/past.go:17-20`), so the hub *could* open the
watching session's `jobs.jsonl` and `LoadGrants()` — it just doesn't today.

### The one thing that does NOT exist today

There is **no spawn-time observer signal.** At delegate spawn, an observer is an ordinary delegate —
`spawnConfig` (`agent/session_config.go:190-249`) carries `parentSessionID`/`parentJobID` but
nothing role-shaped, and `IsSubagent` is the only subagent flag on `SessionMeta`
(`agent/schema/snapshot.go:52-54`). The observer identity only comes into being when the parent
installs the watch. **Any "stamp at spawn" option is therefore not viable** — there is nothing to
stamp until `job_watch` runs.

---

## How subagents reach the web today (ground truth)

### Sidebar tree (disk-meta path, local + remote)
- `/api/tree` → `handleAPITree` (`cmd/serf-hub/web_api_tree.go:32`) → `navigationTreeInputs`
  (`:107-127`) → `hubcore.BuildTree` (`internal/hubcore/tree.go:261`). Local metas come from
  `s.cfg.Past.AllMetas()` (`web_api_tree.go:114`, disk `.meta.json`); **remote** threads come from
  `source.ListThreads(...)` and are projected into synthetic `SessionMeta`s
  (`appThreadTreeEntries`, `:162-199`) — a remote thread has only what appwire returns, no disk
  meta.
- Subagent nesting: `BuildTree` nests `Kind=="subagent"` under `ParentSessionID`
  (`tree.go:329-331,401-416`); `nodeKind` returns `"subagent"` from `IsSubagent`
  (`tree.go:248-256`). `TreeNode` (`tree.go:115-127`) has no observer field.

### `SessionDetail` (per-session detail DTO)
- `hubapi.SessionDetail` (`hubapi/types.go:64-93`) carries `IsSubagent` (`:85`), `ParentSessionID`
  (fork lineage), `DivergenceTurn`, `ForkLabel` — **no observer field.**
- `apiSessionDetail` (`cmd/serf-hub/web_api_tree.go:450-518`) blends two sources: local past meta
  (`pe.Meta`, `detail.IsSubagent = pe.Meta.IsSubagent` at `:514`) and, for live/remote, the appwire
  thread snapshot (`hubDetailFromAppThread`, `:274`; live overwrite at `:492`).

### Workspace data (the seam the auto-open should ride)
- The workspace page is rendered from `WorkspaceData` (`cmd/serf-hub/web_types.go:161-199`).
  `workspaceData(id)` (`web_workspace.go:243`) reads `pe.Meta` from the past index and calls
  `fillSubagentLineage` (`:293,321` → `:381-396`), which sets `ParentRouteID`/`ParentTitle` from
  the **local meta** — exactly the precedent an observer field follows.
- Those fields render as data on `#conversation` / `.workspace-header`
  (`templates/partials/workspace.html:2,37-43`); the parent breadcrumb banner uses them
  (`workspace.html:6-14`).
- **`#conversation`'s data-attributes are the JS↔server contract.** `SerfRenderer.init` reads
  `conversationEl.dataset.sessionId` (`renderer.js:97`) and siblings; this is where an observer
  attribute is read.

### appwire thread shape (for the remote story)
- `appwire.Thread` (`appwire/types.go:136-157`) + `SerfThread` (`:170-193`) carry `Kind`
  (`"subagent"`), `ParentRef`, and the codex-only `AgentRole` (`:152`). No observer/watch field.
  This is the struct a remote daemon would have to extend to make remote auto-open work.

**Where an `observerOf`/`isObserver`-style field naturally lives:** on the **watched worker's**
`SessionMeta` (the durable, disk-read source the hub already consumes), threaded through
`WorkspaceData` → `#conversation` data-attribute → the auto-open JS. Putting it on the worker (not
the observer) makes "the session I'm viewing has an observer" a single local field read.

---

## The multi-pane hook (where auto-open attaches)

From the panes layer (verified against `assets/panes.js` and `assets/renderer.js`):

- `window.SerfPanes` API (`panes.js:131`): `open(href, title)`, `close(href)`, `openHrefs()`,
  `restore()`, `setSidePanesWidth(px)`, `MAX_SIDE_PANES`.
- `SerfPanes.open(href, title)` (`panes.js:34-65`): creates an `<iframe class="pane-frame"
  src=href>`. **Dedups by exact href** — a second `open()` with the same href **focuses the existing
  pane and returns it**, never a duplicate (`panes.js:38-39`). Returns `null` if href is falsy, no
  sidebar region, or the cap is hit.
- **Pane cap:** `MAX_SIDE_PANES = 3` (`panes.js:5`), enforced in `open` (`panes.js:40`).
- **Persistence:** open panes are saved to `localStorage["serf-hub.panes"]` as `[{href,title}]`
  (`panes.js` `persist()`, ~`:110-119`); `restore()` re-creates them on load (~`:121-129`, wired in
  `onLoad`). **Closed panes are not tracked** — `close()` removes the pane and re-persists the
  remaining open set (`panes.js:67-72`), so there is no record that the user dismissed a specific
  href. (This is the crux of Open Question 1.)
- **Manual open-beside today:** the ⇲ button on a subagent row in the transcript, added in
  `applyJobRefTarget` (`renderer.js:2374-2402`), only when `window.SerfPanes` exists (so panes don't
  nest inside a pane iframe). It calls
  `window.SerfPanes.open("/s/" + encodeURIComponent(ref), label)` (`renderer.js:2394`) where `ref`
  is the subagent's session id from `row.dataset.transcriptRef`.
- **Session-load entry point:** `SerfRenderer.init(conversationEl)` (`renderer.js:65`), wired to
  `DOMContentLoaded` + `htmx:afterSwap` via `autoInit` (`renderer.js:3620-3638`). It sets
  `this.sessionId = conversationEl.dataset.sessionId` (`renderer.js:97`). **This is the single place
  that knows "the session being viewed is X,"** and the natural home for the auto-open rule.
- **Pane iframe URL:** `/s/<id>` → `handleSession` (`web_workspace.go:23`) serves the full app shell
  parameterized to `/_partials/s/<id>/workspace` (`web_workspace.go:44`). There is **no** embed/
  minimal-shell param today (the pane shows full chrome); the MVP accepted that, and we inherit it.

---

## The signal: what to surface, where it's computed, how it flows

**Surface:** on the **watched worker** session, the set of session ids of its **live observer
subagents** — call it `ObservedBy []string`.

**Where computed (the only layer that knows the pairs):** the agent's `job_watch` layer, which mints
the `(observer, watched)` grant (`agent/job_watch.go:2016-2041`, `:2054-2084`). That code already
resolves the observer's child session id and the watched job; it is the one place with both halves
in hand.

**How it flows out:**
1. Agent persists the link onto the **watched worker's** `SessionMeta.ObservedBy`
   (`agent/schema/snapshot.go`, new field) at grant-mint time.
2. Hub reads it for **local** sessions via the path it already uses
   (`PastIndex` → `pe.Meta` → `fillSubagentLineage`-style fill into `WorkspaceData`,
   `web_workspace.go:381-396` / `web_types.go:161-199`).
3. Template renders it as `data-observers="<id>,<id>"` on `#conversation`
   (`templates/partials/workspace.html:37-43`).
4. `SerfRenderer.init` reads `conversationEl.dataset.observers`, filters to **live** observers,
   and calls `SerfPanes.open("/s/<observerId>", label)` for each.

**Remote (codex/remote daemon):** the hub has no jobstore access for remote sources, only the
appwire `Thread` snapshot. To make remote auto-open work, the remote daemon would have to compute
`ObservedBy` and emit it on `appwire.Thread`/`SerfThread` (`appwire/types.go:136-193`), and the hub
would map it in `hubDetailFromAppThread` (`web_api_tree.go:274`) / the remote tree projection
(`appThreadTreeEntries`, `web_api_tree.go:162-199`). **Out of scope for v1** — recommend local-only
first.

---

## Options + recommendation

### Option A — persist `ObservedBy` on the watched worker's meta at watch-install time  ⭐ recommended

Stamp the worker's `SessionMeta.ObservedBy` when `job_watch` mints the read grant. The hub reads the
worker's meta on the path it already uses; the worker page learns its observers locally with one
field.

**Pros**
- Reuses the **exact** existing local-meta → `WorkspaceData` → `#conversation` data-attribute seam
  (`fillSubagentLineage` precedent, `web_workspace.go:381-396`). No new hub disk-read of the
  jobstore, no new RPC.
- The signal is durable (survives daemon restart / serf resume, like `Goal`/`PinnedNote` on
  `SessionMeta`, `agent/schema/snapshot.go:55-60`).
- Smallest blast radius on the hub: additive field + one fill + one template attribute.

**Cons / risks**
- Requires a **write to the watched worker's meta from the watching (parent) session's** runtime.
  The watching session knows the worker's session id (decoded from the watched job's
  `TranscriptRef`), but writing another session's meta is a cross-session write the agent does not
  do today for this purpose — needs care (the worker may be live in another goroutine/process;
  `SaveSessionMeta` is atomic-rename, `agent/schema/snapshot.go:114`, but a concurrent worker meta
  write could race). **Mitigation:** write the link on the **observer's own meta** instead (an
  `ObservesSession`/`ObservesJob` field the observer's runtime owns) and have the hub invert it — but
  inversion across all metas is an O(N) scan in the hub. Net: stamping the worker is simplest to
  consume; stamping the observer is simplest to write. **This is the main design fork inside Option
  A** (see Open Question 3).
- Liveness must be resolved at render: a stamped observer may have ended. The hub already has the
  live set (`Roster`/`liveMap`, `tree.go:288-292`); filter `ObservedBy` to live before auto-opening,
  or let the JS skip dead panes.

**Effort:** ~150–280 LOC (meta field ~5; watch-time stamp ~30–60; meta→WorkspaceData→template
~30–50; JS auto-open ~50–90; tests moderate).

### Option B — compute the relationship live in the hub from the jobstore grant table

Teach the hub to open the watching session's `jobs.jsonl` and `LoadGrants()`
(`agent/internal/jobstore/store.go:123`), invert to worker→observers, and surface it.

**Pros**
- **No agent change** — uses the durable grant table as-is; nothing new to persist.
- Always current (reflects every grant, including per-fire wildcard grants `:2054-2084`).

**Cons / risks**
- The hub would import/consume `agent/internal/jobstore` (an internal package) and read a file it
  has never touched (`cmd/serf-hub` has zero jobstore references today). New coupling between the
  hub and an agent-internal format.
- **Inversion is non-trivial:** grants are keyed `observerSession → watchedJob`; mapping a *worker
  session* to its observers requires resolving each watched job's `TranscriptRef` → worker session
  id, across the relevant session's `jobs.jsonl`. "Which `jobs.jsonl` holds the grant?" is the
  **watching parent's** store, not the worker's — so given a worker session, the hub must find its
  parent, open the parent's store, fold grants, and resolve job→session for each. More moving parts
  than a single meta field.
- Local-only by construction (no jobstore for remote), same as Option A.
- Per-request disk read + fold on every session load (cacheable, but new I/O on a hot path).

**Effort:** ~200–350 LOC (hub jobstore reader + inversion + caching + tests), plus the same JS
auto-open (~50–90). Higher than A and adds hub↔agent-internal coupling.

### Option C — new appwire query (`thread/observers` or a field on the thread read)

Expose observers over appwire so **both** local and remote sources answer uniformly. The daemon
(which owns the jobstore) computes `ObservedBy` and returns it on `thread/read` or a dedicated
query; the hub consumes it the same way for local and remote.

**Pros**
- **The only option that can ever cover remote sources** — the remote daemon does the compute.
- Clean layering: the hub stays a consumer; the jobstore stays inside the agent/daemon.

**Cons / risks**
- Largest surface: wire-type addition (`appwire/types.go`), daemon-side computation + handler, hub
  mapping (`hubDetailFromAppThread` `:274`, tree projection `:162-199`), and codex parity
  (or explicit "codex unsupported"). Touches the most components.
- Overkill for the stated v1 ask (auto-open for the local session you're driving).

**Effort:** ~350–550 LOC across appwire + daemon + hub + tests. Right long-term shape if remote
auto-open becomes a requirement; too much for the MVP.

### Recommendation

Ship **Option A (persist on meta), local-only, capped at the existing 3-pane limit**, with the
**stamp-the-observer's-own-meta + hub-side inversion** variant **if** the cross-session worker-meta
write proves racy in practice; otherwise stamp the worker directly. Treat **Option C** as the
documented path for remote auto-open later. Rationale: A rides seams the multi-pane MVP already
built and the lineage code already established, persists durably like `Goal`/`PinnedNote`, and adds
no hub↔agent-internal coupling. B's only real win (no agent change) is outweighed by inverting an
observer-keyed table into a worker-keyed answer plus new internal-package coupling. C is the only
remote-capable option but is MVP overkill.

---

## The web auto-open rule

In `SerfRenderer.init(conversationEl)` (`renderer.js:65`), after `this.sessionId` is set
(`renderer.js:97`):

1. **Read the signal.** `const observers = (conversationEl.dataset.observers || "").split(",")
   .filter(Boolean);` (server renders `data-observers` from `WorkspaceData.ObserverRouteIDs`).
2. **Guard the environment.** Do nothing if `!window.SerfPanes` (we're inside a pane iframe — same
   guard as the manual ⇲ button, `renderer.js:2379-2380`) or `observers.length === 0`.
3. **Respect the cap.** `SerfPanes.open` already enforces `MAX_SIDE_PANES = 3` and returns `null`
   when full (`panes.js:40`); just iterate and stop honoring further opens (it no-ops safely).
4. **Dedup is free for re-navigation.** `SerfPanes.open` focuses an existing same-href pane rather
   than duplicating (`panes.js:38-39`), and `restore()` re-creates persisted panes on reload — so an
   already-open observer pane won't double.
5. **Don't fight the user's close (Open Question 1).** Because `panes` does not remember a
   *user-closed* href, a plain auto-open would re-open a pane the user just dismissed on the next
   `init()` (e.g., htmx swap or reload). Options: (a) accept re-open; (b) add a short-lived
   per-session "suppressed observers" set in `localStorage` that `close()` writes and the auto-open
   rule checks; (c) only auto-open observers **not present in `openHrefs()` and not in the suppressed
   set**. Recommend (b)+(c) — small, and it makes "I closed it, leave it closed" stick.
6. **Skip dead observers.** Filter `data-observers` to **live** observers server-side (the hub has
   the live set, `tree.go:288-292`), so we never auto-open a pane for an ended observer.

Per-observer call: `SerfPanes.open("/s/" + encodeURIComponent(observerId), observerLabel)` — same
shape as the manual hook (`renderer.js:2394`).

---

## Effort & risk summary (LOC / components, not wall-clock)

| Component | Option A (recommended) | Option B (hub reads jobstore) | Option C (appwire) |
|---|---|---|---|
| `SessionMeta.ObservedBy` field | ~5 | 0 | 0 |
| Watch-time stamp in `job_watch` | ~30–60 | 0 | ~30–60 (daemon-side compute) |
| Hub jobstore reader + worker↔observer inversion | 0 | ~120–200 | 0 |
| Meta → `WorkspaceData` → `#conversation` attr | ~30–50 | ~30–50 | ~30–50 |
| appwire wire-type + handler + mapping | 0 | 0 | ~150–250 |
| JS auto-open rule (+ closed-pane suppression) | ~50–90 | ~50–90 | ~50–90 |
| Tests | moderate | moderate–large | large |
| Remote (codex) coverage | ✗ (deferred) | ✗ | ✓ |
| New hub↔agent-internal coupling | none | yes (`internal/jobstore`) | none |

Riskiest assumptions to validate early:
1. **Cross-session meta write safety** (Option A worker-stamp): confirm whether the watching session
   can safely write the worker's meta, or whether to stamp the observer's own meta and invert in the
   hub. Drives the A sub-variant.
2. **Closed-pane suppression** is wanted (Open Question 1) — without it, auto-open re-opens dismissed
   panes on every navigation.
3. **"Which session triggers auto-open"** (worker vs parent vs both) — drives where the field is read
   and what "viewing" means.

---

## Phased plan

**Phase 0 — decisions (no code)**
- Get Jesse's answers, especially: which session triggers auto-open (worker/parent/both), and
  closed-pane suppression yes/no.

**Phase 1 — surface the signal (agent)**
- Add `ObservedBy []string` to `SessionMeta` (`agent/schema/snapshot.go`).
- Stamp it at grant mint in `job_watch` (`agent/job_watch.go:2016-2041`, `:2054-2084`), per the
  worker-stamp vs observer-stamp decision from Phase 0.
- Unit tests: a sidecar watch records the observer on the worker (or observer) meta; survives resume.

**Phase 2 — flow to the page (hub)**
- Add `ObserverRouteIDs []string` to `WorkspaceData` (`web_types.go:161-199`); fill it from the
  worker's meta in `workspaceData`/a `fillObserverLink` mirror of `fillSubagentLineage`
  (`web_workspace.go:381-396`), filtered to **live** observers.
- Render `data-observers` on `#conversation` (`templates/partials/workspace.html:37-43`).

**Phase 3 — auto-open (JS)**
- Implement the auto-open rule in `SerfRenderer.init` (`renderer.js:65,97`) per "The web auto-open
  rule" above, including closed-pane suppression if chosen.
- e2e scenario: open a session with a live observer → observer pane auto-opens; close it → it stays
  closed across an htmx swap; reopen the session in a fresh tab → it auto-opens again.

**Phase 4 — remote (only if needed)**
- Option C: add `ObservedBy` to `appwire.Thread`/`SerfThread`, daemon-side compute, hub mapping in
  `hubDetailFromAppThread` (`web_api_tree.go:274`) and remote tree projection (`:162-199`), codex
  parity or explicit unsupported.

---

## Open questions for Jesse

1. **Auto-open on every load, or once-per-dismissal?** `SerfPanes` persists open panes but never
   records a *user-closed* href (`panes.js` `persist()` writes only the open set; `close()` just
   drops the pane, `:67-72`). Without a suppression memory, auto-open re-opens a dismissed observer
   pane on the next navigation/reload. Add per-session "suppressed observers" memory, or accept
   re-open?
2. **Which session triggers auto-open?** The observer watches a **worker**; both are delegate
   children of a **parent** that ran `job_watch`. Fire when viewing the worker (pair them), the
   parent, or any of the three? (Recommendation: the **worker**.)
3. **Stamp the worker's meta or the observer's?** Worker-stamp = trivial hub consume but a
   cross-session meta write (race risk); observer-stamp = clean write but an O(N) hub inversion.
   Which tradeoff?
4. **Multiple observers?** A worker can have >1 observer (the field is a set). Auto-open all (up to
   the 3-pane cap), or just the first/most-recent? With docs/other subagent panes competing for the
   same 3 slots, do observers get priority?
5. **What if the observer ended?** Recommend filtering to live observers server-side and not
   auto-opening dead ones — but should an ended observer still be *manually* openable (replay)? (It
   is today via the ⇲ button.)
6. **Remote sources:** is local-only acceptable for v1 (codex/remote sessions get no auto-open until
   Option C), or is remote a hard requirement?
7. **Live vs read-only observer pane:** the auto-opened observer pane is a full `/s/<id>` iframe with
   its own composer (you can message the observer). Acceptable, or should auto-opened panes be a
   stripped/read-only variant (needs the embed-shell param the multi-pane MVP deferred)?
