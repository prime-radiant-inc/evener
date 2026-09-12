import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, expect, test } from "vitest";
import type { ThreadModel } from "../../../../protocol/model";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
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
    rename: false,
  },
  goal: null,
  contextUsed: 0,
  contextWindow: 0,
  contextPressure: 0,
  usage: null,
  workMillis: 0,
  reasoningEffortLevels: [],
  supportsReasoning: false,
  cwd: "/workspace/project",
};
const path = "docs/superpowers/specs/2026-09-10-daemon-idle-retirement-design.md";
const source = `[written **design**](${path})`;

function message(markdown: string, live = false, snapshot: ThreadModel | null = thread) {
  return (
    <TranscriptRenderProvider thread={snapshot ?? undefined}>
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
});

test.each([false, true])("agent file hyperlink opens the session-scoped document pane (live=%s)", (live) => {
  render(message(source, live));
  const link = screen.getByRole("link", { name: "written design" });
  const url = new URL(link.getAttribute("href") ?? "", "https://hub.example");
  expect(url.pathname).toBe("/doc/file");
  expect(url.searchParams.get("session")).toBe("local:034MXwo6BpPH0QQCgdICSf");
  expect(url.searchParams.get("path")).toBe(path);
  expect(link.querySelector("strong")?.textContent).toBe("design");
  expect(fireEvent.click(link)).toBe(false);
  expect(workspaceStore.getState().panes).toMatchObject([
    { type: "doc", params: { session: "local:034MXwo6BpPH0QQCgdICSf", path, kind: "file" } },
  ]);
});

test("file hyperlink has the standard open-beside affordance", () => {
  render(message(source));
  const open = screen.getByRole("button", { name: `Open beside: ${path}` });
  expect(open.getAttribute("title")).toBe("Open");
  expect(open.querySelector("svg")).not.toBeNull();
  fireEvent.click(open);
  expect(workspaceStore.getState().panes).toMatchObject([
    { type: "doc", params: { session: "local:034MXwo6BpPH0QQCgdICSf", path, kind: "file" } },
  ]);
});

test.each([
  ["remote:034MXwo6BpPH0QQCgdICSf", false],
  ["remote:034MXwo6BpPH0QQCgdICSf", true],
  ["agentsview:034MXwo6BpPH0QQCgdICSf", false],
  ["agentsview:034MXwo6BpPH0QQCgdICSf", true],
  ["localhost:034MXwo6BpPH0QQCgdICSf", false],
  ["LOCAL:034MXwo6BpPH0QQCgdICSf", false],
  ["local:remote:034MXwo6BpPH0QQCgdICSf", false],
] as const)("non-local session %s keeps ordinary file links (live=%s)", (ref, live) => {
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
});

test.each(["034MXwo6BpPH0QQCgdICSf", "local:034MXwo6BpPH0QQCgdICSf"])(
  "local session ref %s retains file actions",
  (ref) => {
    render(message(source, false, { ...thread, ref }));
    fireEvent.click(screen.getByRole("button"));
    expect(workspaceStore.getState().panes).toMatchObject([{ type: "doc", params: { session: ref, path } }]);
  },
);

test("switching to a non-local snapshot removes existing file actions", () => {
  const { rerender } = render(message(source));
  expect(screen.getAllByRole("button")).toHaveLength(1);
  rerender(message(source, false, { ...thread, ref: "remote:034MXwo6BpPH0QQCgdICSf" }));
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
  ["./docs/design.md", "./docs/design.md", "file"],
  ["README", "README", "file"],
  ["/workspace/project/docs/design.md", "docs/design.md", "file"],
  ["images/screen%20shot.png", "images/screen shot.png", "image"],
  ["images/diagram.svg", "images/diagram.svg", "file"],
])("file link %s resolves to the actual file path", (href, expectedPath, kind) => {
  render(message(`[file](${href})`));
  const link = screen.getByRole("link", { name: "file" });
  const url = new URL(link.getAttribute("href") ?? "", "https://hub.example");
  expect(url.pathname).toBe(kind === "image" ? "/doc/image" : "/doc/file");
  expect(url.searchParams.get("path")).toBe(expectedPath);
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
  expect(container.querySelectorAll("a")).toHaveLength(1);
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
