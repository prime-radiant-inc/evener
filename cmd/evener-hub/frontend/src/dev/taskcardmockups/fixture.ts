// Fixed, production-shaped fixtures for the inline task-list card rework
// mock-ups (taskcardmockups.html). Each scenario is exactly the wire truth
// the real card consumes: `argumentsJSON` is the task_list call's own
// arguments, `state` is the authoritative Task[] snapshot that rides
// item.raw (snake_case json tags from agent/task/task_store.go), and
// `output` carries the daemon's Progress footer verbatim. Timestamps are
// fixed ISO strings - a dev harness gets no live clocks either.
//
// Dev-support scaffolding for a design decision, not production code.

import type { EvenerUsage, ItemModel, ThreadModel, TurnModel } from "@evener/appwire-client";
import { makeTranscriptPreviewModel } from "../../transcriptDisplay/previewFixture";

const T13H40 = "2026-09-23T13:40:00.000Z";
const T14H12 = "2026-09-23T14:12:00.000Z";
const T14H20 = "2026-09-23T14:20:00.000Z";
const T14H40 = "2026-09-23T14:40:00.000Z";
const NOW = "2026-09-23T14:58:00.000Z";
const USAGE: EvenerUsage = { inputTokens: 128, outputTokens: 64, totalTokens: 192 };

// The note THIS call added (task 2's second note, appended server-side).
const FRESH_NOTE = "Folded names the single latest touch; the open body is the window: settled, working, next.";
// Task 2's stale note, from an earlier call: the CARD must not render it
// (the fresh-only rule); the PANE legitimately shows it as the task's
// latest note, so the wording has to read naturally on both surfaces.
const OLD_NOTE = "Scouted the fold contract and the row grammar before deciding anything.";

/** agent/task/task_store.go's Task json shape, snake_case as it rides the wire. */
interface WireTask {
  id: number;
  type: string;
  description: string;
  prompt: string;
  status: "open" | "in_progress" | "done" | "cancelled";
  notes?: string[];
  started?: boolean;
  created_at?: string;
  updated_at?: string;
  completed_at?: string;
}

/** One task_list call's full evidence: the agent's intent, the call's own
 * arguments, the authoritative final state, and the daemon's output. */
export interface Scenario {
  intent: string;
  argumentsJSON: string;
  state: WireTask[];
  output: string;
}

// The main scenario every variant renders: task 2 completes with a fresh
// note, the daemon auto-advances task 3 to in_progress, tasks 4-5 stay open.
export const MAIN: Scenario = {
  intent: "Recording the format decision and starting the build",
  argumentsJSON: JSON.stringify({ action: "update", updates: [{ id: 2, status: "done", notes: FRESH_NOTE }] }),
  state: [
    {
      id: 1,
      type: "research",
      description: "Study the current card, fold contract, and design tokens",
      prompt: "",
      status: "done",
      notes: [],
      created_at: T13H40,
      updated_at: T14H20,
      completed_at: T14H20,
    },
    {
      id: 2,
      type: "implement",
      description: "Decide the folded line's semantics",
      prompt: "",
      status: "done",
      notes: [OLD_NOTE, FRESH_NOTE],
      created_at: T14H12,
      updated_at: NOW,
      completed_at: NOW,
    },
    {
      id: 3,
      type: "implement",
      description: "Build the mock-up harness with real components",
      prompt: "",
      status: "in_progress",
      started: true,
      notes: [OLD_NOTE],
      created_at: T14H12,
      updated_at: NOW,
    },
    {
      id: 4,
      type: "verify",
      description: "Review the variants with Jesse",
      prompt: "",
      status: "open",
      notes: [],
      created_at: T14H12,
      updated_at: T14H12,
    },
    {
      id: 5,
      type: "implement",
      description: "Implement the chosen design behind tests",
      prompt: "",
      status: "open",
      notes: [],
      created_at: T14H12,
      updated_at: T14H12,
    },
  ],
  output: "Progress: 2 done, 0 cancelled, 3 remaining (5 total)",
};

