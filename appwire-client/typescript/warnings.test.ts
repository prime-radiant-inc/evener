// @vitest-environment node

// The informational-warning contract: which warning codes mark a notice as
// budget arithmetic (or another "no action needed" event) rather than an
// actionable failure, so clients can demote and verbosity-gate those rows by
// contract instead of matching prose.

import { describe, expect, test } from "vitest";
import payloadsGo from "../../agent/events/payloads.go?raw";
import type { ItemModel } from "./model";
import { goConstantValue } from "./testing/goConstants";
import { isInformationalWarning, WarningCodeContextBudget } from "./warnings";

// goWarningCode reads one warning code's value out of agent/events/payloads.go
// (goConstants.ts's own header says why reading the source is what makes the
// binding real).
function goWarningCode(name: string): string {
  return goConstantValue(payloadsGo, name, "agent/events/payloads.go");
}

describe("the informational context-budget code is bound to agent/events/payloads.go", () => {
  test("the exported value is the daemon's own constant", () => {
    expect(WarningCodeContextBudget).toBe(goWarningCode("WarningCodeContextBudget"));
  });
});

function warningItem(overrides: Partial<ItemModel> = {}): ItemModel {
  return {
    id: "item_1",
    turnId: "turn_1",
    type: "warning",
    text: "budget arithmetic",
    status: "completed",
    ...overrides,
  };
}

describe("isInformationalWarning", () => {
  test("true for a warning item carrying an informational code", () => {
    expect(isInformationalWarning(warningItem({ warning: { code: WarningCodeContextBudget } }))).toBe(true);
  });

  test("false for an uncoded warning, another code, and a coded non-warning item", () => {
    expect(isInformationalWarning(warningItem())).toBe(false);
    expect(isInformationalWarning(warningItem({ warning: { code: "delegate_abandoned_by_drain" } }))).toBe(false);
    expect(isInformationalWarning(warningItem({ type: "steering", warning: { code: WarningCodeContextBudget } }))).toBe(
      false,
    );
  });
});
