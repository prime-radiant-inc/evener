import { cleanup, render, screen, waitFor } from "@testing-library/react";
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
  fake.on("serf/dirs/complete", () => ({ data: [] }));
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

test("renders the six-field launch bar", async () => {
  renderSpawn(readyClient());
  expect(await screen.findByLabelText("Harness")).toBeTruthy();
  expect(screen.getByText("Model")).toBeTruthy();
  expect(screen.getByLabelText("Reasoning effort")).toBeTruthy();
  expect(screen.getByLabelText("Working directory")).toBeTruthy();
  expect(screen.getByLabelText("Branch")).toBeTruthy();
  expect(screen.getByLabelText("Access mode")).toBeTruthy();
});

test("a full submit sends the cwd, prompt, and access-mode sandbox, then routes to /s/{ref}", async () => {
  const user = userEvent.setup();
  const fake = readyClient();
  renderSpawn(fake);
  await screen.findByLabelText("Harness");

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "do the thing");
  await user.type(screen.getByLabelText("Working directory"), "/tmp/project");
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
  localStorage.setItem("serf-hub.spawn-defaults.global.working_dir", "/saved/project");
  localStorage.setItem("serf-hub.spawn-defaults./saved/project", JSON.stringify({ access_mode: "workspace-write" }));
  renderSpawn(readyClient());

  await waitFor(() =>
    expect((screen.getByLabelText("Working directory") as HTMLInputElement).value).toBe("/saved/project"),
  );
  expect((screen.getByLabelText("Access mode") as HTMLSelectElement).value).toBe("workspace-write");
});

test("prefills the prompt and working dir from ?dir=/?prompt=", async () => {
  window.history.pushState({}, "", "/new?dir=%2Fhome%2Fme%2Fapp&prompt=fix%20it");
  renderSpawn(readyClient());

  await waitFor(() =>
    expect((screen.getByRole("textbox", { name: "Prompt" }) as HTMLTextAreaElement).value).toBe("fix it"),
  );
  expect((screen.getByLabelText("Working directory") as HTMLInputElement).value).toBe("/home/me/app");
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
  await user.type(screen.getByLabelText("Working directory"), "/tmp/new");
  await user.click(screen.getByRole("button", { name: "Spawn" }));

  await user.click(await screen.findByRole("button", { name: "Create & start" }));

  await waitFor(() =>
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/dirs/create",
      expect.objectContaining({ body: JSON.stringify({ path: "/tmp/new" }) }),
    ),
  );
  await waitFor(() => expect(fake.calls.some((c) => c.method === "thread/start")).toBe(true));
});

test("aborts with the validator message for a non-fixable working dir", async () => {
  const user = userEvent.setup();
  const fake = readyClient((f) => {
    f.on("serf/path/validate", () => ({ path: "/etc/hosts", valid: false, error: "path is not a directory" }));
  });
  renderSpawn(fake);
  await screen.findByLabelText("Harness");

  await user.type(screen.getByRole("textbox", { name: "Prompt" }), "go");
  await user.type(screen.getByLabelText("Working directory"), "/etc/hosts");
  await user.click(screen.getByRole("button", { name: "Spawn" }));

  expect(await screen.findByText("path is not a directory")).toBeTruthy();
  expect(fake.calls.some((c) => c.method === "thread/start")).toBe(false);
});

test("surfaces the discard notice when a prefilled model is no longer offered (floor §1.10)", async () => {
  localStorage.setItem("serf-hub.spawn-defaults.global.working_dir", "/p");
  localStorage.setItem("serf-hub.spawn-defaults./p", JSON.stringify({ model: "openai/gpt-4o" }));
  renderSpawn(readyClient());

  expect(await screen.findByText(/discarded last-used model openai\/gpt-4o/i)).toBeTruthy();
  // The stale blob was pruned by the sweep.
  await waitFor(() => expect(localStorage.getItem("serf-hub.spawn-defaults./p")).toBeNull());
});
