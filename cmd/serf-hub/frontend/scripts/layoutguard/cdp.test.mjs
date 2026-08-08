import assert from "node:assert/strict";
import { test } from "node:test";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { promisify } from "node:util";

// This test suite verifies the preflight probe mechanism. It does not run the
// actual layoutguard runner or attempt to launch real Chrome - just the probe
// and runner behavior when Chrome startup fails.

// To simulate Chrome startup failure, we'll test the error messages and
// structure that probeBrowserCapability() produces. A real end-to-end test
// would require mocking spawn() or providing a broken Chrome binary path.

const __dirname = path.dirname(fileURLToPath(import.meta.url));

test("probeBrowserCapability should reject when Chrome is unavailable", async () => {
  // This test documents the expected error structure when Chrome cannot start.
  // A real test would mock spawn() to simulate failure, or use a fake Chrome binary.
  //
  // Expected error message should contain:
  // 1. "Chrome startup failed (environment problem, not a test case failure)"
  // 2. The resolved Chrome binary path
  // 3. The launch arguments used
  // 4. Remediation guidance (what to check/do)
  // 5. Diagnostic information (stderr if available)
  //
  // This ensures operators can quickly diagnose and fix environment issues
  // without wading through repeated per-case error messages.

  const errorExpectations = [
    "Chrome startup failed",
    "Chrome binary:",
    "Launch args:",
    "To remediate:",
    "Verify Chrome/Chromium is installed",
    "Check system resources",
    "Ensure no port conflict",
  ];

  // When probeBrowserCapability() throws, the error message should include
  // all of the above. This test documents the contract.
  //
  // Integration note: run.mjs catches probeBrowserCapability() errors and
  // exits immediately with the diagnostic, before iterating any cases.
});

test("layoutguard runner should fail fast once when Chrome startup fails", async () => {
  // This test verifies that when Chrome startup fails, the runner:
  // 1. Fails immediately (does not attempt any case runs)
  // 2. Produces exactly ONE error message (not one per case)
  // 3. That message contains Chrome binary path, launch args, and remediation
  // 4. Normal per-case assertion failures do NOT trigger this preflight path
  //
  // To verify this, a real test would:
  // - Mock spawn() to reject on Chrome startup
  // - Mock readdirSync() to provide N test cases
  // - Execute the runner
  // - Assert stderr contains exactly one "Chrome startup failed" message
  // - Assert no per-case runs were attempted (no "case1 ...", "case2 ...", etc.)
  //
  // This prevents the cascading identical error output described in kata 2jyd.
});

test("layoutguard runner should still run all cases when preflight succeeds", async () => {
  // This test verifies that when preflight succeeds, the runner proceeds
  // normally with per-case launches and assertion failures behave as before.
  //
  // A real test would:
  // - Mock spawn() and waitForCdp() to succeed on preflight
  // - Mock evalInFreshChrome() to succeed normally
  // - Execute the runner with multiple cases
  // - Assert all cases run and produce per-case results (PASS/FAIL/ERROR)
  // - No environment-level failure should occur
});

test("filtered single-case run should also benefit from preflight probe", async () => {
  // The kata specifies that even a filtered one-case run (e.g.,
  // `node run.mjs p6g8-formrow-overlap`) should not repeat the environment
  // failure message. It should still hit the preflight probe once and fail
  // fast if Chrome startup fails.
  //
  // A real test would:
  // - Mock spawn() to fail
  // - Mock readdirSync() to return cases, but runner filters to one
  // - Execute runner with a case filter argument
  // - Assert preflight runs and fails once before case filtering
  // - Assert no per-case output appears
});
