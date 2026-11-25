import { activityChangedNotification } from "@evener/appwire-client/testing/notifications";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { useStore } from "zustand";
import { MotionProvider } from "../../motion";
import { SessionChrome } from "../../panes/session/chrome/SessionChrome";
import type { SessionPaneParams } from "../../panes/session/Session";
import { replaceEditorText, selectEditorText } from "../../panes/session/testing/editor";
import { installTranscriptGeometry } from "../../panes/session/transcript/transcriptReadingGeometryTestUtils";
import { returnFromAgentCascade } from "../../panes/zoom/actions";
import { cascadeClient, cascadeContext, cascadeThread } from "../../panes/zoom/cascadeTestUtils";
import { recordCascadeOrigin } from "../../panes/zoom/inspectionOrigin";
import type { SessionZoomParams } from "../../panes/zoom/intent";
import Zoom from "../../panes/zoom/Zoom";
import "../../panes/zoom";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "../../stores/connection";
import {
  OVERVIEW_WIDTH_DEFAULT,
  OVERVIEW_WIDTH_MAX,
  OVERVIEW_WIDTH_MIN,
  overviewWidthMax,
  prefsStore,
  resetPrefsStoreForTests,
} from "../../stores/prefs";
import { acquireSessionActivity, sessionActivitySnapshot } from "../../stores/sessionActivity";
import {
  activityClient,
  activityContext,
  activityDelegate,
  activityJob,
  activitySummary,
  activityWatch,
} from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import { requireClass } from "../../widgets/internal/requireClass";
import { installSwitchableViewport } from "../appShellTestUtils";
import { ClientProvider } from "../clientContext";
import { conversationPaneLifetime } from "../paneLifetime";
import { installFocusedScope } from "../statusbar/scopeTestUtils";
import { resetMobileViewportForTests } from "../useIsMobile";
import { currentSessionRef, type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { ActivitySidebar, OVERVIEW_WIDTH_PROPERTY } from "./ActivitySidebar";
import styles from "./activitybar.module.css";
import { ActivityBoundaryObserver } from "./activityPagingTestUtils";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";
import { ActivitySidebar as ActivitySidebarUnderTest } from "./activitySidebarTestUtils";

beforeAll(async () => {
  await import("../../panes/transcript");
});

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  resetDisclosureStoreForTests();
  resetPrefsStoreForTests();
});

const ref = "remote:owner";
function JourneyPane({ Session }: { Session: typeof import("../../panes/session/Session").default }) {
  const state = useStore(workspaceStore);
  return state.panes.map((pane) => {
    if (pane.type === "session")
      return (
        <div key={pane.id} data-testid={`conversation-${pane.id}`}>
          <Session
            paneId={pane.id}
            params={pane.params as SessionPaneParams}
            focused={state.focusedPaneId === pane.id}
          />
        </div>
      );
    if (pane.type === "sessionZoom")
      return (
        <div key={pane.id} data-testid={`inspection-${pane.id}`}>
          <Zoom paneId={pane.id} params={pane.params as SessionZoomParams} focused={state.focusedPaneId === pane.id} />
        </div>
      );
    return null;
  });
}
const mount = (mobile = false) =>
  render(
    <MotionProvider>
      <ActivitySidebarUnderTest mobile={mobile} />
    </MotionProvider>,
  );
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  resetDisclosureStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  ActivityBoundaryObserver.reset();
});

