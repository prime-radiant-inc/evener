import { readFileSync } from "node:fs";
import path from "node:path";
import {
  act,
  cleanup,
  fireEvent,
  render,
  waitFor,
} from "@testing-library/react";
import { createRef } from "react";
import { afterEach, describe, expect, it } from "vitest";
import type {
  ActivityMarkerRenderProps,
  ChromeRenderProps,
  ConversationSkin,
  NarrativeItemRenderProps,
} from "../conversation/contract";
import {
  VirtualTranscript,
  type VirtualTranscriptHandle,
} from "../conversation/VirtualTranscript";
import type { BoundedDisplayText, ConversationDisplayItem } from "../model";
import { stillwaterConversationSkin } from "../stillwater/ConversationSkin";
import { fieldNotesConversationSkin } from "./ConversationSkin";
import { fieldNotesModule } from "./index";

function bounded(text: string): BoundedDisplayText {
  return {
    text,
    truncated: false,
    originalUtf8Bytes: new TextEncoder().encode(text).length,
  };
}

const chromeProps: ChromeRenderProps = {
  title: bounded("Compile typed modules"),
  project: bounded("evener-core"),
  status: bounded("Running"),
  updatedLabel: bounded("12 minutes ago"),
};

const narrativeProps: NarrativeItemRenderProps = {
  item: {
    key: "assistant-key",
    sourceKind: "assistant",
    body: bounded("Skin text must not replace frame body"),
    label: bounded("Assistant"),
    tone: "running",
    streaming: true,
    questionKey: null,
    evidenceKey: null,
    sequence: "seq-A2",
  },
  body: <p data-frame-owned-body="true">Frame-owned editorial narrative</p>,
  focused: true,
};

const markerProps: ActivityMarkerRenderProps = {
  item: {
    key: "tool-key",
    sourceKind: "tool",
    semanticKind: "tool",
    label: bounded("read_file <renderer>"),
    preview: bounded("Read 24 lines from renderer."),
    duration: bounded("18 ms"),
    tone: "running",
    state: "running",
    evidenceKey: null,
    sequence: "seq-A3",
  },
  focused: false,
};

const fieldNotesSource = readFileSync(
  path.join(__dirname, "ConversationSkin.tsx"),
  "utf8",
);
const fieldNotesCss = readFileSync(
  path.join(__dirname, "field-notes.css"),
  "utf8",
).replace(/\/\*[\s\S]*?\*\//g, "");

function ruleBody(selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = fieldNotesCss.match(new RegExp(`${escaped}\\s*\\{([^}]+)\\}`));
  expect(match, `missing CSS rule ${selector}`).not.toBeNull();
  return match?.[1] ?? "";
}

function animationDurationMs(body: string): number {
  const match = body.match(
    /animation(?:-duration)?\s*:[^;]*?(\d+(?:\.\d+)?)(ms|s)/,
  );
  expect(match, `missing animation duration in ${body}`).not.toBeNull();
  const value = Number(match?.[1] ?? Number.NaN);
  return match?.[2] === "s" ? value * 1_000 : value;
}

const ANCHOR_VIEWPORT_HEIGHT = 180;
const anchorRowHeights = new Map<string, number>();
const measuredAnchorRows = new Set<string>();

class AnchorResizeObserver implements ResizeObserver {
  static readonly instances = new Set<AnchorResizeObserver>();
  readonly targets = new Set<Element>();

  constructor(private readonly callback: ResizeObserverCallback) {
    AnchorResizeObserver.instances.add(this);
  }

  observe(target: Element): void {
    this.targets.add(target);
  }

  unobserve(target: Element): void {
    this.targets.delete(target);
  }

  disconnect(): void {
    this.targets.clear();
    AnchorResizeObserver.instances.delete(this);
  }

  static emit(): void {
    for (const observer of AnchorResizeObserver.instances) {
      const entries = [...observer.targets].map((target) => {
        const rect = target.getBoundingClientRect();
        const key = (target as HTMLElement).dataset.itemKey;
        if (key !== undefined) measuredAnchorRows.add(key);
        return {
          target,
          contentRect: rect,
          borderBoxSize: [{ inlineSize: rect.width, blockSize: rect.height }],
          contentBoxSize: [{ inlineSize: rect.width, blockSize: rect.height }],
          devicePixelContentBoxSize: [],
        } as unknown as ResizeObserverEntry;
      });
      if (entries.length > 0) observer.callback(entries, observer);
    }
  }
}

function installAnchorGeometry(): () => void {
  const originalResizeObserver = globalThis.ResizeObserver;
  const originalRect = HTMLElement.prototype.getBoundingClientRect;
  const originalScrollTo = HTMLElement.prototype.scrollTo;
  const descriptors = new Map(
    ["offsetHeight", "offsetWidth", "clientHeight", "scrollHeight"].map(
      (property) => [
        property,
        Object.getOwnPropertyDescriptor(HTMLElement.prototype, property),
      ],
    ),
  );
  globalThis.ResizeObserver = AnchorResizeObserver;
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get() {
      return this.hasAttribute("data-virtual-list-scroll")
        ? ANCHOR_VIEWPORT_HEIGHT
        : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "offsetWidth", {
    configurable: true,
    get: () => 390,
  });
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get() {
      return this.hasAttribute("data-virtual-list-scroll")
        ? ANCHOR_VIEWPORT_HEIGHT
        : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get() {
      if (!this.hasAttribute("data-virtual-list-scroll")) return 0;
      return Number.parseFloat(
        (this.firstElementChild as HTMLElement | null)?.style.height ?? "0",
      );
    },
  });
  HTMLElement.prototype.getBoundingClientRect = function () {
    const key = this.dataset.itemKey;
    const height =
      key !== undefined
        ? (anchorRowHeights.get(key) ?? 96)
        : this.hasAttribute("data-virtual-list-scroll")
          ? ANCHOR_VIEWPORT_HEIGHT
          : 0;
    return {
      x: 0,
      y: 0,
      top: 0,
      left: 0,
      right: 390,
      bottom: height,
      width: 390,
      height,
      toJSON: () => ({}),
    };
  };
  HTMLElement.prototype.scrollTo = function (
    optionsOrX?: ScrollToOptions | number,
    y?: number,
  ): void {
    this.scrollTop =
      typeof optionsOrX === "number"
        ? (y ?? 0)
        : (optionsOrX?.top ?? this.scrollTop);
    this.dispatchEvent(new Event("scroll"));
  };
  return () => {
    AnchorResizeObserver.instances.clear();
    globalThis.ResizeObserver = originalResizeObserver;
    HTMLElement.prototype.getBoundingClientRect = originalRect;
    HTMLElement.prototype.scrollTo = originalScrollTo;
    for (const [property, descriptor] of descriptors) {
      if (descriptor === undefined) {
        Reflect.deleteProperty(HTMLElement.prototype, property);
      } else {
        Object.defineProperty(HTMLElement.prototype, property, descriptor);
      }
    }
  };
}

