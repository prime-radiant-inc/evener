import { expect, test } from "vitest";
import { entityKindOf, findEntityIds, jobOwnerSessionId } from "./entityIds";

// Real shapes from identifier/*.go; the owner payload is a valid UUIDv7.
const OWNER = "02wMz5TxvEMoJEDTDGOTil";
const JOB = `job_${OWNER}_000000000123`;
const DELEGATE = "dlg_02wMz5TxvEMoJEDTDGOTil"; // 4 + 22
const WATCH = "watch_02wMz5TxvEMoJEDTDGOTil"; // 6 + 22

test("detects each kind at its exact length", () => {
  const found = findEntityIds(`a ${JOB} b ${DELEGATE} c ${WATCH} d`);
  expect(found.map((m) => m.kind)).toEqual(["job", "delegate", "watch"]);
  expect(found.map((m) => m.id)).toEqual([JOB, DELEGATE, WATCH]);
  expect(found[0]?.start).toBe(2);
  expect(found[0]?.end).toBe(2 + JOB.length);
});

test("rejects wrong lengths, bad alphabet, and bad UUIDv7 payloads", () => {
  expect(findEntityIds(JOB.slice(0, -1))).toEqual([]);
  expect(findEntityIds(`job_${OWNER}_00000000012!`)).toEqual([]);
  expect(findEntityIds("dlg_0000000000000000000000")).toEqual([]); // decodes to non-v7
});

test("rejects a match embedded in a longer token", () => {
  expect(findEntityIds(`x${DELEGATE}`)).toEqual([]);
  expect(findEntityIds(`${DELEGATE}z`)).toEqual([]);
  expect(findEntityIds(`${DELEGATE}_more`)).toEqual([]);
});

test("entityKindOf and jobOwnerSessionId agree with detection", () => {
  expect(entityKindOf(DELEGATE)).toBe("delegate");
  expect(entityKindOf(JOB)).toBe("job");
  expect(entityKindOf("nope")).toBeUndefined();
  expect(jobOwnerSessionId(JOB)).toBe(OWNER);
  expect(jobOwnerSessionId(DELEGATE)).toBeUndefined();
});
