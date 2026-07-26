import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadStartResponse } from "../../protocol/types.gen";
import { ClientProvider } from "../../shell/clientContext";
import { Toast } from "../../widgets";
import promptCardStyles from "../../widgets/promptcard/promptcard.module.css";
import textareaStyles from "../../widgets/textarea/textarea.module.css";
import Spawn from "./Spawn";

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
    serf: { ref, capabilities: NO_CAPABILITIES, queue: {} },
  };
}

function startResponse(ref: string): ThreadStartResponse {
  return { thread: threadWithRef(ref), turn: { id: "turn_1", itemsView: "full", status: "idle" } };
}

// A ready FakeClient with every mount-time catalog scripted so the form fully
// hydrates; individual tests override specific methods as needed.
function readyClient(configure?: (fake: FakeClient) => void): FakeClient {
  const fake = new FakeClient("ready");
  fake.on("serf/harnesses/list", () => ({
    data: [
      { id: "serf", label: "serf", kind: "serf" },
      { id: "codex-cli", label: "codex-cli", kind: "codex" },
    ],
  }));
  fake.on("serf/launch/schema", () => ({ options: [] }));
  fake.on("model/list", () => ({
    data: [
      { provider: "anthropic", model: "claude-sonnet-4-5" },
      { provider: "openai", model: "gpt-5" },
    ],
  }));
  fake.on("serf/projects/recent", () => ({ data: [] }));
  fake.on("serf/paths/complete", () => ({ data: [] }));
  fake.on("serf/path/validate", () => ({ path: "", valid: true }));
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

// The working directory is a PathField: the closed field is a trigger holding
// the path as text, and the value is entered inside its browse panel (which is
// portaled to document.body, so it is queried from `screen`, not the form).
const LAST_WORKING_DIR_KEY = "serf-hub.spawn-defaults.global.last-working-dir";

function workingDir(): HTMLElement {
  return screen.getByLabelText("Working directory");
}

// The Model field's closed trigger (ModelField -> ModelCatalog): a plain
// button, not a labelable control (see AdvancedOptions.tsx's own note on the
// composite-widget label pattern), so it is found by its "— change model"
// accessible-name suffix rather than by label.
function modelTrigger(): HTMLElement {
  return screen.getByRole("button", { name: /change model/i });
}

/** The trigger's rendered path. It also carries a chevron and a screen-reader
 * hint, so the value is matched inside the text rather than compared whole. */
function expectWorkingDir(path: string): void {
  expect(workingDir().textContent).toContain(path);
}

async function setWorkingDir(user: ReturnType<typeof userEvent.setup>, path: string): Promise<void> {
  await user.click(workingDir());
  // The panel opens pre-filled and fully selected, so typing replaces whatever
  // was there; Enter with no row highlighted commits the typed literal.
  await screen.findByRole("combobox", { name: "Path" });
  await user.keyboard(`${path}{Enter}`);
}

/** Waits for the mount-time catalogs to land. The Advanced-options toggle is
 * the sentinel because it renders unconditionally and is not itself one of the
 * awaited catalogs' outputs - unlike the harness select, which now lives INSIDE
 * that collapsed panel and so isn't in the tree at rest. */
async function settled(): Promise<void> {
  await screen.findByRole("button", { name: "Advanced options" });
}

let fetchMock: ReturnType<typeof vi.fn>;

beforeAll(() => {
  globalThis.localStorage = new MemoryStorage() as unknown as Storage;
});

beforeEach(() => {
  localStorage.clear();
  fetchMock = vi.fn((url: string) => {
    if (url.startsWith("/api/git/head")) {
      return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ branch: "main" }) } as Response);
    }
    if (url === "/api/dirs/create") {
      return Promise.resolve({
        ok: true,
        status: 200,
        json: () => Promise.resolve({ path: "/x", created: true }),
      } as Response);
    }
    return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) } as Response);
  });
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.pushState({}, "", "/");
});

// --- the page's shape ------------------------------------------------------
//
// Prompt card first, taking the page's slack; ONE configuration row beneath it
// (working directory, model, effort); harness in Advanced options.

