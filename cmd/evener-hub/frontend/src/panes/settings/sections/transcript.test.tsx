import type { AnyNotification } from "@evener/appwire-client";
import {
  ConnectionClosedError,
  friendlyErrorMessage,
  makeTranscriptDisplayConfig,
  RequestTimeoutError,
  shippedDefault,
  toWireConfig,
  WireError,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { connectionStore } from "../../../stores/connection";
import {
  initTranscriptDisplay,
  resetTranscriptDisplayStoreForTests,
  transcriptDisplayStore,
} from "../../../stores/transcriptDisplay";
import { Toast } from "../../../widgets";
import { resetToastStoreForTests } from "../../../widgets/toast/store";
import { toastKinds } from "../../../widgets/toast/toastTestUtils";
import { TranscriptSection } from "./transcript";

const originalPatchHubDefault = transcriptDisplayStore.getState().patchHubDefault;
const activeObservers = new Set<MutationObserver>();

class FailingStorage extends MemoryStorage {
  setItem(): void {
    throw new Error("storage blocked");
  }
}

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
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

function successToasts(): string[] {
  return toastKinds().filter((kind) => kind === "success");
}

async function savedOnce(): Promise<void> {
  await waitFor(() => expect(successToasts()).toHaveLength(1));
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

test.each([false, true])("shows one layout initialized from the current viewport, mobile=%s", async (mobile) => {
  vi.stubGlobal("matchMedia", () => ({ matches: mobile, addEventListener() {}, removeEventListener() {} }));
  transcriptDisplayStore.setState({ hubSupport: "supported" });
  renderWithToasts();
  expect(screen.getAllByRole("article").map((card) => card.getAttribute("data-testid"))).toEqual([
    `transcript-display-card-${mobile ? "mobile" : "desktop"}`,
  ]);
  await userEvent.setup().click(screen.getByRole("radio", { name: mobile ? "Desktop" : "Mobile" }));
  expect(screen.getAllByRole("article").map((card) => card.getAttribute("data-testid"))).toEqual([
    `transcript-display-card-${mobile ? "desktop" : "mobile"}`,
  ]);
  expect(screen.getByTestId(`transcript-display-preview-canvas-${mobile ? "desktop" : "mobile"}`)).toBeTruthy();
});

test("clearing the selected browser override follows shared defaults without writing them", async () => {
  const client = new FakeClient("ready");
  await mountReadySection(client);
  const override = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });
  act(() => {
    transcriptDisplayStore.getState().setLocal("desktop", override);
    transcriptDisplayStore.getState().setLocal("mobile", override);
  });
  const hub = transcriptDisplayStore.getState().hub;
  await userEvent.setup().click(screen.getByRole("button", { name: "Use the shared default" }));
  expect(transcriptDisplayStore.getState().local.desktop).toBeUndefined();
  expect(transcriptDisplayStore.getState().local.mobile).toEqual(override);
  expect(transcriptDisplayStore.getState().effective("desktop")).toEqual(hub.desktop?.config);
  expect(transcriptDisplayStore.getState().hub).toEqual(hub);
  expect(client.calls.filter((call) => call.method === "evener/settings/transcriptDisplay/patch")).toEqual([]);
});

