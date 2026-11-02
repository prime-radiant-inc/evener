import { expect, test } from "vitest";
import type { ThreadModel } from "./model";
import { canReadSharedNotes, canWriteSharedNotes } from "./sharedNotesAvailability";

const session = (status: string, resumeRequired?: boolean, sharedNotes = true) =>
  ({ capabilities: { sharedNotes }, status: { type: status }, resumeRequired }) as unknown as ThreadModel;

test("a live idle session with the capability accepts note edits", () => {
  expect(canWriteSharedNotes(session("idle"))).toBe(true);
});

// A resumeRequired session presents as a live, idle thread with the
// SharedNotes read capability retained (appwire.ThreadCapabilities.SharedNotes
// is not zeroed by the recovery fence), so the status/capability pair alone
// cannot close the write gate. Notes stay readable but must not accept edits
// until an explicit resume.
test("the recovery fence closes the write gate but not the read gate", () => {
  const fenced = session("idle", true);
  expect(canWriteSharedNotes(fenced)).toBe(false);
  expect(canReadSharedNotes(fenced)).toBe(true);
});

test.each(["ended", "closed", "notLoaded", "restartRequired"])(
  "a %s session is readable but not writable",
  (status) => {
    expect(canWriteSharedNotes(session(status))).toBe(false);
    expect(canReadSharedNotes(session(status))).toBe(true);
  },
);

test("without the capability or a model nothing is writable", () => {
  expect(canWriteSharedNotes(session("idle", undefined, false))).toBe(false);
  expect(canWriteSharedNotes(undefined)).toBe(false);
});
