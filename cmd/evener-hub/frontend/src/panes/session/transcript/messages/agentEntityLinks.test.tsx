import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { StrictMode } from "react";
import { afterEach, expect, test } from "vitest";
import type { ActivityJob, ActivityTree } from "../../../../protocol/activityData";
import { buildEntityView, type EntityView } from "../../../../protocol/entityView";
import type { ThreadModel } from "../../../../protocol/model";
import { resetWorkspaceStoreForTests, workspaceStore } from "../../../../shell/workspace";
import { TranscriptRenderProvider } from "../../../../transcriptDisplay/renderContext";
import "../../../doc";
import "../../index";
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

function agentMessage(markdown: string, resolved: ReadonlyMap<string, EntityView> = entities) {
  return (
    <TranscriptRenderProvider thread={thread} entities={resolved}>
      <AgentMessageItem
        item={{ id: "agent", turnId: turn.id, type: "agentMessage", text: markdown, pendingText: [markdown] }}
        turn={turn}
        sessionRef={thread.ref}
        live={false}
      />
    </TranscriptRenderProvider>
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
