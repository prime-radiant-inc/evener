import type { DevicePairingStatus } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../../../shell/clientContext";
import { DeviceSetup } from "./DeviceSetup";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

test("keeps observing invitation expiry after a stale pending read follows approval", async () => {
  vi.useFakeTimers();
  const client = new FakeClient("ready");
  const invitation = { invitation: "01".repeat(24), origin: "http://hub.local:9180", tailnet: true, expiresAt: 600000 };
  const comparison = "2be9-271d-6222-d455-d2d1-c217-62b2-3b82";
  const claims = [{ publicKey: "02".repeat(32), client: "Phone", comparison }];
  const pending = { state: "pending", claims };
  let release: (status: DevicePairingStatus) => void = () => {};
  const stale = new Promise<DevicePairingStatus>((resolve) => {
    release = resolve;
  });
  let reads = 0;
  client.on("evener/devicePairing/create", () => invitation);
  client.on("evener/devicePairing/read", () => {
    reads++;
    if (reads === 1) return pending;
    if (reads === 2) return stale;
    return { state: "expired", claims: [] };
  });
  client.on("evener/devicePairing/approve", () => ({ state: "approved", claims, approvedKey: claims[0]?.publicKey }));
  client.on("evener/devicePairing/cancel", () => ({ state: "expired", claims: [] }));
  render(
    <ClientProvider client={client}>
      <DeviceSetup initialOrigin="http://hub.local:9180" method="sound" />
    </ClientProvider>,
  );
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: "Create invitation" }));
  });
  const lines = document.querySelector("pre")?.textContent?.trim().split("\n");
  expect(lines).toHaveLength(4);
  expect(lines?.map((line) => line.split(/\s+/).length)).toEqual([2, 2, 2, 2]);
  expect(lines?.flatMap((line) => line.split(/\s+/)).join("-")).toBe(comparison);
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1200);
  });
  await act(async () => {
    fireEvent.click(screen.getByRole("button", { name: `Codes match — approve ${comparison}` }));
  });
  expect(screen.queryByRole("button", { name: "Create invitation" })).toBeNull();
  await act(async () => {
    release(pending);
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1200);
  });
  expect(screen.getByRole("button", { name: "Create invitation" })).toBeTruthy();
});
