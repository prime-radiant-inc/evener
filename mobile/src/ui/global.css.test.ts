import { readFileSync } from "node:fs";
import path from "node:path";
import { cleanup, render } from "@testing-library/react";
import { createElement } from "react";
import { afterEach, describe, expect, it } from "vitest";
import { createShellServices } from "../screens/fixture-services";
import { RootShell } from "../screens/RootShell";
import { createConnectionStore } from "../state/connection";
import { createNavigationStore } from "../state/navigation";
import { createPreferencesStore } from "../state/preferences";

afterEach(() => cleanup());

/* ---------------------------------------------------------------------------
 * Foundation 7C lane D — parser-based contract tests.
 *
 * These tests do NOT use loose substring/regex matching. They parse the
 * production CSS through the browser CSSOM (`<style>` + `sheet.cssRules`,
 * recursing into grouping rules like `@media`) and the host HTML through
 * `DOMParser`. Every assertion is built on exact selector rules and exact
 * declaration maps, so a coincidental mention in a comment, an unrelated
 * rule, or a grouped selector cannot satisfy an exact-selector check.
 * ------------------------------------------------------------------------- */

const cssPath = path.join(__dirname, "global.css");
const cssSource = readFileSync(cssPath, "utf8");

const htmlPath = path.join(__dirname, "..", "..", "index.html");
const htmlSource = readFileSync(htmlPath, "utf8");

/* -------------------------------------------------------------------------
 * CSSOM parser — collect every exact style rule across the whole sheet,
 * recursing into @media / @supports grouping rules. A later rule with the
 * same selector is recorded separately so duplicate-ownership can be
 * detected and rejected.
 * ----------------------------------------------------------------------- */

interface StyleRuleEntry {
  /** Exact selectorText from CSSOM (grouped selectors stay as one string). */
  selector: string;
  /** Media condition text if this rule lives inside a grouping rule. */
  media: string;
  /** Exact property -> value map (CSSOM-normalized). */
  decls: Record<string, string>;
}

interface KeyframesEntry {
  name: string;
}

interface SheetModel {
  styleRules: StyleRuleEntry[];
  keyframes: KeyframesEntry[];
  /** Raw constructor names encountered, for diagnostics. */
  ruleTypes: string[];
}

function parseSheet(source: string): SheetModel {
  const style = document.createElement("style");
  style.textContent = source;
  document.head.appendChild(style);
  const sheet = style.sheet as CSSStyleSheet;

  const styleRules: StyleRuleEntry[] = [];
  const keyframes: KeyframesEntry[] = [];
  const ruleTypes: string[] = [];

  function declMap(rule: CSSStyleRule): Record<string, string> {
    const map: Record<string, string> = {};
    for (let i = 0; i < rule.style.length; i++) {
      const prop = rule.style[i];
      if (prop === undefined) continue;
      map[prop] = rule.style.getPropertyValue(prop);
    }
    return map;
  }

  function walk(rules: CSSRuleList, media: string): void {
    for (const rule of Array.from(rules)) {
      const ctor = rule.constructor.name;
      ruleTypes.push(ctor);
      if (ctor === "CSSMediaRule" || ctor === "CSSSupportsRule") {
        const group = rule as CSSMediaRule;
        const cond =
          "conditionText" in group
            ? ((group as CSSConditionRule).conditionText ?? "")
            : "";
        walk(group.cssRules, cond);
      } else if (ctor === "CSSStyleRule") {
        const sr = rule as CSSStyleRule;
        styleRules.push({
          selector: sr.selectorText,
          media,
          decls: declMap(sr),
        });
      } else if (ctor === "CSSKeyframesRule") {
        keyframes.push({ name: (rule as CSSKeyframesRule).name });
      }
    }
  }
  walk(sheet.cssRules, "");
  // Remove the injected <style> so it does not leak across tests.
  style.remove();
  return { styleRules, keyframes, ruleTypes };
}

const sheet = parseSheet(cssSource);

function must<T>(value: T | undefined, label: string): T {
  if (value === undefined) throw new Error(`missing ${label}`);
  return value;
}

function selectorMembers(selector: string): string[] {
  const members: string[] = [];
  let start = 0;
  let depth = 0;
  let quote: '"' | "'" | null = null;
  let escaped = false;
  for (let index = 0; index < selector.length; index += 1) {
    const char = selector[index];
    if (escaped) {
      escaped = false;
      continue;
    }
    if (char === "\\") {
      escaped = true;
      continue;
    }
    if (quote !== null) {
      if (char === quote) quote = null;
      continue;
    }
    if (char === '"' || char === "'") {
      quote = char;
    } else if (char === "(") {
      depth += 1;
    } else if (char === ")") {
      depth = Math.max(0, depth - 1);
    } else if (char === "," && depth === 0) {
      members.push(selector.slice(start, index).trim());
      start = index + 1;
    }
  }
  members.push(selector.slice(start).trim());
  return members;
}

