import "../doc";
import "../session";
import "../transcript";
import "./index";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { waitFor } from "@testing-library/react";
import type { DockviewApi } from "dockview-core";
import { afterEach, beforeEach, expect, test } from "vitest";
import { captureAttentionPlacement, presentAttentionTarget } from "../../shell/attentionPlacement";
import { conversationPaneLifetime } from "../../shell/paneLifetime";
import {
  consumePaneFocus,
  type OpenPaneRecord,
  recordTranscriptOpenOrigin,
  registerDockviewApi,
  resetWorkspaceStoreForTests,
  transcriptOpenOrigin,
  workspaceStore,
} from "../../shell/workspace";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "../../stores/connection";
import { acquireSessionActivity } from "../../stores/sessionActivity";
import { activityDelegate } from "../../stores/sessionActivityTestUtils";
import { readRecoveryPick, resolveRecoveryPick, writeRecoveryPick } from "../session/composer/draft";
import { resetTranscriptViewRegistryForTests } from "../session/transcript/flow/transcriptViewRegistry";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import { enterAgentCascade, popAgentCascade, resumeCascadeScope, returnFromAgentCascade } from "./actions";
import { cascadeClient, cascadeContext } from "./cascadeTestUtils";
import { associatedCascade, cascadeOrigin } from "./inspectionOrigin";
import { deriveCascadePath, parseZoomParams, type SessionZoomParams } from "./intent";

beforeEach(() => {
  installLocalStorage(new MemoryStorage());
  resetWorkspaceStoreForTests();
  resetTranscriptViewRegistryForTests();
  resetConnectionStoreForTests();
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

test("Agents entry keeps the center and Return removes only its secondary inspector", () => {
  const source = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  const lifetime = conversationPaneLifetime(source);
  const composer = lifetime.composer;
  if (!composer) throw new Error("Expected source composer");
  composer.editText("keep the source draft");
  composer.editSkillNames(["source-skill"]);
  const view = retainedTranscriptReadView(lifetime, "root", "session");
  const independent = pane(workspaceStore.getState().openPane("session", { ref: "independent" }));
  const inspectorId = enterAgentCascade(rootChild(), source.id);
  expect(inspectorId).not.toBe(source.id);
  expect(pane(source.id)).toBe(source);
  expect(pane(inspectorId)).toMatchObject({ type: "sessionZoom", slot: "secondary" });
  const inspectorLifetime = conversationPaneLifetime(pane(inspectorId));
  expect(inspectorLifetime).not.toBe(lifetime);
  // The cascade column embeds the live conversation, so the inspector lifetime
  // reads its origin source as a session: it carries its own composer state
  // for that ref and never adopts the center pane's.
  expect(inspectorLifetime.composer?.ref).toBe("root");
  expect(inspectorLifetime.composer).not.toBe(composer);
  expect(conversationPaneLifetime(source).composer).toBe(composer);
  expect(view.alive).toBe(true);
  expect(retainedTranscriptReadView(lifetime, "root", "session")).toBe(view);
  expect(pane(independent.id)).toBe(independent);
  returnFromAgentCascade(inspectorId);
  expect(workspaceStore.getState().panes).toEqual([source, independent]);
  expect(workspaceStore.getState().focusedPaneId).toBe(source.id);
  expect(consumePaneFocus(source.id)).toBe(true);
  expect(composer.getSnapshot()).toMatchObject({ text: "keep the source draft", skillNames: ["source-skill"] });
  expect(view.alive).toBe(true);
});

test("root re-entry reuses its associated inspector after a deeper drill", () => {
  const source = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  const sourceView = retainedTranscriptReadView(conversationPaneLifetime(source), "root", "session");
  sourceView.setReadable(true);
  const inspectorId = enterAgentCascade(rootChild(), source.id);
  const lifetime = conversationPaneLifetime(pane(inspectorId));
  const childView = retainedTranscriptReadView(lifetime, "child", "cascade");
  enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), inspectorId);
  const grandchildView = retainedTranscriptReadView(lifetime, "grandchild", "cascade");
  workspaceStore.getState().focusPane(source.id);
  expect(enterAgentCascade(rootChild())).toBe(inspectorId);
  expect(pane(inspectorId).params).toMatchObject({
    ref: "child",
    edges: [{ ownerRef: "root", childRef: "child", delegateId: "d1" }],
  });
  expect(pane(source.id)).toBe(source);
  expect(workspaceStore.getState().panes).toHaveLength(2);
  expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
  expect(conversationPaneLifetime(pane(inspectorId))).toBe(lifetime);
  expect(childView.alive).toBe(true);
  expect(grandchildView.alive).toBe(false);
  expect(sourceView.alive).toBe(true);
  expect(sourceView.readable).toBe(true);
});