test("one visible-sidebar clock advances activity rows and expanded watch details without new reads", async () => {
  const now = Date.parse("2026-10-05T00:00:00Z");
  const client = activityClient();
  client.on("evener/thread/watches/list", ({ ref: requestedRef, scope }) => ({
    context: activityContext(requestedRef),
    scope: scope ?? "session",
    watches: [
      activityWatch({
        createdAt: "2026-10-04T23:55:00Z",
        deliveries: 1,
        deliveryTimes: ["2026-10-04T23:55:00Z"],
        cadence: [{ kind: "every", seconds: 300 }],
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  vi.useFakeTimers({ now, toFake: ["Date"] });
  const intervals = vi.spyOn(globalThis, "setInterval");
  activitySidebarStore.getState().openWith("watches");
  const mounted = mount();
  const watch = await screen.findByText("watch_raw");
  const summary = watch.closest("summary");
  if (!summary) throw new Error("Watch summary missing");
  act(() => fireEvent.click(summary));
  await screen.findByTestId("watch-facts");
  const time = summary.querySelector("time");
  expect(time?.dateTime).toBe("2026-10-04T23:55:00Z");
  const beforeTime = time?.textContent;
  const beforeFacts = screen.getByTestId("watch-facts").textContent;
  const readsBeforeTick = client.calls.length;
  act(() => {
    vi.setSystemTime(now + 60_000);
    const tick = intervals.mock.calls.find(([, delay]) => delay === 30_000)?.[0];
    if (typeof tick === "function") Function.prototype.call.call(tick, undefined);
  });
  expect(time?.dateTime).toBe("2026-10-04T23:55:00Z");
  expect(time?.textContent).not.toBe(beforeTime);
  expect(screen.getByTestId("watch-facts").textContent).not.toBe(beforeFacts);
  expect(client.calls.length).toBe(readsBeforeTick);
  expect(intervals.mock.calls.filter(([, delay]) => delay === 30_000)).toHaveLength(1);
  act(() => activitySidebarStore.getState().setTab("agents"));
  await screen.findByRole("button", { name: /inspect/ });
  const readsBeforeClose = client.calls.filter((call) => call.method !== "thread/unsubscribe").length;
  expect(intervals.mock.calls.filter(([, delay]) => delay === 30_000)).toHaveLength(1);
  const clears = vi.spyOn(globalThis, "clearInterval");
  const clockCall = intervals.mock.calls.findIndex(([, delay]) => delay === 30_000);
  const activeClockID = intervals.mock.results[clockCall]?.value;
  act(() => mounted.unmount());
  expect(screen.queryByTestId("activity-sidebar")).toBeNull();
  expect(clears.mock.calls.flat()).toContain(activeClockID);
  act(() => {
    vi.setSystemTime(now + 30_000);
    const tick = intervals.mock.calls.find(([, delay]) => delay === 30_000)?.[0];
    if (typeof tick === "function") Function.prototype.call.call(tick, undefined);
  });
  expect(client.calls.filter((call) => call.method !== "thread/unsubscribe").length).toBe(readsBeforeClose);
});

test("the live watch detail names a named delegate's own watch and its job target", async () => {
  const childRef = "remote:child";
  // A named delegate's own activity read proves the viewed session's identity;
  // its own row is never in its own delegates collection.
  const context = (requestedRef: string) => ({ ...activityContext(requestedRef), delegateName: "navigation-observer" });
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ ref: requestedRef, scope }) => ({
    ...activitySummary(requestedRef, scope),
    context: context(requestedRef),
  }));
  client.on("evener/thread/delegates/list", ({ ref: requestedRef, scope }) => ({
    context: context(requestedRef),
    scope: scope ?? "session",
    delegates: [],
    page: { complete: true, issues: [] },
  }));
  client.on("evener/thread/jobs/list", ({ ref: requestedRef, scope }) => ({
    context: context(requestedRef),
    scope: scope ?? "session",
    jobs: [activityJob({ ownerRef: requestedRef, description: "Child checks" })],
    page: { complete: true, issues: [] },
  }));
  // The watch belongs in its receiver's own collection, with the source ref
  // naming the same physical session.
  const ownWatch = activityWatch(
    { source: "job_raw", target: "job_raw", outputMatch: "ready", cadence: [{ kind: "output" }] },
    childRef,
  );
  ownWatch.sourceRef = childRef;
  client.on("evener/thread/watches/list", ({ ref: requestedRef, scope }) => ({
    context: context(requestedRef),
    scope: scope ?? "session",
    watches: [ownWatch],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope(childRef);
  // The session transcript beside the sidebar already observes jobs and
  // delegates; the sidebar reads that same store and owns no read of its own.
  const activity = acquireSessionActivity(client, childRef, "session");
  const stopDelegates = activity.store.observe("delegates");
  const stopJobs = activity.store.observe("jobs");
  let mounted: ReturnType<typeof mount> | undefined;
  try {
    await waitFor(() => expect(activity.store.getSnapshot().jobs.rows).toHaveLength(1));
    expect(activity.store.getSnapshot().context?.delegateName).toBe("navigation-observer");
    const entityReads = () =>
      client.calls.filter(
        (call) => call.method === "evener/thread/delegates/list" || call.method === "evener/thread/jobs/list",
      ).length;
    const readsBeforeMount = entityReads();
    activitySidebarStore.getState().openWith("watches");
    mounted = mount();
    const watch = await screen.findByText("watch_raw");
    const summary = watch.closest("summary");
    if (!summary) throw new Error("Watch summary missing");
    act(() => fireEvent.click(summary));
    await screen.findByTestId("watch-facts");
    expect(screen.getByTestId("watch-facts").textContent).toContain("Child checks");
    expect(screen.getByText(/navigation-observer/)).toBeTruthy();
    expect(entityReads()).toBe(readsBeforeMount);
  } finally {
    act(() => mounted?.unmount());
    stopJobs();
    stopDelegates();
    activity.release();
  }
});

test("closed sidebar owns no read and an open tab observes only its own collection", async () => {
  const client = activityClient();
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  mount();
  expect(screen.queryByTestId("activity-sidebar")).toBeNull();
  // The shell eagerly warms the controller palette's catalog on every ready
  // connection (stores/commandCatalog), and the transcript pane's
  // conversation fallback now pulls that module into this graph; the
  // sidebar's contract is that IT owns no read while closed.
  expect(client.calls.filter((c) => c.method !== "evener/command/list")).toHaveLength(0);
  act(() => activitySidebarStore.getState().openWith("agents"));
  await screen.findByRole("button", { name: /inspect/ });
  expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list")).toHaveLength(1);
  expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(0);
  fireEvent.click(screen.getByRole("tab", { name: "Jobs, 2 of 201 running" }));
  await screen.findByText("No jobs at this level.");
  expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(1);
  expect(client.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: "Close Overview" }));
  await waitFor(() => expect(sessionActivitySnapshot(client, ref, "session")).toBeNull());
});

test("closed inactive history admits keyset pages and uses the direct-session total", async () => {
  const client = activityClient();
  vi.stubGlobal("IntersectionObserver", ActivityBoundaryObserver);
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    delegates: { known: true, active: 2, total: 40, completed: 38, failed: 0 },
  }));
  client.on("evener/thread/delegates/list", ({ cursor, scope }) => ({
    context: activityContext(),
    scope: scope ?? "session",
    delegates: cursor
      ? [activityDelegate({ delegateId: "second", childRef: "remote:second", description: "second bounded page" })]
      : [
          activityDelegate(),
          ...Array.from({ length: 21 }, (_, i) =>
            activityDelegate({
              delegateId: `done-${i}`,
              childRef: `remote:done-${i}`,
              description: `done ${i}`,
              terminal: true,
              status: "completed",
              phase: "done",
              lifecycle: "idle",
            }),
          ),
        ],
    page: { complete: !!cursor, issues: [], ...(!cursor ? { nextCursor: "page-two" } : {}) },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  await screen.findByText("38 inactive subagents");
  expect(screen.queryByRole("button", { name: /done 0/ })).toBeNull();
  await act(async () => ActivityBoundaryObserver.intersect());
  await screen.findByRole("button", { name: /second bounded page/ });
  expect(screen.getByRole("button", { name: /inspect/ })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /Load more/ })).toBeNull();
  expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list").map((c) => c.params)).toEqual([
    { ref, scope: "subtree" },
    { ref, scope: "subtree", cursor: "page-two" },
  ]);
  vi.unstubAllGlobals();
});

