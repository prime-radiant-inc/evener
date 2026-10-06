import { expect, test } from "vitest";
import { FakeClient } from "../../testing/fakeClient";
import { capability, completeSession, manifest, wireSnapshot } from "../../testing/navigation";
import { memoryNavigationPersistence } from "../../testing/navigationPersistence";
import { navigationInvalidatedNotification } from "../../testing/notifications";
import type { InitializeResponse, NavigationReadParams, NavigationReadResponse } from "../../types.gen";
import { NavigationRevalidator } from "./revalidator";
import { selectGlobalRows, selectLiveRows, selectNextSectionOffset, selectSectionRemaining } from "./selectors";
import { createNavigationStore } from "./store";
import { keyID } from "./types";

const generation = "generation_test";
const initialize = (generationId: string): InitializeResponse => ({
  serverInfo: { name: "fake", version: "1" },
  protocolVersion: "evener-appwire-v6",
  sourceId: "fake",
  features: {
    threadList: false,
    threadTurnsList: false,
    turnStart: false,
    turnSteer: false,
    threadClear: false,
    threadShutdown: false,
    forkFromTurn: false,
    tasks: false,
    transcriptList: false,
    modelList: false,
    directoryComplete: false,
    auth: false,
  },
  navigation: capability(generationId),
});
const row = (ref: string) => completeSession({ ref, title: ref, project: "alpha", live: true, state: "running" });
const manifestResponse = (params: NavigationReadParams) => wireSnapshot(params, manifest());
const pageResponse = (
  params: NavigationReadParams,
  refs: string[],
  remaining: number,
  revision = 1,
  gen = generation,
) =>
  wireSnapshot(
    params,
    { sessions: refs.map(row), remaining, truncated: remaining > 0 },
    `"${params.resource}-${params.offset}-${revision}"`,
    revision,
    gen,
  );
const liveParams = (offset: number, limit: number) => ({
  resource: "section" as const,
  section: "live" as const,
  offset,
  limit,
  representationVersion: 3 as const,
});

function makeStore(script: (params: NavigationReadParams) => NavigationReadResponse | Promise<NavigationReadResponse>) {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", script);
  const store = createNavigationStore({ persistence: memoryNavigationPersistence() });
  store.init(client, capability());
  return { client, store };
}

test("hidden Live page leaves visible rows and page boundary unchanged, then foreground adopts it", async () => {
  const calls: NavigationReadParams[] = [];
  const { client, store } = makeStore((params) => {
    calls.push(params);
    if (params.resource === "manifest") return manifestResponse(params);
    if (params.resource === "section" && params.section === "live") {
      if (params.offset === 0) return pageResponse(params, ["live-001", "live-002"], 4);
      if (params.offset === 2) return pageResponse(params, ["live-003", "live-004"], 2);
      if (params.offset === 4) return pageResponse(params, ["live-005", "live-006"], 0);
    }
    throw new Error(`unexpected navigation read ${JSON.stringify(params)}`);
  });
  const hidden = store.acquireBackgroundLivePages();
  await store.getState().loadSection("live", 0, 2);
  await store.getState().loadSection("live", 2, 2);
  const before = selectLiveRows(store.getState()).map((item) => item.ref);

  await hidden.load(4, 2);

  expect(before).toEqual(["live-001", "live-002", "live-003", "live-004"]);
  expect(selectLiveRows(store.getState()).map((item) => item.ref)).toEqual(before);
  expect(selectNextSectionOffset("live", store.getState())).toBe(4);
  expect(selectSectionRemaining("live", store.getState())).toBe(2);
  const callsBeforeForeground = calls.filter((params) => params.resource === "section" && params.offset === 4).length;
  await store.getState().loadSection("live", 4, 2);
  expect(calls.filter((params) => params.resource === "section" && params.offset === 4)).toHaveLength(
    callsBeforeForeground,
  );
  expect(selectLiveRows(store.getState()).map((item) => item.ref)).toEqual([
    "live-001",
    "live-002",
    "live-003",
    "live-004",
    "live-005",
    "live-006",
  ]);
  expect(selectNextSectionOffset("live", store.getState())).toBe(6);
  expect(selectSectionRemaining("live", store.getState())).toBe(0);
  hidden.release();
  store.reset();
  client.close();
});

