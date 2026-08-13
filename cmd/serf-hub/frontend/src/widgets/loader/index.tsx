// Adapted from Beautiful UI's Loading State (beautifului.dev, MIT © 2026 Shane Levine) — see LICENSES/beautiful-ui.txt.
import { requireClass } from "../internal/requireClass";
import styles from "./loader.module.css";

export interface LoaderProps {
  /** Visible + accessible label. Defaults to "Loading". */
  label?: string;
  /** Epoch-ms instant the wait began. Paired with `now` to render an mm:ss
   * elapsed readout; omit either one and no readout renders at all. */
  startedAt?: number;
  /** Epoch-ms "current" instant elapsed is measured against. Never read
   * from Date.now() internally - Loader is a pure render of its props,
   * mirroring Cadence (see src/widgets/cadence/index.tsx): the caller (which
   * does own a clock) controls what "now" means, and a component with no
   * internal timer can't drift or fake liveness between renders. */
  now?: number;
}

const CELL_COUNT = 9;

const BASE_CLASS = {
  loader: requireClass(styles.loader, "loader.module.css", "loader"),
  grid: requireClass(styles.grid, "loader.module.css", "grid"),
  cell: requireClass(styles.cell, "loader.module.css", "cell"),
  label: requireClass(styles.label, "loader.module.css", "label"),
  elapsed: requireClass(styles.elapsed, "loader.module.css", "elapsed"),
};

function formatElapsed(startedAt: number, now: number): string {
  const clampedMs = Math.max(0, now - startedAt); // clock-skew guard, same stance as Cadence's ticksFor
  const totalSeconds = Math.floor(clampedMs / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

/**
 * A small pixel-grid glyph plus an optional mm:ss elapsed readout, for a
 * genuinely indeterminate, user-initiated wait (a network fetch, a spawn) -
 * NOT a stand-in for agent liveness. Cadence already owns that signal with
 * its own honest-liveness stance (pure render, decaying trace, no faked
 * motion); reusing Loader's shimmer for "the agent is working" would just
 * be a second, competing liveness indicator.
 *
 * The grid's animation is the one deliberate exception to the app's ban on
 * idle animation (Direction, Global Constraints): it only exists at all
 * inside `@media (prefers-reduced-motion: no-preference)` (see
 * loader.module.css), runs at a slow, ~1.4s period, and freezes to a static
 * dim grid under reduced motion - the elapsed readout keeps ticking either
 * way, since it's driven by props, not by the animation.
 */
export function Loader({ label, startedAt, now }: LoaderProps) {
  const accessibleLabel = label ?? "Loading";
  const showElapsed = startedAt !== undefined && now !== undefined;

  return (
    <span className={BASE_CLASS.loader} role="status" aria-label={accessibleLabel}>
      <span data-testid="loader-grid" aria-hidden="true" className={BASE_CLASS.grid}>
        {Array.from({ length: CELL_COUNT }, (_, i) => (
          // Interchangeable, content-free, decorative cells - same
          // index-key rationale as Skeleton's placeholder lines.
          // biome-ignore lint/suspicious/noArrayIndexKey: interchangeable decorative cells, see above
          <span key={i} data-testid="loader-cell" className={BASE_CLASS.cell} />
        ))}
      </span>
      {label !== undefined && (
        <span data-testid="loader-label" className={BASE_CLASS.label}>
          {label}
        </span>
      )}
      {showElapsed && (
        <span data-testid="loader-elapsed" className={BASE_CLASS.elapsed}>
          {formatElapsed(startedAt, now)}
        </span>
      )}
    </span>
  );
}
