import { expect, it } from "vitest";
import type {
  AnyNotification,
  LaunchConfigLayer,
  LaunchConfigResolved,
} from "../../appwire-client/typescript/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { LaunchSettings } from "./launchSettings";

function fixture(layerName: "global" | "project" = "global", cwd = "/") {
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
    client,
    model: new LaunchSettings(client, cwd, layerName),
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
it("retains a draft while offline and reconciles the same hub without writing", async () => {
  const f = fixture();
  await f.model.refresh();
  f.model.edit("maxRounds", 8);
  await f.model.setConnection(null);
  expect(await f.model.save()).toBe(false);
  expect(f.model.getSnapshot().draft?.maxRounds).toBe(8);
  await f.model.setConnection(f.client);
  expect(f.model.getSnapshot().draft?.maxRounds).toBe(8);
  expect(f.model.getSnapshot().dirty).toBe(true);
  expect(f.model.getSnapshot().changedElsewhere).toBe(false);
  expect(
    f.calls.filter((call) => call.method.endsWith("setLayer")),
  ).toHaveLength(0);
  expect(await f.model.save()).toBe(true);
});
it("preserves a reconnect draft and rejects an externally changed baseline", async () => {
  const f = fixture();
  await f.model.refresh();
  f.model.edit("maxRounds", 8);
  await f.model.setConnection(null);
  f.layer = { ...f.layer, maxRounds: 9 };
  await f.model.setConnection(f.client);
  expect(f.model.getSnapshot().draft?.maxRounds).toBe(8);
  expect(f.model.getSnapshot().changedElsewhere).toBe(true);
  expect(await f.model.save()).toBe(false);
  expect(f.layer.maxRounds).toBe(9);
});
it("does not send a write after its preflight connection has been replaced", async () => {
  const f = fixture();
  await f.model.refresh();
  f.model.edit("maxRounds", 8);
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
  const save = f.model.save();
  await f.model.setConnection(null);
  f.io.request = original;
  await f.model.setConnection(f.client);
  release();
  expect(await save).toBe(false);
  expect(f.model.getSnapshot().draft?.maxRounds).toBe(8);
  expect(
    f.calls.filter((call) => call.method.endsWith("setLayer")),
  ).toHaveLength(0);
});
it("reconciles a write applied before disconnect without replaying or accepting its late reply", async () => {
  const f = fixture();
  await f.model.refresh();
  f.model.edit("maxRounds", 8);
  let release!: () => void;
  let sent!: () => void;
  const pending = new Promise<void>((done) => {
    release = done;
  });
  const written = new Promise<void>((done) => {
    sent = done;
  });
  const original = f.io.request;
  f.io.request = async (method, params) => {
    const value = await original(method, params);
    if (method.endsWith("setLayer")) {
      sent();
      await pending;
    }
    return value;
  };
  const save = f.model.save();
  await written;
  await f.model.setConnection(null);
  f.io.request = original;
  await f.model.setConnection(f.client);
  expect(f.model.getSnapshot().dirty).toBe(false);
  f.model.edit("maxRounds", 10);
  release();
  expect(await save).toBe(false);
  expect(f.model.getSnapshot().draft?.maxRounds).toBe(10);
  expect(f.model.getSnapshot().dirty).toBe(true);
  expect(
    f.calls.filter((call) => call.method.endsWith("setLayer")),
  ).toHaveLength(1);
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

it("scopes project reads and writes and refuses global-only fields", async () => {
  const f = fixture("project", "/project-fixture");
  await f.model.refresh();
  expect(() => f.model.edit("nonInteractive", true)).toThrow();
  f.model.edit("maxRounds", 7);
  expect(await f.model.save()).toBe(true);
  const writes = f.calls.filter(
    (call) => call.method === "evener/launch/setLayer",
  );
  expect(writes).toHaveLength(1);
  expect(writes[0]?.params).toEqual({
    cwd: "/project-fixture",
    layer: "project",
    config: { model: "fixture/model", maxRounds: 7, env: { KEEP: "value" } },
  });
  const reads = f.calls.filter(
    (call) => call.method === "evener/launch/getLayer",
  );
  expect(reads.length).toBeGreaterThan(0);
  for (const read of reads)
    expect(read.params).toEqual({ cwd: "/project-fixture", layer: "project" });
});

it("trusts only the reviewed repository hash and confirms with independent resolve", async () => {
  const f = fixture("project", "/repo");
  const request = f.io.request;
  let trusted = false;
  f.io.request = async (method, params) => {
    if (method === "evener/launch/trustRepo") {
      trusted = true;
      throw Error("mutation reply lost");
    }
    const result = await request(method, params);
    if (method === "evener/launch/resolve")
      return {
        ...(result as LaunchConfigResolved),
        repo: {
          path: "/repo/.evener/launch.toml",
          hash: "reviewed",
          trust: trusted ? "trusted" : "untrusted",
          preview: "max_rounds = 7",
        },
      };
    return result;
  };
  await f.model.refresh();
  expect(await f.model.trustRepository("obsolete")).toBe(false);
  expect(
    f.calls.filter((c) => c.method === "evener/launch/trustRepo"),
  ).toHaveLength(0);
  f.model.edit("maxRounds", 8);
  expect(await f.model.trustRepository("reviewed")).toBe(false);
  await f.model.refresh(true);
  expect(await f.model.trustRepository("reviewed")).toBe(true);
  expect(f.calls.filter((c) => c.method === "evener/launch/trustRepo")).toEqual(
    [
      {
        method: "evener/launch/trustRepo",
        params: { cwd: "/repo", hash: "reviewed" },
      },
    ],
  );
  expect(f.model.getSnapshot().resolved?.repo?.trust).toBe("trusted");
  expect(f.calls.at(-1)?.method).toBe("evener/launch/resolve");
});

it("does not infer trust from a successful reply if the current file differs", async () => {
  const f = fixture("project", "/repo");
  const request = f.io.request;
  let hash = "reviewed";
  f.io.request = async (method, params) => {
    if (method === "evener/launch/trustRepo") {
      hash = "changed";
      return {};
    }
    const result = await request(method, params);
    if (method === "evener/launch/resolve")
      return {
        ...(result as LaunchConfigResolved),
        repo: {
          path: "/repo/.evener/launch.toml",
          hash,
          trust: "changed",
          preview: "max_rounds = 9",
        },
      };
    return result;
  };
  await f.model.refresh();
  expect(await f.model.trustRepository("reviewed")).toBe(false);
  expect(f.model.getSnapshot().resolved?.repo?.hash).toBe("changed");
  expect(f.model.getSnapshot().error).toBeTruthy();
});
