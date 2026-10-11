# Cascade pane redesign: the origin never renders

Status: ruled by Jesse on 2026-10-10 after seeing the origin-collapse fix live;
supersedes [the origin-collapse rule](2026-10-10-cascade-origin-collapse.md).

## Authority and scope

Jesse ruled on the secondary cascade pane, verbatim intent:

1. "Origin never appears in the pane. but also, I think we want to make the
   subpanes scroll internally, rather than the pane that hosts all the spines
   and subagents scroll as a unit."
2. "No title."
3. "Remove it; spines are the breadcrumb."
4. "Per-column, moved to column bottoms."

The origin conversation — the center/primary pane's session the inspector was
opened from — renders nowhere in the cascade: no readable column and no spine.
This supersedes the 2026-10-10 origin-collapse rule (a 52px spine for the
duplicate origin) with full removal. Placement, ancestry, peek, history,
activity, input-delivery, Return, saved-intent and previous-layout contracts
otherwise remain in force. The evergreen authority after implementation is
[Session activity](../../product/session-activity.md#automatic-agent-cascade).

## Rules

- **Rendered scopes.** With a live origin association
  (`cascadeOrigin(pane)`), the origin's scope (`requestedRef ===
  refParam(origin.params)`) is removed from the rendered scopes entirely.
  Cascades without a live origin render every scope, unchanged. The readable
  set stays over the full path: the leaf and its immediate parent, with the
  parent dropped when it is the removed origin. Level 1 renders one column and
  zero spines; level 2 renders two columns and zero spines; level 3 renders
  one spine and two columns, and deeper levels spine only non-origin ancestors.
- **No title.** The pane header renders no "Agent cascade" heading. The
  desktop tab label keeps the registered selected-conversation title, and the
  mobile top bar publishes the selected conversation's name.
- **No path nav.** The horizontally scrolling `Agent path` nav is deleted.
  The compact spines are the clickable breadcrumb (`popAgentCascade`), the
  pane footer's scope crumbs mirror the rendered scopes, and the "Return to
  previous view" button still closes the pane to its source.
- **Controls at column bottoms.** Agents/Jobs/Watches/Tasks stay per-column
  (`ScopeActivityControls` in `CascadeColumn`) but render below the transcript
  content instead of in the header.
- **Internal scrolling.** The pane body hosting the spines and columns never
  scrolls as a unit: it renders flush (`PaneScaffold bodyScroll={false}`),
  and each column's content area scrolls internally with its header and
  controls fixed within the column. Spines stay visible.

## Decisions

- **Fill at depth 1.** A lone rendered column fills the pane (width `100%`)
  rather than pinning at 440px, so the first drill reads as the subagent's own
  conversation pane. Deeper levels keep the fixed widths — 52px spines, 400px
  parent, 440px leaf — with the existing horizontal track scroll.
- **Pop to the origin is unreachable.** With the origin filtered out, no
  surface can select it: `popAgentCascade` refuses a live origin's ref, and
  the footer and sidebar breadcrumbs drop the origin crumb. The guard's two
  pop-to-root journeys (the "real root pop supplies a read-only cascade
  reader" reflow leg and the "pop to root before restoring real branch" leg)
  were deleted rather than reproduced; the shared-reader reflow proof now
  rides the depth-1 leaf column itself.
- **The original-conversation Open shortcut is unreachable.** The reload
  journey's root-pop + original-conversation Open block is gone. Opening a
  conversation that is already open as an ordinary pane keeps its existing
  branch semantics — focus it, do not close the inspector — and the journey
  closes the inspector through Return instead.
- **The root's own collection through its own pane.** The guard's root Agents
  peek (inside the deleted root spine) moved to the source-scoped activity
  sidebar, which renders the same `AgentsTab` over the same shared collection:
  the paging-cursor, branch-truncation and reconnect-extent proofs are
  unchanged in kind. The compact spine-status proof rides the first rendered
  ancestor; the fixture's only busy conversation is the origin itself, which
  no longer renders, so the running-indicator family stays covered by the
  jsdom suites.

## Mobile implications

Removing the nav also removes the mobile raw-ID nav surface tracked in
issue #4212 (the header-surface case remains). A saved phone cascade keeps its
selected read-only leaf, its usable Return and its single-column shape, now
with no nav and no pane title; the mobile top bar carries the selected
conversation's name. Ordinary phone Agents entry remains `openTranscript`.

## First review round (2026-10-11)

Three medium findings from the combined review, resolved:

- The pane body's flush layout was order-fragile: a shared CSS module via
  `composes` lost deterministically to the scaffold's base `.body` rules and
  its mobile media rule (which restored padding on phones), and the
  cross-module composes broke the layoutguard harness, which loads per-case
  CSS sources. The flush variant is now a compound `.body.bodyNoScroll` rule
  that wins regardless of rule order, and the guard asserts the pane body's
  overflow, content growth and padding at desktop and phone widths.
- A layout saved before the redesign can carry the inspector popped to its
  origin; restoring it binds the origin and leaves an empty track. Ruled by
  Jesse on 2026-10-11: leave it empty — no compatibility recovery.
- The unrendered origin's identity change went unobserved, so a rebound
  origin left stale descendants in the pane. The cascade now holds a
  hidden observer on the live origin's activity subscription and reconciles
  its binding; a rebound retires the stale descendants and the emptied pane
  keeps the Return path.

## Second review round (2026-10-11)

Two follow-on medium findings from later combined reviews, resolved:

- The origin observer watched only the origin's own activity summary: with
  the child's ancestry already carrying the origin's identity, an origin
  replaced before that summary resolved registered the replacement as the
  first identity and never retired the stale descendants. The observer now
  falls back to the origin scope's ancestry identity from the unfiltered
  path while the summary is pending.
- After a nested origin's replacement popped the selection onto the
  never-rendering origin, the phone rendered the nearest ancestor column
  under the selected conversation's title. The phone's single column is now
  the selected leaf itself (`phoneScopes`): when the removed origin is that
  leaf, the phone keeps only the Return path, matching the ruled
  origin-at-root shape at every depth. The desktop track is unchanged.

## Proof

- jsdom (`Zoom.test.tsx`, `intent.test.ts`): the origin-free shapes at depths
  1–3, the no-live-origin two-column shape, the origin-close recovery, the
  rebound live origin retiring stale descendants (including before its first
  summary resolves and with the phone keeping only the Return path when the
  pop lands on a nested origin), the lone-column fill style,
  the controls-below-content order, and the no-nav/no-title chrome; the
  DockHost, AppShell and ActivitySidebar journeys pin the restored and
  drilled shapes.
- The real cascadeguard: first drill asserts one pane-filling column and zero
  spines; six edges settle at four spines over `refs[1..6]`; the pane body
  owns no unit scrollbar and renders flush at desktop and phone widths; the
  branch truncation and paging proofs run through the source-scoped sidebar;
  phone legs assert the single-column shape with no nav or title.

Verification commands from the repository root:

```sh
make test-web
make build-web
go test -tags browserguard ./cmd/evener-hub -run '^TestAgentCascadeBrowser$' -count=1 -v
```
