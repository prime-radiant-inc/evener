import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterAll, afterEach, beforeAll, expect, test, vi } from "vitest";
import { AppShell } from "../../shell/AppShell";
import { navigationStore } from "../../stores/navigation/store";
import { threadsStore } from "../../stores/threads";
import { CHILD, createEditorialClient, PARENT } from "./fixture";

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

// Same load-sensitive tripwire used by the AppShell warm-up tests. The store
// reads are awaited separately; findByText supplies the act-aware wait for
// React's later DOM commit and fails boundedly if the fixture never appears.
const FIXTURE_READY_TRIPWIRE_MS = 10_000;

async function waitForFixtureText(text: string, timeout = FIXTURE_READY_TRIPWIRE_MS): Promise<HTMLElement> {
  return screen.findByText(text, undefined, { timeout });
}

test("fixture wait rejects when the requested text never appears", async () => {
  render(<div />);
  await expect(waitForFixtureText("missing fixture text", 50)).rejects.toThrow();
}, 500);

test("real AppShell opens fixture parent, distinct child, and returns via navigation", async () => {
  window.history.replaceState({}, "", "/");
  const user = userEvent.setup();
  const client = createEditorialClient();
  render(<AppShell client={client} />);
  // Await the boot's own completions - the fixture answers every read
  // synchronously, so the manifest and live-section loads are the awaitable
  // work behind the rail rows - then let the scheduler commit. The same
  // holds after each click: ensureThread joins the pane's own thread/read.
  await act(async () => {
    await navigationStore.getState().loadManifest();
    await navigationStore.getState().loadSection("live");
  });
  await waitForFixtureText("Editorial fixture parent");
  await user.click(screen.getByText("Editorial fixture parent"));
  await act(async () => {
    await threadsStore.getState().ensureThread(PARENT);
  });
  await waitForFixtureText("Parent analysis: fixture-only evidence.");
  expect(screen.getByText("Parent analysis: fixture-only evidence.")).toBeTruthy();
  await user.click(screen.getByText("Editorial fixture child"));
  await act(async () => {
    await threadsStore.getState().ensureThread(CHILD);
  });
  await waitForFixtureText("Child report: independent transcript, not the parent snapshot.");
  expect(screen.getByText("Child report: independent transcript, not the parent snapshot.")).toBeTruthy();
  expect(
    client.calls.some((call) => call.method === "thread/read" && (call.params as { ref?: string }).ref === CHILD),
  ).toBe(true);
  const parentLink = screen
    .getAllByText("Editorial fixture parent")
    .find((element) => element.closest('[role="treeitem"]'));
  if (!parentLink) throw new Error("Parent rail destination missing");
  await user.click(parentLink);
  // Navigation is synchronous state, not a load: assert it directly.
  expect(window.location.pathname).toBe("/s/local%3Aeditorial-parent");
  await act(async () => {
    await threadsStore.getState().ensureThread(PARENT);
  });
  await waitForFixtureText("Parent analysis: fixture-only evidence.");
  expect(screen.getByText("Parent analysis: fixture-only evidence.")).toBeTruthy();
  expect(client.rejectedRequests).toEqual([]);
}, 20000);
