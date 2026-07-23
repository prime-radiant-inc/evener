# M9 — Whole-Product Live E2E Plan (journey-suite decomposition)

**Date:** 2026-07-22 · **Author:** controller (doc-only; execution is post-deletion)
**Branch:** `worktree-webui-workspace-shell` (integration) · **Milestone:** M9, the whole-product
live end-to-end pass, run **once against the final artifact**.

> This is a planning document, written now so M9 executes with zero planning latency the moment the
> deletion lands. It does **not** run anything. Each suite is a set of **scenario cards**
> (`e2e-scenario-testing` skill) an agent drives against a freshly built, isolated hub. Suites are
> heavy (real hub + real model + Chrome) — respect the pacing cap in §4.

## 1. What M9 runs against (the final artifact)

Per Jesse's adopted order (ledger 2026-07-22 pre-flight authorities): **W6 close → W6 merge → W8 →
M10 deletion + flag-flip → M9 → final whole-branch review.** M9 therefore runs on the tree **after**
the legacy UI has been deleted and the flag flipped — the exact configuration that ships. That is the
point of running it last: verify the final configuration once, and let the full e2e catch any
deletion mistake instead of shipping it unverified.

By M9 the artifact contains, all merged to integration:
- **Waves 1–4:** protocol/reducer, design system (28 widgets), workspace shell (dockview host, rail,
  mobile stack, routing), transcript engine (streaming, tools, subagents, ask cards, scroll/liveness).
- **Wave 5:** composer / queue / pending / askDock / session chrome.
- **Wave 6:** spawn pane (hub-spawn), ⌘K command palette, notifications engine, display/rail
  (`sidebarMode`, ⌘B, left drawer).
- **Wave 7:** settings (17 sections), credentials, marketplaces/plugins, prefs store.
- **Wave 8:** rich model catalog (spawn + settings), transcript-parity clusters (`ItemModel.error`
  rendering, task/plan cards, steering classification, turn-failure diagnostics), doc/image viewer
  pane, `/thread/{ref}` single-pane mode + read-only transcript pane + open-beside/popout, PWA
  re-brand, session-chrome + settings polish.
- **Main-absorbed Go:** wire-honesty (args/exitCode/resolved-broadcast), instance-CRUD broadcast,
  flake fixes, terminal-error tool status (`MW-A`), raw-file doc data path (`MW-B`).
- **M10 deletion + flag-flip:** legacy `assets/*.js` + `style.css` + `templates/**` + `jstest/**`
  (262 whole files) and ~31 Go surgical sites removed; `newWebEnabled()` deleted; the 5 page-route
  handlers now serve the SPA unconditionally.

### The flag's fate — verified

`cmd/serf-hub/webnext.go:16` today is `func newWebEnabled() bool { return
os.Getenv("SERF_HUB_WEB") == "new" }`, gating 5 page-route sites (`web.go:226`, `web_workspace.go:45`
& `:158`, `web_settings.go:46`, `web_launchconfig.go:8`). The M10 flip **deletes** `newWebEnabled()`
and makes those 5 sites unconditional `serveSPAIndex` (kill-list §3, `webnext.go` KEEP-list). **After
the flip `SERF_HUB_WEB` is read nowhere — it becomes dead/no-op.** Consequences for M9:
- M9 hubs are launched with **no** `SERF_HUB_WEB` set — the default now serves the SPA. (The wave-6/7
  live proofs required `SERF_HUB_WEB=new` because they ran pre-flip; that requirement is gone.)
- Setting `SERF_HUB_WEB=new`, `=anything`, or leaving it unset must all behave **identically** — S7
  asserts this (the env var no longer branches anything).
- `cmd/serf-hub/webnext_test.go` currently pins legacy-vs-new parity under `SERF_HUB_WEB=new`; the
  deletion updates/removes those tests. S7's Go-gate re-run catches any breakage.

## 2. Mission and the dedup principle

