import { once } from "node:events";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Thread, ThreadModel, ThreadReadResponse } from "@evener/appwire-client";
import { bindArtifactPath, bindFilePath, DOC_FILE_MAX_BYTES } from "@evener/appwire-client/docContent";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { DockviewApi } from "dockview-core";
import { lazy, StrictMode } from "react";
import { afterAll, afterEach, beforeEach, expect, test, vi } from "vitest";
import { registerPaneForTests } from "../../shell/paneRegistry";
import { PaneVisibilityContext } from "../../shell/paneVisibility";
import {
  documentPaneState,
  registerDockviewApi,
  resetWorkspaceStoreForTests,
  useWorkspaceStore,
  workspaceStore,
} from "../../shell/workspace";
import { connectionStore, resetConnectionStoreForTests } from "../../stores/connection";
import { resetThreadsStoreForTests, threadsStore } from "../../stores/threads";
import { readModuleCss } from "../../styles/cssBlock";
import DocPane from "./DocPane";
import docpaneStyles from "./docpane.module.css";
import { type DocParams, openDocBeside } from "./openDoc";
import "./index";

const ref = "remote:034MXwo6BpPH0QQCgdICSf";
function thread(cwd = "/work/a"): Thread {
  return {
    id: "thr_owner",
    sessionId: "sess_owner",
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd,
    cliVersion: "1.0.0",
    source: "evener",
    evener: {
      ref,
      capabilities: {
        send: true,
        steer: true,
        interrupt: true,
        compact: true,
        clear: true,
        forkFromTurn: true,
        shutdown: true,
        changeModel: true,
        changeVisionModel: true,
        queue: true,
        goal: true,
        sharedNotes: true,
        rename: true,
      },
      queue: { revision: 0 },
    },
  };
}
let cwd: string;
let client: FakeClient;
let requests: URL[];
let answer: (request: IncomingMessage, response: ServerResponse) => void;
let closeHTTP: () => Promise<void>;
const nativeFetch = globalThis.fetch;
let images: HTMLImageElement[];

async function listenHTTP(server: Server): Promise<string> {
  for (let attempt = 0; attempt < 64; attempt += 1) {
    server.listen(0, "127.0.0.1");
    await once(server, "listening");
    const address = server.address();
    if (!address || typeof address === "string") throw new Error("expected TCP fixture");
    // Installed Node 22 Fetch's highest forbidden port is 10080. The sandbox
    // can allocate these as ephemeral ports, so use the safe range above it.
    if (address.port > 10080) return `http://127.0.0.1:${address.port}`;
    await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
  }
  throw new Error("could not obtain a Fetch-safe HTTP fixture port in 64 attempts");
}

afterAll(
  registerPaneForTests({
    id: "session",
    title: () => "test session",
    component: lazy(() => Promise.resolve({ default: () => null })),
  }),
);
beforeEach(async () => {
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  resetWorkspaceStoreForTests();
  cwd = "/work/a";
  requests = [];
  images = [];
  client = new FakeClient("ready");
  client.on("thread/read", (params) => ({ thread: thread(cwd), requestGeneration: params.requestGeneration }));
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  answer = (_request, response) => {
    response.setHeader("Content-Type", "text/plain");
    response.end("healthy bytes");
  };
  const server = createServer((request, response) => {
    // Do not leave native fetch pooled sockets alive across fixture retirement.
    response.setHeader("Connection", "close");
    requests.push(new URL(request.url ?? "", "http://fixture"));
    answer(request, response);
  });
  const origin = await listenHTTP(server);
  vi.stubGlobal("fetch", (url: string, options?: RequestInit) => nativeFetch(new URL(url, origin), options));
  vi.stubGlobal("Image", function imageRequest() {
    const image = document.createElement("img");
    images.push(image);
    return image;
  });
  closeHTTP = async () => {
    server.closeAllConnections();
    await new Promise<void>((resolve, reject) => server.close((error) => (error ? reject(error) : resolve())));
  };
});
afterEach(async () => {
  cleanup();
  resetConnectionStoreForTests();
  resetThreadsStoreForTests();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  registerDockviewApi(null);
  await closeHTTP();
});
function respond(body: string | Uint8Array, status = 200, headers: Record<string, string> = {}) {
  answer = (_request, response) => {
    response.writeHead(status, { "Content-Type": "text/plain", ...headers });
    response.end(body);
  };
}
function renderDoc(path: string, kind: "file" | "image" = "file", retained = true) {
  const sourcePaneId = workspaceStore.getState().openPane("session", { ref });
  if (retained) {
    const reference = bindFilePath(path, cwd);
    if (!reference) throw new Error("expected reference");
    openDocBeside({ session: ref, sourcePaneId, reference });
  } else workspaceStore.getState().openPane("doc", { session: ref, path, kind });
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.type === "doc");
  if (!pane) throw new Error("expected real document pane");
  const view = render(
    <PaneVisibilityContext value={true}>
      <DocPane params={pane.params as DocParams} paneId={pane.id} focused={false} />
    </PaneVisibilityContext>,
  );
  return { ...view, pane, sourcePaneId };
}
async function publishCWD(next: string) {
  cwd = next;
  await act(async () => {
    client.emitNotification({ method: "evener/thread/resync", params: { ref, threadId: "thr_owner" } });
  });
  await waitFor(() => expect(threadsStore.getState().threads.get(ref)?.cwd).toBe(next));
}

