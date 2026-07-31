# sidebar-expand-survives-live-resync: an expanded project stays expanded across a refetch triggered by live activity elsewhere

**What this covers**: the rail's per-row expand bookkeeping surviving a whole-
tree refetch fired by unrelated live activity. Regression class: a notification
anywhere on the hub re-fetches `/api/tree` and re-renders the tree — if the
expand bookkeeping doesn't carry across that render, a project you deliberately
opened silently collapses the moment something happens somewhere else.

The mechanism moved wholesale. There is no `doResync()` and no
`window.SerfSidebarModel` — expansion is React state in `Rail.tsx`
(`expandedOverrides`, `:164`, seeded from `localStorage` by `loadExpansion`),
resolved per row by `overrideLookup` (`railNodes.ts:106-108`) and persisted on
every toggle by `setExpanded` (`Rail.tsx:197-205`). The refetch is
`treeStore.refresh()` (`stores/tree.ts:339-352`), scheduled on a 250ms debounce
by any of `thread/started`, `thread/closed`, `serf/attention/changed`,
`serf/tree/changed` (`REFRESH_NOTIFICATIONS`, `stores/tree.ts:443-450,455-467`).
Those two live in different stores and never touch each other, which is exactly
the property this card exists to keep true.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — the selector
map there is the single place these hooks are maintained. Rows are
`[data-session-ref="local:<SID>"]` (`RailRow.tsx:509`); there is no `.sb-row`
class, no `data-ref`, and no `.project-header[data-project-key]`. A **project**
row has no testid at all: reach it by the `role="treeitem"` element containing
its name, or by its `+` button's accessible name `New session in <project name>`
(`RailRow.tsx:602-609`, `IconButton` puts `label` on `aria-label`).

**The `aria-expanded="undefined"` bug this card used to document is gone, and
structurally cannot return.** The old sidebar computed
`String(model.expanded.has(k) || p.default_expanded)` and rendered the literal
string `"undefined"` when both operands were falsy/absent. Today the Tree widget
writes `aria-expanded={branchHasChildren ? expanded : undefined}` with
`expanded = node.expanded === true` (`widgets/tree/index.tsx:195,204`) — a real
JSX boolean, which React renders as `"true"`/`"false"` and *omits entirely* when
`undefined` — and `projectNodes` resolves the value through `?? false` before it
ever gets there (`railNodes.ts:271`, `:107`). Step 3 keeps checking it, as a
regression guard rather than a known-bug note.

## Pre-state

- A freshly built `serf-hub` + daemon on an isolated `$HOME` and a kernel-
  assigned port — the Setup checklist in `docs/agentic-testing.md`. Never a
  real hub. Build the frontend (`make build-web`) before the hub.
- Browser authenticated to the test hub, on its own Chrome profile (see the
  runbook's "Claim your own Chrome instance first").
- A project A holding at least one **ended** session, so it renders collapsed:
  a project auto-expands only while `rollup_live>0 || rollup_attn>0`
  (`internal/hubcore/tree.go:946`), which becomes `default_expanded` on the
  wire (`web_api_tree.go:925`, `omitempty`, so it is simply absent when false).
- A second, brand-new working directory for the live session in step 5, and a
  real credentialed model for it — the notification has to come from a genuine
  turn, not a synthetic poke.

## Steps

Step 1 is browser-free. Steps 2-7 need a browser: the state under test lives
entirely in the client bundle, so there is no REST-level counterpart to assert.

1. Spawn a session in `$A` (`POST /api/spawn`), let its first turn finish, then
   `POST /api/sessions/local:$SID_A/shutdown`. Confirm over
   `GET /api/tree` that `$A`'s project entry has no `default_expanded` field
   (or `false`) and that you have its `key` and its server-canonical
   `working_dir` — read both back from the response, never from your shell
   variable (see Sharp edges).
2. Navigate to `/auth?token=$TOKEN&next=/`. Confirm `$A`'s project row is
   present and **collapsed**: its `role="treeitem"` carries
   `aria-expanded="false"` and its session's `[data-session-ref]` row is absent
   from the DOM.
3. Read the attribute itself, not just its truthiness:
   ```javascript
   Array.from(document.querySelectorAll('[role="treeitem"]'), (el) => ({
     text: el.textContent?.slice(0, 40),
     expanded: el.getAttribute("aria-expanded"),   // "true" | "false" | null
     hasAttr: el.hasAttribute("aria-expanded"),
   }))
   ```
4. Click `$A`'s row (or its chevron, `[data-testid="rail-chevron"]`, scoped
   inside that row) to expand it. Confirm the session row now renders and
   `localStorage["serf.rail.expanded.v1"]` parses to an object with
   `"projectnode:<A key>": true` (`railExpansion.ts:19`, id scheme
   `railNodes.ts:209-211`).
