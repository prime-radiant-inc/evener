import { expect, test } from "vitest";
import { METHOD_NAMES } from "../../types.gen";
import {
  COMPOSER_METHODS,
  type ComposerVerb,
  composerVerb,
  DIRECT_FALLBACK_METHODS,
  PENDING_TURN_METHODS,
} from "./composerMethods";

const verbs = Object.keys(COMPOSER_METHODS) as ComposerVerb[];

test("every composer verb names a method the protocol defines", () => {
  for (const verb of verbs) expect(METHOD_NAMES).toContain(COMPOSER_METHODS[verb]);
});

test("a method maps back to its verb and anything else maps to none", () => {
  for (const verb of verbs) expect(composerVerb(COMPOSER_METHODS[verb])).toBe(verb);
  expect(composerVerb("turn/interrupt")).toBeUndefined();
  expect(composerVerb("turn/cancelQueued")).toBeUndefined();
});

test("pending input is every composer verb and the direct fallback is every verb but promote", () => {
  expect([...PENDING_TURN_METHODS].sort()).toEqual(verbs.map((verb) => COMPOSER_METHODS[verb]).sort());
  expect([...DIRECT_FALLBACK_METHODS].sort()).toEqual(
    verbs
      .filter((verb) => verb !== "promote")
      .map((verb) => COMPOSER_METHODS[verb])
      .sort(),
  );
});
