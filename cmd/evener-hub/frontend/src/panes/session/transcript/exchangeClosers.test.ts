import { expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { exchangeClosersFor } from "./exchangeClosers";

function item(id: string, type: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return { id, type, text: id, status: "completed", ...overrides } as ItemModel;
}

function turn(id: string, items: ItemModel[]): TurnModel {
  return { id, items, status: "completed" } as TurnModel;
}

test("last text-bearing agent message before the next user message closes the exchange", () => {
  const closers = exchangeClosersFor([
    turn("t1", [item("u1", "userMessage"), item("a1", "agentMessage"), item("a2", "agentMessage")]),
    turn("t2", [item("u2", "userMessage")]),
  ]);
  expect([...closers]).toEqual(["a2"]);
});

test("trailing exchange closes at the transcript end", () => {
  const closers = exchangeClosersFor([
    turn("t1", [item("u1", "userMessage"), item("a1", "agentMessage"), item("a2", "agentMessage")]),
  ]);
  expect([...closers]).toEqual(["a2"]);
});

test("an empty finalize (no text) never closes", () => {
  const closers = exchangeClosersFor([
    turn("t1", [item("u1", "userMessage"), item("a1", "agentMessage"), item("a2", "agentMessage", { text: "" })]),
    turn("t2", [item("u2", "userMessage")]),
  ]);
  expect([...closers]).toEqual(["a1"]);
});

test("tools-last exchange: the closer is still the last prose, not the tool call", () => {
  const closers = exchangeClosersFor([
    turn("t1", [item("u1", "userMessage"), item("a1", "agentMessage"), item("c1", "commandExecution")]),
    turn("t2", [item("u2", "userMessage")]),
  ]);
  expect([...closers]).toEqual(["a1"]);
});

test("exchange with no agent prose at all closes nothing", () => {
  const closers = exchangeClosersFor([
    turn("t1", [item("u1", "userMessage"), item("c1", "commandExecution")]),
    turn("t2", [item("u2", "userMessage")]),
  ]);
  expect(closers.size).toBe(0);
});

test("queued user messages: the closer is the last prose before the next user message", () => {
  const closers = exchangeClosersFor([
    turn("t1", [item("u1", "userMessage")]),
    turn("t2", [item("u2", "userMessage"), item("a1", "agentMessage"), item("a2", "agentMessage")]),
    turn("t3", [item("u3", "userMessage")]),
  ]);
  expect([...closers]).toEqual(["a2"]);
});

test("agent messages before any user message close nothing", () => {
  const closers = exchangeClosersFor([turn("t1", [item("a1", "agentMessage")])]);
  expect(closers.size).toBe(0);
});

test("each exchange gets its own closer, across turns", () => {
  const closers = exchangeClosersFor([
    turn("t1", [item("u1", "userMessage"), item("a1", "agentMessage")]),
    turn("t2", [item("a2", "agentMessage")]),
    turn("t3", [item("u2", "userMessage")]),
    turn("t4", [item("a3", "agentMessage"), item("a4", "agentMessage")]),
  ]);
  expect([...closers]).toEqual(["a2", "a4"]);
});

test("a failed turn's prose never closes: the end cap already closes the turn", () => {
  const failed = turn("t1", [item("u1", "userMessage"), item("a1", "agentMessage")]);
  (failed as { error?: unknown }).error = { code: "boom" };
  const closers = exchangeClosersFor([failed, turn("t2", [item("u2", "userMessage")])]);
  expect(closers.size).toBe(0);
});

test("completed prose followed by an in-progress tool call never closes: the exchange is still working", () => {
  const closers = exchangeClosersFor([
    turn("t1", [
      item("u1", "userMessage"),
      item("a1", "agentMessage"),
      item("c1", "commandExecution", { status: "inProgress" }),
    ]),
  ]);
  expect(closers.size).toBe(0);
});

test("once the trailing work settles, the last prose closes", () => {
  const closers = exchangeClosersFor([
    turn("t1", [
      item("u1", "userMessage"),
      item("a1", "agentMessage"),
      item("c1", "commandExecution", { status: "completed" }),
      item("a2", "agentMessage"),
    ]),
  ]);
  expect([...closers]).toEqual(["a2"]);
});

test("an in-progress item before the last prose still suppresses: the wash means concluded work", () => {
  const closers = exchangeClosersFor([
    turn("t1", [
      item("u1", "userMessage"),
      item("c1", "commandExecution", { status: "inProgress" }),
      item("a1", "agentMessage"),
    ]),
  ]);
  expect(closers.size).toBe(0);
});