test("the prompt card leads the page, ahead of every configuration field", async () => {
  renderSpawn(readyClient());
  await settled();

  const card = screen.getByTestId("spawn-prompt-card");
  const dir = screen.getByLabelText("Working directory");
  // DOCUMENT_POSITION_FOLLOWING (4): the directory field comes AFTER the card in
  // document order - see MDN's Node.compareDocumentPosition bitmask.
  expect(card.compareDocumentPosition(dir) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
});

test("the configuration row is working directory, model and effort - and nothing else", async () => {
  renderSpawn(readyClient());
  await settled();

  expect(screen.getByLabelText("Working directory")).toBeTruthy();
  expect(screen.getByText("Model")).toBeTruthy();
  expect(screen.getByLabelText("Effort")).toBeTruthy();
});

// Harness moves into Advanced options: most installs have exactly one, so a
// field whose answer is always "serf" shouldn't lead the page. It stays fully
// functional there - the switch still blanks a non-serf model (see the harness
// tests in harnessModels.test.ts for that rule's own coverage).
test("harness moved into Advanced options, and still works there", async () => {
  const user = userEvent.setup();
  renderSpawn(readyClient());
  await settled();

  expect(screen.queryByLabelText("Harness")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const harness = screen.getByLabelText("Harness") as HTMLSelectElement;
  await user.selectOptions(harness, "codex-cli");
  expect(harness.value).toBe("codex-cli");
});

// "Start", not "Spawn": spawn is implementation vocabulary, and the rail's own
// button already says "New session". The page title matches the verb.
test("the primary verb is Start, in the card's own corner, and the page is titled to match", async () => {
  renderSpawn(readyClient());
  await settled();

  const start = screen.getByTestId("spawn-submit");
  expect(start.textContent).toBe("Start");
  expect(screen.getByRole("heading", { name: "Start an agent" })).toBeTruthy();
  // Inside the card, not in a detached actions strip below it.
  expect(screen.getByTestId("spawn-prompt-card").contains(start)).toBe(true);
  expect(screen.queryByRole("button", { name: "Spawn" })).toBeNull();
});

// The dormant-start rule rides in the placeholder rather than a separate
// instruction line above the form: it is a fact about THIS field.
test("the dormant hint lives in the placeholder, not a standalone instruction line", async () => {
  renderSpawn(readyClient());
  await settled();

  const prompt = screen.getByRole("textbox", { name: "Prompt" });
  expect(prompt.getAttribute("placeholder")).toBe("What should the agent work on? Leave blank to start it dormant.");
  expect(screen.queryByText(/leave the prompt blank/i)).toBeNull();
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
  // Not a text box: it is a readout of the directory's HEAD, and the wire has
  // nowhere to send a branch anyway (startThread.ts's own branch note).
  expect(screen.queryByLabelText("Branch")).toBeNull();
  expect(screen.getByTestId("spawn-branch").querySelector("input")).toBeNull();
});

test("the branch readout is absent when the working directory has no resolvable HEAD", async () => {
  // The default fetch mock 404s everything except /api/git/head; override it so
  // HEAD resolution fails soft to "" the way branch.ts documents.
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) } as Response)),
  );
  localStorage.setItem("serf-hub.spawn-defaults.global.working_dir", "/tmp/plain");
  renderSpawn(readyClient());
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
  expect(localStorage.getItem("serf-hub.spawn-defaults.global.working_dir")).toBe("/tmp/project");
});

// A blank prompt starts a DORMANT session, exactly as the placeholder
// promises. The daemon honours it: hubThreadStart calls StartTurn only when
// len(params.Input) > 0 (cmd/serf-hub/app_threadlifecycle.go), and buildInput
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
  localStorage.setItem("serf-hub.spawn-defaults.global.working_dir", "/saved/project");
  localStorage.setItem("serf-hub.spawn-defaults./saved/project", JSON.stringify({ access_mode: "workspace-write" }));
  renderSpawn(readyClient());

  await waitFor(() => expectWorkingDir("/saved/project"));
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  expect((screen.getByLabelText("Access mode") as HTMLSelectElement).value).toBe("workspace-write");
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

// Spec 3.7: browsing writes the working-directory field continuously, so the
// last-used-directory global is stamped once when the panel closes rather than
// on every browse step.
test("stamps the last-working-directory global when the browse panel closes", async () => {
  const user = userEvent.setup();
  renderSpawn(readyClient((f) => f.on("serf/paths/complete", () => ({ data: ["/tmp/project/src"] }))));
  await settled();

  await user.click(workingDir());
  await screen.findByRole("combobox", { name: "Path" });
  // Browsing alone must not stamp - only the close does.
  await user.click(await screen.findByText("src"));
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBeNull();

  await user.keyboard("{Escape}");
  await waitFor(() => expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBe("/tmp/project/src"));
});