5. **Arm a refetch counter, then cause live activity elsewhere.** There is no
   `seq` field to read any more, so count the store's own fetches
   (`fetchTree` calls `fetch("/api/tree", …)`, `stores/tree.ts:186-190`):
   ```javascript
   (() => {
     const orig = window.fetch;
     window.__treeFetches = 0;
     window.fetch = (...args) => {
       if (String(args[0]).startsWith("/api/tree")) window.__treeFetches++;
       return orig.apply(window, args);
     };
     return { port: location.port, armed: true };
   })()
   ```
   Then, from the shell, spawn a **live** session in the second working
   directory against a real model and let it run a real turn.
6. Wait ~5s (the 250ms debounce plus the socket round trip and the turn's own
   first status flip), then re-read: `window.__treeFetches`, the same
   `aria-expanded` probe from step 3, and whether `$A`'s session row is still
   in the DOM with the same title.
7. **Reload check** (the capability the old sidebar didn't have): hard-navigate
   to `/auth?token=$TOKEN&next=/` again and re-run step 3's probe without
   touching anything.

## Expected

- **Step 2/3**: `$A`'s treeitem reports `aria-expanded="false"` — the string
  `"false"`, not `"undefined"`, and not a missing attribute (it has children,
  so `branchHasChildren` is true and the attribute is written). Falsify: the
  literal string `"undefined"` anywhere in that probe's output. That would mean
  someone reintroduced string-coerced ARIA into `widgets/tree`; file it, it is
  an invalid ARIA value screen readers reject.
- **Step 4**: the session row appears, the treeitem flips to
  `aria-expanded="true"`, and the localStorage blob gains
  `"projectnode:<A key>": true`. Falsify: the row appears but nothing is
  persisted — the toggle bypassed `setExpanded` and will not survive step 7.
- **Step 6 (the core assertion)**: `window.__treeFetches` is **> 0** (a refetch
  genuinely ran — without this the whole step is an idle-DOM false pass), the
  new live session's row is visible under the `Live` heading (independent proof
  the new tree landed), **and** `$A` is still expanded with the same session
  title and `aria-expanded="true"`. Falsify: `$A` collapsed, or its row count
  dropped to zero, or a title reverted to a stale value — the refetch dropped
  the client's expand bookkeeping and the regression is back.
- **Step 7**: `$A` is still expanded on a cold page load, from
  `serf.rail.expanded.v1` alone. Falsify: it comes back collapsed — the write
  in step 4 is not being read at mount (`loadExpansion` is the lazy `useState`
  initializer, `Rail.tsx:164`).

## Cleanup

- `POST /api/sessions/local:<SID>/shutdown` for every session you spawned.
- Kill the hub by the PID you captured; `rm -rf` the run directory and the
  scratch working directories (Cleanup recipe in `docs/agentic-testing.md`).
- The `window.fetch` wrapper from step 5 dies with the tab; a reload (step 7)
  already clears it, so re-arm if you want to re-run step 6.

## Sharp edges

- **`mktemp -d` on macOS gives `/var/folders/…` but the server reports
  `/private/var/folders/…`.** Project working dirs are symlink-resolved
  (`identifier.ResolveProject`) before they ever reach `/api/tree`, so match
  projects by the `working_dir` the server hands back, not by your shell
  variable, or the key lookup silently comes up empty.
- **This is a client-state card; there is no browser-free half.** The expand
  map never leaves the browser — `/api/tree` carries only the server's
  `default_expanded` hint, and asserting on that would test a different thing
  entirely. A controller without Chrome can run step 1 and nothing else.
- **A project with no children gets no `aria-expanded` attribute at all**
  (`widgets/tree/index.tsx:204` passes `undefined` when `branchHasChildren` is
  false, and React omits it). That is correct, not a failure of step 3 —
  distinguish "attribute absent" from "attribute is the string `undefined`",
  which is why the probe reports `hasAttr` separately.
- **Don't try to force a refetch by hand.** There is no
  `window.SerfSidebar.refresh()`; the only triggers are the four notification
  methods in `REFRESH_NOTIFICATIONS` and the rail's own mount effect. Causing
  real live activity is the point of step 5 anyway — a hand-poked refetch would
  not exercise the notification path this card guards.
- The unit tests already cover the remount case in isolation
  (`shell/rail/Rail.test.tsx`, the expansion-persistence and project-expansion
  describes at `:748-848`). This card's value is the assembled system: a real
  socket notification, a real refetch, a real second project.
- Use a dedicated Chrome profile. The hub's auth cookie is not port-scoped, so
  two hubs sharing one profile clobber each other's session and render a
  spurious unauthorized page that has nothing to do with the rail. Keep the
  `location.port` assertion inside every `eval` regardless.
