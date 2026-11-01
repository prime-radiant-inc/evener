import { describe, expect, it } from "vitest";
import { getToasts } from "../../../widgets/toast/store";
import { pushRecoveryRefusal, pushSelectionRefusal, selectionRefusal } from "./submitProtocol";

describe("selectionRefusal", () => {
  it("refuses staged selections the target never advertised", () => {
    expect(selectionRefusal({ skillNames: ["a"] }, undefined)).toBe("skill-unsupported");
    expect(selectionRefusal({ skillNames: ["a"] }, { skillInput: false })).toBe("skill-unsupported");
    expect(selectionRefusal({ commandNames: ["c"] }, { skillInput: true })).toBe("command-unsupported");
  });

  it("accepts selections the target advertises and empty selections anywhere", () => {
    expect(selectionRefusal({ skillNames: ["a"], commandNames: ["c"] }, { skillInput: true, commandInput: true })).toBe(
      undefined,
    );
    expect(selectionRefusal({ skillNames: [], commandNames: [] }, undefined)).toBe(undefined);
    expect(selectionRefusal({}, undefined)).toBe(undefined);
  });
});

describe("refusal toasts", () => {
  it("names each refusal outcome by reason so surfaces report the same thing", () => {
    const before = getToasts().length;
    pushSelectionRefusal("skill-unsupported");
    pushSelectionRefusal("command-unsupported");
    pushRecoveryRefusal("changed");
    pushRecoveryRefusal("consumed");
    expect(
      getToasts()
        .slice(before)
        .map((toast) => [toast.kind, toast.reason]),
    ).toEqual([
      ["error", "skill-unsupported"],
      ["error", "command-unsupported"],
      ["info", "changed"],
      ["info", "consumed"],
    ]);
  });
});
