// @vitest-environment node
import { expect, test } from "vitest";
import { paneFor } from "../../shell/paneRegistry";
import { paneToURL } from "../../shell/routing";
import { sessionPanelTitle } from "./index";

test.each([
  ["sessionTasks", "Tasks"],
  ["sessionActivity", "Activity"],
  ["sessionDetails", "Details"],
] as const)("registers the %s session panel pane", (id, label) => {
  expect(paneFor(id).title({ ref: "ref_a" }, { threadName: () => "Build" })).toBe(`${label} · Build`);
  expect(paneFor(id).title({ ref: "ref_a" }, {})).toBe(`${label} · ref_a`);
  expect(paneToURL(id, { ref: "ref_a" })).toBeNull();
});

test("uses the same title derivation for fallback and renamed sessions", () => {
  for (const [id, kind] of [
    ["sessionTasks", "tasks"],
    ["sessionActivity", "activity"],
    ["sessionDetails", "details"],
  ] as const) {
    const descriptor = paneFor(id);
    expect(descriptor.title({ ref: "ref_a" }, {})).toBe(sessionPanelTitle(kind, "ref_a"));
    expect(descriptor.title({ ref: "ref_a" }, { threadName: () => "Renamed session" })).toBe(
      sessionPanelTitle(kind, "ref_a", "Renamed session"),
    );
  }
});
