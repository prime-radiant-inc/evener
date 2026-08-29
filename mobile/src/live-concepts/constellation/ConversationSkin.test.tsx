import { readFileSync } from "node:fs";
import path from "node:path";
import { cleanup, render } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type {
  ActivityMarkerRenderProps,
  ChromeRenderProps,
  NarrativeItemRenderProps,
} from "../conversation/contract";
import type { BoundedDisplayText } from "../model";
import { constellationConversationSkin } from "./ConversationSkin";

function bounded(text: string): BoundedDisplayText {
  return {
    text,
    truncated: false,
    originalUtf8Bytes: new TextEncoder().encode(text).length,
  };
}

const chromeProps: ChromeRenderProps = {
  title: bounded("Trace the live graph"),
  project: bounded("evener/mobile"),
  status: bounded("Running"),
  updatedLabel: bounded("just now"),
};

const narrativeProps: NarrativeItemRenderProps = {
  item: {
    key: "assistant-key",
    sourceKind: "assistant",
    body: bounded("Skin-owned text must not replace frame body"),
    label: null,
    tone: "running",
    streaming: true,
    questionKey: null,
    evidenceKey: null,
    sequence: "2",
  },
  body: <p data-frame-owned-body="true">Frame-owned narrative</p>,
  focused: true,
};

const markerProps: ActivityMarkerRenderProps = {
  item: {
    key: "tool-key",
    sourceKind: "tool",
    semanticKind: "tool",
    label: bounded("exec_command <script>"),
    preview: bounded("Completed three checks"),
    duration: bounded("18 ms"),
    tone: "success",
    state: "completed",
    evidenceKey: null,
    sequence: "3",
  },
  focused: false,
};

