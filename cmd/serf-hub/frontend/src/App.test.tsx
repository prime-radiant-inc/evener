import { afterEach, beforeAll, test, expect } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { App } from "./App";

// Await every lazily-loaded route's module ONCE up front so React.lazy
// resolves from a warm module cache. The slow part of lazy-loading in a
// full parallel vitest run is the transform/import work, which is an
// awaitable completion — not something to race with a widened findBy
// deadline. A genuinely broken module fails this await with its real error
// instead of a timeout.
beforeAll(async () => {
  await import("./dev/WidgetGallery");
  await import("./dev/DevHarness");
  await import("./panes/welcome/Welcome");
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
});

test("renders the app shell (welcome pane) at the default route", async () => {
  render(<App />);
  expect(await screen.findByText("No session open")).toBeTruthy();
  // DevHarness moved out of the default route onto its own /dev/harness
  // route (see the next test) - it must not also render here.
  expect(screen.queryByText(/connection:/i)).toBeNull();
});

test("renders the dev widget gallery at /dev/widgets", async () => {
  window.history.pushState({}, "", "/dev/widgets");
  render(<App />);
  // Module pre-awaited in beforeAll, so this only waits out React's own
  // lazy/Suspense commit cycle — deterministic within findBy's default.
  expect(await screen.findByText(/widget gallery/i)).toBeTruthy();
});

test("renders the dev harness at /dev/harness", async () => {
  window.history.pushState({}, "", "/dev/harness");
  render(<App />);
  // Module pre-awaited in beforeAll, so this only waits out React's own
  // lazy/Suspense commit cycle — deterministic within findBy's default.
  expect(await screen.findByText(/connection:/i)).toBeTruthy();
});
