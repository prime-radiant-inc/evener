// @vitest-environment node

// The informational-warning contract: which warning codes mark a notice as
// budget arithmetic (or another "no action needed" event) rather than an
// actionable failure, so clients can demote and verbosity-gate those rows by
// contract instead of matching prose.

import { describe, expect, test } from "vitest";
import payloadsGo from "../../agent/events/payloads.go?raw";
import type { ItemModel } from "./model";
import { goConstantValue } from "./testing/goConstants";
import {
  attentionWarningNotice,
  isInformationalWarning,
  WarningCodeContextBudget,
  WarningCodeDelegateAttentionRestore,
  WarningCodeMCPReconnected,
  warningWords,
} from "./warnings";

// goWarningCode reads one warning code's value out of agent/events/payloads.go
// (goConstants.ts's own header says why reading the source is what makes the
// binding real).
function goWarningCode(name: string): string {
  return goConstantValue(payloadsGo, name, "agent/events/payloads.go");
}

describe("the informational codes are bound to agent/events/payloads.go", () => {
  test("the MCP recovery code is the daemon's own constant", () => {
    expect(WarningCodeMCPReconnected).toBe(goWarningCode("WarningCodeMCPReconnected"));
  });
  test("the exported value is the daemon's own constant", () => {
    expect(WarningCodeContextBudget).toBe(goWarningCode("WarningCodeContextBudget"));
  });

  test("the delegate-attention restore code is the daemon's own constant", () => {
    expect(WarningCodeDelegateAttentionRestore).toBe(goWarningCode("WarningCodeDelegateAttentionRestore"));
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
  test.each(["warning", "systemMessage"])("MCP recovery is informational through %s", (type) => {
    const warning = { code: WarningCodeMCPReconnected };
    const notice = warningItem({ type, eventKind: "warning", warning, raw: { warning } });
    expect(isInformationalWarning(notice)).toBe(true);
    expect(attentionWarningNotice(notice)).toBeNull();
  });

  test("true for a warning item carrying an informational code", () => {
    expect(isInformationalWarning(warningItem({ warning: { code: WarningCodeContextBudget } }))).toBe(true);
  });

  // The daemon's warnings arrive as overlay notices: a systemMessage with
  // eventKind "warning" and its code on raw.warning.code.
  test("true for a warning notice carrying an informational code on its raw", () => {
    const notice = warningItem({
      type: "systemMessage",
      eventKind: "warning",
      raw: { warning: { title: "Context budget", code: WarningCodeContextBudget } },
    });
    expect(isInformationalWarning(notice)).toBe(true);
  });

  // A failed restore of a cold delegate's attention: the daemon retries it on
  // its own, so it is detail for Full, not an alarm at every level.
  test("true for a delegate-attention restore warning, as an item or a notice", () => {
    expect(isInformationalWarning(warningItem({ warning: { code: WarningCodeDelegateAttentionRestore } }))).toBe(true);
    expect(
      isInformationalWarning(
        warningItem({
          type: "systemMessage",
          eventKind: "warning",
          raw: { warning: { title: "Evener error", code: WarningCodeDelegateAttentionRestore } },
        }),
      ),
    ).toBe(true);
  });

  test("false for an uncoded warning notice, and for a coded notice of another kind", () => {
    expect(
      isInformationalWarning(
        warningItem({ type: "systemMessage", eventKind: "warning", raw: { warning: { title: "Careful" } } }),
      ),
    ).toBe(false);
    expect(isInformationalWarning(warningItem({ type: "systemMessage", eventKind: "warning" }))).toBe(false);
    expect(
      isInformationalWarning(
        warningItem({
          type: "systemMessage",
          eventKind: "error",
          raw: { warning: { code: WarningCodeContextBudget } },
        }),
      ),
    ).toBe(false);
  });

  test("false for an uncoded warning, another code, and a coded non-warning item", () => {
    expect(isInformationalWarning(warningItem())).toBe(false);
    expect(isInformationalWarning(warningItem({ warning: { code: "delegate_abandoned_by_drain" } }))).toBe(false);
    expect(isInformationalWarning(warningItem({ type: "steering", warning: { code: WarningCodeContextBudget } }))).toBe(
      false,
    );
  });
});

// A daemon warning that isn't informational is a failure a human should see:
// both clients render it with the warning treatment, reading its title and
// hint from the notice's raw.warning (#3387).
describe("attentionWarningNotice", () => {
  const notice = (raw: unknown) => warningItem({ type: "systemMessage", eventKind: "warning", text: "disk full", raw });

  test("reads an uncoded warning notice's message, title and hint", () => {
    expect(
      attentionWarningNotice(notice({ warning: { title: "Evener error", hint: "Free some space, then retry." } })),
    ).toEqual({ message: "disk full", title: "Evener error", hint: "Free some space, then retry." });
  });

  test("leaves out a blank or non-string title and hint", () => {
    expect(attentionWarningNotice(notice({ warning: { title: "  ", hint: 3 } }))).toEqual({ message: "disk full" });
    expect(attentionWarningNotice(notice(undefined))).toEqual({ message: "disk full" });
  });

  test("reads its hint, then its title, as the message when it has no text", () => {
    const blank = (raw: unknown) =>
      attentionWarningNotice(warningItem({ type: "systemMessage", eventKind: "warning", text: " ", raw }));
    expect(blank({ warning: { title: "Evener error", hint: "Retry." } })).toEqual({
      message: "Retry.",
      title: "Evener error",
    });
    expect(blank({ warning: { title: "Evener error" } })).toEqual({ message: "Evener error" });
  });

  // A warning with nothing to show draws no block: it stays the (empty)
  // quiet line, as a blank type:"warning" item draws nothing.
  test("is null for a warning notice with no text, title or hint", () => {
    expect(
      attentionWarningNotice(
        warningItem({ type: "systemMessage", eventKind: "warning", text: "  ", raw: { warning: { title: " " } } }),
      ),
    ).toBeNull();
  });

  test("is null for an informational warning, and for anything but a warning notice", () => {
    expect(
      attentionWarningNotice(notice({ warning: { title: "Context budget", code: WarningCodeContextBudget } })),
    ).toBeNull();
    expect(attentionWarningNotice(warningItem({ type: "systemMessage", eventKind: "plugin_loaded" }))).toBeNull();
    expect(attentionWarningNotice(warningItem({ warning: { title: "Relay" } }))).toBeNull();
  });
});

// Every warning's words, shown once each: both clients render a warning from
// these, so a title-only or hint-only warning reads the same on both (#3387).
describe("warningWords", () => {
  test("keeps the text as the message, with its title and hint beside it", () => {
    expect(warningWords("disk full", "Evener error", "Retry.")).toEqual({
      message: "disk full",
      title: "Evener error",
      hint: "Retry.",
    });
  });

  // The hint before the title: a title is a label ("Context budget"), the
  // hint a sentence that can stand as the line.
  test("falls back to the hint, then the title, for a missing or blank message", () => {
    expect(warningWords(undefined, "Evener error", "Retry.")).toEqual({ message: "Retry.", title: "Evener error" });
    expect(warningWords("  ", "Evener error", undefined)).toEqual({ message: "Evener error" });
  });

  test("drops a hint or title that says what the message says", () => {
    expect(warningWords("disk full", "disk full", "disk full")).toEqual({ message: "disk full" });
  });

  test("is null when nothing is a non-blank string", () => {
    expect(warningWords(" ", 3, undefined)).toBeNull();
  });
});
