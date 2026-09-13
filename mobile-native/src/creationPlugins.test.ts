import { expect, it } from "vitest";
import type { PluginPreviewResponse } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";
import {
  type ConversationClientLike,
  createNewSessionService,
} from "../../mobile/src/services/newSession";
import { createNewSessionStore } from "./newSession";

async function setup(names: string[]) {
  let resolve!: (value: PluginPreviewResponse) => void;
  let reject!: (error: Error) => void;
  const preview = new Promise<PluginPreviewResponse>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  const calls: { method: string; params: unknown }[] = [];
  const service = createNewSessionService({
    request: async (method: string, params: unknown) => {
      calls.push({ method, params });
      if (method === "evener/plugin/preview") return preview;
      if (method === "thread/start")
        return { thread: { evener: { ref: "local:new" } }, turn: {} };
      if (method === "model/list") return { data: [] };
      throw new Error(`Unexpected ${method}`);
    },
  } as ConversationClientLike);
  const store = createNewSessionStore("hub-a");
  store.getState().bind(service);
  await store.getState().setCwd("/project", false);
  store.getState().setLaunchOverrides({ enabledPlugins: names, maxRounds: 7 });
  return { store, calls, resolve, reject };
}
for (const names of [[], ["alpha"]]) {
  it(`validates the exact explicit plugin selection before starting (${names.length})`, async () => {
    const { store, calls, resolve } = await setup(names);
    const pending = store.getState().submit();
    expect(calls.map((c) => c.method)).toEqual(["evener/plugin/preview"]);
    expect(calls[0]?.params).toEqual({
      cwd: "/project",
      launchOverrides: { enabledPlugins: names, maxRounds: 7 },
    });
    resolve({
      plugins: names.map((name) => ({
        name,
        selected: true,
        source: "installed",
        skillCount: 0,
        agentCount: 0,
        commandCount: 0,
        hookCount: 0,
        mcpCount: 0,
      })),
    });
    expect(await pending).toMatchObject({ status: "created" });
    expect(calls[1]).toMatchObject({
      method: "thread/start",
      params: { launchOverrides: { enabledPlugins: names, maxRounds: 7 } },
    });
  });
}
it("blocks missing plugins and keeps the explicit selection for correction", async () => {
  const { store, calls, resolve } = await setup(["missing"]);
  const pending = store.getState().submit();
  resolve({ plugins: [] });
  expect(await pending).toEqual({ status: "blocked" });
  expect(calls).toHaveLength(1);
  expect(store.getState().launchOverrides.enabledPlugins).toEqual(["missing"]);
  expect(store.getState().error).toContain("missing");
});
it("does not start after disconnecting during plugin validation", async () => {
  const { store, calls, resolve } = await setup([]);
  const pending = store.getState().submit();
  store.getState().bind(null);
  resolve({ plugins: [] });
  expect(await pending).toEqual({ status: "obsolete" });
  expect(calls.map((c) => c.method)).toEqual(["evener/plugin/preview"]);
});
it("reports preview failure without dispatching creation", async () => {
  const { store, calls, reject } = await setup([]);
  const pending = store.getState().submit();
  reject(new Error("offline"));
  expect(await pending).toEqual({ status: "failed" });
  expect(calls.map((c) => c.method)).toEqual(["evener/plugin/preview"]);
  expect(store.getState().launchOverrides.enabledPlugins).toEqual([]);
});

it("omits plugin selection for an unsupported harness", async () => {
  const { store, calls } = await setup(["alpha"]);
  await store.getState().setHarness("codex");
  expect(store.getState().launchOverrides).toEqual({ maxRounds: 7 });
  store
    .getState()
    .setLaunchOverrides({ enabledPlugins: ["stale"], maxRounds: 8 });
  expect(await store.getState().submit()).toMatchObject({ status: "created" });
  expect(calls.find((c) => c.method === "thread/start")?.params).toMatchObject({
    launchOverrides: { maxRounds: 8 },
  });
  expect(calls.some((c) => c.method === "evener/plugin/preview")).toBe(false);
});

it("does not report an uncertain creation when disconnecting before start", async () => {
  const { store, resolve } = await setup([]);
  const pending = store.getState().submit();
  store.getState().bind(null);
  resolve({ plugins: [] });
  await pending;
  expect(store.getState().error).toBeNull();
});
