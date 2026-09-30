import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { ActivityJob, ActivityTree, EvenerDelegateInfo, ItemModel } from "@evener/appwire-client";
import { buildEntityView, makeTranscriptDisplayConfig, type ParsedNotification } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactElement } from "react";
import { afterEach, beforeAll, expect, test, vi } from "vitest";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { navigationStore } from "../../../../stores/navigation/store";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import { resetDisclosureStoreForTests } from "../../../../widgets/disclosure/disclosureStore";
import { FailureGlyph } from "../../../../widgets/failureglyph";
import { ToolIcon } from "../../../../widgets/toolicon";
import { NotificationCard } from "./NotificationCard";

beforeAll(async () => {
  await import("../../");
});

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
  resetDisclosureStoreForTests();
  navigationStore.setState({ resources: new Map() });
});

function seedLocation(ref: string, topLevelRef: string) {
  const key = { kind: "location", ref } as const;
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: {
      generation_id: "generation_test",
      revision: 1,
      ref,
      top_level_ref: topLevelRef,
      top_level: ref === topLevelRef,
    },
    loadedRevision: 1,
    targetRevision: null,
    forceToken: 0,
    etag: "etag",
    loading: false,
    stale: false,
    error: null,
    generationID: "generation_test",
  });
  navigationStore.setState({ resources });
}

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

// The session's entity map, built the way useEntityView builds it: the shell
// job from the retained tree, the watch its job_watch item folds to, and the
// live delegate record. A notification's identity fields name these entities,
// so they resolve to cards through this map.
const ENTITY_JOB = "job_02wMz5TxvEMoJEDTDGOTil_000000000123";
const ENTITY_WATCH = "watch_034KEfjYFbfoUaPeHJcLXY";
const ENTITY_DELEGATE = "dlg_034HQ2kSDXfKFq1mm3idL1";

function notificationEntities() {
  const job: ActivityJob = {
    jobId: ENTITY_JOB,
    ownerSessionId: "02wMz5TxvEMoJEDTDGOTil",
    ownerRef: "local:s",
    type: "shell",
    status: "completed",
    outcome: "success",
    terminal: true,
    background: false,
    hasOutput: true,
    description: "Compile the frontend",
    command: "npm run build",
    startedAt: "2026-09-13T20:00:00Z",
    endedAt: "2026-09-13T20:00:01Z",
    exitCode: 0,
    outputBytes: 12,
  };
  const tree: ActivityTree = {
    revision: 1,
    root: {
      kind: "session",
      sessionId: "02wMz5TxvEMoJEDTDGOTil",
      ref: "local:s",
      label: "root",
      aggregate: "completed",
      counts: { active: 0, failed: 0, completed: 1, complete: true },
      entries: [{ kind: "shell", job }],
      branch: {},
    },
  };
  const watch: ItemModel = {
    id: "watch-item",
    turnId: "turn_1",
    position: { entry: 1, item: 1 },
    type: "commandExecution",
    text: "",
    toolName: "job_watch",
    argumentsJSON: JSON.stringify({ operation: "inspect", watch_id: ENTITY_WATCH }),
    output: "",
    raw: {
      watch_id: ENTITY_WATCH,
      watching: true,
      source: ENTITY_JOB,
      condition: "output_match: ready",
      deliveries: 2,
    },
    status: "completed",
  };
  const delegate: EvenerDelegateInfo = {
    delegateId: ENTITY_DELEGATE,
    ownerSessionId: "02wMz5TxvEMoJEDTDGOTil",
    rootSessionId: "02wMz5TxvEMoJEDTDGOTil",
    childSessionId: "child",
    transcriptRef: "local:child",
    type: "delegate",
    lifecycle: "running",
    phase: "running",
    status: "running",
    resumable: true,
    needsAttention: false,
    projectionRevision: 1,
    task: "Review the first line",
  };
  return buildEntityView({
    sessionRef: "local:s",
    tree,
    delegates: [delegate],
    turns: [{ id: "turn_1", status: "completed", items: [watch] }],
    stale: false,
    ended: false,
  });
}

