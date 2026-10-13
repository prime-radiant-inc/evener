import { bindFilePath } from "@evener/appwire-client/docContent";
import { beforeEach, expect, test } from "vitest";
import {
  documentPaneState,
  type OpenPaneRecord,
  resetWorkspaceStoreForTests,
  workspaceStore,
} from "../../shell/workspace";
import { type DocParams, openDocBeside } from "./openDoc";

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

// The opener is the ONE delegation boundary every producer routes through, so a
// captured opening origin must survive it unchanged: openDocBeside passes the
// caller's params object through to openBeside without reshaping or dropping the
// origin. Origin here is the real approved serialized shape, carrying a runtime
// compositionOwnerId - never a durable owner field.
test("openDocBeside propagates a captured browser-origin into the open call unchanged", () => {
  const spy = vi.spyOn(paneActions, "openBeside").mockImplementation(() => {});
  const origin = { kind: "browser-origin" as const, paneId: "owner-pane", compositionOwnerId: "owner-A" };
  openDocBeside({ session: "sess_3", path: "NOTES.md", kind: "file", origin });
  expect(spy).toHaveBeenCalledWith({
    type: "doc",
    params: { session: "sess_3", path: "NOTES.md", kind: "file", origin },
  });
});

test("openDocBeside propagates an explicit browser-readonly-session origin unchanged", () => {
  const spy = vi.spyOn(paneActions, "openBeside").mockImplementation(() => {});
  const origin = { kind: "browser-readonly-session" as const, sourcePaneId: "source-pane" };
  openDocBeside({ session: "sess_4", path: "NOTES.md", kind: "file", origin });
  expect(spy).toHaveBeenCalledWith({
    type: "doc",
    params: { session: "sess_4", path: "NOTES.md", kind: "file", origin },
  });
});

test("openDocBeside focuses the open document instead of duplicating it when the origin churns", () => {
  vi.restoreAllMocks();
  const sourceId = workspaceStore.getState().openPane("transcript", { ref: "sess_5" });
  const source = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
  const reference = bindFilePath("src/x.ts", "/work");
  if (!source || !reference) throw new Error("fixture did not create a source and bound file");

  openDocBeside({
    session: "sess_5",
    reference,
    sourcePaneId: sourceId,
    origin: { kind: "browser-origin", paneId: "pane_session_1", compositionOwnerId: "composer-before-reload" },
  });
  const existing = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  if (!existing) throw new Error("document did not open");

  // A composer identity churns across a reload, so the same document's next
  // open-beside carries a fresh opening origin. The document is still the
  // same document: the reused pane is focused, never duplicated beside
  // itself.
  openDocBeside({
    session: "sess_5",
    reference,
    sourcePaneId: sourceId,
    origin: { kind: "browser-origin", paneId: "pane_session_1", compositionOwnerId: "composer-after-reload" },
  });

  const documents = workspaceStore.getState().panes.filter((pane) => pane.type === "doc");
  expect(documents).toHaveLength(1);
  const document = documents[0];
  if (!document) throw new Error("the reused document pane is missing");
  expect(document.id).toBe(existing.id);
  expect(workspaceStore.getState().focusedPaneId).toBe(existing.id);
  expect(documentPaneState(document)).toEqual({ reference, origin: source, reopen: 1 });
});

test("openDocBeside refreshes the reused document's serialized origin", () => {
  vi.restoreAllMocks();
  const sourceId = workspaceStore.getState().openPane("transcript", { ref: "sess_6" });
  const source = workspaceStore.getState().panes.find((pane) => pane.id === sourceId);
  const reference = bindFilePath("src/x.ts", "/work");
  if (!source || !reference) throw new Error("fixture did not create a source and bound file");

  openDocBeside({
    session: "sess_6",
    reference,
    sourcePaneId: sourceId,
    origin: { kind: "browser-origin", paneId: "pane_session_1", compositionOwnerId: "composer-before-reload" },
  });

  // The next open-beside captures the live composer, not the one that opened
  // the document first: a note's destination captures the pane's serialized
  // origin, so a stale one here would strand every later annotation on a
  // composer that no longer exists.
  openDocBeside({
    session: "sess_6",
    reference,
    sourcePaneId: sourceId,
    origin: { kind: "browser-origin", paneId: "pane_session_1", compositionOwnerId: "composer-after-reload" },
  });

  const document = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  if (!document) throw new Error("the reused document pane is missing");
  expect((document.params as DocParams).origin).toEqual({
    kind: "browser-origin",
    paneId: "pane_session_1",
    compositionOwnerId: "composer-after-reload",
  });
  expect(documentPaneState(document)).toEqual({ reference, origin: source, reopen: 1 });
});

// A cascade origin keeps its secondary slot on a NEW document open because
// promoting the inspection into main would break "main is the routed session"
// and let the next primary navigation replace the whole cascade. Reusing an
// already-open document must keep the same rule: opening the same file twice
// from a cascade must not promote the zoom pane either.
test("openDocBeside never promotes a cascade origin when reusing the open document", () => {
  vi.restoreAllMocks();
  const zoom: OpenPaneRecord = {
    id: "pane_zoom",
    type: "sessionZoom",
    slot: "secondary",
    params: {
      ref: "child",
      source: { type: "session", params: { ref: "root" } },
      edges: [{ ownerRef: "root", childRef: "child", delegateId: "edge" }],
    },
  };
  const reference = bindFilePath("src/x.ts", "/work");
  if (!reference) throw new Error("fixture did not bind its file");
  const document: OpenPaneRecord = {
    id: "pane_doc",
    type: "doc",
    slot: "main",
    params: { session: "root", path: "src/x.ts", kind: "file" },
  };
  workspaceStore.setState({ panes: [zoom, document], focusedPaneId: document.id });

  // The document is already open, so the open reuses its pane.
  openDocBeside({ session: "root", reference, sourcePaneId: zoom.id });

  const zoomAfter = workspaceStore.getState().panes.find((pane) => pane.id === zoom.id);
  if (!zoomAfter) throw new Error("the zoom pane is missing");
  expect(zoomAfter.slot).toBe("secondary");
  expect(workspaceStore.getState().focusedPaneId).toBe(document.id);
  const documents = workspaceStore.getState().panes.filter((pane) => pane.type === "doc");
  expect(documents).toHaveLength(1);
});
