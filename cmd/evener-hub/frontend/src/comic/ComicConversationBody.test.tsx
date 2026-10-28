import "fake-indexeddb/auto";
import type { ThreadTurnsListResponse } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { subagentOutcomesDelegatesResponse } from "@evener/appwire-client/testing/subagentWireFixtures";
import { act, cleanup, fireEvent, render, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { CommittedSession } from "../panes/session/testing/CommittedSession";
import { pagingByRef } from "../panes/session/transcript/transcriptPaging";
import { resetTranscriptPagingForTests } from "../panes/session/transcript/useTranscript";
import { ClientProvider } from "../shell/clientContext";
import { installKeybindings } from "../shell/installKeybindings";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { connectionStore } from "../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../stores/threads";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "../stores/transcriptDisplay";
import { type ComicReadingPosition, readComicPosition, writeComicPosition } from "./readerPosition";
import * as comicReader from "./useComicReader";

beforeEach(() => {
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  resetWorkspaceStoreForTests();
  resetThreadsStoreForTests();
  resetTranscriptDisplayStoreForTests();
  transcriptDisplayStore.getState().setPresentation("desktop", "comic");
  transcriptDisplayStore.getState().setPresentation("mobile", "comic");
  sessionStorage.clear();
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
    return this.hasAttribute("data-index") ? 40 : 500;
  });
  vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(760);
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(760);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(500);
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    return new DOMRect(0, 0, 760, this.style.height ? Number.parseFloat(this.style.height) : this.offsetHeight);
  });
  const original = Object.getOwnPropertyDescriptor(Range.prototype, "getBoundingClientRect");
  Object.defineProperty(Range.prototype, "getBoundingClientRect", {
    configurable: true,
    value: () => new DOMRect(0, 24, 8, 20),
  });
  onTestFinished(() => {
    if (original) Object.defineProperty(Range.prototype, "getBoundingClientRect", original);
    else Reflect.deleteProperty(Range.prototype, "getBoundingClientRect");
  });
});
afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", client: null });
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
  resetWorkspaceStoreForTests();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

let fixtureId = 0;
function setup(messageCount = 30, olderCursor?: string) {
  const root = `local:keyboard-clear-${++fixtureId}`;
  const child = `local:keyboard-child-${fixtureId}`;
  const fixture = subagentOutcomesDelegatesResponse();
  const initialSessionId = root.slice(root.indexOf(":") + 1);
  let sessionId = initialSessionId;
  let instanceId = "first";
  const thread = (ref: string) =>
    wireThread(ref, {
      id: "stable-wire-thread",
      sessionId: ref === root ? sessionId : child.slice(child.indexOf(":") + 1),
      evener: { ...wireThread(ref).evener, instanceId },
      turns: [
        {
          id: "turn",
          status: "completed",
          itemsView: "full",
          items: Array.from({ length: messageCount }, (_, n) => ({
            id: `message-${n}`,
            type: "agentMessage",
            text: `MESSAGE_${sessionId}_${n}`,
          })),
        },
      ],
    });
  const client = new FakeClient("ready");
  client.on("thread/read", (p) => ({ thread: thread(p.ref ?? root), olderCursor }));
  client.on("thread/unsubscribe", () => ({}));
  const context = (ref: string) => ({
    ...fixture.context,
    ref,
    sessionId: ref === root ? sessionId : child.slice(child.indexOf(":") + 1),
  });
  const counts = { known: true, total: 1, active: 0, failed: 0, completed: 1 };
  client.on("evener/thread/activity/read", (p) => ({
    context: context(p.ref),
    scope: "session",
    delegates: counts,
    jobs: counts,
    watches: counts,
  }));
  client.on("evener/thread/delegates/list", (p) => ({
    ...fixture,
    context: context(p.ref),
    scope: "session",
    delegates:
      p.ref === root && sessionId === initialSessionId
        ? fixture.delegates.slice(0, 1).map((row) => ({ ...row, ownerRef: root, childRef: child }))
        : [],
  }));
  connectionStore.getState().connect(client);
  const mount = (paneId: string) =>
    render(
      <ClientProvider client={client}>
        <CommittedSession paneId={paneId} params={{ ref: root }} focused />
      </ClientProvider>,
    );
  return {
    root,
    child,
    client,
    mount,
    holdRootReads: () => {
      let release: () => void = () => {};
      const ready = new Promise<void>((resolve) => {
        release = resolve;
      });
      client.on("thread/read", async (p) => {
        if (p.ref === root) await ready;
        return { thread: thread(p.ref ?? root), olderCursor };
      });
      return release;
    },
    restart: async () => {
      instanceId = "restarted";
      await act(async () =>
        client.emitNotification({
          method: "evener/thread/resync",
          params: { ref: root, threadId: "stable-wire-thread" },
        }),
      );
      await waitFor(() => expect(threadsStore.getState().threads.get(root)?.instanceId).toBe(instanceId));
    },
    clear: async () => {
      sessionId = "replacement";
      client.on("thread/clear", (p) => ({
        ref: root,
        receipt: {
          clientMutationId: p.clientMutationId,
          disposition: "applied",
          threadId: "stable-wire-thread",
          instanceId,
          projectionState: "reflected",
        },
        thread: thread(root),
      }));
      await act(async () => {
        await threadsStore.getState().clearThread(root);
      });
    },
  };
}

