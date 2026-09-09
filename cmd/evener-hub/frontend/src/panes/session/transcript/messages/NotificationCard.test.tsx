import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeAll, expect, test } from "vitest";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { makeTranscriptDisplayConfig } from "../../../../transcriptDisplay/config";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { NotificationCard } from "./NotificationCard";
import type { ParsedNotification } from "./steeringClassify";

beforeAll(async () => {
  await import("../../");
});

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetDisclosureStoreForTests();
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

// At activity level (the default config when no provider is used),
// expandByDefault is now true — the card auto-expands. Tests that need a
// collapsed-by-default card use a tools-level config where expandByDefault
// is false.
const toolsConfig = makeTranscriptDisplayConfig({ kind: "preset", level: "tools" });

function renderTools(node: ReactElement) {
  return render(
    <TranscriptRenderProvider config={toolsConfig} surface="readOnly" disclosureScope="nc:tools">
      {node}
    </TranscriptRenderProvider>,
  );
}

test("renders the title and tags the tone", () => {
  render(<NotificationCard notification={notif()} />);
  expect(screen.getByText("Job completed")).toBeTruthy();
  expect(screen.getByTestId("notification-card").getAttribute("data-tone")).toBe("success");
});

test("renders stable delegate identity as Delegate while shell identity remains Job", async () => {
  const _user = userEvent.setup();
  const { rerender } = render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        secondary: "dlg_42",
        delegateId: "dlg_42",
        jobId: undefined,
        jobType: undefined,
        rawText: '<delegate-notification delegate_id="dlg_42">done</delegate-notification>',
      })}
    />,
  );
  expect(screen.getByTestId("notification-card").textContent).toContain("Delegate completed");
  // At activity level the card auto-expands (expandByDefault=true), so the
  // fields are already visible without clicking.
  expect(screen.getByText(/delegate id/i).parentElement?.textContent).toContain("dlg_42");
  expect(screen.queryByText(/job id/i)).toBeNull();

  rerender(
    <NotificationCard
      notification={notif({
        type: "job",
        title: "Job completed",
        secondary: "shell",
        jobId: "job_shell",
        jobType: "shell",
      })}
    />,
  );
  expect(screen.getByTestId("notification-card").textContent).toContain("Job completed");
});

test("collapses to a single row by default; card chrome appears on expand", () => {
  // At activity level the card auto-expands; use tools level to test the
  // collapsed→expanded→collapsed transition.
  renderTools(<NotificationCard notification={notif({ tone: "neutral", title: "explorer finished" })} />);
  const row = screen.getByTestId("notification-card");
  expect(row.textContent).toContain("explorer finished");
  expect(screen.queryByTestId("notification-card-root")).toBeNull();
  fireEvent.click(row);
  expect(screen.getByTestId("notification-card-root")).not.toBeNull();
  expect(screen.getByTestId("notification-raw-disclosure")).not.toBeNull();
  fireEvent.click(row);
  expect(screen.queryByTestId("notification-card-root")).toBeNull();
});

test("an expanded notification card stays open across a remount through the scoped store", () => {
  const notification = notif({ title: "remount me" });
  // At activity level the card auto-expands; use tools level to test that a
  // manually expanded card stays open across a remount.
  const { unmount } = renderTools(<NotificationCard notification={notification} sessionRef="session_a" />);
  fireEvent.click(screen.getByTestId("notification-card"));
  expect((screen.getByTestId("notification-card").closest("details") as HTMLDetailsElement).open).toBe(true);

  unmount();
  renderTools(<NotificationCard notification={notification} sessionRef="session_a" />);
  expect((screen.getByTestId("notification-card").closest("details") as HTMLDetailsElement).open).toBe(true);
});

