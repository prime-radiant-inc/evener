import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { MotionProvider } from "../../motion";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { activityClient, activityDetailsThread, activityThread } from "../../stores/sessionActivityTestUtils";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { installFocusedScope } from "../statusbar/scopeTestUtils";
import { resetWorkspaceStoreForTests } from "../workspace";
import { ActivitySidebar } from "./ActivitySidebar";
import { activitySidebarStore, resetActivitySidebarStoreForTests } from "./activitySidebarStore";

const ref = "remote:about-owner";
function mountSidebar() {
  return render(
    <MotionProvider>
      <ActivitySidebar />
    </MotionProvider>,
  );
}
beforeEach(() => installLocalStorage(new MemoryStorage()));
afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetActivitySidebarStoreForTests();
  resetThreadsStoreForTests();
  connectionStore.setState({ client: null, state: "idle" });
  vi.restoreAllMocks();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

test("About renders the explicit remote session's hydrated accounting", async () => {
  const client = activityClient();
  client.on("thread/read", ({ ref }) => activityDetailsThread(ref));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("about-owner");
  expect(screen.getByText("anthropic/claude-sonnet")).toBeTruthy();
  expect(screen.getByText("idle")).toBeTruthy();
  expect(screen.getByTestId("session-details-context").textContent).toContain("42% used");
  expect(screen.getByTestId("session-details-context").textContent).toContain("58K left");
  expect(screen.getByTestId("session-details-tokens").textContent).toContain("↑100K");
  expect(screen.getByTestId("session-details-tokens").textContent).toContain("↓20K");
  expect(screen.getByTestId("session-details-cost").textContent).toContain("~$1.00");
  expect(screen.getByTestId("session-details-work-time").textContent).toContain("4s");
  expect(screen.getByTestId("session-details-cwd").textContent).toContain("/work/session");
  expect(screen.getByTestId("session-details-project").textContent).toContain("/work");
  expect(screen.getByTestId("session-details-branch").textContent).toContain("feature/overview");
  expect(screen.getByTestId("session-details-created")).toBeTruthy();
  expect(screen.getByTestId("session-details-updated")).toBeTruthy();
  expect(screen.getAllByRole("radio").map((el) => el.textContent?.split("\n")[0])).toEqual([
    "Agents",
    "Jobs",
    "Watches",
    "Tasks",
    "About",
  ]);
  expect(screen.getByRole("radio", { name: "About" }).textContent).toBe("About");
  expect(client.calls.some((call) => call.method.endsWith("/list"))).toBe(false);
});

test.each(["ended", "closed"] as const)("About preserves %s accounting without live context", async (status) => {
  const client = activityClient();
  client.on("thread/read", ({ ref }) => activityDetailsThread(ref, { status: { type: status } }));
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("about-owner");
  expect(screen.getByText(status)).toBeTruthy();
  expect(screen.queryByTestId("session-details-context")).toBeNull();
  expect(screen.getByTestId("session-details-cost").textContent).toContain("~$1.00");
  expect(screen.getByTestId("session-details-tokens").textContent).toContain("↑100K");
  expect(screen.getByTestId("session-details-tokens").textContent).toContain("↓20K");
});

test("About omits unavailable accounting and empty sections", async () => {
  const client = activityClient();
  client.on("thread/read", ({ ref }) => {
    const response = activityThread(ref);
    response.thread.cwd = "";
    return response;
  });
  connectionStore.getState().connect(client);
  installFocusedScope(ref);
  activitySidebarStore.getState().openWith("about");
  mountSidebar();
  await screen.findByText("owner");
  expect(screen.queryByRole("heading", { name: "Usage" })).toBeNull();
  expect(screen.queryByRole("heading", { name: "Location" })).toBeNull();
  expect(screen.queryByTestId("session-details-context")).toBeNull();
  expect(screen.queryByTestId("session-details-cost")).toBeNull();
  expect(screen.queryByTestId("session-details-tokens")).toBeNull();
  expect(screen.queryByTestId("session-details-work-time")).toBeNull();
});
