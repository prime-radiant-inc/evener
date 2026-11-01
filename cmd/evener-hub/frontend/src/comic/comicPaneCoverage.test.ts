import { beforeEach, expect, test } from "vitest";
import "../panes/session";
import { type OpenPaneRecord, resetWorkspaceStoreForTests, workspaceStore } from "../shell/workspace";
import {
  captureComicDocumentCoverage,
  comicDocumentCoversConversation,
  comicPaneCoversConversation,
  publishComicPaneCoverage,
} from "./comicPaneCoverage";

beforeEach(resetWorkspaceStoreForTests);

function source() {
  const id = workspaceStore.getState().openPane("session", { ref: "root" });
  return workspaceStore.getState().panes.find((pane) => pane.id === id)!;
}

test("only committed readers cover children, collapse and unmount retire coverage", () => {
  const pane = source();
  expect(comicPaneCoversConversation(pane, "child")).toBe(false);
  const old = publishComicPaneCoverage(pane, "root", ["child", "grandchild"]);
  expect(comicPaneCoversConversation(pane, "child")).toBe(true);
  expect(comicPaneCoversConversation(pane, "unrelated")).toBe(false);
  const release = publishComicPaneCoverage(pane, "root", ["child"]);
  old();
  expect(comicPaneCoversConversation(pane, "child")).toBe(true);
  expect(comicPaneCoversConversation(pane, "grandchild")).toBe(false);
  release();
  expect(comicPaneCoversConversation(pane, "child")).toBe(false);
});

test("reused pane IDs and changed root bindings cannot inherit coverage", () => {
  const pane = source();
  publishComicPaneCoverage(pane, "root", ["child"]);
  const replacement = { ...pane };
  workspaceStore.setState({ panes: [replacement] });
  expect(comicPaneCoversConversation(pane, "child")).toBe(false);
  expect(comicPaneCoversConversation(replacement, "child")).toBe(false);
  publishComicPaneCoverage(replacement, "wrong-root", ["child"]);
  expect(comicPaneCoversConversation(replacement, "child")).toBe(false);
});

test("only the exact opened document retains proof after synchronous unmount, reopening clears it", () => {
  const pane = source();
  const release = publishComicPaneCoverage(pane, "root", ["child"]);
  const retain = captureComicDocumentCoverage(pane, "child");
  release();
  const doc: OpenPaneRecord = { id: "doc", type: "doc", params: { session: "child" }, slot: "secondary" };
  retain(doc);
  expect(comicDocumentCoversConversation(doc, pane, "child")).toBe(true);
  expect(comicDocumentCoversConversation({ ...doc }, pane, "child")).toBe(false);
  expect(comicDocumentCoversConversation(doc, { ...pane }, "child")).toBe(false);
  expect(comicDocumentCoversConversation(doc, pane, "other")).toBe(false);
  expect(comicPaneCoversConversation(pane, "child")).toBe(false);
  captureComicDocumentCoverage(pane, "child")(doc);
  expect(comicDocumentCoversConversation(doc, pane, "child")).toBe(false);
  retain(doc);
  workspaceStore.setState({ panes: [{ ...pane }] });
  expect(comicDocumentCoversConversation(doc, pane, "child")).toBe(false);
});

test("cascade readers own replacement and cleanup independently, and cannot authorize absent scopes", () => {
  const pane: OpenPaneRecord = {
    id: "cascade",
    type: "sessionZoom",
    slot: "secondary",
    params: {
      ref: "column",
      source: { type: "session", params: { ref: "root" } },
      edges: [{ ownerRef: "root", childRef: "column", delegateId: "edge" }],
    },
  };
  workspaceStore.setState({ panes: [pane] });
  const first = {};
  const second = {};
  const obsolete = publishComicPaneCoverage(pane, "root", ["inline-a", "collapsed"], first);
  const secondRelease = publishComicPaneCoverage(pane, "column", ["inline-b"], second);
  const firstRelease = publishComicPaneCoverage(pane, "root", ["inline-a"], first);
  obsolete();
  expect(comicPaneCoversConversation(pane, "inline-a")).toBe(true);
  expect(comicPaneCoversConversation(pane, "collapsed")).toBe(false);
  expect(comicPaneCoversConversation(pane, "inline-b")).toBe(true);
  publishComicPaneCoverage(pane, "absent-column", ["unrelated"], {});
  expect(comicPaneCoversConversation(pane, "unrelated")).toBe(false);
  firstRelease();
  expect(comicPaneCoversConversation(pane, "inline-a")).toBe(false);
  expect(comicPaneCoversConversation(pane, "inline-b")).toBe(true);
  const retain = captureComicDocumentCoverage(pane, "inline-b");
  secondRelease();
  const doc: OpenPaneRecord = { id: "doc", type: "doc", params: { session: "inline-b" }, slot: "secondary" };
  retain(doc);
  expect(comicDocumentCoversConversation(doc, pane, "inline-b")).toBe(true);
  expect(comicPaneCoversConversation(pane, "inline-b")).toBe(false);
  expect(comicDocumentCoversConversation({ ...doc }, pane, "inline-b")).toBe(false);
  publishComicPaneCoverage(pane, "column", ["inline-b"], second);
  pane.params = { ref: "other", source: { type: "session", params: { ref: "other" } }, edges: [] };
  expect(comicPaneCoversConversation(pane, "inline-b")).toBe(false);
  expect(comicDocumentCoversConversation(doc, pane, "inline-b")).toBe(false);
  const replacement = { ...pane };
  workspaceStore.setState({ panes: [replacement] });
  expect(comicPaneCoversConversation(pane, "inline-b")).toBe(false);
  expect(comicPaneCoversConversation(replacement, "inline-b")).toBe(false);
});
