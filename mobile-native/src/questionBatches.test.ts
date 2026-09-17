import { expect, it } from "vitest";
import { QuestionBatches } from "./questionBatches";

const first = {
  key: "first:0",
  callId: "first",
  header: "First",
  question: "Choose",
  options: [],
  multiSelect: false,
  // The rows' own refs carry the bounded display copy the sheet renders
  // (project.ts's MobileQuestionRef).
  display: { header: "First", question: "Choose", options: [] },
};
const second = { ...first, key: "second:0", callId: "second" };
it("freezes a sending batch and settles only its own questions", () => {
  const state = new QuestionBatches();
  state.reconcile([first]);
  const batch = state.getSnapshot()[0];
  if (!batch) throw new Error("Missing batch");
  expect(state.begin(batch)).toBe(true);
  state.reconcile([first, second]);
  expect(state.getSnapshot().map((b) => b.questions.map((q) => q.key))).toEqual(
    [[first.key], [second.key]],
  );
  state.reconcile([second]);
  expect(state.getSnapshot()[0]?.questions).toEqual([first]);
  state.finish(batch.id, true);
  state.reconcile([first, second]);
  expect(state.getSnapshot().flatMap((b) => b.questions)).toEqual([second]);
});
it("rejects stale or repeated submissions and retains failed answers", () => {
  const state = new QuestionBatches();
  state.reconcile([first]);
  const batch = state.getSnapshot()[0];
  if (!batch) throw new Error("Missing batch");
  state.reconcile([first, second]);
  expect(state.begin(batch)).toBe(false);
  const current = state.getSnapshot()[0];
  if (!current) throw new Error("Missing batch");
  expect(state.begin(current)).toBe(true);
  expect(state.begin(current)).toBe(false);
  state.finish(current.id, false);
  expect(state.getSnapshot()[0]?.sending).toBe(false);
  expect(state.getSnapshot()[0]?.questions).toEqual([first, second]);
  state.reconcile([]);
  expect(state.getSnapshot()).toEqual([]);
});

// The package's reconciliation hands back the very ref objects it was given, so
// the display copy the rows built survives into the batch the sheet renders.
// This adapter's type says so; this is the check behind that claim.
it("carries the rows' bounded display copy into the batch", () => {
  const state = new QuestionBatches();
  state.reconcile([{ ...first, display: { ...first.display, header: "bounded" } }]);
  expect(state.getSnapshot()[0]?.questions[0]?.display.header).toBe("bounded");
});
