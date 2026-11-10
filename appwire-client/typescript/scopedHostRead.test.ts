// @vitest-environment node

import { afterEach, describe, expect, test, vi } from "vitest";
import {
  createScopedHostReadStore,
  pluginComponentsReadInputs,
  pluginPreviewReadInputs,
  SCOPED_HOST_READ_DEBOUNCE_MS,
  type ScopedHostReadInputs,
} from "./scopedHostRead";
import { FakeClient } from "./testing/fakeClient";
import type { HostForwardedResult } from "./types.gen";

const client = {};

function inputs(overrides: Partial<ScopedHostReadInputs<string>> = {}): ScopedHostReadInputs<string> {
  return { client, host: "local", cwd: "/a", inputKey: "k", enabled: true, read: async () => "answer", ...overrides };
}

async function settle() {
  await vi.advanceTimersByTimeAsync(SCOPED_HOST_READ_DEBOUNCE_MS);
}

afterEach(() => {
  vi.useRealTimers();
});

describe("scoped host read store", () => {
  test("reads once after the inputs settle and reports the answer", async () => {
    vi.useFakeTimers();
    const read = vi.fn(async () => "answer");
    const store = createScopedHostReadStore<string>();
    const stop = store.start(inputs({ read }));
    expect(store.view(inputs({ read }))).toEqual({ status: "loading" });
    expect(read).not.toHaveBeenCalled();
    await settle();
    expect(read).toHaveBeenCalledTimes(1);
    expect(store.view(inputs())).toEqual({ status: "ready", response: "answer" });
    stop();
  });

  test("cancelling before the debounce elapses never reads", async () => {
    vi.useFakeTimers();
    const read = vi.fn(async () => "answer");
    const store = createScopedHostReadStore<string>();
    store.start(inputs({ read }))();
    await settle();
    expect(read).not.toHaveBeenCalled();
  });

  test("keeps the previous response while the same scope refreshes, and drops it for another host", async () => {
    vi.useFakeTimers();
    const store = createScopedHostReadStore<string>();
    store.start(inputs());
    await settle();

    const refreshed = inputs({ inputKey: "k2" });
    expect(store.view(refreshed)).toEqual({ status: "loading", response: "answer" });
    expect(store.view(inputs({ host: "buildbox" }))).toEqual({ status: "loading" });
    expect(store.view(inputs({ client: {} }))).toEqual({ status: "loading" });
  });

  test("an error for the same inputs keeps the response beside it; a new key does not claim it", async () => {
    vi.useFakeTimers();
    const store = createScopedHostReadStore<string>();
    store.start(inputs());
    await settle();

    const failing = inputs({
      read: async () => {
        throw new Error("down");
      },
    });
    store.retry();
    store.start(failing);
    await settle();
    const state = store.view(failing);
    expect(state.status).toBe("error");
    expect(state.response).toBe("answer");

    const other = inputs({ inputKey: "other", read: failing.read });
    store.start(other);
    await settle();
    expect(store.view(other).response).toBeUndefined();
  });

  test("an answer to a cancelled read is dropped", async () => {
    vi.useFakeTimers();
    let resolve: (value: string) => void = () => {};
    const slow = inputs({ read: () => new Promise<string>((r) => (resolve = r)) });
    const store = createScopedHostReadStore<string>();
    const stop = store.start(slow);
    await settle();
    stop();
    resolve("late");
    await vi.advanceTimersByTimeAsync(0);
    expect(store.view(slow)).toEqual({ status: "loading" });
  });

  test("retry changes the request identity so the view reports loading until it is started again", async () => {
    vi.useFakeTimers();
    const store = createScopedHostReadStore<string>();
    store.start(inputs());
    await settle();
    store.retry();
    expect(store.view(inputs())).toEqual({ status: "loading", response: "answer" });
    store.start(inputs());
    await settle();
    expect(store.view(inputs())).toEqual({ status: "ready", response: "answer" });
  });

  test("a disabled read reports its disabled state and forgets the last response", async () => {
    vi.useFakeTimers();
    const store = createScopedHostReadStore<string>();
    store.start(inputs());
    await settle();
    const off = inputs({ enabled: false, disabledState: { status: "ready", response: "off" } });
    store.start(off);
    expect(store.view(off)).toEqual({ status: "ready", response: "off" });
    expect(store.view(inputs())).toEqual({ status: "loading" });
  });

  test("notifies subscribers when an answer arrives", async () => {
    vi.useFakeTimers();
    const store = createScopedHostReadStore<string>();
    const listener = vi.fn();
    store.subscribe(listener);
    store.start(inputs());
    listener.mockClear();
    await settle();
    expect(listener).toHaveBeenCalled();
  });
});

describe("pluginPreviewReadInputs", () => {
  test("asks the selected host for the preview and sends overrides only when there are some", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/plugin/preview", () => ({ plugins: [] }));
    await pluginPreviewReadInputs({ client: fake, cwd: "/a", launchOverrides: {}, pluginRevision: 0 }).read();
    await pluginPreviewReadInputs({
      client: fake,
      cwd: "/a",
      launchOverrides: { enabledPlugins: ["p"] },
      pluginRevision: 0,
    }).read();
    expect(fake.calls.map((call) => call.params)).toEqual([
      { cwd: "/a" },
      { cwd: "/a", launchOverrides: { enabledPlugins: ["p"] } },
    ]);
  });
});

describe("pluginComponentsReadInputs", () => {
  const PAIR = { plugin: "linter", marketplace: "acme" };
  const PATH = { path: "/opt/plugin" };

  test.each([
    ["an installed pair", PAIR],
    ["a path target", PATH],
  ] as const)("reads %s on the local host in a one-target batch and unwraps its result row", async (_label, target) => {
    const fake = new FakeClient("ready");
    const row = { ...target, components: { skills: ["lint"] } };
    fake.on("evener/plugin/components", () => ({ results: [row] }));
    const inputs = pluginComponentsReadInputs({ client: fake, target });
    expect(inputs.inputKey).toBe(JSON.stringify(target));
    await expect(inputs.read()).resolves.toBe(row);
    expect(fake.calls).toEqual([{ method: "evener/plugin/components", params: { targets: [target] } }]);
  });

  test.each([
    ["an installed pair", PAIR],
    ["a path target", PATH],
  ] as const)("reads %s through a remote host's request path", async (_label, target) => {
    const fake = new FakeClient("ready");
    fake.on(
      "evener/host/request",
      () => ({ results: [{ ...target, components: {} }] }) as unknown as HostForwardedResult,
    );
    await pluginComponentsReadInputs({ client: fake, host: "buildbox", target }).read();
    expect(fake.calls).toEqual([
      {
        method: "evener/host/request",
        params: { host: "buildbox", method: "evener/plugin/components", params: { targets: [target] } },
      },
    ]);
  });

  test("an answer with no result row reads as an error row, not an empty inventory", async () => {
    const fake = new FakeClient("ready");
    fake.on("evener/plugin/components", () => ({ results: [] }));
    const row = await pluginComponentsReadInputs({ client: fake, target: PAIR }).read();
    expect(row.components).toBeUndefined();
    expect(row.error).toEqual(expect.any(String));
  });
});
