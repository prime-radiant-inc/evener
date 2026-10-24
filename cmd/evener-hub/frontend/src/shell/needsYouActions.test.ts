// @vitest-environment jsdom

import type { ThreadModel } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { capability, manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { afterEach, expect, test, vi } from "vitest";
import { navigationStore, resetNavigationStoreForTests } from "../stores/navigation/store";
import { threadsStore } from "../stores/threads";
import "../panes/session";
import "../panes/transcript";
import "../panes/zoom";
import { enterAgentCascade, popAgentCascade, reconcileCascadeBinding } from "../panes/zoom/actions";
import { activityDelegate } from "../stores/sessionActivityTestUtils";
import { chromeStore, resetChromeStoreForTests } from "./chromeStore";
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
  resetChromeStoreForTests();
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

test("demand retries a failed later page at its own offset before advancing", async () => {
  const client = new FakeClient("ready");
  const offsets: number[] = [];
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "retry-later-generation",
          sections: { live: { count: 0 }, needs_you: { count: 52 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "retry-later-generation",
      );
    }
    if (params.resource === "section") {
      const offset = params.offset ?? -1;
      offsets.push(offset);
      const sessions =
        offset === 0
          ? Array.from({ length: 50 }, (_, index) => ({ ref: `local:first-${index}` }))
          : [{ ref: "local:later-a" }, { ref: "local:later-b" }];
      return wireSnapshot(params, { sessions, remaining: offset === 0 ? 2 : 0 }, '"page"', 1, "retry-later-generation");
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("retry-later-generation"));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const controller = createNeedsYouController();
  // Page zero arrives with the boot manifest; one More loads the second page.
  await controller.loadMore();
  expect(offsets).toEqual([0, 50]);

  // A refresh of the loaded later page fails: the revalidator keeps the rows
  // it already has and marks the page stale with the error, so the page's
  // choices are unusable until that exact page is read again.
  const pageKey = { kind: "section", section: "needs_you", offset: 50, limit: 50 } as const;
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(pageKey), {
    key: pageKey,
    data: {
      generation_id: "retry-later-generation",
      revision: 1,
      sessions: [
        {
          ref: "local:later-a",
          host_id: "local",
          session_id: "local:later-a",
          title: "local:later-a",
          project: "Proj",
          state: "awaiting",
          kind: "session",
          live: true,
          children: [],
        },
      ],
      remaining: 0,
      truncated: false,
    },
    loadedRevision: 1,
    targetRevision: 1,
    forceToken: 0,
    etag: '"page"',
    loading: false,
    stale: true,
    error: new Error("transport failure"),
    generationID: "retry-later-generation",
  });
  const visibleResourceKeys = new Set(navigationStore.getState().visibleResourceKeys);
  visibleResourceKeys.add(keyID(pageKey));
  navigationStore.setState({ resources, visibleResourceKeys });

  await controller.loadMore();
  // The retry asks for the failed page itself, not the offset past it.
  expect(offsets.at(-1)).toBe(50);
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

test("the modal-open counter advances on every modal open, including a transient one, and not on closes", async () => {
  const controller = createNeedsYouController();
  const modal = document.createElement("div");
  const transient = document.createElement("div");
  transient.setAttribute("aria-modal", "true");
  try {
    const initial = controller.getSnapshot().modalOpenCount;
    modal.setAttribute("aria-modal", "true");
    document.body.append(modal);
    await whenBlocked(controller, true);
    expect(controller.getSnapshot().modalOpenCount).toBe(initial + 1);

    // A close is invisible to the counter, which keeps an older dialog's close
    // from consuming a pending palette selection.
    modal.setAttribute("aria-modal", "false");
    await whenBlocked(controller, false);
    expect(controller.getSnapshot().modalOpenCount).toBe(initial + 1);

    // An open and close in one mutation batch still advance the counter and
    // refresh the snapshot even though the public blocked flag never changes.
    const notifications = vi.fn();
    const unsubscribe = controller.subscribe(notifications);
    document.body.append(transient);
    transient.remove();
    await Promise.resolve();
    expect(controller.getSnapshot().blocked).toBe(false);
    expect(controller.getSnapshot().modalOpenCount).toBe(initial + 2);
    expect(notifications).not.toHaveBeenCalled();
    unsubscribe();

    // A modal opening while another is already open is still a new open intent,
    // and it notifies so a pending selection can cancel while still blocked.
    modal.setAttribute("aria-modal", "true");
    await whenBlocked(controller, true);
    const withOneOpen = controller.getSnapshot().modalOpenCount;
    const notified = vi.fn();
    const unsubscribeNotified = controller.subscribe(notified);
    const second = document.createElement("div");
    second.setAttribute("aria-modal", "true");
    document.body.append(second);
    await Promise.resolve();
    expect(controller.getSnapshot().modalOpenCount).toBe(withOneOpen + 1);
    expect(notified).toHaveBeenCalledTimes(1);
    second.remove();
    await Promise.resolve();
    expect(controller.getSnapshot().modalOpenCount).toBe(withOneOpen + 1);
    unsubscribeNotified();

    modal.setAttribute("aria-modal", "false");
    await whenBlocked(controller, false);
    expect(controller.getSnapshot().modalOpenCount).toBe(withOneOpen + 1);
  } finally {
    controller.dispose();
    modal.remove();
    transient.remove();
  }
});

test("an aria-modal that flips true and back before the observer runs still counts as an open", async () => {
  const older = document.createElement("div");
  older.setAttribute("aria-modal", "true");
  document.body.append(older);
  const flip = document.createElement("div");
  document.body.append(flip);
  const controller = createNeedsYouController();
  try {
    const initial = controller.getSnapshot().modalOpenCount;
    const notifications = vi.fn();
    const unsubscribe = controller.subscribe(notifications);
    // The observer delivers two records for this element, with old values
    // absent and "true", but by the time it runs the element reads "false".
    // Neither record's current value can prove the open, so the pair must.
    flip.setAttribute("aria-modal", "true");
    flip.setAttribute("aria-modal", "false");
    await Promise.resolve();
    expect(controller.getSnapshot().modalOpenCount).toBe(initial + 1);
    expect(notifications).toHaveBeenCalledTimes(1);
    // The older dialog is still open, so the preserved state is still blocked.
    expect(controller.getSnapshot().blocked).toBe(true);
    expect(older.getAttribute("aria-modal")).toBe("true");

    // The mirror: a plain close never advances the counter.
    older.setAttribute("aria-modal", "false");
    await whenBlocked(controller, false);
    expect(controller.getSnapshot().modalOpenCount).toBe(initial + 1);
    unsubscribe();
  } finally {
    controller.dispose();
    older.remove();
    flip.remove();
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

// A cascade can rebind its focused leaf to a different backing session behind
// identical params (a resume retypes it in place). A pending Next captured before
// that must not act for the reader that was replaced.
test("a held next response does not act after the focused cascade rebinds its leaf", async () => {
  const client = new FakeClient("ready");
  let releasePage!: () => void;
  let entered!: () => void;
  const pageEntered = new Promise<void>((resolve) => (entered = resolve));
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "cascade-rebind",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "cascade-rebind",
      );
    }
    if (params.resource === "section") {
      entered();
      return new Promise((resolve) => {
        releasePage = () =>
          resolve(
            wireSnapshot(params, { sessions: [{ ref: "local:target" }], remaining: 0 }, '"page"', 1, "cascade-rebind"),
          );
      });
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("cascade-rebind"));
  const paneId = workspaceStore
    .getState()
    .openPane(
      "sessionZoom",
      { ref: "local:leaf", source: { type: "session", params: { ref: "local:leaf" } }, edges: [] },
      { slot: "main" },
    );
  workspaceStore.getState().focusPane(paneId);
  const pane = workspaceStore.getState().panes.find((item) => item.id === paneId);
  if (!pane) throw new Error("missing cascade pane");
  const lifetime = conversationPaneLifetime(pane);
  lifetime.resolvedSessions.set("local:leaf", "session-before");
  const controller = createNeedsYouController();
  const before = { path: window.location.pathname, history: window.history.length };

  const pending = controller.next();
  await pageEntered;
  reconcileCascadeBinding(pane, "local:leaf", "session-after");
  releasePage();
  await pending;

  expect(window.location.pathname).toBe(before.path);
  expect(window.history.length).toBe(before.history);
  controller.dispose();
});

// Clear replaces the focused thread's identity in place: same pane, ref, route
// and navigation generation. A pending Next captured before that must not take
// focus when its page finally arrives.
test("a held next response does not act after the focused session is cleared in place", async () => {
  const client = new FakeClient("ready");
  let releasePage!: () => void;
  let entered!: () => void;
  const pageEntered = new Promise<void>((resolve) => (entered = resolve));
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "cleared-source",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "cleared-source",
      );
    }
    if (params.resource === "section") {
      entered();
      return new Promise((resolve) => {
        releasePage = () =>
          resolve(
            wireSnapshot(params, { sessions: [{ ref: "local:target" }], remaining: 0 }, '"page"', 1, "cleared-source"),
          );
      });
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("cleared-source"));
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  workspaceStore.getState().focusPane(a);
  const model = { threadId: "thread-a", instanceId: "instance-before-clear" } as unknown as ThreadModel;
  threadsStore.setState({ threads: new Map([["local:a", model]]) });
  const before = { path: window.location.pathname, history: window.history.length };
  const controller = createNeedsYouController();
  const pending = controller.next();
  await pageEntered;

  // What a clear publishes: the same thread with a different fused identity.
  threadsStore.setState({
    threads: new Map([["local:a", { ...model, instanceId: "instance-after-clear" }]]),
  });

  releasePage();
  await pending;
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
  expect(window.location.pathname).toBe(before.path);
  expect(window.history.length).toBe(before.history);
  controller.dispose();
});

// A retry of a populated page keeps its rows and clears the error while it is in
// flight. If demand went away then, the control would unmount and its visibility
// effect would cancel the navigation that started the retry.
test("a populated page's retry keeps demand available while it is in flight", async () => {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "retrying-page",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "retrying-page",
      );
    }
    if (params.resource === "section") {
      return wireSnapshot(
        params,
        {
          sessions: [
            {
              ref: "local:a",
              host_id: "local",
              session_id: "local:a",
              title: "local:a",
              project: "Proj",
              state: "awaiting",
              kind: "session",
              live: true,
              children: [],
            },
          ],
          remaining: 0,
        },
        '"page"',
        1,
        "retrying-page",
      );
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("retrying-page"));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const pageKey = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;
  const page = navigationStore.getState().resources.get(keyID(pageKey));
  if (!page) throw new Error("missing needs-you page");
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(pageKey), { ...page, loading: true, stale: true, error: null });
  const visibleResourceKeys = new Set(navigationStore.getState().visibleResourceKeys);
  visibleResourceKeys.add(keyID(pageKey));
  navigationStore.setState({ resources, visibleResourceKeys });

  const controller = createNeedsYouController();
  expect(controller.getSnapshot().canLoadMore).toBe(true);
  controller.dispose();
});