// The activity-level default (the config the plain renders above use, where
// the card auto-expands so its fields are visible) plus the entity map.
function renderWithEntities(node: ReactElement) {
  return render(
    <TranscriptRenderProvider surface="readOnly" disclosureScope="nc:entities" entities={notificationEntities()}>
      {node}
    </TranscriptRenderProvider>,
  );
}

test("renders the title and tags the tone", () => {
  render(<NotificationCard notification={notif()} />);
  expect(screen.getByText("Job completed")).toBeTruthy();
  expect(screen.getByTestId("notification-card").getAttribute("data-tone")).toBe("success");
});

// The head leads with a status glyph seated in the transcript's icon rail
// (the same seat ToolRow's kind icon and ThinkBlock's bulb occupy). Shape
// carries the status for the quiet tones; ERROR renders FailureGlyph - the
// red cross that IS the failure signal now that the error chip is gone.
// One table maps every tone to the glyph it must draw.
const TONE_GLYPH_PROBES = [
  { tone: "success", probe: <ToolIcon kind="check" /> },
  { tone: "warning", probe: <ToolIcon kind="alert" /> },
  { tone: "neutral", probe: <ToolIcon kind="info" /> },
  { tone: "error", probe: <FailureGlyph /> },
] as const;

function renderedIconPath(root: ParentNode | null): string | null {
  return root?.querySelector("path")?.getAttribute("d") ?? null;
}

test.each(TONE_GLYPH_PROBES)(
  "a $tone head leads with the tone's glyph, before any chip or title text",
  ({ tone, probe }) => {
    render(<NotificationCard notification={notif({ tone })} />);
    const head = screen.getByTestId("notification-card");
    const statusIcon = head.querySelector('[data-testid="notification-status-icon"]');
    expect(statusIcon, `tone ${tone} rendered no status icon`).toBeTruthy();
    // First child of the head: the rail slot precedes the chip and title.
    expect(head.firstElementChild).toBe(statusIcon);
    // The glyph is the tone's own shape - the shared line-art widget for the
    // quiet tones, the FailureGlyph cross for error.
    const kindProbe = render(probe);
    expect(renderedIconPath(statusIcon)).toBe(renderedIconPath(kindProbe.container));
    // One cleanup unmounts every root rendered this iteration (card + probe);
    // without it the next iteration's queries would match two cards.
    cleanup();
  },
);

test("the status icon is decorative: aria-hidden, no accessible name of its own", () => {
  render(<NotificationCard notification={notif()} />);
  expect(screen.getByTestId("notification-status-icon").getAttribute("aria-hidden")).toBe("true");
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
  // A delegate card carries no echo metadata: identity lives on the head (the
  // entity trigger) and in the daemon's own frame, so the expanded body
  // renders neither a delegate-id nor a status field.
  expect(screen.getByTestId("notification-card").textContent).toContain("dlg_42");
  expect(screen.queryByText(/delegate id/i)).toBeNull();
  expect(screen.queryByText(/job id/i)).toBeNull();
  expect(screen.queryByTestId("notification-field-status")).toBeNull();

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

// The redesigned delegate card (mockups 24-delegate-complete): the head names
// the delegate and the outcome; the body is the report - the message as a
// sans-serif bubble and the structured result as a table - with no echo
// metadata row and no raw disclosure (the hover card carries identity, and
// the daemon's frame keeps the verbatim wire text).
test("a delegate report renders its message as a bubble and its structured result as a table", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        tone: "success",
        name: "task2-review",
        secondary: "task2-review",
        delegateId: ENTITY_DELEGATE,
        message: "Non-null assertions removed; no new breakage found.",
        structuredResult: {
          finding_verdict: "ADDRESSED",
          fix_round: "All findings addressed, no new Critical/Important breakage",
          new_breakage: "None",
          out_of_scope: "None",
          tests_rerun: "false",
          artifacts: [],
        },
        structuredResultValid: true,
        rawText: `<delegate-notification delegate_id="${ENTITY_DELEGATE}">packet</delegate-notification>`,
      })}
    />,
  );
  const root = screen.getByTestId("notification-card-root");
  expect(screen.getByTestId("notification-message").textContent).toContain("Non-null assertions removed");
  const table = screen.getByTestId("notification-structured-result");
  expect(table.querySelectorAll("tr")).toHaveLength(6);
  expect(table.querySelector("th")?.textContent).toBe("Finding verdict");
  expect(table.querySelectorAll("td")[0]?.textContent).toBe("ADDRESSED");
  expect(table.textContent).toContain("Fix round");
  expect(table.textContent).toContain("All findings addressed, no new Critical/Important breakage");
  expect(table.textContent).toContain("Tests rerun");
  // An empty array renders as an explicit none, not an empty cell.
  expect(table.textContent).toContain("(none)");
  // No echo metadata, no raw disclosure: neither survives the redesign.
  expect(screen.queryByTestId("notification-field-delegate-id")).toBeNull();
  expect(screen.queryByTestId("notification-field-status")).toBeNull();
  expect(root.querySelector("details")).toBeNull();
});

