import assert from "node:assert/strict";
import { isDeepStrictEqual } from "node:util";
import { composeAskAnswers } from "@evener/appwire-client";
import { mutateAndReadback, requireOwnedHub } from "./management-recovery.mjs";
import { projectQuestions, validateQuestionSelections } from "./questions-projection.mjs";

const record = (value) => value !== null && typeof value === "object" && !Array.isArray(value);
const validText = (value) => typeof value === "string" && value.trim().length > 0;
const itemLimit = 40;
const hasUser = (items) => items.some((item) => item.type === "userMessage");

function decodeRead(value, ref, expectedInstanceId, threadId) {
  assert.ok(record(value) && record(value.thread) && record(value.thread.evener), "Invalid thread read.");
  const thread = value.thread;
  assert.ok(validText(thread.id), "Invalid thread identity.");
  assert.equal(thread.evener.ref, ref, "Thread reference changed.");
  if (threadId !== undefined) assert.equal(thread.id, threadId, "Thread identity changed.");
  if (expectedInstanceId !== undefined)
    assert.equal(thread.evener.instanceId, expectedInstanceId, "Thread instance changed.");
  return value;
}

function pageItems(turns, latest = false) {
  assert.ok(Array.isArray(turns), "Invalid transcript page.");
  const items = turns.flatMap((turn) => {
    assert.ok(record(turn) && Array.isArray(turn.items), "Invalid transcript fragment.");
    return turn.items;
  });
  if (latest && turns.length)
    assert.notEqual(turns.at(-1).hasLaterItems, true, "Latest transcript window is incomplete.");
  validateItems(items);
  return items;
}

function validateItems(items) {
  const keys = new Set();
  let previous;
  for (const item of items) {
    assert.ok(
      record(item) &&
        validText(item.id) &&
        validText(item.type) &&
        validText(item.transcriptKey) &&
        record(item.position),
      "Positioned v4 transcript items are required.",
    );
    const { entry, item: offset } = item.position;
    assert.ok(
      Number.isSafeInteger(entry) && entry >= 0 && Number.isSafeInteger(offset) && offset >= 0,
      "Invalid transcript position.",
    );
    assert.ok(!keys.has(item.transcriptKey), "Overlapping transcript items.");
    keys.add(item.transcriptKey);
    if (previous)
      assert.ok(
        entry > previous.entry || (entry === previous.entry && offset > previous.item),
        "Transcript positions overlap or are out of order.",
      );
    previous = item.position;
  }
}

function cursorValue(cursor) {
  assert.ok(cursor === undefined || validText(cursor), "Invalid transcript cursor.");
  return cursor;
}

async function readQuestions(hub, ref, expectedInstanceId) {
  const request = () =>
    hub.request("thread/read", {
      ref,
      includeTurns: true,
      itemsView: "fragment",
      itemLimit,
      subscribe: false,
    });
  let snapshot = decodeRead(await request(), ref, expectedInstanceId);
  const initialThread = snapshot.thread;
  const initialItems = pageItems(initialThread.turns, true);
  let items = initialItems;
  let cursor = cursorValue(snapshot.olderCursor);
  const seen = new Set();
  while (!hasUser(items) && cursor !== undefined) {
    assert.ok(!seen.has(cursor), "Transcript cursor repeated; review a fresh snapshot.");
    seen.add(cursor);
    // A stale cursor rejects this review; do not combine different transcript incarnations.
    const page = await hub.request("thread/turns/list", { ref, cursor, itemsView: "fragment", itemLimit });
    assert.ok(record(page), "Invalid transcript page.");
    const older = pageItems(page.data);
    assert.ok(older.length > 0, "Empty transcript continuation.");
    items = [...older, ...items];
    validateItems(items);
    cursor = cursorValue(page.nextCursor);
  }
  if (seen.size) {
    snapshot = decodeRead(await request(), ref, expectedInstanceId, initialThread.id);
    assert.equal(
      snapshot.thread.evener.instanceId,
      initialThread.evener.instanceId,
      "Thread instance changed during paging.",
    );
    assert.ok(
      isDeepStrictEqual(pageItems(snapshot.thread.turns, true), initialItems),
      "Transcript changed during paging; review a fresh snapshot.",
    );
  }
  // An empty new session or system-only prelude has no writable question batch.
  if (!hasUser(items) && !items.some((item) => item.toolName === "ask_user"))
    return { thread: snapshot.thread, calls: [], questions: [] };
  return { thread: snapshot.thread, ...projectQuestions(items) };
}

