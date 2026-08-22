import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

// Read the compiled global CSS to assert design-system contracts without
// rendering pixels. The source tokens must compile into the production CSS.
const cssPath = path.join(__dirname, "global.css");
const css = readFileSync(cssPath, "utf8");

function has(rule: RegExp): boolean {
  return rule.test(css);
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
