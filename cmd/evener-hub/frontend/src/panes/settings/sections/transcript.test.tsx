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
    config: toWireConfig(confirmed),
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
