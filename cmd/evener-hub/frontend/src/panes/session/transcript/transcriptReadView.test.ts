import { afterEach, expect, test } from "vitest";
import { conversationPaneLifetime } from "../../../shell/paneLifetime";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { retainedTranscriptReadView } from "./transcriptReadView";

afterEach(resetWorkspaceStoreForTests);

test("newer reader input clears and invalidates the retained capture", () => {
  const pane: OpenPaneRecord = { id: "pane", type: "transcript", params: { ref: "root" }, slot: "main" };
  workspaceStore.setState({ panes: [pane] });
  const view = retainedTranscriptReadView(conversationPaneLifetime(pane), "root", "transcript");
  const revision = view.positioningRevision ?? 0;
  const captured = {
    anchorId: "current-entry",
    anchorOffset: -900,
    normalizedOffset: 0.3,
    followingBottom: false,
    positioningRevision: revision,
  };
  view.setCapture(captured);
  view.supersedePositioning?.();
  expect(view.getCapture()).toBeUndefined();
  expect(view.positioningRevision).toBe(revision + 1);
  view.setCapture(captured);
  expect(view.getCapture()).toBeUndefined();
});

test("retiring a reader clears its capture without retiring an independent same-ref reader", () => {
  const first: OpenPaneRecord = { id: "first", type: "transcript", params: { ref: "root" }, slot: "main" };
  const second: OpenPaneRecord = { id: "second", type: "transcript", params: { ref: "root" }, slot: "secondary" };
  workspaceStore.setState({ panes: [first, second] });
  const a = retainedTranscriptReadView(conversationPaneLifetime(first), "root", "transcript");
  const b = retainedTranscriptReadView(conversationPaneLifetime(second), "root", "transcript");
  const captured = { anchorId: "current-entry", anchorOffset: -900, normalizedOffset: 0.3, followingBottom: false };
  a.setCapture(captured);
  b.setCapture(captured);
  a.dispose();
  expect(a.getCapture()).toBeUndefined();
  expect(b.getCapture()).toBe(captured);
  expect(b.alive).toBe(true);
  a.setCapture(captured);
  expect(a.getCapture()).toBeUndefined();
});

test("a pane retains each ref and role's capture when its reader collapses", () => {
  const pane: OpenPaneRecord = { id: "pane:root", type: "transcript", params: { ref: "root" }, slot: "main" };
  workspaceStore.setState({ panes: [pane] });
  const lifetime = conversationPaneLifetime(pane);
  const view = retainedTranscriptReadView(lifetime, "shared", "cascade");
  view.setCapture({ anchorId: "turn-20", anchorOffset: 12, normalizedOffset: 0.4, followingBottom: false });
  view.setReadable(true);
  view.setReadable(false);
  const returned = retainedTranscriptReadView(lifetime, "shared", "cascade");
  expect(returned.getCapture()).toEqual({
    anchorId: "turn-20",
    anchorOffset: 12,
    normalizedOffset: 0.4,
    followingBottom: false,
  });
  expect(returned).toBe(view);
  expect(retainedTranscriptReadView(lifetime, "shared", "transcript").getCapture()).toBeUndefined();
  expect(retainedTranscriptReadView(lifetime, "other", "cascade").getCapture()).toBeUndefined();
});

test("same-ref readers in independent panes and reused pane IDs own different captures", () => {
  const first: OpenPaneRecord = { id: "pane", type: "transcript", params: { ref: "root" }, slot: "main" };
  const second: OpenPaneRecord = { id: "other", type: "transcript", params: { ref: "root" }, slot: "secondary" };
  workspaceStore.setState({ panes: [first, second] });
  const a = retainedTranscriptReadView(conversationPaneLifetime(first), "shared", "cascade");
  const b = retainedTranscriptReadView(conversationPaneLifetime(second), "shared", "cascade");
  a.setCapture({ anchorId: "turn-a", anchorOffset: 12, normalizedOffset: 0.4, followingBottom: false });
  expect(b.id).not.toBe(a.id);
  expect(b.getCapture()).toBeUndefined();
  const replacement = { ...first };
  workspaceStore.setState({ panes: [replacement, second] });
  const fresh = retainedTranscriptReadView(conversationPaneLifetime(replacement), "shared", "cascade");
  expect(a.alive).toBe(false);
  expect(b.alive).toBe(true);
  expect(fresh.id).not.toBe(a.id);
  expect(fresh.getCapture()).toBeUndefined();
});

test("popping a reader disposes that handle and a later drill creates a fresh handle", () => {
  const pane: OpenPaneRecord = { id: "pane", type: "transcript", params: { ref: "root" }, slot: "main" };
  workspaceStore.setState({ panes: [pane] });
  const lifetime = conversationPaneLifetime(pane);
  const child = retainedTranscriptReadView(lifetime, "child", "cascade");
  const root = retainedTranscriptReadView(lifetime, "root", "cascade");
  child.dispose();
  child.dispose();
  expect(child.alive).toBe(false);
  expect(root.alive).toBe(true);
  const returned = retainedTranscriptReadView(lifetime, "child", "cascade");
  expect(returned).not.toBe(child);
  expect(returned.alive).toBe(true);
});
