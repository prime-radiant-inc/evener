import type {
  HostForwardedResult,
  HostRequestParams,
  ModelDescriptor,
  Thread,
  ThreadCapabilities,
  ThreadStartParams,
  ThreadStartResponse,
} from "@evener/appwire-client";
import { WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { resetCredentialsStoreForTests, resetHostInstancesForTests } from "../../stores/credentials";
import { resetExtensionsStoreForTests } from "../../stores/extensions";
import { hostsStore } from "../../stores/hosts";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { resetToastStoreForTests } from "../../widgets/toast/store";
import { toastKinds } from "../../widgets/toast/toastTestUtils";
import { selectEditorText } from "../session/testing/editor";
import { resetSpawnDraftsForTests, selectSpawnDirectory } from "./spawnDrafts";
import {
  deferred,
  effortControl,
  effortOptionValues,
  effortSelect,
  expectWorkingDir,
  fillPrompt,
  installCanvasStubs,
  modelListRequests,
  openDesktopPluginSelection,
  pastePngInto,
  pickModel,
  promptField,
  renderSpawn,
  SPAWN_PLUGIN_PREVIEW,
  seedSources,
  settled,
  setupUser,
  setWorkingDir,
  visitSpawnURL,
  workingDir,
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
  expect(promptField().textContent).toBe("retained-sentinel[image 1]");
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
  expect(promptField().textContent).toBe("submitted-sentinel-newer[image 2]");
  expect(screen.queryByRole("button", { name: "View submitted.png" })).toBeNull();
  expect(screen.getByRole("button", { name: "View newer.png" })).toBeTruthy();
  expect((effortControl() as HTMLSelectElement).value).toBe("high");
  await visitSpawnURL("/new?dir=/tmp/draft-b");
  expect(screen.queryByTestId("attachment-tile")).toBeNull();
  await visitSpawnURL("/new?dir=/tmp/draft-a");
  expect(screen.getByRole("button", { name: "View newer.png" })).toBeTruthy();
});

test.each([false, true])(
  "submitted launch markers preserve retained command and skill identities (remount: %s)",
  async (remount) => {
    installCanvasStubs();
    const user = setupUser();
    const cwd = "/tmp/retained-launch-selections";
    window.history.pushState({}, "", `/new?dir=${cwd}`);
    const draft = selectSpawnDirectory(cwd);
    draft.fields.setState({
      prompt: "😀 /same /same /same",
      skillNames: ["same"],
      commandNames: ["same"],
      mentions: [
        { kind: "command", name: "same", offset: 3 },
        { kind: "skill", name: "same", offset: 9 },
      ],
    });
    const started = deferred<ThreadStartResponse>();
    const fake = readyClient((f) => f.on("thread/start", () => started.promise));
    const mounted = renderSpawn(fake);
    await settled();
    let editor = promptField();
    selectEditorText(editor, 0);
    act(() => pastePngInto(editor, "before.png"));
    await screen.findByRole("button", { name: "View before.png" });
    selectEditorText(editor, editor.textContent?.length ?? 0);
    act(() => pastePngInto(editor, "after.png"));
    await screen.findByRole("button", { name: "View after.png" });
    expect(editor.textContent).toBe("[image 1]😀 /same /same /same[image 2]");
    expect(within(editor).getAllByTestId("composer-command-chip")).toHaveLength(1);
    expect(within(editor).getAllByTestId("composer-skill-chip")).toHaveLength(1);
    await user.click(screen.getByTestId("spawn-submit"));
    await waitFor(() => expect(fake.calls.filter((call) => call.method === "thread/start")).toHaveLength(1));
    if (remount) {
      mounted.unmount();
      renderSpawn(fake);
      await settled();
      editor = promptField();
    }
    selectEditorText(editor, editor.textContent?.length ?? 0);
    await user.paste(" later");
    await act(async () => started.resolve(startResponse("local:retained-launch")));

    expect.soft(editor.textContent).toBe("😀 /same /same /same later");
    expect.soft(within(editor).queryAllByTestId("composer-command-chip")).toHaveLength(1);
    expect.soft(within(editor).queryAllByTestId("composer-skill-chip")).toHaveLength(1);
    expect.soft(draft.fields.getState()).toMatchObject({
      prompt: "😀 /same /same /same later",
      skillNames: ["same"],
      commandNames: ["same"],
      mentions: [
        { kind: "command", name: "same", offset: 3 },
        { kind: "skill", name: "same", offset: 9 },
      ],
    });
    expect(screen.queryByRole("button", { name: "View before.png" })).toBeNull();
    expect(screen.queryByRole("button", { name: "View after.png" })).toBeNull();
    await visitSpawnURL(`/new?dir=${cwd}`);
    await user.click(screen.getByTestId("spawn-submit"));
    const submissions = fake.calls.filter((call) => call.method === "thread/start");
    expect(submissions).toHaveLength(2);
    expect(submissions[1]?.params).toMatchObject({
      input: [
        {
          type: "text",
          text: "😀 /same /same /same later",
          mentions: [
            { kind: "command", name: "same", offset: 3 },
            { kind: "skill", name: "same", offset: 9 },
          ],
        },
        { type: "skill", name: "same" },
        { type: "command", name: "same" },
      ],
    });
  },
);

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
    selectEditorText(promptField(), promptField().textContent?.length ?? 0);
    await user.type(promptField(), "-newer", { skipClick: true });
    await visitSpawnURL("/new?dir=/tmp/draft-b");
    await fillPrompt(user, "draft-b-sentinel");
    act(() => pastePngInto(promptField(), "b.png"));
    expect(images).toHaveLength(2);
    selectEditorText(promptField(), promptField().textContent?.length ?? 0);
    await act(async () => {
      if (outcome === "success") images[0]?.onload?.();
      else images[0]?.onerror?.();
    });
    expect(promptField().textContent).toBe("draft-b-sentinel[image 1]");
    expect(screen.getByRole("img", { name: "b.png (still processing)" })).toBeTruthy();
    if (outcome === "failure") {
      act(() => pastePngInto(promptField(), "b2.png"));
      expect(promptField().textContent).toBe("draft-b-sentinel[image 1][image 2]");
    }
    await visitSpawnURL("/new?dir=/tmp/draft-a");
    if (outcome === "success") {
      await screen.findByRole("button", { name: "View a.png" });
      expect(promptField().textContent).toBe("draft-a-sentinel[image 1]-newer");
    } else {
      expect(screen.queryByTestId("attachment-tile")).toBeNull();
      expect(promptField().textContent).toBe("draft-a-sentinel-newer");
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

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement;
  await setWorkingDir(user, "/tmp/project");
  await fillPrompt(user, "do the thing");
  await act(async () => {
    pastePngInto(prompt);
  });
  await waitFor(() => expect(prompt.textContent).toBe("do the thing[image 1]"));
  await waitFor(() => expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy());

  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));

  expect(prompt.textContent).toBe("");
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

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement;
  await fillPrompt(user, "do the thing");
  await act(async () => {
    pastePngInto(prompt);
  });
  await waitFor(() => expect(prompt.textContent).toBe("do the thing[image 1]"));
  await waitFor(() => expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy());

  await user.click(screen.getByTestId("spawn-submit"));

  await screen.findByText(/start failed/i);
  expect(prompt.textContent).toBe("do the thing[image 1]");
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

  // Addressed by testid so a still-stuck Start fails on the loader assertion
  // below with a clear message rather than on a failed query.
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(within(button).queryByRole("status")).toBeNull();
  expect(button.disabled).toBe(false);
});

