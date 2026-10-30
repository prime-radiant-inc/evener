import type { ThreadModel } from "@evener/appwire-client";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { documentPaneState, resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import "../../../doc";
import "../../index";
import { AgentMessageItem } from "./AgentMessageItem";

const thread: ThreadModel = {
  ref: "local:034MXwo6BpPH0QQCgdICSf",
  threadId: "034MXwo6BpPH0QQCgdICSf",
  name: "File links",
  status: { type: "idle" },
  modelProvider: "",
  model: "",
  visionModel: "",
  askPending: false,
  pendingEscalations: [],
  turns: [],
  queue: null,
  tasks: null,
  jobsUpdatedAt: null,
  jobsTreeRevision: null,
  lastFrameAt: 0,
  capabilities: {
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
  },
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
  cwd: "/workspace/project",
};
const path = "docs/superpowers/specs/2026-09-10-daemon-idle-retirement-design.md";
const source = `[written **design**](${path})`;

function message(markdown: string, live = false, snapshot: ThreadModel | null = thread, sourcePaneId = "pane_fixture") {
  return (
    <TranscriptRenderProvider thread={snapshot ?? undefined} sourcePaneId={snapshot ? sourcePaneId : undefined}>
      <AgentMessageItem
        item={{ id: "message", turnId: "turn", type: "agentMessage", text: markdown, pendingText: [markdown] }}
        turn={{ id: "turn", status: "completed", items: [] }}
        sessionRef={snapshot?.ref}
        live={live}
      />
    </TranscriptRenderProvider>
  );
}

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  vi.restoreAllMocks();
});

test.each([false, true])("agent file hyperlink opens the session-scoped document pane (live=%s)", (live) => {
  render(message(source, live));
  const link = screen.getByRole("link", { name: "written design" });
  const url = new URL(link.getAttribute("href") ?? "", "https://hub.example");
  expect(url.pathname).toBe("/doc/file");
  expect(url.searchParams.get("session")).toBe("local:034MXwo6BpPH0QQCgdICSf");
  expect(url.searchParams.get("path")).toBe(`${thread.cwd}/${path}`);
  expect(url.searchParams.get("format")).toBe("raw");
  expect(link.querySelector("strong")?.textContent).toBe("design");
  expect(fireEvent.click(link)).toBe(false);
  expect(workspaceStore.getState().panes).toMatchObject([
    { type: "doc", params: { session: "local:034MXwo6BpPH0QQCgdICSf", path, kind: "file" } },
  ]);
});

test("file hyperlink has the standard open-beside affordance", () => {
  render(message(source));
  const open = screen.getByRole("button", { name: `Open beside: ${path}` });
  expect(open.getAttribute("title")).toBeTruthy();
  expect(open.querySelector("svg")).not.toBeNull();
  fireEvent.click(open);
  expect(workspaceStore.getState().panes).toMatchObject([
    { type: "doc", params: { session: "local:034MXwo6BpPH0QQCgdICSf", path, kind: "file" } },
  ]);
});

test.each([["local:remote:034MXwo6BpPH0QQCgdICSf", false]] as const)(
  "invalid session %s keeps ordinary file links (live=%s)",
  (ref, live) => {
    render(message("[file](docs/design.md) [image](images/diagram.png)", live, { ...thread, ref }));
    const link = screen.getByRole("link", { name: "file" });
    expect(link.getAttribute("href")).toBe("docs/design.md");
    expect(screen.getByRole("link", { name: "image" }).getAttribute("href")).toBe("images/diagram.png");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    expect(screen.queryByRole("button")).toBeNull();
    let prevented = true;
    document.addEventListener(
      "click",
      (event) => {
        prevented = event.defaultPrevented;
        event.preventDefault(); // Observe ordinary navigation without asking jsdom to navigate.
      },
      { once: true },
    );
    fireEvent.click(link);
    expect(prevented).toBe(false);
    expect(workspaceStore.getState().panes).toEqual([]);
  },
);

