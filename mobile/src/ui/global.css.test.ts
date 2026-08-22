import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

// Read the compiled global CSS to assert design-system contracts without
// rendering pixels. The source tokens must compile into the production CSS.
const cssPath = path.join(__dirname, "global.css");
const css = readFileSync(cssPath, "utf8");

// Read the HTML host page to assert viewport meta and root markup.
const htmlPath = path.join(__dirname, "..", "..", "index.html");
const html = readFileSync(htmlPath, "utf8");

function has(rule: RegExp): boolean {
  return rule.test(css);
}

/**
 * Extract the first rule block for a selector from the CSS source.
 * Returns the declarations string (inside the braces) or null if not found.
 * This is more precise than a loose substring search because it isolates
 * which selector owns a declaration, preventing coincidental passes from
 * an unrelated rule that happens to mention the same token.
 */
function ruleBlock(selector: string): string | null {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  // Match the selector followed by its { ... } block. The /s flag lets the
  // body span newlines. We capture the inside of the braces.
  const re = new RegExp(`${escaped}\\s*\\{([^}]*)\\}`, "s");
  const m = css.match(re);
  return m ? m[1] : null;
}

/**
 * Extract the viewport meta tag content from index.html.
 */
function viewportMeta(): string | null {
  const m = html.match(
    /<meta\s+name=["']viewport["']\s+content=["']([^"']*)["']/i,
  );
  return m ? m[1] : null;
}

describe("design tokens — semantic colors (forest-teal + summit-amber)", () => {
  it("defines accent tokens (forest-teal light/dark)", () => {
    expect(has(/--accent\s*:\s*#0A7D62/i)).toBe(true);
  });

  it("defines accentInk for light theme", () => {
    expect(
      has(/--accent-ink\s*:\s*#0B6B54/i) || has(/--accentInk\s*:\s*#0B6B54/i),
    ).toBe(true);
  });

  it("defines dark-mode accent (summit/teal light)", () => {
    expect(
      css.match(/--accent\s*:\s*#3DD6A8/i) ?? css.match(/#3DD6A8/i),
    ).toBeTruthy();
  });

  it("defines canvas and raised surface tokens", () => {
    expect(
      has(/--canvas\s*:\s*#F2F2F7/i) || has(/--background\s*:\s*#F2F2F7/i),
    ).toBe(true);
    expect(
      has(/--raised\s*:\s*#FFFFFF/i) || has(/--surface\s*:\s*#FFFFFF/i),
    ).toBe(true);
  });

  it("defines status colors: live (green), attention (amber), danger (red)", () => {
    expect(has(/--live\s*:\s*#1E7A3F/i)).toBe(true);
    expect(has(/--attention\s*:\s*#8A4D00/i)).toBe(true);
    expect(has(/--danger\s*:\s*#C22F28/i)).toBe(true);
  });
});

describe("design tokens — 44px tap target", () => {
  it("defines a 44px tap-target token", () => {
    expect(has(/--tap-target\s*:\s*44px/i)).toBe(true);
  });

  it("applies min tap target to interactive controls", () => {
    expect(has(/min-height\s*:\s*var\(--tap-target\)/i)).toBe(true);
    expect(has(/min-width\s*:\s*var\(--tap-target\)/i)).toBe(true);
  });
});

describe("design tokens — safe area", () => {
  it("defines safe-area insets from env()", () => {
    expect(has(/--safe-area-top\s*:\s*env\(safe-area-inset-top/i)).toBe(true);
    expect(has(/--safe-area-bottom\s*:\s*env\(safe-area-inset-bottom/i)).toBe(
      true,
    );
  });

  it("applies safe-area padding to the shell chrome", () => {
    expect(has(/var\(--safe-area-top\)/i)).toBe(true);
    expect(has(/var\(--safe-area-bottom\)/i)).toBe(true);
  });
});

describe("design tokens — spacing scale", () => {
  it("defines the 4/8/12/16/20/24/32 spacing scale", () => {
    for (const v of [4, 8, 12, 16, 20, 24, 32]) {
      expect(has(new RegExp(`--space-${v}\\s*:\\s*${v}px`, "i"))).toBe(true);
    }
  });
});

describe("design tokens — radii", () => {
  it("defines control, row, and sheet radii (10/14/20)", () => {
    expect(
      has(/--radius-control\s*:\s*10px/i) || has(/--radius-sm\s*:\s*10px/i),
    ).toBe(true);
    expect(
      has(/--radius-row\s*:\s*14px/i) || has(/--radius-md\s*:\s*14px/i),
    ).toBe(true);
    expect(
      has(/--radius-sheet\s*:\s*20px/i) || has(/--radius-lg\s*:\s*20px/i),
    ).toBe(true);
  });
});

describe("Dynamic Type — semantic type scale", () => {
  it("defines content-size categories via data attribute classes", () => {
    expect(has(/\[data-content-size/i) || has(/data-content-size/i)).toBe(true);
  });

  it("maps at least small, large, extraExtraLarge, and AX5 to scale steps", () => {
    expect(has(/extraExtraLarge/i) || has(/xxl/i)).toBe(true);
    expect(has(/accessibilityExtraExtraExtraLarge/i) || has(/ax5/i)).toBe(true);
  });
});

describe("reduced motion — disables spatial movement", () => {
  it("defines a prefers-reduced-motion media block", () => {
    expect(has(/@media\s*\(prefers-reduced-motion/i)).toBe(true);
  });

  it("removes transitions/animations under reduced motion", () => {
    expect(has(/transition\s*:\s*none/i) || has(/animation\s*:\s*none/i)).toBe(
      true,
    );
  });
});

describe("themes — light and dark", () => {
  it("defines a dark color-scheme media block", () => {
    expect(has(/prefers-color-scheme:\s*dark/i)).toBe(true);
  });

  it("defines explicit dark canvas #000 and raised #1C1C1E", () => {
    expect(has(/--canvas\s*:\s*#000/i) || has(/--background\s*:\s*#000/i)).toBe(
      true,
    );
    expect(
      has(/--raised\s*:\s*#1C1C1E/i) || has(/--surface\s*:\s*#1C1C1E/i),
    ).toBe(true);
  });
});

describe("keyboard viewport — bottom dock above keyboard", () => {
  it("defines a keyboard inset token", () => {
    expect(has(/--keyboard-inset/i) || has(/keyboard|visualViewport/i)).toBe(
      true,
    );
  });
});

describe("fix round 1 — dark glyph contrast", () => {
  it("dark-mode status glyphs use dark ink on bright fills for >=3:1", () => {
    expect(
      has(/--status-glyph-ink\s*:\s*#000/i) || has(/color\s*:\s*#000/i),
    ).toBe(true);
  });

  it("status marks always have a text label class, not glyph alone", () => {
    expect(has(/evener-status-mark__label/i)).toBe(true);
  });
});

describe("fix round 1 — no duplicate safe-area padding", () => {
  it("shell does NOT apply safe-area insets (edge owners apply them once)", () => {
    const shell = ruleBlock(".evener-shell");
    expect(shell).not.toBeNull();
    expect(shell).not.toContain("safe-area");
  });
});

describe("fix round 1 — intrinsic rows at 375/AX", () => {
  it("list rows use min-height not fixed height", () => {
    const rowFixedHeight = css.match(
      /\.evener-list-row\s*\{[^}]*\bheight\s*:\s*\d+px[^}]*\}/s,
    );
    expect(rowFixedHeight).toBeFalsy();
  });
});

describe("fix round 1 — tablist container", () => {
  it("bottom bar CSS class exists for tablist role", () => {
    expect(has(/evener-bottombar/i)).toBe(true);
  });
});

/* ---------------------------------------------------------------------------
 * Foundation 7C lane D — safe-area ownership, dynamic type, onboarding scroll,
 * keyboard-ready geometry. These tests parse rule blocks (not loose
 * substrings) to reject duplicate inset ownership, missing scrolling,
 * inherited font failure, and missing viewport meta.
 * ------------------------------------------------------------------------- */

describe("7C lane D — viewport meta honors safe areas and keyboard", () => {
  it("index.html has a viewport meta tag", () => {
    expect(viewportMeta()).not.toBeNull();
  });

  it("viewport meta includes viewport-fit=cover", () => {
    expect(viewportMeta()).toMatch(/viewport-fit\s*=\s*cover/i);
  });

  it("viewport meta includes interactive-widget=resizes-content", () => {
    expect(viewportMeta()).toMatch(/interactive-widget\s*=\s*resizes-content/i);
  });

  it("viewport meta preserves width=device-width", () => {
    expect(viewportMeta()).toMatch(/width\s*=\s*device-width/i);
  });

  it("viewport meta preserves initial-scale=1", () => {
    expect(viewportMeta()).toMatch(/initial-scale\s*=\s*1(\.0)?/i);
  });
});

describe("7C lane D — one unambiguous safe-area owner per edge", () => {
  it("shell owns viewport height/overflow but NOT safe-area padding", () => {
    const shell = ruleBlock(".evener-shell");
    expect(shell).not.toBeNull();
    expect(shell).toContain("var(--viewport-height)");
    expect(shell).toContain("overflow");
    expect(shell).not.toContain("safe-area");
  });

  it("TopBar owns top + horizontal safe edges", () => {
    const topbar = ruleBlock(".evener-topbar");
    expect(topbar).not.toBeNull();
    expect(topbar).toContain("var(--safe-area-top)");
    // horizontal edges
    const horizMatch =
      topbar.includes("var(--safe-area-left)") ||
      topbar.includes("var(--safe-area-right)");
    expect(horizMatch).toBe(true);
  });

  it("TopBar does NOT own the bottom safe edge", () => {
    const topbar = ruleBlock(".evener-topbar");
    expect(topbar).not.toBeNull();
    expect(topbar).not.toContain("var(--safe-area-bottom)");
  });

  it("BottomBar owns the bottom safe edge", () => {
    const bottombar = ruleBlock(".evener-bottombar");
    expect(bottombar).not.toBeNull();
    expect(bottombar).toContain("var(--safe-area-bottom)");
  });

  it("BottomBar does NOT own the top safe edge", () => {
    const bottombar = ruleBlock(".evener-bottombar");
    expect(bottombar).not.toBeNull();
    expect(bottombar).not.toContain("var(--safe-area-top)");
  });

  it("onboarding owns all four safe edges", () => {
    const onboarding = ruleBlock(".evener-onboarding");
    expect(onboarding).not.toBeNull();
    expect(onboarding).toContain("var(--safe-area-top)");
    expect(onboarding).toContain("var(--safe-area-bottom)");
    const horiz =
      onboarding.includes("var(--safe-area-left)") ||
      onboarding.includes("var(--safe-area-right)");
    expect(horiz).toBe(true);
  });

  it("Sheet owns bottom + horizontal safe edges", () => {
    const sheet = ruleBlock(".evener-sheet");
    expect(sheet).not.toBeNull();
    expect(sheet).toContain("var(--safe-area-bottom)");
    const horiz =
      sheet.includes("var(--safe-area-left)") ||
      sheet.includes("var(--safe-area-right)");
    expect(horiz).toBe(true);
  });

  it("Sheet does NOT own the top safe edge", () => {
    const sheet = ruleBlock(".evener-sheet");
    expect(sheet).not.toBeNull();
    expect(sheet).not.toContain("var(--safe-area-top)");
  });

  it("screen-scroll gets horizontal safety without top/bottom double counting", () => {
    const scroll = ruleBlock(".evener-screen-scroll");
    expect(scroll).not.toBeNull();
    // horizontal safety is allowed (and expected) for content that may
    // exceed the shell's lateral bounds, but vertical insets must NOT be
    // doubled here — TopBar/BottomBar own the vertical edges.
    expect(scroll).not.toContain("var(--safe-area-top)");
    expect(scroll).not.toContain("var(--safe-area-bottom)");
  });

  it("a simulated 34px bottom inset yields 34px total, never 68px (no double application)", () => {
    // Count how many distinct top-level layout rule blocks apply BOTH
    // --safe-area-bottom AND --keyboard-inset in the same padding
    // declaration. A dock should use max() — not calc(+) — so the inset
    // never stacks keyboard + safe-area into 68px when only 34px is real.
    const dock = ruleBlock(".evener-dock");
    expect(dock).not.toBeNull();
    // The dock must NOT use calc(x + y) which would double-count.
    const doubleCount =
      /calc\s*\([^)]*--keyboard-inset[^)]*\+\s*var\(--safe-area-bottom\)/;
    expect(dock).not.toMatch(doubleCount);
    // Instead it should use max() so only the larger of keyboard/safe applies.
    expect(dock).toMatch(/max\s*\(/);
    expect(dock).toContain("--keyboard-inset");
    expect(dock).toContain("--safe-area-bottom");
  });
});

describe("7C lane D — onboarding is the one vertical scroller", () => {
  it("onboarding uses flex:1 so it participates in shell flex geometry", () => {
    const onboarding = ruleBlock(".evener-onboarding");
    expect(onboarding).not.toBeNull();
    expect(onboarding).toMatch(/flex\s*:\s*1/i);
  });

  it("onboarding uses min-height:0 to allow shrinking below content", () => {
    const onboarding = ruleBlock(".evener-onboarding");
    expect(onboarding).not.toBeNull();
    expect(onboarding).toMatch(/min-height\s*:\s*0/i);
  });

  it("onboarding has overflow-y:auto for vertical scrolling", () => {
    const onboarding = ruleBlock(".evener-onboarding");
    expect(onboarding).not.toBeNull();
    expect(onboarding).toMatch(/overflow-y\s*:\s*auto/i);
  });

  it("onboarding does NOT use min-height:viewport-height (would clip instead of scroll)", () => {
    const onboarding = ruleBlock(".evener-onboarding");
    expect(onboarding).not.toBeNull();
    expect(onboarding).not.toMatch(
      /min-height\s*:\s*var\(--viewport-height\)/i,
    );
  });
});

describe("7C lane D — dynamic type recomputes root font and line height", () => {
  it("root :root sets font-size and line-height from type tokens", () => {
    const root = ruleBlock(":root");
    expect(root).not.toBeNull();
    expect(root).toContain("font-size");
    expect(root).toContain("line-height");
  });

  it("all 11 bounded content-size categories are defined", () => {
    const categories = [
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
    ];
    for (const cat of categories) {
      expect(has(new RegExp(`\\[data-content-size="${cat}"\\]`, "i"))).toBe(
        true,
      );
    }
  });

  it("each content-size category sets --type-base and --type-leading", () => {
    const categories = [
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
    ];
    for (const cat of categories) {
      const block = ruleBlock(`[data-content-size="${cat}"]`);
      expect(block).not.toBeNull();
      expect(block).toContain("--type-base");
      expect(block).toContain("--type-leading");
    }
  });

  it("AX5 (accessibilityExtraExtraExtraLarge) maps to 30px base", () => {
    const block = ruleBlock(
      '[data-content-size="accessibilityExtraExtraExtraLarge"]',
    );
    expect(block).not.toBeNull();
    expect(block).toMatch(/--type-base\s*:\s*30px/i);
  });

  it("body does NOT override font-size (portal descendants inherit root)", () => {
    const body = ruleBlock("body");
    expect(body).not.toBeNull();
    expect(body).not.toMatch(/font-size\s*:/i);
  });
});

describe("7C lane D — conversation participates in shell flex geometry", () => {
  it("conversation uses flex:1 not position:fixed inset:0", () => {
    const conv = ruleBlock(".evener-conversation");
    expect(conv).not.toBeNull();
    expect(conv).toMatch(/flex\s*:\s*1/i);
    expect(conv).not.toMatch(/position\s*:\s*fixed/i);
  });

  it("conversation keeps one scroller (screen-scroll) for content", () => {
    // The conversation markup reuses evener-screen-scroll; the CSS class must
    // still exist and remain a scroller.
    const scroll = ruleBlock(".evener-screen-scroll");
    expect(scroll).not.toBeNull();
    expect(scroll).toMatch(/overflow-y\s*:\s*auto/i);
  });

  it("conversation preserves reduced-motion animation override", () => {
    const reduced = css.match(
      /data-reduced-motion.*?\.evener-conversation\s*\{[^}]*animation\s*:\s*none/s,
    );
    expect(reduced).toBeTruthy();
  });
});
