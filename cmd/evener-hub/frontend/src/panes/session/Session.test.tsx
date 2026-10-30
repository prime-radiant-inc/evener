import type { ThreadReadResponse } from "@evener/appwire-client";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IDBFactory, IDBKeyRange } from "fake-indexeddb";
import { useEffect, useSyncExternalStore } from "react";
import { afterEach, beforeAll, beforeEach, expect, onTestFinished, test, vi } from "vitest";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "../../shell/activitybar/activitySidebarStore";
import { openAttentionSession, restoreCurrentAttentionEntry } from "../../shell/attentionHistory";
import { ClientProvider } from "../../shell/clientContext";
import { NeedsYouProvider } from "../../shell/NeedsYouProvider";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import * as StatusBarModule from "../../shell/statusbar/StatusBar";
import { resetMobileViewportForTests } from "../../shell/useIsMobile";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
import { resetActivityPanelStoreForTests } from "../../stores/activityPanel";
import { resetConnectionStoreForTests } from "../../stores/connection";
import { MutationOutboxIndexedDB } from "../../stores/mutationOutboxIndexedDB";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { sessionActivitySnapshot } from "../../stores/sessionActivity";
import { activityDelegate, activitySummary } from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests, setMutationStorageForTests, threadsStore } from "../../stores/threads";
import { ALL_CAPABILITIES as CAPABILITIES } from "../../threadTestUtils";
import { getToasts } from "../../widgets/toast/store";
import { enterAgentCascade } from "../zoom/actions";
import { cascadeContext } from "../zoom/cascadeTestUtils";
import type { SessionZoomParams } from "../zoom/intent";
import Zoom from "../zoom/Zoom";
import * as SessionChromeModule from "./chrome/SessionChrome";
import { resetAskDockStoreForTests } from "./composer/askDock/askDockStore";
import * as PngEncoding from "./composer/attachments/encodePng";
import * as ComposerModule from "./composer/Composer";
import { requestComposerFocus } from "./composer/composerFocus";
import { writeComposerDraft } from "./composer/draft";
import { resetPendingTurnsStoreForTests } from "./composer/queue/pendingTurnsStore";
import { flushPendingTurnsProjectionForTests } from "./composer/queue/testing/flushPendingTurnsProjection";
import { retainedConversationComposer } from "./composer/retainedComposer";
import type { SessionPaneParams } from "./Session";
import "./index";
import { settleActivityDiscovery } from "./testing/activityDiscovery";
import { CommittedSession as Session } from "./testing/CommittedSession";
import { installControlledImageEncoding } from "./testing/imageEncoding";
import { captureTranscriptView } from "./transcript/flow/transcriptViewRegistry";
import { installTranscriptGeometry } from "./transcript/transcriptReadingGeometryTestUtils";
import { retainedTranscriptReadView } from "./transcript/transcriptReadView";
import { resetTranscriptPagingForTests } from "./transcript/useTranscript";
import "./testing/editorGeometry";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectReadyFakeClient } from "../../testing/connectionTestUtils";
import { readResponse } from "./sessionTestUtils";

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

test("the real Session binds its committed pane source across a view remount", async ({ onTestFinished }) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  const encoding = installControlledImageEncoding();
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("root"));
  const pane = { id: "source-pane", type: "session" as const, params: { ref: "root" }, slot: "main" as const };
  workspaceStore.setState({ panes: [pane], focusedPaneId: pane.id });
  const first = render(
    <ClientProvider client={fake}>
      <Session params={pane.params} paneId={pane.id} focused />
    </ClientProvider>,
  );
  await screen.findByRole("textbox", { name: "Message" });
  await settleActivityDiscovery("root");
  await flushPendingTurnsProjectionForTests();
  const file = new File([new Uint8Array([1, 2, 3])], "source.png", { type: "image/png" });
  const picker = first.container.querySelector('input[type="file"]');
  if (!picker) throw new Error("Session has no image picker");
  fireEvent.change(picker, { target: { files: [file] } });
  const source = conversationPaneLifetime(pane).composer;
  expect(source?.attachments.getState().items).toHaveLength(1);
  first.unmount();
  render(
    <ClientProvider client={fake}>
      <Session params={pane.params} paneId={pane.id} focused />
    </ClientProvider>,
  );
  await screen.findByRole("button", { name: "Remove source.png" });
  expect(screen.getByRole("textbox", { name: "Message" }).textContent).toBe("[image 1]");
  await act(async () => encoding.resolve());
  await screen.findByRole("button", { name: "View source.png" });
  expect(conversationPaneLifetime(pane).composer).toBe(source);
  expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/steer")).toEqual([]);
});

