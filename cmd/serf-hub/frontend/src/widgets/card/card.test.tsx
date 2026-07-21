import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { requireClass } from "../internal/requireClass";
import rawStyles from "./card.module.css";
import { Card } from "./index";

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