test("a delegate report without a structured result shows its message and no table", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        tone: "success",
        name: "parser-rename",
        secondary: "parser-rename",
        delegateId: "dlg_p",
        message: "Renamed the expand helpers; all 214 tests in the package pass.",
      })}
    />,
  );
  expect(screen.getByTestId("notification-message").textContent).toContain("Renamed the expand helpers");
  expect(screen.queryByTestId("notification-structured-result")).toBeNull();
  expect(screen.queryByTestId("notification-structured-note")).toBeNull();
});

// A result the daemon refused to capture or validate is a fact the caller
// needs (they asked for a schema): a quiet ink-low note, never a table of
// rows that failed their schema, and never a red banner.
test("a structured result that failed its verdict shows a quiet note, never a table", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        tone: "success",
        name: "task2-review",
        secondary: "task2-review",
        delegateId: "dlg_t",
        message: "Sweep complete; see the findings.",
        structuredResultValid: false,
        structuredResultReason: "schema_result_too_large",
      })}
    />,
  );
  expect(screen.getByTestId("notification-structured-note").textContent).toBe("Structured result too large to show.");
  expect(screen.queryByTestId("notification-structured-result")).toBeNull();
});

// The daemon accepts structured results up to a megabyte
// (delegatestore.MaxTerminalStructuredResultBytes), so the table needs the
// bound the message already has (MESSAGE_MAX): a many-keyed or long-valued
// result renders a bounded window, never thousands of rows in one mount.
test("a structured result is bounded as the message is", () => {
  const many: Record<string, string> = {};
  for (let i = 1; i <= 120; i += 1) many[`key_${i}`] = `v${i}`;
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        tone: "success",
        name: "bulk-review",
        secondary: "bulk-review",
        delegateId: "dlg_b",
        message: "Report.",
        structuredResult: many,
        structuredResultValid: true,
      })}
    />,
  );
  const table = screen.getByTestId("notification-structured-result");
  expect(table.querySelectorAll("tr")).toHaveLength(101); // 100 rows + the more-row
  expect(table.textContent).toContain("(+20 more rows)");
});

test("a single very long structured value renders truncated", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        tone: "success",
        name: "blob-report",
        secondary: "blob-report",
        delegateId: "dlg_l",
        message: "Report.",
        structuredResult: { blob: "x".repeat(5000) },
        structuredResultValid: true,
      })}
    />,
  );
  const cell = screen.getByTestId("notification-structured-result").querySelector("td");
  expect(cell?.textContent?.length ?? 0).toBeLessThanOrEqual(2001);
  expect(cell?.textContent).toContain("…");
});

// A valid-but-empty result is not a report: the row renders static rather
// than expanding to a body whose only block (the table) renders nothing.
test("a valid but empty structured result renders no expandable empty body", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        tone: "success",
        name: "empty-report",
        secondary: "empty-report",
        delegateId: "dlg_e",
        structuredResult: {},
        structuredResultValid: true,
      })}
    />,
  );
  expect(screen.getByTestId("notification-card").closest("details")).toBeNull();
});

