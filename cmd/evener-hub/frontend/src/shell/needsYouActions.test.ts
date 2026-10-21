// @vitest-environment jsdom

import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { capability, manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { afterEach, expect, test, vi } from "vitest";
import { navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import "../panes/session";
import "../panes/transcript";
import "../panes/zoom";
import { enterAgentCascade, popAgentCascade } from "../panes/zoom/actions";
import { activityDelegate } from "../stores/sessionActivityTestUtils";
import { focusedSessionRef } from "./focusedSession";
import { createNeedsYouController, type NeedsYouController } from "./needsYouActions";
import { closePalette, openPalette, paletteStore } from "./palette/paletteController";
import { conversationPaneLifetime } from "./paneLifetime";
import * as routing from "./routing";
import { navigate } from "./routing";
import { resetWorkspaceStoreForTests, workspaceStore } from "./workspace";

function whenNavigation(predicate: (state: ReturnType<typeof navigationStore.getState>) => boolean): Promise<void> {
  if (predicate(navigationStore.getState())) return Promise.resolve();
  return new Promise((resolve) => {
    const unsubscribe = navigationStore.subscribe((state) => {
      if (!predicate(state)) return;
      unsubscribe();
      resolve();
    });
  });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => (resolve = done));
  return { promise, resolve };
}

async function heldNeedsYouPage() {
  const client = new FakeClient("ready");
  const entered = deferred<void>();
  const response = deferred<void>();
  const offsets: number[] = [];
  client.on("evener/navigation/read", async (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({ sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } } }),
      );
    }
    if (params.resource !== "section" || params.section !== "needs_you") {
      throw new Error(`Unexpected navigation resource ${params.resource}`);
    }
    offsets.push(params.offset ?? -1);
    entered.resolve();
    await response.promise;
    return wireSnapshot(params, { sessions: [{ ref: "local:b" }], remaining: 0 });
  });
  navigationStore.init(client, capability());
  await entered.promise;
  return { release: () => response.resolve(), offsets };
}

function pane(id: string) {
  const record = workspaceStore.getState().panes.find((item) => item.id === id);
  if (!record) throw new Error(`Missing pane ${id}`);
  return record;
}

function routePosition() {
  return { path: window.location.pathname, historyLength: window.history.length, state: window.history.state };
}

function whenBlocked(controller: NeedsYouController, blocked: boolean): Promise<void> {
  if (controller.getSnapshot().blocked === blocked) return Promise.resolve();
  return new Promise((resolve) => {
    const unsubscribe = controller.subscribe(() => {
      if (controller.getSnapshot().blocked !== blocked) return;
      unsubscribe();
      resolve();
    });
  });
}

afterEach(() => {
  closePalette();
  resetNavigationStoreForTests();
  resetWorkspaceStoreForTests();
  window.history.replaceState({}, "", "/");
});

