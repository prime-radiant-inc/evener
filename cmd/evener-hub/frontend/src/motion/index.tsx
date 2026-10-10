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
  domAnimation,
  LazyMotion,
  MotionConfig,
  m,
  type Transition,
  useReducedMotion,
} from "motion/react";
import type { ReactNode } from "react";

export { AnimatePresence, m, useReducedMotion };

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

function parseEase(raw: string): Transition["ease"] {
  const named = CSS_EASE[raw];
  if (named) return named;
  const bezier = /^cubic-bezier\(\s*(-?[\d.]+)\s*,\s*(-?[\d.]+)\s*,\s*(-?[\d.]+)\s*,\s*(-?[\d.]+)\s*\)$/.exec(raw);
  if (bezier) {
    return [Number(bezier[1]), Number(bezier[2]), Number(bezier[3]), Number(bezier[4])];
  }
  return "easeOut";
}

// One parsed Transition per distinct token pair. Callers treat the result as
// read-only: the memo hands the same object to every caller with the same
// tokens, so a token change re-reads (motion.test.tsx pins both halves).
const transitionCache = new Map<string, Transition>();

export function spatialTransition(): Transition {
  const style = getComputedStyle(document.documentElement);
  const rawDuration = style.getPropertyValue("--motion-duration-spatial").trim();
  const rawEase = style.getPropertyValue("--motion-easing-standard").trim();
  const key = `${rawDuration}|${rawEase}`;
  const cached = transitionCache.get(key);
  if (cached) return cached;
  const ms = rawDuration.endsWith("ms") ? Number.parseFloat(rawDuration) : Number.NaN;
  const transition: Transition = { duration: Number.isFinite(ms) ? ms / 1000 : 0.24, ease: parseEase(rawEase) };
  transitionCache.set(key, transition);
  return transition;
}