test("the doc pane's markdown body text reads at the prose step, pinned on the pane's own host element", () => {
  const css = readModuleCss(import.meta.url, "docpane.module.css");
  expect(css.match(/--prose-font-size/g)).toHaveLength(1);
  expect(css).toMatch(/\.markdown\s*\{[^}]*--prose-font-size:\s*var\(--font-size-prose\);/);
});
test("a text file renders as a copyable code block, with normalized title and an absolute requested target", async () => {
  respond("plain body line");
  renderDoc("./src//notes.txt");
  expect((await screen.findByText("plain body line")).closest("pre")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Copy" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "notes.txt" })).toBeTruthy();
  expect(requests[0]?.searchParams.get("path")).toBe("/work/a/src/notes.txt");
  expect(requests[0]?.searchParams.get("session")).toBe(ref);
});
test("a long text file shows every line, never folded to its tail", async () => {
  // Well past CodeBlock's fold threshold, so a fold would hide the head.
  respond(Array.from({ length: 250 }, (_, i) => `line ${i + 1}`).join("\n"));
  renderDoc("src/long.txt");
  expect(await screen.findByText("line 1")).toBeTruthy();
  expect(screen.getByText("line 250")).toBeTruthy();
  expect(screen.queryByRole("button", { name: /earlier line/ })).toBeNull();
});
test("a markdown file uses the sanitizing Markdown widget and prose host", async () => {
  respond("# Heading\n\nbody");
  renderDoc("docs/README.md");
  const heading = await screen.findByRole("heading", { name: "Heading" });
  expect(heading.tagName).toBe("H1");
  expect(heading.closest(`.${docpaneStyles.markdown}`)).toBeTruthy();
});
test("raw HTML is neutralized", async () => {
  respond("before <img src=x onerror=alert(1)> after");
  const { container } = renderDoc("docs/EVIL.md");
  await screen.findByText(/before/);
  expect(container.querySelector("img")).toBeNull();
});
test("binary file preserves filename and human size notice", async () => {
  respond(new Uint8Array(2048), 200, { "Content-Type": "application/octet-stream" });
  renderDoc("out/blob.bin");
  expect(await screen.findByText("Binary file not shown")).toBeTruthy();
  expect(screen.getByText(/blob\.bin \(2 KiB\)/)).toBeTruthy();
});
test("truncated file preserves the 512 KiB cap, true total and partial content", async () => {
  respond(`partial head${" ".repeat(DOC_FILE_MAX_BYTES - 12)}`, 200, {
    "X-Doc-Truncated": "true",
    "X-Doc-Total-Size": `${2 * 1024 * 1024}`,
  });
  renderDoc("logs/huge.log");
  expect(await screen.findByText("partial head")).toBeTruthy();
  expect(screen.getByText("Truncated")).toBeTruthy();
  expect(screen.getByText("Showing the first 512 KiB of 2 MiB.")).toBeTruthy();
});
test("untruncated file shows no truncation notice", async () => {
  respond("small");
  renderDoc("small.txt");
  await screen.findByText("small");
  expect(screen.queryByText(/truncated/i)).toBeNull();
});
test.each([
  [404, "File not available"],
  [403, "Access denied"],
  [501, "Open it on the host"],
  [503, "Couldn't load file"],
] as const)("HTTP %i renders its actual classified error", async (status, title) => {
  respond("unavailable", status);
  renderDoc("plan.md");
  expect(await screen.findByText(title)).toBeTruthy();
});
test("loading placeholder while HTTP response is deferred", () => {
  answer = () => {};
  renderDoc("slow.txt");
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
});
test("restored document hydrates its exact serialized session, not focused navigation, and releases its lease", async () => {
  let hydrate: ((response: ThreadReadResponse) => void) | undefined;
  const readResponse = (requestGeneration?: number): ThreadReadResponse => ({
    thread: thread(),
    bootGeneration: "1",
    epoch: 1,
    snapshot: { incarnation: "inc-1", length: 0 },
    requestGeneration,
  });
  client.on("thread/read", async (params) => ({
    ...(await new Promise<ThreadReadResponse>((resolve) => {
      hydrate = resolve;
    })),
    requestGeneration: params.requestGeneration,
  }));
  const serialized = JSON.stringify([
    { id: "p1", params: { paneType: "session", paneParams: { ref: "local:unrelated" } } },
    { id: "p2", params: { paneType: "doc", paneParams: { session: ref, path: "plan.md", kind: "file" } } },
  ]);
  // Only the dockview serialization boundary is doubled. restoreLayout,
  // retained pane state, session hydration and HTTP reads remain production.
  const host = {
    panels: [] as Array<{ id: string; params: unknown }>,
    activePanel: { id: "p1" },
    fromJSON(value: string) {
      this.panels = JSON.parse(value);
    },
    clear() {
      this.panels = [];
    },
    removePanel() {},
  };
  registerDockviewApi(host as unknown as DockviewApi);
  expect(workspaceStore.getState().restoreLayout(serialized)).toBe(true);
  registerDockviewApi(null);
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.id === "p2");
  if (!pane) throw new Error("missing restored doc");
  expect(documentPaneState(pane)).toBeUndefined();
  const view = render(<DocPane params={pane.params as DocParams} paneId={pane.id} focused={false} />);
  await waitFor(() => expect(hydrate).toBeTypeOf("function"));
  expect(requests).toHaveLength(0);
  await act(async () => hydrate?.(readResponse()));
  await screen.findByText("healthy bytes");
  expect(
    client.calls.filter((call) => call.method === "thread/read").map((call) => (call.params as { ref: string }).ref),
  ).toEqual([ref]);
  expect(requests[0]?.searchParams.get("path")).toBe("/work/a/plan.md");
  expect(documentPaneState(pane)?.reference.cwd).toBe("/work/a");
  // Retirement captures one final versioned read before releasing the lease.
  client.on("thread/read", ({ requestGeneration }) => readResponse(requestGeneration));
  view.unmount();
  await waitFor(() => expect(client.calls.some((call) => call.method === "thread/unsubscribe")).toBe(true));
  expect(threadsStore.getState().threads.has(ref)).toBe(false);
  expect(
    client.calls.filter((call) => call.method === "thread/read").map((call) => (call.params as { ref: string }).ref),
  ).toEqual([ref, ref]);
});
test.each(["plan.md", "/work/a/plan.md"])(
  "owning cwd publication clears %s immediately, B failure and late A cannot publish, explicit B reopen recovers",
  async (path) => {
    let aReads = 0;
    let lateA: (() => void) | undefined;
    answer = (request, response) => {
      const target = new URL(request.url ?? "", "http://fixture").searchParams.get("path");
      response.setHeader("Content-Type", "text/plain");
      if (target === "/work/a/plan.md" && cwd === "/work/a") {
        aReads += 1;
        if (aReads === 1) response.end("A bytes");
        else lateA = () => response.end("late A bytes");
      } else {
        response.statusCode = target === "/work/a/plan.md" ? 403 : 503;
        response.end("unavailable");
      }
    };
    const view = renderDoc(path);
    await screen.findByText("A bytes");
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    await waitFor(() => expect(lateA).toBeTypeOf("function"));
    await publishCWD("/work/b");
    expect(screen.queryByText("A bytes")).toBeNull();
    expect(documentPaneState(view.pane)?.reference).toMatchObject({
      cwd: "/work/b",
      readTarget: path.startsWith("/") ? "/work/a/plan.md" : "/work/b/plan.md",
    });
    await act(async () => lateA?.());
    await screen.findByText(path.startsWith("/") ? "Access denied" : "Couldn't load file");
    expect(screen.queryByText("late A bytes")).toBeNull();
    respond("B bytes");
    const reference = bindFilePath("plan.md", "/work/b");
    if (!reference) throw new Error("expected B");
    act(() => openDocBeside({ session: ref, sourcePaneId: view.sourcePaneId, reference }));
    expect(await screen.findByText("B bytes")).toBeTruthy();
    expect(workspaceStore.getState().panes.filter((pane) => pane.type === "doc")).toHaveLength(1);
  },
);
test("actual Reload recovers a created missing file without remounting", async () => {
  const directory = await mkdtemp(join(tmpdir(), "evener-doc-created-"));
  const file = join(directory, "created.txt");
  cwd = directory;
  answer = async (request, response) => {
    const target = new URL(request.url ?? "", "http://fixture").searchParams.get("path");
    response.setHeader("Content-Type", "text/plain");
    if (target !== file) {
      response.statusCode = 403;
      response.end("unexpected target");
      return;
    }
    try {
      response.end(await readFile(file, "utf8"));
    } catch (error) {
      response.statusCode =
        typeof error === "object" && error !== null && "code" in error && error.code === "ENOENT" ? 404 : 500;
      response.end("file unavailable");
    }
  };
  const view = renderDoc("created.txt");
  try {
    await screen.findByText("File not available");
    await writeFile(file, "created bytes", "utf8");
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    expect(await screen.findByText("created bytes")).toBeTruthy();
    expect(requests.map((request) => request.searchParams.get("path"))).toEqual([file, file]);
  } finally {
    view.unmount();
    await rm(directory, { recursive: true, force: true });
  }
});
test("same-identity failed Reload retains healthy bytes and actual error explanation", async () => {
  renderDoc("plan.md");
  await screen.findByText("healthy bytes");
  respond("attachment unavailable", 503);
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await screen.findByText(/readDocFile: error \(status 503\)/);
  expect(screen.getByText("healthy bytes")).toBeTruthy();
});
async function loadImage() {
  await waitFor(() => expect(images.length).toBeGreaterThan(0));
  await act(async () => fireEvent.load(images.at(-1) as HTMLImageElement));
}
test("image authenticated generation request, filename, no raw read and zoom behavior", async () => {
  renderDoc("out/pic.png", "image");
  await loadImage();
  const image = screen.getByTestId("doc-image");
  const url = new URL(image.getAttribute("src") ?? "", "http://fixture");
  expect(url.pathname).toBe("/doc/image");
  expect(url.searchParams.get("session")).toBe(ref);
  expect(url.searchParams.get("path")).toBe("/work/a/out/pic.png");
  expect(url.searchParams.get("read")).toBeTruthy();
  expect(image.getAttribute("alt")).toBe("pic.png");
  expect(requests).toHaveLength(0);
  expect(screen.getByRole("button", { name: "Zoom image" })).toBeTruthy();
  const user = userEvent.setup();
  expect(screen.queryByRole("dialog")).toBeNull();
  await user.click(image);
  expect(screen.getByRole("dialog")).toBeTruthy();
  expect(screen.getByTestId("doc-lightbox-img").getAttribute("src")).toBe(image.getAttribute("src"));
});
test("failed image shows an unavailable notice", async () => {
  renderDoc("out/missing.png", "image");
  await waitFor(() => expect(images.length).toBe(1));
  await act(async () => fireEvent.error(images[0] as HTMLImageElement));
  expect(screen.getByText("Image not available")).toBeTruthy();
});

