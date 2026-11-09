// Shared builders and fixtures for the reducer test files.

import type { ThreadModel } from "../model";
import { applyNotification } from "../reducer";
import { hydrateThread } from "../transcriptHistory";
import type {
  HistoryUpdatedParams,
  InputItem,
  QueueState,
  SandboxEscalationRequested,
  Thread,
  ThreadCapabilities,
  Turn,
} from "../types.gen";

export const CAPABILITIES: ThreadCapabilities = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  changeVisionModel: true,
  queue: true,
  goal: true,
  sharedNotes: true,
  rename: true,
};

export type TestThreadOverrides = Omit<Partial<Thread>, "evener"> & {
  evener?: Partial<Omit<Thread["evener"], "queue">> & { queue?: Partial<QueueState> };
};

export function testThread(overrides: TestThreadOverrides = {}): Thread {
  const { evener, ...threadOverrides } = overrides;
  return {
    id: "thr_t",
    sessionId: "sess_t",
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "evener",
    evener: {
      ref: "ref_t",
      capabilities: CAPABILITIES,
      ...evener,
      queue: { revision: 0, ...evener?.queue },
    },
    ...threadOverrides,
  };
}

export function testHydrate(overrides: TestThreadOverrides = {}): ThreadModel {
  const thread = testThread(overrides);
  return hydrateThread({ thread }, thread.evener.ref, 1000);
}

// testHydrate's v6 twin: adds `snapshot`, so a fold that follows can be a
// live history/updated (item/completed's and turn/completed's read-model
// replacement) — testHydrate's own plain hydrate has no snapshot, so its
// model.history is never established and a live history/updated on it can
// only ever invalidate (reducer.ts's classifySignal), never merge. Kept
// separate from testHydrate itself rather than added there: most of this
// file's tests build a turn's FULL item set directly through the fixture
// (never folding a live history/updated at all), and mergeHistory orders
// items lacking an explicit `position` by inserting each new one ahead of
// same-position (i.e. still-undefined) siblings — switching every
// testHydrate call to v6 reorders any of those fixtures that hydrate 2+
// undated items in one turn, a regression this split avoids.
// applyHistory folds one history/updated notification onto `model`, carrying
// this file's standard baseline identity (threadId "thr_t", ref "ref_t",
// bootGeneration "", epoch 1, snapshot incarnation "inc-1" length 1) plus the
// caller's distinct content — items or turns — and any baseline field that
// differs. The optional arrival time passes straight through to
// applyNotification.
export function applyHistory(model: ThreadModel, params: Partial<HistoryUpdatedParams>, at: number): ThreadModel {
  return applyNotification(
    model,
    {
      method: "history/updated",
      params: {
        threadId: "thr_t",
        ref: "ref_t",
        bootGeneration: "",
        epoch: 1,
        snapshot: { incarnation: "inc-1", length: 1 },
        ...params,
      },
    },
    at,
  );
}

export function testHydrateV6(overrides: TestThreadOverrides = {}): ThreadModel {
  const thread = testThread(overrides);
  return hydrateThread(
    { thread, bootGeneration: "", epoch: 1, snapshot: { incarnation: "inc-1", length: 1 } },
    thread.evener.ref,
    1000,
  );
}

export function fragmentTurn(id: string, itemIds: string[], overrides: Partial<Turn> = {}): Turn {
  return {
    id,
    status: "completed",
    itemsView: "fragment",
    items: itemIds.map((itemId) => ({
      id: itemId,
      turnId: id,
      type: "agentMessage",
      text: `${id}-${itemId}`,
      status: "completed",
    })),
    ...overrides,
  };
}

export function positionedFragmentTurn(
  id: string,
  items: Array<readonly [string, number]>,
  overrides: Partial<Turn> = {},
): Turn {
  const turn = fragmentTurn(
    id,
    items.map(([itemId]) => itemId),
    overrides,
  );
  return {
    ...turn,
    items: items.map(([itemId, entry], item) => ({
      id: itemId,
      turnId: id,
      type: "agentMessage",
      text: `${id}-${itemId}`,
      status: "completed",
      position: { entry, item },
    })),
  };
}

export function testEscalation(overrides: Partial<SandboxEscalationRequested> = {}): SandboxEscalationRequested {
  return {
    threadId: "thr_t",
    ref: "ref_t",
    escalationId: "esc_1",
    mode: "exempt_denied_path",
    tool: "write_file",
    kind: "file_tool",
    deniedPath: "/etc/passwd",
    ...overrides,
  };
}

export const SHA_IMAGE = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";

export function hydrateWithShaImage(imageOverrides: Partial<InputItem> = {}): ThreadModel {
  // The default fixture's wire session ("sess_t") differs from its ref
  // ("ref_t") on purpose: the sha route must be built from the serving
  // session, never the ref — a stable workspace alias (e.g. local:stable)
  // names no Past.Find entry and its /s/{ref}/images/{sha} 404s.
  const turns: Turn[] = [
    {
      id: "turn_1",
      status: "completed",
      itemsView: "full",
      items: [
        {
          type: "userMessage",
          id: "item_user",
          turnId: "turn_1",
          text: "look at this",
          status: "completed",
          images: [{ type: "image", name: "photo.png", metadata: { sha: SHA_IMAGE }, ...imageOverrides }],
        },
      ],
    },
  ];
  return testHydrate({ turns });
}