test("More requests one bounded real needs-you page and keeps its snapshot stable", async () => {
  const client = new FakeClient("ready");
  const reads: Array<{ resource: string; offset?: number; limit?: number }> = [];
  client.on("evener/navigation/read", (params) => {
    reads.push(params);
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "actions-test",
          sections: { live: { count: 0 }, needs_you: { count: 2 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "actions-test",
      );
    }
    if (params.resource === "section") {
      return wireSnapshot(
        params,
        {
          sessions: [{ ref: params.offset === 0 ? "local:first" : "local:second" }],
          remaining: params.offset === 0 ? 1 : 0,
        },
        '"page"',
        1,
        "actions-test",
      );
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  const release = navigationStore.init(client, capability("actions-test"));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const controller = createNeedsYouController();
  const before = controller.getSnapshot();
  expect(controller.getSnapshot()).toBe(before);
  await controller.loadMore();
  expect(reads.filter((read) => read.resource === "section").map(({ offset, limit }) => [offset, limit])).toEqual([
    [0, 50],
    [1, 50],
  ]);
  expect(controller.getSnapshot().canLoadMore).toBe(false);
  controller.dispose();
  release();
});

test("dispose releases the action owner's subscriptions", () => {
  const controller = createNeedsYouController();
  const before = controller.getSnapshot();
  let calls = 0;
  controller.subscribe(() => calls++);
  controller.dispose();
  navigationStore.setState({ clientGenerationID: "after-dispose" });
  expect(controller.getSnapshot()).toBe(before);
  expect(calls).toBe(0);
});

test("a deliberate retry recovers a failed boundary page", async () => {
  const client = new FakeClient("ready");
  const offsets: number[] = [];
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "retry-generation",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "retry-generation",
      );
    }
    if (params.resource === "section") {
      offsets.push(params.offset ?? -1);
      if (offsets.length === 1) throw new Error("transport failure");
      return wireSnapshot(
        params,
        { sessions: [{ ref: "local:recovered" }], remaining: 0 },
        '"recovered"',
        1,
        "retry-generation",
      );
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("retry-generation"));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const controller = createNeedsYouController();
  expect(controller.getSnapshot().readFailed).toBe(true);
  await controller.loadMore();
  expect(offsets).toEqual([0, 0]);
  expect(controller.getSnapshot().readFailed).toBe(false);
  expect(controller.getSnapshot().choices.map((choice) => choice.summary.ref)).toEqual(["local:recovered"]);
  controller.dispose();
});

test("next and select navigate the exact server-ordered reader while More leaves history and focus unchanged", async () => {
  const client = new FakeClient("ready");
  const pageReads: number[] = [];
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "outcome-generation",
          sections: { live: { count: 0 }, needs_you: { count: 3 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "outcome-generation",
      );
    }
    if (params.resource === "section") {
      pageReads.push(params.offset ?? -1);
      return wireSnapshot(
        params,
        {
          sessions: ["local:a", "local:b", "local:c"].map((ref) => ({
            ref,
            host_id: "local",
            session_id: ref,
            title: ref,
            project: "Proj",
            state: "awaiting",
            kind: "session",
            live: true,
            children: [],
          })),
          remaining: 0,
        },
        '"ordered-page"',
        1,
        "outcome-generation",
      );
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("outcome-generation"));
  await whenNavigation(
    (state) =>
      !!state.manifest?.data &&
      [...state.resources.values()].some(
        (resource) => resource.key.kind === "section" && !resource.loading && !!resource.data,
      ),
  );

  window.history.replaceState({}, "", "/s/local%3Aa");
  const sourceId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const controller = createNeedsYouController();
  openPalette();
  expect(controller.getSnapshot().blocked).toBe(true);
  await controller.next();
  expect(window.location.pathname).toBe("/s/local%3Aa");
  closePalette();
  expect(controller.getSnapshot().blocked).toBe(false);
  const editor = document.createElement("textarea");
  document.body.append(editor);
  editor.focus();
  const beforeNext = window.history.length;
  await controller.next();
  expect(window.location.pathname).toBe("/s/local%3Ab");
  const b = workspaceStore
    .getState()
    .panes.find((pane) => (pane.params as { ref?: string } | undefined)?.ref === "local:b");
  expect(b).toBeDefined();
  expect(workspaceStore.getState().focusedPaneId).toBe(b?.id);
  expect(workspaceStore.getState().focusedPaneId).not.toBe(sourceId);
  expect(window.history.length).toBe(beforeNext + 1);
  expect(controller.getSnapshot().nextRef).toBe("local:c");
  editor.remove();

  controller.select("local:a");
  expect(window.location.pathname).toBe("/s/local%3Aa");
  const a = workspaceStore
    .getState()
    .panes.find((pane) => (pane.params as { ref?: string } | undefined)?.ref === "local:a");
  expect(workspaceStore.getState().focusedPaneId).toBe(a?.id);
  expect(window.history.length).toBe(beforeNext + 2);

  const beforeMore = {
    pathname: window.location.pathname,
    focusedPaneId: workspaceStore.getState().focusedPaneId,
    historyLength: window.history.length,
  };
  await controller.loadMore();
  expect(pageReads).toEqual([0, 3]);
  expect({
    pathname: window.location.pathname,
    focusedPaneId: workspaceStore.getState().focusedPaneId,
    historyLength: window.history.length,
  }).toEqual(beforeMore);
  controller.dispose();
});

test("an incomplete last-loaded row demands one page before any wrap", async () => {
  const client = new FakeClient("ready");
  const offsets: number[] = [];
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "partial-wrap",
          sections: { live: { count: 0 }, needs_you: { count: 3 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "partial-wrap",
      );
    }
    if (params.resource === "section") {
      offsets.push(params.offset ?? -1);
      const sessions = (params.offset ?? 0) === 0 ? ["local:a", "local:b"] : ["local:c"];
      return wireSnapshot(
        params,
        { sessions: sessions.map((ref) => ({ ref })), remaining: (params.offset ?? 0) === 0 ? 1 : 0 },
        `"partial-${params.offset}"`,
        1,
        "partial-wrap",
      );
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("partial-wrap"));
  await whenNavigation(
    (state) =>
      !!state.manifest?.data &&
      [...state.resources.values()].some(
        (resource) => resource.key.kind === "section" && !resource.loading && !!resource.data,
      ),
  );
  window.history.replaceState({}, "", "/s/local%3Ab");
  const sourceId = workspaceStore.getState().openPane("session", { ref: "local:b" });
  const controller = createNeedsYouController();

  expect(controller.getSnapshot().choices.map((choice) => choice.summary.ref)).toEqual(["local:a", "local:b"]);
  expect(controller.getSnapshot().nextRef).toBeNull();
  const before = window.history.length;
  await controller.next();
  expect(offsets).toEqual([0, 2]);
  expect(window.location.pathname).toBe("/s/local%3Ac");
  const c = workspaceStore
    .getState()
    .panes.find((pane) => (pane.params as { ref?: string } | undefined)?.ref === "local:c");
  expect(c).toBeDefined();
  expect(workspaceStore.getState().focusedPaneId).toBe(c?.id);
  expect(workspaceStore.getState().focusedPaneId).not.toBe(sourceId);
  expect(window.history.length).toBe(before + 1);
  controller.dispose();
});

test("self-only incomplete pages leave one deliberate demand available without recursive filling", async () => {
  const client = new FakeClient("ready");
  const offsets: number[] = [];
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "self-incomplete",
          sections: { live: { count: 0 }, needs_you: { count: 3 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "self-incomplete",
      );
    }
    if (params.resource === "section") {
      offsets.push(params.offset ?? -1);
      const offset = params.offset ?? 0;
      return wireSnapshot(
        params,
        { sessions: [{ ref: offset === 2 ? "local:b" : "local:a" }], remaining: offset === 2 ? 0 : 1 },
        `"self-${offset}"`,
        1,
        "self-incomplete",
      );
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("self-incomplete"));
  await whenNavigation(
    (state) =>
      !!state.manifest?.data &&
      [...state.resources.values()].some(
        (resource) => resource.key.kind === "section" && !resource.loading && !!resource.data,
      ),
  );
  window.history.replaceState({}, "", "/s/local%3Aa");
  workspaceStore.getState().openPane("session", { ref: "local:a" });
  const controller = createNeedsYouController();

  await controller.next();
  expect(offsets).toEqual([0, 1]);
  expect(controller.getSnapshot().nextRef).toBeNull();
  expect(controller.getSnapshot().canLoadMore).toBe(true);
  expect(window.location.pathname).toBe("/s/local%3Aa");
  await controller.next();
  expect(offsets).toEqual([0, 1, 2]);
  expect(window.location.pathname).toBe("/s/local%3Ab");
  controller.dispose();
});

test("a failed later page keeps healthy choices selectable and retries the same boundary", async () => {
  const client = new FakeClient("ready");
  const offsets: number[] = [];
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "healthy-retry",
          sections: { live: { count: 0 }, needs_you: { count: 3 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "healthy-retry",
      );
    }
    if (params.resource === "section") {
      const offset = params.offset ?? -1;
      offsets.push(offset);
      if (offset === 2 && offsets.filter((value) => value === 2).length === 1) throw new Error("boundary failed");
      return wireSnapshot(
        params,
        {
          sessions: offset === 0 ? [{ ref: "local:a" }, { ref: "local:b" }] : [{ ref: "local:c" }],
          remaining: offset === 0 ? 1 : 0,
        },
        `"retry-${offset}-${offsets.length}"`,
        1,
        "healthy-retry",
      );
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("healthy-retry"));
  await whenNavigation(
    (state) =>
      !!state.manifest?.data &&
      [...state.resources.values()].some(
        (resource) => resource.key.kind === "section" && !resource.loading && !!resource.data,
      ),
  );
  window.history.replaceState({}, "", "/s/local%3Aa");
  workspaceStore.getState().openPane("session", { ref: "local:a" });
  const controller = createNeedsYouController();
  const generation = navigationStore.getState().clientGenerationID;
  await controller.loadMore();
  expect(offsets).toEqual([0, 2]);
  expect(controller.getSnapshot().readFailed).toBe(true);
  expect(controller.getSnapshot().choices.find((choice) => choice.summary.ref === "local:b")?.selectable).toBe(true);

  const before = window.history.length;
  controller.select("local:b");
  expect(window.location.pathname).toBe("/s/local%3Ab");
  const b = workspaceStore
    .getState()
    .panes.find((pane) => (pane.params as { ref?: string } | undefined)?.ref === "local:b");
  expect(workspaceStore.getState().focusedPaneId).toBe(b?.id);
  expect(window.history.length).toBe(before + 1);

  await controller.loadMore();
  expect(offsets).toEqual([0, 2, 2]);
  expect(navigationStore.getState().clientGenerationID).toBe(generation);
  expect(controller.getSnapshot().readFailed).toBe(false);
  expect(controller.getSnapshot().choices.map((choice) => choice.summary.ref)).toEqual([
    "local:a",
    "local:b",
    "local:c",
  ]);
  controller.dispose();
});

test("a stale resource blocks selection until its actual refreshed response restores it", async () => {
  const client = new FakeClient("ready");
  let refreshPage!: () => void;
  let refreshEntered!: () => void;
  const refreshStarted = new Promise<void>((resolve) => (refreshEntered = resolve));
  let refreshed = false;
  let calls = 0;
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "stale-refresh",
          sections: { live: { count: 0 }, needs_you: { count: 2 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "stale-refresh",
      );
    }
    if (params.resource === "section") {
      calls++;
      if (calls === 1) {
        return wireSnapshot(
          params,
          { sessions: [{ ref: "local:a" }, { ref: "local:b" }], remaining: 0 },
          '"stale-1"',
          1,
          "stale-refresh",
        );
      }
      refreshEntered();
      return new Promise((resolve) => {
        refreshPage = () => {
          refreshed = true;
          resolve(
            wireSnapshot(
              params,
              { sessions: [{ ref: "local:a" }, { ref: "local:b" }], remaining: 0 },
              '"stale-2"',
              2,
              "stale-refresh",
            ),
          );
        };
      });
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("stale-refresh"));
  await whenNavigation(
    (state) =>
      !!state.manifest?.data &&
      [...state.resources.values()].some((resource) => resource.key.kind === "section" && !resource.loading),
  );
  window.history.replaceState({}, "", "/s/local%3Aa");
  workspaceStore.getState().openPane("session", { ref: "local:a" });
  const controller = createNeedsYouController();
  const key = [...navigationStore.getState().resources.keys()][0];
  const initialURL = window.location.pathname;
  client.emitNotification({
    method: "evener/navigation/invalidated",
    params: {
      generationId: "stale-refresh",
      sequence: 1,
      targets: [{ kind: "section", section: "needs_you", revision: 2 }],
    },
  } as never);
  await whenNavigation((state) =>
    [...state.resources.values()].some(
      (resource) => resource.key.kind === "section" && resource.loading && resource.stale,
    ),
  );
  await refreshStarted;
  expect(key).toBeDefined();
  expect(controller.getSnapshot().choices.find((choice) => choice.summary.ref === "local:b")?.selectable).toBe(false);
  controller.select("local:b");
  expect(window.location.pathname).toBe(initialURL);
  expect(
    workspaceStore.getState().panes.some((pane) => (pane.params as { ref?: string } | undefined)?.ref === "local:b"),
  ).toBe(false);

  refreshPage();
  await whenNavigation(
    (state) =>
      refreshed &&
      [...state.resources.values()].some(
        (resource) => resource.key.kind === "section" && !resource.stale && !resource.loading,
      ),
  );
  expect(controller.getSnapshot().choices.find((choice) => choice.summary.ref === "local:b")?.selectable).toBe(true);
  const before = window.history.length;
  controller.select("local:b");
  expect(window.location.pathname).toBe("/s/local%3Ab");
  const b = workspaceStore
    .getState()
    .panes.find((pane) => (pane.params as { ref?: string } | undefined)?.ref === "local:b");
  expect(workspaceStore.getState().focusedPaneId).toBe(b?.id);
  expect(window.history.length).toBe(before + 1);
  controller.dispose();
});

test("duplicate next activations share one held page read and make at most one history hop", async () => {
  const client = new FakeClient("ready");
  let releasePage!: () => void;
  let entered!: () => void;
  const pageEntered = new Promise<void>((resolve) => (entered = resolve));
  const pageOffsets: number[] = [];
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "held-next",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "held-next",
      );
    }
    if (params.resource === "section") {
      pageOffsets.push(params.offset ?? -1);
      entered();
      return new Promise((resolve) => {
        releasePage = () =>
          resolve(
            wireSnapshot(params, { sessions: [{ ref: "local:b" }], remaining: 0 }, '"held-page"', 1, "held-next"),
          );
      });
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("held-next"));
  await pageEntered;
  window.history.replaceState({}, "", "/s/local%3Aa");
  workspaceStore.getState().openPane("session", { ref: "local:a" });
  const controller = createNeedsYouController();
  const before = window.history.length;
  const first = controller.next();
  const second = controller.next();
  releasePage();
  await Promise.all([first, second]);
  expect(pageOffsets).toEqual([0]);
  expect(window.location.pathname).toBe("/s/local%3Ab");
  const b = workspaceStore
    .getState()
    .panes.find((pane) => (pane.params as { ref?: string } | undefined)?.ref === "local:b");
  expect(workspaceStore.getState().focusedPaneId).toBe(b?.id);
  expect(window.history.length).toBe(before + 1);
  controller.dispose();
});