/** All top-level (non-media) style rules whose selectorText exactly equals
 *  `selector`. Media-scoped rules are matched via `rulesInMedia` so that a
 *  `:root` inside `@media` cannot satisfy a top-level `:root` ownership check. */
function rulesFor(selector: string): StyleRuleEntry[] {
  return sheet.styleRules.filter(
    (r) => r.selector === selector && r.media === "",
  );
}

/** The single style rule for an exact selector; fails if 0 or >1. */
function oneRule(selector: string): StyleRuleEntry {
  const matches = rulesFor(selector);
  expect(
    matches.length,
    `expected exactly one rule for ${selector}, found ${matches.length}`,
  ).toBe(1);
  return must(matches[0], `rule ${selector}`);
}

/** All top-level rules that apply to this selector, including grouped rules. */
function effectiveRulesFor(selector: string): StyleRuleEntry[] {
  return sheet.styleRules.filter(
    (rule) =>
      rule.media === "" && selectorMembers(rule.selector).includes(selector),
  );
}

/** Style rules for an exact selector inside a given media condition. */
function rulesInMedia(selector: string, media: string): StyleRuleEntry[] {
  return sheet.styleRules.filter(
    (r) => r.selector === selector && r.media === media,
  );
}

/* -------------------------------------------------------------------------
 * DOMParser — parse index.html and assert exactly one viewport meta with
 * split, exact directives. No loose substring scanning.
 * ----------------------------------------------------------------------- */

function parseHtml(source: string): Document {
  return new DOMParser().parseFromString(source, "text/html");
}

const doc = parseHtml(htmlSource);

function viewportMetas(): HTMLMetaElement[] {
  return Array.from(doc.querySelectorAll("meta")).filter(
    (meta) => meta.getAttribute("name")?.toLowerCase() === "viewport",
  );
}

/** Split a viewport content string and reject duplicate normalized keys. */
function parseViewportDirectives(content: string): Map<string, string> {
  const map = new Map<string, string>();
  for (const part of content.split(",")) {
    const trimmed = part.trim();
    if (trimmed === "") continue;
    const eq = trimmed.indexOf("=");
    const key = (
      eq === -1 ? trimmed : trimmed.slice(0, eq).trim()
    ).toLowerCase();
    if (map.has(key)) throw new Error(`duplicate viewport directive: ${key}`);
    const value = eq === -1 ? "" : trimmed.slice(eq + 1).trim();
    map.set(key, value);
  }
  return map;
}

function viewportDirectives(): Map<string, string> {
  const metas = viewportMetas();
  expect(metas.length, "exactly one viewport meta").toBe(1);
  return parseViewportDirectives(
    must(metas[0], "viewport meta").getAttribute("content") ?? "",
  );
}

/* -------------------------------------------------------------------------
 * Helpers for safe-area edge accounting.
 * ----------------------------------------------------------------------- */

/** Which safe-area edges a declaration map references. */
function safeEdges(decls: Record<string, string>): Set<string> {
  const edges = new Set<string>();
  const all = Object.entries(decls)
    .filter(([property]) => !property.startsWith("--safe-area-"))
    .map(([, value]) => value)
    .join(" ");
  if (all.includes("--safe-area-top") || all.includes("safe-area-inset-top"))
    edges.add("top");
  if (
    all.includes("--safe-area-bottom") ||
    all.includes("safe-area-inset-bottom")
  )
    edges.add("bottom");
  if (all.includes("--safe-area-left") || all.includes("safe-area-inset-left"))
    edges.add("left");
  if (
    all.includes("--safe-area-right") ||
    all.includes("safe-area-inset-right")
  )
    edges.add("right");
  return edges;
}

function safeEdgeConsumers(): Record<string, string[]> {
  const result: Record<string, string[]> = {
    top: [],
    bottom: [],
    left: [],
    right: [],
  };
  for (const rule of sheet.styleRules) {
    const edges = safeEdges(rule.decls);
    for (const selector of selectorMembers(rule.selector)) {
      for (const edge of edges) result[edge]?.push(selector);
    }
  }
  for (const values of Object.values(result)) values.sort();
  return result;
}