test.each(
  ["root", "expanded child"].flatMap((target) =>
    ["comic", "standard", "unmounted"].map((during) => ({ target, during })),
  ),
)(
  "Clear during $during resets the retained $target anchor, but an instance restart does not",
  async ({ target, during }) => {
    const nativeScrollTo = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "scrollTo");
    Object.defineProperty(HTMLElement.prototype, "scrollTo", {
      configurable: true,
      value(this: HTMLElement, options: ScrollToOptions) {
        this.scrollTop = options.top ?? this.scrollTop;
      },
    });
    onTestFinished(() => {
      if (nativeScrollTo) Object.defineProperty(HTMLElement.prototype, "scrollTo", nativeScrollTo);
      else Reflect.deleteProperty(HTMLElement.prototype, "scrollTo");
    });
    vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockReturnValue(6000);
    vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(4000);
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
      return this.hasAttribute("data-index") ? 40 : 4000;
    });
    const { root, child, client, mount, holdRootReads, restart, clear } = setup(2);
    const actual = comicReader.useComicReader;
    const readers = new Map<string, ReturnType<typeof actual>>();
    const allocations: { id: string; sessionRef: string | undefined; position: ComicReadingPosition | undefined }[] =
      [];
    const childAllocations = new Set<string>();
    vi.spyOn(comicReader, "useComicReader").mockImplementation((...args) => {
      const result = actual(...args);
      if (args[0] === child) childAllocations.add(result.reader.id);
      if (args[0] === root && !allocations.some((row) => row.id === result.reader.id)) {
        allocations.push({ id: result.reader.id, sessionRef: args[2], position: result.position });
      }
      readers.set(args[0], result);
      return result;
    });
    let mounted = mount("clear-pane");
    await waitFor(() => expect(mounted.container.querySelectorAll("[data-comic-entry]").length).toBeGreaterThan(0));
    let path: string[] = [];
    if (target === "expanded child") {
      await waitFor(() => expect(mounted.container.querySelector(`[data-comic-child-ref="${child}"]`)).not.toBeNull());
      const strip = mounted.container.querySelector<HTMLElement>(`[data-comic-child-ref="${child}"]`);
      if (!strip) throw Error("missing child strip");
      fireEvent.click(within(strip).getByRole("button", { name: /expand/i }));
      await waitFor(() => expect(readers.get(child)?.reader.alive).toBe(true));
      const source = mounted.container.querySelector<HTMLElement>(`[data-comic-session="${child}"]`);
      if (!source) throw Error("missing expanded child source");
      path = JSON.parse(source.dataset.comicPath ?? "[]");
    }
    const initial = readers.get(root);
    if (!initial) throw Error("missing root reader");
    await waitFor(() =>
      expect(
        mounted.container.querySelector(`[data-comic-entry][data-comic-session="${path.length ? child : root}"]`),
      ).not.toBeNull(),
    );
    const source = mounted.container.querySelector<HTMLElement>(
      `[data-comic-entry][data-comic-session="${path.length ? child : root}"]`,
    );
    if (!source?.dataset.comicEntry) throw Error("missing projected source");
    const position: ComicReadingPosition = {
      mode: "away",
      sessionRef: path.length ? child : root,
      path,
      entryId: source.dataset.comicEntry,
      offset: 7,
      viewportOffset: 24,
    };
    act(() => initial.save(position));
    // Re-enter the same retained seat so ComicTranscript adopts the saved
    // source as its active anchor, rather than testing storage alone.
    await act(async () => transcriptDisplayStore.getState().setPresentation("desktop", "standard"));
    await act(async () => transcriptDisplayStore.getState().setPresentation("desktop", "comic"));
    await waitFor(() => expect(readers.get(root)?.reader.alive).toBe(true));
    const old = readers.get(root);
    if (!old) throw Error("missing restored reader");
    const port = mounted.container.querySelector<HTMLElement>("[data-comic-scroll] > div");
    if (!port) throw Error("missing restored port");
    Object.defineProperty(port, "scrollHeight", { configurable: true, value: 6000 });
    Object.defineProperty(port, "scrollTo", {
      configurable: true,
      value(options: ScrollToOptions) {
        port.scrollTop = options.top ?? port.scrollTop;
      },
    });
    port.scrollTop = 100;
    await restart();
    expect(readers.get(root)).toBe(old);
    expect(readComicPosition(root)).toEqual(position);
    if (path.length) await waitFor(() => expect(readers.get(child)?.reader.alive).toBe(true));
    const oldChild = readers.get(child)?.reader;
    const childAllocationCount = childAllocations.size;
    const childUnsubscribes = () =>
      client.calls.filter(
        (call) => call.method === "thread/unsubscribe" && (call.params as { ref: string }).ref === child,
      );
    const unsubscribesBeforeClear = childUnsubscribes().length;
    if (during === "standard") {
      await act(async () => transcriptDisplayStore.getState().setPresentation("desktop", "standard"));
    } else if (during === "unmounted") {
      mounted.unmount();
    }
    await clear();
    // Final-owner retirement deliberately performs one last full read. Wait
    // for its actual wire release before measuring any forbidden reacquisition.
    if (oldChild) {
      await waitFor(() => expect(childUnsubscribes().length).toBeGreaterThan(unsubscribesBeforeClear));
      expect(oldChild.alive).toBe(false);
    }
    expect(childAllocations.size).toBe(childAllocationCount);
    expect(pagingByRef.get(child)?.paneConsumers.size ?? 0).toBe(0);
    const childReads = () =>
      client.calls.filter((call) => call.method === "thread/read" && (call.params as { ref: string }).ref === child);
    const readsAfterClear = childReads().length;
    if (during === "standard") {
      await act(async () => transcriptDisplayStore.getState().setPresentation("desktop", "comic"));
    } else if (during === "unmounted") {
      await act(async () => {
        mounted = mount("fresh-seat-after-clear");
      });
    }
    await waitFor(() => expect(threadsStore.getState().threads.get(root)?.imageSessionId).toBe("replacement"));
    expect(allocations.at(-1)?.position).toEqual({ mode: "end", sessionRef: root, path: [] });
    await waitFor(() => expect(readers.get(root)?.position).toEqual({ mode: "end", sessionRef: root, path: [] }));
    expect(old.reader.alive).toBe(false);
    expect(readComicPosition(root)).toEqual({ mode: "end", sessionRef: root, path: [] });
    const currentPort = mounted.container.querySelector<HTMLElement>("[data-comic-scroll] > div");
    await waitFor(() => expect(currentPort?.scrollTop).toBe(2000));
    expect(childReads()).toHaveLength(readsAfterClear);
    expect(childAllocations.size).toBe(childAllocationCount);
    expect(pagingByRef.get(child)?.paneConsumers.size ?? 0).toBe(0);
    mounted.unmount();
    await waitFor(() => expect(threadsStore.getState().threads.has(root)).toBe(false));
    const releaseRootReads = holdRootReads();
    onTestFinished(releaseRootReads);
    const rootReads = () =>
      client.calls.filter((call) => call.method === "thread/read" && (call.params as { ref: string }).ref === root);
    const readsBeforeRemount = rootReads().length;
    const allocationsBeforeRemount = allocations.length;
    mounted = mount("clear-pane");
    // Alive is only the reader's committed lifetime. Before hydration it may
    // still carry this seat's old stamp, but no transcript may consume it.
    const loadingReader = readers.get(root)?.reader;
    expect(loadingReader?.alive).toBe(true);
    expect(threadsStore.getState().threads.has(root)).toBe(false);
    expect(mounted.container.querySelectorAll("[data-comic-entry]")).toHaveLength(0);
    expect(mounted.container.querySelector("[data-comic-scroll]")).toBeNull();
    expect(childReads()).toHaveLength(readsAfterClear);
    expect(childAllocations.size).toBe(childAllocationCount);
    expect(pagingByRef.get(child)?.paneConsumers.size ?? 0).toBe(0);
    expect(readComicPosition(root)).toEqual({ mode: "end", sessionRef: root, path: [] });
    await waitFor(() => expect(rootReads().length).toBeGreaterThan(readsBeforeRemount));
    await act(async () => releaseRootReads());
    await waitFor(() => {
      expect(threadsStore.getState().threads.get(root)?.imageSessionId).toBe("replacement");
      expect(mounted.container.querySelectorAll("[data-comic-entry]").length).toBeGreaterThan(0);
    });
    const renderedText = [...mounted.container.querySelectorAll("[data-comic-text]")]
      .map((node) => node.textContent)
      .join("");
    expect(renderedText).toContain("MESSAGE_replacement_0");
    expect(renderedText).not.toContain(`MESSAGE_${root.slice(root.indexOf(":") + 1)}_`);
    const hydratedAllocations = allocations
      .slice(allocationsBeforeRemount)
      .filter((row) => row.sessionRef !== undefined);
    expect(hydratedAllocations.length).toBeGreaterThan(0);
    for (const allocation of hydratedAllocations) {
      expect(allocation.sessionRef).toBe("local:replacement");
      expect(allocation.position).toEqual({ mode: "end", sessionRef: root, path: [] });
    }
    expect(loadingReader?.alive).toBe(false);
    expect(readers.get(root)?.reader.alive).toBe(true);
    expect(readers.get(root)?.position).toEqual({ mode: "end", sessionRef: root, path: [] });
    expect(readComicPosition(root)).toEqual({ mode: "end", sessionRef: root, path: [] });
    expect(childReads()).toHaveLength(readsAfterClear);
    expect(childAllocations.size).toBe(childAllocationCount);
    expect(pagingByRef.get(child)?.paneConsumers.size ?? 0).toBe(0);
  },
);

