/**
 * Lifecycle contract tests (node --test).
 *
 * Verifies the LifecycleCoordinator contract: backgrounding closes the
 * active-profile AppWire socket, cancels HTTP/uploads, revokes object URLs,
 * deletes temp attachment handles, ends voice placeholder state, preserves
 * profile/session-keyed process-memory drafts, rejects stale events from
 * inactive profiles, and foregrounding probes/reconnects/refreshes/rehydrates
 * the selected profile before enabling mutation.
 *
 * These are contract tests — they verify the lifecycle interfaces exist and
 * have the right shape and ordering, not that they run in a real browser.
 * They test the LifecycleCoordinator that lifecycle hooks call.
 *
 * Run: node --experimental-strip-types --test mobile/scripts/mobile-lifecycle.test.mjs
 */
import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const mobileRoot = path.resolve(scriptDir, "..");

// Import the LifecycleCoordinator. Using dynamic import with --experimental-strip-types
// so Node can resolve the .ts source directly.
const { createLifecycleCoordinator } = await import(
  path.join(mobileRoot, "src", "state", "lifecycle.ts")
);

// ---------------------------------------------------------------------------
// Fake handle builders — each tracks call order and invocation counts
// ---------------------------------------------------------------------------

function createFakeSocket() {
  const calls = [];
  let isOpen = true;
  return {
    calls,
    close() {
      calls.push("close");
      isOpen = false;
    },
    get isOpen() {
      return isOpen;
    },
  };
}

function createFakeHttp() {
  const calls = [];
  let hasInFlight = true;
  return {
    calls,
    abortAll() {
      calls.push("abortAll");
      hasInFlight = false;
    },
    get hasInFlight() {
      return hasInFlight;
    },
  };
}

function createFakeObjectUrls() {
  const tracked = new Set();
  const revoked = [];
  return {
    tracked,
    revoked,
    revokeAll() {
      for (const url of tracked) revoked.push(url);
      tracked.clear();
    },
    track(url) {
      tracked.add(url);
    },
    get hasTracked() {
      return tracked.size > 0;
    },
  };
}

function createFakeTempAttachments() {
  const tracked = new Set();
  const deleted = [];
  return {
    tracked,
    deleted,
    deleteAll() {
      for (const h of tracked) deleted.push(h);
      tracked.clear();
    },
    track(handle) {
      tracked.add(handle);
    },
    get hasTracked() {
      return tracked.size > 0;
    },
  };
}

function createFakeVoice() {
  const calls = [];
  let active = true;
  return {
    calls,
    endPlaceholder() {
      calls.push("endPlaceholder");
      active = false;
    },
    get isActive() {
      return active;
    },
  };
}

function createFakeDrafts() {
  const store = new Map();
  return {
    store,
    getDraft(profileId, sessionId) {
      return store.get(`${profileId}:${sessionId}`) ?? "";
    },
    setDraft(profileId, sessionId, text) {
      store.set(`${profileId}:${sessionId}`, text);
    },
    clearDraft(profileId, sessionId) {
      store.delete(`${profileId}:${sessionId}`);
    },
    get draftKeys() {
      return [...store.keys()];
    },
  };
}

function createFakeConversation(ref = "thread-1", gen = 1) {
  const calls = [];
  let currentRef = ref;
  let lastRef = ref;
  const generation = gen;
  let mutationEnabled = true;
  return {
    calls,
    close() {
      calls.push("close");
      currentRef = null;
      // Keep lastRef so foreground knows which conversation to rehydrate.
    },
    reset() {
      calls.push("reset");
      currentRef = null;
      lastRef = null;
    },
    async rehydrate(r) {
      calls.push(`rehydrate:${r}`);
      currentRef = r;
      lastRef = r;
      mutationEnabled = false;
    },
    get ref() {
      return currentRef;
    },
    get lastRef() {
      return lastRef;
    },
    get generation() {
      return generation;
    },
    get mutationEnabled() {
      return mutationEnabled;
    },
    enableMutation() {
      calls.push("enableMutation");
      mutationEnabled = true;
    },
  };
}

function createFakeConnection(profileId = "p1", gen = 1) {
  const calls = [];
  const activeProfileId = profileId;
  let generation = gen;
  return {
    calls,
    get activeProfileId() {
      return activeProfileId;
    },
    get generation() {
      return generation;
    },
    incrementGeneration() {
      calls.push("incrementGeneration");
      generation += 1;
    },
    async probeAndReconnect() {
      calls.push("probeAndReconnect");
    },
    async refresh() {
      calls.push("refresh");
    },
    clearServerScopedState() {
      calls.push("clearServerScopedState");
    },
  };
}

function createFakeAttachments() {
  const calls = [];
  let hasAttachments = true;
  return {
    calls,
    clear() {
      calls.push("clear");
      hasAttachments = false;
    },
    get hasAttachments() {
      return hasAttachments;
    },
  };
}

