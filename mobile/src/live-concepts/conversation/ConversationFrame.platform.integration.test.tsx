import { readFileSync } from "node:fs";
import path from "node:path";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createViewportCoordinator } from "../../ui/platformPresentation";
import { ConversationFrame } from "./ConversationFrame";
import type { ConversationFrameProps, ConversationSkin } from "./contract";

class MutableViewport extends EventTarget {
  constructor(
    public height: number,
    public offsetTop: number,
  ) {
    super();
  }
  set(next: { height: number; offsetTop: number }) {
    this.height = next.height;
    this.offsetTop = next.offsetTop;
    this.dispatchEvent(new Event("resize"));
    this.dispatchEvent(new Event("scroll"));
  }
}

const bounded = (text: string) => ({
  text,
  truncated: false,
  originalUtf8Bytes: text.length,
});
const skin: ConversationSkin = {
  id: "stillwater",
  className: "integration-skin",
  composerAppearance: { density: "compact", accent: "forest" },
  renderNarrativeItem: ({ body }) => body,
  renderActivityMarker: ({ item }) => item.label.text,
  renderConversationChrome: ({ title }) => title.text,
};

function frameProps(): ConversationFrameProps {
  return {
    state: {
      concept: "stillwater",
      platform: "ios",
      appearance: "system",
      contentSize: "large",
      reducedMotion: false,
      phase: "ready",
      connection: { status: "connected" },
      conversation: {
        threadKey: "thread",
        title: bounded("Title"),
        project: bounded("Project"),
        status: bounded("Ready"),
        items: [],
        evidence: [],
        questions: [],
        olderAvailable: false,
        tone: "idle",
        updatedLabel: null,
      },
      composer: {
        draft: "draft",
        canSend: true,
        canSteer: true,
        canQueue: true,
        canInterrupt: true,
        pending: null,
        accepted: null,
        error: null,
      },
      composerMode: "send",
      questionDrafts: {},
      anchor: null,
      focusedItemKey: null,
      unseen: 0,
      openEvidenceKey: null,
      evidenceTriggerKey: null,
    },
    skin,
    dispatch: () => undefined,
    onAnchorChange: () => undefined,
    onUnseenChange: () => undefined,
    onFocusIntentChange: () => undefined,
  };
}

const root = path.join(__dirname, "..", "..");
const css = (relative: string) =>
  readFileSync(path.join(root, relative), "utf8").replace(
    /\/\*[\s\S]*?\*\//gu,
    "",
  );

function numberToken(name: string): number {
  return Number.parseFloat(
    document.documentElement.style.getPropertyValue(name),
  );
}

function elementRect(top: number, bottom: number): DOMRect {
  return {
    top,
    bottom,
    left: 0,
    right: 100,
    width: 100,
    height: bottom - top,
    x: 0,
    y: top,
    toJSON: () => ({}),
  } as DOMRect;
}

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  document.documentElement.removeAttribute("style");
});

