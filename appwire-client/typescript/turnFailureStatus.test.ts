// @vitest-environment node

import { expect, test } from "vitest";
import { isTurnError, turnFailed, turnFailurePresent } from "./itemFailure";

// A failed attempt whose own diagnostic never reached the transcript: the
// readers stamp status "failed" from the completion that ended the attempt and
// deliberately leave `error` unset rather than borrow the recovered attempt's
// message (internal/transcriptindex/window.go, apptranscript.StampTurnFailure).
// Both clients render the failure row and offer Retry from this fact, so the
// canonical status has to answer on its own.
test("a failed turn with no diagnostic still counts as a failure", () => {
  const replayed = { status: "failed", error: undefined };
  expect(turnFailed(replayed)).toBe(true);
  expect(turnFailurePresent(replayed)).toBe(true);
  expect(isTurnError(replayed.error)).toBe(false);
});

// The status is not a substitute for the diagnostic when there is one: the
// error object still wins for the words the row shows.
test("a diagnostic still counts and still narrows", () => {
  const failed = { status: "failed", error: { message: "first attempt failed" } };
  expect(turnFailurePresent(failed)).toBe(true);
  expect(isTurnError(failed.error)).toBe(true);
});

// A turn that did not fail offers nothing, whatever it carries. A stale or
// malformed error on a completed turn must not re-open Retry.
test("a turn that did not fail is not a failure", () => {
  expect(turnFailurePresent({ status: "completed", error: undefined })).toBe(false);
  expect(turnFailurePresent({ status: "inProgress", error: undefined })).toBe(false);
  expect(turnFailurePresent({ status: "interrupted", error: undefined })).toBe(false);
});

// An older build's transcript can carry the diagnostic without this build's
// status stamp. The diagnostic alone is still a failure the client can act on.
test("a diagnostic on a turn whose status this build did not stamp is a failure", () => {
  expect(turnFailurePresent({ status: "completed", error: { message: "boom" } })).toBe(true);
  expect(turnFailed({ status: "completed", error: { message: "boom" } })).toBe(false);
});
