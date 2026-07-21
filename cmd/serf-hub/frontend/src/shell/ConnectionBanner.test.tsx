import { afterEach, beforeEach, describe, test, expect, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ConnectionState } from "../protocol/client";
import { ConnectionBanner } from "./ConnectionBanner";

afterEach(cleanup);

const QUIET_STATES: ConnectionState[] = ["idle", "connecting", "ready"];

for (const state of QUIET_STATES) {
  test(`renders nothing while connection state is "${state}"`, () => {
    const { container } = render(<ConnectionBanner state={state} />);
    expect(container.textContent).toBe("");
  });
}

test('shows a reconnecting message while connection state is "reconnecting"', () => {
  render(<ConnectionBanner state="reconnecting" />);
  expect(screen.getByText(/reconnecting/i)).toBeTruthy();
});

test('shows a closed message while connection state is "closed"', () => {
  render(<ConnectionBanner state="closed" />);
  expect(screen.getByText(/connection closed/i)).toBeTruthy();
});

test('offers a "Reload" action while reconnecting', () => {
  render(<ConnectionBanner state="reconnecting" />);
  expect(screen.getByRole("button", { name: "Reload" })).toBeTruthy();
});

test('offers a "Reload" action while closed', () => {
  render(<ConnectionBanner state="closed" />);
  expect(screen.getByRole("button", { name: "Reload" })).toBeTruthy();
});

// window.location.reload is the only mechanism that reliably re-establishes
// a connection: AppwireClient.connect() caches a single connectPromise for
// the object's whole lifetime (protocol/client.ts) and never resets it, so
// calling connect() again on an already-"closed" client just replays its
// original rejection instead of dialing a new socket - a full reload is the
// only thing guaranteed to hand the app a fresh client. jsdom throws "Not
// implemented: navigation" on a real reload call, so this - like similar
// window.location tests elsewhere - stubs the method to observe the call
// without actually navigating.
describe("clicking Reload", () => {
  let reloadSpy: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    reloadSpy = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload: reloadSpy });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  test("calls window.location.reload from the closed banner", async () => {
    const user = userEvent.setup();
    render(<ConnectionBanner state="closed" />);
    await user.click(screen.getByRole("button", { name: "Reload" }));
    expect(reloadSpy).toHaveBeenCalledTimes(1);
  });

  test("calls window.location.reload from the reconnecting banner", async () => {
    const user = userEvent.setup();
    render(<ConnectionBanner state="reconnecting" />);
    await user.click(screen.getByRole("button", { name: "Reload" }));
    expect(reloadSpy).toHaveBeenCalledTimes(1);
  });
});
