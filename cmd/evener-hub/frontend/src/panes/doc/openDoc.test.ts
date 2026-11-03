import { bindFilePath } from "@evener/appwire-client/docContent";
import { beforeEach, expect, test } from "vitest";
import { captureAttentionPlacement, presentAttentionTarget } from "../../shell/attentionPlacement";
import "../session";
import {
  documentPaneState,
  type OpenPaneRecord,
  recordDocumentPaneState,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "../../shell/workspace";
import { openDocBeside } from "./openDoc";

beforeEach(() => {
  resetWorkspaceStoreForTests();
});

test("openDocBeside opens a real bound file pane owned by its source", () => {
  const sourceId = workspaceStore.getState().openPane("transcript", { ref: "sess_1" });
  const source = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
  const reference = bindFilePath("src/x.ts", "/work");
  if (!source || !reference) throw new Error("fixture did not create a source and bound file");

  openDocBeside({ session: "sess_1", reference, sourcePaneId: sourceId });

  const document = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  expect(document?.params).toEqual({ session: "sess_1", path: "src/x.ts", kind: "file" });
  if (!document) throw new Error("document did not open");
  expect(documentPaneState(document)).toEqual({ reference, origin: source, reopen: 0 });
});

test("openDocBeside opens a real bound image pane with image dedup params", () => {
  const sourceId = workspaceStore.getState().openPane("transcript", { ref: "sess_2" });
  const source = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
  const reference = bindFilePath("out/pic.png", "/work");
  if (!source || !reference) throw new Error("fixture did not create a source and bound image");

  openDocBeside({ session: "sess_2", reference, sourcePaneId: sourceId });

  const document = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  expect(document?.params).toEqual({ session: "sess_2", path: "out/pic.png", kind: "image" });
  if (!document) throw new Error("image document did not open");
  expect(documentPaneState(document)).toEqual({ reference, origin: source, reopen: 0 });
});

test.each<{
  name: string;
  type: OpenPaneRecord["type"];
  params: unknown;
  owns: boolean;
}>([
  { name: "matching session", type: "session", params: { ref: "sess_1" }, owns: true },
  { name: "different session", type: "session", params: { ref: "sess_2" }, owns: false },
  { name: "matching transcript", type: "transcript", params: { ref: "sess_1", parentRef: "sess_2" }, owns: true },
  { name: "matching job owner", type: "transcript", params: { ref: "job:run", parentRef: "sess_1" }, owns: true },
  { name: "different job owner", type: "transcript", params: { ref: "job:run", parentRef: "sess_2" }, owns: false },
  { name: "missing job owner", type: "transcript", params: { ref: "job:run" }, owns: false },
  { name: "non-string subject", type: "transcript", params: { ref: 1, parentRef: "sess_1" }, owns: false },
  { name: "missing subject", type: "transcript", params: {}, owns: false },
  { name: "job prefix on a session", type: "session", params: { ref: "job:run", parentRef: "sess_1" }, owns: false },
  { name: "unsupported source type", type: "settings", params: { ref: "sess_1" }, owns: false },
])("openDocBeside keeps the source ownership rule for $name", ({ type, params, owns }) => {
  const source: OpenPaneRecord = { id: "source", type, params, slot: "main" };
  workspaceStore.setState({ panes: [source], focusedPaneId: source.id });
  const reference = bindFilePath("src/owner.ts", "/work");
  if (!reference) throw new Error("fixture did not bind its file");

  openDocBeside({ session: "sess_1", reference, sourcePaneId: source.id });

  const document = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  if (!document) throw new Error("document did not open");
  expect(documentPaneState(document)).toEqual({ reference, origin: owns ? source : undefined, reopen: 0 });
});

test.each(["session", "transcript"] as const)("openDocBeside retains the invalid %s params error", (type) => {
  const source: OpenPaneRecord = { id: "source", type, params: null, slot: "main" };
  workspaceStore.setState({ panes: [source], focusedPaneId: source.id });
  const reference = bindFilePath("src/owner.ts", "/work");
  if (!reference) throw new Error("fixture did not bind its file");

  expect(() => openDocBeside({ session: "sess_1", reference, sourcePaneId: source.id })).toThrow(TypeError);
  expect(workspaceStore.getState().panes).toEqual([source]);
});

test("openDocBeside chooses presented source and document duplicates", () => {
  const parkedSourceId = workspaceStore.getState().openPane("session", { ref: "sess_1" });
  const sourceEntry = captureAttentionPlacement();
  if (!sourceEntry) throw new Error("missing source placement");
  presentAttentionTarget("sess_away");
  const presentedSourceId = workspaceStore.getState().openPane("session", { ref: "sess_1" });
  const parkedDocId = workspaceStore.getState().openPane("doc", { session: "sess_1", path: "src/x.ts", kind: "file" });
  const parkedDoc = workspaceStore.getState().panes.find((pane) => pane.id === parkedDocId);
  const reference = bindFilePath("src/x.ts", "/work");
  if (!parkedDoc || !reference) throw new Error("missing document fixture");
  recordDocumentPaneState(parkedDoc, { reference, origin: sourceEntry.pane, reopen: 7 });
  const docEntry = captureAttentionPlacement();
  if (!docEntry) throw new Error("missing document placement");
  presentAttentionTarget("sess_elsewhere");
  const presentedDocId = workspaceStore.getState().openPane("doc", {
    session: "sess_1",
    path: "src/x.ts",
    kind: "file",
  });

  openDocBeside({ session: "sess_1", reference, sourcePaneId: parkedSourceId });

  const state = workspaceStore.getState();
  const presentedDoc = state.panes.find((pane) => pane.id === presentedDocId);
  expect(state.focusedPaneId).toBe(presentedDocId);
  if (!presentedDoc) throw new Error("missing presented duplicate document");
  expect(documentPaneState(presentedDoc)).toEqual({
    reference,
    origin: state.panes.find((pane) => pane.id === presentedSourceId),
    reopen: 0,
  });
  expect(documentPaneState(parkedDoc)).toEqual({ reference, origin: sourceEntry.pane, reopen: 7 });
  expect(state.panes.find((pane) => pane.id === docEntry.pane.id)).toBe(docEntry.pane);
});

test("openDocBeside opens a fresh presented document when only parked matches exist", () => {
  const sourceId = workspaceStore.getState().openPane("session", { ref: "sess_1" });
  const sourceEntry = captureAttentionPlacement();
  if (!sourceEntry) throw new Error("missing source placement");
  presentAttentionTarget("sess_away");
  const parkedDocId = workspaceStore.getState().openPane("doc", { session: "sess_1", path: "src/x.ts", kind: "file" });
  const parkedDoc = workspaceStore.getState().panes.find((pane) => pane.id === parkedDocId);
  const reference = bindFilePath("src/x.ts", "/work");
  if (!parkedDoc || !reference) throw new Error("missing parked document fixture");
  recordDocumentPaneState(parkedDoc, { reference, origin: sourceEntry.pane, reopen: 4 });
  const docEntry = captureAttentionPlacement();
  if (!docEntry) throw new Error("missing document placement");
  presentAttentionTarget("sess_elsewhere");

  openDocBeside({ session: "sess_1", reference, sourcePaneId: sourceId });

  const state = workspaceStore.getState();
  const opened = state.panes.find((pane) => pane.type === "doc" && pane.slot !== "parked");
  if (!opened) throw new Error("missing fresh presented document");
  expect(opened.id).not.toBe(parkedDocId);
  expect(opened.params).toEqual({ session: "sess_1", path: "src/x.ts", kind: "file" });
  expect(documentPaneState(opened)).toEqual({ reference, origin: undefined, reopen: 0 });
  expect(documentPaneState(parkedDoc)).toEqual({ reference, origin: sourceEntry.pane, reopen: 4 });
  expect(state.panes).toContain(docEntry.pane);
});
