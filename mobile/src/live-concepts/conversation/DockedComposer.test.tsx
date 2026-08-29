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

afterEach(cleanup);

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
