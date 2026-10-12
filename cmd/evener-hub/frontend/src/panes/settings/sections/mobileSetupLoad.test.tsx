import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { connectionStore } from "../../../stores/connection";
import { emptyStateReasons } from "../../../widgets/emptystate/emptyStateTestUtils";
import { MobileSection } from "./mobile";
import { alertReasons } from "./reasonTestUtils";

const setupChunk = vi.hoisted(() => {
  let reject: (error: Error) => void = () => {};
  const promise = new Promise<never>((_resolve, rejectLoad) => {
    reject = rejectLoad;
  });
  return { promise, reject };
});
vi.mock("./devicePairing/DeviceSetup", () => setupChunk.promise);

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

test("keeps the pairing link accessible while setup loads and after a load failure", async () => {
  const authURL = "http://hub.example.test:9180/auth/mobile-secret";
  const client = new FakeClient("ready");
  client.on("evener/mobile/pairing", () => ({ authUrl: authURL }));
  connectionStore.getState().connect(client);
  const writeText = vi.fn().mockResolvedValue(undefined);
  vi.stubGlobal("navigator", { clipboard: { writeText } });
  render(
    <ClientProvider client={client}>
      <MobileSection />
    </ClientProvider>,
    { onCaughtError: vi.fn() },
  );
  const failure = new Error("Simulated setup chunk load failure");
  try {
    await screen.findByRole("button", { name: "Copy pairing link" });
    fireEvent.click(screen.getByRole("button", { name: "Pair a device" }));
    fireEvent.click(await screen.findByRole("button", { name: "Close setup" }));
    fireEvent.click(await screen.findByRole("button", { name: "Copy pairing link" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(authURL));

    writeText.mockClear();
    fireEvent.click(screen.getByRole("button", { name: "Pair a device" }));
    await act(async () => setupChunk.reject(failure));
    await waitFor(() => expect(alertReasons(document.body)).toEqual(["unavailable"]));
    expect(emptyStateReasons(document.body)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Close setup" }));
    fireEvent.click(await screen.findByRole("button", { name: "Copy pairing link" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(authURL));
  } finally {
    await act(async () => setupChunk.reject(failure));
  }
});
