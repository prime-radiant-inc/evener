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
  // The gallery is React.lazy and its import.meta.glob pulls in every
  // gallery section; under a full parallel vitest run that dynamic import
  // can far exceed findBy's 1s default (passes in ~0.5s in isolation), so
  // give the lazy chunk a generous ceiling instead of a load-dependent one.
  expect(await screen.findByText(/widget gallery/i, undefined, { timeout: 15_000 })).toBeTruthy();
}, 20_000);
