import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { WireError } from "../../protocol/errors";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type {
  AnyNotification,
  InstanceListResponse,
  LaunchConfigResolved,
  LaunchOption,
  ModelDescriptor,
  ModelListParams,
  ModelListResponse,
  PluginPreviewResponse,
  Thread,
  ThreadCapabilities,
  ThreadStartParams,
  ThreadStartResponse,
} from "../../protocol/types.gen";
import { ClientProvider } from "../../shell/clientContext";
import { navigate } from "../../shell/routing";
import { connectionStore } from "../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../stores/credentials";
import { extensionsStore, resetExtensionsStoreForTests } from "../../stores/extensions";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { Toast } from "../../widgets";
import promptCardStyles from "../../widgets/promptcard/promptcard.module.css";
import textareaStyles from "../../widgets/textarea/textarea.module.css";
import { getToasts, resetToastStoreForTests } from "../../widgets/toast/store";
import Welcome from "../welcome/Welcome";
import Spawn from "./Spawn";
import { resetSpawnDraftsForTests, spawnDraftsStore } from "./spawnDrafts";

let modelListOverride: ModelDescriptor[] | null = null;

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
  configure?.(fake);
  return fake;
}

function modelListRequests(fake: FakeClient): ModelListParams[] {
  return fake.calls.filter((call) => call.method === "model/list").map((call) => call.params as ModelListParams);
}

function renderSpawn(client: FakeClient) {
  return render(
    <ClientProvider client={client}>
      <Spawn params={{}} paneId="spawn-1" focused={true} />
      <Toast />
    </ClientProvider>,
  );
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
  const user = userEvent.setup();
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
  await user.type(input, `${path}{Enter}`);
  const confirm = screen.getByRole("button", { name: "Use this folder" });
  await waitFor(() => expect((confirm as HTMLButtonElement).disabled).toBe(false));
  await user.click(confirm);
}

/** Waits for the mount-time catalogs to land. The Advanced-options toggle is
 * the sentinel because it renders unconditionally and is not itself one of the
 * awaited catalogs' outputs - unlike the harness select, which now lives INSIDE
 * that collapsed panel and so isn't in the tree at rest. */
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const client = readyClient();
  const mounted = renderSpawn(client);
  await settled();
  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "draft-a-sentinel");
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  renderSpawn(readyClient());
  await settled();
  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "draft-a-sentinel");
  fireEvent.change(effortControl(), { target: { value: "high" } });
  await visitSpawnURL("/settings?dir=/tmp/foreign&prompt=foreign");
  expectWorkingDir("/tmp/draft-a");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("draft-a-sentinel");
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("");
  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "draft-b-sentinel");
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("draft-a-sentinel");
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
});

