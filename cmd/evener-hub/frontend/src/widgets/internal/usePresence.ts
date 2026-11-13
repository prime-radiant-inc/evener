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
    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      setMounted(false);
      onExitedRef.current?.();
    };
    const element = layer.current;
    const running = element ? runningExit(element) : [];
    if (element === null || running.length === 0) {
      finish();
      return;
    }
    const timer = setTimeout(finish, motionDurationMs(exitToken, element) + FALLBACK_SLACK_MS);
    // A cancelled animation rejects `finished`; the layer is leaving either way.
    Promise.all(running.map((animation) => animation.finished)).then(finish, finish);
    return () => {
      settled = true;
      clearTimeout(timer);
    };
  }, [open, mounted, layer, exitToken]);

  return { mounted: open || mounted, phase: open ? "open" : "closing" };
}
