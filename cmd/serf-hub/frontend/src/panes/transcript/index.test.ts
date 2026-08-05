// @vitest-environment node

import { expect, test } from "vitest";
import { paneFor } from "../../shell/paneRegistry";
import "./index"; // registers the "transcript" pane type as a side effect

test("registers the read-only transcript pane under the transcript type id", () => {
  expect(paneFor("transcript").id).toBe("transcript");
});

test("the transcript pane is not a singleton - distinct refs are distinct panes", () => {
  // openBeside leans on workspace.ts's same-params dedup, not a singleton flag,
  // so this must stay falsy or two different threads would collapse into one.
  expect(paneFor("transcript").singleton).toBeFalsy();
});

test("title prefers the live thread name and falls back to the raw ref", () => {
  const descriptor = paneFor("transcript");
  expect(descriptor.title({ ref: "ref_x" }, { threadName: () => "Named thread" })).toBe("Named thread");
  // Fallback path (unknown/not-yet-hydrated name) keeps the raw ref - never a
  // blank tab, and the /thread cross-cutting "keep the fallback title" quirk in
  // spirit: nothing here blanks it back out.
  expect(descriptor.title({ ref: "ref_x" }, { threadName: () => undefined })).toBe("ref_x");
  expect(descriptor.title({ ref: "ref_x" }, {})).toBe("ref_x");
});
