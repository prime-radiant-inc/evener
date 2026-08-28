import type { ItemModel, ThreadModel, TurnModel } from "../protocol/model";
import type { EvenerUsage, ThreadItemEventKind } from "../protocol/types.gen";

const THREAD_ID = "preview-thread";
const TURN_ID = "preview-turn";
const TIMESTAMP = "2026-08-25T18:00:00.000Z";
const FRAME_TIME = 1787680800000;
const USAGE: EvenerUsage = { inputTokens: 128, outputTokens: 64, totalTokens: 192 };

function item(id: string, type: string, text: string, overrides: Partial<ItemModel> = {}): ItemModel {
  return {
    id,
    turnId: TURN_ID,
    type,
    text,
    status: "completed",
    startedAt: TIMESTAMP,
    completedAt: TIMESTAMP,
    ...overrides,
  };
}

function systemItem(
  id: string,
  eventKind: ThreadItemEventKind,
  text: string,
  overrides: Partial<ItemModel> = {},
): ItemModel {
  return item(id, "systemMessage", text, { eventKind, ...overrides });
}

function previewTurn(): TurnModel {
  return {
    id: TURN_ID,
    status: "completed",
    startedAt: TIMESTAMP,
    completedAt: TIMESTAMP,
    durationMs: 2400,
    usage: { ...USAGE },
    cost: "0.0125",
    items: [
      item("preview-user", "userMessage", "Inspect the transcript display flow"),
      item("preview-tool-success", "commandExecution", "", {
        toolName: "read_file",
        callId: "preview-call-success",
        description: "Inspect the transcript display flow",
        argumentsJSON: '{"path":"src"}',
        output: "src/transcriptDisplay",
        exitCode: 0,
      }),
      item("preview-tool-failure", "commandExecution", "", {
        toolName: "shell",
        callId: "preview-call-failure",
        description: "Run the focused checks",
        argumentsJSON: '{"command":"npm test"}',
        error: "exit status 1",
        exitCode: 1,
      }),
      item("preview-reasoning", "reasoning", "I will inspect the display projection and its test coverage.", {
        reasoningSummaries: [["I will inspect the display projection and its test coverage."]],
      }),
      item("preview-agent", "agentMessage", "The transcript display flow is ready."),
      systemItem("preview-environment", "environment", "Working tree environment is ready.", {
        raw: { source: "preview", level: "low" },
      }),
      systemItem("preview-system-prompt", "system_prompt", "System prompt loaded for the example."),
      systemItem("preview-prompt-loaded", "prompt_loaded", "Prompt loaded for the example."),
      systemItem("preview-hook-success", "hook_completed", "Preview hook completed.", {
        exitCode: 0,
        raw: { hook: "preview", durationMs: 120 },
      }),
      systemItem("preview-hook-failure", "hook_completed", "Preview hook failed.", {
        exitCode: 2,
        raw: { hook: "preview", durationMs: 80 },
      }),
    ],
  };
}

/**
 * Fixed, production-shaped data for Settings transcript examples.
 *
 * The object is assembled inside the factory instead of being shared as a
 * module-level mutable fixture, so a preview projector can safely annotate or
 * otherwise transform its input without leaking state between cards/tests.
 */
export function makeTranscriptPreviewModel(): ThreadModel {
  return {
    ref: "preview:transcript",
    threadId: THREAD_ID,
    name: "Transcript display example",
    status: { type: "idle" },
    modelProvider: "preview",
    model: "preview-model",
    reasoningEffort: "medium",
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns: [previewTurn()],
    queue: null,
    tasks: null,
    jobsUpdatedAt: null,
    jobsTreeRevision: null,
    lastFrameAt: FRAME_TIME,
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
    contextUsed: 192,
    contextWindow: 4096,
    contextPressure: 0.047,
    usage: { ...USAGE },
    cost: "0.0125",
    failedToolCalls: 1,
    workMillis: 2400,
    reasoningEffortLevels: ["low", "medium", "high"],
    supportsReasoning: true,
    cwd: "/workspace/preview",
    createdAt: TIMESTAMP,
    updatedAt: TIMESTAMP,
  };
}
