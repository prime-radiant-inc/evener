import { afterEach, beforeAll, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { App } from "./App";

// Await the gallery module ONCE up front so React.lazy resolves from a warm
// module cache. The slow part of lazy-loading in a full parallel vitest run
// is the transform/import work, which is an awaitable completion — not
// something to race with a widened findBy deadline. A genuinely broken
// gallery module fails this await with its real error instead of a timeout.
beforeAll(async () => {
  await import("./dev/WidgetGallery");
});

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
  // Module pre-awaited in beforeAll, so this only waits out React's own
  // lazy/Suspense commit cycle — deterministic within findBy's default.
  expect(await screen.findByText(/widget gallery/i)).toBeTruthy();
});
