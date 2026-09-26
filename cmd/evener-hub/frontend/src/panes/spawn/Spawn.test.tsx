import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type {
  AnyNotification,
  HostForwardedResult,
  HostRequestParams,
  InstanceListResponse,
  LaunchConfigResolved,
  LaunchOption,
  ModelDescriptor,
  ModelListParams,
  ModelListResponse,
  NavigationManifest,
  PluginPreviewResponse,
  Thread,
  ThreadCapabilities,
  ThreadStartParams,
  ThreadStartResponse,
} from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import type { ResourceState } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { ClientProvider } from "../../shell/clientContext";
import { navigate } from "../../shell/routing";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests, resetHostInstancesForTests } from "../../stores/credentials";
import { extensionsStore, resetExtensionsStoreForTests } from "../../stores/extensions";
import { HOST_DEPENDENT_DISCOVERY_METHODS } from "../../stores/hostRouting";
import { hostsStore } from "../../stores/hosts";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { Toast } from "../../widgets";
import promptCardStyles from "../../widgets/promptcard/promptcard.module.css";
import textareaStyles from "../../widgets/textarea/textarea.module.css";
import { getToasts, resetToastStoreForTests } from "../../widgets/toast/store";
import Welcome from "../welcome/Welcome";
import Spawn, { CONNECT_ATTACH_TIMEOUT_MS } from "./Spawn";
import { loadDefaultsBlob } from "./spawnDefaults";
import {
  applySpawnURL,
  resetSpawnDraftsForTests,
  selectSpawnDirectory,
  setDraftField,
  spawnDraftsStore,
} from "./spawnDrafts";
import { SPAWN_SLASH_CATALOG_DEBOUNCE_MS } from "./useSpawnSlashCatalog";

let modelListOverride: ModelDescriptor[] | null = null;

const NO_CAPABILITIES: ThreadCapabilities = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  changeVisionModel: false,
  queue: false,
  goal: false,
  sharedNotes: false,
  rename: false,
};

function threadWithRef(ref: string): Thread {
  return {
    id: ref.includes(":") ? ref.slice(ref.indexOf(":") + 1) : ref,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "local",
    evener: { ref, capabilities: NO_CAPABILITIES, queue: { revision: 0 } },
  };
}

function startResponse(ref: string): ThreadStartResponse {
  return { thread: threadWithRef(ref), turn: { id: "turn_1", itemsView: "full", status: "idle" } };
}

// routedDiscoveryDefault answers one forwarded discovery method the way
// readyClient answers its controller-scoped twin, so a test that selects a
// remote host hydrates exactly as a local mount does: the spawn form issues the
// same calls, just wrapped in evener/host/request (component 07b). A test
// overrides the proxy handler wholesale when it needs host-specific values.
function routedDiscoveryDefault(method: string): HostForwardedResult {
  switch (method) {
    case "evener/instance/list":
      return {
        instances: [
          {
            name: "anthropic",
            providerId: "anthropic",
            protocol: "anthropic",
            auth: "bearer",
            implicit: false,
            isDefault: true,
            activeSource: "store",
            hasStoredOAuth: false,
            credentialRequired: true,
          },
        ],
        availableProviders: [],
      };
    case "evener/harnesses/list":
      return {
        data: [
          { id: "evener", label: "evener", kind: "evener" },
          { id: "external", label: "external", kind: "external" },
        ],
      };
    case "evener/launch/schema":
      return { options: [] };
    case "model/list":
      return {
        data: modelListOverride ?? [
          { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
          { provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" },
        ],
      };
    case "evener/launch/resolve":
      return { effective: {}, layers: {}, provenance: {} };
    case "evener/git/head":
      return { head: "main" };
    case "evener/projects/recent":
    case "evener/paths/complete":
      return { data: [] };
    case "evener/path/validate":
      return { path: "", valid: true };
    case "evener/dirs/create":
      return { path: "", created: true };
    case "evener/plugin/preview":
      return { plugins: [] };
    case "evener/spawn/slashCatalog":
      return { commands: [], skills: [] };
    default:
      return {};
  }
}

// A ready FakeClient with every mount-time catalog scripted so the form fully
// hydrates; individual tests override specific methods as needed.
function readyClient(configure?: (fake: FakeClient) => void): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("evener/instance/list", () => ({
    instances: [
      {
        name: "anthropic",
        providerId: "anthropic",
        protocol: "anthropic",
        auth: "bearer",
        implicit: false,
        isDefault: true,
        activeSource: "store",
        hasStoredOAuth: false,
        credentialRequired: true,
      },
    ],
    availableProviders: [],
  }));
  fake.on("evener/harnesses/list", () => ({
    data: [
      { id: "evener", label: "evener", kind: "evener" },
      { id: "external", label: "external", kind: "external" },
    ],
  }));
  fake.on("evener/launch/schema", () => ({ options: [] }));
  fake.on("model/list", () => ({
    data: modelListOverride ?? [
      { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
      { provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" },
    ],
  }));
  fake.on("evener/projects/recent", () => ({ data: [] }));
  fake.on("evener/paths/complete", () => ({ data: [] }));
  fake.on("evener/path/validate", () => ({ path: "", valid: true }));
  fake.on("evener/dirs/create", ({ path }) => ({ path, created: true }));
  fake.on("evener/git/head", () => ({ head: "main" }));
  fake.on("evener/plugin/preview", () => ({ plugins: [] }));
  fake.on("evener/spawn/slashCatalog", () => ({ commands: [], skills: [] }));
  fake.on("thread/start", () => startResponse("local:abc123"));
  // The explicit attach trigger (component 06's Connect): the picker fires
  // evener/host/attach when a remote host is selected or its Connect action is
  // tapped. Tests that assert the call override or inspect fake.calls.
  fake.on("evener/host/attach", () => ({ attached: true }));
  // Host-routed discovery (component 07b): a selected remote host's calls go
  // through evener/host/request, so the same answers are wired here. Tests that
  // pin host-specific values override this handler in `configure`.
  fake.on("evener/host/request", (params) => routedDiscoveryDefault((params as HostRequestParams).method));
  configure?.(fake);
  return fake;
}

function modelListRequests(fake: FakeClient): ModelListParams[] {
  return fake.calls.filter((call) => call.method === "model/list").map((call) => call.params as ModelListParams);
}

/** Every evener/host/attach the pane issued, for the picker's attach assertions. */
function attachCalls(fake: FakeClient): { host: string }[] {
  return fake.calls
    .filter((call) => call.method === "evener/host/attach")
    .map((call) => call.params as { host: string });
}

function renderSpawn(client: FakeClient, focused = true) {
  return render(
    <ClientProvider client={client}>
      <Spawn params={{}} paneId="spawn-1" focused={focused} />
      <Toast />
    </ClientProvider>,
  );
}

// Seeds the navigation manifest's launch sources, which the host picker reads
// through selectSources. Rendered only when the list holds a non-local source.
function seedSources(
  sources: NavigationManifest["sources"],
  overrides: Partial<ResourceState<NavigationManifest>> = {},
): void {
  const data: NavigationManifest = {
    generation_id: "generation_test",
    revision: 1,
    sources,
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
    sections: { live: { count: 0 }, needs_you: { count: 0 }, pin_sections: { count: 0 } },
    catalogs: { projects: { count: 0 }, archived_projects: { count: 0 }, test_runs: { count: 0 } },
  };
  const resource: ResourceState<NavigationManifest> = {
    key: { kind: "manifest" },
    data,
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: '"test"',
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
    ...overrides,
  };
  navigationStore.setState({ manifest: resource });
}

// The working directory is changed through an explicit-confirmation picker.
const LAST_WORKING_DIR_KEY = "evener-hub.spawn-defaults.global.last-working-dir";

function workingDir(): HTMLElement {
  return screen.getByLabelText(/^Working directory:/, { selector: "#spawn-cwd" });
}

// The DESKTOP Model control lives in the prompt card's own control row (the
// session composer's ModelSwitchTrigger, every width): a plain button, not a
// labelable control, so it is found by its "— change model" accessible-name
// suffix. Advanced options can carry a second such button, so the lookup stays
// scoped to the card.
function modelTrigger(): HTMLElement {
  return within(screen.getByTestId("spawn-controls")).getByRole("button", { name: /change model/i });
}

/** The trigger's value hook inside the card's control row. */
function modelValue(): HTMLElement {
  return screen.getByTestId("spawn-model-value");
}

/** The quiet effort control in the card's control row (StatusRow's overlay-select recipe). */
function effortControl(): HTMLElement {
  return screen.getByLabelText("Prompt reasoning effort");
}

// The card's effort control and the Advanced Options schema field share the
// wording "Reasoning effort": with the panel open, AT and label automation
// must still resolve each control unambiguously, so the card's own label
// carries its surface ("Prompt reasoning effort").
test("the card effort control keeps a distinct accessible name with Advanced options open", async () => {
  const user = setupUser();
  const advancedOption: LaunchOption = {
    field: "reasoning_effort",
    wireField: "reasoningEffort",
    label: "Reasoning effort",
    group: "model",
    kind: "select",
    perLaunch: true,
    choices: [
      { value: "low", label: "low" },
      { value: "high", label: "high" },
    ],
  };
  renderSpawn(
    readyClient((f) => {
      f.on("evener/launch/schema", () => ({ options: [advancedOption] }));
    }),
  );
  await settled();

  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  expect(screen.getByLabelText("Prompt reasoning effort")).toBe(effortControl());
  expect(screen.getAllByLabelText(/reasoning effort/i)).toHaveLength(2);
});

/** The visible effort readout (aria-hidden: the select speaks the value). */
function effortReadout(): HTMLElement {
  const trigger = screen.getByTestId("spawn-effort");
  const readout = trigger.querySelector("[data-testid='spawn-effort-value']");
  if (!readout) throw new Error("the card's effort control has no visible readout");
  return readout as HTMLElement;
}

/** The trigger's rendered path. It also carries a chevron and a screen-reader
 * hint, so the value is matched inside the text rather than compared whole. */
function expectWorkingDir(path: string): void {
  expect(workingDir().textContent).toContain(path);
}

async function setWorkingDir(user: ReturnType<typeof userEvent.setup>, path: string): Promise<void> {
  await user.click(workingDir());
  const input = await screen.findByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.paste(path);
  await user.keyboard("{Enter}");
  const confirm = screen.getByRole("button", { name: "Use this folder" });
  await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
  await user.click(confirm);
}

/** Enters prompt text as one paste instead of key by key: each prompt
 * keystroke re-renders the whole pane. Tests about per-keystroke prompt
 * behavior (the slash menu) type with `user.type` instead. */
async function fillPrompt(user: ReturnType<typeof userEvent.setup>, text: string): Promise<void> {
  await user.click(promptField());
  await user.paste(text);
}

/** Waits for the mount-time catalogs to land. The Advanced-options toggle is
 * the sentinel because it renders unconditionally and is not itself one of the
 * awaited catalogs' outputs - unlike the harness select, which now lives INSIDE
 * that collapsed panel and so isn't in the tree at rest. */
// The pane debounces its catalog, slash-catalog and plugin-preview loads by
// 250ms. Fake timers own that clock, and the stubbed `jest` global lets Testing
// Library's findBy/waitFor polls advance it, so each debounce costs a poll
// rather than a quarter second of real time.
// vi.dynamicImportSettled() is unaffected: Vitest waits on its own saved real
// timers (getSafeTimers), not the faked globals, so the lazy chunk loads these
// tests await still settle.
function setupUser() {
  return userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
}

async function settled(): Promise<void> {
  await screen.findByRole("button", { name: "Advanced options" });
}

async function visitSpawnURL(url: string): Promise<void> {
  await act(async () => {
    window.history.pushState({}, "", url);
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
}

test("draft survives unmount and bare /new return before any successful start", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const client = readyClient();
  const mounted = renderSpawn(client);
  await settled();
  await fillPrompt(user, "draft-a-sentinel");
  fireEvent.change(effortControl(), { target: { value: "high" } });
  mounted.unmount();
  await visitSpawnURL("/settings");
  await visitSpawnURL("/new");
  renderSpawn(client);
  await settled();
  expectWorkingDir("/tmp/draft-a");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("draft-a-sentinel");
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBeNull();
});

test("project navigation isolates drafts and ignores non-new URL prefill", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  renderSpawn(readyClient());
  await settled();
  await fillPrompt(user, "draft-a-sentinel");
  fireEvent.change(effortControl(), { target: { value: "high" } });
  await visitSpawnURL("/settings?dir=/tmp/foreign&prompt=foreign");
  expectWorkingDir("/tmp/draft-a");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("draft-a-sentinel");
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("");
  await fillPrompt(user, "draft-b-sentinel");
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("draft-a-sentinel");
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
});

test("re-entering the same /new URL after leaving /new re-applies its explicit prefill", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/reentry-a");
  const client = readyClient();
  const mounted = renderSpawn(client);
  await settled();
  await fillPrompt(user, "sentinel-a");
  // The picker switches drafts without touching the URL, so the prefill
  // marker still names this same URL.
  await setWorkingDir(user, "/tmp/reentry-b");
  await fillPrompt(user, "sentinel-b");
  expectWorkingDir("/tmp/reentry-b");
  expect(window.location.search).toBe("?dir=/tmp/reentry-a");
  // Leaving /new can unmount the pane (mobile StackHost mounts only the
  // active route). Returning to the identical explicit URL is a fresh
  // request: it selects draft A again, and B's draft persists in the map.
  mounted.unmount();
  await visitSpawnURL("/settings");
  await visitSpawnURL("/new?dir=/tmp/reentry-a");
  renderSpawn(client);
  await settled();
  expectWorkingDir("/tmp/reentry-a");
  expect(promptField().value).toBe("sentinel-a");
  expect(completionDraft("/tmp/reentry-b").fields.getState().prompt).toBe("sentinel-b");
});

test("remounting the same /new URL without leaving /new keeps the picker-selected draft", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/reentry-a");
  const client = readyClient();
  const mounted = renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/reentry-b");
  await fillPrompt(user, "sentinel-b");
  expectWorkingDir("/tmp/reentry-b");
  // A remount with no intervening departure must not clobber the current
  // draft back to the URL's prefill directory.
  mounted.unmount();
  renderSpawn(client);
  await settled();
  expectWorkingDir("/tmp/reentry-b");
  expect(promptField().value).toBe("sentinel-b");
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<T>((done, fail) => {
    resolve = done;
    reject = fail;
  });
  return { promise, resolve, reject };
}

function completionDraft(cwd: string) {
  const draft = spawnDraftsStore.getState().drafts.get(cwd);
  if (!draft) throw new Error(`missing draft ${cwd}`);
  return draft;
}

async function completionNavigate(url: string): Promise<void> {
  await act(async () => navigate(url));
}

// Removing the launch's view-ownership check must break these, even when
// picker changes leave the URL unchanged or the user returns to the same view.
test.each(["unchanged", "picker", "picker return", "away", "route return", "remount", "focus return"])(
  "completion ownership: %s while thread/start is pending",
  async (scenario) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
    const started = deferred<ThreadStartResponse>();
    const fake = readyClient((f) => f.on("thread/start", () => started.promise));
    const mounted = renderSpawn(fake);
    await fillPrompt(user, "submitted-a");
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
    const origin = completionDraft("/tmp/completion-a");
    if (scenario.startsWith("picker")) {
      await setWorkingDir(user, "/tmp/completion-b");
      await fillPrompt(user, "unsent-b");
      expect(window.location.search).toBe("?dir=/tmp/completion-a");
      if (scenario === "picker return") await setWorkingDir(user, "/tmp/completion-a");
    } else if (scenario === "away" || scenario === "route return") {
      await completionNavigate("/settings");
      if (scenario === "route return") await completionNavigate("/new?dir=/tmp/completion-a");
    } else if (scenario === "remount") {
      mounted.unmount();
      await completionNavigate("/settings");
      await completionNavigate("/new?dir=/tmp/completion-a");
      renderSpawn(fake);
    } else if (scenario === "focus return") {
      for (const focused of [false, true]) {
        mounted.rerender(
          <ClientProvider client={fake}>
            <Spawn params={{}} paneId="spawn-1" focused={focused} />
            <Toast />
          </ClientProvider>,
        );
      }
    }
    await act(async () => started.resolve(startResponse("local:completion-a")));
    await waitFor(() => expect(origin.fields.getState().busy).toBe(false));
    expect(origin.fields.getState().prompt).toBe("");
    expect(origin.fields.getState().busyStartedAt).toBeNull();
    expect(origin.busyRef.current).toBe(false);
    if (scenario.startsWith("picker"))
      expect(completionDraft("/tmp/completion-b").fields.getState().prompt).toBe("unsent-b");
    expect(window.location.pathname).toBe(
      scenario === "unchanged" ? "/s/local%3Acompletion-a" : scenario === "away" ? "/settings" : "/new",
    );
  },
);

test.each(["A first", "B first"])("completion ownership: concurrent launches finish %s", async (order) => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const startA = deferred<ThreadStartResponse>();
  const startB = deferred<ThreadStartResponse>();
  const fake = readyClient((f) =>
    f.on("thread/start", ({ cwd }) => (cwd === "/tmp/completion-a" ? startA.promise : startB.promise)),
  );
  renderSpawn(fake);
  await fillPrompt(user, "submitted-a");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  await setWorkingDir(user, "/tmp/completion-b");
  await fillPrompt(user, "submitted-b");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(2));
  expect(fake.calls.filter((call) => call.method === "thread/start").map((call) => call.params)).toMatchObject([
    { cwd: "/tmp/completion-a", input: [{ type: "text", text: "submitted-a" }] },
    { cwd: "/tmp/completion-b", input: [{ type: "text", text: "submitted-b" }] },
  ]);
  if (order === "A first") {
    await act(async () => startA.resolve(startResponse("local:completion-a")));
    expect(window.location.pathname).toBe("/new");
    expect(promptField().value).toBe("submitted-b");
    await act(async () => startB.resolve(startResponse("local:completion-b")));
  } else {
    await act(async () => startB.resolve(startResponse("local:completion-b")));
    expect(window.location.pathname).toBe("/s/local%3Acompletion-b");
    await act(async () => startA.resolve(startResponse("local:completion-a")));
  }
  expect(window.location.pathname).toBe("/s/local%3Acompletion-b");
  for (const cwd of ["/tmp/completion-a", "/tmp/completion-b"]) {
    expect(completionDraft(cwd).fields.getState()).toMatchObject({ prompt: "", busy: false, busyStartedAt: null });
  }
});

// Stamping only in doSpawn would incorrectly regain authority after these awaits.
test.each(["preflight", "create confirmation"])(
  "completion ownership: departure during %s cannot reacquire navigation on return",
  async (boundary) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
    const validation = deferred<{ path: string; valid: boolean }>();
    const creation = deferred<{ path: string; created: boolean }>();
    const fake = readyClient((f) => {
      f.on("evener/path/validate", () =>
        boundary === "preflight" ? validation.promise : { path: "/tmp/completion-a", valid: false },
      );
      f.on("evener/dirs/create", () => creation.promise);
    });
    renderSpawn(fake);
    await fillPrompt(user, "submitted-a");
    await user.click(screen.getByTestId("spawn-submit"));
    if (boundary === "create confirmation")
      await user.click(await screen.findByRole("button", { name: "Create & start" }));
    await waitFor(() =>
      expect(
        fake.calls.some(
          (call) => call.method === (boundary === "preflight" ? "evener/path/validate" : "evener/dirs/create"),
        ),
      ).toBe(true),
    );
    await completionNavigate("/settings");
    await completionNavigate("/new?dir=/tmp/completion-a");
    await act(async () => {
      if (boundary === "preflight") validation.resolve({ path: "/tmp/completion-a", valid: true });
      else creation.resolve({ path: "/tmp/completion-a", created: true });
    });
    await waitFor(() => expect(completionDraft("/tmp/completion-a").fields.getState().busy).toBe(false));
    expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
      cwd: "/tmp/completion-a",
      input: [{ type: "text", text: "submitted-a" }],
    });
    expect(promptField().value).toBe("");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(window.location.pathname).toBe("/new");
  },
);

test("completion ownership: late missing-directory preflight cannot open a dialog over settings", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const validation = deferred<{ path: string; valid: boolean }>();
  const fake = readyClient((f) => f.on("evener/path/validate", () => validation.promise));
  renderSpawn(fake);
  await fillPrompt(user, "submitted-a");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "evener/path/validate")).toBe(true));
  await completionNavigate("/settings");
  await act(async () => validation.resolve({ path: "/tmp/completion-a", valid: false }));
  expect(completionDraft("/tmp/completion-a").fields.getState()).toMatchObject({
    busy: false,
    createDialogPath: "/tmp/completion-a",
    prompt: "submitted-a",
  });
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(window.location.pathname).toBe("/settings");
  await completionNavigate("/new");
  await user.click(await screen.findByRole("button", { name: "Create & start" }));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
});

test("completion ownership: a query-only navigation retires a pending launch's claim to the view", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => f.on("thread/start", () => started.promise));
  renderSpawn(fake);
  await fillPrompt(user, "submitted-a");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  // Same draft, same pathname: only the query changes. The user's newest
  // navigation still wins over the older in-flight launch.
  await completionNavigate("/new?dir=/tmp/completion-a&prompt=updated");
  await act(async () => started.resolve(startResponse("local:completion-a")));
  await waitFor(() => expect(completionDraft("/tmp/completion-a").fields.getState().busy).toBe(false));
  expect(window.location.pathname).toBe("/new");
  expect(window.location.search).toBe("?dir=/tmp/completion-a&prompt=updated");
});

test("completion menu ownership: a remounted prompt's cleared snapshot retires its menu", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => {
    f.on("thread/start", () => started.promise);
    f.on("evener/spawn/slashCatalog", () => ({ commands: [{ name: "review", description: "Review" }], skills: [] }));
  });
  const mounted = renderSpawn(fake);
  await user.type(promptField(), "/re");
  await screen.findByTestId("composer-slash-menu");
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  mounted.unmount();
  renderSpawn(fake);
  await screen.findByTestId("composer-slash-menu");
  await act(async () => started.resolve(startResponse("local:completion-a")));
  expect(promptField().value).toBe("");
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(window.location.pathname).toBe("/new");
});

test.each(["unchanged", "newer prompt", "other draft"])("completion menu ownership: %s", async (scenario) => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => {
    f.on("thread/start", () => started.promise);
    f.on("evener/spawn/slashCatalog", () => ({ commands: [{ name: "review", description: "Review" }], skills: [] }));
  });
  renderSpawn(fake);
  await user.type(promptField(), "/re");
  await screen.findByTestId("composer-slash-menu");
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  if (scenario === "other draft") await setWorkingDir(user, "/tmp/completion-b");
  if (scenario !== "unchanged") {
    await user.clear(promptField());
    await user.type(promptField(), "/rev");
    await screen.findByTestId("composer-slash-menu");
  }
  await act(async () => started.resolve(startResponse("local:completion-a")));
  await waitFor(() => expect(completionDraft("/tmp/completion-a").fields.getState().busy).toBe(false));
  expect(promptField().value).toBe(scenario === "unchanged" ? "" : "/rev");
  if (scenario === "unchanged") expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  else {
    expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();
    expect(document.activeElement).toBe(promptField());
    expect(slashOptions().map((option) => option.textContent)).toContainEqual(expect.stringContaining("/review"));
  }
});

test("entering an in-memory draft validates its model after another draft swept its saved default", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-a", JSON.stringify({ model: "openai/gpt-5" }));
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-b", JSON.stringify({ model: "openai/retired-b" }));
  const catalog = deferred<ModelListResponse>();
  renderSpawn(readyClient((f) => f.on("model/list", () => catalog.promise)));
  await settled();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-b");
  expect(modelValue().textContent).toBe("openai/retired-b");
  await visitSpawnURL("/new?dir=/tmp/lifecycle-a");
  await act(async () => catalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  expect(localStorage.getItem("evener-hub.spawn-defaults./tmp/lifecycle-b")).toBeNull();
  expect(modelValue().textContent).toBe("openai/gpt-5");
  await visitSpawnURL("/new?dir=/tmp/lifecycle-b");
  expect(modelValue().textContent).not.toContain("retired-b");
  expect(screen.getByText(/discarded last-used model openai\/retired-b/i)).toBeTruthy();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-a");
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
});

test("entering a new draft validates defaults saved after the global sweep", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
  const catalog = deferred<ModelListResponse>();
  renderSpawn(readyClient((f) => f.on("model/list", () => catalog.promise)));
  await settled();
  await act(async () => catalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-b", JSON.stringify({ model: "openai/retired-b" }));
  await visitSpawnURL("/new?dir=/tmp/lifecycle-b");
  expect(modelValue().textContent).not.toContain("retired-b");
  expect(screen.getByText(/discarded last-used model openai\/retired-b/i)).toBeTruthy();
});

test.each(["unknown", "empty", "error"])("global cleanup fails open for an %s catalog", async (kind) => {
  window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
  const saved = JSON.stringify({ model: "openai/retained" });
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-a", saved);
  localStorage.setItem("evener-hub.spawn-defaults.global.model", "openai/retained");
  const catalog = deferred<ModelListResponse>();
  renderSpawn(readyClient((f) => f.on("model/list", () => catalog.promise)));
  await settled();
  await act(async () => {
    if (kind === "error") catalog.reject(new Error("catalog unavailable"));
    else catalog.resolve({ data: kind === "unknown" ? [{ provider: "private", model: "other" }] : [] });
  });
  expect(modelValue().textContent).toBe("openai/retained");
  expect(localStorage.getItem("evener-hub.spawn-defaults./tmp/lifecycle-a")).toBe(saved);
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBe("openai/retained");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
});

test("a draft's discard notice survives another draft selecting a model and clears on its own selection", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-a", JSON.stringify({ model: "openai/retired-a" }));
  renderSpawn(readyClient());
  expect(await screen.findByText(/discarded last-used model openai\/retired-a/i)).toBeTruthy();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-b");
  await user.click(modelTrigger());
  await user.click(await screen.findByRole("option", { name: /gpt-5/ }));
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-a");
  expect(screen.getByText(/discarded last-used model openai\/retired-a/i)).toBeTruthy();
  await user.click(modelTrigger());
  await user.click(await screen.findByRole("option", { name: /gpt-5/ }));
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
});

test("two draft discard notices coexist across provider refresh and pane remount", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-a", JSON.stringify({ model: "openai/retired-a" }));
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-b", JSON.stringify({ model: "openai/retired-b" }));
  let refreshed = false;
  const fake = readyClient((f) =>
    f.on("model/list", () => ({
      data: [
        { provider: "openai", model: "gpt-5" },
        ...(refreshed ? [] : [{ provider: "openai", model: "retired-b" }]),
      ],
    })),
  );
  connectionStore.getState().connect(fake);
  const mounted = renderSpawn(fake);
  expect(await screen.findByText(/discarded last-used model openai\/retired-a/i)).toBeTruthy();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-b");
  expect(modelValue().textContent).toBe("openai/retired-b");
  refreshed = true;
  await act(async () => credentialsStore.getState().fetch());
  expect(await screen.findByText(/discarded last-used model openai\/retired-b/i)).toBeTruthy();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-a");
  expect(screen.getByText(/discarded last-used model openai\/retired-a/i)).toBeTruthy();
  mounted.unmount();
  renderSpawn(fake);
  await settled();
  expect(screen.getByText(/discarded last-used model openai\/retired-a/i)).toBeTruthy();
  await visitSpawnURL("/new?dir=/tmp/lifecycle-b");
  expect(screen.getByText(/discarded last-used model openai\/retired-b/i)).toBeTruthy();
});

test.each(["evener", "external"])("%s scoped catalogs cannot sweep unrelated Evener defaults", async (harness) => {
  window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-a", JSON.stringify({ harness }));
  const valid = JSON.stringify({ model: "openai/gpt-5", access_mode: "plan" });
  const unknown = JSON.stringify({ model: "private/secret", reasoning_effort: "high" });
  localStorage.setItem("evener-hub.spawn-defaults./tmp/unrelated", valid);
  localStorage.setItem("evener-hub.spawn-defaults./tmp/unknown", unknown);
  localStorage.setItem(
    "evener-hub.spawn-defaults./tmp/stale",
    JSON.stringify({ model: "openai/retired", access_mode: "plan" }),
  );
  localStorage.setItem("evener-hub.spawn-defaults./tmp/malformed", JSON.stringify({ model: "unqualified" }));
  localStorage.setItem("evener-hub.spawn-defaults.global.model", "openai/gpt-5");
  const fake = readyClient((f) =>
    f.on("model/list", (params) => ({
      data:
        params.harness === "evener" && params.cwd === undefined
          ? [{ provider: "openai", model: "gpt-5" }]
          : [{ provider: "openai", model: "scoped-only" }],
    })),
  );
  renderSpawn(fake);
  await settled();
  expect(localStorage.getItem("evener-hub.spawn-defaults./tmp/unrelated")).toBe(valid);
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBe("openai/gpt-5");
  expect(localStorage.getItem("evener-hub.spawn-defaults./tmp/unknown")).toBe(unknown);
  expect(JSON.parse(localStorage.getItem("evener-hub.spawn-defaults./tmp/stale") ?? "null")).toEqual({
    access_mode: "plan",
  });
  expect(localStorage.getItem("evener-hub.spawn-defaults./tmp/malformed")).toBeNull();
  expect(modelListRequests(fake).some((params) => params.harness === "evener" && params.cwd === undefined)).toBe(true);
});

test.each(["native-model", "openai/native-model"])(
  "Evener cleanup preserves an external draft's live %s model",
  async (model) => {
    window.history.pushState({}, "", "/new?dir=/tmp/lifecycle-a");
    localStorage.setItem("evener-hub.spawn-defaults./tmp/lifecycle-a", JSON.stringify({ harness: "external", model }));
    renderSpawn(readyClient((f) => f.on("model/list", () => ({ data: [{ provider: "openai", model: "gpt-5" }] }))));
    await settled();
    expect(modelValue().textContent).toBe(model);
    expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  },
);

test.each([false, true])("project navigation isolates stale-model notices (late catalog: %s)", async (late) => {
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-a", JSON.stringify({ model: "openai/retired" }));
  const catalog = deferred<ModelListResponse>();
  const fake = readyClient((f) =>
    f.on("model/list", ({ harness, cwd }) =>
      harness === "evener" && cwd === undefined ? catalog.promise : { data: [{ provider: "openai", model: "gpt-5" }] },
    ),
  );
  renderSpawn(fake);
  await waitFor(() => expect(fake.calls.some((c) => c.method === "model/list")).toBe(true));
  if (late) await visitSpawnURL("/new?dir=/tmp/review-b");
  await act(async () => catalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  if (!late) {
    expect(await screen.findByText(/discarded last-used model openai\/retired/i)).toBeTruthy();
    await visitSpawnURL("/new?dir=/tmp/review-b");
  }
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  expect(modelValue().textContent).not.toContain("retired");
});

test.each([false, true])("stale-model sweep retires its originating draft (navigate: %s)", async (navigate) => {
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-a", JSON.stringify({ model: "openai/retired" }));
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-b", JSON.stringify({ model: "openai/gpt-5" }));
  const catalog = deferred<ModelListResponse>();
  const fake = readyClient((f) =>
    f.on("model/list", ({ harness, cwd }) =>
      harness === "evener" && cwd === undefined ? catalog.promise : { data: [{ provider: "openai", model: "gpt-5" }] },
    ),
  );
  renderSpawn(fake);
  await waitFor(() => expect(fake.calls.some((c) => c.method === "model/list")).toBe(true));
  expect(modelValue().textContent).toBe("openai/retired");
  if (navigate) await visitSpawnURL("/new?dir=/tmp/review-b");
  await act(async () => catalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  if (navigate) {
    expect(modelValue().textContent).toBe("openai/gpt-5");
    expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
    await visitSpawnURL("/new?dir=/tmp/review-a");
  }
  expect(modelValue().textContent).not.toContain("retired");
  expect(screen.getByText(/discarded last-used model openai\/retired/i)).toBeTruthy();
});

test("stale-model sweep preserves a newer user selection in its originating draft and isolates B", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-a", JSON.stringify({ model: "openai/retired" }));
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-b", JSON.stringify({ model: "openai/gpt-5" }));
  const catalog = deferred<ModelListResponse>();
  let refreshing = false;
  let refreshRequested = false;
  const fake = readyClient((f) =>
    f.on("model/list", ({ harness, cwd }) => {
      if (refreshing && harness === "evener" && cwd === undefined) {
        refreshRequested = true;
        return catalog.promise;
      }
      return {
        data: [
          { provider: "openai", model: "retired" },
          { provider: "openai", model: "gpt-5" },
        ],
      };
    }),
  );
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  refreshing = true;
  await act(async () => credentialsStore.getState().fetch());
  await waitFor(() => expect(refreshRequested).toBe(true));
  await user.click(modelTrigger());
  await user.clear(await screen.findByRole("combobox", { name: "Model" }));
  await user.click(await screen.findByRole("option", { name: /gpt-5/ }));
  expect(modelValue().textContent).toBe("openai/gpt-5");
  await visitSpawnURL("/new?dir=/tmp/review-b");
  expect(modelValue().textContent).toBe("openai/gpt-5");
  await act(async () => catalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  await visitSpawnURL("/new?dir=/tmp/review-a");
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
});

test("stale-model sweep snapshots the current draft on provider refresh after navigation", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-a", JSON.stringify({ model: "openai/gpt-5" }));
  localStorage.setItem("evener-hub.spawn-defaults./tmp/review-b", JSON.stringify({ model: "openai/retired" }));
  const catalog = deferred<ModelListResponse>();
  let refreshing = false;
  let refreshRequested = false;
  const fake = readyClient((f) =>
    f.on("model/list", ({ harness, cwd }) => {
      if (refreshing && harness === "evener" && cwd === undefined) {
        refreshRequested = true;
        return catalog.promise;
      }
      return {
        data: [
          { provider: "openai", model: "retired" },
          { provider: "openai", model: "gpt-5" },
        ],
      };
    }),
  );
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await waitFor(() => expect(fake.calls.some((c) => c.method === "model/list")).toBe(true));
  await visitSpawnURL("/new?dir=/tmp/review-b");
  expect(modelValue().textContent).toBe("openai/retired");
  refreshing = true;
  await act(async () => credentialsStore.getState().fetch());
  await waitFor(() => expect(refreshRequested).toBe(true));
  await visitSpawnURL("/new?dir=/tmp/review-a");
  await act(async () => catalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  await visitSpawnURL("/new?dir=/tmp/review-b");
  expect(modelValue().textContent).not.toContain("retired");
  expect(screen.getByText(/discarded last-used model openai\/retired/i)).toBeTruthy();
});

test("project navigation clears the old default-model gate while the new resolve is pending", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  const next = deferred<LaunchConfigResolved>();
  const fake = readyClient((f) =>
    f.on("evener/launch/resolve", ({ cwd }) =>
      cwd === "/tmp/review-a" ? { effective: {}, layers: {}, provenance: {} } : next.promise,
    ),
  );
  renderSpawn(fake);
  await waitFor(() => expect(modelValue().textContent).toBe("Choose a model"));
  await visitSpawnURL("/new?dir=/tmp/review-b");
  await waitFor(() =>
    expect(
      fake.calls.some(
        (c) => c.method === "evener/launch/resolve" && (c.params as { cwd?: string }).cwd === "/tmp/review-b",
      ),
    ).toBe(true),
  );
  expect(modelValue().textContent).not.toBe("Choose a model");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
  await act(async () =>
    next.resolve({ effective: { model: "anthropic/claude-sonnet-4-5" }, layers: {}, provenance: {} }),
  );
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
});

test.each(["settled error", "late error"])(
  "project navigation isolates advanced path validation: %s",
  async (scenario) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/review-a");
    const validation = deferred<{ path: string; valid: boolean; error: string }>();
    const fake = readyClient((f) => {
      f.on("evener/launch/schema", () => ({
        options: [
          {
            field: "agent",
            wireField: "agent",
            label: "Agent",
            kind: "text",
            group: "general",
            pathKind: "command",
            perLaunch: true,
          },
        ],
      }));
      f.on("evener/path/validate", ({ path }) =>
        path === "review-agent" ? validation.promise : { path, valid: true },
      );
    });
    renderSpawn(fake);
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-agent" } });
    const finish = async () =>
      act(async () => validation.resolve({ path: "review-agent", valid: false, error: "review-a-invalid" }));
    if (scenario === "settled error") {
      await finish();
      expect(screen.getByText("review-a-invalid")).toBeTruthy();
    }
    await visitSpawnURL("/new?dir=/tmp/review-b");
    if (scenario === "late error") await finish();
    expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("");
    expect(screen.queryByText("review-a-invalid")).toBeNull();
    // Navigation must not collapse the intentionally mounted advanced controls.
    expect(screen.getByRole("button", { name: "Show resolved config" })).toBeTruthy();
  },
);