// Mod+J works before the first connection arrives. Navigation has no client
// then, so the demand that activation asks for cannot run; the activation is
// void-ed by its callers, so a rejection here would be unhandled.
test("an activation before navigation initializes neither rejects nor navigates", async () => {
  const controller = createNeedsYouController();
  const before = window.location.pathname;
  try {
    await expect(controller.next()).resolves.toBeUndefined();
    expect(window.location.pathname).toBe(before);
  } finally {
    controller.dispose();
  }
});

// A Dockview popout is a separate document. A modal opened there while Next is
// waiting for a page has to block (and cancel) that action; watching only the
// opener's document let the late page navigate away and dismiss the dialog.
test("a modal in the focused reader's own document blocks a pending next", async () => {
  const client = new FakeClient("ready");
  let releasePage!: () => void;
  let entered!: () => void;
  const pageEntered = new Promise<void>((resolve) => (entered = resolve));
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "popout-modal",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "popout-modal",
      );
    }
    if (params.resource === "section") {
      entered();
      return new Promise((resolve) => {
        releasePage = () =>
          resolve(
            wireSnapshot(params, { sessions: [{ ref: "local:target" }], remaining: 0 }, '"page"', 1, "popout-modal"),
          );
      });
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("popout-modal"));
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  workspaceStore.getState().focusPane(a);
  const controller = createNeedsYouController();
  const before = { path: window.location.pathname, history: window.history.length };
  const pending = controller.next();
  await pageEntered;

  // The reader is adopted into a popout, and a dialog opens there.
  const popout = document.implementation.createHTMLDocument("popout");
  const dialog = popout.createElement("div");
  dialog.setAttribute("aria-modal", "true");
  popout.body.appendChild(dialog);
  chromeStore.getState().setPaneDocumentFor(a, popout);
  await Promise.resolve();
  expect(controller.getSnapshot().blocked).toBe(true);

  releasePage();
  await pending;
  expect(workspaceStore.getState().focusedPaneId).toBe(a);
  expect(window.location.pathname).toBe(before.path);
  expect(window.history.length).toBe(before.history);
  controller.dispose();
});

