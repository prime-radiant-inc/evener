import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeAll, expect, test } from "vitest";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { NotificationCard } from "./NotificationCard";
import type { ParsedNotification } from "./steeringClassify";

beforeAll(async () => {
  await import("../../");
});

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
});

function notif(overrides: Partial<ParsedNotification> = {}): ParsedNotification {
  return {
    type: "job",
    title: "Job completed",
    tone: "success",
    secondary: "delegate",
    excerpt: "",
    concerns: [],
    rawText: '<job-notification job_id="j">raw</job-notification>',
    ...overrides,
  };
}

test("renders the title and tags the tone", () => {
  render(<NotificationCard notification={notif()} />);
  expect(screen.getByText("Job completed")).toBeTruthy();
  expect(screen.getByTestId("notification-card").getAttribute("data-tone")).toBe("success");
});

test("a success/neutral notification recedes: no tone chip (color spent only on warning/error)", () => {
  render(<NotificationCard notification={notif({ tone: "success" })} />);
  expect(screen.queryByText("error")).toBe(null);
  expect(screen.queryByText("warning")).toBe(null);
});

test("an error notification earns a danger chip", () => {
  render(<NotificationCard notification={notif({ tone: "error" })} />);
  expect(screen.getByText("error")).toBeTruthy();
});

test("the secondary line surfaces the demoted metadata", () => {
  render(<NotificationCard notification={notif({ secondary: "shell · exit 2 · boom" })} />);
  expect(screen.getByText("shell · exit 2 · boom")).toBeTruthy();
});

test("renders parsed job fields and excerpt as ordinary readable text", () => {
  render(
    <NotificationCard
      notification={notif({
        jobId: "job_42",
        jobType: "delegate",
        status: "completed",
        reason: "completed cleanly",
        outputBytes: 4,
        exitCode: 0,
        excerpt: "The child report is ready.",
      })}
    />,
  );
  expect(screen.getByTestId("notification-field-status").textContent).toContain("completed");
  expect(screen.getByTestId("notification-field-job-type").textContent).toContain("delegate");
  expect(screen.getByTestId("notification-field-output").textContent).toContain("4");
  expect(screen.getByTestId("notification-field-reason").textContent).toContain("completed cleanly");
  expect(screen.getByTestId("notification-field-exit").textContent).toContain("0");
  expect(screen.getByText("The child report is ready.")).toBeTruthy();
  expect(screen.getByTestId("notification-raw").textContent).toContain("raw");
});

test("a valid local child ref opens the shared transcript action beside the focused main pane", async () => {
  workspaceStore.setState({
    panes: [{ id: "main", type: "session", params: { ref: "local:parent" }, slot: "main" }],
    focusedPaneId: "main",
  });
  const user = userEvent.setup();
  render(<NotificationCard notification={notif({ transcriptRef: "local:child" })} />);
  const button = screen.getByRole("button", { name: "Open subagent" });
  expect(button.textContent).toContain("open");
  await user.click(button);
  const opened = workspaceStore.getState().panes.find((pane) => pane.type === "transcript");
  expect(opened?.params).toEqual({ ref: "local:child" });
  expect(opened?.slot).toBe("secondary");
});

test("opening a child restores the notification owner as main when an unrelated session is focused", async () => {
  workspaceStore.setState({
    panes: [
      { id: "unrelated", type: "session", params: { ref: "local:unrelated" }, slot: "main" },
      { id: "owner", type: "session", params: { ref: "local:owner" }, slot: "secondary" },
    ],
    focusedPaneId: "unrelated",
  });
  const user = userEvent.setup();
  render(<NotificationCard notification={notif({ transcriptRef: "local:child" })} sessionRef="local:owner" />);

  await user.click(screen.getByRole("button", { name: "Open subagent" }));

  const state = workspaceStore.getState();
  const owner = state.panes.find(
    (pane) => pane.type === "session" && (pane.params as { ref?: string }).ref === "local:owner",
  );
  const child = state.panes.find(
    (pane) => pane.type === "transcript" && (pane.params as { ref?: string }).ref === "local:child",
  );
  expect(state.panes.some((pane) => (pane.params as { ref?: string }).ref === "local:unrelated")).toBe(false);
  expect(owner?.slot).toBe("main");
  expect(child?.params).toEqual({ ref: "local:child", parentRef: "local:owner" });
  expect(child?.slot).toBe("secondary");
  expect(state.mainPane()?.id).toBe(owner?.id);
  expect(state.focusedPaneId).toBe(child?.id);
});

