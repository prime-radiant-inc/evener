// Test-only builders shared by this subpath's suites (pendingEntries.test.ts,
// pendingTurns.test.ts): a fully-populated, typed ThreadModel a test can
// override the one or two fields it cares about on, rather than each suite
// hand-rolling (or casting past) the shape.

import type { ThreadModel } from "../../model";

export function threadModel(overrides: Partial<ThreadModel> = {}): ThreadModel {
  const { jobsTreeRevision = null, ...rest } = overrides;
  return {
    ref: "ref_a",
    threadId: "thread_a",
    name: "",
    status: { type: "active" },
    modelProvider: "",
    model: "",
    visionModel: "",
    askPending: false,
    pendingEscalations: [],
    turns: [],
    queue: { revision: 1 },
    tasks: null,
    jobsUpdatedAt: null,
    lastFrameAt: 0,
    capabilities: {} as ThreadModel["capabilities"],
    goal: null,
    humanNote: "",
    agentNote: "",
    sessionUrls: [],
    contextUsed: 0,
    contextWindow: 0,
    contextPressure: 0,
    usage: null,
    workMillis: 0,
    reasoningEffortLevels: [],
    supportsReasoning: false,
    cwd: "",
    createdAt: "",
    updatedAt: "",
    ...rest,
    jobsTreeRevision,
  };
}
