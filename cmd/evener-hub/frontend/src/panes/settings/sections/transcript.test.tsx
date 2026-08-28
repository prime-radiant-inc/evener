import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../../protocol/errors";
import { FakeClient } from "../../../protocol/testing/fakeClient";
import type { AnyNotification } from "../../../protocol/types.gen";
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

const originalPatchHubDefault = transcriptDisplayStore.getState().patchHubDefault;
const activeObservers = new Set<MutationObserver>();

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

class FailingStorage extends MemoryStorage {
  setItem(): void {
    throw new Error("storage blocked");
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
  for (const observer of activeObservers) observer.disconnect();
  activeObservers.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  connectionStore.setState({ state: "idle", serverInfo: undefined, features: undefined, client: null });
  resetTranscriptDisplayStoreForTests();
  transcriptDisplayStore.setState({ patchHubDefault: originalPatchHubDefault });
});

function observeAlertInsertions(): { messages: string[]; stop(): void } {
  const messages: string[] = [];
  const observer = new MutationObserver((records) => {
    for (const record of records) {
      for (const node of record.addedNodes) {
        if (!(node instanceof HTMLElement)) continue;
        const alert = node.matches('[role="alert"]') ? node : node.querySelector<HTMLElement>('[role="alert"]');
        if (alert !== null) messages.push(alert.textContent ?? "");
      }
    }
  });
  observer.observe(document.body, { childList: true, subtree: true });
  activeObservers.add(observer);
  return {
    messages,
    stop: () => {
      observer.disconnect();
      activeObservers.delete(observer);
    },
  };
}

function renderWithToasts() {
  render(
    <>
      <TranscriptSection />
      <Toast />
    </>,
  );
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void; reject(error: unknown): void } {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((next, fail) => {
    resolve = next;
    reject = fail;
  });
  return { promise, resolve, reject };
}

async function mountReadySection(
  client: FakeClient,
  confirmed = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" }),
) {
  client.on("evener/settings/transcriptDisplay/get", () => ({
    desktop: { revision: 1, config: toWireConfig(confirmed) },
    mobile: { revision: 1, config: toWireConfig(shippedDefault("mobile").config) },
  }));
  connectionStore.getState().connect(client);
  connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
  renderWithToasts();
  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1));
  await waitFor(() => expect(transcriptDisplayStore.getState().hubLoading).toBe(false));
}

test("renders Desktop then Mobile stacked cards with hub-sync and browser-local scope copy", () => {
  transcriptDisplayStore.setState({ hubSupport: "supported" });
  renderWithToasts();

  const cards = screen.getAllByRole("article");
  expect(cards.map((card) => card.getAttribute("data-testid"))).toEqual([
    "transcript-display-card-desktop",
    "transcript-display-card-mobile",
  ]);
  expect(
    screen.getByText(
      "Transcript display defaults sync to devices paired with this hub. Live transcript choices remain browser-local.",
    ),
  ).toBeTruthy();
  expect(
    screen.getAllByText(
      "Transcript display defaults sync to devices paired with this hub. Live transcript choices remain browser-local.",
    ),
  ).toHaveLength(1);
  expect(cards.every((card) => card.querySelector(":scope > div") !== null)).toBe(true);
  expect(screen.getAllByText("Example only—not your data")).toHaveLength(2);
});

test("shows an accessible status for an unknown or older hub", () => {
  renderWithToasts();
  expect(screen.getAllByRole("status")).toHaveLength(1);
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
  expect(screen.getAllByRole("alert")).toHaveLength(1);
  expect(screen.getByRole("alert").textContent).toContain("server unavailable");
  expect(screen.getAllByText(/server unavailable/)).toHaveLength(1);
  expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  expect(screen.queryByText("Settings saved")).toBeNull();
  expect(screen.getByRole("region", { name: "Notifications" }).textContent).toBe("");
});

test("keeps browser-storage failure inline without a failure notification", async () => {
  transcriptDisplayStore.setState({ hubSupport: "supported" });
  renderWithToasts();
  vi.stubGlobal("localStorage", new FailingStorage());
  transcriptDisplayStore.getState().setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "full" }));

  expect(transcriptDisplayStore.getState().storageWarning).toMatch(/may not survive restart/);
  await waitFor(() => expect(screen.getAllByRole("alert")).toHaveLength(1));
  expect(screen.getByRole("alert").textContent).toContain("Browser-local storage warning");
  expect(screen.getByRole("region", { name: "Notifications" }).textContent).toBe("");
});

