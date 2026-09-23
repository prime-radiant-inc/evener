import type { ActivityJob, ActivityTree, ItemModel, TurnModel } from "@evener/appwire-client";
import { buildEntityView, type EntityView } from "@evener/appwire-client";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { navigationStore } from "../../../stores/navigation/store";
import { TranscriptRenderProvider } from "../../../transcriptDisplay/renderContext";
import { EntityRef } from "./EntityRef";
import { delegateView } from "./entityView.testFixture";

beforeAll(async () => {
  await import("../");
});

beforeEach(() => {
  resetWorkspaceStoreForTests();
  navigationStore.setState({ resources: new Map() });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

function advance(ms: number) {
  act(() => {
    vi.advanceTimersByTime(ms);
  });
}

function jobView(
  id = "job_x",
  description = "Compile the frontend",
  state: { stale?: boolean; ended?: boolean } = {},
  overrides: Partial<ActivityJob> = {},
): EntityView {
  const job: ActivityJob = {
    ownerSessionId: "s",
    ownerRef: "local:s",
    type: "shell",
    status: "completed",
    outcome: "success",
    terminal: true,
    background: false,
    hasOutput: true,
    description,
    command: "npm run build",
    startedAt: "2026-09-13T20:00:00Z",
    endedAt: "2026-09-13T20:00:01Z",
    exitCode: 0,
    outputBytes: 12,
    ...overrides,
    jobId: id,
  };
  const tree: ActivityTree = {
    revision: 1,
    root: {
      kind: "session",
      sessionId: "s",
      ref: "local:s",
      label: "root",
      aggregate: "completed",
      counts: { active: 0, failed: 0, completed: 1, complete: true },
      entries: [{ kind: "shell", job }],
      branch: {},
    },
  };
  const view = buildEntityView({
    sessionRef: "local:s",
    tree,
    turns: [],
    stale: state.stale ?? false,
    ended: state.ended ?? false,
  }).get(id);
  if (!view) throw new Error("expected job fixture to resolve");
  return view;
}

function watchView(
  id = "watch_x",
  raw: Record<string, unknown> = {
    watch_id: id,
    watching: true,
    source: "job_x",
    condition: "output_match: ready",
    deliveries: 2,
  },
): EntityView {
  const item: ItemModel = {
    id: "watch-item",
    turnId: "turn-1",
    position: { entry: 1, item: 1 },
    type: "commandExecution",
    text: "",
    toolName: "job_watch",
    argumentsJSON: '{"operation":"inspect","watch_id":"watch_x"}',
    output: "",
    raw,
    status: "completed",
  };
  const turns: TurnModel[] = [{ id: "turn-1", status: "completed", items: [item] }];
  const view = buildEntityView({ sessionRef: "local:s", turns, stale: false, ended: false }).get(id);
  if (!view) throw new Error("expected watch fixture to resolve");
  return view;
}

function focusCard() {
  fireEvent.focus(screen.getByTestId("entity-trigger"));
  advance(300);
  return screen.getByRole("tooltip");
}

test("unresolved renders plain text", () => {
  render(<EntityRef view={undefined} id="dlg_missing" />);

  expect(screen.getByText("dlg_missing")).toBeTruthy();
  expect(screen.queryByTestId("entity-trigger")).toBeNull();
  expect(screen.queryByRole("button")).toBeNull();
  expect(screen.queryByRole("tooltip")).toBeNull();
});

test("navigable renders exactly one OpenButton that opens the pane", () => {
  render(<EntityRef view={jobView()} id="job_x" />);

  const buttons = screen.getAllByRole("button");
  expect(buttons).toHaveLength(1);
  fireEvent.click(buttons[0]!);

  const transcriptPanes = workspaceStore.getState().panes.filter((pane) => pane.type === "transcript");
  expect(transcriptPanes).toHaveLength(1);
  expect(transcriptPanes[0]).toMatchObject({
    type: "transcript",
    params: { ref: "job:job_x", parentRef: "local:s" },
  });
});

test("EntityRef reuses an already-open exact transcript pane", () => {
  const workspace = workspaceStore.getState();
  const owner = workspace.openPane("session", { ref: "local:s" });
  const exact = workspace.openPane("transcript", { ref: "job:job_x", parentRef: "local:s" });
  const unrelated = workspace.openPane("transcript", { ref: "local:other" });
  workspace.focusPane(unrelated);
  render(<EntityRef view={jobView()} id="job_x" />);

  fireEvent.click(screen.getByRole("button", { name: "Open job log" }));

  const transcriptPanes = workspaceStore
    .getState()
    .panes.filter((pane) => pane.type === "transcript" && (pane.params as { ref?: unknown }).ref === "job:job_x");
  expect(transcriptPanes).toHaveLength(1);
  expect(transcriptPanes[0]?.id).toBe(exact);
  expect(workspaceStore.getState().mainPane()?.id).toBe(owner);
  expect(workspaceStore.getState().focusedPaneId).toBe(exact);
  expect(workspaceStore.getState().panes.some((pane) => pane.id === unrelated)).toBe(true);
});

test("delegate renders exactly one OpenButton for its open target", () => {
  render(<EntityRef view={delegateView()} id="dlg_x" />);

  expect(screen.getAllByRole("button", { name: "Open delegate transcript" })).toHaveLength(1);
});

test("triggerOnly keeps the resolved hover-card trigger and omits its OpenButton", () => {
  vi.useFakeTimers();
  render(<EntityRef view={delegateView()} id="dlg_x" triggerOnly />);

  expect(screen.getByTestId("entity-trigger").tabIndex).toBe(0);
  expect(screen.queryByRole("button")).toBeNull();
  expect(focusCard().textContent).toContain("Delegate");
});

test("embedded keeps the resolved hover card while omitting the trigger tab stop", () => {
  vi.useFakeTimers();
  render(<EntityRef view={watchView()} id="watch_x" embedded />);

  const trigger = screen.getByTestId("entity-trigger");
  expect(trigger.getAttribute("tabindex")).toBeNull();
  expect(screen.queryByRole("button")).toBeNull();
  fireEvent.mouseEnter(trigger);
  advance(300);
  expect(screen.getByRole("tooltip").textContent).toContain("Watch");
});

test("the id trigger does not navigate", () => {
  render(<EntityRef view={jobView()} id="job_x" />);

  fireEvent.click(screen.getByTestId("entity-trigger"));
  expect(workspaceStore.getState().panes).toEqual([]);
});

test("watch renders a card trigger with no OpenButton", () => {
  vi.useFakeTimers();
  render(<EntityRef view={watchView()} id="watch_x" />);

  expect(screen.getByTestId("entity-trigger").tabIndex).toBe(0);
  expect(screen.queryByRole("button")).toBeNull();
  const card = focusCard();
  expect(card.textContent).toContain("watching");
  expect(card.textContent).toContain("ready");
  expect(card.textContent).toContain("2 deliveries");
  expect(card.textContent).toContain("job_x");
  expect(card.textContent).toContain("last-known");
});

test("watch card renders a trigger and its note separately", () => {
  vi.useFakeTimers();
  render(
    <EntityRef
      view={watchView("watch_noted", {
        watch_id: "watch_noted",
        watching: true,
        source: "job_x",
        condition: "output_match: ready; note: Preserve the release context",
        note: "Preserve the release context",
      })}
      id="watch_noted"
    />,
  );

  const card = focusCard();
  // The card words its trigger through the shared composer, so a pattern reads
  // exactly as it does in a watch list row: quoted, with no label of its own.
  expect(card.textContent).toContain("“ready”");
  expect(card.textContent).not.toContain("output matching");
  const noteLabel = within(card).getByText("Note");
  expect(noteLabel.tagName).toBe("DT");
  expect(noteLabel.nextElementSibling?.textContent).toBe("Preserve the release context");
});

test("note-only watch card renders note content without raw condition grammar", () => {
  vi.useFakeTimers();
  render(
    <EntityRef
      view={watchView("watch_note_only", {
        watch_id: "watch_note_only",
        watching: true,
        source: "job_x",
        condition: "note: Follow up after the release",
      })}
      id="watch_note_only"
    />,
  );

  const card = focusCard();
  expect(within(card).getByText("Follow up after the release")).toBeTruthy();
  expect(card.textContent).not.toContain("note: Follow up after the release");
});

test("watch card without a trigger or note has no summary content", () => {
  vi.useFakeTimers();
  render(
    <EntityRef
      view={watchView("watch_empty", {
        watch_id: "watch_empty",
        watching: true,
        source: "job_x",
      })}
      id="watch_empty"
    />,
  );

  const card = focusCard().querySelector('[data-entity-kind="watch"]');
  expect(card?.children).toHaveLength(2);
});

test("wildcard watch card renders any event with the watch-list cadence wording", () => {
  vi.useFakeTimers();
  render(
    <EntityRef
      view={watchView("watch_wildcard", {
        watch_id: "watch_wildcard",
        watching: true,
        source: "job_x",
        condition: "events: [*] every 3",
      })}
      id="watch_wildcard"
    />,
  );

  expect(focusCard().textContent).toContain("any event (every 3)");
});

test("shows the card after the shared focus delay, describes both controls, and hides immediately", () => {
  vi.useFakeTimers();
  render(<EntityRef view={jobView()} id="job_x" />);
  const trigger = screen.getByTestId("entity-trigger");
  const openButton = screen.getByRole("button", { name: "Open job log" });

  expect(trigger.getAttribute("aria-describedby")).toBeNull();
  expect(openButton.getAttribute("aria-describedby")).toBeNull();
  fireEvent.focus(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
  advance(299);
  expect(screen.queryByRole("tooltip")).toBeNull();
  advance(1);

  const card = screen.getByRole("tooltip");
  expect(card.parentElement).toBe(document.body);
  expect(trigger.getAttribute("aria-describedby")).toBe(card.id);
  expect(openButton.getAttribute("aria-describedby")).toBe(card.id);
  expect(card.querySelector("button, a, input, select, textarea, [tabindex]")).toBeNull();

  fireEvent.blur(trigger, { relatedTarget: openButton });
  fireEvent.focus(openButton, { relatedTarget: trigger });
  expect(screen.getByRole("tooltip")).toBe(card);
  expect(openButton.getAttribute("aria-describedby")).toBe(card.id);

  fireEvent.blur(openButton);
  expect(screen.queryByRole("tooltip")).toBeNull();
  expect(trigger.getAttribute("aria-describedby")).toBeNull();
  expect(openButton.getAttribute("aria-describedby")).toBeNull();
});

test("focusing the OpenButton reveals the entity card after the shared delay", () => {
  vi.useFakeTimers();
  render(<EntityRef view={jobView()} id="job_x" />);

  fireEvent.focus(screen.getByRole("button", { name: "Open job log" }));
  expect(screen.queryByRole("tooltip")).toBeNull();
  advance(299);
  expect(screen.queryByRole("tooltip")).toBeNull();
  advance(1);

  expect(screen.getByRole("tooltip").textContent).toContain("Compile the frontend");
});

test("job card states an exited-nonzero run as Command failed", () => {
  vi.useFakeTimers();
  const view = jobView(
    "job_cen",
    "Compile the frontend",
    {},
    {
      status: "command_exited_nonzero",
      outcome: "failure",
      exitCode: 2,
    },
  );
  render(<EntityRef view={view} id="job_cen" />);

  fireEvent.focus(screen.getByTestId("entity-trigger"));
  advance(300);

  const card = screen.getByRole("tooltip");
  expect(card.textContent).toContain("Command failed");
  expect(card.textContent).not.toContain("command_exited_nonzero");
});

test("job card states a signal-killed run as Command killed", () => {
  vi.useFakeTimers();
  const view = jobView(
    "job_kil",
    "Compile the frontend",
    {},
    {
      status: "command_killed",
      outcome: "failure",
    },
  );
  render(<EntityRef view={view} id="job_kil" />);

  fireEvent.focus(screen.getByTestId("entity-trigger"));
  advance(300);

  const card = screen.getByRole("tooltip");
  expect(card.textContent).toContain("Command killed");
  expect(card.textContent).not.toContain("command_killed");
});

test("job card joins a legacy failed record to the display word by its reason", () => {
  vi.useFakeTimers();
  const view = jobView(
    "job_legacy",
    "Compile the frontend",
    {},
    {
      status: "failed",
      outcome: "failure",
      reason: "exit_nonzero",
      exitCode: 2,
    },
  );
  render(<EntityRef view={view} id="job_legacy" />);

  fireEvent.focus(screen.getByTestId("entity-trigger"));
  advance(300);

  expect(screen.getByRole("tooltip").textContent).toContain("Command failed");
});

test("falls back to the shared render-context entity map", () => {
  vi.useFakeTimers();
  const view = jobView("job_context", "From context");
  render(
    <TranscriptRenderProvider entities={new Map([["job_context", view]])}>
      <EntityRef id="job_context" />
    </TranscriptRenderProvider>,
  );

  expect(screen.getAllByRole("button", { name: "Open job log" })).toHaveLength(1);
  expect(focusCard().textContent).toContain("From context");
});

test("an explicit view takes precedence over the context entity map", () => {
  vi.useFakeTimers();
  const explicit = jobView("job_same", "Explicit view");
  const contextual = jobView("job_same", "Context view");
  render(
    <TranscriptRenderProvider entities={new Map([["job_same", contextual]])}>
      <EntityRef id="job_same" view={explicit} />
    </TranscriptRenderProvider>,
  );

  const text = focusCard().textContent;
  expect(text).toContain("Explicit view");
  expect(text).not.toContain("Context view");
});

test("job card carries normalized detail plus a stale caption", () => {
  vi.useFakeTimers();
  render(<EntityRef view={jobView("job_old", "Archived compile", { stale: true })} id="job_old" />);

  const text = focusCard().textContent;
  expect(text).toContain("completed");
  expect(text).toContain("shell");
  expect(text).toContain("npm run build");
  expect(text).toContain("1s");
  expect(text).toContain("exit 0");
  expect(text).toContain("12 bytes");
  expect(text).toContain("stale");
});

test("delegate card carries status, mandate, agent/model, duration, and usage", () => {
  vi.useFakeTimers();
  render(<EntityRef view={delegateView()} id="dlg_x" />);

  const text = focusCard().textContent;
  expect(text).toContain("running");
  expect(text).toContain("Review the first line");
  expect(text).toContain("reviewer");
  expect(text).toContain("gpt-test");
  expect(text).toContain("2s");
  expect(text).toContain("↑1k ↓300");
});

// A blank resolvedModel is absence, not a model name: the card falls through
// to the next name the projection carries.
test("delegate card skips a blank resolved model", () => {
  vi.useFakeTimers();
  render(
    <EntityRef
      view={delegateView("dlg_blank_model", {}, { resolvedModel: "  ", model: "fallback-model" })}
      id="dlg_blank_model"
    />,
  );

  expect(focusCard().textContent).toContain("fallback-model");
});

test("an ended running job shows ended without a live indicator or stale caption", () => {
  vi.useFakeTimers();
  render(
    <EntityRef
      view={jobView(
        "job_ended",
        "Retained job",
        { stale: true, ended: true },
        { status: "running", outcome: undefined, terminal: false, endedAt: undefined, exitCode: undefined },
      )}
      id="job_ended"
    />,
  );

  const card = focusCard();
  expect(card.textContent).toContain("ended");
  expect(card.textContent).not.toContain("stale");
  expect(card.textContent?.toLowerCase()).not.toContain("running");
  expect(card.querySelector('[data-state="running"]')).toBeNull();
});

test("a stale running job shows stale without a live indicator", () => {
  vi.useFakeTimers();
  render(
    <EntityRef
      view={jobView(
        "job_stale",
        "Retained job",
        { stale: true },
        { status: "running", outcome: undefined, terminal: false, endedAt: undefined, exitCode: undefined },
      )}
      id="job_stale"
    />,
  );

  const card = focusCard();
  expect(card.textContent).toContain("stale");
  expect(card.textContent?.toLowerCase()).not.toContain("running");
  expect(card.querySelector('[data-state="running"]')).toBeNull();
});

test("an ended delegate suppresses retained running and quiet indicators", () => {
  vi.useFakeTimers();
  render(
    <EntityRef
      view={delegateView("dlg_ended", { ended: true }, { status: "running", runningForMs: 9_000, quietForMs: 4_000 })}
      id="dlg_ended"
    />,
  );

  const text = focusCard().textContent?.toLowerCase();
  expect(text).toContain("ended");
  expect(text).not.toContain("stale");
  expect(text).not.toContain("running");
  expect(text).not.toContain("quiet");
});

test.each([
  ["missing", { watch_id: "watch_missing", watching: false }],
  ["ended", { watch_id: "watch_missing", watching: false, end_reason: "cleared" }],
])("a source-less %s watch does not claim this session as its source", (_state, raw) => {
  vi.useFakeTimers();
  render(<EntityRef view={watchView("watch_missing", raw)} id="watch_missing" />);

  const text = focusCard().textContent;
  expect(text).not.toContain("Source");
  expect(text).not.toContain("this session");
});
