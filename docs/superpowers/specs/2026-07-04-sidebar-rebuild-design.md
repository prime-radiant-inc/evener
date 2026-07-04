# Sidebar Rebuild & Thread Management — Design (v1)

Date: 2026-07-04
Status: Draft — awaiting Jesse's spec review
Workstream: 3 of 6 from the 2026-07-03 web-UI UX diagnostic. Mandate: "nothing is sacred; good is better than keeping back-compat."

## Problem — each symptom, mechanized

- **Slow.** The sidebar is one htmx element re-fetching the entire server-rendered tree and replacing its whole DOM (app.html:36-41) on an 11-event allowlist that includes `item/started`/`item/completed` (sidebar.js:287-309) — every tool call in a watched session rebuilds the sidebar behind a 50ms debounce (sidebar.js:277-285). Server side has no cache: every request re-sorts/re-classifies every indexed session including archived (tree.go:285-673), `BuildTree`+`DeriveAttention` run three independent times across handleSidebar/handleAPITree/the watcher, and a configured Codex remote source costs a synchronous network hop per render (web_api_tree.go:132-155).
- **Content disappears; expansion lost.** Non-live projects ship empty children every render (sidebar.html:114); a manual expand lazy-fetches rows (sidebar.js:116-142); the next full swap discards them and only the active session's project ever auto-refetches (sidebar.js:92-106) — the section restores as "expanded" and empty. Latent races compound it: no `hx-sync`/in-flight guard anywhere (stale response overwrites newer), and the disclosure snapshot is one shared unscoped variable (sidebar.js:211).
- **Flashes.** "loading…" text on first paint — the sidebar is hard-excluded from the skeleton system (skeleton.js:15-20); every refresh destroys and recreates all row nodes; scroll offset survives by accident while content shifts under it.
- **Hard to read; bad hit targets.** 10px metadata on the two lowest-contrast tokens (`--text-dim` ≈2.9:1 fails AA, `--text-muted` ≈4.7:1 borderline; style.css:4-30, 69-76); the chevron's clickable box is ~12px because a documented desktop override strips the 32px tap-min (style.css:687-692) and the fixing rule is dead CSS — the template never applies `.btn-icon` (sidebar.html:77-78 vs style.css:711-724); subagent rows ≈17px tall.
- Also: sidebar.js's own header comments are false on three counts (dead stagger path, wrong trigger description; sidebar.js:6-10, 170-177) — the rebuild deletes them.

## Decisions

1. **Client-rendered, keyed (Jesse, 2026-07-04).** The sidebar stops being an htmx partial; data is the state, the DOM is a projection. The Go sidebar templates and `/_partials/sidebar{,/project}` routes die.
2. **Rename in scope, daemon-truth (Jesse, 2026-07-04).** `serf/thread/name/set`; `NameSource` gains `"user"`; both auto-namer paths skip user-named sessions.
3. Carried: project-level delete only (no per-session delete); no name-prefix matching for test runs (the 2026-06-17 spec removed that hack; its named successor — a first-class origin marker — is what ships); pinned tier sits below Needs-you (urgency outranks curation); native `confirm()` for delete in v1 (house precedent, credentials.html:416,430).

## Foundation

**Tree JSON.** `/api/tree` (hubapi.TreeResponse) grows additive fields; the TUI ignores them. TreeResponse: `+NeedsYou []TreeNode`, `+Favorites []TreeNode`, `+ArchivedProjects []TreeProject`, `+TestRuns []TreeProject`. TreeProject: `+RollupLive, +RollupAttn int`, `+DefaultExpanded bool`, `+MoreCurrent/+MoreRecent/+MoreArchived int`, `+Worktrees int` (count of sessions referencing a managed worktree — feeds the delete confirm), and `Sessions` carries only inlined (expanded-by-default) projects' rows. TreeNode: `+Tier string` ("current|recent|archived"), `+Branch string`, `+ClusterCount int`, `+Favorite bool`. Subagent children gain the same per-tier 50-cap as top-level rows (today they are uncapped in every payload; tree.go:400-454). Collapsed projects ship rowless; `/api/tree/project?key=` returns one project's rows as JSON (replacing `/_partials/sidebar/project`).

