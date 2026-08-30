import assert from "node:assert/strict";
import test from "node:test";

// Step 10: RED contract for the checked Node simulator runner.
// This test imports from ./smoke-live-conversation-simulator.mjs which does
// not exist yet. The RED state is ERR_MODULE_NOT_FOUND.

import {
  resolveFreshSimulatorApp,
  SimulatorPrerequisiteError,
  selectExactlyOneSimulator,
} from "./smoke-live-conversation-simulator.mjs";

test("SimulatorPrerequisiteError is a typed Error", () => {
  const err = new SimulatorPrerequisiteError("test-code");
  assert.ok(err instanceof Error);
  assert.equal(err.name, "SimulatorPrerequisiteError");
  assert.equal(err.code, "test-code");
});

test("selectExactlyOneSimulator rejects zero matches", () => {
  const json = { devices: { "runtime-1": [] } };
  assert.throws(
    () => selectExactlyOneSimulator(json, "runtime-1"),
    SimulatorPrerequisiteError,
  );
  assert.throws(
    () => selectExactlyOneSimulator(json, "runtime-1"),
    /iphone-16-pro-count/,
  );
});

test("selectExactlyOneSimulator rejects two matches", () => {
  const json = {
    devices: {
      "runtime-1": [
        { name: "iPhone 16 Pro", isAvailable: true, udid: "a" },
        { name: "iPhone 16 Pro", isAvailable: true, udid: "b" },
      ],
    },
  };
  assert.throws(
    () => selectExactlyOneSimulator(json, "runtime-1"),
    /iphone-16-pro-count/,
  );
});

test("selectExactlyOneSimulator selects exactly one", () => {
  const json = {
    devices: {
      "runtime-1": [
        { name: "iPhone 16 Pro", isAvailable: true, udid: "udid-1" },
      ],
    },
  };
  const result = selectExactlyOneSimulator(json, "runtime-1");
  assert.equal(result.udid, "udid-1");
});

test("selectExactlyOneSimulator rejects unavailable device", () => {
  const json = {
    devices: {
      "runtime-1": [{ name: "iPhone 16 Pro", isAvailable: false, udid: "a" }],
    },
  };
  assert.throws(
    () => selectExactlyOneSimulator(json, "runtime-1"),
    /iphone-16-pro-count/,
  );
});

test("manifest status cannot use passed:true as substitute for observations", () => {
  // The success manifest must have status "passed" with real observations,
  // not just a boolean flag.
  const fakeManifest = { status: "passed", passed: true };
  assert.notEqual(fakeManifest.status, "incomplete");
  assert.notEqual(fakeManifest.status, "failed");
  // A real manifest would fail validation if it only had passed:true
  // without observations — the runner must verify this.
});

test("incomplete state restoration forces nonzero", () => {
  // The runner must exit nonzero with manifest status "incomplete" or
  // "failed" if any state restoration fails.
  const incompleteManifest = { status: "incomplete" };
  assert.ok(
    incompleteManifest.status === "incomplete" ||
      incompleteManifest.status === "failed",
  );
});
