import type { AnyNotification, ThreadReadResponse } from "../types.gen";
import { wireThread } from "./notifications";

export function currentRead(ref: string): ThreadReadResponse {
  return {
    thread: wireThread(ref),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 40 },
    requestGeneration: 1,
    olderCursor: "older-40",
    overlay: [
      {
        key: "stream:s1:agentMessage",
        kind: "stream",
        streamId: "s1",
        roundId: "r1",
        turnId: "turn_1",
        item: { id: "a1", turnId: "turn_1", type: "agentMessage", text: "alpha" },
      },
    ],
  };
}

export function textDelta(ref: string, delta: string): AnyNotification {
  return {
    method: "overlay/delta",
    params: {
      ref,
      threadId: `thr_${ref}`,
      key: "stream:s1:agentMessage",
      field: "text",
      delta,
    },
  };
}
