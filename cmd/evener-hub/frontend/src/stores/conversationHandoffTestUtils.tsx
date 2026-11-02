import type * as appwire from "@evener/appwire-client";
import { wireThread } from "@evener/appwire-client/testing/notifications";
import { projectComicConversation } from "../comic/projection";
import { backgroundHolds, foregroundHolds } from "./threadOwners";
import { threadsStore } from "./threads";

export const ref = "local:claim-handoff";
export const sourceItems = [
  { id: "source-a", text: "CLAIM_BODY_A" },
  { id: "source-b", text: "CLAIM_BODY_B" },
];
export const sourceIds = [...sourceItems.map((item) => item.id), "turn:itemless-error:error"];

export function response(requestedRef = ref): appwire.ThreadReadResponse {
  return {
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-claim", length: 3 },
    thread: wireThread(requestedRef, {
      turns: [
        ...sourceItems.map((item, index) => ({
          id: `turn-${item.id}`,
          status: "completed" as const,
          itemsView: "full" as const,
          version: 1,
          items: [
            {
              ...item,
              type: "agentMessage",
              transcriptKey: item.id,
              turnId: `turn-${item.id}`,
              version: 1,
              position: { entry: index + 1, item: 0 },
            },
          ],
        })),
        {
          id: "itemless-error",
          status: "failed",
          itemsView: "full",
          version: 1,
          items: [],
          error: { message: "CLAIM_ERROR" },
        },
      ],
    }),
  };
}

export function snapshot(model = threadsStore.getState().threads.get(ref)) {
  return {
    foreground: foregroundHolds(ref),
    background: backgroundHolds(ref),
    items: model?.turns.flatMap((turn) => turn.items.map((item) => item.id)) ?? [],
    sources: model ? projectComicConversation(model).map((entry) => entry.id) : [],
  };
}
