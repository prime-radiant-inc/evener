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
import { stillwaterConversationSkin } from "./ConversationSkin";

function bounded(text: string): BoundedDisplayText {
  return {
    text,
    truncated: false,
    originalUtf8Bytes: new TextEncoder().encode(text).length,
  };
}

const chromeProps: ChromeRenderProps = {
  title: bounded("Fix auth flow"),
  project: bounded("evener/mobile"),
  status: bounded("Running"),
  updatedLabel: bounded("2 minutes ago"),
};

const userProps: NarrativeItemRenderProps = {
  item: {
    key: "user-key",
    sourceKind: "user",
    body: bounded("User message"),
    label: null,
    tone: "idle",
    streaming: false,
    questionKey: null,
    evidenceKey: null,
    sequence: "1",
  },
  body: <p>User message</p>,
  focused: false,
};

const assistantProps: NarrativeItemRenderProps = {
  item: {
    key: "assistant-key",
    sourceKind: "assistant",
    body: bounded("[Skin must not render this](https://example.com)"),
    label: null,
    tone: "running",
    streaming: true,
    questionKey: null,
    evidenceKey: null,
    sequence: "2",
  },
  body: (
    <p data-frame-owned-assistant-body="true">Frame-owned assistant body</p>
  ),
  focused: true,
};

const markerProps: ActivityMarkerRenderProps = {
  item: {
    key: "tool-key",
    sourceKind: "tool",
    semanticKind: "tool",
    label: bounded("read_file <img src=x>"),
    preview: bounded("Read 240 lines"),
    duration: bounded("12 ms"),
    tone: "success",
    state: "completed",
    evidenceKey: null,
    sequence: "3",
  },
  focused: false,
};

const stillwaterSource = readFileSync(
  path.join(__dirname, "ConversationSkin.tsx"),
  "utf8",
);

const stillwaterCss = readFileSync(
  path.join(__dirname, "stillwater.css"),
  "utf8",
).replace(/\/\*[\s\S]*?\*\//g, "");

afterEach(cleanup);

describe("stillwaterConversationSkin", () => {
  it("implements only the frozen visual skin contract", () => {
    expect(stillwaterConversationSkin).toMatchObject({
      id: "stillwater",
      className: "concept-stillwater sw-conversation-skin",
      composerAppearance: { density: "comfortable", accent: "forest" },
    });
    expect(stillwaterSource).not.toMatch(
      /items\.map|useVirtualizer|<textarea|overflowY/,
    );

    const skinOnlyRenders = [
      render(stillwaterConversationSkin.renderConversationChrome(chromeProps)),
      render(stillwaterConversationSkin.renderNarrativeItem(userProps)),
      render(stillwaterConversationSkin.renderNarrativeItem(assistantProps)),
      render(stillwaterConversationSkin.renderActivityMarker(markerProps)),
    ];
    for (const skinOnly of skinOnlyRenders) {
      expect(
        skinOnly.container.querySelectorAll(
          "button,a[href],input,textarea,select",
        ),
      ).toHaveLength(0);
      skinOnly.unmount();
    }
  });

  it("keeps title/status-first chrome and restrained Stillwater row treatments", () => {
    const chrome = render(
      stillwaterConversationSkin.renderConversationChrome(chromeProps),
    );
    expect(
      chrome.container.querySelector(".sw-conversation-chrome"),
    ).not.toBeNull();
    expect(
      chrome.getByRole("heading", { name: "Fix auth flow" }),
    ).toBeVisible();
    expect(chrome.getByText("Running")).toHaveClass(
      "sw-conversation-chrome__status",
    );

    const user = render(
      stillwaterConversationSkin.renderNarrativeItem(userProps),
    );
    expect(
      user.container.querySelector(".sw-conversation-user"),
    ).not.toBeNull();

    const assistant = render(
      stillwaterConversationSkin.renderNarrativeItem(assistantProps),
    );
    expect(
      assistant.container.querySelector(".sw-conversation-assistant"),
    ).not.toBeNull();
    expect(
      assistant.container.querySelector("[data-frame-owned-assistant-body]"),
    ).toHaveTextContent("Frame-owned assistant body");
    expect(assistant.container.querySelector("a[href]")).toBeNull();
    expect(assistant.container).not.toHaveTextContent(
      "Skin must not render this",
    );

    const marker = render(
      stillwaterConversationSkin.renderActivityMarker(markerProps),
    );
    expect(
      marker.container.querySelector(".sw-conversation-marker"),
    ).not.toBeNull();
    expect(marker.container.querySelector("img")).toBeNull();
    expect(marker.container).toHaveTextContent("read_file <img src=x>");
    for (const decoration of marker.container.querySelectorAll(
      "[data-stillwater-decoration]",
    )) {
      expect(decoration).toHaveAttribute("aria-hidden", "true");
    }
  });

  it("keeps forest tokens and removes legacy conversation geometry", () => {
    expect(stillwaterCss).toContain("--sw-accent: #18775f");
    expect(stillwaterCss).toContain(".sw-conversation-surface");
    expect(stillwaterCss).toContain(".sw-conversation-assistant");
    expect(stillwaterCss).toContain(".sw-conversation-user");
    expect(stillwaterCss).toContain(".sw-conversation-marker");
    expect(stillwaterCss).not.toContain("--visual-viewport-height");
    expect(stillwaterCss).not.toMatch(/\.sw-transcript(?:[\s.{:#>])/);
    expect(stillwaterCss).not.toMatch(/\.sw-composer(?:[\s.{:#>])/);
    expect(stillwaterCss).not.toMatch(/font-size\s*:[^;]*(?:vw|vh|vmin|vmax)/);
    expect(stillwaterSource).not.toMatch(
      /AssistantMessage|renderSafeMarkdown|onExternalLink/,
    );
  });
});
