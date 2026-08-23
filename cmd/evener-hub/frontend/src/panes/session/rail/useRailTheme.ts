// useRailTheme — resolves design tokens to RGB strings for canvas rendering.
//
// The token-contract test (src/styles/token-contract.test.ts) fails CI on any
// color literal outside tokens.css, so the rail's canvas renderer cannot use
// hex/rgb literals directly. Instead it reads resolved values via
// getComputedStyle on the document element and caches them, re-resolving when
// the theme changes (observing the data-theme attribute via MutationObserver).
//
// The mapping (per the spec):
//   IN strata → --accent, OUT strata → --alive, prompt anchors → --attention,
//   errors/cliffs → --danger, Σ burn → --ink-hi, idle hatch → --ink-low over
//   --surface-inset, thumb → --surface-2 + --accent edge.

import { useEffect, useState } from "react";

/** Resolved RGB values for the rail's canvas renderer. */
export interface RailTheme {
  accent: string; // IN strata
  alive: string; // OUT strata, jobs ok
  attention: string; // prompt anchors
  danger: string; // errors, result cliffs, jobs failed
  inkHi: string; // Σ burn line
  inkMid: string; // secondary text
  inkLow: string; // idle hatch, placeholders
  surfaceCanvas: string; // rail background
  surfaceInset: string; // strata band background
  surface2: string; // thumb background
  edge: string; // hairline borders
  hover1: string; // hover wash
  fontMono: string; // mono font family
}

/** Parse a CSS color value to an rgba() string usable by canvas fillStyle. */
function resolveToken(name: string): string {
  if (typeof document === "undefined") return "";
  const v = getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  return v;
}

/** Resolve all rail theme tokens from CSS custom properties. */
function resolveRailTheme(): RailTheme {
  return {
    accent: resolveToken("--accent"),
    alive: resolveToken("--alive"),
    attention: resolveToken("--attention"),
    danger: resolveToken("--danger"),
    inkHi: resolveToken("--ink-hi"),
    inkMid: resolveToken("--ink-mid"),
    inkLow: resolveToken("--ink-low"),
    surfaceCanvas: resolveToken("--surface-canvas"),
    surfaceInset: resolveToken("--surface-inset"),
    surface2: resolveToken("--surface-2"),
    edge: resolveToken("--edge"),
    hover1: resolveToken("--hover-1"),
    fontMono: resolveToken("--font-mono"),
  };
}

/**
 * React hook that resolves rail theme tokens and re-resolves when the
 * data-theme attribute changes. Returns the resolved RailTheme.
 */
export function useRailTheme(): RailTheme {
  const [theme, setTheme] = useState<RailTheme>(() => resolveRailTheme());

  useEffect(() => {
    // Re-resolve on data-theme changes.
    const observer = new MutationObserver(() => {
      setTheme(resolveRailTheme());
    });
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["data-theme"],
    });
    return () => observer.disconnect();
  }, []);

  return theme;
}

/** Convert a hex color (#RGB or #RRGGBB) to "r, g, b" for rgba() templates. */
export function hexToRgb(hex: string): string {
  const h = hex.replace("#", "");
  if (h.length === 3) {
    const r = h.charAt(0);
    const g = h.charAt(1);
    const b = h.charAt(2);
    return `${parseInt(r + r, 16)}, ${parseInt(g + g, 16)}, ${parseInt(b + b, 16)}`;
  }
  if (h.length === 6) {
    return `${parseInt(h.slice(0, 2), 16)}, ${parseInt(h.slice(2, 4), 16)}, ${parseInt(h.slice(4, 6), 16)}`;
  }
  return "0, 0, 0";
}

/** Create an rgba() string from a resolved color and alpha. */
export function withAlpha(color: string, alpha: number): string {
  // If already rgba/rgb, replace alpha; if hex, convert.
  if (color.startsWith("rgba")) {
    return color.replace(/[\d.]+\)$/, `${alpha})`);
  }
  if (color.startsWith("rgb(")) {
    return color.replace("rgb(", "rgba(").replace(")", `, ${alpha})`);
  }
  return `rgba(${hexToRgb(color)}, ${alpha})`;
}
