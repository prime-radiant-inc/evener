import { readFileSync } from "node:fs";
import path from "node:path";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { createViewportCoordinator } from "../../ui/platformPresentation";
import { __resetSheetHistory, __sheetHistorySettled } from "../../ui/Sheet";
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

function frameProps(
  composerOverrides: Partial<ConversationFrameProps["state"]["composer"]> = {},
): ConversationFrameProps {
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
        ...composerOverrides,
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
    onExternalLink: async () => undefined,
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

afterEach(async () => {
  cleanup();
  await __sheetHistorySettled();
  __resetSheetHistory();
  history.replaceState(null, "");
  vi.restoreAllMocks();
  document.documentElement.removeAttribute("style");
});

describe("ConversationFrame platform integration", () => {
  it("requires genuine measurement before evidence opens and reflects the sole-scroller inert lock", async () => {
    class PlatformResizeObserver implements ResizeObserver {
      static readonly instances = new Set<PlatformResizeObserver>();
      readonly targets = new Set<Element>();
      constructor(private readonly callback: ResizeObserverCallback) {
        PlatformResizeObserver.instances.add(this);
      }
      observe(target: Element): void {
        this.targets.add(target);
      }
      unobserve(target: Element): void {
        this.targets.delete(target);
      }
      disconnect(): void {
        this.targets.clear();
        PlatformResizeObserver.instances.delete(this);
      }
      static emitAll(): void {
        for (const observer of PlatformResizeObserver.instances) {
          const entries = [...observer.targets].map((target) => {
            const rect = target.getBoundingClientRect();
            return {
              target,
              contentRect: rect,
              borderBoxSize: [
                { inlineSize: rect.width, blockSize: rect.height },
              ],
              contentBoxSize: [
                { inlineSize: rect.width, blockSize: rect.height },
              ],
              devicePixelContentBoxSize: [
                { inlineSize: rect.width, blockSize: rect.height },
              ],
            } as unknown as ResizeObserverEntry;
          });
          if (entries.length > 0) observer.callback(entries, observer);
        }
      }
    }

    const originalResizeObserver = globalThis.ResizeObserver;
    const originalRect = HTMLElement.prototype.getBoundingClientRect;
    const originalFocus = HTMLElement.prototype.focus;
    const originalInert = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      "inert",
    );
    const originalOffsetHeight = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      "offsetHeight",
    );
    const originalClientHeight = Object.getOwnPropertyDescriptor(
      HTMLElement.prototype,
      "clientHeight",
    );
    globalThis.ResizeObserver = PlatformResizeObserver;
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
      configurable: true,
      get() {
        return this.hasAttribute("data-virtual-list-scroll") ? 360 : 0;
      },
    });
    Object.defineProperty(HTMLElement.prototype, "clientHeight", {
      configurable: true,
      get() {
        return this.hasAttribute("data-virtual-list-scroll") ? 360 : 0;
      },
    });
    Object.defineProperty(HTMLElement.prototype, "inert", {
      configurable: true,
      get() {
        return this.hasAttribute("inert");
      },
      set(value: boolean) {
        this.toggleAttribute("inert", value);
      },
    });
    HTMLElement.prototype.getBoundingClientRect = function () {
      const height = this.matches("[data-virtual-row]")
        ? 96
        : this.hasAttribute("data-virtual-list-scroll")
          ? 360
          : 0;
      return elementRect(0, height);
    };
    HTMLElement.prototype.focus = function (options?: FocusOptions): void {
      if (this.closest("[inert]") !== null) return;
      originalFocus.call(this, options);
    };

    let mountedView: ReturnType<typeof render> | null = null;
    try {
      const base = frameProps();
      const conversation = base.state.conversation;
      if (conversation === null)
        throw new Error("conversation fixture missing");
      const dispatch = vi.fn<ConversationFrameProps["dispatch"]>();
      const onAnchorChange = vi.fn<ConversationFrameProps["onAnchorChange"]>();
      const state = {
        ...base.state,
        conversation: {
          ...conversation,
          items: [
            {
              key: "platform-evidence-item",
              sourceKind: "failure" as const,
              body: bounded("Bounded platform failure"),
              label: bounded("Failure"),
              tone: "failed" as const,
              streaming: false,
              questionKey: null,
              evidenceKey: "platform-evidence",
              sequence: "platform-1",
            },
          ],
          evidence: [
            {
              key: "platform-evidence",
              family: "failure" as const,
              title: bounded("Platform evidence"),
              sections: [
                {
                  heading: bounded("Detail"),
                  body: bounded("Platform bounded detail"),
                },
              ],
              redacted: true,
            },
          ],
        },
      };
      const view = render(
        <ConversationFrame
          {...base}
          state={state}
          dispatch={dispatch}
          onAnchorChange={onAnchorChange}
        />,
      );
      mountedView = view;
      const trigger = await screen.findByRole("button", {
        name: "Show evidence",
      });

      fireEvent.click(trigger);
      expect(dispatch).not.toHaveBeenCalledWith(
        expect.objectContaining({ type: "openEvidence" }),
      );
      act(() => PlatformResizeObserver.emitAll());
      const measuredScroller = document.querySelector<HTMLElement>(
        '[data-virtual-list-scroll="true"]',
      );
      if (measuredScroller === null)
        throw new Error("measured scroller missing");
      fireEvent.scroll(measuredScroller);
      await waitFor(() =>
        expect(onAnchorChange).toHaveBeenCalledWith({
          threadKey: "thread",
          itemKey: "platform-evidence-item",
          offsetPx: 0,
          following: true,
        }),
      );
      fireEvent.click(trigger);
      expect(dispatch).toHaveBeenCalledWith({
        type: "openEvidence",
        evidenceKey: "platform-evidence",
        triggerKey: "platform-evidence-item",
      });

      view.rerender(
        <ConversationFrame
          {...base}
          dispatch={dispatch}
          onAnchorChange={onAnchorChange}
          state={{
            ...state,
            openEvidenceKey: "platform-evidence",
            evidenceTriggerKey: "platform-evidence-item",
          }}
        />,
      );
      const main = screen.getByRole("main", { hidden: true });
      const transcript = main.querySelector<HTMLElement>(
        '[data-virtual-list-scroll="true"]',
      );
      if (transcript === null) throw new Error("transcript scroller missing");
      expect(main.inert).toBe(true);
      expect(transcript.inert).toBe(true);
      expect(transcript).toHaveAttribute("aria-hidden", "true");
      expect(transcript).toHaveAttribute("data-scroll-locked", "true");
      expect(transcript).not.toHaveAttribute("data-page-scroll-owner");
      expect(
        document.querySelectorAll('[data-page-scroll-owner="true"]'),
      ).toHaveLength(1);
      expect(
        screen.getByRole("region", { name: "Activity and evidence details" }),
      ).toHaveAttribute("data-page-scroll-owner", "true");
      transcript.focus();
      expect(transcript).not.toHaveFocus();

      fireEvent.click(
        screen.getByRole("button", { name: "Close activity and evidence" }),
      );
      await waitFor(() =>
        expect(dispatch).toHaveBeenCalledWith({ type: "closeEvidence" }),
      );
      view.rerender(
        <ConversationFrame
          {...base}
          dispatch={dispatch}
          onAnchorChange={onAnchorChange}
          state={state}
        />,
      );
      await waitFor(() => expect(main.inert).toBe(false));
      expect(transcript.inert).toBe(false);
      expect(transcript).not.toHaveAttribute("aria-hidden");
      expect(transcript).not.toHaveAttribute("data-scroll-locked");
      expect(transcript).toHaveAttribute("data-page-scroll-owner", "true");
      expect(
        document.querySelectorAll('[data-page-scroll-owner="true"]'),
      ).toHaveLength(1);
      transcript.focus();
      expect(transcript).toHaveFocus();
    } finally {
      mountedView?.unmount();
      PlatformResizeObserver.instances.clear();
      globalThis.ResizeObserver = originalResizeObserver;
      HTMLElement.prototype.getBoundingClientRect = originalRect;
      HTMLElement.prototype.focus = originalFocus;
      if (originalInert === undefined)
        Reflect.deleteProperty(HTMLElement.prototype, "inert");
      else Object.defineProperty(HTMLElement.prototype, "inert", originalInert);
      if (originalOffsetHeight === undefined)
        Reflect.deleteProperty(HTMLElement.prototype, "offsetHeight");
      else
        Object.defineProperty(
          HTMLElement.prototype,
          "offsetHeight",
          originalOffsetHeight,
        );
      if (originalClientHeight === undefined)
        Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
      else
        Object.defineProperty(
          HTMLElement.prototype,
          "clientHeight",
          originalClientHeight,
        );
    }
  });

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
    const user = userEvent.setup();
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
    const view = render(
      <ConversationFrame {...frameProps({ draft: "", canInterrupt: false })} />,
    );

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
    expect(
      screen.getByRole("button", { name: "Submit message" }),
    ).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Interrupt" })).toBeNull();
    composer.getBoundingClientRect = () => elementRect(490, 534);
    composer.focus();
    expect(document.activeElement).toBe(composer);
    const currentComposerViewport = {
      top: viewport.offsetTop,
      bottom: viewport.offsetTop + viewport.height,
    };
    const initialComposerRect = composer.getBoundingClientRect();
    expect(initialComposerRect.bottom).toBeGreaterThan(
      currentComposerViewport.top,
    );
    expect(initialComposerRect.top).toBeLessThan(
      currentComposerViewport.bottom,
    );

    const controls = [
      { name: "Back", element: screen.getByRole("button", { name: "Back" }) },
      { name: "Work", element: screen.getByRole("button", { name: "Work" }) },
      { name: "Voice", element: screen.getByRole("button", { name: "Voice" }) },
      {
        name: "Switch concept",
        element: screen.getByRole("button", { name: "Switch concept" }),
      },
    ];
    const focusOrder: string[] = [];
    const tabKeys: string[] = [];
    frame.addEventListener("keydown", (event) => {
      if (event.key === "Tab") tabKeys.push(event.key);
    });
    const rects = new Map<HTMLElement, { top: number; bottom: number }>();
    const scrollCalls = new Map<HTMLElement, ReturnType<typeof vi.fn>>();
    for (const { name, element } of controls) {
      let rect = { top: 0, bottom: 44 };
      rects.set(element, rect);
      element.getBoundingClientRect = () => {
        const current = rects.get(element);
        if (current === undefined) throw new Error("control rectangle missing");
        return elementRect(current.top, current.bottom);
      };
      const scrollIntoView = vi.fn(() => {
        queueMicrotask(() => {
          rect = { top: viewport.offsetTop, bottom: viewport.offsetTop + 44 };
          rects.set(element, rect);
          viewport.dispatchEvent(new Event("scroll"));
        });
      });
      scrollCalls.set(element, scrollIntoView);
      element.scrollIntoView = scrollIntoView;
      element.addEventListener("focus", () => focusOrder.push(name));
    }
    for (const [index, { name, element }] of controls.entries()) {
      await user.tab();
      expect(document.activeElement, `${name} active after Tab`).toBe(element);
      await waitFor(() => {
        const current = {
          top: viewport.offsetTop,
          bottom: viewport.offsetTop + viewport.height,
        };
        const settled = element.getBoundingClientRect();
        expect(document.activeElement).toBe(element);
        expect(settled.bottom).toBeGreaterThan(current.top);
        expect(settled.top).toBeLessThan(current.bottom);
      });
      expect(scrollCalls.get(element)).toHaveBeenCalledWith({
        block: "nearest",
        inline: "nearest",
      });
      expect(tabKeys).toHaveLength(index + 1);
    }
    expect(focusOrder).toEqual(["Back", "Work", "Voice", "Switch concept"]);
    coordinator.stop();
  });

  it.each([
    {
      name: "Submit",
      composer: { draft: "ready", canInterrupt: false },
      target: "Submit message",
    },
    {
      name: "Interrupt",
      composer: { draft: "", canInterrupt: true },
      target: "Interrupt",
    },
  ])(
    "leaves native textarea traversal active when $name is enabled",
    async ({ composer: composerOverrides, target }) => {
      const user = userEvent.setup();
      render(<ConversationFrame {...frameProps(composerOverrides)} />);
      const message = screen.getByRole("textbox", { name: "Message" });
      const expected = screen.getByRole("button", { name: target });

      message.focus();
      await user.tab();

      expect(document.activeElement).toBe(expected);
      expect(screen.getByRole("button", { name: "Back" })).not.toHaveFocus();
    },
  );

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