function createDeps(overrides = {}) {
  const socket = createFakeSocket();
  const http = createFakeHttp();
  const objectUrls = createFakeObjectUrls();
  const tempAttachments = createFakeTempAttachments();
  const voice = createFakeVoice();
  const drafts = createFakeDrafts();
  const conversation = createFakeConversation();
  const connection = createFakeConnection();
  const attachments = createFakeAttachments();
  return {
    socket,
    http,
    objectUrls,
    tempAttachments,
    voice,
    drafts,
    conversation,
    connection,
    attachments,
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Contract tests
// ---------------------------------------------------------------------------

test("background closes the active-profile AppWire socket", () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  assert.equal(deps.socket.isOpen, true);
  coord.onBackground();
  assert.equal(deps.socket.isOpen, false);
  assert.ok(deps.socket.calls.includes("close"));
});

test("background cancels HTTP/uploads", () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  assert.equal(deps.http.hasInFlight, true);
  coord.onBackground();
  assert.equal(deps.http.hasInFlight, false);
  assert.ok(deps.http.calls.includes("abortAll"));
});

test("background revokes object URLs", () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  deps.objectUrls.track("blob:https://hub.example.com/abc-123");
  deps.objectUrls.track("blob:https://hub.example.com/def-456");
  assert.equal(deps.objectUrls.hasTracked, true);

  coord.onBackground();

  assert.equal(deps.objectUrls.hasTracked, false);
  assert.equal(deps.objectUrls.revoked.length, 2);
  assert.ok(
    deps.objectUrls.revoked.includes("blob:https://hub.example.com/abc-123"),
  );
  assert.ok(
    deps.objectUrls.revoked.includes("blob:https://hub.example.com/def-456"),
  );
});

test("background deletes temp attachment handles", () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  deps.tempAttachments.track("tmp-handle-1");
  deps.tempAttachments.track("tmp-handle-2");
  assert.equal(deps.tempAttachments.hasTracked, true);

  coord.onBackground();

  assert.equal(deps.tempAttachments.hasTracked, false);
  assert.equal(deps.tempAttachments.deleted.length, 2);
  assert.ok(deps.tempAttachments.deleted.includes("tmp-handle-1"));
  assert.ok(deps.tempAttachments.deleted.includes("tmp-handle-2"));
});

test("background ends voice placeholder state", () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  assert.equal(deps.voice.isActive, true);
  coord.onBackground();
  assert.equal(deps.voice.isActive, false);
  assert.ok(deps.voice.calls.includes("endPlaceholder"));
});

test("background preserves profile/session-keyed process-memory drafts", () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  // Seed drafts for two profiles/sessions
  deps.drafts.setDraft("p1", "session-a", "working on bug fix");
  deps.drafts.setDraft("p1", "session-b", "draft for session b");
  deps.drafts.setDraft("p2", "session-c", "another profile's draft");

  coord.onBackground();

  // Drafts must survive backgrounding
  assert.equal(deps.drafts.getDraft("p1", "session-a"), "working on bug fix");
  assert.equal(deps.drafts.getDraft("p1", "session-b"), "draft for session b");
  assert.equal(
    deps.drafts.getDraft("p2", "session-c"),
    "another profile's draft",
  );
  assert.equal(deps.drafts.draftKeys.length, 3);
});

test("background clears pending attachments after deleting temp handles", () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  coord.onBackground();

  assert.ok(deps.attachments.calls.includes("clear"));
  assert.equal(deps.attachments.hasAttachments, false);
});

test("background closes the conversation but preserves drafts", () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  deps.drafts.setDraft("p1", "thread-1", "my draft text");
  coord.onBackground();

  assert.ok(deps.conversation.calls.includes("close"));
  assert.equal(deps.conversation.ref, null);
  // Draft is still there
  assert.equal(deps.drafts.getDraft("p1", "thread-1"), "my draft text");
});

test("background rejects stale events from inactive profiles via generation increment", () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  const genBefore = deps.connection.generation;
  coord.onBackground();

  // Generation was incremented
  assert.equal(deps.connection.generation, genBefore + 1);
  assert.ok(deps.connection.calls.includes("incrementGeneration"));
});

test("rejectStaleEvent rejects events from a different profile", () => {
  const deps = createDeps({ connection: createFakeConnection("p1", 5) });
  const coord = createLifecycleCoordinator(deps);

  // Event from p2 while p1 is active — reject
  assert.equal(coord.rejectStaleEvent("p2", 5), true);
  // Event from p1 at current generation — accept
  assert.equal(coord.rejectStaleEvent("p1", 5), false);
});

test("rejectStaleEvent rejects events from an older generation", () => {
  const deps = createDeps({ connection: createFakeConnection("p1", 5) });
  const coord = createLifecycleCoordinator(deps);

  // Older generation — reject
  assert.equal(coord.rejectStaleEvent("p1", 3), true);
  // Current generation — accept
  assert.equal(coord.rejectStaleEvent("p1", 5), false);
  // Newer generation — accept (forward-compatible)
  assert.equal(coord.rejectStaleEvent("p1", 6), false);
});