test.each(["settled success", "settled error", "late success", "late error"])(
  "project navigation isolates advanced config preview: %s",
  async (scenario) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/review-a");
    const preview = deferred<LaunchConfigResolved>();
    let previewRequested = false;
    const fake = readyClient((f) =>
      f.on("evener/launch/resolve", ({ cwd }) =>
        previewRequested && cwd === "/tmp/review-a"
          ? preview.promise
          : { effective: { model: "anthropic/claude-sonnet-4-5" }, layers: {}, provenance: {} },
      ),
    );
    renderSpawn(fake);
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    previewRequested = true;
    await user.click(screen.getByRole("button", { name: "Show resolved config" }));
    const finish = async () =>
      act(async () => {
        if (scenario.endsWith("error")) preview.reject(new Error("review-a-resolve-error"));
        else preview.resolve({ effective: { model: "review-a-resolved-model" }, layers: {}, provenance: {} });
      });
    if (scenario.startsWith("settled")) {
      await finish();
      if (scenario.endsWith("error")) expect(screen.getByText(/review-a-resolve-error/)).toBeTruthy();
      else
        expect(screen.getByRole("group", { name: "Resolved config" }).textContent).toContain("review-a-resolved-model");
    }
    await visitSpawnURL("/new?dir=/tmp/review-b");
    if (scenario.startsWith("late")) await finish();
    expect(screen.queryByRole("group", { name: "Resolved config" })).toBeNull();
    expect(screen.queryByText(/review-a-resolve-error/)).toBeNull();
    await user.click(screen.getByRole("button", { name: "Show resolved config" }));
    expect((await screen.findByRole("group", { name: "Resolved config" })).textContent).toContain(
      "anthropic/claude-sonnet-4-5",
    );
  },
);

test.each(["%20/tmp/review-a%20", "%20%20"])(
  "URL directory normalization preserves draft and launch identity: %s",
  async (dir) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/review-a");
    const fake = readyClient();
    renderSpawn(fake);
    await fillPrompt(user, "normalized-draft");
    await visitSpawnURL(`/new?dir=${dir}`);
    expect(promptField().value).toBe("normalized-draft");
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
    expect(fake.calls.find((c) => c.method === "thread/start")?.params).toMatchObject({
      cwd: "/tmp/review-a",
      input: [{ type: "text", text: "normalized-draft" }],
    });
  },
);

test("directory picker assigns the unscoped draft and restores each project's launch settings", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new");
  const fake = readyClient();
  const mounted = renderSpawn(fake);
  await fillPrompt(user, "unscoped-sentinel");
  fireEvent.change(effortControl(), { target: { value: "high" } });
  mounted.unmount();
  renderSpawn(fake);
  expect(promptField().value).toBe("unscoped-sentinel");
  await setWorkingDir(user, "/tmp/draft-a");
  await pickModel(user, "gpt-5", "openai/gpt-5");
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Access mode"), "Read-only");
  await user.selectOptions(screen.getByLabelText("Harness"), "evener");
  await setWorkingDir(user, "/tmp/draft-b");
  expect(promptField().value).toBe("");
  expect((effortControl() as HTMLSelectElement).value).toBe("");
  await fillPrompt(user, "draft-b-sentinel");
  await setWorkingDir(user, "/tmp/draft-a");
  expect(promptField().value).toBe("unscoped-sentinel");
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
  expect(modelValue().textContent).toBe("openai/gpt-5");
  expect((screen.getByLabelText("Access mode") as HTMLSelectElement).value).toBe("read-only");
  expect((screen.getByLabelText("Harness") as HTMLSelectElement).value).toBe("evener");
  await setWorkingDir(user, "/tmp/draft-b");
  expect(promptField().value).toBe("draft-b-sentinel");
});

test("URL prefill is applied to its project but not replayed over edits on remount", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a&prompt=seed");
  const fake = readyClient();
  const mounted = renderSpawn(fake);
  await user.type(promptField(), "-edited");
  mounted.unmount();
  renderSpawn(fake);
  expect(promptField().value).toBe("seed-edited");
  await visitSpawnURL("/new?dir=/tmp/draft-b&prompt=other-seed");
  expect(promptField().value).toBe("other-seed");
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  expect(promptField().value).toBe("seed-edited");
  await visitSpawnURL("/new?prompt=replacement");
  expect(promptField().value).toBe("replacement");
});

test("unchanged URL directory prefill does not replace a picker-selected draft on remount", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  const fake = readyClient();
  const mounted = renderSpawn(fake);
  await setWorkingDir(user, "/tmp/review-b");
  await fillPrompt(user, "picker-draft");
  mounted.unmount();
  renderSpawn(fake);
  expectWorkingDir("/tmp/review-b");
  expect(promptField().value).toBe("picker-draft");
  await visitSpawnURL("/new?dir=/tmp/review-a");
  expectWorkingDir("/tmp/review-a");
  expect(promptField().value).toBe("");
});

test.each(["unrelated field", "newer same field", "other project"])(
  "late advanced path validation preserves %s edits after remount",
  async (scenario) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/review-a");
    const validation = deferred<{ path: string; valid: boolean }>();
    const fake = readyClient((f) => {
      f.on("evener/launch/schema", () => ({
        options: [
          {
            field: "agent",
            wireField: "agent",
            label: "Agent",
            kind: "text",
            group: "general",
            pathKind: "command",
            perLaunch: true,
          },
          {
            field: "maxRounds",
            wireField: "maxRounds",
            label: "Max rounds",
            kind: "integer",
            group: "general",
            perLaunch: true,
          },
        ],
      }));
      f.on("evener/path/validate", ({ path }) =>
        path === "review-agent" ? validation.promise : { path, valid: true },
      );
    });
    const mounted = renderSpawn(fake);
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-agent" } });
    mounted.unmount();
    renderSpawn(fake);
    await user.click(screen.getByRole("button", { name: "Advanced options" }));
    fireEvent.change(screen.getByLabelText("Max rounds"), { target: { value: "7" } });
    if (scenario === "newer same field") {
      fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-new" } });
    }
    if (scenario === "other project") {
      await visitSpawnURL("/new?dir=/tmp/review-b");
      fireEvent.change(screen.getByLabelText("Max rounds"), { target: { value: "9" } });
    }
    await act(async () => validation.resolve({ path: "review-agent", valid: scenario !== "newer same field" }));
    if (scenario === "other project") {
      expect((screen.getByLabelText("Max rounds") as HTMLInputElement).value).toBe("9");
      expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("");
      await visitSpawnURL("/new?dir=/tmp/review-a");
    }
    expect((screen.getByLabelText("Max rounds") as HTMLInputElement).value).toBe("7");
    const agent = scenario === "newer same field" ? "review-new" : "review-agent";
    expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe(agent);
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
    expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
      cwd: "/tmp/review-a",
      launchOverrides: { agent, maxRounds: 7 },
    });
  },
);

test("successful creation preserves edits made while directory preflight was pending", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const validation = deferred<{ path: string; valid: boolean }>();
  const fake = readyClient((f) => f.on("evener/path/validate", () => validation.promise));
  renderSpawn(fake);
  await fillPrompt(user, "submitted-sentinel");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "evener/path/validate")).toBe(true));
  await user.type(promptField(), "-newer");
  await act(async () => validation.resolve({ path: "/tmp/draft-a", valid: true }));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    input: [{ type: "text", text: "submitted-sentinel" }],
  });
  expect(promptField().value).toBe("submitted-sentinel-newer");
});

test("late missing-directory preflight keeps Create and start with its originating draft", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const validation = deferred<{ path: string; valid: boolean }>();
  const fake = readyClient((f) => f.on("evener/path/validate", () => validation.promise));
  renderSpawn(fake);
  await fillPrompt(user, "draft-a-sentinel");
  await user.click(screen.getByTestId("spawn-submit"));
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  await fillPrompt(user, "draft-b-sentinel");
  await act(async () => validation.resolve({ path: "/tmp/draft-a", valid: false }));
  expect(screen.queryByRole("dialog")).toBeNull();
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  await user.click(await screen.findByRole("button", { name: "Create & start" }));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  expect(fake.calls.find((call) => call.method === "evener/dirs/create")?.params).toEqual({ path: "/tmp/draft-a" });
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    cwd: "/tmp/draft-a",
    input: [{ type: "text", text: "draft-a-sentinel" }],
  });
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  expect(promptField().value).toBe("draft-b-sentinel");
});

test("late successful creation clears only the originating draft across remount and project navigation", async () => {
  installCanvasStubs();
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => f.on("thread/start", () => started.promise));
  const mounted = renderSpawn(fake);
  await fillPrompt(user, "submitted-sentinel");
  act(() => pastePngInto(promptField(), "submitted.png"));
  await screen.findByRole("button", { name: "View submitted.png" });
  fireEvent.change(effortControl(), { target: { value: "high" } });
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  mounted.unmount();
  renderSpawn(fake);
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  await fillPrompt(user, "other-project-sentinel");
  act(() => pastePngInto(promptField(), "other.png"));
  await screen.findByRole("button", { name: "View other.png" });
  await act(async () => started.resolve(startResponse("local:abc123")));
  expect(promptField().value).toBe("other-project-sentinel[image 1]");
  expect(screen.getByRole("button", { name: "View other.png" })).toBeTruthy();
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  expect(promptField().value).toBe("");
  expect(screen.queryByTestId("attachment-tile")).toBeNull();
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1);
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.working_dir")).toBe("/tmp/draft-a");
});

test("restoring a project's effort never clamps it against the previous project's catalog", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const fake = readyClient((f) => {
    f.on("model/list", ({ cwd }) => ({
      data: [
        {
          provider: "anthropic",
          model: "claude-sonnet-4-5",
          displayName: "anthropic/claude-sonnet-4-5",
          reasoningEffortLevels: cwd === "/tmp/draft-b" ? ["low"] : ["high"],
        },
      ],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "high", "none"]));
  await user.selectOptions(effortControl(), "high");
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "low", "none"]));
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "high", "none"]));
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
});

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime });
  vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "setInterval", "clearInterval"] });
  localStorage.clear();
  resetSpawnDraftsForTests();
  resetCredentialsStoreForTests();
  // The registry and the remote-host partitions are module singletons: a
  // leftover ready snapshot from another test would gate a remote read.
  resetHostInstancesForTests();
  hostsStore.getState().resetForTests();
  resetNavigationStoreForTests();
  modelListOverride = null;
});

test("missing credentials surface setup in the composer without opening a dialog or losing its draft", async () => {
  const user = setupUser();
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  const connect = await screen.findByRole("button", { name: "Connect provider" });
  expect(screen.queryByRole("dialog")).toBeNull();
  await fillPrompt(user, "draft-sentinel");
  await setWorkingDir(user, "/tmp/my-project");
  expect((screen.getByRole("button", { name: "Start" }) as HTMLButtonElement).disabled).toBe(true);
  // userEvent owns its async act environment. Await it before opening the
  // separate act scope for lazy-import completion; nesting races their flags.
  await user.click(connect);
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.getByText("Show all providers")).toBeTruthy();
  await user.keyboard("{Escape}");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("draft-sentinel");
  expectWorkingDir("/tmp/my-project");
});

test("connection handoff shows the actual instance models and preserves draft until explicit Start", async () => {
  const user = setupUser();
  let saved = false;
  const row = {
    name: "team-local",
    providerId: "team-local",
    protocol: "openai-chat",
    auth: "bearer",
    implicit: false,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    authModes: ["apiKey"],
    baseUrl: "https://team.example/v1",
    endpointFingerprint: "fp-team",
  };
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({
      instances: saved ? [{ ...row, activeSource: "store", hasStoredFile: true }] : [],
      availableProviders: [
        {
          id: row.providerId,
          name: "Team local",
          protocol: row.protocol,
          auth: row.auth,
          implicit: row.implicit,
          authModes: row.authModes,
          setup: row,
        },
      ],
    }));
    fake.on("model/list", () => ({
      data: saved
        ? [
            { provider: "team-local", model: "served-model", displayName: "Served model" },
            { provider: "other", model: "unrelated", displayName: "Unrelated model" },
          ]
        : [],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return { provider, supported: true, signedIn: true, activeSource: "store", hasStoredOAuth: false };
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await screen.findByRole("button", { name: "Connect provider" });
  await fillPrompt(user, "handoff-draft");
  await setWorkingDir(user, "/tmp/handoff-project");
  await user.click(screen.getByRole("button", { name: "Connect provider" }));
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  // team-local is not one of the curated providers the picker leads with, so
  // its card shows behind the catalogue disclosure.
  await user.click(await screen.findByText("Show all providers"));
  await user.click(screen.getByRole("button", { name: "Team local" }));
  await user.type(screen.getByLabelText("API key"), "fixture-only-key");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await user.click(await screen.findByRole("button", { name: "Continue" }));
  // The completed connection hands off to the picker, which reopens scoped to
  // the connected instance: its models are offered, the unrelated provider's
  // are not.
  const option = await screen.findByRole("option", { name: /Served model/ });
  expect(screen.queryByRole("option", { name: /Unrelated model/ })).toBeNull();
  expect(client.calls.filter((call) => call.method === "thread/start")).toEqual([]);
  expect(client.calls.filter((call) => call.method === "evener/instance/setDefault")).toEqual([]);
  expectWorkingDir("/tmp/handoff-project");
  expect(screen.getByRole("textbox", { name: "Prompt" })).toHaveProperty("value", "handoff-draft");
  await user.click(option);
  expect(modelValue().textContent).toBe("team-local/served-model");
  expect(client.calls.filter((call) => call.method === "thread/start")).toEqual([]);
  await user.click(screen.getByRole("button", { name: "Start" }));
  await waitFor(() => expect(client.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  expect(client.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    cwd: "/tmp/handoff-project",
    model: "team-local/served-model",
  });
});

test("fresh guided connection waits for Continue and explicit model choice without changing the draft", async () => {
  const user = setupUser();
  let saved = false;
  const setup = {
    name: "openai",
    providerId: "openai",
    protocol: "openai-chat",
    auth: "bearer",
    implicit: true,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    authModes: ["apiKey"],
    baseUrl: "https://provider.example/v1",
    endpointFingerprint: "fp-provider",
  };
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => {
      const row = { ...setup, activeSource: saved ? "store" : "none", hasStoredFile: saved };
      return {
        instances: saved ? [row] : [],
        availableProviders: [
          {
            id: "openai",
            name: "OpenAI",
            protocol: row.protocol,
            auth: row.auth,
            implicit: true,
            authModes: ["apiKey"],
            setup: row,
          },
        ],
      };
    });
    fake.on("model/list", () => ({
      data: saved ? [{ provider: "openai", model: "from-server", displayName: "Server choice" }] : [],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return {
        provider,
        supported: true,
        signedIn: true,
        activeSource: "store",
        hasStoredOAuth: false,
      };
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "missing/old-default" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await screen.findByRole("button", { name: "Connect provider" });
  await fillPrompt(user, "guided-draft");
  await setWorkingDir(user, "/tmp/guided");
  await user.click(screen.getByRole("button", { name: "Connect provider" }));
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  await user.click(await screen.findByRole("button", { name: "OpenAI" }));
  await user.type(screen.getByLabelText("API key"), "fixture-only-key");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  const next = await screen.findByRole("button", { name: "Continue" });
  expect(screen.queryByRole("option", { name: /Server choice/ })).toBeNull();
  expect(modelValue().textContent).not.toBe("openai/from-server");
  expect(client.calls.filter((call) => call.method === "thread/start")).toEqual([]);
  await user.click(next);
  await screen.findByRole("option", { name: /Server choice/ });
  expect(modelValue().textContent).not.toBe("openai/from-server");
  await user.keyboard("{Escape}");
  expectWorkingDir("/tmp/guided");
  expect(screen.getByRole("textbox", { name: "Prompt" })).toHaveProperty("value", "guided-draft");
  expect(modelValue().textContent).not.toBe("openai/from-server");
  expect(client.calls.filter((call) => call.method === "evener/instance/setDefault")).toEqual([]);
  expect(client.calls.filter((call) => call.method === "thread/start")).toEqual([]);
});

test("closing the handoff without choosing requires an explicit model before Start", async () => {
  const user = setupUser();
  let saved = false;
  const setup = {
    name: "openai",
    providerId: "openai",
    protocol: "openai-chat",
    auth: "bearer",
    implicit: true,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    authModes: ["apiKey"],
    baseUrl: "https://provider.example/v1",
    endpointFingerprint: "fp-provider",
  };
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => {
      const row = { ...setup, activeSource: saved ? "store" : "none", hasStoredFile: saved };
      return {
        instances: saved ? [row] : [],
        availableProviders: [
          {
            id: "openai",
            name: "OpenAI",
            protocol: row.protocol,
            auth: row.auth,
            implicit: true,
            authModes: ["apiKey"],
            setup: row,
          },
        ],
      };
    });
    fake.on("model/list", () => ({
      data: saved ? [{ provider: "openai", model: "from-server", displayName: "Server choice" }] : [],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return {
        provider,
        supported: true,
        signedIn: true,
        activeSource: "store",
        hasStoredOAuth: false,
      };
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({
      // A NON-empty default whose provider has no credentials: the exact case
      // the uncredentialed-default fallback exists for - and onboarding
      // suppresses that fallback in favor of the explicit choice.
      effective: { model: "missing/old-default" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await fillPrompt(user, "guided-draft");
  await setWorkingDir(user, "/tmp/guided-required");
  await user.click(screen.getByRole("button", { name: "Connect provider" }));
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  await user.click(await screen.findByRole("button", { name: "OpenAI" }));
  await user.type(screen.getByLabelText("API key"), "fixture-only-key");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await user.click(await screen.findByRole("button", { name: "Continue" }));
  await screen.findByRole("option", { name: /Server choice/ });
  await user.keyboard("{Escape}");
  // The resolved default is non-empty, so the noDefaultModel gate alone would
  // leave Start enabled - submitting into a certain thread/start failure on
  // the uncredentialed default. Onboarding suppressed the fallback, so the
  // explicit pick is required.
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
  await user.click(modelTrigger());
  await user.click(await screen.findByRole("option", { name: /Server choice/ }));
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
  await user.click(screen.getByRole("button", { name: "Start" }));
  await waitFor(() => expect(client.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  expect(client.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    model: "openai/from-server",
  });
});

test("retrying missing provider setup discovers a local server started afterward", async () => {
  const user = setupUser();
  let available = false;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({
      instances: [
        {
          name: "ollama",
          providerId: "ollama",
          protocol: "openai-chat",
          auth: "none",
          implicit: true,
          isDefault: true,
          activeSource: "none",
          hasStoredOAuth: false,
          credentialRequired: false,
        },
      ],
      availableProviders: [],
    }));
    fake.on("model/list", () => ({ data: available ? [{ provider: "ollama", model: "local-model" }] : [] }));
    fake.on("evener/auth/test", () => ({ provider: "ollama", status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "ollama/local-model" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await screen.findByRole("button", { name: "Connect provider" });
  const retry = screen.getByRole("button", { name: "Retry provider check" });
  available = true;
  await user.click(retry);
  await waitFor(() => expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull());
  await user.click(modelTrigger());
  expect(await screen.findByRole("option", { name: /local-model/ })).toBeTruthy();
});

test("successful keyless testing refreshes availability without an auth notification", async () => {
  const user = setupUser();
  let available = false;
  const keyless = {
    name: "ollama",
    providerId: "ollama",
    protocol: "openai-chat",
    auth: "none",
    implicit: true,
    isDefault: true,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: false,
    baseUrl: "http://localhost:11434/v1",
    endpointFingerprint: "fp-ollama",
  };
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({
      instances: [keyless],
      availableProviders: [
        {
          id: "ollama",
          name: "Local endpoint",
          protocol: "openai-chat",
          auth: "none",
          implicit: true,
          authModes: [],
          setup: keyless,
        },
      ],
    }));
    fake.on("model/list", () => ({ data: available ? [{ provider: "ollama", model: "local-model" }] : [] }));
    fake.on("evener/auth/test", () => ({ provider: "ollama", status: "success", message: "" }));
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "ollama/local-model" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  const connectProvider = await screen.findByRole("button", { name: "Connect provider" });
  // Finish the lazy dialog's mount and catalog refresh before retaining a button
  // reference: the refresh replaces the initially cached instance rows.
  await user.click(connectProvider);
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  await user.click(await screen.findByText("Show all providers"));
  await user.click(screen.getByRole("button", { name: "Local endpoint" }));
  const testConnection = await screen.findByRole("button", { name: "Check connection" });
  available = true;
  await user.click(testConnection);
  await user.click(await screen.findByRole("button", { name: "Continue" }));
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toEqual([
    { method: "evener/auth/test", params: { provider: "ollama", expectedEndpointFingerprint: "fp-ollama" } },
  ]);
  await waitFor(() => {
    expect(screen.queryByRole("option", { name: /local-model/ })).not.toBeNull();
    expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull();
  });
});

test("credential changes reload the cached model catalog and re-enter setup after removal", async () => {
  const user = setupUser();
  let configured = false;
  let modelRequests = 0;
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({
      instances: configured
        ? [
            {
              name: "work",
              providerId: "openai",
              protocol: "openai-responses",
              auth: "bearer",
              implicit: false,
              isDefault: true,
              activeSource: "store",
              hasStoredOAuth: false,
              credentialRequired: true,
            },
          ]
        : [],
      availableProviders: [],
    }));
    fake.on("model/list", () => {
      modelRequests++;
      return { data: configured ? [{ provider: "work", model: "test-model" }] : [] };
    });
    fake.on("evener/launch/resolve", () => ({ effective: { model: "work/test-model" }, layers: {}, provenance: {} }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await screen.findByRole("button", { name: "Connect provider" });
  await setWorkingDir(user, "/tmp/my-project");
  await fillPrompt(user, "draft-sentinel");
  const requestsBefore = modelRequests;
  configured = true;
  await act(async () => credentialsStore.getState().fetch());
  await waitFor(() => expect(modelRequests).toBeGreaterThan(requestsBefore));
  expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull();
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("draft-sentinel");
  configured = false;
  await act(async () => credentialsStore.getState().fetch());
  await screen.findByRole("button", { name: "Connect provider" });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
  resetNavigationStoreForTests();
  resetThreadsStoreForTests();
  vi.unstubAllGlobals();
  // The shell mounts the Spawn pane only at /new (shell/routing.ts), so a test
  // that sets no route of its own exercises the real mounted state: the active
  // New Session view.
  window.history.pushState({}, "", "/new");
  resetToastStoreForTests();
});

// --- the page's shape ------------------------------------------------------
//
// Prompt card first, taking the page's slack; ONE configuration row beneath it
// (working directory, model, effort); harness in Advanced options.

test("the directory is established before composing the prompt", async () => {
  renderSpawn(readyClient());
  await settled();

  const card = screen.getByTestId("spawn-prompt-card");
  const dir = screen.getByLabelText(/^Working directory:/, { selector: "#spawn-cwd" });
  expect(dir.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
});

test("the desktop directory trigger announces the confirmed path", async () => {
  const user = setupUser();
  renderSpawn(readyClient());
  await settled();
  await setWorkingDir(user, "/tmp/project");
  expect(screen.getByLabelText("Working directory: /tmp/project", { selector: "#spawn-cwd" })).toBe(workingDir());
});

test("the directory and git info sit above the prompt; model and effort live in the card", async () => {
  renderSpawn(readyClient());
  await settled();

  const dir = screen.getByLabelText(/^Working directory:/, { selector: "#spawn-cwd" });
  const card = screen.getByTestId("spawn-prompt-card");
  const controls = screen.getByTestId("spawn-controls");
  expect(dir.compareDocumentPosition(card) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  // Below-card model/effort fields are gone: the desktop field wrapper and the
  // bordered Effort select no longer render.
  expect(screen.queryByTestId("spawn-desktop-model")).toBeNull();
  expect(screen.queryByLabelText("Effort")).toBeNull();
  // Model + effort are the card's own controls, beside attach and Start.
  expect(card.contains(controls)).toBe(true);
  expect(card.contains(screen.getByTestId("spawn-attach"))).toBe(true);
  expect(card.contains(modelTrigger())).toBe(true);
  expect(card.contains(effortControl())).toBe(true);
  expect(controls.querySelector("[data-testid='spawn-submit']")).toBeTruthy();
  expect(screen.getByRole("textbox", { name: "Prompt" }).style.getPropertyValue("--textarea-min-lines")).toBe("6");
});

// Issue #198: the attach button, model trigger, and effort control are the
// composer's, in the composer's place - the card's own control row - rather
// than a fixed band at the foot of the viewport and bespoke rows in the
// settings list. The row order below is the Treatment A list plus
// session-only Plugins. Model AND effort left it when the card took the job.
test("mobile Spawn sets attachments, the model, and effort from inside the prompt card", async () => {
  renderSpawn(readyClient());
  await settled();

  const mobileConfig = screen.getByTestId("spawn-mobile-config");
  expect(
    [...mobileConfig.querySelectorAll<HTMLElement>("[data-testid='mobile-spawn-row']")].map((row) => row.dataset.label),
  ).toEqual(["Harness", "Working directory", "Branch", "Access mode", "Plugins"]);

  const card = screen.getByTestId("spawn-prompt-card");
  const controls = screen.getByTestId("spawn-controls");
  expect(card.contains(controls)).toBe(true);
  expect(card.contains(screen.getByTestId("spawn-attach"))).toBe(true);
  expect(card.contains(modelTrigger())).toBe(true);
  expect(card.contains(effortControl())).toBe(true);
  expect(controls.querySelector("[data-testid='spawn-submit']")).toBeTruthy();
  expect(screen.getByRole("textbox", { name: "Prompt" }).style.getPropertyValue("--textarea-min-lines")).toBe("6");
});

// The card's trigger says what the Model field says: "(default)" while the
// hub's own default will do, the chosen id once someone picks one.
test("the card's model trigger reads (default) until a model is picked, then names it", async () => {
  const user = setupUser();
  renderSpawn(readyClient());
  await settled();

  expect(modelValue().textContent).toBe("(default)");

  await user.click(modelTrigger());
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.clear(combo);
  await user.type(combo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  await waitFor(() => expect(modelValue().textContent).toBe("openai/gpt-5"));
});

// kata xgk8: a hub with no default to fall back on must not offer "(default)"
// anywhere, including on the card - the word reads exactly like Effort's own
// working default and invites a submit the daemon refuses.
test("the card's model trigger names the required choice when the hub has no default", async () => {
  const user = setupUser();
  renderSpawn(
    readyClient((f) => {
      f.on("evener/launch/resolve", () => ({ effective: { model: "" }, layers: {}, provenance: {} }));
      f.on("model/list", () => ({ data: [] }));
    }),
  );
  await settled();
  await setWorkingDir(user, "/tmp/project");

  await waitFor(() => expect(modelValue().textContent).toBe("Choose a model"));
});

test("mobile Spawn keeps the approved prompt hierarchy visible while the prompt is typed", async () => {
  const user = setupUser();
  renderSpawn(readyClient());
  await settled();

  await fillPrompt(user, "typed mobile work");

  expect(screen.getByTestId("pane-title-mobile").textContent).toBe("New session");
  expect(screen.getByRole("heading", { name: "What should the agent do?" })).toBeTruthy();
  expect(screen.getByText("Leave blank to start a dormant session.")).toBeTruthy();
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("typed mobile work");
});

// The placeholder repeated the heading and subtitle standing right above it
// almost word for word, so the field spent its one line saying what the page
// had already said. The dormant-start rule it also carried stays on the page,
// in that intro.
test("the prompt placeholder does not repeat the heading", async () => {
  renderSpawn(readyClient());
  await settled();

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
  expect(prompt.placeholder).toBe("Describe the task…");
  expect(screen.getByText("Leave blank to start a dormant session.")).toBeTruthy();
});

// The card's control-row compression and touch floors are pinned by the
// spawn-attach-in-card layoutguard case (geometric assertions against the
// real cascade at phone width: containment, active ellipsis on a long model
// id, the 44px effort tap floor), not by source-text matching here - a CSS
// grep passes even when the rules are unused or overridden.
test("mobile-only spawn hierarchy and row scale stay gated from desktop", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const spawnCss = readFileSync(join(here, "spawn.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const rowsCss = readFileSync(join(here, "MobileSettingRows.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const paneCss = readFileSync(join(here, "../../widgets/panescaffold/panescaffold.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    "",
  );

  // The prompt heading shows at every width now (critique R7); only the
  // settings-style rows stay phone-only.
  expect(spawnCss).toContain(".promptIntro");
  expect(spawnCss).not.toContain(".mobilePromptIntro");
  expect(spawnCss).toContain(".mobileConfig");
  expect(spawnCss).toContain("@media (max-width: 899px)");
  expect(rowsCss).toContain("min-height: 48px");
  expect(rowsCss).toContain("font-size: var(--font-size-body)");
  expect(paneCss).toContain(".desktopTitle");
  expect(paneCss).toContain(".mobileTitle");
  expect(paneCss).toContain("@media (max-width: 899px)");
});

// Harness moves into Advanced options: most installs have exactly one, so a
// field whose answer is always "evener" shouldn't lead the page. It stays fully
// functional there - the switch still blanks a non-evener model (see the harness
// tests in spawnHarnessModels.test.ts for that rule's own coverage).
test("harness moved into Advanced options, and still works there", async () => {
  const user = setupUser();
  renderSpawn(readyClient());
  await settled();

  expect(screen.queryByLabelText("Harness")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const harness = screen.getByLabelText("Harness") as HTMLSelectElement;
  await user.selectOptions(harness, "external");
  expect(harness.value).toBe("external");
});

// The approved mobile Treatment A action band uses the direct user-facing
// action "Start". The page title remains the existing React identity.
test("the primary verb is Start, in the card's own corner, and the page is titled to match", async () => {
  renderSpawn(readyClient());
  await settled();

  const start = screen.getByTestId("spawn-submit");
  expect(start.textContent).toBe("Start");
  expect(screen.getByTestId("pane-title-desktop").textContent).toBe("New session");
  // Inside the card, not in a detached actions strip below it.
  expect(screen.getByTestId("spawn-prompt-card").contains(start)).toBe(true);
  expect(screen.getByRole("button", { name: "Start" })).toBeTruthy();
});

// The word beside the paper plane collapses by PANE width, not viewport
// width: a docked pane squeezed narrow on a desktop display needs the same
// icon-only button the phone gets, and a viewport media query cannot see that
// (the overflowguard's 390px-pane-in-desktop-window measurement proved it).
// The 559px boundary matches the composer cluster's own compact threshold
// (SessionChrome's GoalControl chip swap).
test("the Start button's word collapses to the glyph below the compact pane threshold", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "spawn.module.css"), "utf8");
  expect(css).toMatch(/\.form\s*\{[^}]*container-type:\s*inline-size/);
  expect(css).toMatch(/@container \(max-width: 559px\)[\s\S]*?\.submitLabel\s*\{[^}]*display:\s*none/);
});

// Writing the prompt is what starting an agent IS, so the caret starts there
// rather than on whichever field happens to be first in the DOM.
test("the prompt field is focused on mount", async () => {
  renderSpawn(readyClient());
  await settled();
  expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Prompt" }));
});

// An unfocused spawn pane (a background tab) must never yank keyboard focus,
// and mobile must never pop the on-screen keyboard on load.
test("an unfocused pane never focuses the prompt field on mount", async () => {
  renderSpawn(readyClient(), false);
  await settled();
  expect(document.activeElement).not.toBe(screen.getByRole("textbox", { name: "Prompt" }));
});

test("a focused pane never focuses the prompt field on mount on mobile", async () => {
  vi.stubGlobal("matchMedia", ((query: string) => ({
    matches: true,
    media: query,
    addEventListener() {},
    removeEventListener() {},
  })) as unknown as typeof window.matchMedia);
  renderSpawn(readyClient());
  await settled();
  expect(document.activeElement).not.toBe(screen.getByRole("textbox", { name: "Prompt" }));
});

// The card and the session composer are the SAME object: both render
// widgets/promptcard. The class on the rendered card is the proof that reaches
// across both files (Composer.test.tsx asserts the mirror image).
test("the prompt card IS the shared PromptCard widget, not a lookalike", async () => {
  renderSpawn(readyClient());
  await settled();
  expect(screen.getByTestId("spawn-prompt-card").className.split(" ")).toContain(promptCardStyles.card);
});

// The card draws the one border and owns the focus ring, so the field inside
// must draw neither - otherwise it is a box inside a box. Caught in Chrome: the
// field rendered its own border inside the card's and its resize grabber floated
// loose in the corner between the two.
test("the prompt field is seamless, so the card's border is the only one", async () => {
  renderSpawn(readyClient());
  await settled();
  expect(screen.getByRole("textbox", { name: "Prompt" }).className.split(" ")).toContain(textareaStyles.seamless);
});

// The prompt takes the page's vertical slack via its own min-height, which is
// what closes the dead gap that used to sit under the actions row.
test("the prompt field opens at a size worth writing in, not one line", async () => {
  renderSpawn(readyClient());
  await settled();
  expect(
    (screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).style.getPropertyValue(
      "--textarea-min-lines",
    ),
  ).toBe("6");
});

// --- branch: a read-only HEAD readout on the directory row -----------------

test("branch renders as a suffix on the directory row, not as an editable peer field", async () => {
  // The readout's only source is the chosen directory's HEAD, so a working
  // directory has to exist before there is a branch to show - an empty cwd
  // correctly renders nothing. ?dir= is how every other test here seeds one.
  window.history.pushState({}, "", "/new?dir=%2Fhome%2Fme%2Fapp");
  renderSpawn(readyClient());
  await settled();

  await waitFor(() => expect(screen.getByTestId("spawn-branch").textContent).toContain("main"));
  // Not a text box: it is a readout of the directory's HEAD.
  expect(screen.queryByLabelText("Branch")).toBeNull();
  expect(screen.getByTestId("spawn-branch").querySelector("input")).toBeNull();
});

test("the branch readout is absent when the working directory has no resolvable HEAD", async () => {
  localStorage.setItem("evener-hub.spawn-defaults.global.working_dir", "/tmp/plain");
  renderSpawn(
    readyClient((fake) => {
      fake.on("evener/git/head", () => {
        throw new Error("git head unavailable");
      });
    }),
  );
  await settled();

  await waitFor(() => expectWorkingDir("/tmp/plain"));
  expect(screen.queryByTestId("spawn-branch")).toBeNull();
});

// The icon controls draw real SVG glyphs rather than bare "+"/"×" characters,
// matching the composer's own attach control. Their spoken names come from
// IconButton's label either way.
test("the attach control draws an SVG glyph, not a literal text character", async () => {
  renderSpawn(readyClient());
  await settled();

  const attach = screen.getByRole("button", { name: "Attach image" });
  expect(attach.querySelector("svg")).toBeTruthy();
  expect(attach.textContent).toBe("");
});

test("Access mode moved from the top-level bar into Advanced options (9ct0)", async () => {
  const user = setupUser();
  renderSpawn(readyClient());
  await settled();

  expect(screen.queryByLabelText("Access mode")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  expect(screen.getByLabelText("Access mode")).toBeTruthy();
});

test("a full submit sends the cwd, prompt, and access-mode sandbox, then routes to /s/{ref}", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "do the thing");
  await setWorkingDir(user, "/tmp/project");
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Access mode"), "Read-only");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({
    cwd: "/tmp/project",
    input: [{ type: "text", text: "do the thing" }],
    launchOverrides: { sandbox: "read-only" },
  });
  // Sticky defaults persist the working dir globally on submit (floor §1.9).
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.working_dir")).toBe("/tmp/project");
});

test("Spawn preview omits enabledPlugins while selection remains untouched", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "evener/plugin/preview").at(-1)?.params).toEqual({
      cwd: "/tmp/project",
    }),
  );
});

test("desktop plugin summary remains mounted with exact loading and error status", async () => {
  const pending = new Promise<PluginPreviewResponse>(() => {});
  const pendingClient = readyClient((f) => f.on("evener/plugin/preview", () => pending));
  renderSpawn(pendingClient);
  expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("Inspecting plugins…");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);

  cleanup();
  const errorClient = readyClient((f) => {
    f.on("evener/plugin/preview", () => {
      throw new Error("preview unavailable");
    });
  });
  renderSpawn(errorClient);
  await waitFor(() =>
    expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("Couldn't inspect plugins"),
  );
  // The failure says WHY, and the retry is a small inline action.
  expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("preview unavailable");
  expect(screen.getByTestId("spawn-plugin-summary").textContent).not.toContain("0 of 0");
  expect(within(screen.getByTestId("spawn-plugin-summary")).getByRole("button", { name: "Retry" })).toBeTruthy();
});

const SPAWN_PLUGIN_PREVIEW: PluginPreviewResponse = {
  plugins: [
    {
      name: "alpha",
      source: "installed",
      selected: true,
      skillCount: 1,
      agentCount: 0,
      commandCount: 0,
      hookCount: 0,
      mcpCount: 0,
    },
    {
      name: "beta",
      source: "directory",
      path: "/tmp/beta",
      selected: true,
      skillCount: 0,
      agentCount: 0,
      commandCount: 1,
      hookCount: 0,
      mcpCount: 0,
    },
  ],
};

test("desktop plugin summary lists the configured plugin names", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", () => SPAWN_PLUGIN_PREVIEW);
  });
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");

  await waitFor(() =>
    expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("Configured plugins: alpha, beta"),
  );
  expect(screen.getByTestId("spawn-plugin-summary").textContent).not.toContain("session only");

  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));
  await waitFor(() =>
    expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("Configured plugins: alpha"),
  );
  expect(screen.getByTestId("spawn-plugin-summary").textContent).not.toContain("beta");
});

async function openDesktopPluginSelection(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await waitFor(() => expect(screen.getByTestId("spawn-plugin-disclosure")).toBeTruthy());
  const disclosure = screen.getByTestId("spawn-plugin-disclosure") as HTMLDetailsElement;
  if (!disclosure.open) await user.click(screen.getByText("Plugins for this session"));
}

// A known issue belongs to the selected plugin in this draft, not the mounted
// form, current URL prefill revision, or whichever request finishes last.
test.each(["bare return", "picker return", "remount", "unchanged"])(
  "completion issue ownership: known-invalid selection survives %s and failed previews",
  async (scenario) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
    let previewMode: "invalid" | "error" | "valid" = "invalid";
    const fake = readyClient((f) => {
      f.on("evener/plugin/preview", ({ cwd, launchOverrides }) => {
        if (previewMode === "error") throw new Error("preview unavailable");
        if (cwd === "/tmp/completion-a" && previewMode === "invalid" && launchOverrides?.enabledPlugins) {
          return { ...SPAWN_PLUGIN_PREVIEW, selectionErrors: [{ name: "alpha", reason: "plugin is unavailable" }] };
        }
        return SPAWN_PLUGIN_PREVIEW;
      });
    });
    let mounted = renderSpawn(fake);
    await fillPrompt(user, "unsent-a");
    await openDesktopPluginSelection(user);
    await user.click(screen.getByRole("switch", { name: "beta" }));
    await screen.findByRole("button", { name: "Remove alpha" });
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
    // An unrelated selection edit cannot forgive alpha when its refresh fails.
    previewMode = "error";
    await user.click(screen.getByRole("switch", { name: "beta" }));
    await screen.findAllByText("Couldn't inspect plugins");
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
    if (scenario === "bare return") {
      await completionNavigate("/settings");
      await completionNavigate("/new");
    } else if (scenario === "picker return") {
      await setWorkingDir(user, "/tmp/completion-b");
      await screen.findAllByText("Couldn't inspect plugins");
      expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
      await setWorkingDir(user, "/tmp/completion-a");
    } else if (scenario === "remount") {
      mounted.unmount();
      await completionNavigate("/settings");
      await completionNavigate("/new");
      mounted = renderSpawn(fake);
    }
    await screen.findAllByText("Couldn't inspect plugins");
    expect(promptField().value).toBe("unsent-a");
    expect(completionDraft("/tmp/completion-a").fields.getState().pluginSelection).toEqual({
      mode: "explicit",
      names: ["alpha", "beta"],
    });
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
    await user.click(promptField());
    await user.keyboard("{Meta>}{Enter}{/Meta}");
    expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(0);
    // A genuinely valid preview of the SAME selection is authoritative.
    previewMode = "valid";
    await user.click(within(screen.getByTestId("spawn-plugin-summary")).getByRole("button", { name: "Retry" }));
    await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
    expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
      cwd: "/tmp/completion-a",
      launchOverrides: { enabledPlugins: ["alpha", "beta"] },
    });
  },
);

