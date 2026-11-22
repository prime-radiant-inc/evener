import { afterEach, describe, expect, test, vi } from "vitest";
import { copyText } from "./clipboard";

const originalClipboard = navigator.clipboard;
const originalExecCommand = document.execCommand;

afterEach(() => {
  Object.defineProperty(navigator, "clipboard", { value: originalClipboard, configurable: true });
  Object.defineProperty(document, "execCommand", { value: originalExecCommand, configurable: true, writable: true });
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

  test("the execCommand fallback restores focus to the element that had it", async () => {
    Object.defineProperty(navigator, "clipboard", { value: undefined, configurable: true });
    document.execCommand = vi.fn().mockReturnValue(true);
    const button = document.createElement("button");
    document.body.appendChild(button);
    try {
      button.focus();
      expect(document.activeElement).toBe(button);

      const ok = await copyText("ABCD-1234");

      expect(ok).toBe(true);
      expect(document.activeElement).toBe(button);
    } finally {
      button.remove();
    }
  });

  test("the execCommand fallback uses the document that owns the copy button", async () => {
    const frame = document.createElement("iframe");
    document.body.appendChild(frame);
    try {
      const popout = frame.contentDocument;
      const popoutWindow = frame.contentWindow;
      if (!popout || !popoutWindow) throw new Error("iframe has no contentDocument");
      Object.defineProperty(popoutWindow.navigator, "clipboard", { value: undefined, configurable: true });
      const execCommand = vi.fn().mockImplementation(() => {
        expect(popout.querySelector("textarea")).not.toBeNull();
        return true;
      });
      popout.execCommand = execCommand;
      const button = popout.createElement("button");
      popout.body.appendChild(button);
      button.focus();
      expect(popout.activeElement).toBe(button);

      const ok = await copyText("ABCD-1234", popout);

      expect(ok).toBe(true);
      expect(execCommand).toHaveBeenCalledExactlyOnceWith("copy");
      expect(popout.activeElement).toBe(button);
      expect(document.querySelector("textarea")).toBeNull();
    } finally {
      frame.remove();
    }
  });
});
