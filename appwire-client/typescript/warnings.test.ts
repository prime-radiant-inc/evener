// @vitest-environment node

// The informational-warning contract: which warning codes mark a notice as
// budget arithmetic (or another "no action needed" event) rather than an
// actionable failure, so clients can demote and verbosity-gate those rows by
// contract instead of matching prose.

import { describe, expect, test } from "vitest";
import payloadsGo from "../../agent/events/payloads.go?raw";
import type { ItemModel } from "./model";
import { isInformationalWarning, WarningCodeContextBudget } from "./warnings";

// goWarningCode reads one warning code's value out of
// agent/events/payloads.go, the file the daemon stamps every WarningData.Code
// from. Reading the source (the way errors.test.ts binds its discriminants to
// appwire/errors.go) is what makes the binding real: renaming the constant or
// changing its value in Go fails here, rather than leaving every client
// matching a code the daemon no longer sends.
function goWarningCode(name: string): string {
  const match = payloadsGo.match(new RegExp(`${name}\\s*=\\s*"([^"]+)"`));
  if (!match) throw new Error(`agent/events/payloads.go has no ${name} constant`);
  return match[1]!;
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
