import { bindFilePath } from "@evener/appwire-client/docContent";
import { beforeEach, expect, test } from "vitest";
import { documentPaneState, resetWorkspaceStoreForTests, workspaceStore } from "../../shell/workspace";
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
