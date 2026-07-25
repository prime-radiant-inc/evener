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

// One case per keyword, spelled out rather than ranged over RECONNECT_KEYWORDS:
// a table derived from the implementation would keep passing if a keyword were
// deleted from it. Go carries the same vocabulary and the same shape of table
// (agent/diagnostic), and TestHubFailureKeywordsMatchWebClient in cmd/serf-hub
// fails if the two lists stop agreeing.
test.each([
  "resume timed out waiting for rendezvous",
  "daemon spawn failed",
  "process exited before rendezvous",
  "appwire connection dropped",
  "websocket: close 1006 (abnormal closure)",
  "stream failed to connect",
  "source not found: ref_a",
  "local daemon unavailable: dial tcp 127.0.0.1:9180: connect: connection refused",
  "session unavailable",
])("a reconnect-class message with no hub source still classifies as connection: %s", (message) => {
  const info = classifyTurnError(err({ message }));
  expect(info.connection).toBe(true);
  expect(info.badge).toBe("connection");
  expect(info.recoveryLabel).toBe("Reconnect & retry");
});

test("an unrelated failure is not dragged into the reconnect class", () => {
  expect(classifyTurnError(err({ message: "the tool wrote no output" })).connection).toBe(false);
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
