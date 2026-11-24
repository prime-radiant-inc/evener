import { render, screen, within } from "@testing-library/react";
import { expect, test } from "vitest";
import { SectionGate } from "./sectionGate";

function Harness({ loading, error }: { loading: boolean; error: string | null }) {
  return <SectionGate loading={loading} error={error} onRetry={() => {}} />;
}

test("a load failure lands in the status region that was mounted before it", () => {
  const { rerender } = render(<Harness loading error={null} />);

  // The region exists, empty, before the failure arrives. The loading
  // indicator is its sibling, not part of the announcement.
  const region = screen.getByTestId("section-gate-status");
  expect(region.getAttribute("role")).toBe("status");
  expect(region.textContent).toBe("");
  expect(within(region).queryByRole("status")).toBeNull();
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();

  rerender(<Harness loading={false} error="fixture load failure" />);

  // Same node, now carrying the message: a region created with its content
  // already inside may never be announced.
  expect(screen.getByTestId("section-gate-status")).toBe(region);
  expect(region.isConnected).toBe(true);
  expect(region.textContent).toContain("fixture load failure");
  // The visible failure and its Retry stay outside the announcement region.
  expect(within(region).queryByRole("button")).toBeNull();
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
});
