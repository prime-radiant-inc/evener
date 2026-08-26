import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test } from "vitest";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import { connectionStore } from "../../../stores/connection";
import {
  initTranscriptDisplay,
  resetTranscriptDisplayStoreForTests,
  transcriptDisplayStore,
} from "../../../stores/transcriptDisplay";
import { makeTranscriptDisplayConfig, shippedDefault, toWireConfig } from "../../../transcriptDisplay/config";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { TranscriptSection } from "./transcript";

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
  // @ts-expect-error MemoryStorage is the deterministic browser storage seam.
  globalThis.localStorage = new MemoryStorage();
});

beforeEach(() => {
  localStorage.clear();
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetTranscriptDisplayStoreForTests();
  initTranscriptDisplay();
  resetToastStoreForTests();
});

afterEach(() => {
  cleanup();
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetTranscriptDisplayStoreForTests();
});

function renderWithToasts() {
  render(
    <>
      <TranscriptSection />
      <Toast />
    </>,
  );
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((next) => {
    resolve = next;
  });
  return { promise, resolve };
}

test("renders Desktop then Mobile stacked cards with hub-sync and browser-local scope copy", () => {
  transcriptDisplayStore.setState({ hubSupport: "supported" });
  renderWithToasts();

  const cards = screen.getAllByRole("article");
  expect(cards.map((card) => card.getAttribute("data-testid"))).toEqual([
    "transcript-display-card-desktop",
    "transcript-display-card-mobile",
  ]);
  expect(screen.getByText(/Hub defaults sync to devices paired with this hub\./)).toBeTruthy();
  expect(
    screen.getByText(/A live transcript choice is browser-local and does not change another machine\./),
  ).toBeTruthy();
  expect(screen.getAllByText("Example only—not your data")).toHaveLength(2);
});

test("shows an accessible status for an unknown or older hub", () => {
  renderWithToasts();
  expect(screen.getByRole("status").textContent).toMatch(/Waiting/);
  expect(screen.getByText(/Waiting for the hub connection to report transcript display support/)).toBeTruthy();

  cleanup();
  transcriptDisplayStore.setState({ hubSupport: "unsupported" });
  renderWithToasts();
  expect(screen.getByText(/older hub does not support synced transcript defaults/)).toBeTruthy();
  expect(screen.getAllByRole("radio").every((radio) => (radio as HTMLButtonElement).disabled)).toBe(true);
});

test("keeps the preview on the draft until the hub acknowledges the canonical response", async () => {
  const client = new FakeClient("ready");
  const confirmed = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const draft = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const patchResult = {
    layout: "desktop" as const,
    revision: 2,
    config: toWireConfig(draft),
  };
  let releasePatch!: (value: typeof patchResult) => void;
  const pendingPatch = new Promise<typeof patchResult>((resolve) => {
    releasePatch = resolve;
  });
  client.on("evener/settings/transcriptDisplay/get", () => ({
    desktop: { revision: 1, config: toWireConfig(confirmed) },
    mobile: { revision: 1, config: toWireConfig(shippedDefault("mobile").config) },
  }));
  client.on("evener/settings/transcriptDisplay/patch", () => pendingPatch);
  connectionStore.getState().connect(client);
  connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
  renderWithToasts();

  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1));
  await userEvent.setup().click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(draft);
  expect(screen.queryByText("Settings saved")).toBeNull();
  expect(screen.getByRole("status").textContent).toMatch(/Saving hub default/);

  releasePatch(patchResult);
  expect(await screen.findByText("Settings saved")).toBeTruthy();
  expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
  expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(2);
});

test("renders a server error as an alert with retry instead of a saved state", () => {
  transcriptDisplayStore.setState({ hubSupport: "supported", hubError: "server unavailable" });
  renderWithToasts();
  expect(screen.getByRole("alert").textContent).toContain("server unavailable");
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  expect(screen.queryByText("Settings saved")).toBeNull();
});

