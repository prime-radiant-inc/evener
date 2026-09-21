// @vitest-environment node

import { describe, expect, test } from "vitest";
import {
  type PluginSelectionState,
  pluginSelectionFromOverrides,
  reconcilePluginSelection,
  selectAllPlugins,
  selectNoPlugins,
  setPluginSelected,
  withPluginSelection,
} from "./spawnPluginSelectionState";
import type { LaunchConfigLayer, PluginLaunchCandidate, PluginPreviewResponse } from "./types.gen";

function candidate(name: string, selected = false): PluginLaunchCandidate {
  return {
    name,
    source: "test",
    selected,
    skillCount: 0,
    agentCount: 0,
    commandCount: 0,
    hookCount: 0,
    mcpCount: 0,
  };
}
function preview(...plugins: PluginLaunchCandidate[]): PluginPreviewResponse {
  return { plugins };
}

describe("plugin selection state", () => {
  test("untouched default omits enabledPlugins from overrides", () => {
    expect(withPluginSelection({ sandbox: "off" }, { mode: "default" })).toEqual({ sandbox: "off" });
  });

  test("first toggle materializes all server-selected candidates", () => {
    expect(setPluginSelected({ mode: "default" }, preview(candidate("a", true), candidate("b")), "b", false)).toEqual({
      mode: "explicit",
      names: ["a"],
    });
  });

  test("explicit none stays present on the wire", () => {
    expect(withPluginSelection({ sandbox: "off" }, { mode: "explicit", names: [] })).toEqual({
      sandbox: "off",
      enabledPlugins: [],
    });
  });

  test("select all and none preserve explicit presence", () => {
    const p = preview(candidate("b"), candidate("a"));
    expect(selectAllPlugins(p)).toEqual({ mode: "explicit", names: ["b", "a"] });
    expect(selectNoPlugins()).toEqual({ mode: "explicit", names: [] });
  });

  test("default refresh follows candidates without materializing selection", () => {
    expect(reconcilePluginSelection({ mode: "default" }, preview(candidate("new", true)))).toEqual({ mode: "default" });
  });

  test("explicit refresh keeps surviving names in candidate order and retains stale names", () => {
    const selection: PluginSelectionState = { mode: "explicit", names: ["old", "a", "b"] };
    expect(reconcilePluginSelection(selection, preview(candidate("b"), candidate("new"), candidate("a")))).toEqual({
      mode: "explicit",
      names: ["b", "a", "old"],
    });
  });

  test("stale names with selection errors remain selectable", () => {
    expect(
      reconcilePluginSelection(
        { mode: "explicit", names: ["missing", "a"] },
        { ...preview(candidate("a")), selectionErrors: [{ name: "missing", reason: "not found" }] },
      ),
    ).toEqual({ mode: "explicit", names: ["a", "missing"] });
  });

  test("removed explicit names remain on the wire even without a selection error", () => {
    expect(reconcilePluginSelection({ mode: "explicit", names: ["removed", "a"] }, preview(candidate("a")))).toEqual({
      mode: "explicit",
      names: ["a", "removed"],
    });
  });

  test("explicit toggles preserve stable preview order", () => {
    const p = preview(candidate("b"), candidate("a"));
    expect(setPluginSelected({ mode: "explicit", names: ["a"] }, p, "b", true)).toEqual({
      mode: "explicit",
      names: ["b", "a"],
    });
    expect(setPluginSelected({ mode: "explicit", names: ["b", "a"] }, p, "b", false)).toEqual({
      mode: "explicit",
      names: ["a"],
    });
  });

  test("withPluginSelection does not mutate advanced overrides", () => {
    const overrides: LaunchConfigLayer = { sandbox: "off", maxRounds: 2 };
    expect(withPluginSelection(overrides, { mode: "explicit", names: ["a"] })).toEqual({
      sandbox: "off",
      maxRounds: 2,
      enabledPlugins: ["a"],
    });
    expect(overrides).toEqual({ sandbox: "off", maxRounds: 2 });
  });

  test("pluginSelectionFromOverrides decodes the omitted and explicit forms", () => {
    expect(pluginSelectionFromOverrides({ sandbox: "off" })).toEqual({ mode: "default" });
    expect(pluginSelectionFromOverrides({ enabledPlugins: [] })).toEqual({ mode: "explicit", names: [] });
    expect(pluginSelectionFromOverrides({ enabledPlugins: ["b", "a"] })).toEqual({
      mode: "explicit",
      names: ["b", "a"],
    });
  });

  test("pluginSelectionFromOverrides does not alias the overrides array", () => {
    const overrides: LaunchConfigLayer = { enabledPlugins: ["a"] };
    const selection = pluginSelectionFromOverrides(overrides);
    if (selection.mode === "explicit") selection.names.push("b");
    expect(overrides.enabledPlugins).toEqual(["a"]);
  });

  test("a selection round-trips through withPluginSelection and back", () => {
    const overrides: LaunchConfigLayer = { sandbox: "off", maxRounds: 2 };
    for (const selection of [
      { mode: "default" },
      { mode: "explicit", names: [] },
      { mode: "explicit", names: ["a", "b"] },
    ] satisfies PluginSelectionState[]) {
      expect(pluginSelectionFromOverrides(withPluginSelection(overrides, selection))).toEqual(selection);
    }
  });
});
