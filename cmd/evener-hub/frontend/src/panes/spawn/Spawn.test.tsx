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
  LaunchOption,
  ModelDescriptor,
  ModelListResponse,
  PluginPreviewResponse,
  Thread,
  ThreadCapabilities,
  ThreadStartParams,
  ThreadStartResponse,
} from "../../protocol/types.gen";
import { ClientProvider } from "../../shell/clientContext";
import { connectionStore } from "../../stores/connection";
import { credentialsStore, resetCredentialsStoreForTests } from "../../stores/credentials";
import { extensionsStore, resetExtensionsStoreForTests } from "../../stores/extensions";
import { Toast } from "../../widgets";
import promptCardStyles from "../../widgets/promptcard/promptcard.module.css";
import textareaStyles from "../../widgets/textarea/textarea.module.css";
import { resetToastStoreForTests } from "../../widgets/toast/store";
import Welcome from "../welcome/Welcome";
import Spawn from "./Spawn";

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
  fake.on("thread/start", () => startResponse("local:abc123"));
  configure?.(fake);
  return fake;
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

beforeAll(() => {
  globalThis.localStorage = new MemoryStorage() as unknown as Storage;
});

beforeEach(() => {
  localStorage.clear();
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
  await user.click(connect);
  await screen.findByRole("dialog");
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
  await user.click(await screen.findByRole("button", { name: "Connect provider" }));
  const testConnection = await screen.findByRole("button", { name: "Test connection" });
  available = true;
  await user.click(testConnection);
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
  vi.unstubAllGlobals();
  window.history.pushState({}, "", "/");
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
    f.on(
      "model/list",
      () =>
        new Promise<ModelListResponse>((done) => {
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
  expect(fake.calls.filter((call) => call.method === "model/list")).toHaveLength(1);

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

  await user.click(await screen.findByText("openai/gpt-5"));
  await waitFor(() =>
    expect(effortOptionValues()).toEqual(["", "minimal", "low", "medium", "high", "xhigh", "max", "none"]),
  );
  expect(fake.calls.filter((call) => call.method === "model/list")).toHaveLength(1);
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
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "model/list")).toHaveLength(1));

  modelListOverride = [
    { provider: "anthropic", model: "claude-sonnet-4-5", displayName: "anthropic/claude-sonnet-4-5" },
    { provider: "google-vertex", model: "gemini-3.8-flash", displayName: "google-vertex/gemini-3.8-flash" },
  ];
  act(() => fake.emitNotification({ method: "evener/auth/updated", params: { provider: "google-vertex" } }));
  await waitFor(() => expect(fake.calls.filter((call) => call.method === "model/list")).toHaveLength(2));

  await user.click(modelTrigger());
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.clear(combo);
  await user.type(combo, "gemini");
  await screen.findByText("google-vertex/gemini-3.8-flash");
  // The picker shares the reloaded promise rather than issuing a third call.
  expect(fake.calls.filter((call) => call.method === "model/list")).toHaveLength(2);
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

test("resets the prompt and attachments after a successful spawn, but keeps sticky defaults (floor §1.14 L186)", async () => {
  installCanvasStubs();
  const user = userEvent.setup();
  const fake = readyClient();
  renderSpawn(fake);
  await settled();

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
  await setWorkingDir(user, "/tmp/project");
  await user.type(prompt, "do the thing");
  pastePngInto(prompt);
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
  pastePngInto(prompt);
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
  pastePngInto(prompt, "shot.png");
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
  pastePngInto(prompt, "shot.png");
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
  act(() => {
    pastePngInto(prompt, "shot.png");
  });
  const removeButton = screen.getByRole("button", { name: "Remove shot.png" });
  removeButton.focus();
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