// A machinery stop has no report: the head's ending says everything, so the
// row renders as a static line - no chevron, no expandable empty body.
test("a machinery stop renders its ending on a static head with nothing to expand", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        tone: "warning",
        title: "Delegate stopped",
        name: "docs-index",
        secondary: "docs-index · stopped by its coordinator",
        delegateId: "dlg_d",
        reason: "stopped by its coordinator",
        ending: "stopped by its coordinator",
      })}
    />,
  );
  const head = screen.getByTestId("notification-card");
  expect(head.textContent).toContain("Delegate stopped");
  expect(head.textContent).toContain("docs-index");
  expect(head.textContent).toContain("stopped by its coordinator");
  expect(head.closest("details")).toBeNull();
  expect(screen.queryByTestId("notification-chevron")).toBeNull();
  expect(screen.queryByTestId("notification-card-root")).toBeNull();
});

// An unlabeled delegate (no name, description, or id) still has an ending and
// an Open control: the static head's secondary cannot be gated on the label
// alone, or a frame with a transcript_ref loses both.
test("an unlabeled static delegate keeps its ending and Open control", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        tone: "warning",
        title: "Delegate stopped",
        secondary: "stopped by its coordinator",
        delegateId: undefined,
        name: undefined,
        transcriptRef: "local:child",
      })}
    />,
  );
  const head = screen.getByTestId("notification-card");
  expect(head.textContent).toContain("stopped by its coordinator");
  expect(screen.getByRole("button", { name: "Open subagent" })).toBeTruthy();
  expect(head.closest("details")).toBeNull();
});

// A validated result with no record shape (a top-level array or scalar schema)
// renders through the card's value grammar rather than vanishing.
test("a validated non-record result renders through the value grammar", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        tone: "success",
        name: "corpus-sweep",
        secondary: "corpus-sweep",
        delegateId: "dlg_a",
        structuredResult: ["alpha", "beta"],
        structuredResultValid: true,
      })}
    />,
  );
  expect(screen.getByTestId("notification-structured-json").textContent).toContain("alpha, beta");
});

// A schema key can be as long as its author likes; the label cell stays
// bounded as a value cell is, with an ellipsis past the bound.
test("a very long result key renders a bounded label", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        tone: "success",
        name: "long-key",
        secondary: "long-key",
        delegateId: "dlg_l",
        structuredResult: { ["k".repeat(3000)]: "value" },
        structuredResultValid: true,
      })}
    />,
  );
  const label = document.querySelector('[data-testid="notification-structured-result"] th')?.textContent ?? "";
  expect(label.length).toBe(2001);
  expect(label.endsWith("…")).toBe(true);
});

// A validated explicit null result is present: the card says "(none)" through
// the value grammar rather than rendering nothing.
test("a validated null result renders as none", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        tone: "success",
        name: "null-report",
        secondary: "null-report",
        delegateId: "dlg_n",
        structuredResult: null,
        structuredResultValid: true,
      })}
    />,
  );
  expect(screen.getByTestId("notification-structured-json").textContent).toContain("(none)");
});

// A legacy attribute frame's `reason` is a raw producer code (exit_nonzero):
// the head composes the packet's `ending` words, never the code.
test("a legacy failed frame's raw reason code never reaches the head", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        tone: "error",
        title: "Delegate failed",
        name: "split-retry",
        secondary: "split-retry",
        delegateId: "dlg_2",
        reason: "exit_nonzero",
        message: "The retry loop needs a fixed seed.",
      })}
    />,
  );
  const head = screen.getByTestId("notification-card");
  expect(head.textContent).toContain("split-retry");
  expect(head.textContent).not.toContain("exit_nonzero");
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

test("a success/neutral notification recedes: no tone chip (color spent on the warning chip and the failure glyph)", () => {
  render(<NotificationCard notification={notif({ tone: "success" })} />);
  expect(screen.queryByText("error")).toBe(null);
  expect(screen.queryByText("warning")).toBe(null);
});