test("Comic registers focused-seat transcript actions outside its scroll port", async () => {
  const { root, mount } = setup();
  installKeybindings();
  const first = mount("keyboard-first");
  const second = mount("keyboard-second");
  await waitFor(() => expect(first.container.querySelectorAll("[data-comic-row]").length).toBeGreaterThan(1));
  await waitFor(() => expect(second.container.querySelectorAll("[data-comic-row]").length).toBeGreaterThan(1));
  const a = first.container.querySelector<HTMLElement>("[data-comic-scroll] > div");
  const b = second.container.querySelector<HTMLElement>("[data-comic-scroll] > div");
  if (!a || !b) throw Error("missing scroll ports");
  for (const port of [a, b]) {
    Object.defineProperty(port, "scrollHeight", { configurable: true, value: 1500 });
    Object.defineProperty(port, "scrollTo", {
      configurable: true,
      value(options: ScrollToOptions) {
        port.scrollTop = options.top ?? port.scrollTop;
      },
    });
  }
  a.scrollTop = 200;
  b.scrollTop = 200;
  workspaceStore.setState({ focusedPaneId: "keyboard-second" });
  const press = (key: string) =>
    act(() => {
      document.body.dispatchEvent(
        new KeyboardEvent("keydown", { key, code: key, altKey: true, bubbles: true, cancelable: true }),
      );
    });
  press("ArrowUp");
  expect(a.scrollTop).toBe(200);
  expect(b.scrollTop).toBe(160);
  press("ArrowDown");
  expect(b.scrollTop).toBe(200);
  press("Home");
  await waitFor(() => expect(b.scrollTop).toBe(0));
  const backgroundOffset = a.scrollTop;
  press("End");
  expect(readComicPosition(root)).toEqual({
    mode: "end",
    sessionRef: root,
    path: [],
  });
  expect(a.scrollTop).toBe(backgroundOffset);
  await waitFor(() => expect(b.scrollTop).toBe(1000));
});