**Renderer** (sidebar.js rewritten). Hand-rolled keyed reconciliation — no framework: nodes keyed by project key / session ID; type+key match patches attributes and text in place, mismatches create/remove; unchanged keys keep their DOM node identity, so hover state, open menus, transitions, and scroll geometry survive every update by construction. Client model: tree data + expansion set (same localStorage keys as today) + lazy-children cache + fetch sequence counter. Rows remain real `<a href>` links (progressive navigation preserved) and keep their htmx workspace-swap attributes — the renderer calls `htmx.process()` on newly created nodes so dynamically built rows behave identically to server-rendered ones; `aria-expanded` on all toggles; first paint renders skeleton rows immediately (the skeleton.js exclusion becomes irrelevant — the client owns its own loading state).

**Update contract.** Qualifying events: `thread/started`, `thread/closed`, `thread/status/changed`, `serf/job/started`, `serf/job/finished`, `serf/attention/changed`, connection-restored. Dropped from today's list: `item/*`, `turn/*`, `thread/queueChanged` — none carries anything the sidebar renders. `serf/attention/changed`'s `changed[]`+`summary` apply to row levels and badges instantly; everything else schedules a coalesced resync: ≥2s spacing, trailing, sequence-guarded (a response older than the latest applied one is discarded), plus a 60s idle resync as the healer. On resync the client re-requests children for projects *it* has expanded — expansion is client-truth, so the lazy-children hole cannot exist. Mutations (archive/favorite/delete/rename) update the model optimistically, POST, then resync.

**Server.** One memoized tree: `BuildTree`+`DeriveAttention` compute once per inputs-version (bumped by past-index rebuild observers, roster changes, decision writes, PokeAttention) and every consumer — `/api/tree`, the attention watcher — reads the memo; the three-way independent recompute collapses. Remote sources move to an async cache (background refresh per source, ~30s + poke); no tree request ever blocks on a network hop.

## Features

**Row menu (⋯).** One popover component on chip-picker's positioning/dismiss/singleton patterns (dir-picker.js:7-72), with keyboard navigation (arrows/Enter/Esc) and a real ≥24px reveal button (hover + `:focus-within`, like today's archive button but visible-sized). Session rows: Open, Open beside, Favorite/Unfavorite, Rename, Archive/Unarchive. Project rows: New session, Settings, Archive/Unarchive, Delete…. The bare hover-only archive box dies; archive keeps its endpoint and optimistic dimming.

**Favorites.** New `favorite` table cloned from the archive store's shape (`kind TEXT, id TEXT, favorited INTEGER, decided_at INTEGER, PK(kind,id)`; archive.go:48-55 precedent), kind="session" in v1; `POST /api/favorite` mirrors `/api/archive` (web_api_archive.go:9-49). Pinned tier: unarchived favorited sessions, most-recent-activity first, **excluding sessions currently in Needs-you** (no duplicate rows; they return to Pinned when calm). Rows show ★ state via `TreeNode.Favorite`.

**Project delete.** `POST /api/project/delete {key}` — session-set-scoped, never name/path-scoped: the handler resolves the project node's current session set (top-level rows plus descendant children, each a session with its own files), **refuses 409 while any is live** (roster check), then per session ID deletes `sessions/<id>.meta.json`, `sessions/<id>.transcript.jsonl`, and the `sessions/<id>/` directory (jobs.jsonl + job logs), each path derived from that session's own indexed StateDir — no directory removal above the per-session dir, ever. Scrubs FTS rows and archive+favorite rows, then `PastIndex.Rebuild()` + `PokeAttention()`. `worktrees/` is never touched (uncommitted work); the confirm text states session count and, when any session references a managed worktree, the worktree count and that they are preserved. Rationale for set-scoping: "project" is four non-co-referring keys (sidebar basename tree.go:210-217; archive-decision name; origin-hashed state bucket runtime_dir.go:16-38; cwd-hashed launchconfig) — basename collisions make any name-driven deletion unsafe.

