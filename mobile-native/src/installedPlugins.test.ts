import { expect, it, vi } from "vitest";
import type {
  AnyNotification,
  PluginEntry,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { InstalledPlugins } from "./installedPlugins";

const plugin = (marketplace: string, enabled = true): PluginEntry => ({
  plugin: "tools",
  marketplace,
  enabled,
  version: "1",
  autoUpgrade: false,
  broken: false,
  installPath: "/fixture",
  installedAt: 1,
  lastUpdated: 1,
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => {
    resolve = done;
  });
  return { promise, resolve };
}
function boundary() {
  const listeners = new Set<(event: AnyNotification) => void>();
  const calls: { method: string; params: unknown }[] = [];
  const io = {
    request: async (_method: string, _params: unknown): Promise<unknown> => ({
      plugins: [plugin("a"), plugin("b")],
    }),
  };
  const client = {
    request: (method, params) => {
      calls.push({ method, params });
      return io.request(method, params);
    },
    onNotification: (fn) => {
      listeners.add(fn);
      return () => {
        listeners.delete(fn);
      };
    },
  } as ConversationClientLike;
  return { model: new InstalledPlugins(client), io, calls, listeners };
}
it("preserves marketplace identity through every plugin operation", async () => {
  const { model, calls } = boundary();
  await model.refresh();
  const target = { plugin: "tools", marketplace: "b" };
  await model.install(target);
  await model.upgrade(target);
  await model.disable(target);
  await model.enable(target);
  await model.setAutoUpgrade(target, true);
  await model.remove(target);
  expect(calls.filter((c) => !c.method.endsWith("/list"))).toEqual([
    { method: "evener/plugin/install", params: target },
    { method: "evener/plugin/upgrade", params: target },
    { method: "evener/plugin/disable", params: target },
    { method: "evener/plugin/enable", params: target },
    {
      method: "evener/plugin/setAutoUpgrade",
      params: { ...target, autoUpgrade: true },
    },
    { method: "evener/plugin/remove", params: target },
  ]);
});
it("reconciles an uncertain write without replaying it", async () => {
  const { model, io, calls } = boundary();
  await model.refresh();
  io.request = async (method) => {
    if (method.endsWith("/disable")) throw Error("reply lost");
    return { plugins: [plugin("b", false)] };
  };
  await expect(
    model.disable({ plugin: "tools", marketplace: "b" }),
  ).rejects.toThrow("reply lost");
  expect(calls.filter((c) => c.method.endsWith("/disable"))).toHaveLength(1);
  expect(model.getSnapshot().plugins).toEqual([plugin("b", false)]);
  expect(model.getSnapshot().busy).toBe(false);
});
it("retains known plugins after a failed read and rejects late stale reads", async () => {
  const { model, io } = boundary();
  await model.refresh();
  const old = deferred<unknown>();
  io.request = () => old.promise;
  const pending = model.refresh();
  io.request = async () => ({ plugins: [plugin("b", false)] });
  await model.refresh();
  old.resolve({ plugins: [plugin("a")] });
  await pending;
  io.request = async () => {
    throw Error("offline");
  };
  await model.refresh();
  expect(model.getSnapshot().plugins).toEqual([plugin("b", false)]);
  expect(model.getSnapshot().error).toBeTruthy();
  expect(model.getSnapshot().loading).toBe(false);
});
it("blocks overlapping mutations and prevents a pre-write read replacing its result", async () => {
  const { model, io } = boundary();
  const old = deferred<unknown>();
  io.request = () => old.promise;
  const reading = model.refresh();
  const write = deferred<unknown>();
  io.request = (method) =>
    method.endsWith("/remove")
      ? write.promise
      : Promise.resolve({ plugins: [] });
  const removing = model.remove({ plugin: "tools", marketplace: "b" });
  await expect(
    model.enable({ plugin: "tools", marketplace: "a" }),
  ).rejects.toThrow("progress");
  write.resolve({ plugins: [] });
  await removing;
  old.resolve({ plugins: [plugin("b")] });
  await reading;
  expect(model.getSnapshot().plugins).toEqual([]);
});
it("refreshes external updates and isolates a disposed hub from another hub", async () => {
  const a = boundary();
  const b = boundary();
  a.model.start();
  await a.model.refresh();
  a.io.request = async () => ({ plugins: [plugin("a", false)] });
  for (const fn of a.listeners)
    fn({ method: "evener/plugin/updated", params: {} });
  await vi.waitFor(() =>
    expect(a.model.getSnapshot().plugins).toEqual([plugin("a", false)]),
  );
  const late = deferred<unknown>();
  a.io.request = () => late.promise;
  const pending = a.model.refresh();
  a.model.dispose();
  const snapshot = a.model.getSnapshot();
  await b.model.refresh();
  late.resolve({ plugins: [] });
  await pending;
  expect(a.listeners.size).toBe(0);
  expect(a.model.getSnapshot()).toBe(snapshot);
  expect(b.model.getSnapshot().plugins).toEqual([plugin("a"), plugin("b")]);
  await expect(
    a.model.remove({ plugin: "tools", marketplace: "a" }),
  ).rejects.toThrow("closed");
});