test.each(["failed", "loading then failed", "ready invalid", "newer origin selection"])(
  "completion issue ownership: late A preserves B's %s preview gate",
  async (scenario) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
    const started = deferred<ThreadStartResponse>();
    const refresh = deferred<PluginPreviewResponse>();
    let refreshRequested = false;
    const fake = readyClient((f) => {
      f.on("thread/start", () => started.promise);
      f.on("evener/plugin/preview", ({ cwd, launchOverrides }) => {
        const names = launchOverrides?.enabledPlugins;
        if (cwd === "/tmp/completion-b" && names?.length === 1) {
          return { ...SPAWN_PLUGIN_PREVIEW, selectionErrors: [{ name: "alpha", reason: "plugin is unavailable" }] };
        }
        if (cwd === "/tmp/completion-b" && names?.length === 2) {
          refreshRequested = true;
          return refresh.promise;
        }
        return SPAWN_PLUGIN_PREVIEW;
      });
    });
    renderSpawn(fake);
    await openDesktopPluginSelection(user);
    await user.click(screen.getByRole("switch", { name: "beta" }));
    await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
    await fillPrompt(user, "submitted-a");
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
    if (scenario === "newer origin selection") await user.click(screen.getByRole("button", { name: "None" }));
    await setWorkingDir(user, "/tmp/completion-b");
    await fillPrompt(user, "unsent-b");
    await openDesktopPluginSelection(user);
    await user.click(screen.getByRole("switch", { name: "beta" }));
    await screen.findByRole("button", { name: "Remove alpha" });
    if (scenario !== "ready invalid") {
      await user.click(screen.getByRole("switch", { name: "beta" }));
      await waitFor(() => expect(refreshRequested).toBe(true));
      if (scenario !== "loading then failed") {
        await act(async () => refresh.reject(new Error("preview unavailable")));
        await screen.findAllByText("Couldn't inspect plugins");
      }
    }
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
    await act(async () => started.resolve(startResponse("local:completion-a")));
    await waitFor(() => expect(completionDraft("/tmp/completion-a").fields.getState().busy).toBe(false));
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
    if (scenario === "loading then failed") {
      await act(async () => refresh.reject(new Error("preview unavailable")));
      await screen.findAllByText("Couldn't inspect plugins");
    }
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
    expect(promptField().value).toBe("unsent-b");
    expect(window.location.pathname).toBe("/new");
    expect(completionDraft("/tmp/completion-a").fields.getState().pluginSelection).toEqual(
      scenario === "newer origin selection" ? { mode: "explicit", names: [] } : { mode: "default" },
    );
    await user.click(promptField());
    await user.keyboard("{Meta>}{Enter}{/Meta}");
    expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1);
  },
);

test.each(["remove plugin", "unsupported harness"])(
  "completion issue ownership: %s retires known issues",
  async (scenario) => {
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
    let failPreview = false;
    const fake = readyClient((f) =>
      f.on("evener/plugin/preview", ({ launchOverrides }) => {
        if (failPreview) throw new Error("preview unavailable");
        return launchOverrides?.enabledPlugins
          ? { ...SPAWN_PLUGIN_PREVIEW, selectionErrors: [{ name: "alpha", reason: "plugin is unavailable" }] }
          : SPAWN_PLUGIN_PREVIEW;
      }),
    );
    renderSpawn(fake);
    await openDesktopPluginSelection(user);
    await user.click(screen.getByRole("switch", { name: "beta" }));
    await screen.findByRole("button", { name: "Remove alpha" });
    failPreview = true;
    if (scenario === "remove plugin") {
      await user.click(screen.getByRole("button", { name: "Remove alpha" }));
      await screen.findAllByText("Couldn't inspect plugins");
    } else {
      await user.click(screen.getByRole("button", { name: "Advanced options" }));
      await user.selectOptions(screen.getByLabelText("Harness"), "external");
    }
    await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
    const call = fake.calls.find((call) => call.method === "thread/start");
    if (!call) throw new Error("start was not issued");
    if (scenario === "remove plugin") expect(call.params).toMatchObject({ launchOverrides: { enabledPlugins: [] } });
    else expect((call.params as ThreadStartParams).launchOverrides?.enabledPlugins).toBeUndefined();
  },
);

for (const navigation of ["picker", "URL"] as const) {
  const projectA = "/tmp/plugin-project-a";
  const projectB = "/tmp/plugin-project-b";
  const plugin = SPAWN_PLUGIN_PREVIEW.plugins[0];
  if (!plugin) throw new Error("plugin preview fixture is empty");
  const previewA: PluginPreviewResponse = {
    plugins: [{ ...plugin, name: "a-only" }],
  };
  const previewB: PluginPreviewResponse = {
    plugins: [{ ...plugin, name: "b-only" }],
  };
  const navigate = async (user: ReturnType<typeof userEvent.setup>, cwd: string) => {
    if (navigation === "picker") await setWorkingDir(user, cwd);
    else await visitSpawnURL(`/new?dir=${cwd}`);
  };

  test(`${navigation}: a ready preview from A cannot invalidate B's restored selection after B's preview fails`, async () => {
    const user = setupUser();
    window.history.pushState({}, "", `/new?dir=${projectB}`);
    let returningToB = false;
    let rejectB: ((error: Error) => void) | undefined;
    const fake = readyClient((f) => {
      f.on("evener/plugin/preview", ({ cwd }) => {
        if (cwd === projectA) return previewA;
        if (returningToB) return new Promise<PluginPreviewResponse>((_, reject) => (rejectB = reject));
        return previewB;
      });
    });
    renderSpawn(fake);
    await openDesktopPluginSelection(user);
    await user.click(screen.getByRole("button", { name: "All" }));
    await waitFor(() =>
      expect(fake.calls.filter((call) => call.method === "evener/plugin/preview").at(-1)?.params).toMatchObject({
        cwd: projectB,
        launchOverrides: { enabledPlugins: ["b-only"] },
      }),
    );
    await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));

    await navigate(user, projectA);
    await waitFor(() => expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("a-only"));
    returningToB = true;
    await navigate(user, projectB);
    await waitFor(() => expect(rejectB).toBeTypeOf("function"));
    await act(async () => {
      if (!rejectB) throw new Error("B preview was not requested");
      rejectB(new Error("B preview unavailable"));
    });
    await screen.findAllByText("Couldn't inspect plugins");

    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() =>
      expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
        cwd: projectB,
        launchOverrides: { enabledPlugins: ["b-only"] },
      }),
    );
  });

  test(`${navigation}: a ready preview from A cannot block default B while B's preview is pending`, async () => {
    const user = setupUser();
    window.history.pushState({}, "", `/new?dir=${projectA}`);
    const fake = readyClient((f) => {
      f.on("evener/plugin/preview", ({ cwd }) =>
        cwd === projectA
          ? { ...previewA, selectionErrors: [{ name: "a-gone", reason: "unavailable" }] }
          : new Promise<PluginPreviewResponse>(() => {}),
      );
    });
    renderSpawn(fake);
    await waitFor(() => expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("a-only"));
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

    await navigate(user, projectB);
    await waitFor(() =>
      expect(fake.calls.filter((call) => call.method === "evener/plugin/preview").at(-1)?.params).toEqual({
        cwd: projectB,
      }),
    );
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
    // The directory picker moved focus; the submit shortcut belongs to the prompt.
    await user.click(screen.getByRole("textbox", { name: "Prompt" }));
    await user.keyboard("{Meta>}{Enter}{/Meta}");
    await waitFor(() =>
      expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({ cwd: projectB }),
    );
  });
}

test("explicit plugin selection reaches Preview, resolve, and Thread Start", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", () => SPAWN_PLUGIN_PREVIEW);
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));

  await waitFor(() => {
    expect(fake.calls.filter((call) => call.method === "evener/plugin/preview").at(-1)?.params).toMatchObject({
      cwd: "/tmp/project",
      launchOverrides: { enabledPlugins: ["alpha"] },
    });
  });
  await waitFor(() => {
    expect(fake.calls.filter((call) => call.method === "evener/launch/resolve").at(-1)?.params).toMatchObject({
      cwd: "/tmp/project",
      launchOverrides: { enabledPlugins: ["alpha"] },
    });
  });
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    launchOverrides: { enabledPlugins: ["alpha"] },
  });
  await waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "evener/plugin/preview").at(-1)?.params).toEqual({
      cwd: "/tmp/project",
    }),
  );
});

test("a missing working directory still exposes plugin selection before Create & start", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", (params) => {
      if (params.cwd === "/tmp/new") return SPAWN_PLUGIN_PREVIEW;
      return { plugins: [] };
    });
    f.on("evener/path/validate", () => ({
      path: "/tmp/new",
      valid: false,
      error: "stat /tmp/new: no such file or directory",
    }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/new");
  renderSpawn(fake);
  await settled();

  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));
  await waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "evener/plugin/preview").at(-1)?.params).toMatchObject({
      cwd: "/tmp/new",
      launchOverrides: { enabledPlugins: ["alpha"] },
    }),
  );

  await user.click(screen.getByTestId("spawn-submit"));
  await user.click(await screen.findByRole("button", { name: "Create & start" }));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    cwd: "/tmp/new",
    launchOverrides: { enabledPlugins: ["alpha"] },
  });
});

test("explicit selection blocks while refresh is pending but preview failure still submits to server validation", async () => {
  const user = setupUser();
  let rejectRefresh!: (reason?: unknown) => void;
  const refreshPending = new Promise<PluginPreviewResponse>((_, reject) => {
    rejectRefresh = reject;
  });
  let explicitPreviewCalls = 0;
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", (params) => {
      const names = params.launchOverrides?.enabledPlugins;
      if (Array.isArray(names) && names.length === 1 && names[0] === "alpha") {
        explicitPreviewCalls += 1;
        if (explicitPreviewCalls === 2) return refreshPending;
      }
      return SPAWN_PLUGIN_PREVIEW;
    });
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));
  await waitFor(() => expect(explicitPreviewCalls).toBe(1));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));

  // A plugin notification starts a fresh inspection while the explicit allow-list remains selected.
  await act(async () => {
    // The Spawn pane's preview refresh is exercised by changing its revision through
    // the same notification path as the connected app shell.
    fake.emitNotification({ method: "evener/plugin/updated", params: {} } as AnyNotification);
  });
  expect(extensionsStore.getState().pluginRevision).toBe(1);
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true));
  await waitFor(() => expect(explicitPreviewCalls).toBe(2));
  // The refresh must not unmount the list: the previous plugins stay visible
  // (and toggleable) while the new inspection is in flight.
  expect(screen.getByRole("switch", { name: "alpha" })).toBeTruthy();
  expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("Configured plugins: alpha");

  rejectRefresh(new Error("preview unavailable"));
  await waitFor(() => expect(screen.getAllByText("Couldn't inspect plugins").length).toBeGreaterThan(0));
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);

  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    launchOverrides: { enabledPlugins: ["alpha"] },
  });
});

test("known-invalid explicit selection stays blocked when an unrelated edit's refresh fails", async () => {
  const user = setupUser();
  let rejectRefresh!: (reason?: unknown) => void;
  const refreshPending = new Promise<PluginPreviewResponse>((_, reject) => {
    rejectRefresh = reject;
  });
  const invalidPreview: PluginPreviewResponse = {
    ...SPAWN_PLUGIN_PREVIEW,
    selectionErrors: [{ name: "alpha", reason: "plugin is unavailable" }],
  };
  let explicitPreviewCalls = 0;
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", (params) => {
      const names = params.launchOverrides?.enabledPlugins;
      if (Array.isArray(names) && names.length === 1 && names[0] === "alpha") {
        explicitPreviewCalls += 1;
        return invalidPreview;
      }
      if (Array.isArray(names) && names.length === 2 && names.includes("alpha") && names.includes("beta")) {
        explicitPreviewCalls += 1;
        return refreshPending;
      }
      return SPAWN_PLUGIN_PREVIEW;
    });
    f.on("thread/start", () => {
      throw new Error("start must not be reached");
    });
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));
  await waitFor(() => expect(explicitPreviewCalls).toBe(1));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true));

  await user.click(screen.getByRole("switch", { name: "beta" }));
  await waitFor(() => expect(explicitPreviewCalls).toBe(2));
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  rejectRefresh(new Error("preview unavailable"));
  await waitFor(() => expect(screen.getAllByText("Couldn't inspect plugins").length).toBeGreaterThan(0));
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  await user.keyboard("{Meta>}{Enter}{/Meta}");
  expect(fake.calls.some((call) => call.method === "thread/start")).toBe(false);
});

test("explicit empty plugin selection reaches Thread Start as an empty list", async () => {
  const user = setupUser();
  const fake = readyClient((f) => f.on("evener/plugin/preview", () => SPAWN_PLUGIN_PREVIEW));
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("button", { name: "None" }));
  await waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "evener/plugin/preview").at(-1)?.params).toMatchObject({
      launchOverrides: { enabledPlugins: [] },
    }),
  );
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    launchOverrides: { enabledPlugins: [] },
  });
});

test("selection survives Advanced-options updates and failed Start", async () => {
  const user = setupUser();
  const advancedOption: LaunchOption = {
    field: "maxRounds",
    wireField: "maxRounds",
    label: "Max rounds",
    group: "general",
    kind: "integer",
    perLaunch: true,
  };
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", () => SPAWN_PLUGIN_PREVIEW);
    f.on("evener/launch/schema", () => ({ options: [advancedOption] }));
    f.on("thread/start", () => {
      throw new Error("start failed");
    });
  });
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));
  await waitFor(() => expect(screen.getByRole("switch", { name: "beta" }).getAttribute("aria-checked")).toBe("false"));

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.clear(screen.getByLabelText("Max rounds"));
  await user.type(screen.getByLabelText("Max rounds"), "7");
  await waitFor(() => {
    expect(fake.calls.filter((call) => call.method === "evener/plugin/preview").at(-1)?.params).toMatchObject({
      launchOverrides: { enabledPlugins: ["alpha"], maxRounds: 7 },
    });
  });
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  expect(screen.getByRole("switch", { name: "beta" }).getAttribute("aria-checked")).toBe("false");
});

test("preview failure exposes retry without guessing zero or blocking default Start", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", () => {
      throw new Error("preview unavailable");
    });
  });
  renderSpawn(fake);

  await waitFor(() => expect(screen.getAllByText("Couldn't inspect plugins").length).toBeGreaterThan(0));
  expect(screen.queryByText(/0 of 0/)).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
  await user.click(screen.getAllByRole("button", { name: "Retry" })[0]!);
  await waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "evener/plugin/preview").length).toBeGreaterThan(1),
  );
});

test("retained stale plugin names block every submit path until removed", async () => {
  const user = setupUser();
  const stalePreview: PluginPreviewResponse = {
    plugins: [
      { ...SPAWN_PLUGIN_PREVIEW.plugins[0]!, name: "alpha" },
      { ...SPAWN_PLUGIN_PREVIEW.plugins[1]!, name: "gone" },
      { ...SPAWN_PLUGIN_PREVIEW.plugins[1]!, name: "beta" },
    ],
  };
  const refreshedPreview: PluginPreviewResponse = {
    plugins: [{ ...SPAWN_PLUGIN_PREVIEW.plugins[0]!, name: "alpha" }],
  };
  const advancedOption: LaunchOption = {
    field: "maxRounds",
    wireField: "maxRounds",
    label: "Max rounds",
    group: "general",
    kind: "integer",
    perLaunch: true,
  };
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", (params) =>
      params.launchOverrides?.maxRounds === 7 ? refreshedPreview : stalePreview,
    );
    f.on("evener/launch/schema", () => ({ options: [advancedOption] }));
    f.on("thread/start", () => {
      throw new Error("start failed");
    });
  });
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));
  await waitFor(() => expect(screen.getByTestId("spawn-plugin-disclosure")).toBeTruthy());

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.clear(screen.getByLabelText("Max rounds"));
  await user.type(screen.getByLabelText("Max rounds"), "7");
  await waitFor(() => expect(screen.getByRole("button", { name: "Remove gone" })).toBeTruthy());

  const pathValidateCallsBefore = fake.calls.filter((call) => call.method === "evener/path/validate").length;
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await user.click(screen.getByTestId("spawn-submit"));
  expect(fake.calls.filter((call) => call.method === "evener/path/validate")).toHaveLength(pathValidateCallsBefore);
  expect(fake.calls.some((call) => call.method === "thread/start")).toBe(false);
  expect(screen.getByRole("button", { name: "Remove gone" })).toBeTruthy();

  await user.click(screen.getByRole("button", { name: "Remove gone" }));
  await waitFor(() => {
    expect(fake.calls.filter((call) => call.method === "evener/plugin/preview").at(-1)?.params).toMatchObject({
      launchOverrides: { enabledPlugins: ["alpha"], maxRounds: 7 },
    });
  });
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
});

test("switching to a non-Evener harness hides plugins and clears explicit selection from start", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", () => SPAWN_PLUGIN_PREVIEW);
  });
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));
  await waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "evener/plugin/preview").at(-1)?.params).toMatchObject({
      launchOverrides: { enabledPlugins: ["alpha"] },
    }),
  );

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Harness"), "external");

  expect(screen.queryByTestId("spawn-plugin-desktop")).toBeNull();
  expect(
    [
      ...screen.getByTestId("spawn-mobile-config").querySelectorAll<HTMLElement>("[data-testid='mobile-spawn-row']"),
    ].map((row) => row.dataset.label),
  ).not.toContain("Plugins");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  const start = fake.calls.find((call) => call.method === "thread/start");
  expect(start?.params).not.toMatchObject({ launchOverrides: { enabledPlugins: ["alpha"] } });
});

test("clearing an invalid selection after preview failure reaches Create & start", async () => {
  const user = setupUser();
  let previewAvailable = true;
  const invalidPreview: PluginPreviewResponse = {
    ...SPAWN_PLUGIN_PREVIEW,
    selectionErrors: [{ name: "alpha", reason: "plugin is unavailable" }],
  };
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", (params) => {
      if (!previewAvailable) throw new Error("preview unavailable");
      if (params.launchOverrides?.enabledPlugins) return invalidPreview;
      return SPAWN_PLUGIN_PREVIEW;
    });
    f.on("evener/path/validate", () => ({
      path: "/tmp/new",
      valid: false,
      error: "stat /tmp/new: no such file or directory",
    }));
  });
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/new");
  renderSpawn(fake);
  await settled();
  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));
  await waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "evener/plugin/preview").at(-1)?.params).toMatchObject({
      launchOverrides: { enabledPlugins: ["alpha"] },
    }),
  );
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true));

  previewAvailable = false;
  await user.click(screen.getByRole("button", { name: "Retry" }));
  await waitFor(() => expect(screen.getAllByText("Couldn't inspect plugins").length).toBeGreaterThan(0));

  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByRole("button", { name: "None" }));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await user.click(await screen.findByRole("button", { name: "Create & start" }));

  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({
    cwd: "/tmp/new",
    launchOverrides: { enabledPlugins: [] },
  });
});

// A blank prompt starts a DORMANT session, exactly as the placeholder
// promises. The daemon honours it: hubThreadStart calls StartTurn only when
// len(params.Input) > 0 (cmd/evener-hub/app_threadlifecycle.go), and buildInput
// drops a blank prompt, so the wire carries input: [] - the session is created
// and no turn is started.
test("an empty prompt starts a dormant session rather than erroring", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ cwd: "/tmp/project", input: [] });
  expect(screen.queryByText(/prompt is empty/i)).toBeNull();
});

// Whitespace is a blank prompt: buildInput keeps the text item only when it is
// non-empty AFTER trimming, so "   " takes the same dormant path rather than
// starting a turn that says nothing.
test("a whitespace-only prompt starts a dormant session too", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "   ");
  await setWorkingDir(user, "/tmp/project");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ input: [] });
});

test("loads sticky defaults from localStorage on mount", async () => {
  const user = setupUser();
  localStorage.setItem("evener-hub.spawn-defaults.global.working_dir", "/saved/project");
  localStorage.setItem("evener-hub.spawn-defaults./saved/project", JSON.stringify({ access_mode: "workspace-write" }));
  renderSpawn(readyClient());

  await waitFor(() => expectWorkingDir("/saved/project"));
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  expect((screen.getByLabelText("Access mode") as HTMLSelectElement).value).toBe("workspace-write");
});

test("Welcome preserves URL-prefilled setup fields when routing to Spawn", async () => {
  window.history.pushState({}, "", "/?dir=%2Fhome%2Fme%2Fapp&prompt=fix%20it#setup");
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  });
  connectionStore.getState().connect(client);
  const welcome = render(<Welcome params={{}} paneId="welcome" focused={true} />);
  await waitFor(() => expect(window.location.pathname).toBe("/new"));
  welcome.unmount();
  renderSpawn(client);
  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("fix it"),
  );
  expectWorkingDir("/home/me/app");
  expect(window.location.hash).toBe("#setup");
});

test("prefills the prompt and working dir from ?dir=/?prompt=", async () => {
  window.history.pushState({}, "", "/new?dir=%2Fhome%2Fme%2Fapp&prompt=fix%20it");
  renderSpawn(readyClient());

  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("fix it"),
  );
  expectWorkingDir("/home/me/app");
});

// kata 11ee: the spawn pane is a dockview singleton (index.tsx), so a second
// /new?dir=<encoded> navigation while it's already open (and mounted) never
// remounts it - the singleton refocus just updates workspace.ts's
// focusedPaneId, it doesn't tear down and recreate Spawn's own React tree.
// The mount-only prefill effect (readUrlPrefill in a []-deps useEffect) then
// never reruns, so the second dir prefill is silently dropped. Reproduced
// here at the level Spawn.tsx itself can observe it: window.location.search
// changes and the SAME instance receives the routing.ts navigate() signal
// (pushState + a synthetic "popstate", exactly as AppShell's own listener
// and project.tsx's useQueryCwd precedent both key off) with no unmount in
// between.
test("kata 11ee: a second ?dir= navigation while already mounted still prefills the working dir", async () => {
  window.history.pushState({}, "", "/new?dir=%2Fhome%2Fme%2Fapp");
  renderSpawn(readyClient());
  await waitFor(() => expectWorkingDir("/home/me/app"));

  act(() => {
    window.history.pushState({}, "", "/new?dir=%2Fhome%2Fother");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await waitFor(() => expectWorkingDir("/home/other"));
});

// Same defect class as the ?dir= case above - readUrlPrefill's ?prompt= entry
// goes through the identical mount-only effect, so a repeat "Spawn with
// prompt" palette command (shell/palette/commands.ts's own /new?prompt= nav)
// while the pane is already open must refill the prompt too.
test("kata 11ee: a second ?prompt= navigation while already mounted still prefills the prompt", async () => {
  window.history.pushState({}, "", "/new?prompt=first");
  renderSpawn(readyClient());
  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("first"),
  );

  act(() => {
    window.history.pushState({}, "", "/new?prompt=second");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("second"),
  );
});

// Guards against a naive fix that unconditionally re-applies BOTH fields on
// every popstate: a navigation that carries neither param (e.g. some other
// in-app nav, then back to a plain /new) must never clobber values already
// typed into the form - readUrlPrefill's own "absent param -> no entry"
// contract (urlPrefill.test.ts) has to keep holding on every later
// navigation, not just the first mount.
test("kata 11ee: a navigation with no ?dir=/?prompt= at all leaves already-typed values untouched", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=%2Fhome%2Fme%2Fapp");
  renderSpawn(readyClient());
  await waitFor(() => expectWorkingDir("/home/me/app"));
  await fillPrompt(user, "typed by hand");

  act(() => {
    window.history.pushState({}, "", "/new");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  expectWorkingDir("/home/me/app");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("typed by hand");
});

test("only confirming a directory updates the launch defaults", async () => {
  const user = setupUser();
  renderSpawn(readyClient((f) => f.on("evener/paths/complete", () => ({ data: ["/tmp/project/src"] }))));
  await settled();
  await user.click(workingDir());
  await user.click(await screen.findByRole("button", { name: "Open /tmp/project/src" }));
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBeNull();
  const confirm = screen.getByRole("button", { name: "Use this folder" });
  await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
  await user.click(confirm);
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBe("/tmp/project/src");
});

test("Escape discards directory browsing while preserving the prompt and launch directory", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=%2Ftmp%2Fproject");
  const fake = readyClient((f) => f.on("evener/paths/complete", () => ({ data: ["/tmp/project/src"] })));
  renderSpawn(fake);
  await settled();
  await fillPrompt(user, "my important draft text");
  await user.click(workingDir());
  await user.click(await screen.findByRole("button", { name: "Open /tmp/project/src" }));
  const pathname = window.location.pathname;
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "Choose directory" })).toBeNull();
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(window.location.pathname).toBe(pathname);
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe(
    "my important draft text",
  );
  expectWorkingDir("/tmp/project");
});

// The read side of that same global (spec 3.4): with no ?dir= prefill and no
// per-project blob the field is empty, and the panel opens on the last
// directory a session was launched in rather than on $HOME.
test("the browse panel opens on the stamped last-working-directory global", async () => {
  const user = setupUser();
  localStorage.setItem(LAST_WORKING_DIR_KEY, "/home/me/lastone");
  const complete = vi.fn((_params: { prefix: string }) => ({ data: ["/home/me/lastone/src"] }));
  renderSpawn(readyClient((f) => f.on("evener/paths/complete", complete)));
  await settled();
  // Nothing else may have seeded the field: the fallback is only consulted
  // when the value is empty, and an empty field shows its placeholder.
  expectWorkingDir("Working directory");

  await user.click(workingDir());
  await screen.findByRole("textbox", { name: "Path" });

  await waitFor(() => expect(complete.mock.calls.map(([params]) => params.prefix)).toContain("/home/me/lastone/"));
});

// Both list RPCs behind the working-directory field return a Go slice, which
// marshals as JSON null rather than [] when it is empty - a hub with no
// remembered projects, or a directory with no children, answers `null`. Caught
// against a real hub: the panel crashed on mount reading .length of null.
test("survives a null data payload from either list RPC", async () => {
  const user = setupUser();
  const nulled = { data: null as unknown as string[] };
  renderSpawn(
    readyClient((f) => {
      f.on("evener/projects/recent", () => nulled);
      f.on("evener/paths/complete", () => nulled);
    }),
  );
  await settled();

  await user.click(workingDir());

  // The panel is up, listing nothing, rather than having thrown its tree away.
  expect(await screen.findByRole("textbox", { name: "Path" })).toBeTruthy();
  expect(await screen.findByText("No subfolders to display.")).toBeTruthy();
});

test("offers to create a missing directory, then creates it and spawns", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/path/validate", () => ({
      path: "/tmp/new",
      valid: false,
      error: "stat /tmp/new: no such file or directory",
    }));
    f.on("evener/dirs/create", () => ({ path: "/tmp/new", created: true }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/new");
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "go");
  await user.click(screen.getByTestId("spawn-submit"));

  await user.click(await screen.findByRole("button", { name: "Create & start" }));

  await waitFor(() =>
    expect(fake.calls).toContainEqual({ method: "evener/dirs/create", params: { path: "/tmp/new" } }),
  );
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
  // doSpawn's busy reset is shared by both callers - handleCreateConfirm's
  // success path re-enables the button the same way handleSpawn's does.
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
});

test("aborts with the validator message for a non-fixable working dir, then a corrected retry actually spawns", async () => {
  const user = setupUser();
  let dirIsValid = false;
  const fake = readyClient((f) => {
    f.on("evener/path/validate", () =>
      dirIsValid
        ? { path: "/tmp/project", valid: true }
        : { path: "/etc/hosts", valid: false, error: "path is not a directory" },
    );
  });
  window.history.pushState({}, "", "/new?dir=/etc/hosts");
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "go");
  await user.click(screen.getByTestId("spawn-submit"));

  expect(await screen.findByText("path is not a directory")).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  // Failure paths already reset busy (verified, unchanged by this fix) - the
  // button must stay usable so the user can correct the path and retry.
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);

  // kata 61v2 corollary: the VISUAL state resetting is not proof the guard of
  // record (busyRef) released too - only an actual second spawn proves that.
  dirIsValid = true;
  await setWorkingDir(user, "/tmp/project");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "thread/start")).toHaveLength(1));
});

// kata xkp2: filed as "the Spawn button is enabled but a click with the
// working directory left at its placeholder does nothing - no session, no
// toast, no dialog, no request reaches the daemon". Investigated with a
// evener/path/validate response that mirrors the real daemon EXACTLY for an
// empty path: fspaths.ValidateLaunchPath rejects an empty (or all-
// whitespace) path unconditionally, before it even looks at `kind`
// (cmd/evener-hub/internal/fspaths/app_paths.go:150-154), with the literal
// string "path is required" - one of preflightDir's own NON_FIXABLE_REASONS.
// Given that real response, the click above aborts exactly like the
// non-fixable case one test up: visibly, via a toast, with zero thread/start
// calls. No silent path was found (see the kata comment for the full
// writeup) - this is coverage for a state nothing exercised before: every
// other spawn test either sets a working directory first, or leans on
// readyClient()'s validate stub, which (unlike the real daemon) answers
// "valid" for any path including "".
test("kata xkp2: Spawn with the working directory left at its placeholder aborts visibly, not silently", async () => {
  // This test's pathname assertion below names the suite's original default
  // route literally, so the test keeps that starting state explicitly; a
  // validation abort never reaches navigation on any route.
  window.history.pushState({}, "", "/");
  const user = setupUser();
  const fake = readyClient((f) => {
    // path: "" is the real wire shape too - ValidateLaunchPath's early
    // return leaves the Go struct's Path field at its zero value.
    f.on("evener/path/validate", () => ({ path: "", valid: false, error: "path is required" }));
  });
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "say hello");
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  // The state the kata's DOM readout recorded: enabled, no aria-disabled,
  // the working-directory control still showing its placeholder.
  expect(button.disabled).toBe(false);
  expect(button.getAttribute("aria-disabled")).toBeNull();
  expectWorkingDir("Working directory");

  await user.click(button);

  expect(await screen.findByText("path is required")).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(window.location.pathname).toBe("/");
  // Released, not stuck disabled/"Spawning…" (kata 61v2's own failure class).
  expect(button.disabled).toBe(false);
});

// --- kata xgk8: Model's "(default)" must not claim an answer the daemon --
// --- will refuse -----------------------------------------------------------
//
// The daemon's thread/start resolves Model from the SAME layered launch
// config evener/launch/resolve previews (app_threadlifecycle.go: overrides.Model
// wins when set, otherwise the resolved Effective.Model - empty is refused
// with "model is required"). Leaving Model untouched sends no model
// override, so an empty resolve preview means the daemon WILL refuse the
// submit - "(default)" next to a working Effort default is a lie in that
// state. The preview is fail-open (an unmocked/failing resolve never blocks
// Spawn) - only a CONFIRMED empty default does.

test("Model keeps reading '(default)' and Spawn stays untouched when the hub resolves a real default (kata xgk8, happy path)", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  expect(modelTrigger().textContent).toContain("(default)");
  expect(screen.queryByRole("alert")).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);

  await fillPrompt(user, "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
});

test("kata xgk8: Model reads as required (not '(default)') and Spawn is disabled when the hub has no default model", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  await waitFor(() => expect(modelTrigger().textContent).not.toContain("(default)"));
  expect(screen.getByRole("alert").textContent).toMatch(/no default model/i);
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  // Defense in depth: the ⌘+Enter submit chord must not bypass the disabled
  // button either (handleSpawn's own guard, not just the button's attribute).
  await fillPrompt(user, "do the thing");
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
});

test("kata xgk8: choosing a model clears the required state and lets Start proceed", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true));

  await user.click(modelTrigger());
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  expect(screen.queryByRole("alert")).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);

  await fillPrompt(user, "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ model: "openai/gpt-5" });
});

// The daemon's own launch-config schema exposes a SECOND "model" wireField
// inside Advanced options (perLaunchEvenerOptions - schema.go's real "model"
// LaunchOption, kind modelPicker) alongside the top-level Model chip; floor
// §1.11 has the Advanced field's override win at submit time. The preview
// here must agree - an override set ONLY through Advanced options satisfies
// the requirement without the top-level chip ever leaving "(default)".
test("kata xgk8: an Advanced-options model override satisfies the requirement without touching the top-level Model field", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "model",
          wireField: "model",
          label: "Model",
          group: "general",
          kind: "modelPicker",
          perLaunch: true,
        },
      ],
    }));
    f.on("evener/launch/resolve", (params) => ({
      effective: { model: params.launchOverrides?.model ?? "" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true));

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const modelPickers = screen.getAllByRole("button", { name: /change model/i });
  // The Advanced-panel picker is the last one added: the card's trigger and
  // the top-level chip both render above it.
  await user.click(modelPickers[modelPickers.length - 1]!);
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  expect(modelTrigger().textContent).toContain("(default)"); // top-level chip untouched
});

// roborev: the Advanced-options model override used to win at submit even
// after the user touched the visible top-level Model control, so the chip
// displayed one model while thread/start launched another. Touching the
// top-level control is the user's newest intent and must clear the stale
// Advanced override (and its displayed value).
test("changing the top-level Model clears a standing Advanced-options model override (roborev)", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        { field: "model", wireField: "model", label: "Model", group: "general", kind: "modelPicker", perLaunch: true },
      ],
    }));
    f.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // A standing Advanced-options override: openai/gpt-5.
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const advancedPickers = screen.getAllByRole("button", { name: /change model/i });
  await user.click(advancedPickers[advancedPickers.length - 1]!);
  const advancedCombo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(advancedCombo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  // Then the user changes the visible top-level Model control.
  await pickModel(user, "claude-sonnet-4-5", "anthropic/claude-sonnet-4-5");
  expect(modelValue().textContent).toBe("anthropic/claude-sonnet-4-5");
  // The Advanced panel must not keep displaying the discarded value.
  const advancedPickersAfter = screen.getAllByRole("button", { name: /change model/i });
  expect(advancedPickersAfter[advancedPickersAfter.length - 1]!.textContent).not.toContain("openai/gpt-5");

  await fillPrompt(user, "model precedence");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  // The launched request carries what the top-level control displays, not the
  // stale override.
  expect(params.model).toBe("anthropic/claude-sonnet-4-5");
  expect(params.launchOverrides?.model).toBeUndefined();
});

