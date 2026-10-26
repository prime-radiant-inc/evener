import type { NavigationSessionSummary } from "@evener/appwire-client";
import { keyID, NAVIGATION_SECTION_LIMIT, type ResourceKey } from "@evener/appwire-client/state/navigation";
import { parseZoomParams } from "../panes/zoom/intent";
import {
  selectNeedsYouRows,
  selectNextSectionOffset,
  selectSectionCanDemand,
  selectSectionRemaining,
} from "../stores/navigation/selectors";
import { navigationStore } from "../stores/navigation/store";
import { threadsStore } from "../stores/threads";
import { openAttentionSession } from "./attentionHistory";
import { chromeStore } from "./chromeStore";
import { focusedSessionPaneId, focusedSessionRef } from "./focusedSession";
import { paletteStore } from "./palette/paletteController";
import { existingConversationPaneLifetime } from "./paneLifetime";
import { needsYouRefs, nextNeedsYouRef } from "./rail/needsYouCycle";
import { observeRouteHistory } from "./routing";
import { workspaceStore } from "./workspace";

export type NeedsYouChoice = {
  summary: NavigationSessionSummary;
  selectable: boolean;
  refreshState: "refreshing" | "failed" | null;
};
export type NeedsYouSnapshot = {
  choices: readonly NeedsYouChoice[];
  nextRef: string | null;
  canLoadMore: boolean;
  canLoadInitialPage: boolean;
  loading: boolean;
  readFailed: boolean;
  blocked: boolean;
  modalOpenCount: number;
};
export type NeedsYouController = {
  getSnapshot(): NeedsYouSnapshot;
  /** True once dispose released this controller's resources. */
  readonly disposed: boolean;
  subscribe(listener: () => void): () => void;
  next(): Promise<void>;
  select(ref: string): void;
  loadMore(): Promise<void>;
  cancel(): void;
  dispose(): void;
};

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

function focusedReaderRef(pane: { type: string; params: unknown } | undefined): string | null {
  if (!pane) return null;
  if (pane.type === "session") {
    const ref = (pane.params as { ref?: unknown }).ref;
    return typeof ref === "string" && ref !== "" ? ref : null;
  }
  if (pane.type === "sessionZoom") return parseZoomParams(pane.params)?.ref ?? null;
  return null;
}

function sourceIdentity(): string {
  const state = workspaceStore.getState();
  const pane = state.panes.find((item) => item.id === state.focusedPaneId);
  // The exact reader behind that pane, not just its record: a cascade can rebind
  // its leaf to a different backing session behind identical params (a resume),
  // and a pending Next must not act for the reader that was replaced.
  const conversation =
    pane !== undefined && (pane.type === "session" || pane.type === "sessionZoom" || pane.type === "transcript");
  const lifetime = pane && conversation ? existingConversationPaneLifetime(pane) : null;
  const readerRef = focusedReaderRef(pane);
  const boundSession = readerRef && lifetime ? (lifetime.resolvedSessions.get(readerRef) ?? null) : null;
  // Clear replaces a thread's fused identity in place - same pane, ref, route and
  // navigation generation - so a pending Next has to see that too.
  const model = readerRef ? threadsStore.getState().threads.get(readerRef) : undefined;
  const threadInstance = model?.instanceId ?? model?.threadId ?? null;
  return JSON.stringify({
    pane,
    ref: focusedSessionRef(),
    paneId: focusedSessionPaneId(),
    boundSession,
    threadInstance,
  });
}

export type NeedsYouControllerOptions = {
  /**
   * Build the controller without observing anything yet; the first subscriber
   * starts observation. The provider uses this because it builds the controller
   * during render so the shell's content commits on the first pass, and React
   * discards a render under StrictMode - an observing controller built there
   * could never be released.
   */
  observeOnSubscribe?: boolean;
};

/**
 * Whether the needs-you section may ask for another page.
 *
 * Both the snapshot's `canLoadMore` and `runNext`'s one demand ask this, so a
 * settled queue cannot keep offering an action that spends a read. The settled
 * page, remaining count, failed boundary and manifest fallback are the shared
 * navigation selector's rule; the populated retry is needs-you's own.
 */
function needsYouPages(state: ReturnType<typeof navigationStore.getState>) {
  return [...state.resources.values()].filter(
    (resource) => resource.key.kind === "section" && resource.key.section === "needs_you",
  );
}