// kata cp3m: Escape closes the working-directory browse popover only - the
// popover's own Escape handler (widgets/popover) both preventDefault()s and
// stopPropagation()s (verified live: a document/window-level listener never
// even sees the keydown), so it must never reach a form-level or route-level
// handler and discard the draft or navigate the pane away. Live reproduction
// against a real build (headless AND headed Chrome, mouse-click and
// keyboard-commit directory selection, with and without a model chosen
// first) did not reproduce the kata's reported discard; this regression test
// locks the correct behavior in place either way - prompt, model, and
// working directory all survive Escape, and no navigation occurs.
test("kata cp3m: Escape after selecting a working directory closes only the popover - the draft survives", async () => {
  const user = userEvent.setup();
  const fake = readyClient((f) => f.on("serf/paths/complete", () => ({ data: ["/tmp/project/src"] })));
  renderSpawn(fake);
  await settled();

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "my important draft text");
  await user.click(workingDir());
  await screen.findByRole("combobox", { name: "Path" });
  await user.click(await screen.findByText("src"));
  expectWorkingDir("/tmp/project/src");

  const pathnameBeforeEscape = window.location.pathname;
  await user.keyboard("{Escape}");

  // The popover is gone...
  await waitFor(() => expect(screen.queryByRole("combobox", { name: "Path" })).toBeNull());
  // ...but nothing else moved: no submit was attempted, no navigation
  // happened, and every field the user had already filled in is untouched.
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(window.location.pathname).toBe(pathnameBeforeEscape);
  expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe(
    "my important draft text",
  );
  expectWorkingDir("/tmp/project/src");
});

// The read side of that same global (spec 3.4): with no ?dir= prefill and no
// per-project blob the field is empty, and the panel opens on the last
// directory a session was launched in rather than on $HOME.
test("the browse panel opens on the stamped last-working-directory global", async () => {
  const user = userEvent.setup();
  localStorage.setItem(LAST_WORKING_DIR_KEY, "/home/me/lastone");
  const complete = vi.fn((_params: { prefix: string }) => ({ data: ["/home/me/lastone/src"] }));
  renderSpawn(readyClient((f) => f.on("serf/paths/complete", complete)));
  await settled();
  // Nothing else may have seeded the field: the fallback is only consulted
  // when the value is empty, and an empty field shows its placeholder.
  expectWorkingDir("Working directory");

  await user.click(workingDir());
  await screen.findByRole("combobox", { name: "Path" });

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
      f.on("serf/projects/recent", () => nulled);
      f.on("serf/paths/complete", () => nulled);
    }),
  );
  await settled();

  await user.click(workingDir());

  // The panel is up, listing nothing, rather than having thrown its tree away.
  expect(await screen.findByRole("combobox", { name: "Path" })).toBeTruthy();
  expect(await screen.findByText("Nothing here.")).toBeTruthy();
});

test("offers to create a missing directory, then creates it and spawns", async () => {
  const user = userEvent.setup();
  const fake = readyClient((f) => {
    f.on("serf/path/validate", () => ({
      path: "/tmp/new",
      valid: false,
      error: "stat /tmp/new: no such file or directory",
    }));
  });
  renderSpawn(fake);
  await settled();

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "go");
  await setWorkingDir(user, "/tmp/new");
  await user.click(screen.getByTestId("spawn-submit"));

  await user.click(await screen.findByRole("button", { name: "Create & start" }));

  await waitFor(() =>
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/dirs/create",
      expect.objectContaining({ body: JSON.stringify({ path: "/tmp/new" }) }),
    ),
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
    f.on("serf/path/validate", () =>
      dirIsValid
        ? { path: "/tmp/project", valid: true }
        : { path: "/etc/hosts", valid: false, error: "path is not a directory" },
    );
  });
  renderSpawn(fake);
  await settled();

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "go");
  await setWorkingDir(user, "/etc/hosts");
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

