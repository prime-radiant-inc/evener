import type { ThreadReadResponse } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { capability, manifest, wireSnapshot } from "@evener/appwire-client/testing/navigation";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { resetActivitySidebarStoreForTests } from "../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../shell/clientContext";
import { StackHost } from "../../shell/mobile/StackHost";
import { NeedsYouProvider } from "../../shell/NeedsYouProvider";
import * as StatusBarModule from "../../shell/statusbar/StatusBar";
import { resetMobileViewportForTests } from "../../shell/useIsMobile";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { resetActivityPanelStoreForTests } from "../../stores/activityPanel";
import { resetConnectionStoreForTests } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../stores/threads";
import * as SessionChromeModule from "./chrome/SessionChrome";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import { ackAskUserCall } from "./composer/askDock/askDockTestUtils";
import * as ComposerModule from "./composer/Composer";
import { readDraft, writeComposerDraft } from "./composer/draft";
import { resetPendingTurnsStoreForTests } from "./composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./composer/queue/testing/flushPendingTurnsProjection";
import type { SessionPaneParams } from "./Session";
import "./index";
import { CommittedSession as Session } from "./testing/CommittedSession";
import { installMobileViewport } from "./testing/mobileViewport";
import { installTranscriptGeometry } from "./transcript/transcriptReadingGeometryTestUtils";
import { resetTranscriptPagingForTests } from "./transcript/useTranscript";
import "./testing/editorGeometry";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectReadyFakeClient } from "../../testing/connectionTestUtils";
import { readResponse, versionedReadResponse } from "./sessionTestUtils";

// The session footer's composer boundary is swapped for a visible stub here
// ONLY to prove Session.tsx mounts it with the right ref and no longer adds a
// standalone SessionChrome sibling. Composer.test.tsx proves the real composer
// owns the inline SessionChrome; its marker is mirrored inside this boundary so
// this suite can pin the Session-level placement without duplicating Composer's
// own behavior tests.
//
// vi.spyOn, not a hoisted vi.mock, so individual tests can hand the real
// Composer/SessionChrome back mid-file and render them. Re-stubbed in
// beforeEach below too, not just once here: a test that restores a slot
// without re-stubbing it (test.each has no onTestFinished) would otherwise
// leave the real component in place for every later test in this file.
function stubSessionSlots(): void {
  vi.spyOn(ComposerModule, "Composer").mockImplementation(({ ref }: { ref: string }) => (
    <div data-testid="composer-slot">
      {ref}
      <div data-testid="session-chrome-inline" />
    </div>
  ));
  vi.spyOn(SessionChromeModule, "SessionChrome").mockImplementation(({ ref }: { ref: string }) => (
    <div data-testid="session-chrome">{ref}</div>
  ));
  vi.spyOn(StatusBarModule, "StatusBar").mockImplementation(() => <></>);
}

stubSessionSlots();

const NEEDS_YOU_PAGE_KEY = { kind: "section", section: "needs_you", offset: 0, limit: 50 } as const;

function installNeedsYouRows(refs: string[]): void {
  const rows = refs.map((ref) => ({
    ref,
    host_id: "local",
    session_id: ref,
    title: ref,
    project: "Proj",
    state: "awaiting",
    kind: "session",
    live: true,
    children: [],
  }));
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(NEEDS_YOU_PAGE_KEY), {
    key: NEEDS_YOU_PAGE_KEY,
    data: { generation_id: "g", revision: 1, sessions: rows, remaining: 0, truncated: false },
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: '"test"',
    loading: false,
    stale: false,
    error: null,
    generationID: "g",
  });
  const visibleResourceKeys = new Set(navigationStore.getState().visibleResourceKeys);
  visibleResourceKeys.add(keyID(NEEDS_YOU_PAGE_KEY));
  navigationStore.setState({
    mode: "v3",
    resources,
    visibleResourceKeys,
    manifest: {
      key: { kind: "manifest" },
      data: {
        generation_id: "g",
        revision: 1,
        sources: [],
        attentionSummary: { needsYou: refs.length, error: 0, working: 0 },
        sections: { live: { count: 0 }, needs_you: { count: refs.length }, pin_sections: { count: 0 } },
        catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
      },
      loadedRevision: 1,
      targetRevision: null,
      forceToken: 0,
      etag: null,
      loading: false,
      stale: false,
      error: null,
      generationID: "g",
    },
  });
}

