import { expect, test, vi } from "vitest";
import { foldWarningParams } from "./warningFold";

test("foldWarningParams scans each stored warning string once", () => {
  const scannedLengths: number[] = [];
  const originalExec = RegExp.prototype.exec;
  const spy = vi.spyOn(RegExp.prototype, "exec").mockImplementation(function (this: RegExp, value: string) {
    if (this.source === "\\S") scannedLengths.push(value.length);
    return originalExec.call(this, value);
  });

  let folded: ReturnType<typeof foldWarningParams>;
  try {
    folded = foldWarningParams({ threadId: "thr_t", ref: "ref_t", title: "Title", hint: "Hint", source: "Source" });
  } finally {
    spy.mockRestore();
  }

  expect(folded).toEqual({ text: "", title: "Title", hint: "Hint", source: "Source" });
  expect([...scannedLengths].sort((a, b) => a - b)).toEqual(
    ["Title".length, "Hint".length, "Source".length].sort((a, b) => a - b),
  );
});
