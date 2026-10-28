import { AppwireClient } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { connectionStore } from "../stores/connection";
import { useBrowserConnection } from "./useBrowserConnection";

const disposals: Array<() => void> = [];
afterEach(() => {
  cleanup();
  for (const dispose of disposals.splice(0)) dispose();
  connectionStore.setState({ client: null, state: "idle", serverInfo: undefined });
  vi.restoreAllMocks();
});

test("connects one injected client and releases its background owner without closing the client", async () => {
  const client = new FakeClient("ready");
  const connect = vi.spyOn(client, "connect");
  const close = vi.spyOn(client, "close");
  const dispose = vi.fn(async () => {});
  function Probe() {
    const connection = useBrowserConnection({ client, startBackground: () => ({ dispose }) });
    return <output data-testid="provided-client">{connection.client === client ? "current" : "stale"}</output>;
  }
  const view = render(<Probe />);
  expect(view.getByTestId("provided-client").textContent).toBe("current");
  expect(connectionStore.getState().client).toBe(client);
  // Navigation may independently request the same FakeClient's cached handshake.
  expect(connect).toHaveBeenCalled();
  view.unmount();
  await vi.waitFor(() => expect(dispose).toHaveBeenCalledTimes(1));
  expect(close).not.toHaveBeenCalled();
});

test("adopts a replacement in store and hook result without closing injected clients", async () => {
  const stale = new FakeClient("ready");
  const fresh = new FakeClient("ready");
  const close = vi.spyOn(stale, "close");
  let replace: ((client: FakeClient) => void) | undefined;
  function Probe() {
    const connection = useBrowserConnection({ client: stale });
    replace = connection.onClientReplaced;
    return <output data-testid="provided-client">{connection.client === fresh ? "fresh" : "stale"}</output>;
  }
  const view = render(<Probe />);
  act(() => {
    connectionStore.getState().connect(fresh);
    replace?.(fresh);
  });
  expect(view.getByTestId("provided-client").textContent).toBe("fresh");
  expect(connectionStore.getState().client).toBe(fresh);
  view.unmount();
  expect(close).not.toHaveBeenCalled();
});

test("closes its own real client after its background disposal settles", async () => {
  const close = vi.spyOn(AppwireClient.prototype, "close");
  let release!: () => void;
  const disposal = new Promise<void>((resolve) => {
    release = resolve;
  });
  function Probe() {
    useBrowserConnection({ startBackground: () => ({ dispose: () => disposal }) });
    return null;
  }
  const view = render(<Probe />);
  expect(connectionStore.getState().client).toBeInstanceOf(AppwireClient);
  view.unmount();
  expect(close).not.toHaveBeenCalled();
  await act(async () => {
    release();
    await disposal;
  });
  await vi.waitFor(() => expect(close).toHaveBeenCalledTimes(1));
});
