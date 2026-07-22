// The title channel: document.title = an optional attention-count prefix +
// the focused pane's title.
//
// The base title is the ONE honest divergence from the legacy's server-side
// "<section> · serf hub" (floor §3.2): the SPA has panes, not server-rendered
// sections, so the base comes from the focused pane via workspaceStore +
// paneRegistry.title() — the same threadName-backed ctx DockHost builds for a
// pane's tab (DockHost.tsx:210). The "· serf hub" suffix is preserved, so the
// shape stays exactly the legacy's, only the left-hand source differs.
import { paneFor } from "../shell/paneRegistry";
import { workspaceStore } from "../shell/workspace";
import { threadsStore } from "../stores/threads";
import type { AttentionSummary } from "../stores/tree";

// Base title from the focused pane, or bare "serf hub" with none focused. A
// pane whose title() throws (an unregistered type — never expected, since
// only registered types are ever opened) degrades to the bare form rather
// than throwing out of a store-subscription callback.
export function baseTitle(): string {
  const { panes, focusedPaneId } = workspaceStore.getState();
  const pane = panes.find((p) => p.id === focusedPaneId);
  if (!pane) return "serf hub";
  try {
    const ctx = { threadName: (ref: string) => threadsStore.getState().threads.get(ref)?.name };
    const paneTitle = paneFor(pane.type).title(pane.params, ctx);
    return paneTitle ? `${paneTitle} · serf hub` : "serf hub";
  } catch {
    return "serf hub";
  }
}

// Prepends "(<needsYou + error>) " only when that sum > 0 AND the title pref
// is on. The pref is read from the shipped prefs store by the caller — this
// function NEVER defaults it on (floor §3.1 all-OFF; the top cross-wave trap).
export function formatTitle(base: string, summary: AttentionSummary | null, titleOn: boolean): string {
  if (!titleOn) return base;
  const count = summary ? summary.needsYou + summary.error : 0;
  return count > 0 ? `(${count}) ${base}` : base;
}

export function applyTitle(titleOn: boolean, summary: AttentionSummary | null): void {
  document.title = formatTitle(baseTitle(), summary, titleOn);
}
