import { beforeEach, expect, test } from "vitest";
import { activityPanelStore } from "./activityPanel";

beforeEach(() => activityPanelStore.getState().resetForTests());
test("fold disclosure is isolated by routing ref and toggle is reversible", () => {
  const state = activityPanelStore.getState();
  state.toggleFold("alias:a", "fold:qualified");
  expect(activityPanelStore.getState().entries.get("alias:a")?.expandedFoldIDs).toEqual(["fold:qualified"]);
  expect(activityPanelStore.getState().entries.has("alias:b")).toBe(false);
  state.toggleFold("alias:a", "fold:qualified");
  expect(activityPanelStore.getState().entries.get("alias:a")?.expandedFoldIDs).toEqual([]);
});