test.each(["main", "secondary"] as const)(
  "visible %s document reads independently of focus, hiding pauses and close releases ownership",
  async (slot) => {
    const sourcePaneId = workspaceStore.getState().openPane("session", { ref });
    const reference = bindFilePath("plan.md", cwd);
    if (!reference) throw new Error("expected reference");
    openDocBeside({ session: ref, sourcePaneId, reference });
    const pane = workspaceStore.getState().panes.find((candidate) => candidate.type === "doc");
    if (!pane) throw new Error("expected doc");
    if (slot === "main") workspaceStore.getState().promotePane(pane.id);
    workspaceStore.getState().focusPane(sourcePaneId);
    const paneId = pane.id;
    function Mounted({ visible }: { visible: boolean }) {
      const record = useWorkspaceStore((state) => state.panes.find((candidate) => candidate.id === paneId));
      return (
        record && (
          <PaneVisibilityContext value={visible}>
            <DocPane params={record.params as DocParams} paneId={record.id} focused={false} />
          </PaneVisibilityContext>
        )
      );
    }
    const view = render(
      <StrictMode>
        <Mounted visible={false} />
      </StrictMode>,
    );
    await waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(true));
    expect(requests).toHaveLength(0);
    view.rerender(
      <StrictMode>
        <Mounted visible />
      </StrictMode>,
    );
    await screen.findByText("healthy bytes");
    expect(requests).toHaveLength(1);
    view.rerender(
      <StrictMode>
        <Mounted visible={false} />
      </StrictMode>,
    );
    respond("current bytes");
    fireEvent.click(screen.getByRole("button", { name: "Reload" }));
    expect(requests).toHaveLength(1);
    view.rerender(
      <StrictMode>
        <Mounted visible />
      </StrictMode>,
    );
    await screen.findByText("current bytes");
    expect(requests).toHaveLength(2);
    act(() => workspaceStore.getState().closePane(pane.id));
    await waitFor(() => expect(threadsStore.getState().threads.has(ref)).toBe(false));
    expect(screen.queryByText("current bytes")).toBeNull();
  },
);