test.each([
  ["034MXwo6BpPH0QQCgdICSf", false],
  ["local:034MXwo6BpPH0QQCgdICSf", false],
  ["remote:034MXwo6BpPH0QQCgdICSf", false],
  ["remote:034MXwo6BpPH0QQCgdICSf", true],
  ["agentsview:034MXwo6BpPH0QQCgdICSf", false],
  ["agentsview:034MXwo6BpPH0QQCgdICSf", true],
  ["localhost:034MXwo6BpPH0QQCgdICSf", false],
  ["LOCAL:034MXwo6BpPH0QQCgdICSf", false],
] as const)("valid session ref %s opens source-bound files and images (live=%s)", (ref, live) => {
  const owner = workspaceStore.getState().openPane("session", { ref });
  render(message("[file](docs/design.md) [image](images/diagram.png)", live, { ...thread, ref }, owner));
  for (const [name, expectedPath, kind] of [
    ["file", "docs/design.md", "file"],
    ["image", "images/diagram.png", "image"],
  ] as const) {
    const link = screen.getByRole("link", { name });
    const url = new URL(link.getAttribute("href") ?? "", "https://hub.example");
    expect(url.pathname).toBe(kind === "image" ? "/doc/image" : "/doc/file");
    expect(url.searchParams.get("session")).toBe(ref);
    expect(url.searchParams.get("path")).toBe(`${thread.cwd}/${expectedPath}`);
    expect(url.searchParams.get("format")).toBe(kind === "image" ? null : "raw");
    fireEvent.click(link);
    const doc = workspaceStore
      .getState()
      .panes.find((pane) => pane.type === "doc" && (pane.params as { path: string }).path === expectedPath);
    expect(doc).toMatchObject({ params: { session: ref, path: expectedPath, kind }, slot: "secondary" });
    expect(doc && documentPaneState(doc)).toMatchObject({
      reference: { cwd: thread.cwd, readTarget: `${thread.cwd}/${expectedPath}` },
      origin: { id: owner },
    });
  }
});

test("switching to an invalid snapshot removes existing file actions", () => {
  const { rerender } = render(message(source));
  expect(screen.getAllByRole("button")).toHaveLength(1);
  rerender(message(source, false, { ...thread, ref: "local:remote:034MXwo6BpPH0QQCgdICSf" }));
  expect(screen.getByRole("link").getAttribute("href")).toBe(path);
  expect(screen.queryByRole("button")).toBeNull();
  rerender(message(source));
  fireEvent.click(screen.getByRole("button"));
  expect(workspaceStore.getState().panes).toMatchObject([{ type: "doc", params: { session: thread.ref, path } }]);
});

test.each([
  ["docs/design%20notes.md", "docs/design notes.md", "file"],
  ["docs/notes%23one%3Ftwo%26three.md#section", "docs/notes#one?two&three.md", "file"],
  ["docs/100%25.md", "docs/100%.md", "file"],
  ["docs/caf%C3%A9.md?download=1#section", "docs/café.md", "file"],
  ["./docs/design.md", "docs/design.md", "file"],
  ["README", "README", "file"],
  ["/workspace/project/docs/design.md", "docs/design.md", "file"],
  ["images/screen%20shot.png", "images/screen shot.png", "image"],
  ["images/diagram.svg", "images/diagram.svg", "file"],
  ["./README.md:12", "README.md", "file"],
  ["./README.md%3A12", "README.md:12", "file"],
  ["./README.md%23L12", "README.md#L12", "file"],
  ["docs/a.md#L12", "docs/a.md", "file"],
  ["docs/100%2525.md", "docs/100%25.md", "file"],
])("file link %s resolves to the actual file path", (href, expectedPath, kind) => {
  render(message(`[file](${href})`));
  const link = screen.getByRole("link", { name: "file" });
  const url = new URL(link.getAttribute("href") ?? "", "https://hub.example");
  expect(url.pathname).toBe(kind === "image" ? "/doc/image" : "/doc/file");
  expect(url.searchParams.get("path")).toBe(`${thread.cwd}/${expectedPath}`);
  expect(url.searchParams.get("session")).toBe(thread.ref);
  expect(url.searchParams.get("format")).toBe(kind === "file" ? "raw" : null);
  fireEvent.click(link);
  expect(workspaceStore.getState().panes).toMatchObject([
    { type: "doc", params: { session: thread.ref, path: expectedPath, kind } },
  ]);
});