test("independent same-ref sources receive different secondary inspection contexts", () => {
  const source = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  const independent: OpenPaneRecord = { ...source, id: "independent-root", slot: "secondary" };
  workspaceStore.setState({ panes: [source, independent], focusedPaneId: source.id });
  const inspectorId = enterAgentCascade(rootChild(), source.id);
  const otherInspectorId = enterAgentCascade(rootChild(), independent.id);
  expect(inspectorId).not.toBe(source.id);
  expect(otherInspectorId).not.toBe(independent.id);
  expect(otherInspectorId).not.toBe(inspectorId);
  expect(pane(source.id)).toBe(source);
  expect(pane(independent.id)).toBe(independent);
  expect(cascadeOrigin(pane(inspectorId))).toBe(source);
  expect(cascadeOrigin(pane(otherInspectorId))).toBe(independent);
  returnFromAgentCascade(inspectorId);
  expect(pane(otherInspectorId).type).toBe("sessionZoom");
  expect(workspaceStore.getState().focusedPaneId).toBe(source.id);
});

test("entering from a legacy secondary transcript opens the generic cascade and Return leaves its record untouched", () => {
  const root = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  const source = pane(workspaceStore.getState().openPane("transcript", { ref: "child", parentRef: "root" }));
  recordTranscriptOpenOrigin(source, root);
  const lifetime = conversationPaneLifetime(source);
  const view = retainedTranscriptReadView(lifetime, "child", "transcript");
  const inspectorId = enterAgentCascade(
    activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }),
    source.id,
  );
  expect(inspectorId).not.toBe(source.id);
  expect(pane(source.id)).toBe(source);
  // A legacy transcript record is not an entry source: the cascade opens
  // generic and unbound, sourcing the owner conversation as a session.
  expect(pane(inspectorId).params).toMatchObject({
    ref: "grandchild",
    source: { type: "session", params: { ref: "child" } },
    inspection: { origin: null },
  });
  expect(cascadeOrigin(pane(inspectorId))).toBeNull();
  returnFromAgentCascade(inspectorId);
  expect(workspaceStore.getState().panes).toEqual([root, source]);
  expect(workspaceStore.getState().focusedPaneId).toBeNull();
  expect(consumePaneFocus(source.id)).toBe(false);
  expect(conversationPaneLifetime(source)).toBe(lifetime);
  expect(lifetime.composer).toBeNull();
  expect(view.alive).toBe(true);
  expect(transcriptOpenOrigin(source)).toBe(root);
});

test("Return never requests focus for an identical replacement origin", () => {
  const source = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  const inspectorId = enterAgentCascade(rootChild(), source.id);
  workspaceStore.getState().closePane(source.id);
  const replacement: OpenPaneRecord = { ...source, params: { ref: "root" } };
  workspaceStore.setState({ panes: [replacement, ...workspaceStore.getState().panes] });
  workspaceStore.getState().focusPane(inspectorId);
  const replacementLifetime = conversationPaneLifetime(replacement);
  returnFromAgentCascade(inspectorId);
  expect(workspaceStore.getState().panes).toEqual([replacement]);
  expect(workspaceStore.getState().focusedPaneId).toBeNull();
  expect(consumePaneFocus(replacement.id)).toBe(false);
  expect(conversationPaneLifetime(replacement)).toBe(replacementLifetime);
  expect(replacementLifetime.alive).toBe(true);
});

test("cascade entry ignores a stale parked source and leaves its existing inspector unchanged", () => {
  const source = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  const inspectorId = enterAgentCascade(rootChild(), source.id);
  const inspector = pane(inspectorId);
  workspaceStore.getState().focusPane(source.id);
  const entry = captureAttentionPlacement();
  if (!entry) throw new Error("missing source placement");
  presentAttentionTarget("away");

  const entered = enterAgentCascade(rootChild(), source.id);

  expect(entered).not.toBe(inspectorId);
  expect(pane(source.id)).toBe(source);
  expect(source.slot).toBe("parked");
  expect(pane(inspectorId)).toBe(inspector);
  expect((pane(entered).params as SessionZoomParams).inspection).toEqual({ origin: null });
  expect(workspaceStore.getState().panes).toContain(entry.pane);
});

