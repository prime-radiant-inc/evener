import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { RecordingBroadcastChannel } from "./testing/recordingBroadcastChannel";

async function freshFactory() {
  vi.resetModules();
  return (await import("./browserBroadcastChannel")).createBrowserBroadcastChannel;
}

describe("default browser BroadcastChannel", () => {
  beforeEach(() => {
    RecordingBroadcastChannel.instances = [];
    vi.stubGlobal("BroadcastChannel", RecordingBroadcastChannel);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.unstubAllEnvs();
  });

  it("passes no channel in test mode, even though the VM has BroadcastChannel", async () => {
    vi.stubEnv("MODE", "test");
    const factory = await freshFactory();
    expect(factory("fixture.v1")).toBeNull();
    expect(RecordingBroadcastChannel.instances).toHaveLength(0);
  });

  it("passes no channel when the host has no BroadcastChannel", async () => {
    vi.stubEnv("MODE", "production");
    vi.stubGlobal("BroadcastChannel", undefined);
    const factory = await freshFactory();
    expect(factory("fixture.v1")).toBeNull();
  });

  it("creates the named channel in production mode", async () => {
    vi.stubEnv("MODE", "production");
    const factory = await freshFactory();
    factory("fixture.v1");
    expect(RecordingBroadcastChannel.instances.map((channel) => channel.name)).toEqual(["fixture.v1"]);
  });
});