test("complete empty and self-only resources hide unavailable navigation without extra reads", async () => {
  const client = new FakeClient("ready");
  const offsets: number[] = [];
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "complete-empty",
          sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "complete-empty",
      );
    }
    if (params.resource === "section") {
      offsets.push(params.offset ?? -1);
      return wireSnapshot(params, { sessions: [], remaining: 0 }, '"empty"', 1, "complete-empty");
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("complete-empty"));
  const controller = createNeedsYouController();
  expect(controller.getSnapshot().canLoadMore).toBe(true);
  await controller.loadMore();
  expect(offsets).toEqual([0]);
  expect(controller.getSnapshot().choices).toEqual([]);
  expect(controller.getSnapshot().nextRef).toBeNull();
  expect(controller.getSnapshot().canLoadMore).toBe(false);
  controller.dispose();
});

test("more than eight overlapping pages retain server order, deduplicate, and wrap only at complete end", async () => {
  const client = new FakeClient("ready");
  const requests: Array<{ offset?: number; limit?: number }> = [];
  const firstPage = Array.from({ length: 50 }, (_, index) => `local:item-${index}`);
  const secondPage = ["local:item-49", ...Array.from({ length: 9 }, (_, index) => `local:item-${index + 50}`)];
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "overlap-pages",
          sections: { live: { count: 0 }, needs_you: { count: 59 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "overlap-pages",
      );
    }
    if (params.resource === "section") {
      requests.push({ offset: params.offset, limit: params.limit });
      const offset = params.offset ?? 0;
      const refs = offset === 0 ? firstPage : secondPage;
      return wireSnapshot(
        params,
        { sessions: refs.map((ref) => ({ ref })), remaining: offset === 0 ? 9 : 0 },
        `"overlap-${offset}"`,
        1,
        "overlap-pages",
      );
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("overlap-pages"));
  await whenNavigation(
    (state) =>
      !!state.manifest?.data &&
      [...state.resources.values()].some(
        (resource) => resource.key.kind === "section" && !resource.loading && !!resource.data,
      ),
  );
  window.history.replaceState({}, "", "/s/local%3Aitem-0");
  workspaceStore.getState().openPane("session", { ref: "local:item-0" });
  const controller = createNeedsYouController();
  expect(controller.getSnapshot().choices.map((choice) => choice.summary.ref)).toEqual(firstPage);
  expect(controller.getSnapshot().nextRef).toBe("local:item-1");
  await controller.loadMore();
  expect(requests).toEqual([
    { offset: 0, limit: 50 },
    { offset: 50, limit: 50 },
  ]);
  expect(controller.getSnapshot().choices.map((choice) => choice.summary.ref)).toEqual([
    ...firstPage,
    ...secondPage.slice(1),
  ]);
  expect(controller.getSnapshot().choices).toHaveLength(59);

  const lastId = workspaceStore.getState().openPane("session", { ref: "local:item-58" });
  window.history.replaceState({}, "", "/s/local%3Aitem-58");
  const beforeWrap = window.history.length;
  await controller.next();
  expect(window.location.pathname).toBe("/s/local%3Aitem-0");
  const first = workspaceStore
    .getState()
    .panes.find((pane) => (pane.params as { ref?: string } | undefined)?.ref === "local:item-0");
  expect(workspaceStore.getState().focusedPaneId).toBe(first?.id);
  expect(workspaceStore.getState().focusedPaneId).not.toBe(lastId);
  expect(window.history.length).toBe(beforeWrap + 1);
  expect(requests).toEqual([
    { offset: 0, limit: 50 },
    { offset: 50, limit: 50 },
  ]);
  controller.dispose();
});