test("a lease retired during refresh's force publication cannot start or republish a forced read", async () => {
  let revision = 1;
  // The forced response is held so a buggily-issued request can be settled
  // explicitly; a correct fix never enters it.
  const responseGate: { release: () => void } = { release: () => {} };
  let holdResponse = false;
  let entered = false;
  const calls: NavigationReadParams[] = [];
  const { client, store } = makeStore(async (params) => {
    calls.push(params);
    if (params.resource === "manifest") return manifestResponse(params);
    if (holdResponse) {
      entered = true;
      await new Promise<void>((resolve) => {
        responseGate.release = resolve;
      });
    }
    return pageResponse(params, ["live-005", "live-006"], 0, revision);
  });
  const key = { kind: "section", section: "live", offset: 4, limit: 2 } as const;
  const id = keyID(key);
  const hidden = store.acquireBackgroundLivePages();
  await hidden.load(4, 2);
  revision = 2;
  holdResponse = true;

  let released = false;
  const unsubscribe = store.subscribe((state) => {
    if (released) return;
    if (state.resources.get(id)?.stale) {
      released = true;
      hidden.release();
    }
  });
  let enteredObserved = false;
  let latePublished = false;
  try {
    await expect(hidden.refresh(4, 2)).rejects.toThrow("background navigation lease released");
  } finally {
    // Settle any request a bug did issue, and restore every owner, the shared
    // store and the client even when the refresh assertion failed.
    enteredObserved = entered;
    responseGate.release();
    await new Promise((resolve) => setTimeout(resolve, 0));
    latePublished = store.getState().resources.has(id);
    unsubscribe();
    hidden.release();
    store.reset();
    client.close();
  }
  // The sole lease retired synchronously during force's publication, so no
  // forced read may start afterwards, and none may republish later.
  expect(released).toBe(true);
  expect(enteredObserved).toBe(false);
  expect(latePublished).toBe(false);
});

test("two background leases deduplicate, and releasing one during a request preserves the other", async () => {
  let releaseRequest!: () => void;
  const blocked = new Promise<void>((resolve) => {
    releaseRequest = resolve;
  });
  let calls = 0;
  const { client, store } = makeStore(async (params) => {
    if (params.resource === "manifest") return manifestResponse(params);
    calls++;
    await blocked;
    return pageResponse(params, ["live-001", "live-002"], 4);
  });
  const first = store.acquireBackgroundLivePages();
  const second = store.acquireBackgroundLivePages();
  const one = first.load(0, 2);
  const two = second.load(0, 2);
  first.release();
  releaseRequest();
  await Promise.all([one, two]);
  expect(calls).toBe(1);
  expect(selectLiveRows(store.getState())).toEqual([]);
  expect(selectNextSectionOffset("live", store.getState())).toBe(0);
  second.release();
  store.reset();
  client.close();
});

