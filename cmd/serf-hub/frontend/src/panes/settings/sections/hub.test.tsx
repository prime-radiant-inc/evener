import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { SettingsOverviewResponse } from "../../../protocol/types.gen";
import { connectionStore } from "../../../stores/connection";
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
    runDir: "/tmp/serf-run",
    spawnTimeout: "30s",
  },
};

beforeEach(() => {
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetSettingsOverviewStoreForTests();
});

afterEach(cleanup);

test("fetches the overview on mount and renders the 3 read-only fields with their help text", async () => {
  const fake = connectFakeClient();
  fake.on("serf/settings/overview", () => SAMPLE_RESPONSE);

  render(<HubSection />);

  expect(await screen.findByText("127.0.0.1:9180")).toBeTruthy();
  expect(screen.getByText("/tmp/serf-run")).toBeTruthy();
  expect(screen.getByText("30s")).toBeTruthy();
  expect(screen.getByText("Listen address").tagName).toBe("DT");
  expect(screen.getByText(/Address and port the hub HTTP server binds to/)).toBeTruthy();
  expect(fake.calls).toEqual([{ method: "serf/settings/overview", params: {} }]);
});

test("shows a loading skeleton before the overview resolves", () => {
  const fake = connectFakeClient();
  fake.on("serf/settings/overview", () => new Promise(() => {})); // never resolves

  render(<HubSection />);
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
});

describe("on load failure", () => {
  test("shows an inline error state with a retry action instead of a toast", async () => {
    const fake = connectFakeClient();
    fake.on("serf/settings/overview", () => {
      throw new Error("hub unreachable");
    });

    render(<HubSection />);

    expect(await screen.findByText("Couldn't load hub settings")).toBeTruthy();
    expect(screen.getByText("hub unreachable")).toBeTruthy();
  });

  test("Retry re-requests the overview", async () => {
    const fake = connectFakeClient();
    fake.on("serf/settings/overview", () => {
      throw new Error("hub unreachable");
    });
    const user = userEvent.setup();

    render(<HubSection />);
    await screen.findByText("Couldn't load hub settings");

    fake.on("serf/settings/overview", () => SAMPLE_RESPONSE);
    await user.click(screen.getByRole("button", { name: "Retry" }));

    expect(await screen.findByText("127.0.0.1:9180")).toBeTruthy();
    await waitFor(() => expect(fake.calls).toHaveLength(2));
  });
});