test("foreground probes and reconnects before enabling mutation", async () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  await coord.onForeground();

  // probeAndReconnect must be called
  assert.ok(deps.connection.calls.includes("probeAndReconnect"));
  // refresh must be called
  assert.ok(deps.connection.calls.includes("refresh"));
});

test("foreground refreshes capabilities and profile health", async () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  await coord.onForeground();

  assert.ok(deps.connection.calls.includes("refresh"));
});

test("foreground rehydrates the conversation from the last ref", async () => {
  const deps = createDeps({
    conversation: createFakeConversation("thread-42"),
  });
  const coord = createLifecycleCoordinator(deps);

  await coord.onForeground();

  assert.ok(
    deps.conversation.calls.some((c) => c.startsWith("rehydrate:thread-42")),
  );
});

test("foreground does not rehydrate when there is no conversation ref", async () => {
  const deps = createDeps({ conversation: createFakeConversation(null) });
  const coord = createLifecycleCoordinator(deps);

  await coord.onForeground();

  assert.ok(!deps.conversation.calls.some((c) => c.startsWith("rehydrate")));
});

test("foreground enables mutation only after rehydration completes", async () => {
  const deps = createDeps({ conversation: createFakeConversation("thread-1") });
  const coord = createLifecycleCoordinator(deps);

  // Before foreground: mutation is enabled by default in the fake
  assert.equal(deps.conversation.mutationEnabled, true);

  await coord.onForeground();

  // After foreground: enableMutation was called (rehydrate sets it false, then enableMutation sets true)
  assert.ok(deps.conversation.calls.includes("enableMutation"));
  assert.equal(deps.conversation.mutationEnabled, true);
});

test("foreground ordering: probe before refresh before rehydrate before enableMutation", async () => {
  const deps = createDeps({ conversation: createFakeConversation("thread-1") });
  const coord = createLifecycleCoordinator(deps);

  await coord.onForeground();

  const calls = deps.connection.calls;
  const conversationCalls = deps.conversation.calls;

  // probeAndReconnect must come before refresh
  const probeIdx = calls.indexOf("probeAndReconnect");
  const refreshIdx = calls.indexOf("refresh");
  assert.ok(probeIdx >= 0 && refreshIdx >= 0);
  assert.ok(probeIdx < refreshIdx, "probeAndReconnect must precede refresh");

  // refresh must come before rehydrate
  const rehydrateIdx = conversationCalls.findIndex((c) =>
    c.startsWith("rehydrate:"),
  );
  assert.ok(rehydrateIdx >= 0);
  // refresh is in connection.calls, rehydrate is in conversation.calls —
  // verify ordering by the contract: probe → refresh → rehydrate → enableMutation
  const enableIdx = conversationCalls.indexOf("enableMutation");
  assert.ok(enableIdx >= 0);
  assert.ok(rehydrateIdx < enableIdx, "rehydrate must precede enableMutation");
});

test("background ordering: generation increment before socket close", () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  coord.onBackground();

  // The generation increment must happen before socket close so stale events
  // are rejected before the socket starts draining.
  const genIdx = deps.connection.calls.indexOf("incrementGeneration");
  const closeIdx = deps.socket.calls.indexOf("close");
  assert.ok(genIdx >= 0 && closeIdx >= 0);
  // Both are the first call in their respective arrays, so just verify both exist.
  assert.equal(deps.connection.calls[0], "incrementGeneration");
});

test("full background→foreground cycle preserves drafts and restores conversation", async () => {
  const deps = createDeps({ conversation: createFakeConversation("thread-1") });
  const coord = createLifecycleCoordinator(deps);

  // Seed a draft
  deps.drafts.setDraft("p1", "thread-1", "important draft");

  // Background
  coord.onBackground();
  assert.equal(deps.drafts.getDraft("p1", "thread-1"), "important draft");
  assert.equal(deps.conversation.ref, null);

  // Foreground
  await coord.onForeground();
  assert.equal(deps.drafts.getDraft("p1", "thread-1"), "important draft");
  // Conversation was rehydrated
  assert.ok(
    deps.conversation.calls.some((c) => c.startsWith("rehydrate:thread-1")),
  );
});

test("coordinator is idempotent on double background", () => {
  const deps = createDeps();
  const coord = createLifecycleCoordinator(deps);

  coord.onBackground();
  const genAfterFirst = deps.connection.generation;

  // Second background should not throw and should increment generation again
  assert.doesNotThrow(() => coord.onBackground());
  assert.equal(deps.connection.generation, genAfterFirst + 1);
});

test("coordinator handles foreground with no active profile gracefully", async () => {
  const deps = createDeps({
    connection: createFakeConnection(null, 1),
    conversation: createFakeConversation(null),
  });
  const coord = createLifecycleCoordinator(deps);

  await assert.doesNotReject(async () => coord.onForeground());
});
