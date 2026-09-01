import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { WireError } from "../../../protocol/errors";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import { ClientProvider } from "../../../shell/clientContext";
import { connectionStore } from "../../../stores/connection";
import { MobileSection } from "./mobile";

let client: FakeClient;

beforeEach(() => {
  client = new FakeClient("ready");
  connectionStore.getState().connect(client);
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

function renderMobileSection() {
  render(
    <ClientProvider client={client}>
      <MobileSection />
    </ClientProvider>,
  );
}

test("renders HTTP observation and reusable-capability warnings for a mixed-case scheme", async () => {
  const authURL = "HTTP://192.168.1.20:9180/auth/mobile-secret";
  client.on("evener/mobile/pairing", () => ({ authUrl: authURL }));

  renderMobileSection();

  expect(await screen.findByRole("img", { name: "Mobile app pairing QR code" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Copy pairing link" })).toBeTruthy();
  expect(screen.getByText(/anyone who can observe this network can observe the pairing capability/i)).toBeTruthy();
  expect(screen.getByText(/this link remains valid and can be reused/i)).toBeTruthy();
  expect(client.calls).toEqual([{ method: "evener/mobile/pairing", params: { origin: window.location.origin } }]);
});

test("renders the reusable-capability warning for HTTPS without the HTTP observation warning", async () => {
  const authURL = "https://hub.example.test/auth/mobile-secret";
  client.on("evener/mobile/pairing", () => ({ authUrl: authURL }));

  renderMobileSection();

  expect(await screen.findByRole("img", { name: "Mobile app pairing QR code" })).toBeTruthy();
  expect(screen.getByText(/this link remains valid and can be reused/i)).toBeTruthy();
  expect(screen.queryByText(/anyone who can observe this network/i)).toBeNull();
});

test("shows configuration guidance instead of a QR when the Hub has no reachable origin", async () => {
  client.on("evener/mobile/pairing", () => {
    throw new WireError("mobile pairing requires a reachable non-loopback Hub origin", -32013, {
      evenerErrorInfo: "conflict",
    });
  });

  renderMobileSection();

  expect(await screen.findByText("Mobile pairing needs a reachable Hub origin")).toBeTruthy();
  expect(screen.getByText(/configure mobile_base_url/i)).toBeTruthy();
  expect(screen.queryByRole("img", { name: "Mobile app pairing QR code" })).toBeNull();
});