test("a qualified remote child ref keeps its identity when opened", async () => {
  workspaceStore.setState({
    panes: [{ id: "main", type: "session", params: { ref: "local:parent" }, slot: "main" }],
    focusedPaneId: "main",
  });
  const user = userEvent.setup();
  render(<NotificationCard notification={notif({ transcriptRef: "remote:child" })} />);
  await user.click(screen.getByRole("button", { name: "Open subagent" }));
  expect(workspaceStore.getState().panes.find((pane) => pane.type === "transcript")?.params).toEqual({
    ref: "remote:child",
  });
});

test("missing and malformed refs have no dead open-subagent action", () => {
  for (const ref of [undefined, "", "child", "local:child:extra", "local:bad..child"]) {
    const { unmount } = render(<NotificationCard notification={notif({ transcriptRef: ref })} />);
    expect(screen.queryByRole("button", { name: "Open subagent" })).toBeNull();
    unmount();
  }
});

test("the raw block is always kept inspectable", () => {
  render(
    <NotificationCard
      notification={notif({ rawText: '<job-notification job_id="abc">the raw text</job-notification>' })}
    />,
  );
  expect(screen.getByTestId("notification-raw").textContent).toContain("the raw text");
});

test("an excerpt is shown, entity-decoded, as escaped text (never live HTML)", () => {
  render(<NotificationCard notification={notif({ excerpt: "&lt;script&gt;alert(1)&lt;/script&gt;" })} />);
  // Decoded to <script>… but rendered as text, so it appears verbatim and no
  // script element is ever created.
  expect(screen.getByText("<script>alert(1)</script>")).toBeTruthy();
  expect(document.querySelector("script")).toBe(null);
});

test("a very long excerpt remains a bounded normal-text preview without adding a nested disclosure", () => {
  const long = "x".repeat(900);
  render(<NotificationCard notification={notif({ excerpt: long })} />);
  expect(screen.getByText(/x{500}…/)).toBeTruthy();
  expect(screen.getByTestId("notification-card-root").querySelectorAll("details")).toHaveLength(1);
});

test("keeps raw notification as the one direct full-width disclosure row", () => {
  render(<NotificationCard notification={notif({ excerpt: "useful output" })} />);
  const root = screen.getByTestId("notification-card-root");
  const raw = screen.getByTestId("notification-raw-disclosure");
  expect(root.querySelectorAll("details")).toHaveLength(1);
  expect(raw.parentElement).toBe(root);
  expect(raw.querySelector("summary")?.textContent).toBe("Raw notification");
  expect(raw.querySelector("pre")?.textContent).toContain("<job-notification");
});

test("keeps the raw disclosure native and preserves its visible marker row", () => {
  render(<NotificationCard notification={notif()} />);
  const raw = screen.getByTestId("notification-raw-disclosure") as HTMLDetailsElement;
  const summary = raw.querySelector("summary");
  expect(summary?.tagName).toBe("SUMMARY");
  expect(summary?.getAttribute("role")).toBeNull();

  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "notificationcard.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const summaryRule = css.match(/\.summary\s*\{([^}]*)\}/)?.[1] ?? "";
  expect(summaryRule).toContain("display: list-item");
  expect(summaryRule).toContain("width: 100%");
  expect(summaryRule).toContain("max-width: 100%");
  expect(summaryRule).toContain("min-width: 0");
});

test("a communicate message renders through markdown", () => {
  const { container } = render(<NotificationCard notification={notif({ message: "**bold** result" })} />);
  expect(container.querySelector("strong")?.textContent).toBe("bold");
});

test("concerns surface as a quiet note", () => {
  render(<NotificationCard notification={notif({ concerns: ["edge case A", "edge case B"] })} />);
  expect(screen.getByText(/edge case A; edge case B/)).toBeTruthy();
});