test("an error notification carries no chip: the failure glyph is the whole signal", () => {
  render(<NotificationCard notification={notif({ tone: "error" })} />);
  expect(screen.queryByText("error")).toBeNull();
  expect(screen.getByTestId("failure-glyph")).toBeTruthy();
});

test("the secondary line surfaces the demoted metadata", () => {
  render(<NotificationCard notification={notif({ secondary: "shell · exit 2 · boom" })} />);
  // The trailing word renders inside the tail unit (atomic with the chevron),
  // so assert on the row's whole text rather than a single text node.
  expect(screen.getByTestId("notification-card").textContent).toContain("shell · exit 2 · boom");
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
  // A delegate row that carries a report renders the disclosure head; a
  // machinery stop is a static line with no Open grammar to pin, so these
  // fixtures speak as reported runs.
  render(
    <NotificationCard
      notification={notif({ type: "delegate", transcriptRef: "local:child", message: "Report complete." })}
    />,
  );
  const button = screen.getByRole("button", { name: "Open subagent" });
  expect(button.textContent).toBe(""); // the one icon-only form: no visible words
  await user.click(button);
  const opened = workspaceStore.getState().panes.find((pane) => pane.type === "transcript");
  expect(opened?.params).toEqual({ ref: "local:child" });
  expect(opened?.slot).toBe("secondary");
});

// A real daemon frame carries delegate_id and name only
// (agent/delegate_delivery.go delegateNotificationContent) — never a
// transcript_ref — so the frame alone leaves the Open control dead on every
// live subagent report. The card resolves the subagent through the session's
// entity map by delegate id, the resolution the phone already makes (#3075).
test("a real delegate frame without a transcript_ref opens the subagent resolved by delegate id", async () => {
  workspaceStore.setState({
    panes: [{ id: "main", type: "session", params: { ref: "local:s" }, slot: "main" }],
    focusedPaneId: "main",
  });
  const user = userEvent.setup();
  renderWithEntities(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        secondary: ENTITY_DELEGATE,
        delegateId: ENTITY_DELEGATE,
        rawText: `<delegate-notification delegate_id="${ENTITY_DELEGATE}">done</delegate-notification>`,
      })}
      sessionRef="local:s"
    />,
  );
  await user.click(screen.getByRole("button", { name: "Open subagent" }));
  const opened = workspaceStore.getState().panes.find((pane) => pane.type === "transcript");
  expect(opened?.params).toEqual({ ref: "local:child", parentRef: "local:s" });
});

test("a delegate frame whose delegate id the entity map cannot answer shows no dead Open control", () => {
  renderWithEntities(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        secondary: "dlg_unknown",
        delegateId: "dlg_unknown",
        rawText: '<delegate-notification delegate_id="dlg_unknown">done</delegate-notification>',
      })}
    />,
  );
  expect(screen.queryByRole("button", { name: "Open subagent" })).toBeNull();
});

test("binds Open to the final notification text fragment instead of permitting a lone control line", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        secondary:
          "Inspect the complete delegated implementation and verify every browser geometry invariant before reporting",
        transcriptRef: "local:child",
        message: "Report complete.",
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

// A collapsed card gives no other expand affordance (the native details marker
// is suppressed), so the summary owes the reader the same trailing chevron
// every other disclosure row in the transcript shows (ThinkBlock, ToolRow).
test("the summary shows a trailing disclosure chevron that turns when the card opens", () => {
  renderTools(<NotificationCard notification={notif({ tone: "neutral", title: "explorer finished" })} />);
  const chevron = screen.getByTestId("notification-chevron");
  expect(chevron.getAttribute("aria-hidden")).toBe("true");
  expect(chevron.getAttribute("data-open")).toBe("false");
  fireEvent.click(screen.getByTestId("notification-card"));
  expect(screen.getByTestId("notification-chevron").getAttribute("data-open")).toBe("true");
});

// The app's row grammar (ToolRow's): the chevron rides INLINE at the end of the
// words it opens, after the Open control and inside the same atomic tail unit —
// never a flex sibling that could strand alone on a wrapped line.
test("with a transcript ref the chevron rides inside the atomic tail unit after the Open control", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "delegate",
        secondary: "Inspect the workspace",
        transcriptRef: "local:child",
        message: "Report complete.",
      })}
    />,
  );
  const chevron = screen.getByTestId("notification-chevron");
  const button = screen.getByRole("button", { name: "Open subagent" });
  const openTrailing = button.parentElement?.parentElement;
  const secondaryTail = openTrailing?.parentElement;
  expect(chevron.parentElement).toBe(secondaryTail);
  const tailChildren = [...(secondaryTail?.children ?? [])];
  expect(tailChildren.indexOf(openTrailing!)).toBeLessThan(tailChildren.indexOf(chevron));
});