const STRUCTURE_PROFILE = {
  id: "p1",
  name: "workstation",
  origin: "https://hub.example.com:8443",
} as const;

function renderRootStructure(mode: "onboarding" | "conversation" | "new") {
  const profiles = mode === "onboarding" ? [] : [STRUCTURE_PROFILE];
  const services = createShellServices({
    profiles,
    activeProfileId: mode === "onboarding" ? null : STRUCTURE_PROFILE.id,
  });
  const connection = createConnectionStore(services.profile);
  connection.setState({
    profiles,
    activeProfileId: mode === "onboarding" ? null : STRUCTURE_PROFILE.id,
    status: "ready",
    refresh: async () => {},
  });
  const navigation = createNavigationStore();
  if (mode === "conversation") {
    navigation.getState().pushConversation({
      sessionId: "session-1",
      title: "Conversation",
    });
  } else if (mode === "new") {
    navigation.getState().setTab("new");
  }
  const preferences = createPreferencesStore();
  return render(
    createElement(RootShell, {
      services,
      stores: { connection, navigation, preferences },
    }),
  ).container;
}

/* =========================================================================
 * EXISTING DESIGN-TOKEN CONTRACTS (parser-backed, exact selectors)
 * ====================================================================== */

describe("design tokens — semantic colors (forest-teal + summit-amber)", () => {
  it("root defines accent tokens (forest-teal light)", () => {
    const root = oneRule(":root");
    expect(root.decls["--accent"]).toBe("#0a7d62"); // jsdom lowercases hex
  });

  it("root defines accentInk for light theme", () => {
    const root = oneRule(":root");
    expect(root.decls["--accent-ink"]).toBe("#0b6b54");
  });

  it("dark media defines teal-light accent #3dd6a8", () => {
    const dark = rulesInMedia(":root", "(prefers-color-scheme: dark)");
    expect(dark.length).toBe(1);
    expect(must(dark[0], "dark root rule").decls["--accent"]).toBe("#3dd6a8");
  });

  it("root defines canvas #f2f2f7 and raised #ffffff", () => {
    const root = oneRule(":root");
    expect(root.decls["--canvas"]).toBe("#f2f2f7");
    expect(root.decls["--raised"]).toBe("#ffffff");
  });

  it("root defines status colors live/attention/danger", () => {
    const root = oneRule(":root");
    expect(root.decls["--live"]).toBe("#1e7a3f");
    expect(root.decls["--attention"]).toBe("#8a4d00");
    expect(root.decls["--danger"]).toBe("#c22f28");
  });
});

describe("design tokens — 44px tap target", () => {
  it("root defines a 44px tap-target token", () => {
    const root = oneRule(":root");
    expect(root.decls["--tap-target"]).toBe("44px");
  });

  it("interactive controls apply min tap target", () => {
    const button = oneRule(".evener-button");
    expect(button.decls["min-height"]).toBe("var(--tap-target)");
    expect(button.decls["min-width"]).toBe("var(--tap-target)");
    const icon = oneRule(".evener-icon-button");
    expect(icon.decls["min-height"]).toBe("var(--tap-target)");
    expect(icon.decls["min-width"]).toBe("var(--tap-target)");
  });
});

describe("design tokens — safe area tokens", () => {
  it("root defines all four safe-area insets from env()", () => {
    const root = oneRule(":root");
    // jsdom normalizes env() by stripping the space after the comma.
    expect(root.decls["--safe-area-top"]).toBe("env(safe-area-inset-top,0px)");
    expect(root.decls["--safe-area-bottom"]).toBe(
      "env(safe-area-inset-bottom,0px)",
    );
    expect(root.decls["--safe-area-left"]).toBe(
      "env(safe-area-inset-left,0px)",
    );
    expect(root.decls["--safe-area-right"]).toBe(
      "env(safe-area-inset-right,0px)",
    );
  });
});

describe("design tokens — spacing scale", () => {
  it("root defines the 4/8/12/16/20/24/32 spacing scale", () => {
    const root = oneRule(":root");
    for (const v of [4, 8, 12, 16, 20, 24, 32]) {
      expect(root.decls[`--space-${v}`]).toBe(`${v}px`);
    }
  });
});

describe("design tokens — radii", () => {
  it("root defines control/row/sheet radii (10/14/20)", () => {
    const root = oneRule(":root");
    expect(root.decls["--radius-control"]).toBe("10px");
    expect(root.decls["--radius-row"]).toBe("14px");
    expect(root.decls["--radius-sheet"]).toBe("20px");
  });
});