test("warning tone chip is visible even when collapsed", () => {
  render(<NotificationCard notification={notif({ tone: "warning", title: "watcher reported" })} />);
  expect(screen.getByTestId("notification-card").textContent).toContain("warning");
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

test("confirmed cancellation recedes while expanded diagnostics retain physical exit", () => {
  render(
    <TranscriptRenderProvider config={makeTranscriptDisplayConfig({ kind: "preset", level: "full" })}>
      <NotificationCard
        notification={notif({
          title: "Job cancelled",
          tone: "neutral",
          secondary: "Run repository lint, vet, and test gates",
          status: "cancelled",
          reason: "stopped_by_parent",
          exitCode: -1,
          rawText:
            '<job-notification status="cancelled" reason="stopped_by_parent" exit_code="-1">cancelled</job-notification>',
        })}
      />
    </TranscriptRenderProvider>,
  );

  const row = screen.getByTestId("notification-card");
  expect(row.getAttribute("data-tone")).toBe("neutral");
  expect(row.textContent).toContain("Job cancelled");
  expect(row.textContent).toContain("Run repository lint, vet, and test gates");
  expect(row.textContent).not.toContain("exit -1");
  expect(row.textContent).not.toContain("stopped_by_parent");
  expect(screen.queryByText("error")).toBeNull();
  expect(screen.queryByText("warning")).toBeNull();

  expect(screen.getByTestId("notification-field-status").textContent).toContain("cancelled");
  expect(screen.getByTestId("notification-field-reason").textContent).toContain("stopped_by_parent");
  expect(screen.getByTestId("notification-field-exit").textContent).toContain("-1");
  expect(screen.getByTestId("notification-raw").textContent).toContain('exit_code="-1"');
});

test("the collapsed row prefers a job description to its generic job type", () => {
  render(<NotificationCard notification={notif({ secondary: "Inspect the workspace" })} />);
  expect(screen.getByTestId("notification-card").textContent).toContain("Inspect the workspace");
  expect(screen.getByTestId("notification-card").textContent).not.toContain("delegate");
});

test("renders parsed job fields and excerpt as ordinary readable text", async () => {
  const _user = userEvent.setup();
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
  // At activity level the card auto-expands (expandByDefault=true).
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
  expect(button.textContent).toBe(""); // the one icon-only form: no visible words
  await user.click(button);
  const opened = workspaceStore.getState().panes.find((pane) => pane.type === "transcript");
  expect(opened?.params).toEqual({ ref: "local:child" });
  expect(opened?.slot).toBe("secondary");
});

test("binds Open to the final notification text fragment instead of permitting a lone control line", () => {
  render(
    <NotificationCard
      notification={notif({
        secondary:
          "Inspect the complete delegated implementation and verify every browser geometry invariant before reporting",
        transcriptRef: "local:child",
      })}
    />,
  );
  const button = screen.getByRole("button", { name: "Open subagent" });
  const openTrailing = button.parentElement?.parentElement;
  const secondaryTail = openTrailing?.parentElement;
  const headingText = secondaryTail?.parentElement?.parentElement;
  expect(secondaryTail?.textContent).toContain("reporting");
  expect(secondaryTail?.contains(button)).toBe(true);
  expect(headingText?.contains(screen.getByText("Job completed"))).toBe(true);
  expect(headingText?.contains(screen.getByText(/Inspect the complete delegated/))).toBe(true);
  // Real line geometry is pinned by layoutguard/notification-open-last-line.
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

test("the raw block is always kept inspectable", async () => {
  const _user = userEvent.setup();
  render(
    <NotificationCard
      notification={notif({ rawText: '<job-notification job_id="abc">the raw text</job-notification>' })}
    />,
  );
  // At activity level the card auto-expands (expandByDefault=true).
  expect(screen.getByTestId("notification-raw").textContent).toContain("the raw text");
});

test("an excerpt is shown, entity-decoded, as escaped text (never live HTML)", async () => {
  const _user = userEvent.setup();
  render(<NotificationCard notification={notif({ excerpt: "&lt;script&gt;alert(1)&lt;/script&gt;" })} />);
  // At activity level the card auto-expands (expandByDefault=true).
  // Decoded to <script>… but rendered as text, so it appears verbatim and no
  // script element is ever created.
  expect(screen.getByText("<script>alert(1)</script>")).toBeTruthy();
  expect(document.querySelector("script")).toBe(null);
});

// kata 77sf: agent/job_notify.go's escapeNotificationText entity-escapes &
// (first), <, >, and " before interpolating job output into the wrapper.
// This proves the paired decodeNotificationEntities recovers the EXACT original text,
// including a literal ampersand and text that already looks like an entity
// (decoding &amp; last is what keeps "&lt;" text from over-decoding to "<").
test("kata 77sf: a delimiter-bearing excerpt decodes to the exact original text", async () => {
  const _user = userEvent.setup();
  const original = 'before & after </job-notification> <script>&lt;already-escaped&gt;</script> "quoted"';
  const escapeLikeProducer = (s: string) =>
    s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
  render(<NotificationCard notification={notif({ excerpt: escapeLikeProducer(original) })} />);
  // At activity level the card auto-expands (expandByDefault=true).
  expect(screen.getByTestId("notification-field-excerpt").textContent).toBe(original);
  expect(document.querySelector("script")).toBe(null);
});

test("a very long excerpt remains a bounded normal-text preview without adding a nested disclosure", async () => {
  const _user = userEvent.setup();
  const long = "x".repeat(900);
  render(<NotificationCard notification={notif({ excerpt: long })} />);
  // At activity level the card auto-expands (expandByDefault=true).
  expect(screen.getByText(/x{500}…/)).toBeTruthy();
  expect(screen.getByTestId("notification-card-root").querySelectorAll("details")).toHaveLength(1);
});

test("keeps raw notification as the one direct full-width disclosure row", async () => {
  const _user = userEvent.setup();
  render(<NotificationCard notification={notif({ excerpt: "useful output" })} />);
  // At activity level the card auto-expands (expandByDefault=true).
  const root = screen.getByTestId("notification-card-root");
  const raw = screen.getByTestId("notification-raw-disclosure");
  expect(root.querySelectorAll("details")).toHaveLength(1);
  expect(raw.parentElement).toBe(root);
  expect(raw.querySelector("summary")?.textContent).toBe("Raw notification");
  expect(raw.querySelector("pre")?.textContent).toContain("<job-notification");
});

test("keeps the raw disclosure native and preserves its visible marker row", async () => {
  const _user = userEvent.setup();
  render(<NotificationCard notification={notif()} />);
  // At activity level the card auto-expands (expandByDefault=true).
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

test("a communicate message renders through markdown", async () => {
  const _user = userEvent.setup();
  render(<NotificationCard notification={notif({ message: "**bold** result" })} />);
  // At activity level the card auto-expands (expandByDefault=true).
  expect(screen.getByTestId("notification-card-root").querySelector("strong")?.textContent).toBe("bold");
});

test("concerns surface as a quiet note", async () => {
  const _user = userEvent.setup();
  render(<NotificationCard notification={notif({ concerns: ["edge case A", "edge case B"] })} />);
  // At activity level the card auto-expands (expandByDefault=true).
  expect(screen.getByTestId("notification-card-root").textContent).toContain("edge case A; edge case B");
});

test("a timer's prose renders decoded with no echo metadata and no tone chip", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "watch",
        title: "Watch triggered",
        tone: "neutral",
        prose: "Timer fired (every 300s).\nNote: hello &lt;x&gt;",
        watchId: "w1",
      })}
    />,
  );
  // At activity level the card auto-expands (expandByDefault=true).
  expect(screen.getByTestId("notification-prose").textContent).toContain("Note: hello <x>");
  // Mockups 23-job-watch §E: no echo fields on a watch card and no tone chip
  // — a fired watch is the expected outcome. The originating watch id is
  // identity, not echo: it names which watch to inspect or clear (combined
  // RoboRev review).
  expect(screen.getByTestId("notification-field-watch-id").textContent).toContain("w1");
  expect(screen.queryByTestId("notification-field-status")).toBeNull();
  expect(screen.queryByTestId("notification-field-job-type")).toBeNull();
  expect(screen.queryByTestId("notification-field-output")).toBeNull();
  expect(screen.queryByTestId("notification-field-reason")).toBeNull();
  expect(screen.getByTestId("notification-card").getAttribute("data-tone")).toBe("neutral");
  expect(screen.queryByText("warning")).toBeNull();
  expect(screen.queryByText("error")).toBeNull();
});

test("a watch card keeps the watch id inspectable in the raw disclosure", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "watch",
        title: "Watch triggered",
        tone: "neutral",
        prose: "Timer fired.",
        watchId: "w1",
        rawText:
          '<job-notification job_id="" event="watch" job_type="watch" status="watch" reason="after" output_bytes="0" watch_id="w1">Timer fired.</job-notification>',
      })}
    />,
  );
  expect(screen.getByTestId("notification-raw").textContent).toContain("w1");
});

