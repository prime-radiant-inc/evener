import { cleanup, render, screen } from "@testing-library/react";
import { Suspense } from "react";
import { afterEach, beforeAll, expect, test } from "vitest";
import { paneFor } from "../../shell/paneRegistry";

// Await the module ONCE up front so React.lazy resolves from a warm module
// cache (mirrors App.test.tsx's own beforeAll pattern for the same reason:
// the slow part of lazy-loading is the transform/import work, an awaitable
// completion, not something to race with a widened findBy deadline).
beforeAll(async () => {
  await import("./index");
});

afterEach(cleanup);

test('registers "welcome" as a singleton pane with a constant title', () => {
  const descriptor = paneFor("welcome");
  expect(descriptor.id).toBe("welcome");
  expect(descriptor.singleton).toBe(true);
  expect(descriptor.title({}, {})).toBe("Welcome");
});

test("the registered component renders the welcome pane", async () => {
  const descriptor = paneFor("welcome");
  const WelcomeComponent = descriptor.component;
  render(
    <Suspense fallback={null}>
      <WelcomeComponent params={{}} paneId="welcome-1" focused={true} />
    </Suspense>,
  );
  expect(await screen.findByText("No session open")).toBeTruthy();
});
