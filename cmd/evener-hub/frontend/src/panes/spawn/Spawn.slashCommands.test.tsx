import "../session/testing/editorGeometry";
import type {
  HostForwardedResult,
  HostRequestParams,
  ModelDescriptor,
  ModelListResponse,
  Thread,
  ThreadCapabilities,
  ThreadStartResponse,
} from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { resetCredentialsStoreForTests, resetHostInstancesForTests } from "../../stores/credentials";
import { resetExtensionsStoreForTests } from "../../stores/extensions";
import { hostsStore } from "../../stores/hosts";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { getToasts, resetToastStoreForTests } from "../../widgets/toast/store";
import { errorToastsNaming, toastKinds } from "../../widgets/toast/toastTestUtils";
import { resetSpawnDraftsForTests } from "./spawnDrafts";
import {
  deferred,
  effortControl,
  expectResolvedDefaultModel,
  fillPrompt,
  modelListRequests,
  modelState,
  modelTrigger,
  promptField,
  renderSpawn,
  settled,
  setupUser,
  setWorkingDir,
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
  await waitFor(() => expectResolvedDefaultModel("anthropic/claude-sonnet-4-5"));

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
  await waitFor(() => expect(modelState()).toBe("required"));
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
  await waitFor(() => expectResolvedDefaultModel("anthropic/claude-sonnet-4-5"));

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
  await waitFor(() => expectResolvedDefaultModel("anthropic/claude-sonnet-4-5"));

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
  await act(async () => otherCatalog.resolve());
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
  await waitFor(() => expectResolvedDefaultModel("anthropic/claude-sonnet-4-5"));

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
  await act(async () => otherCatalog.resolve());
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

  await waitFor(() => expect(errorToastsNaming('"nope"')).toHaveLength(1));
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(fake.calls.some((c) => c.method === "thread/model/set")).toBe(false);
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(button.disabled).toBe(false);
  expect(within(button).queryByRole("status")).toBeNull();
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

  await waitFor(() => expect(modelState()).toBe("required"));
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

  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(fake.calls.some((c) => c.method === "goal/set")).toBe(false);
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(button.disabled).toBe(false);
  expect(within(button).queryByRole("status")).toBeNull();
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
  await waitFor(() => expectResolvedDefaultModel("anthropic/claude-sonnet-4-5"));

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

  await waitFor(() => expect(errorToastsNaming('"ultra"')).toHaveLength(1));
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(fake.calls.some((c) => c.method === "thread/reasoning-effort/set")).toBe(false);
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(button.disabled).toBe(false);
  expect(within(button).queryByRole("status")).toBeNull();
});

test("a bare /reasoning-effort toasts, starts nothing, applies nothing, and leaves Start usable", async () => {
  const user = setupUser();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  await user.type(promptField(), "/reasoning-effort");
  await user.keyboard("{Escape}");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(toastKinds()).toEqual(["error"]));
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(fake.calls.some((c) => c.method === "thread/reasoning-effort/set")).toBe(false);
  const button = screen.getByTestId("spawn-submit") as HTMLButtonElement;
  expect(button.disabled).toBe(false);
  expect(within(button).queryByRole("status")).toBeNull();
});
