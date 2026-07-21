import { beforeAll, test, expect } from "vitest";
import { itemRendererFor } from "../types";

// Mirrors panes/session/index.test.tsx's own beforeAll pattern: await the
// side-effect module ONCE so every registerItemRenderer call inside it has
// definitely run before any assertion below.
beforeAll(async () => {
  await import("./index");
});

test("importing this module registers every message item type in this barrel", () => {
  const AgentMessageItem = itemRendererFor("agentMessage");
  const UserMessageItem = itemRendererFor("userMessage");
  const SteeringItem = itemRendererFor("steering");
  const SystemNoticeItem = itemRendererFor("systemMessage");
  const ThinkBlock = itemRendererFor("reasoning");
  const WarningItem = itemRendererFor("warning");

  // Each resolves to something OTHER than the raw fallback - i.e. a real
  // registration happened for every one of these six types via this one
  // side-effect import, not by chance already being registered by some
  // other test file that happened to run first.
  const RawFallback = itemRendererFor("index-test-truly-unregistered-type");
  for (const renderer of [AgentMessageItem, UserMessageItem, SteeringItem, SystemNoticeItem, ThinkBlock, WarningItem]) {
    expect(renderer).not.toBe(RawFallback);
  }

  // And distinct from one another - six real, different components, not
  // one accidentally shadowing the rest (also proves "warning" is
  // registered exactly once: if some other registration had clobbered it,
  // it would collide with one of the others here instead of being unique).
  const unique = new Set([AgentMessageItem, UserMessageItem, SteeringItem, SystemNoticeItem, ThinkBlock, WarningItem]);
  expect(unique.size).toBe(6);
});

test("re-exports TurnSeparator for the controller to wire into TurnBlock at merge", async () => {
  const mod = await import("./index");
  expect(typeof mod.TurnSeparator).toBe("function");
});