// The main scenario's twin without any fresh note: identical state and
// aggregate, but this call added no note, so no variant may render one.
export const MAIN_NOFRESH: Scenario = {
  ...MAIN,
  argumentsJSON: JSON.stringify({ action: "update", updates: [{ id: 2, status: "done" }] }),
  state: MAIN.state.map((task) =>
    task.id === 2 ? { ...task, notes: [OLD_NOTE], updated_at: NOW, completed_at: NOW } : task,
  ),
};

// A two-task list with nothing settled yet: the window is current + next
// only, proving the "at most three" clause shrinks gracefully.
export const SMALL: Scenario = {
  intent: "Starting the ship task on a two-task list",
  argumentsJSON: JSON.stringify({
    action: "update",
    updates: [
      {
        id: 6,
        status: "in_progress",
        notes: "One straight path: register the descriptors, drive TranscriptBody with fixtures.",
      },
    ],
  }),
  state: [
    {
      id: 6,
      type: "implement",
      description: "Ship the chosen design",
      prompt: "",
      status: "in_progress",
      started: true,
      notes: [],
      created_at: T14H12,
      updated_at: NOW,
    },
    {
      id: 7,
      type: "fix",
      description: "Retire the harness once merged",
      prompt: "",
      status: "open",
      notes: [],
      created_at: T14H12,
      updated_at: T14H12,
    },
  ],
  output: "Progress: 0 done, 0 cancelled, 2 remaining (2 total)",
};

// Everything done: the window holds only the most recently completed task,
// and the aggregate reads "All 3 tasks done" with a full meter.
export const ALLDONE: Scenario = {
  intent: "Closing out the last open task",
  argumentsJSON: JSON.stringify({ action: "update", updates: [{ id: 10, status: "done" }] }),
  state: [
    {
      id: 8,
      type: "research",
      description: "Audit the current card's a11y",
      prompt: "",
      status: "done",
      notes: [],
      created_at: T13H40,
      updated_at: T14H12,
      completed_at: T14H12,
    },
    {
      id: 9,
      type: "verify",
      description: "Typecheck the harness against the real item shape",
      prompt: "",
      status: "done",
      notes: [],
      created_at: T14H12,
      updated_at: T14H40,
      completed_at: T14H40,
    },
    {
      id: 10,
      type: "implement",
      description: "Write the production tests",
      prompt: "",
      status: "done",
      notes: [],
      created_at: T14H12,
      updated_at: NOW,
      completed_at: NOW,
    },
  ],
  output: "Progress: 3 done, 0 cancelled, 0 remaining (3 total)",
};

// The most recent settle is a cancellation, not a completion: the window's
// first slot holds the cancelled task (struck), the auto-started task takes
// the in-progress slot, and the fresh note rides the cancelled row.
export const CANCELLED: Scenario = {
  intent: "Dropping the rebalancing idea and moving on",
  argumentsJSON: JSON.stringify({
    action: "update",
    updates: [
      {
        id: 12,
        status: "cancelled",
        notes: "Dropped: the store keeps one in-progress task by design; rebalancing is a non-goal.",
      },
    ],
  }),
  state: [
    {
      id: 11,
      type: "research",
      description: "Audit the current card's a11y",
      prompt: "",
      status: "done",
      notes: [],
      created_at: T13H40,
      updated_at: T14H12,
      completed_at: T14H12,
    },
    {
      id: 12,
      type: "implement",
      description: "Support rebalancing multiple in-progress tasks",
      prompt: "",
      status: "cancelled",
      notes: ["Dropped: the store keeps one in-progress task by design; rebalancing is a non-goal."],
      created_at: T14H12,
      // The terminal stamp the store mints on every settle (done or
      // cancelled) - wire-true since the store began stamping settles.
      completed_at: NOW,
      updated_at: NOW,
    },
    {
      id: 13,
      type: "implement",
      description: "Write the production tests",
      prompt: "",
      status: "in_progress",
      started: true,
      notes: [],
      created_at: T14H12,
      updated_at: NOW,
    },
    {
      id: 14,
      type: "implement",
      description: "Ship behind the merge gate",
      prompt: "",
      status: "open",
      notes: [],
      created_at: T14H12,
      updated_at: T14H12,
    },
  ],
  output: "Progress: 1 done, 1 cancelled, 2 remaining (4 total)",
};

