import type { ThreadModel } from "@evener/appwire-client";
import { expect, test } from "vitest";
import { isAttentionWorthy, verticalInputCanMovePort } from "./scrollPredicates";

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

test("a pending question is attention-worthy independently of the status", () => {
  expect(isAttentionWorthy(model("idle", true))).toBe(true);
});

test("warnings and restart needs remain attention-worthy", () => {
  expect(isAttentionWorthy(model("warning"))).toBe(true);
  expect(isAttentionWorthy(model("restartRequired"))).toBe(true);
});

test("a popout's nested scroller consumes its own wheel before the transcript", () => {
  const frame = document.createElement("iframe");
  document.body.append(frame);
  try {
    const popdoc = frame.contentDocument;
    if (!popdoc) throw new Error("missing popout document");
    const port = popdoc.createElement("div");
    const inner = popdoc.createElement("div");
    inner.style.overflowY = "auto";
    Object.defineProperty(inner, "scrollHeight", { value: 300 });
    Object.defineProperty(inner, "clientHeight", { value: 100 });
    port.append(inner);
    popdoc.body.append(port);
    expect(verticalInputCanMovePort(port, { scrollTop: 100, scrollHeight: 500, clientHeight: 100 }, inner, 1)).toBe(
      false,
    );
  } finally {
    frame.remove();
  }
});