**Test runs.** `schema.SessionMeta` gains `Origin string` (`origin,omitempty`), set from a `SERF_SESSION_ORIGIN` env/launch plumb at session create. Tree: projects whose every session carries `Origin=="test"` land in a collapsed bottom "Test runs (N)" group with per-project Delete and a group-level "Delete all test runs" (iterating the same endpoint). The agentic-testing recipe (docs/agentic-testing.md) exports the marker so new runs stop minting permanent unmarked residue. Existing unmarked backlog (~129 projects): cleared with the new delete verb — one-time local script driving `/api/project/delete` per key, provided to Jesse, not shipped.

**Rename.** `serf/thread/name/set` (ScopeBoth) for live sessions — daemon sets `Name`, `NameSource="user"`, `NameUpdatedAt`, persists meta. Ended sessions (no daemon): the hub edits the meta file directly, guarded by a not-live check, then FTS update + index rebuild. Both namer launch sites (`launchInitialPromptNamer`, `launchCompactionNamer`; session_namer.go:186,223) skip when `NameSource=="user"` — without that rule any rename gets clobbered by the compaction namer. UI: menu → inline edit in the row (Enter commits, Esc cancels).

**Typography & density.** 11px floor (nothing at 10px); `--text-dim` banned below 12px text (2.9:1); row meta 11px on a re-checked ≥4.5:1 pairing; the whole project header becomes the expand/collapse target (buttons `stopPropagation`) with the chevron as indicator, ≥24px hit boxes on all sidebar controls at desktop (the tap-min-strip override dies); subagent rows and the "Completed (N)" toggle get ≥24px heights; title 13px and 2-line clamp stay.

## Error handling

Resync failure keeps the last good model rendered (never blank); sequence guard makes reordered responses harmless. Delete is idempotent per session file (missing file = already gone, continue) and reports per-session failures without aborting the batch; a rebuild always follows. Rename on a just-ended session races safely: the live path 404s at the daemon and the client retries via the hub path after resync. Old daemons: rename method absent → menu item hidden for that source (capability check); everything else is hub-local.

## What survives

Tier semantics and windows (currentWindow/archiveWindow/50-cap, just hardened by the attention work), the archive store + `/api/archive`, event delegation, rail mode, resizer, localStorage expansion keys, `serf/attention/changed` as the badge authority.

## Out of scope

TUI adoption of tiers/favorites/menu (additive JSON keeps it untouched); drag-reorder of Pinned; multi-select bulk operations; per-session delete; a custom confirm modal; right-click context menus; list virtualization (payload caps + keyed reconciliation suffice at current scale — worst case ~150 top-level rows per expanded project).

## Testing

- jstest (new suite = the new contract; old sidebar suite dies with the template): keyed-reconcile identity/stability — including the exact reported regression (manually expanded non-live project's rows survive a refresh storm); sequence guard drops stale responses; trigger allowlist boundary including the dropped `item/*`/`turn/*`; menu open/dismiss/keyboard/focus-return; optimistic favorite/archive with rollback on POST failure; skeleton first paint; scroll preservation across resync.
- Go: favorite store; delete handler matrix (liveness refusal, basename-collision scoping — two projects sharing a basename, delete one, other's files untouched — FTS/flag scrub, idempotency, worktree preservation); origin bucketing (all-test vs mixed projects); tree JSON additive shape + subagent caps; memo invalidation on each input edge; rename live/ended paths + namer suppression regression.
- e2e cards: expand-nonlive-project-survives-working-session; favorite → pinned across reload; project delete full cycle (confirm text, live-refusal, post-delete tree); rename live + ended, name survives compaction.

## Estimate

~1,250–1,750 loc including tests: foundation ~600–850 (renderer 350–450; server memo + remote cache + JSON shape 250–400); features ~550–750 (menu 120–180, favorites 100–140, delete 150–200, origin 80–120, rename 100–150); typography/density ~100–150.
