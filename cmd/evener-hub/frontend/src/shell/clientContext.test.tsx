import { act, cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { FakeClient } from "../protocol/testing/fakeClient";
import { connectionStore } from "../stores/connection";
import { ClientProvider, useClient } from "./clientContext";

afterEach(() => {
  cleanup();
  connectionStore.setState({ client: null, state: "idle", serverInfo: undefined, features: undefined });
});

function Consumer() {
  const client = useClient();
  return <p>client state: {client.state}</p>;
}

test("useClient reads the client provided by the nearest ClientProvider", () => {
  const fake = new FakeClient("ready");

  render(
    <ClientProvider client={fake}>
      <Consumer />
    </ClientProvider>,
  );

  expect(screen.getByText("client state: ready")).toBeTruthy();
});

test("useClient throws a clear error when rendered outside a ClientProvider", () => {
  // Swallow the expected React error-boundary console.error noise from this
  // deliberate throw-during-render - matches how failing render assertions
  // are proven elsewhere in this codebase without leaving console output
  // for a genuinely unexpected error to hide behind.
  const spy = vi.spyOn(console, "error").mockImplementation(() => {});
  try {
    expect(() => render(<Consumer />)).toThrow(/useClient/);
  } finally {
    spy.mockRestore();
  }
});

function Identity() {
  const client = useClient();
  return <p>client identity: {(client as { identity?: string }).identity ?? client.state}</p>;
}

test("useClient follows the connection-store client after a retry swap", () => {
  const original = new FakeClient("ready");
  const fresh = new FakeClient("connecting");
  (fresh as { identity?: string }).identity = "fresh-client";

  render(
    <ClientProvider client={original}>
      <Identity />
    </ClientProvider>,
  );
  expect(screen.getByText("client identity: ready")).toBeTruthy();

  act(() => {
    connectionStore.getState().connect(fresh);
  });
  expect(screen.getByText("client identity: fresh-client")).toBeTruthy();
});