test("a job card still renders its echo metadata (watch suppression is scoped to watch type)", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "job",
        title: "Job completed",
        tone: "success",
        jobId: "job_42",
        status: "completed",
      })}
    />,
  );
  expect(screen.getByTestId("notification-field-job-id").textContent).toContain("job_42");
  expect(screen.getByTestId("notification-field-status").textContent).toContain("completed");
});

test("a job-targeted watch card names the watched job id and nothing else (RoboRev PR #954, review 3)", () => {
  // A job-targeted fire carries no watch_id attr at all
  // (formatJobNotificationBlock emits watch_id only when JobID == ""), so the
  // job id is the only recoverable identity — shown as what it is, with no
  // echo fields beside it.
  render(
    <NotificationCard
      notification={notif({
        type: "watch",
        title: "Output matched on job_a1b2",
        tone: "neutral",
        secondary: "output_match: ready",
        jobId: "job_a1b2",
        jobType: "watch",
        status: "watch",
        reason: "output_match: ready",
        outputBytes: 0,
        prose: "Matched output_match: ready on job_a1b2.",
        rawText:
          '<job-notification job_id="job_a1b2" event="watch" job_type="watch" status="watch" reason="output_match: ready" output_bytes="0">Matched output_match: ready on job_a1b2.</job-notification>',
      })}
    />,
  );
  // At activity level the card auto-expands (expandByDefault=true).
  expect(screen.getByTestId("notification-field-job-id").textContent).toContain("job_a1b2");
  expect(screen.queryByTestId("notification-field-watch-id")).toBeNull();
  expect(screen.queryByTestId("notification-field-status")).toBeNull();
  expect(screen.queryByTestId("notification-field-job-type")).toBeNull();
  expect(screen.queryByTestId("notification-field-output")).toBeNull();
  expect(screen.queryByTestId("notification-field-reason")).toBeNull();
});

