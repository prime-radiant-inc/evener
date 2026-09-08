import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Component, type ReactNode } from "react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import { ClientProvider } from "../../shell/clientContext";
import { connectionStore } from "../../stores/connection";
import { resetCredentialsStoreForTests } from "../../stores/credentials";
import { resetExtensionsStoreForTests } from "../../stores/extensions";
import { Toast } from "../../widgets";
import { resetToastStoreForTests } from "../../widgets/toast/store";
import * as connectDialogChunk from "./connectDialogChunk";
import { resetConnectDialogLoaderForTests } from "./connectDialogChunk";
import Spawn, { resetConnectDialogChunkForTests } from "./Spawn";

// The connect-provider dialog chunk is a separate network request from
// index.html, so a hub restarting mid-load, a slow link, or a deploy that
// replaced the hashed filename all land on a rejected import() - the
// browser's own "Failed to fetch dynamically imported module". Replacing the
// loader is that failure with no network involved; the real dialog (plus its
// instance-credential editors) never loads here.
//
// The spy goes on the namespace import (DockRegion.test.tsx's own recipe):
// under isolate:false the module registry is shared by every file in the
// worker, so a hoisted vi.mock would fix whichever file instantiated
// Spawn.tsx first to whatever was in effect at that moment. Mutating the
// shared connectDialogChunk module object's own `loadConnectDialog`
// property in place keeps Spawn.tsx's live binding pointed at the spy's
// current implementation, and mockRestore() in afterEach hands the real
// function back for whatever file runs next.
const realLoadConnectDialog = connectDialogChunk.loadConnectDialog;
const loadConnectDialog = vi.spyOn(connectDialogChunk, "loadConnectDialog");

const CHUNK_ERROR = "Failed to fetch dynamically imported module: /webassets/ConnectProviderDialog-a1b2c3.js";

function StubConnectDialog({ onClose }: { onClose(): void; onConnected(): void }) {
  return (
    <div role="dialog" aria-label="stub connect dialog">
      <p>connect dialog mounted</p>
      <button type="button" onClick={onClose}>
        Close stub
      </button>
    </div>
  );
}

// Node 26 shadows jsdom's real localStorage with a non-functional global
// under vitest; Spawn reads spawn-defaults through it on mount - the same
// in-memory stand-in every spawn test uses.
class MemoryStorage {
  private store = new Map<string, string>();
  get length(): number {
    return this.store.size;
  }
  key(index: number): string | null {
    return Array.from(this.store.keys())[index] ?? null;
  }
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

// A ready FakeClient with no configured provider, so the spawn pane offers
// the "Connect provider" button that opens the lazy dialog.
function missingCredentialsClient(): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  fake.on("evener/harnesses/list", () => ({ data: [{ id: "evener", label: "evener", kind: "evener" }] }));
  fake.on("evener/launch/schema", () => ({ options: [] }));
  fake.on("model/list", () => ({ data: [] }));
  fake.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  fake.on("evener/projects/recent", () => ({ data: [] }));
  fake.on("evener/paths/complete", () => ({ data: [] }));
  fake.on("evener/path/validate", () => ({ path: "", valid: true }));
  fake.on("evener/dirs/create", ({ path }) => ({ path, created: true }));
  fake.on("evener/git/head", () => ({ head: "main" }));
  fake.on("evener/plugin/preview", () => ({ plugins: [] }));
  fake.on("thread/start", () => {
    throw new Error("not under test");
  });
  return fake;
}

function renderSpawn(client: FakeClient) {
  return render(
    <ClientProvider client={client}>
      <Spawn params={{}} paneId="spawn-1" focused={true} />
      <Toast />
    </ClientProvider>,
  );
}

async function openConnectDialog(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await user.click(await screen.findByRole("button", { name: "Connect provider" }));
}

// Suppress console.error noise from React's error-boundary logging during
// tests that deliberately trigger chunk-load failures. The errors are
// expected; the boundary catches them. Matched on React's own stable
// componentDidCatch format string plus its fixed boundary-recovery
// sentence, rather than blanket-silenced, so any *other* console.error a
// regression here might produce still reaches real console.error and
// stays visible in test output (DockRegion.test.tsx's own recipe).
const REACT_ERROR_BOUNDARY_FORMAT = "%o\n\n%s\n\n%s\n";
const REACT_ERROR_BOUNDARY_PREFACE = "The above error occurred in one of your React components.";
const realConsoleError = console.error.bind(console);
let consoleErrorSpy: ReturnType<typeof vi.spyOn>;

beforeAll(() => {
  globalThis.localStorage = new MemoryStorage() as unknown as Storage;
});

beforeEach(() => {
  consoleErrorSpy = vi.spyOn(console, "error").mockImplementation((...args: unknown[]) => {
    if (args[0] === REACT_ERROR_BOUNDARY_FORMAT && args[2] === REACT_ERROR_BOUNDARY_PREFACE) {
      return;
    }
    realConsoleError(...args);
  });
  localStorage.clear();
  resetCredentialsStoreForTests();
  loadConnectDialog.mockReset();
  loadConnectDialog.mockImplementation(realLoadConnectDialog);
  // The dialog chunk is one shared lazy() payload per page load, so each test
  // needs its own - a payload that resolved (or rejected) in the last test
  // would never call this test's loader at all.
  resetConnectDialogChunkForTests();
  resetConnectDialogLoaderForTests();
});

afterEach(() => {
  consoleErrorSpy.mockRestore();
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  vi.unstubAllGlobals();
  window.history.pushState({}, "", "/");
  resetToastStoreForTests();
  // Whichever override the LAST test set (mockRejectedValue/mockResolvedValue/
  // mockResolvedValueOnce...) would otherwise still be armed on this shared
  // spy for the next file in the worker that calls the real loadConnectDialog -
  // see this file's own comment on the vi.spyOn call above.
  loadConnectDialog.mockReset();
  loadConnectDialog.mockImplementation(realLoadConnectDialog);
});