// The chevron never strands: it rides inside the atomic tail unit with the
// words it opens, so when the line runs out the WHOLE unit wraps - never the
// glyph alone (the same guarantee the Open control has; roborev #1143).
test("with a title-only card the chevron rides the title's atomic tail unit", () => {
  renderTools(<NotificationCard notification={notif({ secondary: undefined, transcriptRef: undefined })} />);
  const chevron = screen.getByTestId("notification-chevron");
  const tail = chevron.parentElement;
  // The title splits like the secondary does: leading words as their own span,
  // the FINAL word and the chevron as one atomic unit, so a line that runs out
  // moves the word and the glyph together - never the glyph alone.
  expect(tail?.previousElementSibling?.textContent).toBe("Job ");
  expect(tail?.textContent).toContain("completed");
  expect(tail?.parentElement?.textContent).toContain("Job completed");
  expect(chevron.previousElementSibling?.textContent).toBe("completed");
  const tailChildren = [...(tail?.children ?? [])];
  expect(tailChildren.indexOf(chevron)).toBe(tailChildren.length - 1);
});

test("with a secondary and no transcript ref the chevron rides the secondary's atomic tail unit", () => {
  renderTools(
    <NotificationCard
      notification={notif({ secondary: "Inspect the workspace and report back", transcriptRef: undefined })}
    />,
  );
  const chevron = screen.getByTestId("notification-chevron");
  const tail = chevron.parentElement;
  // The unit carries the secondary's FINAL word with the chevron: the two can
  // move to the next line together, never the chevron alone.
  expect(tail?.textContent).toContain("back");
  expect(tail?.parentElement?.textContent).toContain("Inspect the workspace and report");
  const tailChildren = [...(tail?.children ?? [])];
  expect(tailChildren.indexOf(chevron)).toBe(tailChildren.length - 1);
  // Same discriminator as the title-only case: the secondary's one element
  // child is the trailing-word unit, not a loose chevron after the text.
  expect(tail?.parentElement?.children.length).toBe(1);
});

test("opening a child restores the notification owner as main when an unrelated session is focused", async () => {
  seedLocation("local:owner", "local:owner");
  workspaceStore.setState({
    panes: [
      { id: "unrelated", type: "session", params: { ref: "local:unrelated" }, slot: "main" },
      { id: "owner", type: "session", params: { ref: "local:owner" }, slot: "secondary" },
    ],
    focusedPaneId: "unrelated",
  });
  const user = userEvent.setup();
  render(
    <NotificationCard
      notification={notif({ type: "delegate", transcriptRef: "local:child", message: "Report complete." })}
      sessionRef="local:owner"
    />,
  );

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
  render(
    <NotificationCard
      notification={notif({ type: "delegate", transcriptRef: "remote:child", message: "Report complete." })}
    />,
  );
  await user.click(screen.getByRole("button", { name: "Open subagent" }));
  expect(workspaceStore.getState().panes.find((pane) => pane.type === "transcript")?.params).toEqual({
    ref: "remote:child",
  });
});

test("missing and malformed refs have no dead open-subagent action", () => {
  for (const ref of [undefined, "", "child", "local:child:extra", "local:bad..child"]) {
    const { unmount } = render(<NotificationCard notification={notif({ type: "delegate", transcriptRef: ref })} />);
    expect(screen.queryByRole("button", { name: "Open subagent" })).toBeNull();
    unmount();
  }
});

