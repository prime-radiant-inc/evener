// @vitest-environment node

import { describe, expect, test, vi } from "vitest";
import {
  type CommandCatalogClient,
  createCommandCatalog,
  createSessionCommandCatalog,
  sessionPluginNames,
} from "./commandCatalog";
import type { AnyNotification } from "./types.gen";

const PLUGIN_UPDATED: AnyNotification = { method: "evener/plugin/updated", params: {} };

// A client through the module's own Pick: scripted per-method reads, recorded
// requests, and a notification sink the test drives by hand.
function boundary(commands: unknown[] = [{ name: "review", source: "plugin", pluginName: "loaded" }]) {
  const requests: Array<{ method: string; params: unknown }> = [];
  const handlers = new Set<(n: AnyNotification) => void>();
  const io = {
    read: async (method: string): Promise<unknown> =>
      method === "evener/command/list"
        ? { commands }
        : {
            thread: {
              evener: {
                ref: "local:test",
                diagnostics: {
                  plugins: [{ name: "loaded" }],
                  skills: [
                    {
                      name: "testing",
                      description: "fixture",
                      disableModelInvocation: false,
                      userInvocable: true,
                      available: true,
                    },
                  ],
                },
              },
            },
          },
  };
  const client = {
    request: async (method: string, params: unknown) => {
      requests.push({ method, params });
      return io.read(method);
    },
    onNotification: (handler: (n: AnyNotification) => void) => {
      handlers.add(handler);
      return () => {
        handlers.delete(handler);
      };
    },
  } as unknown as CommandCatalogClient;
  const notify = (n: AnyNotification) => {
    for (const handler of handlers) handler(n);
  };
  return { client, requests, io, notify, handlers };
}

describe("createCommandCatalog", () => {
  test("two catalogs over two connections share nothing", async () => {
    const first = boundary([{ name: "one", source: "user" }]);
    const second = boundary([{ name: "two", source: "user" }]);
    const a = createCommandCatalog(first.client);
    const b = createCommandCatalog(second.client);

    await a.getState().refresh();
    expect(a.getState().commands.map((c) => c.name)).toEqual(["one"]);
    expect(b.getState()).toBe(b.getInitialState());
    expect(second.requests).toEqual([]);

    a.watch();
    b.watch();
    first.notify(PLUGIN_UPDATED);
    await vi.waitFor(() => expect(first.requests).toHaveLength(2));
    // b has never been read, so a plugin change does not start reading it.
    second.notify(PLUGIN_UPDATED);
    await Promise.resolve();
    expect(second.requests).toEqual([]);
    await b.getState().refresh();
    second.notify(PLUGIN_UPDATED);
    await vi.waitFor(() => expect(second.requests).toHaveLength(2));
    expect(b.getState().commands.map((c) => c.name)).toEqual(["two"]);
    expect(first.requests).toHaveLength(2);
  });

  test("a failed re-read keeps the last catalog and names the failure; the watch disposer stops re-reads", async () => {
    const { client, io, notify, handlers } = boundary();
    const catalog = createCommandCatalog(client);
    const listener = vi.fn();
    catalog.subscribe(listener);
    await catalog.getState().refresh();
    expect(catalog.getState()).toMatchObject({ loading: false, error: null });
    expect(catalog.getState().commands).toHaveLength(1);

    io.read = async () => {
      throw new Error("down");
    };
    await catalog.getState().refresh();
    expect(catalog.getState().commands).toHaveLength(1);
    expect(catalog.getState().error).toContain("down");
    expect(listener.mock.calls.some(([state]) => state.loading)).toBe(true);
    expect(catalog.getState().loading).toBe(false);

    const stop = catalog.watch();
    expect(handlers.size).toBe(1);
    stop();
    expect(handlers.size).toBe(0);
    notify(PLUGIN_UPDATED);
    expect(catalog.getState().loading).toBe(false);
  });

  test("a refresh during a load re-runs the load once more, and only the final read lands", async () => {
    const { client, requests, io } = boundary();
    const catalog = createCommandCatalog(client);
    let complete!: (value: unknown) => void;
    io.read = () =>
      new Promise((resolve) => {
        complete = resolve;
      });
    const pending = catalog.getState().refresh();
    const coalesced = catalog.getState().refresh();
    expect(coalesced).toBe(pending);
    expect(requests).toHaveLength(1);
    const stale = complete;
    io.read = async () => ({ commands: [{ name: "fresh", source: "user" }] });
    stale({ commands: [{ name: "stale", source: "user" }] });
    await pending;
    expect(requests).toHaveLength(2);
    expect(catalog.getState().commands.map((c) => c.name)).toEqual(["fresh"]);
  });

  test("an empty successful read still counts as loaded, so a plugin change re-reads it", async () => {
    const { client, requests, notify } = boundary([]);
    const catalog = createCommandCatalog(client);
    catalog.watch();
    await catalog.getState().refresh();
    expect(catalog.getState().commands).toEqual([]);

    // A length/emptiness gate would treat this authoritative-empty catalog as
    // unread and never re-read it; the arrived-catalog gate must not.
    notify(PLUGIN_UPDATED);
    await vi.waitFor(() => expect(requests.filter((r) => r.method === "evener/command/list")).toHaveLength(2));
  });
});

