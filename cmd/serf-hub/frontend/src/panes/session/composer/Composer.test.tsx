import { cleanup, render } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { Composer } from "./Composer";

afterEach(cleanup);

// Wave 5 T1 carves this slot as an empty placeholder; T2 (composer core),
// T3 (queue strip), and T4 (ask dock) fill in its real internals inside
// their own subtree without ever touching Session.tsx again. This test
// pins ONLY the T1 contract: mounting with a ref renders nothing visible.
test("renders nothing (T1 placeholder - streams fill this in)", () => {
  const { container } = render(<Composer ref="ref_a" />);
  expect(container.firstChild).toBeNull();
});