test("final background lease release removes settled data before foreground reacquisition", async () => {
  const calls: NavigationReadParams[] = [];
  const { client, store } = makeStore((params) => {
    calls.push(params);
    return params.resource === "manifest"
      ? manifestResponse(params)
      : pageResponse(
          params,
          [
            calls.filter((call) => call.resource === "section" && call.offset === 4).length === 1
              ? "retired-background-row"
              : "fresh-background-row",
          ],
          0,
        );
  });
  const resourceKey = { kind: "section", section: "live", offset: 4, limit: 2 } as const;
  const id = keyID(resourceKey);
  const background = store.acquireBackgroundLivePages();
  await background.load(4, 2);
  expect(store.getState().resources.get(id)?.data).not.toBeNull();

  background.release();
  expect(store.getState().resources.has(id)).toBe(false);

  const visibleSnapshots: Array<string[] | null> = [];
  const unsubscribe = store.subscribe((state) => {
    if (state.visibleResourceKeys.has(id)) {
      const data = state.resources.get(id)?.data as { sessions?: Array<{ ref: string }> } | null | undefined;
      visibleSnapshots.push(data?.sessions?.map((session) => session.ref) ?? null);
    }
  });
  await store.getState().loadSection("live", 4, 2);
  unsubscribe();
  expect(visibleSnapshots.length).toBeGreaterThan(0);
  expect(visibleSnapshots.flat()).not.toContain("retired-background-row");
  expect(calls.filter((params) => params.resource === "section" && params.offset === 4)).toHaveLength(2);
  const shared = store.acquireBackgroundLivePages();
  await shared.load(4, 2);
  shared.release();
  expect(store.getState().resources.has(id)).toBe(true);
  store.reset();
  client.close();
});

for (const held of [false, true]) {
  for (const reacquireAs of ["foreground", "background"] as const) {
    test(`final release retires before synchronous ${reacquireAs} reacquisition (${held ? "held" : "settled"} old request)`, async () => {
      let finishOldRequest!: () => void;
      const oldRequestGate = new Promise<void>((resolve) => {
        finishOldRequest = resolve;
      });
      const calls: NavigationReadParams[] = [];
      const { client, store } = makeStore(async (params) => {
        calls.push(params);
        if (params.resource === "manifest") return manifestResponse(params);
        const pageRead = calls.filter((call) => call.resource === "section" && call.offset === 4).length;
        if (pageRead === 1) {
          if (held) await oldRequestGate;
          return pageResponse(params, ["retired-old-row"], 0, 1);
        }
        return pageResponse(params, ["owned-new-row"], 0, pageRead === 3 ? 2 : 1);
      });
      const resourceKey = { kind: "section", section: "live", offset: 4, limit: 2 } as const;
      const id = keyID(resourceKey);
      const background = store.acquireBackgroundLivePages();
      let loadingSeen!: () => void;
      const oldLoading = new Promise<void>((resolve) => {
        loadingSeen = resolve;
      });
      const unsubscribeLoading = store.subscribe((state) => {
        if (state.resources.get(id)?.loading) loadingSeen();
      });
      const oldRead = background.load(4, 2);
      if (held) {
        await oldLoading;
      } else {
        await oldRead;
      }
      unsubscribeLoading();

      let replacement: ReturnType<typeof store.acquireBackgroundLivePages> | undefined;
      let replacementRead: Promise<unknown> | undefined;
      let reacquired = false;
      const unsubscribeReacquire = store.subscribe((state) => {
        if (reacquired || state.resources.has(id)) return;
        reacquired = true;
        if (reacquireAs === "foreground") {
          replacementRead = store.getState().loadSection("live", 4, 2);
        } else {
          replacement = store.acquireBackgroundLivePages();
          replacementRead = replacement.load(4, 2);
        }
      });

      background.release();
      expect(reacquired).toBe(true);
      if (held) {
        finishOldRequest();
        await oldRead;
      }
      await replacementRead;
      unsubscribeReacquire();

      expect(store.getState().resources.get(id)?.data).toMatchObject({ sessions: [{ ref: "owned-new-row" }] });
      expect(store.getState().resources.get(id)?.loading).toBe(false);
      if (reacquireAs === "foreground") {
        expect(store.getState().visibleResourceKeys.has(id)).toBe(true);
        expect(selectLiveRows(store.getState()).map((item) => item.ref)).toContain("owned-new-row");
      } else {
        expect(store.getState().visibleResourceKeys.has(id)).toBe(false);
        expect(store.getState().resources.has(id)).toBe(true);
      }

      client.emitNotification(
        navigationInvalidatedNotification({
          generationId: generation,
          sequence: 1,
          targets: [{ kind: "section", section: "live", revision: 2 }],
        }),
      );
      const refreshed =
        reacquireAs === "foreground" ? await store.getState().loadSection("live", 4, 2) : await replacement!.load(4, 2);
      expect(refreshed.data).toMatchObject({ sessions: [{ ref: "owned-new-row" }] });
      expect(calls.filter((params) => params.resource === "section" && params.offset === 4)).toHaveLength(3);
      expect(store.getState().resources.get(id)?.data).toMatchObject({ sessions: [{ ref: "owned-new-row" }] });
      replacement?.release();
      store.reset();
      client.close();
    });
  }
}

