import { describe, expect, it } from "vitest";
import WebSocket from "ws";
import type { WebSocketLike } from "../../cmd/evener-hub/frontend/src/protocol/transport";
import { createConversationService } from "../../mobile/src/services/conversation";
import { createConversationStore } from "../../mobile/src/state/conversation";
import { createActivityStore } from "../../mobile/src/state/activity";
import { createHubClient } from "./connection";
import { createDemoHub } from "../scripts/demo-hub.mjs";

describe("native demonstration hub", () => {
  it("drives the shared stores through notification-triggered active and idle projections", async () => {
    const hub = await createDemoHub(0);
    const client = createHubClient(
      hub.origin,
      "",
      (url) => new WebSocket(url) as unknown as WebSocketLike,
    );
    const service = createConversationService(client);
    const store = createConversationStore();
    const sink = createActivityStore().getState();
    const statusChanged = (status: string) =>
      new Promise<void>((resolve) => {
        const unsubscribe = store.subscribe((state) => {
          if (state.conversation?.status !== status) return;
          unsubscribe();
          resolve();
        });
      });
    try {
      await client.connect();
      await store.getState().openProjected(service, sink, "demo:playground");
      const active = statusChanged("active");
      store.getState().setDraft("Scripted mobile turn");
      await store
        .getState()
        .send(service, [{ type: "text", text: store.getState().draft }]);
      await active;
      expect(store.getState().conversation?.capabilities.interrupt).toBe(true);
      expect(store.getState().draft).toBe("");
      const idle = statusChanged("idle");
      await store.getState().interrupt(service);
      await idle;
      expect(store.getState().conversation?.capabilities.send).toBe(true);
    } finally {
      store.getState().close();
      service.close();
      client.close();
      await hub.close();
    }
  });

  it("uses the real handshake, instance-bound send, projection, and interrupt", async () => {
    const hub = await createDemoHub(0);
    const client = createHubClient(
      hub.origin,
      "",
      (url) => new WebSocket(url) as unknown as WebSocketLike,
    );
    const service = createConversationService(client);
    try {
      await client.connect();
      const initial = await service.open("demo:playground");
      expect(initial.capabilities.send).toBe(true);
      const receipt = await service.send([
        { type: "text", text: "  mobile input\n🦋  " },
      ]);
      expect(receipt.projectionState).toBe("pending");
      const active = await service.open("demo:playground");
      expect(active.status).toBe("active");
      expect(active.items.find((item) => item.kind === "user")).toMatchObject({
        text: "  mobile input\n🦋  ",
      });
      expect(active.items.some((item) => item.kind === "assistant")).toBe(true);
      expect(active.capabilities.interrupt).toBe(true);
      const stopped = await service.interrupt();
      expect(stopped.projectionState).toBe("reflected");
      expect((await service.open("demo:playground")).status).toBe("idle");
      await expect(
        client.request("turn/start", {
          ref: "demo:playground",
          expectedInstanceId: "wrong",
          clientMutationId: "wrong",
          input: [],
        }),
      ).rejects.toThrow();
      expect(
        (await service.open("demo:playground")).items.filter(
          (item) => item.kind === "user",
        ),
      ).toHaveLength(1);
    } finally {
      service.close();
      client.close();
      await hub.close();
    }
  });
});