// A Dockview popout is a separate window with its own constructors, so an
// opener-realm `instanceof Element` rejects every node the popout creates.
// An iframe is the only jsdom document with a distinct realm;
// `createHTMLDocument` shares the opener's constructors and cannot catch this.
test("a transient modal in a real popout window cancels a pending next", async () => {
  const client = new FakeClient("ready");
  let releasePage!: () => void;
  let entered!: () => void;
  const pageEntered = new Promise<void>((resolve) => (entered = resolve));
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "popout-realm",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "popout-realm",
      );
    }
    if (params.resource === "section") {
      entered();
      return new Promise((resolve) => {
        releasePage = () =>
          resolve(
            wireSnapshot(params, { sessions: [{ ref: "local:target" }], remaining: 0 }, '"page"', 1, "popout-realm"),
          );
      });
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("popout-realm"));
  const a = workspaceStore.getState().openPane("session", { ref: "local:a" });
  workspaceStore.getState().focusPane(a);
  const frame = document.createElement("iframe");
  document.body.appendChild(frame);
  const popout = frame.contentDocument;
  if (!popout) throw new Error("missing popout document");
  chromeStore.getState().setPaneDocumentFor(a, popout);
  const controller = createNeedsYouController();
  const before = { path: window.location.pathname, history: window.history.length };
  try {
    const pending = controller.next();
    await pageEntered;
    const countBefore = controller.getSnapshot().modalOpenCount;

    // Opened and closed before the observer's callback runs: only the records
    // prove the interaction happened.
    const dialog = popout.createElement("div");
    dialog.setAttribute("aria-modal", "true");
    popout.body.appendChild(dialog);
    dialog.remove();
    await Promise.resolve();
    expect(controller.getSnapshot().modalOpenCount).toBe(countBefore + 1);

    releasePage();
    await pending;
    expect(window.location.pathname).toBe(before.path);
    expect(window.history.length).toBe(before.history);
  } finally {
    controller.dispose();
    frame.remove();
  }
});