test("final background lease release removes an in-flight page and fences its late response", async () => {
  let finishRequest!: () => void;
  const pending = new Promise<void>((resolve) => {
    finishRequest = resolve;
  });
  const { client, store } = makeStore(async (params) => {
    if (params.resource === "manifest") return manifestResponse(params);
    await pending;
    return pageResponse(params, ["late-background-row"], 0);
  });
  const resourceKey = { kind: "section", section: "live", offset: 4, limit: 2 } as const;
  const id = keyID(resourceKey);
  const background = store.acquireBackgroundLivePages();
  const request = background.load(4, 2);
  await Promise.resolve();
  expect(store.getState().resources.get(id)?.loading).toBe(true);

  background.release();
  expect(store.getState().resources.has(id)).toBe(false);
  finishRequest();
  await request;
  expect(store.getState().resources.has(id)).toBe(false);
  expect(selectLiveRows(store.getState())).toEqual([]);
  store.reset();
  client.close();
});

test("foreground adopts a failed hidden in-flight page and the next wait rejects without a duplicate read", async () => {
  let finishRequest!: () => void;
  const pending = new Promise<void>((resolve) => {
    finishRequest = resolve;
  });
  const calls: NavigationReadParams[] = [];
  const { client, store } = makeStore(async (params) => {
    calls.push(params);
    if (params.resource === "manifest") return manifestResponse(params);
    if (params.offset === 4) {
      await pending;
      throw new Error("hidden page unavailable");
    }
    return pageResponse(params, ["live-001", "live-002"], 4);
  });
  const background = store.acquireBackgroundLivePages();
  const hiddenRead = background.load(4, 2);
  await Promise.resolve();
  const foregroundRead = store.getState().loadSection("live", 4, 2);
  expect(calls.filter((params) => params.resource === "section" && params.offset === 4)).toHaveLength(1);

  finishRequest();
  const [hiddenResult, foregroundResult] = await Promise.all([hiddenRead, foregroundRead]);
  expect((hiddenResult.error as Error | null)?.message).toBe("hidden page unavailable");
  expect((foregroundResult.error as Error | null)?.message).toBe("hidden page unavailable");
  await expect(
    store.getState().awaitNavigationTargets([{ kind: "section", section: "live", revision: 2 }], generation),
  ).rejects.toThrow("target convergence failed");
  expect(calls.filter((params) => params.resource === "section" && params.offset === 4)).toHaveLength(1);
  background.release();
  store.reset();
  client.close();
});

test("a matching project resource holds foreground target convergence until its read settles", async () => {
  let finishProject!: () => void;
  const pending = new Promise<void>((resolve) => {
    finishProject = resolve;
  });
  const { client, store } = makeStore(async (params) => {
    if (params.resource === "manifest") return manifestResponse(params);
    if (params.resource === "project") {
      await pending;
      return wireSnapshot(
        params,
        {
          current: { sessions: [row("project-session")], remaining: 0 },
          recent: { sessions: [], remaining: 0 },
          archived: { sessions: [], remaining: 0 },
        },
        '"project-2"',
        2,
      );
    }
    throw new Error(`unexpected navigation read ${JSON.stringify(params)}`);
  });
  const projectLoad = store.getState().loadProject("project-key");
  await Promise.resolve();
  let settled = false;
  const convergence = store
    .getState()
    .awaitNavigationTargets([{ kind: "project", projectKey: "project-key", revision: 2 }], generation)
    .then(() => {
      settled = true;
    });
  await Promise.resolve();
  expect(settled).toBe(false);

  finishProject();
  await projectLoad;
  await convergence;
  expect(settled).toBe(true);
  store.reset();
  client.close();
});