describe("sessionPluginNames", () => {
  test("no inventory hides every plugin command; an empty inventory is authoritative; names pass through", () => {
    expect(sessionPluginNames(undefined)).toBeNull();
    expect(sessionPluginNames({})).toBeNull();
    expect(sessionPluginNames({ plugins: [] })).toEqual(new Set());
    expect(sessionPluginNames({ plugins: [{ name: "a" }, { name: "b" }] })).toEqual(new Set(["a", "b"]));
  });
});

describe("createSessionCommandCatalog", () => {
  const sessionBoundary = () =>
    boundary([
      { name: "review", source: "plugin", pluginName: "loaded" },
      { name: "review", source: "plugin", pluginName: "absent" },
      { name: "notes", source: "user" },
    ]);

  test("offers only session-loaded plugin commands with qualified insertions and advertised skills", async () => {
    const { client, requests } = sessionBoundary();
    const catalog = createSessionCommandCatalog(client, "local:test");
    await catalog.refresh();
    expect(requests).toContainEqual({
      method: "thread/read",
      params: { ref: "local:test", includeTurns: false },
    });
    expect(catalog.getState().items.map((item) => item.invocation)).toEqual(["/loaded:review", "/notes", "/testing"]);
  });

  test("does not replace usable results with incomplete or foreign-session catalogs", async () => {
    const { client, io } = sessionBoundary();
    const catalog = createSessionCommandCatalog(client, "local:test");
    await catalog.refresh();
    io.read = async () => {
      throw new Error("offline");
    };
    await catalog.refresh();
    expect(catalog.getState().items).toHaveLength(3);
    expect(catalog.getState().error).toBeTruthy();
    io.read = async (method) =>
      method === "evener/command/list" ? { commands: [] } : { thread: { evener: { ref: "local:other" } } };
    await catalog.refresh();
    expect(catalog.getState().error).toBeTruthy();
    expect(catalog.getState().items).toHaveLength(3);
  });

  test("a retry after a failure clears the error for as long as it loads", async () => {
    const { client, io } = sessionBoundary();
    const catalog = createSessionCommandCatalog(client, "local:test");
    io.read = async () => {
      throw new Error("offline");
    };
    await catalog.refresh();
    expect(catalog.getState().error).toBeTruthy();
    let complete!: (value: unknown) => void;
    io.read = (method) =>
      method === "evener/command/list"
        ? Promise.resolve({ commands: [] })
        : new Promise((resolve) => {
            complete = resolve;
          });
    const retry = catalog.refresh();
    expect(catalog.getState()).toMatchObject({ loading: true, error: null });
    complete({ thread: { evener: { ref: "local:test" } } });
    await retry;
    expect(catalog.getState()).toMatchObject({ items: [], loading: false, error: null });
  });

  test("does not publish catalog responses after its owner leaves", async () => {
    const { client, io } = sessionBoundary();
    const catalog = createSessionCommandCatalog(client, "local:test");
    let complete!: (value: unknown) => void;
    io.read = (method) =>
      method === "evener/command/list"
        ? Promise.resolve({ commands: [] })
        : new Promise((resolve) => {
            complete = resolve;
          });
    const pending = catalog.refresh();
    catalog.dispose();
    const prior = catalog.getState();
    complete({
      thread: {
        evener: {
          ref: "local:test",
          diagnostics: {
            skills: [{ name: "late", disableModelInvocation: false, userInvocable: true, available: true }],
          },
        },
      },
    });
    await pending;
    expect(catalog.getState()).toBe(prior);
  });

  test("start loads once and re-reads on a plugin change or this session's resync, never another session's", async () => {
    const { client, requests, notify, handlers } = sessionBoundary();
    const catalog = createSessionCommandCatalog(client, "local:test");
    catalog.start();
    catalog.start();
    expect(handlers.size).toBe(1);
    await vi.waitFor(() => expect(catalog.getState().items).toHaveLength(3));
    const loads = () => requests.filter((r) => r.method === "evener/command/list").length;
    expect(loads()).toBe(1);
    notify({ method: "evener/thread/resync", params: { ref: "local:other" } } as AnyNotification);
    expect(loads()).toBe(1);
    notify({ method: "evener/thread/resync", params: { ref: "local:test" } } as AnyNotification);
    await vi.waitFor(() => expect(loads()).toBe(2));
    notify(PLUGIN_UPDATED);
    await vi.waitFor(() => expect(loads()).toBe(3));
    catalog.dispose();
    expect(handlers.size).toBe(0);
  });
});