test.each([
  "https://example.com/docs/design.md",
  "http://example.com/docs/design.md",
  "//example.com/docs/design.md",
  "mailto:person@example.com",
  "#section",
  "?view=files",
  "/settings",
  "/workspace/project-other/design.md",
  "../outside.md",
  "docs/../../outside.md",
  "docs/%2e%2e/outside.md",
  "/workspace/project/../outside.md",
  "%2f%2fexample.com/design.md",
  "docs%5coutside.md",
  "docs/%00invalid.md",
  "docs/bad%ZZ.md",
  "docs%2Fa.md",
  "docs/%2e%2e%2foutside.md",
])("non-file or unsafe link %s keeps its original navigation without an affordance", (href) => {
  render(message(`[link](${href})`));
  const link = screen.getByRole("link", { name: "link" });
  expect(link.getAttribute("href")).toBe(href);
  expect(link.getAttribute("target")).toBe("_blank");
  expect(link.getAttribute("rel")).toBe("noopener noreferrer");
  expect(screen.queryByRole("button")).toBeNull();
  expect(workspaceStore.getState().panes).toEqual([]);
});

test("file enhancement leaves raw HTML and dangerous schemes neutralized", () => {
  const { container } = render(message('[bad](javascript:alert%281%29) <a href="docs/raw.md">raw</a>'));
  expect(container.querySelector("a")?.hasAttribute("href")).toBe(false);
  expect(container.querySelectorAll("a")).toHaveLength(2);
  expect(container.textContent).toContain('<a href="docs/raw.md">raw</a>');
  const generated = screen.getByRole("link", { name: "docs/raw.md" });
  const safe = new URL(generated.getAttribute("href") ?? "", "https://hub.example");
  expect(safe.pathname).toBe("/doc/file");
  expect(safe.searchParams.get("format")).toBe("raw");
  expect(safe.searchParams.get("path")).toBe("/workspace/project/docs/raw.md");
  expect(container.querySelector('a[href="docs/raw.md"]')).toBeNull();
  expect(screen.queryByRole("button")).toBeNull();
});

test.each(["ctrlKey", "metaKey", "shiftKey", "altKey"])("%s clicks preserve browser navigation", (modifier) => {
  render(message(source));
  let prevented = true;
  const observeNavigation = (event: MouseEvent) => {
    prevented = event.defaultPrevented;
    event.preventDefault(); // jsdom has no browser navigation; observe before suppressing it.
  };
  document.addEventListener("click", observeNavigation, { once: true });
  fireEvent.click(screen.getByRole("link"), { [modifier]: true });
  expect(prevented).toBe(false);
  expect(workspaceStore.getState().panes).toEqual([]);
});

test("stream updates and settlement keep one working affordance per file link", () => {
  const { rerender } = render(<StrictMode>{message(source, true)}</StrictMode>);
  expect(screen.getAllByRole("button")).toHaveLength(1);
  const next = `${source}\n\n[second](docs/second.md)`;
  rerender(<StrictMode>{message(next, true)}</StrictMode>);
  expect(screen.getAllByRole("button")).toHaveLength(2);
  fireEvent.click(screen.getByRole("link", { name: "second" }));
  expect(workspaceStore.getState().panes).toMatchObject([{ type: "doc", params: { path: "docs/second.md" } }]);
  rerender(<StrictMode>{message(next)}</StrictMode>);
  expect(screen.getAllByRole("button")).toHaveLength(2);
  fireEvent.click(screen.getByRole("button", { name: "Open beside: docs/second.md" }));
  expect(workspaceStore.getState().panes).toHaveLength(1);
  rerender(<StrictMode>{message("no links")}</StrictMode>);
  expect(screen.queryByRole("button")).toBeNull();
});

