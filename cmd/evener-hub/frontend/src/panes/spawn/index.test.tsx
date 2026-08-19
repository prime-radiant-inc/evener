import { expect, test } from "vitest";
import { paneFor } from "../../shell/paneRegistry";
import "./index"; // registers the "spawn" pane type

test("registers the spawn pane as a singleton titled 'New session'", () => {
  const pane = paneFor("spawn");
  expect(pane.id).toBe("spawn");
  expect(pane.singleton).toBe(true);
  expect(pane.title({}, {})).toBe("New session");
});