test("inspection Return closes normally without requesting focus for a parked origin", () => {
  const source = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  const lifetime = conversationPaneLifetime(source);
  const inspectorId = enterAgentCascade(rootChild(), source.id);
  workspaceStore.getState().focusPane(source.id);
  const entry = captureAttentionPlacement();
  if (!entry) throw new Error("missing source placement");
  presentAttentionTarget("away");

  returnFromAgentCascade(inspectorId);

  expect(workspaceStore.getState().panes.some((record) => record.id === inspectorId)).toBe(false);
  expect(workspaceStore.getState().panes).toContain(source);
  expect(source.slot).toBe("parked");
  expect(conversationPaneLifetime(source)).toBe(lifetime);
  expect(lifetime.alive).toBe(true);
  expect(consumePaneFocus(source.id)).toBe(false);
  expect(workspaceStore.getState().panes).toContain(entry.pane);
});

test("a replacement origin gets a fresh inspector instead of claiming the retired one", () => {
  const source = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  const inspectorId = enterAgentCascade(rootChild(), source.id);
  workspaceStore.getState().closePane(source.id);
  const replacement: OpenPaneRecord = { ...source, params: { ref: "root" } };
  workspaceStore.setState({ panes: [replacement, ...workspaceStore.getState().panes] });
  expect(parseZoomParams(pane(inspectorId).params)?.inspection).toEqual({ origin: null });
  const replacementInspectorId = enterAgentCascade(rootChild(), replacement.id);
  expect(replacementInspectorId).not.toBe(inspectorId);
  expect(cascadeOrigin(pane(inspectorId))).toBeNull();
  expect(associatedCascade(replacement)?.id).toBe(replacementInspectorId);
  expect(pane(replacement.id)).toBe(replacement);
  expect(workspaceStore.getState().panes).toHaveLength(3);
});

test("older secondary fallback intent still retypes its exact saved pane on Return", () => {
  const source = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  const legacy = pane(
    workspaceStore.getState().openPane(
      "sessionZoom",
      {
        ref: "child",
        source: { type: "transcript", params: { ref: "root" } },
        edges: [{ ownerRef: "root", childRef: "child", delegateId: "d1" }],
      },
      { slot: "secondary" },
    ),
  );
  const lifetime = conversationPaneLifetime(legacy);
  returnFromAgentCascade(legacy.id);
  expect(pane(source.id)).toBe(source);
  expect(pane(legacy.id)).toMatchObject({ type: "transcript", slot: "secondary", params: { ref: "root" } });
  expect(conversationPaneLifetime(pane(legacy.id))).toBe(lifetime);
  // The saved source reads as the session it always was, so the shared
  // lifetime carries the conversation's composer for that ref.
  expect(lifetime.composer?.ref).toBe("root");
});

test("legacy cascade Return preserves the same pane position, source work and unrelated records", () => {
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
  sourceView.setCapture(capture);
  store.retypePane(source, "sessionZoom", {
    ref: "child",
    source: { type: "session", params: source.params },
    edges: [{ ownerRef: "root", childRef: "child", delegateId: "d1" }],
  });
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
});

test("drill and pop prune only abandoned cascade views and never claim another same-root pane", () => {
  const source = pane(workspaceStore.getState().openPane("session", { ref: "root" }));
  const independent: OpenPaneRecord = { ...source, id: "independent-root", slot: "secondary" };
  workspaceStore.setState({ panes: [source, independent], focusedPaneId: source.id });
  const inspectorId = enterAgentCascade(rootChild(), source.id);
  expect(inspectorId).not.toBe(source.id);
  const lifetime = conversationPaneLifetime(pane(inspectorId));
  const child = retainedTranscriptReadView(lifetime, "child", "cascade");
  expect(
    enterAgentCascade(activityDelegate({ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }), inspectorId),
  ).toBe(inspectorId);
  expect((pane(inspectorId).params as SessionZoomParams).ref).toBe("grandchild");
  const grandchild = retainedTranscriptReadView(lifetime, "grandchild", "cascade");
  popAgentCascade(inspectorId, "child");
  expect((pane(inspectorId).params as SessionZoomParams).ref).toBe("child");
  expect(child.alive).toBe(true);
  expect(grandchild.alive).toBe(false);
  expect(
    enterAgentCascade(activityDelegate({ ownerRef: "root", childRef: "sibling", delegateId: "d3" }), source.id),
  ).toBe(inspectorId);
  expect((pane(inspectorId).params as SessionZoomParams).edges).toEqual([
    { ownerRef: "root", childRef: "sibling", delegateId: "d3" },
  ]);
  expect(child.alive).toBe(false);
  expect(conversationPaneLifetime(pane(inspectorId))).toBe(lifetime);
  expect(pane(inspectorId).slot).toBe("secondary");
  expect(pane(source.id)).toBe(source);
  expect(pane(independent.id)).toBe(independent);
  expect(workspaceStore.getState().focusedPaneId).toBe(inspectorId);
});