// The same mismatch for reasoning effort: the visible top-level Effort select
// was inert while an Advanced-options reasoning_effort override stood.
test("changing the top-level Effort clears a standing Advanced-options effort override (roborev)", async () => {
  const user = setupUser();
  const fake = readyClient((f) =>
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "reasoning_effort",
          wireField: "reasoningEffort",
          label: "Reasoning effort",
          group: "model",
          kind: "select",
          perLaunch: true,
          choices: [
            { value: "low", label: "low" },
            { value: "high", label: "high" },
          ],
        },
      ],
    })),
  );
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");

  // A standing Advanced-options override: high.
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Reasoning effort"), "high");

  // Then the user changes the visible top-level Effort control.
  fireEvent.change(effortControl(), { target: { value: "low" } });
  expect((effortControl() as HTMLSelectElement).value).toBe("low");
  // The Advanced field is cleared too, so it cannot show the discarded value.
  expect((screen.getByLabelText("Reasoning effort") as HTMLSelectElement).value).toBe("");

  await fillPrompt(user, "effort precedence");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  expect(params.reasoningEffort).toBe("low");
  expect(params.launchOverrides?.reasoningEffort).toBeUndefined();
});

// The reverse order stays intact: an Advanced override set AFTER the
// top-level control is the user's newest intent and still wins at submit.
test("an Advanced-options model override set after the top-level Model still wins (roborev)", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        { field: "model", wireField: "model", label: "Model", group: "general", kind: "modelPicker", perLaunch: true },
      ],
    }));
    f.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  await pickModel(user, "claude-sonnet-4-5", "anthropic/claude-sonnet-4-5");

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const advancedPickers = screen.getAllByRole("button", { name: /change model/i });
  await user.click(advancedPickers[advancedPickers.length - 1]!);
  const advancedCombo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(advancedCombo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  await fillPrompt(user, "override wins");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  expect(params.model).toBe("openai/gpt-5");
});

test("an Advanced-options effort override set after the top-level Effort still wins (roborev)", async () => {
  const user = setupUser();
  const fake = readyClient((f) =>
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "reasoning_effort",
          wireField: "reasoningEffort",
          label: "Reasoning effort",
          group: "model",
          kind: "select",
          perLaunch: true,
          choices: [
            { value: "low", label: "low" },
            { value: "high", label: "high" },
          ],
        },
      ],
    })),
  );
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");

  fireEvent.change(effortControl(), { target: { value: "low" } });
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Reasoning effort"), "high");

  await fillPrompt(user, "effort override wins");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  expect(params.reasoningEffort).toBe("high");
});

// --- resolved-default labels -------------------------------------------------
//
// A launch-config control whose unset state reads "(default)" names the value
// a session started now would inherit instead: the field's entry in the
// effective layer of evener/launch/resolve for the current working directory.
// Until that resolve lands - or if it fails - the label stays plain
// "(default)": an unresolved answer must never be dressed up as a known one.

function effortOptionLabels(): (string | null)[] {
  const select = screen.getByLabelText("Prompt reasoning effort") as HTMLSelectElement;
  return Array.from(select.options).map((o) => o.textContent);
}

test("Effort, Model, and the mobile rows name the resolved default once launch/resolve lands", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5", reasoningEffort: "high" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  // No working directory yet, so no resolve has run: plain "(default)".
  expect(effortOptionLabels()[0]).toBe("(default)");
  expect(modelValue().textContent).toBe("(default)");

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // Effort's empty option names the inherited effort.
  await waitFor(() => expect(effortOptionLabels()[0]).toBe("high (default)"));
  // The card's Model trigger names the inherited model.
  expect(modelTrigger().textContent).toContain("anthropic/claude-sonnet-4-5 (default)");
  expect(modelValue().textContent).toBe("anthropic/claude-sonnet-4-5 (default)");
});

// The visible effort readout is the selected option's own label - including
// the resolved default's ("high (default)"), not the bare "(default)" the
// empty value renders before the resolve lands.
test("the card's effort readout names the resolved default once launch/resolve lands", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5", reasoningEffort: "high" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  expect(effortReadout().textContent).toBe("(default)");

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  await waitFor(() => expect(effortReadout().textContent).toBe("high (default)"));
});

// Access mode is the chip-level face of the launch-config sandbox field
// (floor §1.8), so it follows the same resolved-default rule: its empty
// option names the inherited sandbox in the chip's own friendly wording.
test("Access mode names the resolved sandbox default once launch/resolve lands", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { sandbox: "workspace-write" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  // The desktop Access mode select lives inside the Advanced panel.
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const accessOptionLabels = () => {
    const select = screen.getByLabelText("Access mode") as HTMLSelectElement;
    return Array.from(select.options).map((o) => o.textContent);
  };

  // No working directory yet, so no resolve has run: plain "(default)".
  expect(accessOptionLabels()[0]).toBe("(default)");

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // The desktop Access mode select's empty option names the inherited sandbox.
  await waitFor(() => expect(accessOptionLabels()[0]).toBe("Workspace write (default)"));
  // The mobile Access mode row derives its resting label from the same
  // options list, so it inherits the resolved wording too.
  const mobileConfig = screen.getByTestId("spawn-mobile-config");
  const accessRow = mobileConfig.querySelector('[data-label="Access mode"]');
  expect(accessRow?.textContent).toContain("Workspace write (default)");
});

test("the (default) labels stay plain when the resolve fails", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => {
      throw new Error("resolve down");
    });
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
  await act(async () => {}); // let the rejection's state writes land

  expect(effortOptionLabels()[0]).toBe("(default)");
  expect(modelTrigger().textContent).not.toContain("claude");
  expect(modelValue().textContent).toBe("(default)");
});

// The Advanced panel's own unset labels resolve the same way, off the same
// resolve the pane already runs: a boolean field reads "On (default)"/"Off
// (default)" per the effective value.
test("an Advanced-options boolean names the resolved default (On/Off)", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "no_project_prompts",
          wireField: "noProjectPrompts",
          label: "No project prompts",
          group: "general",
          kind: "boolean",
          perLaunch: true,
        },
      ],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5", noProjectPrompts: true },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const select = screen.getByLabelText("No project prompts") as HTMLSelectElement;
  expect(Array.from(select.options).map((o) => o.textContent)).toEqual(["On (default)", "On", "Off"]);
});

// --- uncredentialed-default fallback ---------------------------------------
//
// A resolved default whose provider has no credentials is a guaranteed
// thread/start failure (spawn.go's "provider credentials missing for
// <provider>..."). model/list only enumerates providers it could actually
// construct (launchcheck.go), so a default provider missing from that SET is
// the honest signal this form has for "not credentialed" - see the effect's
// own comment in Spawn.tsx. "(default)" is never an explicit row (only the
// closed trigger's text for value === ""), so hiding it IS preselecting a
// real model instead of leaving Model blank.

test("preselects the first launchable model when the resolved default's provider isn't in the launchable set", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [
        { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
        { provider: "anthropic", model: "claude-opus-4", displayName: "anthropic/claude-opus-4" },
      ],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" }, // launch.toml's default; openai has no credentials here
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // Falls back to the FIRST launchable model, not merely "some" model -
  // model/list's own order, which scopedCatalog.ts preserves into the picker.
  await waitFor(() => expect(modelTrigger().textContent).toContain("anthropic/claude-sonnet-4-5"));
  expect(modelTrigger().textContent).not.toContain("(default)");
  expect(screen.queryByRole("alert")).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);

  await fillPrompt(user, "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ model: "anthropic/claude-sonnet-4-5" });
});

// An Advanced-options model override wins over the top-level chip at submit
// (floor §1.11, spawnSchema's resolveScalars), so while one is set the chip is
// not what launches: rewriting it in the fallback's name would display a
// model that does not launch. Here the override's provider is launchable
// while the user configures it and drops out of model/list afterwards - the
// state a removed credential leaves behind - and the untouched chip must not
// be rewritten.
test("an advanced model override stops the uncredentialed-default fallback from rewriting the chip", async () => {
  const user = setupUser();
  let openaiLaunchable = true;
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        { field: "model", wireField: "model", label: "Model", group: "general", kind: "modelPicker", perLaunch: true },
      ],
    }));
    f.on("model/list", () => ({
      data: [
        { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
        ...(openaiLaunchable ? [{ provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" }] : []),
      ],
    }));
    f.on("evener/launch/resolve", (params) => ({
      // The hub applies the override first (resolveScalars), so the effective
      // model IS the override whose provider stops being launchable below.
      effective: {
        model: params.launchOverrides?.model ?? "anthropic/claude-opus-4",
        reasoningEffort: openaiLaunchable ? "high" : "low",
      },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  // A credentialed default lands first: the untouched chip reads its resolved
  // default and no fallback substitution fires.
  await waitFor(() => expect(modelValue().textContent).toBe("anthropic/claude-opus-4 (default)"));

  // The override is configured while openai is still in model/list.
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const modelPickers = screen.getAllByRole("button", { name: /change model/i });
  await user.click(modelPickers[modelPickers.length - 1]!);
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  // Wait for that pass to land (the override, not the chip, is the resolved
  // effective model now) before the provider disappears, so the drop below is
  // the only pending catalog pass.
  await waitFor(() => expect(modelValue().textContent).toBe("openai/gpt-5 (default)"));

  // openai drops out of model/list while the override stands, and the catalog
  // scope refreshes so the fallback effect re-reads the listing and the
  // resolve.
  openaiLaunchable = false;
  await act(async () => credentialsStore.getState().fetch());

  // "low (default)" marks the post-drop resolve landing (it replaced the
  // "high (default)" of the pass above), so the fallback has run its course
  // by the time the chip is judged.
  await waitFor(() => expect(effortReadout().textContent).toBe("low (default)"));
  // The chip still names the model that will launch - the override - and the
  // Advanced field still shows it.
  expect(modelValue().textContent).toBe("openai/gpt-5 (default)");
  expect(modelTrigger().textContent).not.toContain("anthropic/claude-sonnet-4-5");
  const advancedPickersAfter = screen.getAllByRole("button", { name: /change model/i });
  expect(advancedPickersAfter[advancedPickersAfter.length - 1]!.textContent).toContain("openai/gpt-5");
});

test("entering onboarding for one draft scope does not suppress the fallback for the next", async () => {
  const user = setupUser();
  // The first scope resolves a credentialed default, so no fallback runs and
  // Model stays empty. The working-directory switch starts a new catalog scope
  // whose default is uncredentialed - exactly when the fallback must apply.
  let defaultModel = "anthropic/claude-opus-4";
  const fake = readyClient((f) => {
    // Nothing stored, so the pane offers the Connect provider entry point - the
    // path that marks this draft as having entered onboarding.
    f.on("evener/instance/list", () => ({
      instances: [],
      availableProviders: [
        {
          id: "openai",
          name: "OpenAI",
          protocol: "openai-chat",
          auth: "bearer",
          implicit: true,
          authModes: ["apiKey"],
          setup: {
            name: "openai",
            providerId: "openai",
            protocol: "openai-chat",
            auth: "bearer",
            implicit: true,
            isDefault: false,
            activeSource: "none",
            hasStoredOAuth: false,
            credentialRequired: true,
            authModes: ["apiKey"],
            baseUrl: "https://provider.example/v1",
            endpointFingerprint: "fp-provider",
          },
        },
      ],
    }));
    f.on("model/list", () => ({
      data: [
        { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
        { provider: "anthropic", model: "claude-opus-4", displayName: "anthropic/claude-opus-4" },
      ],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: defaultModel },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await user.click(await screen.findByRole("button", { name: "Connect provider" }));
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  await user.keyboard("{Escape}");

  defaultModel = "openai/gpt-5.5"; // openai has no credentials in this fixture
  await setWorkingDir(user, "/tmp/later-draft");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  await waitFor(() => expect(modelTrigger().textContent).toContain("anthropic/claude-sonnet-4-5"));
});

test("onboarding a second draft scope does not forget the first scope's explicit choice", async () => {
  const user = setupUser();
  // Both scopes start with a credentialed default so nothing auto-fills while
  // onboarding is opened; the first scope's default turns uncredentialed only
  // after both have been onboarded, which is when the fallback would replace
  // its (still explicit) choice.
  const uncredentialedDirs = new Set<string>();
  const fake = readyClient((f) => {
    f.on("evener/instance/list", () => ({
      instances: [],
      availableProviders: [
        {
          id: "openai",
          name: "OpenAI",
          protocol: "openai-chat",
          auth: "bearer",
          implicit: true,
          authModes: ["apiKey"],
          setup: {
            name: "openai",
            providerId: "openai",
            protocol: "openai-chat",
            auth: "bearer",
            implicit: true,
            isDefault: false,
            activeSource: "none",
            hasStoredOAuth: false,
            credentialRequired: true,
            authModes: ["apiKey"],
            baseUrl: "https://provider.example/v1",
            endpointFingerprint: "fp-provider",
          },
        },
      ],
    }));
    f.on("model/list", () => ({
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
    }));
    f.on("evener/launch/resolve", ({ cwd }) => ({
      effective: { model: uncredentialedDirs.has(cwd) ? "openai/gpt-5.5" : "anthropic/claude-opus-4" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  for (const dir of ["/tmp/first-draft", "/tmp/second-draft"]) {
    await setWorkingDir(user, dir);
    await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
    await user.click(screen.getByRole("button", { name: "Connect provider" }));
    await act(async () => {
      await vi.dynamicImportSettled();
    });
    await user.keyboard("{Escape}");
  }

  uncredentialedDirs.add("/tmp/first-draft");
  await setWorkingDir(user, "/tmp/first-draft");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
  // The first draft's onboarding choice still stands, so the substitute
  // fallback must not fill its model in: the pane asks for an explicit choice.
  expect(modelTrigger().textContent).toContain("Choose a model");
  expect(modelTrigger().textContent).not.toContain("anthropic/claude-sonnet-4-5");
});

test("an unmanaged harness with a resolved default model does not demand a model choice", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/unmanaged-draft");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/unmanaged-draft", JSON.stringify({ harness: "external" }));
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
  // The external harness carries its own model through unmanaged, so the
  // resolved default keeps the chip off the required state and Start live.
  expect(modelTrigger().textContent).not.toContain("Choose a model");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
});

test("an unmanaged harness whose hub resolves no default model still starts", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/unmanaged-no-default");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/unmanaged-no-default", JSON.stringify({ harness: "external" }));
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      // No Evener default model, but a resolved effort so the readout settling
      // on "high (default)" proves the resolve response has been applied.
      effective: { model: "", reasoningEffort: "high" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await waitFor(() => expect(effortReadout().textContent).toBe("high (default)"));

  // The unmanaged harness carries its own model, so the hub's missing Evener
  // default must not become a requirement of this pane: no "Choose a model",
  // no requirement note, Start live.
  expect(modelTrigger().textContent).not.toContain("Choose a model");
  expect(modelValue().textContent).toBe("(default)");
  expect(screen.queryByRole("alert")).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
});

test("an unmanaged harness is never auto-filled by the uncredentialed-default fallback", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/unmanaged-fallback");
  localStorage.setItem("evener-hub.spawn-defaults./tmp/unmanaged-fallback", JSON.stringify({ harness: "external" }));
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
    }));
    f.on("evener/launch/resolve", () => ({
      // openai is absent from model/list, so the fallback would otherwise
      // replace the untouched Model with the first launchable Evener model.
      effective: { model: "openai/gpt-5.5" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // The fallback must not run for an unmanaged harness: Model stays untouched
  // and the chip keeps naming the resolved default instead of the first
  // launchable Evener model.
  await waitFor(() => expect(modelValue().textContent).toBe("openai/gpt-5.5 (default)"));
  expect(modelTrigger().textContent).not.toContain("anthropic/claude-sonnet-4-5");

  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  if (!start) throw new Error("start was not issued");
  expect((start.params as ThreadStartParams).model).not.toBe("anthropic/claude-sonnet-4-5");
});

test("an Advanced-options model override after onboarding satisfies the requirement", async () => {
  const user = setupUser();
  let saved = false;
  const setup = {
    name: "openai",
    providerId: "openai",
    protocol: "openai-chat",
    auth: "bearer",
    implicit: true,
    isDefault: false,
    activeSource: "none",
    hasStoredOAuth: false,
    credentialRequired: true,
    authModes: ["apiKey"],
    baseUrl: "https://provider.example/v1",
    endpointFingerprint: "fp-provider",
  };
  const client = readyClient((fake) => {
    fake.on("evener/launch/schema", () => ({
      options: [
        { field: "model", wireField: "model", label: "Model", group: "general", kind: "modelPicker", perLaunch: true },
      ],
    }));
    fake.on("evener/instance/list", () => {
      const row = { ...setup, activeSource: saved ? "store" : "none", hasStoredFile: saved };
      return {
        instances: saved ? [row] : [],
        availableProviders: [
          {
            id: "openai",
            name: "OpenAI",
            protocol: row.protocol,
            auth: row.auth,
            implicit: true,
            authModes: ["apiKey"],
            setup: row,
          },
        ],
      };
    });
    fake.on("model/list", () => ({
      data: saved ? [{ provider: "openai", model: "from-server", displayName: "Server choice" }] : [],
    }));
    fake.on("evener/auth/apiKey/set", ({ provider }) => {
      saved = true;
      return {
        provider,
        supported: true,
        signedIn: true,
        activeSource: "store",
        hasStoredOAuth: false,
      };
    });
    fake.on("evener/auth/test", ({ provider }) => ({ provider, status: "success", message: "" }));
    // The resolved default is non-empty and uncredentialed, and it does NOT
    // follow the Advanced override: the requirement must be satisfied by the
    // override itself rather than by waiting for a re-resolve.
    fake.on("evener/launch/resolve", () => ({
      effective: { model: "missing/old-default" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await setWorkingDir(user, "/tmp/guided-override");
  await user.click(screen.getByRole("button", { name: "Connect provider" }));
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  await user.click(await screen.findByRole("button", { name: "OpenAI" }));
  await user.type(screen.getByLabelText("API key"), "fixture-only-key");
  await user.click(screen.getByRole("button", { name: "Save and check" }));
  await user.click(await screen.findByRole("button", { name: "Continue" }));
  await screen.findByRole("option", { name: /Server choice/ });
  await user.keyboard("{Escape}");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const modelPickers = screen.getAllByRole("button", { name: /change model/i });
  await user.click(modelPickers[modelPickers.length - 1]!);
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "from");
  await user.click((await screen.findAllByRole("option", { name: /Server choice/ }))[0]!);

  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
  expect(client.calls.filter((call) => call.method === "thread/start")).toEqual([]);
});

test("keeps the form usable and leaves Model at '(default)' when no provider is credentialed at all", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("model/list", () => ({ data: [] })); // nothing launchable to fall back to
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // No new dead-end UI: the server's own "provider credentials missing"
  // message on submit is what speaks here, not a form the picker can't
  // resolve on its own - the field stays exactly as it always has.
  expect(modelTrigger().textContent).toContain("(default)");
  expect(screen.queryByRole("alert")).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
});

test("a sticky per-project model default is never clobbered by the uncredentialed-default fallback", async () => {
  // The sticky pref names a provider that IS launchable, but not the list's
  // first entry - if the fallback logic ignored modelRef and ran anyway, it
  // would silently overwrite this with models[0] ("anthropic/claude-opus-4").
  localStorage.setItem("evener-hub.spawn-defaults.global.working_dir", "/p");
  localStorage.setItem("evener-hub.spawn-defaults./p", JSON.stringify({ model: "anthropic/claude-sonnet-4-5" }));
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [
        { provider: "anthropic", model: "claude-opus-4", displayName: "anthropic/claude-opus-4" },
        { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
      ],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" }, // openai uncredentialed
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  expect(modelTrigger().textContent).toContain("anthropic/claude-sonnet-4-5");
  expect(modelTrigger().textContent).not.toContain("claude-opus-4");
});

test("auth notification retires global cleanup before the instance refresh completes", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/auth-generation");
  const saved = JSON.stringify({ model: "openai/newly-visible" });
  localStorage.setItem("evener-hub.spawn-defaults./tmp/auth-generation", saved);
  localStorage.setItem("evener-hub.spawn-defaults.global.model", "openai/newly-visible");
  const oldCatalog = deferred<ModelListResponse>();
  const instanceRefresh = deferred<InstanceListResponse>();
  const refreshStarted = deferred<void>();
  let notified = false;
  const client = readyClient((fake) => {
    fake.on("model/list", ({ harness, cwd }) =>
      harness === "evener" && cwd === undefined && !notified
        ? oldCatalog.promise
        : { data: [{ provider: "openai", model: "newly-visible" }] },
    );
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await settled();
  await act(async () => credentialsStore.getState().fetch());
  const refreshedInstances = { instances: credentialsStore.getState().instances, availableProviders: [] };
  client.on("evener/instance/list", () => {
    refreshStarted.resolve();
    return instanceRefresh.promise;
  });
  expect(modelValue().textContent).toBe("openai/newly-visible");
  // The obsolete global catalog must already be in flight (parked on
  // oldCatalog) before the notification, or resolving it later is a no-op.
  await waitFor(() =>
    expect(modelListRequests(client).some((params) => params.harness === "evener" && params.cwd === undefined)).toBe(
      true,
    ),
  );
  notified = true;
  await act(async () => client.emitNotification({ method: "evener/auth/updated", params: {} }));
  // The instance refetch is coalesced behind a timer; fire it, then await the
  // request it issues.
  await act(async () => {
    await vi.runOnlyPendingTimersAsync();
    await refreshStarted.promise;
  });
  try {
    await act(async () => oldCatalog.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
    // Assert before releasing instance/list: its completion cannot repair
    // anything an obsolete global catalog has already removed.
    expect({
      model: modelValue().textContent,
      saved: localStorage.getItem("evener-hub.spawn-defaults./tmp/auth-generation"),
      global: localStorage.getItem("evener-hub.spawn-defaults.global.model"),
    }).toEqual({ model: "openai/newly-visible", saved, global: "openai/newly-visible" });
    expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
  } finally {
    await act(async () => instanceRefresh.resolve(refreshedInstances));
  }
  expect(modelValue().textContent).toBe("openai/newly-visible");
});

test("a model response from before a credential refresh cannot discard the saved selection", async () => {
  const saved = JSON.stringify({ model: "openai/gpt-5" });
  localStorage.setItem("evener-hub.spawn-defaults.global.working_dir", "/p");
  localStorage.setItem("evener-hub.spawn-defaults./p", saved);
  let refreshed = false;
  const pending: Array<(response: ModelListResponse) => void> = [];
  const client = readyClient((fake) => {
    fake.on("model/list", () =>
      refreshed
        ? { data: [{ provider: "openai", model: "gpt-5" }] }
        : new Promise<ModelListResponse>((resolve) => pending.push(resolve)),
    );
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  await waitFor(() => expect(modelTrigger().textContent).toContain("openai/gpt-5"));
  expect(pending.length).toBeGreaterThan(0);
  refreshed = true;
  await act(async () => credentialsStore.getState().fetch());
  await act(async () => {
    for (const resolve of pending) resolve({ data: [{ provider: "openai", model: "gpt-4o" }] });
  });
  expect(modelTrigger().textContent).toContain("openai/gpt-5");
  expect(localStorage.getItem("evener-hub.spawn-defaults./p")).toBe(saved);
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
});

test("surfaces the discard notice when a prefilled model is no longer offered (floor §1.10)", async () => {
  localStorage.setItem("evener-hub.spawn-defaults.global.working_dir", "/p");
  localStorage.setItem("evener-hub.spawn-defaults./p", JSON.stringify({ model: "openai/gpt-4o" }));
  renderSpawn(readyClient());

  expect(await screen.findByText(/discarded last-used model openai\/gpt-4o/i)).toBeTruthy();
  // The stale blob was pruned by the sweep.
  await waitFor(() => expect(localStorage.getItem("evener-hub.spawn-defaults./p")).toBeNull());
});

// --- Effort: the ladder belongs to the selected model -----------------------
//
// The Effort select used to render one hardcoded ladder (minimal/low/medium/
// high + none) for EVERY model. model/list now serves each model's own
// reasoningEffortLevels, so the select derives its options from the selected
// model's descriptor - or, with Model left at "(default)", from the hub's
// resolved default model - falling back to the classic ladder only when the
// hub can't enumerate levels.

function scriptModelList(models: ModelDescriptor[]): void {
  modelListOverride = models;
}

function effortSelect(): HTMLSelectElement {
  return screen.getByLabelText("Prompt reasoning effort") as HTMLSelectElement;
}

function effortOptionValues(): string[] {
  return within(effortSelect())
    .getAllByRole("option")
    .map((option) => (option as HTMLOptionElement).value);
}

async function pickModel(user: ReturnType<typeof userEvent.setup>, query: string, qualified: string): Promise<void> {
  await user.click(modelTrigger());
  const combo = await screen.findByRole("combobox", { name: "Model" });
  // The panel's input survives between opens with its last query, so a second
  // pick must clear before entering or the new query appends to the old one.
  await user.clear(combo);
  await user.paste(query);
  await user.click(await screen.findByText(qualified));
}

test("the Effort select offers the selected model's own ladder and re-derives it on a model switch", async () => {
  const user = setupUser();
  scriptModelList([
    {
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      displayName: "anthropic/claude-sonnet-4-5",
      supportsReasoning: true,
      reasoningEffortLevels: ["low", "medium", "high"],
    },
    {
      provider: "openai",
      model: "gpt-5",
      displayName: "openai/gpt-5",
      supportsReasoning: true,
      reasoningEffortLevels: ["minimal", "low", "medium", "high", "xhigh", "max"],
    },
  ]);
  renderSpawn(readyClient());
  await settled();

  await pickModel(user, "gpt-5", "openai/gpt-5");
  await waitFor(() =>
    expect(effortOptionValues()).toEqual(["", "minimal", "low", "medium", "high", "xhigh", "max", "none"]),
  );

  // A chosen level the next model's ladder doesn't name can't stay selected -
  // the select must never display a value it doesn't offer.
  await user.selectOptions(effortSelect(), "xhigh");
  await pickModel(user, "sonnet", "anthropic/claude-sonnet-4-5");
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "low", "medium", "high", "none"]));
  expect(effortSelect().value).toBe("");
});

test("a model the catalog says cannot reason disables the Effort select and clears a chosen effort", async () => {
  const user = setupUser();
  scriptModelList([
    {
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      displayName: "anthropic/claude-sonnet-4-5",
      supportsReasoning: true,
      reasoningEffortLevels: ["low", "medium", "high"],
    },
    {
      provider: "openai",
      model: "gpt-5",
      displayName: "openai/gpt-5",
      supportsReasoning: false,
      reasoningEffortLevels: [],
    },
  ]);
  renderSpawn(readyClient());
  await settled();

  await pickModel(user, "sonnet", "anthropic/claude-sonnet-4-5");
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "low", "medium", "high", "none"]));
  await user.selectOptions(effortSelect(), "high");

  await pickModel(user, "gpt-5", "openai/gpt-5");
  await waitFor(() => expect(effortSelect().disabled).toBe(true));
  expect(effortSelect().value).toBe("");
});

// A disabled effort control must LOOK disabled: the visible wrapper carries
// the state (not just the transparent select inside it), so it drops its
// hover face and pointer cursor like every other disabled control.
test("a disabled effort control renders its disabled state on the visible wrapper", async () => {
  const user = setupUser();
  scriptModelList([
    {
      provider: "openai",
      model: "gpt-5",
      displayName: "openai/gpt-5",
      supportsReasoning: false,
      reasoningEffortLevels: [],
    },
  ]);
  renderSpawn(readyClient());
  await settled();

  await pickModel(user, "gpt-5", "openai/gpt-5");
  await waitFor(() => expect(effortSelect().disabled).toBe(true));

  const trigger = screen.getByTestId("spawn-effort");
  expect(trigger.getAttribute("data-disabled")).toBe("true");
});

// The transparent overlay select is the topmost hittable layer, so it must
// inherit the wrapper's cursor - its own `pointer` would otherwise win over
// the wrapper's `not-allowed` on a disabled control. CSS gate: jsdom
// evaluates no cascade, so the computed cursor is asserted on the source.
test("the effort overlay select inherits the wrapper cursor", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const spawnCss = readFileSync(join(here, "spawn.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

  expect(spawnCss).toMatch(/\.effortSelect\s*\{[^}]*cursor:\s*inherit/);
  expect(spawnCss).toContain(".effortTrigger:not([data-disabled");
});

test("with Model left at '(default)', the Effort select follows the hub's resolved default model", async () => {
  const user = setupUser();
  scriptModelList([
    {
      provider: "anthropic",
      model: "claude-sonnet-4-5",
      displayName: "anthropic/claude-sonnet-4-5",
      supportsReasoning: true,
      reasoningEffortLevels: ["low", "medium", "high"],
    },
    {
      provider: "openai",
      model: "gpt-5",
      displayName: "openai/gpt-5",
      supportsReasoning: true,
      reasoningEffortLevels: ["low", "high"],
    },
  ]);
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));

  // gpt-5's provider IS in readyClient's model/list, so the uncredentialed
  // fallback doesn't preselect it - Model stays "(default)" and the ladder
  // still has to be gpt-5's own.
  expect(modelTrigger().textContent).toContain("(default)");
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "low", "high", "none"]));
});

test("the classic ladder remains when the hub can't enumerate the model's own levels", async () => {
  const user = setupUser();
  // The default model/list fixture has no reasoning metadata, so the catalog
  // degrades to label-only entries - the select must keep working.
  renderSpawn(readyClient());
  await settled();

  await pickModel(user, "gpt-5", "openai/gpt-5");
  await waitFor(() => expect(effortOptionValues()).toEqual(["", "minimal", "low", "medium", "high", "none"]));
});

// The pane-level Effort preview and the picker share one harness/cwd-scoped
// model/list promise. A rich response therefore reaches both consumers
// without the old REST enrichment request or a two-source merge race.
test("the Effort select and picker share one scoped model/list response", async () => {
  const user = setupUser();
  let resolve: ((response: ModelListResponse) => void) | undefined;
  const fake = readyClient((f) => {
    f.on("model/list", ({ harness }) =>
      harness === "evener"
        ? { data: [] }
        : new Promise<ModelListResponse>((done) => {
            resolve = done;
          }),
    );
  });
  renderSpawn(fake);
  await settled();

  await waitFor(() => expect(resolve).toBeDefined());
  if (!resolve) throw new Error("model/list test response resolver was not installed");
  const resolveModelList = resolve;
  await user.click(modelTrigger());
  expect(screen.getByRole("combobox", { name: "Model" })).toBeTruthy();
  expect(modelListRequests(fake).filter((params) => params.harness === undefined)).toHaveLength(1);

  await act(async () => {
    resolveModelList({
      data: [
        {
          provider: "openai",
          model: "gpt-5",
          displayName: "openai/gpt-5",
          supportsReasoning: true,
          reasoningEffortLevels: ["minimal", "low", "medium", "high", "xhigh", "max"],
        },
      ],
    });
  });

  expect(
    modelListRequests(fake).filter((params) => params.harness === "evener" && params.cwd === undefined),
  ).toHaveLength(1);
  await user.click(await screen.findByText("openai/gpt-5"));
  await waitFor(() =>
    expect(effortOptionValues()).toEqual(["", "minimal", "low", "medium", "high", "xhigh", "max", "none"]),
  );
  expect(modelListRequests(fake).filter((params) => params.harness === undefined)).toHaveLength(1);
});

// A credential change can make models discoverable (a stored Vertex credential
// JSON enables the publisher-model listing), so the pane's own scoped
// model/list cache must not outlive evener/auth/updated: the catalog reloads
// and the picker sees the new listing without a remount.
test("evener/auth/updated drops the pane's model/list cache so the catalog and picker reload", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(modelListRequests(fake).filter((params) => params.harness === undefined)).toHaveLength(1));
  expect(
    modelListRequests(fake).filter((params) => params.harness === "evener" && params.cwd === undefined),
  ).toHaveLength(1);

  modelListOverride = [
    { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
    { provider: "google-vertex", model: "gemini-3.8-flash", displayName: "google-vertex/gemini-3.8-flash" },
  ];
  act(() => fake.emitNotification({ method: "evener/auth/updated", params: { provider: "google-vertex" } }));
  await waitFor(() => expect(modelListRequests(fake).filter((params) => params.harness === undefined)).toHaveLength(2));
  expect(
    modelListRequests(fake).filter((params) => params.harness === "evener" && params.cwd === undefined),
  ).toHaveLength(2);

  await user.click(modelTrigger());
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.clear(combo);
  await user.type(combo, "gemini");
  await screen.findByText("google-vertex/gemini-3.8-flash");
  // The picker shares the reloaded promise rather than issuing a third call.
  expect(modelListRequests(fake).filter((params) => params.harness === undefined)).toHaveLength(2);
  expect(
    modelListRequests(fake).filter((params) => params.harness === "evener" && params.cwd === undefined),
  ).toHaveLength(2);
});

// --- post-success reset (floor §1.14 L186, wave6-report.md gap) -----------
//
// The spawn pane is a dockview singleton (paneRegistry.ts: "focus existing
// instead of second copy"), so unlike a one-shot legacy page load it can
// still be sitting there, fully mounted, after a successful spawn navigates
// the workspace to the new session. Legacy clears the pending-attachment bag
// and resets the paste marker-counter BEFORE navigating away specifically so
// a returning user can't resend a stale image (spawn.js:1331-1336); Spawn.tsx
// had no equivalent, so both the prompt text and any attachment chip just
// sat there, re-sendable, once the pane was revisited.

function pastePngInto(el: HTMLElement, name = "shot.png"): void {
  const file = new File([new Uint8Array([1, 2, 3])], name, { type: "image/png" });
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    value: { items: [{ kind: "file", type: "image/png", getAsFile: () => file }] },
  });
  el.dispatchEvent(event);
}

// Mirrors Composer.test.tsx's own installCanvasStubs - the same
// useAttachments/reencodeToPng pipeline underlies both panes' image staging.
function installCanvasStubs(): void {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  HTMLCanvasElement.prototype.toBlob = (callback: BlobCallback): void => {
    callback(new Blob([new Uint8Array([9, 9, 9])], { type: "image/png" }));
  };
  class FakeImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = 4;
    height = 4;
    private _src = "";
    set src(value: string) {
      this._src = value;
      Promise.resolve().then(() => this.onload?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = FakeImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
}

test("failed creation retains images and advanced/plugin settings through remount", async () => {
  installCanvasStubs();
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "maxRounds",
          wireField: "maxRounds",
          label: "Max rounds",
          group: "general",
          kind: "integer",
          perLaunch: true,
        },
      ],
    }));
    f.on("evener/plugin/preview", () => SPAWN_PLUGIN_PREVIEW);
    f.on("thread/start", () => {
      throw new WireError("draft-start-failure", -32000);
    });
  });
  const mounted = renderSpawn(fake);
  await fillPrompt(user, "retained-sentinel");
  act(() => pastePngInto(promptField(), "retained.png"));
  await screen.findByRole("button", { name: "View retained.png" });
  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.clear(screen.getByLabelText("Max rounds"));
  await user.type(screen.getByLabelText("Max rounds"), "7");
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1);
  await screen.findByText(/draft-start-failure/);
  mounted.unmount();
  renderSpawn(fake);
  expect(promptField().value).toBe("retained-sentinel[image 1]");
  expect(screen.getByRole("button", { name: "View retained.png" })).toBeTruthy();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  expect((screen.getByLabelText("Max rounds") as HTMLInputElement).value).toBe("7");
  await openDesktopPluginSelection(user);
  expect(screen.getByRole("switch", { name: "beta" }).getAttribute("aria-checked")).toBe("false");
  // In-memory retention must not serialize the image or prompt into defaults.
  const stored = Array.from({ length: localStorage.length }, (_, index) =>
    localStorage.getItem(localStorage.key(index) ?? ""),
  );
  expect(stored.join("\n")).not.toContain("retained-sentinel");
  expect(stored.join("\n")).not.toContain(btoa(String.fromCharCode(9, 9, 9)));
  fake.on("thread/start", () => startResponse("local:abc123"));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const submissions = fake.calls.filter((call) => call.method === "thread/start");
  expect(submissions).toHaveLength(2);
  expect(submissions[1]?.params).toEqual(submissions[0]?.params);
  expect(submissions[1]?.params).toMatchObject({ launchOverrides: { maxRounds: 7, enabledPlugins: ["alpha"] } });
});

test("successful submitted snapshot clears only its images and preserves newer edits after remount", async () => {
  installCanvasStubs();
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => f.on("thread/start", () => started.promise));
  const mounted = renderSpawn(fake);
  await fillPrompt(user, "submitted-sentinel");
  act(() => pastePngInto(promptField(), "submitted.png"));
  await screen.findByRole("button", { name: "View submitted.png" });
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  mounted.unmount();
  renderSpawn(fake);
  await user.type(promptField(), "-newer");
  act(() => pastePngInto(promptField(), "newer.png"));
  await screen.findByRole("button", { name: "View newer.png" });
  fireEvent.change(effortControl(), { target: { value: "high" } });
  await act(async () => started.resolve(startResponse("local:abc123")));
  expect(promptField().value).toBe("submitted-sentinel-newer[image 2]");
  expect(screen.queryByRole("button", { name: "View submitted.png" })).toBeNull();
  expect(screen.getByRole("button", { name: "View newer.png" })).toBeTruthy();
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  expect(screen.queryByTestId("attachment-tile")).toBeNull();
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  expect(screen.getByRole("button", { name: "View newer.png" })).toBeTruthy();
});

test.each([
  { outcome: "success", remount: false },
  { outcome: "failure", remount: false },
  { outcome: "success", remount: true },
  { outcome: "failure", remount: true },
])(
  "pending image encode $outcome stays with its project across navigation (remount: $remount)",
  async ({ outcome, remount }) => {
    installCanvasStubs();
    const images: { onload: (() => void) | null; onerror: (() => void) | null }[] = [];
    vi.stubGlobal(
      "Image",
      class {
        onload: (() => void) | null = null;
        onerror: (() => void) | null = null;
        width = 4;
        height = 4;
        set src(_value: string) {
          images.push(this);
        }
      },
    );
    const user = setupUser();
    window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
    const fake = readyClient();
    const mounted = renderSpawn(fake);
    await fillPrompt(user, "draft-a-sentinel");
    act(() => pastePngInto(promptField(), "a.png"));
    expect(screen.getByRole("img", { name: "a.png (still processing)" })).toBeTruthy();
    if (remount) {
      mounted.unmount();
      renderSpawn(fake);
    }
    expect(screen.getByRole("img", { name: "a.png (still processing)" })).toBeTruthy();
    await user.click(screen.getByTestId("spawn-submit"));
    expect(fake.calls.some((call) => call.method === "thread/start")).toBe(false);
    await user.type(promptField(), "-newer");
    await visitSpawnURL("/new?dir=/tmp/draft-b");
    await fillPrompt(user, "draft-b-sentinel");
    act(() => pastePngInto(promptField(), "b.png"));
    expect(images).toHaveLength(2);
    promptField().setSelectionRange(promptField().value.length, promptField().value.length);
    await act(async () => {
      if (outcome === "success") images[0]?.onload?.();
      else images[0]?.onerror?.();
    });
    expect(promptField().value).toBe("draft-b-sentinel[image 1]");
    expect(screen.getByRole("img", { name: "b.png (still processing)" })).toBeTruthy();
    if (outcome === "failure") {
      act(() => pastePngInto(promptField(), "b2.png"));
      expect(promptField().value).toBe("draft-b-sentinel[image 1][image 2]");
    }
    await visitSpawnURL("/new?dir=/tmp/draft-a");
    if (outcome === "success") {
      await screen.findByRole("button", { name: "View a.png" });
      expect(promptField().value).toBe("draft-a-sentinel[image 1]-newer");
    } else {
      expect(screen.queryByTestId("attachment-tile")).toBeNull();
      expect(promptField().value).toBe("draft-a-sentinel-newer");
    }
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
    await act(async () => images[1]?.onload?.());
    if (outcome === "failure") await act(async () => images[2]?.onload?.());
    await visitSpawnURL("/new?dir=/tmp/draft-b");
    await screen.findByRole("button", { name: "View b.png" });
  },
);

test("resets the prompt and attachments after a successful spawn, but keeps sticky defaults (floor §1.14 L186)", async () => {
  installCanvasStubs();
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
  await setWorkingDir(user, "/tmp/project");
  await fillPrompt(user, "do the thing");
  await act(async () => {
    pastePngInto(prompt);
  });
  await waitFor(() => expect(prompt.value).toBe("do the thing[image 1]"));
  await waitFor(() => expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy());

  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));

  expect(prompt.value).toBe("");
  expect(screen.queryByRole("button", { name: /remove/i })).toBeNull();
  // Sticky default (floor §1.9) survives a successful spawn - only the
  // transient prompt/attachment state resets.
  expectWorkingDir("/tmp/project");
});

