import { afterEach, expect, test, vi } from "vitest";
import * as paneActions from "../../shell/paneActions";
import { openDocBeside } from "./openDoc";

afterEach(() => {
  vi.restoreAllMocks();
});

// mockImplementation isolates the delegation contract from openBeside's real
// body: T6 filled openBeside to route through workspaceStore.openPane(), which
// throws for a pane type not registered in this file's module graph. This test
// asserts only that openDocBeside CALLS openBeside with the right shape, so it
// stubs the call target rather than exercising a real pane open.
test("openDocBeside routes a doc PaneRef through openBeside", () => {
  const spy = vi.spyOn(paneActions, "openBeside").mockImplementation(() => {});
  openDocBeside({ session: "sess_1", path: "src/x.ts", kind: "file" });
  // Locks the delegation shape: a "doc" pane carrying the exact params, not
  // some other pane type or a reshaped params bag.
  expect(spy).toHaveBeenCalledWith({ type: "doc", params: { session: "sess_1", path: "src/x.ts", kind: "file" } });
});

test("openDocBeside preserves the image kind unchanged", () => {
  const spy = vi.spyOn(paneActions, "openBeside").mockImplementation(() => {});
  openDocBeside({ session: "sess_2", path: "out/pic.png", kind: "image" });
  expect(spy).toHaveBeenCalledWith({ type: "doc", params: { session: "sess_2", path: "out/pic.png", kind: "image" } });
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
