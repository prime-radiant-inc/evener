import { expect, test } from "vitest";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { exchangeOpenersFor } from "./exchangeOpeners";

function item(id: string, type: string): ItemModel {
  return { id, type, text: id, status: "completed" } as ItemModel;
}

function turn(id: string, items: ItemModel[]): TurnModel {
  return { id, items, status: "completed" } as TurnModel;
}

test("first agent message after a user message opens the agent's exchange half", () => {
  const openers = exchangeOpenersFor([
    turn("t1", [item("u1", "userMessage"), item("a1", "agentMessage"), item("a2", "agentMessage")]),
  ]);
  expect([...openers]).toEqual(["a1"]);
});

test("queued user messages: the opener is still the first agent reply", () => {
  const openers = exchangeOpenersFor([
    turn("t1", [item("u1", "userMessage")]),
    turn("t2", [item("u2", "userMessage"), item("a1", "agentMessage")]),
  ]);
  expect([...openers]).toEqual(["a1"]);
});

test("agent messages before any user message open nothing", () => {
  const openers = exchangeOpenersFor([turn("t1", [item("a1", "agentMessage")])]);
  expect(openers.size).toBe(0);
});

test("each new exchange gets its own opener, across turns", () => {
  const openers = exchangeOpenersFor([
    turn("t1", [item("u1", "userMessage"), item("a1", "agentMessage")]),
    turn("t2", [item("a2", "agentMessage")]),
    turn("t3", [item("u2", "userMessage")]),
    turn("t4", [item("a3", "agentMessage"), item("a4", "agentMessage")]),
  ]);
  expect([...openers]).toEqual(["a1", "a3"]);
});