/* =========================================================================
 * VIEWPORT META — exact, one meta, split directives, no dupes/extras
 * ====================================================================== */

describe("7C lane D — viewport meta honors safe areas and keyboard", () => {
  it("index.html has exactly one viewport meta tag", () => {
    expect(viewportMetas().length).toBe(1);
  });

  it("viewport directives are exactly the expected set", () => {
    const d = viewportDirectives();
    // Exactly these four keys, no more, no less.
    expect(d.size).toBe(4);
    expect(Array.from(d.keys()).sort()).toEqual(
      ["initial-scale", "interactive-widget", "viewport-fit", "width"].sort(),
    );
  });

  it("width=device-width exactly", () => {
    expect(viewportDirectives().get("width")).toBe("device-width");
  });

  it("initial-scale=1.0 exactly", () => {
    expect(viewportDirectives().get("initial-scale")).toBe("1.0");
  });

  it("viewport-fit=cover exactly", () => {
    expect(viewportDirectives().get("viewport-fit")).toBe("cover");
  });

  it("interactive-widget=resizes-content exactly", () => {
    expect(viewportDirectives().get("interactive-widget")).toBe(
      "resizes-content",
    );
  });

  it("rejects a duplicate viewport meta (parser counts all)", () => {
    const dup = parseHtml(
      '<head><meta name="viewport" content="width=device-width"><meta name="Viewport" content="viewport-fit=cover"></head>',
    );
    const metas = Array.from(dup.querySelectorAll("meta")).filter(
      (meta) => meta.getAttribute("name")?.toLowerCase() === "viewport",
    );
    expect(metas.length).toBe(2);
  });

  it("rejects duplicate directives before map insertion", () => {
    expect(() =>
      parseViewportDirectives(
        "width=device-width, WIDTH=device-width, initial-scale=1.0",
      ),
    ).toThrow(/duplicate viewport directive: width/);
  });
});

/* =========================================================================
 * SAFE-AREA OWNERSHIP — one unambiguous owner per edge, exact selectors
 * ====================================================================== */