test("a mutation waits for its forced foreground location refresh", async () => {
  let finishLocation!: () => void;
  const pending = new Promise<void>((resolve) => {
    finishLocation = resolve;
  });
  let holdLocation = false;
  let locationReads = 0;
  const { client, store } = makeStore(async (params) => {
    if (params.resource === "manifest") return manifestResponse(params);
    if (params.resource === "location") {
      locationReads++;
      if (holdLocation) await pending;
      return wireSnapshot(params, { session: row("live-001") }, '"location-1"', 1);
    }
    throw new Error(`unexpected navigation read ${JSON.stringify(params)}`);
  });
  await store.getState().lookupLocation("live-001");
  holdLocation = true;
  let settled = false;
  const convergence = store
    .getState()
    .applyNavigationMutation({
      generation_id: generation,
      targets: [{ kind: "section", section: "live", revision: 2 }],
    })
    .then(() => {
      settled = true;
    });
  await Promise.resolve();
  expect(locationReads).toBe(2);
  expect(settled).toBe(false);

  finishLocation();
  await convergence;
  expect(settled).toBe(true);
  store.reset();
  client.close();
});

test("reset retires background leases and clears foreground visible keys", async () => {
  const { client, store } = makeStore((params) =>
    params.resource === "manifest" ? manifestResponse(params) : pageResponse(params, ["live-001", "live-002"], 4),
  );
  const background = store.acquireBackgroundLivePages();
  await background.load(0, 2);
  await store.getState().loadSection("live", 0, 2);
  expect(
    store.getState().visibleResourceKeys.has(keyID({ kind: "section", section: "live", offset: 0, limit: 2 })),
  ).toBe(true);
  store.reset();
  expect(store.getState().visibleResourceKeys.size).toBe(0);
  expect(selectGlobalRows(store.getState())).toEqual([]);
  background.release();
  client.close();
});

test("a generation reset retains the foreground page boundary and excludes hidden pages", async () => {
  let version = 1;
  let gen = generation;
  const { client, store } = makeStore((params) => {
    if (params.resource === "manifest") return manifestResponse(params);
    const offset = params.offset ?? 0;
    const refs =
      offset === 0 ? ["live-001", "live-002"] : offset === 2 ? ["live-003", "live-004"] : ["live-005", "live-006"];
    return pageResponse(params, refs, offset === 4 ? 0 : 2, version, gen);
  });
  const hidden = store.acquireBackgroundLivePages();
  await store.getState().loadSection("live", 0, 2);
  await store.getState().loadSection("live", 2, 2);
  await hidden.load(4, 2);
  version = 2;
  gen = "generation_replacement";
  client.emitStateChange("reconnecting");
  client.emitReady(initialize("generation_replacement"));
  expect(store.getState().clientGenerationID).toBe("generation_replacement");
  await store
    .getState()
    .awaitNavigationTargets([{ kind: "section", section: "live", revision: 2 }], "generation_replacement");
  expect(selectLiveRows(store.getState()).map((item) => item.ref)).toEqual([
    "live-001",
    "live-002",
    "live-003",
    "live-004",
  ]);
  expect(selectNextSectionOffset("live", store.getState())).toBe(4);
  expect(selectSectionRemaining("live", store.getState())).toBe(2);
  hidden.release();
  store.reset();
  client.close();
});

