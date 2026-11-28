// @vitest-environment node

import type { Thread, ThreadCapabilities, ThreadStartResponse } from "@evener/appwire-client";
import { ClientNotReadyError, ConnectionClosedError, RequestTimeoutError, WireError } from "@evener/appwire-client";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { expect, test } from "vitest";

import {
  buildThreadStartParams,
  classifyStartFailure,
  formatStartFailureToast,
  spawnRequestFingerprint,
  startThread,
} from "./startThread";

// startThread never reads capabilities; all-false is a representative default.
const NO_CAPABILITIES: ThreadCapabilities = {
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
  sharedNotes: false,
  rename: false,
};

// A wire-true minimal Thread: only the fields startThread reads (evener.ref)
// carry meaning; the rest are representative defaults, mirroring
// stores/threads.test.ts's own testThread helper (this project has no shared
// test-utils module - see that file's note).
function threadWithRef(ref: string): Thread {
  return {
    id: ref.includes(":") ? ref.slice(ref.indexOf(":") + 1) : ref,
    sessionId: `sess_${ref}`,
    preview: "test",
    ephemeral: false,
    modelProvider: "anthropic/claude-sonnet-4-5",
    createdAt: 1000,
    updatedAt: 1000,
    status: { type: "idle" },
    cwd: "/tmp/project",
    cliVersion: "1.0.0",
    source: "local",
    evener: { ref, capabilities: NO_CAPABILITIES, queue: { revision: 0 } },
  };
}

function startResponse(ref: string): ThreadStartResponse {
  return { thread: threadWithRef(ref), turn: { id: "turn_1", itemsView: "full", status: "idle" } };
}

test("launch keeps selected command and skill identities separate from raw inline prose", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));
  await startThread(fake, {
    cwd: "/tmp/p",
    prompt: "keep /same around /same",
    skillNames: ["same"],
    commandNames: ["same"],
  });
  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [
      { type: "text", text: "keep /same around /same" },
      { type: "skill", name: "same" },
      { type: "command", name: "same" },
    ],
  });
});

test("a bare prompt + cwd sends thread/start with a text input and the cwd", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:abc123"));

  await startThread(fake, { cwd: "/tmp/project", prompt: "do the thing" });

  expect(fake.calls).toHaveLength(1);
  expect(fake.calls[0]?.method).toBe("thread/start");
  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/project",
    input: [{ type: "text", text: "do the thing" }],
  });
});

test("returns the created thread's ref VERBATIM (qualified, never local:-stripped)", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:abc123"));

  const result = await startThread(fake, { cwd: "/tmp/project", prompt: "hi" });

  // The SPA routes every session by the qualified ref; thread/read rejects a
  // bare id (ParseRef requires ":"). Stripping would open a dead session pane.
  expect(result.ref).toBe("local:abc123");
});

test("preserves the raw untrimmed prompt in the text item (floor §1.12)", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));

  await startThread(fake, { cwd: "/tmp/p", prompt: "  keep\n  whitespace  " });

  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [{ type: "text", text: "  keep\n  whitespace  " }],
  });
});

test("a whitespace-only prompt with an attachment sends an image-only input (no text item)", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));

  await startThread(fake, {
    cwd: "/tmp/p",
    prompt: "   ",
    attachments: [{ marker: 1, mediaType: "image/png", data: "QkFTRTY0", name: "shot.png" }],
  });

  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [{ type: "image", mediaType: "image/png", data: "QkFTRTY0", name: "shot.png" }],
  });
});

// kata 6nmz: a raw "[image N]" marker on the wire misleads small models -
// haiku read one as a file path and called read_file("[image 1]") instead of
// looking at the vision block it was sitting next to. The composer keeps the
// marker as its tile anchor; the wire gets prose.
test("translates the prompt's [image N] markers to prose at send (kata 6nmz)", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));

  await startThread(fake, {
    cwd: "/tmp/p",
    prompt: "[image 1]Describe the attached image",
    attachments: [{ marker: 1, mediaType: "image/png", data: "QkFTRTY0", name: "shot.png" }],
  });

  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [
      { type: "text", text: "(attached image 1: shot.png)Describe the attached image" },
      { type: "image", mediaType: "image/png", data: "QkFTRTY0", name: "shot.png" },
    ],
  });
});

test("an unnamed attachment's marker translates without a dangling name separator (kata 6nmz)", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));

  await startThread(fake, {
    cwd: "/tmp/p",
    prompt: "look: [image 1]",
    attachments: [{ marker: 1, mediaType: "image/png", data: "QkFTRTY0" }],
  });

  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [
      { type: "text", text: "look: (attached image 1)" },
      { type: "image", mediaType: "image/png", data: "QkFTRTY0" },
    ],
  });
});

test("a marker-less prompt reaches the wire byte-identical, untrimmed (kata 6nmz / floor §1.12)", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));

  await startThread(fake, {
    cwd: "/tmp/p",
    prompt: "  keep\n  every\n  byte  ",
    attachments: [{ marker: 1, mediaType: "image/png", data: "QkFTRTY0", name: "shot.png" }],
  });

  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [
      { type: "text", text: "  keep\n  every\n  byte  " },
      { type: "image", mediaType: "image/png", data: "QkFTRTY0", name: "shot.png" },
    ],
  });
});