test("missing context fails closed and hydrated snapshot changes rebind existing links", () => {
  const { rerender } = render(message(source, false, null));
  expect(screen.getByRole("link").getAttribute("href")).toBe(path);
  expect(screen.queryByRole("button")).toBeNull();
  rerender(message(source));
  expect(screen.getAllByRole("button")).toHaveLength(1);
  rerender(message(source, false, { ...thread, ref: "local:other", cwd: "/other/worktree" }));
  fireEvent.click(screen.getByRole("link"));
  expect(workspaceStore.getState().panes).toMatchObject([
    { type: "doc", params: { session: "local:other", path, kind: "file" } },
  ]);
  rerender(message(source, false, { ...thread, cwd: "" }));
  expect(screen.queryByRole("button")).toBeNull();
  expect(screen.getByRole("link").getAttribute("href")).toBe(path);
});

test("reference links in long streaming Markdown receive the same file action", () => {
  const text = `${"A paragraph of context.\n\n".repeat(120)}[design][spec]\n\n[spec]: docs/design.md`;
  render(message(text, true));
  fireEvent.click(screen.getByRole("link", { name: "design" }));
  expect(workspaceStore.getState().panes).toMatchObject([
    { type: "doc", params: { session: thread.ref, path: "docs/design.md", kind: "file" } },
  ]);
});

test("streaming caret stays attached to the final prose block", () => {
  render(message(source, true));
  const stream = screen.getByTestId("agent-message-stream");
  // Matches agentmessageitem.module.css's live caret selector.
  expect(stream.querySelector(":scope > div > :last-child")).toBe(screen.getByRole("link").closest("p"));
});

test("opening a file keeps the session in the main pane and places the document beside it", () => {
  workspaceStore.getState().openPane("session", { ref: thread.ref });
  render(message(source));
  fireEvent.click(screen.getByRole("button"));
  expect(workspaceStore.getState().panes).toMatchObject([
    { type: "session", params: { ref: thread.ref }, slot: "main" },
    { type: "doc", params: { session: thread.ref, path, kind: "file" }, slot: "secondary" },
  ]);
});

const examplePaths = [
  "docs/superpowers/specs/2026-10-02-web-session-overview-design.md",
  "docs/superpowers/specs/2026-10-02-web-session-overview-review.md",
];

function assertDocument(
  session: string,
  expectedPath: string,
  cwd: string,
  sourcePaneId: string,
  provenance = "relative",
) {
  const panes = workspaceStore.getState().panes;
  const doc = panes.find(
    (pane) =>
      pane.type === "doc" &&
      (pane.params as { session: string; path: string }).session === session &&
      (pane.params as { path: string }).path === expectedPath,
  );
  if (!doc) throw new Error("document did not open");
  expect(doc).toMatchObject({ params: { session, path: expectedPath, kind: "file" }, slot: "secondary" });
  expect(documentPaneState(doc)).toMatchObject({
    reference: { path: expectedPath, cwd, readTarget: `${cwd}/${expectedPath}`, provenance },
    origin: { id: sourcePaneId, params: { ref: session } },
  });
  expect(panes.find((pane) => pane.id === sourcePaneId)?.slot).toBe("main");
}

test.each([false, true])(
  "Jesse's plain filenames open from their owner without recognition fetches (live=%s)",
  (live) => {
    const fetch = vi.spyOn(globalThis, "fetch");
    const owner = workspaceStore.getState().openPane("session", { ref: thread.ref });
    workspaceStore.getState().openPane("session", { ref: "local:unrelated" });
    render(message(`Spec: ${examplePaths[0]}\nReview: ${examplePaths[1]}`, live, thread, owner));
    expect(fetch).not.toHaveBeenCalled();
    expect(screen.queryByRole("button")).toBeNull();
    for (const filename of examplePaths) {
      const link = screen.getByRole("link", { name: filename });
      expect(fireEvent.click(link)).toBe(false);
      assertDocument(thread.ref, filename, thread.cwd, owner);
    }
    expect(fetch).not.toHaveBeenCalled();
  },
);

