import { expect, test } from "vitest";
import { summaryFailureWireFixture, summaryFailureWireRead } from "./testing/summaryFailureWireFixtures";
import { currentTranscriptState, decodeCurrentTranscriptState, threadModelFromCurrentState } from "./transcriptCache";
import { hydrateThread } from "./transcriptHistory";
import { attentionWarningNotice, isInformationalWarning } from "./warnings";

// Dropping route data or extending the producer's raw.warning fields must fail
// before the browser or phone sees the record. The decoder treats raw as opaque.
test("summary failure survives strict current-state decoding with its real route and recovery", () => {
  const fixture = summaryFailureWireFixture();
  expect(fixture.warning.summary_failure.terminal).toEqual({
    provider: "openai",
    model: "gpt-5.2",
    route: "session",
    kind: "rejected_output",
    stage: "validation",
    reason: "empty_reply",
    attempts: 3,
  });
  expect(fixture.warning.summary_failure.previous?.[0]).toEqual({
    provider: "openai",
    model: "summary-model-8af1",
    route: "cheap",
    kind: "rejected_output",
    stage: "validation",
    reason: "empty_reply",
    attempts: 3,
  });
  const original = hydrateThread(summaryFailureWireRead(), "local:summary-fixture", 0);
  const state = currentTranscriptState(original);
  const decoded = decodeCurrentTranscriptState(JSON.parse(JSON.stringify(state)));
  expect(decoded).toBeDefined();
  if (!decoded) throw new Error("producer warning was rejected by the strict parser");
  const restored = threadModelFromCurrentState(decoded);
  const items = restored.turns.flatMap((turn) => turn.items);
  const warning = items.find((item) => item.id === fixture.item.id);
  expect(warning).toBeDefined();
  if (!warning) throw new Error("producer warning was lost");
  expect(warning.raw).toEqual({
    warning: {
      code: "summary_failure",
      source: fixture.warning.source,
      title: fixture.warning.title,
      hint: fixture.warning.hint,
    },
  });
  expect(isInformationalWarning(warning)).toBe(false);
  const notice = attentionWarningNotice(warning);
  expect(notice).toEqual({
    message: fixture.warning.message,
    title: fixture.warning.title,
    hint: fixture.warning.hint,
  });
  for (const identifier of ["openai/gpt-5.2", "rejected_output", "empty_reply"])
    expect(notice?.message).toContain(identifier);
  expect(items.find((item) => item.id === fixture.recovery.id)).toMatchObject({
    raw: { compaction: { layer: "summarize" } },
  });

  const unexpected = JSON.parse(JSON.stringify(state));
  unexpected.overlay[fixture.item.id].item.summary_failure = fixture.warning.summary_failure;
  expect(decodeCurrentTranscriptState(unexpected)).toBeUndefined();
});
