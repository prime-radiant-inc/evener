import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NotFound } from "./NotFound";

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
});

test('shows "Page not found"', () => {
  render(<NotFound />);
  expect(screen.getByText("Page not found")).toBeTruthy();
});

test('offers a "Go home" action', () => {
  render(<NotFound />);
  expect(screen.getByRole("button", { name: "Go home" })).toBeTruthy();
});

test('clicking "Go home" navigates to /', async () => {
  window.history.pushState({}, "", "/not/a/real/route");
  const user = userEvent.setup();
  render(<NotFound />);
  await user.click(screen.getByRole("button", { name: "Go home" }));
  expect(window.location.pathname).toBe("/");
});
