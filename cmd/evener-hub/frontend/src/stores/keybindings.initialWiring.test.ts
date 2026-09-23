import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { beforeAll, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage } from "../storageTestUtils";

// The adapter module evaluates AFTER the handshake when the settings chunk
// loads lazily (or under HMR): connectionStore already holds a ready client
// and its feature set. The module's initial wiring must then load the hub's
// overrides exactly once - not zero times (support never published) and not
// twice (the wiring and a re-detected transition both refreshing). The store
// module is imported fresh per test so its module-level wiring runs against
// the pre-seeded connection.

class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null {
    return this.store.has(key) ? (this.store.get(key) ?? null) : null;
  }
  setItem(key: string, value: string): void {
    this.store.set(key, String(value));
  }
  removeItem(key: string): void {
    this.store.delete(key);
  }
  clear(): void {
    this.store.clear();
  }
}

beforeAll(() => {
  // @ts-expect-error see stores/keybindings.test.ts's MemoryStorage comment
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  vi.resetModules();
});

const getMethod = "evener/settings/keybindings/get";

async function seededConnection(supported: boolean) {
  const { connectionStore } = await import("./connection");
  const client = new FakeClient("ready");
  client.on(getMethod, () => ({ version: 1, revision: 3, rules: [] }));
  connectionStore.getState().connect(client);
  connectionStore.setState({
    features: { ...(await client.connect()).features, keybindingsSettings: supported },
  });
  return client;
}

test("a module evaluating after the handshake loads the hub's overrides exactly once", async () => {
  const client = await seededConnection(true);

  const { keybindingsStore } = await import("./keybindings");

  await vi.waitFor(() => expect(keybindingsStore.getState().revision).toBe(3));
  expect(keybindingsStore.getState()).toMatchObject({ hubSupport: "supported", loaded: true });
  // Let any second kick land before counting.
  await Promise.resolve();
  expect(client.calls.filter((c) => c.method === getMethod)).toHaveLength(1);
});

test("a module evaluating after an unsupported handshake publishes unsupported and issues no request", async () => {
  const client = await seededConnection(false);

  const { keybindingsStore } = await import("./keybindings");

  expect(keybindingsStore.getState().hubSupport).toBe("unsupported");
  await Promise.resolve();
  expect(client.calls.filter((c) => c.method === getMethod)).toHaveLength(0);
});
