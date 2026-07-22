import { afterEach, expect, test, vi } from "vitest";
import * as paneActions from "../../shell/paneActions";
import { openDocBeside } from "./openDoc";

afterEach(() => {
  vi.restoreAllMocks();
});

test("openDocBeside routes a doc PaneRef through openBeside", () => {
  const spy = vi.spyOn(paneActions, "openBeside");
  openDocBeside({ session: "sess_1", path: "src/x.ts", kind: "file" });
  // Locks the delegation shape: a "doc" pane carrying the exact params, not
  // some other pane type or a reshaped params bag.
  expect(spy).toHaveBeenCalledWith({ type: "doc", params: { session: "sess_1", path: "src/x.ts", kind: "file" } });
});

test("openDocBeside preserves the image kind unchanged", () => {
  const spy = vi.spyOn(paneActions, "openBeside");
  openDocBeside({ session: "sess_2", path: "out/pic.png", kind: "image" });
  expect(spy).toHaveBeenCalledWith({ type: "doc", params: { session: "sess_2", path: "out/pic.png", kind: "image" } });
});
