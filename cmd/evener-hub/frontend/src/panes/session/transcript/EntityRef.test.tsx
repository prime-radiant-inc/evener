import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, expect, test, vi } from "vitest";
import type { ActivityJob, ActivityTree } from "../../../protocol/activityData";
import { buildEntityView, type EntityView } from "../../../protocol/entityView";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../shell/workspace";
import { navigationStore } from "../../../stores/navigation/store";
import { TranscriptRenderProvider } from "../../../transcriptDisplay/renderContext";
import { EntityRef } from "./EntityRef";

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
): EntityView {
  const job: ActivityJob = {
    jobId: id,
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

function watchView(id = "watch_x"): EntityView {
  const item: ItemModel = {
    id: "watch-item",
    turnId: "turn-1",
    position: { entry: 1, item: 1 },
    type: "commandExecution",
    text: "",
    toolName: "job_watch",
    argumentsJSON: '{"operation":"inspect","watch_id":"watch_x"}',
    output: "",
    raw: {
      watch_id: id,
      watching: true,
      source: "job_x",
      condition: "output_match: ready",
      deliveries: 2,
    },
    status: "completed",
  };
  const turns: TurnModel[] = [{ id: "turn-1", status: "completed", items: [item] }];
  const view = buildEntityView({ sessionRef: "local:s", turns, stale: false, ended: false }).get(id);
  if (!view) throw new Error("expected watch fixture to resolve");
  return view;
}

function delegateView(id = "dlg_x"): EntityView {
  const view = buildEntityView({
    sessionRef: "local:s",
    delegates: [
      {
        delegateId: id,
        ownerSessionId: "s",
        rootSessionId: "s",
        childSessionId: "child",
        transcriptRef: "local:child",
        type: "delegate",
        lifecycle: "running",
        phase: "running",
        status: "running",
        resumable: true,
        needsAttention: false,
        projectionRevision: 1,
        task: "Review the first line\nthen continue",
        agentType: "reviewer",
        resolvedModel: "gpt-test",
        runningForMs: 2_000,
        usage: { inputTokens: 1_200, outputTokens: 300 },
      },
    ],
    turns: [],
    stale: false,
    ended: false,
  }).get(id);
  if (!view) throw new Error("expected delegate fixture to resolve");
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

test("delegate renders exactly one OpenButton for its open target", () => {
  render(<EntityRef view={delegateView()} id="dlg_x" />);

  expect(screen.getAllByRole("button", { name: "Open delegate transcript" })).toHaveLength(1);
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

test("shows the card after the shared focus delay, describes the trigger, and hides immediately", () => {
  vi.useFakeTimers();
  render(<EntityRef view={jobView()} id="job_x" />);
  const trigger = screen.getByTestId("entity-trigger");

  fireEvent.focus(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
  advance(299);
  expect(screen.queryByRole("tooltip")).toBeNull();
  advance(1);

  const card = screen.getByRole("tooltip");
  expect(card.parentElement).toBe(document.body);
  expect(trigger.getAttribute("aria-describedby")).toBe(card.id);
  expect(card.querySelector("button, a, input, select, textarea, [tabindex]")).toBeNull();

  fireEvent.blur(trigger);
  expect(screen.queryByRole("tooltip")).toBeNull();
  expect(trigger.getAttribute("aria-describedby")).toBeNull();
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

test("job card carries normalized detail plus stale and ended captions", () => {
  vi.useFakeTimers();
  render(<EntityRef view={jobView("job_old", "Archived compile", { stale: true, ended: true })} id="job_old" />);

  const text = focusCard().textContent;
  expect(text).toContain("completed");
  expect(text).toContain("shell");
  expect(text).toContain("npm run build");
  expect(text).toContain("1s");
  expect(text).toContain("exit 0");
  expect(text).toContain("12 bytes");
  expect(text).toContain("stale");
  expect(text).toContain("ended");
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
