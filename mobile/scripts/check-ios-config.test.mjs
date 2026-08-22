import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";
import {
  defaultBuiltPlistPath,
  parseDeploymentTargets,
  validateIosConfiguration,
} from "./check-ios-config.mjs";

const requiredUsageMetadata = {
  NSCameraUsageDescription: "Scan a pairing code.",
  NSMicrophoneUsageDescription: "Capture voice input.",
  NSSpeechRecognitionUsageDescription: "Transcribe voice input.",
  NSLocalNetworkUsageDescription: "Connect to a local Hub.",
};
const requiredMetadata = {
  MinimumOSVersion: "17.0",
  ...requiredUsageMetadata,
};

test("accepts iOS 17 targets with every nonempty usage description", () => {
  assert.doesNotThrow(() =>
    validateIosConfiguration({
      deploymentTargets: ["17.0", "17.0"],
      metadata: requiredMetadata,
    }),
  );
});

for (const key of Object.keys(requiredUsageMetadata)) {
  test(`rejects an empty ${key}`, () => {
    assert.throws(
      () =>
        validateIosConfiguration({
          deploymentTargets: ["17.0", "17.0"],
          metadata: { ...requiredMetadata, [key]: "   " },
        }),
      new RegExp(`${key}.*nonempty`, "i"),
    );
  });
}

test("rejects a generated target below the iOS 17 floor", () => {
  assert.throws(
    () =>
      validateIosConfiguration({
        deploymentTargets: ["17.0", "16.4"],
        metadata: requiredMetadata,
      }),
    /deployment target.*17\.0/i,
  );
});

test("rejects a missing generated target configuration", () => {
  assert.throws(
    () =>
      validateIosConfiguration({
        deploymentTargets: ["17.0"],
        metadata: requiredMetadata,
      }),
    /exactly two.*deployment target/i,
  );
});

test("rejects a built app below the iOS 17 floor", () => {
  assert.throws(
    () =>
      validateIosConfiguration({
        deploymentTargets: ["17.0", "17.0"],
        metadata: { ...requiredMetadata, MinimumOSVersion: "16.4" },
      }),
    /built app.*minimum.*17\.0/i,
  );
});

test("rejects arbitrary public HTTP loads", () => {
  assert.throws(
    () =>
      validateIosConfiguration({
        deploymentTargets: ["17.0", "17.0"],
        metadata: {
          ...requiredMetadata,
          NSAppTransportSecurity: { NSAllowsArbitraryLoads: true },
        },
      }),
    /arbitrary HTTP loads/i,
  );
});

test("extracts every generated deployment target", () => {
  assert.deepEqual(
    parseDeploymentTargets(`
      IPHONEOS_DEPLOYMENT_TARGET = 17.0;
      OTHER_SETTING = value;
      IPHONEOS_DEPLOYMENT_TARGET = 17.0;
    `),
    ["17.0", "17.0"],
  );
});

test("defaults to the simulator app produced by the foundation build", () => {
  assert.equal(
    defaultBuiltPlistPath("/mobile"),
    path.join(
      "/mobile",
      "src-tauri",
      "gen",
      "apple",
      "build",
      "arm64-sim",
      "Evener.app",
      "Info.plist",
    ),
  );
});