test("scope and its clear action precede editing, while shared changes preserve both local layouts", async () => {
  const client = new FakeClient("ready");
  client.on("evener/settings/transcriptDisplay/patch", (params) => ({
    layout: params.layout,
    config: params.config,
    revision: 2,
  }));
  await mountReadySection(client);
  const desktop = makeTranscriptDisplayConfig({ kind: "preset", level: "chat" });
  const mobile = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  act(() => {
    transcriptDisplayStore.getState().setLocal("desktop", desktop);
    transcriptDisplayStore.getState().setLocal("mobile", mobile);
  });
  const user = userEvent.setup();
  await user.click(screen.getByRole("radio", { name: "Full detail" }));
  await savedOnce();
  expect(transcriptDisplayStore.getState().local).toEqual({ desktop, mobile });
  expect(transcriptDisplayStore.getState().effective("desktop")).toEqual(desktop);
  const scope = screen.getByTestId("transcript-display-scope");
  const controls = screen.getByTestId("transcript-display-controls-desktop");
  expect(scope.compareDocumentPosition(controls) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  const clear = within(scope).getByRole("button");
  expect(clear.compareDocumentPosition(controls) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  await user.click(clear);
  expect(transcriptDisplayStore.getState().local).toEqual({ mobile });
  expect(transcriptDisplayStore.getState().effective("desktop")).toEqual(
    transcriptDisplayStore.getState().hub.desktop?.config,
  );
  expect(client.calls.filter((call) => call.method === "evener/settings/transcriptDisplay/patch")).toHaveLength(1);
});

test("shows an accessible status for an unknown or older hub", () => {
  renderWithToasts();
  expect(screen.getAllByRole("status")).toHaveLength(1);
  expect(screen.getByRole("status").getAttribute("data-reason")).toBe("hub-unknown");

  cleanup();
  transcriptDisplayStore.setState({ hubSupport: "unsupported" });
  renderWithToasts();
  expect(screen.getByRole("status").getAttribute("data-reason")).toBe("unsupported");
  expect(
    within(screen.getByTestId("transcript-display-card-desktop"))
      .getAllByRole("radio")
      .every((radio) => (radio as HTMLButtonElement).disabled),
  ).toBe(true);
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
  expect(successToasts()).toEqual([]);
  expect(within(screen.getByTestId("transcript-display-card-desktop")).getByRole("status")).toBeTruthy();

  releasePatch(patchResult);
  await savedOnce();
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
  expect(successToasts()).toEqual([]);
  expect(screen.getByRole("region", { name: "Notifications" }).textContent).toBe("");
});

test("keeps browser-storage failure inline without a failure notification", async () => {
  transcriptDisplayStore.setState({ hubSupport: "supported" });
  renderWithToasts();
  vi.stubGlobal("localStorage", new FailingStorage());
  act(() => {
    transcriptDisplayStore
      .getState()
      .setLocal("desktop", makeTranscriptDisplayConfig({ kind: "preset", level: "full" }));
  });

  expect(transcriptDisplayStore.getState().storageWarning).not.toBeNull();
  await waitFor(() => expect(screen.getAllByRole("alert")).toHaveLength(1));
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
    if (patchCalls === 1) throw new WireError("server unavailable", -32000);
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
  expect(successToasts()).toEqual([]);

  await userEvent.setup().click(screen.getByRole("button", { name: "Retry" }));
  expect(within(screen.getByTestId("transcript-display-card-desktop")).getAllByRole("status")).toHaveLength(1);
  expect(screen.getAllByRole("status")).toHaveLength(1);
  expect(screen.queryByRole("alert")).toBeNull();
  retryResponse.resolve({ layout: "desktop", revision: 2, config: toWireConfig(draft) });
  await savedOnce();
  const notifications = screen.getByRole("region", { name: "Notifications" });
  await waitFor(() => expect(notifications.textContent).toMatch(/\S/));
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
  await screen.findByRole("alert");
  expect(successToasts()).toEqual([]);
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
  await savedOnce();
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
  await savedOnce();
  await act(async () => {
    responses[0]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(firstDraft) });
    await responses[0]?.promise;
  });
  expect(successToasts()).toHaveLength(1);
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
  await savedOnce();

  await act(async () => {
    responses[0]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(firstA) });
    responses[1]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(middleB) });
    await Promise.all([responses[0]?.promise, responses[1]?.promise]);
  });
  expect(successToasts()).toHaveLength(1);
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
  await savedOnce();
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
  act(() => {
    client.emitNotification({
      method: "evener/settings/transcriptDisplay/changed",
      params: { layout: "desktop", revision: 2, config: toWireConfig(draft) },
    } as AnyNotification);
  });
  await act(async () => {
    response.resolve({ layout: "desktop", revision: 2, config: toWireConfig(draft) });
    await response.promise;
  });

  await savedOnce();
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
  const initialize = await client.connect();
  client.emitReady({ ...initialize, features: { ...initialize.features, transcriptDisplaySettings: true } });
  renderWithToasts();
  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1));
  await waitFor(() => expect(transcriptDisplayStore.getState().hubLoading).toBe(false));
  await userEvent.setup().click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  const refreshCountBeforeReconnect = refreshes;
  reconnecting = true;
  const request = vi.spyOn(client, "request");
  await act(async () => {
    client.emitStateChange("idle");
    client.emitReady();
    await Promise.all(request.mock.results.map((result) => result.value));
  });
  await waitFor(() =>
    expect(
      client.calls.filter((call) => call.method === "evener/settings/transcriptDisplay/get").length,
    ).toBeGreaterThan(refreshCountBeforeReconnect),
  );
  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.config).toEqual(draft));
  expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(draft);
  response.resolve({ layout: "desktop", revision: 2, config: toWireConfig(draft) });

  await screen.findByRole("alert");
  expect(successToasts()).toEqual([]);
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
  await user.click(screen.getByRole("radio", { name: "Mobile" }));
  await user.click(screen.getByRole("radio", { name: "Full detail" }));
  await waitFor(() => expect(responses).toHaveLength(2));
  expect(transcriptDisplayStore.getState().drafts).toEqual({ desktop: draft, mobile: draft });
  expect(screen.getAllByRole("status")).toHaveLength(1);
  await user.click(screen.getByRole("radio", { name: "Desktop" }));
  const desktop = screen.getByTestId("transcript-display-card-desktop");

  responses.find((entry) => entry.layout === "desktop")?.response.reject(new WireError("desktop unavailable", -32000));
  expect((await within(desktop).findByRole("alert")).textContent).toContain("desktop unavailable");
  expect(transcriptDisplayStore.getState().drafts.mobile).toEqual(draft);
  expect(screen.getByRole("region", { name: "Notifications" }).textContent).toBe("");

  await user.click(within(desktop).getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(responses).toHaveLength(3));
  expect(screen.getAllByRole("status")).toHaveLength(1);
  responses
    .find((entry) => entry.layout === "mobile")
    ?.response.resolve({ layout: "mobile", revision: 2, config: toWireConfig(draft) });
  responses[2]?.response.resolve({ layout: "desktop", revision: 2, config: toWireConfig(draft) });
  await waitFor(() => expect(successToasts()).toHaveLength(2));
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
  let competingPatch!: ReturnType<ReturnType<typeof transcriptDisplayStore.getState>["patchHubDefault"]>;
  act(() => {
    competingPatch = transcriptDisplayStore.getState().patchHubDefault("desktop", newerDraft);
  });
  await waitFor(() => expect(responses).toHaveLength(2));

  responses[0]?.resolve({ layout: "desktop", revision: 2, config: toWireConfig(firstDraft) });
  await screen.findByRole("alert");
  expect(successToasts()).toEqual([]);
  expect(transcriptDisplayStore.getState().drafts.desktop).toEqual(newerDraft);

  await act(async () => {
    responses[1]?.reject(
      new WireError("conflict-from-hub", -32013, {
        evenerErrorInfo: "conflict",
        layout: "desktop",
        current: { revision: 2, config: toWireConfig(firstDraft) },
      }),
    );
    await expect(competingPatch).rejects.toThrow("conflict-from-hub");
  });
  expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 2, config: firstDraft });
  expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
  expect(successToasts()).toEqual([]);
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
    new WireError("conflict-from-hub", -32013, {
      evenerErrorInfo: "conflict",
      layout: "desktop",
      current: { revision: 2, config: toWireConfig(firstDraft) },
    }),
  );

  expect((await screen.findByRole("alert")).textContent).toContain("conflict-from-hub");
  expect(successToasts()).toEqual([]);
  expect(transcriptDisplayStore.getState().drafts.desktop).toBeUndefined();
  expect(transcriptDisplayStore.getState().hub.desktop).toEqual({ revision: 2, config: firstDraft });
  expect(screen.getAllByRole("button", { name: "Retry" }).length).toBeGreaterThan(0);
});

