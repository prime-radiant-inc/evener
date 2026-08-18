import { cleanup, render, screen } from "@testing-library/react";
import { Suspense } from "react";
import { afterEach, beforeAll, expect, test } from "vitest";
import { paneFor } from "../../shell/paneRegistry";

// Warm BOTH modules up front: ./index runs registerPane() (so paneFor works),
// and ./Welcome is the React.lazy() chunk the registered component actually
// import()s on first render - awaiting ./index alone leaves that chunk cold,
// so the render below would race findBy's deadline against Welcome.tsx's first
// transform+import and can time out under load. Awaiting the real lazy chunk
// here resolves React.lazy from a warm module cache instead (mirrors
// AppShell.test.tsx, which awaits the lazy pane chunks for exactly this
// reason: the slow part of lazy-loading is the transform/import work, an
// awaitable completion, not something to race with a widened findBy deadline).
// A warm module cache is only half of it, though: React.lazy keeps a payload
// of its own that stays uninitialized until React first RENDERS the
// component, so the first render still suspends, still commits its Suspense
// fallback (a null fallback counts), and then waits out react-dom's
// FALLBACK_THROTTLE_MS (300ms, react-dom 19.2) before it will commit the
// revealed content - a flicker guard that is pure wall clock and does not
// shrink on a fast machine. Measured: the render test below cost 317ms of it
// against a findBy budget that defaults to 1000ms. So render the component
// once here too, in a hook whose ceiling is a tripwire rather than an
// assertion window (same fix as App.test.tsx, commit c1a8616ea).
beforeAll(async () => {
  await import("./index");
  await import("./Welcome");

  const WelcomeComponent = paneFor("welcome").component;
  render(
    <Suspense fallback={null}>
      <WelcomeComponent params={{}} paneId="welcome-warm" focused={true} />
    </Suspense>,
  );
  await screen.findByText("No session open");
  cleanup();
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
