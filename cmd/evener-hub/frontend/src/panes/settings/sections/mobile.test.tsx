import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, type RenderOptions, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { connectionStore, resetConnectionStoreForTests } from "../../../stores/connection";
import { emptyStateReasons } from "../../../widgets/emptystate/emptyStateTestUtils";
import { MobileSection } from "./mobile";
import { statusReasons } from "./reasonTestUtils";

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

// Vitest's mocker builds its wrapper error in the runner's realm, not the test
// file's VM context, so `instanceof Error` is false for it under the vmThreads
// pool. The Error brand survives the realm boundary.
function isError(value: unknown): value is Error {
  return Object.prototype.toString.call(value) === "[object Error]";
}

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

test("keeps the pairing link usable when the QR chunk fails to load", async () => {
  // Vitest wraps the rejected mock factory; assert both that wrapper and the
  // original failure instead of accepting an arbitrary QR rendering error.
  const mockErrorMessage =
    '[vitest] There was an error when mocking a module. If you are using "vi.mock" factory, make sure there are no top level variables inside, since this call is hoisted to top of the file. Read more: https://vitest.dev/api/vi.html#vi-mock';
  const onCaughtError = vi.fn<NonNullable<RenderOptions["onCaughtError"]>>((error, info) => {
    if (!isError(error) || error.message !== mockErrorMessage || error.cause !== qrChunkControl.error) {
      console.error(error, info);
    }
  });
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
    await waitFor(() => expect(statusReasons(document.body)).toEqual(["unavailable"]));
    expect(emptyStateReasons(document.body)).toEqual([]);
    expect(screen.getByRole("button", { name: "Copy pairing link" })).toBeTruthy();
    expect(screen.getByRole("region", { name: "Scan with your phone" })).toBeTruthy();
    expect(screen.queryByRole("img", { name: "Mobile app pairing QR code" })).toBeNull();
    expect(onCaughtError).toHaveBeenCalledTimes(1);
    const caughtError = onCaughtError.mock.calls[0]?.[0];
    expect(isError(caughtError)).toBe(true);
    expect(caughtError).toHaveProperty("message", mockErrorMessage);
    expect((caughtError as Error).cause).toBe(qrChunkControl.error);
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
  expect(screen.getByTestId("mobile-http-warning")).toBeTruthy();
  expect(screen.getByTestId("mobile-reuse-warning")).toBeTruthy();
  expect(client.calls).toEqual([{ method: "evener/mobile/pairing", params: { origin: window.location.origin } }]);
});

test("renders the reusable-capability warning for HTTPS without the HTTP observation warning", async () => {
  const authURL = "https://hub.example.test/auth/mobile-secret";
  client.on("evener/mobile/pairing", () => ({ authUrl: authURL }));

  renderMobileSection();

  expect(await screen.findByRole("img", { name: "Mobile app pairing QR code" })).toBeTruthy();
  expect(screen.getByTestId("mobile-reuse-warning")).toBeTruthy();
  expect(screen.queryByTestId("mobile-http-warning")).toBeNull();
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

test("does not offer a direct credential QR when no destination is available", async () => {
  client.on("evener/mobile/pairing", () => {
    throw new WireError("mobile pairing requires a reachable non-loopback Hub origin", -32013, {
      evenerErrorInfo: "conflict",
    });
  });

  renderMobileSection();

  await waitFor(() => expect(emptyStateReasons(document.body)).toEqual(["needs-configuration"]));
  expect(screen.queryByRole("img", { name: "Mobile app pairing QR code" })).toBeNull();
});
