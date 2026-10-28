// @vitest-environment node

import { expect, test } from "vitest";
import { RETRY_SAFE_MUTATION_METHODS as WIRE_RETRY_SAFE_MUTATION_METHODS } from "../../types.gen";
import { RETRY_SAFE_MUTATION_METHODS } from "./dispatcher";

// The dispatcher settles a record from the mutation receipt in the response.
// urls/remove is retry-safe on the wire but its response carries no receipt,
// so a queued record for it could never settle and would hold up its target.
test("the dispatcher accepts every retry-safe method whose response carries a receipt", () => {
  expect(WIRE_RETRY_SAFE_MUTATION_METHODS).toContain("urls/remove");
  expect(RETRY_SAFE_MUTATION_METHODS.has("urls/remove")).toBe(false);
  for (const method of WIRE_RETRY_SAFE_MUTATION_METHODS) {
    if (method !== "urls/remove") expect(RETRY_SAFE_MUTATION_METHODS.has(method)).toBe(true);
  }
});
