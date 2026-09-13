import { expect, test } from "vitest";
import { applyNotification, hydrateThread } from "./reducer";
import type { Thread, ThreadCapabilities } from "./types.gen";

const CAPABILITIES: ThreadCapabilities = {
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

type TestThreadOverrides = Omit<Partial<Thread>, "evener"> & {
  evener?: Partial<Omit<Thread["evener"], "queue">> & { queue?: Partial<Thread["evener"]["queue"]> };
};

function testThread(overrides: TestThreadOverrides = {}): Thread {
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

function testHydrate(overrides: TestThreadOverrides = {}) {
  const thread = testThread(overrides);
  return hydrateThread({ thread }, thread.evener.ref, 1000);
}

test("hydrateThread maps humanNote/agentNote/sessionUrls from thread.evener", () => {
  const model = testHydrate({
    evener: {
      humanNote: "human hello",
      agentNote: "agent hello",
      sessionUrls: [{ id: "u1", url: "https://x.test/y", label: "x", addedBy: "agent", addedAt: 7 }],
    },
  });
  expect(model.humanNote).toBe("human hello");
  expect(model.agentNote).toBe("agent hello");
  expect(model.sessionUrls).toEqual([{ id: "u1", url: "https://x.test/y", label: "x", addedBy: "agent", addedAt: 7 }]);
});

test("hydrateThread defaults notes/urls when thread.evener omits them (old daemon / source-backed thread)", () => {
  const model = testHydrate();
  expect(model.humanNote).toBe("");
  expect(model.agentNote).toBe("");
  expect(model.sessionUrls).toEqual([]);
});

// The notes write gate keys off the recovery fence, which rides the snapshot
// as thread.evener.resumeRequired (absent means not fenced). Without this the
// model cannot tell a fenced live+idle session from an editable one, since the
// SharedNotes capability is deliberately retained for reading.
test("hydrateThread carries the recovery fence from thread.evener.resumeRequired", () => {
  expect(testHydrate().resumeRequired).toBe(false);
  expect(testHydrate({ evener: { resumeRequired: true } }).resumeRequired).toBe(true);
});

test("evener/notes/updated replaces both notes and stamps lastFrameAt", () => {
  const initial = testHydrate({ evener: { humanNote: "old human", agentNote: "old agent" } });
  const updated = applyNotification(
    initial,
    {
      method: "evener/notes/updated",
      params: { threadId: "thr_t", ref: "ref_t", humanNote: "new human", agentNote: "new agent" },
    },
    2000,
  );
  expect(updated.humanNote).toBe("new human");
  expect(updated.agentNote).toBe("new agent");
  expect(updated.lastFrameAt).toBe(2000);

  const cleared = applyNotification(
    updated,
    { method: "evener/notes/updated", params: { threadId: "thr_t", ref: "ref_t" } },
    3000,
  );
  expect(cleared.humanNote).toBe("");
  expect(cleared.agentNote).toBe("");
  expect(cleared.lastFrameAt).toBe(3000);
});

test("evener/urls/updated replaces the list and stamps lastFrameAt", () => {
  const initial = testHydrate({ evener: { sessionUrls: [{ id: "u1", url: "https://x.test/y" }] } });
  const updated = applyNotification(
    initial,
    {
      method: "evener/urls/updated",
      params: { threadId: "thr_t", ref: "ref_t", urls: [{ id: "u2", url: "https://y.test/z", label: "y" }] },
    },
    2000,
  );
  expect(updated.sessionUrls).toEqual([{ id: "u2", url: "https://y.test/z", label: "y" }]);
  expect(updated.lastFrameAt).toBe(2000);

  const cleared = applyNotification(
    updated,
    { method: "evener/urls/updated", params: { threadId: "thr_t", ref: "ref_t" } },
    3000,
  );
  expect(cleared.sessionUrls).toEqual([]);
  expect(cleared.lastFrameAt).toBe(3000);
});

test("notes/urls pushes for a different thread are ignored (same object returned)", () => {
  const initial = testHydrate({ evener: { humanNote: "h", agentNote: "a" } });
  const other = { threadId: "thr_other", ref: "ref_other" } as const;
  expect(
    applyNotification(initial, { method: "evener/notes/updated", params: { ...other, humanNote: "x" } }, 2000),
  ).toBe(initial);
  expect(applyNotification(initial, { method: "evener/urls/updated", params: { ...other, urls: [] } }, 2000)).toBe(
    initial,
  );
});