function decodeReceipt(value, input, threadId) {
  assert.ok(record(value) && record(value.turn) && record(value.receipt), "Invalid answer acknowledgment.");
  const receipt = value.receipt;
  assert.equal(receipt.clientMutationId, input.clientMutationId, "Mutation receipt changed.");
  assert.equal(receipt.threadId, threadId, "Receipt thread changed.");
  assert.equal(receipt.instanceId, input.expectedInstanceId, "Receipt instance changed.");
  assert.ok(validText(receipt.turnId), "Missing receipt turn.");
  assert.equal(value.turn.id, receipt.turnId, "Receipt turn changed.");
  assert.ok(["applied", "replayed"].includes(receipt.disposition), "Invalid receipt disposition.");
  assert.equal(receipt.projectionState, "pending", "Invalid receipt projection state.");
  return receipt;
}

export async function runQuestions(hub, { action = "list", params = {}, ownedHub } = {}) {
  assert.ok(["list", "answer"].includes(action) && record(params), "Invalid question action or parameters.");
  const allowed =
    action === "list" ? ["ref"] : ["ref", "expectedInstanceId", "reviewedCalls", "selections", "clientMutationId"];
  assert.ok(
    Object.keys(params).every((key) => allowed.includes(key)),
    "Unknown question parameter.",
  );
  assert.ok(validText(params.ref), "Provide the session reference.");
  if (action === "answer") {
    requireOwnedHub(process.env.EVENER_QUESTION_MUTATION, ownedHub);
    assert.ok(validText(params.expectedInstanceId), "Provide the reviewed session instance.");
    assert.ok(
      validText(params.clientMutationId) && params.clientMutationId === params.clientMutationId.trim(),
      "Provide a stable caller-authored mutation ID.",
    );
    assert.ok(
      Array.isArray(params.reviewedCalls) && params.reviewedCalls.length > 0 && Array.isArray(params.selections),
      "Provide the complete reviewed batch and explicit selections.",
    );
  }
  const input = structuredClone(params);
  await hub.connect();
  const before = await readQuestions(hub, input.ref, input.expectedInstanceId);
  if (action === "list") return { outcome: "read", execution: "unverified", ...before };
  assert.equal(before.thread.evener.capabilities?.send, true, "Session cannot accept an answer.");
  assert.equal(before.thread.evener.askPending, true, "Session no longer has confirmed pending questions.");
  assert.ok(
    before.calls.length > 0 && isDeepStrictEqual(before.calls, input.reviewedCalls),
    "Question batch changed; review current state.",
  );
  const text = composeAskAnswers(validateQuestionSelections(before.questions, input.selections));
  let receipt;
  const result = await mutateAndReadback(
    hub,
    "turn/start",
    {
      ref: input.ref,
      expectedInstanceId: input.expectedInstanceId,
      clientMutationId: input.clientMutationId,
      input: [{ type: "text", text }],
    },
    (value) => {
      receipt = decodeReceipt(value, input, before.thread.id);
    },
    async () =>
      decodeRead(
        await hub.request("thread/read", {
          ref: input.ref,
          includeTurns: false,
          subscribe: false,
        }),
        input.ref,
        input.expectedInstanceId,
        before.thread.id,
      ),
  );
  // Readback may already contain another question. Acceptance is not execution proof.
  return { ...result, receipt, execution: "unverified" };
}