function attentionSession(paneId = "attention-pane", ref = "ref_a"): OpenPaneRecord {
  return { id: paneId, type: "session", params: { ref }, slot: "main" };
}

// A live (non-dormant) transcript: the floating stack's flow overlay only
// exists once the transcript subtree mounts.
function attentionRead(ref: string): ThreadReadResponse {
  return versionedReadResponse(ref, {
    turns: [
      {
        id: "turn_attention",
        status: "completed",
        itemsView: "full",
        items: [
          {
            id: "item_attention",
            turnId: "turn_attention",
            type: "agentMessage",
            text: "attention content",
            status: "completed",
          },
        ],
      },
    ],
  });
}

function startAskTurn(fake: FakeClient, ref: string): void {
  fake.emitNotification({
    method: "history/updated",
    params: {
      threadId: `thr_${ref}`,
      ref,
      bootGeneration: "1",
      epoch: 1,
      snapshot: { incarnation: "inc-1", length: 1 },
      turns: [{ id: "turn_ask", status: "inProgress", itemsView: "" }],
    },
  } as never);
  ackAskUserCall(fake, ref, "turn_ask", "item_ask", "call_ask");
}

function approvalRead(ref: string): ThreadReadResponse {
  const read = attentionRead(ref);
  return {
    ...read,
    thread: {
      ...read.thread,
      evener: {
        ...read.thread.evener,
        pendingEscalations: [
          {
            ref,
            threadId: `thr_${ref}`,
            escalationId: "esc_1",
            mode: "workspace-write",
            tool: "shell",
            kind: "shell",
            deniedPath: "/etc/hosts",
          },
        ],
      },
    },
  };
}

// jsdom performs no real layout (every element's offsetHeight is 0, no
// ResizeObserver) - VirtualList's own test suite stubs this for the exact
// same reason (see widgets/virtuallist/virtuallist.test.tsx's file-level
// comment): without it, @tanstack/react-virtual sees a 0px-tall viewport
// and never renders a single row, which wouldn't exercise TurnBlock at all.
const CONTAINER_HEIGHT = 500;

let offsetHeightDescriptor: PropertyDescriptor | undefined;

let mutationStorage: MutationOutboxIndexedDB;

// jsdom computes no layout, so the transcript's scroll port reads as
// zero-tall (every scroll* property is 0). The offsetHeight stub below is what
// gives it a rendered box, which is what scrollMetrics.shouldAutoLoadOlder
// requires before it reads "nothing overflows" as the "too short to fill"
// shape - so a pane rendered here pages on its own, the way a real browser's
// short first page does. LoadOlderRow's own suite drives the geometry and the
// resize re-check explicitly.

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  stubSessionSlots();
  globalThis.indexedDB = new IDBFactory();
  globalThis.IDBKeyRange = IDBKeyRange;
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetTranscriptPagingForTests();
  resetAskDockStoreForTests();
  resetNavigationStoreForTests();
  resetActivityPanelStoreForTests();
  resetActivitySidebarStoreForTests();
  resetMobileViewportForTests();
  mutationStorage = new MutationOutboxIndexedDB();
  setMutationStorageForTests(mutationStorage);
  resetPendingTurnsStoreForTests();
  localStorage.clear();
  offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: CONTAINER_HEIGHT });
});

afterEach(() => {
  cleanup();
  resetPendingTurnsStoreForTests();
  resetAskDockStoreForTests();
  resetActivityPanelStoreForTests();
  resetActivitySidebarStoreForTests();
  resetMobileViewportForTests();
  resetWorkspaceStoreForTests();
  window.history.pushState({}, "", "/");
  vi.useRealTimers();
  vi.unstubAllGlobals();
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
});

function renderAttentionSession(fake: FakeClient, pane: OpenPaneRecord) {
  return render(
    <ClientProvider client={fake}>
      <NeedsYouProvider>
        <Session params={pane.params as SessionPaneParams} paneId={pane.id} focused={false} />
      </NeedsYouProvider>
    </ClientProvider>,
  );
}