describe("7C lane D — one unambiguous safe-area owner per edge", () => {
  it("shell owns viewport height/overflow but NOT any safe-area padding", () => {
    const shell = oneRule(".evener-shell");
    expect(shell.decls.height).toBe("var(--viewport-height)");
    expect(shell.decls.overflow).toBe("hidden");
    expect(shell.decls["padding-top"]).toBeUndefined();
    expect(shell.decls["padding-bottom"]).toBeUndefined();
    expect(shell.decls["padding-left"]).toBeUndefined();
    expect(shell.decls["padding-right"]).toBeUndefined();
    expect(safeEdges(shell.decls).size).toBe(0);
  });

  it("TopBar (base) owns top + left + right safe edges, not bottom", () => {
    const topbar = oneRule(".evener-topbar");
    const edges = safeEdges(topbar.decls);
    expect(edges.has("top")).toBe(true);
    expect(edges.has("left")).toBe(true);
    expect(edges.has("right")).toBe(true);
    expect(edges.has("bottom")).toBe(false);
    // Exact top padding composes space + safe once.
    expect(topbar.decls["padding-top"]).toBe(
      "calc(var(--space-8) + var(--safe-area-top))",
    );
    expect(topbar.decls["padding-left"]).toBe(
      "calc(var(--space-16) + var(--safe-area-left))",
    );
    expect(topbar.decls["padding-right"]).toBe(
      "calc(var(--space-16) + var(--safe-area-right))",
    );
  });

  it("nested screen-scroll TopBar cancels ancestor horizontal inset exactly", () => {
    const nested = oneRule(".evener-screen-scroll .evener-topbar");
    // Asymmetric left/right negative margins matching the scroll's insets.
    expect(nested.decls["margin-left"]).toBe(
      "calc(-1 * var(--safe-area-left))",
    );
    expect(nested.decls["margin-right"]).toBe(
      "calc(-1 * var(--safe-area-right))",
    );
    // The compensation rule must NOT itself add safe-area padding (it only
    // cancels the ancestor; the base TopBar re-adds space+safe once).
    expect(nested.decls["padding-left"]).toBeUndefined();
    expect(nested.decls["padding-right"]).toBeUndefined();
    expect(nested.decls["padding-top"]).toBeUndefined();
  });

  it("BottomBar owns bottom + left + right safe edges, not top", () => {
    const bar = oneRule(".evener-bottombar");
    const edges = safeEdges(bar.decls);
    expect(edges.has("bottom")).toBe(true);
    expect(edges.has("left")).toBe(true);
    expect(edges.has("right")).toBe(true);
    expect(edges.has("top")).toBe(false);
    expect(bar.decls["padding-bottom"]).toBe("var(--safe-area-bottom)");
    expect(bar.decls["padding-left"]).toBe("var(--safe-area-left)");
    expect(bar.decls["padding-right"]).toBe("var(--safe-area-right)");
  });

  it("screen-scroll owns left + right only (no vertical double counting)", () => {
    const scroll = oneRule(".evener-screen-scroll");
    const edges = safeEdges(scroll.decls);
    expect(edges.has("left")).toBe(true);
    expect(edges.has("right")).toBe(true);
    expect(edges.has("top")).toBe(false);
    expect(edges.has("bottom")).toBe(false);
    expect(scroll.decls["padding-left"]).toBe("var(--safe-area-left)");
    expect(scroll.decls["padding-right"]).toBe("var(--safe-area-right)");
    expect(scroll.decls["padding-top"]).toBeUndefined();
    expect(scroll.decls["padding-bottom"]).toBeUndefined();
  });

  it("onboarding owns all four safe edges", () => {
    const onb = oneRule(".evener-onboarding");
    const edges = safeEdges(onb.decls);
    expect(edges.has("top")).toBe(true);
    expect(edges.has("bottom")).toBe(true);
    expect(edges.has("left")).toBe(true);
    expect(edges.has("right")).toBe(true);
  });

  it("Sheet owns bottom + horizontal safe edges, not top", () => {
    const sheetRule = oneRule(".evener-sheet");
    const edges = safeEdges(sheetRule.decls);
    expect(edges.has("bottom")).toBe(true);
    expect(edges.has("left")).toBe(true);
    expect(edges.has("right")).toBe(true);
    expect(edges.has("top")).toBe(false);
    expect(sheetRule.decls["padding-bottom"]).toBe("var(--safe-area-bottom)");
    expect(sheetRule.decls["padding-left"]).toBe("var(--safe-area-left)");
    expect(sheetRule.decls["padding-right"]).toBe("var(--safe-area-right)");
  });

  it("conversation owns NO safe-area edges (TopBar owns top, dock owns bottom)", () => {
    const conv = oneRule(".evener-conversation");
    expect(safeEdges(conv.decls).size).toBe(0);
    expect(conv.decls["padding-top"]).toBeUndefined();
    expect(conv.decls["padding-bottom"]).toBeUndefined();
    expect(conv.decls["padding-left"]).toBeUndefined();
    expect(conv.decls["padding-right"]).toBeUndefined();
  });

  it("dock uses max(keyboard, safe-bottom) — never calc(+)", () => {
    const dock = oneRule(".evener-dock");
    expect(dock.decls["padding-bottom"]).toBe(
      "max(var(--keyboard-inset), var(--safe-area-bottom))",
    );
    // Reject any calc that adds the two (would double to 68px).
    expect(dock.decls["padding-bottom"]).not.toContain("calc(");
  });

  it("enumerates every effective safe-edge consumer, including grouped selectors", () => {
    expect(safeEdgeConsumers()).toEqual({
      top: [".evener-onboarding", ".evener-topbar"],
      bottom: [
        ".evener-bottombar",
        ".evener-dock",
        ".evener-onboarding",
        ".evener-sheet",
      ],
      left: [
        ".evener-bottombar",
        ".evener-onboarding",
        ".evener-screen-scroll",
        ".evener-screen-scroll .evener-topbar",
        ".evener-sheet",
        ".evener-topbar",
      ],
      right: [
        ".evener-bottombar",
        ".evener-onboarding",
        ".evener-screen-scroll",
        ".evener-screen-scroll .evener-topbar",
        ".evener-sheet",
        ".evener-topbar",
      ],
    });
  });

  it("a 34px safe bottom composes to exactly 34px for the conversation dock", () => {
    const conv = oneRule(".evener-conversation");
    expect(safeEdges(conv.decls).has("bottom")).toBe(false);
    const dockValue = oneRule(".evener-dock").decls["padding-bottom"];
    expect(dockValue).toBe(
      "max(var(--keyboard-inset), var(--safe-area-bottom))",
    );
    const composedBottom = (keyboard: number, safe: number): number =>
      Math.max(keyboard, safe);
    expect(composedBottom(0, 34)).toBe(34);
    expect(composedBottom(34, 34)).toBe(34);
    expect(composedBottom(300, 34)).toBe(300);
  });
});

/* =========================================================================
 * ONBOARDING — sole vertical scroller, shell flex geometry
 * ====================================================================== */