test("reverts a failed hub mutation and retries the last draft", async () => {
  const client = new FakeClient("ready");
  const confirmed = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const draft = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  type PatchResult = { layout: "desktop"; revision: number; config: ReturnType<typeof toWireConfig> };
  const retryResponse = deferred<PatchResult>();
  let patchCalls = 0;
  client.on("evener/settings/transcriptDisplay/get", () => ({
    desktop: { revision: 1, config: toWireConfig(confirmed) },
    mobile: { revision: 1, config: toWireConfig(shippedDefault("mobile").config) },
  }));
  client.on("evener/settings/transcriptDisplay/patch", () => {
    patchCalls += 1;
    if (patchCalls === 1) throw new Error("server unavailable");
    return retryResponse.promise;
  });
  connectionStore.getState().connect(client);
  connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
  renderWithToasts();
  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1));

  const alertInsertions = observeAlertInsertions();
  await userEvent.setup().click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  expect((await screen.findByRole("alert")).textContent).toContain("server unavailable");
  expect(screen.getAllByRole("alert")).toHaveLength(1);
  await waitFor(() => expect(alertInsertions.messages).toHaveLength(1));
  expect(alertInsertions.messages[0]).toContain("server unavailable");
  expect(screen.getAllByText(/server unavailable/)).toHaveLength(1);
  expect(screen.getByRole("region", { name: "Notifications" }).textContent).toBe("");
  expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
  expect(transcriptDisplayStore.getState().hub.desktop?.config).toEqual(confirmed);
  expect(screen.queryByText("Settings saved")).toBeNull();

  await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
  expect(screen.getAllByRole("status")).toHaveLength(1);
  expect(screen.getByRole("status").textContent).toContain("Saving");
  expect(screen.queryByText(/Could not load transcript display defaults/)).toBeNull();
  retryResponse.resolve({ layout: "desktop", revision: 2, config: toWireConfig(draft) });
  expect(await screen.findByText("Settings saved")).toBeTruthy();
  expect(screen.getByRole("region", { name: "Notifications" }).textContent).toContain("Settings saved");
  alertInsertions.stop();
  expect(patchCalls).toBe(2);
});

test("does not acknowledge a canonical response for a different requested config", async () => {
  const client = new FakeClient("ready");
  const confirmed = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const canonicalWinner = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
  client.on("evener/settings/transcriptDisplay/get", () => ({
    desktop: { revision: 1, config: toWireConfig(confirmed) },
    mobile: { revision: 1, config: toWireConfig(shippedDefault("mobile").config) },
  }));
  client.on("evener/settings/transcriptDisplay/patch", () => ({
    layout: "desktop",
    revision: 2,
    config: toWireConfig(canonicalWinner),
  }));
  connectionStore.getState().connect(client);
  connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
  vi.spyOn(transcriptDisplayStore.getState(), "patchHubDefault").mockResolvedValue({
    revision: 2,
    config: canonicalWinner,
  });
  renderWithToasts();
  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1));

  await userEvent.setup().click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  expect((await screen.findByRole("alert")).textContent).toContain("did not acknowledge");
  expect(screen.queryByText("Settings saved")).toBeNull();
  expect(screen.getByRole("region", { name: "Notifications" }).textContent).toBe("");
  expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
});

test("acknowledges an ordinary same-config response after the store clears its draft", async () => {
  const client = new FakeClient("ready");
  const confirmed = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const requested = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
  client.on("evener/settings/transcriptDisplay/get", () => ({
    desktop: { revision: 1, config: toWireConfig(confirmed) },
    mobile: { revision: 1, config: toWireConfig(shippedDefault("mobile").config) },
  }));
  client.on("evener/settings/transcriptDisplay/patch", () => ({
    layout: "desktop",
    revision: 2,
    config: toWireConfig(requested),
  }));
  connectionStore.getState().connect(client);
  connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
  vi.spyOn(transcriptDisplayStore.getState(), "patchHubDefault").mockResolvedValue({ revision: 2, config: requested });
  renderWithToasts();
  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1));
  await waitFor(() => expect(transcriptDisplayStore.getState().hubLoading).toBe(false));

  await userEvent.setup().click(screen.getAllByRole("radio", { name: "Activity" })[0]!);
  expect(await screen.findByText("Settings saved")).toBeTruthy();
  expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
});

