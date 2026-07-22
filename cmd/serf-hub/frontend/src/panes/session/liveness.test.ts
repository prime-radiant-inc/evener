// @vitest-environment node
import { expect, test } from "vitest";
import { cadenceStateForStatus } from "./liveness";

// cadenceStateForStatus: direct unit tests, mirroring shell/rail/RailRow.tsx's
// own cadenceStateFor precedent (exported specifically for this). This maps
// the RAW wire ThreadStatus.type vocabulary (appwire/types.go's constants),
// not hubcore's already-normalized NormalizeState output RailRow's version
// consumes - see this file's own comment for why they're deliberately
// separate functions.

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
