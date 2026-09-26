import { beforeEach, describe, expect, test, vi } from "vitest";

// k7harness-entry renders SessionChrome - and therefore ActivityPanel - outside
// the app shell that wires the activity stores, so it has to register the
// activity summary link itself, exactly as AppShell.tsx and
// dev/surface-sections/chrome.tsx do. Without that call the panel's first
// continuation fetch throws "no summary link registered".
describe("k7harness entry", () => {
  beforeEach(() => {
    vi.resetModules();
    document.body.innerHTML = '<div id="root"></div>';
  });

  // The harness entry pulls in the whole SessionChrome graph, whose Vite
  // transform alone can outrun the default 5s ceiling on a loaded host.
  test("registers the activity summary link, so a continuation fetch does not throw", async () => {
    await import("./k7harness-entry");
    const { activityPanelStore } = await import("../stores/activityPanel");

    expect(() => activityPanelStore.getState().beginFetch("ref_a", { nodeID: "session:sess_a" })).not.toThrow();
  }, 30_000);
});