test("allows the winner-first concurrent save to toast exactly once", async () => {
  const client = new FakeClient("ready");
  const firstDraft = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const winnerDraft = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
  type PatchResult = { layout: "desktop"; revision: number; config: ReturnType<typeof toWireConfig> };
  const responses: Array<ReturnType<typeof deferred<PatchResult>>> = [];
  client.on("evener/settings/transcriptDisplay/patch", () => {
    const response = deferred<PatchResult>();
    responses.push(response);
    return response.promise;
  });
  await mountReadySection(client);
  const user = userEvent.setup();
  await user.click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  await user.click(screen.getAllByRole("radio", { name: "Activity" })[0]!);
  await waitFor(() => expect(responses).toHaveLength(2));

  responses[1]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(winnerDraft) });
  expect(await screen.findByText("Settings saved")).toBeTruthy();
  responses[0]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(firstDraft) });
  await waitFor(() => expect(screen.getAllByText("Settings saved")).toHaveLength(1));
  expect(screen.queryByRole("alert")).toBeNull();
});

test("does not let a stale A response duplicate the current A success after an A-B-A overlap", async () => {
  const client = new FakeClient("ready");
  const firstA = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const middleB = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
  type PatchResult = { layout: "desktop"; revision: number; config: ReturnType<typeof toWireConfig> };
  const responses: Array<ReturnType<typeof deferred<PatchResult>>> = [];
  client.on("evener/settings/transcriptDisplay/patch", () => {
    const response = deferred<PatchResult>();
    responses.push(response);
    return response.promise;
  });
  await mountReadySection(client);
  const user = userEvent.setup();
  await user.click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  await user.click(screen.getAllByRole("radio", { name: "Activity" })[0]!);
  await user.click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  await waitFor(() => expect(responses).toHaveLength(3));

  await act(async () => {
    responses[2]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(firstA) });
    await responses[2]?.promise;
  });
  expect(await screen.findByText("Settings saved")).toBeTruthy();

  await act(async () => {
    responses[0]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(firstA) });
    responses[1]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(middleB) });
    await Promise.all([responses[0]?.promise, responses[1]?.promise]);
  });
  expect(screen.getAllByText("Settings saved")).toHaveLength(1);
  expect(screen.queryByRole("alert")).toBeNull();
});

test("does not insert a transient alert for the loser-first concurrent save", async () => {
  const client = new FakeClient("ready");
  const firstDraft = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const winnerDraft = makeTranscriptDisplayConfig({ kind: "preset", level: "activity" });
  type PatchResult = { layout: "desktop"; revision: number; config: ReturnType<typeof toWireConfig> };
  const responses: Array<ReturnType<typeof deferred<PatchResult>>> = [];
  client.on("evener/settings/transcriptDisplay/patch", () => {
    const response = deferred<PatchResult>();
    responses.push(response);
    return response.promise;
  });
  await mountReadySection(client);
  const alertInsertions = observeAlertInsertions();
  const user = userEvent.setup();
  await user.click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  await user.click(screen.getAllByRole("radio", { name: "Activity" })[0]!);
  await waitFor(() => expect(responses).toHaveLength(2));

  responses[0]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(firstDraft) });
  responses[1]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(winnerDraft) });
  expect(await screen.findByText("Settings saved")).toBeTruthy();
  expect(alertInsertions.messages).toEqual([]);
  alertInsertions.stop();
});

test("acknowledges a notification received before its matching save response", async () => {
  const client = new FakeClient("ready");
  const draft = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const response = deferred<{ layout: "desktop"; revision: number; config: ReturnType<typeof toWireConfig> }>();
  client.on("evener/settings/transcriptDisplay/patch", () => response.promise);
  await mountReadySection(client);
  await userEvent.setup().click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  client.emitNotification({
    method: "evener/settings/transcriptDisplay/changed",
    params: { layout: "desktop", revision: 2, config: toWireConfig(draft) },
  } as AnyNotification);
  response.resolve({ layout: "desktop", revision: 2, config: toWireConfig(draft) });

  expect(await screen.findByText("Settings saved")).toBeTruthy();
  expect(screen.getAllByText("Settings saved")).toHaveLength(1);
});

