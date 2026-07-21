import { cleanup, render, screen } from "@testing-library/react";
import { Suspense } from "react";
import { afterEach, beforeAll, expect, test } from "vitest";
import { paneFor } from "../../shell/paneRegistry";

// Await the module ONCE up front so React.lazy resolves from a warm module
// cache (mirrors panes/welcome/index.test.tsx's own beforeAll pattern for
// the same reason: the slow part of lazy-loading is the transform/import
// work, an awaitable completion, not something to race with a widened
// findBy deadline).
beforeAll(async () => {
  await import("./index");
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
