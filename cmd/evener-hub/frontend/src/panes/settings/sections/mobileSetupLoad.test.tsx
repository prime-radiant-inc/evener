import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../../shell/clientContext";
import { connectionStore } from "../../../stores/connection";
import { MobileSection } from "./mobile";

vi.mock("./devicePairing/DeviceSetup", () => {
  throw new Error("Simulated setup chunk load failure");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
});

test("returns to the working pairing link when device setup cannot load", async () => {
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
  await screen.findByRole("button", { name: "Copy pairing link" });
  fireEvent.click(screen.getByRole("button", { name: "Pair a device" }));
  fireEvent.click(await screen.findByRole("button", { name: "Back to pairing" }));
  fireEvent.click(await screen.findByRole("button", { name: "Copy pairing link" }));
  await waitFor(() => expect(writeText).toHaveBeenCalledWith(authURL));
});
