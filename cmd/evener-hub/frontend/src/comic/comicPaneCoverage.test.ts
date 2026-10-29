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