test("a complete self-only page has no next target and is not mistaken for remaining data", async () => {
  const client = new FakeClient("ready");
  const sectionReads: number[] = [];
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "self-complete",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "self-complete",
      );
    }
    if (params.resource === "section") {
      sectionReads.push(params.offset ?? -1);
      return wireSnapshot(params, { sessions: [{ ref: "local:a" }], remaining: 0 }, '"self"', 1, "self-complete");
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("self-complete"));
  await whenNavigation(
    (state) =>
      !!state.manifest?.data &&
      [...state.resources.values()].some(
        (resource) => resource.key.kind === "section" && !resource.loading && !!resource.data,
      ),
  );
  window.history.replaceState({}, "", "/s/local%3Aa");
  workspaceStore.getState().openPane("session", { ref: "local:a" });
  const controller = createNeedsYouController();
  expect(controller.getSnapshot().choices.map((choice) => choice.summary.ref)).toEqual(["local:a"]);
  expect(controller.getSnapshot().nextRef).toBeNull();
  expect(controller.getSnapshot().canLoadMore).toBe(false);
  const historyLength = window.history.length;
  await controller.next();
  expect(sectionReads).toEqual([0]);
  expect(window.location.pathname).toBe("/s/local%3Aa");
  expect(window.history.length).toBe(historyLength);
  controller.dispose();
});