// A shell job's notification block carries the producer's read_transcript ref
// ("job:<id>", agent/job_notify.go's jobTranscriptRef). That ref opens the
// job-log surface, not a subagent transcript, so the head's subagent control
// never shows for it (the job log opens from the card's job-id trigger and
// the activity tree).
test("a shell job notification shows no open-in-a-new-panel affordance", () => {
  render(<NotificationCard notification={notif({ jobType: "shell", transcriptRef: "job:job_x" })} />);
  expect(screen.queryByRole("button", { name: "Open subagent" })).toBeNull();
});

// The head's Open control is subagent-only: no job-notification type ever
// shows it, whatever ref the frame carries (the gate lives in NotificationCard).
test.each(["job", "watch", "watch-send"] as const)(
  "a %s notification never shows the open-subagent affordance",
  (type) => {
    render(
      <NotificationCard
        notification={notif({ type, secondary: "Run the bounded test set", transcriptRef: "local:child" })}
      />,
    );
    expect(screen.queryByRole("button", { name: "Open subagent" })).toBeNull();
  },
);

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

test("an excerpt is shown as escaped text (never live HTML)", async () => {
  const _user = userEvent.setup();
  // The parser hands the excerpt already decoded to plain text (issue #3086);
  // the card renders it as text, so no script element is ever created.
  render(<NotificationCard notification={notif({ excerpt: "<script>alert(1)</script>" })} />);
  // At activity level the card auto-expands (expandByDefault=true).
  expect(screen.getByText("<script>alert(1)</script>")).toBeTruthy();
  expect(document.querySelector("script")).toBe(null);
});