test("image reopen and Reload retain healthy image until load, cwd replacement clears it and retired load cannot publish", async () => {
  const view = renderDoc("pic.png", "image");
  await loadImage();
  const first = screen.getByTestId("doc-image").getAttribute("src");
  const reference = bindFilePath("pic.png", cwd);
  if (!reference) throw new Error("expected image reference");
  act(() => openDocBeside({ session: ref, sourcePaneId: view.sourcePaneId, reference }));
  await waitFor(() => expect(images).toHaveLength(2));
  expect(screen.getByTestId("doc-image").getAttribute("src")).toBe(first);
  await act(async () => fireEvent.error(images[1] as HTMLImageElement));
  expect(screen.getByTestId("doc-image").getAttribute("src")).toBe(first);
  expect(screen.getByRole("status").textContent).toMatch(/Image/);
  fireEvent.click(screen.getByRole("button", { name: "Reload" }));
  await waitFor(() => expect(images).toHaveLength(3));
  await publishCWD("/work/b");
  expect(screen.queryByTestId("doc-image")).toBeNull();
  await act(async () => fireEvent.load(images[2] as HTMLImageElement));
  await waitFor(() => expect(images).toHaveLength(4));
  expect(screen.queryByTestId("doc-image")).toBeNull();
  const replacement = images[3] as HTMLImageElement;
  expect(new URL(replacement.src).searchParams.get("path")).toBe("/work/b/pic.png");
  await act(async () => fireEvent.load(replacement));
  expect(screen.getByTestId("doc-image").getAttribute("src")).not.toBe(first);
});