describe("7C lane D — onboarding is the one vertical scroller", () => {
  it("real RootShell onboarding has exactly one flow scroller and no screen-scroll ancestor", () => {
    const container = renderRootStructure("onboarding");
    const onboarding = container.querySelector(".evener-onboarding");
    expect(onboarding).not.toBeNull();
    expect(container.querySelectorAll(".evener-onboarding").length).toBe(1);
    expect(container.querySelectorAll(".evener-screen-scroll").length).toBe(0);
    expect(onboarding?.parentElement).toHaveClass("evener-shell");
  });

  it("onboarding uses flex:1 to participate in shell flex geometry", () => {
    expect(oneRule(".evener-onboarding").decls.flex).toBe("1 1 auto");
  });

  it("onboarding uses min-height:0 to allow shrinking below content", () => {
    // jsdom normalizes unitless 0 to 0px.
    expect(oneRule(".evener-onboarding").decls["min-height"]).toBe("0px");
  });

  it("onboarding has overflow-y:auto for vertical scrolling", () => {
    expect(oneRule(".evener-onboarding").decls["overflow-y"]).toBe("auto");
  });

  it("onboarding does NOT pin min-height to viewport-height (would clip)", () => {
    const onb = oneRule(".evener-onboarding");
    expect(onb.decls["min-height"]).not.toContain("viewport-height");
  });
});

/* =========================================================================
 * DYNAMIC TYPE — exact :root[data-content-size] rules, bounded values
 * ====================================================================== */

const CONTENT_SIZE_CATEGORIES = [
  "small",
  "medium",
  "large",
  "extraLarge",
  "extraExtraLarge",
  "extraExtraExtraLarge",
  "accessibilityMedium",
  "accessibilityLarge",
  "accessibilityExtraLarge",
  "accessibilityExtraExtraLarge",
  "accessibilityExtraExtraExtraLarge",
] as const;

const CONTENT_SIZE_VALUES: Record<string, { base: string; leading: string }> = {
  small: { base: "15px", leading: "1.35" },
  medium: { base: "16px", leading: "1.35" },
  large: { base: "17px", leading: "1.4" },
  extraLarge: { base: "19px", leading: "1.4" },
  extraExtraLarge: { base: "21px", leading: "1.45" },
  extraExtraExtraLarge: { base: "23px", leading: "1.45" },
  accessibilityMedium: { base: "20px", leading: "1.45" },
  accessibilityLarge: { base: "22px", leading: "1.5" },
  accessibilityExtraLarge: { base: "24px", leading: "1.5" },
  accessibilityExtraExtraLarge: { base: "27px", leading: "1.5" },
  accessibilityExtraExtraExtraLarge: { base: "30px", leading: "1.55" },
};

describe("7C lane D — dynamic type recomputes root font and line height", () => {
  it("root :root sets font-size and line-height from type tokens", () => {
    const root = oneRule(":root");
    expect(root.decls["font-size"]).toBe("var(--type-base, 17px)");
    expect(root.decls["line-height"]).toBe("var(--type-leading, 1.4)");
  });

  it("root :root sets the system font-family stack", () => {
    const root = oneRule(":root");
    expect(root.decls["font-family"]).toContain("-apple-system");
  });

  it("defines exactly 11 bounded content-size categories as :root[data-content-size]", () => {
    const actual = sheet.styleRules
      .filter((rule) => rule.media === "")
      .flatMap((rule) => selectorMembers(rule.selector))
      .filter(
        (selector) =>
          selector.startsWith(':root[data-content-size="') &&
          selector.endsWith('"]'),
      )
      .sort();
    const expected = CONTENT_SIZE_CATEGORIES.map(
      (category) => `:root[data-content-size="${category}"]`,
    ).sort();
    expect(actual).toEqual(expected);
  });

  it("each content-size category sets bounded --type-base and --type-leading", () => {
    for (const cat of CONTENT_SIZE_CATEGORIES) {
      const rule = oneRule(`:root[data-content-size="${cat}"]`);
      const expected = must(CONTENT_SIZE_VALUES[cat], `values for ${cat}`);
      expect(rule.decls["--type-base"]).toBe(expected.base);
      expect(rule.decls["--type-leading"]).toBe(expected.leading);
    }
  });

  it("AX5 (accessibilityExtraExtraExtraLarge) maps to 30px base / 1.55 leading", () => {
    const rule = oneRule(
      ':root[data-content-size="accessibilityExtraExtraExtraLarge"]',
    );
    expect(rule.decls["--type-base"]).toBe("30px");
    expect(rule.decls["--type-leading"]).toBe("1.55");
  });

  it("content-size selectors are pinned to :root (portal-compatible)", () => {
    // A bare [data-content-size="..."] (not :root-qualified) must NOT exist.
    for (const cat of CONTENT_SIZE_CATEGORIES) {
      const bare = rulesFor(`[data-content-size="${cat}"]`);
      expect(
        bare.length,
        `bare [data-content-size="${cat}"] should not exist`,
      ).toBe(0);
    }
  });

  it("body explicitly inherits font-size and line-height (not just family)", () => {
    const body = oneRule("body");
    expect(body.decls["font-family"]).toBe("inherit");
    expect(body.decls["font-size"]).toBe("inherit");
    expect(body.decls["line-height"]).toBe("inherit");
  });

  it("controls use font:inherit so they share the Dynamic Type scale", () => {
    const button = oneRule(".evener-button");
    expect(button.decls.font).toBe("inherit");
    const input = oneRule(".evener-input");
    expect(input.decls.font).toBe("inherit");
    const tab = oneRule(".evener-tab");
    expect(tab.decls.font).toBe("inherit");
  });
});

