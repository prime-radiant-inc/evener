import { afterEach, describe, expect, test, vi } from "vitest";
import { copyText } from "./clipboard";

const originalClipboard = navigator.clipboard;

afterEach(() => {
  Object.defineProperty(navigator, "clipboard", { value: originalClipboard, configurable: true });
  vi.restoreAllMocks();
});

describe("copyText", () => {
  test("uses the async Clipboard API when available", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const ok = await copyText("ABCD-1234");
    expect(writeText).toHaveBeenCalledWith("ABCD-1234");
    expect(ok).toBe(true);
  });

  test("falls back to execCommand when the Clipboard API is unavailable", async () => {
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    const execCommand = vi.fn().mockReturnValue(true);
    document.execCommand = execCommand;
    const ok = await copyText("ABCD-1234");
    expect(execCommand).toHaveBeenCalledWith("copy");
    expect(ok).toBe(true);
  });

  test("falls back to execCommand when the Clipboard API throws (e.g. non-secure context)", async () => {
    const writeText = vi.fn().mockRejectedValue(new Error("not allowed"));
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    const execCommand = vi.fn().mockReturnValue(true);
    document.execCommand = execCommand;
    const ok = await copyText("ABCD-1234");
    expect(execCommand).toHaveBeenCalledWith("copy");
    expect(ok).toBe(true);
  });

  test("reports failure when neither path succeeds", async () => {
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    document.execCommand = vi.fn().mockReturnValue(false);
    const ok = await copyText("ABCD-1234");
    expect(ok).toBe(false);
  });

  test("the execCommand fallback does not leave a stray textarea in the document", async () => {
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    document.execCommand = vi.fn().mockReturnValue(true);
    await copyText("ABCD-1234");
    expect(document.querySelector("textarea")).toBeNull();
  });
});