test("sequence gaps revalidate background pages without making them visible", async () => {
  let revision = 1;
  const calls: NavigationReadParams[] = [];
  const { client, store } = makeStore((params) => {
    calls.push(params);
    if (params.resource === "manifest") return manifestResponse(params);
    return params.offset === 4
      ? pageResponse(params, ["live-005", "live-006"], 0, revision)
      : pageResponse(params, ["live-001", "live-002"], 4, revision);
  });
  const hidden = store.acquireBackgroundLivePages();
  await store.getState().loadSection("live", 0, 2);
  await hidden.load(4, 2);
  const before = calls.length;
  revision = 2;
  client.emitNotification(navigationInvalidatedNotification({ generationId: generation, sequence: 2, targets: [] }));
  await store.getState().awaitNavigationTargets([{ kind: "section", section: "live", revision: 2 }]);
  expect(calls.length).toBeGreaterThan(before);
  expect(selectLiveRows(store.getState()).map((item) => item.ref)).toEqual(["live-001", "live-002"]);
  expect(selectNextSectionOffset("live", store.getState())).toBe(2);
  hidden.release();
  store.reset();
  client.close();
});

test("later visible page refresh includes all prior visible pages but not the hidden extent", async () => {
  let revision = 1;
  const calls: NavigationReadParams[] = [];
  const { client, store } = makeStore((params) => {
    calls.push(params);
    if (params.resource === "manifest") return manifestResponse(params);
    const offset = params.offset ?? 0;
    const refs =
      offset === 0 ? ["live-001", "live-002"] : offset === 2 ? ["live-003", "live-004"] : ["live-005", "live-006"];
    return pageResponse(params, refs, offset === 4 ? 0 : 2, revision);
  });
  const hidden = store.acquireBackgroundLivePages();
  await store.getState().loadSection("live", 0, 2);
  await store.getState().loadSection("live", 2, 2);
  await hidden.load(4, 2);
  const before = calls.length;
  revision = 2;
  client.emitNotification(
    navigationInvalidatedNotification({
      generationId: generation,
      sequence: 1,
      targets: [{ kind: "section", section: "live", revision: 2 }],
    }),
  );
  await store.getState().awaitNavigationTargets([{ kind: "section", section: "live", revision: 2 }]);
  expect(
    calls
      .slice(before)
      .map((params) => params.offset)
      .filter((offset) => offset !== undefined)
      .sort(),
  ).toEqual([0, 2, 4]);
  expect(selectLiveRows(store.getState()).map((item) => item.ref)).toEqual([
    "live-001",
    "live-002",
    "live-003",
    "live-004",
  ]);
  hidden.release();
  store.reset();
  client.close();
});

test("releasing an old lease cannot publish into the replacement client", async () => {
  const oldClient = new FakeClient("ready");
  const oldResponse = (() => {
    let release!: () => void;
    const pending = new Promise<void>((resolve) => {
      release = resolve;
    });
    oldClient.on("evener/navigation/read", async (params) => {
      if (params.resource === "manifest") return manifestResponse(params);
      await pending;
      return pageResponse(params, ["old-client-row"], 0);
    });
    return { pending, release };
  })();
  const newClient = new FakeClient("ready");
  newClient.on("evener/navigation/read", (params) =>
    params.resource === "manifest"
      ? wireSnapshot(params, manifest(), '"manifest-replacement"', 1, "generation_replacement")
      : pageResponse(params, ["new-client-row"], 0, 1, "generation_replacement"),
  );
  const store = createNavigationStore({ persistence: memoryNavigationPersistence() });
  store.init(oldClient, capability());
  const oldLease = store.acquireBackgroundLivePages();
  const oldLoad = oldLease.load(0, 2);
  store.init(newClient, capability("generation_replacement"));
  await expect(oldLease.load(2, 2)).rejects.toThrow("background navigation lease released");
  oldResponse.release();
  await oldLoad;
  await store.getState().loadSection("live", 0, 2);
  expect(selectLiveRows(store.getState()).map((item) => item.ref)).toEqual(["new-client-row"]);
  oldLease.release();
  store.reset();
  oldClient.close();
  newClient.close();
});

