import { type ReactNode, useLayoutEffect, useReducer, useRef } from "react";
import { animate, spatialTransition, useReducedMotion } from "../../motion";
import { requireClass } from "../internal/requireClass";
import styles from "./promptcard.module.css";

export interface PromptCardProps {
  /** The field itself - a seamless autoGrow Textarea. The card draws the one
   * border and owns the :focus-within ring, so the field inside must draw
   * neither (see widgets/textarea's `seamless`). */
  field: ReactNode;
  /** Leading control-row slot, before the spacer - the attach button. */
  leading?: ReactNode;
  /** Trailing control-row slot, after the spacer - the primary verb and
   * whatever sits beside it. */
  actions?: ReactNode;
  /** Optional stable hook for a caller-owned control-row behavior test. */
  controlsTestId?: string;
  /** The empty, resting shape: the field and the control row share one line
   * instead of stacking, and the card animates the height between the two
   * shapes. Callers own what the row contains; the card only lays it out. */
  folded?: boolean;
  /** Use the folded one-line row even when the card is narrower than its
   * normal width threshold. The caller guarantees its control row fits; the
   * composer uses it for the phone's menu-only resting row. */
  foldedCompact?: boolean;
  /** Forwarded to the card element so a test can address it by a stable hook
   * (the composer's `composer-input-card`, spawn's `spawn-prompt-card`). */
  "data-testid"?: string;
  /** Hides the card AND takes it out of the interaction tree, for a surface
   * that temporarily owns the input instead (the composer while an ask_user
   * question is pending - see panes/session/composer's AskDock). */
  hidden?: boolean;
}

const CLASS = {
  root: requireClass(styles.root, "promptcard.module.css", "root"),
  card: requireClass(styles.card, "promptcard.module.css", "card"),
  field: requireClass(styles.field, "promptcard.module.css", "field"),
  controls: requireClass(styles.controls, "promptcard.module.css", "controls"),
  actions: requireClass(styles.actions, "promptcard.module.css", "actions"),
};

/**
 * The app's one prompt surface: a bordered card holding a text field over a
 * control row. Both places a person writes a prompt to an agent - the session
 * composer and the spawn form - render THIS, so "message the agent" and "start
 * an agent" are the same object rather than two things that resemble each
 * other.
 *
 * The card owns the border, the radius, the padding, the control-row layout,
 * and the focus ring (via :focus-within, since the seamless field inside draws
 * none of its own). Callers own the field's props and their own buttons; this
 * widget has no opinion about verbs and holds no content state - only the
 * geometry of the shape it can animate between.
 */
export function PromptCard({
  field,
  leading,
  actions,
  folded = false,
  foldedCompact = false,
  hidden,
  controlsTestId,
  "data-testid": testId,
}: PromptCardProps) {
  const rootRef = useRef<HTMLDivElement | null>(null);
  const cardRef = useRef<HTMLDivElement | null>(null);
  // The shape whose height the card is leaving. A fold swaps the layout, so
  // the natural height steps; capture the old height during the render that
  // changes the shape, while the DOM still holds it, for the effect below to
  // interpolate between two real measurements rather than guess.
  const priorShape = useRef<{ folded: boolean; height: number | null }>({ folded, height: null });
  const shapeChanged = useRef(false);
  const committedFolded = useRef(folded);
  const hiddenBefore = useRef(hidden);
  const [, releaseHold] = useReducer((release: number) => release + 1, 0);
  const reducedMotion = useReducedMotion();

  if (priorShape.current.folded !== folded) {
    priorShape.current = { folded, height: cardRef.current?.offsetHeight ?? null };
    shapeChanged.current = true;
  }
  // The commit that swaps the shape lays out the new one, and any layout read
  // before the effect below (a transcript list measuring its rows) would see
  // it. The root holds the outgoing height through that commit, so the layout
  // around the card keeps its size until the animation starts: a scroll port
  // above the composer that grew for one layout would clamp its offset and
  // keep the clamped offset when it shrank back, leaving its reader short of
  // the end. The hold follows the committed shape, so a render React discards
  // never holds a later one, and React both writes and releases it.
  const heldHeight =
    folded !== committedFolded.current && !hidden && !hiddenBefore.current && !reducedMotion
      ? priorShape.current.height
      : null;

  // biome-ignore lint/correctness/useExhaustiveDependencies: the fold flag is the shape-change trigger; the measured heights live in refs.
  useLayoutEffect(() => {
    const root = rootRef.current;
    const card = cardRef.current;
    const wasHidden = hiddenBefore.current;
    hiddenBefore.current = hidden;
    const changed = shapeChanged.current;
    shapeChanged.current = false;
    committedFolded.current = folded;
    if (!root || !card) return undefined;
    // The release re-renders before paint, after the card holds its own height.
    if (root.style.height !== "") releaseHold();
    // Height stays auto at rest: the field's own autogrow and any control
    // change reflow normally. Only the fold switch interpolates.
    card.style.removeProperty("height");
    card.removeAttribute("data-animating");
    const from = wasHidden ? null : priorShape.current.height;
    const animates = changed && !hidden && !reducedMotion && from !== null;
    // Measured while the root still holds the outgoing height.
    const to = animates ? card.offsetHeight : null;
    if (from === null || to === null || Math.abs(to - from) < 1) return undefined;
    card.style.height = `${from}px`;
    card.dataset.animating = "true";
    const animation = animate(card, { height: `${to}px` }, spatialTransition());
    animation.finished
      .then(() => {
        card.style.removeProperty("height");
        card.removeAttribute("data-animating");
      })
      .catch(() => {
        // A newer fold stopped this animation and owns the height now.
      });
    return () => {
      animation.stop();
      card.removeAttribute("data-animating");
    };
  }, [folded, hidden, reducedMotion]);

  return (
    <div ref={rootRef} className={CLASS.root} style={heldHeight === null ? undefined : { height: heldHeight }}>
      <div
        ref={cardRef}
        className={CLASS.card}
        data-testid={testId}
        data-folded={folded ? "true" : undefined}
        data-folded-compact={foldedCompact ? "true" : undefined}
        hidden={hidden}
        inert={hidden}
      >
        <div className={CLASS.field}>{field}</div>
        {(leading !== undefined || actions !== undefined) && (
          <div className={CLASS.controls} data-testid={controlsTestId}>
            {leading}
            <div className={CLASS.actions}>{actions}</div>
          </div>
        )}
      </div>
    </div>
  );
}
