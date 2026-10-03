import "../doc";
import "../session";
import "../transcript";
import "./index";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import type { DockviewApi } from "dockview-core";
import { afterEach, beforeEach, expect, test } from "vitest";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import {
  type OpenPaneRecord,
  recordTranscriptOpenOrigin,
  registerDockviewApi,
  resetWorkspaceStoreForTests,
  transcriptOpenOrigin,
  workspaceStore,
} from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { activityDelegate } from "../../stores/sessionActivityTestUtils";
import {
  registerTranscriptView,
  resetTranscriptViewRegistryForTests,
} from "../session/transcript/flow/transcriptViewRegistry";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import { enterAgentCascade, openCascadeConversation, popAgentCascade, returnFromAgentCascade } from "./actions";
import type { SessionZoomParams } from "./intent";

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  resetWorkspaceStoreForTests();
  resetTranscriptViewRegistryForTests();
  connectionStore.setState({ state: "idle", client: null, serverInfo: undefined });
});
afterEach(() => resetWorkspaceStoreForTests());

function pane(id: string): OpenPaneRecord {
  const record = workspaceStore.getState().panes.find((item) => item.id === id);
  if (!record) throw new Error(`Missing committed pane ${id}`);
  return record;
}

test("targeted retype publishes a new record with the same source lifetime and unrelated panes", () => {
  const store = workspaceStore.getState();
  const sourceId = store.openPane("session", { ref: "root" });
  store.openPane("session", { ref: "independent" });
  store.openPane("doc", { session: "root", path: "notes.md", kind: "file" });
  const jobId = store.openPane("transcript", { ref: "job:output", parentRef: "root" });
  const source = pane(sourceId);
  const before = workspaceStore.getState();
  const lifetime = conversationPaneLifetime(source);
  const composer = lifetime.composer;
  if (!composer) throw new Error("Expected a source composer");
  composer.editText("keep the root draft");
  composer.editSkillNames(["root-skill"]);
  const observed: boolean[] = [];
  const unsubscribe = workspaceStore.subscribe((state) => {
    const replacement = state.panes.find((item) => item.id === sourceId);
    if (replacement && replacement !== source) {
      observed.push(conversationPaneLifetime(replacement) === lifetime && lifetime.alive);
    }
  });
  try {
    store.retypePane(source, "transcript", source.params);
    const replacement = pane(sourceId);
    expect(replacement.type).toBe("transcript");
    expect(replacement).not.toBe(source);
    expect(replacement.params).toBe(source.params);
    expect(replacement.slot).toBe(source.slot);
    expect(workspaceStore.getState().focusedPaneId).toBe(jobId);
    expect(workspaceStore.getState().panes.filter((item) => item.id !== sourceId)).toEqual(
      before.panes.filter((item) => item.id !== sourceId),
    );
    for (const item of before.panes.filter((item) => item !== source)) expect(pane(item.id)).toBe(item);
    expect(observed).toEqual([true]);
    expect(conversationPaneLifetime(replacement)).toBe(lifetime);
    expect(lifetime.composer).toBe(composer);
    expect(composer.getSnapshot()).toMatchObject({ text: "keep the root draft", skillNames: ["root-skill"] });
    expect(store.retypePane(replacement, "session", source.params)).toBe(true);
    expect(pane(sourceId)).toEqual(source);
    expect(conversationPaneLifetime(pane(sourceId))).toBe(lifetime);
  } finally {
    unsubscribe();
  }
});