// A popout can publish its document while another pane is focused, and focusing
// it later writes nothing new to chromeStore. The modal watch has to follow the
// focus itself, or a dialog opened there cannot cancel a pending Next.
test("focusing an already-published popout watches its modals", async () => {
  const client = new FakeClient("ready");
  let releasePage!: () => void;
  let entered!: () => void;
  const pageEntered = new Promise<void>((resolve) => (entered = resolve));
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "focus-popout",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "focus-popout",
      );
    }
    if (params.resource === "section") {
      entered();
      return new Promise((resolve) => {
        releasePage = () =>
          resolve(
            wireSnapshot(params, { sessions: [{ ref: "local:target" }], remaining: 0 }, '"page"', 1, "focus-popout"),
          );
      });
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("focus-popout"));
  const a = workspaceStore.getState().openPane("session", { ref: "ref_a" });
  const b = workspaceStore.getState().openPane("session", { ref: "ref_b" }, { slot: "secondary" });
  workspaceStore.getState().focusPane(a);

  const popout = document.implementation.createHTMLDocument("popout");
  // Published while another pane is focused: nothing new is written later.
  chromeStore.getState().setPaneDocumentFor(b, popout);
  await Promise.resolve();

  const controller = createNeedsYouController();
  const before = { path: window.location.pathname, history: window.history.length };
  const pending = controller.next();
  await pageEntered;

  workspaceStore.getState().focusPane(b);
  await Promise.resolve();
  const dialog = popout.createElement("div");
  dialog.setAttribute("aria-modal", "true");
  popout.body.appendChild(dialog);
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(controller.getSnapshot().blocked).toBe(true);

  releasePage();
  await pending;
  expect(window.location.pathname).toBe(before.path);
  expect(window.history.length).toBe(before.history);
  controller.dispose();
});

// A dialog in a popout the reader has left must not keep attention navigation
// blocked in the window they came back to.
test("a modal in an unfocused popout stops blocking after focus returns", async () => {
  const client = new FakeClient("ready");
  client.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return wireSnapshot(
        params,
        manifest({
          generation_id: "unfocused-modal",
          sections: { live: { count: 0 }, needs_you: { count: 1 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "unfocused-modal",
      );
    }
    if (params.resource === "section") {
      return wireSnapshot(params, { sessions: [{ ref: "local:a" }], remaining: 0 }, '"page"', 1, "unfocused-modal");
    }
    throw new Error(`unexpected resource ${params.resource}`);
  });
  navigationStore.init(client, capability("unfocused-modal"));
  const a = workspaceStore.getState().openPane("session", { ref: "ref_a" });
  const b = workspaceStore.getState().openPane("session", { ref: "ref_b" }, { slot: "secondary" });
  workspaceStore.getState().focusPane(b);

  const controller = createNeedsYouController();
  const popout = document.implementation.createHTMLDocument("popout");
  const dialog = popout.createElement("div");
  dialog.setAttribute("aria-modal", "true");
  popout.body.appendChild(dialog);
  chromeStore.getState().setPaneDocumentFor(b, popout);
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(controller.getSnapshot().blocked).toBe(true);

  workspaceStore.getState().focusPane(a);
  await new Promise((resolve) => setTimeout(resolve, 0));
  expect(controller.getSnapshot().blocked).toBe(false);
  controller.dispose();
});
