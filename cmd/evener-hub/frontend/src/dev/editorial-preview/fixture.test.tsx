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

/** Yields to the event loop, one macrotask turn at a time inside act, until
 * `settled` holds. The shell commits some boot renders through React's
 * scheduler, which only runs when the event loop turns past microtasks - and
 * the scripted fixture answers so fast that the whole boot is one microtask
 * cascade, so an act that only awaits those never lets the scheduler commit.
 * The turn bound is a livelock tripwire (never reached in practice), NOT a
 * wall-clock ceiling: a turn completes whenever the scheduler gets CPU, so
 * machine load cannot trip it the way findByText's 1s default did (sighted
 * at a load average of 900 on 16 CPUs, twice in one gate run). */
async function flushSchedulerTurns(settled: () => boolean, maxTurns = 20): Promise<void> {
  for (let turn = 0; turn < maxTurns && !settled(); turn += 1) {
    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
  }
}

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
  await flushSchedulerTurns(() => screen.queryByText("Editorial fixture parent") !== null);
  await user.click(screen.getByText("Editorial fixture parent"));
  await act(async () => {
    await threadsStore.getState().ensureThread(PARENT);
  });
  await flushSchedulerTurns(() => screen.queryByText("Parent analysis: fixture-only evidence.") !== null);
  expect(screen.getByText("Parent analysis: fixture-only evidence.")).toBeTruthy();
  await user.click(screen.getByText("Editorial fixture child"));
  await act(async () => {
    await threadsStore.getState().ensureThread(CHILD);
  });
  await flushSchedulerTurns(
    () => screen.queryByText("Child report: independent transcript, not the parent snapshot.") !== null,
  );
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
  await flushSchedulerTurns(() => screen.queryByText("Parent analysis: fixture-only evidence.") !== null);
  expect(screen.getByText("Parent analysis: fixture-only evidence.")).toBeTruthy();
  expect(client.rejectedRequests).toEqual([]);
}, 20000);