test("foreground location and project reads are registered alongside Live page demand", async () => {
  const { client, store } = makeStore((params) => {
    if (params.resource === "manifest") return manifestResponse(params);
    if (params.resource === "section") return pageResponse(params, ["live-001", "live-002"], 4);
    if (params.resource === "location") return wireSnapshot(params, { session: row("live-001") });
    if (params.resource === "project")
      return wireSnapshot(params, {
        current: { sessions: [row("project-session")], remaining: 0 },
        recent: { sessions: [], remaining: 0 },
        archived: { sessions: [], remaining: 0 },
      });
    throw new Error(`unexpected navigation read ${JSON.stringify(params)}`);
  });
  const background = store.acquireBackgroundLivePages();
  await store.getState().loadSection("live", 0, 2);
  await background.load(2, 2);
  await store.getState().lookupLocation("live-001");
  await store.getState().loadProject("project-key");
  expect(store.getState().visibleResourceKeys).toEqual(
    new Set([
      keyID({ kind: "manifest" }),
      keyID({ kind: "section", section: "live", offset: 0, limit: 2 }),
      keyID({ kind: "location", ref: "local:live-001" }),
      keyID({ kind: "project", projectKey: "project-key" }),
    ]),
  );
  background.release();
  store.reset();
  client.close();
});

test("foreground convergence ignores a failed hidden key while explicit hidden-key wait rejects", async () => {
  const revalidator = new NavigationRevalidator(generation);
  const visible = { kind: "section", section: "live", offset: 0, limit: 2 } as const;
  const hidden = { kind: "section", section: "live", offset: 4, limit: 2 } as const;
  const request = (params: NavigationReadParams, refs: string[]) => async () => ({
    status: 200 as const,
    generationID: generation,
    revision: params.offset === 0 ? 2 : 1,
    etag: `"${params.offset}"`,
    data: { sessions: refs.map(row), remaining: 0, truncated: false },
  });
  await revalidator.load(visible, request(liveParams(0, 2), ["live-001", "live-002"]));
  await revalidator.load(hidden, async () => {
    throw new Error("hidden page unavailable");
  });
  revalidator.invalidate({ kind: "section", section: "live", revision: 2 });
  const visibleKeys = new Set([keyID(visible)]);
  await expect(
    revalidator.waitForTargets([{ kind: "section", section: "live", revision: 2 }], generation, visibleKeys),
  ).resolves.toBeUndefined();
  await expect(
    revalidator.waitForTargets(
      [{ kind: "section", section: "live", revision: 2 }],
      generation,
      new Set([keyID(hidden)]),
    ),
  ).rejects.toThrow("target convergence failed");
  revalidator.dispose();
});