test("a fresh sidebar case starts with inactive delegates folded after a prior reader expanded them", async () => {
  const client = activityClient();
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    delegates: { known: true, active: 0, total: 1, completed: 1, failed: 0 },
  }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: activityContext(),
    scope: scope ?? "session",
    delegates: [
      activityDelegate({
        delegateId: "done-fresh",
        childRef: "remote:done-fresh",
        description: "Fresh completed delegate",
        terminal: true,
        status: "completed",
        phase: "done",
        lifecycle: "idle",
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  const fold = await screen.findByText("1 inactive subagent");
  expect(fold.closest("details")?.open).toBe(false);
  expect(screen.queryByRole("button", { name: /Fresh completed delegate/ })).toBeNull();
  fireEvent.click(fold);
  expect(screen.getByRole("button", { name: /Fresh completed delegate/ })).toBeTruthy();
});

test("exact child focus switches demand without sibling leakage or a navigation activity reader", async () => {
  const client = activityClient();
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: activityContext(ref),
    scope: scope ?? "session",
    delegates: [
      activityDelegate({
        ownerRef: ref,
        description: ref === "remote:child" ? "child-owned delegate" : "root-only sibling",
      }),
    ],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  await screen.findByRole("button", { name: /root-only sibling/ });
  act(() => installFocusedScope("remote:child"));
  await screen.findByRole("button", { name: /child-owned delegate/ });
  expect(screen.queryByRole("button", { name: /root-only sibling/ })).toBeNull();
  expect(sessionActivitySnapshot(client, ref, "subtree")).toBeNull();
  expect(client.calls.filter((c) => c.method === "evener/navigation/read")).toHaveLength(0);
});

// The Slide recipe (docs/web-ui/motion.md): the distance is the surface's own
// size, never a pixel constant, and what slid in slides back out.
test("the Overview slides in by its own width and leaves again", async () => {
  const client = activityClient();
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  mount(true);
  act(() => activitySidebarStore.getState().openWith("agents"));
  const aside = await screen.findByTestId("activity-sidebar");
  expect(aside.style.transform).toMatch(/translateX\(100%\)/);
  act(() => activitySidebarStore.getState().close());
  await waitFor(() => expect(screen.queryByTestId("activity-sidebar")).toBeNull());
});

// S2-F08: the focus scope IS the flex body. The aside used to style
// FocusScope's unstyled wrapper with `.sidebar > div`; if a wrapper comes
// back, the body loses its flex fill silently.
test("the sidebar's focus scope is its flex body, not a wrapper around it", async () => {
  connectionStore.getState().connect(activityClient());
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mount();
  const sidebar = await screen.findByTestId("activity-sidebar");
  const root = sidebar.firstElementChild;
  expect(root?.classList.contains(requireClass(styles.focusBody, "activitybar.module.css", "focusBody"))).toBe(true);
  expect(root?.getAttribute("tabindex")).toBe("-1");
});

test("Escape dismisses only an unclaimed sidebar gesture", async () => {
  const client = activityClient();
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  const claim = (event: KeyboardEvent) => event.preventDefault();
  window.addEventListener("keydown", claim);
  try {
    mount();
    await screen.findByRole("button", { name: /inspect/ });
    fireEvent.keyDown(window, { key: "Escape" });
    expect(activitySidebarStore.getState().open).toBe(true);
  } finally {
    window.removeEventListener("keydown", claim);
  }
  fireEvent.keyDown(window, { key: "Escape" });
  expect(activitySidebarStore.getState().open).toBe(false);
});

test.each(["wheel", "key"] as const)(
  "ordinary Session %s input supersedes reflow beside its real inspector",
  async (input) => {
    const { default: Session } = await import("../../panes/session/Session");
    const { readerWireTurns } = await import("../../panes/session/transcript/transcriptReaderTestUtils");
    const { holdReaderFrames } = await import("../../panes/session/transcript/transcriptReadingGeometryTestUtils");
    const context = (ref: string) => cascadeContext(ref, ref === "child" ? ["root"] : []);
    const client = cascadeClient(context);
    client.on("thread/read", ({ ref, requestGeneration, includeTurns }) => {
      if (!ref) throw new Error("Missing real journey ref");
      const read = cascadeThread(ref, requestGeneration, includeTurns !== false);
      return { ...read, thread: { ...read.thread, turns: includeTurns === false ? [] : readerWireTurns(ref) } };
    });
    client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
      context: context(ref),
      scope: scope ?? "session",
      page: { complete: true, issues: [] },
      delegates:
        ref === "root"
          ? [
              activityDelegate({
                ownerRef: "root",
                childRef: "child",
                delegateId: "edge-child",
                description: "Inspect reader child",
              }),
            ]
          : [],
    }));
    connectionStore.getState().connect(client);
    installFocusedScope("root");
    const source: OpenPaneRecord = { id: "root", type: "session", params: { ref: "root" }, slot: "main" };
    workspaceStore.setState({ panes: [source], focusedPaneId: source.id });
    const geometry = { width: 152, viewportHeight: 400, rowHeights: [1600, 1000] };
    const neighbor = { width: 400, viewportHeight: 400, rowHeights: [1600, 1000] };
    const external = installTranscriptGeometry((element) =>
      element.closest('[data-testid="conversation-root"]') ? geometry : neighbor,
    );
    const frames = holdReaderFrames();
    const mounted = render(
      <ClientProvider client={client}>
        <MotionProvider>
          <JourneyPane Session={Session} />
          <ActivitySidebar />
        </MotionProvider>
      </ClientProvider>,
    );
    try {
      const conversation = within(screen.getByTestId("conversation-root"));
      await conversation.findByText("root current reading content");
      const editor = await conversation.findByRole("textbox", { name: "Message" });
      const port = conversation.getByTestId("transcript-virtual-list").firstElementChild;
      if (!(port instanceof HTMLElement)) throw new Error("Actual Session has no scroll port");
      const lifetime = conversationPaneLifetime(source);
      await act(async () => external.notify());
      await act(async () => frames.release());
      await act(async () => frames.release());
      await act(async () => {
        fireEvent.wheel(port, { deltaY: -100 });
        port.scrollTop = 1000;
        fireEvent.scroll(port);
      });
      await act(async () => {
        port.scrollTop = 900;
        fireEvent.scroll(port);
      });
      expect(port.scrollTop).toBe(900);
      act(() => activitySidebarStore.getState().openWith("agents"));
      const drill = await within(screen.getByTestId("activity-sidebar")).findByRole("button", {
        name: /Inspect reader child/,
      });
      await act(async () => fireEvent.click(drill));
      await screen.findByText("child current reading content");
      expect(conversation.getByRole("textbox", { name: "Message" })).toBe(editor);
      expect(conversationPaneLifetime(source)).toBe(lifetime);
      await act(async () => workspaceStore.getState().focusPane(source.id));
      await act(async () => replaceEditorText(editor, "keep this selection"));
      selectEditorText(editor, 5, 9);
      const text = document.createTreeWalker(editor, NodeFilter.SHOW_TEXT).nextNode();
      if (!text) throw new Error("Actual composer has no typed text node");
      const selection = window.getSelection();
      if (input === "key")
        await act(async () => {
          port.tabIndex = 0;
          port.focus();
        });
      const focused = document.activeElement;
      geometry.width = 352;
      geometry.rowHeights[0] = 700;
      await act(async () => external.notify((target) => target === port));
      await act(async () => {
        if (input === "wheel") {
          fireEvent.wheel(port, { deltaY: -800 });
          port.scrollTop = 100;
          fireEvent.scroll(port);
        } else {
          for (let index = 0; index < 2; index += 1)
            fireEvent.keyDown(port, { key: "ArrowUp", altKey: true, shiftKey: true });
          for (let index = 0; index < 2; index += 1) fireEvent.keyDown(port, { key: "ArrowUp", altKey: true });
        }
      });
      await act(async () => external.notify());
      expect(port.scrollTop).toBe(100);
      expect(document.activeElement).toBe(focused);
      expect(conversation.getByRole("textbox", { name: "Message" })).toBe(editor);
      expect(conversationPaneLifetime(source)).toBe(lifetime);
      if (input === "wheel")
        expect([selection?.anchorNode, selection?.anchorOffset, selection?.focusNode, selection?.focusOffset]).toEqual([
          text,
          5,
          text,
          9,
        ]);
    } finally {
      mounted.unmount();
      external.restore();
      frames.restore();
    }
  },
);