test("re-entering the same /new URL after leaving /new re-applies its explicit prefill", async () => {
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=/tmp/reentry-a");
  const client = readyClient();
  const mounted = renderSpawn(client);
  await settled();
  await user.type(promptField(), "sentinel-a");
  // The picker switches drafts without touching the URL, so the prefill
  // marker still names this same URL.
  await setWorkingDir(user, "/tmp/reentry-b");
  await user.type(promptField(), "sentinel-b");
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=/tmp/reentry-a");
  const client = readyClient();
  const mounted = renderSpawn(client);
  await settled();
  await setWorkingDir(user, "/tmp/reentry-b");
  await user.type(promptField(), "sentinel-b");
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
    const user = userEvent.setup();
    window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
    const started = deferred<ThreadStartResponse>();
    const fake = readyClient((f) => f.on("thread/start", () => started.promise));
    const mounted = renderSpawn(fake);
    await user.type(promptField(), "submitted-a");
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
    const origin = completionDraft("/tmp/completion-a");
    if (scenario.startsWith("picker")) {
      await setWorkingDir(user, "/tmp/completion-b");
      await user.type(promptField(), "unsent-b");
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const startA = deferred<ThreadStartResponse>();
  const startB = deferred<ThreadStartResponse>();
  const fake = readyClient((f) =>
    f.on("thread/start", ({ cwd }) => (cwd === "/tmp/completion-a" ? startA.promise : startB.promise)),
  );
  renderSpawn(fake);
  await user.type(promptField(), "submitted-a");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  await setWorkingDir(user, "/tmp/completion-b");
  await user.type(promptField(), "submitted-b");
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
    const user = userEvent.setup();
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
    await user.type(promptField(), "submitted-a");
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const validation = deferred<{ path: string; valid: boolean }>();
  const fake = readyClient((f) => f.on("evener/path/validate", () => validation.promise));
  renderSpawn(fake);
  await user.type(promptField(), "submitted-a");
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=/tmp/completion-a");
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => f.on("thread/start", () => started.promise));
  renderSpawn(fake);
  await user.type(promptField(), "submitted-a");
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
    const user = userEvent.setup();
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
    const user = userEvent.setup();
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
    const user = userEvent.setup();
    window.history.pushState({}, "", "/new?dir=/tmp/review-a");
    const fake = readyClient();
    renderSpawn(fake);
    await user.type(promptField(), "normalized-draft");
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new");
  const fake = readyClient();
  const mounted = renderSpawn(fake);
  await user.type(promptField(), "unscoped-sentinel");
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
  await user.type(promptField(), "draft-b-sentinel");
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=/tmp/review-a");
  const fake = readyClient();
  const mounted = renderSpawn(fake);
  await setWorkingDir(user, "/tmp/review-b");
  await user.type(promptField(), "picker-draft");
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
    const user = userEvent.setup();
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const validation = deferred<{ path: string; valid: boolean }>();
  const fake = readyClient((f) => f.on("evener/path/validate", () => validation.promise));
  renderSpawn(fake);
  await user.type(promptField(), "submitted-sentinel");
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const validation = deferred<{ path: string; valid: boolean }>();
  const fake = readyClient((f) => f.on("evener/path/validate", () => validation.promise));
  renderSpawn(fake);
  await user.type(promptField(), "draft-a-sentinel");
  await user.click(screen.getByTestId("spawn-submit"));
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  await user.type(promptField(), "draft-b-sentinel");
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => f.on("thread/start", () => started.promise));
  const mounted = renderSpawn(fake);
  await user.type(promptField(), "submitted-sentinel");
  act(() => pastePngInto(promptField(), "submitted.png"));
  await screen.findByRole("button", { name: "View submitted.png" });
  fireEvent.change(effortControl(), { target: { value: "high" } });
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
  mounted.unmount();
  renderSpawn(fake);
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  await user.type(promptField(), "other-project-sentinel");
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
  const user = userEvent.setup();
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
  globalThis.localStorage = new MemoryStorage() as unknown as Storage;
});

beforeEach(() => {
  localStorage.clear();
  resetSpawnDraftsForTests();
  resetCredentialsStoreForTests();
  modelListOverride = null;
});

test("missing credentials surface setup in the composer without opening a dialog or losing its draft", async () => {
  const user = userEvent.setup();
  const client = readyClient((fake) => {
    fake.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  });
  connectionStore.getState().connect(client);
  renderSpawn(client);
  const connect = await screen.findByRole("button", { name: "Connect provider" });
  expect(screen.queryByRole("dialog")).toBeNull();
  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "draft-sentinel");
  await setWorkingDir(user, "/tmp/my-project");
  expect((screen.getByRole("button", { name: "Start" }) as HTMLButtonElement).disabled).toBe(true);
  await act(async () => {
    fireEvent.click(connect);
    await vi.dynamicImportSettled();
  });
  expect(screen.getByRole("dialog")).toBeTruthy();
  await user.keyboard("{Escape}");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("draft-sentinel");
  expectWorkingDir("/tmp/my-project");
});

test("retrying missing provider setup discovers a local server started afterward", async () => {
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const connectProvider = await screen.findByRole("button", { name: "Connect provider" });
  // Finish the lazy dialog's mount and catalog refresh before retaining a button
  // reference: the refresh replaces the initially cached instance rows.
  await user.click(connectProvider);
  await act(async () => {
    await vi.dynamicImportSettled();
  });
  const testConnection = await screen.findByRole("button", { name: "Test connection" });
  available = true;
  await user.click(testConnection);
  expect(client.calls.filter((call) => call.method === "evener/auth/test")).toEqual([
    { method: "evener/auth/test", params: { provider: "ollama" } },
  ]);
  await waitFor(() => expect(screen.queryByRole("button", { name: "Connect provider" })).toBeNull());
  await user.click(modelTrigger());
  expect(await screen.findByRole("option", { name: /local-model/ })).toBeTruthy();
});

