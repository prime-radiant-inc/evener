import { expect, test } from "vitest";
import { initNotifications } from ".";

test("initNotifications is safe to call (the T1 no-op engine stub)", () => {
  expect(() => initNotifications()).not.toThrow();
});

test("initNotifications is idempotent (safe to call repeatedly)", () => {
  initNotifications();
  expect(() => initNotifications()).not.toThrow();
});
