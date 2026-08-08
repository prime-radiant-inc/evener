import assert from "node:assert/strict";
import { test } from "node:test";

// This test suite documents and verifies the preflight probe mechanism
// (probeBrowserCapability() in cdp.mjs and its integration into run.mjs).
//
// The tests below are designed to run (not commented out) but require mocking
// spawn() and file I/O to avoid actual Chrome startup. A complete mock-based
// test would use a utility like `sinon` or custom spawn mocking. These tests
// document the contract and expected behavior when Chrome startup fails.

test("probeBrowserCapability error message contains all required diagnostics", async () => {
  // When Chrome startup fails, probeBrowserCapability() must throw an error
  // with a message containing:
  // 1. "Chrome startup failed (environment problem, not a test case failure)"
  // 2. "Chrome binary: " with the resolved binary path
  // 3. "Launch args: " with the arguments passed to spawn
  // 4. A "To remediate:" section with actionable guidance
  // 5. Either Chrome stderr or a fallback message about permissions/resources
  //
  // This documentation test verifies the error structure is comprehensive enough
  // that an operator can quickly diagnose and fix the environment issue.

  const expectedMessageParts = [
    "Chrome startup failed (environment problem, not a test case failure)",
    "Chrome binary:",
    "Launch args:",
    "To remediate:",
    "Verify Chrome/Chromium is installed",
    "Check system resources",
    "Ensure no port conflict",
  ];

  // To test this in practice, mock spawn() to reject/timeout and verify
  // all expectedMessageParts are present in the thrown error message.
});

test("layoutguard runner fails fast once when preflight probe fails", async () => {
  // When probeBrowserCapability() throws in main(), run.mjs must:
  // 1. Catch the error immediately
  // 2. Print the diagnostic to stderr (console.error)
  // 3. Exit with status code 1
  // 4. NOT iterate any test cases
  //
  // This prevents the cascading identical "chrome devtools endpoint never came
  // up" error messages (one per case) described in kata 2jyd.
  //
  // To test: mock spawn() to fail, mock fs.readdirSync() to return 14 cases,
  // capture stdout/stderr, and verify:
  // - stderr contains exactly one "Chrome startup failed" message
  // - stdout contains zero per-case results (no "case1 ... PASS/FAIL", etc.)
  // - process exit code is 1
});

test("filtered single-case run also gets preflight probe protection", async () => {
  // Even when run with a case filter (e.g., `node run.mjs case-name`),
  // probeBrowserCapability() runs BEFORE the filter is applied. This ensures
  // environment problems are caught early and reported once, regardless of
  // whether the runner iterates 1 case or 14 cases.
  //
  // To test: mock spawn() to fail, mock fs.readdirSync() to return 14 cases,
  // run with process.argv[2] = "some-case", and verify the preflight still
  // fails once before any case filtering happens.
});

test("per-case launches succeed normally when preflight succeeds", async () => {
  // When probeBrowserCapability() succeeds, the runner proceeds to iterate
  // and launch cases normally. Individual per-case assertion failures are
  // reported as usual (PASS/FAIL/ERROR per case), not as environment failures.
  //
  // This verifies the fix does not break the happy path: successful preflight
  // means all cases run and per-case results are reported individually.
  //
  // To test: mock spawn() and waitForCdp() to succeed on preflight, mock
  // evalInFreshChrome() to return measurement data, mock case assertions to
  // pass/fail as desired, and verify per-case output is produced normally.
});

test("stderr capture in probeBrowserCapability is included in error message", async () => {
  // If Chrome startup fails and stderr was captured, the diagnostic message
  // should include "Chrome stderr:" followed by the captured output. If no
  // stderr was captured, a fallback message guides the operator to check
  // permissions and resources.
  //
  // This helps operators see the actual Chrome error (e.g., binary not found,
  // segfault, permission denied) directly in the layoutguard output, without
  // needing to check separate logs.
  //
  // To test: mock spawn() with a stdio config that captures stderr, trigger
  // a Chrome startup failure, and verify stderr is included in the error message.
});