test("the real Session consumer retains its structured editor and pending images through attention history", async ({
  onTestFinished,
}) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  const encoding = installControlledImageEncoding();
  const fake = connectReadyFakeClient();
  fake.on("thread/read", ({ ref }) => {
    const threadRef = ref ?? "origin";
    return readResponse(threadRef, {
      evener: {
        ref: threadRef,
        capabilities: { ...CAPABILITIES, skillInput: true, commandInput: true },
        queue: { revision: 0 },
      },
    });
  });
  const origin = { id: "editor-origin", type: "session" as const, params: { ref: "origin" }, slot: "main" as const };
  const secondary = {
    id: "editor-secondary",
    type: "session" as const,
    params: { ref: "secondary" },
    slot: "secondary" as const,
  };
  window.history.replaceState({}, "", "/s/origin");
  workspaceStore.setState({ panes: [origin, secondary], focusedPaneId: origin.id });
  const text = "Keep /review and /deploy";
  const mentions = [
    { kind: "skill" as const, name: "review", offset: 5 },
    { kind: "command" as const, name: "deploy", offset: 17 },
  ];
  writeComposerDraft("origin", { text, skillNames: ["review"], commandNames: ["deploy"], mentions });
  const secondaryText = "Only /review and /deploy in X";
  const secondaryMentions = [
    { kind: "skill" as const, name: "review", offset: 5 },
    { kind: "command" as const, name: "deploy", offset: 17 },
  ];
  writeComposerDraft("secondary", {
    text: secondaryText,
    skillNames: ["review"],
    commandNames: ["deploy"],
    mentions: secondaryMentions,
  });
  const secondarySource = conversationPaneLifetime(secondary).composer;
  if (!secondarySource) throw new Error("secondary pane has no retained composer source");
  expect(secondarySource.getSnapshot()).toMatchObject({
    text: secondaryText,
    skillNames: ["review"],
    commandNames: ["deploy"],
    mentions: secondaryMentions,
  });
  const focusedPaneMounts: string[] = [];
  const focusedPaneUnmounts: string[] = [];
  const focusedPaneSources: Array<{
    paneId: string;
    source: ReturnType<typeof conversationPaneLifetime>["composer"];
    snapshot: ReturnType<NonNullable<ReturnType<typeof conversationPaneLifetime>["composer"]>["getSnapshot"]>;
  }> = [];

  function FocusedSessionView({ pane }: { pane: typeof origin | typeof secondary }) {
    const committedPane = workspaceStore.getState().panes.find((candidate) => candidate.id === pane.id);
    if (!committedPane) throw new Error(`focused Session pane ${pane.id} is absent from workspace`);
    const source = conversationPaneLifetime(committedPane).composer;
    if (!source) throw new Error(`focused Session pane ${pane.id} has no composer source`);
    focusedPaneSources.push({ paneId: pane.id, source, snapshot: source.getSnapshot() });
    useEffect(() => {
      focusedPaneMounts.push(pane.id);
      return () => {
        focusedPaneUnmounts.push(pane.id);
      };
    }, [pane.id]);
    return <Session params={pane.params} paneId={pane.id} focused />;
  }

  function FocusedSessionHost() {
    const workspace = useSyncExternalStore(workspaceStore.subscribe, workspaceStore.getState);
    useEffect(() => {
      const restore = () => restoreCurrentAttentionEntry();
      window.addEventListener("popstate", restore);
      return () => window.removeEventListener("popstate", restore);
    }, []);
    const pane = workspace.panes.find((candidate) => candidate.id === workspace.focusedPaneId);
    return pane?.type === "session" ? (
      <FocusedSessionView key={pane.id} pane={pane as typeof origin | typeof secondary} />
    ) : null;
  }

  const view = render(
    <ClientProvider client={fake}>
      <NeedsYouProvider>
        <FocusedSessionHost />
      </NeedsYouProvider>
    </ClientProvider>,
  );
  const editor = await screen.findByRole("textbox", { name: "Message" });
  await settleActivityDiscovery("origin");
  await flushPendingTurnsProjectionForTests();
  await waitFor(() => expect(document.activeElement).toBe(editor));
  const picker = view.container.querySelector('input[type="file"]');
  if (!picker) throw new Error("Session has no image picker");
  fireEvent.change(picker, {
    target: { files: [new File([new Uint8Array([9, 8, 7])], "success.png", { type: "image/png" })] },
  });
  fireEvent.change(picker, {
    target: { files: [new File([new Uint8Array([6, 5, 4])], "failure.png", { type: "image/png" })] },
  });
  const source = conversationPaneLifetime(origin).composer;
  if (!source) throw new Error("Session has no retained composer source");
  const originLifetime = conversationPaneLifetime(origin);
  const stagedText = `[image 1][image 2]${text}`;
  const stagedMentions = mentions.map((mention) => ({ ...mention, offset: mention.offset + 18 }));
  expect(source.getSnapshot()).toMatchObject({
    text: stagedText,
    skillNames: ["review"],
    commandNames: ["deploy"],
    mentions: stagedMentions,
  });
  expect(source.attachments.getState().items.map(({ name, pending }) => ({ name, pending }))).toEqual([
    { name: "success.png", pending: true },
    { name: "failure.png", pending: true },
  ]);
  const routeSelection = document.createRange();
  routeSelection.setStart(editor.firstChild ?? editor, 5);
  routeSelection.collapse(true);
  window.getSelection()?.removeAllRanges();
  window.getSelection()?.addRange(routeSelection);

  act(() => openAttentionSession("destination"));
  await screen.findByRole("textbox", { name: "Message" });
  const destination = workspaceStore
    .getState()
    .panes.find((pane) => pane.id === workspaceStore.getState().focusedPaneId);
  if (!destination) throw new Error("attention opener did not present a destination reader");
  const destinationLifetime = conversationPaneLifetime(destination);
  const destinationSource = destinationLifetime.composer;
  const destinationSnapshot = destinationSource?.getSnapshot();
  expect(workspaceStore.getState().panes).toContain(origin);
  expect(focusedPaneUnmounts).toContain(origin.id);
  expect(focusedPaneMounts).toContain(destination.id);
  expect(document.activeElement).not.toBe(screen.getByRole("textbox", { name: "Message" }));
  await act(async () => encoding.resolve(0));
  await act(async () => encoding.reject(1));
  expect(getToasts().map(({ kind }) => kind)).toEqual(["error"]);
  expect(source.attachments.getState().items).toEqual([
    expect.objectContaining({ marker: 1, name: "success.png", data: "AQID", pending: false }),
  ]);
  expect(vi.mocked(PngEncoding.reencodeToPng)).toHaveBeenCalledTimes(2);

  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.back();
    await back;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(origin.id));
  const returnedEditor = await screen.findByRole("textbox", { name: "Message" });
  expect(workspaceStore.getState().panes).toContain(origin);
  expect(conversationPaneLifetime(origin)).toBe(originLifetime);
  expect(conversationPaneLifetime(origin).composer).toBe(source);
  const retainedText = `[image 1]${text}`;
  const retainedMentions = mentions.map((mention) => ({ ...mention, offset: mention.offset + 9 }));
  expect(source.getSnapshot()).toMatchObject({
    text: retainedText,
    skillNames: ["review"],
    commandNames: ["deploy"],
    mentions: retainedMentions,
  });
  expect(returnedEditor.textContent).toBe(retainedText);
  expect(source.attachments.getState().items).toEqual([
    expect.objectContaining({ marker: 1, name: "success.png", mediaType: "image/png", data: "AQID", pending: false }),
  ]);
  expect(vi.mocked(PngEncoding.reencodeToPng)).toHaveBeenCalledTimes(2);
  expect(document.activeElement).not.toBe(returnedEditor);
  await userEvent.click(returnedEditor);
  expect(document.activeElement).toBe(returnedEditor);
  act(() => returnedEditor.blur());
  act(() => requestComposerFocus("origin"));
  await waitFor(() => expect(document.activeElement).toBe(returnedEditor));
  expect(fake.calls.filter((call) => call.method === "turn/start" || call.method === "turn/steer")).toEqual([]);
  const forward = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.forward();
    await forward;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(destination.id));
  expect(workspaceStore.getState().panes).toContain(destination);
  expect(conversationPaneLifetime(destination)).toBe(destinationLifetime);
  expect(focusedPaneUnmounts.filter((id) => id === origin.id)).toHaveLength(2);
  expect(focusedPaneMounts.filter((id) => id === destination.id)).toHaveLength(2);
  expect(destinationSource?.getSnapshot()).toMatchObject({
    text: destinationSnapshot?.text,
    skillNames: destinationSnapshot?.skillNames,
    commandNames: destinationSnapshot?.commandNames,
    mentions: destinationSnapshot?.mentions,
  });

  act(() => workspaceStore.getState().focusPane(secondary.id));
  const secondaryEditor = await screen.findByRole("textbox", { name: "Message" });
  expect(window.location.pathname).toBe("/s/destination");
  expect(focusedPaneMounts).toContain(secondary.id);
  expect(focusedPaneUnmounts).toContain(origin.id);
  expect(focusedPaneSources.at(-1)).toMatchObject({ paneId: secondary.id, source: secondarySource });
  expect(focusedPaneSources.at(-1)?.snapshot).toMatchObject({ mentions: secondaryMentions });
  expect(secondaryEditor.textContent).toBe(secondaryText);
  expect(secondarySource.getSnapshot()).toMatchObject({
    text: secondaryText,
    skillNames: ["review"],
    commandNames: ["deploy"],
    mentions: secondaryMentions,
  });
  const secondaryPane = workspaceStore.getState().panes.find((pane) => pane.id === secondary.id);
  if (!secondaryPane) throw new Error("focus-only departure lost the existing secondary reader");
  const secondaryLifetime = conversationPaneLifetime(secondaryPane);
  const originSnapshot = source.getSnapshot();
  expect(source.getSnapshot()).toEqual(originSnapshot);
  expect(destinationSource?.getSnapshot()).toMatchObject({
    text: destinationSnapshot?.text,
    skillNames: destinationSnapshot?.skillNames,
    commandNames: destinationSnapshot?.commandNames,
    mentions: destinationSnapshot?.mentions,
  });
  const secondaryPicker = view.container.querySelector('input[type="file"]');
  if (!secondaryPicker) throw new Error("secondary Session has no image picker");
  fireEvent.change(secondaryPicker, {
    target: { files: [new File([new Uint8Array([4, 3, 2, 1])], "secondary.png", { type: "image/png" })] },
  });
  expect(secondarySource.attachments.getState().items[0]).toMatchObject({ name: "secondary.png", pending: true });
  expect(vi.mocked(PngEncoding.reencodeToPng)).toHaveBeenCalledTimes(3);
  const secondarySnapshot = secondarySource.getSnapshot();
  act(() => openAttentionSession("third"));
  const third = workspaceStore.getState().panes.find((pane) => pane.id === workspaceStore.getState().focusedPaneId);
  if (!third) throw new Error("attention opener did not present the third reader");
  await screen.findByRole("textbox", { name: "Message" });
  expect(focusedPaneUnmounts).toContain(secondary.id);
  await act(async () => encoding.resolve(2));
  const secondaryBack = new Promise<void>((resolve) =>
    window.addEventListener("popstate", () => resolve(), { once: true }),
  );
  await act(async () => {
    window.history.back();
    await secondaryBack;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(secondary.id));
  const restoredSecondaryEditor = await screen.findByRole("textbox", { name: "Message" });
  expect(workspaceStore.getState().panes).toContain(secondaryPane);
  expect(conversationPaneLifetime(secondaryPane)).toBe(secondaryLifetime);
  expect(secondaryLifetime.composer).toBe(secondarySource);
  expect(secondarySource.getSnapshot()).toMatchObject({
    text: `[image 1]${secondaryText}`,
    skillNames: ["review"],
    commandNames: ["deploy"],
    mentions: secondaryMentions.map((mention) => ({ ...mention, offset: mention.offset + 9 })),
  });
  expect(restoredSecondaryEditor.textContent).toBe(`[image 1]${secondaryText}`);
  expect(secondarySource.attachments.getState().items).toEqual([
    expect.objectContaining({
      marker: 1,
      name: "secondary.png",
      mediaType: "image/png",
      data: "AQID",
      pending: false,
    }),
  ]);
  expect(focusedPaneMounts.filter((id) => id === secondary.id)).toHaveLength(2);
  expect(vi.mocked(PngEncoding.reencodeToPng)).toHaveBeenCalledTimes(3);
  const thirdForward = new Promise<void>((resolve) =>
    window.addEventListener("popstate", () => resolve(), { once: true }),
  );
  await act(async () => {
    window.history.forward();
    await thirdForward;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(third.id));
  expect(secondarySource.getSnapshot()).toMatchObject({
    text: secondarySnapshot.text,
    skillNames: secondarySnapshot.skillNames,
    commandNames: secondarySnapshot.commandNames,
    mentions: secondarySnapshot.mentions,
  });
  expect(source.getSnapshot()).toMatchObject({
    text: originSnapshot.text,
    skillNames: originSnapshot.skillNames,
    commandNames: originSnapshot.commandNames,
    mentions: originSnapshot.mentions,
  });
  expect(destinationSource?.getSnapshot()).toMatchObject({
    text: destinationSnapshot?.text,
    skillNames: destinationSnapshot?.skillNames,
    commandNames: destinationSnapshot?.commandNames,
    mentions: destinationSnapshot?.mentions,
  });
});

