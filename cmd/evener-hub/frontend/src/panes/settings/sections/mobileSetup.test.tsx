import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { connectionStore, resetConnectionStoreForTests } from "../../../stores/connection";
import { emptyStateReasons } from "../../../widgets/emptystate/emptyStateTestUtils";
import { MobileSection } from "./mobile";
import { statusReasons } from "./reasonTestUtils";

let client: FakeClient;
beforeEach(() => {
  client = new FakeClient("ready");
  connectionStore.getState().connect(client);
});
afterEach(() => {
  cleanup();
  resetConnectionStoreForTests();
});
function renderMobileSection() {
  render(
    <ClientProvider client={client}>
      <MobileSection />
    </ClientProvider>,
  );
}

test("keeps in-app destination selection available when automatic discovery cannot find an origin", async () => {
  client.on("evener/mobile/pairing", () => {
    throw new WireError("unavailable destination", -32013, { evenerErrorInfo: "conflict" });
  });
  renderMobileSection();
  await waitFor(() => expect(emptyStateReasons(document.body)).toEqual(["needs-configuration"]));
  fireEvent.click(screen.getByRole("button", { name: "Pair using sound" }));
  expect(await screen.findByRole("textbox", { name: "Hub address" })).toBeTruthy();
});

test("keeps destination selection usable when the discovered URL is not supported by the browser", async () => {
  client.on("evener/mobile/pairing", () => ({ authUrl: "http://[fe80::1%25en0]:9180/auth/mobile-secret" }));
  renderMobileSection();
  await waitFor(() => expect(statusReasons(document.body)).toEqual(["unavailable"]));
  expect(emptyStateReasons(document.body)).toEqual([]);
  fireEvent.click(screen.getByRole("button", { name: "Pair using sound" }));
  expect(await screen.findByRole("textbox", { name: "Hub address" })).toBeTruthy();
});

test.each(["unreachable", "error", "ready"])(
  "keeps manual phone QR setup available with %s destination discovery",
  async (discovery) => {
    const origin = "http://100.87.98.83:9180";
    const discovered = "http://192.168.1.2:9180";
    client.on("evener/mobile/pairing", () => {
      if (discovery === "unreachable")
        throw new WireError("unavailable destination", -32013, { evenerErrorInfo: "conflict" });
      return {
        authUrl: `${discovery === "ready" ? discovered : "http://[fe80::1%25en0]:9180"}/auth/mobile-secret`,
      };
    });
    client.on("evener/devicePairing/create", (params) => ({
      invitation: "01".repeat(24),
      origin: params.origin,
      tailnet: params.tailnet,
      expiresAt: 600000,
    }));
    client.on("evener/devicePairing/read", () => ({ state: "pending", claims: [] }));

    renderMobileSection();
    fireEvent.click(
      await screen.findByRole("button", {
        name: discovery === "ready" ? "Use another Hub address" : "Enter Hub address",
      }),
    );
    const address = await screen.findByRole("textbox", { name: "Hub address" });
    expect((address as HTMLInputElement).value).toBe(discovery === "ready" ? discovered : window.location.origin);
    fireEvent.change(address, { target: { value: origin } });
    fireEvent.click(screen.getByRole("button", { name: "Create invitation" }));

    const qr = await screen.findByRole("img", { name: "Device pairing invitation QR code" });
    expect(qr.querySelector("svg path")).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Play setup sound" })).toBeNull();
    expect(client.calls.find((call) => call.method === "evener/devicePairing/create")?.params).toEqual({
      origin,
      tailnet: window.location.protocol === "http:",
    });
    fireEvent.click(screen.getByRole("button", { name: "Close setup" }));
    expect(await screen.findByRole("button", { name: "Pair using sound" })).toBeTruthy();
  },
);

test("seeds device setup from the discovered destination and submits an edited address", async () => {
  const origin = "http://100.87.98.83:9180";
  const edited = "https://hub.example.test";
  client.on("evener/mobile/pairing", () => ({ authUrl: `${origin}/auth/mobile-secret` }));
  client.on("evener/devicePairing/create", (params) => ({
    invitation: "01".repeat(24),
    origin: params.origin,
    tailnet: params.tailnet,
    expiresAt: 600000,
  }));
  client.on("evener/devicePairing/read", () => ({ state: "pending", claims: [] }));

  renderMobileSection();
  await screen.findByRole("button", { name: "Copy pairing link" });
  fireEvent.click(screen.getByRole("button", { name: "Pair using sound" }));
  const address = await screen.findByRole("textbox", { name: "Hub address" });
  expect((address as HTMLInputElement).value).toBe(origin);
  fireEvent.change(address, { target: { value: edited } });
  fireEvent.click(screen.getByRole("button", { name: "Create invitation" }));
  expect(await screen.findByRole("button", { name: "Play setup sound" })).toBeTruthy();
  expect(screen.queryByRole("img")).toBeNull();
  expect(client.calls.find((call) => call.method === "evener/devicePairing/create")?.params).toEqual({
    origin: edited,
    tailnet: window.location.protocol === "http:",
  });
});
