// @vitest-environment node
import { expect, test } from "vitest";
import { paneFor } from "../../shell/paneRegistry";
import { paneToURL } from "../../shell/routing";
import "./index";

test.each([
  ["sessionTasks", "Tasks"],
  ["sessionActivity", "Activity"],
  ["sessionDetails", "Details"],
] as const)("registers the %s session panel pane", (id, label) => {
  expect(paneFor(id).title({ ref: "ref_a" }, { threadName: () => "Build" })).toBe(`${label} · Build`);
  expect(paneFor(id).title({ ref: "ref_a" }, {})).toBe(`${label} · ref_a`);
  expect(paneToURL(id, { ref: "ref_a" })).toBeNull();
});
