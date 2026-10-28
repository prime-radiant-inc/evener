import "../session/testing/editorGeometry";
import type {
  AnyNotification,
  HostForwardedResult,
  HostRequestParams,
  LaunchOption,
  ModelDescriptor,
  PluginPreviewResponse,
  Thread,
  ThreadCapabilities,
  ThreadStartParams,
  ThreadStartResponse,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, screen, waitFor, within } from "@testing-library/react";
import type userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { resetCredentialsStoreForTests, resetHostInstancesForTests } from "../../stores/credentials";
import { extensionsStore, resetExtensionsStoreForTests } from "../../stores/extensions";
import { hostsStore } from "../../stores/hosts";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { resetToastStoreForTests } from "../../widgets/toast/store";
import { resetSpawnDraftsForTests } from "./spawnDrafts";
import {
  completionDraft,
  completionNavigate,
  deferred,
  fillPrompt,
  openDesktopPluginSelection,
  promptField,
  renderSpawn,
  SPAWN_PLUGIN_PREVIEW,
  settled,
  setupUser,
  setWorkingDir,
  visitSpawnURL,
  waitForPluginPreviewState,
} from "./spawnTestUtils";

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
  modelListOverride = null;
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
  expect(screen.getByTestId("spawn-plugin-summary").dataset.state).toBe("loading");
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);

  cleanup();
  const errorClient = readyClient((f) => {
    f.on("evener/plugin/preview", () => {
      throw new Error("preview unavailable");
    });
  });
  renderSpawn(errorClient);
  await waitFor(() => expect(screen.getByTestId("spawn-plugin-summary").dataset.state).toBe("error"));
  // The failure says WHY, and the retry is a small inline action.
  expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("preview unavailable");
  expect(screen.getByTestId("spawn-plugin-summary").textContent).not.toContain("0 of 0");
  expect(within(screen.getByTestId("spawn-plugin-summary")).getByRole("button", { name: "Retry" })).toBeTruthy();
});

test("desktop plugin summary lists the configured plugin names", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/plugin/preview", () => SPAWN_PLUGIN_PREVIEW);
  });
  renderSpawn(fake);
  await settled();
  await setWorkingDir(user, "/tmp/project");

  await waitFor(() => expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("alpha, beta"));

  await openDesktopPluginSelection(user);
  await user.click(screen.getByRole("switch", { name: "beta" }));
  await waitFor(() => expect(screen.getByTestId("spawn-plugin-summary").textContent).not.toContain("beta"));
  expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("alpha");
});

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
    await waitForPluginPreviewState("error");
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
    if (scenario === "bare return") {
      await completionNavigate("/settings");
      await completionNavigate("/new");
    } else if (scenario === "picker return") {
      await setWorkingDir(user, "/tmp/completion-b");
      await waitForPluginPreviewState("error");
      expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
      await setWorkingDir(user, "/tmp/completion-a");
    } else if (scenario === "remount") {
      mounted.unmount();
      await completionNavigate("/settings");
      await completionNavigate("/new");
      mounted = renderSpawn(fake);
    }
    await waitForPluginPreviewState("error");
    expect(promptField().textContent).toBe("unsent-a");
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
        await waitForPluginPreviewState("error");
      }
    }
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
    await act(async () => started.resolve(startResponse("local:completion-a")));
    await waitFor(() => expect(completionDraft("/tmp/completion-a").fields.getState().busy).toBe(false));
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
    if (scenario === "loading then failed") {
      await act(async () => refresh.reject(new Error("preview unavailable")));
      await waitForPluginPreviewState("error");
    }
    expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true);
    expect(promptField().textContent).toBe("unsent-b");
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
      await waitForPluginPreviewState("error");
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
    await waitForPluginPreviewState("error");

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
  expect(screen.getByTestId("spawn-plugin-summary").textContent).toContain("alpha");

  rejectRefresh(new Error("preview unavailable"));
  await waitForPluginPreviewState("error");
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
  await waitForPluginPreviewState("error");
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

  await waitForPluginPreviewState("error");
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
  await waitForPluginPreviewState("error");

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
