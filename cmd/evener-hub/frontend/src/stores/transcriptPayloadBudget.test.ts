import { describe, expect, test } from "vitest";
import { countUtf8Bytes, encodedPayloadBytes, TranscriptPayloadBudget } from "./transcriptPayloadBudget";

describe("transcript payload budget", () => {
  test("counts UTF-8 JSON payload bytes including emoji and structured output", () => {
    const value = { text: "🙂", raw: { output: ["工具", 7] } };
    expect(encodedPayloadBytes(value)).toBe(new TextEncoder().encode(JSON.stringify(value)).byteLength);
    expect(countUtf8Bytes("🙂")).toBe(4);
  });

  test("refuses unencodable payload and over-budget reservations", () => {
    expect(encodedPayloadBytes(undefined)).toBe(Number.POSITIVE_INFINITY);
    const budget = new TranscriptPayloadBudget(10);
    expect(budget.reserve(7)).toBe(true);
    expect(budget.reserve(4)).toBe(false);
    expect(budget.used).toBe(7);
    budget.release(7);
    expect(budget.used).toBe(0);
  });
});