test("legacy transcript Return preserves its exact Back origin while the origin is also promoted", () => {
  const store = workspaceStore.getState();
  const root = pane(store.openPane("session", { ref: "root" }));
  const child = pane(store.openPane("transcript", { ref: "child", parentRef: "root" }));
  recordTranscriptOpenOrigin(child, root);
  store.retypePane(child, "sessionZoom", {
    ref: "grandchild",
    source: { type: "transcript", params: child.params },
    edges: [{ ownerRef: "child", childRef: "grandchild", delegateId: "d2" }],
  });
  store.retypePane(root, "sessionZoom", {
    ref: "child",
    source: { type: "session", params: root.params },
    edges: [{ ownerRef: "root", childRef: "child", delegateId: "d1" }],
  });
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
      params: { ref: "child", source: { type: "session", params: { ref: "root" } } },
    });
    expect(workspaceStore.getState().panes).toHaveLength(2);
    returnFromAgentCascade(cascadeId);
    expect(workspaceStore.getState().panes).toEqual([source]);
    expect(consumePaneFocus(source.id)).toBe(false);
  },
);

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
  const inspectorId = enterAgentCascade(rootChild(), source.id);
  popAgentCascade(inspectorId, "root");
  returnFromAgentCascade(inspectorId);
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

// Ancestry reachable only from the hub's authoritative chain — the shape left
// behind after popping above the recorded edge segment — supplies a rendered
// scope the recorded edges never name. Resuming it must persist the resumed
// binding so the rendered path follows, and only then report success.
const ancestry: Record<string, string[]> = { root: [], child: ["root"], grandchild: ["root", "child"] };
const ancestryContext = (ref: string) => cascadeContext(ref, ancestry[ref] ?? ["root"]);

test("a resume of an ancestor recorded only by authoritative ancestry follows the resumed ref", async () => {
  const client = cascadeClient(ancestryContext);
  connectionStore.getState().connect(client);
  const lease = acquireSessionActivity(client, "grandchild", "session");
  try {
    await waitFor(() => expect(lease.store.getSnapshot().context?.ancestryKnown).toBe(true));
    const paneId = workspaceStore.getState().openPane("sessionZoom", {
      ref: "grandchild",
      source: { type: "session", params: { ref: "root" } },
      edges: [],
    });
    const lifetime = conversationPaneLifetime(pane(paneId));

    // "child" renders only because "grandchild"'s authoritative ancestry names
    // it: it is absent from params.ref, params.source and params.edges.
    expect(
      deriveCascadePath(pane(paneId).params as SessionZoomParams, ancestryContext("grandchild")).scopes.map(
        (scope) => scope.requestedRef,
      ),
    ).toEqual(["root", "child", "grandchild"]);

    const applied = resumeCascadeScope(lifetime, "child", "child-resumed");
    expect(applied).toBe(true);
    expect(
      deriveCascadePath(pane(paneId).params as SessionZoomParams, ancestryContext("grandchild")).scopes.map(
        (scope) => scope.requestedRef,
      ),
    ).toEqual(["root", "child-resumed", "grandchild"]);
  } finally {
    lease.release();
  }
});

// The pick is keyed by conversation, so a resume that rewrites a column's
// ref must carry the stored pick with it - even for a non-source column no
// autosave touches - or the replacement composer restores the oldest
// rejected row instead of the column's preserved work.
test("a non-source cascade resume keeps the column's pick restorable", async () => {
  const client = cascadeClient(ancestryContext);
  connectionStore.getState().connect(client);
  const lease = acquireSessionActivity(client, "grandchild", "session");
  try {
    await waitFor(() => expect(lease.store.getSnapshot().context?.ancestryKnown).toBe(true));
    const paneId = workspaceStore.getState().openPane("sessionZoom", {
      ref: "grandchild",
      source: { type: "session", params: { ref: "root" } },
      edges: [],
    });
    const lifetime = conversationPaneLifetime(pane(paneId));
    writeRecoveryPick("child-pick");
    const applied = resumeCascadeScope(lifetime, "child", "child-resumed");
    expect(applied).toBe(true);
    // The pick is keyed by row id, so the cascade resume leaves it
    // untouched: once the row's durable target follows the resumed ref,
    // resolution finds it there with nothing carried.
    expect(readRecoveryPick("child-pick")).toBeDefined();
    const resolved = await resolveRecoveryPick("child-resumed", async (rowId) =>
      rowId === "child-pick" ? { targetRef: "child-resumed" } : undefined,
    );
    expect(resolved?.entry.rowId).toBe("child-pick");
  } finally {
    lease.release();
  }
});