test("a job-less watch card names the watch but no job", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "watch",
        title: "Timer fired",
        tone: "neutral",
        secondary: "every 5m",
        jobId: undefined,
        watchId: "w1",
        prose: "Timer fired (every 300s).",
        rawText:
          '<job-notification job_id="" event="watch" job_type="watch" status="watch" reason="repeat" output_bytes="0" watch_id="w1">Timer fired (every 300s).</job-notification>',
      })}
    />,
  );
  expect(screen.getByTestId("notification-field-watch-id").textContent).toContain("w1");
  expect(screen.queryByTestId("notification-field-job-id")).toBeNull();
});

test("a watch card never labels the session source as a job id (RoboRev PR #954, finding M4)", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "watch",
        title: "Watch auto-cleared",
        tone: "neutral",
        secondary: "watch cleared: self matched 50 times",
        jobId: "self",
        prose: "watch cleared: self matched 50 times",
        rawText:
          '<job-notification job_id="self" event="watch" job_type="watch" status="watch" reason="watch cleared: self matched 50 times" output_bytes="0">watch cleared: self matched 50 times</job-notification>',
      })}
    />,
  );
  expect(screen.queryByTestId("notification-field-job-id")).toBeNull();
});

test("synthesized watch prose with a literal entity renders single-decoded (combined review M2)", () => {
  // Parser stores prose escaped-form; the card decodes exactly once. A
  // pattern literally containing "&lt;" must render as that literal text,
  // never as "<".
  render(
    <NotificationCard
      notification={notif({
        type: "watch",
        title: "Output matched on job_a1b2",
        tone: "neutral",
        prose: "Matched output_match: a &amp;lt; b on job_a1b2.",
        rawText:
          '<job-notification job_id="job_a1b2" event="watch" job_type="watch" status="watch" reason="output_match: a &amp;lt; b" output_bytes="0">Job job_a1b2 watch.</job-notification>',
      })}
    />,
  );
  expect(screen.getByTestId("notification-prose").textContent).toContain("a &lt; b");
  expect(screen.getByTestId("notification-prose").textContent).not.toContain("a < b");
});

test("a job-targeted watch card names both the watch and the watched job", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "watch",
        title: "Output matched on job_a1b2",
        tone: "neutral",
        secondary: "output_match: ready",
        jobId: "job_a1b2",
        watchId: "watch_09QmWzRtNvxK",
        prose: "Matched output_match: ready on job_a1b2.",
        rawText:
          '<job-notification job_id="job_a1b2" event="watch" job_type="watch" status="watch" reason="output_match: ready" output_bytes="0" watch_id="watch_09QmWzRtNvxK">Matched output_match: ready on job_a1b2.</job-notification>',
      })}
    />,
  );
  expect(screen.getByTestId("notification-field-watch-id").textContent).toContain("watch_09QmWzRtNvxK");
  expect(screen.getByTestId("notification-field-job-id").textContent).toContain("job_a1b2");
  expect(screen.queryByTestId("notification-field-status")).toBeNull();
  expect(screen.queryByTestId("notification-field-reason")).toBeNull();
});

