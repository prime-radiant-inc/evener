import type { ThreadModel } from "@evener/appwire-client";
import { expect, test } from "vitest";
import { isAttentionWorthy } from "./scrollPredicates";

function model(status: string, askPending = false): ThreadModel {
  return {
    status: { type: status },
    askPending,
    pendingEscalations: [],
    turns: [],
  } as unknown as ThreadModel;
}

test("a finished awaiting turn is not attention-worthy", () => {
  expect(isAttentionWorthy(model("awaiting"))).toBe(false);
});

test("an awaiting turn with a pending question is attention-worthy", () => {
  expect(isAttentionWorthy(model("awaiting", true))).toBe(true);
});

test("warnings and restart needs remain attention-worthy", () => {
  expect(isAttentionWorthy(model("warning"))).toBe(true);
  expect(isAttentionWorthy(model("restartRequired"))).toBe(true);
});
