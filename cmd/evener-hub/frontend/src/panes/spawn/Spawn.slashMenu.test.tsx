import type {
  AnyNotification,
  HostForwardedResult,
  HostRequestParams,
  ModelDescriptor,
  Thread,
  ThreadCapabilities,
  ThreadStartResponse,
} from "@evener/appwire-client";
import { SCOPED_HOST_READ_DEBOUNCE_MS, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import type userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { installLocalStorage, MemoryStorage } from "../../storageTestUtils";
import { connectionStore } from "../../stores/connection";
import { resetCredentialsStoreForTests, resetHostInstancesForTests } from "../../stores/credentials";
import { resetExtensionsStoreForTests } from "../../stores/extensions";
import { hostsStore } from "../../stores/hosts";
import { resetNavigationStoreForTests } from "../../stores/navigation/store";
import { resetThreadsStoreForTests } from "../../stores/threads";
import { getToasts, resetToastStoreForTests } from "../../widgets/toast/store";
import { editorCursor } from "../session/testing/editor";
import { resetSpawnDraftsForTests } from "./spawnDrafts";
import { promptField, renderSpawn, settled, setupUser, slashMenu, slashOptions, visitSpawnURL } from "./spawnTestUtils";

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

test("launcher command and skill selections with identical spelling survive failed start and project remount", async () => {
  const user = setupUser();
  window.history.pushState({}, "", "/new?dir=/tmp/atomic-a");
  const fake = readyClient((client) => {
    client.on("evener/spawn/slashCatalog", () => ({
      commands: [{ name: "same", description: "command intent", source: "user" }],
      skills: [
        {
          name: "same",
          description: "skill intent",
          disableModelInvocation: false,
          userInvocable: true,
          available: true,
        },
      ],
    }));
    client.on("thread/start", () => {
      throw new WireError("start refused", -32602);
    });
  });
  const mounted = renderSpawn(fake);
  await settled();
  await typeSlashQuery(user, fake, "Use /sa");
  await user.click(slashOptions().find((option) => option.textContent?.includes("command intent"))!);
  await user.keyboard("and /sa");
  await user.click(slashOptions().find((option) => option.textContent?.includes("skill intent"))!);
  await user.paste("literal /same");
  expect(promptField().textContent).toBe("Use /same and /same literal /same");
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  expect(getToasts()).toEqual(
    expect.arrayContaining([expect.objectContaining({ text: "Start failed: start refused" })]),
  );
  expect(promptField().querySelectorAll("[data-command-name]")).toHaveLength(1);
  expect(promptField().querySelectorAll("[data-skill-name]")).toHaveLength(1);
  await visitSpawnURL("/new?dir=/tmp/atomic-b");
  expect(promptField().textContent).toBe("");
  mounted.unmount();
  renderSpawn(fake);
  await visitSpawnURL("/new?dir=/tmp/atomic-a");
  expect(promptField().textContent).toBe("Use /same and /same literal /same");
  expect(promptField().querySelectorAll("[data-command-name]")).toHaveLength(1);
  expect(promptField().querySelectorAll("[data-skill-name]")).toHaveLength(1);
  fake.on("thread/start", () => startResponse("local:abc123"));
  await user.click(screen.getByTestId("spawn-submit"));
  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
  expect(fake.calls.filter((call) => call.method === "thread/start").at(-1)?.params).toMatchObject({
    input: [
      { type: "text", text: "Use /same and /same literal /same" },
      { type: "skill", name: "same" },
      { type: "command", name: "same" },
    ],
  });
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

  expect((promptField() as HTMLDivElement).textContent).toBe("/review ");
  expect(editorCursor(promptField())).toBe("/review ".length);
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
  expect(document.activeElement).toBe(promptField());

  // Plain Enter is Spawn's newline key: with the menu open it commits the
  // highlighted item instead of submitting.
  await user.clear(promptField());
  await user.type(promptField(), "/rev");
  await user.keyboard("{Enter}");

  expect((promptField() as HTMLDivElement).textContent).toBe("/review ");
  expect(fake.calls.some((call) => call.method === "thread/start")).toBe(false);

  // Mod+Enter ALWAYS submits, even with the menu open: commit nothing.
  await user.clear(promptField());
  await user.type(promptField(), "/rev");
  expect(screen.queryByTestId("composer-slash-menu")).not.toBeNull();
  await user.keyboard("{Meta>}{Enter}{/Meta}");

  await waitFor(() => expect(fake.calls.some((call) => call.method === "thread/start")).toBe(true));
  // The menu committed nothing: a successful Start clears the prompt (Spawn's
  // own doSpawn reset), so the field must NOT hold the committed "/review ".
  expect((promptField() as HTMLDivElement).textContent).not.toBe("/review ");
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
  expect((promptField() as HTMLDivElement).textContent).toBe("");
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
  expect((promptField() as HTMLDivElement).textContent).toBe("/re");
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
  expect((promptField() as HTMLDivElement).textContent).toBe("/reasoning-effort ");
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
  renderSpawn(fake, true, { connectStore: false });

  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLDivElement).textContent).toBe("/rev"),
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
  expect((promptField() as HTMLDivElement).textContent).toBe("/re");

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
    await vi.advanceTimersByTimeAsync(SCOPED_HOST_READ_DEBOUNCE_MS + 1);
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

  expect((promptField() as HTMLDivElement).textContent).toBe("/review ");
  // The option's onMouseDown preventDefault keeps focus in the field, so the
  // click's onSelect commits rather than racing the blur-close.
  expect(document.activeElement).toBe(promptField());
  expect(screen.queryByTestId("composer-slash-menu")).toBeNull();
});
