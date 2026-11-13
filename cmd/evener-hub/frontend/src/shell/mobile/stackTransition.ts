// The phone stack's pane-to-pane motion (docs/web-ui/motion.md, recipe 4
// Slide): going deeper, the new pane slides in from the right over the old
// one; going back, the old pane slides out to the right and uncovers the new
// one.
//
// The old pane is NOT kept alive for this. StackHost unmounts it in the same
// commit that mounts the new one, exactly as without motion, so its
// subscriptions, title, back target and handoff claims end at once and no
// session is ever read by two live panes. What stays on screen for the length
// of the slide is the old pane's last frame: the DOM subtree React has just
// detached, put back inert and pointer-transparent in a leaving layer, with
// its scroll offsets restored. It is removed when the slide ends (or at once
// when no slide runs: reduced motion, or a document without Web Animations).
import { afterMotion } from "../../widgets/internal/usePresence";

/** A push goes one pane deeper; a pop comes back out. */
export type StackTransitionKind = "push" | "pop";

export interface StackTransitionClasses {
  /** On the incoming pane slot while it slides in (composes the Slide entrance). */
  enter: string;
  /** On the leaving layer under a push (the old pane stays put beneath). */
  leavingUnder: string;
  /** On the leaving layer over a pop (composes the Slide recipe; leaves under data-motion="closing"). */
  leavingOver: string;
}

/** Last-known scroll offsets of the elements inside the shown pane. A detached
 * subtree loses its scroll positions, so they are recorded as they change and
 * written back once the old pane is in its leaving layer. */
export type ScrollOffsets = Map<Element, readonly [top: number, left: number]>;

const classList = (names: string) => names.split(/\s+/).filter(Boolean);

/**
 * Runs one transition and returns a function that ends it at once. `outgoing`
 * is the old pane's slot, already detached by React; `incoming` is the new
 * pane's live slot. `onEnd` runs once when the transition is over, however it
 * ends. The stack body carries data-stack-transition while one runs, the
 * condition a browser guard waits on before it measures a pane.
 */
export function startStackTransition(
  body: HTMLElement,
  kind: StackTransitionKind,
  outgoing: HTMLElement,
  incoming: HTMLElement,
  offsets: ScrollOffsets,
  classes: StackTransitionClasses,
  onEnd: () => void,
): () => void {
  const enter = classList(classes.enter);
  // A pane interrupted mid-entrance leaves from where it rests, not from a
  // restarted entrance.
  outgoing.classList.remove(...enter);
  const layer = body.ownerDocument.createElement("div");
  layer.classList.add(...classList(kind === "pop" ? classes.leavingOver : classes.leavingUnder));
  layer.setAttribute("aria-hidden", "true");
  layer.setAttribute("inert", "");
  if (kind === "pop") layer.setAttribute("data-motion", "closing");
  layer.append(outgoing);
  if (kind === "pop") body.append(layer);
  else {
    body.prepend(layer);
    incoming.classList.add(...enter);
  }
  // Put back in the document, the old frame would replay every entrance it
  // holds (a revealed body fading in again); settle those where they ended.
  // A looping animation cannot finish and keeps running.
  if (typeof outgoing.getAnimations === "function") {
    for (const animation of outgoing.getAnimations({ subtree: true })) {
      const end = animation.effect?.getComputedTiming().endTime;
      if (typeof end === "number" && Number.isFinite(end)) animation.finish();
    }
  }
  for (const [element, [top, left]] of offsets) {
    if (!outgoing.contains(element)) continue;
    element.scrollTop = top;
    element.scrollLeft = left;
  }
  body.setAttribute("data-stack-transition", kind);

  let ended = false;
  let cancelWait = () => {};
  const end = () => {
    if (ended) return;
    ended = true;
    cancelWait();
    layer.remove();
    incoming.classList.remove(...enter);
    if (body.getAttribute("data-stack-transition") === kind) body.removeAttribute("data-stack-transition");
    onEnd();
  };
  cancelWait =
    kind === "pop"
      ? afterMotion(layer, "--motion-duration-spatial-exit", end)
      : afterMotion(incoming, "--motion-duration-spatial", end);
  return end;
}
