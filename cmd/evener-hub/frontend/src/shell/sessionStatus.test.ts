// @vitest-environment node
import { describe, expect, test } from "vitest";
import { cadenceStateFor } from "./sessionStatus";

// cadenceStateFor reads both the raw ThreadStatus.type vocabulary a live
// ThreadModel carries (systemError, closed) and a navigation summary's
// normalized one (errored, ended).
describe("cadenceStateFor", () => {
  test.each([
    ["errored", "failed"],
    ["systemError", "failed"],
    ["awaiting", "needs-you"],
    ["active", "working"],
    ["warning", "needs-you"],
    ["restartRequired", "needs-you"],
    ["idle", "idle"],
    ["ended", "ended"],
    ["closed", "ended"],
    ["notLoaded", "idle"],
    ["", "idle"],
    ["some-unknown-future-state", "idle"],
  ] as const)("maps wire state %s to Cadence state %s", (wireState, expected) => {
    expect(cadenceStateFor(wireState)).toBe(expected);
  });
});
