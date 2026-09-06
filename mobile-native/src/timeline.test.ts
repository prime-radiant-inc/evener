import { expect, it } from "vitest";
import type { MobileTimelineItem } from "../../mobile/src/conversation/model";
import { groupTimeline, isInterruptedNotice } from "./timeline";

const setup: MobileTimelineItem = {
  kind: "notice",
  id: "setup",
  origin: "system",
  family: "hidden-instruction",
  tone: "system",
  text: "instructions",
};
const diagnostic: MobileTimelineItem = {
  ...setup,
  id: "diagnostic",
  family: "diagnostic",
  text: "details",
};
const message: MobileTimelineItem = {
  kind: "user",
  id: "message",
  text: "task",
};
const warning: MobileTimelineItem = {
  ...setup,
  id: "warning",
  family: "warning",
  tone: "warning",
};

it("collapses only typed non-warning interruption notices", () => {
  const interrupted = {
    ...setup,
    origin: "steering" as const,
    steeringKind: "interrupted",
  };
  expect(isInterruptedNotice(interrupted)).toBe(true);
  expect(isInterruptedNotice({ ...interrupted, tone: "warning" })).toBe(false);
  expect(isInterruptedNotice({ ...interrupted, origin: "system" })).toBe(false);
  expect(
    isInterruptedNotice({
      ...interrupted,
      steeringKind: undefined,
      text: "Interrupted",
    }),
  ).toBe(false);
  expect(
    isInterruptedNotice({ ...interrupted, steeringKind: "notification" }),
  ).toBe(false);
});

it("groups consecutive internal entries without losing order or contents", () => {
  const input = [setup, diagnostic, message, { ...setup, id: "later" }];
  const rows = groupTimeline(input);
  expect(rows).toHaveLength(3);
  expect(rows[0]).toMatchObject({
    kind: "details",
    entries: [setup, diagnostic],
  });
  expect(rows[1]).toBe(message);
  expect(
    rows.flatMap((row) => (row.kind === "details" ? row.entries : [row])),
  ).toEqual(input);
  expect(input).toHaveLength(4);
});

it("keeps warnings visible even when classified as internal details", () => {
  const internalWarning = {
    ...diagnostic,
    id: "internal-warning",
    tone: "warning" as const,
  };
  expect(
    groupTimeline([setup, warning, internalWarning, diagnostic]),
  ).toMatchObject([
    { kind: "details", entries: [setup] },
    warning,
    internalWarning,
    { kind: "details", entries: [diagnostic] },
  ]);
});

it("retains a disclosure identity as adjacent details arrive", () => {
  expect(groupTimeline([setup])[0]?.id).toBe(
    groupTimeline([setup, diagnostic])[0]?.id,
  );
  expect(groupTimeline([])).toEqual([]);
});
