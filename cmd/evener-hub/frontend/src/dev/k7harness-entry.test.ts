import { act, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, test, vi } from "vitest";

// The standalone harness mounts the chrome outside the app shell.
describe("k7harness entry", () => {
  beforeEach(() => {
    vi.resetModules();
    document.body.innerHTML = '<div id="root"></div>';
  });

  // The harness entry pulls in the whole SessionChrome graph, whose Vite
  // transform alone can outrun the default 5s ceiling on a loaded host.
  test("mounts the standalone chrome", async () => {
    // The entry renders its root as it loads.
    await act(async () => {
      await import("./k7harness-entry");
    });
    await waitFor(() => expect(document.querySelector('[data-testid^="session-chrome"]')).toBeTruthy());
  }, 30_000);
});