// Every exit from a launch attempt hands Start back: a path that leaves the
// busy flag set strands the button disabled on a pane that stays mounted.
function expectStartUsable(): void {
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(button.disabled).toBe(false);
  expect(within(button).queryByRole("status")).toBeNull();
}

test("a rejected thread/start toasts and leaves Start usable for a retry that starts", async () => {
  const user = setupUser();
  let attempts = 0;
  const fake = readyClient((f) => {
    f.on("thread/start", () => {
      attempts += 1;
      if (attempts === 1) throw new Error("start refused");
      return startResponse("local:abc123");
    });
  });
  window.history.pushState({}, "", "/new");
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(toastKinds()).toContain("error"));
  expect(window.location.pathname).not.toBe("/s/local%3Aabc123");
  expectStartUsable();

  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  expect(attempts).toBe(2);
});

test("a failed directory creation toasts, closes the dialog and leaves Start usable", async () => {
  const user = setupUser();
  const fake = readyClient((f) => {
    f.on("evener/path/validate", () => ({
      path: "/tmp/new",
      valid: false,
      error: "stat /tmp/new: no such file or directory",
    }));
    f.on("evener/dirs/create", () => {
      throw new Error("create refused");
    });
  });
  window.history.pushState({}, "", "/new?dir=/tmp/new");
  renderSpawn(fake);
  await settled();

  await fillPrompt(user, "go");
  await user.click(screen.getByTestId("spawn-submit"));
  await user.click(await screen.findByRole("button", { name: "Create & start" }));

  await waitFor(() => expect(toastKinds()).toContain("error"));
  expect(screen.queryByRole("button", { name: "Create & start" })).toBeNull();
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expectStartUsable();
});

test("a remote /model value without a provider leaves Start usable", async () => {
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

  await waitFor(() => expect(toastKinds()).toContain("error"));
  expect(fake.calls.some((call) => call.method === "thread/start")).toBe(false);
  expectStartUsable();
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

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement;
  await act(async () => {
    pastePngInto(prompt, "shot.png");
  });
  await waitFor(() => expect(prompt.textContent).toBe("[image 1]"));

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

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement;
  await act(async () => {
    pastePngInto(prompt, "shot.png");
  });
  await waitFor(() => expect(prompt.textContent).toBe("[image 1]"));

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

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement;
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
  expect(within(button).getByRole("status").getAttribute("aria-label")).toBeTruthy();

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
  await settled();
  await waitFor(() => expectWorkingDir("/home/me/app"));
  await user.click(
    surface === "desktop"
      ? workingDir()
      : screen.getByLabelText(/^Working directory:/, { selector: "button:not(#spawn-cwd)" }),
  );
  const input = await screen.findByRole("textbox", { name: "Path" });
  await settled();
  await user.clear(input);
  await user.type(input, "/uncommitted");
  await act(async () => {
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
