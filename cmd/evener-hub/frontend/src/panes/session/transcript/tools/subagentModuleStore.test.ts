import { cleanup, render, screen, within } from "@testing-library/react";
import { createElement, Fragment } from "react";
import { afterEach, expect, test } from "vitest";
import type { ItemModel, ThreadModel, TurnModel } from "../../../../protocol/model";
import type { EvenerDelegateInfo } from "../../../../protocol/types.gen";
import { resetThreadsStoreForTests, threadsStore } from "../../../../stores/threads";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { ToolCallItem } from "../ToolCallItem";
import { resetSubagentModuleStoreForTests } from "./subagentModuleStore";
import "./subagentModule";

afterEach(() => {
  cleanup();
  resetThreadsStoreForTests();
  resetSubagentModuleStoreForTests();
  resetDisclosureStoreForTests();
});

test("delegate cards with the same ids read stable status only from their own session", () => {
  const done = {
    delegateId: "dlg_shared",
    status: "idle",
    outcome: "completed",
    terminal: true,
    projectionRevision: 1,
  } as EvenerDelegateInfo;
  const failed = {
    delegateId: "dlg_shared",
    status: "idle",
    outcome: "failed",
    terminal: true,
    projectionRevision: 1,
  } as EvenerDelegateInfo;
  threadsStore.setState((state) => ({
    threads: new Map(state.threads)
      .set("session_a", { delegates: [done] } as ThreadModel)
      .set("session_b", { delegates: [failed] } as ThreadModel),
  }));

  const turn: TurnModel = { id: "turn_shared", status: "completed", items: [] };
  const delegate: ItemModel = {
    id: "item_a",
    turnId: turn.id,
    type: "commandExecution",
    toolName: "delegate",
    callId: "call_a",
    text: "",
    description: "Shared delegate",
    argumentsJSON: JSON.stringify({ prompt: "shared task" }),
    output: JSON.stringify({ delegate_id: "dlg_shared", status: "running" }),
  };
  render(
    createElement(
      Fragment,
      null,
      createElement(ToolCallItem, { item: delegate, turn, live: false, sessionRef: "session_a" }),
      createElement(ToolCallItem, {
        item: { ...delegate, id: "item_b", callId: "call_b" },
        turn,
        live: false,
        sessionRef: "session_b",
      }),
    ),
  );

  const [cardA, cardB] = screen.getAllByTestId("subagent-row");
  expect(within(cardA!).getByText("Status: done")).toBeTruthy();
  expect(within(cardB!).getByText("Status: failed")).toBeTruthy();
});
