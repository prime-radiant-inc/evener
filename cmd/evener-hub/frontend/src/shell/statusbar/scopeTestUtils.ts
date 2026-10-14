import type { NavigationSessionSummary } from "@evener/appwire-client";
import { keyID, type ResourceState } from "@evener/appwire-client/state/navigation";
import { navigationStore } from "../../stores/navigation/store";
import { workspaceStore } from "../workspace";

export function summaryOf(
  partial: Partial<NavigationSessionSummary> & { ref: string; title: string },
): NavigationSessionSummary {
  return {
    host_id: "local",
    session_id: partial.ref,
    project: "evener",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...partial,
  } as NavigationSessionSummary;
}
export function installFocusedScope(ref: string, row?: NavigationSessionSummary): void {
  workspaceStore.setState({
    panes: [{ id: "selected", type: "transcript", params: { ref }, slot: "main" }],
    focusedPaneId: "selected",
  });
  const key = { kind: "section", section: "live", offset: 0, limit: 50 } as const;
  const resource: ResourceState = {
    key,
    data: { sessions: row ? [row] : [] },
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: "tag",
    loading: false,
    stale: false,
    error: null,
    generationID: "g1",
  };
  // The display selectors only surface a loaded section row once the store has
  // registered its key as visible (visibleResourceKeys, set by the ordinary
  // load()/loadSection() foreground path). Seeding `resources` directly must
  // register the same visibility, exactly as the product's foreground load
  // does; otherwise every seeded row is treated as background-only and hidden.
  const id = keyID(key);
  navigationStore.setState({ mode: "v3", resources: new Map([[id, resource]]), visibleResourceKeys: new Set([id]) });
}
