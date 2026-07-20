import { afterEach, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { App } from "./App";

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
});

test("renders the shell placeholder", () => {
  render(<App />);
  expect(screen.getByText(/workspace shell/i)).toBeTruthy();
});

test("renders the dev widget gallery at /dev/widgets", async () => {
  window.history.pushState({}, "", "/dev/widgets");
  render(<App />);
  expect(await screen.findByText(/widget gallery/i)).toBeTruthy();
});