test("merges the access mode into launchOverrides.sandbox (floor §1.8)", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));

  await startThread(fake, { cwd: "/tmp/p", prompt: "go", accessMode: "read-only" });

  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [{ type: "text", text: "go" }],
    launchOverrides: { sandbox: "read-only" },
  });
});

test("an advanced-schema sandbox wins over the access mode (floor §1.8)", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));

  await startThread(fake, {
    cwd: "/tmp/p",
    prompt: "go",
    accessMode: "full",
    launchOverrides: { sandbox: "workspace-write" },
  });

  expect(fake.calls[0]?.params).toMatchObject({ launchOverrides: { sandbox: "workspace-write" } });
});

test("passes the direct optional fields (harness/model/provider/effort/overrides) through", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));

  await startThread(fake, {
    cwd: "/tmp/p",
    prompt: "go",
    harness: "evener",
    modelProvider: "openai",
    model: "gpt-5",
    reasoningEffort: "high",
    launchOverrides: { sandbox: "full" },
  });

  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [{ type: "text", text: "go" }],
    harness: "evener",
    modelProvider: "openai",
    model: "gpt-5",
    reasoningEffort: "high",
    launchOverrides: { sandbox: "full" },
  });
});

// Component 06b: the host picker threads an explicit launch source. A LOCAL
// choice stays off the wire entirely so the single-host request is
// byte-identical to what it was before the picker existed.
test("omits source for a local (or absent) choice", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));

  await startThread(fake, { cwd: "/tmp/p", prompt: "go", source: "local" });

  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [{ type: "text", text: "go" }],
  });
});

test("sets source for a non-local choice", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("buildbox:r"));

  await startThread(fake, { cwd: "/tmp/p", prompt: "go", source: "buildbox" });

  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [{ type: "text", text: "go" }],
    source: "buildbox",
  });
});

test("an ordinary launch omits the Manager start flag entirely", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:plain"));
  await startThread(fake, { cwd: "/tmp/p", prompt: "hello" });
  const params = fake.calls[0]?.params as Record<string, unknown>;
  expect("manager" in params).toBe(false);
});

test("choosing Manager sends manager:true on thread/start", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:boss"));
  const result = await startThread(fake, { cwd: "/tmp/p", prompt: "hello", manager: true });
  const params = fake.calls[0]?.params as Record<string, unknown>;
  expect(params.manager).toBe(true);
  expect(result.ref).toBe("local:boss");
});

test("buildThreadStartParams excludes clientMutationId and startThread sends it only when supplied", async () => {
  const req = { cwd: "/tmp/p", prompt: "hello", clientMutationId: "mut-123" };
  expect(buildThreadStartParams(req)).toEqual({
    cwd: "/tmp/p",
    input: [{ type: "text", text: "hello" }],
  });
  expect("clientMutationId" in buildThreadStartParams(req)).toBe(false);

  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:idem"));
  await startThread(fake, { cwd: "/tmp/p", prompt: "hello" });
  await startThread(fake, req);

  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [{ type: "text", text: "hello" }],
  });
  expect("clientMutationId" in ((fake.calls[0]?.params ?? {}) as Record<string, unknown>)).toBe(false);
  expect((fake.calls[1]?.params as Record<string, unknown> | undefined)?.clientMutationId).toBe("mut-123");
});

test("spawnRequestFingerprint omits clientMutationId and raw attachment bytes while detecting same-length image edits", () => {
  const imageA = "A".repeat(4096);
  const imageB = `${"A".repeat(4095)}B`;
  const fpA = spawnRequestFingerprint({
    cwd: "/tmp/p",
    prompt: "look [image 1]",
    attachments: [{ marker: 1, mediaType: "image/png", data: imageA, name: "shot.png" }],
    clientMutationId: "mut-1",
  });
  const fpB = spawnRequestFingerprint({
    cwd: "/tmp/p",
    prompt: "look [image 1]",
    attachments: [{ marker: 1, mediaType: "image/png", data: imageB, name: "shot.png" }],
  });
  expect(fpA).not.toContain(imageA);
  expect(fpA).not.toBe(fpB);
});

const START_FAILURE_TIMEOUT = new RequestTimeoutError('AppwireClient: "thread/start" timed out after 180000ms');
const START_FAILURE_DROPPED = new Error("AppwireClient: socket closed (code 1006)");
const START_FAILURE_NOT_SENT = new Error('AppwireClient: cannot call "thread/start" while state is "reconnecting"');
const START_FAILURE_NOT_ACCEPTED = new WireError("sentinel-not-accepted", -32602, {
  evenerErrorInfo: "invalidParams",
  mutationOutcome: "notAccepted",
});
const START_FAILURE_BLOCKED = new WireError("sentinel-blocked", -32000, {
  evenerErrorInfo: "mutationOutcomeUnknown",
  mutationOutcome: "unknown",
  retryDisposition: "blocked",
});
const START_FAILURE_PARTIAL = new WireError("sentinel-partial-turn", -32014, {
  evenerErrorInfo: "sessionUnavailable",
});

