import type { SessionActivityContext, SessionActivitySummary, ThreadReadResponse } from "@evener/appwire-client";
import { deferred } from "@evener/appwire-client/testing/deferred";
import { act, cleanup, fireEvent, render, renderHook, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { MotionProvider } from "../../motion";
import { ActivitySidebar } from "../../shell/activitybar/ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "../../shell/activitybar/activitySidebarStore";
import { ClientProvider } from "../../shell/clientContext";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { activityDelegate, activitySummary, activityThread } from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { resetDisclosureStoreForTests } from "../../widgets/disclosure/disclosureStore";
import { getToasts, pushToast, resetToastStoreForTests } from "../../widgets/toast/store";
import { useAttachments } from "../session/composer/attachments/useAttachments";
import { readComposerDraft } from "../session/composer/draft";
import { installControlledImageEncoding } from "../session/testing/imageEncoding";
import { enterAgentCascade } from "./actions";
import { cascadeClient, cascadeContext } from "./cascadeTestUtils";
import type { SessionZoomParams } from "./intent";
import Zoom from "./Zoom";
import "./index";

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  resetThreadsStoreForTests();
  resetDisclosureStoreForTests();
  resetToastStoreForTests();
});
afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
  resetThreadsStoreForTests();
  resetDisclosureStoreForTests();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

function fixture(contextOverride: (ref: string) => SessionActivityContext | undefined = () => undefined) {
  const context = (ref: string) =>
    contextOverride(ref) ??
    cascadeContext(ref, ref === "grandchild" ? ["root", "child"] : ref === "root" ? [] : ["root"]);
  const client = cascadeClient(context);
  const delegatesPage = (ref: string) => ({
    context: context(ref),
    scope: "session" as const,
    delegates: [
      activityDelegate({
        ownerRef: ref,
        childRef: "sibling",
        delegateId: "edge-sibling",
        description: `${ref} direct child`,
      }),
    ],
    page: { complete: true, issues: [] },
  });
  client.on("evener/thread/delegates/list", ({ ref }) => delegatesPage(ref));
  const params: SessionZoomParams = {
    ref: "grandchild",
    source: { type: "session", params: { ref: "root" } },
    edges: [
      { ownerRef: "root", childRef: "child", delegateId: "edge-child" },
      { ownerRef: "child", childRef: "grandchild", delegateId: "edge-grandchild" },
    ],
  };
  workspaceStore.setState({
    panes: [{ id: "cascade", type: "sessionZoom", slot: "main", params }],
    focusedPaneId: "cascade",
  });
  connectionStore.getState().connect(client);
  const contents = (cascade = true) => (
    <ClientProvider client={client}>
      <MotionProvider>
        {cascade && <Zoom paneId="cascade" params={params} focused />}
        <ActivitySidebar />
      </MotionProvider>
    </ClientProvider>
  );
  return {
    client,
    context,
    delegatesPage,
    contents,
    mount() {
      return render(contents());
    },
  };
}
function scope(ref: string) {
  const element = [...screen.queryAllByTestId("cascade-column"), ...screen.queryAllByTestId("cascade-spine")].find(
    (item) => item.getAttribute("data-scope-ref") === ref,
  );
  if (!element) throw new Error(`Missing scope ${ref}`);
  return within(element);
}
async function openPeek(ref: string, tab = "Agents") {
  const trigger = await scope(ref).findByRole("button", { name: new RegExp(`^${tab},.* - peek at ${ref}$`) });
  await act(async () => fireEvent.click(trigger));
  return screen.findByTestId("activity-peek");
}
function leaf() {
  const pane = workspaceStore.getState().panes.find((record) => record.id === "cascade");
  if (!pane) throw new Error("Missing cascade pane");
  return (pane.params as SessionZoomParams).ref;
}

test.each(["root", "child", "grandchild"])(
  "cascade scope %s keeps About out of its four activity peeks",
  async (ref) => {
    fixture().mount();
    await scope("child").findByRole("button", { name: "Open conversation" });
    const controls = scope(ref);
    for (const category of ["Agents", "Jobs", "Watches", "Tasks"]) {
      expect(controls.getByRole("button", { name: new RegExp(`^${category},.* - peek at ${ref}$`) })).toBeTruthy();
    }
    expect(controls.getByRole("button", { name: `Tasks, 0 of 0 done - peek at ${ref}` })).toBeTruthy();
    expect(controls.queryByRole("button", { name: `About - peek at ${ref}` })).toBeNull();
    expect(controls.getAllByRole("button", { name: new RegExp(` - peek at ${ref}$`) })).toHaveLength(4);
  },
);

