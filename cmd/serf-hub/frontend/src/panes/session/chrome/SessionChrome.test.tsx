import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { SessionChrome } from "./SessionChrome";

afterEach(cleanup);

// Wave 5 T1 carves this slot as an empty placeholder; T5 (session chrome:
// status row, model switch, session actions, goal, tasks panel) fills in
// its real internals without ever touching Session.tsx again. This test
// pins ONLY the T1 contract: mounting with a ref renders nothing visible.
test("renders nothing (T1 placeholder - T5 fills this in)", () => {
  const { container } = render(<SessionChrome ref="ref_a" />);
  expect(container.firstChild).toBeNull();
});