test("a held next response cannot override focus A to X to A", async () => {
  const client = new FakeClient("ready");
  let releasePage!: () => void;
  let entered!: () => void;
  const pageEntered = new Promise<void>((resolve) => (entered = resolve));
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "source-roundtrip",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "source-roundtrip",
      );
    }
    if (params.resource === "section") {
      entered();
      return new Promise((resolve) => {
        releasePage = () =>
          resolve(
            wireSnapshot(params, { sessions: [{ ref: "local:b" }], remaining: 0 }, '"page"', 1, "source-roundtrip"),
          );
      });
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("source-roundtrip"));
  await pageEntered;
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const x = workspaceStore.getState().openPane("session", { ref: "local:x" });
  workspaceStore.getState().focusPane(a);
  const controller = createNeedsYouController();
  const before = { path: window.location.pathname, history: window.history.length };
  const pending = controller.next();
  workspaceStore.getState().focusPane(x);
  workspaceStore.getState().focusPane(a);
  releasePage();
  await pending;
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
  expect(window.location.pathname).toBe(before.path);
  expect(window.history.length).toBe(before.history);
  controller.dispose();
});

test("accepted route replacement and same-path browser traversal cancel a held next", async () => {
  const client = new FakeClient("ready");
  let releasePage!: () => void;
  let entered!: () => void;
  const pageEntered = new Promise<void>((resolve) => (entered = resolve));
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "route-roundtrip",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "route-roundtrip",
      );
    }
    if (params.resource === "section") {
      entered();
      return new Promise((resolve) => {
        releasePage = () =>
          resolve(
            wireSnapshot(params, { sessions: [{ ref: "local:b" }], remaining: 0 }, '"page"', 1, "route-roundtrip"),
          );
      });
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("route-roundtrip"));
  await pageEntered;
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  workspaceStore.getState().focusPane(a);
  window.history.replaceState({}, "", "/s/local%3Aa");
  const controller = createNeedsYouController();
  const pending = controller.next();
  window.history.pushState({ samePathEntry: true }, "", "/s/local%3Aa");
  const popped = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  window.history.back();
  await popped;
  const accepted: string[] = [];
  navigate("/s/local%3Ax");
  accepted.push(window.location.pathname);
  navigate("/s/local%3Aa", { replace: true });
  accepted.push(window.location.pathname);
  expect(accepted).toEqual(["/s/local%3Ax", "/s/local%3Aa"]);
  const beforeRelease = { path: window.location.pathname, history: window.history.length };
  releasePage();
  await pending;
  expect(window.location.pathname).toBe(beforeRelease.path);
  expect(window.history.length).toBe(beforeRelease.history);
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
  controller.dispose();
});

test("a real blocking modal append and removal cancels a held next", async () => {
  const client = new FakeClient("ready");
  let releasePage!: () => void;
  let entered!: () => void;
  const pageEntered = new Promise<void>((resolve) => (entered = resolve));
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "modal-transition",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "modal-transition",
      );
    }
    if (params.resource === "section") {
      entered();
      return new Promise((resolve) => {
        releasePage = () =>
          resolve(
            wireSnapshot(params, { sessions: [{ ref: "local:b" }], remaining: 0 }, '"page"', 1, "modal-transition"),
          );
      });
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("modal-transition"));
  await pageEntered;
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  workspaceStore.getState().focusPane(a);
  window.history.replaceState({}, "", "/s/local%3Aa");
  const controller = createNeedsYouController();
  const historyBefore = window.history.length;
  const pending = controller.next();
  const modal = document.createElement("div");
  modal.setAttribute("aria-modal", "true");
  document.body.append(modal);
  modal.remove();
  releasePage();
  await pending;
  expect(window.location.pathname).toBe("/s/local%3Aa");
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
  expect(window.history.length).toBe(historyBefore);
  controller.dispose();
});