test("background refresh re-reads only its fresh hidden page without visible expansion", async () => {
  let revision = 1;
  const calls: NavigationReadParams[] = [];
  const { client, store } = makeStore((params) => {
    calls.push(params);
    if (params.resource === "manifest") return manifestResponse(params);
    return params.offset === 0
      ? pageResponse(params, ["live-001", "live-002"], 4, revision)
      : pageResponse(params, ["live-005", "live-006"], 0, revision);
  });
  const visibleKey = { kind: "section", section: "live", offset: 0, limit: 2 } as const;
  const hiddenKey = { kind: "section", section: "live", offset: 4, limit: 2 } as const;
  const hiddenId = keyID(hiddenKey);
  await store.getState().loadSection("live", 0, 2);
  const hidden = store.acquireBackgroundLivePages();
  const sectionReads = (offset: number) =>
    calls.filter((params) => params.resource === "section" && params.offset === offset).length;
  const foregroundReadsBefore = sectionReads(0);

  await hidden.load(4, 2);
  expect(sectionReads(4)).toBe(1);
  // A plain load is served from the fresh cache.
  await hidden.load(4, 2);
  expect(sectionReads(4)).toBe(1);

  // refresh reaches the wire for only the requested hidden page.
  revision = 2;
  const refreshed = await hidden.refresh(4, 2);
  expect(refreshed.data).toMatchObject({ sessions: [{ ref: "live-005" }, { ref: "live-006" }] });
  expect(sectionReads(4)).toBe(2);
  expect(sectionReads(0)).toBe(foregroundReadsBefore);
  expect(store.getState().visibleResourceKeys.has(hiddenId)).toBe(false);
  expect(store.getState().visibleResourceKeys.has(keyID(visibleKey))).toBe(true);
  // Shared foreground resources remain usable after a hidden refresh.
  expect(selectLiveRows(store.getState()).map((item) => item.ref)).toEqual(["live-001", "live-002"]);

  hidden.release();
  await expect(hidden.refresh(4, 2)).rejects.toThrow("background navigation lease released");
  await expect(hidden.load(4, 2)).rejects.toThrow("background navigation lease released");
  store.reset();
  client.close();
});

test("releasing during an in-flight background refresh fences the retired lease", async () => {
  let revision = 1;
  const requestGate: { release: () => void } = { release: () => {} };
  let holdRequest = false;
  const calls: NavigationReadParams[] = [];
  const { client, store } = makeStore(async (params) => {
    calls.push(params);
    if (params.resource === "manifest") return manifestResponse(params);
    if (holdRequest)
      await new Promise<void>((resolve) => {
        requestGate.release = resolve;
      });
    return pageResponse(params, ["live-005", "live-006"], 0, revision);
  });
  const key = { kind: "section", section: "live", offset: 4, limit: 2 } as const;
  const id = keyID(key);
  const hidden = store.acquireBackgroundLivePages();
  await hidden.load(4, 2);
  const readsBefore = calls.filter((params) => params.resource === "section" && params.offset === 4).length;
  revision = 2;

  holdRequest = true;
  const refreshed = hidden.refresh(4, 2);
  await Promise.resolve();
  expect(store.getState().resources.get(id)?.loading).toBe(true);
  hidden.release();
  requestGate.release();
  await refreshed;
  expect(store.getState().resources.has(id)).toBe(false);
  expect(calls.filter((params) => params.resource === "section" && params.offset === 4).length).toBe(readsBefore + 1);
  store.reset();
  client.close();
});

test("releasing during refresh's synchronous force publication does not re-register the lease", async () => {
  let revision = 1;
  const calls: NavigationReadParams[] = [];
  const { client, store } = makeStore((params) => {
    calls.push(params);
    if (params.resource === "manifest") return manifestResponse(params);
    return pageResponse(params, ["live-005", "live-006"], 0, revision);
  });
  const key = { kind: "section", section: "live", offset: 4, limit: 2 } as const;
  const id = keyID(key);
  const hidden = store.acquireBackgroundLivePages();
  await hidden.load(4, 2);
  const readsBefore = calls.filter((params) => params.resource === "section" && params.offset === 4).length;
  revision = 2;

  let released = false;
  const unsubscribe = store.subscribe((state) => {
    if (released) return;
    if (state.resources.get(id)?.stale) {
      released = true;
      hidden.release();
    }
  });
  try {
    await expect(hidden.refresh(4, 2)).rejects.toThrow("background navigation lease released");
  } finally {
    unsubscribe();
  }
  expect(released).toBe(true);
  // The continuation must not start a second read for the retired lease: at
  // most the single read force itself may have started during its publication.
  expect(calls.filter((params) => params.resource === "section" && params.offset === 4).length).toBeLessThanOrEqual(
    readsBefore + 1,
  );
  store.reset();
  client.close();
});
