// The title channel: document.title = an optional attention-count prefix +
// the focused pane's title. Navigation summaries are bounded and cached; this
// module never expands a project merely to render chrome.

import { navigationSummaryFor, resolveThreadName } from "../panes/session/threadTitle";
import type { AttentionSummary } from "../protocol/types.gen";
import { paneFor } from "../shell/paneRegistry";
import { workspaceStore } from "../shell/workspace";
import { threadsStore } from "../stores/threads";

export function baseTitle(): string {
  const { panes, focusedPaneId } = workspaceStore.getState();
  const pane = panes.find((p) => p.id === focusedPaneId);
  if (!pane) return "evener hub";
  try {
    const ctx = {
      threadName: (ref: string) => resolveThreadName(threadsStore.getState().threads, navigationSummaryFor(ref), ref),
    };
    const paneTitle = paneFor(pane.type).title(pane.params, ctx);
    return paneTitle ? `${paneTitle} · evener hub` : "evener hub";
  } catch {
    return "evener hub";
  }
}

export function formatTitle(base: string, summary: AttentionSummary | null, titleOn: boolean): string {
  if (!titleOn) return base;
  const count = summary ? summary.needsYou + summary.error : 0;
  return count > 0 ? `(${count}) ${base}` : base;
}

export function applyTitle(titleOn: boolean, summary: AttentionSummary | null): void {
  document.title = formatTitle(baseTitle(), summary, titleOn);
}