test.each([
  [
    "pre-dispatch timeout",
    START_FAILURE_TIMEOUT,
    { startDispatched: false, retryablePendingStart: false },
    { kind: "failed", clearPendingStart: false, retryable: false },
  ],
  [
    "pre-dispatch notAccepted",
    START_FAILURE_NOT_ACCEPTED,
    { startDispatched: false, retryablePendingStart: false },
    { kind: "failed", clearPendingStart: false, retryable: false },
  ],
  [
    "dispatched notAccepted",
    START_FAILURE_NOT_ACCEPTED,
    { startDispatched: true, retryablePendingStart: true },
    { kind: "failed", clearPendingStart: true, retryable: false },
  ],
  [
    "dispatched blocked",
    START_FAILURE_BLOCKED,
    { startDispatched: true, retryablePendingStart: true },
    { kind: "failed", clearPendingStart: true, retryable: false },
  ],
  [
    "dispatched reconnecting (not sent)",
    START_FAILURE_NOT_SENT,
    { startDispatched: true, retryablePendingStart: true },
    { kind: "failed", clearPendingStart: false, retryable: false },
  ],
  [
    "dispatched ConnectionClosedError",
    new ConnectionClosedError("AppwireClient: closed"),
    { startDispatched: true, retryablePendingStart: true },
    { kind: "failed", clearPendingStart: false, retryable: false },
  ],
  [
    "dispatched ClientNotReadyError",
    new ClientNotReadyError("not ready"),
    { startDispatched: true, retryablePendingStart: true },
    { kind: "failed", clearPendingStart: false, retryable: false },
  ],
  [
    "dispatched timeout (keyed)",
    START_FAILURE_TIMEOUT,
    { startDispatched: true, retryablePendingStart: true },
    { kind: "uncertain-timeout", clearPendingStart: false, retryable: true },
  ],
  [
    "dispatched timeout (keyless)",
    START_FAILURE_TIMEOUT,
    { startDispatched: true, retryablePendingStart: false },
    { kind: "uncertain-timeout", clearPendingStart: false, retryable: false },
  ],
  [
    "dispatched dropped socket (keyed)",
    START_FAILURE_DROPPED,
    { startDispatched: true, retryablePendingStart: true },
    { kind: "uncertain-disconnected", clearPendingStart: false, retryable: true },
  ],
  [
    "dispatched dropped socket (keyless)",
    START_FAILURE_DROPPED,
    { startDispatched: true, retryablePendingStart: false },
    { kind: "uncertain-disconnected", clearPendingStart: false, retryable: false },
  ],
  [
    "dispatched post-spawn WireError (keyed)",
    START_FAILURE_PARTIAL,
    { startDispatched: true, retryablePendingStart: true },
    { kind: "uncertain-partial", clearPendingStart: false, retryable: true },
  ],
  [
    "dispatched WireError (keyless)",
    START_FAILURE_PARTIAL,
    { startDispatched: true, retryablePendingStart: false },
    { kind: "failed", clearPendingStart: false, retryable: false },
  ],
  [
    "dispatched opaque error (keyed)",
    new Error("opaque"),
    { startDispatched: true, retryablePendingStart: true },
    { kind: "uncertain", clearPendingStart: false, retryable: true },
  ],
  [
    "dispatched opaque error (keyless)",
    new Error("opaque"),
    { startDispatched: true, retryablePendingStart: false },
    { kind: "failed", clearPendingStart: false, retryable: false },
  ],
] as const)("classifyStartFailure classifies %s", (_label, err, context, expected) => {
  expect(classifyStartFailure(err, context)).toEqual(expected);
});

test("formatStartFailureToast preserves server sentinels and avoids raw transport text or generic fallback", () => {
  const partialMsg = formatStartFailureToast(
    classifyStartFailure(START_FAILURE_PARTIAL, { startDispatched: true, retryablePendingStart: true }),
    START_FAILURE_PARTIAL,
  );
  expect(partialMsg).toContain("sentinel-partial-turn");

  const timeoutMsg = formatStartFailureToast(
    classifyStartFailure(START_FAILURE_TIMEOUT, { startDispatched: true, retryablePendingStart: true }),
    START_FAILURE_TIMEOUT,
  );
  expect(timeoutMsg).not.toMatch(/AppwireClient|timed out after 180000ms/);

  const droppedMsg = formatStartFailureToast(
    classifyStartFailure(START_FAILURE_DROPPED, { startDispatched: true, retryablePendingStart: true }),
    START_FAILURE_DROPPED,
  );
  expect(droppedMsg).not.toMatch(/AppwireClient|code 1006/);

  const genericMsg = formatStartFailureToast(
    classifyStartFailure(new TypeError("internal"), { startDispatched: false, retryablePendingStart: false }),
    new TypeError("internal"),
  );
  expect(timeoutMsg).not.toBe(genericMsg);
  expect(droppedMsg).not.toBe(genericMsg);
});