// The store keeps its own failure messages, and gives a hub WireError and the
// client's connection-state failure their friendly form, so the notice never
// shows the client's wiring text.
test.each([
  ["a hub-reported WireError", new WireError("transcript defaults are being rewritten", -32000)],
  ["the client's connection-state failure", new ConnectionClosedError("AppwireClient: closed")],
  ["the client's request timeout", new RequestTimeoutError('AppwireClient: "a method" timed out after 1ms')],
])("a hub read failure from %s shows its friendly form", async (_name, err) => {
  const client = new FakeClient("ready");
  client.on("evener/settings/transcriptDisplay/get", () => {
    throw err;
  });
  connectionStore.getState().connect(client);
  connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
  renderWithToasts();
  await waitFor(() => expect(transcriptDisplayStore.getState().hubError).not.toBeNull());
  expect(document.body.textContent).toContain(friendlyErrorMessage(err));
  expect(screen.queryByText(/AppwireClient/)).toBeNull();
});

// The client's own write failures (a timeout, a lost connection) show their
// friendly form on the card, never the client's wiring text.
test.each([
  ["the client's request timeout", new RequestTimeoutError('AppwireClient: "a method" timed out after 1ms')],
  ["the client's lost socket", new Error("AppwireClient: socket closed (code 1006)")],
])("a failed save from %s shows its friendly form", async (_name, err) => {
  const client = new FakeClient("ready");
  const confirmed = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });
  client.on("evener/settings/transcriptDisplay/get", () => ({
    desktop: { revision: 1, config: toWireConfig(confirmed) },
    mobile: { revision: 1, config: toWireConfig(shippedDefault("mobile").config) },
  }));
  client.on("evener/settings/transcriptDisplay/patch", () => {
    throw err;
  });
  connectionStore.getState().connect(client);
  connectionStore.setState({ features: { ...(await client.connect()).features, transcriptDisplaySettings: true } });
  renderWithToasts();
  await waitFor(() => expect(transcriptDisplayStore.getState().hub.desktop?.revision).toBe(1));

  await userEvent.setup().click(screen.getAllByRole("radio", { name: "Full detail" })[0]!);
  expect((await screen.findByRole("alert")).textContent).toContain(friendlyErrorMessage(err));
  expect(screen.queryByText(/AppwireClient/)).toBeNull();
});
