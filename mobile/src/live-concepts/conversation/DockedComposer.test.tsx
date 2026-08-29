import { readFileSync } from "node:fs";
import path from "node:path";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { DockedComposer } from "./DockedComposer";
import type { ConversationFrameAction, LiveComposerView } from "./primitives";

const composer = (
  overrides: Partial<LiveComposerView> = {},
): LiveComposerView => ({
  draft: "",
  canSend: true,
  canSteer: true,
  canQueue: true,
  canInterrupt: true,
  pending: null,
  accepted: null,
  error: null,
  ...overrides,
});

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe("DockedComposer", () => {
  it.each(["send", "steer", "queue"] as const)(
    "dispatches %s mode and submit exactly",
    (mode) => {
      const dispatch = vi.fn<(action: ConversationFrameAction) => void>();
      render(
        <DockedComposer
          composer={composer({ draft: "exact draft" })}
          mode="send"
          appearance={{ density: "compact", accent: "forest" }}
          dispatch={dispatch}
        />,
      );
      fireEvent.click(screen.getByRole("button", { name: `Use ${mode} mode` }));
      expect(dispatch).toHaveBeenCalledWith({ type: "setComposerMode", mode });
      dispatch.mockClear();
      fireEvent.click(screen.getByRole("button", { name: "Submit message" }));
      expect(dispatch).toHaveBeenCalledWith({ type: "submit", mode: "send" });
    },
  );

  it("preserves the exact controlled draft and dispatches edits", () => {
    const dispatch = vi.fn<(action: ConversationFrameAction) => void>();
    const exact = " line one\nline two  ";
    render(
      <DockedComposer
        composer={composer({ draft: exact })}
        mode="queue"
        appearance={{ density: "comfortable", accent: "luminous" }}
        dispatch={dispatch}
      />,
    );
    const textbox = screen.getByRole("textbox", { name: "Message" });
    expect(textbox).toHaveValue(exact);
    fireEvent.change(textbox, { target: { value: `${exact}!` } });
    expect(dispatch).toHaveBeenCalledWith({
      type: "setDraft",
      value: `${exact}!`,
    });
  });

  it("disables unavailable capabilities with explanations and keeps interrupt independent", () => {
    const dispatch = vi.fn<(action: ConversationFrameAction) => void>();
    render(
      <DockedComposer
        composer={composer({
          canSend: false,
          canSteer: false,
          canQueue: false,
          canInterrupt: true,
        })}
        mode="send"
        appearance={{ density: "comfortable", accent: "rust" }}
        dispatch={dispatch}
      />,
    );
    expect(screen.getByRole("textbox", { name: "Message" })).toBeDisabled();
    expect(
      screen.getByText("Send is unavailable for this conversation."),
    ).toBeVisible();
    expect(
      screen.getByText("Steer is unavailable for this conversation."),
    ).toBeVisible();
    expect(
      screen.getByText("Queue is unavailable for this conversation."),
    ).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Interrupt" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "interrupt" });
  });

  it("locks editing during a mutation while showing the exact snapshot", () => {
    render(
      <DockedComposer
        composer={composer({
          draft: "submitted bytes",
          pending: {
            kind: "send",
            status: "pending",
            draftSnapshot: "submitted bytes",
            generation: 9,
          },
        })}
        mode="send"
        appearance={{ density: "compact", accent: "forest" }}
        dispatch={vi.fn()}
      />,
    );
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveValue(
      "submitted bytes",
    );
    expect(screen.getByRole("textbox", { name: "Message" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Submit message" }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: "Interrupt" })).toBeEnabled();
  });

  it("restores a failed mutation draft for editing and resubmission", () => {
    const dispatch = vi.fn<(action: ConversationFrameAction) => void>();
    render(
      <DockedComposer
        composer={composer({
          draft: "failed exact draft",
          pending: {
            kind: "send",
            status: "failed",
            draftSnapshot: "failed exact draft",
            generation: 10,
          },
        })}
        mode="send"
        appearance={{ density: "compact", accent: "forest" }}
        dispatch={dispatch}
      />,
    );
    const textbox = screen.getByRole("textbox", { name: "Message" });
    expect(textbox).toBeEnabled();
    expect(textbox).toHaveValue("failed exact draft");
    fireEvent.change(textbox, { target: { value: "recovered draft" } });
    expect(dispatch).toHaveBeenCalledWith({
      type: "setDraft",
      value: "recovered draft",
    });
    fireEvent.click(screen.getByRole("button", { name: "Submit message" }));
    expect(dispatch).toHaveBeenCalledWith({ type: "submit", mode: "send" });
  });

  it("measures content growth through six lines, then enables native scrolling", () => {
    const baseProps = {
      mode: "send" as const,
      appearance: { density: "compact" as const, accent: "forest" as const },
      dispatch: vi.fn<(action: ConversationFrameAction) => void>(),
    };
    const { rerender } = render(
      <DockedComposer {...baseProps} composer={composer({ draft: "one" })} />,
    );
    const textbox = screen.getByRole("textbox", {
      name: "Message",
    }) as HTMLTextAreaElement;
    vi.spyOn(window, "getComputedStyle").mockReturnValue({
      lineHeight: "24px",
      fontSize: "16px",
      paddingBlockStart: "8px",
      paddingBlockEnd: "8px",
      getPropertyValue: () => "",
    } as unknown as CSSStyleDeclaration);
    Object.defineProperty(textbox, "scrollHeight", {
      configurable: true,
      value: 48,
    });
    rerender(
      <DockedComposer {...baseProps} composer={composer({ draft: "two" })} />,
    );
    expect(textbox.style.blockSize).toBe("48px");
    expect(textbox.style.overflowY).toBe("hidden");

    Object.defineProperty(textbox, "scrollHeight", {
      configurable: true,
      value: 150,
    });
    rerender(
      <DockedComposer {...baseProps} composer={composer({ draft: "six" })} />,
    );
    expect(textbox.style.blockSize).toBe("150px");
    expect(textbox.style.overflowY).toBe("hidden");

    Object.defineProperty(textbox, "scrollHeight", {
      configurable: true,
      value: 220,
    });
    rerender(
      <DockedComposer {...baseProps} composer={composer({ draft: "seven" })} />,
    );
    expect(textbox.style.blockSize).toBe("160px");
    expect(textbox.style.overflowY).toBe("auto");
  });

  it("caps text area growth at six lines in production CSS", () => {
    const css = readFileSync(
      path.join(__dirname, "conversation-frame.css"),
      "utf8",
    );
    expect(css.replace(/\/\*[\s\S]*?\*\//gu, "")).toContain(
      "max-block-size: calc(6 * 1.5em + 1rem)",
    );
  });
});
