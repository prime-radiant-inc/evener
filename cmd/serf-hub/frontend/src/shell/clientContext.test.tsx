import { afterEach, test, expect, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { FakeClient } from "../protocol/testing/fakeClient";
import { ClientProvider, useClient } from "./clientContext";

afterEach(cleanup);

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
