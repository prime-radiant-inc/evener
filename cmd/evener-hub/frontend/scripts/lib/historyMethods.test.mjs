import { expect, test } from "vitest";
import { isHistoryPageMethod, isHistoryReadMethod } from "./historyMethods.mjs";

test("recognizes both history read aliases and rejects pages and other methods", () => {
  for (const method of ["thread/read", "evener/thread/history/read"]) expect(isHistoryReadMethod(method)).toBe(true);
  for (const method of ["thread/turns/list", "evener/thread/history/turns/list", "evener/thread/delegates/list", "ping", undefined]) {
    expect(isHistoryReadMethod(method)).toBe(false);
  }
});

test("recognizes both history page aliases and rejects reads and other methods", () => {
  for (const method of ["thread/turns/list", "evener/thread/history/turns/list"]) expect(isHistoryPageMethod(method)).toBe(true);
  for (const method of ["thread/read", "evener/thread/history/read", "evener/thread/delegates/list", "ping", undefined]) {
    expect(isHistoryPageMethod(method)).toBe(false);
  }
});