test("retype remaps exact transcript Open origins as both keys and values before publication", () => {
  const store = workspaceStore.getState();
  const root = pane(store.openPane("session", { ref: "root" }));
  const child = pane(store.openPane("transcript", { ref: "child", parentRef: "root" }));
  const grandchild = pane(store.openPane("transcript", { ref: "grandchild", parentRef: "child" }));
  recordTranscriptOpenOrigin(child, root);
  recordTranscriptOpenOrigin(grandchild, child);
  expect(transcriptOpenOrigin(child)).toBe(root);
  expect(transcriptOpenOrigin(grandchild)).toBe(child);
  store.retypePane(child, "session", { ref: "child" });
  const promotedChild = pane(child.id);
  expect(promotedChild.type).toBe("session");
  expect(transcriptOpenOrigin(promotedChild)).toBe(root);
  expect(transcriptOpenOrigin(grandchild)).toBe(promotedChild);
  store.retypePane(root, "transcript", root.params);
  const promotedRoot = pane(root.id);
  expect(transcriptOpenOrigin(promotedChild)).toBe(promotedRoot);
  store.retypePane(promotedChild, "transcript", child.params);
  const returnedChild = pane(child.id);
  expect(transcriptOpenOrigin(returnedChild)).toBe(promotedRoot);
  expect(transcriptOpenOrigin(grandchild)).toBe(returnedChild);
});

test.each(["close", "reset"] as const)("stale retype after %s cannot borrow a reused pane ID", (removal) => {
  const original = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  const oldLifetime = conversationPaneLifetime(original);
  if (removal === "close") workspaceStore.getState().closePane(original.id);
  else resetWorkspaceStoreForTests();
  const replacement: OpenPaneRecord = { ...original, params: { ref: "replacement" } };
  workspaceStore.setState({ panes: [replacement], focusedPaneId: replacement.id });
  const newLifetime = conversationPaneLifetime(replacement);
  expect(workspaceStore.getState().retypePane(original, "transcript", original.params)).toBe(false);
  expect(pane(original.id)).toBe(replacement);
  expect(oldLifetime.alive).toBe(false);
  expect(newLifetime.alive).toBe(true);
  expect(newLifetime).not.toBe(oldLifetime);
});

const rootChild = () => activityDelegate({ ownerRef: "root", childRef: "child", delegateId: "d1" });

test("cascade promotion and return preserve the same pane position, source work and unrelated records", () => {
  const store = workspaceStore.getState();
  const source = pane(store.openPane("session", { ref: "root" }));
  store.openPane("session", { ref: "independent" });
  store.openPane("doc", { session: "root", path: "notes.md", kind: "file" });
  store.openPane("transcript", { ref: "job:output", parentRef: "root" });
  const before = workspaceStore.getState();
  const lifetime = conversationPaneLifetime(source);
  const composer = lifetime.composer;
  composer?.editText("source draft");
  const sourceView = retainedTranscriptReadView(lifetime, "root", "session");
  const capture = { anchorId: "source-entry", anchorOffset: -17, normalizedOffset: 0.3, followingBottom: false };
  const releaseView = registerTranscriptView({
    id: sourceView.id,
    capture: () => capture,
    restore: () => {},
    announce: () => {},
  });
  try {
    expect(enterAgentCascade(rootChild(), source.id)).toBe(source.id);
    const promoted = pane(source.id);
    expect(promoted.type).toBe("sessionZoom");
    expect(promoted.slot).toBe(source.slot);
    expect(promoted.params).toEqual({
      ref: "child",
      source: { type: "session", params: source.params },
      edges: [{ ownerRef: "root", childRef: "child", delegateId: "d1" }],
    });
    expect(conversationPaneLifetime(promoted)).toBe(lifetime);
    expect(sourceView.getCapture()).toEqual(capture);
    expect(workspaceStore.getState().focusedPaneId).toBe(before.focusedPaneId);
    for (const other of before.panes.filter((item) => item !== source)) expect(pane(other.id)).toBe(other);
    returnFromAgentCascade(source.id);
    expect(pane(source.id)).toEqual(source);
    expect(pane(source.id).params).toBe(source.params);
    expect(lifetime.composer).toBe(composer);
    expect(composer?.getSnapshot().text).toBe("source draft");
    expect(sourceView.alive).toBe(true);
    expect(sourceView.getCapture()).toEqual(capture);
  } finally {
    releaseView();
  }
});