test("a cold focused conversation can demand its first needs-you page before the manifest arrives", async ({
  onTestFinished,
}) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => attentionRead("ref_a"));
  const manifestGate = deferred<void>();
  const pageRequests: Array<{ offset?: number; limit?: number }> = [];
  fake.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      return manifestGate.promise.then(() =>
        wireSnapshot(
          params,
          manifest({
            generation_id: "cold-needs-you",
            sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
          }),
          '"manifest"',
          1,
          "cold-needs-you",
        ),
      );
    }
    if (params.resource === "section" && params.section === "needs_you") {
      pageRequests.push({ offset: params.offset, limit: params.limit });
      return wireSnapshot(params, { sessions: [], remaining: 0 }, '"page"', 1, "cold-needs-you");
    }
    throw new Error(`unexpected navigation resource ${params.resource}`);
  });
  const releaseNavigation = navigationStore.init(fake, capability("cold-needs-you"));
  const manifestRead = navigationStore.getState().loadManifest();
  const pane = attentionSession();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  window.history.replaceState({}, "", "/s/ref_a");
  let view: ReturnType<typeof render> | undefined;

  try {
    await waitFor(() => expect(navigationStore.getState().manifest?.loading).toBe(true));
    view = renderAttentionSession(fake, pane);
    await screen.findByRole("textbox", { name: "Message" });
    await flushPendingTurnsProjectionForTests();

    const next = await screen.findByTestId("next-needs-you-next");
    expect(pageRequests).toEqual([]);
    fireEvent.click(next);
    await waitFor(() => expect(pageRequests).toEqual([{ offset: 0, limit: 50 }]));
  } finally {
    view?.unmount();
    releaseNavigation();
    manifestGate.resolve();
    await manifestRead;
  }
});

test("a focused conversation can retry needs-you after its manifest read fails", async ({ onTestFinished }) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => attentionRead("ref_a"));
  let manifestReads = 0;
  const pageRequests: Array<{ offset?: number; limit?: number }> = [];
  fake.on("evener/navigation/read", (params) => {
    if (params.resource === "manifest") {
      manifestReads += 1;
      throw new Error("manifest unavailable");
    }
    if (params.resource === "section" && params.section === "needs_you") {
      pageRequests.push({ offset: params.offset, limit: params.limit });
      return wireSnapshot(params, { sessions: [], remaining: 0 }, '"page"', 1, "failed-needs-you");
    }
    throw new Error(`unexpected navigation resource ${params.resource}`);
  });
  const releaseNavigation = navigationStore.init(fake, capability("failed-needs-you"));
  const pane = attentionSession("failed-needs-you-pane");
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  window.history.replaceState({}, "", "/s/ref_a");
  let view: ReturnType<typeof render> | undefined;

  try {
    await waitFor(() => expect(manifestReads).toBeGreaterThanOrEqual(2));
    await waitFor(() => expect(navigationStore.getState().manifest?.error).toBeTruthy());
    view = renderAttentionSession(fake, pane);
    await screen.findByRole("textbox", { name: "Message" });
    await flushPendingTurnsProjectionForTests();

    const next = await screen.findByTestId("next-needs-you-next");
    expect(pageRequests).toEqual([]);
    fireEvent.click(next);
    await waitFor(() => expect(pageRequests).toEqual([{ offset: 0, limit: 50 }]));
  } finally {
    view?.unmount();
    releaseNavigation();
  }
});