test("real same-URL readers preserve the exact owning reader through attention Back and Forward", async ({
  onTestFinished,
}) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(() => {
    vi.restoreAllMocks();
    stubSessionSlots();
  });
  const encoding = installControlledImageEncoding();
  const fake = connectReadyFakeClient();
  fake.on("thread/read", ({ ref }) => {
    if (!ref) throw new Error("thread/read requires a ref");
    return readResponse(ref, { id: `thr_${ref}`, sessionId: `session-${ref}` });
  });
  fake.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    context: cascadeContext(ref, ref === "local:same" ? ["local:owner"] : []),
  }));
  const session = {
    id: "same-url-session",
    type: "session" as const,
    params: { ref: "local:same" },
    slot: "main" as const,
  };
  const owner = {
    id: "same-url-owner",
    type: "session" as const,
    params: { ref: "local:owner" },
    slot: "secondary" as const,
  };
  window.history.replaceState({}, "", "/s/local%3Asame");
  workspaceStore.setState({ panes: [session, owner], focusedPaneId: session.id });
  const mounts: string[] = [];
  const unmounts: string[] = [];
  function FocusedCommittedReader({ pane }: { pane: OpenPaneRecord }) {
    useEffect(() => {
      mounts.push(pane.id);
      return () => {
        unmounts.push(pane.id);
      };
    }, [pane.id]);
    if (pane.type === "session") return <Session params={pane.params as SessionPaneParams} paneId={pane.id} focused />;
    if (pane.type === "sessionZoom") return <Zoom paneId={pane.id} params={pane.params as SessionZoomParams} focused />;
    return null;
  }
  function FocusedCommittedHost() {
    const workspace = useSyncExternalStore(workspaceStore.subscribe, workspaceStore.getState);
    useEffect(() => {
      const restore = () => restoreCurrentAttentionEntry();
      window.addEventListener("popstate", restore);
      return () => window.removeEventListener("popstate", restore);
    }, []);
    const pane = workspace.panes.find((candidate) => candidate.id === workspace.focusedPaneId);
    if (!pane) return null;
    return <FocusedCommittedReader key={pane.id} pane={pane} />;
  }
  const view = render(
    <ClientProvider client={fake}>
      <FocusedCommittedHost />
    </ClientProvider>,
  );
  const sessionEditorAtStart = await screen.findByRole("textbox", { name: "Message" });
  expect(mounts.filter((id) => id === session.id)).toHaveLength(1);
  const sessionDraft = { text: "Session reader draft" };
  const cascadeDraft = { text: "Cascade reader draft" };
  await userEvent.type(sessionEditorAtStart, sessionDraft.text);
  await settleActivityDiscovery("local:same");
  const originLifetime = conversationPaneLifetime(session);
  const initialLength = window.history.length;
  let cascadeId = "";
  act(() => {
    cascadeId = enterAgentCascade(
      activityDelegate({ ownerRef: "local:owner", childRef: "local:same", delegateId: "same-url-editor-edge" }),
      owner.id,
    );
    workspaceStore.getState().focusPane(cascadeId);
  });
  expect(workspaceStore.getState().panes.find((pane) => pane.id === cascadeId)?.type).toBe("sessionZoom");
  await screen.findByTestId("cascade-column");
  await settleActivityDiscovery("local:same");
  await waitFor(() => expect(screen.getByRole("textbox", { name: "Message" })).toBeTruthy());
  const cascade = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
  if (!cascade) throw new Error("cascade record was not committed");
  const cascadeLifetime = conversationPaneLifetime(cascade);
  const sessionReadView = retainedTranscriptReadView(originLifetime, "local:same", "session");
  const cascadeReadView = retainedTranscriptReadView(cascadeLifetime, "local:same", "cascade");
  expect(cascadeReadView).not.toBe(sessionReadView);
  expect(cascadeReadView.id).not.toBe(sessionReadView.id);
  const sessionSource = originLifetime.composer;
  const cascadeSource = retainedConversationComposer(cascadeLifetime, "local:same");
  if (!sessionSource || !cascadeSource || sessionSource === cascadeSource)
    throw new Error("same-URL readers lack distinct composer sources");
  const cascadeEditorAtStart = screen.getByRole("textbox", { name: "Message" });
  await userEvent.clear(cascadeEditorAtStart);
  await userEvent.type(cascadeEditorAtStart, cascadeDraft.text);
  const picker = view.container.querySelector('input[type="file"]');
  if (!picker) throw new Error("cascade composer has no image picker");
  fireEvent.change(picker, {
    target: { files: [new File([new Uint8Array([11, 12, 13])], "cascade.png", { type: "image/png" })] },
  });
  expect(cascadeSource.attachments.getState().items).toMatchObject([{ name: "cascade.png", pending: true }]);
  await act(async () => encoding.resolve(0));
  await waitFor(() =>
    expect(cascadeSource.attachments.getState().items[0]).toMatchObject({ data: "AQID", pending: false }),
  );
  act(() => openAttentionSession("local:same"));
  const sessionEditorAtArrival = await screen.findByRole("textbox", { name: "Message" });
  const sessionSourceTextAtArrival = sessionSource.getSnapshot().text;
  const sessionEditorTextAtArrival = sessionEditorAtArrival.textContent ?? "";
  expect(sessionSourceTextAtArrival).not.toBe("");
  expect(sessionEditorTextAtArrival).not.toBe("");
  const presented = workspaceStore.getState().panes.find((pane) => pane.id === workspaceStore.getState().focusedPaneId);
  if (!presented || presented.id !== session.id)
    throw new Error("same-URL attention opener did not present the session reader");
  const presentedLifetime = conversationPaneLifetime(presented);
  expect(window.location.pathname).toBe("/s/local%3Asame");
  expect(window.history.length).toBe(initialLength + 1);
  expect(presentedLifetime).toBe(originLifetime);
  expect(unmounts).toContain(cascade.id);
  expect(mounts).toContain(session.id);
  expect(mounts.filter((id) => id === session.id)).toHaveLength(2);
  expect(document.activeElement).not.toBe(screen.getByRole("textbox", { name: "Message" }));
  const back = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.back();
    await back;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(cascade.id));
  const cascadeEditor = await screen.findByRole("textbox", { name: "Message" });
  expect(window.location.pathname).toBe("/s/local%3Asame");
  expect(conversationPaneLifetime(cascade)).toBe(cascadeLifetime);
  expect(cascadeLifetime.composers.get("local:same")).toBe(cascadeSource);
  const stagedCascadeText = `${cascadeDraft.text}[image 1]`;
  expect(cascadeSource.getSnapshot().text).toBe(stagedCascadeText);
  expect(cascadeEditor.textContent).toBe(stagedCascadeText);
  expect(cascadeSource.attachments.getState().items).toMatchObject([
    { name: "cascade.png", data: "AQID", pending: false },
  ]);
  expect(document.activeElement).not.toBe(cascadeEditor);
  expect(mounts.filter((id) => id === cascade.id)).toHaveLength(2);
  const forward = new Promise<void>((resolve) => window.addEventListener("popstate", () => resolve(), { once: true }));
  await act(async () => {
    window.history.forward();
    await forward;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(session.id));
  const sessionEditor = await screen.findByRole("textbox", { name: "Message" });
  expect(conversationPaneLifetime(session)).toBe(originLifetime);
  expect(originLifetime.composer).toBe(sessionSource);
  expect(sessionSource.getSnapshot().text).toBe(sessionSourceTextAtArrival);
  expect(sessionEditor.textContent).toBe(sessionEditorTextAtArrival);
  expect(cascadeSource.getSnapshot().text).toBe(stagedCascadeText);
  expect(cascadeSource.attachments.getState().items).toMatchObject([
    { name: "cascade.png", data: "AQID", pending: false },
  ]);
  expect(mounts.filter((id) => id === session.id)).toHaveLength(3);
  expect(vi.mocked(PngEncoding.reencodeToPng)).toHaveBeenCalledTimes(1);
  expect(document.activeElement).not.toBe(sessionEditor);
  const returnBack = new Promise<void>((resolve) =>
    window.addEventListener("popstate", () => resolve(), { once: true }),
  );
  await act(async () => {
    window.history.back();
    await returnBack;
  });
  await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(cascade.id));
  const returnedCascadeEditor = await screen.findByRole("textbox", { name: "Message" });
  expect(conversationPaneLifetime(cascade)).toBe(cascadeLifetime);
  expect(cascadeLifetime.composers.get("local:same")).toBe(cascadeSource);
  expect(cascadeSource.getSnapshot().text).toBe(stagedCascadeText);
  expect(returnedCascadeEditor.textContent).toBe(stagedCascadeText);
  expect(cascadeSource.attachments.getState().items).toMatchObject([
    { name: "cascade.png", data: "AQID", pending: false },
  ]);
  expect(document.activeElement).not.toBe(returnedCascadeEditor);
  expect(mounts.filter((id) => id === cascade.id)).toHaveLength(3);
  await userEvent.click(screen.getByRole("button", { name: "Send" }));
  await waitFor(() => expect(fake.calls.find((call) => call.method === "turn/start")).toBeDefined());
  const submitted = fake.calls.find((call) => call.method === "turn/start");
  expect(submitted?.params).toMatchObject({
    ref: "local:same",
    input: [
      { type: "text", text: expect.stringContaining("Cascade reader draft") },
      { type: "image", mediaType: "image/png", data: "AQID", name: "cascade.png" },
    ],
  });
});