function needsYouCanDemand(state: ReturnType<typeof navigationStore.getState>): boolean {
  const retryingPopulatedPage = needsYouPages(state).some(
    (page) => page.loading && ((page.data as { sessions?: unknown[] } | null)?.sessions?.length ?? 0) > 0,
  );
  return retryingPopulatedPage || selectSectionCanDemand("needs_you", state);
}

// The next needs-you session after the focused one. While more of the queue is
// unloaded, the last loaded row has no known successor: wrapping to the first
// row would skip rows not yet read, so the answer is null until they load.
function resolveNextRef(state: ReturnType<typeof navigationStore.getState>): string | null {
  const refs = [...new Set(needsYouRefs(selectNeedsYouRows(state)))];
  const focused = focusedSessionRef();
  if (selectSectionRemaining("needs_you", state) > 0 && focused === refs.at(-1)) return null;
  return nextNeedsYouRef(refs, focused);
}

export function createNeedsYouController(options: NeedsYouControllerOptions = {}): NeedsYouController {
  let actionSerial = 0;
  let disposed = false;
  let started = false;
  let modalOpen = typeof document !== "undefined" && !!document.querySelector('[aria-modal="true"]');
  let modalOpenCount = 0;
  let hasReadManifest = !!navigationStore.getState().manifest?.data;
  let source = sourceIdentity();
  let routeSerial = 0;
  const listeners = new Set<() => void>();
  const read = (): NeedsYouSnapshot => {
    const state = navigationStore.getState();
    const rows = selectNeedsYouRows(state);
    const uniqueRows = [...new Map(rows.map((row) => [row.ref, row])).values()];
    const choices = uniqueRows.map((summary): NeedsYouChoice => {
      const pageKey = rowPageKey(summary.ref, state);
      const page = pageKey ? state.resources.get(pageKey) : undefined;
      let refreshState: NeedsYouChoice["refreshState"] = null;
      if (page?.error) refreshState = "failed";
      else if (page?.stale) refreshState = "refreshing";
      return {
        summary,
        selectable: !!page && refreshState === null,
        refreshState,
      };
    });
    const nextRef = resolveNextRef(state);
    const pages = needsYouPages(state);
    const failed = pages.some((page) => !!page.error);
    const loading = pages.some((page) => page.loading);
    const canLoadMore = needsYouCanDemand(state);
    const canLoadInitialPage = !hasReadManifest && state.mode === "v3" && state.manifest?.loading === true;
    return Object.freeze({
      choices: Object.freeze(choices.map((choice) => Object.freeze(choice))),
      nextRef:
        nextRef && choices.some((choice) => choice.summary.ref === nextRef && choice.selectable) ? nextRef : null,
      canLoadMore,
      canLoadInitialPage,
      loading,
      readFailed: failed || !!state.manifest?.error || state.mode === "error",
      blocked: modalOpen || paletteStore.getState().open,
      modalOpenCount,
    });
  };
  let snapshot = read();
  const emit = (notifyCounterOnly = false) => {
    if (disposed) return;
    const next = read();
    const samePublicState =
      next.choices.length === snapshot.choices.length &&
      next.choices.every((choice, index) => {
        const prior = snapshot.choices[index];
        return (
          prior?.summary === choice.summary &&
          prior.selectable === choice.selectable &&
          prior.refreshState === choice.refreshState
        );
      }) &&
      next.nextRef === snapshot.nextRef &&
      next.canLoadMore === snapshot.canLoadMore &&
      next.canLoadInitialPage === snapshot.canLoadInitialPage &&
      next.loading === snapshot.loading &&
      next.readFailed === snapshot.readFailed &&
      next.blocked === snapshot.blocked;
    const counterChanged = next.modalOpenCount !== snapshot.modalOpenCount;
    if (samePublicState && !counterChanged) return;
    snapshot = next;
    // A counter-only update refreshes the snapshot so a later read sees the new
    // count, but it notifies only when the palette needs to learn about it now:
    // a modal opened while another modal remained open. The palette also
    // compares the count in attempt, so the close that unblocks it cannot fire
    // a superseded intent.
    if (samePublicState && !notifyCounterOnly) return;
    for (const listener of listeners) listener();
  };
  const cancel = () => {
    actionSerial += 1;
  };
  let unsubscribeNavigation = () => {};
  let unsubscribeWorkspace = () => {};
  let unsubscribePalette = () => {};
  let unsubscribeRoute = () => {};
  let unsubscribeChrome = () => {};
  const onPopState = () => {
    routeSerial += 1;
    cancel();
  };
  // A popout is a separate window with its own constructors, so an
  // opener-realm `instanceof Element` rejects every node it creates. The node
  // type is the same number in every realm.
  const containsModal = (node: Node): boolean =>
    node.nodeType === 1 &&
    ((node as Element).matches('[aria-modal="true"]') || !!(node as Element).querySelector('[aria-modal="true"]'));
  // MutationObserver reports an attribute record's old value but not its new
  // one, so a single record cannot prove which way aria-modal went. A record
  // whose old value was not "true" opened a modal when the element now reads
  // "true" (it stayed open) or when a later record for the same target has
  // oldValue "true" (it opened and closed again before this callback ran). A
  // close alone never counts: its old value is "true", and a redundant write
  // after a close has no later oldValue-"true" record to point at.
  const attributeOpenedModal = (record: MutationRecord, batch: readonly MutationRecord[]): boolean => {
    if (record.oldValue === "true") return false;
    if ((record.target as Element).getAttribute("aria-modal") === "true") return true;
    return batch
      .slice(batch.indexOf(record) + 1)
      .some((later) => later.type === "attributes" && later.target === record.target && later.oldValue === "true");
  };
  // Every document the shell might open a modal in: the opener's, and the
  // focused reader's own (a Dockview popout is a separate window, and Dockview
  // warns us of it through PaneScaffold's published document).
  const observedDocuments = new Set<Document>();
  const modalIn = (doc: Document): boolean => !!doc.querySelector('[aria-modal="true"]');
  const anyModalOpen = (): boolean => {
    for (const doc of observedDocuments) if (modalIn(doc)) return true;
    return false;
  };
  const modalObserver = new MutationObserver((records) => {
    const next = anyModalOpen();
    // A UI mutation that opens a modal is a new modal-open transition. This is
    // true even when another modal is already open (the palette itself is one),
    // and even when the new modal closes again before this callback runs. Only
    // opens advance the counter, so closing a dialog that was open before a
    // pending palette selection cannot consume that selection's intent.
    const modalOpened = records.some((record) => {
      if (record.type === "attributes") {
        return attributeOpenedModal(record, records);
      }
      return [...record.addedNodes].some(containsModal);
    });
    const modalTransition = records.some((record) => {
      if (record.type === "attributes") {
        return record.oldValue === "true" || (record.target as Element).getAttribute("aria-modal") === "true";
      }
      return [...record.addedNodes, ...record.removedNodes].some(containsModal);
    });
    if (modalTransition || next !== modalOpen) {
      if (modalOpened) modalOpenCount += 1;
      const wasOpen = modalOpen;
      modalOpen = next;
      cancel();
      emit(wasOpen && next);
    }
  });
  // Creating a controller only allocates state. Observation starts on the first
  // subscriber, which is an effect (React subscribes there), so the provider can
  // build the controller while it renders and still release every resource the
  // moment observation has begun. A controller built by a render React discards
  // never observes anything and has nothing to release.
  const focusedPaneDocument = (): Document | null => {
    const focused = workspaceStore.getState().focusedPaneId;
    return focused ? (chromeStore.getState().paneDocuments.get(focused) ?? null) : null;
  };
  const observeModalDocument = (doc: Document | null): void => {
    if (!doc || observedDocuments.has(doc) || !doc.documentElement) return;
    observedDocuments.add(doc);
    modalObserver.observe(doc.documentElement, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeOldValue: true,
      attributeFilter: ["aria-modal"],
    });
  };
  // A reader adopted into a popout reports a new document; start watching it and
  // treat a modal already open there as the modal transition it is.
  // The observed set is the opener plus the FOCUSED reader's document, no more:
  // a dialog in an unfocused popout must not keep attention navigation blocked
  // in the window the reader is actually looking at. MutationObserver can only
  // drop every target at once, so a changed set re-observes from scratch.
  const syncModalDocuments = () => {
    const wanted = new Set<Document>();
    if (typeof document !== "undefined") wanted.add(document);
    const focused = focusedPaneDocument();
    if (focused?.documentElement) wanted.add(focused);
    const unchanged = wanted.size === observedDocuments.size && [...wanted].every((doc) => observedDocuments.has(doc));
    if (unchanged) return;
    // MutationObserver can only drop every target at once. A first sync has
    // nothing to drop, so it must not spend the observer's one disconnect:
    // disposal is the only release the accounting tests see.
    const wasWatching = observedDocuments.size > 0;
    observedDocuments.clear();
    if (wasWatching) modalObserver.disconnect();
    for (const doc of wanted) observeModalDocument(doc);
    const next = anyModalOpen();
    if (next === modalOpen) return;
    modalOpen = next;
    cancel();
    emit();
  };
  const start = () => {
    if (started || disposed) return;
    started = true;
    syncModalDocuments();
    modalOpen = anyModalOpen();
    if (navigationStore.getState().manifest?.data) hasReadManifest = true;
    unsubscribeNavigation = navigationStore.subscribe((state, previous) => {
      if (state.manifest?.data) hasReadManifest = true;
      if (state.clientGenerationID !== previous.clientGenerationID) cancel();
      emit();
    });
    unsubscribeWorkspace = workspaceStore.subscribe(() => {
      const next = sourceIdentity();
      if (next !== source) {
        source = next;
        cancel();
      }
      // Focus can move to a popout whose document was already published, which
      // writes nothing new to chromeStore: follow the focused pane here too.
      syncModalDocuments();
      emit();
    });
    unsubscribePalette = paletteStore.subscribe((state, previous) => {
      if (state.open !== previous.open) cancel();
      emit();
    });
    unsubscribeRoute = observeRouteHistory(() => {
      routeSerial += 1;
      cancel();
    });
    window.addEventListener("popstate", onPopState);
    unsubscribeChrome = chromeStore.subscribe(() => syncModalDocuments());
    snapshot = read();
  };
  if (!options.observeOnSubscribe) start();

  const demand = async (serial: number): Promise<void> => {
    const state = navigationStore.getState();
    // Navigation reads only exist once the store has a client. An activation
    // before the first connection (an eager Mod+J, a restored shortcut) has
    // nothing to demand and no page to fail on.
    if (state.mode === "unknown") return;
    // A failed page keeps the rows it already had, so an offset computed from
    // them would skip past the failure and leave its choices unusable. Retry
    // the earliest failed visible page at its own offset and limit first; only
    // an all-clean set advances the page.
    const failed = [...state.resources.values()]
      .filter(
        (resource) =>
          resource.key.kind === "section" &&
          resource.key.section === "needs_you" &&
          resource.error !== null &&
          state.visibleResourceKeys.has(keyID(resource.key)),
      )
      .sort((a, b) => (a.key.kind === "section" && b.key.kind === "section" ? a.key.offset - b.key.offset : 0))[0];
    const failedPage = failed?.key.kind === "section" ? failed.key : null;
    const offset = failedPage ? failedPage.offset : selectNextSectionOffset("needs_you", state);
    const limit = failedPage ? failedPage.limit : NAVIGATION_SECTION_LIMIT;
    try {
      await state.loadSection("needs_you", offset, limit);
    } catch {
      // loadSection records the failure on the resource itself. Nothing here
      // may reject: activations are void-ed by their callers.
    }
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
    let nextRef = resolveNextRef(state);
    if (
      !nextRef ||
      !rows.some((row) => {
        if (row.ref !== nextRef) return false;
        const pageKey = rowPageKey(row.ref, state);
        const page = pageKey ? state.resources.get(pageKey) : undefined;
        return !!page && !page.error && !page.stale;
      })
    ) {
      if (state.mode === "unknown" || state.mode === "error" || needsYouCanDemand(state)) {
        await demand(serial);
        state = navigationStore.getState();
        if (disposed || serial !== actionSerial) return;
        if (sourceIdentity() !== sourceAtStart || routeSerial !== routeAtStart || snapshot.blocked) return;
        nextRef = resolveNextRef(state);
        if (!nextRef && selectSectionRemaining("needs_you", state) > 0) return;
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
    // The action's own availability decides: a complete queue must not spend a
    // page read just because a stale control was pressed.
    if (disposed || snapshot.blocked || !snapshot.canLoadMore) return;
    cancel();
    const serial = actionSerial;
    await demand(serial);
  };
  return {
    getSnapshot: () => snapshot,
    get disposed() {
      return disposed;
    },
    subscribe(listener) {
      start();
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
      unsubscribeChrome();
      window.removeEventListener("popstate", onPopState);
      modalObserver.disconnect();
      listeners.clear();
    },
  };
}