describe("ConversationFrame platform integration", () => {
  it("uses coordinator tokens to subtract keyboard once and apply safe area once", () => {
    const viewport = new MutableViewport(532, 0);
    const fakeWindow = Object.create(window) as Window & typeof globalThis;
    Object.defineProperty(fakeWindow, "innerHeight", {
      configurable: true,
      value: 852,
    });
    const coordinator = createViewportCoordinator({
      visualViewport: viewport,
      window: fakeWindow,
      document,
    });
    coordinator.start();
    document.documentElement.style.setProperty("--safe-area-bottom", "34px");
    const view = render(<ConversationFrame {...frameProps()} />);

    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe("852px");
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("320px");
    expect(
      numberToken("--viewport-height") - numberToken("--keyboard-inset"),
    ).toBe(532);
    const composer = view.container.querySelector<HTMLElement>(
      '[data-frame-part="composer"][data-live-conversation-composer="true"]',
    );
    expect(composer).not.toBeNull();
    const frame = view.getByRole("main", { name: "Conversation" });
    frame.getBoundingClientRect = () => elementRect(0, 532);
    if (composer === null) throw new Error("composer target missing");
    composer.getBoundingClientRect = () => elementRect(430, 532);
    const frameBottom =
      numberToken("--viewport-height") - numberToken("--keyboard-inset");
    expect(frameBottom).toBe(532);
    expect(frameBottom).toBe(viewport.offsetTop + viewport.height);
    expect(frame.getBoundingClientRect().bottom).toBe(532);
    expect(composer.getBoundingClientRect().bottom).toBe(532);
    expect(
      document.documentElement.style.getPropertyValue("--safe-area-bottom"),
    ).toBe("34px");
    const frameCss = css("live-concepts/conversation/conversation-frame.css");
    expect(frameCss).toMatch(
      /block-size:\s*calc\(\s*var\(--viewport-height, 100dvh\)\s*-\s*var\(--keyboard-inset, 0px\)\s*\)/u,
    );
    expect(frameCss).toContain(
      "padding-block-end: var(--safe-area-bottom, 0px)",
    );
    expect(frameCss.match(/keyboard-inset/gu)).toHaveLength(1);

    for (const control of [
      ...screen.getAllByRole("button"),
      ...screen.getAllByRole("textbox"),
    ]) {
      expect(control).toBeVisible();
    }
    coordinator.stop();
  });

  it("starts panned with composer focused and conditionally settles top controls in the current viewport", async () => {
    const viewport = new MutableViewport(500, 47);
    const fakeWindow = Object.create(window) as Window & typeof globalThis;
    Object.defineProperty(fakeWindow, "innerHeight", {
      configurable: true,
      value: 852,
    });
    const coordinator = createViewportCoordinator({
      visualViewport: viewport,
      window: fakeWindow,
      document,
    });
    coordinator.start();
    const view = render(<ConversationFrame {...frameProps()} />);

    expect(
      document.documentElement.style.getPropertyValue("--viewport-height"),
    ).toBe("852px");
    expect(
      document.documentElement.style.getPropertyValue("--keyboard-inset"),
    ).toBe("305px");
    expect(
      numberToken("--viewport-height") - numberToken("--keyboard-inset"),
    ).toBe(547);

    const frame = view.getByRole("main", { name: "Conversation" });
    const composerWrapper = view.container.querySelector<HTMLElement>(
      '[data-frame-part="composer"][data-live-conversation-composer="true"]',
    );
    if (composerWrapper === null) throw new Error("composer target missing");
    frame.getBoundingClientRect = () => elementRect(0, 547);
    composerWrapper.getBoundingClientRect = () => elementRect(470, 547);
    expect(frame.getBoundingClientRect().bottom).toBe(547);
    expect(composerWrapper.getBoundingClientRect().bottom).toBe(547);

    const composer = screen.getByRole("textbox", { name: "Message" });
    composer.getBoundingClientRect = () => elementRect(490, 534);
    composer.focus();
    expect(document.activeElement).toBe(composer);

    for (const control of [
      screen.getByRole("button", { name: "Back" }),
      screen.getByRole("button", { name: "Work" }),
      screen.getByRole("button", { name: "Switch concept" }),
    ]) {
      let rect = { top: 0, bottom: 44 };
      control.getBoundingClientRect = () => elementRect(rect.top, rect.bottom);
      const scrollIntoView = vi.fn(() => {
        queueMicrotask(() => {
          rect = { top: viewport.offsetTop, bottom: viewport.offsetTop + 44 };
          viewport.dispatchEvent(new Event("scroll"));
        });
      });
      control.scrollIntoView = scrollIntoView;
      control.focus();
      await waitFor(() => {
        const current = {
          top: viewport.offsetTop,
          bottom: viewport.offsetTop + viewport.height,
        };
        const settled = control.getBoundingClientRect();
        expect(document.activeElement).toBe(control);
        expect(settled.bottom).toBeGreaterThan(current.top);
        expect(settled.top).toBeLessThan(current.bottom);
      });
      expect(scrollIntoView).toHaveBeenCalledWith({
        block: "nearest",
        inline: "nearest",
      });
      composer.focus();
    }
    coordinator.stop();
  });

  it("audits existing shell, Voice, dock, and old composer safe-area ownership", () => {
    expect(css("ui/global.css")).toMatch(
      /\.evener-shell\s*\{[^}]*height:\s*var\(--viewport-height\)/su,
    );
    expect(css("screens/VoiceScreen.module.css")).toMatch(
      /\.screen\s*\{[^}]*height:\s*var\(--viewport-height\)/su,
    );
    expect(css("ui/global.css")).toMatch(
      /\.evener-dock\s*\{[^}]*padding-bottom:\s*max\(var\(--keyboard-inset\),\s*var\(--safe-area-bottom\)\)/su,
    );
    expect(css("components/composer/Composer.css")).toMatch(
      /\.evener-composer__dock\s*\{[^}]*(keyboard-inset|safe-area-bottom)/su,
    );
    expect(css("components/voice/VoiceControls.module.css")).toMatch(
      /padding-bottom:\s*calc\([^}]*(safe-area-bottom)/su,
    );
  });
});
