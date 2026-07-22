import { cleanup, render, screen } from "@testing-library/react";
import { Suspense } from "react";
import { afterEach, beforeAll, expect, test } from "vitest";
import { paneFor } from "../../shell/paneRegistry";

// Warm BOTH modules up front: ./index runs registerPane() (so paneFor works),
// and ./Session is the React.lazy() chunk the registered component actually
// import()s on first render - awaiting ./index alone leaves that chunk cold,
// so the render below then raced findBy's deadline against Session.tsx's
// (large) first transform+import and timed out under load. Awaiting the real
// lazy chunk here resolves React.lazy from a warm module cache instead
// (mirrors AppShell.test.tsx, which awaits ./Session for exactly this reason:
// the slow part of lazy-loading is the transform/import work, an awaitable
// completion, not something to race with a widened findBy deadline).
beforeAll(async () => {
  await import("./index");
  await import("./Session");
});

afterEach(cleanup);

test('registers "session" as a non-singleton pane', () => {
  const descriptor = paneFor("session");
  expect(descriptor.id).toBe("session");
  expect(descriptor.singleton).toBeUndefined();
});

test("title() prefers ctx.threadName() over the raw ref", () => {
  const descriptor = paneFor("session");
  const title = descriptor.title({ ref: "ref_abc123" }, { threadName: () => "My session" });
  expect(title).toBe("My session");
});

test("title() falls back to the raw ref when threadName() returns undefined", () => {
  const descriptor = paneFor("session");
  const title = descriptor.title({ ref: "ref_abc123" }, { threadName: () => undefined });
  expect(title).toBe("ref_abc123");
});

test("title() falls back to the raw ref when ctx has no threadName at all", () => {
  const descriptor = paneFor("session");
  const title = descriptor.title({ ref: "ref_abc123" }, {});
  expect(title).toBe("ref_abc123");
});

test("the registered component renders the ref it was opened with", async () => {
  const descriptor = paneFor("session");
  const SessionComponent = descriptor.component;
  render(
    <Suspense fallback={null}>
      <SessionComponent params={{ ref: "ref_abc123" }} paneId="session-1" focused={true} />
    </Suspense>,
  );
  expect(await screen.findByText("ref_abc123")).toBeTruthy();
});
