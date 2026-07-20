import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { requireClass } from "../internal/requireClass";
import { Card } from "./index";
import rawStyles from "./card.module.css";

afterEach(cleanup);

const styles = {
  card: requireClass(rawStyles.card, "card.module.css", "card"),
};

test("renders its children", () => {
  render(
    <Card>
      <p>Hello</p>
    </Card>,
  );
  expect(screen.getByText("Hello")).toBeTruthy();
});

test("wraps children in a single container carrying the card class", () => {
  render(
    <Card>
      <span data-testid="child" />
    </Card>,
  );
  const child = screen.getByTestId("child");
  expect(child.parentElement?.classList.contains(styles.card)).toBe(true);
});