test.each(["success", "failure"] as const)(
  "a pending source encode %s cannot change a same-ID restored cascade source",
  async (outcome) => {
    const encoding = installControlledImageEncoding();
    const { mount } = fixture();
    const view = mount();
    await scope("child").findByRole("button", { name: "Open conversation" });
    const old = workspaceStore.getState().panes[0];
    if (!old) throw new Error("Missing original cascade");
    const lifetime = conversationPaneLifetime(old);
    const source = lifetime.composer;
    if (!source) throw new Error("Missing original source");
    const attachments = renderHook(() => useAttachments(source.editor, source.attachments));
    act(() =>
      attachments.result.current.ingestFiles(
        [new File([new Uint8Array([1, 2, 3])], "original.png", { type: "image/png" })],
        (message) => pushToast("error", message),
      ),
    );
    expect(source.attachments.getState().items[0]?.pending).toBe(true);
    const generation = source.attachments.getState().generationRef.current;
    attachments.unmount();
    view.unmount();
    resetWorkspaceStoreForTests();
    const restored = { ...old, params: { ...(old.params as SessionZoomParams) } };
    workspaceStore.setState({ panes: [restored], focusedPaneId: restored.id });
    const replacement = conversationPaneLifetime(restored);
    const replacementSource = replacement.composer;
    if (!replacementSource) throw new Error("Missing restored source");
    const text = "new original [image 1]";
    replacementSource.editor.write(text, text.length);
    mount();
    await scope("grandchild").findByRole("button", { name: "Open conversation" });
    await act(async () => {
      if (outcome === "success") await encoding.resolve();
      else await encoding.reject();
    });
    expect(lifetime.alive).toBe(false);
    expect(replacement.serial).not.toBe(lifetime.serial);
    expect(source.attachments.getState().generationRef.current).toBeGreaterThan(generation);
    expect(source.attachments.getState().items).toEqual([]);
    expect(replacementSource.attachments.getState().items).toEqual([]);
    expect(readComposerDraft("root")).toEqual({ text, skillNames: [] });
    expect(replacementSource.getSnapshot().text).toBe(text);
    expect(workspaceStore.getState().panes[0]).toBe(restored);
    expect(getToasts()).toEqual([]);
  },
);

test("a closed spine keeps unknown runtime explicit without fetching its history or activity collections", async () => {
  const { client, mount } = fixture();
  const metadata = deferred<ThreadReadResponse>();
  client.on("thread/read", ({ ref, requestGeneration }) => {
    if (ref === "root") return metadata.promise;
    const response = activityThread(ref);
    return {
      ...response,
      requestGeneration,
      thread: { ...response.thread, id: `wire-${ref}`, sessionId: `session-${ref}`, name: ref, turns: [] },
    };
  });
  mount();
  try {
    expect(await scope("root").findByText("State unknown")).toBeTruthy();
    expect(scope("root").getByRole("button", { name: "Agents, counts unknown - peek at root" })).toBeTruthy();
    expect(
      client.calls.filter(
        (call) =>
          /\/delegates\/list|\/jobs\/list|\/watches\/list/.test(call.method) &&
          (call.params as { ref: string }).ref === "root",
      ),
    ).toHaveLength(0);
    expect(
      client.calls.filter(
        (call) =>
          call.method === "thread/read" &&
          (call.params as { ref: string; includeTurns?: boolean }).ref === "root" &&
          (call.params as { includeTurns?: boolean }).includeTurns !== false,
      ),
    ).toHaveLength(0);
  } finally {
    await act(async () =>
      metadata.resolve({
        ...activityThread("root"),
        thread: { ...activityThread("root").thread, id: "wire-root", sessionId: "session-root" },
      }),
    );
  }
});

