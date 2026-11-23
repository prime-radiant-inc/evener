import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { Diagnostics } from "./diagnostics";

afterEach(cleanup);

test("plain string diagnostics render one item each, with no per-item reason", () => {
  render(<Diagnostics diagnostics={["first warning", "second warning"]} />);
  const items = within(screen.getByTestId("diagnostics")).getAllByRole("listitem");
  expect(items.map((item) => item.hasAttribute("data-reason"))).toEqual([false, false]);
});

test("{ message, reason } diagnostics keep each item's data-reason and the list count", () => {
  render(
    <Diagnostics
      diagnostics={[
        { message: "first warning", reason: "unknown-action" },
        { message: "second warning", reason: "reserved-chord" },
      ]}
    />,
  );
  const items = within(screen.getByTestId("diagnostics")).getAllByRole("listitem");
  expect(items.map((item) => item.getAttribute("data-reason"))).toEqual(["unknown-action", "reserved-chord"]);
});

test("renders nothing when there are no diagnostics", () => {
  render(<Diagnostics diagnostics={[]} />);
  expect(screen.queryByTestId("diagnostics")).toBeNull();
});