// --- kata xgk8: Model's "(default)" must not claim an answer the daemon --
// --- will refuse -----------------------------------------------------------
//
// The daemon's thread/start resolves Model from the SAME layered launch
// config serf/launch/resolve previews (app_threadlifecycle.go: overrides.Model
// wins when set, otherwise the resolved Effective.Model - empty is refused
// with "model is required"). Leaving Model untouched sends no model
// override, so an empty resolve preview means the daemon WILL refuse the
// submit - "(default)" next to a working Effort default is a lie in that
// state. The preview is fail-open (an unmocked/failing resolve never blocks
// Start) - only a CONFIRMED empty default does.

test("Model keeps reading '(default)' and Start stays untouched when the hub resolves a real default (kata xgk8, happy path)", async () => {
  const user = userEvent.setup();
  const fake = readyClient((f) => {
    f.on("serf/launch/resolve", () => ({
      effective: { model: "anthropic/claude-sonnet-4-5" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "serf/launch/resolve")).toBe(true));

  expect(modelTrigger().textContent).toContain("(default)");
  expect(screen.queryByRole("alert")).toBeNull();
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
  await user.click(screen.getByTestId("spawn-submit"));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));
});

test("kata xgk8: Model reads as required (not '(default)') and Start is disabled when the hub has no default model", async () => {
  const user = userEvent.setup();
  const fake = readyClient((f) => {
    f.on("serf/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "serf/launch/resolve")).toBe(true));

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
    f.on("serf/launch/resolve", () => ({ effective: {}, layers: {}, provenance: {} }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "serf/launch/resolve")).toBe(true));
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
// inside Advanced options (perLaunchSerfOptions - schema.go's real "model"
// LaunchOption, kind modelPicker) alongside the top-level Model chip; floor
// §1.11 has the Advanced field's override win at submit time. The preview
// here must agree - an override set ONLY through Advanced options satisfies
// the requirement without the top-level chip ever leaving "(default)".
test("kata xgk8: an Advanced-options model override satisfies the requirement without touching the top-level Model field", async () => {
  const user = userEvent.setup();
  const fake = readyClient((f) => {
    f.on("serf/launch/schema", () => ({
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
    f.on("serf/launch/resolve", (params) => ({
      effective: { model: params.launchOverrides?.model ?? "" },
      layers: {},
      provenance: {},
    }));
  });
  renderSpawn(fake);
  await settled();

  await setWorkingDir(user, "/tmp/project");
  await waitFor(() => expect(fake.calls.some((c) => c.method === "serf/launch/resolve")).toBe(true));
  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(true));

  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  const modelPickers = screen.getAllByRole("button", { name: /change model/i });
  // [0] is the top-level chip; the Advanced-panel one is whichever else opens
  // a picker - pick the last one added (Advanced options render after it).
  await user.click(modelPickers[modelPickers.length - 1]!);
  const combo = await screen.findByRole("combobox", { name: "Model" });
  await user.type(combo, "gpt-5");
  await user.click(await screen.findByText("openai/gpt-5"));

  await waitFor(() => expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false));
  expect(screen.getAllByRole("button", { name: /change model/i })[0]?.textContent).toContain("(default)"); // top-level chip untouched
});

test("surfaces the discard notice when a prefilled model is no longer offered (floor §1.10)", async () => {
  localStorage.setItem("serf-hub.spawn-defaults.global.working_dir", "/p");
  localStorage.setItem("serf-hub.spawn-defaults./p", JSON.stringify({ model: "openai/gpt-4o" }));
  renderSpawn(readyClient());

  expect(await screen.findByText(/discarded last-used model openai\/gpt-4o/i)).toBeTruthy();
  // The stale blob was pruned by the sweep.
  await waitFor(() => expect(localStorage.getItem("serf-hub.spawn-defaults./p")).toBeNull());
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

  await screen.findByText(/spawn failed/i);
  expect(prompt.value).toBe("do the thing[image 1]");
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
  // handleSpawn's catch already resets busy on a thrown startThread (same
  // class of bug, verified already-fixed here - the button must stay usable
  // so the user can retry without reloading).
  expect((screen.getByTestId("spawn-submit") as HTMLButtonElement).disabled).toBe(false);
});

test("re-enables the Start button after a successful start (post-success state hygiene, same class as §1.14)", async () => {
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

// kata 61v2: three fast clicks on Start spawned three separate live daemons
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
