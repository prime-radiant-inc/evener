import type { NavigationSessionSummary } from "@evener/appwire-client";
import { keyID, type ResourceKey } from "@evener/appwire-client/state/navigation";
import {
  selectNeedsYouCount,
  selectNeedsYouRows,
  selectNextSectionOffset,
  selectSectionRemaining,
} from "../stores/navigation/selectors";
import { navigationStore } from "../stores/navigation/store";
import { openAttentionSession } from "./attentionHistory";
import { focusedSessionPaneId, focusedSessionRef } from "./focusedSession";
import { paletteStore } from "./palette/paletteController";
import { needsYouRefs, nextNeedsYouRef } from "./rail/needsYouCycle";
import { observeRouteHistory } from "./routing";
import { workspaceStore } from "./workspace";

export type NeedsYouChoice = { summary: NavigationSessionSummary; selectable: boolean };
export type NeedsYouSnapshot = {
  choices: readonly NeedsYouChoice[];
  nextRef: string | null;
  canLoadMore: boolean;
  loading: boolean;
  readFailed: boolean;
  blocked: boolean;
};
export type NeedsYouController = {
  getSnapshot(): NeedsYouSnapshot;
  subscribe(listener: () => void): () => void;
  next(): Promise<void>;
  select(ref: string): void;
  loadMore(): Promise<void>;
  cancel(): void;
  dispose(): void;
};

const PAGE_LIMIT = 50;
const rowPageKey = (ref: string, state: ReturnType<typeof navigationStore.getState>): string | null => {
  const pages = [...state.resources.values()]
    .filter((resource) => resource.key.kind === "section" && resource.key.section === "needs_you")
    .sort((a, b) => (a.key.kind === "section" && b.key.kind === "section" ? a.key.offset - b.key.offset : 0));
  for (const page of pages) {
    const rows = (page.data as { sessions?: NavigationSessionSummary[] } | null)?.sessions ?? [];
    if (rows.some((row) => row.ref === ref)) return keyID(page.key as ResourceKey);
  }
  return null;
};

function sourceIdentity(): string {
  const state = workspaceStore.getState();
  const pane = state.panes.find((item) => item.id === state.focusedPaneId);
  return JSON.stringify({ pane, ref: focusedSessionRef(), paneId: focusedSessionPaneId() });
}