test("credential changes reload the cached model catalog and re-enter setup after removal", async () => {
  const user = userEvent.setup();
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
  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "draft-sentinel");
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
  connectionStore.setState({ state: "idle", serverInfo: undefined, client: null });
  resetExtensionsStoreForTests();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
  renderSpawn(readyClient());
  await settled();

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "typed mobile work");

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
// tests in harnessModels.test.ts for that rule's own coverage).
test("harness moved into Advanced options, and still works there", async () => {
  const user = userEvent.setup();
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
  const user = userEvent.setup();
  renderSpawn(readyClient());
  await settled();

  expect(screen.queryByLabelText("Access mode")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  expect(screen.getByLabelText("Access mode")).toBeTruthy();
});

test("a full submit sends the cwd, prompt, and access-mode sandbox, then routes to /s/{ref}", async () => {
  const user = userEvent.setup();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
    const user = userEvent.setup();
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
    await user.type(promptField(), "unsent-a");
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
    const user = userEvent.setup();
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
    await user.type(promptField(), "submitted-a");
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
    if (scenario === "newer origin selection") await user.click(screen.getByRole("button", { name: "None" }));
    await setWorkingDir(user, "/tmp/completion-b");
    await user.type(promptField(), "unsent-b");
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
    const user = userEvent.setup();
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
    const user = userEvent.setup();
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
    const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "   ");
  await setWorkingDir(user, "/tmp/project");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ input: [] });
});

test("loads sticky defaults from localStorage on mount", async () => {
  const user = userEvent.setup();
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=%2Fhome%2Fme%2Fapp");
  renderSpawn(readyClient());
  await waitFor(() => expectWorkingDir("/home/me/app"));
  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "typed by hand");

  act(() => {
    window.history.pushState({}, "", "/new");
    window.dispatchEvent(new PopStateEvent("popstate"));
  });

  expectWorkingDir("/home/me/app");
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("typed by hand");
});

test("only confirming a directory updates the launch defaults", async () => {
  const user = userEvent.setup();
  renderSpawn(readyClient((f) => f.on("evener/paths/complete", () => ({ data: ["/tmp/project/src"] }))));
  await settled();
  await user.click(workingDir());
  await user.click(await screen.findByRole("button", { name: "Open /tmp/project/src" }));
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBeNull();
  await user.click(screen.getByRole("button", { name: "Use this folder" }));
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBe("/tmp/project/src");
});

test("Escape discards directory browsing while preserving the prompt and launch directory", async () => {
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=%2Ftmp%2Fproject");
  const fake = readyClient((f) => f.on("evener/paths/complete", () => ({ data: ["/tmp/project/src"] })));
  renderSpawn(fake);
  await settled();
  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "my important draft text");
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "go");
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
  const user = userEvent.setup();
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

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "go");
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
  const user = userEvent.setup();
  const fake = readyClient((f) => {
    // path: "" is the real wire shape too - ValidateLaunchPath's early
    // return leaves the Go struct's Path field at its zero value.
    f.on("evener/path/validate", () => ({ path: "", valid: false, error: "path is required" }));
  });
  renderSpawn(fake);
  await settled();

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "say hello");
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
  const user = userEvent.setup();
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

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
});

test("kata xgk8: Model reads as required (not '(default)') and Spawn is disabled when the hub has no default model", async () => {
  const user = userEvent.setup();
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
  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
  await user.keyboard("{Meta>}{Enter}{/Meta}");
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
});

test("kata xgk8: choosing a model clears the required state and lets Start proceed", async () => {
  const user = userEvent.setup();
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

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ model: "anthropic/claude-sonnet-4-5" });
});