test("a client generation replacement cancels a held next response", async () => {
  const oldClient = new FakeClient("ready");
  let releasePage!: () => void;
  let entered!: () => void;
  const pageEntered = new Promise<void>((resolve) => (entered = resolve));
  oldClient.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "old-generation",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "old-generation",
      );
    }
    if (params.resource === "section") {
      entered();
      return new Promise((resolve) => {
        releasePage = () =>
          resolve(
            wireSnapshot(params, { sessions: [{ ref: "local:b" }], remaining: 0 }, '"old-page"', 1, "old-generation"),
          );
      });
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(oldClient, capability("old-generation"));
  await pageEntered;
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  workspaceStore.getState().focusPane(a);
  window.history.replaceState({}, "", "/s/local%3Aa");
  const controller = createNeedsYouController();
  const pending = controller.next();

  const newClient = new FakeClient("ready");
  newClient.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "new-generation",
          sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "new-generation",
      );
    }
    if (params.resource === "section")
      return wireSnapshot(params, { sessions: [], remaining: 0 }, '"empty"', 1, "new-generation");
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(newClient, capability("new-generation"));
  expect(navigationStore.getState().clientGenerationID).not.toBe("old-generation");
  const before = { path: window.location.pathname, history: window.history.length };
  releasePage();
  await pending;
  expect(window.location.pathname).toBe(before.path);
  expect(window.history.length).toBe(before.history);
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
  controller.dispose();
});

test("disposing the controller while a page waits prevents its late navigation", async () => {
  const client = new FakeClient("ready");
  let releasePage!: () => void;
  let entered!: () => void;
  const pageEntered = new Promise<void>((resolve) => (entered = resolve));
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "dispose-pending",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "dispose-pending",
      );
    }
    if (params.resource === "section") {
      entered();
      return new Promise((resolve) => {
        releasePage = () =>
          resolve(
            wireSnapshot(params, { sessions: [{ ref: "local:b" }], remaining: 0 }, '"page"', 1, "dispose-pending"),
          );
      });
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("dispose-pending"));
  await pageEntered;
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  workspaceStore.getState().focusPane(a);
  window.history.replaceState({}, "", "/s/local%3Aa");
  const controller = createNeedsYouController();
  const before = { path: window.location.pathname, history: window.history.length };
  const pending = controller.next();
  controller.dispose();
  controller.dispose();
  releasePage();
  await pending;
  expect(window.location.pathname).toBe(before.path);
  expect(window.history.length).toBe(before.history);
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
});

test("a cascade leaf round trip cancels held Next without replacing its reader lifetime", async () => {
  const page = await heldNeedsYouPage();
  const rootId = workspaceStore.getState().openPane("session", { ref: "local:root" });
  const sourceId = enterAgentCascade(
    activityDelegate({ ownerRef: "local:root", childRef: "local:a", delegateId: "first" }),
    rootId,
  );
  const original = pane(sourceId);
  const lifetime = conversationPaneLifetime(original);
  expect(focusedSessionRef()).toBe("local:a");
  const controller = createNeedsYouController();
  try {
    const before = routePosition();
    const pending = controller.next();
    expect(
      enterAgentCascade(activityDelegate({ ownerRef: "local:a", childRef: "local:x", delegateId: "second" }), sourceId),
    ).toBe(sourceId);
    expect(focusedSessionRef()).toBe("local:x");
    popAgentCascade(sourceId, "local:a");
    expect(focusedSessionRef()).toBe("local:a");
    const returned = pane(sourceId);
    page.release();
    await pending;
    expect(routePosition()).toEqual(before);
    expect(workspaceStore.getState().focusedPaneId).toBe(sourceId);
    expect(pane(sourceId)).toBe(returned);
    expect(conversationPaneLifetime(returned)).toBe(lifetime);
    expect(lifetime.alive).toBe(true);
    expect(workspaceStore.getState().panes.map((item) => item.id)).toEqual([rootId, sourceId]);
  } finally {
    controller.dispose();
  }
});

test("a same-pane role round trip cancels held Next even with the original ref restored", async () => {
  const page = await heldNeedsYouPage();
  const sourceId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const original = pane(sourceId);
  const lifetime = conversationPaneLifetime(original);
  const controller = createNeedsYouController();
  try {
    const before = routePosition();
    const pending = controller.next();
    expect(workspaceStore.getState().retypePane(original, "transcript", original.params)).toBe(true);
    expect(pane(sourceId).type).toBe("transcript");
    expect(workspaceStore.getState().focusedPaneId).toBe(sourceId);
    expect(workspaceStore.getState().retypePane(pane(sourceId), "session", original.params)).toBe(true);
    const returned = pane(sourceId);
    expect(focusedSessionRef()).toBe("local:a");
    page.release();
    await pending;
    expect(routePosition()).toEqual(before);
    expect(workspaceStore.getState().focusedPaneId).toBe(sourceId);
    expect(workspaceStore.getState().panes).toEqual([returned]);
    expect(pane(sourceId)).toBe(returned);
    expect(conversationPaneLifetime(returned)).toBe(lifetime);
    expect(lifetime.alive).toBe(true);
  } finally {
    controller.dispose();
  }
});

test("closing the source disposes its lifetime and held Next cannot recreate it", async () => {
  const page = await heldNeedsYouPage();
  const sourceId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  const lifetime = conversationPaneLifetime(pane(sourceId));
  const controller = createNeedsYouController();
  try {
    const before = routePosition();
    const pending = controller.next();
    workspaceStore.getState().closePane(sourceId);
    expect(lifetime.alive).toBe(false);
    page.release();
    await pending;
    expect(routePosition()).toEqual(before);
    expect(workspaceStore.getState().panes).toEqual([]);
    expect(workspaceStore.getState().focusedPaneId).toBeNull();
    expect(lifetime.alive).toBe(false);
  } finally {
    controller.dispose();
  }
});

