import {
  conversationPaneLifetime,
  existingConversationPaneLifetime,
  type PaneLifetime,
} from "../../shell/paneLifetime";
import { navigate, observeRouteHistory, paneToURL, refParam, routeHistoryEntryId } from "../../shell/routing";
import { type OpenPaneRecord, onWorkspaceRestore, presentedPanes, workspaceStore } from "../../shell/workspace";
import { type SessionZoomParams, zoomIntentOf } from "./intent";

const bindings = new WeakMap<PaneLifetime, PaneLifetime | null>();
let returnedOrigin: PaneLifetime | null = null;
let returnedOriginURL: string | null = null;
let returnedOriginEntryId: string | null = null;
let publishingReturnURL = false;

export function recordCascadeReturnedOrigin(origin: OpenPaneRecord | null): void {
  const next = origin ? conversationPaneLifetime(origin) : null;
  if (next !== returnedOrigin) {
    returnedOriginURL = null;
    returnedOriginEntryId = null;
  }
  returnedOrigin = next;
}

export function isCascadeReturnedOrigin(pane: OpenPaneRecord | undefined): boolean {
  if (!pane || !returnedOrigin?.alive) return false;
  return existingConversationPaneLifetime(pane) === returnedOrigin;
}

export function publishCascadeReturnURL(pane: OpenPaneRecord, url: string, opts?: { replace?: boolean }): void {
  if (!isCascadeReturnedOrigin(pane)) {
    navigate(url, opts);
    return;
  }
  publishingReturnURL = true;
  try {
    returnedOriginURL = url;
    navigate(url, opts);
    returnedOriginEntryId = typeof window !== "undefined" ? routeHistoryEntryId(window.history.state) : null;
  } finally {
    publishingReturnURL = false;
  }
}

export function cascadeReturnedOriginForURL(pathname: string): OpenPaneRecord | null {
  if (returnedOrigin === null || !returnedOrigin.alive || returnedOriginURL === null) return null;
  if (pathname !== returnedOriginURL) return null;
  const currentEntryId = typeof window !== "undefined" ? routeHistoryEntryId(window.history.state) : null;
  if (currentEntryId !== returnedOriginEntryId) return null;
  return (
    presentedPanes(workspaceStore.getState().panes).find(
      (pane) => existingConversationPaneLifetime(pane) === returnedOrigin,
    ) ?? null
  );
}

function isConversation(pane: OpenPaneRecord): boolean {
  const ref = refParam(pane.params);
  return (
    ref !== null &&
    !ref.startsWith("job:") &&
    (pane.type === "session" || pane.type === "transcript" || zoomIntentOf(pane) !== null)
  );
}

function restoredOrigin(params: SessionZoomParams, panes: readonly OpenPaneRecord[]): OpenPaneRecord | null {
  const locator = params.inspection?.origin;
  if (!locator) return null;
  return (
    panes.find(
      (pane) => pane.id === locator.paneId && pane.type === locator.type && refParam(pane.params) === locator.ref,
    ) ?? null
  );
}

function boundOrigin(lifetime: PaneLifetime): OpenPaneRecord | null {
  const originLifetime = bindings.get(lifetime);
  if (!originLifetime?.alive) return null;
  return (
    workspaceStore
      .getState()
      .panes.find((pane) => isConversation(pane) && conversationPaneLifetime(pane) === originLifetime) ?? null
  );
}

export function recordCascadeOrigin(inspector: OpenPaneRecord, origin: OpenPaneRecord | null): void {
  const panes = workspaceStore.getState().panes;
  const params = zoomIntentOf(inspector);
  if (!params?.inspection || !panes.includes(inspector)) return;
  const valid = origin !== null && panes.includes(origin) && restoredOrigin(params, panes) === origin;
  bindings.set(conversationPaneLifetime(inspector), valid ? conversationPaneLifetime(origin) : null);
  if (!valid && params.inspection.origin !== null) {
    workspaceStore.getState().retypePane(inspector, "sessionZoom", {
      ...params,
      inspection: { origin: null },
    });
  }
}

export function cascadeOrigin(inspector: OpenPaneRecord): OpenPaneRecord | null {
  if (!workspaceStore.getState().panes.includes(inspector)) return null;
  if (!zoomIntentOf(inspector)?.inspection) return null;
  return boundOrigin(conversationPaneLifetime(inspector));
}

export function cascadeOriginRef(inspector: OpenPaneRecord): string | null {
  const origin = cascadeOrigin(inspector);
  return origin === null ? null : refParam(origin.params);
}

export function associatedCascade(origin: OpenPaneRecord): OpenPaneRecord | null {
  if (!workspaceStore.getState().panes.includes(origin)) return null;
  return (
    workspaceStore.getState().panes.find((pane) => pane.slot === "secondary" && cascadeOrigin(pane) === origin) ?? null
  );
}

// IDs are resolved only against a validated, unpublished restore generation.
// Live queries follow the bound lifetime, so an identical replacement cannot
// inherit the old inspector's Return destination.
onWorkspaceRestore((panes) => {
  returnedOrigin = null;
  returnedOriginURL = null;
  returnedOriginEntryId = null;
  for (const pane of panes) {
    const params = zoomIntentOf(pane);
    if (!params?.inspection) continue;
    const origin = restoredOrigin(params, panes);
    bindings.set(conversationPaneLifetime(pane), origin ? conversationPaneLifetime(origin) : null);
    if (!origin) pane.params = { ...params, inspection: { origin: null } };
  }
});

observeRouteHistory((_prevState, nextState, _prevURL, nextURL) => {
  if (publishingReturnURL) {
    returnedOriginEntryId = routeHistoryEntryId(nextState);
    return;
  }
  if (
    returnedOriginURL !== null &&
    (nextURL !== returnedOriginURL || routeHistoryEntryId(nextState) !== returnedOriginEntryId)
  ) {
    returnedOriginURL = null;
    returnedOriginEntryId = null;
  }
});

workspaceStore.subscribe((state, previous) => {
  if (returnedOrigin !== null) {
    const originPresent =
      returnedOrigin.alive &&
      presentedPanes(state.panes).some((pane) => existingConversationPaneLifetime(pane) === returnedOrigin);
    if (!originPresent) {
      const ownsCurrentReturnEntry =
        returnedOriginURL !== null &&
        typeof window !== "undefined" &&
        window.location.pathname === returnedOriginURL &&
        routeHistoryEntryId(window.history.state) === returnedOriginEntryId;
      returnedOrigin = null;
      returnedOriginURL = null;
      returnedOriginEntryId = null;
      if (ownsCurrentReturnEntry) {
        const survivingMain = state.panes.find((pane) => pane.slot === "main");
        const fallbackURL = survivingMain ? paneToURL(survivingMain.type, survivingMain.params) : null;
        navigate(fallbackURL ?? "/", { replace: true });
      }
    } else if (returnedOriginURL === null) {
      const focused = state.panes.find((pane) => pane.id === state.focusedPaneId);
      if (!focused || existingConversationPaneLifetime(focused) !== returnedOrigin) {
        returnedOrigin = null;
      }
    }
  }
  if (state.panes === previous.panes) return;
  for (const pane of state.panes) {
    const params = zoomIntentOf(pane);
    if (!params?.inspection) continue;
    const lifetime = conversationPaneLifetime(pane);
    if (!bindings.has(lifetime) || boundOrigin(lifetime) !== null) continue;
    bindings.set(lifetime, null);
    if (params.inspection.origin !== null) {
      workspaceStore.getState().retypePane(pane, "sessionZoom", {
        ...params,
        inspection: { origin: null },
      });
    }
  }
});