test("unbound restored read waits for ready before one owning lease and releases it on close", async () => {
  client = new FakeClient("connecting");
  client.on("thread/read", (params) => ({ thread: thread(), requestGeneration: params.requestGeneration }));
  client.on("thread/unsubscribe", () => ({}));
  connectionStore.getState().connect(client);
  const view = renderDoc("plan.md", "file", false);
  expect(client.calls).toHaveLength(0);
  expect(requests).toHaveLength(0);
  act(() => client.emitStateChange("ready"));
  await screen.findByText("healthy bytes");
  const admitted = client.calls.filter((call) => call.method === "thread/read");
  expect(admitted.length).toBeGreaterThan(0);
  expect(new Set(admitted.map((call) => (call.params as { ref: string }).ref))).toEqual(new Set([ref]));
  act(() => client.emitStateChange("ready"));
  expect(client.calls.filter((call) => call.method === "thread/read")).toHaveLength(admitted.length);
  view.unmount();
  await waitFor(() => expect(client.calls.filter((call) => call.method === "thread/unsubscribe")).toHaveLength(1));
  expect(threadsStore.getState().threads.has(ref)).toBe(false);
});

// A workspace restored from a saved layout rebuilds a document pane from its
// params alone, with no in-memory reference. An artifacts document must still
// name the file it read: its path relative to the artifacts root, plus the
// root it came from, and the pane rebinds against the directory the thread
// snapshot advertises.
test("a restored artifacts document reads the same retained file again", async () => {
  const artifactsDir = "/state/projects/example/sessions/sess_owner/artifacts";
  client.on("thread/read", (params) => ({
    thread: { ...thread(cwd), evener: { ...thread(cwd).evener, artifactsDir } },
    requestGeneration: params.requestGeneration,
  }));
  respond("retained report body");
  const relativePath = "docs-quality-repairs.md";
  workspaceStore.getState().openPane("doc", {
    session: ref,
    path: relativePath,
    root: "artifacts",
    artifactTarget: `${artifactsDir}/${relativePath}`,
    kind: "file",
  });
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.type === "doc");
  if (!pane) throw new Error("expected a document pane");
  render(
    <PaneVisibilityContext value={true}>
      <DocPane params={pane.params as DocParams} paneId={pane.id} focused={false} />
    </PaneVisibilityContext>,
  );
  await screen.findByText("retained report body");
  const read = requests.map((url) => url.searchParams.get("path"));
  expect(read).toEqual([`${artifactsDir}/${relativePath}`]);
});