test("reverts a failed hub mutation and retries the last draft", async () => {
  const client = new FakeClient("ready");
  const confirmed = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  let patchCalls = 0;
  client.on("evener/settings/transcriptDisplay/get", () => ({
    desktop: { revision: 1, config: toWireConfig(confirmed) },
    mobile: { revision: 1, config: toWireConfig(shippedDefault("mobile").config) },
  }));
  client.on("evener/settings/transcriptDisplay/patch", (params) => {
    patchCalls += 1;
    if (patchCalls === 1) throw new Error("server unavailable");
    return { layout: params.layout, revision: 2, config: params.config };
  });
  connectionStore.getState().connect(client);
  connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
  renderWithToasts();
  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1));

  await userEvent.setup().click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  expect((await screen.findByRole("alert")).textContent).toContain("server unavailable");
  expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
  expect(transcriptDisplayStore.getState().hub.desktop?.config).toEqual(confirmed);
  expect(screen.queryByText("Settings saved")).toBeNull();

  await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
  expect(await screen.findByText("Settings saved")).toBeTruthy();
  expect(patchCalls).toBe(2);
});

test("does not toast when a resolved PATCH is stale and preserves the newer draft", async () => {
  const client = new FakeClient("ready");
  const confirmed = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const firstDraft = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const newerDraft = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
  type PatchResult = { layout: "desktop"; revision: number; config: ReturnType<typeof toWireConfig> };
  const responses: Array<ReturnType<typeof deferred<PatchResult>>> = [];
  client.on("evener/settings/transcriptDisplay/get", () => ({
    desktop: { revision: 1, config: toWireConfig(confirmed) },
    mobile: { revision: 1, config: toWireConfig(shippedDefault("mobile").config) },
  }));
  client.on("evener/settings/transcriptDisplay/patch", () => {
    const response = deferred<PatchResult>();
    responses.push(response);
    return response.promise;
  });
  connectionStore.getState().connect(client);
  connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
  renderWithToasts();
  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1));

  await userEvent.setup().click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  await waitFor(() => expect(responses).toHaveLength(1));
  const competingPatch = transcriptDisplayStore.getState().patchHubDefault("desktop", newerDraft);
  await waitFor(() => expect(responses).toHaveLength(2));

  responses[0]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(firstDraft) });
  expect((await screen.findByRole("alert")).textContent).toContain("did not acknowledge");
  expect(screen.queryByText("Settings saved")).toBeNull();
  expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(newerDraft);

  responses[1]?.resolve({ layout: "desktop", revision: 3, config: toWireConfig(newerDraft) });
  await competingPatch;
  expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 3, config: newerDraft });
  expect(screen.queryByText("Settings saved")).toBeNull();
});

test("allows only the newest overlapping Settings save to acknowledge and toast", async () => {
  const client = new FakeClient("ready");
  const confirmed = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const firstDraft = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const newestDraft = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
  type PatchResult = { layout: "desktop"; revision: number; config: ReturnType<typeof toWireConfig> };
  const responses: Array<ReturnType<typeof deferred<PatchResult>>> = [];
  client.on("evener/settings/transcriptDisplay/get", () => ({
    desktop: { revision: 1, config: toWireConfig(confirmed) },
    mobile: { revision: 1, config: toWireConfig(shippedDefault("mobile").config) },
  }));
  client.on("evener/settings/transcriptDisplay/patch", () => {
    const response = deferred<PatchResult>();
    responses.push(response);
    return response.promise;
  });
  connectionStore.getState().connect(client);
  connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
  renderWithToasts();
  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1));

  const user = userEvent.setup();
  await user.click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  await user.click(screen.getAllByRole("radio", { name: "Activity" })[0]!);
  await waitFor(() => expect(responses).toHaveLength(2));
  responses[0]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(firstDraft) });
  responses[1]?.resolve({ layout: "desktop", revision: 3, config: toWireConfig(newestDraft) });

  expect(await screen.findAllByText("Settings saved")).toHaveLength(1);
  expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
  expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 3, config: newestDraft });
});
