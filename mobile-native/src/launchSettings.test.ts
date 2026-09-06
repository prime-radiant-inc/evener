import { expect, it } from "vitest";
import type {
  AnyNotification,
  LaunchConfigLayer,
  LaunchConfigResolved,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { LaunchSettings } from "./launchSettings";

function fixture() {
  let layer: LaunchConfigLayer = {
    model: "fixture/model",
    maxRounds: 3,
    env: { KEEP: "value" },
  };
  const listeners = new Set<(event: AnyNotification) => void>();
  const calls: { method: string; params: unknown }[] = [];
  const resolved = (): LaunchConfigResolved => ({
    effective: { ...layer },
    layers: { global: layer },
    provenance: {},
  });
  const io = {
    request: async (method: string, params: unknown): Promise<unknown> => {
      if (method === "evener/launch/schema")
        return {
          options: [
            {
              wireField: "maxRounds",
              field: "max_rounds",
              label: "Rounds",
              kind: "int",
              group: "Agent",
              perLaunch: true,
              defaultableLayers: ["global", "project"],
            },
            {
              wireField: "nonInteractive",
              field: "non_interactive",
              label: "Interactive",
              kind: "bool",
              group: "Agent",
              perLaunch: true,
              defaultableLayers: ["global"],
            },
            {
              wireField: "model",
              field: "model",
              label: "Model",
              kind: "string",
              group: "Agent",
              perLaunch: true,
              defaultableLayers: ["global"],
            },
          ],
        };
      if (method === "evener/launch/getLayer") return structuredClone(layer);
      if (method === "evener/launch/resolve") return resolved();
      if (method === "evener/launch/setLayer") {
        layer = structuredClone(
          (params as { config: LaunchConfigLayer }).config,
        );
        return resolved();
      }
      throw Error(method);
    },
  };
  const client = {
    request: (method, params) => {
      calls.push({ method, params });
      return io.request(method, params);
    },
    onNotification: (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
  } as ConversationClientLike;
  return {
    model: new LaunchSettings(client, "/", "global"),
    io,
    calls,
    listeners,
    get layer() {
      return layer;
    },
    set layer(value) {
      layer = value;
    },
  };
}
it("preserves untouched fields and explicit false/zero while removing an override", async () => {
  const f = fixture();
  await f.model.refresh();
  f.model.edit("nonInteractive", false);
  f.model.edit("maxRounds", 0);
  f.model.edit("model", undefined);
  expect(await f.model.save()).toBe(true);
  expect(f.layer).toEqual({
    maxRounds: 0,
    nonInteractive: false,
    env: { KEEP: "value" },
  });
  expect(f.model.getSnapshot().dirty).toBe(false);
});
it("refuses fields outside the editable schema and layers", async () => {
  const f = fixture();
  await f.model.refresh();
  expect(() => f.model.edit("env", {})).toThrow();
  expect(f.model.getSnapshot().dirty).toBe(false);
});
it("rejects a stale save before any write and preserves the draft", async () => {
  const f = fixture();
  await f.model.refresh();
  f.model.edit("maxRounds", 8);
  f.layer = { ...f.layer, model: "external/model" };
  expect(await f.model.save()).toBe(false);
  expect(
    f.calls.filter((call) => call.method.endsWith("setLayer")),
  ).toHaveLength(0);
  expect(f.model.getSnapshot().changedElsewhere).toBe(true);
  expect(f.model.getSnapshot().draft?.maxRounds).toBe(8);
  await f.model.refresh(true);
  expect(f.model.getSnapshot().draft?.model).toBe("external/model");
  expect(f.model.getSnapshot().dirty).toBe(false);
});
it("marks externally updated dirty editors without replacing their drafts", async () => {
  const f = fixture();
  f.model.start();
  await f.model.refresh();
  f.model.edit("maxRounds", 8);
  for (const listener of f.listeners)
    listener({
      method: "evener/launch/updated",
      params: { cwd: "/", layer: "global" },
    });
  expect(f.model.getSnapshot().draft?.maxRounds).toBe(8);
  expect(f.model.getSnapshot().changedElsewhere).toBe(true);
  expect(await f.model.save()).toBe(false);
  f.model.dispose();
});
it("does not replay uncertain writes and reads the actual layer afterwards", async () => {
  const f = fixture();
  await f.model.refresh();
  f.model.edit("maxRounds", 8);
  const original = f.io.request;
  f.io.request = async (method, params) => {
    const result = await original(method, params);
    if (method.endsWith("setLayer")) throw Error("response lost");
    return result;
  };
  expect(await f.model.save()).toBe(false);
  expect(
    f.calls.filter((call) => call.method.endsWith("setLayer")),
  ).toHaveLength(1);
  expect(f.model.getSnapshot().current?.maxRounds).toBe(8);
  expect(f.model.getSnapshot().error).toBeTruthy();
  expect(f.model.getSnapshot().dirty).toBe(false);
});
it("keeps layer editing available if only effective-value resolution fails", async () => {
  const f = fixture();
  const original = f.io.request;
  f.io.request = (method, params) =>
    method.endsWith("resolve")
      ? Promise.reject(Error("unavailable"))
      : original(method, params);
  await f.model.refresh();
  expect(f.model.getSnapshot().current?.maxRounds).toBe(3);
  expect(f.model.getSnapshot().resolved).toBeNull();
  expect(f.model.getSnapshot().resolveError).toBeTruthy();
});
it("does not publish a load after disposal", async () => {
  const f = fixture();
  let release!: () => void;
  const pending = new Promise<void>((done) => {
    release = done;
  });
  const original = f.io.request;
  f.io.request = async (method, params) => {
    await pending;
    return original(method, params);
  };
  const read = f.model.refresh();
  f.model.dispose();
  release();
  await read;
  expect(f.model.getSnapshot().current).toBeNull();
});
it("allows only one save and prevents edits during its preflight", async () => {
  const f = fixture();
  await f.model.refresh();
  f.model.edit("maxRounds", 5);
  let release!: () => void;
  const pending = new Promise<void>((done) => {
    release = done;
  });
  const original = f.io.request;
  f.io.request = async (method, params) => {
    if (method.endsWith("getLayer")) await pending;
    return original(method, params);
  };
  const save = f.model.save();
  expect(await f.model.save()).toBe(false);
  expect(() => f.model.edit("maxRounds", 9)).toThrow();
  release();
  expect(await save).toBe(true);
  expect(
    f.calls.filter((call) => call.method.endsWith("setLayer")),
  ).toHaveLength(1);
});
it("keeps a failed preflight retryable without sending a write", async () => {
  const f = fixture();
  await f.model.refresh();
  f.model.edit("maxRounds", 5);
  const original = f.io.request;
  f.io.request = async () => {
    throw Error("offline");
  };
  expect(await f.model.save()).toBe(false);
  expect(
    f.calls.filter((call) => call.method.endsWith("setLayer")),
  ).toHaveLength(0);
  expect(f.model.getSnapshot().dirty).toBe(true);
  f.io.request = original;
  expect(await f.model.save()).toBe(true);
});
it("ignores an obsolete refresh after a newer baseline is loaded", async () => {
  const f = fixture();
  let release!: () => void;
  const pending = new Promise<void>((done) => {
    release = done;
  });
  const original = f.io.request;
  f.io.request = async (method, params) => {
    const value = await original(method, params);
    await pending;
    return value;
  };
  const old = f.model.refresh();
  f.io.request = original;
  f.layer = { model: "new/model" };
  await f.model.refresh();
  release();
  await old;
  expect(f.model.getSnapshot().current).toEqual({ model: "new/model" });
});