test.each(["primary", "secondary", "cascade leaf", "focus-only departure"] as const)(
  "real Session preserves the %s reader's away position through attention history",
  async (journey) => {
    vi.mocked(ComposerModule.Composer).mockRestore();
    onTestFinished(() => {
      vi.restoreAllMocks();
      stubSessionSlots();
    });
    const fake = connectReadyFakeClient();
    fake.on("thread/read", ({ ref }) => {
      if (!ref) throw new Error("thread/read requires a ref");
      return readResponse(ref, {
        id: `thr_${ref}`,
        sessionId: `session-${ref}`,
        evener: {
          ref,
          capabilities: { ...CAPABILITIES, skillInput: true, commandInput: true },
          queue: { revision: 0 },
        },
        turns: Array.from({ length: 5 }, (_, index) => ({
          id: `${ref}-turn-${index}`,
          status: "completed" as const,
          itemsView: "full" as const,
          items: [
            {
              id: `${ref}-entry-${index}`,
              turnId: `${ref}-turn-${index}`,
              type: "userMessage" as const,
              text: `${ref} reading row ${index}`,
              status: "completed" as const,
            },
          ],
        })),
      });
    });
    fake.on("evener/thread/activity/read", ({ ref, scope }) => ({
      ...activitySummary(ref, scope),
      context: cascadeContext(ref, ref === "local:leaf" ? ["local:owner"] : []),
    }));

    const primary = {
      id: "reading-primary",
      type: "session" as const,
      params: { ref: "local:origin" },
      slot: "main" as const,
    };
    const secondary = {
      id: "reading-secondary",
      type: "session" as const,
      params: { ref: "local:origin" },
      slot: "secondary" as const,
    };
    const peer = { id: "reading-peer", type: "session" as const, params: { ref: "local:peer" }, slot: "main" as const };
    const x = { id: "reading-X", type: "session" as const, params: { ref: "local:X" }, slot: "secondary" as const };
    const owner = {
      id: "reading-owner",
      type: "session" as const,
      params: { ref: "local:owner" },
      slot: "main" as const,
    };
    window.history.replaceState({}, "", journey === "cascade leaf" ? "/s/local%3Aowner" : "/s/local%3Aorigin");
    if (journey === "secondary") workspaceStore.setState({ panes: [peer, secondary], focusedPaneId: secondary.id });
    else if (journey === "focus-only departure")
      workspaceStore.setState({ panes: [primary, x], focusedPaneId: primary.id });
    else if (journey === "cascade leaf") workspaceStore.setState({ panes: [owner], focusedPaneId: owner.id });
    else workspaceStore.setState({ panes: [primary], focusedPaneId: primary.id });
    const originRef = journey === "cascade leaf" ? "local:leaf" : "local:origin";
    const draftText = `Read at ${journey} /review /inspect`;
    const structuredDraft = {
      text: draftText,
      skillNames: ["review"],
      commandNames: ["inspect"],
      mentions: [
        { kind: "skill" as const, name: "review", offset: `Read at ${journey} `.length },
        { kind: "command" as const, name: "inspect", offset: `Read at ${journey} /review `.length },
      ],
    };
    writeComposerDraft(originRef, structuredDraft);

    const geometry = { width: 760, viewportHeight: 400, rowHeights: Array.from({ length: 5 }, () => 600) };
    const external = installTranscriptGeometry((element) => {
      const id = element.closest<HTMLElement>("[data-reading-reader]")?.dataset.readingReader;
      return id ? (geometryByReader.get(id) ?? geometry) : geometry;
    });
    const geometryByReader = new Map<string, typeof geometry>();
    let cascadeId = "";

    const mounts: string[] = [];
    const unmounts: string[] = [];
    function FocusedReader({ pane }: { pane: OpenPaneRecord }) {
      useEffect(() => {
        mounts.push(pane.id);
        return () => {
          unmounts.push(pane.id);
        };
      }, [pane.id]);
      if (pane.type === "session")
        return <Session params={pane.params as SessionPaneParams} paneId={pane.id} focused />;
      if (pane.type === "sessionZoom")
        return <Zoom paneId={pane.id} params={pane.params as SessionZoomParams} focused />;
      return null;
    }
    function FocusedReaderHost() {
      const workspace = useSyncExternalStore(workspaceStore.subscribe, workspaceStore.getState);
      useEffect(() => {
        const restore = () => restoreCurrentAttentionEntry();
        window.addEventListener("popstate", restore);
        return () => window.removeEventListener("popstate", restore);
      }, []);
      const pane = workspace.panes.find((candidate) => candidate.id === workspace.focusedPaneId);
      if (!pane) return null;
      return (
        <div key={pane.id} data-reading-reader={pane.id}>
          <FocusedReader pane={pane} />
        </div>
      );
    }

    const view = render(
      <ClientProvider client={fake}>
        <FocusedReaderHost />
      </ClientProvider>,
    );
    try {
      if (journey === "cascade leaf") {
        await screen.findByRole("textbox", { name: "Message" });
        await settleActivityDiscovery("local:owner");
        act(() => {
          cascadeId = enterAgentCascade(
            activityDelegate({ ownerRef: "local:owner", childRef: "local:leaf", delegateId: "reading-leaf-edge" }),
            owner.id,
          );
          workspaceStore.getState().focusPane(cascadeId);
        });
        await screen.findByTestId("cascade-column");
        await settleActivityDiscovery("local:leaf");
        const leafPane = workspaceStore.getState().panes.find((pane) => pane.id === cascadeId);
        if (!leafPane) throw new Error("cascade leaf pane was not committed");
        const leafView = retainedTranscriptReadView(conversationPaneLifetime(leafPane), "local:leaf", "cascade");
        await waitFor(() => expect(leafView.readable).toBe(true));
        await screen.findByRole("textbox", { name: "Message" });
      }
      const originPaneId = journey === "secondary" ? secondary.id : journey === "cascade leaf" ? cascadeId : primary.id;
      const originPane = workspaceStore.getState().panes.find((pane) => pane.id === originPaneId);
      if (!originPane) throw new Error(`missing ${journey} origin pane`);
      const originLifetime = conversationPaneLifetime(originPane);
      const originReadView = retainedTranscriptReadView(
        originLifetime,
        originRef,
        originPane.type === "sessionZoom" ? "cascade" : "session",
      );
      const initialComposer = originLifetime.composer;
      await screen.findByRole("textbox", { name: "Message" });
      await settleActivityDiscovery(originRef);
      await waitFor(() => expect(originReadView.readable).toBe(true));
      if (!initialComposer) throw new Error("real Session has no retained Composer source");
      act(() => {
        initialComposer.updateText(structuredDraft.text);
        initialComposer.updateSkillNames(structuredDraft.skillNames);
        initialComposer.updateSelectionMeta(structuredDraft);
        initialComposer.persistDraft(structuredDraft.text);
      });
      expect(initialComposer.getSnapshot()).toMatchObject(structuredDraft);
      const portFor = async (paneId: string) => {
        let portFound!: HTMLElement;
        await waitFor(() => {
          const port = view.container.querySelector<HTMLElement>(
            `[data-reading-reader="${paneId}"] [data-testid="transcript-virtual-list"] > div`,
          );
          expect(port).not.toBeNull();
          if (port) portFound = port;
        });
        return portFound;
      };
      const mountedCapture = async (viewId: string) => {
        let captured!: NonNullable<ReturnType<typeof captureTranscriptView>>;
        await waitFor(() => {
          const current = captureTranscriptView(viewId);
          expect(current).toBeDefined();
          if (current) captured = current;
        });
        return captured;
      };
      const expectUnregistered = async (viewId: string) => {
        await waitFor(() => expect(captureTranscriptView(viewId)).toBeUndefined());
      };
      const scrollReaderToAnchor = async (
        paneId: string,
        ref: string,
        rowIndex: number,
        scrollTop: number,
        expectedOffset: number,
      ) => {
        geometryByReader.set(paneId, {
          width: 760,
          viewportHeight: 400,
          rowHeights: Array.from({ length: 5 }, () => 600),
        });
        await act(async () => external.notify());
        const readerPort = await portFor(paneId);
        await waitFor(() => expect(readerPort.clientHeight).toBe(400));
        await act(async () => {
          fireEvent.wheel(readerPort, { deltaY: 700 });
          readerPort.scrollTop = scrollTop;
          fireEvent.scroll(readerPort);
        });
        const id = `${ref}-entry-${rowIndex}`;
        const visibleAnchor = readerPort.querySelector<HTMLElement>(`[data-view-anchor-id="${id}"]`);
        if (!visibleAnchor) throw new Error(`reader ${paneId} has no visible anchor ${id}`);
        expect(visibleAnchor.getBoundingClientRect().top - readerPort.getBoundingClientRect().top).toBe(expectedOffset);
        return { port: readerPort, anchorId: id, anchorOffset: expectedOffset, scrollTop };
      };
      const originGeometry = { width: 760, viewportHeight: 400, rowHeights: Array.from({ length: 5 }, () => 600) };
      geometryByReader.set(originPaneId, originGeometry);
      await act(async () => external.notify());
      const port = await portFor(originPaneId);
      await waitFor(() => expect(port.clientHeight).toBe(400));
      await act(async () => {
        fireEvent.wheel(port, { deltaY: 700 });
        port.scrollTop = 750;
        fireEvent.scroll(port);
      });
      const anchorId = `${originRef}-entry-1`;
      const anchor = port.querySelector<HTMLElement>(`[data-view-anchor-id="${anchorId}"]`);
      if (!anchor) throw new Error(`reading anchor ${anchorId} is not rendered`);
      const initialAnchorOffset = anchor.getBoundingClientRect().top - port.getBoundingClientRect().top;
      expect(initialAnchorOffset).toBe(-150);
      const initialReadViewId = originReadView.id;
      const initialRegistryCapture = await mountedCapture(initialReadViewId);
      expect(initialRegistryCapture).toMatchObject({ anchorId, anchorOffset: -150 });
      expect(initialComposer?.getSnapshot()).toMatchObject(structuredDraft);

      const openAttention = (ref: string) => act(() => openAttentionSession(ref));
      const awaitPopstate = async (direction: "back" | "forward") => {
        const popped = new Promise<void>((resolve) =>
          window.addEventListener("popstate", () => resolve(), { once: true }),
        );
        await act(async () => {
          window.history[direction]();
          await popped;
        });
      };
      if (journey === "focus-only departure") {
        openAttention("local:B");
        await screen.findByRole("textbox", { name: "Message" });
        expect(unmounts).toContain(originPaneId);
        await expectUnregistered(initialReadViewId);
        await waitFor(() => expect(originReadView.getCapture()).toMatchObject({ anchorId, anchorOffset: -150 }));
        const bPaneId = workspaceStore.getState().focusedPaneId;
        if (!bPaneId) throw new Error("attention B has no focused pane ID");
        const bPane = workspaceStore.getState().panes.find((pane) => pane.id === bPaneId);
        if (!bPane) throw new Error("attention B pane is missing");
        const bLifetime = conversationPaneLifetime(bPane);
        const bReadView = retainedTranscriptReadView(bLifetime, "local:B", "session");
        const bAnchor = await scrollReaderToAnchor(bPaneId, "local:B", 2, 1300, -100);
        const bRegistryCapture = await mountedCapture(bReadView.id);
        expect(bRegistryCapture).toMatchObject({ anchorId: bAnchor.anchorId, anchorOffset: -100 });
        act(() => workspaceStore.getState().focusPane(x.id));
        await waitFor(() => expect(unmounts).toContain(bPaneId));
        await expectUnregistered(bReadView.id);
        expect(window.location.pathname).toBe("/s/local%3AB");
        const xPane = workspaceStore.getState().panes.find((pane) => pane.id === x.id);
        if (!xPane) throw new Error("focus-only departure removed existing secondary X");
        writeComposerDraft("local:X", structuredDraft);
        const xLifetime = conversationPaneLifetime(xPane);
        const xReadView = retainedTranscriptReadView(xLifetime, "local:X", "session");
        await settleActivityDiscovery("local:X");
        await waitFor(() => expect(xReadView.readable).toBe(true));
        const focusedXEditor = await screen.findByRole("textbox", { name: "Message" });
        const xComposer = xLifetime.composer;
        if (!xComposer) throw new Error("focused secondary X has no real Composer source");
        act(() => {
          xComposer.updateText(structuredDraft.text);
          xComposer.updateSkillNames(structuredDraft.skillNames);
          xComposer.updateSelectionMeta(structuredDraft);
          xComposer.persistDraft(structuredDraft.text);
        });
        expect(xComposer.getSnapshot()).toMatchObject(structuredDraft);
        geometryByReader.set(x.id, originGeometry);
        await act(async () => external.notify());
        const xPort = await portFor(x.id);
        await act(async () => {
          fireEvent.wheel(xPort, { deltaY: 700 });
          xPort.scrollTop = 750;
          fireEvent.scroll(xPort);
        });
        const xAnchor = xPort.querySelector<HTMLElement>('[data-view-anchor-id="local:X-entry-1"]');
        if (!xAnchor) throw new Error("focused secondary X has no real transcript anchor");
        expect(xAnchor.getBoundingClientRect().top - xPort.getBoundingClientRect().top).toBe(-150);
        const xViewId = xReadView.id;
        const xInitialCapture = await mountedCapture(xViewId);
        const xSnapshot = xLifetime.composer?.getSnapshot();
        openAttention("local:C");
        await screen.findByRole("textbox", { name: "Message" });
        const attentionCPaneId = workspaceStore.getState().focusedPaneId;
        if (!attentionCPaneId) throw new Error("attention C has no focused pane ID");
        expect(unmounts).toContain(x.id);
        await expectUnregistered(xViewId);
        const xCapture = xReadView.getCapture();
        if (!xCapture) throw new Error("focus-only reader X did not capture its actual scroll position on departure");
        await waitFor(() => expect(xReadView.getCapture()).toMatchObject(xCapture));
        const cPane = workspaceStore.getState().panes.find((pane) => pane.id === attentionCPaneId);
        if (!cPane) throw new Error("attention C pane is missing");
        const cLifetime = conversationPaneLifetime(cPane);
        const cReadView = retainedTranscriptReadView(cLifetime, "local:C", "session");
        const cEditor = screen.getByRole("textbox", { name: "Message" });
        const cDisplayedDraft = cEditor.textContent;
        const cAnchor = await scrollReaderToAnchor(attentionCPaneId, "local:C", 3, 2000, -200);
        const cRegistryCapture = await mountedCapture(cReadView.id);
        expect(cRegistryCapture).toMatchObject({ anchorId: cAnchor.anchorId, anchorOffset: -200 });
        await awaitPopstate("back");
        await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(x.id));
        await waitFor(() => expect(unmounts).toContain(attentionCPaneId));
        const xRemountedCapture = await mountedCapture(xViewId);
        expect(xRemountedCapture).toMatchObject(xInitialCapture);
        const returnedXEditor = await screen.findByRole("textbox", { name: "Message" });
        expect(document.activeElement).not.toBe(returnedXEditor);
        const returnedXPort = await portFor(x.id);
        const returnedXAnchor = returnedXPort.querySelector<HTMLElement>('[data-view-anchor-id="local:X-entry-1"]');
        if (!returnedXAnchor) throw new Error("back did not render secondary X's captured anchor");
        await waitFor(() =>
          expect(returnedXAnchor.getBoundingClientRect().top - returnedXPort.getBoundingClientRect().top).toBe(-150),
        );
        expect(xReadView.id).toBe(xViewId);
        expect(xReadView.getCapture()).toMatchObject(xCapture);
        expect(xLifetime.composer?.getSnapshot()).toMatchObject({
          text: xSnapshot?.text,
          skillNames: xSnapshot?.skillNames,
          commandNames: xSnapshot?.commandNames,
          mentions: xSnapshot?.mentions,
        });
        expect(returnedXEditor.textContent).toBe(xSnapshot?.text);
        expect(conversationPaneLifetime(xPane)).toBe(xLifetime);
        expect(xSnapshot && xLifetime.composer?.getSnapshot()).toMatchObject({
          text: xSnapshot?.text,
          skillNames: xSnapshot?.skillNames,
          commandNames: xSnapshot?.commandNames,
          mentions: xSnapshot?.mentions,
        });
        expect(document.activeElement).not.toBe(returnedXEditor);
        expect(unmounts).toContain(attentionCPaneId);
        await awaitPopstate("forward");
        await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(attentionCPaneId));
        await waitFor(() => expect(mounts.filter((id) => id === attentionCPaneId).length).toBeGreaterThanOrEqual(2));
        const returnedCEditor = await screen.findByRole("textbox", { name: "Message" });
        expect(document.activeElement).not.toBe(returnedCEditor);
        const returnedCPort = await portFor(attentionCPaneId);
        const returnedCAnchor = returnedCPort.querySelector<HTMLElement>(`[data-view-anchor-id="${cAnchor.anchorId}"]`);
        if (!returnedCAnchor) throw new Error("Forward did not remount C's captured transcript anchor");
        await waitFor(() =>
          expect(returnedCAnchor.getBoundingClientRect().top - returnedCPort.getBoundingClientRect().top).toBe(-200),
        );
        expect(returnedCPort.scrollTop).toBe(2000);
        expect(await mountedCapture(cReadView.id)).toMatchObject(cRegistryCapture);
        expect(conversationPaneLifetime(cPane)).toBe(cLifetime);
        expect(returnedCEditor.textContent).toBe(cDisplayedDraft);
        expect(cLifetime.composer?.getSnapshot()).toMatchObject({ text: cDisplayedDraft });
        expect(conversationPaneLifetime(bPane)).toBe(bLifetime);
        await expectUnregistered(xViewId);
        expect(focusedXEditor).not.toBe(document.activeElement);
        return;
      }

      openAttention("local:destination");
      await screen.findByRole("textbox", { name: "Message" });
      expect(unmounts).toContain(originPaneId);
      await expectUnregistered(initialReadViewId);
      await waitFor(() => expect(originReadView.getCapture()).toMatchObject({ anchorId, anchorOffset: -150 }));
      const initialCapture = originReadView.getCapture();
      if (!initialCapture) throw new Error("hiding the real reader did not capture its read view");
      const destinationPaneId = workspaceStore.getState().focusedPaneId;
      if (!destinationPaneId) throw new Error("attention destination has no focused pane ID");
      const destinationPane = workspaceStore.getState().panes.find((pane) => pane.id === destinationPaneId);
      if (!destinationPane) throw new Error("attention destination pane is missing");
      const destinationLifetime = conversationPaneLifetime(destinationPane);
      const destinationReadView = retainedTranscriptReadView(destinationLifetime, "local:destination", "session");
      const destinationEditor = screen.getByRole("textbox", { name: "Message" });
      const destinationDisplayedDraft = destinationEditor.textContent;
      const destinationAnchor = await scrollReaderToAnchor(destinationPaneId, "local:destination", 3, 1900, -100);
      const destinationRegistryCapture = await mountedCapture(destinationReadView.id);
      expect(destinationRegistryCapture).toMatchObject({ anchorId: destinationAnchor.anchorId, anchorOffset: -100 });
      await awaitPopstate("back");
      await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(originPaneId));
      await waitFor(() => expect(unmounts).toContain(destinationPaneId));
      const remountedOriginCapture = await mountedCapture(initialReadViewId);
      expect(remountedOriginCapture).toMatchObject(initialRegistryCapture);
      const returnedEditor = await screen.findByRole("textbox", { name: "Message" });
      expect(document.activeElement).not.toBe(returnedEditor);
      const returnedPort = await portFor(originPaneId);
      const returnedAnchor = returnedPort.querySelector<HTMLElement>(`[data-view-anchor-id="${anchorId}"]`);
      if (!returnedAnchor) throw new Error("Back did not render the captured transcript anchor");
      await waitFor(() =>
        expect(returnedAnchor.getBoundingClientRect().top - returnedPort.getBoundingClientRect().top).toBe(-150),
      );
      expect(returnedPort.scrollTop).toBe(750);
      expect(originReadView.id).toBe(initialReadViewId);
      expect(originReadView.getCapture()).toMatchObject(initialCapture);
      expect(conversationPaneLifetime(originPane)).toBe(originLifetime);
      expect(originLifetime.composer).toBe(initialComposer);
      expect(initialComposer?.getSnapshot()).toMatchObject(structuredDraft);
      expect(document.activeElement).not.toBe(returnedEditor);
      expect(mounts.filter((id) => id === originPaneId).length).toBeGreaterThanOrEqual(2);
      if (journey === "secondary") {
        expect(workspaceStore.getState().panes).toContain(peer);
        expect(workspaceStore.getState().panes).toContain(secondary);
        expect(secondary.slot).toBe("secondary");
        expect(conversationPaneLifetime(peer).alive).toBe(true);
      }
      if (journey === "cascade leaf") {
        expect(originPane.type).toBe("sessionZoom");
        expect(originReadView.role).toBe("cascade");
        expect(originReadView.paneId).toBe(cascadeId);
        expect(originReadView.requestedRef).toBe("local:leaf");
      }
      await awaitPopstate("forward");
      await waitFor(() => expect(workspaceStore.getState().focusedPaneId).toBe(destinationPaneId));
      await waitFor(() => expect(mounts.filter((id) => id === destinationPaneId).length).toBeGreaterThanOrEqual(2));
      const returnedDestinationEditor = await screen.findByRole("textbox", { name: "Message" });
      expect(document.activeElement).not.toBe(returnedDestinationEditor);
      const returnedDestinationPort = await portFor(destinationPaneId);
      const returnedDestinationAnchor = returnedDestinationPort.querySelector<HTMLElement>(
        `[data-view-anchor-id="${destinationAnchor.anchorId}"]`,
      );
      if (!returnedDestinationAnchor) throw new Error("Forward did not remount the destination transcript anchor");
      await waitFor(() =>
        expect(
          returnedDestinationAnchor.getBoundingClientRect().top - returnedDestinationPort.getBoundingClientRect().top,
        ).toBe(-100),
      );
      expect(returnedDestinationPort.scrollTop).toBe(1900);
      expect(await mountedCapture(destinationReadView.id)).toMatchObject(destinationRegistryCapture);
      expect(conversationPaneLifetime(destinationPane)).toBe(destinationLifetime);
      expect(returnedDestinationEditor.textContent).toBe(destinationDisplayedDraft);
      expect(destinationLifetime.composer?.getSnapshot()).toMatchObject({ text: destinationDisplayedDraft });
      await expectUnregistered(initialReadViewId);
    } finally {
      view.unmount();
      external.restore();
    }
  },
);