M9 is **not** a re-run of the wave closes. Each wave close already proved its surfaces live on an
**intermediate** tree. M9's job is twofold:

1. **Re-prove the CRITICAL cross-surface spines on the final artifact** — the deletion + flip is the
   one change no wave close saw, so the spines get re-driven once on the shipping configuration.
2. **Cover what no single-wave close could** — the four categories below, none of which any wave
   owned end-to-end:
   - **Cross-pane flows** — dockview open-beside / popout / multi-pane / drag-resize / layout
     persistence across real multi-session use (W8 T8 proved single-agent basics only).
   - **Multi-tab everything** — each wave proved exactly one two-tab case (W6 notifications election;
     W7 credential staleness). M9 sweeps concurrent-tab propagation across *all* surfaces.
   - **Mobile viewport sweeps** — mobile stack (W3), spawn form / drawer / DirField (W6), single-pane
     mobile (W8) were built piecemeal and never swept as one viewport pass.
   - **The deletion's blast radius on shared endpoints** — the catastrophic failure mode is a deleted
     shared endpoint (kill-list §2: **24 protected endpoints**, incl. the **13-route `hubapi.Client`
     TUI contract** the SPA never touches).

Where a card would merely repeat a wave-close journey verbatim, it is **cut**; the dedup note on each
suite says what the close already proved and what M9 adds on top.

## 3. Isolation recipe (every suite obeys this)

The host-global-flock lesson is load-bearing: `serf-hub` takes a **host-global flock at
`$HOME/.serf/hub.lock`** (`cmd/serf-hub/main.go:133-135`, "single hub per host"). Two hubs under the
same `$HOME` cannot coexist, and a parallel suite that shares `$HOME` will collide (W5/W6 closes hit
exactly this against each other). Therefore:

- **Build once, centrally.** The controller builds the artifact **once** at M9 kickoff:
  `npm run build` → `git restore dist/PLACEHOLDER` → `go build -o <shared>/serf-hub ./cmd/serf-hub`
  (embeds the fresh dist) and `go build -o <shared>/serf ./cmd/serf` (for hub-spawn + the TUI in S7).
  Every suite runs **that** binary and confirms it is the fresh one (skill principle 1: the #1 e2e
  mistake is testing a stale process).
- **Per-suite fake `$HOME`.** Each suite exports `HOME=<scratch>/s{N}-home`, giving it its own
  `hub.lock`, `~/.serf/run` rendezvous, and state root — all `HOME`/XDG-derived (`rendezvous.go:40`,
  `config.go:89`). This is the same isolation the Go suite uses (`t.Setenv(SERF_STATE_DIR,
  t.TempDir())`), **not** a flock bypass.
- **Also isolate `XDG_CONFIG_HOME`** for any suite that touches plugins/marketplaces (S4). The plugin
  root is **global** — `cfg.PluginRoot` unset falls to `~/.config/serf/plugins` honoring
  `XDG_CONFIG_HOME`, and is **not** isolated by the hub state root (W7 close wrote to the shared store
  and had to clean up via CLI). Set `XDG_CONFIG_HOME=<scratch>/s4-config` so marketplace/plugin
  mutations stay hermetic.
- **Distinct loopback port per suite.** Bind `127.0.0.1:<port>` with a unique port per suite so no two
  hubs contend. The wave closes used 19280 (W7) / 19281 (W5) / 19286 (W6); M9 suites each pick a
  unique unused port (a suite that finds its port busy is a finding, not a retry target).
- **Serialized / isolated Chrome per suite.** Parallel agents that shared one Chrome profile/port
  contaminated screenshots and held DOM-eval (ledger, W2-T8). Each suite drives its **own** Chrome
  instance (own remote-debug port + own profile dir). Multi-tab *within* a suite = multiple tabs in
  **that** suite's Chrome, never a shared instance across suites.
