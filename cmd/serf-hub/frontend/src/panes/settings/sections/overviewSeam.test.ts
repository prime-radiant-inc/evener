// @vitest-environment node
import { describe, expect, test } from "vitest";
import { useSettingsOverview } from "./overviewSeam";

describe("useSettingsOverview (placeholder pending stores/settingsOverview.ts, T4)", () => {
  test("returns the pinned SettingsOverviewStore shape: no data yet, not loading, no error", () => {
    const state = useSettingsOverview();
    expect(state.data).toBeNull();
    expect(state.loading).toBe(false);
    expect(state.error).toBeNull();
    expect(typeof state.fetch).toBe("function");
  });

  test("fetch() resolves without throwing (a harmless no-op until the real store lands)", async () => {
    await expect(useSettingsOverview().fetch()).resolves.toBeUndefined();
  });
});
