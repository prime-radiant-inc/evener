// Test-only fixtures for the needs-you navigation section. Production code
// does not import this module.
import type { NavigationSessionSummary } from "@evener/appwire-client";
import { keyID, type ResourceState } from "@evener/appwire-client/state/navigation";
import { navigationStore } from "../stores/navigation/store";

export const NEEDS_YOU_PAGE_LIMIT = 50;

export function needsYouRow(ref: string, title = ref): NavigationSessionSummary {
  return {
    ref,
    host_id: "local",
    session_id: ref,
    title,
    project: "Proj",
    state: "awaiting",
    kind: "session",
    live: true,
    children: [],
  };
}

function needsYouPage(
  offset: number,
  rows: NavigationSessionSummary[],
  patch: Partial<ResourceState<unknown>>,
): ResourceState<unknown> {
  return {
    key: { kind: "section", section: "needs_you", offset, limit: NEEDS_YOU_PAGE_LIMIT },
    data: { generation_id: "g", revision: 1, sessions: rows, remaining: 0, truncated: false },
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: `"test-${offset}"`,
    loading: false,
    stale: false,
    error: null,
    generationID: "g",
    ...patch,
  };
}

export function installNeedsYouPage(
  offset: number,
  rows: NavigationSessionSummary[],
  patch: Partial<ResourceState<unknown>> = {},
): void {
  const page = needsYouPage(offset, rows, patch);
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(page.key), page);
  const visibleResourceKeys = new Set(navigationStore.getState().visibleResourceKeys);
  visibleResourceKeys.add(keyID(page.key));
  navigationStore.setState({ mode: "v3", resources, visibleResourceKeys });
}

export function updateNeedsYouPage(offset: number, patch: Partial<ResourceState<unknown>>): void {
  const key = keyID({ kind: "section", section: "needs_you", offset, limit: NEEDS_YOU_PAGE_LIMIT });
  const page = navigationStore.getState().resources.get(key);
  if (!page) throw new Error("seeded needs-you page missing");
  const resources = new Map(navigationStore.getState().resources);
  resources.set(key, { ...page, ...patch });
  navigationStore.setState({ resources });
}
