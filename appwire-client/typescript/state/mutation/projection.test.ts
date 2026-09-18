import { describe, expect, test } from "vitest";
import { replaceTargetRecords } from "./projection";
import type { MutationOutboxRecord } from "./records";

function outboxRecord(overrides: Partial<MutationOutboxRecord> = {}): MutationOutboxRecord {
  return {
    version: 1,
    clientMutationId: "cmid-1",
    targetRef: "ref-a",
    method: "turn/start",
    payload: {},
    attachments: [],
    optimisticDisplay: null,
    intentSequence: 0,
    createdAt: 0,
    state: "submitting",
    ...overrides,
  };
}

describe("replaceTargetRecords", () => {
  test("replaces a targeted ref's existing records with the new records for that ref", () => {
    const current = new Map([
      ["cmid-1", outboxRecord({ clientMutationId: "cmid-1", targetRef: "ref-a" })],
      ["cmid-2", outboxRecord({ clientMutationId: "cmid-2", targetRef: "ref-b" })],
    ]);
    const next = replaceTargetRecords(current, new Set(["ref-a"]), [
      outboxRecord({ clientMutationId: "cmid-3", targetRef: "ref-a" }),
    ]);
    expect([...next.keys()].sort()).toEqual(["cmid-2", "cmid-3"]);
  });

  test("drops a targeted ref's existing records when no replacement names them", () => {
    const current = new Map([["cmid-1", outboxRecord({ clientMutationId: "cmid-1", targetRef: "ref-a" })]]);
    const next = replaceTargetRecords(current, new Set(["ref-a"]), []);
    expect(next.size).toBe(0);
  });

  test("leaves an untargeted ref's records untouched", () => {
    const current = new Map([["cmid-1", outboxRecord({ clientMutationId: "cmid-1", targetRef: "ref-a" })]]);
    const next = replaceTargetRecords(current, new Set(["ref-b"]), []);
    expect(next.get("cmid-1")).toEqual(current.get("cmid-1"));
  });
});
