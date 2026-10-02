import { act } from "@testing-library/react";
import { beforeEach, describe, expect, test, vi } from "vitest";

// The standalone harness mounts the same shared activity owner as the app.
describe("k7harness entry", () => {
  beforeEach(() => {
    vi.resetModules();
    document.body.innerHTML = '<div id="root"></div>';
  });

  // The harness entry pulls in the whole SessionChrome graph, whose Vite
  // transform alone can outrun the default 5s ceiling on a loaded host.
  test("mounts the standalone chrome without a separate activity reader", async () => {
    // The entry renders its root as it loads.
    await act(async () => {
      await import("./k7harness-entry");
    });
    const { activityPanelStore } = await import("../stores/activityPanel");

    activityPanelStore.getState().toggleFold("ref_a", "fold:qualified");
    expect(activityPanelStore.getState().entries.get("ref_a")?.expandedFoldIDs).toEqual(["fold:qualified"]);
  }, 30_000);
});
