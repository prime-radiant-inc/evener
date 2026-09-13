import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterAll, afterEach, beforeAll, expect, test, vi } from "vitest";
import { AppShell } from "../../shell/AppShell";
import { createEditorialClient } from "./fixture";

beforeAll(async () => {
  // Same independent environment boundary as Session.test.tsx: jsdom's zero
  // viewport otherwise prevents the real virtual list from rendering any row.
  vi.spyOn(HTMLElement.prototype, "offsetHeight", "get").mockReturnValue(500);
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  await import("../../panes/welcome/Welcome");
  await import("../../panes/session/Session");
  await import("../../shell/DockHost");
});
afterEach(cleanup);
afterAll(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

test("real AppShell opens fixture parent, distinct child, and returns via navigation", async () => {
  window.history.replaceState({}, "", "/");
  const user = userEvent.setup();
  const client = createEditorialClient();
  render(<AppShell client={client} />);
  await user.click(await screen.findByText("Editorial fixture parent"));
  await screen.findByText("Parent analysis: fixture-only evidence.");
  await user.click(await screen.findByText("Editorial fixture child"));
  await screen.findByText("Child report: independent transcript, not the parent snapshot.");
  expect(
    client.calls.some(
      (call) => call.method === "thread/read" && (call.params as { ref?: string }).ref === "local:editorial-child",
    ),
  ).toBe(true);
  const parentLink = screen
    .getAllByText("Editorial fixture parent")
    .find((element) => element.closest('[role="treeitem"]'));
  if (!parentLink) throw new Error("Parent rail destination missing");
  await user.click(parentLink);
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aeditorial-parent"));
  await screen.findByText("Parent analysis: fixture-only evidence.");
  expect(client.rejectedRequests).toEqual([]);
}, 20000);
