// The Go-constant binding extractor's own contract. The suffix hazard below
// is the live shape of appwire/errors.go: ErrorMutationOutcomeUnknown
// ErrorInfo = "mutationOutcomeUnknown" (line 31) precedes MutationOutcomeUnknown
// MutationOutcome = "unknown" (line 94), so an unanchored name can match the
// longer identifier and silently return the wrong constant's value — the exact
// mismatch this helper exists to surface loudly, not assert wrongly.

import { describe, expect, test } from "vitest";
import { goConstantValue } from "./goConstants";

const COLLIDING_SOURCE = `const ErrorMutationOutcomeUnknown ErrorInfo = "mutationOutcomeUnknown"
const MutationOutcomeUnknown MutationOutcome = "unknown"
`;

describe("goConstantValue", () => {
  test("does not match the name as a suffix of a longer Go identifier", () => {
    expect(goConstantValue(COLLIDING_SOURCE, "MutationOutcomeUnknown", "fixture.go")).toBe("unknown");
  });

  test("extracts the typed and untyped declaration forms", () => {
    expect(goConstantValue('const A ErrorInfo = "a"\n', "A", "f.go")).toBe("a");
    expect(goConstantValue('const B = "b"\n', "B", "f.go")).toBe("b");
  });

  test("throws naming the file when no such constant exists", () => {
    expect(() => goConstantValue("const C = 1\n", "C", "f.go")).toThrow("f.go has no C constant");
  });
});
