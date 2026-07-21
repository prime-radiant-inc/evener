import { afterEach, beforeAll, beforeEach, test, expect, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { AppwireClient } from "../protocol/client";
import { FakeClient } from "../protocol/testing/fakeClient";
import type { InitializeResponse } from "../protocol/types.gen";
import { connectionStore } from "../stores/connection";
import { AppShell } from "./AppShell";

const ALL_FEATURES_OFF = {
  threadList: false,
  threadTurnsList: false,
  turnStart: false,
  turnSteer: false,
  threadClear: false,
  threadShutdown: false,
  forkFromTurn: false,
  tasks: false,
  transcriptList: false,
  modelList: false,
  directoryComplete: false,
  auth: false,
};

// Await the welcome pane's lazy-loaded module ONCE up front so React.lazy
// resolves from a warm module cache - mirrors App.test.tsx's own beforeAll
// pattern for the same reason: the slow part of lazy-loading is the
// transform/import work, an awaitable completion, not something to race
// with a widened findBy deadline.
beforeAll(async () => {
  await import("../panes/welcome/Welcome");
});

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
});

test("mounts and renders the welcome pane", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  expect(await screen.findByText("No session open")).toBeTruthy();
});

test("wires the injected client into connectionStore (connects on mount)", () => {
  const fake = new FakeClient("ready");
  render(<AppShell client={fake} />);
  expect(connectionStore.getState().client).toBe(fake);
  expect(connectionStore.getState().state).toBe("ready");
});

test("shows no banner while the injected client is ready", async () => {
  render(<AppShell client={new FakeClient("ready")} />);
  await screen.findByText("No session open");
  expect(screen.queryByText(/reconnecting/i)).toBeNull();
  expect(screen.queryByText(/connection closed/i)).toBeNull();
});

test("banner reflects reconnecting state when injected", async () => {
  const fake = new FakeClient("ready");
  render(<AppShell client={fake} />);
  await screen.findByText("No session open");

  act(() => {
    fake.emitStateChange("reconnecting");
  });

  expect(await screen.findByText(/reconnecting to the server/i)).toBeTruthy();
});

test('clicking "New session" navigates to /new and the welcome pane shows a note', async () => {
  const user = userEvent.setup();
  render(<AppShell client={new FakeClient("ready")} />);
  const button = await screen.findByRole("button", { name: "New session" });

  await user.click(button);

  expect(window.location.pathname).toBe("/new");
  expect(await screen.findByText(/starting a new session isn't available yet/i)).toBeTruthy();
});

test("populates connectionStore.serverInfo from the injected client's scripted connect() response", async () => {
  const fake = new FakeClient("ready");
  const scripted: InitializeResponse = {
    serverInfo: { name: "serf-hub-test", version: "9.9.9" },
    protocolVersion: "1",
    sourceId: "src_test",
    features: ALL_FEATURES_OFF,
  };
  fake.scriptConnect(() => scripted);

  render(<AppShell client={fake} />);
  await screen.findByText("No session open");

  await waitFor(() => {
    expect(connectionStore.getState().serverInfo).toEqual({ name: "serf-hub-test", version: "9.9.9" });
  });
});

test("closes the client it constructed itself on unmount", () => {
  const closeSpy = vi.spyOn(AppwireClient.prototype, "close");
  // No client prop: AppShell constructs its own real AppwireClient (never
  // connected under MODE==="test" - see AppShell.tsx) and must own tearing
  // it down again on unmount, the same one-client-per-window invariant a
  // real navigation away from the app would need.
  const { unmount } = render(<AppShell />);

  unmount();

  expect(closeSpy).toHaveBeenCalledTimes(1);
  closeSpy.mockRestore();
});