test.each(["ArrowUp", "ArrowDown", "Home", "End"])(
  "focused %s cancels real pending restoration, unfocused action does not",
  async (key) => {
    const { root, client, mount } = setup(2, "96");
    let reply: ((value: ThreadTurnsListResponse) => void) | undefined;
    client.on(
      "thread/turns/list",
      () =>
        new Promise<ThreadTurnsListResponse>((resolve) => {
          reply = resolve;
        }),
    );
    writeComicPosition(root, {
      mode: "away",
      sessionRef: root,
      path: [],
      entryId: "absent-source",
      offset: 0,
      viewportOffset: 24,
    });
    const mounted = mount("pending-keyboard");
    await waitFor(() => expect(reply).toBeDefined());
    const port = mounted.container.querySelector<HTMLElement>("[data-comic-scroll] > div");
    if (!port) throw Error("missing scroll port");
    Object.defineProperty(port, "scrollTo", {
      configurable: true,
      value(options: ScrollToOptions) {
        port.scrollTop = options.top ?? port.scrollTop;
      },
    });
    port.scrollTop = 200;
    const press = () =>
      act(() => {
        document.body.dispatchEvent(
          new KeyboardEvent("keydown", { key, code: key, altKey: true, bubbles: true, cancelable: true }),
        );
      });
    workspaceStore.setState({ focusedPaneId: "other-pane" });
    press();
    expect(pagingByRef.get(root)?.paneConsumers.size).toBe(1);
    workspaceStore.setState({ focusedPaneId: "pending-keyboard" });
    press();
    expect(pagingByRef.get(root)?.paneConsumers.size ?? 0).toBe(0);
    await act(async () => {
      reply?.({ data: [], nextCursor: "56" });
    });
    expect(client.calls.filter((call) => call.method === "thread/turns/list")).toHaveLength(1);
  },
);
