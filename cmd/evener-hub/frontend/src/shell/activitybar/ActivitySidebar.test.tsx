import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { useStore } from "zustand";
import { MotionProvider } from "../../motion";
import type { SessionPaneParams } from "../../panes/session/Session";
import { cascadeClient, cascadeContext, cascadeThread } from "../../panes/zoom/cascadeTestUtils";
import type { SessionZoomParams } from "../../panes/zoom/intent";
import Zoom from "../../panes/zoom/Zoom";
import "../../panes/zoom";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import {
  activityClient,
  activityContext,
  activityDelegate,
  activitySummary,
} from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import { ClientProvider } from "../clientContext";
import { conversationPaneLifetime } from "../paneLifetime";
import { installFocusedScope } from "../statusbar/scopeTestUtils";
import { currentSessionRef, type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../workspace";
import { ActivitySidebar } from "./ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";

beforeAll(async () => {
  await import("../../panes/transcript");
});

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  resetDisclosureStoreForTests();
});

const ref = "remote:owner";
const mount = (mobile = false) =>
  render(
    <MotionProvider>
      <ActivitySidebar mobile={mobile} />
    </MotionProvider>,
  );
afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
  resetThreadsStoreForTests();
});

test("closed sidebar owns no read and an open tab observes only its own collection", async () => {
  const client = activityClient();
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  mount();
  expect(screen.queryByTestId("activity-sidebar")).toBeNull();
  expect(client.calls).toHaveLength(0);
  act(() => activitySidebarStore.getState().openWith("agents"));
  await screen.findByRole("button", { name: /inspect/ });
  expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list")).toHaveLength(1);
  expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(0);
  fireEvent.click(screen.getByRole("radio", { name: "Jobs, 2 of 201 running" }));
  await screen.findByText("No jobs at this level.");
  expect(client.calls.filter((c) => c.method === "evener/thread/jobs/list")).toHaveLength(1);
  expect(client.calls.filter((c) => c.method === "thread/read")).toHaveLength(1);
  fireEvent.click(screen.getByRole("button", { name: "Close Overview" }));
  await waitFor(() => expect(sessionActivitySnapshot(client, ref, "session")).toBeNull());
});

test("inactive fold and explicit keyset pages preserve useful loaded rows", async () => {
  const client = activityClient();
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
  await screen.findByRole("button", { name: /Inactive subagents \(21\)/ });
  expect(screen.queryByRole("button", { name: /done 0/ })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: /Inactive subagents/ }));
  expect(screen.getByRole("button", { name: /done 0/ })).toBeTruthy();
  expect(screen.queryByRole("button", { name: /done 20/ })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: /Show 1 more/ }));
  expect(screen.getByRole("button", { name: /done 20/ })).toBeTruthy();
  fireEvent.click(screen.getByRole("button", { name: "Load more subagents" }));
  await screen.findByRole("button", { name: /second bounded page/ });
  expect(screen.getByRole("button", { name: /inspect/ })).toBeTruthy();
  expect(client.calls.filter((c) => c.method === "evener/thread/delegates/list").map((c) => c.params)).toEqual([
    { ref, scope: "session" },
    { ref, scope: "session", cursor: "page-two" },
  ]);
});

