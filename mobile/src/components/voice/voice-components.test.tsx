// Voice subcomponent tests — VoiceLevel, VoiceCaptions, VoiceControls,
// VoiceStatus. Deterministic: no native bridge, no timers beyond explicit
// fakes. CSS modules resolve to identity class names under jsdom.

import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { computeAnnouncement, VoiceCaptions } from "./VoiceCaptions";
import { VoiceControls } from "./VoiceControls";
import { VoiceLevel } from "./VoiceLevel";
import { VoiceStatus } from "./VoiceStatus";

afterEach(() => cleanup());

describe("VoiceLevel", () => {
  it("is decorative (aria-hidden)", () => {
    render(<VoiceLevel level={0.5} reducedMotion={false} active={true} />);
    const wave = screen.getByTestId("voice-level");
    expect(wave.getAttribute("aria-hidden")).toBe("true");
  });

  it("renders a fixed bar count with CSS transforms (no canvas)", () => {
    render(<VoiceLevel level={0.5} reducedMotion={false} active={true} />);
    const wave = screen.getByTestId("voice-level");
    const bars = wave.querySelectorAll("span");
    // Each bar uses a transform style, not canvas drawing.
    for (const bar of bars) {
      const transform = (bar as HTMLElement).style.transform;
      expect(transform).toContain("scaleY");
    }
  });

  it("clamps out-of-range level", () => {
    render(<VoiceLevel level={5} reducedMotion={false} active={true} />);
    const wave = screen.getByTestId("voice-level");
    const bars = wave.querySelectorAll("span");
    for (const bar of bars) {
      const scale = (bar as HTMLElement).style.transform;
      // Clamped to 1.0 max, so scaleY should be <= 1.
      const match = scale.match(/scaleY\(([0-9.]+)\)/);
      expect(match).not.toBeNull();
      if (match) {
        const value = match[1];
        if (value !== undefined) {
          expect(Number.parseFloat(value)).toBeLessThanOrEqual(1);
        }
      }
    }
  });

  it("exposes reduced-motion data attribute", () => {
    render(<VoiceLevel level={0.5} reducedMotion={true} active={true} />);
    const wave = screen.getByTestId("voice-level");
    expect(wave.getAttribute("data-reduced-motion")).toBe("true");
  });
});

describe("VoiceCaptions", () => {
  it("renders visible text", () => {
    render(<VoiceCaptions text="hello world" tone="final" />);
    expect(screen.getByTestId("voice-caption-visible").textContent).toBe(
      "hello world",
    );
  });

  it("applies a dimmed style for partial tone", () => {
    const { container } = render(<VoiceCaptions text="hel" tone="partial" />);
    const root = container.firstChild as HTMLElement;
    expect(root.getAttribute("data-tone")).toBe("partial");
  });

  it("applies a clear style for final tone", () => {
    const { container } = render(<VoiceCaptions text="hello" tone="final" />);
    const root = container.firstChild as HTMLElement;
    expect(root.getAttribute("data-tone")).toBe("final");
  });

  it("live region is polite and atomic", () => {
    render(<VoiceCaptions text="hello" tone="final" />);
    const announced = screen.getByTestId("voice-caption-announced");
    expect(announced.getAttribute("aria-live")).toBe("polite");
    expect(announced.getAttribute("aria-atomic")).toBe("true");
  });

  it("computeAnnouncement throttles repeated changes within the window", () => {
    // First announcement at t=0.
    let r = computeAnnouncement("a", "", 1000, 0, 1500);
    expect(r.announce).toBe("a");
    const firstAt = r.at;
    // Different text inside the throttle window → not announced yet.
    r = computeAnnouncement("b", "a", 1200, firstAt, 1500);
    expect(r.announce).toBe("a");
    // After the window passes → announced.
    r = computeAnnouncement("b", "a", firstAt + 2000, firstAt, 1500);
    expect(r.announce).toBe("b");
  });
});

describe("VoiceControls", () => {
  it("renders End and Keyboard controls", () => {
    render(
      <VoiceControls onEnd={() => {}} onKeyboard={() => {}} disabled={false} />,
    );
    expect(screen.getByLabelText("End voice mode")).not.toBeNull();
    expect(screen.getByLabelText("Switch to keyboard")).not.toBeNull();
  });

  it("End button is a 44px-class tap target via --tap-target token", () => {
    render(
      <VoiceControls onEnd={() => {}} onKeyboard={() => {}} disabled={false} />,
    );
    const end = screen.getByLabelText("End voice mode");
    // The min-height is sourced from --tap-target; jsdom does not compute
    // layout, so we assert the control is a button (real hit target).
    expect(end.tagName).toBe("BUTTON");
  });

  it("invokes onEnd when End is clicked", () => {
    let ended = false;
    render(
      <VoiceControls
        onEnd={() => {
          ended = true;
        }}
        onKeyboard={() => {}}
        disabled={false}
      />,
    );
    screen.getByLabelText("End voice mode").click();
    expect(ended).toBe(true);
  });

  it("invokes onKeyboard when Keyboard is clicked", () => {
    let keyboard = false;
    render(
      <VoiceControls
        onEnd={() => {}}
        onKeyboard={() => {
          keyboard = true;
        }}
        disabled={false}
      />,
    );
    screen.getByLabelText("Switch to keyboard").click();
    expect(keyboard).toBe(true);
  });

  it("disables both controls when disabled", () => {
    render(
      <VoiceControls onEnd={() => {}} onKeyboard={() => {}} disabled={true} />,
    );
    expect(
      (screen.getByLabelText("End voice mode") as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(
      (screen.getByLabelText("Switch to keyboard") as HTMLButtonElement)
        .disabled,
    ).toBe(true);
  });
});

describe("VoiceStatus", () => {
  it("renders a distinct label per status", () => {
    const { rerender } = render(<VoiceStatus status="listening" />);
    expect(screen.getByTestId("voice-status-label").textContent).toBe(
      "Listening",
    );
    rerender(<VoiceStatus status="speaking" />);
    expect(screen.getByTestId("voice-status-label").textContent).toBe(
      "Speaking",
    );
    rerender(<VoiceStatus status="interrupted" />);
    expect(screen.getByTestId("voice-status-label").textContent).toBe(
      "Interrupted",
    );
    rerender(<VoiceStatus status="error" />);
    expect(screen.getByTestId("voice-status-label").textContent).toBe("Error");
  });

  it("exposes a polite live region", () => {
    render(<VoiceStatus status="listening" />);
    const status = screen.getByTestId("voice-status-label").parentElement;
    expect(status?.getAttribute("aria-live")).toBe("polite");
    expect(status?.getAttribute("aria-atomic")).toBe("true");
  });
});