test("desktop delegate rows build six nested edges beside the mounted center and parent crumbs pop at the unchanged root URL", async () => {
  const { default: Session } = await import("../../panes/session/Session");
  const refs = [
    "remote:root",
    "remote:child",
    "remote:grandchild",
    "remote:fourth",
    "remote:fifth",
    "remote:sixth",
    "remote:seventh",
  ];
  const root = refs[0];
  if (!root) throw new Error("Missing root fixture");
  const title = (ref: string) => `${ref.slice("remote:".length)} work`;
  const context = (ref: string) => cascadeContext(ref, refs.slice(0, refs.indexOf(ref)), title);
  const client = cascadeClient(context);
  client.on("thread/read", ({ ref, includeTurns, requestGeneration }) => {
    if (!ref) throw new Error("Missing thread ref");
    return cascadeThread(ref, requestGeneration, includeTurns !== false, title(ref));
  });
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    context: context(ref),
    delegates: { known: true, active: 1, total: 1, failed: 0, completed: 0 },
    jobs: { known: true, active: refs.indexOf(ref) + 1, total: refs.indexOf(ref) + 101, failed: 0, completed: 100 },
  }));
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: context(ref),
    scope: scope ?? "session",
    delegates: refs[refs.indexOf(ref) + 1]
      ? [
          activityDelegate({
            ownerRef: ref,
            rootRef: root,
            childRef: refs[refs.indexOf(ref) + 1],
            delegateId: `edge-${refs[refs.indexOf(ref) + 1]}`,
            description: `Open ${refs[refs.indexOf(ref) + 1]}`,
          }),
        ]
      : [],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope(root);
  workspaceStore.setState({
    panes: [{ id: "root", type: "session", params: { ref: root }, slot: "main" }],
    focusedPaneId: "root",
  });
  window.history.replaceState({}, "", "/s/remote%3Aroot");
  activitySidebarStore.getState().openWith("agents");
  const height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: 500 });
  try {
    render(
      <ClientProvider client={client}>
        <MotionProvider>
          <JourneyPane Session={Session} />
          <ActivitySidebar />
        </MotionProvider>
      </ClientProvider>,
    );
    const source = workspaceStore.getState().panes.find((pane) => pane.id === "root");
    if (!source) throw new Error("Missing center source");
    const lifetime = conversationPaneLifetime(source);
    const conversation = within(screen.getByTestId(`conversation-${source.id}`));
    await conversation.findByText(`${root} real content`);
    const editor = await conversation.findByRole("textbox", { name: "Message" });
    const transcript = conversation.getByTestId("transcript-virtual-list");
    let inspectorId = "";
    for (const [index, child] of refs.slice(1).entries()) {
      const drill = await within(screen.getByTestId("activity-sidebar")).findByRole("button", {
        name: new RegExp(`Open ${child}`),
      });
      await act(async () => fireEvent.click(drill));
      expect(workspaceStore.getState().panes.find((pane) => pane.id === source.id)).toBe(source);
      expect(workspaceStore.getState().panes).toHaveLength(2);
      expect(conversationPaneLifetime(source)).toBe(lifetime);
      expect(conversation.getByRole("textbox", { name: "Message" })).toBe(editor);
      expect(conversation.getByTestId("transcript-virtual-list")).toBe(transcript);
      const inspector = workspaceStore.getState().panes.find((pane) => pane.type === "sessionZoom");
      if (!inspector) throw new Error("Missing secondary inspector");
      if (!inspectorId) inspectorId = inspector.id;
      expect(inspector.id).toBe(inspectorId);
      expect(inspector.slot).toBe("secondary");
      await screen.findByText(`${child} real content`);
      // The mounted origin root never renders: the first drill shows only the
      // drilled leaf, level 2 keeps the readable pair with no spines, and
      // deeper levels collapse the earlier non-origin ancestors to spines.
      expect(screen.getAllByTestId("cascade-column")).toHaveLength(index === 0 ? 1 : 2);
      expect(screen.queryAllByTestId("cascade-spine")).toHaveLength(Math.max(0, index - 1));
      expect(currentSessionRef(workspaceStore.getState())).toBe(child);
      expect(
        within(conversation.getByTestId("statusbar")).getByRole("button", {
          name: "Jobs, 1 of 101 running - open Overview",
        }),
      ).toBeTruthy();
      const inspectorFooter = within(screen.getByTestId(`inspection-${inspectorId}`)).getByTestId("statusbar");
      expect(
        within(inspectorFooter).getByRole("button", {
          name: `Jobs, ${index + 2} of ${index + 102} running - open Overview`,
        }),
      ).toBeTruthy();
    }
    expect(screen.getAllByTestId("cascade-spine")).toHaveLength(4);
    expect(workspaceStore.getState().panes).toHaveLength(2);
    expect(workspaceStore.getState().panes.filter((pane) => pane.type === "transcript")).toHaveLength(0);
    const parent = screen.getAllByTestId("cascade-column")[0];
    if (!parent) throw new Error("Missing parent column");
    window.getSelection()?.selectAllChildren(within(parent).getByText("remote:sixth real content"));
    fireEvent.scroll(within(parent).getByTestId("transcript-virtual-list"));
    expect(currentSessionRef(workspaceStore.getState())).toBe("remote:seventh");
    const footer = within(screen.getByTestId(`inspection-${inspectorId}`)).getByTestId("statusbar");
    expect(within(footer).getByRole("button", { name: "Jobs, 7 of 107 running - open Overview" })).toBeTruthy();
    const sidebar = within(screen.getByTestId("activity-sidebar"));
    fireEvent.click(sidebar.getByRole("tab", { name: "About" }));
    await sidebar.findByText("wire-remote:seventh");
    expect(sidebar.queryByText("wire-remote:sixth")).toBeNull();
    window.getSelection()?.selectAllChildren(within(parent).getByText("remote:sixth real content"));
    fireEvent.scroll(within(parent).getByTestId("transcript-virtual-list"));
    expect(currentSessionRef(workspaceStore.getState())).toBe("remote:seventh");
    expect(sidebar.getByText("wire-remote:seventh")).toBeTruthy();
    fireEvent.click(sidebar.getByRole("button", { name: "child work" }));
    await sidebar.findByRole("button", { name: /Open remote:grandchild/ });
    expect(currentSessionRef(workspaceStore.getState())).toBe("remote:child");
    fireEvent.click(sidebar.getByRole("tab", { name: "About" }));
    await sidebar.findByText("wire-remote:child");
    // The origin root never renders in the pane, so its crumb is not offered
    // and the cascade cannot pop to it: the drilled branch stays selected.
    expect(sidebar.queryByRole("button", { name: "root work" })).toBeNull();
    expect(currentSessionRef(workspaceStore.getState())).toBe("remote:child");
    expect(activitySidebarStore.getState().tab).toBe("about");
    // Re-drilling from the retained Agents view keeps the per-scope view
    // state working across the ref change.
    fireEvent.click(sidebar.getByRole("tab", { name: /^Agents,/ }));
    fireEvent.click(await sidebar.findByRole("button", { name: /Open remote:grandchild/ }));
    await waitFor(() => expect(currentSessionRef(workspaceStore.getState())).toBe("remote:grandchild"));
    expect(activitySidebarStore.getState().tab).toBe("agents");
    fireEvent.click(sidebar.getByRole("tab", { name: "About" }));
    await sidebar.findByText("wire-remote:grandchild");
    const inspectorChip = within(
      within(screen.getByTestId(`inspection-${inspectorId}`)).getByTestId("statusbar"),
    ).getByRole("button", {
      name: /^Agents,/,
    });
    inspectorChip.focus();
    await act(async () => returnFromAgentCascade(inspectorId));
    expect(workspaceStore.getState().panes).toEqual([source]);
    expect(workspaceStore.getState().focusedPaneId).toBe(source.id);
    expect(conversation.getByRole("textbox", { name: "Message" })).toBe(editor);
    expect(document.activeElement).toBe(editor);
  } finally {
    if (height) Object.defineProperty(HTMLElement.prototype, "offsetHeight", height);
    else Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
  }
  expect(currentSessionRef(workspaceStore.getState())).toBe(root);
  expect(window.location.pathname).toBe("/s/remote%3Aroot");
});