test("a delivery-failure card earns a warning chip and failure title", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "watch",
        title: "Watch delivery failed",
        tone: "warning",
        secondary: "watch send failed: delivery_id=wd_1: child unreachable",
        jobId: "job_a1b2",
        watchId: "watch_09QmWzRtNvxK",
        prose: "watch send failed: delivery_id=wd_1: child unreachable",
        rawText:
          '<job-notification job_id="job_a1b2" event="watch" job_type="watch" status="watch" reason="watch send failed: delivery_id=wd_1: child unreachable" output_bytes="0" watch_id="watch_09QmWzRtNvxK">watch send failed</job-notification>',
      })}
    />,
  );
  expect(screen.getByTestId("notification-card").getAttribute("data-tone")).toBe("warning");
  expect(screen.getByTestId("notification-card").textContent).toContain("warning");
  expect(screen.getByTestId("notification-card").textContent).toContain("Watch delivery failed");
});

test("two watch cards on the same job expand independently (combined review: disclosure key)", () => {
  // The disclosure identity prefers the watch id: two watches on one job
  // must not share a key, or expanding one expands both.
  const cardA = notif({
    type: "watch",
    title: "Output matched on job_a1b2",
    tone: "neutral",
    jobId: "job_a1b2",
    watchId: "watch_aaa",
    prose: "Matched output_match: ready on job_a1b2.",
    rawText: "raw-a",
  });
  const cardB = notif({
    type: "watch",
    title: "Output matched on job_a1b2",
    tone: "neutral",
    jobId: "job_a1b2",
    watchId: "watch_bbb",
    prose: "Matched output_match: done on job_a1b2.",
    rawText: "raw-b",
  });
  renderTools(
    <>
      <NotificationCard notification={cardA} />
      <NotificationCard notification={cardB} />
    </>,
  );
  const rows = screen.getAllByTestId("notification-card");
  expect(rows).toHaveLength(2);
  expect(screen.queryByTestId("notification-card-root")).toBeNull();
  fireEvent.click(rows[0]!);
  const roots = screen.getAllByTestId("notification-card-root");
  expect(roots).toHaveLength(1);
  expect(roots[0]!.textContent).toContain("ready");
});

test("a legacy watch card without a watch id still expands by job id", () => {
  renderTools(
    <NotificationCard
      notification={notif({
        type: "watch",
        title: "Output matched on job_a1b2",
        tone: "neutral",
        jobId: "job_a1b2",
        prose: "Matched output_match: ready on job_a1b2.",
        rawText: "raw-legacy",
      })}
    />,
  );
  expect(screen.queryByTestId("notification-card-root")).toBeNull();
  fireEvent.click(screen.getByTestId("notification-card"));
  expect(screen.getByTestId("notification-card-root").textContent).toContain("ready");
});

test("repeat firings of one watch expand independently (combined review: disclosure key)", () => {
  // Same watch id, different bodies: content joins identity so repeats do
  // not share a disclosure key (timer repeats previously diverged via
  // rawText; the watchId-first key regressed them into one).
  const first = notif({
    type: "watch",
    title: "Timer fired",
    tone: "neutral",
    watchId: "w1",
    prose: "Timer fired (every 300s).",
    rawText: "raw-first",
  });
  const second = notif({
    type: "watch",
    title: "Timer fired",
    tone: "neutral",
    watchId: "w1",
    prose: "Timer fired (every 300s), 3 times since your last turn.",
    rawText: "raw-second",
  });
  renderTools(
    <>
      <NotificationCard notification={first} />
      <NotificationCard notification={second} />
    </>,
  );
  const rows = screen.getAllByTestId("notification-card");
  expect(rows).toHaveLength(2);
  fireEvent.click(rows[0]!);
  expect(screen.getAllByTestId("notification-card-root")).toHaveLength(1);
});

test("byte-identical repeat frames expand independently via disclosure id (L2)", () => {
  // Same watch id AND same raw text (a repeated delivery renders the same
  // frame twice): without a per-delivery discriminator the two cards share
  // one disclosure key and toggle together.
  const repeat = (rawText: string) =>
    notif({
      type: "watch",
      title: "Timer fired",
      tone: "neutral",
      watchId: "w1",
      prose: "Timer fired (every 300s).",
      rawText,
    });
  const shared =
    '<job-notification job_id="" event="watch" status="watch" reason="repeat" output_bytes="0" watch_id="w1">Timer fired (every 300s).</job-notification>';
  renderTools(
    <>
      <NotificationCard notification={repeat(shared)} disclosureId="item_1:0" />
      <NotificationCard notification={repeat(shared)} disclosureId="item_1:1" />
    </>,
  );
  const rows = screen.getAllByTestId("notification-card");
  expect(rows).toHaveLength(2);
  fireEvent.click(rows[0]!);
  expect(screen.getAllByTestId("notification-card-root")).toHaveLength(1);
});
