import { keyID, type ResourceKey, type ResourceState } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { ClientProvider } from "../clientContext";
import { Rail } from "./Rail";

const row = (ref: string) => ({
  ref,
  host_id: "local",
  session_id: ref,
  title: ref,
  project: "",
  state: "running",
  kind: "session",
  live: true,
  children: [],
});
function resource(key: ResourceKey, refs: string[], remaining: number): ResourceState {
  return {
    key,
    data: { generation_id: "g1", revision: 1, sessions: refs.map(row), remaining, truncated: remaining > 0 },
    loadedRevision: 1,
    targetRevision: 1,
    forceToken: 0,
    etag: `"${key.kind}-${key.kind === "section" ? key.offset : 0}"`,
    loading: false,
    stale: false,
    error: null,
    generationID: "g1",
  };
}

beforeEach(() => resetNavigationStoreForTests());
afterEach(() => {
  cleanup();
  resetNavigationStoreForTests();
});

test("Rail shows only foreground Live pages and keeps the visible Load more boundary", () => {
  const first = { kind: "section", section: "live", offset: 0, limit: 2 } as const;
  const hidden = { kind: "section", section: "live", offset: 4, limit: 2 } as const;
  navigationStore.setState({
    mode: "v3",
    clientGenerationID: "g1",
    manifest: {
      key: { kind: "manifest" },
      data: {
        generation_id: "g1",
        revision: 1,
        sources: [],
        attentionSummary: { needsYou: 0, error: 0, working: 0 },
        sections: { live: { count: 6 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
        catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
      },
      loadedRevision: 1,
      targetRevision: 1,
      forceToken: 0,
      etag: "manifest",
      loading: false,
      stale: false,
      error: null,
      generationID: "g1",
    },
    resources: new Map([
      [keyID(first), resource(first, ["live-001", "live-002"], 4)],
      [keyID(hidden), resource(hidden, ["live-005", "live-006"], 0)],
    ]),
    visibleResourceKeys: new Set([keyID(first)]),
  });

  render(<Rail />, {
    wrapper: ({ children }) => <ClientProvider client={new FakeClient()}>{children}</ClientProvider>,
  });

  expect(screen.getByText("live-001")).toBeTruthy();
  expect(screen.getByText("live-002")).toBeTruthy();
  expect(screen.queryByText("live-005")).toBeNull();
  expect(screen.queryByText("live-006")).toBeNull();
  expect(screen.getByText("+4 older")).toBeTruthy();
});