test("desktop session panes own separate location and activity footers", async ({ onTestFinished }) => {
  vi.mocked(StatusBarModule.StatusBar).mockRestore();
  onTestFinished(stubSessionSlots);
  const user = userEvent.setup();
  const fake = connectReadyFakeClient();
  fake.on("thread/read", ({ ref }) => {
    if (ref === undefined) throw new Error("thread/read requires a ref");
    return readResponse(ref, { cwd: `/work/${ref}` });
  });
  fake.on("evener/git/head", ({ cwd }) => ({
    head: cwd.endsWith("local:one") ? "branch-one" : "branch-two",
    originUrl: "git@github.com:owner/repo.git",
  }));
  fake.on("evener/thread/activity/read", ({ ref, scope }) => ({
    ...activitySummary(ref, scope),
    jobs: {
      known: true,
      active: ref === "local:one" ? 1 : 2,
      total: ref === "local:one" ? 3 : 4,
      completed: 2,
      failed: 0,
    },
  }));
  workspaceStore.setState({
    panes: [
      { id: "pane-one", type: "session", params: { ref: "local:one" }, slot: "main" },
      { id: "pane-two", type: "session", params: { ref: "local:two" }, slot: "secondary" },
    ],
    focusedPaneId: "pane-one",
  });

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "local:one" }} paneId="pane-one" focused />
      <Session params={{ ref: "local:two" }} paneId="pane-two" focused={false} />
    </ClientProvider>,
  );

  const bars = await screen.findAllByTestId("statusbar");
  expect(bars).toHaveLength(2);
  // The ordinary entry route hydrates a couple of tasks behind the first
  // paint, so wait for both panes' models before reading their footers.
  await waitFor(() => expect(threadsStore.getState().threads.has("local:one")).toBe(true));
  await waitFor(() => expect(threadsStore.getState().threads.has("local:two")).toBe(true));
  const first = within(bars[0]!);
  const second = within(bars[1]!);
  expect(first.getByTestId("composer-repo-path").textContent).toBe("/work/local:one");
  expect(second.getByTestId("composer-repo-path").textContent).toBe("/work/local:two");
  expect(screen.getAllByTestId("composer-repo-location")).toHaveLength(2);
  expect((await first.findByTestId("composer-repo-ref")).textContent).toBe("owner/repo#branch-one");
  expect((await second.findByTestId("composer-repo-ref")).textContent).toBe("owner/repo#branch-two");
  expect(await first.findByRole("button", { name: /Jobs, 1 of 3 running/ })).toBeTruthy();
  await user.click(await second.findByRole("button", { name: /Jobs, 2 of 4 running/ }));
  expect(workspaceStore.getState().focusedPaneId).toBe("pane-two");
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, tab: "jobs" });
});

