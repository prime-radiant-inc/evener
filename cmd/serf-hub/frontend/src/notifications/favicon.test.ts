import { afterEach, beforeEach, describe, expect, test } from "vitest";
import type { AttentionSummary } from "../stores/tree";
import { applyFavicon, buildFaviconDataURI, dotColorFor } from "./favicon";

function summary(needsYou: number, error: number, working: number): AttentionSummary {
  return { needsYou, error, working };
}

function faviconHref(): string | null {
  return document.querySelector<HTMLLinkElement>("link[rel='icon']")?.getAttribute("href") ?? null;
}

beforeEach(() => {
  for (const link of Array.from(document.querySelectorAll("link[rel='icon']"))) link.remove();
});
afterEach(() => {
  for (const link of Array.from(document.querySelectorAll("link[rel='icon']"))) link.remove();
});

describe("dotColorFor", () => {
  // Priority error > needs_you > working; no dot when none apply
  // (notifications.js:35-44,156-161).
  test("error wins over everything", () => {
    expect(dotColorFor(summary(2, 1, 3))).toBe("#f7768e");
  });
  test("needs_you wins over working", () => {
    expect(dotColorFor(summary(2, 0, 3))).toBe("#e0af68");
  });
  test("working when it is the only active level", () => {
    expect(dotColorFor(summary(0, 0, 1))).toBe("#7aa2f7");
  });
  test("no dot when idle", () => {
    expect(dotColorFor(summary(0, 0, 0))).toBeNull();
  });
});

describe("buildFaviconDataURI", () => {
  // Pinned dark-theme SVG, byte-for-byte (the one sanctioned non-token color
  // site): base neutral circle, every '#' encoded as %23 (notifications.js:
  // 126-139). A recolor or an encoding change bites here.
  test("plain (no dot)", () => {
    expect(buildFaviconDataURI(null)).toBe(
      "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><circle cx='50' cy='50' r='40' fill='%237e8593'/></svg>",
    );
  });
  test("with an error dot", () => {
    expect(buildFaviconDataURI("#f7768e")).toBe(
      "data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 100 100'><circle cx='50' cy='50' r='40' fill='%237e8593'/><circle cx='78' cy='78' r='18' fill='%23f7768e' stroke='%231a1b26' stroke-width='4'/></svg>",
    );
  });
});

describe("applyFavicon", () => {
  test("creates a single link[rel=icon] when none exists", () => {
    applyFavicon(true, summary(1, 0, 0));
    expect(document.querySelectorAll("link[rel='icon']").length).toBe(1);
  });

  test("reuses the existing link on re-apply", () => {
    applyFavicon(true, summary(1, 0, 0));
    applyFavicon(true, summary(0, 0, 1));
    expect(document.querySelectorAll("link[rel='icon']").length).toBe(1);
  });

  test("pref ON draws the highest-priority dot", () => {
    applyFavicon(true, summary(2, 1, 0));
    expect(faviconHref()).toBe(buildFaviconDataURI("#f7768e"));
  });

  test("pref ON with no active attention draws no dot", () => {
    applyFavicon(true, summary(0, 0, 0));
    expect(faviconHref()).toBe(buildFaviconDataURI(null));
  });

  // THE all-OFF trap (schedule-W6 #4): favicon pref OFF must draw the plain
  // favicon with NO dot, even when attention is high. A mutation that
  // resurrected the legacy's favicon-default-TRUE would draw the amber/red
  // dot here and fail.
  test("pref OFF draws the plain favicon even with high attention", () => {
    applyFavicon(false, summary(9, 9, 9));
    expect(faviconHref()).toBe(buildFaviconDataURI(null));
  });

  test("null summary draws no dot", () => {
    applyFavicon(true, null);
    expect(faviconHref()).toBe(buildFaviconDataURI(null));
  });
});
