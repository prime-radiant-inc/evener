import { render } from "@testing-library/react";
import { expect, test } from "vitest";
import { accessibleName } from "./accessibleNameTestUtils";

function summaryIn(markup: React.ReactNode): HTMLElement {
  const { container } = render(<details>{markup}</details>);
  const summary = container.querySelector("summary");
  if (!summary) throw new Error("no summary rendered");
  return summary;
}

test("a summary named only by its visible content reads that content", () => {
  expect(accessibleName(summaryIn(<summary>Show more</summary>))).toBe("Show more");
});

test("a summary whose whole label is hidden has no name", () => {
  expect(
    accessibleName(
      summaryIn(
        <summary>
          <span hidden>Show more</span>
        </summary>,
      ),
    ),
  ).toBe("");
});

test("a summary whose whole label is display: none has no name", () => {
  expect(
    accessibleName(
      summaryIn(
        <summary>
          <span style={{ display: "none" }}>Show more</span>
        </summary>,
      ),
    ),
  ).toBe("");
});

test("a summary whose whole label is aria-hidden has no name", () => {
  expect(
    accessibleName(
      summaryIn(
        <summary>
          <span aria-hidden="true">Show more</span>
        </summary>,
      ),
    ),
  ).toBe("");
});

test("an aria-label override replaces the content", () => {
  expect(accessibleName(summaryIn(<summary aria-label="Override">Show more</summary>))).toBe("Override");
});