test("a reconnect does not flash an empty needs-you control while its manifest reloads", async ({ onTestFinished }) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  const firstClient = connectReadyFakeClient();
  firstClient.on("thread/read", () => attentionRead("ref_a"));
  firstClient.on("evener/navigation/read", (params) => {
    if (params.resource !== "manifest") throw new Error(`unexpected navigation resource ${params.resource}`);
    return wireSnapshot(
      params,
      manifest({
        generation_id: "reconnect-old-empty",
        sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
      }),
      '"manifest"',
      1,
      "reconnect-old-empty",
    );
  });
  const releaseFirst = navigationStore.init(firstClient, capability("reconnect-old-empty"));
  const pane = attentionSession("reconnect-empty-pane");
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  window.history.replaceState({}, "", "/s/ref_a");
  const replacementClient = new FakeClient("ready");
  const replacementManifestGate = deferred<void>();
  replacementClient.on("evener/navigation/read", (params) => {
    if (params.resource !== "manifest") throw new Error(`unexpected navigation resource ${params.resource}`);
    return replacementManifestGate.promise.then(() =>
      wireSnapshot(
        params,
        manifest({
          generation_id: "reconnect-new-empty",
          sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
        }),
        '"manifest"',
        1,
        "reconnect-new-empty",
      ),
    );
  });
  let releaseReplacement: (() => void) | undefined;
  let replacementManifestRead: Promise<unknown> | undefined;
  let view: ReturnType<typeof render> | undefined;

  try {
    await waitFor(() => expect(navigationStore.getState().manifest?.data?.generation_id).toBe("reconnect-old-empty"));
    view = renderAttentionSession(firstClient, pane);
    await screen.findByRole("textbox", { name: "Message" });
    await flushPendingTurnsProjectionForTests();
    expect(screen.queryByTestId("next-needs-you-next")).toBeNull();

    act(() => {
      releaseReplacement = navigationStore.init(replacementClient, capability("reconnect-new-empty"));
      replacementManifestRead = navigationStore.getState().loadManifest();
    });
    await waitFor(() => expect(navigationStore.getState().manifest?.loading).toBe(true));
    expect(screen.queryByTestId("next-needs-you-next")).toBeNull();
    await act(async () => {
      replacementManifestGate.resolve();
      await replacementManifestRead;
    });
    expect(navigationStore.getState().manifest?.data?.generation_id).toBe("reconnect-new-empty");
    expect(screen.queryByTestId("next-needs-you-next")).toBeNull();
  } finally {
    view?.unmount();
    releaseReplacement?.();
    releaseFirst();
    replacementManifestGate.resolve();
    await replacementManifestRead?.catch(() => undefined);
  }
});

test("a focused draft stays visible and composition suppression clears without losing the draft", async ({
  onTestFinished,
}) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => attentionRead("ref_a"));
  installNeedsYouRows(["ref_a", "ref_b"]);
  const pane = attentionSession();
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  window.history.replaceState({}, "", "/s/ref_a");
  writeComposerDraft("ref_a", { text: "keep this draft", skillNames: [] });

  renderAttentionSession(fake, pane);
  const editor = await screen.findByRole("textbox", { name: "Message" });
  await flushPendingTurnsProjectionForTests();

  // A saved draft does not hide the control, focused or not.
  await waitFor(() => expect(screen.getByTestId("next-needs-you")).toBeTruthy());
  expect(editor.textContent).toContain("keep this draft");
  const overlay = screen.getByTestId("flow-overlay-attention");
  expect(overlay.contains(screen.getByTestId("next-needs-you"))).toBe(true);
  const composerForm = screen.getByTestId("composer-form");
  expect(
    screen.getByTestId("next-needs-you").compareDocumentPosition(composerForm) & Node.DOCUMENT_POSITION_FOLLOWING,
  ).toBeTruthy();

  // Editor focus alone does not suppress it: a session open autofocuses this
  // editor, and the control must stay available.
  fireEvent.focus(editor);
  await waitFor(() => expect(screen.getByTestId("next-needs-you")).toBeTruthy());

  // An in-progress IME composition suppresses it, and composition end returns
  // it automatically.
  fireEvent.compositionStart(editor);
  await waitFor(() => expect(screen.queryByTestId("next-needs-you")).toBeNull());
  fireEvent.compositionEnd(editor);
  await waitFor(() => expect(screen.getByTestId("next-needs-you")).toBeTruthy());
  expect(readDraft("ref_a")).toBe("keep this draft");
});