// A saved artifacts pane records the concrete file it opened. A stable ref can
// outlive the session it named (Clear moves the ref to a new session), and
// that session may hold a different report of the same name. Restoring must
// read the recorded target or refuse, never silently show the replacement.
test("a restored artifacts document does not adopt a replacement session's same-named file", async () => {
  const oldDir = "/state/projects/example/sessions/sess_owner/artifacts";
  const newDir = "/state/projects/example/sessions/sess_replacement/artifacts";
  client.on("thread/read", (params) => ({
    thread: { ...thread(cwd), evener: { ...thread(cwd).evener, artifactsDir: newDir } },
    requestGeneration: params.requestGeneration,
  }));
  respond("replacement report body");
  workspaceStore.getState().openPane("doc", {
    session: ref,
    path: "report.md",
    root: "artifacts",
    artifactTarget: `${oldDir}/report.md`,
    kind: "file",
  });
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.type === "doc");
  if (!pane) throw new Error("expected a document pane");
  render(
    <PaneVisibilityContext value={true}>
      <DocPane params={pane.params as DocParams} paneId={pane.id} focused={false} />
    </PaneVisibilityContext>,
  );
  // The pane must settle on the structural unavailable state rather than sit on
  // a skeleton: the snapshot has arrived and does not contain the target. The
  // assertion is the marker, not the copy.
  await waitFor(() => expect(screen.getByTestId("empty-state").getAttribute("data-reason")).toBe("unavailable"));
  expect(requests.map((url) => url.searchParams.get("path"))).not.toContain(`${newDir}/report.md`);
  expect(screen.queryByText("replacement report body")).toBeNull();
});

