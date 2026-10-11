import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import { Eyebrow } from "./index";

test("renders its label without becoming a heading by default", () => {
  render(<Eyebrow>Agent setup</Eyebrow>);
  expect(screen.getByText("Agent setup")).toBeTruthy();
  expect(screen.queryByRole("heading")).toBeNull();
});

test('as="h3" makes the label a level-3 heading', () => {
  render(<Eyebrow as="h3">Navigation</Eyebrow>);
  expect(screen.getByRole("heading", { level: 3, name: "Navigation" })).toBeTruthy();
});

test('as="span" keeps the label inline beside sibling text', () => {
  render(
    <p>
      <Eyebrow as="span">Session</Eyebrow> running
    </p>,
  );
  const label = screen.getByText("Session");
  expect(label.tagName).toBe("SPAN");
  expect(label.parentElement?.textContent).toBe("Session running");
});