test("a failed spawn leaves the prompt and attachment staged (failure paths keep everything)", async () => {
  installCanvasStubs();
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("thread/start", () => {
      throw new Error("boom");
    });
  });
  renderSpawn(fake);
  await settled();

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
  await fillPrompt(user, "do the thing");
  await act(async () => {
    pastePngInto(prompt);
  });
  await waitFor(() => expect(prompt.value).toBe("do the thing[image 1]"));
  await waitFor(() => expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy());

  await user.click(screen.getByTestId("spawn-submit"));

  await screen.findByText(/start failed/i);
  expect(prompt.value).toBe("do the thing[image 1]");
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
  // handleSpawn's catch already resets busy on a thrown startThread (same
  // class of bug, verified already-fixed here - the button must stay usable
  // so the user can retry without reloading).
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
});

// T3: the first-run worst moment - the hub is fine but no agent daemon could
// be reached for cwd (thread/start rejects with the hubLaunch WireError
// family, appwire.HubLaunchError's own discriminator). The raw launch-check
// text is replaced with copy a person can act on, distinct from a genuinely
// unreachable hub.
test("a spawn that fails because no agent daemon could be reached shows actionable copy, not the raw launch-check text", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("thread/start", () => {
      throw new WireError("evener launch-check timed out", -32014, { evenerErrorInfo: "hubLaunch" });
    });
  });
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await screen.findByText(
    "Start failed: No agent daemon responded for this project. Start one by running evener in the repo, then retry.",
  );
  expect(screen.queryByText(/launch-check timed out/i)).toBeNull();
});

// The other failure family (T3): the hub connection itself is down. This
// must keep the existing hub-unreachable sentence, not the daemon-missing
// copy above - the two are not interchangeable advice.
test("a spawn that fails because the hub connection is down keeps the hub-unreachable message", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("thread/start", () => {
      throw new Error('AppwireClient: cannot call "thread/start" while state is "closed"');
    });
  });
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await screen.findByText("Start failed: Can't reach the hub right now.");
  expect(screen.queryByText(/AppwireClient/i)).toBeNull();
});

test("re-enables the Spawn button after a successful start (post-success state hygiene, same class as §1.14)", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));

  // Addressed by testid so a still-stuck "Starting…" fails on the label
  // assertion below with a clear message rather than on a failed query.
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(button.textContent).toBe("Start");
  expect(button.disabled).toBe(false);
});

// --- staged attachments are tiles, not text chips (kata kbg7) --------------
//
// Spawn stages images through the composer's own useAttachments pipeline, and
// renders them the composer's way too: the shared AttachmentTile, one element
// shape from paste to send (kata 39xe). It used to diverge at render - a text
// Chip with " (processing…)" appended - so one act read as two different
// things depending on which surface started it, and Spawn was the only place
// in the app that narrated a pending attachment in words while the composer's
// own pending tile stays deliberately static (widgets/skeleton's
// honest-liveness rule). Jesse's call, 2026-07-31: unify on the tile.

// Mirrors Composer.test.tsx's installStalledDecodeStub - the decode never
// settles either way, which is what pins a test to the pending state. The
// marker text lands synchronously with the paste, so waiting on it under a
// settling decode can return either side of the transition.
function installStalledDecodeStub(): void {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  HTMLCanvasElement.prototype.toBlob = () => {}; // never invokes its callback
  class NeverLoadsImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    src = ""; // a plain field: assigning it never schedules onload/onerror
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = NeverLoadsImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
}

// Mirrors Composer.test.tsx's installGatedDecodeStub - settles on demand
// rather than on a microtask (installCanvasStubs) or never
// (installStalledDecodeStub). release() resolves only once the whole encode
// chain has actually delivered its bytes, so the pending -> settled
// transition can be awaited as a real completion instead of polled for.
function installGatedDecodeStub(): { release: () => Promise<void> } {
  HTMLCanvasElement.prototype.getContext = (() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext;
  let markDelivered!: () => void;
  const delivered = new Promise<void>((resolve) => {
    markDelivered = resolve;
  });
  HTMLCanvasElement.prototype.toBlob = (callback: BlobCallback): void => {
    const blob = new Blob([new Uint8Array([9, 9, 9])], { type: "image/png" });
    const readBytes = blob.arrayBuffer.bind(blob);
    blob.arrayBuffer = async () => {
      const buffer = await readBytes();
      markDelivered();
      return buffer;
    };
    callback(blob);
  };
  const waiting: (() => void)[] = [];
  class GatedImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = 4;
    height = 4;
    private _src = "";
    set src(value: string) {
      this._src = value;
      waiting.push(() => this.onload?.());
    }
    get src(): string {
      return this._src;
    }
  }
  // @ts-expect-error stubbing the global Image constructor for this test file only
  globalThis.Image = GatedImage;
  URL.createObjectURL = () => "blob:fake";
  URL.revokeObjectURL = () => {};
  return {
    release: () => {
      for (const fire of waiting.splice(0)) fire();
      return delivered;
    },
  };
}

test("a settled attachment renders as a thumbnail tile, not a text chip (kata kbg7)", async () => {
  installCanvasStubs();
  renderSpawn(readyClient());
  await settled();

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
  await act(async () => {
    pastePngInto(prompt, "shot.png");
  });
  await waitFor(() => expect(prompt.value).toBe("[image 1]"));

  // The whole thumbnail is the control that opens the lightbox, named for the
  // file it shows - the composer's tile exactly.
  const openButton = await screen.findByRole("button", { name: "View shot.png" });
  expect((openButton.querySelector("img") as HTMLImageElement).src).toMatch(/^data:image\/png;base64,/);
  // The filename is the tile's accessible name now, not chip text on the page.
  expect(screen.queryByText("shot.png")).toBeNull();
});

test("a pending attachment is the same tile, and says nothing about its progress (kata kbg7)", async () => {
  installStalledDecodeStub();
  renderSpawn(readyClient());
  await settled();

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
  await act(async () => {
    pastePngInto(prompt, "shot.png");
  });
  await waitFor(() => expect(prompt.value).toBe("[image 1]"));

  // An empty slot the thumbnail will fill, named so a screen reader hears
  // which attachment is holding things up, with its remove button already
  // present - the same tile the settled case draws.
  expect(screen.getByRole("img", { name: "shot.png (still processing)" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Remove shot.png" })).toBeTruthy();
  // The tile IS the pending signal. No visible words claiming a progress this
  // UI cannot actually report.
  expect(screen.queryByText(/processing/i)).toBeNull();
});

// Kata 39xe's invariant, at the Spawn seam. Pending and settled are the SAME
// element tree at the same list position, so React updates the remove button
// instead of unmounting it, and a user holding tab-focus on it when the decode
// lands keeps that focus. Spawn never had 39xe's defect - its chip was one
// element type in both states - but it renders the tile now, so the invariant
// has to hold HERE too or the unification would have imported the bug it was
// fixing. This asserts the mechanism (the identical node is still focused),
// not a side effect: an implementation that remounted an identically-labelled
// button would satisfy "a focused remove button exists" while still dropping
// the user's focus.
test("focus on a staged attachment's remove button survives its decode settling (kata 39xe)", async () => {
  const gate = installGatedDecodeStub();
  renderSpawn(readyClient());
  await settled();

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
  await act(async () => {
    pastePngInto(prompt, "shot.png");
  });
  const removeButton = screen.getByRole("button", { name: "Remove shot.png" });
  await act(async () => {
    removeButton.focus();
  });
  expect(document.activeElement).toBe(removeButton);

  await act(async () => {
    await gate.release();
  });

  // The transition really happened: the tile now offers the decoded image, so
  // the assertions below are about the settled state, not a decode that
  // quietly never landed.
  expect(screen.getByRole("button", { name: "View shot.png" })).toBeTruthy();
  expect(removeButton.isConnected).toBe(true);
  expect(document.activeElement).toBe(removeButton);
});

// kata 61v2: three fast clicks on Spawn spawned three separate live daemons
// running the same prompt. `disabled={busy}` alone is not a re-entrancy guard
// - `busy` is React state, and its read inside handleSpawn's closure only
// reflects whatever was committed as of the LAST render. Three clicks fired
// in the same tick (fireEvent is synchronous, unlike userEvent.click, which
// awaits between pointer events and lets React flush a render in between)
// all read the SAME stale `busy === false` before the first click's
// setBusy(true) ever commits, so all three pass `if (busy) return` and all
// three call thread/start. Live-verified over raw CDP: three real
// dispatchEvent("click") calls with zero delay produced three daemons and
// three sessions running the identical prompt (a real button.click() call on
// an actually-disabled DOM button is a browser-level no-op, so a genuinely
// laggy render is what turns an ordinary double-click into this).
test("kata 61v2: three clicks in the same tick still spawn only one session", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "do the thing");

  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  act(() => {
    fireEvent.click(button);
    fireEvent.click(button);
    fireEvent.click(button);
  });

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));

  expect(fake.calls.filter((c) => c.method === "thread/start")).toHaveLength(1);
});

test("kata 61v2 corollary: a successful spawn releases the guard for the next one", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "first session");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "thread/start")).toHaveLength(1));

  // The Spawn pane is a dockview singleton that can stay mounted behind the
  // session pane doSpawn navigates to (see doSpawn's own comment on the
  // sticky-defaults reset) - a second Start on the SAME mounted instance must
  // not be permanently blocked by the first success's guard release.
  await fillPrompt(user, "second session");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "thread/start")).toHaveLength(2));
});

// FIX 2a: the busy "Spawning…" state gets the Loader widget (widgets/loader)
// instead of static text - a genuinely indeterminate, user-initiated wait is
// exactly what Loader exists for.
test("shows a Loader, not static text, while the spawn request is in flight", async () => {
  const user = setupUser();
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const fake = readyClient((f) => {
    f.on("thread/start", async () => {
      await gate;
      return startResponse("local:abc123");
    });
  });
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  const button = await screen.findByTestId("spawn-submit");
  expect(within(button).getByRole("status", { name: "Starting" })).toBeTruthy();
  expect(within(button).queryByText("Starting…")).toBeNull();

  release();
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
});

// A preserved effort the fallback ladder cannot name is a lie on screen. The
// stale-effort effect deliberately skips when the catalog has no ladder for the
// model (knownEffortLevels === null): the fallback is a guess, and clobbering a
// sticky default on a guess would lose the user's setting. But the OPTIONS came
// from that guess too, so an effort like xhigh survived in state with no
// <option> to render it -- the select fell back to its first entry and showed
// "(default)" while thread/start still received xhigh. What is shown and what
// is sent must be the same value.
test("an effort the fallback ladder cannot name is still offered, not silently sent as (default)", async () => {
  const user = setupUser();
  scriptModelList([
    {
      provider: "openai",
      model: "gpt-5",
      displayName: "openai/gpt-5",
      supportsReasoning: true,
      reasoningEffortLevels: ["minimal", "low", "medium", "high", "xhigh", "max"],
    },
    // No reasoning metadata at all: catalogEffortLevels returns null here, so
    // the select falls back to the guessed minimal/low/medium/high ladder.
    { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
  ]);
  let started: ThreadStartParams | undefined;
  renderSpawn(
    readyClient((fake) => {
      fake.on("thread/start", (params) => {
        started = params;
        return startResponse("local:abc123");
      });
    }),
  );
  await settled();

  await pickModel(user, "gpt-5", "openai/gpt-5");
  await waitFor(() => expect(effortOptionValues()).toContain("xhigh"));
  await user.selectOptions(effortSelect(), "xhigh");

  await pickModel(user, "sonnet", "anthropic/claude-sonnet-4-5");
  // The fallback ladder took over (it starts at "minimal", the real one did
  // not) and still offers the preserved level, because state still holds it.
  await waitFor(() => expect(effortOptionValues()).toContain("minimal"));
  expect(effortOptionValues()).toContain("xhigh");

  const displayed = effortSelect().value;
  expect(displayed).toBe("xhigh");

  await fillPrompt(user, "go");
  await user.click(screen.getByRole("button", { name: "Start" }));
  await waitFor(() => expect(started).toBeDefined());
  expect(started?.reasoningEffort ?? "").toBe(displayed);
});

// The model catalog follows the committed directory, not the picker's draft.
test("typing a working directory reloads the model catalog only after confirmation", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(fake.calls.some((call) => call.method === "model/list")).toBe(true));

  // Global cleanup can run before the initial directory-scoped catalog load.
  await waitFor(() => expect(modelListRequests(fake).some((params) => Object.hasOwn(params, "cwd"))).toBe(true));
  const baseline = fake.calls.filter((call) => call.method === "model/list").length;
  await user.click(workingDir());
  const input = await screen.findByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.type(input, "/tmp/some/project{Enter}");
  const confirm = screen.getByRole("button", { name: "Use this folder" });
  await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
  expect(fake.calls.filter((call) => call.method === "model/list")).toHaveLength(baseline);
  await user.click(confirm);

  await waitFor(() =>
    expect(fake.calls.filter((call) => call.method === "model/list").at(-1)?.params).toMatchObject({
      cwd: "/tmp/some/project",
    }),
  );
  expect(fake.calls.filter((call) => call.method === "model/list")).toHaveLength(baseline + 1);
});

test.each(["desktop", "mobile"])("%s directory picker follows route directory changes while open", async (surface) => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=%2Fhome%2Fme%2Fapp");
  renderSpawn(readyClient());
  await waitFor(() => expectWorkingDir("/home/me/app"));
  await user.click(
    surface === "desktop"
      ? workingDir()
      : screen.getByLabelText(/^Working directory:/, { selector: "button:not(#spawn-cwd)" }),
  );
  const input = await screen.findByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.type(input, "/uncommitted");
  act(() => {
    window.history.pushState({}, "", "/new?dir=%2Fhome%2Fother");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Path" }) as HTMLInputElement).value).toBe("/home/other"),
  );
  const confirm = screen.getByRole("button", { name: "Use this folder" });
  await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
  await user.click(confirm);
  expectWorkingDir("/home/other");
});

// --- spawn prompt-box inline slash menu (Task 5) ---------------------------
//
// The prompt box completes the same trailing-slash token the session composer
// does, against the pre-session catalog (evener/spawn/slashCatalog) merged
// with the spawn-scoped builtins (goal, model, reasoning-effort). Key
// handling is ADAPTED for Spawn's submit model, not ported verbatim: plain
// Enter is the newline key and commits the open menu, while only
// Mod/Ctrl+Enter submits.

function slashMenu() {
  return screen.getByTestId("composer-slash-menu");
}

function slashOptions() {
  return within(slashMenu()).getAllByRole("option");
}

function promptField(): HTMLTextAreaElement {
  return screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
}

// The slash catalog is debounced (useSpawnSlashCatalog): wait for the
// stubbed response to land instead of sleeping a fixed window past the
// settle time, which flakes under load.
async function typeSlashQuery(user: ReturnType<typeof userEvent.setup>, fake: FakeClient, text: string): Promise<void> {
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/spawn/slashCatalog")).toBe(true));
  await user.type(promptField(), text);
}

test("typing /re opens the menu with builtin and catalog matches but not /simplify", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => ({
      commands: [{ name: "review", description: "review the diff" }],
      skills: [
        {
          name: "simplify",
          description: "rewrite",
          disableModelInvocation: false,
          userInvocable: true,
          available: true,
        },
      ],
    }));
  });
  renderSpawn(fake);
  await settled();

  await typeSlashQuery(user, fake, "/re");

  // /reasoning-effort (builtin) and /review (catalog) both match "re";
  // "simplify" has no "r", so the embedding matcher needs every query char
  // in the label and it must stay out. Asserted negatively on purpose so a
  // future matcher change stays honest.
  expect(slashOptions().map((el) => el.textContent)).toEqual([
    expect.stringContaining("/reasoning-effort"),
    expect.stringContaining("/review"),
  ]);
  expect(slashMenu().querySelectorAll('[role="option"]')).toHaveLength(2);
});

test("typing further narrows the spawn slash menu live", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => ({
      commands: [{ name: "review", description: "review the diff" }],
      skills: [
        {
          name: "simplify",
          description: "rewrite",
          disableModelInvocation: false,
          userInvocable: true,
          available: true,
        },
      ],
    }));
  });
  renderSpawn(fake);
  await settled();

  await typeSlashQuery(user, fake, "/rev");

  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/review")]);

  await user.clear(promptField());
  await user.type(promptField(), "/sim");

  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/simplify")]);
});

test("Tab and plain Enter commit the spawn menu; Mod+Enter submits instead", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => ({
      commands: [{ name: "review", description: "review the diff" }],
      skills: [],
    }));
  });
  renderSpawn(fake);
  await settled();

  await typeSlashQuery(user, fake, "/rev");
  await user.keyboard("{Tab}");

  expect((promptField() as HTMLTextAreaElement).value).toBe("/review ");
  expect(promptField().selectionStart).toBe("/review ".length);
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(document.activeElement).toBe(promptField());

  // Plain Enter is Spawn's newline key: with the menu open it commits the
  // highlighted item instead of submitting.
  await user.clear(promptField());
  await user.type(promptField(), "/rev");
  await user.keyboard("{Enter}");

  expect((promptField() as HTMLTextAreaElement).value).toBe("/review ");
  expect(fake.calls.some((call) => call.method === "thread/start")).toBe(false);

  // Mod+Enter ALWAYS submits, even with the menu open: commit nothing.
  await user.clear(promptField());
  await user.type(promptField(), "/rev");
  expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  // The menu committed nothing: a successful Start clears the prompt (Spawn's
  // own doSpawn reset), so the field must NOT hold the committed "/review ".
  expect((promptField() as HTMLTextAreaElement).value).not.toBe("/review ");
});

test("a successful submit closes the slash menu with the cleared prompt", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => ({
      commands: [{ name: "review", description: "review the diff" }],
      skills: [],
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await typeSlashQuery(user, fake, "/re");
  expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();

  // Mod+Enter submits even with the menu open; the pane stays mounted behind
  // the session pane, so the stale token must not survive the cleared prompt.
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  expect((promptField() as HTMLTextAreaElement).value).toBe("");
  await waitFor(() => expect(screen.queryByTestId("composer-slash-menu")).toBeNull());
});

test("a same-cwd catalog refresh hides stale rows until the new response lands", async () => {
  const user = setupUser();
  let resolveRefresh!: (response: { commands: { name: string; description: string }[]; skills: never[] }) => void;
  let requests = 0;
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => {
      requests += 1;
      if (requests === 1) {
        return { commands: [{ name: "review", description: "review the diff" }], skills: [] };
      }
      return new Promise((done) => {
        resolveRefresh = done as (response: {
          commands: { name: string; description: string }[];
          skills: never[];
        }) => void;
      });
    });
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await typeSlashQuery(user, fake, "/rev");
  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/review")]);

  // A plugin change starts a same-cwd refresh; the v1 rows must not stay
  // offered while the new config loads — the new session may no longer load
  // them, and a picked stale entry would submit as literal text.
  await act(async () => {
    fake.emitNotification({ method: "evener/plugin/updated", params: {} } as AnyNotification);
  });
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();

  // Wait for the refresh request to go out (debounced) before resolving it.
  await waitFor(() => {
    expect(fake.calls.filter((c) => c.method === "evener/spawn/slashCatalog")).toHaveLength(2);
  });
  await act(async () => {
    resolveRefresh({ commands: [{ name: "revamp", description: "revamp the turn" }], skills: [] });
  });
  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/revamp")]);
});

test("Shift+Tab does not commit the spawn menu", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => ({
      commands: [{ name: "review", description: "review the diff" }],
      skills: [],
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await typeSlashQuery(user, fake, "/re");
  expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();

  // Modified Tab falls through for focus navigation instead of committing:
  // the prompt keeps its text and no completion is spliced in.
  await user.keyboard("{Shift>}{Tab}{/Shift}");
  expect((promptField() as HTMLTextAreaElement).value).toBe("/re");
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
});

test("reopening the identical token restarts the highlight at the first option", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => ({
      commands: [{ name: "review", description: "review the diff" }],
      skills: [],
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await typeSlashQuery(user, fake, "/re");
  // Move off the first option, dismiss, and reopen the identical token.
  await user.keyboard("{ArrowDown}");
  await user.keyboard("{Escape}");
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  await user.type(promptField(), "e");
  await user.clear(promptField());
  await user.type(promptField(), "/re");

  // Committing now must splice the FIRST option, not the previously
  // highlighted one.
  await user.keyboard("{Tab}");
  expect((promptField() as HTMLTextAreaElement).value).toBe("/reasoning-effort ");
});

test("catalog entries colliding with pre-session builtins are not offered twice", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => ({
      commands: [
        { name: "goal", description: "project goal runner", source: "project" },
        { name: "deploy", description: "deploy the thing", source: "project" },
      ],
      skills: [
        {
          name: "model",
          description: "project model helper",
          disableModelInvocation: false,
          userInvocable: true,
          available: true,
        },
      ],
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  // "/goal" the project command and "/model" the project skill share their
  // invocations with pre-session builtins, which always win at submit — so
  // the menu offers each invocation exactly once (the builtin), while the
  // non-colliding project command still appears.
  await typeSlashQuery(user, fake, "/goal");
  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/goal")]);

  await user.clear(promptField());
  await typeSlashQuery(user, fake, "/model");
  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/model")]);

  await user.clear(promptField());
  await typeSlashQuery(user, fake, "/dep");
  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/deploy")]);
});

test("a prefilled slash token opens its menu without waiting for a keystroke", async () => {
  window.history.pushState({}, "", "/new?prompt=%2Frev");
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => ({
      commands: [{ name: "review", description: "review the diff" }],
      skills: [],
    }));
  });
  renderSpawn(fake);

  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("/rev"),
  );
  // The catalog lands debounced; the menu for the prefilled token must open
  // on its own once rows arrive — no keystroke needed.
  await waitFor(() => expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull());
  expect(slashOptions().map((el) => el.textContent)).toEqual([expect.stringContaining("/review")]);
});

test("Escape, no-match, mid-word slash, and blur all close the spawn slash menu", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => ({
      commands: [{ name: "review", description: "review the diff" }],
      skills: [],
    }));
  });
  renderSpawn(fake);
  await settled();

  await typeSlashQuery(user, fake, "/re");
  expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();

  await user.keyboard("{Escape}");
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect((promptField() as HTMLTextAreaElement).value).toBe("/re");

  await user.clear(promptField());
  await user.type(promptField(), "/zzz");
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();

  await user.clear(promptField());
  await user.type(promptField(), "foo/bar");
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();

  await user.clear(promptField());
  await user.type(promptField(), "/re");
  expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();
  fireEvent.blur(promptField());
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("a non-evener harness sends no slashCatalog call and typing /goal shows no menu", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Harness"), "external");

  // From this commit on, no slashCatalog timer can be pending: the harness
  // switch clears the mount timer via the hook's effect cleanup and the
  // disabled hook schedules nothing. Drop any mount-window calls so the
  // assertion below pins post-switch behavior only — a fixed sleep here
  // flakes under load (it relies on beating the 250ms mount timer).
  fake.calls.splice(0);

  await user.type(promptField(), "/goal");
  // Run the fake clock past the debounce so a timer the switch failed to
  // cancel would fire here and be caught.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(SPAWN_SLASH_CATALOG_DEBOUNCE_MS + 1);
  });
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(fake.calls.some((call) => call.method === "evener/spawn/slashCatalog")).toBe(false);
});

test("the open spawn menu wires listbox roles, aria-controls, and aria-activedescendant on the prompt", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => ({
      commands: [{ name: "review", description: "review the diff" }],
      skills: [],
    }));
  });
  renderSpawn(fake);
  await settled();

  await typeSlashQuery(user, fake, "/re");

  expect(slashMenu().getAttribute("role")).toBe("listbox");
  const activeId = promptField().getAttribute("aria-activedescendant");
  expect(activeId).toBeTruthy();
  expect(document.getElementById(activeId ?? "")).toBe(slashOptions()[0]);
  // aria-controls names the listbox the prompt is completing against - the
  // other half of the activedescendant wiring, set on the same native node.
  const controls = promptField().getAttribute("aria-controls");
  expect(controls).toBeTruthy();
  expect(document.getElementById(controls ?? "")).toBe(slashMenu());

  await user.keyboard("{Escape}");
  expect(promptField().getAttribute("aria-activedescendant")).toBeNull();
  expect(promptField().getAttribute("aria-controls")).toBeNull();
});

test("ArrowDown/ArrowUp move the spawn menu highlight and wrap at both ends", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => ({
      commands: [{ name: "review", description: "review the diff" }],
      skills: [],
    }));
  });
  renderSpawn(fake);
  await settled();

  await typeSlashQuery(user, fake, "/re");
  // Two matches: the /reasoning-effort built-in then the /review catalog
  // command, so index 0 is the built-in.
  expect(slashOptions()).toHaveLength(2);
  expect(slashOptions()[0]?.getAttribute("aria-selected")).toBe("true");
  expect(promptField().getAttribute("aria-activedescendant")).toBe(slashOptions()[0]?.id);

  await user.keyboard("{ArrowDown}");
  expect(slashOptions()[1]?.getAttribute("aria-selected")).toBe("true");
  expect(slashOptions()[0]?.getAttribute("aria-selected")).toBe("false");
  expect(promptField().getAttribute("aria-activedescendant")).toBe(slashOptions()[1]?.id);

  await user.keyboard("{ArrowDown}"); // wraps past the last option back to the first
  expect(slashOptions()[0]?.getAttribute("aria-selected")).toBe("true");
  expect(promptField().getAttribute("aria-activedescendant")).toBe(slashOptions()[0]?.id);

  await user.keyboard("{ArrowUp}"); // wraps the other way, back to the last
  expect(slashOptions()[1]?.getAttribute("aria-selected")).toBe("true");
  expect(promptField().getAttribute("aria-activedescendant")).toBe(slashOptions()[1]?.id);
});

test("clicking a spawn menu option commits it without ever blurring the prompt", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/spawn/slashCatalog", () => ({
      commands: [{ name: "review", description: "review the diff" }],
      skills: [],
    }));
  });
  renderSpawn(fake);
  await settled();

  await typeSlashQuery(user, fake, "/re");
  // index 0 is the built-in /reasoning-effort, 1 /review.
  await user.click(slashOptions()[1] as HTMLElement);

  expect((promptField() as HTMLTextAreaElement).value).toBe("/review ");
  // The option's onMouseDown preventDefault keeps focus in the field, so the
  // click's onSelect commits rather than racing the blur-close.
  expect(document.activeElement).toBe(promptField());
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

// --- Task 6: submit interception for pre-session builtins --------------------
//
// A prompt parsing as /goal, /model, or /reasoning-effort starts the session
// with the literal text, then applies the builtin against the new ref, then
// navigates. Everything else spawns exactly as today.

test("a /goal prompt starts a dormant session and applies goal/set on the new ref", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("goal/set", () => ({ started: true }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await user.type(promptField(), "/goal build the widget");
  // Dismiss the inline menu without altering the text: plain Enter commits
  // the highlight here, and Mod+Enter is the submit under test.
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect((start?.params as { input?: unknown[] } | undefined)?.input ?? []).toEqual([]);
  // The goal follow-up fires after navigation without blocking it, so wait
  // for the call rather than assuming it landed.
  let goal: { params?: unknown } | undefined;
  await waitFor(() => {
    goal = fake.calls.find((c) => c.method === "goal/set");
    expect(goal).toBeTruthy();
  });
  expect(goal?.params).toMatchObject({ ref: "local:abc123", objective: "build the widget" });
});

test("a /model prompt starts a dormant session with the model on thread/start and no follow-up set", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  // The pre-start known-value check reads the pane-level modelCatalog, which
  // lands on a 250ms settle after mount (CATALOG_SETTLE_MS) — settled() only
  // waits for the Advanced-options toggle, not the catalog, so submitting
  // immediately races it (resolveSpawnModelItems(null) is [] and the known
  // value fail-closes). Setting a working directory and waiting for the
  // cwd-scoped resolve to commit its state-derived trigger text guarantees
  // the catalog commit happened first: the catalog effect is declared before
  // the resolve effect and both share the one keyed model/list promise, so
  // the resolve's Promise.all commit is strictly after the catalog's.
  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
  await waitFor(() => expect(modelValue().textContent).toBe("anthropic/claude-sonnet-4-5 (default)"));

  await user.type(promptField(), "/model openai/gpt-5");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({
    input: [],
    modelProvider: "openai",
    model: "gpt-5",
  });
  // No follow-up mutation: thread/model/set refuses mid-turn with Conflict
  // once the non-empty first input reserves the turn, so the value rides the
  // start call itself.
  expect(fake.calls.some((c) => c.method === "thread/model/set")).toBe(false);
  expect(getToasts()).toEqual([]);
});

test("a /model prompt bootstraps past the required-model guard with the value on thread/start and no literal first turn", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({ effective: { model: "" }, layers: {}, provenance: {} }));
    f.on("model/list", () => ({ data: [{ provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" }] }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");

  // The hub has no default model, so Start is gated — but the prompt itself
  // supplies the missing model. The bootstrap validates against the pane
  // catalog, so the test waits for the catalog-backed trigger text the same
  // way the known-value test does.
  await waitFor(() => expect(modelValue().textContent).toBe("Choose a model"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/spawn/slashCatalog")).toBe(true));

  await user.type(promptField(), "/model openai/gpt-5");
  await user.keyboard("{Escape}");
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(button.disabled).toBe(false);
  await user.click(button);

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({
    input: [],
    modelProvider: "openai",
    model: "gpt-5",
  });
  expect(fake.calls.some((c) => c.method === "thread/model/set")).toBe(false);
});

test("a /model prompt wins over a matching Advanced Options model override on thread/start with no literal first turn", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "model",
          wireField: "model",
          kind: "text",
          label: "Model",
          group: "general",
          perLaunch: true,
          driverSupport: { evener: true },
        },
      ],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(modelValue().textContent).toBe("anthropic/claude-sonnet-4-5 (default)"));

  // An Advanced Options model override loses to the typed slash value: the
  // user typed the value as the submit itself, the most specific intent.
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.type(screen.getByLabelText("Model"), "anthropic/other-model");

  await user.type(promptField(), "/model openai/gpt-5");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({
    input: [],
    modelProvider: "openai",
    model: "gpt-5",
  });
  expect(fake.calls.some((c) => c.method === "thread/model/set")).toBe(false);
});

test("a /model value from the previous cwd does not validate after switching directories", async () => {
  const user = setupUser();
  // Held until the assertions below: the new scope's catalog must still be
  // pending when the submit validates.
  const otherCatalog = deferred<void>();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
    // Scope-dependent catalog: gpt-5 exists only under /tmp/project. The
    // /tmp/other response is delayed past the submit below so the pane
    // catalog is deterministically STALE (old scope) at submit time — the
    // window the fix closes. Without the fix, validation reads the stale
    // snapshot and the submit goes through; with it, the scope mismatch
    // fail-closes before any load state matters.
    f.on("model/list", async (params) => {
      if ((params as { cwd?: string }).cwd === "/tmp/other") {
        await otherCatalog.promise;
        return {
          data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
        };
      }
      return {
        data: [
          { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
          { provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" },
        ],
      };
    });
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(modelValue().textContent).toBe("anthropic/claude-sonnet-4-5 (default)"));

  // Switch directories: the pane catalog still holds the old scope until the
  // new scoped load lands. A model valid only for the old scope must not
  // validate against the stale snapshot.
  await setWorkingDir(user, "/tmp/other");
  // The new scope's catalog request must actually be pending when the submit
  // validates: wait for it to be issued (its debounce runs on the fake clock).
  await waitFor(() => expect(modelListRequests(fake).some((params) => params.cwd === "/tmp/other")).toBe(true));

  await user.type(promptField(), "/model openai/gpt-5");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(screen.getByText(/\/model: unknown value "openai\/gpt-5"/)).toBeTruthy());
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  otherCatalog.resolve();
});

test("a /reasoning-effort value from the previous cwd does not validate after switching directories", async () => {
  const user = setupUser();
  // Held until the assertions below: the new scope's catalog must still be
  // pending when the submit validates.
  const otherCatalog = deferred<void>();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
    // Delay the new scope's catalog past the submit below so validation
    // runs in the stale window deterministically.
    f.on("model/list", async (params) => {
      if ((params as { cwd?: string }).cwd === "/tmp/other") {
        await otherCatalog.promise;
      }
      return {
        data: [
          { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
          { provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" },
        ],
      };
    });
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(modelValue().textContent).toBe("anthropic/claude-sonnet-4-5 (default)"));

  // Set the chip to the same value: without the fix, the stale chip
  // re-authorizes the typed value through `current` even with no levels.
  await user.selectOptions(effortControl(), "high");

  // Switch directories: the merged effort ladder still holds the old scope
  // until the new scoped load lands. "high" validates against the stale
  // ladder but must fail closed.
  await setWorkingDir(user, "/tmp/other");
  // The new scope's catalog request must actually be pending when the submit
  // validates: wait for it to be issued (its debounce runs on the fake clock).
  await waitFor(() => expect(modelListRequests(fake).some((params) => params.cwd === "/tmp/other")).toBe(true));

  await user.type(promptField(), "/reasoning-effort high");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(screen.getByText(/\/reasoning-effort: unknown value "high"/)).toBeTruthy());
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  otherCatalog.resolve();
});