test("the measured attention stack becomes transcript end clearance", async ({ onTestFinished }) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => attentionRead("ref_a"));
  installNeedsYouRows(["ref_a", "ref_b"]);
  // Give the floating stack a measured height without disturbing the
  // transcript's own rect-based geometry. It sits flush against the reader's
  // own bottom edge (jsdom reports that edge at 0), so the clearance it needs
  // is exactly its own 96 px.
  const originalRect = HTMLElement.prototype.getBoundingClientRect;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.dataset.testid === "flow-overlay-stack") return { top: -96, bottom: 0, height: 96 } as DOMRect;
    return originalRect.call(this);
  });
  const pane = attentionSession("clearance-pane");
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  window.history.replaceState({}, "", "/s/ref_a");

  renderAttentionSession(fake, pane);
  await screen.findByRole("textbox", { name: "Message" });
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(screen.getByTestId("transcript-end-clearance")).toBeTruthy());

  // The stack's committed measurement reaches the synthetic trailing row
  // unchanged.
  const clearance = screen.getByTestId("transcript-end-clearance");
  expect(clearance.style.height).toBe("96px");
  expect(clearance.closest('[data-row-id="live-edge"]')).toBeTruthy();
  const pill = screen.getByTestId("flow-overlay-pill");
  const attention = screen.getByTestId("flow-overlay-attention");
  expect(pill.compareDocumentPosition(attention) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
});

test("the measured clearance does not toggle New Content or move an end-following reader", async ({
  onTestFinished,
}) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => attentionRead("ref_a"));
  installNeedsYouRows(["ref_a", "ref_b"]);
  // The floating stack's own measured height is what FlowOverlay reports and
  // ConversationBody turns into the synthetic trailing row. It starts
  // unmeasured (no clearance row), then appears at 96 px and disappears.
  let stackHeight = 0;
  const originalRect = HTMLElement.prototype.getBoundingClientRect;
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (this: HTMLElement) {
    if (this.dataset.testid === "flow-overlay-stack") {
      return { top: -stackHeight, bottom: 0, height: stackHeight } as DOMRect;
    }
    return originalRect.call(this);
  });
  // Real scroll geometry: one 600 px turn in a 400 px port, with the trailing
  // clearance row measured at the stack's height. installTranscriptGeometry's
  // notify() is the browser's resize delivery, including the new row's own
  // measurement on the frame after it mounts.
  const geometry = installTranscriptGeometry(() => ({
    width: 760,
    viewportHeight: 400,
    rowHeights: [600, stackHeight],
  }));
  try {
    const pane = attentionSession("clearance-pill-pane");
    workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
    window.history.replaceState({}, "", "/s/ref_a");
    renderAttentionSession(fake, pane);
    await screen.findByRole("textbox", { name: "Message" });
    await flushPendingTurnsProjectionForTests();
    const section = screen.getByTestId("transcript-virtual-list");
    const port = section.firstElementChild as HTMLElement;
    await act(async () => geometry.notify());

    // The pill is the only surface for the unseen count, and NewContentPill
    // renders itself whenever the count is above zero even while `visible` is
    // false. A missing pill therefore asserts both halves: still hidden, and
    // the count still zero.
    const expectEndUnchanged = () => {
      expect(screen.queryByTestId("new-content-pill")).toBeNull();
      // Still at the true end, not merely at an unchanged offset: this is the
      // position the re-pin has to move as the clearance grows and shrinks.
      expect(port.scrollTop).toBe(port.scrollHeight - port.clientHeight);
    };

    // One turn, already at the end (600 - 400), no clearance yet.
    expect(screen.getAllByTestId("turn-block")).toHaveLength(1);
    expect(screen.queryByTestId("transcript-end-clearance")).toBeNull();
    expect(port.scrollTop).toBe(200);
    expectEndUnchanged();

    stackHeight = 96;
    await act(async () => geometry.notify());
    await act(async () => geometry.notify());
    // The clearance really arrived as the synthetic trailing row, and the
    // reader was carried to the new end rather than left short of it.
    const clearance = screen.getByTestId("transcript-end-clearance");
    expect(clearance.style.height).toBe("96px");
    expect(clearance.closest('[data-row-id="live-edge"]')).toBeTruthy();
    expect(port.scrollTop).toBe(296);
    // No transcript item or turn left the projection for the clearance.
    expect(screen.getAllByTestId("turn-block")).toHaveLength(1);
    expectEndUnchanged();

    stackHeight = 0;
    await act(async () => geometry.notify());
    await act(async () => geometry.notify());
    expect(screen.queryByTestId("transcript-end-clearance")).toBeNull();
    expect(port.scrollTop).toBe(200);
    expectEndUnchanged();
  } finally {
    geometry.restore();
  }
});