test("keeps the form usable and leaves Model at '(default)' when no provider is credentialed at all", async () => {
  const user = userEvent.setup();
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
  notified = true;
  await act(async () => client.emitNotification({ method: "evener/auth/updated", params: {} }));
  await act(async () => refreshStarted.promise);
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
  // pick must clear before typing or the new query appends to the old one.
  await user.clear(combo);
  await user.type(combo, query);
  await user.click(await screen.findByText(qualified));
}

test("the Effort select offers the selected model's own ladder and re-derives it on a model switch", async () => {
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  await user.type(promptField(), "retained-sentinel");
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
  const user = userEvent.setup();
  window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
  const started = deferred<ThreadStartResponse>();
  const fake = readyClient((f) => f.on("thread/start", () => started.promise));
  const mounted = renderSpawn(fake);
  await user.type(promptField(), "submitted-sentinel");
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
    const user = userEvent.setup();
    window.history.pushState({}, "", "/new?dir=/tmp/draft-a");
    const fake = readyClient();
    const mounted = renderSpawn(fake);
    await user.type(promptField(), "draft-a-sentinel");
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
    await user.type(promptField(), "draft-b-sentinel");
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
  const user = userEvent.setup();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
  await setWorkingDir(user, "/tmp/project");
  await user.type(prompt, "do the thing");
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
  const user = userEvent.setup();
  const fake = readyClient((f) => {
    f.on("thread/start", () => {
      throw new Error("boom");
    });
  });
  renderSpawn(fake);
  await settled();

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
  await user.type(prompt, "do the thing");
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
  const user = userEvent.setup();
  const fake = readyClient((f) => {
    f.on("thread/start", () => {
      throw new WireError("evener launch-check timed out", -32014, { evenerErrorInfo: "hubLaunch" });
    });
  });
  renderSpawn(fake);
  await settled();

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
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
  const user = userEvent.setup();
  const fake = readyClient((f) => {
    f.on("thread/start", () => {
      throw new Error('AppwireClient: cannot call "thread/start" while state is "closed"');
    });
  });
  renderSpawn(fake);
  await settled();

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await screen.findByText("Start failed: Can't reach the hub right now.");
  expect(screen.queryByText(/AppwireClient/i)).toBeNull();
});

test("re-enables the Spawn button after a successful start (post-success state hygiene, same class as §1.14)", async () => {
  const user = userEvent.setup();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
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
  const user = userEvent.setup();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");

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
  const user = userEvent.setup();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "first session");
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.filter((c) => c.method === "thread/start")).toHaveLength(1));

  // The Spawn pane is a dockview singleton that can stay mounted behind the
  // session pane doSpawn navigates to (see doSpawn's own comment on the
  // sticky-defaults reset) - a second Start on the SAME mounted instance must
  // not be permanently blocked by the first success's guard release.
  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "second session");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(fake.calls.filter((c) => c.method === "thread/start")).toHaveLength(2));
});

// FIX 2a: the busy "Spawning…" state gets the Loader widget (widgets/loader)
// instead of static text - a genuinely indeterminate, user-initiated wait is
// exactly what Loader exists for.
test("shows a Loader, not static text, while the spawn request is in flight", async () => {
  const user = userEvent.setup();
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

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
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
  const user = userEvent.setup();
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

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "go");
  await user.click(screen.getByRole("button", { name: "Start" }));
  await waitFor(() => expect(started).toBeDefined());
  expect(started?.reasoningEffort ?? "").toBe(displayed);
});

// The model catalog follows the committed directory, not the picker's draft.
test("typing a working directory reloads the model catalog only after confirmation", async () => {
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});

test("a same-cwd catalog refresh hides stale rows until the new response lands", async () => {
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(fake.calls.some((call) => call.method === "evener/spawn/slashCatalog")).toBe(false);
});

test("the open spawn menu wires listbox roles and aria-activedescendant on the prompt", async () => {
  const user = userEvent.setup();
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

  await user.keyboard("{Escape}");
  expect(promptField().getAttribute("aria-activedescendant")).toBeNull();
});

// --- Task 6: submit interception for pre-session builtins --------------------
//
// A prompt parsing as /goal, /model, or /reasoning-effort starts the session
// with the literal text, then applies the builtin against the new ref, then
// navigates. Everything else spawns exactly as today.

test("a /goal prompt starts a dormant session and applies goal/set on the new ref", async () => {
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
        await new Promise((resolve) => setTimeout(resolve, 5000));
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

  await user.type(promptField(), "/model openai/gpt-5");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(screen.getByText(/\/model: unknown value "openai\/gpt-5"/)).toBeTruthy());
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
});

test("a /reasoning-effort value from the previous cwd does not validate after switching directories", async () => {
  const user = userEvent.setup();
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
        await new Promise((resolve) => setTimeout(resolve, 5000));
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

  await user.type(promptField(), "/reasoning-effort high");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(screen.getByText(/\/reasoning-effort: unknown value "high"/)).toBeTruthy());
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
});

test("a model picked after a failed background load validates for /model", async () => {
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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

test("a bare /goal toasts, starts nothing, and leaves Start usable", async () => {
  const user = userEvent.setup();
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
  const user = userEvent.setup();
  const fake = readyClient((f) => {
    f.on("goal/set", () => ({ started: true }));
  });
  connectionStore.getState().connect(fake);
  renderSpawn(fake);
  await settled();

  await user.type(promptField(), "hello world");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  const start = fake.calls.find((c) => c.method === "thread/start");
  expect(start?.params).toMatchObject({ input: [{ type: "text", text: "hello world" }] });
  expect(fake.calls.some((c) => c.method === "goal/set")).toBe(false);
});

test("a /goal prompt on a non-evener harness spawns verbatim with no goal/set call", async () => {
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
  const user = userEvent.setup();
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