test("a model picked after a failed background load validates for /model", async () => {
  const user = setupUser();
  let listCalls = 0;
  const fake = readyClient((f) => {
    f.on("model/list", () => {
      listCalls += 1;
      // The pane's background load fails; the picker's on-demand load (a
      // cache-cleared retry) succeeds.
      if (listCalls === 1) throw new Error("list down");
      return { data: [{ provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" }] };
    });
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await user.click(modelTrigger());
  await user.click(await screen.findByRole("option", { name: /gpt-5/ }));

  // The pick stamps the merged entry as a current-scope snapshot, so a typed
  // /model for the just-picked model validates instead of fail-closing
  // against the never-loaded background stamp.
  await user.type(promptField(), "/model openai/gpt-5");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({
    input: [],
    modelProvider: "openai",
    model: "gpt-5",
  });
});

test("an unknown /model value toasts, starts nothing, and leaves Start usable", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await user.type(promptField(), "/model nope");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(screen.getByText(/\/model: unknown value "nope"/)).toBeTruthy());
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(fake.calls.some((c) => c.method === "thread/model/set")).toBe(false);
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(button.disabled).toBe(false);
  expect(button.textContent).toBe("Start");
});

test("a known /model value starts before the pane catalog lands instead of fail-closing", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new");
  // The pane catalog settles 250ms after mount (CATALOG_SETTLE_MS). A deferred
  // model/list holds it null for the whole test, so this reproduces that window
  // deterministically: pre-fix, resolveSpawnModelItems(null) is [] and a known
  // value fail-closes with a spurious "unknown value" toast.
  const catalog = deferred<ModelListResponse>();
  const fake = readyClient((f) => f.on("model/list", () => catalog.promise));
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await user.type(promptField(), "/model openai/gpt-5");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({
    input: [],
    modelProvider: "openai",
    model: "gpt-5",
  });
  expect(getToasts()).toEqual([]);
});

test("a shapeless /model value still refuses while the pane catalog is unloaded", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new");
  // Pending catalog = the unloaded window. The value never resolves against a
  // catalog here, so only the provider/model shape check can refuse it: an
  // expression that regressed to matching `matched.id` (undefined) would
  // forward `modelProvider: "foo", model: ""` and silently drop the request.
  const catalog = deferred<ModelListResponse>();
  const fake = readyClient((f) => f.on("model/list", () => catalog.promise));
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await user.type(promptField(), "/model foo");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(screen.getByText(/\/model: unknown value "foo"/)).toBeTruthy());
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
});

test("a /model value bootstraps past the required-model guard after a failed catalog load", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new");
  // The background model/list rejects, so the pane catalog never commits (null
  // stamp). The hub also reports no default, so Start is gated on a model -
  // and the old bootstrap, which required a catalog match, left Start disabled
  // through this window even though thread/start would accept the value.
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({ effective: { model: "" }, layers: {}, provenance: {} }));
    f.on("model/list", () => {
      throw new Error("list down");
    });
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");

  await waitFor(() => expect(modelValue().textContent).toBe("Choose a model"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "model/list")).toBe(true));

  await user.type(promptField(), "/model openai/gpt-5");
  await user.keyboard("{Escape}");
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(button.disabled).toBe(false);
  await user.click(button);

  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ modelProvider: "openai", model: "gpt-5" });
});

test("a bare /goal toasts, starts nothing, and leaves Start usable", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("goal/set", () => ({ started: true }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await user.type(promptField(), "/goal");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(screen.getByText("/goal needs a value")).toBeTruthy());
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(fake.calls.some((c) => c.method === "goal/set")).toBe(false);
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(button.disabled).toBe(false);
  expect(button.textContent).toBe("Start");
});

test("plain text spawns with no goal/set call", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("goal/set", () => ({ started: true }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "hello world");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ input: [{ type: "text", text: "hello world" }] });
  expect(fake.calls.some((c) => c.method === "goal/set")).toBe(false);
});

test("a /goal prompt on a non-evener harness spawns verbatim with no goal/set call", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("goal/set", () => ({ started: true }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Harness"), "external");

  await user.type(promptField(), "/goal x");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ input: [{ type: "text", text: "/goal x" }] });
  expect(fake.calls.some((c) => c.method === "goal/set")).toBe(false);
});

test("a bare /model with no catalog spawns with no model follow-up and no error toast", async () => {
  const user = setupUser();
  const fake = readyClient();
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await user.type(promptField(), "/model");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect((start?.params as { input?: unknown[] } | undefined)?.input ?? []).toEqual([]);
  expect(fake.calls.some((c) => c.method === "thread/model/set")).toBe(false);
  // No error toast: the fail-open path toasts nothing.
  expect(getToasts()).toEqual([]);
});

test("a /reasoning-effort prompt starts a dormant session with the effort on thread/start and no follow-up set", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  // Wait for the pane catalog to commit with a matching scope stamp (the
  // resolve-commit proxy: the catalog effect is declared before the resolve
  // effect and both share the one keyed model/list promise, so the resolve's
  // commit is strictly after the catalog's). The stubbed catalog lists the
  // default model with no ladder metadata, so validation must fall back to
  // the fallback ladder here — fail-closed is only for proven scope
  // staleness, and "high" is on the fallback ladder.
  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "evener/launch/resolve")).toBe(true));
  await waitFor(() => expect(modelValue().textContent).toBe("anthropic/claude-sonnet-4-5 (default)"));

  await user.type(promptField(), "/reasoning-effort high");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({
    input: [],
    reasoningEffort: "high",
  });
  // No follow-up mutation: the value rides the start call itself, so it
  // applies even though the new thread is not yet in threadsStore.
  expect(fake.calls.some((c) => c.method === "thread/reasoning-effort/set")).toBe(false);
  expect(getToasts()).toEqual([]);
});

test("an unknown /reasoning-effort value toasts, starts nothing, and leaves Start usable", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await user.type(promptField(), "/reasoning-effort ultra");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(screen.getByText(/\/reasoning-effort: unknown value "ultra"/)).toBeTruthy());
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(fake.calls.some((c) => c.method === "thread/reasoning-effort/set")).toBe(false);
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(button.disabled).toBe(false);
  expect(button.textContent).toBe("Start");
});

test("a bare /reasoning-effort toasts, starts nothing, applies nothing, and leaves Start usable", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await user.type(promptField(), "/reasoning-effort");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(screen.getByText("/reasoning-effort needs a value")).toBeTruthy());
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(fake.calls.some((c) => c.method === "thread/reasoning-effort/set")).toBe(false);
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(button.disabled).toBe(false);
  expect(button.textContent).toBe("Start");
});

// RoboRev PR1131 finding 3: model/list can serialize an empty Go slice as
// `data: null` (appwire's ModelListResponse.Data carries no omitempty); the
// sweep and validation callbacks passed r.data straight into helpers that
// iterate it, so a null payload rejected the callback and skipped its work.
test("a null model/list payload does not reject the defaults sweep", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/null-catalog-sweep");
  localStorage.setItem(
    "evener-hub.spawn-defaults./tmp/null-catalog-sweep",
    JSON.stringify({ model: "openai/gpt-5", access_mode: "plan" }),
  );
  const fake = readyClient((f) => {
    f.on("model/list", () => ({ data: null }) as unknown as ModelListResponse);
  });
  renderSpawn(fake);
  await settled();
  // The sweep ran to completion instead of throwing: an unenumerated provider's
  // model is left alone (verdict "unknown"), so the blob is untouched.
  const raw = localStorage.getItem("evener-hub.spawn-defaults./tmp/null-catalog-sweep");
  expect(raw).not.toBeNull();
  expect(JSON.parse(raw as string)).toEqual({ model: "openai/gpt-5", access_mode: "plan" });
});

// RoboRev PR1131 finding 8: `branch` stayed component-local while SpawnForm
// persists across draft switches, so project B showed project A's branch until
// B's evener/git/head completed - and indefinitely whenever it failed.
test("the branch readout never shows the previous project's branch after a draft switch", async () => {
  const headA = deferred<{ head: string }>();
  const headB = deferred<{ head: string }>();
  const fake = readyClient((f) => {
    f.on("evener/git/head", ({ cwd }) => (cwd === "/tmp/branch-project-a" ? headA.promise : headB.promise));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/branch-project-a");
  renderSpawn(fake);
  await settled();
  await act(async () => headA.resolve({ head: "feature-a" }));
  await waitFor(() => expect(screen.getByTestId("spawn-branch").textContent).toBe("feature-a"));

  await visitSpawnURL("/new?dir=/tmp/branch-project-b");
  // B's HEAD is still in flight: A's branch must not linger under B.
  expect(screen.queryByTestId("spawn-branch")).toBeNull();

  await act(async () => headB.resolve({ head: "feature-b" }));
  await waitFor(() => expect(screen.getByTestId("spawn-branch").textContent).toBe("feature-b"));
});

// RoboRev PR1131 finding 5: draft ownership was keyed on the readValues callback
// identity, which Spawn recreates via useCallback(..., [draft]) on every draft
// change. Returning to a draft after visiting another (A -> B -> A) yields a NEW
// callback identity for the SAME draft, so an in-flight path validation's error
// was dropped even though its field value still lives in A's own draft (the
// validation still wrote its `invalid` flag, silently hiding the message).
test("a draft re-entered after another keeps its late path-validation error", async () => {
  const user = setupUser();
  const validation = deferred<{ path: string; valid: boolean; error: string }>();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "agent",
          wireField: "agent",
          label: "Agent",
          kind: "text",
          group: "general",
          pathKind: "command",
          perLaunch: true,
        },
      ],
    }));
    f.on("evener/path/validate", ({ path }) => (path === "review-agent" ? validation.promise : { path, valid: true }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/reentry-validation-a");
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-agent" } });

  // Visit another draft and come back: A's draft is the SAME object, but Spawn's
  // readValues useCallback produces a new identity for it.
  await visitSpawnURL("/new?dir=/tmp/reentry-validation-b");
  await visitSpawnURL("/new?dir=/tmp/reentry-validation-a");
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("review-agent");

  await act(async () => validation.resolve({ path: "review-agent", valid: false, error: "reentry-a-invalid" }));
  expect(await screen.findByText("reentry-a-invalid")).toBeTruthy();
});

// RoboRev PR1131 finding: the validation MESSAGE lived only in component-local
// `errors` while the `invalid` flag is draft-persisted in advancedValues. The
// sibling regression above only covers a validation that settles AFTER its
// draft is active again; a result that lands while the draft is INACTIVE still
// writes the flag (so collectAdvancedOverrides keeps dropping the field) but has
// no error to show when the draft is revisited. The field must not be silently
// excluded with no message.
test("a path-validation error that settles while its draft is inactive appears when the draft is revisited", async () => {
  const user = setupUser();
  const validation = deferred<{ path: string; valid: boolean; error: string }>();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "agent",
          wireField: "agent",
          label: "Agent",
          kind: "text",
          group: "general",
          pathKind: "command",
          perLaunch: true,
        },
      ],
    }));
    f.on("evener/path/validate", ({ path }) => (path === "review-agent" ? validation.promise : { path, valid: true }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/inactive-validation-a");
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-agent" } });
  await waitFor(() => expect(fake.calls.some((call) => call.method === "evener/path/validate")).toBe(true));

  // Leave A before the validator settles, so its rejection lands while B is active.
  await visitSpawnURL("/new?dir=/tmp/inactive-validation-b");
  await act(async () => validation.resolve({ path: "review-agent", valid: false, error: "inactive-a-invalid" }));

  // Returning to A finds the value and its draft-persisted invalid flag intact:
  // the field is still excluded from A's launch overrides...
  await visitSpawnURL("/new?dir=/tmp/inactive-validation-a");
  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("review-agent");
  expect(completionDraft("/tmp/inactive-validation-a").fields.getState().advancedOverrides).toEqual({});
  // ...so the message explaining the exclusion must be available again.
  expect(await screen.findByText("inactive-a-invalid")).toBeTruthy();

  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  const startParams = fake.calls.find((call) => call.method === "thread/start")?.params as
    | ThreadStartParams
    | undefined;
  expect(startParams?.cwd).toBe("/tmp/inactive-validation-a");
  // startThread omits an empty layer entirely, so accept either encoding: the
  // point is that the invalid field never reaches the server.
  expect(startParams?.launchOverrides ?? {}).toEqual({});
});

// The same loss through the other door the finding names: a pane remount
// recreates the component, so local `errors` starts empty even though the
// draft still holds the invalid field.
test("a remounted draft still shows the path-validation error stored with its invalid field", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({
      options: [
        {
          field: "agent",
          wireField: "agent",
          label: "Agent",
          kind: "text",
          group: "general",
          pathKind: "command",
          perLaunch: true,
        },
      ],
    }));
    f.on("evener/path/validate", ({ path }) =>
      path === "review-agent" ? { path, valid: false, error: "remount-a-invalid" } : { path, valid: true },
    );
  });
  window.history.pushState({}, "", "/new?dir=/tmp/remount-validation-a");
  const mounted = renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-agent" } });
  expect(await screen.findByText("remount-a-invalid")).toBeTruthy();

  mounted.unmount();
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  expect((screen.getByLabelText("Agent") as HTMLInputElement).value).toBe("review-agent");
  expect(completionDraft("/tmp/remount-validation-a").fields.getState().advancedOverrides).toEqual({});
  expect(await screen.findByText("remount-a-invalid")).toBeTruthy();
});

// --- host picker (Component 06b) -------------------------------------------

test("host picker lists sources, preselects local, and disables offline hosts", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ]);
  renderSpawn(readyClient());
  await settled();

  const picker = screen.getByLabelText("Host") as HTMLSelectElement;
  expect(picker.value).toBe("local");
  const options = within(picker).getAllByRole("option") as HTMLOptionElement[];
  expect(options.map((option) => option.value)).toEqual(["local", "buildbox", "offline-host"]);
  expect((options.find((option) => option.value === "buildbox") as HTMLOptionElement).disabled).toBe(false);
  const offline = options.find((option) => option.value === "offline-host") as HTMLOptionElement;
  expect(offline.disabled).toBe(true);
  // The reason is in the option's own accessible text, not only a tooltip.
  expect(offline.textContent).toContain("offline");
});

// The Host row sits above the working directory: the folder list, recents,
// and validation all come from the selected machine (hostRequest), so the
// form reads pick-the-machine first, then the folder on it. Asserted in DOM
// order, not visually, so keyboard focus follows the same path.
test("the host row renders above the working directory", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  renderSpawn(readyClient());
  await settled();

  const host = screen.getByLabelText("Host");
  const dir = workingDir();
  expect(host.compareDocumentPosition(dir) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
});

// Selecting an already-online remote host is NOT an attach request: the
// manifest reports the host online (its channel is attached), so the picker must
// not spend a redundant evener/host/attach on it. Only a host the manifest
// reports offline needs the dial, and that row's option is disabled, so the
// Connect affordance below is the path that reaches it. Before the fix every
// picker selection dialed, online or not.
test("selecting an already-online remote host issues no attach call", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });

  // The selection still moves the draft - and so the launch target - while
  // issuing no attach call at all.
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  expect(attachCalls(fake)).toEqual([]);
});

// Two rapid activations of one Connect must dial once. The in-flight check
// cannot read `connectingHosts` state: setConnectingHosts is asynchronous, so
// two activations in the same batch both read the pre-update set and dial
// twice. The guard is a ref, mutated synchronously before the request, so the
// second activation sees the first one still in flight.
test("a rapid double Connect dials the host once", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ]);
  // A never-resolving first dial keeps it in flight while the second activation
  // is dispatched.
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const fake = readyClient((f) => {
    f.on("evener/host/attach", async () => {
      await gate;
      return { attached: true };
    });
  });
  renderSpawn(fake);
  await settled();

  const connect = screen.getByRole("button", { name: "Connect offline-host" }) as HTMLButtonElement;
  act(() => {
    connect.click();
    connect.click();
  });

  expect(attachCalls(fake)).toHaveLength(1);
  release();
  await waitFor(() => expect(screen.queryByRole("button", { name: /Connecting offline-host/ })).toBeNull());
});

// Changing the host select never dials: an online row is already attached (a
// dial would only be redundant), and an offline row's option is disabled, so a
// select event cannot name it. The Connect button below is the single attach
// path. Before the fix the onChange branch dialed any selected offline row, a
// latent double-attach if the disabled rendering were ever relaxed.
test("changing the host select issues no attach call", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ]);
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  // An online selection moves the draft without an attach.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  expect(attachCalls(fake)).toEqual([]);

  // Even a programmatic change to an offline row — unreachable through the
  // disabled option in the real UI — still issues no attach: the Connect
  // button owns that path.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "offline-host" } });
  await settled();
  expect(attachCalls(fake)).toEqual([]);

  // The Connect button still attaches the offline host.
  fireEvent.click(screen.getByRole("button", { name: "Connect offline-host" }));
  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) => call.method === "evener/host/attach" && (call.params as { host: string }).host === "offline-host",
      ),
    ).toBe(true),
  );
});

// A never-attached host is listed offline with a disabled spawn option
// (component 06b), so the picker must offer a concrete, enabled Connect
// affordance for it — otherwise a configured [[hosts]] entry is dead UI. The
// Connect action issues the same evener/host/attach call.
test("an offline host offers an enabled Connect action that attaches it", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ]);
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  const picker = screen.getByLabelText("Host") as HTMLSelectElement;
  const offline = within(picker).getByRole("option", { name: /offline-host/ }) as HTMLOptionElement;
  expect(offline.disabled).toBe(true);

  const connect = screen.getByRole("button", { name: "Connect offline-host" }) as HTMLButtonElement;
  expect(connect.disabled).toBe(false);
  fireEvent.click(connect);

  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) => call.method === "evener/host/attach" && (call.params as { host: string }).host === "offline-host",
      ),
    ).toBe(true),
  );
});

// A Connect failure is surfaced, never a silent no-op: the row stays offline
// and the user sees the manager's error.
test("a failed Connect surfaces the attach error", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ]);
  const fake = readyClient((f) => {
    f.on("evener/host/attach", () => {
      throw new Error("host is unreachable");
    });
  });
  renderSpawn(fake);
  await settled();

  fireEvent.click(screen.getByRole("button", { name: "Connect offline-host" }));
  expect(await screen.findByText(/Connect offline-host failed/i)).toBeTruthy();
});

// The Connect action must survive a slow server-side attach: the Ensure seam
// behind `evener/host/attach` spends sequential bounded phases (deploy,
// restart, launch-contract refresh — 10 minutes each) that the AppWire
// client's 30s default request timeout does not cover, so a deploy that would
// succeed arrives after the client already rejected and the user sees a failed
// toast for a host that is online. The Connect call therefore carries an
// explicit attach timeout derived from the manager bound, and a slow attach
// resolving inside it succeeds with no failure toast. Before the fix the call
// passed no timeout option, so the default applied.
test("a slow Connect attach resolves inside the attach timeout with no failure toast", async () => {
  expect(CONNECT_ATTACH_TIMEOUT_MS).toBeGreaterThan(30_000);
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ]);
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  const fake = readyClient((f) => {
    f.on("evener/host/attach", async () => {
      // Gated past the assertion below, so on the old code — where the call
      // carried no timeout option — the waitFor already failed before the
      // server answers; on the fixed code the call carries the attach timeout
      // and the late success clears the pending marker with no failure toast.
      await gate;
      return { attached: true };
    });
  });
  renderSpawn(fake);
  await settled();

  fireEvent.click(screen.getByRole("button", { name: "Connect offline-host" }));
  // The attach RPC carries the explicit attach timeout, not the 30s default.
  await waitFor(() =>
    expect(
      fake.calls.some(
        (call) => call.method === "evener/host/attach" && call.opts?.timeoutMs === CONNECT_ATTACH_TIMEOUT_MS,
      ),
    ).toBe(true),
  );
  release();
  await waitFor(() => expect(screen.queryByRole("button", { name: /Connecting offline-host/ })).toBeNull());
  expect(screen.queryByText(/Connect offline-host failed/i)).toBeNull();
});

test("no host picker renders when the manifest has only local", async () => {
  seedSources([{ id: "local", label: "Local", kind: "local", online: true }]);
  renderSpawn(readyClient());
  await settled();

  expect(screen.queryByLabelText("Host")).toBeNull();
});

test("choosing a non-local host sends source and persists it in the draft", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/host-target");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await fillPrompt(user, "run on buildbox");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(completionDraft("/tmp/host-target").fields.getState().source).toBe("buildbox");
});

test("a local host choice omits source from the thread/start request", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/local-target");
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "stay local");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params).not.toHaveProperty("source");
});
// A draft can name a remote host while the manifest is not (yet) readable: a
// reconnect reseeds the navigation store and the manifest is back in flight,
// and the draft - which outlives the pane - still holds the chosen host. The
// empty `sources` in that window is not evidence the host is gone, so the
// submission must carry the draft's own source; reading the empty list as a
// fallback would start the session locally with no indication (Component 06b).
test("a draft naming a remote host submits it while the manifest is loading", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/loading-manifest");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");

  // The manifest drops back to in-flight (a reconnect reseeds the store): the
  // draft keeps the host, only the manifest's sources are unavailable.
  await act(async () => {
    navigationStore.setState({
      manifest: {
        key: { kind: "manifest" },
        data: null,
        loadedRevision: null,
        targetRevision: null,
        forceToken: 0,
        etag: null,
        loading: true,
        stale: false,
        error: null,
        generationID: "generation_test",
      },
    });
  });

  await fillPrompt(user, "keep the host");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
});

// The hub keeps offline sources in the manifest (only the online flag flips),
// so a host chosen while online stays present in the draft but must no longer
// be submittable once it goes offline - otherwise thread/start is rejected
// with "spawn source is not available".
test("a host that goes offline while a draft names it falls back to local", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/offline-target");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");

  await act(async () => {
    seedSources([
      { id: "local", label: "Local", kind: "local", online: true },
      { id: "buildbox", label: "buildbox", kind: "ssh", online: false },
    ]);
  });

  // The option is still listed (disabled) but the effective choice is local.
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");

  await fillPrompt(user, "no longer reachable");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params).not.toHaveProperty("source");
});

// The offline fallback is a real choice, not a render-time convenience: if the
// host returns while the form is still mounted it must not silently re-select
// itself. Selecting "Local" to affirmatively settle on local fires no change
// event when the select already reads local, so a draft left holding the stale
// host id would be the only thing that flips both the picker and the submitted
// source back to the remote host.
test("a host that returns online after its offline fallback stays on local", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/reonline-target");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");

  await act(async () => {
    seedSources([
      { id: "local", label: "Local", kind: "local", online: true },
      { id: "buildbox", label: "buildbox", kind: "ssh", online: false },
    ]);
  });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");
  // The fallback is recorded in the draft, so it is not merely a render value.
  expect(completionDraft("/tmp/reonline-target").fields.getState().source).toBe("local");

  await act(async () => {
    seedSources([
      { id: "local", label: "Local", kind: "local", online: true },
      { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
    ]);
  });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");

  await fillPrompt(user, "still local");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params).not.toHaveProperty("source");
});

// The other fallback: a draft naming a host that has left the manifest
// entirely (removed while the draft lived). The picker shows local and the
// wire omits source rather than launching the unknown host.
test("a draft naming a host removed from the manifest shows local and omits source", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/removed-target");
  renderSpawn(fake);
  await settled();

  const draft = completionDraft("/tmp/removed-target");
  await act(async () => {
    setDraftField(draft, "source", "decommissioned");
  });

  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");

  await fillPrompt(user, "host is gone");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params).not.toHaveProperty("source");
});

// --- remote target: discovery ownership (Component 06b, round four) --------

// The working-directory preflight (evener/path/validate + evener/dirs/create) is
// the CONTROLLER's own filesystem check. A remote launch's cwd belongs to the
// selected source instead, so the local answer has no authority: a path that
// exists only on the remote reads as missing here, and confirming "Create &
// start" would create the directory on THIS host before the remote launch still
// failed on its own cwd. The remote hub validates its own cwd via thread/start.
test("a remote host submission skips the controller-local directory preflight", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  // A cwd the local check calls a creatable missing directory: left ungated, the
  // preflight opens the Create & start dialog and never reaches thread/start.
  const fake = readyClient((f) =>
    f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" })),
  );
  window.history.pushState({}, "", "/new?dir=/remote/only/path");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  // No local "doesn't exist yet" offer, and nothing created on the controller.
  expect(screen.queryByRole("button", { name: "Create & start" })).toBeNull();
  expect(fake.calls.some((call) => call.method === "evener/dirs/create")).toBe(false);
});

// The gate is the TARGET, not the mere presence of a picker: a local submission
// keeps the controller-local preflight and its Create & start offer.
test("a local submission keeps the controller-local directory preflight", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) =>
    f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" })),
  );
  window.history.pushState({}, "", "/new?dir=/local/only/path");
  renderSpawn(fake);
  await settled();

  await user.click(screen.getByTestId("spawn-submit"));
  expect(await screen.findByRole("button", { name: "Create & start" })).toBeTruthy();
  expect(fake.calls.some((call) => call.method === "thread/start")).toBe(false);
});

// Provider readiness is read from THIS controller (evener/instance/list). A
// remote launch runs on the selected source's own hub, which resolves its own
// credentials, so a controller-local "missing provider" must not disable Start
// for a remote target - the remote hub's own thread/start error is the
// authority until source-aware discovery lands.
test("a remote host submission is not blocked by missing controller-local providers", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => f.on("evener/instance/list", () => ({ instances: [], availableProviders: [] })));
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/remote-providers");
  renderSpawn(fake);

  // Local: the controller's missing provider disables Start and explains why.
  await screen.findByRole("button", { name: "Connect provider" });
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  // Remote: that same controller-local state must not block the launch.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() => expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull());
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));

  // Switching back restores the local block.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "local" } });
  await screen.findByRole("button", { name: "Connect provider" });
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
});

// The remote-target fix must not trade a WRONG early judgment for no feedback at
// all. With a remote host selected the launch really reaches thread/start, and
// the selected host's own validation is what reports: its WireError surfaces
// verbatim through friendlyLaunchErrorMessage, and nothing is created on the
// controller (the remote hub runs its own hubThreadStart, which canonicalizes
// and rejects a cwd it cannot see).
test("a remote launch rejected by the selected host surfaces that host's own error", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    // The controller would call this cwd a creatable missing directory...
    f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" }));
    // ...and the selected host rejects it as its own cwd failure.
    f.on("thread/start", () => {
      throw new WireError("cwd: no such file or directory on buildbox", -32602);
    });
  });
  window.history.pushState({}, "", "/new?dir=/remote/only/path");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await user.click(screen.getByTestId("spawn-submit"));

  await screen.findByText("Start failed: cwd: no such file or directory on buildbox");
  expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true);
  expect(fake.calls.some((call) => call.method === "evener/dirs/create")).toBe(false);
  expect(screen.queryByRole("button", { name: "Create & start" })).toBeNull();
});

// Relaxing the controller-local provider gate must not mean a doomed remote
// launch is SILENTLY accepted: when the selected host cannot serve it either,
// that host's own credential failure is what the person sees - so "allowed to
// submit" and "actually succeeded" stay distinguishable, and no session is
// navigated to on the failure path.
test("a remote launch the selected host cannot serve reports that host's own failure", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
    f.on("thread/start", () => {
      throw new WireError("provider credentials missing for anthropic", -32014, { evenerErrorInfo: "hubLaunch" });
    });
  });
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/remote-providers-fail");
  renderSpawn(fake);

  // Starts blocked by the CONTROLLER's missing provider check (local target).
  await screen.findByRole("button", { name: "Connect provider" });
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() => expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull());

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));

  await screen.findByText("Start failed: provider credentials missing for anthropic");
  expect(window.location.pathname).not.toContain("/s/");
});

// The plugin preview is the CONTROLLER's own inspection (evener/plugin/preview
// answers for this hub's host). A remote launch runs on the selected source's
// own hub, which resolves its own plugins, so a controller-local "this plugin
// is unavailable" must not disable Start or refuse the submit for a remote
// target - the selection is forwarded and the selected hub validates it at
// start, the same authority model the launch already follows for cwd,
// providers and models.
test("a remote host submission is not blocked by controller-local plugin issues", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) =>
    f.on("evener/plugin/preview", ({ launchOverrides }) =>
      launchOverrides?.enabledPlugins?.includes("alpha")
        ? { ...SPAWN_PLUGIN_PREVIEW, selectionErrors: [{ name: "alpha", reason: "plugin is unavailable" }] }
        : SPAWN_PLUGIN_PREVIEW,
    ),
  );
  window.history.pushState({}, "", "/new?dir=/tmp/remote-plugins");
  renderSpawn(fake);
  await settled();

  // An explicit selection the CONTROLLER's preview reports as unavailable:
  // local Start is disabled by that controller-local reading.
  await act(async () => {
    setDraftField(completionDraft("/tmp/remote-plugins"), "pluginSelection", {
      mode: "explicit",
      names: ["alpha"],
    });
  });
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true));

  // The same controller-local issue must not gate a remote launch. Re-pinned to
  // component 07b's host-catalog gate: a host switch first asks the new host for
  // its harnesses/schema and Start stays blocked until those answers settle, so
  // each verdict below is read once the selected host's discovery has landed
  // (that window is pinned by the sibling "Start is blocked only until the
  // selected host's catalog answers settle" test - not by these assertions).
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "local" } });
  // Drain the local host's own catalog round-trip first: while it is in flight
  // Start is blocked by that window, not by the controller-local plugin issue
  // this assertion is about.
  await settled();
  // The switch back also reset the plugin selection to the host's own default
  // (component 07b review, round four - pinned by the sibling "a host switch
  // drops an explicit plugin selection the new host never previewed"), so
  // re-establish the controller-local selection before reading the local verdict
  // this assertion is about.
  await act(async () => {
    setDraftField(completionDraft("/tmp/remote-plugins"), "pluginSelection", { mode: "explicit", names: ["alpha"] });
  });
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true));
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  // Re-establish the selection for the host now selected: component 07b's
  // round-four reset means an explicit selection only ever belongs to the host
  // it was made on (the sibling "a host switch drops an explicit plugin
  // selection..." test pins the drop), so the verbatim-forwarding assertion
  // below is about a selection the REMOTE host's own form is carrying.
  await act(async () => {
    setDraftField(completionDraft("/tmp/remote-plugins"), "pluginSelection", { mode: "explicit", names: ["alpha"] });
  });

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  // The selection is forwarded verbatim for the selected hub to validate.
  expect(params.launchOverrides).toMatchObject({ enabledPlugins: ["alpha"] });
});

// The model and effort catalogs the form validates against (model/list, the
// model ladder) belong to THIS controller. A value supported only by the
// selected host must not be refused locally - the value rides thread/start and
// the remote hub validates it.
test("a remote host does not pre-validate /model against the controller catalog", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/remote-model");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  // A model absent from the controller's catalog: a local target would toast
  // `unknown value` and never reach thread/start.
  await user.type(promptField(), "/model buildbox-only/model-x");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.modelProvider).toBe("buildbox-only");
  expect(params.model).toBe("model-x");
  // The builtin is consumed, not forwarded as a literal first turn.
  expect(params.input).toEqual([]);
  expect(screen.queryByText(/unknown value/)).toBeNull();
});

test("a remote host does not pre-validate /reasoning-effort against the controller ladder", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/remote-effort");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  // A level outside the controller's ladder: a local target would toast
  // `unknown value` and never reach thread/start.
  await user.type(promptField(), "/reasoning-effort ultra");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.reasoningEffort).toBe("ultra");
  expect(params.input).toEqual([]);
  expect(screen.queryByText(/unknown value/)).toBeNull();
});

// --- remote target: model and cwd ownership (Component 06b, round seven) -----

// The uncredentialed-default fallback reads THIS controller's model/list and
// installs models[0] when launch/resolve names a default whose provider is not
// launchable here. A remote target resolves its own default model from its own
// host's credentials and catalog, so injecting the controller's pick would ride
// thread/start and stop the selected host from resolving its own default.
test("a remote host does not inherit the controller's uncredentialed-default fallback model", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" }, // openai is not launchable on THIS controller
      layers: {},
      provenance: {},
    }));
    // The selected host answers its own catalog and launch defaults (component
    // 07b): those reads are forwarded, so the proxy answers them too.
    f.on("evener/host/request", (params) => {
      const method = (params as HostRequestParams).method;
      if (method === "model/list") {
        return {
          data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
        } as HostForwardedResult;
      }
      if (method === "evener/launch/resolve") {
        return { effective: { model: "openai/gpt-5.5" }, layers: {}, provenance: {} } as HostForwardedResult;
      }
      return routedDiscoveryDefault(method);
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/remote-fallback");
  renderSpawn(fake);
  // Select the remote host before the fallback's CATALOG_SETTLE_MS window can
  // elapse, so this pins the remote-at-mount path.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() =>
    expect(
      fake.calls.some(
        (c) =>
          c.method === "evener/launch/resolve" ||
          (c.method === "evener/host/request" && (c.params as HostRequestParams).method === "evener/launch/resolve"),
      ),
    ).toBe(true),
  );
  // Outlast the settle window: a still-armed fallback would fire in here.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(400);
  });

  expect(modelTrigger().textContent).toContain("(default)");
  expect(modelTrigger().textContent).not.toContain("claude-sonnet-4-5");

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));

  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  // No controller-derived model is forwarded; the host resolves its own default.
  expect(params.model).toBeUndefined();
  expect(params.modelProvider).toBeUndefined();
});

// The same defect from the other direction: a fallback installed while the
// target was local must be retired when the person switches to a remote host.
// Only a still-fallback-derived value is cleared - a person's own pick is not.
test("a controller-catalog fallback model is retired when the target becomes remote", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" },
      layers: {},
      provenance: {},
    }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/remote-fallback-flip");
  renderSpawn(fake);
  await settled();

  // Local target: the fallback installs the controller's first launchable model.
  await waitFor(() => expect(modelTrigger().textContent).toContain("anthropic/claude-sonnet-4-5"));

  // Switching to a remote host retires it: it is the controller's model, not
  // the selected host's.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() => expect(modelTrigger().textContent).toContain("(default)"));
  expect(modelTrigger().textContent).not.toContain("claude-sonnet-4-5");

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));

  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.model).toBeUndefined();
});

// /model takes RAW user text, unlike every other splitModelId caller (which
// splits a provider/model catalog id that always contains a slash). "foo"
// splits to provider "foo" with an empty model, which the selected host ignores
// (its own model field stays empty) - the requested model was silently dropped.
// It is refused pre-launch with the same unknown-value message the local path
// uses.
test("a remote /model value without a provider is refused before launch", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/remote-bare-model");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await user.type(promptField(), "/model foo");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(screen.getByText(/\/model: unknown value "foo"/)).toBeTruthy());
  expect(fake.calls.some((call) => call.method === "thread/start")).toBe(false);
});

// A remote launch's cwd and model belong to the SELECTED HOST, not this
// controller, so they must not be written to the global scalar defaults a later
// LOCAL spawn reads - the remote cwd would default the next local Spawn to a
// path that usually does not exist here.
test("a remote launch does not persist its cwd as the controller's global working directory", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/srv/app");
  renderSpawn(fake);
  await settled();

  // A model plus a cwd: both are host-scoped on a remote submit.
  await act(async () => {
    setDraftField(completionDraft("/srv/app"), "model", "openai/gpt-5");
  });
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((c) => c.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.cwd).toBe("/srv/app");
  // The controller's global defaults a later LOCAL spawn consults are untouched.
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.working_dir")).toBeNull();
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBeNull();
});

// --- remote target: host-scoped project defaults (Component 06b, round eight) ---

// model/list's first entry: the model the uncredentialed-default fallback
// installs, and (in the cross-draft test) a legitimately sticky value that must
// not be confused with it.
const MODEL_A_FALLBACK = "anthropic/claude-sonnet-4-5";

// The per-project blob is keyed by the cwd ALONE, and that path is often also a
// local checkout: a model chosen on the selected host must not become this
// path's LOCAL default - one this hub may not serve. The remote project's
// harness/access layer is still remembered (round seven's blob disposition,
// minus the host-specific model).
test("a remote launch's model does not become the cwd's local project default", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/srv/app");
  const mounted = renderSpawn(fake);
  await settled();

  // A host-only model, as the remote hub's own catalog would offer it.
  await act(async () => {
    setDraftField(completionDraft("/srv/app"), "model", "buildbox-only/gpt-5");
  });
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  // The launch still forwards the model the person configured - the selected
  // host resolves it against its own catalog.
  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.model).toBe("buildbox-only/gpt-5");
  // ...but it is not remembered as this path's local default.
  expect(loadDefaultsBlob("/srv/app").model).toBeUndefined();

  // A later LOCAL spawn of the same path (a fresh page, so fresh drafts) reads
  // exactly those persisted defaults: Model is untouched, not the host's.
  mounted.unmount();
  resetSpawnDraftsForTests();
  renderSpawn(fake);
  await settled();
  expect(modelTrigger().textContent).toContain("(default)");
  expect(modelTrigger().textContent).not.toContain("gpt-5");
});

// The fallback marker is per DRAFT (round eight). SpawnForm is a singleton
// reused across drafts, so a form-local marker let draft A's provenance retire
// an identical model string draft B legitimately owned: B's sticky per-project
// default was cleared when B went remote even though the fallback never touched
// B's model.
test("a fallback installed for one draft does not retire another draft's identical sticky model", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  // Draft B's own sticky default - deliberately the exact string draft A's
  // fallback installs.
  localStorage.setItem("evener-hub.spawn-defaults./tmp/fallback-owner", JSON.stringify({ model: MODEL_A_FALLBACK }));
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: MODEL_A_FALLBACK }],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" }, // openai is not launchable on THIS controller
      layers: {},
      provenance: {},
    }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/fallback-installer");
  renderSpawn(fake);
  await settled();

  // Draft A: the controller-catalog fallback installs models[0].
  await waitFor(() => expect(modelTrigger().textContent).toContain(MODEL_A_FALLBACK));

  // Draft B, reached through the cwd picker: same string, different provenance.
  await setWorkingDir(user, "/tmp/fallback-owner");
  expect(modelTrigger().textContent).toContain(MODEL_A_FALLBACK);

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  // B's model is B's own sticky default, so it survives the remote switch.
  expect(modelTrigger().textContent).toContain(MODEL_A_FALLBACK);
  expect(modelTrigger().textContent).not.toContain("(default)");

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.model).toBe(MODEL_A_FALLBACK);
});

