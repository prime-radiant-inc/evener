// @vitest-environment node

import { expect, test } from "vitest";
import { FakeClient } from "../../protocol/testing/fakeClient";
import type { Thread, ThreadCapabilities, ThreadStartResponse } from "../../protocol/types.gen";

import { startThread } from "./startThread";

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
  queue: false,
  goal: false,
  rename: false,
};

// A wire-true minimal Thread: only the fields startThread reads (serf.ref)
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
    serf: { ref, capabilities: NO_CAPABILITIES, queue: { revision: 0 } },
  };
}

function startResponse(ref: string): ThreadStartResponse {
  return { thread: threadWithRef(ref), turn: { id: "turn_1", itemsView: "full", status: "idle" } };
}

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
    attachments: [{ mediaType: "image/png", data: "QkFTRTY0", name: "shot.png" }],
  });

  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [{ type: "image", mediaType: "image/png", data: "QkFTRTY0", name: "shot.png" }],
  });
});

// kata 6nmz: a raw "[image N]" marker on the wire misleads small models -
// haiku read one as a file path and called read_file("[image 1]") instead of
// looking at the vision block it was sitting next to. The composer keeps the
// marker as its chip anchor; the wire gets prose.
test("translates the prompt's [image N] markers to prose at send (kata 6nmz)", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));

  await startThread(fake, {
    cwd: "/tmp/p",
    prompt: "[image 1]Describe the attached image",
    attachments: [{ mediaType: "image/png", data: "QkFTRTY0", name: "shot.png" }],
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
    attachments: [{ mediaType: "image/png", data: "QkFTRTY0" }],
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
    attachments: [{ mediaType: "image/png", data: "QkFTRTY0", name: "shot.png" }],
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

test("branch is display-only and is never sent on the wire (floor §1.7; thread/start has no branch field)", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));

  await startThread(fake, { cwd: "/tmp/p", prompt: "go", branch: "feature/x" });

  expect(fake.calls[0]?.params).toEqual({ cwd: "/tmp/p", input: [{ type: "text", text: "go" }] });
});

test("passes the direct optional fields (harness/model/provider/effort/overrides) through", async () => {
  const fake = new FakeClient("ready");
  fake.on("thread/start", () => startResponse("local:r"));

  await startThread(fake, {
    cwd: "/tmp/p",
    prompt: "go",
    harness: "serf",
    modelProvider: "openai",
    model: "gpt-5",
    reasoningEffort: "high",
    launchOverrides: { sandbox: "full" },
  });

  expect(fake.calls[0]?.params).toEqual({
    cwd: "/tmp/p",
    input: [{ type: "text", text: "go" }],
    harness: "serf",
    modelProvider: "openai",
    model: "gpt-5",
    reasoningEffort: "high",
    launchOverrides: { sandbox: "full" },
  });
});
