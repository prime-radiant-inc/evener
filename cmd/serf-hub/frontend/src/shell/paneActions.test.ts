import { expect, test } from "vitest";
import { openBeside, popOutPane } from "./paneActions";

// T1 ships these as no-op stubs T6 fills; the seam contract producers depend on
// (PIN-A) is that calling a stub is safe - it must never throw, only do nothing
// yet. These lock that contract + the signatures until T6 lands real bodies.

test("openBeside is a callable no-op stub (never throws)", () => {
  expect(() => openBeside({ type: "doc", params: { session: "s", path: "p", kind: "file" } })).not.toThrow();
});

test("popOutPane is a callable no-op stub (never throws)", () => {
  expect(() => popOutPane("pane_doc_1")).not.toThrow();
});