test("a pending question suppresses the control and returns automatically", async ({ onTestFinished }) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => attentionRead("ref_a"));
  installNeedsYouRows(["ref_a", "ref_b"]);
  const pane = attentionSession("question-pane");
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  window.history.replaceState({}, "", "/s/ref_a");

  renderAttentionSession(fake, pane);
  await screen.findByRole("textbox", { name: "Message" });
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(screen.getByTestId("next-needs-you")).toBeTruthy());

  act(() => startAskTurn(fake, "ref_a"));
  await waitFor(() => expect(screen.queryByTestId("next-needs-you")).toBeNull());
  act(() =>
    fake.emitNotification({
      method: "thread/status/changed",
      params: { threadId: "thr_ref_a", ref: "ref_a", status: { type: "idle" }, askPending: false },
    } as never),
  );
  await waitFor(() => expect(screen.getByTestId("next-needs-you")).toBeTruthy());
});

test("a pending approval suppresses the control and returns when it resolves", async ({ onTestFinished }) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => approvalRead("ref_a"));
  fake.on("evener/sandbox/escalation/resolve", () => ({}));
  installNeedsYouRows(["ref_a", "ref_b"]);
  const pane = attentionSession("approval-pane");
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  window.history.replaceState({}, "", "/s/ref_a");

  renderAttentionSession(fake, pane);
  await screen.findByRole("textbox", { name: "Message" });
  await flushPendingTurnsProjectionForTests();
  // The approval has to be in the hydrated model before the control's absence
  // counts as suppression, or this case can pass while the model is still
  // loading and then resolve an approval the read installs afterwards.
  await screen.findByRole("group", { name: "Sandbox approval" });
  await waitFor(() => expect(screen.queryByTestId("next-needs-you")).toBeNull());

  await act(async () => {
    await threadsStore.getState().resolveEscalation("ref_a", "esc_1", true);
  });
  await waitFor(() => expect(screen.queryByRole("group", { name: "Sandbox approval" })).toBeNull());
  await waitFor(() => expect(screen.getByTestId("next-needs-you")).toBeTruthy());
});

test("a dormant transcript gets the same attention affordance without a fake transcript", async ({
  onTestFinished,
}) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("ref_a", { turns: [] }));
  installNeedsYouRows(["ref_a", "ref_b"]);
  const pane = attentionSession("dormant-pane");
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  window.history.replaceState({}, "", "/s/ref_a");

  renderAttentionSession(fake, pane);
  await screen.findByRole("textbox", { name: "Message" });
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(screen.getByTestId("next-needs-you")).toBeTruthy());

  expect(screen.queryByTestId("transcript-virtual-list")).toBeNull();
  const dock = screen.getByTestId("attention-dock");
  expect(dock.contains(screen.getByTestId("next-needs-you"))).toBe(true);
});

// The real browser guard measured the floating control's absence at every
// width below 900px (320, 390, 700, 899) while a session was focused and the
// needs-you tier was populated. The desktop cascade grants the control through
// the focused leaf's keyboard-scroll pane id; the phone host renders exactly
// one pane, its focused session, through the same pane registry. This mounts
// that host shape over a focused session and asks the same question the
// browser did: is Next there?
test("the mobile stack host offers attention navigation over its focused session", async ({ onTestFinished }) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  onTestFinished(installMobileViewport());
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => attentionRead("ref_a"));
  installNeedsYouRows(["ref_a", "ref_b"]);
  const pane = attentionSession("mobile-attention-pane");
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  window.history.replaceState({}, "", "/s/ref_a");

  render(
    <ClientProvider client={fake}>
      <NeedsYouProvider>
        <StackHost />
      </NeedsYouProvider>
    </ClientProvider>,
  );
  await act(async () => await vi.dynamicImportSettled());
  await screen.findByRole("textbox", { name: "Message" });
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(screen.getByTestId("next-needs-you-next")).toBeTruthy());
});