/* =========================================================================
 * THEMES — exact :root[data-theme] rules, color-scheme, precedence
 * ====================================================================== */

describe("7C lane D — system and explicit theme variables on html", () => {
  it("dark media sets color-scheme: dark on :root", () => {
    const dark = rulesInMedia(":root", "(prefers-color-scheme: dark)");
    expect(dark.length).toBe(1);
    const darkRoot = must(dark[0], "dark root rule");
    expect(darkRoot.decls["color-scheme"]).toBe("dark");
    expect(darkRoot.decls["--canvas"]).toBe("#000000");
    expect(darkRoot.decls["--raised"]).toBe("#1c1c1e");
  });

  it("explicit light theme is :root[data-theme=light] with color-scheme: light", () => {
    const rule = oneRule(':root[data-theme="light"]');
    expect(rule.decls["color-scheme"]).toBe("light");
    expect(rule.decls["--canvas"]).toBe("#f2f2f7");
    expect(rule.decls["--raised"]).toBe("#ffffff");
  });

  it("explicit dark theme is :root[data-theme=dark] with color-scheme: dark", () => {
    const rule = oneRule(':root[data-theme="dark"]');
    expect(rule.decls["color-scheme"]).toBe("dark");
    expect(rule.decls["--canvas"]).toBe("#000000");
    expect(rule.decls["--raised"]).toBe("#1c1c1e");
  });

  it("theme selectors are pinned to :root (not bare attribute)", () => {
    expect(rulesFor('[data-theme="light"]').length).toBe(0);
    expect(rulesFor('[data-theme="dark"]').length).toBe(0);
  });

  it("html/body background follows canvas token", () => {
    // jsdom normalizes the grouped selector to "html,\nbody".
    const htmlBody = oneRule("html,\nbody");
    expect(htmlBody.decls.background).toBe("var(--canvas)");
    expect(htmlBody.decls.color).toBe("var(--primary)");
  });

  it("root :root declares color-scheme: light dark", () => {
    expect(oneRule(":root").decls["color-scheme"]).toBe("light dark");
  });
});

/* =========================================================================
 * CONVERSATION — flex geometry, one scroller, reduced-motion semantics
 * ====================================================================== */

