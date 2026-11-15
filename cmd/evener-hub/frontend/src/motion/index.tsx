// The one module that imports the motion library. Components animate through
// these re-exports, never through "motion/react" (or the legacy
// "framer-motion" specifier) directly, so the library, its configuration, and
// the reduced-motion contract each live in exactly one place
// (import-boundary.test.ts enforces the boundary).
//
// reducedMotion="user": a prefers-reduced-motion system collapses every
// animation to its end state - the layout still changes, only the
// interpolation is gone. LazyMotion + domAnimation arms the `m` component's
// animations with the SMALL feature pack (initial/animate/exit transitions -
// everything the spatial surfaces use): `m` without it mounts inert and never
// interpolates (pinned by motion.test.tsx's interpolation test).
import {
  AnimatePresence,
  animate,
  domAnimation,
  LazyMotion,
  MotionConfig,
  m,
  type Transition,
  useReducedMotion,
} from "motion/react";
import type { ReactNode } from "react";

export { AnimatePresence, animate, m, useReducedMotion };

export function MotionProvider({ children }: { children: ReactNode }) {
  return (
    <MotionConfig reducedMotion="user">
      <LazyMotion features={domAnimation}>{children}</LazyMotion>
    </MotionConfig>
  );
}

// The spatial transition for user-initiated geometry changes (the activity
// sidebar, the zoom columns). The tokens are the single source of truth for
// duration and easing; this reads them at call time so a theme or scale change
// never leaves a stale copy behind. Milliseconds in CSS, seconds in the
// library; CSS easing spellings (ease-out) map to the library's (easeOut).
const CSS_EASE: Record<string, Transition["ease"]> = {
  linear: "linear",
  "ease-in": "easeIn",
  "ease-out": "easeOut",
  "ease-in-out": "easeInOut",
};

function parseEase(raw: string, fallback: Transition["ease"]): Transition["ease"] {
  const named = CSS_EASE[raw];
  if (named) return named;
  const bezier = /^cubic-bezier\(\s*(-?[\d.]+)\s*,\s*(-?[\d.]+)\s*,\s*(-?[\d.]+)\s*,\s*(-?[\d.]+)\s*\)$/.exec(raw);
  if (bezier) {
    return [Number(bezier[1]), Number(bezier[2]), Number(bezier[3]), Number(bezier[4])];
  }
  return fallback;
}

// One parsed Transition per distinct token pair. Callers treat the result as
// read-only: the memo hands the same object to every caller with the same
// tokens, so a token change re-reads (motion.test.tsx pins both halves).
const transitionCache = new Map<string, Transition>();

// The fallbacks are the values tokens.css ships, for a document without the
// stylesheet.
function tokenTransition(
  durationToken: string,
  easingToken: string,
  fallbackSeconds: number,
  fallbackEase: Transition["ease"],
): Transition {
  const style = getComputedStyle(document.documentElement);
  const rawDuration = style.getPropertyValue(durationToken).trim();
  const rawEase = style.getPropertyValue(easingToken).trim();
  const key = `${durationToken}|${rawDuration}|${rawEase}`;
  const cached = transitionCache.get(key);
  if (cached) return cached;
  const ms = rawDuration.endsWith("ms") ? Number.parseFloat(rawDuration) : Number.NaN;
  const transition: Transition = {
    duration: Number.isFinite(ms) ? ms / 1000 : fallbackSeconds,
    ease: parseEase(rawEase, fallbackEase),
  };
  transitionCache.set(key, transition);
  return transition;
}

export function spatialTransition(): Transition {
  return tokenTransition("--motion-duration-spatial", "--motion-easing-standard", 0.24, "easeOut");
}

// The Slide recipe's exit: an edge-attached surface leaving on the spatial
// exit duration and the exit easing (docs/web-ui/motion.md, recipe 4).
export function spatialExitTransition(): Transition {
  return tokenTransition("--motion-duration-spatial-exit", "--motion-easing-exit", 0.18, "easeIn");
}
