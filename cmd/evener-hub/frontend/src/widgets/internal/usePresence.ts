import { type RefObject, useLayoutEffect, useRef, useState } from "react";
import { type MotionDurationToken, motionDurationMs } from "../../motion/tokens";

/** What a layer writes as its recipe elements' `data-motion` attribute. */
export type MotionPhase = "open" | "closing";

export interface Presence {
  /** Render the layer: true while open and for as long as it is leaving. */
  mounted: boolean;
  /** "closing" from the moment `open` turns false until the layer unmounts. */
  phase: MotionPhase;
}

export interface PresenceOptions {
  /** The exit token that bounds the fallback removal: the Float/Fade exit by
   * default, the spatial exit for an edge-attached surface. */
  exit?: Extract<MotionDurationToken, "--motion-duration-exit" | "--motion-duration-spatial-exit">;
  /** Called once when a closing layer has been removed. */
  onExited?: () => void;
}

// A frame or two past the exit token: the animation starts on the style
// recalculation after the close commits, so its end can land just after the
// token's own length.
const FALLBACK_SLACK_MS = 50;

// The exit animations a closing layer is waiting on: those on the layer root
// and on every recipe element inside it (each carries data-motion), and only
// the ones that end. A looping animation inside the layer (a Loader, a working
// StatusDot) never finishes and is not part of the exit.
function runningExit(layer: HTMLElement): Animation[] {
  if (typeof layer.getAnimations !== "function") return [];
  const elements: Element[] = [layer, ...layer.querySelectorAll("[data-motion]")];
  const running: Animation[] = [];
  for (const element of elements) {
    for (const animation of element.getAnimations()) {
      if (animation.playState === "finished") continue;
      const end = animation.effect?.getComputedTiming().endTime;
      if (typeof end === "number" && Number.isFinite(end)) running.push(animation);
    }
  }
  return running;
}

// How long an animation still has to run, in milliseconds: none once it has
// finished or been cancelled, its whole length while it waits for its first
// frame.
function remainingMs(animation: Animation): number {
  if (animation.playState === "finished" || animation.playState === "idle") return 0;
  const end = animation.effect?.getComputedTiming().endTime;
  if (typeof end !== "number" || !Number.isFinite(end)) return 0;
  const current = animation.currentTime;
  const elapsed = typeof current === "number" ? current : 0;
  return Math.max(0, end - elapsed);
}

/**
 * Calls `done` once, when the motion now running on `element` is over: at once
 * (synchronously) when no finite animation runs on it or on the recipe
 * elements inside it, otherwise when every one of them has finished or been
 * cancelled, and at the latest a moment past `token`'s duration if an
 * animation's end never arrives (twice that when the main thread kept the
 * motion from starting). Returns a function that cancels the wait.
 * The one way something stays on screen only for the length of its motion:
 * usePresence waits on a leaving layer with it, and the phone stack waits on a
 * pane transition.
 */
export function afterMotion(element: HTMLElement, token: MotionDurationToken, done: () => void): () => void {
  let settled = false;
  const finish = () => {
    if (settled) return;
    settled = true;
    done();
  };
  const running = runningExit(element);
  if (running.length === 0) {
    finish();
    return () => {};
  }
  // The bound counts from the first frame the motion can paint. Work that holds
  // the main thread right after the change (a heavy pane mounting) delays that
  // frame, so a motion still short of its end when the bound passes gets its
  // remaining time once more before it is cut.
  let extended = false;
  const fallback = () => {
    const remaining = extended ? 0 : Math.max(0, ...running.map(remainingMs));
    if (remaining === 0) {
      finish();
      return;
    }
    extended = true;
    timer = setTimeout(fallback, remaining + FALLBACK_SLACK_MS);
  };
  let timer = setTimeout(fallback, motionDurationMs(token, element) + FALLBACK_SLACK_MS);
  // A cancelled animation rejects `finished`; the motion is over either way.
  Promise.all(running.map((animation) => animation.finished)).then(finish, finish);
  return () => {
    settled = true;
    clearTimeout(timer);
  };
}

/**
 * Keeps a layer mounted while it leaves, so what arrived with motion leaves
 * with motion (docs/web-ui/motion.md, "Exits need presence").
 *
 * While `open` is true the layer is mounted in phase "open". When `open`
 * turns false the layer stays mounted in phase "closing" until every exit
 * animation on its recipe elements has finished, then unmounts. When no exit
 * animation runs - reduced motion, whose recipes declare none, or an
 * environment without Web Animations - it unmounts in the same commit, before
 * paint. A timeout bounded by the exit token removes it if the animation's end
 * never arrives. Reopening during the exit cancels the removal and returns the
 * layer to "open".
 *
 * The caller keys everything that must stop at the close off `open`, not off
 * `mounted`: outside-press and scroll dismissal, focus trapping and restoring,
 * modal semantics. A closing layer renders `inert`, writes
 * data-motion="closing" on its recipe elements (whose recipes then ignore the
 * pointer), and has already handed focus back.
 */
export function usePresence(
  open: boolean,
  layer: RefObject<HTMLElement | null>,
  options: PresenceOptions = {},
): Presence {
  const [mounted, setMounted] = useState(open);
  if (open && !mounted) setMounted(true);
  const exitToken = options.exit ?? "--motion-duration-exit";
  const onExitedRef = useRef(options.onExited);
  onExitedRef.current = options.onExited;

  useLayoutEffect(() => {
    if (open || !mounted) return;
    const finish = () => {
      setMounted(false);
      onExitedRef.current?.();
    };
    const element = layer.current;
    if (element === null) {
      finish();
      return;
    }
    return afterMotion(element, exitToken, finish);
  }, [open, mounted, layer, exitToken]);

  return { mounted: open || mounted, phase: open ? "open" : "closing" };
}
