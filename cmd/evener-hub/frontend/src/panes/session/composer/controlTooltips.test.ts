import { expect, test } from "vitest";
import { STEER_RECOVERY_FENCED_REASON, steerTooltipLabel, submitTooltipLabel } from "./controlTooltips";

const modWord = /Mac|iPhone|iPad|iPod/.test(window.navigator.platform) ? "⌘" : "Ctrl";

test.each([
  { canQueue: false, enterToSend: false, label: `Send now · ${modWord}+Enter` },
  { canQueue: true, enterToSend: false, label: `Queue until the agent stops · ${modWord}+Enter` },
  { canQueue: false, enterToSend: true, label: "Send now · Enter" },
  { canQueue: true, enterToSend: true, label: "Queue until the agent stops · Enter" },
])("the Send tooltip for canQueue=$canQueue enterToSend=$enterToSend", ({ canQueue, enterToSend, label }) => {
  expect(submitTooltipLabel({ canQueue, enterToSend })).toBe(label);
});

test.each([
  { recoveryFenced: false, enterToSend: false, label: "Interrupt and redirect now · Shift+Enter" },
  { recoveryFenced: false, enterToSend: true, label: "Interrupt and redirect now" },
  { recoveryFenced: true, enterToSend: false, label: STEER_RECOVERY_FENCED_REASON },
  { recoveryFenced: true, enterToSend: true, label: STEER_RECOVERY_FENCED_REASON },
])(
  "the Steer tooltip for recoveryFenced=$recoveryFenced enterToSend=$enterToSend",
  ({ recoveryFenced, enterToSend, label }) => {
    expect(steerTooltipLabel({ recoveryFenced, enterToSend })).toBe(label);
  },
);