test("a rejected dialog chunk shows the failure message with a Retry", async () => {
  vi.mocked(loadConnectDialog).mockRejectedValue(new Error(CHUNK_ERROR));
  const user = userEvent.setup();
  const client = missingCredentialsClient();
  connectionStore.getState().connect(client);
  renderSpawn(client);

  await openConnectDialog(user);

  expect(await screen.findByText("Couldn't load the connect dialog")).toBeTruthy();
  expect(screen.getByText(CHUNK_ERROR)).toBeTruthy();
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
});

test("Retry fetches the chunk again and mounts the dialog on the second attempt", async () => {
  vi.mocked(loadConnectDialog)
    .mockRejectedValueOnce(new Error(CHUNK_ERROR))
    .mockResolvedValueOnce({ ConnectProviderDialog: StubConnectDialog } as never);
  const user = userEvent.setup();
  const client = missingCredentialsClient();
  connectionStore.getState().connect(client);
  renderSpawn(client);

  await openConnectDialog(user);
  await screen.findByText("Couldn't load the connect dialog");
  await user.click(screen.getByRole("button", { name: "Retry" }));

  // Both halves of a retry, in one assertion each: the dialog it returns
  // replaces the failure state, and the second attempt asks the loader for
  // the cache-busted path. A second same-URL import does not reach Chrome's
  // network stack - it replays the cached failure.
  expect(await screen.findByText("connect dialog mounted")).toBeTruthy();
  expect(vi.mocked(loadConnectDialog).mock.calls).toEqual([[false], [true]]);
  expect(screen.queryByText("Couldn't load the connect dialog")).toBeNull();
});

test("a retry that fails again offers a page reload instead of stranding the provider flow", async () => {
  vi.mocked(loadConnectDialog).mockRejectedValue(new Error(CHUNK_ERROR));
  const reload = vi.fn();
  vi.stubGlobal("location", { ...window.location, reload });
  const user = userEvent.setup();
  const client = missingCredentialsClient();
  connectionStore.getState().connect(client);
  renderSpawn(client);

  await openConnectDialog(user);
  await screen.findByText("Couldn't load the connect dialog");
  // The first failure offers only the cache-busted retry: a deploy that
  // removed the hashed chunk is still only one hypothesis among transient
  // ones, so the reload is the second-strike path, not the first.
  expect(screen.queryByRole("button", { name: "Reload page" })).toBeNull();

  await user.click(screen.getByRole("button", { name: "Retry" }));
  await user.click(await screen.findByRole("button", { name: "Reload page" }));

  expect(reload).toHaveBeenCalledTimes(1);
  expect(vi.mocked(loadConnectDialog).mock.calls).toEqual([[false], [true]]);
});

test("an ordinary retry failure does not prescribe a page reload", async () => {
  // A chunk fetch that fails WITHOUT naming a stale hashed asset (a 500
  // page's HTML where the chunk bytes should be) is not a chunk-load
  // failure: the dialog boundary declines it, so it lands on the next
  // boundary above - never the dialog failure state, and so never the
  // Retry/Reload pair that could misreport it as a stale deploy.
  vi.mocked(loadConnectDialog).mockRejectedValue(new Error("ConnectProviderDialog chunk request failed with 500"));
  const client = missingCredentialsClient();
  connectionStore.getState().connect(client);
  const user = userEvent.setup();
  render(
    <ClientProvider client={client}>
      <DialogTestOuterBoundary>
        <Spawn params={{}} paneId="spawn-1" focused={true} />
      </DialogTestOuterBoundary>
      <Toast />
    </ClientProvider>,
  );

  await openConnectDialog(user);

  expect(
    await screen.findByText("outer boundary caught: ConnectProviderDialog chunk request failed with 500"),
  ).toBeTruthy();
  expect(screen.queryByText("Couldn't load the connect dialog")).toBeNull();
  expect(screen.queryByRole("button", { name: "Reload page" })).toBeNull();
});

// A logic bug thrown by the RESOLVED dialog's own render is not a chunk-load
// failure: the dialog boundary must let it keep unwinding to the next
// boundary above instead of misreporting it as a failed fetch with a Retry.
class DialogTestOuterBoundary extends Component<{ children: ReactNode }, { failure: string | null }> {
  state = { failure: null as string | null };
  static getDerivedStateFromError(error: unknown) {
    return { failure: error instanceof Error ? error.message : String(error) };
  }
  render(): ReactNode {
    if (this.state.failure !== null) return <p>outer boundary caught: {this.state.failure}</p>;
    return this.props.children;
  }
}

test("a logic bug in the resolved dialog keeps unwinding past the dialog boundary", async () => {
  function BuggyDialog() {
    throw new Error("ConnectProviderDialog render logic bug");
  }
  vi.mocked(loadConnectDialog).mockResolvedValue({ ConnectProviderDialog: BuggyDialog } as never);
  const user = userEvent.setup();
  const client = missingCredentialsClient();
  connectionStore.getState().connect(client);
  render(
    <ClientProvider client={client}>
      <DialogTestOuterBoundary>
        <Spawn params={{}} paneId="spawn-1" focused={true} />
      </DialogTestOuterBoundary>
      <Toast />
    </ClientProvider>,
  );

  await openConnectDialog(user);

  expect(await screen.findByText("outer boundary caught: ConnectProviderDialog render logic bug")).toBeTruthy();
  expect(screen.queryByText("Couldn't load the connect dialog")).toBeNull();
});
