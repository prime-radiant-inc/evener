import { conversationPaneLifetime, type PaneLifetime } from "../../shell/paneLifetime";
import { refParam } from "../../shell/routing";
import { type OpenPaneRecord, onWorkspaceRestore, workspaceStore } from "../../shell/workspace";
import { parseZoomParams, type SessionZoomParams } from "./intent";

const bindings = new WeakMap<PaneLifetime, PaneLifetime | null>();

function isConversation(pane: OpenPaneRecord): boolean {
  const ref = refParam(pane.params);
  return (
    ref !== null &&
    !ref.startsWith("job:") &&
    (pane.type === "session" ||
      pane.type === "transcript" ||
      (pane.type === "sessionZoom" && parseZoomParams(pane.params) !== null))
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
  const params = inspector.type === "sessionZoom" ? parseZoomParams(inspector.params) : null;
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
  if (inspector.type !== "sessionZoom" || !parseZoomParams(inspector.params)?.inspection) return null;
  return boundOrigin(conversationPaneLifetime(inspector));
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
  for (const pane of panes) {
    const params = pane.type === "sessionZoom" ? parseZoomParams(pane.params) : null;
    if (!params?.inspection) continue;
    const origin = restoredOrigin(params, panes);
    bindings.set(conversationPaneLifetime(pane), origin ? conversationPaneLifetime(origin) : null);
    if (!origin) pane.params = { ...params, inspection: { origin: null } };
  }
});

workspaceStore.subscribe((state, previous) => {
  if (state.panes === previous.panes) return;
  for (const pane of state.panes) {
    const params = pane.type === "sessionZoom" ? parseZoomParams(pane.params) : null;
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