test.each([
  ["docs/a.md:12.", "docs/a.md"],
  ["‘docs/café.md’…", "docs/café.md"],
  ["docs/100%25.md", "docs/100%25.md"],
  ["./docs//a.md", "docs/a.md"],
  ["/workspace/project/docs/a.md", "docs/a.md"],
  ["`README.md:12`", "README.md"],
  ["`src/Makefile`", "src/Makefile"],
  ["> docs/a.md", "docs/a.md"],
  ["# docs/a.md", "docs/a.md"],
  ["- docs/a.md", "docs/a.md"],
  ["| File |\n| --- |\n| docs/a.md |", "docs/a.md"],
])("generated reference %s captures its normalized binding", (markdown, expectedPath) => {
  const owner = workspaceStore.getState().openPane("session", { ref: thread.ref });
  render(message(markdown, false, thread, owner));
  const link = screen.getByRole("link");
  expect(screen.queryByRole("button")).toBeNull();
  if (markdown.startsWith("`")) expect(link.closest("code")).not.toBeNull();
  const href = new URL(link.getAttribute("href") ?? "", "https://hub.example");
  expect(href.searchParams.get("path")).toBe(`${thread.cwd}/${expectedPath}`);
  fireEvent.click(link);
  assertDocument(
    thread.ref,
    expectedPath,
    thread.cwd,
    owner,
    markdown.startsWith("/workspace") ? "absolute" : "relative",
  );
});