test("a fresh sidebar case starts with inactive delegates folded after a prior reader expanded them", async () => {
  const client = activityClient();
  client.on("evener/thread/delegates/list", () => ({
    context: activityContext(),
    scope: "session",
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
  await screen.findByRole("button", { name: /Inactive subagents/ });
  expect(screen.queryByRole("button", { name: /Fresh completed delegate/ })).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: /Inactive subagents/ }));
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
  expect(sessionActivitySnapshot(client, ref, "session")).toBeNull();
  expect(client.calls.filter((c) => c.method === "evener/navigation/read")).toHaveLength(0);
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
  client.on("evener/thread/activity/read", ({ ref }) => ({
    ...activitySummary(ref),
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
  function JourneyPane() {
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
            <Zoom
              paneId={pane.id}
              params={pane.params as SessionZoomParams}
              focused={state.focusedPaneId === pane.id}
            />
          </div>
        );
      return null;
    });
  }
  const height = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", { configurable: true, value: 500 });
  try {
    render(
      <ClientProvider client={client}>
        <MotionProvider>
          <JourneyPane />
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
      expect(screen.getAllByTestId("cascade-column")).toHaveLength(2);
      expect(screen.queryAllByTestId("cascade-spine")).toHaveLength(index);
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
    expect(screen.getAllByTestId("cascade-spine")).toHaveLength(5);
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
    fireEvent.click(sidebar.getByRole("radio", { name: "About" }));
    await sidebar.findByText("wire-remote:seventh");
    expect(sidebar.queryByText("wire-remote:sixth")).toBeNull();
    window.getSelection()?.selectAllChildren(within(parent).getByText("remote:sixth real content"));
    fireEvent.scroll(within(parent).getByTestId("transcript-virtual-list"));
    expect(currentSessionRef(workspaceStore.getState())).toBe("remote:seventh");
    expect(sidebar.getByText("wire-remote:seventh")).toBeTruthy();
    fireEvent.click(sidebar.getByRole("button", { name: "child work" }));
    await sidebar.findByRole("button", { name: /Open remote:grandchild/ });
    expect(currentSessionRef(workspaceStore.getState())).toBe("remote:child");
    fireEvent.click(sidebar.getByRole("radio", { name: "About" }));
    await sidebar.findByText("wire-remote:child");
    fireEvent.click(sidebar.getByRole("button", { name: "root work" }));
    await sidebar.findByRole("button", { name: /Open remote:child/ });
    expect(activitySidebarStore.getState().tab).toBe("agents");
    fireEvent.click(sidebar.getByRole("button", { name: /Open remote:child/ }));
    await sidebar.findByText("wire-remote:child");
    expect(currentSessionRef(workspaceStore.getState())).toBe("remote:child");
    expect(activitySidebarStore.getState().tab).toBe("about");
    fireEvent.click(sidebar.getByRole("button", { name: "root work" }));
    await sidebar.findByRole("button", { name: /Open remote:child/ });
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

test("activity tabs keep a named keyboard radio group without a visible heading", async () => {
  connectionStore.getState().connect(activityClient());
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("agents");
  mount();
  await screen.findByRole("button", { name: /inspect/ });
  expect(screen.getByRole("complementary", { name: "Overview" })).toBeTruthy();
  expect(screen.getByRole("radiogroup", { name: "Overview kind" })).toBeTruthy();
  expect(screen.queryByText("Overview kind")).toBeNull();
  await act(async () => fireEvent.keyDown(screen.getByRole("radio", { name: /Agents/ }), { key: "End" }));
  expect(activitySidebarStore.getState().tab).toBe("about");
  await act(async () => fireEvent.keyDown(screen.getByRole("radio", { name: "About" }), { key: "Home" }));
  expect(activitySidebarStore.getState().tab).toBe("agents");
  await act(async () => fireEvent.keyDown(screen.getByRole("radio", { name: /Agents/ }), { key: "ArrowRight" }));
  expect(activitySidebarStore.getState().tab).toBe("jobs");
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
    const about = screen.getByRole("radio", { name: "About" });
    await waitFor(() => expect(document.activeElement).toBe(about));
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
    screen.getByRole("radio", { name: "About" }).focus();
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
  client.on("evener/thread/activity/read", () => ({ ...activitySummary(ref), context: context() }));
  client.on("evener/thread/delegates/list", () => ({
    context: context(),
    scope: "session",
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
  act(() =>
    client.emitNotification({
      method: "evener/thread/activity/changed",
      params: { ref, threadId: "owner", sessionId: "owner", resources: ["summary"] },
    }),
  );
  await screen.findByRole("button", { name: "Proven root" });
  expect(screen.queryByText("Finding session context…")).toBeNull();
});
