import { expect, it, vi } from "vitest";
import type {
  AnyNotification,
  MarketplaceEntry,
} from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { Marketplaces } from "./marketplaces";

const entry = (name: string): MarketplaceEntry => ({
  name,
  source: { kind: "directory", path: `/fixture/${name}` },
  lastUpdated: 1,
});
const catalog = (name: string, plugin = name) => ({
  name,
  plugins: [{ name: plugin }],
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
      marketplaces: [entry("a"), entry("b")],
    }),
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
  return { model: new Marketplaces(client), io, calls, listeners };
}
it("never presents a late catalog under another marketplace", async () => {
  const { model, io } = boundary();
  await model.refresh();
  const old = deferred<unknown>();
  io.request = () => old.promise;
  const loading = model.select("a");
  io.request = async () => catalog("b");
  await model.select("b");
  old.resolve(catalog("a"));
  await loading;
  expect(model.getSnapshot().selected).toBe("b");
  expect(model.getSnapshot().catalog).toEqual(catalog("b"));
});
it("preserves marketplace list when a catalog fails and allows retry", async () => {
  const { model, io } = boundary();
  await model.refresh();
  io.request = async () => {
    throw Error("unavailable");
  };
  await model.select("a");
  expect(model.getSnapshot().marketplaces).toEqual([entry("a"), entry("b")]);
  expect(model.getSnapshot().catalogError).toBeTruthy();
  expect(model.getSnapshot().listError).toBeNull();
  io.request = async () => catalog("a");
  await model.browse();
  expect(model.getSnapshot().catalog).toEqual(catalog("a"));
  expect(model.getSnapshot().catalogError).toBeNull();
});
it("refreshes selected catalog on external updates and clears removed selection", async () => {
  const { model, io, listeners } = boundary();
  model.start();
  await model.refresh();
  io.request = async (method) =>
    method.endsWith("/browse") ? catalog("a") : { marketplaces: [entry("a")] };
  await model.select("a");
  io.request = async (method) =>
    method.endsWith("/browse")
      ? catalog("a", "updated")
      : { marketplaces: [entry("a")] };
  for (const listener of listeners)
    listener({ method: "evener/marketplace/updated", params: {} });
  await vi.waitFor(() =>
    expect(model.getSnapshot().catalog).toEqual(catalog("a", "updated")),
  );
  io.request = async () => ({ marketplaces: [] });
  for (const listener of listeners)
    listener({ method: "evener/marketplace/updated", params: {} });
  await vi.waitFor(() => expect(model.getSnapshot().selected).toBeNull());
  expect(model.getSnapshot().catalog).toBeNull();
  model.dispose();
});
it("does not replay uncertain removal and reconciles selection with server state", async () => {
  const { model, io, calls } = boundary();
  await model.refresh();
  io.request = async () => catalog("a");
  await model.select("a");
  io.request = async (method) => {
    if (method.endsWith("/remove")) throw Error("reply lost");
    return { marketplaces: [entry("b")] };
  };
  await expect(model.remove("a")).rejects.toThrow("reply lost");
  expect(calls.filter((call) => call.method.endsWith("/remove"))).toEqual([
    { method: "evener/marketplace/remove", params: { name: "a" } },
  ]);
  expect(model.getSnapshot().selected).toBeNull();
  expect(model.getSnapshot().marketplaces).toEqual([entry("b")]);
  expect(model.getSnapshot().busy).toBe(false);
});
it("fences disposed-hub replies and blocks new mutations", async () => {
  const { model, io, listeners } = boundary();
  const pending = deferred<unknown>();
  io.request = () => pending.promise;
  model.start();
  const loading = model.refresh();
  model.dispose();
  const snapshot = model.getSnapshot();
  pending.resolve({ marketplaces: [entry("a")] });
  await loading;
  expect(model.getSnapshot()).toBe(snapshot);
  expect(listeners.size).toBe(0);
  await expect(
    model.add({ source: { kind: "github", repo: "owner/repo" } }),
  ).rejects.toThrow("closed");
});
it("preserves source inputs and reloads a selected catalog after source refresh", async () => {
  const { model, io, calls } = boundary();
  const source = { kind: "github", repo: "owner/repo", ref: "release" };
  await model.add({ name: "a", source });
  io.request = async (method) =>
    method.endsWith("/browse") ? catalog("a") : { marketplaces: [entry("a")] };
  await model.select("a");
  const stale = deferred<unknown>();
  io.request = () => stale.promise;
  const browsing = model.browse();
  io.request = async (method) =>
    method.endsWith("/browse")
      ? catalog("a", "new")
      : { marketplaces: [entry("a")] };
  await model.refreshSource("a");
  stale.resolve(catalog("a", "old"));
  await browsing;
  expect(model.getSnapshot().catalog).toEqual(catalog("a", "new"));
  expect(
    calls.filter(
      (call) =>
        call.method.endsWith("/add") || call.method.endsWith("/refresh"),
    ),
  ).toEqual([
    { method: "evener/marketplace/add", params: { name: "a", source } },
    { method: "evener/marketplace/refresh", params: { name: "a" } },
  ]);
});
