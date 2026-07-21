import { requireClass } from "../internal/requireClass";
import styles from "./skeleton.module.css";

export interface SkeletonProps {
  lines?: number;
}

const DEFAULT_LINES = 3;

const BASE_CLASS = {
  skeleton: requireClass(styles.skeleton, "skeleton.module.css", "skeleton"),
  line: requireClass(styles.line, "skeleton.module.css", "line"),
};

/** A static loading placeholder: `lines` neutral bars, no shimmer or pulse
 * (honest-liveness rule - see skeleton.module.css). Announces itself once
 * as "Loading" for assistive tech; the individual bars are decorative. */
export function Skeleton({ lines = DEFAULT_LINES }: SkeletonProps) {
  return (
    <div className={BASE_CLASS.skeleton} role="status" aria-label="Loading">
      {Array.from({ length: lines }, (_, i) => (
        <div key={i} data-testid="skeleton-line" aria-hidden="true" className={BASE_CLASS.line} />
      ))}
    </div>
  );
}