test.each(["push", "replace", "away-back", "same-path-back"] as const)(
  "an independent %s route transition cancels held Next while its source is still focused",
  async (transition) => {
    const page = await heldNeedsYouPage();
    const sourceId = workspaceStore.getState().openPane("session", { ref: "local:a" });
    const source = pane(sourceId);
    window.history.replaceState({ entry: "source" }, "", "/s/local%3Aa");
    const controller = createNeedsYouController();
    let traversedBack = false;
    try {
      const pending = controller.next();
      if (transition === "same-path-back") {
        window.history.pushState({ entry: "second" }, "", "/s/local%3Aa");
        const popped = new Promise<void>((resolve) =>
          window.addEventListener("popstate", () => resolve(), { once: true }),
        );
        window.history.back();
        await popped;
        traversedBack = true;
        expect(window.history.state).toEqual({ entry: "source" });
      } else {
        navigate("/s/local%3Ax", { replace: transition === "replace" });
        expect(window.location.pathname).toBe("/s/local%3Ax");
        if (transition === "away-back") navigate("/s/local%3Aa", { replace: true });
      }
      expect(workspaceStore.getState().focusedPaneId).toBe(sourceId);
      const accepted = routePosition();
      page.release();
      await pending;
      expect(routePosition()).toEqual(accepted);
      expect(workspaceStore.getState().focusedPaneId).toBe(sourceId);
      expect(workspaceStore.getState().panes).toEqual([source]);
      expect(pane(sourceId)).toBe(source);
    } finally {
      controller.dispose();
      if (traversedBack) {
        const returned = new Promise<void>((resolve) =>
          window.addEventListener("popstate", () => resolve(), { once: true }),
        );
        window.history.forward();
        await returned;
      }
    }
  },
);

test.each(["settled-modal", "batched-attribute", "palette"] as const)(
  "%s closure keeps old Next cancelled but permits a new deliberate action",
  async (transition) => {
    const page = await heldNeedsYouPage();
    const sourceId = workspaceStore.getState().openPane("session", { ref: "local:a" });
    window.history.replaceState({}, "", "/s/local%3Aa");
    const modal = document.createElement("div");
    document.body.append(modal);
    const controller = createNeedsYouController();
    try {
      const before = routePosition();
      const pending = controller.next();
      if (transition === "palette") {
        openPalette();
        expect(controller.getSnapshot().blocked).toBe(true);
        closePalette();
      } else {
        modal.setAttribute("aria-modal", "true");
        if (transition === "settled-modal") await whenBlocked(controller, true);
        modal.setAttribute("aria-modal", "false");
        if (transition === "settled-modal") await whenBlocked(controller, false);
      }
      page.release();
      await pending;
      expect(controller.getSnapshot().blocked).toBe(false);
      expect(routePosition()).toEqual(before);
      expect(workspaceStore.getState().focusedPaneId).toBe(sourceId);
      expect(workspaceStore.getState().panes.map((item) => item.id)).toEqual([sourceId]);

      await controller.next();
      expect(window.location.pathname).toBe("/s/local%3Ab");
      expect(window.history.length).toBe(before.historyLength + 1);
      const destination = pane(workspaceStore.getState().focusedPaneId ?? "");
      expect(destination.params).toEqual({ ref: "local:b" });
      expect(destination.id).not.toBe(sourceId);
      expect(page.offsets).toEqual([0]);
    } finally {
      controller.dispose();
      modal.remove();
      closePalette();
    }
  },
);

test("non-modal popover mutations leave a held Next free to navigate", async () => {
  const page = await heldNeedsYouPage();
  const sourceId = workspaceStore.getState().openPane("session", { ref: "local:a" });
  window.history.replaceState({}, "", "/s/local%3Aa");
  const controller = createNeedsYouController();
  const popover = document.createElement("div");
  try {
    const before = window.history.length;
    const pending = controller.next();
    popover.setAttribute("role", "menu");
    document.body.append(popover);
    popover.setAttribute("aria-modal", "false");
    popover.append(document.createElement("span"));
    popover.remove();
    page.release();
    await pending;
    expect(controller.getSnapshot().blocked).toBe(false);
    expect(window.location.pathname).toBe("/s/local%3Ab");
    expect(window.history.length).toBe(before + 1);
    expect(workspaceStore.getState().focusedPaneId).not.toBe(sourceId);
    expect(pane(workspaceStore.getState().focusedPaneId ?? "").params).toEqual({ ref: "local:b" });
    expect(page.offsets).toEqual([0]);
  } finally {
    controller.dispose();
    popover.remove();
  }
});

