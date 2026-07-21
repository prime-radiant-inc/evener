import { beforeAll, test, expect } from "vitest";
import { itemRendererFor } from "../types";

// Mirrors panes/session/index.test.tsx's own beforeAll pattern: await the
// side-effect module ONCE so every registerItemRenderer call inside it has
// definitely run before any assertion below.
beforeAll(async () => {
  await import("./index");
});

test("importing this module registers every wave-4 T2 message item type", () => {
  const AgentMessageItem = itemRendererFor("agentMessage");
  const UserMessageItem = itemRendererFor("userMessage");
  const SteeringItem = itemRendererFor("steering");
  const SystemNoticeItem = itemRendererFor("systemMessage");
  const ThinkBlock = itemRendererFor("reasoning");

  // Each resolves to something OTHER than the raw fallback - i.e. a real
  // registration happened for every one of these five types via this one
  // side-effect import, not by chance already being registered by some
  // other test file that happened to run first.
  const RawFallback = itemRendererFor("index-test-truly-unregistered-type");
  for (const renderer of [AgentMessageItem, UserMessageItem, SteeringItem, SystemNoticeItem, ThinkBlock]) {
    expect(renderer).not.toBe(RawFallback);
  }

  // And distinct from one another - five real, different components, not
  // one accidentally shadowing the rest.
  const unique = new Set([AgentMessageItem, UserMessageItem, SteeringItem, SystemNoticeItem, ThinkBlock]);
  expect(unique.size).toBe(5);
});

test("re-exports TurnSeparator for the controller to wire into TurnBlock at merge", async () => {
  const mod = await import("./index");
  expect(typeof mod.TurnSeparator).toBe("function");
});
