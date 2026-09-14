import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { StrictMode, useRef } from "react";
import { afterEach, expect, test } from "vitest";
import type { ActivityJob, ActivityTree } from "../../../../protocol/activityData";
import { buildEntityView, type EntityView } from "../../../../protocol/entityView";
import type { ThreadModel } from "../../../../protocol/model";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import "../../../doc";
import "../../index";
import { useEntityTextEnhancement } from "../EntityText";
import { AgentMessageItem } from "./AgentMessageItem";
import { UserMessageItem } from "./UserMessageItem";

const OWNER = "02wMz5TxvEMoJEDTDGOTil";
const JOB = `job_${OWNER}_000000000123`;
const UNRESOLVED = "dlg_02wMz5TxvEMoJEDTDGOTil";

const thread: ThreadModel = {
  ref: `local:${OWNER}`,
  threadId: OWNER,
  name: "Entity links",
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

function entityViews(): ReadonlyMap<string, EntityView> {
  const job: ActivityJob = {
    jobId: JOB,
    ownerSessionId: OWNER,
    ownerRef: thread.ref,
    type: "shell",
    status: "completed",
    outcome: "success",
    terminal: true,
    background: false,
    hasOutput: true,
    description: "Compile the frontend",
    command: "npm run build",
    startedAt: "2026-09-13T20:00:00Z",
    exitCode: 0,
    outputBytes: 12,
  };
  const tree: ActivityTree = {
    revision: 1,
    root: {
      kind: "session",
      sessionId: OWNER,
      ref: thread.ref,
      label: "root",
      aggregate: "completed",
      counts: { active: 0, failed: 0, completed: 1, complete: true },
      entries: [{ kind: "shell", job }],
      branch: {},
    },
  };
  return buildEntityView({ sessionRef: thread.ref, tree, turns: [], stale: false, ended: false });
}

const entities = entityViews();
const turn = { id: "turn", status: "completed" as const, items: [] };

function agentMessage(markdown: string, resolved: ReadonlyMap<string, EntityView> = entities, live = false) {
  return (
    <TranscriptRenderProvider thread={thread} entities={resolved}>
      <AgentMessageItem
        item={{ id: "agent", turnId: turn.id, type: "agentMessage", text: markdown, pendingText: [markdown] }}
        turn={turn}
        sessionRef={thread.ref}
        live={live}
      />
    </TranscriptRenderProvider>
  );
}

function ExistingEntityHostHarness() {
  const root = useRef<HTMLDivElement>(null);
  const portals = useEntityTextEnhancement(root, []);
  return (
    <>
      <div ref={root} data-testid="existing-host-root">
        <span data-entity-host data-testid="existing-entity-host">
          {JOB}
        </span>
        <span>{` outside ${JOB}`}</span>
      </div>
      {portals}
    </>
  );
}

function userMessage(text: string) {
  return (
    <TranscriptRenderProvider thread={thread} entities={entities}>
      <UserMessageItem
        item={{ id: "user", turnId: turn.id, type: "userMessage", text }}
        turn={turn}
        sessionRef={thread.ref}
        live={false}
      />
    </TranscriptRenderProvider>
  );
}

afterEach(() => {
  cleanup();
  resetWorkspaceStoreForTests();
});

test("an id in agent prose links and opens", () => {
  render(agentMessage(`Started ${JOB}.`));

  expect(screen.getByTestId("entity-trigger").textContent).toBe(JOB);
  fireEvent.click(screen.getByRole("button", { name: "Open job log" }));
  const transcriptPanes = workspaceStore.getState().panes.filter((pane) => pane.type === "transcript");
  expect(transcriptPanes).toMatchObject([{ type: "transcript", params: { ref: `job:${JOB}`, parentRef: thread.ref } }]);
});

test("ids in inline and fenced code are untouched", () => {
  const source = `\`${JOB}\` and\n\n\`\`\`\n${JOB}\n\`\`\``;
  const { container } = render(agentMessage(source));

  expect(screen.queryByTestId("entity-trigger")).toBeNull();
  expect(screen.queryByRole("button", { name: "Open job log" })).toBeNull();
  expect([...container.querySelectorAll("code")].map((node) => node.textContent)).toEqual([JOB, JOB]);
});

test("an id inside an existing Markdown link stays literal while a prose id links", () => {
  const source = `[${JOB}](README.md) and outside ${JOB}.`;
  const { container } = render(agentMessage(source));

  const link = screen.getByRole("link", { name: JOB });
  expect(link.textContent).toBe(JOB);
  expect(link.querySelector('[data-testid="entity-trigger"]')).toBeNull();
  expect(screen.getAllByTestId("entity-trigger")).toHaveLength(1);
  expect(container.querySelectorAll("[data-entity-host]")).toHaveLength(1);

  fireEvent.click(screen.getByRole("button", { name: "Open beside: README.md" }));
  expect(workspaceStore.getState().panes).toMatchObject([{ type: "doc", params: { path: "README.md" } }]);
});

test("stream growth and settlement keep one affordance per id occurrence", () => {
  const initial = `First ${JOB}.`;
  const final = `${initial} Then ${JOB}.`;
  const { container, rerender } = render(agentMessage(initial, entities, true));

  expect(screen.getByTestId("agent-bubble").textContent).toBe(`${initial}\n`);
  expect(screen.getAllByTestId("entity-trigger")).toHaveLength(1);
  expect(container.querySelectorAll("[data-entity-host]")).toHaveLength(1);

  rerender(agentMessage(final, entities, true));
  expect(screen.getByTestId("agent-bubble").textContent).toBe(`${final}\n`);
  expect(screen.getAllByTestId("entity-trigger")).toHaveLength(2);
  expect(container.querySelectorAll("[data-entity-host]")).toHaveLength(2);

  rerender(agentMessage(final));
  expect(screen.getByTestId("agent-bubble").textContent).toBe(`${final}\n`);
  expect(screen.getAllByTestId("entity-trigger")).toHaveLength(2);
  expect(container.querySelectorAll("[data-entity-host]")).toHaveLength(2);
});

test("an effect replay with unchanged source preserves visible text", () => {
  const source = `Before ${JOB}, after.`;
  const { container, rerender } = render(<StrictMode>{agentMessage(source)}</StrictMode>);
  const before = container.textContent;

  rerender(<StrictMode>{agentMessage(source)}</StrictMode>);

  expect(container.textContent).toBe(before);
  expect(container.querySelectorAll("[data-entity-host]")).toHaveLength(1);
  expect(screen.getAllByTestId("entity-trigger")).toHaveLength(1);
});

test("user message text links via the string form", () => {
  const text = `${JOB},${JOB}`;
  render(userMessage(text));

  expect(screen.getByTestId("user-bubble").textContent).toBe(text);
  expect(screen.getAllByTestId("entity-trigger")).toHaveLength(2);
  expect(screen.getAllByRole("button", { name: "Open job log" })).toHaveLength(2);
});

test("an unresolved id in prose stays plain text with no button", () => {
  const { container } = render(agentMessage(`Unknown ${UNRESOLVED}.`, new Map()));

  expect(container.textContent).toContain(UNRESOLVED);
  expect(screen.queryByTestId("entity-trigger")).toBeNull();
  expect(screen.queryByRole("button")).toBeNull();
});

test("an unresolved id gains one affordance without changing surrounding text", () => {
  const source = `Before ${JOB} after.`;
  const { container, rerender } = render(agentMessage(source, new Map()));
  const bubble = screen.getByTestId("agent-bubble");
  const before = bubble.textContent;

  expect(before).toBe(`${source}\n`);
  expect(screen.queryByTestId("entity-trigger")).toBeNull();
  expect(container.querySelectorAll("[data-entity-host]")).toHaveLength(1);

  rerender(agentMessage(source));
  expect(screen.getByTestId("agent-bubble").textContent).toBe(before);
  expect(screen.getAllByTestId("entity-trigger")).toHaveLength(1);
  expect(screen.getAllByRole("button", { name: "Open job log" })).toHaveLength(1);
  expect(container.querySelectorAll("[data-entity-host]")).toHaveLength(1);
});

test("a pre-existing entity host subtree is not wrapped", () => {
  render(
    <TranscriptRenderProvider thread={thread} entities={entities}>
      <ExistingEntityHostHarness />
    </TranscriptRenderProvider>,
  );

  const root = screen.getByTestId("existing-host-root");
  const existingHost = screen.getByTestId("existing-entity-host");
  expect(root.textContent).toBe(`${JOB} outside ${JOB}`);
  expect(existingHost.textContent).toBe(JOB);
  expect(existingHost.querySelector('[data-testid="entity-trigger"]')).toBeNull();
  expect(root.querySelectorAll("[data-entity-host]")).toHaveLength(2);
  expect(screen.getAllByTestId("entity-trigger")).toHaveLength(1);
});