test("does not acknowledge a save from a stale client generation", async () => {
  const client = new FakeClient("idle");
  const confirmed = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const draft = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  const response = deferred<{ layout: "desktop"; revision: number; config: ReturnType<typeof toWireConfig> }>();
  let refreshes = 0;
  let reconnecting = false;
  client.on("evener/settings/transcriptDisplay/get", () => {
    refreshes += 1;
    return {
      desktop: { revision: reconnecting ? 2 : 1, config: toWireConfig(reconnecting ? draft : confirmed) },
      mobile: { revision: 1, config: toWireConfig(shippedDefault("mobile").config) },
    };
  });
  client.on("evener/settings/transcriptDisplay/patch", () => response.promise);
  connectionStore.getState().connect(client);
  connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
  client.emitReady();
  renderWithToasts();
  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1));
  await waitFor(() => expect(transcriptDisplayStore.getState().hubLoading).toBe(false));
  await userEvent.setup().click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  const refreshCountBeforeReconnect = refreshes;
  reconnecting = true;
  client.emitStateChange("idle");
  client.emitReady();
  await waitFor(() =>
    expect(
      client.calls.filter((call) => call.method === "evener/settings/transcriptDisplay/get").length,
    ).toBeGreaterThan(refreshCountBeforeReconnect),
  );
  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.config).toEqual(draft));
  expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(draft);
  response.resolve({ layout: "desktop", revision: 2, config: toWireConfig(draft) });

  expect((await screen.findByRole("alert")).textContent).toContain("did not acknowledge");
  expect(screen.queryByText("Settings saved")).toBeNull();
});

test("keeps Desktop and Mobile pending saves, failures, retry, and Toasts independent", async () => {
  const client = new FakeClient("ready");
  const draft = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  type PatchResult = { layout: "desktop" | "mobile"; revision: number; config: ReturnType<typeof toWireConfig> };
  const responses: Array<{ layout: PatchResult["layout"]; response: ReturnType<typeof deferred<PatchResult>> }> = [];
  client.on("evener/settings/transcriptDisplay/patch", (params) => {
    const response = deferred<PatchResult>();
    if (params.layout !== "desktop" && params.layout !== "mobile") throw new Error("unexpected layout");
    responses.push({ layout: params.layout, response });
    return response.promise;
  });
  await mountReadySection(client);
  const user = userEvent.setup();
  await user.click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  await user.click(screen.getAllByRole("radio", { name: "Full detail" })[1]!);
  await waitFor(() => expect(responses).toHaveLength(2));
  const desktop = screen.getByTestId("transcript-display-card-desktop");
  const mobile = screen.getByTestId("transcript-display-card-mobile");
  expect(screen.getAllByRole("status")).toHaveLength(2);

  responses.find((entry) => entry.layout === "desktop")?.response.reject(new Error("desktop unavailable"));
  expect((await within(desktop).findByRole("alert")).textContent).toContain("desktop unavailable");
  expect(within(mobile).getByRole("status").textContent).toContain("Saving");
  expect(screen.getByRole("region", { name: "Notifications" }).textContent).toBe("");

  await user.click(within(desktop).getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(responses).toHaveLength(3));
  expect(screen.getAllByRole("status")).toHaveLength(2);
  responses
    .find((entry) => entry.layout === "mobile")
    ?.response.resolve({ layout: "mobile", revision: 2, config: toWireConfig(draft) });
  responses[2]?.response.resolve({ layout: "desktop", revision: 2, config: toWireConfig(draft) });
  await waitFor(() => expect(screen.getAllByText("Settings saved")).toHaveLength(2));
  expect(screen.queryByRole("alert")).toBeNull();
  expect(transcriptDisplayStore.getState().drafts).toEqual({});
});

test("does not toast when an overlapping PATCH conflicts with the committed winner", async () => {
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

  responses[1]?.reject(
    new WireError("revision conflict", -32013, {
      evenerErrorInfo: "conflict",
      layout: "desktop",
      current: { revision: 2, config: toWireConfig(firstDraft) },
    }),
  );
  await expect(competingPatch).rejects.toThrow("revision conflict");
  expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 2, config: firstDraft });
  expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
  expect(screen.queryByText("Settings saved")).toBeNull();
});

test("reports a conflict instead of acknowledging two patches with one expected revision", async () => {
  const client = new FakeClient("ready");
  const confirmed = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  const firstDraft = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
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
  responses[1]?.reject(
    new WireError("revision conflict", -32013, {
      evenerErrorInfo: "conflict",
      layout: "desktop",
      current: { revision: 2, config: toWireConfig(firstDraft) },
    }),
  );

  expect((await screen.findByRole("alert")).textContent).toContain("revision conflict");
  expect(screen.queryByText("Settings saved")).toBeNull();
  expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
  expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 2, config: firstDraft });
  expect(screen.getAllByRole("button", { name: "Retry" }).length).toBeGreaterThan(0);
});