afterEach(cleanup);

describe("fieldNotesConversationSkin", () => {
  it("implements only the frozen decoration-only skin contract", () => {
    const skin = fieldNotesConversationSkin;

    expect(skin).toMatchObject({
      id: "field-notes",
      className: "concept-field-notes fn-conversation-skin",
      composerAppearance: { density: "comfortable", accent: "rust" },
    });
    expect(Object.keys(skin).sort()).toEqual([
      "className",
      "composerAppearance",
      "id",
      "renderActivityMarker",
      "renderConversationChrome",
      "renderNarrativeItem",
    ]);
    expect(fieldNotesModule.conversationSkin).toBe(skin);
    expect(fieldNotesSource).not.toMatch(
      /items\.map|useVirtualizer|<textarea|<button|<input|<select|onClick|overflowY/,
    );

    const isolatedRenders = [
      render(skin.renderConversationChrome(chromeProps)),
      render(skin.renderNarrativeItem(narrativeProps)),
      render(skin.renderActivityMarker(markerProps)),
    ];
    for (const isolated of isolatedRenders) {
      expect(
        isolated.container.querySelectorAll(
          "button,a[href],input,textarea,select",
        ),
      ).toHaveLength(0);
      isolated.unmount();
    }
  });

  it("keeps chronology inside each presentation row and out of AX names", () => {
    const skin = fieldNotesConversationSkin;

    const narrative = render(
      <article aria-label="Assistant message; streaming">
        {skin.renderNarrativeItem(narrativeProps)}
      </article>,
    );
    const narrativeSegment = narrative.container.querySelector(
      "[data-chronology-segment='row-local']",
    );
    expect(narrativeSegment).not.toBeNull();
    expect(narrativeSegment).toHaveAttribute("aria-hidden", "true");
    expect(narrativeSegment).toHaveTextContent("seq-A2");
    expect(narrative.container).not.toHaveTextContent(
      "Skin text must not replace frame body",
    );
    expect(
      narrative.container.querySelector("[data-frame-owned-body='true']"),
    ).toHaveTextContent("Frame-owned editorial narrative");
    const margin = narrative.container.querySelector(
      "[data-margin-label='assistant']",
    );
    expect(margin).toHaveTextContent("Assistant · response");
    expect(margin).toHaveAttribute("aria-hidden", "true");
    expect(
      narrative.getByRole("article", {
        name: "Assistant message; streaming",
      }),
    ).not.toHaveAccessibleName(/seq-A2|Assistant · response/);

    const marker = render(skin.renderActivityMarker(markerProps));
    const markerSegment = marker.container.querySelector(
      "[data-chronology-segment='row-local']",
    );
    expect(markerSegment).not.toBeNull();
    expect(markerSegment).toHaveAttribute("aria-hidden", "true");
    expect(markerSegment).toHaveTextContent("seq-A3");
    expect(
      marker.container.querySelector("[data-chronology-rail='document']"),
    ).toBeNull();
    expect(marker.container).toHaveTextContent("read_file <renderer>");
    expect(marker.container.querySelector("renderer")).toBeNull();

    for (const decoration of document.querySelectorAll(
      "[data-field-notes-decoration]",
    )) {
      expect(decoration).toHaveAttribute("aria-hidden", "true");
    }
    expect(
      document.querySelectorAll("[data-field-notes-decoration]"),
    ).not.toHaveLength(0);
  });

  it("preserves warm paper, editorial contrast, margin labels, and local ruled motifs", () => {
    const chrome = render(
      fieldNotesConversationSkin.renderConversationChrome(chromeProps),
    );
    expect(
      chrome.getByRole("heading", { name: "Compile typed modules" }),
    ).toHaveClass("fn-editorial");
    expect(chrome.getByText("evener-core")).toHaveClass(
      "fn-conversation-chrome__project",
    );
    expect(chrome.getByText("12 minutes ago")).toBeVisible();

    const narrative = render(
      fieldNotesConversationSkin.renderNarrativeItem(narrativeProps),
    );
    expect(
      narrative.container.querySelector("[data-editorial-reading='true']"),
    ).not.toBeNull();
    expect(
      narrative.container.querySelector(
        "[data-field-notes-decoration='ruled-motif']",
      ),
    ).toHaveAttribute("aria-hidden", "true");

    expect(fieldNotesCss).toContain("--fn-paper: #fffaf1");
    expect(fieldNotesCss).toContain("--fn-paper-ink: #312d28");
    expect(fieldNotesCss).toContain("--fn-editorial-contrast: #3a3028");
    const chronology = ruleBody(
      ".concept-field-notes.fn-conversation-skin .fn-conversation-chronology",
    );
    expect(chronology).toMatch(/position\s*:\s*absolute/);
    expect(chronology).toMatch(/pointer-events\s*:\s*none/);
    expect(fieldNotesCss).not.toMatch(
      /--visual-viewport-height|--keyboard-inset-height|data-chronology-rail/,
    );
  });

  it("preserves a measured editorial anchor through prepend and skin switches", async () => {
    const restoreGeometry = installAnchorGeometry();
    anchorRowHeights.clear();
    measuredAnchorRows.clear();
    const shortItem: ConversationDisplayItem = {
      ...narrativeProps.item,
      key: "field-short",
      sourceKind: "user",
      body: bounded("Short note"),
      sequence: "seq-short",
    };
    const editorialItem: ConversationDisplayItem = {
      ...narrativeProps.item,
      key: "field-editorial",
      body: bounded("Long editorial response. ".repeat(24)),
      sequence: "seq-editorial",
    };
    const activityItem: ConversationDisplayItem = {
      ...markerProps.item,
      key: "field-activity",
      sequence: "seq-activity",
    };
    const olderItem: ConversationDisplayItem = {
      ...shortItem,
      key: "field-older",
      body: bounded("Prepended note"),
      sequence: "seq-older",
    };
    const items = [shortItem, editorialItem, activityItem] as const;
    anchorRowHeights.set("field-short", 72);
    anchorRowHeights.set("field-editorial", 224);
    anchorRowHeights.set("field-activity", 88);
    anchorRowHeights.set("field-older", 64);
    const transcriptRef = createRef<VirtualTranscriptHandle>();
    const noOp = () => undefined;
    const transcript = (
      skin: ConversationSkin,
      nextItems: readonly ConversationDisplayItem[],
    ) => (
      <VirtualTranscript
        ref={transcriptRef}
        threadKey="field-anchor-thread"
        skinId={skin.id}
        contentSize="large"
        items={nextItems}
        skin={skin}
        savedAnchor={null}
        unseen={0}
        onAnchorChange={noOp}
        onUnseenChange={noOp}
        onFocusIntentChange={noOp}
      />
    );

    try {
      const view = render(transcript(fieldNotesConversationSkin, items));
      await act(async () => {
        AnchorResizeObserver.emit();
        await Promise.resolve();
        AnchorResizeObserver.emit();
      });
      for (const key of ["field-short", "field-editorial", "field-activity"]) {
        expect(measuredAnchorRows).toContain(key);
      }
      const feed = view.getByRole("feed", {
        name: "Conversation transcript",
      });
      feed.scrollTop = 80;
      fireEvent.scroll(feed);
      const before = transcriptRef.current?.captureAnchor();
      expect(before?.itemKey).toBe("field-editorial");
      expect(before).not.toBeNull();
      if (before === null || before === undefined) return;

      view.rerender(
        transcript(fieldNotesConversationSkin, [olderItem, ...items]),
      );
      await act(async () => {
        AnchorResizeObserver.emit();
        await Promise.resolve();
        AnchorResizeObserver.emit();
      });
      await waitFor(() => {
        const afterPrepend = transcriptRef.current?.captureAnchor();
        expect(afterPrepend?.itemKey).toBe(before.itemKey);
        expect(
          Math.abs(
            (afterPrepend?.offsetPx ?? Number.POSITIVE_INFINITY) -
              before.offsetPx,
          ),
        ).toBeLessThanOrEqual(2);
      });

      view.rerender(
        transcript(stillwaterConversationSkin, [olderItem, ...items]),
      );
      await act(async () => {
        AnchorResizeObserver.emit();
        await Promise.resolve();
        AnchorResizeObserver.emit();
      });
      view.rerender(
        transcript(fieldNotesConversationSkin, [olderItem, ...items]),
      );
      await act(async () => {
        AnchorResizeObserver.emit();
        await Promise.resolve();
        AnchorResizeObserver.emit();
      });
      await waitFor(() => {
        const afterSwitch = transcriptRef.current?.captureAnchor();
        expect(afterSwitch?.itemKey).toBe(before.itemKey);
        expect(
          Math.abs(
            (afterSwitch?.offsetPx ?? Number.POSITIVE_INFINITY) -
              before.offsetPx,
          ),
        ).toBeLessThanOrEqual(2);
      });
      for (const row of view.getAllByTestId("virtual-transcript-row")) {
        expect(
          row.querySelector("[data-chronology-segment='row-local']"),
        ).not.toBeNull();
      }
    } finally {
      cleanup();
      restoreGeometry();
    }
  });

  it("bounds route and concept motion and disables root motion through both paths", () => {
    const route = ruleBody(".concept-field-notes .fn-route-enter");
    expect(animationDurationMs(route)).toBeLessThanOrEqual(180);
    const routeKeyframes = fieldNotesCss.match(
      /@keyframes fn-route-in\s*\{([\s\S]*?)\n\}/,
    )?.[1];
    expect(routeKeyframes).toBeDefined();
    const translation = routeKeyframes?.match(
      /translateY\((-?\d+(?:\.\d+)?)(px|rem)\)/,
    );
    expect(translation).not.toBeNull();
    const translationPx =
      Number(translation?.[1] ?? Number.NaN) *
      (translation?.[2] === "rem" ? 16 : 1);
    expect(Math.abs(translationPx)).toBeLessThanOrEqual(16);

    const crossfade = ruleBody(
      '.concept-field-notes.fn-conversation-skin[data-surface="conversation"]',
    );
    expect(animationDurationMs(crossfade)).toBeLessThanOrEqual(120);
    const crossfadeKeyframes = fieldNotesCss.match(
      /@keyframes fn-concept-crossfade\s*\{([\s\S]*?)\n\}/,
    )?.[1];
    expect(crossfadeKeyframes).toBeDefined();
    expect(crossfadeKeyframes).not.toMatch(
      /translate|left|right|margin-inline/,
    );

    const style = document.createElement("style");
    style.textContent = fieldNotesCss;
    document.head.append(style);
    const reduced = document.createElement("main");
    reduced.className = "concept-field-notes fn-conversation-skin";
    reduced.dataset.surface = "conversation";
    reduced.dataset.reducedMotion = "true";
    document.body.append(reduced);
    expect(getComputedStyle(reduced).animationDuration).toBe("0s");

    const media = fieldNotesCss.match(
      /@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}\n\n@media \(prefers-reduced-transparency: reduce\)/,
    )?.[1];
    expect(media).toContain(
      ':root .concept-field-notes.fn-conversation-skin[data-surface="conversation"]',
    );
    expect(media).toMatch(/animation\s*:\s*none/);
    reduced.remove();
    style.remove();
  });
});