- **Cheapest real model, from `.env`.** Drive `openai/gpt-5-nano` (the wave-6-close precedent — the
  cheapest real model that completes real turns), materialized from the repo `.env` via
  `set -a; . ./.env; set +a` (the zsh bare-`. .env` gotcha, ledger). The model id is verified at
  dispatch time; the session's proven fallback is `oai-work/gpt-5.4-mini` with the repo `.env`
  sourced. No mocks anywhere — M9 is a real-hub, real-model, real-browser pass.
- **Never echo credentials.** Tokens/keys are masked in every screenshot, log, and report line (the
  standing invariant; W7 verified the "UI never displays stored values" copy). A card that would
  capture a live device code or bearer token blurs/omits it.
- **Browser-profile hygiene.** dockview persists layout to `serf.workspace.layout.v1`; a reused
  profile restored stale cross-origin session tabs in W6/W7. Each suite starts from a clean profile
  (or clears that key first) so a restored ghost pane is never mistaken for a product bug.
- **Cleanup is part of the test.** Idempotent teardown: shut down the spawned hub + every `serf serve`
  daemon it spawned, remove the scratch `HOME`/`XDG_CONFIG_HOME`, and (S4) re-assert the global plugin
  store baseline if `XDG_CONFIG_HOME` isolation is ever in doubt. **Never touch a pre-existing host
  hub** — leave it running and untouched.

## 4. Pacing

Suites are heavy (each = a hub + spawned daemons + a Chrome instance + real model turns). The pacing
cap is a **controller knob**, not a fixed ceiling — currently **16 heavy agents concurrent** (ledger,
2026-07-22) — and M9 runs **with nothing else heavy alongside** (W8 is merged, the deletion is done —
M9 is the only load). Seven suites therefore run in **two paced batches**:

- **Batch 1 (de-risk the artifact first):** **S7** (deletion blast radius), **S1** (core interaction
  spine), **S2** (transcript + doc pane), **S4** (settings + credentials).
- **Batch 2 (the novel cross-cutting coverage):** **S3** (cross-pane workspace + single-pane), **S5**
  (multi-tab everything), **S6** (mobile viewport sweep).

S7 leads batch 1 deliberately: it validates the shared-endpoint foundation, so a shared-endpoint
regression surfaces as an S7 finding rather than as confusing failures scattered across S1/S2/S4. The
suites are otherwise independent (own hub each), so the batching is a de-risking preference, not a
hard dependency — the controller may reorder within the pacing cap (a controller knob, currently
16-heavy as of 2026-07-22).

## 5. The suites (7)

Each suite is one dispatched agent running a small deck of scenario cards. Scope is deduplicated
against the wave-close proofs; every card states its **falsification condition** (silence is not
success) and cross-checks the rendered claim against the authoritative record (on-disk state / `/rpc`
frame / `/api/tree`) when a visual is ambiguous.

### S1 — Core interaction spine (hub-spawn → live session, under load)
**Surfaces:** spawn pane, session pane, composer/queue/pending, session chrome, model switch, session
actions (fork/aside/compact/clear/rename), goal, tasks panel.
**Dedup:** W6 close proved the queue/steer/edit/promote headline on its intermediate tree; W5 could
**not** (bare `serf serve` advertised the caps as false — hub-spawn is required). M9 re-proves the
spine **on the final artifact** and **with W8's rich model catalog live** at spawn (badges/cost/Recent
now shipped, not the W6 interim Combobox).
**Cards:** (1) hub-spawn a real session from the spawn pane, picking a model from the **rich catalog**
(assert provider grouping + capability badges + a Recent entry render — falsify: only a flat qualified
combobox appears → W8 catalog swap regressed). (2) The headline under-load loop: type under an active
turn → Send flips to **Queue**, Stop + Steer enable; queue two, drain FIFO; promote one ahead; edit
one (restore-to-composer + dequeue); steer mid-turn (assert the "Steering injected" marker + the
injected turn ran). (3) Model switch mid-session; session-actions menu with the correct busy-gating
(Fork/Aside disabled during an active turn); goal set → engine drives autonomously → clear; tasks
panel shows live per-task rows. Cross-check queue order against the `/rpc` turn frames, not just chips.

