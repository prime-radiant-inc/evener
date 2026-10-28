import { HistoryPaging } from "@evener/appwire-client";
import { workspaceStore } from "../../../shell/workspace";
import { olderTranscriptPageKey, threadsStore } from "../../../stores/threads";
import type { TranscriptReadView } from "./transcriptReadView";

export interface TranscriptReader {
  readonly id: string;
  readonly alive: boolean;
  readonly readable: boolean;
  readonly membership: "workspace" | "standalone";
  subscribe(listener: () => void): () => void;
}

// A browser page serves one hub. Socket replacements retain demand for the
// same ref and session binding; a new binding owns a separate history window.
export interface TranscriptPaging {
  ref: string;
  binding: string | undefined;
  paging: HistoryPaging;
  paneConsumers: Map<string, ConsumerMembership>;
}
export interface ConsumerMembership {
  paneId: string;
  type?: "session" | "transcript";
  view?: TranscriptReadView;
  unsubscribe?: () => void;
}
export const pagingByRef = new Map<string, TranscriptPaging>();

// Tests reset the thread store between hub fixtures.
export function resetTranscriptPagingForTests(): void {
  for (const entry of pagingByRef.values()) {
    for (const id of entry.paneConsumers.keys()) {
      entry.paging.cancel(id);
      forgetConsumer(entry, id);
    }
  }
  pagingByRef.clear();
}

export function matchesBinding(
  entry: TranscriptPaging | undefined,
  binding: string | undefined,
): entry is TranscriptPaging {
  return entry !== undefined && (binding === undefined || entry.binding === undefined || entry.binding === binding);
}

export function createPaging(ref: string, binding: string | undefined): TranscriptPaging {
  const entry: TranscriptPaging = {
    ref,
    binding,
    paneConsumers: new Map(),
    paging: new HistoryPaging(
      () => {
        const current = threadsStore.getState().threads.get(ref);
        if (current === undefined || (current.instanceId ?? current.threadId) !== entry.binding) return undefined;
        // Local bodies may remain even when the server has no continuation.
        // The store's tagged scheduling key never goes onto the wire.
        return olderTranscriptPageKey(ref);
      },
      () => threadsStore.getState().loadOlderTurns(ref),
      () => {
        if (pagingByRef.get(ref) === entry) pagingByRef.delete(ref);
      },
    ),
  };
  return entry;
}

export function forgetConsumer(entry: TranscriptPaging, id: string): void {
  entry.paneConsumers.get(id)?.unsubscribe?.();
  entry.paneConsumers.delete(id);
}

export function rememberConsumer(
  entry: TranscriptPaging,
  viewId: string,
  view?: TranscriptReadView,
  standalone?: TranscriptReader,
): HistoryPaging {
  // Unresolved reads and permanent failures both retain consumers. Only a
  // successful settlement or explicit cancellation can retire their ownership.
  const state = entry.paging.getSnapshot();
  if (!state.pending && state.error === null) {
    for (const id of entry.paneConsumers.keys()) forgetConsumer(entry, id);
  }
  if (entry.paneConsumers.get(viewId)?.view !== view || !entry.paneConsumers.has(viewId)) {
    forgetConsumer(entry, viewId);
    const paneId = view?.paneId ?? viewId;
    const pane = workspaceStore.getState().panes.find((pane) => pane.id === paneId);
    const membership: ConsumerMembership = {
      paneId,
      type: pane?.type === "session" || pane?.type === "transcript" ? pane.type : undefined,
      view,
    };
    if (standalone) {
      membership.unsubscribe = standalone.subscribe(() => {
        if (standalone.alive) return;
        entry.paging.cancel(viewId);
        forgetConsumer(entry, viewId);
      });
    } else if (view) {
      membership.unsubscribe = view.subscribe(() => {
        if (view.alive || view.role !== "cascade") return;
        entry.paging.cancel(viewId);
        forgetConsumer(entry, viewId);
      });
    }
    entry.paneConsumers.set(viewId, membership);
  }
  return entry.paging;
}
