import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test } from "vitest";
import { NotFound } from "./NotFound";

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
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
