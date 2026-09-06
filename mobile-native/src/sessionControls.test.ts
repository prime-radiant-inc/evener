import { describe, expect, it } from "vitest";
import type { Thread } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
  type ConversationClientLike,
  createConversationService,
} from "../../mobile/src/services/conversation";
import { SessionControls } from "./sessionControls";

async function boundary(actions: {
  rename?: (name: string) => Promise<void>;
  compact?: () => Promise<void>;
  shutdown?: () => Promise<void>;
  reasoning?: (effort: string) => Promise<void>;
}) {
  const thread: Thread = {
    id: "thread",
    sessionId: "session",
    preview: "",
    ephemeral: false,
    modelProvider: "scripted",
    createdAt: 1,
    updatedAt: 1,
    status: { type: "idle" },
    cwd: "/test",
    cliVersion: "test",
    source: "local",
    turns: [],
    evener: {
      ref: "local:test",
      instanceId: "instance",
      queue: { revision: 0 },
      capabilities: {
        send: true,
        steer: false,
        interrupt: false,
        compact: true,
        clear: false,
        forkFromTurn: false,
        shutdown: true,
        changeModel: false,
        changeVisionModel: false,
        queue: false,
        goal: false,
        rename: true,
      },
    },
  };
  const wire: ConversationClientLike = {
    request: async (method, params) => {
      if (method === "thread/read") return { thread };
      if (method === "evener/thread/name/set")
        await actions.rename?.((params as { name: string }).name);
      else if (method === "thread/compact/start") await actions.compact?.();
      else if (method === "thread/shutdown") await actions.shutdown?.();
      else if (method === "thread/reasoning-effort/set")
        await actions.reasoning?.(
          (params as { reasoningEffort: string }).reasoningEffort,
        );
      else throw new Error(`Unexpected method ${method}`);
      return {};
    },
    onNotification: () => () => {},
  } as ConversationClientLike;
  const service = createConversationService(wire);
  await service.open("local:test");
  return service;
}

describe("conversation-owned session controls", () => {
  it("keeps one outstanding action and refreshes after rename", async () => {
    let resolve!: () => void;
    const names: string[] = [];
    let refreshed = 0;
    const controls = new SessionControls(
      await boundary({
        rename: (name) => {
          names.push(name);
          return new Promise<void>((done) => {
            resolve = done;
          });
        },
        compact: async () => {},
        shutdown: async () => {},
      }),
      async () => {
        refreshed++;
      },
      () => {},
      () => true,
      () => null,
    );
    const first = controls.rename("  Mobile session  ");
    expect(controls.getSnapshot().pending).toBe("rename");
    await controls.rename("Duplicate");
    expect(names).toEqual(["Mobile session"]);
    resolve();
    await first;
    expect(refreshed).toBe(1);
    expect(controls.getSnapshot().pending).toBeNull();
  });
  it("does not refresh a stopped runtime or replay a failed operation", async () => {
    let stopped = 0,
      reads = 0,
      attempts = 0;
    const controls = new SessionControls(
      await boundary({
        rename: async () => {},
        compact: async () => {
          attempts++;
          throw new Error("Socket lost");
        },
        shutdown: async () => {
          stopped++;
        },
      }),
      async () => {
        reads++;
      },
      () => {
        stopped++;
      },
      () => true,
      () => null,
    );
    await controls.compact();
    expect(attempts).toBe(1);
    expect(controls.getSnapshot().error).not.toBeNull();
    await controls.shutdown();
    expect(stopped).toBe(2);
    expect(reads).toBe(0);
  });
  it("invalidates old confirmation callbacks and late completions when leaving a conversation", async () => {
    let resolve!: () => void;
    let callbacks = 0,
      stops = 0;
    const controls = new SessionControls(
      await boundary({
        rename: async () => {},
        compact: () =>
          new Promise<void>((done) => {
            resolve = done;
          }),
        shutdown: async () => {
          stops++;
        },
      }),
      async () => {
        callbacks++;
      },
      () => {
        callbacks++;
      },
      () => true,
      () => null,
    );
    const request = controls.compact();
    controls.dispose();
    resolve();
    await request;
    await controls.shutdown();
    expect(stops).toBe(0);
    expect(callbacks).toBe(0);
  });
  it("rejects an old confirmation after the same service reopens on a different binding", async () => {
    let generation = 1;
    let stops = 0;
    const service = await boundary({
      shutdown: async () => {
        stops++;
      },
    });
    const controls = new SessionControls(
      service,
      async () => {},
      () => {},
      () => generation === 1,
      () => null,
    );
    service.close();
    generation = 2;
    await service.open("local:test");
    await controls.shutdown();
    expect(stops).toBe(0);
  });
  it("refreshes the projection after compaction can resume a cold runtime", async () => {
    let refreshed = 0;
    const controls = new SessionControls(
      await boundary({ compact: async () => {} }),
      async () => {
        refreshed++;
      },
      () => {},
      () => true,
      () => null,
    );
    await controls.compact();
    expect(refreshed).toBe(1);
  });
  it("dispatches supported reasoning once and refreshes the authoritative value", async () => {
    const settings = {
      supportsReasoning: true,
      reasoningEffort: "low",
      reasoningEffortLevels: ["low", "high"],
    };
    const requests: string[] = [];
    let resolve!: () => void;
    const controls = new SessionControls(
      await boundary({
        reasoning: async (effort) => {
          requests.push(effort);
          await new Promise<void>((done) => {
            resolve = done;
          });
        },
      }),
      async () => {
        settings.reasoningEffort = "high";
      },
      () => {},
      () => true,
      () => settings,
    );
    const request = controls.setReasoningEffort("high");
    await controls.setReasoningEffort("low");
    expect(requests).toEqual(["high"]);
    expect(settings.reasoningEffort).toBe("low");
    resolve();
    await request;
    expect(settings.reasoningEffort).toBe("high");
    expect(controls.getSnapshot().pending).toBeNull();
  });
  it("rejects stale or unsupported reasoning choices before dispatch", async () => {
    const settings = {
      supportsReasoning: true,
      reasoningEffort: "low",
      reasoningEffortLevels: ["low", "high"],
    };
    const requests: string[] = [];
    const controls = new SessionControls(
      await boundary({
        reasoning: async (effort) => {
          requests.push(effort);
        },
      }),
      async () => {},
      () => {},
      () => true,
      () => settings,
    );
    await controls.setReasoningEffort("low");
    await controls.setReasoningEffort("invented");
    settings.reasoningEffortLevels = ["low"];
    await controls.setReasoningEffort("high");
    settings.reasoningEffortLevels = ["low", "high"];
    settings.supportsReasoning = false;
    await controls.setReasoningEffort("high");
    settings.supportsReasoning = true;
    controls.dispose();
    await controls.setReasoningEffort("high");
    expect(requests).toEqual([]);
  });
});
