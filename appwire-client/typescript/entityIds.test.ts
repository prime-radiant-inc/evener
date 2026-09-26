// @vitest-environment node

import { expect, test } from "vitest";
import { entityKindOf, findEntityIds, jobOwnerSessionId } from "./entityIds";

// Real shapes from identifier/*.go; the owner payload is a valid UUIDv7.
const OWNER = "02wMz5TxvEMoJEDTDGOTil";
const JOB = `job_${OWNER}_000000000123`;
const DELEGATE = "dlg_02wMz5TxvEMoJEDTDGOTil"; // 4 + 22
const WATCH = "watch_02wMz5TxvEMoJEDTDGOTil"; // 6 + 22

// Each payload keeps every UUID guard except the one named. The version and
// variant vectors are authoritative EncodeUUID outputs for UUIDs differing
// from OWNER only in that field. OVERFLOW is 2^128 plus OWNER, so its low 128
// bits are still the same valid UUIDv7 if the overflow check is removed.
const OVERFLOW = "7q0PCLq3Ou8YT1MG6NeBKt";
const WRONG_VERSION = "02wMz5Txu66lWtmzJELfzf";
const WRONG_VARIANT = "02wMz5TxvEMdJt4gCiJKwd";

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

test.each([
  ["a payload over 128 bits", OVERFLOW],
  ["a valid UUID with the wrong version nibble", WRONG_VERSION],
  ["a UUIDv7 with the wrong variant", WRONG_VARIANT],
])("rejects %s independently", (_name, payload) => {
  expect(findEntityIds(`dlg_${payload}`)).toEqual([]);
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