// An append whose new task lands BEYOND the window: "next" is the first open
// task in list order, so the fresh addition never enters the three-task
// window - only the folded line names it. Included deliberately as a design
// discussion point, not an accident.
export const APPEND: Scenario = {
  intent: "Capturing a late idea without disturbing the window",
  argumentsJSON: JSON.stringify({
    action: "append",
    tasks: [{ type: "research", description: "A late idea: tint fresh notes per theme", prompt: "" }],
  }),
  state: [
    {
      id: 16,
      type: "implement",
      description: "Scaffold the harness page and sections",
      prompt: "",
      status: "in_progress",
      started: true,
      notes: [],
      created_at: T14H12,
      updated_at: NOW,
    },
    {
      id: 17,
      type: "implement",
      description: "Wire the theme flip around each section",
      prompt: "",
      status: "open",
      notes: [],
      created_at: T14H12,
      updated_at: T14H12,
    },
    {
      id: 18,
      type: "research",
      description: "A late idea: tint fresh notes per theme",
      prompt: "",
      status: "open",
      notes: [],
      created_at: NOW,
      updated_at: NOW,
    },
  ],
  output: "Progress: 0 done, 0 cancelled, 3 remaining (3 total)",
};

/** A task_list tool-call item carrying one scenario's full evidence. */
export function scenarioItem(id: string, toolName: string, scenario: Scenario, turnId: string): ItemModel {
  return {
    id,
    turnId,
    type: "commandExecution",
    toolName,
    callId: `call_${id}`,
    text: "",
    status: "completed",
    startedAt: NOW,
    completedAt: NOW,
    description: scenario.intent,
    argumentsJSON: scenario.argumentsJSON,
    output: scenario.output,
    raw: scenario.state,
  };
}

/** A read_file item, for the baseline section's realistic run around the card. */
export function readItem(id: string, turnId: string, path: string, intent: string, excerpt: string): ItemModel {
  return {
    id,
    turnId,
    type: "commandExecution",
    toolName: "read_file",
    callId: `call_${id}`,
    text: "",
    status: "completed",
    startedAt: NOW,
    completedAt: NOW,
    description: intent,
    argumentsJSON: JSON.stringify({ file_path: path, offset: 1, limit: 40 }),
    output: excerpt,
  };
}

/** A one-turn ThreadModel: the section's user message, then its items. */
export function modelForSection(ref: string, turn: { id: string; userText: string; items: ItemModel[] }): ThreadModel {
  const userItem: ItemModel = {
    id: `${turn.id}-user`,
    turnId: turn.id,
    type: "userMessage",
    text: turn.userText,
    status: "completed",
    startedAt: T13H40,
    completedAt: T13H40,
  };
  const turnModel: TurnModel = {
    id: turn.id,
    status: "completed",
    startedAt: T13H40,
    completedAt: NOW,
    durationMs: 2400,
    usage: { ...USAGE },
    cost: "0.0125",
    items: [userItem, ...turn.items],
  };
  // The preview base carries the wire-true ThreadModel shape
  // (previewFixture, the same base the surface-sections harness builds on);
  // only what a mock-up section actually varies is overridden.
  return {
    ...makeTranscriptPreviewModel(),
    ref,
    name: "Task card mock-ups",
    failedToolCalls: 0,
    supportsReasoning: false,
    createdAt: T13H40,
    updatedAt: NOW,
    turns: [turnModel],
  };
}