export function createNeedsYouController(): NeedsYouController {
  let actionSerial = 0;
  let disposed = false;
  let modalOpen = typeof document !== "undefined" && !!document.querySelector('[aria-modal="true"]');
  let source = sourceIdentity();
  let routeSerial = 0;
  const listeners = new Set<() => void>();
  const read = (): NeedsYouSnapshot => {
    const state = navigationStore.getState();
    const rows = selectNeedsYouRows(state);
    const uniqueRows = [...new Map(rows.map((row) => [row.ref, row])).values()];
    const choices = uniqueRows.map((summary) => {
      const pageKey = rowPageKey(summary.ref, state);
      const page = pageKey ? state.resources.get(pageKey) : undefined;
      return { summary, selectable: !!page && !page.error && !page.stale };
    });
    const remaining = selectSectionRemaining("needs_you", state);
    const hasManifest = !!state.manifest?.data;
    const count = selectNeedsYouCount(state);
    const refs = needsYouRefs(uniqueRows);
    let nextRef = nextNeedsYouRef(refs, focusedSessionRef());
    if (remaining > 0 && focusedSessionRef() === refs.at(-1)) nextRef = null;
    const failed = [...state.resources.values()].some(
      (resource) => resource.key.kind === "section" && resource.key.section === "needs_you" && !!resource.error,
    );
    const loading = [...state.resources.values()].some(
      (resource) => resource.key.kind === "section" && resource.key.section === "needs_you" && resource.loading,
    );
    const canLoadMore = !hasManifest || failed || remaining > 0 || count > uniqueRows.length;
    return Object.freeze({
      choices: Object.freeze(choices.map((choice) => Object.freeze(choice))),
      nextRef:
        nextRef && choices.some((choice) => choice.summary.ref === nextRef && choice.selectable) ? nextRef : null,
      canLoadMore,
      loading,
      readFailed: failed || state.mode === "error",
      blocked: modalOpen || paletteStore.getState().open,
    });
  };
  let snapshot = read();
  const emit = () => {
    if (disposed) return;
    const next = read();
    if (
      next.choices.length === snapshot.choices.length &&
      next.choices.every((choice, index) => {
        const prior = snapshot.choices[index];
        return prior?.summary === choice.summary && prior.selectable === choice.selectable;
      }) &&
      next.nextRef === snapshot.nextRef &&
      next.canLoadMore === snapshot.canLoadMore &&
      next.loading === snapshot.loading &&
      next.readFailed === snapshot.readFailed &&
      next.blocked === snapshot.blocked
    )
      return;
    snapshot = next;
    for (const listener of listeners) listener();
  };
  const cancel = () => {
    actionSerial += 1;
  };
  const unsubscribeNavigation = navigationStore.subscribe((state, previous) => {
    if (state.clientGenerationID !== previous.clientGenerationID) cancel();
    emit();
  });
  const unsubscribeWorkspace = workspaceStore.subscribe(() => {
    const next = sourceIdentity();
    if (next !== source) {
      source = next;
      cancel();
    }
    emit();
  });
  const unsubscribePalette = paletteStore.subscribe((state, previous) => {
    if (state.open !== previous.open) cancel();
    emit();
  });
  const unsubscribeRoute = observeRouteHistory(() => {
    routeSerial += 1;
    cancel();
  });
  const onPopState = () => {
    routeSerial += 1;
    cancel();
  };
  window.addEventListener("popstate", onPopState);
  const containsModal = (node: Node): boolean =>
    node instanceof Element && (node.matches('[aria-modal="true"]') || !!node.querySelector('[aria-modal="true"]'));
  const modalObserver = new MutationObserver((records) => {
    const next = !!document.querySelector('[aria-modal="true"]');
    const modalTransition = records.some((record) => {
      if (record.type === "attributes") {
        return record.oldValue === "true" || (record.target as Element).getAttribute("aria-modal") === "true";
      }
      return [...record.addedNodes, ...record.removedNodes].some(containsModal);
    });
    if (modalTransition || next !== modalOpen) {
      modalOpen = next;
      cancel();
      emit();
    }
  });
  modalObserver.observe(document.documentElement, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeOldValue: true,
    attributeFilter: ["aria-modal"],
  });

  const demand = async (serial: number): Promise<void> => {
    const state = navigationStore.getState();
    const offset = selectNextSectionOffset("needs_you", state);
    await state.loadSection("needs_you", offset, PAGE_LIMIT);
    if (disposed || serial !== actionSerial) return;
  };
  const runNext = async (): Promise<void> => {
    if (disposed || snapshot.blocked) return;
    cancel();
    const serial = actionSerial;
    const sourceAtStart = sourceIdentity();
    const routeAtStart = routeSerial;
    let state = navigationStore.getState();
    const rows = selectNeedsYouRows(state);
    let nextRef = nextNeedsYouRef(needsYouRefs(rows), focusedSessionRef());
    const remaining = selectSectionRemaining("needs_you", state);
    const uniqueRefs = needsYouRefs(rows);
    if (remaining > 0 && focusedSessionRef() === uniqueRefs.at(-1)) nextRef = null;
    if (
      !nextRef ||
      !rows.some((row) => {
        if (row.ref !== nextRef) return false;
        const pageKey = rowPageKey(row.ref, state);
        const page = pageKey ? state.resources.get(pageKey) : undefined;
        return !!page && !page.error && !page.stale;
      })
    ) {
      if (
        state.mode === "unknown" ||
        state.mode === "error" ||
        remaining > 0 ||
        selectNeedsYouCount(state) > rows.length ||
        !state.manifest?.data ||
        [...state.resources.values()].some((r) => r.key.kind === "section" && r.key.section === "needs_you" && r.error)
      ) {
        await demand(serial);
        state = navigationStore.getState();
        if (disposed || serial !== actionSerial) return;
        if (sourceIdentity() !== sourceAtStart || routeSerial !== routeAtStart || snapshot.blocked) return;
        const freshRows = selectNeedsYouRows(state);
        nextRef = nextNeedsYouRef(needsYouRefs(freshRows), focusedSessionRef());
        const freshRefs = needsYouRefs(freshRows);
        const freshRemaining = selectSectionRemaining("needs_you", state);
        if (freshRemaining > 0 && focusedSessionRef() === freshRefs.at(-1)) nextRef = null;
        if (!nextRef && freshRemaining > 0) return;
      }
    }
    if (!nextRef || sourceIdentity() !== sourceAtStart || routeSerial !== routeAtStart || snapshot.blocked) return;
    const pageKey = rowPageKey(nextRef, state);
    const page = pageKey ? state.resources.get(pageKey) : undefined;
    if (!page || page.error || page.stale || !selectNeedsYouRows(state).some((row) => row.ref === nextRef)) return;
    cancel();
    openAttentionSession(nextRef);
  };
  const loadMore = async () => {
    if (disposed || snapshot.blocked) return;
    cancel();
    const serial = actionSerial;
    await demand(serial);
  };
  return {
    getSnapshot: () => snapshot,
    subscribe(listener) {
      listeners.add(listener);
      return () => listeners.delete(listener);
    },
    next: runNext,
    select(ref) {
      if (disposed || snapshot.blocked) return;
      cancel();
      const serial = actionSerial;
      const state = navigationStore.getState();
      const pageKey = rowPageKey(ref, state);
      const page = pageKey ? state.resources.get(pageKey) : undefined;
      if (
        serial === actionSerial &&
        page &&
        !page.error &&
        !page.stale &&
        selectNeedsYouRows(state).some((row) => row.ref === ref) &&
        ref !== focusedSessionRef()
      ) {
        openAttentionSession(ref);
      }
    },
    loadMore,
    cancel,
    dispose() {
      if (disposed) return;
      disposed = true;
      cancel();
      unsubscribeNavigation();
      unsubscribeWorkspace();
      unsubscribePalette();
      unsubscribeRoute();
      window.removeEventListener("popstate", onPopState);
      modalObserver.disconnect();
      listeners.clear();
    },
  };
}
