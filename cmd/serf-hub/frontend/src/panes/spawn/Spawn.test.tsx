import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadStartResponse } from "../../protocol/types.gen";
import { ClientProvider } from "../../shell/clientContext";
import { Toast } from "../../widgets";
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

test("renders the five-field launch bar", async () => {
  renderSpawn(readyClient());
  expect(await screen.findByLabelText("Harness")).toBeTruthy();
  expect(screen.getByText("Model")).toBeTruthy();
  expect(screen.getByLabelText("Reasoning effort")).toBeTruthy();
  expect(screen.getByLabelText("Working directory")).toBeTruthy();
  expect(screen.getByLabelText("Branch")).toBeTruthy();
});

// The icon controls draw real SVG glyphs rather than bare "+"/"×" characters,
// matching the composer's own attach control. Their spoken names come from
// IconButton's label either way.
test("the attach control draws an SVG glyph, not a literal text character", async () => {
  renderSpawn(readyClient());
  await screen.findByLabelText("Harness");

  const attach = screen.getByRole("button", { name: "Attach image" });
  expect(attach.querySelector("svg")).toBeTruthy();
  expect(attach.textContent).toBe("");
});

test("Access mode moved from the top-level bar into Advanced options (9ct0)", async () => {
  const user = userEvent.setup();
  renderSpawn(readyClient());
  await screen.findByLabelText("Harness");

  expect(screen.queryByLabelText("Access mode")).toBeNull();
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  expect(screen.getByLabelText("Access mode")).toBeTruthy();
});

test("a full submit sends the cwd, prompt, and access-mode sandbox, then routes to /s/{ref}", async () => {
  const user = userEvent.setup();
  const fake = readyClient();
  renderSpawn(fake);
  await screen.findByLabelText("Harness");

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
  await setWorkingDir(user, "/tmp/project");
  await user.click(screen.getByRole("button", { name: "Advanced options" }));
  await user.selectOptions(screen.getByLabelText("Access mode"), "Read-only");
  await user.click(screen.getByRole("button", { name: "Spawn" }));

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

test("blocks an empty prompt with no attachment", async () => {
  const user = userEvent.setup();
  const fake = readyClient();
  renderSpawn(fake);
  await screen.findByLabelText("Harness");

  await user.click(screen.getByRole("button", { name: "Spawn" }));

  expect(await screen.findByText(/prompt is empty/i)).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  expect(window.location.pathname).toBe("/");
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
  await screen.findByLabelText("Harness");

  await user.click(workingDir());
  await screen.findByRole("combobox", { name: "Path" });
  // Browsing alone must not stamp - only the close does.
  await user.click(await screen.findByText("src"));
  expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBeNull();

  await user.keyboard("{Escape}");
  await waitFor(() => expect(localStorage.getItem(LAST_WORKING_DIR_KEY)).toBe("/tmp/project/src"));
});

// The read side of that same global (spec 3.4): with no ?dir= prefill and no
// per-project blob the field is empty, and the panel opens on the last
// directory a session was launched in rather than on $HOME.
test("the browse panel opens on the stamped last-working-directory global", async () => {
  const user = userEvent.setup();
  localStorage.setItem(LAST_WORKING_DIR_KEY, "/home/me/lastone");
  const complete = vi.fn((_params: { prefix: string }) => ({ data: ["/home/me/lastone/src"] }));
  renderSpawn(readyClient((f) => f.on("serf/paths/complete", complete)));
  await screen.findByLabelText("Harness");
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
  await screen.findByLabelText("Harness");

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
  await screen.findByLabelText("Harness");

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "go");
  await setWorkingDir(user, "/tmp/new");
  await user.click(screen.getByRole("button", { name: "Spawn" }));

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
  await waitFor(() =>
    expect((screen.getByRole("button", { name: "Spawn" }) as HTMLButtonElement).disabled).toBe(false),
  );
});

test("aborts with the validator message for a non-fixable working dir", async () => {
  const user = userEvent.setup();
  const fake = readyClient((f) => {
    f.on("serf/path/validate", () => ({ path: "/etc/hosts", valid: false, error: "path is not a directory" }));
  });
  renderSpawn(fake);
  await screen.findByLabelText("Harness");

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "go");
  await setWorkingDir(user, "/etc/hosts");
  await user.click(screen.getByRole("button", { name: "Spawn" }));

  expect(await screen.findByText("path is not a directory")).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
  // Failure paths already reset busy (verified, unchanged by this fix) - the
  // button must stay usable so the user can correct the path and retry.
  expect((screen.getByRole("button", { name: "Spawn" }) as HTMLButtonElement).disabled).toBe(false);
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
  await screen.findByLabelText("Harness");

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
  await setWorkingDir(user, "/tmp/project");
  await user.type(prompt, "do the thing");
  pastePngInto(prompt);
  await waitFor(() => expect(prompt.value).toBe("do the thing[image 1]"));
  await waitFor(() => expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy());

  await user.click(screen.getByRole("button", { name: "Spawn" }));

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
  await screen.findByLabelText("Harness");

  const prompt = screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement;
  await user.type(prompt, "do the thing");
  pastePngInto(prompt);
  await waitFor(() => expect(prompt.value).toBe("do the thing[image 1]"));
  await waitFor(() => expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy());

  await user.click(screen.getByRole("button", { name: "Spawn" }));

  await screen.findByText(/spawn failed/i);
  expect(prompt.value).toBe("do the thing[image 1]");
  expect(screen.getByRole("button", { name: /remove/i })).toBeTruthy();
  // handleSpawn's catch already resets busy on a thrown startThread (same
  // class of bug, verified already-fixed here - the button must stay usable
  // so the user can retry without reloading).
  expect((screen.getByRole("button", { name: "Spawn" }) as HTMLButtonElement).disabled).toBe(false);
});

test("re-enables the Spawn button after a successful spawn (post-success state hygiene, same class as §1.14)", async () => {
  const user = userEvent.setup();
  const fake = readyClient();
  renderSpawn(fake);
  await screen.findByLabelText("Harness");

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
  await user.click(screen.getByRole("button", { name: "Spawn" }));

  await waitFor(() => expect(window.location.pathname).toBe("/s/local%3Aabc123"));

  // Queried by a name that matches either label so a still-stuck "Spawning…"
  // fails on the assertions below with a clear message, not a failed query.
  const button = screen.getByRole("button", { name: /^spawn/i }) as HTMLButtonElement;
  expect(button.textContent).toBe("Spawn");
  expect(button.disabled).toBe(false);
});