describe("7C lane D — conversation participates in shell flex geometry", () => {
  it("real RootShell conversation has one scroller beside a top bar", () => {
    const container = renderRootStructure("conversation");
    const conversation = container.querySelector<HTMLElement>(
      '[data-concept-root][data-surface="conversation"]',
    );
    if (conversation === null) {
      throw new Error("missing live conversation concept root");
    }
    const scroller = conversation.querySelector<HTMLElement>(
      ':scope > .sw-scroll[data-route="conversation"]',
    );
    if (scroller === null)
      throw new Error("missing live conversation scroller");

    // The production Stillwater Host renders one route scroller. Its sticky
    // top bar and route content are direct children of that scroller, preserving
    // the canonical shell's single-scroll-owner geometry.
    expect(conversation.querySelectorAll(":scope > .sw-scroll").length).toBe(1);
    expect(conversation.querySelectorAll(".sw-scroll").length).toBe(1);
    expect(scroller.querySelectorAll(":scope > .sw-topbar").length).toBe(1);
    expect(scroller.querySelectorAll(":scope > .sw-route-content").length).toBe(
      1,
    );
    expect(container.querySelectorAll(".evener-bottombar").length).toBe(0);
  });

  it("real tab screen nests TopBar under the compensated screen scroller", () => {
    const container = renderRootStructure("new");
    expect(
      container.querySelectorAll(".evener-screen-scroll .evener-topbar").length,
    ).toBe(1);
    expect(container.querySelectorAll(".evener-conversation").length).toBe(0);
  });

  it("conversation uses flex:1, not position:fixed", () => {
    const conv = oneRule(".evener-conversation");
    expect(conv.decls.flex).toBe("1 1 auto");
    expect(conv.decls.position).toBeUndefined();
    expect(conv.decls.inset).toBeUndefined();
  });

  it("conversation keeps background and reduced-motion-compatible animation", () => {
    const conv = oneRule(".evener-conversation");
    expect(conv.decls.background).toBe("var(--canvas)");
    expect(conv.decls.animation).toContain("evener-conversation-in");
  });

  it("conversation keeps one scroller via evener-screen-scroll", () => {
    const scroll = oneRule(".evener-screen-scroll");
    expect(scroll.decls["overflow-y"]).toBe("auto");
  });

  it("reduced-motion media removes conversation animation", () => {
    // The reduced-motion override is a grouped selector (.evener-sheet,
    // .evener-conversation). Find it by media + selector membership.
    const reducedMedia = sheet.styleRules.find(
      (r) =>
        r.media.includes("prefers-reduced-motion") &&
        selectorMembers(r.selector).includes(".evener-conversation"),
    );
    expect(
      reducedMedia,
      "reduced-motion .evener-conversation rule",
    ).toBeTruthy();
    expect(reducedMedia?.decls.animation).toBe("none");
    expect(reducedMedia?.decls.transform).toBe("none");
    // The exact single selector ".evener-conversation" is NOT present inside
    // the media — only the grouped form is. This proves a grouped selector
    // does not satisfy an exact-selector check.
    const exact = sheet.styleRules.filter(
      (r) =>
        r.media.includes("prefers-reduced-motion") &&
        r.selector === ".evener-conversation",
    );
    expect(exact.length).toBe(0);
  });
});

/* =========================================================================
 * PARSER RIGOR — the parser collects ALL rules, rejects grouped/comment
 * satisfactions, and detects duplicate selector rules.
 * ====================================================================== */

describe("parser rigor — exact selectors, duplicates, comments, grouping", () => {
  it("a duplicate later rule for the same selector is collected separately", () => {
    const model = parseSheet(".x { color: red; } .x { color: blue; }");
    const xs = model.styleRules.filter((r) => r.selector === ".x");
    expect(xs.length).toBe(2);
    expect(must(xs[0], "first .x rule").decls.color).toBe("red");
    expect(must(xs[1], "second .x rule").decls.color).toBe("blue");
  });

  it("a grouped selector does not satisfy an exact single-selector check", () => {
    const model = parseSheet(".a, .b { color: red; }");
    // Exact selector ".a" alone is NOT present; only ".a, .b" is.
    expect(model.styleRules.filter((r) => r.selector === ".a").length).toBe(0);
    const grouped = model.styleRules.filter((r) => r.selector === ".a, .b");
    expect(grouped.length).toBe(1);
    expect(must(grouped[0], "grouped rule").decls.color).toBe("red");
  });

  it("a comment cannot satisfy a declaration check", () => {
    // The CSSOM drops comments, so a rule whose only "declaration" is in a
    // comment has an empty declaration map.
    const model = parseSheet("/* color: red; */ .x { margin: 0; }");
    const x = model.styleRules.filter((r) => r.selector === ".x");
    expect(x.length).toBe(1);
    const xRule = must(x[0], ".x rule");
    expect(xRule.decls.color).toBeUndefined();
    // jsdom normalizes unitless 0 to 0px.
    expect(xRule.decls.margin).toBe("0px");
  });

  it("@media nested rules are collected with their media context", () => {
    const model = parseSheet(
      "@media (min-width: 1px) { .y { color: green; } }",
    );
    const y = model.styleRules.find(
      (r) => r.selector === ".y" && r.media === "(min-width: 1px)",
    );
    expect(y?.decls.color).toBe("green");
  });

  it("real sheet has one effective .evener-shell rule, including grouped selectors", () => {
    expect(effectiveRulesFor(".evener-shell").length).toBe(1);
  });

  it("real sheet has one effective .evener-topbar base rule", () => {
    expect(effectiveRulesFor(".evener-topbar").length).toBe(1);
  });
});
