// Wire-true builders for the two session-lifecycle notifications that stores
// consume as bare "something changed, refetch" pokes (tree, credentials,
// extensions). Those stores key off the notification's method and never read
// its params, but the params are no longer free-form: appwire declares
// ThreadStartedParams/ThreadClosedParams, so a `params: {}` stand-in is a
// frame the server would never send and no longer type-checks. These builders
// produce the smallest payload the catalog actually permits, in one place,
// rather than a Thread literal copy-pasted into every poke site.

import type { AnyNotification, Thread } from "../types.gen";

const CAPABILITIES = {
  send: true,
  steer: true,
  interrupt: true,
  compact: true,
  clear: true,
  forkFromTurn: true,
  shutdown: true,
  changeModel: true,
  queue: true,
  goal: true,
  rename: true,
};

// wireThread builds a complete Thread with every required field populated —
// the snapshot shape thread/started carries on the real wire.
export function wireThread(ref = "ref_test", overrides: Partial<Thread> = {}): Thread {
  return {
    id: `thr_${ref}`,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "serf",
    serf: { ref, capabilities: CAPABILITIES, queue: { revision: 0 } },
    ...overrides,
  };
}

export function threadStartedNotification(ref = "ref_test"): AnyNotification {
  const thread = wireThread(ref);
  return { method: "thread/started", params: { threadId: thread.id, ref, thread } };
}

export function threadClosedNotification(ref = "ref_test", reason = "shutdown"): AnyNotification {
  return { method: "thread/closed", params: { threadId: `thr_${ref}`, ref, reason } };
}

// serf/attention/changed carried an undeclared payload until kata 4j2t moved
// its params type across the import cycle that was keeping it nil. Callers had
// been passing `params: {}`, which typechecked against the old empty stub and
// described a message the daemon never sends. tree.ts only keys off the method
// name today, so this shape is unread - which is exactly why it needs a
// realistic default: the first consumer to read `summary` would otherwise find
// every existing test feeding it a message with no summary at all.
export function attentionChangedNotification(ref = "ref_test"): AnyNotification {
  return {
    method: "serf/attention/changed",
    params: {
      changed: [
        { threadId: `thr_${ref}`, title: "test", project: "/tmp/project", level: "needsYou", prevLevel: "working" },
      ],
      summary: { needsYou: 1, error: 0, working: 0 },
    },
  };
}
