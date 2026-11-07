import { workspaceStore } from "../../../shell/workspace";
import { forgetConsumer, rememberConsumer, type TranscriptPaging } from "./transcriptPaging";
import type { TranscriptReadView } from "./transcriptReadView";

// Only a removed workspace predecessor transfers browse intent to a replacement.
// Standalone readers never inspect or mutate workspace membership.
export function adoptRemovedConsumers(entry: TranscriptPaging, viewId: string, view?: TranscriptReadView): void {
  if (view?.role === "cascade") return;
  const panes = workspaceStore.getState().panes;
  const paneId = view?.paneId ?? viewId;
  const current = panes.find((pane) => pane.id === paneId && (pane.params as { ref?: unknown })?.ref === entry.ref);
  if (!current || (current.type !== "session" && current.type !== "transcript")) return;
  for (const [id, membership] of entry.paneConsumers) {
    if (id === viewId || membership.view?.alive || membership.view?.role === "cascade" || membership.type === undefined)
      continue;
    const stillOpen = panes.some(
      (pane) =>
        pane.id === membership.paneId &&
        pane.type === membership.type &&
        (pane.params as { ref?: unknown })?.ref === entry.ref,
    );
    if (stillOpen && (membership.view?.alive !== false || membership.paneId !== paneId)) continue;
    const paging = rememberConsumer(entry, viewId, view);
    void paging.request(viewId).catch(() => {});
    paging.cancel(id);
    forgetConsumer(entry, id);
  }
}

export function cancelRemovedConsumers(entry: TranscriptPaging, ref: string): void {
  const panes = workspaceStore.getState().panes;
  for (const [consumer, membership] of entry.paneConsumers) {
    if (membership.view?.alive || membership.type === undefined) continue;
    if (
      panes.some(
        (pane) =>
          pane.id === membership.paneId &&
          pane.type === membership.type &&
          (pane.params as { ref?: unknown })?.ref === ref,
      )
    )
      continue;
    entry.paging.cancel(consumer);
    forgetConsumer(entry, consumer);
  }
}
