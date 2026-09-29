// The one module that imports the motion library. Components animate through
// these re-exports, never through "motion/react" directly, so the library,
// its configuration, and the reduced-motion contract each live in exactly
// one place (import-boundary.test.ts enforces the boundary).
//
// reducedMotion="user": a prefers-reduced-motion system collapses every
// animation to its end state - the layout still changes, only the
// interpolation is gone. That is the design system's spatial-motion rule and
// it is not per-component opt-in.
import { AnimatePresence, MotionConfig, m, type Transition } from "motion/react";
import type { ReactNode } from "react";

export { AnimatePresence, m };

export function MotionProvider({ children }: { children: ReactNode }) {
  return <MotionConfig reducedMotion="user">{children}</MotionConfig>;
}

// The spatial transition for user-initiated geometry changes (the activity
// sidebar, the zoom columns). The token is the single source of truth for the
// duration; this reads it at call time so a theme or scale change never leaves
// a stale copy behind. Milliseconds in CSS, seconds in the library.
export function spatialTransition(): Transition {
  const raw = getComputedStyle(document.documentElement).getPropertyValue("--motion-duration-spatial").trim();
  const ms = raw.endsWith("ms") ? Number.parseFloat(raw) : Number.NaN;
  return { duration: Number.isFinite(ms) ? ms / 1000 : 0.24, ease: "easeOut" };
}
