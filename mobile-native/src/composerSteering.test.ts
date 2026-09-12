import { describe, expect, it } from "vitest";
import type {
  InputItem,
  Thread,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
  type ConversationClientLike,
  createConversationService,
} from "../../mobile/src/services/conversation";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { steerComposer } from "./composerSteering";

describe("native composer steering wire routing", () => {
  it.each([
    {
      name: "an empty composer with waiting messages",
      depth: 2,
      input: [],
      method: "turn/drainAsSteer",
    },
    {
      name: "text with an empty queue",
      depth: 0,
      input: [{ type: "text", text: "direction" }],
      method: "turn/steer",
    },
    {
      name: "images with an empty queue",
      depth: 0,
      input: [
        {
          type: "image",
          mediaType: "image/png",
          data: "fixture",
          name: "test.png",
        },
      ],
      method: "turn/drainAsSteer",
    },
    {
      name: "text with waiting messages",
      depth: 2,
      input: [{ type: "text", text: "direction" }],
      method: "turn/drainAsSteer",
    },
    {
      name: "rejected image steering",
      depth: 2,
      input: [{ type: "image", mediaType: "image/png", data: "fixture" }],
      method: "turn/drainAsSteer",
      reject: true,
    },
  ])(
    "routes $name with the observed queue guard",
    async ({ depth, input, method, reject }) => {
      const thread: Thread = {
        id: "thread",
        sessionId: "session",
        preview: "",
        ephemeral: false,
        modelProvider: "scripted",
        createdAt: 1,
        updatedAt: 1,
        status: { type: "active" },
        cwd: "/test",
        cliVersion: "test",
        source: "local",
        turns: [],
        evener: {
          ref: "local:test",
          instanceId: "instance",
          queue: { revision: 7, depth, preview: [] },
          capabilities: {
            send: false,
            steer: true,
            interrupt: true,
            queue: true,
            compact: false,
            clear: false,
            forkFromTurn: false,
            shutdown: false,
            changeModel: false,
            changeVisionModel: false,
            goal: false,
            rename: false,
          },
        },
      };
      const calls: { method: string; params: unknown }[] = [];
      const wire = {
        async request(method: string, params: { clientMutationId: string }) {
          if (method === "thread/read") return { thread };
          calls.push({ method, params });
          if (reject)
            throw new Error("Connection closed before acknowledgement");
          return {
            receipt: {
              clientMutationId: params.clientMutationId,
              disposition: "applied",
              threadId: "thread",
              turnId: "turn",
              projectionState: "pending",
              instanceId: "instance",
              ...(method === "turn/drainAsSteer" && depth > 0
                ? { queueEntryIds: ["queue_1", "queue_2"] }
                : {}),
            },
          };
        },
        onNotification: () => () => {},
      } as ConversationClientLike;
      const service = createConversationService(wire);
      const store = createConversationStore();
      try {
        await store.getState().open(service, "local:test");
        store.getState().setDraft("direction");
        await steerComposer(store, service, input as InputItem[]);
        expect(calls).toHaveLength(1);
        expect(calls[0]).toMatchObject({
          method,
          params: { ref: "local:test", expectedInstanceId: "instance", input },
        });
        if (method === "turn/drainAsSteer")
          expect(calls[0]?.params).toHaveProperty("expectedQueueRevision", 7);
        else
          expect(calls[0]?.params).not.toHaveProperty("expectedQueueRevision");
        expect(store.getState().lastAcceptedMutation?.kind).toBe(
          reject ? undefined : "steer",
        );
        expect(store.getState().draft).toBe(reject ? "direction" : "");
        if (!reject && method === "turn/drainAsSteer" && depth > 0)
          expect(
            store.getState().lastAcceptedMutation?.receipt.queueEntryIds,
          ).toEqual(["queue_1", "queue_2"]);
      } finally {
        store.getState().close();
        service.close();
      }
    },
  );
});
