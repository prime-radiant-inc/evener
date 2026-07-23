import { expect, test } from "vitest";
import type { TurnError } from "../../../protocol/types.gen";
import { asTurnError, classifyTurnError } from "./turnFailure";

function err(overrides: Partial<TurnError> = {}): TurnError {
  return { message: "something broke", ...overrides };
}

test("asTurnError narrows a real error object and rejects everything else", () => {
  expect(asTurnError({ message: "boom" })?.message).toBe("boom");
  expect(asTurnError(undefined)).toBeUndefined();
  expect(asTurnError(null)).toBeUndefined();
  expect(asTurnError("boom")).toBeUndefined();
  expect(asTurnError({ title: "no message" })).toBeUndefined();
});

test("a provider cause becomes a provider badge carrying its HTTP status", () => {
  const info = classifyTurnError(err({ cause: { kind: "provider", provider: "openai", status: 429 } }));
  expect(info.badge).toBe("provider 429");
  expect(info.connection).toBe(false);
  expect(info.recoveryLabel).toBe("Retry");
});

test("a provider cause with no status is just 'provider'", () => {
  expect(classifyTurnError(err({ cause: { kind: "provider" } })).badge).toBe("provider");
});

test("a hub-source failure is a connection error with a reconnect recovery label", () => {
  const info = classifyTurnError(err({ source: "hub", message: "local daemon unavailable" }));
  expect(info.badge).toBe("connection");
  expect(info.connection).toBe(true);
  expect(info.recoveryLabel).toBe("Reconnect & retry");
});

test("a reconnect-class message (no hub source) still classifies as connection", () => {
  expect(classifyTurnError(err({ message: "resume timed out waiting for rendezvous" })).connection).toBe(true);
});

test("a plain source with no cause becomes that source's badge", () => {
  expect(classifyTurnError(err({ source: "agent" })).badge).toBe("agent");
});

test("a bare error with no source or cause falls back to the generic 'error' badge and Retry", () => {
  const info = classifyTurnError(err());
  expect(info.badge).toBe("error");
  expect(info.recoveryLabel).toBe("Retry");
});

test("the hint rides through when present, and an empty message falls back", () => {
  expect(classifyTurnError(err({ hint: "check your API key" })).hint).toBe("check your API key");
  expect(classifyTurnError(err({ message: "   " })).message).toBe("Session error");
});