test("same-session pane menus open Overview from their own instance", async ({ onTestFinished }) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
  onTestFinished(stubSessionSlots);
  const ref = "local:duplicate-overview";
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse(ref));
  fake.on("evener/thread/activity/read", ({ scope }) => activitySummary(ref, scope));
  fake.on("evener/git/head", () => ({ head: "main" }));
  workspaceStore.setState({
    panes: [
      { id: "overview-first", type: "session", params: { ref }, slot: "main" },
      { id: "overview-second", type: "session", params: { ref, instance: "second" }, slot: "secondary" },
    ],
    focusedPaneId: "overview-first",
  });
  const { container } = render(
    <ClientProvider client={fake}>
      <Session params={{ ref }} paneId="overview-first" focused />
      <Session params={{ ref }} paneId="overview-second" focused />
    </ClientProvider>,
  );
  const menus = await screen.findAllByRole("button", { name: "Session actions" });
  expect(menus).toHaveLength(2);
  expect(
    Array.from(container.querySelectorAll("[data-session-actions-ref]"), (marker) =>
      marker.getAttribute("data-pane-id"),
    ),
  ).toEqual(["overview-first", "overview-second"]);
  const user = userEvent.setup();
  const secondMenu = menus[1];
  if (!secondMenu) throw new Error("second session menu is missing");
  await user.click(secondMenu);
  await user.click(screen.getByRole("menuitem", { name: "Overview" }));
  expect(workspaceStore.getState().focusedPaneId).toBe("overview-second");
  expect(activitySidebarStore.getState()).toMatchObject({ open: true, ref });
});