const constellationSource = readFileSync(
  path.join(__dirname, "ConversationSkin.tsx"),
  "utf8",
);
const constellationCss = readFileSync(
  path.join(__dirname, "constellation.css"),
  "utf8",
).replace(/\/\*[\s\S]*?\*\//g, "");

function ruleBody(selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = constellationCss.match(
    new RegExp(`${escaped}\\s*\\{([^}]+)\\}`),
  );
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

function groupedRuleBody(source: string, selector: string): string {
  const normalizedSource = source.replace(/\s+/g, " ");
  const selectorStart = normalizedSource.indexOf(selector);
  expect(
    selectorStart,
    `missing CSS selector ${selector}`,
  ).toBeGreaterThanOrEqual(0);
  const bodyStart = normalizedSource.indexOf("{", selectorStart);
  expect(bodyStart, `missing CSS body for ${selector}`).toBeGreaterThan(
    selectorStart,
  );
  const bodyEnd = normalizedSource.indexOf("}", bodyStart);
  expect(bodyEnd, `unterminated CSS body for ${selector}`).toBeGreaterThan(
    bodyStart,
  );
  return normalizedSource.slice(bodyStart + 1, bodyEnd);
}

afterEach(cleanup);

describe("constellationConversationSkin", () => {
  it("implements only the frozen decoration-only skin contract", () => {
    expect(constellationConversationSkin).toMatchObject({
      id: "constellation",
      className: "concept-constellation co-conversation-skin",
      composerAppearance: { density: "compact", accent: "luminous" },
    });
    expect(Object.keys(constellationConversationSkin).sort()).toEqual([
      "className",
      "composerAppearance",
      "id",
      "renderActivityMarker",
      "renderConversationChrome",
      "renderNarrativeItem",
    ]);
    expect(constellationSource).not.toMatch(
      /items\.map|useVirtualizer|<textarea|<button|<input|<select|onClick|overflowY/,
    );

    const isolatedRenders = [
      render(
        constellationConversationSkin.renderConversationChrome(chromeProps),
      ),
      render(constellationConversationSkin.renderNarrativeItem(narrativeProps)),
      render(constellationConversationSkin.renderActivityMarker(markerProps)),
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

  it("keeps dark technical identity, luminous telemetry, and inert local motifs", () => {
    const chrome = render(
      constellationConversationSkin.renderConversationChrome(chromeProps),
    );
    expect(
      chrome.container.querySelector(".co-conversation-chrome"),
    ).not.toBeNull();
    expect(
      chrome.getByRole("heading", { name: "Trace the live graph" }),
    ).toBeVisible();
    expect(chrome.getByText("Running")).toHaveClass(
      "co-conversation-telemetry__status",
    );

    const narrative = render(
      constellationConversationSkin.renderNarrativeItem(narrativeProps),
    );
    expect(
      narrative.container.querySelector(".co-conversation-assistant"),
    ).not.toBeNull();
    expect(
      narrative.container.querySelector("[data-frame-owned-body='true']"),
    ).toHaveTextContent("Frame-owned narrative");
    expect(narrative.container).not.toHaveTextContent(
      "Skin-owned text must not replace frame body",
    );

    const marker = render(
      constellationConversationSkin.renderActivityMarker(markerProps),
    );
    expect(
      marker.container.querySelector(".co-conversation-marker"),
    ).not.toBeNull();
    expect(marker.container).toHaveTextContent("exec_command <script>");
    expect(marker.container.querySelector("script")).toBeNull();

    for (const decoration of document.querySelectorAll(
      "[data-constellation-decoration]",
    )) {
      expect(decoration).toHaveAttribute("aria-hidden", "true");
    }
    expect(
      document.querySelectorAll("[data-constellation-decoration]"),
    ).not.toHaveLength(0);

    expect(constellationCss).toContain("--co-bg: #090d18");
    expect(constellationCss).toContain("--co-accent: #7fe6c4");
    expect(constellationCss).toContain(".co-conversation-telemetry");
    for (const selector of [
      ".concept-constellation.co-conversation-skin .co-conversation-star-field",
      ".concept-constellation.co-conversation-skin .co-conversation-connection",
    ]) {
      const body = ruleBody(selector);
      expect(body).toMatch(/position\s*:\s*absolute/);
      expect(body).toMatch(/pointer-events\s*:\s*none/);
    }
  });

  it("bounds route and concept motion and computes zero reduced-motion duration", () => {
    const route = ruleBody(".concept-constellation .co-route-enter");
    expect(animationDurationMs(route)).toBeLessThanOrEqual(180);
    const routeKeyframes = constellationCss.match(
      /@keyframes co-route-in\s*\{([\s\S]*?)\n\}/,
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
      '.concept-constellation.co-conversation-skin[data-surface="conversation"]',
    );
    expect(animationDurationMs(crossfade)).toBeLessThanOrEqual(120);
    const crossfadeKeyframes = constellationCss.match(
      /@keyframes co-concept-crossfade\s*\{([\s\S]*?)\n\}/,
    )?.[1];
    expect(crossfadeKeyframes).toBeDefined();
    expect(crossfadeKeyframes).not.toMatch(
      /translate|left|right|margin-inline/,
    );

    const style = document.createElement("style");
    style.textContent = constellationCss;
    document.head.append(style);
    const reduced = document.createElement("main");
    reduced.className = "concept-constellation co-conversation-skin";
    reduced.dataset.surface = "conversation";
    reduced.dataset.reducedMotion = "true";
    document.body.append(reduced);
    expect(getComputedStyle(reduced).animationDuration).toBe("0s");

    const reducedMotionMedia = constellationCss.match(
      /@media \(prefers-reduced-motion: reduce\) \{([\s\S]*?)\n\}\n\n@media \(forced-colors: active\)/,
    )?.[1];
    expect(reducedMotionMedia).toBeDefined();
    const mediaRootCrossfade =
      ':root .concept-constellation.co-conversation-skin[data-surface="conversation"]';
    expect(
      groupedRuleBody(reducedMotionMedia ?? "", mediaRootCrossfade),
    ).toMatch(/animation\s*:\s*none/);
    reduced.remove();
    style.remove();
  });
});
