import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { SettingsOverviewResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
import { resetDaemonResidentsStoreForTests } from "../../../stores/daemonResidents";
import { resetSettingsOverviewStoreForTests } from "../../../stores/settingsOverview";
import { HubSection } from "./hub";

function connectFakeClient(): FakeClient {
  const fake = new FakeClient("ready");
  connectionStore.getState().connect(fake);
  return fake;
}

const SAMPLE_RESPONSE: SettingsOverviewResponse = {
  hub: {
    listenAddr: "127.0.0.1:9180",
    runDir: "/tmp/evener-run",
    spawnTimeout: "30s",
    daemonIdleTimeoutMillis: 3600000,
  },
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsOverviewStoreForTests();
  resetDaemonResidentsStoreForTests();
});

afterEach(cleanup);

test("fetches the overview on mount and renders the 3 read-only fields with their help text", async () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => SAMPLE_RESPONSE);
  fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));

  render(<HubSection />);

  expect(await screen.findByText("127.0.0.1:9180")).toBeTruthy();
  expect(screen.getByText("/tmp/evener-run")).toBeTruthy();
  expect(screen.getByText("30s")).toBeTruthy();
  expect(screen.getByText("Listen address").tagName).toBe("DT");
  expect(screen.getByText(/Address and port the hub HTTP server binds to/)).toBeTruthy();
  await waitFor(() =>
    expect(fake.calls).toEqual([
      { method: "evener/settings/overview", params: {} },
      { method: "evener/daemon/list", params: {} },
    ]),
  );
});

test("shows a loading skeleton before the overview resolves", () => {
  const fake = connectFakeClient();
  fake.on("evener/settings/overview", () => new Promise(() => {})); // never resolves
  // daemon/list is not called until settings/overview resolves (HubResidents mounts after data)

  render(<HubSection />);
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
});

describe("on load failure", () => {
  // A plain Error's own message is internal detail (not something the hub
  // wrote for a person to read), so friendlyErrorMessage replaces it with a
  // generic sentence - see protocol/errors.test.ts for that contract.
  test("shows an inline error state with a retry action instead of a toast", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/overview", () => {
      throw new Error("hub unreachable");
    });
    // daemon/list is not called when overview fails (HubResidents never mounts)

    render(<HubSection />);

    expect(await screen.findByText("Couldn't load hub settings")).toBeTruthy();
    expect(await screen.findByText("Something went wrong.")).toBeTruthy();
    expect(screen.queryByText("hub unreachable")).toBeNull();
  });

  test("Retry re-requests the overview", async () => {
    const fake = connectFakeClient();
    fake.on("evener/settings/overview", () => {
      throw new Error("hub unreachable");
    });
    const user = userEvent.setup();

    render(<HubSection />);
    await screen.findByText("Couldn't load hub settings");

    fake.on("evener/settings/overview", () => SAMPLE_RESPONSE);
    fake.on("evener/daemon/list", () => ({ defaultTimeoutMillis: 3600000, daemons: [] }));
    await user.click(screen.getByRole("button", { name: "Retry" }));

    expect(await screen.findByText("127.0.0.1:9180")).toBeTruthy();
    await waitFor(() => expect(fake.calls).toHaveLength(3));
  });
});