test("mobile omits repo location and the desktop activity footer", async ({ onTestFinished }) => {
  vi.mocked(ComposerModule.Composer).mockRestore();
  onTestFinished(stubSessionSlots);
  vi.stubGlobal(
    "matchMedia",
    vi.fn((media: string) => ({
      media,
      matches: media === "(max-width: 899px)",
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
  const fake = connectReadyFakeClient();
  fake.on("thread/read", () => readResponse("local:mobile", { cwd: "/work/mobile" }));
  fake.on("evener/git/head", () => ({ head: "mobile-branch", originUrl: "git@github.com:owner/repo.git" }));
  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "local:mobile" }} paneId="mobile-pane" focused />
    </ClientProvider>,
  );

  await screen.findByTestId("composer-input-card");
  expect(screen.queryByTestId("composer-repo-location")).toBeNull();
  expect(screen.queryByTestId("composer-repo-path")).toBeNull();
  expect(screen.queryByTestId("composer-repo-link")).toBeNull();
  expect(screen.queryByTestId("pane-edge-footer")).toBeNull();
  expect(screen.queryByTestId("statusbar")).toBeNull();
});

test.each(["closed", "ended", "notLoaded"] as const)(
  "the real session keeps an empty focused composer expanded when it becomes %s",
  async (status) => {
    vi.mocked(ComposerModule.Composer).mockRestore();
    vi.mocked(SessionChromeModule.SessionChrome).mockRestore();
    const user = userEvent.setup();
    const ref = "local:stop-focus";
    const fake = connectReadyFakeClient();
    fake.on("thread/read", () => readResponse(ref, { status: { type: "active" } }));
    fake.on("evener/thread/activity/read", ({ scope }) => activitySummary(ref, scope));
    render(
      <ClientProvider client={fake}>
        <Session params={{ ref }} paneId="stop-focus-pane" focused={false} />
      </ClientProvider>,
    );
    const editor = await screen.findByRole("textbox", { name: /^message$/i });
    await waitFor(() => expect(sessionActivitySnapshot(fake, ref, "session")?.summaryState.loading).toBe(false));
    await user.click(editor);
    await act(async () => {
      fake.emitNotification({
        method: "thread/status/changed",
        params: {
          threadId: `thr_${ref}`,
          ref,
          status: { type: status },
          capabilities: { ...CAPABILITIES, interrupt: false, steer: false, shutdown: false },
        },
      });
    });
    expect(screen.getByRole("textbox", { name: /^message$/i })).toBe(editor);
    expect(document.activeElement).toBe(editor);
    expect(editor.style.minHeight).toBe("3lh");
    await user.click(screen.getByRole("button", { name: "Session actions" }));
    await user.click(screen.getByRole("menuitem", { name: "Rename" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Session actions" }));
    expect(editor.textContent).toBe("");
    expect((screen.getByTestId("composer-submit") as HTMLButtonElement).disabled).toBe(true);
  },
);

test("shows a loading placeholder before the thread hydrates", async () => {
  const fake = connectReadyFakeClient();
  const box: { resolve: ((r: ThreadReadResponse) => void) | null } = { resolve: null };
  fake.on("thread/read", () => new Promise<ThreadReadResponse>((resolve) => (box.resolve = resolve)));

  render(
    <ClientProvider client={fake}>
      <Session params={{ ref: "ref_a" }} paneId="p1" focused={true} />
    </ClientProvider>,
  );

  expect(screen.getByText(/loading/i)).toBeTruthy();
  expect(screen.getByTestId("pane-edge-footer")).toBeTruthy();
  // request()'s handler invocation (which captures the resolver) is
  // deferred a microtask behind the synchronous render() above.
  // The ordinary entry route issues this read only after its bounded durable
  // cache lookup settles, which is a macrotask behind render - not the single
  // microtask the legacy route needed. Wait for the request to actually reach
  // the fake before resolving it.
  await waitFor(() => expect(box.resolve).not.toBeNull());
  box.resolve?.(readResponse("ref_a"));
  await waitFor(() => expect(screen.queryByText(/loading/i)).toBeNull());
  expect(screen.getByTestId("pane-edge-footer")).toBeTruthy();
});