// The marker lives WITH the draft, so it survives the pane unmounting and
// remounting (a single-pane/mobile host mounts only the active route, and the
// draft map outlives the form). A form-local ref lost it there, which left the
// controller's fallback model to ride a remote launch after all.
test("a controller-catalog fallback model is still retired after a pane remount", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: MODEL_A_FALLBACK }],
    }));
    f.on("evener/launch/resolve", () => ({
      effective: { model: "openai/gpt-5.5" },
      layers: {},
      provenance: {},
    }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/fallback-remount");
  const mounted = renderSpawn(fake);
  await settled();
  await waitFor(() => expect(modelTrigger().textContent).toContain(MODEL_A_FALLBACK));

  // A remount with no navigation keeps the same draft - and so its provenance.
  mounted.unmount();
  renderSpawn(fake);
  await settled();
  expect(modelTrigger().textContent).toContain(MODEL_A_FALLBACK);

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() => expect(modelTrigger().textContent).toContain("(default)"));
  expect(modelTrigger().textContent).not.toContain("claude-sonnet-4-5");

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.model).toBeUndefined();
  expect(params.modelProvider).toBeUndefined();
});

// The complement of the retire rule, and the invariant that keeps it honest: a
// model the person picked themselves is their own choice (handleModelChange
// drops the draft's fallback mark), so it is never retired on a remote switch -
// not even in a draft where the fallback had installed a different model.
test("a user's explicit model pick is preserved when the target becomes remote", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    f.on("model/list", () => ({
      data: [
        { provider: "anthropic", model: "claude-sonnet-4-5", displayName: MODEL_A_FALLBACK },
        { provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" },
      ],
    }));
    // mistral is not launchable on THIS controller, so the fallback fires even
    // though openai (the person's own pick below) is launchable.
    f.on("evener/launch/resolve", () => ({
      effective: { model: "mistral/large" },
      layers: {},
      provenance: {},
    }));
  });
  window.history.pushState({}, "", "/new?dir=/tmp/fallback-explicit");
  renderSpawn(fake);
  await settled();

  // The fallback installs the controller's first launchable model...
  await waitFor(() => expect(modelTrigger().textContent).toContain(MODEL_A_FALLBACK));
  // ...and then the person picks a different one from the picker.
  await pickModel(user, "gpt-5", "openai/gpt-5");
  expect(modelTrigger().textContent).toContain("openai/gpt-5");

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  // The explicit pick is not the fallback's value, so it is not retired.
  expect(modelTrigger().textContent).toContain("openai/gpt-5");
  expect(modelTrigger().textContent).not.toContain("(default)");

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.model).toBe("openai/gpt-5");
});

// The submit snapshots the source (handleSpawn's closure carries the
// submittedSource/remoteLaunch thread/start and saveDefaults receive) and then
// awaits the local directory preflight, so a host change mid-submit would
// launch on a different host than the picker shows. The picker is disabled
// while busy instead.
test("the host picker cannot change while a submit is in flight", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => f.on("thread/start", () => started.promise));
  window.history.pushState({}, "", "/new?dir=/tmp/busy-host");
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "run locally");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  expect((screen.getByLabelText("Host") as HTMLSelectElement).disabled).toBe(true);

  await act(async () => started.resolve(startResponse("local:busy-host")));
});

// The dir-picker's seed (GLOBAL_LAST_WORKING_DIR_KEY) is the CONTROLLER's own
// browse history: recording a cwd picked while a remote host is selected would
// open the next LOCAL picker at a path that usually does not exist here.
test("a cwd picked for a remote target is not recorded as the controller's picker seed", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  window.history.pushState({}, "", "/new?dir=/tmp/seed-local");
  // The path validator is host-routed (component 07b): the selected host
  // answers for its own filesystem, so the picker's acceptance of the remote
  // path below is the host's verdict.
  renderSpawn(
    readyClient((f) =>
      f.on("evener/host/request", (params) =>
        (params as HostRequestParams).method === "evener/path/validate"
          ? ({ path: "/srv/remote-only", valid: true } as HostForwardedResult)
          : routedDiscoveryDefault((params as HostRequestParams).method),
      ),
    ),
  );
  await settled();

  // A local pick still updates the controller's own browse history.
  await setWorkingDir(user, "/tmp/seed-local-next");
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBe("/tmp/seed-local-next");

  // With a remote host selected, the path belongs to that host instead.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  localStorage.removeItem(LAST_WORKING_DIR_KEY);
  await setWorkingDir(user, "/srv/remote-only");
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBeNull();
});

// --- remote target: source-aware paths (Component 06b, round nine) -----------

// evener/path/validate answers for THIS controller's filesystem, so a directory
// that exists only on the selected host reads as invalid here - and the picker
// refuses to select what its validator rejects. A remote target's cwd belongs to
// that host, which validates it when the session starts (the authority the cwd
// preflight already defers to), so the controller must not make the judgment at
// all (round nine).
test("a remote target can select a directory this controller cannot see", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = readyClient((f) => {
    // The controller's own filesystem cannot see the path...
    f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" }));
    // ...but the selected host can, and it is the host that judges a remote
    // target's path (component 07b, through evener/host/request).
    f.on("evener/host/request", (params) =>
      (params as HostRequestParams).method === "evener/path/validate"
        ? ({ path: "/srv/remote-only", valid: true } as HostForwardedResult)
        : routedDiscoveryDefault((params as HostRequestParams).method),
    );
  });
  window.history.pushState({}, "", "/new?dir=/tmp/picker-base");
  renderSpawn(fake);
  await settled();

  // A LOCAL target keeps the controller's gate unchanged: the same path is
  // reported invalid and cannot be selected.
  await user.click(workingDir());
  const localInput = await screen.findByRole("textbox", { name: "Path" });
  await user.clear(localInput);
  await user.type(localInput, "/srv/remote-only{Enter}");
  expect((await screen.findByRole("alert")).textContent).toBe("no such file or directory");
  expect((screen.getByRole("button", { name: "Use this folder" }) as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByRole("button", { name: "Cancel" }));

  // The same path on a remote target is the host's business: it can be picked.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await setWorkingDir(user, "/srv/remote-only");
  expectWorkingDir("/srv/remote-only");
  expect(screen.queryByText("no such file or directory")).toBeNull();
});

// The picker's "New folder" is a WRITE, and evener/dirs/create MkdirAll's the
// path on the filesystem of the hub it reaches. A remote target's path belongs
// to the selected host, so the request is forwarded there (component 07b)
// instead of materializing the folder on the controller.
test("the picker creates a folder on the selected host, never on the controller", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const forwarded: string[] = [];
  const fake = readyClient((f) =>
    f.on("evener/host/request", (params) => {
      const method = (params as HostRequestParams).method;
      forwarded.push(method);
      if (method === "evener/dirs/create")
        return { path: "/srv/remote-create/child", created: true } as HostForwardedResult;
      return routedDiscoveryDefault(method);
    }),
  );
  window.history.pushState({}, "", "/new?dir=/tmp/remote-create-base");
  renderSpawn(fake);
  await settled();

  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await user.click(workingDir());
  const input = await screen.findByRole("textbox", { name: "Path" });
  await user.clear(input);
  await user.type(input, "/srv/remote-create{Enter}");
  await waitFor(() =>
    expect((screen.getByRole("button", { name: "Use this folder" }) as HTMLButtonElement).disabled).toBe(false),
  );
  await user.click(screen.getByRole("button", { name: "New folder" }));
  await user.type(screen.getByRole("textbox", { name: "Folder name" }), "child{Enter}");

  // Nothing was created on the controller...
  expect(fake.calls.some((call) => call.method === "evener/dirs/create")).toBe(false);
  // ...the creation went to the selected host, through the admin proxy.
  expect(forwarded).toContain("evener/dirs/create");
  for (const call of fake.calls.filter((call) => call.method === "evener/host/request")) {
    expect((call.params as HostRequestParams).host).toBe("buildbox");
  }
});

// The advanced panel validates a path-kind value through the same controller
// closure and marks the field invalid on failure - and
// collectAdvancedOverrides DROPS an invalid field, so a valid remote path
// silently vanished from launchOverrides. For a remote target the value is
// forwarded for the selected host to judge (round nine).
test("a remote target's advanced path value is not dropped for being invisible here", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const schema = {
    options: [
      {
        field: "agent",
        wireField: "agent",
        label: "Agent",
        kind: "text",
        group: "general",
        pathKind: "command",
        perLaunch: true,
      },
    ],
  };
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => schema);
    // The controller cannot see the value...
    f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" }));
    // ...and the selected host judges its own filesystem (component 07b), where
    // the path kind accepts it. The schema is host-dependent too.
    f.on("evener/host/request", (params) => {
      const method = (params as HostRequestParams).method;
      if (method === "evener/launch/schema") return schema as HostForwardedResult;
      if (method === "evener/path/validate") return { path: "", valid: true } as HostForwardedResult;
      return routedDiscoveryDefault(method);
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/remote-advanced-path");
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  // Local: unchanged. The controller's verdict marks the field invalid, the
  // reason is shown, and the value is excluded from the launch.
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "host-only-agent" } });
  expect(await screen.findByText("no such file or directory")).toBeTruthy();
  await waitFor(() =>
    expect(completionDraft("/tmp/remote-advanced-path").fields.getState().advancedOverrides).toEqual({}),
  );

  // Remote: the same kind of value rides thread/start for the host to resolve.
  // (A different string, not the one above: React suppresses a change event
  // whose value is unchanged, so re-typing the identical text would never reach
  // the field's own handler.)
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  // The target switch retires the previous host's schema until the selected
  // host's own answer lands, so the field reappears with the host's options.
  fireEvent.change(await screen.findByLabelText("Agent"), { target: { value: "remote-only-agent" } });
  expect(completionDraft("/tmp/remote-advanced-path").fields.getState().advancedOverrides).toEqual({
    agent: "remote-only-agent",
  });
  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.launchOverrides).toMatchObject({ agent: "remote-only-agent" });
});

// The host picker is a DISPLAY of what the manifest last said about the hosts.
// The store retains that snapshot while the fresh manifest is in flight, and
// reading the SETTLED-only list there hid the picker (or flipped the visible
// choice to Local) for the length of every revalidation - while the launch
// decision still has to come from the settled list, which is what the second
// half of this test pins (round nine).
test("the host picker keeps its host while the manifest revalidates", async () => {
  const user = setupUser();
  const sources: NavigationManifest["sources"] = [
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
    { id: "offline-host", label: "offline-host", kind: "ssh", online: false },
  ];
  seedSources(sources);
  const fake = readyClient();
  window.history.pushState({}, "", "/new?dir=/tmp/host-revalidate");
  renderSpawn(fake);
  await settled();
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });

  await act(async () => seedSources(sources, { loading: true, stale: true }));
  const picker = screen.getByLabelText("Host") as HTMLSelectElement;
  expect(picker.value).toBe("buildbox");
  const options = within(picker).getAllByRole("option") as HTMLOptionElement[];
  expect(options.map((option) => option.value)).toEqual(["local", "buildbox", "offline-host"]);
  // The retained snapshot keeps its own offline labelling: a withheld list
  // reads as "unknown", which would flip this one to online.
  const offline = options.find((option) => option.value === "offline-host") as HTMLOptionElement;
  expect(offline.disabled).toBe(true);
  expect(offline.textContent).toContain("offline");

  // Unsettled, the draft's host is still the launch target (only a settled
  // manifest may confirm a fallback), so what the picker shows is what submits.
  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
});

// --- remote target: target-mode changes (Component 06b, round ten) -----------

// The stale-model sweep judges a draft's model against THIS controller's
// model/list. A remote launch runs on the selected source's own hub, which
// resolves its own model, so a model this catalog does not list can be
// perfectly valid there - and the sweep must not erase it. The target mode is
// re-read when the response lands, so a draft that switches hosts while the
// request is in flight (the sequence below) is not judged by the run it
// started as local. The local half is unchanged and stays pinned by the
// existing sweep tests above.
test("a remote target keeps the draft's model this controller's catalog calls stale", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  window.history.pushState({}, "", "/new?dir=/tmp/remote-stale-model");
  localStorage.setItem(
    "evener-hub.spawn-defaults./tmp/remote-stale-model",
    JSON.stringify({ model: "openai/retired" }),
  );
  const global = deferred<ModelListResponse>();
  const fake = readyClient((f) => {
    f.on("model/list", ({ harness, cwd }) =>
      harness === "evener" && cwd === undefined ? global.promise : { data: [{ provider: "openai", model: "gpt-5" }] },
    );
    // The SELECTED HOST's catalog is what judges a remote draft's model
    // (component 07b), and it still offers the draft's own model.
    f.on("evener/host/request", (params) =>
      (params as HostRequestParams).method === "model/list"
        ? ({ data: [{ provider: "openai", model: "retired" }] } as HostForwardedResult)
        : routedDiscoveryDefault((params as HostRequestParams).method),
    );
  });
  renderSpawn(fake);
  await settled();
  // The sticky model is in the draft, and the controller's global catalog - the
  // one whose verdict sweeps a local draft - has not answered yet.
  expect(modelValue().textContent).toContain("openai/retired");

  // The draft moves to the remote host while that request is in flight.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await act(async () => global.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));

  // The controller's verdict is a fact about THIS controller: the host it is
  // about to launch on still offers the model, so the draft keeps it and
  // nothing claims it was discarded.
  expect(modelValue().textContent).toContain("openai/retired");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();

  // A remote launch forwards the model for the selected host to resolve.
  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.model).toBe("openai/retired");
});

// The same skip applies to a draft that is ALREADY on a remote host when a
// fresh catalog lands (a remount, or a provider refresh under a remote draft):
// nothing is registered to sweep it at all. The first mount parks its own
// request so the model is still in the draft, and the remount's request - the
// registration the fix has to refuse - answers immediately.
test("a draft already on a remote host is never swept by the controller's catalog", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  window.history.pushState({}, "", "/new?dir=/tmp/remote-remount-model");
  localStorage.setItem(
    "evener-hub.spawn-defaults./tmp/remote-remount-model",
    JSON.stringify({ model: "openai/retired" }),
  );
  const parked = deferred<ModelListResponse>();
  let globalRequests = 0;
  let routedRequests = 0;
  const fake = readyClient((f) => {
    f.on("model/list", ({ harness, cwd }) => {
      if (harness !== "evener" || cwd !== undefined) return { data: [{ provider: "openai", model: "gpt-5" }] };
      globalRequests += 1;
      return globalRequests === 1 ? parked.promise : { data: [{ provider: "openai", model: "gpt-5" }] };
    });
    // A remote draft's catalog read is forwarded to the selected host
    // (component 07b), so the controller's own listing is not what a remote
    // mount consults.
    f.on("evener/host/request", (params) => {
      const forwarded = params as HostRequestParams;
      if (forwarded.method !== "model/list") return routedDiscoveryDefault(forwarded.method);
      const { harness, cwd } = forwarded.params as { harness?: string; cwd?: string };
      if (harness !== "evener" || cwd !== undefined) {
        return { data: [{ provider: "openai", model: "gpt-5" }] } as HostForwardedResult;
      }
      routedRequests += 1;
      // The host offers the draft's own model, so its verdict keeps it.
      return { data: [{ provider: "openai", model: "retired" }] } as HostForwardedResult;
    });
  });
  const mounted = renderSpawn(fake);
  await settled();
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await act(async () => parked.resolve({ data: [{ provider: "openai", model: "gpt-5" }] }));
  expect(modelValue().textContent).toContain("openai/retired");

  // Remount: the draft (and its host) persist, so the fresh mount's own
  // catalog request resolves under a remote target from its first render - and
  // that request is the HOST's (routed), never the controller's parked one.
  mounted.unmount();
  renderSpawn(fake);
  await settled();
  await act(async () => {});
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");
  expect(routedRequests).toBeGreaterThan(0);
  expect(globalRequests).toBe(1);
  expect(modelValue().textContent).toContain("openai/retired");
  expect(screen.queryByText(/discarded last-used model/i)).toBeNull();
});

// A path-kind field's `invalid` flag (and its message) records what THIS
// controller could see at the moment the value changed, and
// collectAdvancedOverrides drops a flagged field from launchOverrides. Moving
// the Host picker to a remote source changes who judges the path - the host
// does, at start - but nothing re-validated the stored value, so a remote-only
// path typed while local stayed dropped and kept showing the controller's
// stale error. The mode change re-validates the stored value (round ten).
test("a path marked invalid while local reaches the launch after switching host without re-typing", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const schema = {
    options: [
      {
        field: "agent",
        wireField: "agent",
        label: "Agent",
        kind: "text",
        group: "general",
        pathKind: "command",
        perLaunch: true,
      },
    ],
  };
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => schema);
    // The controller cannot see the path...
    f.on("evener/path/validate", ({ path }) => ({ path, valid: false, error: "no such file or directory" }));
    // ...and the selected host judges its own filesystem (component 07b), where
    // the value is fine. The schema is host-dependent too, so it is answered
    // here for the routed read.
    f.on("evener/host/request", (params) => {
      const method = (params as HostRequestParams).method;
      if (method === "evener/launch/schema") return schema as HostForwardedResult;
      if (method === "evener/path/validate") return { path: "", valid: true } as HostForwardedResult;
      return routedDiscoveryDefault(method);
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/remote-switch-path");
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));

  // Local: the controller's verdict marks the field invalid, shows the reason,
  // and excludes the value from the launch.
  fireEvent.change(await screen.findByLabelText("Agent"), { target: { value: "host-only-agent" } });
  expect(await screen.findByText("no such file or directory")).toBeTruthy();
  await waitFor(() =>
    expect(completionDraft("/tmp/remote-switch-path").fields.getState().advancedOverrides).toEqual({}),
  );

  // The Host picker moves to the remote source WITHOUT re-typing the value.
  // The host switch re-reads the launch schema, so the Advanced fields remount;
  // wait for the value to land in the draft before judging the field.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() =>
    expect(completionDraft("/tmp/remote-switch-path").fields.getState().advancedOverrides).toEqual({
      agent: "host-only-agent",
    }),
  );
  expect(((await screen.findByLabelText("Agent")) as HTMLInputElement).value).toBe("host-only-agent");
  expect(screen.queryByText("no such file or directory")).toBeNull();

  // Back to local, the controller is the judge again: the same stored value is
  // re-marked and dropped rather than left silently flagged or silently kept.
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "local" } });
  expect(await screen.findByText("no such file or directory")).toBeTruthy();
  await waitFor(() =>
    expect(completionDraft("/tmp/remote-switch-path").fields.getState().advancedOverrides).toEqual({}),
  );
  fireEvent.change(screen.getByLabelText("Host"), { target: { value: "buildbox" } });
  await waitFor(() =>
    expect(completionDraft("/tmp/remote-switch-path").fields.getState().advancedOverrides).toEqual({
      agent: "host-only-agent",
    }),
  );

  await fillPrompt(user, "run remotely");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  const params = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(params.source).toBe("buildbox");
  expect(params.launchOverrides).toMatchObject({ agent: "host-only-agent" });
});

// --- host-routed discovery (Component 07b) ---------------------------------

// The discovery methods the spawn form's mount path issues for the selected
// host. The on-demand methods (path completion/validation, dirs/create, recent
// projects, the slash catalog, plugin preview) are covered by their own seams'
// tests; stores/hostRouting.test.ts covers the whole spec set at the seam.
const MOUNT_DISCOVERY_METHODS = [
  "model/list",
  "evener/harnesses/list",
  "evener/launch/schema",
  "evener/launch/resolve",
  "evener/git/head",
  "evener/instance/list",
] as const;

test("a selected remote host routes every discovery call through evener/host/request", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
  ]);
  const fake = new FakeClient("ready");
  const forwarded: Record<string, unknown> = {
    "model/list": {
      data: [{ provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" }],
    },
    "evener/harnesses/list": { data: [{ id: "evener", label: "evener", kind: "evener" }] },
    "evener/launch/schema": { options: [] },
    "evener/launch/resolve": { effective: {}, layers: {}, provenance: {} },
    "evener/git/head": { head: "main" },
    "evener/instance/list": { instances: [], availableProviders: [] },
    "evener/projects/recent": { data: [] },
    "evener/paths/complete": { data: [] },
    "evener/path/validate": { path: "", valid: true },
    "evener/dirs/create": { path: "", created: true },
    "evener/plugin/preview": { plugins: [] },
    "evener/spawn/slashCatalog": { commands: [], skills: [] },
  };
  fake.on("evener/host/request", (params) => {
    const method = (params as HostRequestParams).method;
    return (forwarded[method] ?? {}) as HostForwardedResult;
  });
  connectionStore.getState().connect(fake);

  // Select the remote host before the form mounts, so the mount-time discovery
  // calls are issued against it from the first render.
  const draft = selectSpawnDirectory("/tmp/remote-routing");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/tmp/remote-routing");
  renderSpawn(fake);
  await settled();

  await waitFor(() => {
    const routed = new Set(
      fake.calls
        .filter((call) => call.method === "evener/host/request")
        .map((call) => (call.params as HostRequestParams).method),
    );
    for (const method of MOUNT_DISCOVERY_METHODS) expect(routed.has(method)).toBe(true);
  });

  // Every proxied call names the selected host...
  for (const call of fake.calls.filter((call) => call.method === "evener/host/request")) {
    expect((call.params as HostRequestParams).host).toBe("buildbox");
  }
  // ...and nothing was left controller-scoped.
  for (const method of HOST_DEPENDENT_DISCOVERY_METHODS) {
    expect(fake.calls.some((call) => call.method === method)).toBe(false);
  }
});

// The "buildbox" host's answers for every discovery call the spawn form can
// issue. `fail` names methods the remote host refuses (an offline host, a proxy
// rejection); every other answer is a well-formed empty/minimal response.
function answerRemoteHost(
  fake: FakeClient,
  options: { overrides?: Record<string, unknown>; fail?: readonly string[] } = {},
): void {
  const answers: Record<string, unknown> = {
    "model/list": { data: [{ provider: "openai", model: "gpt-4o", displayName: "openai/gpt-4o" }] },
    "evener/harnesses/list": { data: [{ id: "evener", label: "evener", kind: "evener" }] },
    "evener/launch/schema": { options: [] },
    "evener/launch/resolve": { effective: {}, layers: {}, provenance: {} },
    "evener/git/head": { head: "main" },
    "evener/instance/list": { instances: [], availableProviders: [] },
    "evener/projects/recent": { data: [] },
    "evener/paths/complete": { data: [] },
    "evener/path/validate": { path: "", valid: true },
    "evener/dirs/create": { path: "", created: true },
    "evener/plugin/preview": { plugins: [] },
    "evener/spawn/slashCatalog": { commands: [], skills: [] },
    ...options.overrides,
  };
  fake.on("evener/host/request", (params) => {
    const forwarded = params as HostRequestParams;
    if (forwarded.host !== "buildbox") throw new Error(`unexpected host "${forwarded.host}"`);
    if (options.fail?.includes(forwarded.method)) throw new Error(`remote ${forwarded.method} unavailable`);
    return (answers[forwarded.method] ?? {}) as HostForwardedResult;
  });
}

const REMOTE_SOURCES: NavigationManifest["sources"] = [
  { id: "local", label: "Local", kind: "local", online: true },
  { id: "buildbox", label: "buildbox", kind: "ssh", online: true },
];

// buildbox's own credentialed provider instance, the same shape readyClient
// gives the controller. answerRemoteHost's default is an EMPTY registry (what the
// provider-setup tests want: "configure one on that host to use a model"), so a
// host that is launch-ready has to say so itself - otherwise providerRequired
// holds Start disabled and nothing can be submitted.
const REMOTE_CREDENTIALED_INSTANCE = {
  name: "anthropic",
  providerId: "anthropic",
  protocol: "anthropic",
  auth: "bearer",
  implicit: false,
  isDefault: true,
  activeSource: "store",
  hasStoredOAuth: false,
  credentialRequired: true,
};

function readyRemoteHost(fake: FakeClient, overrides: Record<string, unknown> = {}): void {
  answerRemoteHost(fake, {
    overrides: {
      "evener/instance/list": { instances: [REMOTE_CREDENTIALED_INSTANCE], availableProviders: [] },
      ...overrides,
    },
  });
}

// A remote host's catalog describes only that host. The controller's persisted
// spawn defaults are controller-scoped: sweeping them against another machine's
// model list would permanently delete models the controller still offers, and
// switching back to Local would leave those project defaults lost.
test("a remote host's catalog never sweeps the controller's saved model defaults", async () => {
  window.history.pushState({}, "", "/new?dir=/tmp/remote-sweep");
  const savedBlob = JSON.stringify({ harness: "evener", model: "openai/gpt-5" });
  localStorage.setItem("evener-hub.spawn-defaults./tmp/remote-sweep", savedBlob);
  localStorage.setItem("evener-hub.spawn-defaults.global.model", "openai/gpt-5");
  seedSources(REMOTE_SOURCES);
  // The remote catalog offers the openai provider but not the saved model, so
  // modelValidityAgainstList would classify "openai/gpt-5" as stale.
  const fake = readyClient((f) => answerRemoteHost(f));
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/remote-sweep");
  setDraftField(draft, "source", "buildbox");
  renderSpawn(fake);
  await settled();

  // The remote catalog has been consumed by the sweep effect and the draft
  // validator (which reports the in-memory draft's model as discarded).
  expect(await screen.findByText(/discarded last-used model openai\/gpt-5/i)).toBeTruthy();
  expect(localStorage.getItem("evener-hub.spawn-defaults./tmp/remote-sweep")).toBe(savedBlob);
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBe("openai/gpt-5");
});

// The same rule in the state an unsettled manifest produces: the settled list
// is withheld, so hostChoice is a provisional "local" while the draft still
// names its host - and every catalog this pane reads, the sweep's included, is
// issued against the DRAFT'S host (round nine's `submittedSource = source`).
// The controller's stored defaults must survive it exactly as they survive a
// settled remote target: the sweep's authority is the machine the form is
// reading, never the withheld fallback.
test("a revalidating manifest does not hand the controller's catalog the sweep", async () => {
  const cwd = "/tmp/remote-sweep-revalidate";
  window.history.pushState({}, "", `/new?dir=${cwd}`);
  const savedBlob = JSON.stringify({ harness: "evener", model: "openai/gpt-5" });
  localStorage.setItem(`evener-hub.spawn-defaults.${cwd}`, savedBlob);
  localStorage.setItem("evener-hub.spawn-defaults.global.model", "openai/gpt-5");
  seedSources(REMOTE_SOURCES, { loading: true, stale: true });
  // The host's catalog offers the openai provider but not the saved model, so a
  // sweep running against it would delete both stored values.
  const fake = readyClient((f) =>
    answerRemoteHost(f, {
      overrides: { "model/list": { data: [{ provider: "openai", model: "gpt-4o", displayName: "openai/gpt-4o" }] } },
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory(cwd);
  setDraftField(draft, "source", "buildbox");
  renderSpawn(fake);
  await settled();

  // The pane's catalog is the host's: the routed model/list answers for it, and
  // the controller's own list is never asked at all.
  await waitFor(() =>
    expect(
      fake.calls.filter(
        (call) =>
          call.method === "evener/host/request" &&
          (call.params as HostRequestParams).method === "model/list" &&
          (call.params as HostRequestParams).host === "buildbox",
      ).length,
    ).toBeGreaterThan(0),
  );
  expect(modelListRequests(fake)).toHaveLength(0);
  // So the stored layer is exactly as it was stored.
  expect(localStorage.getItem(`evener-hub.spawn-defaults.${cwd}`)).toBe(savedBlob);
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBe("openai/gpt-5");
});

// A host change drops the previous host's harness/schema catalogs: they describe
// the machine that answered, and a failed load for the new host must show an
// empty catalog rather than the previous host's, which the user could otherwise
// select and only discover is missing at thread/start.
test("a failed remote catalog load shows an empty harness list, not the previous host's", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => answerRemoteHost(f, { fail: ["evener/harnesses/list", "evener/launch/schema"] }));
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/host-catalog");
  renderSpawn(fake);
  await settled();

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const harness = screen.getByLabelText("Harness") as HTMLSelectElement;
  expect(within(harness).getByRole("option", { name: "external" })).toBeTruthy();

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");

  await waitFor(() => expect(within(harness).queryByRole("option", { name: "external" })).toBeNull());
  // The fallback the select renders with no host catalog, not the controller's.
  expect(
    within(harness)
      .queryAllByRole("option")
      .map((option) => option.textContent),
  ).toEqual(["evener"]);
});

// On-demand discovery for a remote host reads that host's filesystem too: the
// directory picker's recent-projects list and path completion are proxied, and
// the controller's own methods are never called.
test("a remote spawn reads recent projects and completes paths from the selected host", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) =>
    answerRemoteHost(f, {
      overrides: {
        "evener/projects/recent": { data: ["/srv/remote-project"] },
        "evener/paths/complete": { data: ["/srv/remote-project/src"] },
      },
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/remote-project");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/srv/remote-project");
  renderSpawn(fake);
  await settled();

  await user.click(workingDir());
  await screen.findByRole("textbox", { name: "Path" });
  expect(await screen.findByRole("button", { name: "Open /srv/remote-project/src" })).toBeTruthy();

  await waitFor(() => {
    const routed = fake.calls
      .filter((call) => call.method === "evener/host/request")
      .map((call) => call.params as HostRequestParams);
    expect(routed.filter((call) => call.method === "evener/projects/recent" && call.host === "buildbox")).toHaveLength(
      1,
    );
    expect(routed.some((call) => call.method === "evener/paths/complete" && call.host === "buildbox")).toBe(true);
  });
  // Neither on-demand call was left controller-scoped.
  expect(fake.calls.some((call) => call.method === "evener/projects/recent")).toBe(false);
  expect(fake.calls.some((call) => call.method === "evener/paths/complete")).toBe(false);
});

// A credential or model change made ON the selected remote host arrives wrapped
// in evener/host/notification tagged with that host (app_host_admin.go's
// fan-out). The pane's scoped model/list cache is keyed on the credential
// generation, so the wrapper must advance it exactly as the unwrapped
// evener/auth/updated does for the controller -- otherwise a remote spawn keeps
// validating against a catalog the host no longer serves.
test("a selected remote host's wrapped config notification reloads the pane model catalog", async () => {
  seedSources(REMOTE_SOURCES);
  // The host's instance refetch never lands, so nothing but the pane's own
  // generation key can invalidate the catalog: this pins the FORM's observation
  // of the wrapper, not the credentials store's debounced fetchHost.
  const fake = readyClient((f) =>
    answerRemoteHost(f, { overrides: { "evener/instance/list": new Promise(() => {}) } }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/remote-notify");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/tmp/remote-notify");
  renderSpawn(fake);
  await settled();

  // Every model/list this form issues is forwarded to buildbox: the pane
  // catalog, the default-model preview, and the global sweep all share the
  // selected host.
  const paneLoads = () =>
    fake.calls.filter(
      (call) =>
        call.method === "evener/host/request" &&
        (call.params as HostRequestParams).host === "buildbox" &&
        (call.params as HostRequestParams).method === "model/list",
    ).length;
  await waitFor(() => expect(paneLoads()).toBeGreaterThan(0));
  const before = paneLoads();

  act(() =>
    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "buildbox", method: "evener/auth/updated", params: {} },
    }),
  );

  await waitFor(() => expect(paneLoads()).toBeGreaterThan(before));
});

// The same wrapper, in the same unsettled window the picker's revalidation test
// above pins: a withholding manifest makes hostChoice a provisional "local"
// while the pane's catalog is still scoped to the draft's host. Keying this
// listener on hostChoice dropped the host's own wrapper for the length of every
// revalidation, so a credential or model change made on the host during one
// never invalidated the catalog this form validates against (component 07b
// review, residual).
test("the draft's host wrapped config notification reloads the catalog while the manifest revalidates", async () => {
  seedSources(REMOTE_SOURCES, { loading: true, stale: true });
  const fake = readyClient((f) =>
    answerRemoteHost(f, {
      overrides: {
        // Both host reads stay in flight: the instance partition therefore
        // keeps one identity (nothing in the pane's catalog cache key moves
        // with it) and every catalog request is a fresh, recorded one rather
        // than a settled answer reused from it - the same isolation the
        // settled version of this test uses.
        "evener/instance/list": new Promise(() => {}),
        "model/list": new Promise(() => {}),
      },
    }),
  );
  connectionStore.getState().connect(fake);
  const cwd = "/tmp/host-notify-revalidate";
  const draft = selectSpawnDirectory(cwd);
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", `/new?dir=${cwd}`);
  renderSpawn(fake);
  await settled();
  // Unsettled manifest: the settled list is withheld, so hostChoice is a
  // provisional "local" while the launch target and every discovery call stay
  // on the draft's host.
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");

  const catalogLoads = (params: unknown) =>
    fake.calls.filter(
      (call) =>
        call.method === "evener/host/request" &&
        (call.params as HostRequestParams).host === "buildbox" &&
        (call.params as HostRequestParams).method === "model/list" &&
        JSON.stringify((call.params as HostRequestParams).params) === JSON.stringify(params),
    ).length;
  // The pane passes through a harness value on its way to the settled draft
  // scope, and issues one catalog request per scope. Wait for the SETTLED
  // scope's own request: its promise stays in the pane's per-scope cache (the
  // host's answer never lands), so from here only a cache invalidation - the
  // credential generation this test is about - can issue another one.
  await waitFor(() => expect(catalogLoads({ cwd })).toBeGreaterThan(0));
  const before = catalogLoads({ cwd });

  act(() =>
    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "buildbox", method: "evener/auth/updated", params: {} },
    }),
  );

  await waitFor(() => expect(catalogLoads({ cwd })).toBeGreaterThan(before));
});

// The other half of the same question, in the state a SETTLED manifest produces
// when the draft's host is not launchable (offline here; a host removed from
// the list is the same). hostChoice falls back to "local" and the write-back
// puts that in the draft, so the machine this pane reads - and the machine it
// would launch on - is the controller. The wrapper from the host it no longer
// reads must therefore not move its catalog: the filter is the host that was
// actually asked, which is what lets the pane recover when the host it DOES
// read changes (the revalidation test above).
test("a wrapper from a draft host the offline fallback replaced does not reload the catalog", async () => {
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: false },
  ]);
  const fake = readyClient();
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-offline-notify");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/tmp/host-offline-notify");
  renderSpawn(fake);
  await settled();

  // The offline fallback is written into the draft (round nine), so the pane's
  // own reads are controller-scoped from here on.
  await waitFor(() => expect(draft.fields.getState().source).toBe("local"));
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");
  // Hydration: the controller's catalog has answered for the SETTLED scope (the
  // scoped load the fallback's write-back triggers). Everything before it is
  // the mount's own churn, and every consumer after it is a cache hit.
  const scopedCatalogLoads = () =>
    fake.calls.filter(
      (call) => call.method === "model/list" && (call.params as { cwd?: string }).cwd === "/tmp/host-offline-notify",
    ).length;
  await waitFor(() => expect(scopedCatalogLoads()).toBeGreaterThan(0));

  const hostLoadsFor = (host: string) =>
    fake.calls.filter(
      (call) =>
        call.method === "evener/host/request" &&
        (call.params as HostRequestParams).host === host &&
        (call.params as HostRequestParams).method === "model/list",
    ).length;
  const before = { controller: modelListRequests(fake).length, replacedHost: hostLoadsFor("buildbox") };
  act(() =>
    fake.emitNotification({
      method: "evener/host/notification",
      params: { host: "buildbox", method: "evener/auth/updated", params: {} },
    }),
  );
  // Nothing to await: the claim is that nothing happens. The window is a
  // tripwire past the pane's 250ms catalog debounce (a generation bump re-runs
  // the catalog effect and issues a request), not the mechanism.
  await act(async () => {
    await vi.advanceTimersByTimeAsync(400);
  });

  // Neither the replaced host's scope nor the controller's catalog moved: the
  // wrapper describes a machine this pane is not reading.
  expect(hostLoadsFor("buildbox")).toBe(before.replacedHost);
  expect(modelListRequests(fake).length).toBe(before.controller);
});

// The provider editor (ConnectProviderDialog) is controller-scoped: it reads and
// writes the credentialsStore's top-level fields on the plain connection. A
// remote host's provider verdict now comes from that host's own partition, so
// offering the controller editor for a remote host would "connect" the wrong
// machine and leave Start disabled. The controller's own path is unchanged.
test("a remote spawn never offers the controller's Connect provider editor", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => {
    answerRemoteHost(f);
    f.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  });
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/remote-connect");
  renderSpawn(fake);
  await settled();

  // Controller with no usable instance: the dialog action is still offered.
  await screen.findByRole("button", { name: "Connect provider" });
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));

  // The remote partition reports missing too, but the only offered remediation
  // is a message naming the host, never the controller's editor.
  await screen.findByText(/configure one on that host to use a model/i);
  expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  // Back on the controller, the editor returns.
  await user.selectOptions(screen.getByLabelText("Host"), "local");
  await screen.findByRole("button", { name: "Connect provider" });
});