test.each([
  "../**docs/a.md**",
  "https://host/**docs/a.md**",
  "docs/**a.md**",
  "docs/a.md,**docs/b.md**",
  "docs/foo(bar)/a.md",
  "https://host/docs/a.md",
  "user@host/docs/a.md",
  "example.com/docs/a.md",
  "../docs/a.md",
  "/workspace/project-other/docs/a.md",
  "README.md",
  "v1.2.3",
  "docs/directory/",
  "`docs/a.md`.other",
  "docs/foo(`a.md`)",
  "../`docs/a.md`",
  "`docs/a.md`/tail",
  "`cat docs/a.md`",
  "```\ndocs/a.md\n```",
  "    docs/a.md",
])("invalid, split or excluded candidate %s never yields a suffix link", (markdown) => {
  render(message(markdown));
  for (const link of screen.queryAllByRole("link")) {
    // GFM keeps HTTPS autolinks. They must not become a file or a suffix action.
    expect(link.getAttribute("href")).toMatch(/^https:\/\//);
    expect(link.textContent).toMatch(/^https:\/\//);
  }
  expect(screen.queryByRole("button")).toBeNull();
});

test("adjacent bracketed paths across formatting retain complete candidates", () => {
  render(message("(**docs/a.md**),(**docs/b.md**)"));
  expect(screen.getAllByRole("link").map((link) => link.textContent)).toEqual(["docs/a.md", "docs/b.md"]);
});

test("hydration and context replacement update unchanged filename callbacks and exact origin", () => {
  const first = workspaceStore.getState().openPane("session", { ref: thread.ref });
  const secondThread = { ...thread, ref: "remote:other", cwd: "/workspace/other" };
  const second = workspaceStore.getState().openPane("session", { ref: secondThread.ref });
  const third = workspaceStore.getState().openPane("transcript", { ref: secondThread.ref });
  const body = "docs/a.md";
  const { rerender, unmount } = render(<StrictMode>{message(body, false, { ...thread, cwd: "" }, first)}</StrictMode>);
  expect(screen.queryByRole("link")).toBeNull();
  rerender(<StrictMode>{message(body, false, thread, first)}</StrictMode>);
  fireEvent.click(screen.getByRole("link"));
  assertDocument(thread.ref, body, thread.cwd, first);
  rerender(<StrictMode>{message(body, false, secondThread, second)}</StrictMode>);
  fireEvent.click(screen.getByRole("link"));
  assertDocument(secondThread.ref, body, secondThread.cwd, second);
  rerender(<StrictMode>{message(body, false, secondThread, third)}</StrictMode>);
  const link = screen.getByRole("link");
  fireEvent.click(link);
  const doc = workspaceStore
    .getState()
    .panes.find((pane) => pane.type === "doc" && (pane.params as { session: string }).session === secondThread.ref);
  expect(doc && documentPaneState(doc)?.origin?.id).toBe(third);
  expect(doc && documentPaneState(doc)?.reopen).toBe(1);
  const bubble = screen.getByTestId("agent-bubble");
  unmount();
  expect(bubble.querySelector("a")).toBeNull();
  expect(bubble.textContent?.trimEnd()).toBe(body);
  const state = workspaceStore.getState().panes;
  link.addEventListener(
    "click",
    (event) => {
      expect(event.defaultPrevented).toBe(false);
      event.preventDefault(); // Suppress jsdom navigation on this detached, retired link.
    },
    { once: true },
  );
  fireEvent.click(link);
  expect(workspaceStore.getState().panes).toBe(state);
});

test("generated stream growth, settlement and aliases retain one action and one document", () => {
  const owner = workspaceStore.getState().openPane("session", { ref: thread.ref });
  const { rerender } = render(<StrictMode>{message("docs/a.md", true, thread, owner)}</StrictMode>);
  fireEvent.click(screen.getByRole("link"));
  const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  rerender(<StrictMode>{message("./docs/a.md:12 and `docs/a.md:20`", true, thread, owner)}</StrictMode>);
  expect(screen.getAllByRole("link")).toHaveLength(2);
  fireEvent.click(screen.getByRole("link", { name: "./docs/a.md" }));
  expect(doc && documentPaneState(doc)?.reopen).toBe(1);
  rerender(<StrictMode>{message("./docs/a.md:12 and `docs/a.md:20`", false, thread, owner)}</StrictMode>);
  fireEvent.click(screen.getByRole("link", { name: "docs/a.md:20" }));
  expect(doc && documentPaneState(doc)?.reopen).toBe(2);
  expect(workspaceStore.getState().panes.filter((pane) => pane.type === "doc")).toHaveLength(1);
  rerender(<StrictMode>{message("replacement", false, thread, owner)}</StrictMode>);
  expect(screen.queryByRole("link")).toBeNull();
  expect(screen.getByTestId("agent-bubble").textContent?.trimEnd()).toBe("replacement");
});

test.each(["ctrlKey", "metaKey", "shiftKey", "altKey", "nonprimary"])(
  "generated %s click preserves the bound raw URL without opening",
  (modifier) => {
    render(message("docs/a.md", false, { ...thread, ref: "remote:owner" }));
    const link = screen.getByRole("link");
    const url = new URL(link.getAttribute("href") ?? "", "https://hub.example");
    expect(url.pathname).toBe("/doc/file");
    expect(url.searchParams.get("format")).toBe("raw");
    expect(url.searchParams.get("session")).toBe("remote:owner");
    expect(url.searchParams.get("path")).toBe("/workspace/project/docs/a.md");
    let prevented = true;
    document.addEventListener(
      "click",
      (event) => {
        prevented = event.defaultPrevented;
        event.preventDefault();
      },
      { once: true },
    );
    fireEvent.click(link, modifier === "nonprimary" ? { button: 1 } : { [modifier]: true });
    expect(prevented).toBe(false);
    expect(workspaceStore.getState().panes).toEqual([]);
  },
);

test("keyboard activation's native anchor click opens the captured source", () => {
  const owner = workspaceStore.getState().openPane("session", { ref: thread.ref });
  render(message("`docs/a.md`", false, thread, owner));
  const link = screen.getByRole("link");
  link.focus();
  expect(document.activeElement).toBe(link);
  // Browsers dispatch a detail=0 click for Enter on an anchor. jsdom does not implement that default action.
  fireEvent.click(link, { detail: 0 });
  assertDocument(thread.ref, "docs/a.md", thread.cwd, owner);
});

test.each(["", "local:", "local:bad:ref", "bad ref", "local:../bad"])(
  "invalid source %s leaves filename text untouched",
  (ref) => {
    render(message("docs/a.md and `README.md`", false, { ...thread, ref }));
    expect(screen.queryByRole("link")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
    expect(screen.getByTestId("agent-bubble").textContent?.trimEnd()).toBe("docs/a.md and README.md");
  },
);

test("missing exact source-pane binding leaves generated text and authored links untouched", () => {
  render(
    <TranscriptRenderProvider thread={thread}>
      <AgentMessageItem
        item={{
          id: "message",
          turnId: "turn",
          type: "agentMessage",
          text: "docs/a.md [explicit](docs/b.md)",
          pendingText: [],
        }}
        turn={{ id: "turn", status: "completed", items: [] }}
        sessionRef={thread.ref}
        live={false}
      />
    </TranscriptRenderProvider>,
  );
  expect(screen.getAllByRole("link")).toHaveLength(1);
  expect(screen.getByRole("link").getAttribute("href")).toBe("docs/b.md");
  expect(screen.queryByRole("button")).toBeNull();
});

test("remote generated images retain source binding and the existing image href mode", () => {
  const remote = { ...thread, ref: "remote:owner" };
  const owner = workspaceStore.getState().openPane("session", { ref: remote.ref });
  render(message("images/diagram.png", false, remote, owner));
  const link = screen.getByRole("link");
  const url = new URL(link.getAttribute("href") ?? "", "https://hub.example");
  expect(url.pathname).toBe("/doc/image");
  expect(url.searchParams.get("format")).toBeNull();
  expect(url.searchParams.get("session")).toBe(remote.ref);
  expect(url.searchParams.get("path")).toBe("/workspace/project/images/diagram.png");
  fireEvent.click(link);
  const doc = workspaceStore.getState().panes.find((pane) => pane.type === "doc");
  expect(doc).toMatchObject({ params: { session: remote.ref, path: "images/diagram.png", kind: "image" } });
  expect(doc && documentPaneState(doc)).toMatchObject({
    origin: { id: owner },
    reference: { readTarget: "/workspace/project/images/diagram.png" },
  });
});

// An agent files a report in its session's retained artifacts directory and
// links it by file:// URL, which is what the tool guidance tells it to do. The
// hub serves that directory from /doc/file, and the thread snapshot names it,
// so the transcript has to turn the link into a document-pane request instead
// of leaving a bare anchor the sanitizer stripped.
const artifactsDir = "/var/state/projects/example/sessions/034MXwo6BpPH0QQCgdICSf/artifacts";
const artifactsThread: ThreadModel = { ...thread, artifactsDir };
const reportPath = `${artifactsDir}/docs-quality-repairs.md`;
const reportParamPath = "docs-quality-repairs.md";

test.each([false, true])("a file URL inside the session artifacts opens as a document (live=%s)", (live) => {
  render(message(`[Full report and retained evidence](file://${reportPath})`, live, artifactsThread));
  const link = screen.getByRole("link", { name: "Full report and retained evidence" });
  const url = new URL(link.getAttribute("href") ?? "", "https://hub.example");
  expect(url.pathname).toBe("/doc/file");
  expect(url.searchParams.get("session")).toBe(thread.ref);
  expect(url.searchParams.get("path")).toBe(reportPath);
  expect(url.searchParams.get("format")).toBe("raw");
  expect(fireEvent.click(link)).toBe(false);
  expect(workspaceStore.getState().panes).toMatchObject([
    { type: "doc", params: { session: thread.ref, path: reportParamPath, root: "artifacts", kind: "file" } },
  ]);
});

test("a file URL inside the session cwd opens as a document too", () => {
  render(message(`[design](file://${thread.cwd}/docs/design.md)`));
  const link = screen.getByRole("link", { name: "design" });
  const url = new URL(link.getAttribute("href") ?? "", "https://hub.example");
  expect(url.pathname).toBe("/doc/file");
  expect(url.searchParams.get("path")).toBe(`${thread.cwd}/docs/design.md`);
  fireEvent.click(link);
  expect(workspaceStore.getState().panes).toMatchObject([
    { type: "doc", params: { session: thread.ref, path: "docs/design.md", kind: "file" } },
  ]);
});

// The hub refuses any path outside cwd and the artifacts directory, so the
// transcript must not offer a pane for one: the anchor keeps its destination
// visible and does nothing more.
test.each([
  ["outside both roots", "/etc/passwd"],
  ["a sibling session's artifacts", "/var/state/projects/example/sessions/OTHER/artifacts/r.md"],
  ["a traversal out of artifacts", `${artifactsDir}/../delegates.jsonl`],
])("a file URL naming %s stays a plain link", (_label, target) => {
  render(message(`[report](file://${target})`, false, artifactsThread));
  const link = screen.getByRole("link", { name: "report" });
  expect(link.getAttribute("href")).toBe(`file://${target}`);
  expect(screen.queryByRole("button")).toBeNull();
  let prevented = true;
  document.addEventListener(
    "click",
    (event) => {
      prevented = event.defaultPrevented;
      event.preventDefault();
    },
    { once: true },
  );
  fireEvent.click(link);
  expect(prevented).toBe(false);
  expect(workspaceStore.getState().panes).toEqual([]);
});

// A thread snapshot that names no artifacts directory (a remote session) can
// only bind cwd paths, so an artifacts file URL must not be guessed at.
test("an artifacts file URL without an advertised directory stays a plain link", () => {
  render(message(`[report](file://${reportPath})`));
  const link = screen.getByRole("link", { name: "report" });
  expect(link.getAttribute("href")).toBe(`file://${reportPath}`);
  expect(workspaceStore.getState().panes).toEqual([]);
});

// A malformed or foreign file URL names no path, so it must not bind even when
// its text looks like it sits under a served root.
test.each([
  "file:///var/state/projects/example/sessions/034MXwo6BpPH0QQCgdICSf/artifacts/bad%zz.md",
  "file://server/share/report.md",
  "file:relative/report.md",
])("a malformed file URL %s never opens a document", (target) => {
  render(message(`[report](${target})`, false, artifactsThread));
  expect(screen.getByRole("link", { name: "report" })).toBeTruthy();
  expect(screen.queryByRole("button")).toBeNull();
  expect(workspaceStore.getState().panes).toEqual([]);
});

test("a file URL inside the session artifacts has the standard open-beside affordance", () => {
  render(message(`[Full report and retained evidence](file://${reportPath})`, false, artifactsThread));
  const open = screen.getByRole("button", { name: "Open beside: docs-quality-repairs.md" });
  fireEvent.click(open);
  expect(workspaceStore.getState().panes).toMatchObject([
    { type: "doc", params: { session: thread.ref, path: reportParamPath, root: "artifacts", kind: "file" } },
  ]);
});

// The reference recorded by a live open must say which root it came from, or
// DocPane rebinds it against the session's working directory once the thread's
// cwd differs from the artifacts dir, and every later consumer of that
// reference points into the working tree instead.
test("opening an artifacts link records a reference that keeps the artifacts root", () => {
  render(message(`[Full report and retained evidence](file://${reportPath})`, false, artifactsThread));
  fireEvent.click(screen.getByRole("link", { name: "Full report and retained evidence" }));
  const pane = workspaceStore.getState().panes.find((candidate) => candidate.type === "doc");
  if (!pane) throw new Error("expected a document pane");
  expect(documentPaneState(pane)?.reference).toMatchObject({
    root: "artifacts",
    cwd: artifactsDir,
    path: "docs-quality-repairs.md",
    readTarget: reportPath,
  });
});