### S2 — Transcript parity + doc/image viewer (W8 surfaces + cross-pane open)
**Surfaces:** transcript renderers (tool cards, `ItemModel.error`, task/plan cards, steering
classification, turn-failure diagnostics, reasoning/think blocks, ask cards), doc/image pane
(`openDocBeside`).
**Dedup:** W4 close proved streaming/tools/scroll; W8 T8 proved the new clusters single-agent. M9
drives them **assembled on the final artifact** and adds the **cross-pane open-beside** the transcript
feeds. Ratification observation point **#1** lives here (see §7).
**Cards:** (1) Drive a session that exercises read/grep/shell/web/job tool cards; force a **denied
shell** and assert its **error text renders** and the row force-expands (W8 T3 + MW-A honest status —
falsify: error text invisible or row shows `status:"completed"`). (2) Drive a `task_list` mutation →
**task card** with progress head; a steering message → **classified** divider (not the generic
"Steering injected"); a forced turn failure → **red end-cap + taxonomy badge + Retry/Reconnect**. (3)
From a file tool card, **open a doc pane beside** the session: text file, markdown (assert
DOMPurify-sanitized render — the legacy had no sanitizer), an image (raw bytes via `/doc/image`), and
a **>512 KiB truncation notice** (beyond-parity). (4) **Ask observation** — drive a real `ask_user`
round-trip and **describe** the transcript representation for ratification (§7 #1).

### S3 — Cross-pane workspace + `/thread/{ref}` single-pane (dockview)
**Surfaces:** dockview host (splits/tabs/popout/persistence), `openBeside`/`popOutPane`,
`/thread/{ref}` single-pane mode, read-only transcript pane.
**Dedup:** no wave close swept multi-pane behavior — W3 built the host, W8 built single-pane, neither
exercised real cross-pane workflows. Ratification observation point **#2** lives here.
**Cards:** (1) Open two live sessions side-by-side; open a read-only **transcript** pane beside them
via `openBeside`; **pop out** a pane to a native window; drag-resize; reload and assert layout
**persists** (and does **not** restore ghost panes from a stale profile — verify against the current
hub's `/api/tree`). (2) `/thread/{ref}` **share link**: assert one **chrome-stripped** pane (rail +
tab strip + search + settings-link hidden), a **live composer** (§7 #2 observation), and the
**fallback title persists** for an unknown ref (beyond-parity, does not blank). (3) Mobile degrade of
open-beside → full navigate (spot-check; the full mobile pass is S6).

### S4 — Settings + credentials + settings-scoped multi-tab staleness (W7 surfaces)
**Surfaces:** all 17 settings sections, credentials/OAuth, marketplaces/plugins, launch-config,
theme/density prefs, overview.
**Dedup:** W7 close proved these on its intermediate tree. M9 re-proves on the final artifact (settings
lazy chunk survived the deletion; providers→credentials redirect from W8 T1) and confirms the
**credential** cross-tab path still fires post-absorb. **Isolation note:** this suite sets
`XDG_CONFIG_HOME` (plugin root is global).
**Cards:** (1) Credential add → real **OAuth device flow** driven to the human-authorization boundary
then **cancelled** (never completed; device code masked). (2) Marketplace add → browse → **install
with confirm** (Source line) → disable; assert the Installed status dot + no double-fire on Refresh
(W8 T7 polish). (3) Dir validate (invalid kept, valid added); launch-config edit → resolve → persisted
to the **isolated** `launch.toml` (Jesse's `~/.serf/launch.toml` untouched). (4) Theme/density persist
+ `prefers-color-scheme` follows OS; overview shows real daemon data with the **bearer token masked**.
(5) Two tabs on Credentials: `apiKeySet` in tab A → tab B live-updates via `serf/auth/updated`
(falsify: tab B stale after a broadcast). **Describe** the instance-CRUD boundary: instance
create/edit/remove now broadcasts (main `28e2b2141`) — confirm it propagates, or record if the absorb
didn't carry it.

### S5 — Multi-tab everything (cross-surface propagation)
**Surfaces:** poll-free sidebar (rename/archive/favorite/project-delete push), escalation
resolved-broadcast, notifications leader election, attention/needs-you tiering, ask cross-tab.
**Dedup:** the multi-tab *category* no single wave owned — W6 proved only notifications election, W7
only credential staleness. M9 sweeps propagation across the session/sidebar/attention surfaces.
**Cards:** (1) Two+ tabs: **rename / archive / favorite / project-delete** in one tab propagates to the
others via the push-fed sidebar (falsify: a second tab needs a reload — the sidebar is poll-free by
design). (2) A **sandbox escalation** resolved in one tab clears its card in the other via
`serf/sandbox/escalation/resolved` (the wire-honesty broadcast + reducer case). (3) **Notifications
leader election** across two tabs: exactly one holds the `serf-hub-os-leader` Web Lock; a needs-you
transition fires title count + favicon dot on the leader only; re-prove on the final artifact. (4) An
`ask_user` answered in one tab settles the dock in the other; the needs-you tier updates in both.

### S6 — Mobile viewport sweep
**Surfaces:** every surface at mobile widths — session stack, spawn form, rail drawer, single-pane,
settings nav-as-page, command palette, notifications.
**Dedup:** built across W3/W6/W8, never swept as one viewport pass. M9 sweeps the pinned breakpoints.
**Cards:** (1) At phone width (≤ 767 and the 900 density band): mobile session **stack** navigation +
**isTrusted gesture-back**; the rail collapses to the **left-anchored ☰ drawer** (W6 fix). (2) Spawn
form on mobile (DirField anchored-popup / bottom-sheet behavior — the W6-T2 residual); **single-pane**
`/thread/{ref}` on mobile; open-beside **degrades to full navigate**. (3) Settings **nav-as-page +
Back** (the accepted mobile divergence); ⌘K palette usable; notifications surface. Sweep the pinned
widths (e.g. 390 / 768 / 900 / 1100 / 1200) and assert no horizontal-scroll / clipped-control
regressions at each.

### S7 — Deletion blast radius (the final-artifact safety net) — runs first
**Surfaces:** the 29 protected shared endpoints (kill-list §2.1's 24, amended by Appendix D's 5
reclassifications — `/api/search`, `/api/dirs/create`, `/api/git/head`, `/doc/file`, `/doc/image`),
the flag no-op, the dead legacy routes, the `/doc` reshape, the PWA/auth-exempt assets, and the
post-deletion gate health.
**Dedup:** entirely novel — no wave close could test a deletion that hadn't happened. This is the suite
that catches a mis-scoped excision.
**Cards:**
1. **SPA-consumed endpoints (S) survive** — via the browser's own network layer against a live hub,
   confirm `/rpc` WS, `/api/tree`, `/api/tree/project`, `/api/sessions/{ref}/rename`, `/api/archive`,
   `/api/favorite`, `/api/project/delete`, `/s/{ref}/images/{sha}`, `/manifest.webmanifest`,
   `/webassets/*`, `/auth`, and the 4 auth-exempt `/assets/icon-*` all serve (falsify: any 404/500).
2. **TUI-consumed endpoints (T) survive** — the SPA never touches these, so drive the **real TUI**
   (built `serf` binary, tmux) against the post-deletion hub, or curl the **13-route `hubapi.Client`
   contract** (`client_test.go` pins the exact paths): `health`, bare `sessions/{ref}`, `send`,
   `tasks`, `interrupt`, `compact`, `clear`, `fork`, `model`, `spawn`, `spawn-schema`, `models`,
   `tree`. This is the highest-value card — the deletion's whole risk is an endpoint the SPA can't
   reveal.
3. **`/api/search` reclassified to protected** — the ⌘K palette consumes REST `/api/search` (W6-T3);
   confirm palette **search → open** works live (the endpoint the kill-list's dry-run re-validation
   flips from orphaned to protected). Confirm **spawn preflight dir-creation** drives `POST
   /api/dirs/create` live from the spawn pane's directory field (falsify: creating a directory during
   preflight 404s, or the created directory never appears in the picker). Confirm **branch-HEAD
   auto-resolution** drives `GET /api/git/head` live when a git-backed directory is chosen at spawn
   (falsify: the branch field fails to populate, or the request 404s). Confirm the 3 genuinely-orphaned
   §1.6 endpoints — `GET /api/upgrade`, `POST /api/sessions/{ref}/reasoning-effort`, `POST
   /api/path/validate` — are **kept, not deleted** per Jesse's safe-default (contract changes aren't
   drive-bys): issue an authenticated request to each post-deletion (falsify: any of the three returns
   404, meaning the safe-default was silently violated).
4. **Flag no-op** — the hub launched with **no** `SERF_HUB_WEB` serves the SPA at every page route
   (`/`, `/new`, `/s/{ref}`, `/thread/{ref}`, `/settings`, `/settings/{section}`, `/credentials`);
   setting `SERF_HUB_WEB=new` or `=garbage` behaves identically (falsify: any page route returns legacy
   HTML or differs by env).
5. **Dead legacy routes 404** — `/_partials/*`, `/_api/subagent-preview`, and the `/s/{id}/{action}`
   form-POSTs return **404** (mux default, no redirects, kill-list §3).
6. **`/doc` reshape** — `/doc/image` serves raw bytes; `/doc/file` raw mode (MW-B) feeds the native
   pane; no dead legacy-asset links remain.
7. **No legacy residue + gates green** — `git grep htmx` returns nothing; no reference to any deleted
   `assets/*.js` / `templates/**`; PWA install + auth-exempt icon fetch work; and re-run the **Go test
   suite + frontend gate** on the post-deletion tree (catches `webnext_test.go` and any dead-symbol
   fallout the deletion left).

## 6. Per-suite report contract

Each suite agent returns to the controller (not a stray report file) a structured result:

- **Suite ID + scope** — which cards ran; explicit dedup note (what the wave close already proved).
- **Per-card verdict** — `PASS` / `FAIL` / `ENV-LIMITED`, each with the **concrete observation** (the
  rendered text, the on-disk value, the `/rpc` frame), never "looks good." Falsification-grade.
- **Findings, each classified** — `PRODUCT DEFECT` / `TEST-HARNESS ARTIFACT` / `ENV-LIMITED
  (finding-not-failure)`, with root cause. No retry-until-green, no timeout widening (§8).
- **Ratification observations** (S2, S3 only) — the *described behavior* of the two ratified-by-default
  items, in enough detail for Jesse's post-hoc veto (§7).
- **Isolation params** — port, `HOME`, `XDG_CONFIG_HOME` (if set), model, Chrome profile — so any
  finding is reproducible.
- **Evidence** — screenshot / captured-pane / on-disk-artifact paths (credentials masked). Suggested
  home: `.superpowers/sdd/m9-evidence/s{N}/`.
- **Gate deltas** (S7 only) — the post-deletion Go + frontend gate exit codes and counts.

The controller consolidates the seven results into the M9 record that feeds the final review (see the
final-review scaffold's M9-ratification section).

## 7. Ratified-by-default items — explicit observation points

Jesse's pre-flight authority #4: two design choices **proceed as built** and are **described in M9
evidence for post-hoc veto** — M9 does not block on them, it *documents* them.

1. **Ask-transcript re-architecture** (observed in **S2**). The wave-4 choice: **no `[data-ask-anchor]`,
   no `.ask-settled-line`, the ask dock is not `form`-owned**. S2 drives a real `ask_user` round-trip
   and **describes**: how the pending ask renders in the transcript, how the settled ask reads, where
   the dock sits relative to the composer, and whether the assembled result is coherent. Verdict is
   *described*, not *graded* — Jesse rules.
2. **`/thread/{ref}` single-pane live composer** (observed in **S3**). The W8 §Ambiguities-#1
   resolution: the share link renders the **session pane with a LIVE composer** (honoring tested legacy
   §2.5), chrome-stripped, rather than a read-only snapshot. S3 **describes**: composer present and
   functional on the share link, chrome fully stripped, fallback title behavior. Jesse vetoes to
   read-only if he prefers (a dispatch-time routing swap, per the W8 plan).

## 8. Failure protocol

**A failure is a finding, not a retry target.** When a card fails:
- **Classify product-vs-test.** Is it a real product defect, a test-harness/scenario artifact (wrong
  selector, wrong layer, vacuous assertion — "executing the card tests the card"), or an environment
  limit? Verify the *right surface*: a "missing" value is often present one layer over (model field vs
  rendered chip; internal capability vs REST projection).
- **No retry-until-green, no timeout widening.** Jesse's standing rules bind M9: *await the actual
  behavior, never widen a ceiling to absorb it* (`feedback_await_behavior_not_timeouts`); *always fix
  flakes at the root* (`feedback_fix_flakes_root_cause`). A card that only passes on the 3rd run is a
  finding about the product or the card, not a pass.
- **Env-limited items are findings, not failures.** Headless Chrome does not grant `Notification`
  permission and has no audio; the OS-notification popup and sound audibility are **recorded as
  env-limited with code + unit corroboration** (the W6-close precedent — the fire path is
  permission-guarded in code, covered by unit tests). Reconnect-does-not-re-alert similarly needs a
  forced WS reconnect; record the mechanism if not driven live.
- **Present-but-not-visible ≠ absent.** Virtualized transcript rows and auto-scroll routinely push a
  real element out of the capture window; scroll/expand and cross-check a sibling read before
  concluding a regression.
- **Product defects are triaged, not silently fixed.** M9 is a verification pass on a frozen artifact;
  a defect it surfaces goes to the controller with evidence and a product-vs-test classification for
  scheduling (fix-before-ship vs punch-list vs accept), and into the final-review record. Vacuous
  green is worse than a red.

## 9. Self-review

- **No placeholders.** Every suite names its surfaces, its dedup basis, and concrete falsifiable
  cards; no "TBD" / "similar to suite N" / "etc. (unspecified)."
- **No invented floor numbers.** Every count is sourced: **24** protected endpoints + **13**-route TUI
  contract + **6** orphaned §1.6 + **5** flag sites + **262** deleted files (kill-list §0/§2/§3);
  **17** settings sections (wave7-report); the **2** ratification items (ledger 2026-07-22). The model
  `openai/gpt-5-nano` and ports 19280/19281/19286 are the *actual* wave-close values, not invented;
  M9 suites pick their own unique ports (stated as a convention, not a pinned number). The model id is
  verified at dispatch time; the session's proven fallback is `oai-work/gpt-5.4-mini` with the repo
  `.env` sourced.
- **Internal consistency.** Suite IDs S1–S7 are used identically in §4 (pacing), §5 (definitions), §6
  (report contract), and §7 (ratification homes). The two ratification items map S2→#1, S3→#2 in both
  §5 and §7. The flag-fate analysis in §1 and S7 card 4 agree (post-flip `SERF_HUB_WEB` is a no-op).
- **Dedup is explicit** on every suite, satisfying the "M9 re-proves spines + covers what no close
  could" mandate rather than re-running wave closes.
- **Isolation recipe is complete** — flock, fake `$HOME`, `XDG_CONFIG_HOME` (plugin-root global
  caveat), distinct port, isolated Chrome, cheapest real model from `.env`, never-echo, profile
  hygiene, cleanup — each tied to the ledger lesson that earned it.
- **Open question for the controller** — see the return note; the port allocation and the exact
  batch-1/batch-2 membership are the only execution-time knobs, and are stated as controller-adjustable
  within the pacing cap (§4; a controller knob, currently 16-heavy as of 2026-07-22).
