import "fake-indexeddb/auto";
import { type ThreadReadResponse, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { subagentOutcomesDelegatesResponse } from "@evener/appwire-client/testing/subagentWireFixtures";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { readComposerDraftSnapshot, writeComposerDraft } from "../panes/session/composer/draft";
import { CommittedSession } from "../panes/session/testing/CommittedSession";
import { resetTranscriptPagingForTests } from "../panes/session/transcript/useTranscript";
import { ClientProvider } from "../shell/clientContext";
import { navigate, paneToURL } from "../shell/routing";
import { resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import { connectionStore } from "../stores/connection";
import { installHydrationRetrySchedulerForTests, resetThreadsStoreForTests, threadsStore } from "../stores/threads";
import { resetTranscriptDisplayStoreForTests, transcriptDisplayStore } from "../stores/transcriptDisplay";
import { readComicPosition, writeComicPosition } from "./readerPosition";

function page(ref: string) {
  return (
    <ClientProvider client={connectionStore.getState().client!}>
      <CommittedSession key={ref} paneId="comic-session-pane" params={{ ref }} focused />
    </ClientProvider>
  );
}
beforeEach(() => {
  resetWorkspaceStoreForTests();
  resetTranscriptDisplayStoreForTests();
  transcriptDisplayStore.getState().setPresentation("desktop", "comic");
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
});
afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  sessionStorage.clear();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
  resetWorkspaceStoreForTests();
});
test("a selected existing session keeps the normal route and one workspace pane", () => {
  const ref = "local:existing";
  const client = new FakeClient("ready");
  client.on("thread/read", () => new Promise(() => {}));
  connectionStore.getState().connect(client);
  window.history.replaceState(null, "", paneToURL("session", { ref }));
  const mounted = render(page(ref));
  expect(window.location.pathname).toBe(paneToURL("session", { ref }));
  expect(workspaceStore.getState().panes).toHaveLength(1);
  expect(screen.getByRole("button", { name: "Verbosity" })).toBeTruthy();
  expect(threadsStore.getState().threads.size).toBe(0);
  mounted.unmount();
});

