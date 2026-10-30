import "fake-indexeddb/auto";
import type {
  NavigationReadParams,
  NavigationReadResponse,
  NavigationSessionSummary,
  NavigationSnapshot,
} from "@evener/appwire-client";
import {
  keyID,
  normalizedGraphFromSnapshot,
  type ResourceKey,
  type ResourceState,
} from "@evener/appwire-client/state/navigation";
import { currentRead } from "@evener/appwire-client/testing/currentTranscript";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, render, screen } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeEach, expect, test } from "vitest";
import { startBackgroundTranscripts } from "../../stores/backgroundTranscripts";
import { connectionStore } from "../../stores/connection";
import { setProductionEntryCacheForTests } from "../../stores/entryTranscriptRoutes";
import { initNavigation, navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { SessionEntryCache } from "../../stores/sessionEntryCache";
import { resetThreadsStoreForTests } from "../../stores/threads";
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
function resource(key: Extract<ResourceKey, { kind: "section" }>, refs: string[], remaining: number): ResourceState {
  const data = { generation_id: "g1", revision: 1, sessions: refs.map(row), remaining, truncated: remaining > 0 };
  const params: NavigationReadParams = {
    resource: "section",
    section: key.section,
    offset: key.offset,
    limit: key.limit,
    representationVersion: 3,
  };
  return {
    key,
    data,
    normalized: {
      key,
      graph: normalizedGraphFromSnapshot(wireSnapshot(params, data).data as NavigationSnapshot),
      version: { generationId: "g1", revision: 1, etag: "e" },
      presence: "present",
    },
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

let cache: SessionEntryCache | undefined;

beforeEach(() => {
  resetNavigationStoreForTests();
  resetThreadsStoreForTests();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  cache = undefined;
});
afterEach(() => {
  cleanup();
  resetNavigationStoreForTests();
  resetThreadsStoreForTests();
  cache?.close();
  cache = undefined;
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

test("Rail keeps a previously loaded later visible page and Load more through background discovery and reconnect", async () => {
  const world = { generation: "generation_test", revision: 1 };
  const pages = new Map<number, NavigationSessionSummary[]>([
    [0, [row("live-001"), row("live-002")]],
    [2, [row("live-003"), row("live-004")]],
    [4, [row("live-005"), row("live-006")]],
    [6, [row("live-007"), row("live-008")]],
  ]);
  const remainingFor = (offset: number) => (offset === 6 ? 0 : 4);

  function navigationRead(params: NavigationReadParams): NavigationReadResponse {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        {
          generation_id: world.generation,
          revision: world.revision,
          sources: [],
          attentionSummary: { needsYou: 0, error: 0, working: 0 },
          sections: { live: { count: 8 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
          catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
        },
        `"manifest-${world.revision}"`,
        world.revision,
        world.generation,
      );
    }
    if (params.resource === "section" && params.section === "live") {
      const offset = params.offset ?? 0;
      const remaining = remainingFor(offset);
      return wireSnapshot(
        params,
        { sessions: pages.get(offset) ?? [], remaining, truncated: remaining > 0 },
        `"live-${offset}-${world.revision}"`,
        world.revision,
        world.generation,
      );
    }
    return wireSnapshot(
      params,
      { sessions: [], remaining: 0, truncated: false },
      `"${params.resource}"`,
      1,
      world.generation,
    );
  }

  cache = new SessionEntryCache({
    indexedDB: new IDBFactory(),
    keyRange: IDBKeyRange,
    databaseName: `rail-bg-${crypto.randomUUID()}`,
  });
  setProductionEntryCacheForTests(cache);

  const client = new FakeClient("ready");
  client.on("evener/navigation/read", navigationRead);
  client.on("thread/read", (params) => ({
    ...currentRead(params.ref ?? "live-001"),
    requestGeneration: (params as { requestGeneration?: number }).requestGeneration ?? 1,
  }));
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  initNavigation(client, {
    version: 1,
    generationId: world.generation,
    sequence: 0,
    readVersions: [3],
  });

  // Foreground-load page zero and a LATER page, as a user's Load more would:
  // offset 4 becomes a visible loaded later page.
  await navigationStore.getState().loadSection("live", 0);
  await navigationStore.getState().loadSection("live", 4);

  const owner = startBackgroundTranscripts(navigationStore);
  await owner.settled();
  const acquired = client.calls
    .filter((call) => call.method === "thread/read")
    .map((call) => (call.params as { ref: string }).ref);
  expect(new Set(acquired)).toEqual(new Set([...pages.values()].flatMap((page) => page.map((session) => session.ref))));
  expect(client.calls.every((call) => call.method === "evener/navigation/read" || call.method === "thread/read")).toBe(
    true,
  );

  render(<Rail />, {
    wrapper: ({ children }) => <ClientProvider client={client}>{children}</ClientProvider>,
  });

  // Background discovery loaded the hidden middle page (offset 2) without
  // expanding the rail, and the previously loaded later visible page and its
  // Load more boundary remain rendered.
  expect(screen.getByText("live-005")).toBeTruthy();
  expect(screen.getByText("live-006")).toBeTruthy();
  expect(screen.queryByText("live-003")).toBeNull();
  expect(screen.queryByText("live-004")).toBeNull();
  expect(screen.queryByText("live-007")).toBeNull();
  expect(screen.queryByText("live-008")).toBeNull();
  expect(screen.getByText("+4 older")).toBeTruthy();

  // Reconnect: a fresh generation re-boots navigation and the owner rediscovers.
  world.generation = "generation_replacement";
  world.revision = 1;
  await act(async () => {
    client.emitStateChange("reconnecting");
    client.emitReady({
      serverInfo: { name: "fake", version: "1" },
      protocolVersion: "evener-appwire-v6",
      sourceId: "fake",
      features: {} as never,
      navigation: { version: 1, generationId: world.generation, sequence: 0, readVersions: [3] },
    });
    await owner.settled();
  });

  expect(screen.getByText("live-005")).toBeTruthy();
  expect(screen.getByText("live-006")).toBeTruthy();
  expect(screen.queryByText("live-003")).toBeNull();
  expect(screen.getByText("+4 older")).toBeTruthy();

  await act(async () => {
    await owner.dispose();
  });
});
