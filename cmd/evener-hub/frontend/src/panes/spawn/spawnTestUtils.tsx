import type {
  HostForwardedResult,
  HostRequestParams,
  ModelDescriptor,
  ModelListParams,
  NavigationManifest,
  PluginPreviewResponse,
  Thread,
  ThreadCapabilities,
  ThreadStartResponse,
} from "@evener/appwire-client";
import type { ResourceState } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, vi } from "vitest";
import { ClientProvider } from "../../shell/clientContext";
import { navigate } from "../../shell/routing";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore, resetConnectionStoreForTests } from "../../stores/connection";
import { resetCredentialsStoreForTests, resetHostInstancesForTests } from "../../stores/credentials";
import { resetExtensionsStoreForTests } from "../../stores/extensions";
import { hostsStore } from "../../stores/hosts";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { enterText } from "../../textEntryTestUtils";
import { Toast } from "../../widgets";
import { resetToastStoreForTests } from "../../widgets/toast/store";
import Spawn from "./Spawn";
import { resetSpawnDraftsForTests, spawnDraftsStore } from "./spawnDrafts";

export function modelListRequests(fake: FakeClient): ModelListParams[] {
  return fake.calls.filter((call) => call.method === "model/list").map((call) => call.params as ModelListParams);
}

// connectStore wires `client` as the app's connected client, as the shell does:
// the launch-config store behind the path and resolve reads speaks through it,
// and so do the global stores that fetch on connect. Tests counting this pane's
// own requests opt out.
export function renderSpawn(client: FakeClient, focused = true, { connectStore = true } = {}) {
  if (connectStore) act(() => connectionStore.getState().connect(client));
  return render(
    <ClientProvider client={client}>
      <Spawn params={{}} paneId="spawn-1" focused={focused} />
      <Toast />
    </ClientProvider>,
  );
}