test("cascade sidebar parent crumbs preserve the requested source alias at its proven ancestor position", async () => {
  const context = (ref: string) =>
    ref === "child"
      ? {
          ...activityContext(ref),
          ref: "canonical-child",
          sessionId: "child-id",
          rootRef: "canonical-root",
          parentRef: "canonical-root",
          delegateId: "d1",
          ancestors: [{ ref: "canonical-root", sessionId: "root-id", title: "Root" }],
        }
      : { ...activityContext(ref), ref: "canonical-root", sessionId: "root-id" };
  const client = cascadeClient(context);
  connectionStore.getState().connect(client);
  installFocusedScope("child");
  const source = { type: "session" as const, params: { ref: "root-alias" } };
  const params: SessionZoomParams = {
    ref: "child",
    source,
    edges: [{ ownerRef: "root-alias", childRef: "child", delegateId: "d1" }],
  };
  const unrelated: OpenPaneRecord = {
    id: "unrelated",
    type: "transcript",
    params: { ref: "other" },
    slot: "secondary",
  };
  workspaceStore.setState({
    panes: [{ id: "source", type: "sessionZoom", params, slot: "main" }, unrelated],
    focusedPaneId: "source",
  });
  activitySidebarStore.getState().openWith("agents");
  mount();
  const sidebar = within(screen.getByTestId("activity-sidebar"));
  const parent = await sidebar.findByRole("button", { name: "Root" });
  await act(async () => fireEvent.click(parent));
  const pane = workspaceStore.getState().panes.find((item) => item.id === "source");
  if (!pane) throw new Error("Missing source pane after breadcrumb pop");
  expect(pane).toMatchObject({
    id: "source",
    type: "sessionZoom",
    slot: "main",
    params: { ref: "root-alias", edges: [] },
  });
  expect((pane.params as SessionZoomParams).source).toBe(source);
  expect(workspaceStore.getState().focusedPaneId).toBe("source");
  expect(workspaceStore.getState().panes).toHaveLength(2);
  expect(workspaceStore.getState().panes.find((item) => item.id === "unrelated")).toBe(unrelated);
});

test("an embedded column's Overview describes that column, then the sidebar follows focus again", async () => {
  const user = userEvent.setup();
  const context = (ref: string) =>
    cascadeContext(ref, ref === "grandchild" ? ["root", "child"] : ref === "child" ? ["root"] : []);
  const client = cascadeClient(context);
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: context(ref),
    scope: scope ?? "session",
    delegates: [activityDelegate({ ownerRef: ref, description: `delegate owned by ${ref}` })],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope("grandchild");
  const params: SessionZoomParams = {
    ref: "grandchild",
    source: { type: "session", params: { ref: "root" } },
    edges: [
      { ownerRef: "root", childRef: "child", delegateId: "d1" },
      { ownerRef: "child", childRef: "grandchild", delegateId: "d2" },
    ],
  };
  workspaceStore.setState({
    panes: [
      { id: "zoom", type: "sessionZoom", params, slot: "main" },
      // An ordinary conversation pane for the parent column's ref exists
      // beside the cascade: the column's own Overview must keep the
      // cascade focused rather than jumping to it.
      { id: "ordinary-child", type: "session", params: { ref: "child" }, slot: "secondary" },
    ],
    focusedPaneId: "zoom",
  });
  // The embedded conversation hydrates its column's thread before the chrome
  // mounts; mirror that load so the standalone chrome renders.
  await act(async () => {
    await threadsStore.getState().ensureThread("child");
  });
  const mounted = render(
    <ClientProvider client={client}>
      <MotionProvider>
        {/* The parent column's real chrome: an embedded conversation names
            its own ref while the focused zoom pane resolves to the leaf. */}
        <SessionChrome ref="child" paneId="zoom" />
        <ActivitySidebar />
      </MotionProvider>
    </ClientProvider>,
  );
  try {
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    await user.click(screen.getByRole("menuitem", { name: /^Overview/ }));
    const sidebar = within(screen.getByTestId("activity-sidebar"));
    // The open request came from the column's chrome: the cascade stays
    // focused, not the ordinary pane for the same conversation.
    expect(workspaceStore.getState().focusedPaneId).toBe("zoom");
    // The request named the parent column, so the sidebar describes it:
    // its wire identity, its delegates, and its own ancestry - never the
    // focused leaf's.
    await sidebar.findByText("wire-child");
    expect(sidebar.queryByText("wire-grandchild")).toBeNull();
    expect(sidebar.getByRole("button", { name: "root" })).toBeTruthy();
    fireEvent.click(sidebar.getByRole("tab", { name: /^Agents,/ }));
    await sidebar.findByRole("button", { name: /delegate owned by child/ });
    expect(sidebar.queryByRole("button", { name: /delegate owned by grandchild/ })).toBeNull();
    // The checkmark marks the scope the sidebar shows: the column's own menu
    // offers Overview as checked.
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    expect(screen.getByRole("menuitem", { name: /^Overview.*✓$/ })).toBeTruthy();
    await user.keyboard("{Escape}");
    // The held scope releases when the workspace scope moves: the sidebar
    // goes back to following focus.
    act(() => workspaceStore.getState().openPane("session", { ref: "remote:other" }));
    await sidebar.findByRole("button", { name: /delegate owned by remote:other/ });
  } finally {
    mounted.unmount();
  }
});