// The branch readout describes the SELECTED host's working tree. Switching
// hosts with the same directory must drop the previous host's HEAD immediately,
// not display it until (or after) the new host's evener/git/head resolves.
test("switching hosts with the same directory drops the previous host's branch readout", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => {
    f.on("evener/git/head", () => ({ head: "local-branch" }));
    // The remote host's HEAD never lands: the controller's readout must not
    // stand in for it.
    answerRemoteHost(f, { overrides: { "evener/git/head": new Promise(() => {}) } });
  });
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/host-branch");
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(screen.getByTestId("spawn-branch").textContent).toContain("local-branch"));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));

  await waitFor(() => expect(screen.queryByTestId("spawn-branch")).toBeNull());
});

// ...and the readout must survive the manifest going UNSETTLED again. A
// revalidating manifest withholds its sources, so hostChoice is a provisional
// "local" while the draft's host is still the launch and discovery target
// (round nine) - the same window the picker above pins. Keying the readout on
// hostChoice instead blanked a readout the pane's own evener/git/head call had
// already answered for the draft's host, for the length of every revalidation
// (component 07b review, residual).
test("the branch readout keeps the draft's host while the manifest revalidates", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => {
    f.on("evener/git/head", () => ({ head: "local-branch" }));
    answerRemoteHost(f, { overrides: { "evener/git/head": { head: "host-branch" } } });
  });
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/host-branch-revalidate");
  renderSpawn(fake);
  await settled();

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect(screen.getByTestId("spawn-branch").textContent).toBe("host-branch"));

  // The fresh manifest is in flight: the settled list is withheld, so the
  // launch target stays the draft's host (round nine) and so does the host the
  // readout describes.
  await act(async () => seedSources(REMOTE_SOURCES, { loading: true, stale: true }));
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");
  expect(screen.getByTestId("spawn-branch").textContent).toBe("host-branch");
});

// The resolved default launch config is a property of the selected host, not
// just the directory. Switching hosts with the same directory must not let the
// previous host's default satisfy -- or fail -- the Start model check while the
// new host's evener/launch/resolve is pending.
test("switching hosts with the same directory drops the previous host's default-model verdict", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => {
    // The controller has no default model: Start reads as model-required.
    f.on("evener/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
    // The remote host's resolve never lands, so only the host key can clear the
    // controller's stale "no default" verdict.
    answerRemoteHost(f, { overrides: { "evener/launch/resolve": new Promise(() => {}) } });
  });
  connectionStore.getState().connect(fake);
  window.history.pushState({}, "", "/new?dir=/tmp/host-default");
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(screen.getByRole("alert").textContent).toMatch(/no default model/i));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));

  await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
});

// The draft's launch config is chosen from the SELECTED host's catalogs, and the
// draft store carries it across a host switch. Left alone, a harness that exists
// only on the host that produced it still rides thread/start - and the form
// cannot even show the mismatch, because an empty harness catalog renders the
// "evener" fallback label. buildbox offers only "evener".
test("switching to a host that does not offer the draft's harness drops it from the launch", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => readyRemoteHost(f));
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-harness");
  setDraftField(draft, "harness", "external");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/tmp/host-harness");
  renderSpawn(fake);
  await settled();

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  // The controller offered "external"; buildbox does not, so the draft's
  // selection drops back to the host's own default.
  await waitFor(() => expect(draft.fields.getState().harness).toBe(""));

  // And the launch carries no harness: the host resolves its own default rather
  // than being handed a harness only the controller has.
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  const started = fake.calls.find((entry) => entry.method === "thread/start");
  expect(started?.params).toMatchObject({ model: "openai/gpt-4o" });
  expect(started?.params).not.toHaveProperty("harness");
});

// The same defect class for Advanced options: the override map is keyed by the
// schema field names the selected host exposes, and an empty schema renders no
// Advanced fields at all - so a stale override would ride thread/start with
// nothing on screen to account for it.
test("switching to a host whose schema lacks the draft's advanced options drops them", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const controllerOption: LaunchOption = {
    field: "agent",
    wireField: "agent",
    label: "Agent",
    group: "general",
    kind: "text",
    perLaunch: true,
  };
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({ options: [controllerOption] }));
    // buildbox's own schema (answerRemoteHost) exposes nothing.
    readyRemoteHost(f);
  });
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-schema");
  setDraftField(draft, "advancedOverrides", { agent: "controller-agent" });
  setDraftField(draft, "advancedValues", { agent: { value: "controller-agent" } });
  window.history.pushState({}, "", "/new?dir=/tmp/host-schema");
  renderSpawn(fake);
  await settled();
  expect(draft.fields.getState().advancedOverrides).toEqual({ agent: "controller-agent" });

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));

  await waitFor(() => expect(draft.fields.getState().advancedOverrides).toEqual({}));
  expect(draft.fields.getState().advancedValues).toEqual({});
});

// Until the SELECTED host's catalogs answer, the form has no authority about
// what that host offers - and the draft's launch config has not been reconciled
// against them. A submit through that window could carry a value the host does
// not have, so Start is blocked until the answers land (either way: an empty
// catalog is an answer, so a host that refuses one can never hold Start
// hostage).
test("Start is blocked only until the selected host's catalog answers settle", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  let releaseHarnesses!: (result: HostForwardedResult) => void;
  const fake = readyClient((f) =>
    readyRemoteHost(f, {
      "evener/harnesses/list": new Promise<HostForwardedResult>((resolve) => {
        releaseHarnesses = resolve;
      }),
    }),
  );
  connectionStore.getState().connect(fake);
  // A chosen model keeps modelRequired out of the picture, so the only thing
  // that can disable Start here is the catalog block itself (buildbox's
  // evener/launch/resolve reports no default model).
  const draft = selectSpawnDirectory("/tmp/host-pending");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/tmp/host-pending");
  renderSpawn(fake);
  await settled();
  // Nothing about the controller's own catalogs is pending.
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  await settled();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);

  act(() => releaseHarnesses({ data: [{ id: "evener", label: "evener", kind: "evener" }] }));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
});

// The persisted defaults a submit writes are controller-scoped localStorage. A
// remote launch's cwd, harness and model all belong to the selected host, so a
// later LOCAL spawn of the same path must not inherit any of them - and a remote
// submit must not delete a local project's stored layer for that path either.
test("a remote submit writes no controller spawn defaults", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => readyRemoteHost(f));
  connectionStore.getState().connect(fake);
  // A layer stored for the same path by (or for) the controller - the remote
  // submit below has no authority over it.
  const localBlob = JSON.stringify({ harness: "evener", access_mode: "read-only" });
  localStorage.setItem("evener-hub.spawn-defaults./srv/remote-submit", localBlob);
  const draft = selectSpawnDirectory("/srv/remote-submit");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/srv/remote-submit");
  renderSpawn(fake);
  await settled();

  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  const started = fake.calls.find((entry) => entry.method === "thread/start");
  expect(started?.params).toMatchObject({ source: "buildbox" });

  // The per-project blob a local spawn of the same path would read is untouched
  // (component 07b review, round four: the cwd key cannot distinguish the two;
  // this is the half of round four this rebase keeps - the "writes nothing at
  // all" half is re-pinned to main's round eight in the cases above).
  expect(localStorage.getItem("evener-hub.spawn-defaults./srv/remote-submit")).toBe(localBlob);
  // And the controller-wide scalars a local spawn reads are untouched.
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.model")).toBeNull();
  expect(localStorage.getItem("evener-hub.spawn-defaults.global.working_dir")).toBeNull();
});

// --- host-switch hardening (Component 07b review, round four) ---------------

// The host selector must not stay interactive while a submission is awaiting
// preflight or thread/start: a change there moves the picker (and the draft's
// source) while the pending operation keeps using the hostChoice captured at
// submit time, so the launch - and the defaults it persists - can belong to a
// different host than the one on screen.
test("the host picker is locked while a submit is in flight", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => readyRemoteHost(f));
  // The start never resolves, so the submit stays in flight and the picker's
  // lock is the only thing keeping it from moving off the captured host.
  fake.on("thread/start", () => new Promise<ThreadStartResponse>(() => {}));
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-lock");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/tmp/host-lock");
  renderSpawn(fake);
  await settled();

  const picker = () => screen.getByLabelText("Host") as HTMLSelectElement;
  await waitFor(() => expect(picker().disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));

  expect(picker().disabled).toBe(true);
  expect(picker().value).toBe("buildbox");
  const startedOn = fake.calls.find((call) => call.method === "thread/start")?.params as ThreadStartParams;
  expect(startedOn.source).toBe("buildbox");
});

// A plugin selection is a list of names resolved against the SELECTED host's own
// preview. A host switch whose preview fails can never be reconciled, so the
// previous host's explicit names would keep riding combinedOverrides into the
// new host's preview, slash catalog and thread/start - handing that host
// plugins it may not provide.
test("a host switch drops an explicit plugin selection the new host never previewed", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", () => SPAWN_PLUGIN_PREVIEW);
    // buildbox's own preview never answers: there is no list to reconcile the
    // controller's selection against on that host.
    answerRemoteHost(f, {
      fail: ["evener/plugin/preview"],
      overrides: {
        "evener/instance/list": { instances: [REMOTE_CREDENTIALED_INSTANCE], availableProviders: [] },
        // The controller's own catalog (readyClient) offers the same model, so
        // the model-validity effect keeps it across the switch.
        "model/list": { data: [{ provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" }] },
      },
    });
  });
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-plugin-switch");
  setDraftField(draft, "model", "openai/gpt-5");
  window.history.pushState({}, "", "/new?dir=/tmp/host-plugin-switch");
  renderSpawn(fake);
  await settled();

  // An explicit selection on the controller.
  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));
  await waitFor(() =>
    expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("Configured plugins: alpha"),
  );
  expect(draft.fields.getState().pluginSelection).toEqual({ mode: "explicit", names: ["alpha"] });

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await screen.findAllByText("Couldn't inspect plugins");

  // The controller's selection is gone with the switch...
  expect(draft.fields.getState().pluginSelection).toEqual({ mode: "default" });
  // ...so the launch hands buildbox no selection at all.
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  const started = fake.calls.find((entry) => entry.method === "thread/start");
  expect(started?.params).toMatchObject({ source: "buildbox" });
  const startedParams = started?.params as ThreadStartParams;
  expect(startedParams.launchOverrides?.enabledPlugins).toBeUndefined();
});

// Drafts persist at module scope, so a mount can start on a remote host whose
// catalogs have not answered yet. The draft's launch config came from that host
// (or another one) and has not been reconciled against the answers still in
// flight: Start must wait, exactly as it does for a host CHANGE.
test("a mount that starts on a remote draft waits for that host's catalogs", async () => {
  seedSources(REMOTE_SOURCES);
  let releaseHarnesses!: (result: HostForwardedResult) => void;
  const fake = readyClient((f) =>
    readyRemoteHost(f, {
      "evener/harnesses/list": new Promise<HostForwardedResult>((resolve) => {
        releaseHarnesses = resolve;
      }),
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/remote-mount");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/tmp/remote-mount");
  renderSpawn(fake);
  await settled();

  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
  // The ⌘/Ctrl+Enter chord reaches the submit path directly, so the guard has to
  // hold there too: nothing may be preflighted while the catalogs are
  // outstanding.
  fireEvent.keyDown(promptField(), { key: "Enter", metaKey: true });
  await act(async () => {});
  expect(
    fake.calls.filter(
      (call) =>
        call.method === "evener/host/request" && (call.params as HostRequestParams).method === "evener/path/validate",
    ),
  ).toHaveLength(0);
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(0);

  act(() => releaseHarnesses({ data: [{ id: "evener", label: "evener", kind: "evener" }] }));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
});

// A host change clears harnesses/schemaOptions, so the "settled" stamp for the
// host that answered before it must be invalidated with them: switching A→B→A
// before B answers otherwise reconciles the draft against the just-cleared EMPTY
// catalogs and silently wipes the harness and every Advanced-options value the
// restored host still offers - never restored once A's answers land.
test("a switch away and back before the middle host answers keeps the draft", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const controllerOption: LaunchOption = {
    field: "agent",
    wireField: "agent",
    label: "Agent",
    group: "general",
    kind: "text",
    perLaunch: true,
  };
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({ options: [controllerOption] }));
    // buildbox never answers the catalog probe, so only the switch back can
    // decide what the draft is reconciled against.
    answerRemoteHost(f, {
      overrides: {
        "evener/harnesses/list": new Promise<HostForwardedResult>(() => {}),
        "evener/launch/schema": new Promise<HostForwardedResult>(() => {}),
      },
    });
  });
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-rapid");
  setDraftField(draft, "harness", "external");
  setDraftField(draft, "model", "openai/gpt-4o");
  setDraftField(draft, "advancedOverrides", { agent: "controller-agent" });
  setDraftField(draft, "advancedValues", { agent: { value: "controller-agent" } });
  window.history.pushState({}, "", "/new?dir=/tmp/host-rapid");
  renderSpawn(fake);
  await settled();
  await waitFor(() => expect(draft.fields.getState().harness).toBe("external"));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  await user.selectOptions(screen.getByLabelText("Host"), "local");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local"));

  // The controller's catalogs re-answer for the restored host, and the draft's
  // controller-offered config is still there to reconcile against them.
  await waitFor(() => expect(draft.fields.getState().harness).toBe("external"));
  expect(draft.fields.getState().advancedOverrides).toEqual({ agent: "controller-agent" });
  expect(draft.fields.getState().advancedValues).toEqual({ agent: { value: "controller-agent" } });
});

// The directory picker's "last accepted directory" seed is CONTROLLER-wide (the
// fallback a later local picker opens at): a remote host's path stored there
// would seed the next local spawn with a directory this hub usually does not
// have. The controller's own picks keep seeding it.
test("a remote directory pick never seeds the controller's last-working-dir", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) =>
    answerRemoteHost(f, { overrides: { "evener/projects/recent": { data: ["/srv/remote-pick"] } } }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/remote-dir-pick");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new?dir=/tmp/remote-dir-pick");
  renderSpawn(fake);
  await settled();

  await user.click(workingDir());
  await user.click(await screen.findByRole("button", { name: "Open recent /srv/remote-pick" }));
  const confirm = screen.getByRole("button", { name: "Use this folder" });
  await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
  await user.click(confirm);

  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBeNull();
});

// Host reconciliation filters the Advanced-options maps to the fields the
// selected host offers. An error is state about a field, not about the host, so
// the error of a KEPT field must survive the filter - it explains a validation
// the user still has to fix - while a dropped field's error goes with it.
test("host reconciliation keeps the errors of the advanced fields it keeps", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const agentOption: LaunchOption = {
    field: "agent",
    wireField: "agent",
    label: "Agent",
    group: "general",
    kind: "text",
    perLaunch: true,
  };
  const keptOption: LaunchOption = {
    field: "maxRounds",
    wireField: "maxRounds",
    label: "Max rounds",
    group: "general",
    kind: "text",
    perLaunch: true,
  };
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({ options: [agentOption, keptOption] }));
    // buildbox offers maxRounds but not agent.
    readyRemoteHost(f, { "evener/launch/schema": { options: [keptOption] } });
  });
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-errors");
  setDraftField(draft, "model", "openai/gpt-4o");
  setDraftField(draft, "advancedOverrides", { agent: "controller-agent", maxRounds: 7 });
  setDraftField(draft, "advancedValues", {
    agent: { value: "controller-agent" },
    maxRounds: { value: "7" },
  });
  setDraftField(draft, "advancedErrors", { agent: "no agent here", maxRounds: "fix this value" });
  window.history.pushState({}, "", "/new?dir=/tmp/host-errors");
  renderSpawn(fake);
  await settled();

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));

  await waitFor(() => expect(draft.fields.getState().advancedOverrides).toEqual({ maxRounds: 7 }));
  expect(draft.fields.getState().advancedValues).toEqual({ maxRounds: { value: "7" } });
  expect(draft.fields.getState().advancedErrors).toEqual({ maxRounds: "fix this value" });
});

// --- round five ------------------------------------------------------------

/** The calls the pane forwarded to `host` through evener/host/request. */
function routedHostCalls(fake: FakeClient, method: string, host = "buildbox"): HostRequestParams[] {
  return fake.calls
    .filter((call) => call.method === "evener/host/request")
    .map((call) => call.params as HostRequestParams)
    .filter((call) => call.method === method && call.host === host);
}

// A failed catalog load is NOT an answer. The round-four rejection handlers
// cleared the lists and the Promise.all stamped the host "settled" whatever the
// outcome, so a single transient hub error reconciled a perfectly good draft
// against an empty catalog: the harness and every Advanced-options override
// vanished, and a later successful load had nothing left to restore. Start must
// still be released by a settled-but-unanswered load (an offline host must not
// hold the submit hostage) - only the RECONCILIATION waits for a real answer.
test("a failed catalog load leaves the draft's harness and advanced options alone", async () => {
  const fake = readyClient((f) => {
    f.on("evener/harnesses/list", () => {
      throw new Error("hub unavailable");
    });
    f.on("evener/launch/schema", () => {
      throw new Error("hub unavailable");
    });
  });
  const draft = selectSpawnDirectory("/tmp/catalog-failure");
  setDraftField(draft, "harness", "external");
  setDraftField(draft, "advancedOverrides", { agent: "controller-agent" });
  setDraftField(draft, "advancedValues", { agent: { value: "controller-agent" } });
  window.history.pushState({}, "", "/new?dir=/tmp/catalog-failure");
  renderSpawn(fake);
  await settled();

  await waitFor(() => expect(fake.calls.filter((call) => call.method === "evener/harnesses/list")).toHaveLength(1));
  // Let the rejection handlers and the reconciliation effect take their turn.
  await act(async () => {});
  expect(draft.fields.getState().harness).toBe("external");
  expect(draft.fields.getState().advancedOverrides).toEqual({ agent: "controller-agent" });
  expect(draft.fields.getState().advancedValues).toEqual({ agent: { value: "controller-agent" } });
});

// The other half of the same rule, on a host that has no answers at all: the
// request SETTLED, so the submit gate opens - the host refuses what it cannot
// serve at start rather than leaving Start disabled forever.
test("a host whose catalogs all fail still releases Start", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) =>
    answerRemoteHost(f, {
      fail: ["evener/harnesses/list", "evener/launch/schema"],
      // A credentialed registry, so the only thing that could hold Start here is
      // the catalog gate itself.
      overrides: { "evener/instance/list": { instances: [REMOTE_CREDENTIALED_INSTANCE], availableProviders: [] } },
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/catalog-answerless");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/srv/catalog-answerless");
  renderSpawn(fake);
  await settled();

  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({ source: "buildbox" });
});

// An unavailable manifest is not evidence that the draft's host is gone: it is
// empty both while it loads and if it never arrives, so reading that emptiness
// as "fall back to local" silently ran a remote draft's launch on the
// controller. The draft's own source is the last host that was confirmed, and
// it stays the launch AND discovery target until the manifest says otherwise.
test("a remote draft is not silently converted to local while the manifest is unavailable", async () => {
  const user = setupUser();
  const fake = readyClient((f) => readyRemoteHost(f));
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/manifest-gap");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/srv/manifest-gap");
  renderSpawn(fake);
  await settled();

  // Every host-scoped call went to the draft's own host...
  await waitFor(() => expect(routedHostCalls(fake, "evener/harnesses/list")).toHaveLength(1));
  // ...and the launch carries it, so it runs on buildbox rather than here.
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({ source: "buildbox" });
});

// The submit gate covered the harness/schema answers but not the model list, so
// after a host switch a model chosen from the PREVIOUS host's catalog could be
// submitted while the new host's model list was still in flight. A model is
// host-derived like the harness: the gate has to include its host's settlement.
test("a previous host's model cannot be submitted while the new host's model list is outstanding", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const remoteModelList = deferred<{ data: ModelDescriptor[] }>();
  const fake = readyClient((f) => readyRemoteHost(f, { "model/list": remoteModelList.promise }));
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-model-pending");
  // Persisted from, and still valid in, the CONTROLLER's own catalog - it stays
  // non-empty across the switch, so only the gate can hold the submit.
  setDraftField(draft, "model", "openai/gpt-5");
  window.history.pushState({}, "", "/new?dir=/tmp/host-model-pending");
  renderSpawn(fake);
  await settled();
  // A local mount is not host-derived, so it stays startable exactly as before.
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  // buildbox answered for harnesses/schema, so the catalog gate is open and the
  // model list is the only thing still outstanding.
  await waitFor(() => expect(routedHostCalls(fake, "evener/harnesses/list")).toHaveLength(1));
  expect(draft.fields.getState().model).toBe("openai/gpt-5");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
  fireEvent.keyDown(promptField(), { key: "Enter", metaKey: true });
  await act(async () => {});
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(0);

  act(() => remoteModelList.resolve({ data: [{ provider: "openai", model: "gpt-5", displayName: "openai/gpt-5" }] }));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  expect(fake.calls.find((call) => call.method === "thread/start")?.params).toMatchObject({ source: "buildbox" });
});

// The pane's injected validatePath closes over the host selected when the field
// was edited, and the panel accepts the answer on field identity alone - so a
// late controller answer marked (or re-added) an advanced path override after
// the user had switched hosts. The superseded answer must never land: the field
// is judged by the host that is selected NOW.
test("a late path validation from the previous host never marks the field on the new host", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const agentOption: LaunchOption = {
    field: "agent",
    wireField: "agent",
    label: "Agent",
    kind: "text",
    group: "general",
    pathKind: "command",
    perLaunch: true,
  };
  const localValidation = deferred<{ path: string; valid: boolean; error: string }>();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({ options: [agentOption] }));
    f.on("evener/path/validate", ({ path }) =>
      path === "review-agent" ? localValidation.promise : { path, valid: true },
    );
    // buildbox offers the same field but rejects the value.
    readyRemoteHost(f, {
      "evener/launch/schema": { options: [agentOption] },
      "evener/path/validate": { path: "review-agent", valid: false, error: "not on buildbox" },
    });
  });
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-path-validation");
  window.history.pushState({}, "", "/new?dir=/tmp/host-path-validation");
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  fireEvent.change(screen.getByLabelText("Agent"), { target: { value: "review-agent" } });
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "evener/path/validate")).toHaveLength(1));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  await waitFor(() => expect(routedHostCalls(fake, "evener/harnesses/list")).toHaveLength(1));

  // The controller finally accepts the path - after the user left it.
  await act(async () => localValidation.resolve({ path: "review-agent", valid: true, error: "" }));
  // The value is re-judged by buildbox instead of being marked valid by the
  // machine the user left. Re-pinned for main's landed target-change
  // re-validation (the pathValidationTarget effect): that effect already asks
  // the newly selected host about every stored path value, so the superseded
  // local answer's re-ask is not the only routed call - what this pins is that
  // every routed ask names this field and that the verdict that lands is
  // buildbox's.
  await waitFor(() => expect(routedHostCalls(fake, "evener/path/validate").length).toBeGreaterThan(0));
  for (const call of routedHostCalls(fake, "evener/path/validate")) {
    expect(call.params).toMatchObject({
      path: "review-agent",
      kind: "command",
    });
  }
  await waitFor(() =>
    expect(draft.fields.getState().advancedValues).toEqual({ agent: { value: "review-agent", invalid: true } }),
  );
  // ...so it cannot ride the launch to a host that does not have it.
  expect(draft.fields.getState().advancedOverrides).toEqual({});
  expect(await screen.findByText("not on buildbox")).toBeTruthy();
});

// The same guard, through the other consumer the finding names: a pathList add
// goes on the validator's "ok" verdict, so a superseded host's late acceptance
// re-added an entry the selected host never accepted.
test("a late pathList validation from the previous host cannot re-add its entry on the new host", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const pathListOption: LaunchOption = {
    field: "pluginDirs",
    wireField: "pluginDirs",
    label: "Plugin dirs",
    kind: "pathList",
    group: "general",
    pathKind: "command",
    perLaunch: true,
    description: "Add a plugin dir",
  };
  const localValidation = deferred<{ path: string; valid: boolean; error: string }>();
  const fake = readyClient((f) => {
    f.on("evener/launch/schema", () => ({ options: [pathListOption] }));
    f.on("evener/path/validate", ({ path }) =>
      path === "review-dir" ? localValidation.promise : { path, valid: true },
    );
    // buildbox offers the same field but refuses the entry.
    readyRemoteHost(f, {
      "evener/launch/schema": { options: [pathListOption] },
      "evener/path/validate": { path: "review-dir", valid: false, error: "no such dir on buildbox" },
    });
  });
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/tmp/host-pathlist");
  window.history.pushState({}, "", "/new?dir=/tmp/host-pathlist");
  renderSpawn(fake);
  await settled();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.type(screen.getByPlaceholderText("Add a plugin dir"), "review-dir");
  await user.click(screen.getByRole("button", { name: "Add" }));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "evener/path/validate")).toHaveLength(1));

  await user.selectOptions(screen.getByLabelText("Host"), "buildbox");
  await waitFor(() => expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox"));
  await waitFor(() => expect(routedHostCalls(fake, "evener/harnesses/list")).toHaveLength(1));

  // The controller accepts it only after the user has left it.
  await act(async () => localValidation.resolve({ path: "review-dir", valid: true, error: "" }));
  await waitFor(() => expect(routedHostCalls(fake, "evener/path/validate")).toHaveLength(1));
  // buildbox refused it, so the entry never joins the draft's list - and so
  // never rides the launch to a host that never accepted it. (Its message is
  // the add row's own transient feedback; the host switch repopulates the
  // panel's fields from the new host's schema, so only the draft state here is
  // durable.)
  expect(draft.fields.getState().advancedOverrides).toEqual({});
  expect(draft.fields.getState().advancedValues).toEqual({});
});

// The picker's last-working-dir seed is controller-wide: opening a REMOTE picker
// at a path the controller last accepted validates a directory that need not
// exist on the selected host. Only a local launch may seed from it.
test("a remote directory picker does not start from the controller's last working directory", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  localStorage.setItem(LAST_WORKING_DIR_KEY, "/home/controller/last");
  const fake = readyClient((f) =>
    readyRemoteHost(f, { "evener/path/validate": { path: "/home/buildbox", valid: true } }),
  );
  connectionStore.getState().connect(fake);
  // No working directory yet, so the fallback seed is what browsing starts from.
  const draft = selectSpawnDirectory("");
  setDraftField(draft, "source", "buildbox");
  window.history.pushState({}, "", "/new");
  renderSpawn(fake);
  await settled();

  await user.click(workingDir());
  await waitFor(() => expect(routedHostCalls(fake, "evener/path/validate")).toHaveLength(1));
  expect(routedHostCalls(fake, "evener/path/validate")[0]?.params).toMatchObject({ path: "~" });
});

// --- round six -------------------------------------------------------------

// A host's two catalogs answer and fail independently. Round five separated
// "the request settled" from "the answer arrived", but stamped one shared "host
// settled" value and only when BOTH answered - so a host that answered one
// catalog and refused the other reconciled NEITHER half, and the draft's
// harness rode thread/start to a host whose own harness list omits it
// (component 07b review, round six). Here buildbox answers
// evener/harnesses/list (with only "evener") and refuses evener/launch/schema:
// the answered harness half must drop "external", while the half whose request
// never answered leaves the Advanced-options maps alone (the round-five rule).
test("a host that answers its harness list reconciles the draft even when its schema rejects", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) =>
    answerRemoteHost(f, {
      // buildbox's own evener/harnesses/list answers with only "evener"; its
      // evener/launch/schema never answers at all.
      fail: ["evener/launch/schema"],
      // A credentialed registry, so nothing but the catalog gate can hold Start.
      overrides: { "evener/instance/list": { instances: [REMOTE_CREDENTIALED_INSTANCE], availableProviders: [] } },
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/partial-harness");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "harness", "external");
  setDraftField(draft, "model", "openai/gpt-4o");
  setDraftField(draft, "advancedOverrides", { agent: "controller-agent" });
  setDraftField(draft, "advancedValues", { agent: { value: "controller-agent" } });
  window.history.pushState({}, "", "/new?dir=/srv/partial-harness");
  renderSpawn(fake);
  await settled();

  // The answered catalog does not offer "external", so the draft drops it...
  await waitFor(() => expect(draft.fields.getState().harness).toBe(""));
  // ...while the half whose request never answered is untouched.
  expect(draft.fields.getState().advancedOverrides).toEqual({ agent: "controller-agent" });
  expect(draft.fields.getState().advancedValues).toEqual({ agent: { value: "controller-agent" } });

  // Start is released by the settlement (round five), and the launch now
  // carries no harness that only the controller offers.
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  const started = fake.calls.find((entry) => entry.method === "thread/start");
  expect(started?.params).toMatchObject({ source: "buildbox" });
  expect(started?.params).not.toHaveProperty("harness");
});

// The mirror case: the schema answers without the draft's Advanced-options
// field while the harness list refuses. The answered schema half reconciles
// (dropping "agent"), and the unanswered harness half keeps the draft's
// harness - a request that never answered is still not evidence about what the
// host offers.
test("a host that answers its schema reconciles the advanced options even when its harness list rejects", async () => {
  seedSources(REMOTE_SOURCES);
  const keptOption: LaunchOption = {
    field: "maxRounds",
    wireField: "maxRounds",
    label: "Max rounds",
    group: "general",
    kind: "text",
    perLaunch: true,
  };
  const fake = readyClient((f) =>
    answerRemoteHost(f, {
      // buildbox's own schema offers maxRounds but not agent; its harness list
      // never answers.
      fail: ["evener/harnesses/list"],
      overrides: { "evener/launch/schema": { options: [keptOption] } },
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/partial-schema");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "harness", "external");
  setDraftField(draft, "model", "openai/gpt-4o");
  setDraftField(draft, "advancedOverrides", { agent: "controller-agent", maxRounds: 7 });
  setDraftField(draft, "advancedValues", {
    agent: { value: "controller-agent" },
    maxRounds: { value: "7" },
  });
  window.history.pushState({}, "", "/new?dir=/srv/partial-schema");
  renderSpawn(fake);
  await settled();

  // The answered schema omits "agent", so its override and value drop...
  await waitFor(() => expect(draft.fields.getState().advancedOverrides).toEqual({ maxRounds: 7 }));
  expect(draft.fields.getState().advancedValues).toEqual({ maxRounds: { value: "7" } });
  // ...while the harness whose request never answered stays put.
  expect(draft.fields.getState().harness).toBe("external");
});

// --- round seven -----------------------------------------------------------

/** Every "create this directory" the pane issued, naming the host it was
 * addressed to: the plain call is the controller's, a forwarded one carries the
 * host it was scoped to. */
function createDirCallHosts(fake: FakeClient): string[] {
  const hosts: string[] = [];
  for (const call of fake.calls) {
    if (call.method === "evener/dirs/create") hosts.push("local");
    else if (
      call.method === "evener/host/request" &&
      (call.params as HostRequestParams).method === "evener/dirs/create"
    ) {
      hosts.push((call.params as HostRequestParams).host);
    }
  }
  return hosts;
}

// The "Create directory?" confirmation is an action against ONE host: buildbox
// validated the path and supplied the draft's launch config, so the pending
// create belongs to buildbox. The manifest's `online` flag is live, and an
// offline source makes hostChoice fall back to "local" (see its derivation)
// without touching createDialogPath - so before round seven the open dialog's
// "Create & start" created the path and started the session on the host that
// took over, with a draft that host never offered. A host switch now dismisses
// the pending create instead of re-homing it.
test("a host that drops offline while the create dialog is open never creates or starts on the host that took over", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  const fake = readyClient((f) =>
    readyRemoteHost(f, { "evener/path/validate": { path: "/srv/taken-over", valid: false } }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/taken-over");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  window.history.pushState({}, "", "/new?dir=/srv/taken-over");
  renderSpawn(fake);
  await settled();

  // buildbox says the directory does not exist, so the pane offers to create it
  // - on buildbox, which is also where the draft's model came from.
  await user.click(screen.getByTestId("spawn-submit"));
  expect(await screen.findByRole("dialog", { name: "Create directory?" })).toBeTruthy();

  await act(async () => {
    seedSources([
      { id: "local", label: "Local", kind: "local", online: true },
      { id: "buildbox", label: "buildbox", kind: "ssh", online: false },
    ]);
  });
  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");

  // The confirmation went with the host it was bound to...
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(draft.fields.getState()).toMatchObject({ createDialogPath: null, createDialogHost: null });
  // ...so nothing was created anywhere and nothing was started, on either host.
  expect(createDirCallHosts(fake)).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(0);
  // Re-pinned to main's landed offline-fallback write-back (component 06b
  // review, round nine - see "a host that returns online after its offline
  // fallback stays on local"): the picker's fallback is recorded in the draft so
  // the select and the launch target never diverge, which supersedes the branch's
  // round-five "the stale draft value stays until the picker changes it". The
  // draft's other config is untouched, and what round seven still guarantees -
  // no create and no start on the host that took over - is asserted above.
  expect(draft.fields.getState().source).toBe("local");
});

// The binding is re-checked at the confirm, not only by the switch effect: a
// dialog restored from the draft (a remount) can open onto a selection that has
// moved since it was preflighted, and that confirm must not fall back to the
// host selected now. Here buildbox went offline while the pane was unmounted,
// so the restored dialog is bound to buildbox while hostChoice is "local".
test("a restored create dialog whose preflight host is no longer selected aborts instead of creating the path", async () => {
  const user = setupUser();
  seedSources([
    { id: "local", label: "Local", kind: "local", online: true },
    { id: "buildbox", label: "buildbox", kind: "ssh", online: false },
  ]);
  const fake = readyClient();
  const draft = selectSpawnDirectory("/srv/stale-create");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "createDialogPath", "/srv/stale-create");
  setDraftField(draft, "createDialogHost", "buildbox");
  window.history.pushState({}, "", "/new?dir=/srv/stale-create");
  renderSpawn(fake);
  await settled();

  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("local");
  await user.click(await screen.findByRole("button", { name: "Create & start" }));

  expect(createDirCallHosts(fake)).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(0);
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(draft.fields.getState()).toMatchObject({ createDialogPath: null, createDialogHost: null });
  expect(await screen.findByText(/host changed after this directory was checked/i)).toBeTruthy();
});

// The other half of handleSpawn's gate: the host that preflighted the path is
// still selected, but its catalogs have not answered, so the draft's launch
// config has not been reconciled against them. The confirmation is held (the
// binding is intact, so the dialog stays for the next click) and releases on the
// answers - settlement, not success (round five), so it can never dead-end.
test("a create confirmation waits for the selected host's catalogs before creating and starting", async () => {
  const user = setupUser();
  seedSources(REMOTE_SOURCES);
  let releaseHarnesses!: (result: HostForwardedResult) => void;
  const fake = readyClient((f) =>
    readyRemoteHost(f, {
      "evener/harnesses/list": new Promise<HostForwardedResult>((resolve) => {
        releaseHarnesses = resolve;
      }),
    }),
  );
  connectionStore.getState().connect(fake);
  const draft = selectSpawnDirectory("/srv/unsettled-create");
  setDraftField(draft, "source", "buildbox");
  setDraftField(draft, "model", "openai/gpt-4o");
  setDraftField(draft, "createDialogPath", "/srv/unsettled-create");
  setDraftField(draft, "createDialogHost", "buildbox");
  window.history.pushState({}, "", "/new?dir=/srv/unsettled-create");
  renderSpawn(fake);
  await settled();

  expect((screen.getByLabelText("Host") as HTMLSelectElement).value).toBe("buildbox");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
  await user.click(screen.getByRole("button", { name: "Create & start" }));

  expect(createDirCallHosts(fake)).toEqual([]);
  expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(0);
  expect(screen.getByRole("dialog", { name: "Create directory?" })).toBeTruthy();

  act(() => releaseHarnesses({ data: [{ id: "evener", label: "evener", kind: "evener" }] }));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));

  await user.click(screen.getByRole("button", { name: "Create & start" }));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  // The create and the launch both still name the host the path was preflighted
  // on, with the draft that host offered.
  expect(createDirCallHosts(fake)).toEqual(["buildbox"]);
  expect(fake.calls.find((entry) => entry.method === "thread/start")?.params).toMatchObject({
    source: "buildbox",
    cwd: "/srv/unsettled-create",
  });
  expect(screen.queryByRole("dialog")).toBeNull();
});

test("a ?host= prefill seeds the draft's launch host (the rail's project-copy spawn carries it)", () => {
  window.history.replaceState({}, "", "/new?dir=/repo/x&host=devbox");
  applySpawnURL();
  const draft = spawnDraftsStore.getState().current;
  expect(draft?.fields.getState().source).toBe("devbox");
  window.history.replaceState({}, "", "/");
});

test("a ?host=local prefill overrides the draft's last-chosen host for the same directory", () => {
  window.history.replaceState({}, "", "/new?dir=/repo/x&host=devbox");
  applySpawnURL();
  expect(spawnDraftsStore.getState().current?.fields.getState().source).toBe("devbox");
  // The same project's local copy launches with the same cwd; the prefill
  // must retarget the draft, not leave the remote host sticky.
  window.history.replaceState({}, "", "/new?dir=/repo/x&host=local");
  applySpawnURL();
  expect(spawnDraftsStore.getState().current?.fields.getState().source).toBe("local");
  window.history.replaceState({}, "", "/");
});