test("only a user drill animates a new scope while late ancestry commits immediately without changing keyboard focus", async () => {
  let fourthContext = { ...cascadeContext("fourth", []), ancestryKnown: false };
  const { client, context, mount } = fixture((ref) =>
    ref === "fourth" ? fourthContext : ref === "earlier" ? cascadeContext(ref, []) : undefined,
  );
  const authority = deferred<SessionActivitySummary>();
  client.on("evener/thread/activity/read", ({ ref, scope }) =>
    ref === "fourth" ? authority.promise : { ...activitySummary(ref, scope), context: context(ref) },
  );
  mount();
  await scope("child").findByRole("button", { name: "Open conversation" });
  const focused = screen.getByRole("button", { name: "Return to previous view" });
  focused.focus();
  act(() =>
    enterAgentCascade(
      activityDelegate({ ownerRef: "grandchild", childRef: "fourth", delegateId: "edge-fourth" }),
      "cascade",
    ),
  );
  const added = screen
    .getAllByTestId("cascade-column")
    .find((column) => column.getAttribute("data-scope-ref") === "fourth");
  if (!added) throw new Error("Missing drilled scope");
  try {
    expect(added.style.transform).toContain("48px");
    expect(document.activeElement).toBe(focused);
  } finally {
    fourthContext = cascadeContext("fourth", ["earlier", "root", "child", "grandchild"]);
    await act(async () =>
      authority.resolve({
        ...activitySummary("fourth"),
        context: context("fourth"),
      }),
    );
  }
  const hydrated = screen
    .getAllByTestId("cascade-spine")
    .find((spine) => spine.getAttribute("data-scope-ref") === "earlier");
  if (!hydrated) throw new Error("Missing authoritative ancestor");
  expect(hydrated.style.transform).not.toContain("48px");
  expect(document.activeElement).toBe(focused);
});

test("an ancestor peek drills its explicit owner and truncates the branch while leaving other panes intact", async () => {
  const { client, delegatesPage, mount } = fixture();
  mount();
  await scope("child").findByRole("button", { name: "Open conversation" });
  const other = { id: "other", type: "doc" as const, slot: "secondary" as const, params: { ref: "unrelated" } };
  act(() => workspaceStore.setState((state) => ({ panes: [...state.panes, other], focusedPaneId: other.id })));
  // The root peek's rows arrive late, as on a loaded host, so the find below
  // overlaps them on every run and pins its find-before-act order (#3928).
  client.on("evener/thread/delegates/list", async ({ ref }) => {
    if (ref === "root") await new Promise((resolve) => setTimeout(resolve, 50));
    return delegatesPage(ref);
  });
  const peek = await openPeek("root");
  expect(leaf()).toBe("grandchild");
  // Found before act(): a Testing Library wait turns the act environment off
  // until a macrotask after it ends, so an update landing inside an act() scope
  // in that window makes React warn.
  const sibling = await within(peek).findByRole("button", { name: /root direct child/ });
  await act(async () => fireEvent.click(sibling));
  await waitFor(() => expect(leaf()).toBe("sibling"));
  expect(workspaceStore.getState().panes.find((pane) => pane.id === "cascade")?.params).toMatchObject({
    edges: [{ ownerRef: "root", childRef: "sibling", delegateId: "edge-sibling" }],
  });
  expect(workspaceStore.getState().panes.find((pane) => pane.id === other.id)).toBe(other);
});

test("closing a leaf peek releases only its collection while the same-ref sidebar still receives direct delegates", async () => {
  const { client, context, contents, mount } = fixture();
  activitySidebarStore.getState().openWith("agents");
  const { rerender } = mount();
  await within(await screen.findByTestId("activity-sidebar")).findByRole("button", { name: /grandchild direct child/ });
  const reads = () =>
    client.calls.filter(
      (call) => call.method === "evener/thread/delegates/list" && (call.params as { ref: string }).ref === "grandchild",
    ).length;
  await openPeek("grandchild");
  expect(reads()).toBe(1);
  await act(async () =>
    fireEvent.click(within(screen.getByTestId("activity-peek")).getByRole("button", { name: "Close activity peek" })),
  );
  client.on("evener/thread/delegates/list", ({ ref }) => ({
    context: context(ref),
    scope: "session",
    delegates: [activityDelegate({ ownerRef: ref, description: "Fresh sidebar row" })],
    page: { complete: true, issues: [] },
  }));
  act(() =>
    client.emitNotification({
      method: "evener/thread/activity/changed",
      params: {
        ref: "grandchild",
        threadId: "wire-grandchild",
        sessionId: "session-grandchild",
        resources: ["delegates"],
      },
    }),
  );
  await within(screen.getByTestId("activity-sidebar")).findByRole("button", { name: /Fresh sidebar row/ });
  expect(reads()).toBe(2);
  // The readable transcript also observes delegates. Remove that independent
  // reader before checking release of the sidebar's final collection demand.
  rerender(contents(false));
  await act(async () =>
    fireEvent.click(within(screen.getByTestId("activity-sidebar")).getByRole("radio", { name: /Jobs/ })),
  );
  const before = reads();
  act(() =>
    client.emitNotification({
      method: "evener/thread/activity/changed",
      params: {
        ref: "grandchild",
        threadId: "wire-grandchild",
        sessionId: "session-grandchild",
        resources: ["delegates"],
      },
    }),
  );
  await act(async () => Promise.resolve());
  expect(reads()).toBe(before);
});