// Seeds the navigation manifest's launch sources, which the host picker reads
// through selectSources. Rendered only when the list holds a non-local source.
export function seedSources(
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
export const LAST_WORKING_DIR_KEY = "evener-hub.spawn-defaults.global.last-working-dir";

export function workingDir(): HTMLElement {
  return screen.getByLabelText(/^Working directory:/, { selector: "#spawn-cwd" });
}

// The DESKTOP Model control lives in the prompt card's own control row (the
// session composer's ModelSwitchTrigger, every width): a plain button, not a
// labelable control, so it is found by its "— change model" accessible-name
// suffix. Advanced options can carry a second such button, so the lookup stays
// scoped to the card.
export function modelTrigger(): HTMLElement {
  return within(screen.getByTestId("spawn-controls")).getByRole("button", { name: /change model/i });
}

/** The trigger's value hook inside the card's control row. */
export function modelValue(): HTMLElement {
  return screen.getByTestId("spawn-model-value");
}

/** The quiet effort control in the card's control row (StatusRow's overlay-select recipe). */
export function effortControl(): HTMLElement {
  return screen.getByLabelText("Prompt reasoning effort");
}

/** The trigger's rendered path. It also carries a chevron and a screen-reader
 * hint, so the value is matched inside the text rather than compared whole. */
/** What the card's model trigger stands for: "required" (the hub has no
 * default and the user must choose), "chosen", "resolved-default" (the
 * resolved default model), or "default" (before a resolve lands). */
export function modelState(): string | undefined {
  return screen.getByTestId("spawn-model-slot").dataset.state;
}

export function expectModelReadsDefault(): void {
  expect(["default", "resolved-default"]).toContain(modelState());
}

export function expectResolvedDefaultModel(model: string): void {
  expect(modelState()).toBe("resolved-default");
  expect(modelValue().textContent).toContain(model);
}

export async function waitForPluginPreviewState(state: string): Promise<void> {
  await waitFor(() => expect(screen.getByTestId("spawn-plugin-summary").dataset.state).toBe(state));
}

export function expectWorkingDir(path: string): void {
  expect(workingDir().textContent).toContain(path);
}

export async function setWorkingDir(user: ReturnType<typeof userEvent.setup>, path: string): Promise<void> {
  await user.click(workingDir());
  const input = await screen.findByRole("textbox", { name: "Path" });
  await user.clear(input);
  await enterText(user, input, path);
  await user.type(input, "{Enter}");
  const confirm = screen.getByRole("button", { name: "Use this folder" });
  await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
  await user.click(confirm);
}

/** Enters prompt text as one paste instead of key by key: each prompt
 * keystroke re-renders the whole pane. Tests about per-keystroke prompt
 * behavior (the slash menu) type with `user.type` instead. */
export async function fillPrompt(user: ReturnType<typeof userEvent.setup>, text: string): Promise<void> {
  await enterText(user, promptField(), text);
}

// The pane debounces its catalog, slash-catalog and plugin-preview loads by
// 250ms. Fake timers own that clock, and the stubbed `jest` global lets Testing
// Library's findBy/waitFor polls advance it, so each debounce costs a poll
// rather than a quarter second of real time.
// vi.dynamicImportSettled() is unaffected: Vitest waits on its own saved real
// timers (getSafeTimers), not the faked globals, so the lazy chunk loads these
// tests await still settle.
export function setupUser() {
  return userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
}

/** Lets the hub answers the pane is waiting on land inside act: the mount-time
 * catalogs, or whatever a host, directory or route change just requested. The
 * fake client answers on microtasks, and an async act keeps collecting React
 * work until a real macrotask passes with none left. Testing Library cannot do
 * this for us here: under these fake timers its post-wait "drain" is a
 * synchronous fake-clock tick, so answers still in flight when a findBy or
 * waitFor returns would commit outside act. Call it straight after a
 * synchronous action (render, fireEvent, a sync act) so no await sits between
 * the action and the flush. Debounced loads still need their clock advanced
 * first. The Advanced-options toggle is the sentinel that the form rendered;
 * it renders unconditionally. */
export async function settled(): Promise<void> {
  await act(async () => {});
  screen.getByRole("button", { name: "Advanced options" });
}

export async function visitSpawnURL(url: string): Promise<void> {
  await act(async () => {
    window.history.pushState({}, "", url);
    window.dispatchEvent(new PopStateEvent("popstate"));
  });
}

export function completionDraft(cwd: string) {
  const draft = spawnDraftsStore.getState().drafts.get(cwd);
  if (!draft) throw new Error(`missing draft ${cwd}`);
  return draft;
}

export async function completionNavigate(url: string): Promise<void> {
  await act(async () => navigate(url));
}

export const SPAWN_PLUGIN_PREVIEW: PluginPreviewResponse = {
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

export async function openDesktopPluginSelection(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await waitFor(() => expect(screen.getByTestId("spawn-plugin-disclosure")).toBeTruthy());
  const disclosure = screen.getByTestId("spawn-plugin-disclosure") as HTMLDetailsElement;
  const summary = disclosure.querySelector("summary");
  if (!summary) throw new Error("the plugin disclosure has no summary");
  if (!disclosure.open) await user.click(summary);
}

export function effortSelect(): HTMLSelectElement {
  return screen.getByLabelText("Prompt reasoning effort") as HTMLSelectElement;
}

export function effortOptionValues(): string[] {
  return within(effortSelect())
    .getAllByRole("option")
    .map((option) => (option as HTMLOptionElement).value);
}

export async function pickModel(
  user: ReturnType<typeof userEvent.setup>,
  query: string,
  qualified: string,
): Promise<void> {
  await user.click(modelTrigger());
  const combo = await screen.findByRole("combobox", { name: "Model" });
  // The panel's input survives between opens with its last query, so a second
  // pick must clear before entering or the new query appends to the old one.
  await user.clear(combo);
  await enterText(user, combo, query);
  await user.click(await screen.findByText(qualified));
}

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

export function pastePngInto(el: HTMLElement, name = "shot.png"): void {
  const file = new File([new Uint8Array([1, 2, 3])], name, { type: "image/png" });
  const event = new Event("paste", { bubbles: true, cancelable: true });
  Object.defineProperty(event, "clipboardData", {
    value: { getData: () => "", items: [{ kind: "file", type: "image/png", getAsFile: () => file }] },
  });
  el.dispatchEvent(event);
}

// Mirrors Composer.test.tsx's own installCanvasStubs - the same
// useAttachments/reencodeToPng pipeline underlies both panes' image staging.
export function installCanvasStubs(): void {
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

// --- spawn prompt-box inline slash menu (Task 5) ---------------------------
//
// The prompt box completes the same trailing-slash token the session composer
// does, against the pre-session catalog (evener/spawn/slashCatalog) merged
// with the spawn-scoped builtins (goal, model, reasoning-effort). Key
// handling is ADAPTED for Spawn's submit model, not ported verbatim: plain
// Enter is the newline key and commits the open menu, while only
// Mod/Ctrl+Enter submits.

export function slashMenu() {
  return screen.getByTestId("composer-slash-menu");
}

export function slashOptions() {
  return within(slashMenu()).getAllByRole("option");
}

export function promptField(): HTMLDivElement {
  return screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement;
}

/** The model list the next `model/list` answer returns, or null for the default two-model list. Reset before each test by the hooks. */
export const modelListScript: { override: ModelDescriptor[] | null } = { override: null };

export const NO_CAPABILITIES: ThreadCapabilities = {
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

export function threadWithRef(ref: string): Thread {
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

export function startResponse(ref: string): ThreadStartResponse {
  return { thread: threadWithRef(ref), turn: { id: "turn_1", itemsView: "full", status: "idle" } };
}

export function routedDiscoveryDefault(method: string): HostForwardedResult {
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
        data: modelListScript.override ?? [
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

export function readyClient(configure?: (fake: FakeClient) => void): FakeClient {
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
    data: modelListScript.override ?? [
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

export function scriptModelList(models: ModelDescriptor[]): void {
  modelListScript.override = models;
}

/** Registers the lifecycle hooks every Spawn test file shares; call it once at the top level of the file. */
export function installSpawnTestHooks(): void {
  beforeAll(() => {
    installLocalStorage(new MemoryStorage());
    // The afterEach below restores /new after every test; the first test of a
    // file has no earlier test to have done that.
    window.history.pushState({}, "", "/new");
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
    modelListScript.override = null;
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    resetConnectionStoreForTests();
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
}