test.each(["deleted", "recoverable", "repairing", "superseded"] as const)(
  "isolates an expanded %s child while preserving saved intent and the selected root",
  async (outcome) => {
    vi.stubGlobal(
      "ResizeObserver",
      class {
        observe() {}
        unobserve() {}
        disconnect() {}
      },
    );
    vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockImplementation(function (this: HTMLElement) {
      return this.hasAttribute("data-index") ? 40 : 1000;
    });
    vi.spyOn(HTMLElement.prototype, "offsetWidth", "get").mockReturnValue(760);
    vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(1000);
    const fixture = subagentOutcomesDelegatesResponse();
    const root = fixture.context.ref;
    const child = `local:child-dlg_failed-${outcome}`;
    const sibling = `local:child-dlg_reported-${outcome}`;
    const key = JSON.stringify([root, "dlg_failed"]);
    const saved = {
      mode: "away" as const,
      sessionRef: child,
      path: [key],
      entryId: `item-${child}`,
      offset: 3,
      viewportOffset: 24,
    };
    writeComicPosition(root, saved);
    writeComposerDraft(root, { text: "UNSENT_ROOT_FIXTURE", skillNames: [] });
    const draft = readComposerDraftSnapshot(root);
    const retries: (() => void)[] = [];
    const restoreScheduler = installHydrationRetrySchedulerForTests((_attempt, retry) => {
      retries.push(retry);
      return () => {};
    });
    onTestFinished(restoreScheduler);
    const client = new FakeClient("ready");
    const response = (ref: string): ThreadReadResponse => ({
      thread: wireThread(ref, {
        turns: [
          {
            id: `turn-${ref}`,
            status: "completed",
            itemsView: "full",
            items: [{ id: `item-${ref}`, type: "agentMessage", text: `CONTENT_${ref}` }],
          },
        ],
      }),
    });
    let fail!: (reason: Error) => void;
    let childReads = 0;
    client.on("thread/read", (p) => {
      if (p.ref === child && ++childReads === 1)
        return new Promise<ThreadReadResponse>((_resolve, reject) => {
          fail = reject;
        });
      return response(p.ref!);
    });
    client.on("thread/unsubscribe", () => ({}));
    client.on("evener/thread/activity/read", (p) => ({
      context: { ...fixture.context, ref: p.ref },
      scope: "session",
      delegates: { known: true, total: p.ref === root ? 3 : 0, active: 0, failed: 0, completed: 0 },
      jobs: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
      watches: { known: false, total: 0, active: 0, failed: 0, completed: 0 },
    }));
    client.on("evener/thread/delegates/list", (p) => ({
      ...fixture,
      context: { ...fixture.context, ref: p.ref },
      scope: "session",
      delegates:
        p.ref === root
          ? fixture.delegates.map((delegate) => ({ ...delegate, childRef: `${delegate.childRef}-${outcome}` }))
          : [],
    }));
    connectionStore.getState().connect(client);
    window.history.replaceState(null, "", paneToURL("session", { ref: root }));
    const mounted = render(page(root));
    await waitFor(() => expect(childReads).toBe(1));
    await waitFor(() =>
      expect(
        mounted.container.querySelector(
          `[data-comic-flow="availability"][data-comic-session="${child}"] [role="status"]`,
        ),
      ).not.toBeNull(),
    );
    expect(readComicPosition(root)).toEqual(saved);
    const siblingHeader = mounted.container.querySelector(`[data-comic-child-ref="${sibling}"]`)!;
    fireEvent.click(within(siblingHeader as HTMLElement).getByRole("button", { name: /expand/i }));
    // This case tests expansion, not text selection. jsdom leaves a caret in
    // the clicked button and dispatches selectionchange on the next task.
    document.getSelection()?.removeAllRanges();
    await waitFor(() =>
      expect(mounted.container.querySelector(`[data-comic-session="${sibling}"] [data-comic-text]`)?.textContent).toBe(
        `CONTENT_${sibling}`,
      ),
    );
    await act(async () =>
      fail(
        outcome === "deleted"
          ? new WireError("DELETED_FIXTURE", -32001, {
              evenerErrorInfo: "actionUnavailable",
              mutationOutcome: "targetDeleted",
              retryDisposition: "none",
            })
          : outcome === "repairing"
            ? new WireError("REPAIR_FIXTURE", -32000, { evenerErrorInfo: "transcriptIndexRebuilding" })
            : new Error("TEMPORARY_FIXTURE"),
      ),
    );
    if (outcome === "deleted") await waitFor(() => expect(threadsStore.getState().deletedRefs.has(child)).toBe(true));
    else await waitFor(() => expect(retries).toHaveLength(1));
    const availability = mounted.container.querySelector(
      `[data-comic-flow="availability"][data-comic-session="${child}"]`,
    );
    expect(availability).not.toBeNull();
    expect(availability!.querySelector(outcome === "deleted" ? '[role="alert"]' : '[role="status"]')).not.toBeNull();
    if (outcome === "deleted") {
      expect(readComicPosition(root)).toMatchObject({ mode: "away", sessionRef: root, path: [], entryId: key });
      expect(retries).toHaveLength(0);
    } else {
      expect(readComicPosition(root)).toEqual(saved);
      if (outcome === "repairing") expect(threadsStore.getState().repairingRefs.has(child)).toBe(true);
      if (outcome === "superseded") fireEvent.click(screen.getByRole("button", { name: /latest/i }));
      await act(async () => retries[0]!());
      await waitFor(() =>
        expect(mounted.container.querySelector(`[data-comic-session="${child}"] [data-comic-text]`)?.textContent).toBe(
          `CONTENT_${child}`,
        ),
      );
      expect(readComicPosition(root)).toEqual(
        outcome === "superseded" ? { mode: "end", sessionRef: root, path: [] } : saved,
      );
      expect(threadsStore.getState().repairingRefs.has(child)).toBe(false);
      expect(
        mounted.container.querySelector(`[data-comic-flow="availability"][data-comic-session="${child}"]`),
      ).toBeNull();
    }
    expect(window.location.pathname).toBe(paneToURL("session", { ref: root }));
    expect(readComposerDraftSnapshot(root)).toEqual(draft);
    expect(workspaceStore.getState().panes).toMatchObject([{ params: { ref: root } }]);
    expect(mounted.container.querySelector(`[data-comic-session="${root}"] [data-comic-text]`)?.textContent).toBe(
      `CONTENT_${root}`,
    );
    expect(mounted.container.querySelector(`[data-comic-session="${sibling}"] [data-comic-text]`)?.textContent).toBe(
      `CONTENT_${sibling}`,
    );
    fireEvent.click(screen.getByRole("button", { name: /latest/i }));
    expect(readComicPosition(root)).toEqual({ mode: "end", sessionRef: root, path: [] });
    const childHeader = mounted.container.querySelector(`[data-comic-child-ref="${child}"]`)!;
    fireEvent.click(within(childHeader as HTMLElement).getByRole("button", { name: /open conversation/i }));
    expect(window.location.pathname).toBe(paneToURL("session", { ref: child }));
    expect(client.calls.some((call) => call.method === "turn/start" || call.method === "thread/resume")).toBe(false);
    mounted.unmount();
  },
);

test("selection A→B releases A's foreground claim and ignores its late response", async () => {
  const client = new FakeClient("ready");
  let completeA!: (value: ThreadReadResponse) => void;
  const delayedA = new Promise<ThreadReadResponse>((resolve) => {
    completeA = resolve;
  });
  const makeThread = (ref: string) => ({
    id: `thread-${ref}`,
    sessionId: `session-${ref}`,
    preview: ref,
    ephemeral: false,
    modelProvider: "openai/gpt-test",
    createdAt: 1,
    updatedAt: 1,
    status: { type: "idle" },
    cwd: "/tmp",
    cliVersion: "1",
    source: "evener",
    evener: { ref, capabilities: { send: false }, queue: { revision: 0 } },
    turns: [
      {
        id: `turn-${ref}`,
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: `item-${ref}`,
            type: "userMessage",
            text: `INPUT_${ref}`,
            position: { entry: 1, item: 0 },
            version: 1,
          },
        ],
      },
    ],
  });
  const makeRead = (ref: string) => ({ thread: makeThread(ref) }) as unknown as ThreadReadResponse;
  client.on("thread/read", (params) => (params.ref === "local:A" ? delayedA : makeRead("local:B")));
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const mounted = render(page("local:A"));
  await act(async () => {
    navigate(paneToURL("session", { ref: "local:B" })!);
    mounted.rerender(page("local:B"));
    await Promise.resolve();
  });
  await act(async () => {
    completeA(makeRead("local:A"));
    await Promise.resolve();
  });
  expect(window.location.pathname).toBe(paneToURL("session", { ref: "local:B" }));
  expect(workspaceStore.getState().panes).toMatchObject([{ params: { ref: "local:B" } }]);
  expect(threadsStore.getState().threads.has("local:A")).toBe(false);
  const methods = client.calls.map((call) => call.method);
  expect(methods.filter((method) => method === "turn/start")).toHaveLength(0);
  expect(methods.filter((method) => method === "thread/resume")).toHaveLength(0);
  mounted.unmount();
});