// kata 77sf: the parser (steeringNotifications.ts) decodes the producer's
// escaping once and hands the card plain text (issue #3086), so the card
// renders exactly what it is given — including delimiters and entity-looking
// text — as escaped text. The decode round trip itself is pinned in
// steeringNotifications.test.ts.
test("kata 77sf: a delimiter-bearing excerpt renders verbatim as escaped text", async () => {
  const _user = userEvent.setup();
  const original = 'before & after </job-notification> <script>&lt;already-escaped&gt;</script> "quoted"';
  render(<NotificationCard notification={notif({ excerpt: original })} />);
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

test("a timer's prose renders with no echo metadata and no tone chip", () => {
  render(
    <NotificationCard
      notification={notif({
        type: "watch",
        title: "Watch triggered",
        tone: "neutral",
        // The parser already decoded the producer's escaping (issue #3086).
        prose: "Timer fired (every 300s).\nNote: hello <x>",
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

// The delegate card's identity is its head's name: the transcript's shared
// card trigger (mockups 24-delegate-complete), so the id never needs a body
// row. It must genuinely RESOLVE: EntityRef falls back to a bare span whenever
// the map cannot answer, which would look identical to a text-only assertion
// while rendering no card.
test("a delegate card's head name is the entity card trigger, and its body renders no identity fields", () => {
  renderWithEntities(
    <NotificationCard
      notification={notif({
        type: "delegate",
        title: "Delegate completed",
        name: "task1-review",
        secondary: "task1-review",
        delegateId: ENTITY_DELEGATE,
        rawText: `<delegate-notification delegate_id="${ENTITY_DELEGATE}">done</delegate-notification>`,
      })}
    />,
  );

  const head = screen.getByTestId("notification-card");
  expect(within(head).getByTestId("entity-trigger").textContent).toBe("task1-review");
  expect(screen.queryByTestId("notification-field-delegate-id")).toBeNull();
  expect(screen.queryByTestId("notification-field-job-id")).toBeNull();
});

test("the head's delegate name opens the delegate hover card on focus", () => {
  vi.useFakeTimers();
  try {
    renderWithEntities(
      <NotificationCard
        notification={notif({
          type: "delegate",
          title: "Delegate completed",
          tone: "success",
          name: "task1-review",
          secondary: "task1-review",
          delegateId: ENTITY_DELEGATE,
          message: "Done.",
        })}
      />,
    );

    fireEvent.focus(within(screen.getByTestId("notification-card")).getByTestId("entity-trigger"));
    act(() => {
      vi.advanceTimersByTime(300);
    });

    const card = screen.getByRole("tooltip");
    expect(card.textContent).toContain("Delegate");
    expect(card.textContent).toContain("Review the first line");
  } finally {
    vi.useRealTimers();
  }
});

// The trigger rides inside the clickable head, so a click on the name must
// open the delegate's card, never toggle the notification's own disclosure.
test("clicking the head's delegate trigger does not toggle the disclosure", () => {
  render(
    <TranscriptRenderProvider
      config={toolsConfig}
      surface="readOnly"
      disclosureScope="nc:tools"
      entities={notificationEntities()}
    >
      <NotificationCard
        notification={notif({
          type: "delegate",
          title: "Delegate completed",
          tone: "success",
          name: "task1-review",
          secondary: "task1-review",
          delegateId: ENTITY_DELEGATE,
          message: "Done.",
        })}
      />
    </TranscriptRenderProvider>,
  );

  fireEvent.click(within(screen.getByTestId("notification-card")).getByTestId("entity-trigger"));
  expect(screen.queryByTestId("notification-card-root")).toBeNull();

  fireEvent.click(screen.getByTestId("notification-card"));
  expect(screen.getByTestId("notification-card-root")).not.toBeNull();
});

test("a watch card's watch and job identity fields are entity card triggers", () => {
  renderWithEntities(
    <NotificationCard
      notification={notif({
        type: "watch",
        title: `Output matched on ${ENTITY_JOB}`,
        tone: "neutral",
        jobId: ENTITY_JOB,
        watchId: ENTITY_WATCH,
        prose: `Matched output_match: ready on ${ENTITY_JOB}.`,
        rawText: `<job-notification job_id="${ENTITY_JOB}" event="watch" job_type="watch" status="watch" reason="output_match: ready" output_bytes="0" watch_id="${ENTITY_WATCH}">Matched.</job-notification>`,
      })}
    />,
  );

  expect(within(screen.getByTestId("notification-field-watch-id")).getByTestId("entity-trigger").textContent).toBe(
    ENTITY_WATCH,
  );
  expect(within(screen.getByTestId("notification-field-job-id")).getByTestId("entity-trigger").textContent).toBe(
    ENTITY_JOB,
  );
});

test("an identity field's trigger opens the entity card", () => {
  vi.useFakeTimers();
  try {
    renderWithEntities(<NotificationCard notification={notif({ jobId: ENTITY_JOB, jobType: "shell" })} />);

    fireEvent.focus(within(screen.getByTestId("notification-field-job-id")).getByTestId("entity-trigger"));
    act(() => {
      vi.advanceTimersByTime(300);
    });

    const card = screen.getByRole("tooltip");
    expect(card.textContent).toContain("Job");
    expect(card.textContent).toContain("Compile the frontend");
  } finally {
    vi.useRealTimers();
  }
});

// The other half of the contract: an id the session's map cannot answer for
// keeps reading as an ordinary value, with no dead trigger and no open control.
test("an id the entity map cannot resolve keeps rendering as plain text", () => {
  renderWithEntities(
    <NotificationCard notification={notif({ jobId: "job_02wMz5TxvEMoJEDTDGOTil_000000000999", jobType: "shell" })} />,
  );

  const field = screen.getByTestId("notification-field-job-id");
  expect(field.textContent).toContain("job_02wMz5TxvEMoJEDTDGOTil_000000000999");
  expect(within(field).queryByTestId("entity-trigger")).toBeNull();
  expect(within(field).queryByRole("button")).toBeNull();
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
  // The parser synthesizes prose in plain text (issue #3086): a pattern
  // literally containing "&lt;" is already the literal text the card shows,
  // never "<".
  render(
    <NotificationCard
      notification={notif({
        type: "watch",
        title: "Output matched on job_a1b2",
        tone: "neutral",
        prose: "Matched output_match: a &lt; b on job_a1b2.",
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