test("drill and pop prune only abandoned cascade views and never claim another same-root pane", () => {
  const source = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  const independent: OpenPaneRecord = { ...source, id: "independent-root", slot: "secondary" };
  workspaceStore.setState({ panes: [source, independent], focusedPaneId: source.id });
  enterAgentCascade(rootChild(), source.id);
  const lifetime = conversationPaneLifetime(pane(source.id));
  const child = retainedTranscriptReadView(lifetime, "child", "cascade");
  enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), source.id);
  expect((pane(source.id).params as SessionZoomParams).ref).toBe("grandchild");
  const grandchild = retainedTranscriptReadView(lifetime, "grandchild", "cascade");
  popAgentCascade(source.id, "child");
  expect((pane(source.id).params as SessionZoomParams).ref).toBe("child");
  expect(child.alive).toBe(true);
  expect(grandchild.alive).toBe(false);
  enterAgentCascade(activityDelegate({ ownerRef: "root", childRef: "sibling", delegateId: "d3" }), source.id);
  expect((pane(source.id).params as SessionZoomParams).edges).toEqual([
    { ownerRef: "root", childRef: "sibling", delegateId: "d3" },
  ]);
  expect(child.alive).toBe(false);
  expect(pane(independent.id)).toBe(independent);
  expect(workspaceStore.getState().focusedPaneId).toBe(source.id);
});

test("transcript return preserves its exact Back origin while the origin is also promoted", () => {
  const store = workspaceStore.getState();
  const root = pane(store.openPane("session", { ref: "root" }));
  const child = pane(store.openPane("transcript", { ref: "child", parentRef: "root" }));
  recordTranscriptOpenOrigin(child, root);
  enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), child.id);
  enterAgentCascade(rootChild(), root.id);
  expect(pane(child.id).type).toBe("sessionZoom");
  expect(pane(root.id).type).toBe("sessionZoom");
  returnFromAgentCascade(child.id);
  expect(pane(child.id)).toEqual(child);
  expect(pane(child.id).params).toBe(child.params);
  expect(transcriptOpenOrigin(pane(child.id))).toBe(pane(root.id));
  returnFromAgentCascade(root.id);
  expect(transcriptOpenOrigin(pane(child.id))).toBe(pane(root.id));
  expect(pane(root.id)).toEqual(root);
});

test.each(["doc", "job"] as const)(
  "activity from a %s pane opens a contextual secondary cascade without replacing its source",
  (kind) => {
    const store = workspaceStore.getState();
    const source = pane(
      kind === "doc"
        ? store.openPane("doc", { session: "root", path: "notes.md", kind: "file" })
        : store.openPane("transcript", { ref: "job:output", parentRef: "root" }),
    );
    const cascadeId = enterAgentCascade(rootChild(), source.id);
    expect(pane(source.id)).toBe(source);
    expect(cascadeId).not.toBe(source.id);
    expect(pane(cascadeId)).toMatchObject({
      type: "sessionZoom",
      slot: "secondary",
      params: { ref: "child", source: { type: "transcript", params: { ref: "root" } } },
    });
    expect(workspaceStore.getState().panes).toHaveLength(2);
    returnFromAgentCascade(cascadeId);
    expect(pane(cascadeId)).toMatchObject({ type: "transcript", params: { ref: "root" }, slot: "secondary" });
  },
);

test("Open conversation restores the original full session in place before focusing it", () => {
  const store = workspaceStore.getState();
  const source = pane(store.openPane("session", { ref: "root" }));
  const other = pane(store.openPane("session", { ref: "independent" }));
  const lifetime = conversationPaneLifetime(source);
  const composer = lifetime.composer;
  enterAgentCascade(rootChild(), source.id);
  openCascadeConversation(source.id, "root");
  expect(pane(source.id)).toEqual(source);
  expect(conversationPaneLifetime(pane(source.id)).composer).toBe(composer);
  expect(pane(other.id)).toBe(other);
  expect(workspaceStore.getState().focusedPaneId).toBe(source.id);
});