// Two same-named reports in different sessions are different documents. The
// document identity includes the concrete target, so opening the second must
// not match (and overwrite) the first pane's saved reference.
test("same-named reports in different artifact directories are different documents", () => {
  const sourcePaneId = workspaceStore.getState().openPane("session", { ref });
  const first = bindArtifactPath("/state/a/sessions/s1/artifacts/report.md", "/state/a/sessions/s1/artifacts");
  const second = bindArtifactPath("/state/b/sessions/s2/artifacts/report.md", "/state/b/sessions/s2/artifacts");
  if (!first || !second) throw new Error("expected artifact references");
  act(() => openDocBeside({ session: ref, sourcePaneId, reference: first }));
  const firstDocument = workspaceStore.getState().panes.find((candidate) => candidate.type === "doc");
  if (!firstDocument) throw new Error("expected the first document");
  act(() => openDocBeside({ session: ref, sourcePaneId, reference: second }));
  // The first pane keeps the document it opened. Matching on session, name and
  // root alone would have reused that pane and overwritten its saved target.
  expect((firstDocument.params as DocParams).artifactTarget).toBe("/state/a/sessions/s1/artifacts/report.md");
  expect(documentPaneState(firstDocument)?.reference.readTarget).toBe("/state/a/sessions/s1/artifacts/report.md");
  const targets = workspaceStore
    .getState()
    .panes.filter((candidate) => candidate.type === "doc")
    .map((document) => (document.params as DocParams).artifactTarget);
  expect(targets).toEqual(["/state/a/sessions/s1/artifacts/report.md", "/state/b/sessions/s2/artifacts/report.md"]);
});

// A completed snapshot may advertise no artifacts root at all, which is a
// supported answer for a session this hub serves none for. A restored artifacts
// pane must say the file is unavailable then, not wait on a skeleton for a read
// that will never run.
test("a restored artifacts document says unavailable when the snapshot advertises no root", async () => {
  client.on("thread/read", (params) => ({
    thread: thread(cwd),
    requestGeneration: params.requestGeneration,
  }));
  respond("should not be read");
  workspaceStore.getState().openPane("doc", {
    session: ref,
    path: "report.md",
    root: "artifacts",
    artifactTarget: "/state/a/sessions/s1/artifacts/report.md",
    kind: "file",
  });
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.type === "doc");
  if (!pane) throw new Error("expected a document pane");
  render(
    <PaneVisibilityContext value={true}>
      <DocPane params={pane.params as DocParams} paneId={pane.id} focused={false} />
    </PaneVisibilityContext>,
  );
  await waitFor(() => expect(screen.getByTestId("empty-state").getAttribute("data-reason")).toBe("unavailable"));
  expect(requests.map((url) => url.searchParams.get("path"))).not.toContain("/state/a/sessions/s1/artifacts/report.md");
});

// A cached shell publishes before the authoritative read and carries no
// artifacts root yet. The pane must stay pending while that is true, not tell
// the user the report is gone.
test("a restored artifacts document stays pending while the thread is a cached shell", async () => {
  // A shell model carries no artifacts root, and its authoritative read has
  // not landed. That absence must not read as rejection while the shell stands.
  client.on("thread/read", () => new Promise(() => {}));
  await act(async () => {
    threadsStore.setState((state) => ({
      threads: new Map(state.threads).set(ref, hydrateThreadForTest(cwd)),
      cacheLifetimes: new Map(state.cacheLifetimes).set(ref, { shell: true }),
    }));
  });
  respond("not read");
  workspaceStore.getState().openPane("doc", {
    session: ref,
    path: "report.md",
    root: "artifacts",
    artifactTarget: "/state/a/sessions/s1/artifacts/report.md",
    kind: "file",
  });
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.type === "doc");
  if (!pane) throw new Error("expected a document pane");
  render(
    <PaneVisibilityContext value={true}>
      <DocPane params={pane.params as DocParams} paneId={pane.id} focused={false} />
    </PaneVisibilityContext>,
  );
  expect(screen.queryByTestId("empty-state")).toBeNull();
});

// hydrateThreadForTest builds the model a cached shell publishes: no
// artifacts root, and nothing authoritative behind it yet.
function hydrateThreadForTest(at: string): ThreadModel {
  const wire = thread(at);
  return {
    ref,
    threadId: wire.id,
    name: wire.name ?? "",
    status: wire.status,
    modelProvider: wire.modelProvider,
    model: wire.modelProvider,
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    jobsTreeRevision: null,
    lastFrameAt: 0,
    capabilities: wire.evener.capabilities,
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    contextAssumed: false,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: at,
  } as ThreadModel;
}
