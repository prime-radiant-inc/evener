// @vitest-environment node
import { expect, test } from "vitest";
import { cadenceStateForStatus } from "./liveness";

// cadenceStateForStatus consumes raw ThreadStatus.type values. The mapper in
// shell/SessionStatusIndicator.tsx consumes normalized navigation summaries;
// see liveness.ts for why those vocabularies need separate functions.

test("cadenceStateForStatus: active is working", () => {
  expect(cadenceStateForStatus("active")).toBe("working");
});

test("cadenceStateForStatus: awaiting and warning are both needs-you", () => {
  expect(cadenceStateForStatus("awaiting")).toBe("needs-you");
  expect(cadenceStateForStatus("warning")).toBe("needs-you");
});

test("cadenceStateForStatus: systemError is failed", () => {
  expect(cadenceStateForStatus("systemError")).toBe("failed");
});

test("cadenceStateForStatus: closed is ended", () => {
  expect(cadenceStateForStatus("closed")).toBe("ended");
});

test("cadenceStateForStatus: idle, notLoaded, and any unknown value are idle", () => {
  expect(cadenceStateForStatus("idle")).toBe("idle");
  expect(cadenceStateForStatus("notLoaded")).toBe("idle");
  expect(cadenceStateForStatus("something-future-and-unknown")).toBe("idle");
});