test("Open conversation focuses an existing child session or opens secondary without clearing the workspace", () => {
  const store = workspaceStore.getState();
  const source = pane(store.openPane("session", { ref: "root" }));
  const childSession = pane(store.openPane("session", { ref: "child" }));
  const doc = pane(store.openPane("doc", { session: "root", path: "notes.md", kind: "file" }));
  enterAgentCascade(rootChild(), source.id);
  openCascadeConversation(source.id, "child");
  expect(workspaceStore.getState().focusedPaneId).toBe(childSession.id);
  expect(workspaceStore.getState().panes).toHaveLength(3);
  openCascadeConversation(source.id, "grandchild");
  const created = workspaceStore
    .getState()
    .panes.find((item) => item.type === "session" && (item.params as { ref: string }).ref === "grandchild");
  expect(created).toMatchObject({ slot: "secondary", params: { ref: "grandchild" } });
  expect(pane(source.id).type).toBe("sessionZoom");
  expect(pane(doc.id)).toBe(doc);
  expect(pane(childSession.id)).toBe(childSession);
});

test("a restored cascade restores a composer bound to its requested source, never its child", () => {
  const restored: OpenPaneRecord = {
    id: "restored",
    type: "sessionZoom",
    slot: "main",
    params: {
      ref: "child",
      source: { type: "session", params: { ref: "root-alias" } },
      edges: [],
    } satisfies SessionZoomParams,
  };
  workspaceStore.setState({ panes: [restored], focusedPaneId: restored.id });
  const lifetime = conversationPaneLifetime(restored);
  expect(lifetime.sourceRef).toBe("root-alias");
  returnFromAgentCascade(restored.id);
  expect(pane(restored.id)).toMatchObject({ type: "session", params: { ref: "root-alias" } });
  expect(conversationPaneLifetime(pane(restored.id))).toBe(lifetime);
  expect(lifetime.composer?.ref).toBe("root-alias");
});

test("inspection actions issue no sending or runtime-control requests", () => {
  const fake = new FakeClient("ready");
  connectionStore.setState({ state: "ready", client: fake });
  const source = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  enterAgentCascade(rootChild(), source.id);
  popAgentCascade(source.id, "root");
  returnFromAgentCascade(source.id);
  expect(fake.calls).toEqual([]);
});

test("saved invalid Zoom panes are rejected locally and malformed edges keep a directly readable leaf", () => {
  const document = {
    id: "saved-doc",
    params: { paneType: "doc", paneParams: { session: "root", path: "notes.md", kind: "file" } },
  };
  const job = {
    id: "saved-job",
    params: { paneType: "transcript", paneParams: { ref: "job:output", parentRef: "root" } },
  };
  const malformed: SessionZoomParams = {
    ref: "child",
    source: { type: "session", params: { ref: "root" } },
    edges: [
      { ownerRef: "root", childRef: "child", delegateId: "d1" },
      { ownerRef: "child", childRef: "root", delegateId: "d2" },
    ],
  };
  const saved = [
    document,
    job,
    { id: "bad-zoom", params: { paneType: "sessionZoom", paneParams: { ref: "job:output" } } },
    { id: "usable-zoom", params: { paneType: "sessionZoom", paneParams: malformed } },
  ];
  const panels = structuredClone(saved);
  const removed: string[] = [];
  registerDockviewApi({
    panels,
    activePanel: panels[1],
    fromJSON() {},
    removePanel(panel: { id: string }) {
      removed.push(panel.id);
    },
    clear() {},
  } as unknown as DockviewApi);
  expect(workspaceStore.getState().restoreLayout({})).toBe(true);
  expect(workspaceStore.getState().panes.some((item) => item.id === "bad-zoom")).toBe(false);
  expect(removed).toEqual(["bad-zoom"]);
  expect(JSON.stringify(pane(document.id).params)).toBe(JSON.stringify(document.params.paneParams));
  expect(JSON.stringify(pane(job.id).params)).toBe(JSON.stringify(job.params.paneParams));
  expect(pane("usable-zoom").params).toEqual({ ...malformed, edges: [] });
  expect(workspaceStore.getState().focusedPaneId).toBe(job.id);
});
