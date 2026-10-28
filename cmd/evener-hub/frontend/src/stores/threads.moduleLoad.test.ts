// What importing threads.ts does on its own: attach the session-cache channel
// once and start with the cache epoch unobserved. Each case imports a fresh
// copy of the module under a production-mode environment, since the default
// channel factory answers null in test mode.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

class RecordingBroadcastChannel extends EventTarget {
  static instances: RecordingBroadcastChannel[] = [];
  constructor(readonly name: string) {
    super();
    RecordingBroadcastChannel.instances.push(this);
  }
  postMessage(): void {}
  close(): void {}
  deliver(message: unknown): void {
    this.dispatchEvent(new MessageEvent("message", { data: message }));
  }
}

async function freshThreads() {
  vi.resetModules();
  return import("./threads");
}

describe("threads module load", () => {
  beforeEach(() => {
    RecordingBroadcastChannel.instances = [];
    vi.stubGlobal("BroadcastChannel", RecordingBroadcastChannel);
    vi.stubEnv("MODE", "production");
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
  });

  it("attaches the cache channel exactly once", async () => {
    await freshThreads();
    expect(RecordingBroadcastChannel.instances).toHaveLength(1);
  });

  it("starts with the cache epoch unobserved and then ignores a repeated epoch", async () => {
    const { threadsStore } = await freshThreads();
    const [channel] = RecordingBroadcastChannel.instances;
    threadsStore.setState({ cacheLifetimes: new Map([["a", { leaseEpoch: 0 }]]) });
    channel?.deliver({ version: 1, sourceId: "peer", kind: "clear", epoch: 1 });
    expect(threadsStore.getState().cacheLifetimes.get("a")?.suppressed).toBe(true);

    threadsStore.setState({ cacheLifetimes: new Map([["b", { leaseEpoch: 0 }]]) });
    channel?.deliver({ version: 1, sourceId: "peer", kind: "clear", epoch: 1 });
    expect(threadsStore.getState().cacheLifetimes.get("b")?.suppressed).toBeUndefined();
  });
});