test("same-generation reconnect refreshes later choices without reviving route-cancelled Next", async () => {
  const client = new FakeClient("ready");
  const initialization = await client.connect();
  const entered = deferred<void>();
  const response = deferred<void>();
  const reads: Array<{ offset: number; revision: number }> = [];
  let revision = 1;
  client.on("evener/navigation/read", async (params) => {
    const requestedRevision = revision;
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({ sections: { live: { count: 0 }, needs_you: { count: 3 }, pin_sections: { count: 0 } } }),
        `"manifest-${requestedRevision}"`,
        requestedRevision,
      );
    }
    if (params.resource !== "section" || params.section !== "needs_you") {
      throw new Error(`Unexpected navigation resource ${params.resource}`);
    }
    const offset = params.offset ?? -1;
    reads.push({ offset, revision: requestedRevision });
    if (offset === 2 && requestedRevision === 1) {
      entered.resolve();
      await response.promise;
    }
    return wireSnapshot(
      params,
      {
        sessions: [
          {
            ref:
              offset === 0
                ? "local:a"
                : offset === 1
                  ? requestedRevision === 1
                    ? "local:c"
                    : "local:updated"
                  : "local:b",
          },
        ],
        remaining: offset === 0 ? 2 : offset === 1 ? 1 : 0,
      },
      `"page-${offset}-${requestedRevision}"`,
      requestedRevision,
    );
  });
  navigationStore.init(client, capability());
  await whenNavigation((state) =>
    [...state.resources.values()].some(
      (resource) => resource.key.kind === "section" && !!resource.data && !resource.loading,
    ),
  );
  const sourceId = workspaceStore.getState().openPane("session", { ref: "local:c" });
  const source = pane(sourceId);
  window.history.replaceState({}, "", "/s/local%3Ac");
  const controller = createNeedsYouController();
  try {
    await controller.loadMore();
    expect(reads).toEqual([
      { offset: 0, revision: 1 },
      { offset: 1, revision: 1 },
    ]);
    expect(controller.getSnapshot().choices.map((choice) => choice.summary.ref)).toEqual(["local:a", "local:c"]);
    const generation = navigationStore.getState().clientGenerationID;
    const pending = controller.next();
    await entered.promise;
    navigate("/s/local%3Ax");
    const accepted = routePosition();
    client.emitStateChange("reconnecting");
    revision = 2;
    client.emitReady({ ...initialization, navigation: { ...capability(), sequence: 1 } });
    response.resolve();
    await pending;
    await whenNavigation((state) => {
      const sections = [...state.resources.values()].filter((resource) => resource.key.kind === "section");
      return (
        sections.length === 3 &&
        sections.every((resource) => resource.loadedRevision === 2 && !resource.loading && !resource.stale)
      );
    });
    expect(navigationStore.getState().clientGenerationID).toBe(generation);
    expect(navigationStore.getState().protocolError).toBeNull();
    expect(reads).toContainEqual({ offset: 1, revision: 2 });
    expect(controller.getSnapshot().choices.map((choice) => choice.summary.ref)).toEqual([
      "local:a",
      "local:updated",
      "local:b",
    ]);
    expect(controller.getSnapshot().choices.every((choice) => choice.selectable)).toBe(true);
    expect(routePosition()).toEqual(accepted);
    expect(workspaceStore.getState().focusedPaneId).toBe(sourceId);
    expect(workspaceStore.getState().panes).toEqual([source]);
    expect(pane(sourceId)).toBe(source);
  } finally {
    controller.dispose();
  }
});

test("dispose releases each real store, route, window and modal observer subscription exactly once", async () => {
  const navigationReleases: Array<ReturnType<typeof vi.fn>> = [];
  const workspaceReleases: Array<ReturnType<typeof vi.fn>> = [];
  const paletteReleases: Array<ReturnType<typeof vi.fn>> = [];
  const routeReleases: Array<ReturnType<typeof vi.fn>> = [];
  const subscribeNavigation = navigationStore.subscribe;
  const subscribeWorkspace = workspaceStore.subscribe;
  const subscribePalette = paletteStore.subscribe;
  const observeRoute = routing.observeRouteHistory;
  vi.spyOn(navigationStore, "subscribe").mockImplementation((listener) => {
    const release = vi.fn(subscribeNavigation(listener));
    navigationReleases.push(release);
    return release;
  });
  vi.spyOn(workspaceStore, "subscribe").mockImplementation((listener) => {
    const release = vi.fn(subscribeWorkspace(listener));
    workspaceReleases.push(release);
    return release;
  });
  vi.spyOn(paletteStore, "subscribe").mockImplementation((listener) => {
    const release = vi.fn(subscribePalette(listener));
    paletteReleases.push(release);
    return release;
  });
  vi.spyOn(routing, "observeRouteHistory").mockImplementation((listener) => {
    const release = vi.fn(observeRoute(listener));
    routeReleases.push(release);
    return release;
  });
  const addListener = vi.spyOn(window, "addEventListener");
  const removeListener = vi.spyOn(window, "removeEventListener");
  const observe = vi.spyOn(MutationObserver.prototype, "observe");
  const disconnect = vi.spyOn(MutationObserver.prototype, "disconnect");
  const controller = createNeedsYouController();
  const modal = document.createElement("div");
  try {
    const releases = [navigationReleases, workspaceReleases, paletteReleases, routeReleases];
    for (const group of releases) {
      expect(group).toHaveLength(1);
      expect(group[0]).not.toHaveBeenCalled();
    }
    const popListeners = addListener.mock.calls.filter(([type]) => type === "popstate");
    expect(popListeners).toHaveLength(1);
    expect(observe).toHaveBeenCalledTimes(1);
    expect(observe.mock.calls[0]?.[0]).toBe(document.documentElement);
    expect(disconnect).not.toHaveBeenCalled();
    const original = controller.getSnapshot();
    const notifications = vi.fn();
    const unsubscribe = controller.subscribe(notifications);
    modal.setAttribute("aria-modal", "true");
    document.body.append(modal);
    await whenBlocked(controller, true);
    expect(controller.getSnapshot()).not.toBe(original);
    expect(notifications).toHaveBeenCalledTimes(1);
    const blocked = controller.getSnapshot();
    expect(controller.getSnapshot()).toBe(blocked);
    unsubscribe();
    controller.dispose();
    controller.dispose();
    for (const group of releases) expect(group[0]).toHaveBeenCalledTimes(1);
    expect(disconnect).toHaveBeenCalledTimes(1);
    expect(disconnect.mock.contexts[0]).toBe(observe.mock.contexts[0]);
    expect(removeListener.mock.calls.filter(([type]) => type === "popstate")).toEqual([
      ["popstate", popListeners[0]?.[1]],
    ]);
    openPalette();
    navigate("/s/local%3Ax");
    expect(controller.getSnapshot()).toBe(blocked);
    expect(notifications).toHaveBeenCalledTimes(1);
  } finally {
    controller.dispose();
    modal.remove();
    closePalette();
    vi.restoreAllMocks();
  }
});