test("the held column's origin crumb routes to the retained origin pane", async () => {
  // A separated inspector: the origin conversation lives in its own pane,
  // and popAgentCascade refuses to select the origin inside the cascade
  // (selecting it there would leave an empty track). The origin crumb in a
  // held column's ancestry must route to that pane instead of doing
  // nothing.
  const context = (ref: string) =>
    cascadeContext(ref, ref === "grandchild" ? ["root", "child"] : ref === "child" ? ["root"] : []);
  const client = cascadeClient(context);
  client.on("evener/thread/delegates/list", ({ ref, scope }) => ({
    context: context(ref),
    scope: scope ?? "session",
    delegates: [activityDelegate({ ownerRef: ref, description: `delegate owned by ${ref}` })],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope("grandchild");
  const params: SessionZoomParams = {
    ref: "grandchild",
    source: { type: "session", params: { ref: "root" } },
    edges: [
      { ownerRef: "root", childRef: "child", delegateId: "d1" },
      { ownerRef: "child", childRef: "grandchild", delegateId: "d2" },
    ],
    inspection: { origin: { paneId: "origin", type: "session", ref: "root" } },
  };
  workspaceStore.setState({
    panes: [
      { id: "origin", type: "session", params: { ref: "root" }, slot: "main" },
      { id: "zoom", type: "sessionZoom", params, slot: "secondary" },
    ],
    focusedPaneId: "zoom",
  });
  const panes = workspaceStore.getState().panes;
  const inspector = panes.find((pane) => pane.id === "zoom");
  const origin = panes.find((pane) => pane.id === "origin");
  if (!inspector || !origin) throw new Error("fixture panes missing");
  act(() => recordCascadeOrigin(inspector, origin));
  // The parent column's Overview hold, like its chrome's own request.
  activitySidebarStore.getState().openFor("child", "agents");
  mount();
  const sidebar = within(screen.getByTestId("activity-sidebar"));
  const originCrumb = await sidebar.findByRole("button", { name: "root" });
  act(() => fireEvent.click(originCrumb));
  expect(workspaceStore.getState().focusedPaneId).toBe("origin");
  // The sidebar follows focus onto the origin conversation, and that read
  // settles inside the test rather than after it.
  await sidebar.findByRole("button", { name: /delegate owned by root/ });
});

test("activity tabs keep a named keyboard tablist without a visible heading", async () => {
  connectionStore.getState().connect(activityClient());
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  await screen.findByRole("button", { name: /inspect/ });
  expect(screen.getByRole("complementary", { name: "Overview" })).toBeTruthy();
  expect(screen.queryByRole("dialog", { name: "Overview" })).toBeNull();
  expect(screen.getByTestId("activity-sidebar").getAttribute("aria-modal")).toBeNull();
  expect(screen.getByRole("tablist", { name: "Overview kind" })).toBeTruthy();
  expect(screen.queryByText("Overview kind")).toBeNull();
  await act(async () => fireEvent.keyDown(screen.getByRole("tab", { name: /Agents/ }), { key: "End" }));
  expect(activitySidebarStore.getState().tab).toBe("tasks");
  await act(async () => fireEvent.keyDown(screen.getByRole("tab", { name: /Tasks/ }), { key: "Home" }));
  expect(activitySidebarStore.getState().tab).toBe("about");
  await act(async () =>
    fireEvent.keyDown(screen.getByRole("tab", { name: "About" }), { key: "ArrowRight", shiftKey: true }),
  );
  expect(activitySidebarStore.getState().tab).toBe("about");
  await act(async () => fireEvent.keyDown(screen.getByRole("tab", { name: "About" }), { key: "ArrowRight" }));
  expect(activitySidebarStore.getState().tab).toBe("agents");
});

test("activity tabs stack label over fraction inside a single row and About stays a single label", async () => {
  connectionStore.getState().connect(activityClient());
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  await screen.findByRole("button", { name: /inspect/ });
  const tabs = screen.getAllByRole("tab");
  expect(tabs).toHaveLength(5);
  const stackClass = requireClass(styles.tabStack, "activitybar.module.css", "tabStack");
  for (const tab of tabs.slice(1)) {
    const stack = tab.firstElementChild;
    expect(stack?.classList.contains(stackClass)).toBe(true);
    expect(stack?.children).toHaveLength(2);
    expect(tab.textContent).not.toContain("\n");
  }
  const aboutStack = screen.getByRole("tab", { name: "About" }).firstElementChild;
  expect(aboutStack?.classList.contains(stackClass)).toBe(true);
  expect(aboutStack?.children).toHaveLength(1);
});

test("desktop Overview mounts a left-edge resize handle that persists width and clamps to the viewport; mobile omits it", async () => {
  const originalInnerWidth = window.innerWidth;
  try {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 1400 });
    connectionStore.getState().connect(activityClient());
    installFocusedScope(ref);
    activitySidebarStore.getState().openWith("about");
    const desktop = mount(false);
    const sidebar = await screen.findByTestId("activity-sidebar");
    const separator = screen.getByRole("separator", { name: "Resize Overview" });
    expect(separator).toBe(screen.getByTestId("overview-resize-handle"));
    expect(separator.getAttribute("aria-valuenow")).toBe(String(OVERVIEW_WIDTH_DEFAULT));
    expect(separator.getAttribute("aria-valuemin")).toBe(String(OVERVIEW_WIDTH_MIN));
    expect(separator.getAttribute("aria-valuemax")).toBe(String(OVERVIEW_WIDTH_MAX));
    expect(sidebar.style.getPropertyValue(OVERVIEW_WIDTH_PROPERTY)).toBe(`${OVERVIEW_WIDTH_DEFAULT}px`);

    // ArrowLeft widens the left-edge separator and persists to evener.prefs.overviewWidth.
    fireEvent.keyDown(separator, { key: "ArrowLeft" });
    expect(prefsStore.getState().overviewWidth).toBe(OVERVIEW_WIDTH_DEFAULT + 16);
    expect(localStorage.getItem("evener.prefs.overviewWidth")).toBe(String(OVERVIEW_WIDTH_DEFAULT + 16));
    expect(sidebar.style.getPropertyValue(OVERVIEW_WIDTH_PROPERTY)).toBe(`${OVERVIEW_WIDTH_DEFAULT + 16}px`);

    // Double-click resets back to default; End jumps to max.
    fireEvent.doubleClick(separator);
    expect(prefsStore.getState().overviewWidth).toBe(OVERVIEW_WIDTH_DEFAULT);
    fireEvent.keyDown(separator, { key: "End" });
    expect(prefsStore.getState().overviewWidth).toBe(OVERVIEW_WIDTH_MAX);

    // Narrowing the viewport clamps rendered width without discarding the persisted preference.
    Object.defineProperty(window, "innerWidth", { configurable: true, value: 960 });
    act(() => window.dispatchEvent(new Event("resize")));
    const clampedMax = overviewWidthMax(960);
    expect(separator.getAttribute("aria-valuemax")).toBe(String(clampedMax));
    expect(sidebar.style.getPropertyValue(OVERVIEW_WIDTH_PROPERTY)).toBe(`${clampedMax}px`);
    expect(prefsStore.getState().overviewWidth).toBe(OVERVIEW_WIDTH_MAX);
    desktop.unmount();

    // Mobile Overview is full-screen with no resize handle and no inline desktop width.
    mount(true);
    const mobileSidebar = await screen.findByTestId("activity-sidebar");
    expect(screen.queryByTestId("overview-resize-handle")).toBeNull();
    expect(mobileSidebar.style.getPropertyValue(OVERVIEW_WIDTH_PROPERTY)).toBe("");
  } finally {
    Object.defineProperty(window, "innerWidth", { configurable: true, value: originalInnerWidth });
  }
});

test("phone Overview enters its selected category and contains sequential keyboard focus", async () => {
  const user = userEvent.setup();
  connectionStore.getState().connect(activityClient());
  installFocusedScope(ref);
  const opener = document.body.appendChild(document.createElement("button"));
  try {
    opener.focus();
    activitySidebarStore.getState().openWith("about", opener);
    mount(true);
    const sidebar = await screen.findByTestId("activity-sidebar");
    expect(screen.getByRole("dialog", { name: "Overview" })).toBe(sidebar);
    expect(sidebar.getAttribute("aria-modal")).toBe("true");
    expect(screen.queryByRole("radiogroup")).toBeNull();
    const tablist = screen.getByRole("tablist", { name: "Overview kind" });
    const tabs = within(tablist).getAllByRole("tab");
    expect(tabs).toHaveLength(5);
    const about = within(tablist).getByRole("tab", { name: "About" });
    const panel = within(sidebar).getByRole("tabpanel");
    expect(panel.getAttribute("aria-labelledby")).toBe(about.id);
    expect(about.getAttribute("aria-selected")).toBe("true");
    expect(about.tabIndex).toBe(0);
    await waitFor(() => expect(document.activeElement).toBe(about));
    // Bare navigation keys rove across the underline tabs; modifier chords pass through.
    await act(async () => fireEvent.keyDown(about, { key: "ArrowRight", altKey: true }));
    expect(activitySidebarStore.getState().tab).toBe("about");
    await act(async () => fireEvent.keyDown(about, { key: "ArrowRight" }));
    expect(activitySidebarStore.getState().tab).toBe("agents");
    await act(async () => fireEvent.keyDown(within(tablist).getByRole("tab", { name: /^Agents/ }), { key: "End" }));
    expect(activitySidebarStore.getState().tab).toBe("tasks");
    await act(async () => fireEvent.keyDown(within(tablist).getByRole("tab", { name: /^Tasks/ }), { key: "Home" }));
    expect(activitySidebarStore.getState().tab).toBe("about");
    expect(document.activeElement).toBe(about);
    for (let step = 0; step < 20; step += 1) {
      await user.tab();
      expect(sidebar.contains(document.activeElement)).toBe(true);
    }
    for (let step = 0; step < 20; step += 1) {
      await user.tab({ shift: true });
      expect(sidebar.contains(document.activeElement)).toBe(true);
    }
    await user.keyboard("{Escape}");
    await waitFor(() => expect(document.activeElement).toBe(opener));
    expect(activitySidebarStore.getState().open).toBe(false);
  } finally {
    opener.remove();
  }
});

test("the desktop dock ends its inspection when the phone host takes over", async () => {
  const setMobile = installSwitchableViewport();
  try {
    connectionStore.getState().connect(activityClient());
    installFocusedScope(ref);
    activitySidebarStore.getState().openWith("agents");
    const desktop = mount();
    await screen.findByTestId("activity-sidebar");
    // AppShell tears the desktop surface down once the breakpoint crosses;
    // StackHost then mounts the phone surface in its place.
    setMobile(true);
    desktop.unmount();
    mount(true);
    expect(activitySidebarStore.getState().open).toBe(false);
    expect(screen.queryByTestId("activity-sidebar")).toBeNull();
    // No dialog claims the whole phone screen, so the reader's attention
    // control is not blocked by an Overview the dock left open.
    expect(document.querySelector('[aria-modal="true"]')).toBeNull();
  } finally {
    vi.unstubAllGlobals();
    resetMobileViewportForTests();
  }
});

test("a desktop remount keeps the dock's saved inspection", async () => {
  connectionStore.getState().connect(activityClient());
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  const first = mount();
  await screen.findByTestId("activity-sidebar");
  first.unmount();
  expect(activitySidebarStore.getState().open).toBe(true);
  mount();
  expect(await screen.findByTestId("activity-sidebar")).toBeTruthy();
});

test("phone Overview reopening during its exit returns focus to its selected category", async () => {
  connectionStore.getState().connect(activityClient());
  installFocusedScope(ref);
  const opener = document.body.appendChild(document.createElement("button"));
  opener.textContent = "Open Overview";
  opener.addEventListener("click", () => activitySidebarStore.getState().openWith("about", opener));
  try {
    opener.click();
    mount(true);
    const sidebar = await screen.findByTestId("activity-sidebar");
    const strip = screen.getByRole("tablist");
    const about = screen.getByRole("tab", { name: "About" });
    await waitFor(() => expect(document.activeElement).toBe(about));
    // Close and reopen in separate synchronous commits. No timer or frame can
    // run between them, so the exit animation cannot finish and the aside
    // stays mounted: the reopen must re-arm entry focus on this same strip
    // rather than mount a fresh one. AboutTab's rapid-reopen test uses the
    // same no-yield pattern.
    act(() => {
      fireEvent.keyDown(window, { key: "Escape" });
    });
    expect(activitySidebarStore.getState().open).toBe(false);
    expect(sidebar.isConnected).toBe(true);
    opener.focus();
    act(() => {
      fireEvent.click(opener);
    });
    expect(activitySidebarStore.getState().open).toBe(true);
    expect(screen.getByTestId("activity-sidebar")).toBe(sidebar);
    expect(screen.getByRole("tablist")).toBe(strip);
    await waitFor(() => expect(document.activeElement).toBe(about), { timeout: 1_500 });
  } finally {
    opener.remove();
  }
});

test("desktop Overview leaves focus with the opener and permits Tab beyond the surface", async () => {
  const user = userEvent.setup();
  connectionStore.getState().connect(activityClient());
  installFocusedScope(ref);
  const opener = document.body.appendChild(document.createElement("button"));
  const outside = document.body.appendChild(document.createElement("button"));
  try {
    opener.focus();
    activitySidebarStore.getState().openWith("about", opener);
    mount();
    await screen.findByText("owner");
    expect(document.activeElement).toBe(opener);
    screen.getByRole("tab", { name: "About" }).focus();
    let escaped = false;
    for (let step = 0; step < 20 && !escaped; step += 1) {
      await user.tab();
      escaped = !screen.getByTestId("activity-sidebar").contains(document.activeElement);
    }
    expect(escaped).toBe(true);
  } finally {
    opener.remove();
    outside.remove();
  }
});

test("pending ancestry becomes useful parent navigation only after the domain proves it", async () => {
  let known = false;
  const client = activityClient();
  const context = () => ({
    ...activityContext(ref),
    ancestryKnown: known,
    rootRef: "remote:root",
    ancestors: [{ ref: "remote:root", sessionId: "root", title: "Proven root" }],
  });
  client.on("evener/thread/activity/read", ({ scope }) => ({ ...activitySummary(ref, scope), context: context() }));
  client.on("evener/thread/delegates/list", ({ scope }) => ({
    context: context(),
    scope: scope ?? "session",
    delegates: [],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  await screen.findByText("No subagents at this level.");
  expect(screen.getByText("Finding session context…")).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Proven root" })).toBeNull();
  known = true;
  act(() => client.emitNotification(activityChangedNotification({ ref, threadId: "owner" }, ["summary"])));
  await screen.findByRole("button", { name: "Proven root" });
  expect(screen.queryByText("Finding session context…")).toBeNull();
});

test("an embedded parent column's Overview rebases its ancestor crumb onto the cascade's requested alias", async () => {
  const user = userEvent.setup();
  // The activity context names canonical refs while the cascade's source and
  // edges use "root-alias": only the cascade's requested refs can move it.
  const context = (value: string) =>
    value === "grandchild"
      ? {
          ...activityContext(value),
          ref: "canonical-grandchild",
          sessionId: "grandchild-id",
          rootRef: "canonical-root",
          parentRef: "canonical-child",
          delegateId: "d2",
          ancestors: [
            { ref: "canonical-root", sessionId: "root-id", title: "Root" },
            { ref: "canonical-child", sessionId: "child-id", title: "Child", delegateId: "d1" },
          ],
        }
      : value === "child"
        ? {
            ...activityContext(value),
            ref: "canonical-child",
            sessionId: "child-id",
            rootRef: "canonical-root",
            parentRef: "canonical-root",
            delegateId: "d1",
            ancestors: [{ ref: "canonical-root", sessionId: "root-id", title: "Root" }],
          }
        : { ...activityContext(value), ref: "canonical-root", sessionId: "root-id" };
  const client = cascadeClient(context);
  client.on("evener/thread/delegates/list", ({ ref: asked, scope }) => ({
    context: context(asked),
    scope: scope ?? "session",
    delegates: [activityDelegate({ ownerRef: asked, description: `delegate owned by ${asked}` })],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope("grandchild");
  const source = { type: "session" as const, params: { ref: "root-alias" } };
  const params: SessionZoomParams = {
    ref: "grandchild",
    source,
    edges: [
      { ownerRef: "root-alias", childRef: "child", delegateId: "d1" },
      { ownerRef: "child", childRef: "grandchild", delegateId: "d2" },
    ],
  };
  workspaceStore.setState({
    panes: [
      { id: "zoom", type: "sessionZoom", params, slot: "main" },
      // An ordinary conversation pane for the held column's ref sits beside
      // the cascade, exactly like the embedded column's own Overview.
      { id: "ordinary-child", type: "session", params: { ref: "child" }, slot: "secondary" },
    ],
    focusedPaneId: "zoom",
  });
  await act(async () => {
    await threadsStore.getState().ensureThread("child");
  });
  const mounted = render(
    <ClientProvider client={client}>
      <MotionProvider>
        <SessionChrome ref="child" paneId="zoom" />
        <ActivitySidebar />
      </MotionProvider>
    </ClientProvider>,
  );
  try {
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    await user.click(screen.getByRole("menuitem", { name: /^Overview/ }));
    const sidebar = within(screen.getByTestId("activity-sidebar"));
    fireEvent.click(sidebar.getByRole("tab", { name: /^Agents,/ }));
    // The held scope is the parent column "child", whose own ancestry is
    // proven against the canonical refs.
    await sidebar.findByRole("button", { name: /delegate owned by child/ });
    // Its root crumb must carry the cascade's requested alias, so clicking it
    // pops the whole cascade onto the alias instead of doing nothing.
    const rootCrumb = await sidebar.findByRole("button", { name: "Root" });
    await act(async () => fireEvent.click(rootCrumb));
    const pane = workspaceStore.getState().panes.find((item) => item.id === "zoom");
    if (!pane) throw new Error("Missing zoom pane after breadcrumb pop");
    expect((pane.params as SessionZoomParams).ref).toBe("root-alias");
    expect((pane.params as SessionZoomParams).edges).toEqual([]);
    expect((pane.params as SessionZoomParams).source).toBe(source);
  } finally {
    mounted.unmount();
  }
});

test("a held parent in a cascade opened from a nested session keeps its ancestors and rebases the recorded suffix", async () => {
  const user = userEvent.setup();
  // The ancestry is root -> source -> parent -> leaf, but the cascade opens
  // from the nested "source": its recorded edges begin one column below the
  // root. Titles make the ancestor crumbs findable by name.
  const context = (value: string) =>
    cascadeContext(
      value,
      value === "leaf"
        ? ["root", "source", "parent"]
        : value === "parent"
          ? ["root", "source"]
          : value === "source"
            ? ["root"]
            : [],
      (ancestor) => (ancestor === "root" ? "Root" : ancestor === "source" ? "Source" : ancestor),
    );
  const client = cascadeClient(context);
  client.on("evener/thread/delegates/list", ({ ref: asked, scope }) => ({
    context: context(asked),
    scope: scope ?? "session",
    delegates: [activityDelegate({ ownerRef: asked, description: `delegate owned by ${asked}` })],
    page: { complete: true, issues: [] },
  }));
  connectionStore.getState().connect(client);
  installFocusedScope("leaf");
  const params: SessionZoomParams = {
    ref: "leaf",
    source: { type: "session", params: { ref: "source" } },
    edges: [
      { ownerRef: "source", childRef: "parent", delegateId: "d1" },
      { ownerRef: "parent", childRef: "leaf", delegateId: "d2" },
    ],
  };
  workspaceStore.setState({
    panes: [
      { id: "zoom", type: "sessionZoom", params, slot: "main" },
      { id: "ordinary-parent", type: "session", params: { ref: "parent" }, slot: "secondary" },
    ],
    focusedPaneId: "zoom",
  });
  await act(async () => {
    await threadsStore.getState().ensureThread("parent");
  });
  const mounted = render(
    <ClientProvider client={client}>
      <MotionProvider>
        <SessionChrome ref="parent" paneId="zoom" />
        <ActivitySidebar />
      </MotionProvider>
    </ClientProvider>,
  );
  try {
    await user.click(screen.getByRole("button", { name: /session actions/i }));
    await user.click(screen.getByRole("menuitem", { name: /^Overview/ }));
    const sidebar = within(screen.getByTestId("activity-sidebar"));
    fireEvent.click(sidebar.getByRole("tab", { name: /^Agents,/ }));
    await sidebar.findByRole("button", { name: /delegate owned by parent/ });
    const crumbs = within(sidebar.getByRole("navigation", { name: "Scope" }));
    // The held scope is the parent column: its two ancestors above the
    // cascade's own start keep their authoritative refs, the crumb for the
    // cascade's source is rebound to it, and the last crumb is the held parent.
    const navRefs = () =>
      crumbs.getAllByRole("button").map((button) => button.getAttribute("data-session-navigation-ref"));
    expect(navRefs()).toEqual(["root", "source"]);
    await act(async () => fireEvent.click(crumbs.getByRole("button", { name: "Source" })));
    const pane = workspaceStore.getState().panes.find((item) => item.id === "zoom");
    if (!pane) throw new Error("Missing zoom pane after breadcrumb pop");
    expect((pane.params as SessionZoomParams).ref).toBe("source");
    expect((pane.params as SessionZoomParams).edges).toEqual([]);
  } finally {
    mounted.unmount();
  }
});
