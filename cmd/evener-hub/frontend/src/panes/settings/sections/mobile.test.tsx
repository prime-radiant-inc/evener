import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../../protocol/errors";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import { ClientProvider } from "../../../shell/clientContext";
import { connectionStore } from "../../../stores/connection";
import { MobileSection } from "./mobile";

// Lets one test simulate a rejected `import("qrcode.react")` chunk load: the
// mock only throws while the flag is set, so every other test imports the
// real renderer untouched.
const qrChunkControl = vi.hoisted(() => ({
  failChunkLoad: false,
  error: new Error("Simulated QR chunk load failure"),
}));

vi.mock("qrcode.react", async (importOriginal) => {
  if (qrChunkControl.failChunkLoad) {
    throw qrChunkControl.error;
  }
  return importOriginal();
});

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

test("keeps the pairing link usable when the QR chunk fails to load", async () => {
  const onCaughtError = vi.fn();
  const authURL = "https://hub.example.test/auth/mobile-secret";
  client.on("evener/mobile/pairing", () => ({ authUrl: authURL }));

  // This test must run before any other test in this file resolves the real
  // `import("qrcode.react")`: React.lazy caches the resolved chunk process-
  // wide, so once an earlier test renders the QR the chunk-failure path can
  // no longer be simulated in this module registry.
  qrChunkControl.failChunkLoad = true;
  vi.resetModules();
  try {
    const [
      { MobileSection: FreshMobileSection },
      { ClientProvider: FreshClientProvider },
      { connectionStore: freshConnections },
    ] = await Promise.all([
      import("./mobile"),
      import("../../../shell/clientContext"),
      import("../../../stores/connection"),
    ]);
    freshConnections.getState().connect(client);
    render(
      <FreshClientProvider client={client}>
        <FreshMobileSection />
      </FreshClientProvider>,
      { onCaughtError },
    );

    // The failure stays scoped to the QR slot: an error fallback instead of
    // the QR, with the section and its copy-pairing-link action intact.
    expect(await screen.findByText("Couldn't load the QR renderer")).toBeTruthy();
    expect(screen.getByText(/pairing link below still works/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy pairing link" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Mobile app" })).toBeTruthy();
    expect(screen.queryByRole("img", { name: "Mobile app pairing QR code" })).toBeNull();
    expect(onCaughtError).toHaveBeenCalledTimes(1);
    // Vitest wraps a rejected external-module factory; assert the original
    // failure identity, not just the wrapper's generic mocking diagnostic.
    expect(onCaughtError.mock.calls[0]?.[0]).toBeInstanceOf(Error);
    expect(onCaughtError.mock.calls[0]?.[0].cause).toBe(qrChunkControl.error);
  } finally {
    qrChunkControl.failChunkLoad = false;
  }
});

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

test("renders resolved QR svg output inside the labelled image wrapper", async () => {
  const authURL = "https://hub.example.test/auth/mobile-secret";
  client.on("evener/mobile/pairing", () => ({ authUrl: authURL }));

  renderMobileSection();

  const wrapper = await screen.findByRole("img", { name: "Mobile app pairing QR code" });
  // The lazy QR chunk resolved past its Skeleton fallback: a real svg with
  // encoded modules rendered inside the labelled wrapper.
  const svg = wrapper.querySelector("svg");
  if (svg === null) throw new Error("expected the resolved QR svg inside the labelled wrapper");
  expect(svg.querySelector("path")).not.toBeNull();
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