test("an initially empty incomplete parent page retains its real continuation and scroll does not dismiss it", async () => {
  const { client, context, mount } = fixture();
  client.on("evener/thread/delegates/list", ({ ref, cursor }) => ({
    context: context(ref),
    scope: "session",
    delegates:
      cursor === "page-two"
        ? Array.from({ length: 20 }, (_, index) =>
            activityDelegate({ ownerRef: ref, delegateId: `page-${index}`, description: `Direct page row ${index}` }),
          )
        : cursor === "page-three"
          ? [activityDelegate({ ownerRef: ref, delegateId: "page-20", description: "Direct page row 20" })]
          : [],
    page: {
      complete: cursor === "page-three",
      issues: [],
      ...(cursor !== "page-three" ? { nextCursor: cursor ? "page-three" : "page-two" } : {}),
    },
  }));
  mount();
  const peek = await openPeek("root");
  await within(peek).findByRole("button", { name: /Direct page row 19/ });
  expect(within(peek).getAllByRole("button", { name: /Direct page row/ })).toHaveLength(20);
  const reads = () =>
    client.calls
      .filter(
        (call) => call.method === "evener/thread/delegates/list" && (call.params as { ref: string }).ref === "root",
      )
      .map((call) => call.params);
  expect(reads()).toEqual([
    { ref: "root", scope: "session" },
    { ref: "root", scope: "session", cursor: "page-two" },
  ]);
  const more = await within(peek).findByRole("button", { name: "Load more subagents" });
  fireEvent.scroll(peek);
  expect(screen.getByTestId("activity-peek")).toBe(peek);
  await act(async () => fireEvent.click(more));
  await within(peek).findByRole("button", { name: /Direct page row 20/ });
  expect(within(peek).getAllByRole("button", { name: /Direct page row/ })).toHaveLength(21);
  expect(reads()).toEqual([
    { ref: "root", scope: "session" },
    { ref: "root", scope: "session", cursor: "page-two" },
    { ref: "root", scope: "session", cursor: "page-three" },
  ]);
  expect(leaf()).toBe("grandchild");
});

test("a parent Tasks peek mounts the existing scoped task list and opens no task pane", async () => {
  const { client, mount } = fixture();
  client.on("evener/tasks/list", () => ({
    data: [{ id: 1, type: "implement", description: "Parent scoped task", prompt: "", status: "open" }],
  }));
  mount();
  await scope("child").findByRole("button", { name: "Open conversation" });
  expect(client.calls.filter((call) => call.method === "evener/tasks/list")).toHaveLength(0);
  const peek = await openPeek("child", "Tasks");
  expect(await within(peek).findByText("Parent scoped task")).toBeTruthy();
  expect(workspaceStore.getState().panes).toHaveLength(1);
  expect(leaf()).toBe("grandchild");
});

test("spine pop and counts are separate keyboard targets and Escape closes the peek before the sidebar", async () => {
  const user = userEvent.setup();
  const { mount } = fixture();
  activitySidebarStore.getState().openWith("agents");
  mount();
  const pop = await scope("root").findByRole("button", { name: "Show root" });
  pop.focus();
  await user.tab();
  const trigger = scope("root").getByRole("button", { name: "Agents, counts unknown - peek at root" });
  expect(document.activeElement).toBe(trigger);
  await user.keyboard("{Enter}");
  const peek = await screen.findByTestId("activity-peek");
  await waitFor(() => expect(peek.contains(document.activeElement)).toBe(true));
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByTestId("activity-peek")).toBeNull());
  expect(activitySidebarStore.getState().open).toBe(true);
  expect(leaf()).toBe("grandchild");
  expect(document.activeElement).toBe(trigger);
  await user.keyboard("{Escape}");
  expect(activitySidebarStore.getState().open).toBe(false);
  expect(leaf()).toBe("grandchild");
});

// A cascade scope's Agents chip counts that scope's subagents at every depth,
// as the footer does: here each scope runs one child of its own and three in
// all, finished one of four.
test("a cascade scope's Agents chip counts its subagents at every depth", async () => {
  const { client, mount } = fixture();
  client.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    context: cascadeContext(ref, ref === "grandchild" ? ["root", "child"] : ref === "root" ? [] : ["root"]),
    delegates:
      scope === "subtree"
        ? { known: true, total: 4, active: 3, failed: 0, completed: 1 }
        : { known: true, total: 1, active: 1, failed: 0, completed: 0 },
  }));
  mount();
  expect(await scope("root").findByRole("button", { name: "Agents, 3 of 4 active - peek at root" })).toBeTruthy();
});
