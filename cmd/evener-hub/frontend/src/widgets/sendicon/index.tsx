import { isApplePlatform } from "../../platform";
import styles from "../../styles/appleSymbol.module.css";

// Send and Start share a glyph, including the upright submit arrow on Apple platforms.
export function SendIcon() {
  const apple = isApplePlatform();
  return (
    <svg
      viewBox="0 0 14 14"
      width="12"
      height="12"
      className={apple ? styles.symbol : undefined}
      data-apple-send={apple ? "" : undefined}
      aria-hidden="true"
    >
      <path
        d={apple ? "M7 12.2V1.8 M2.7 6.1 7 1.8l4.3 4.3" : "M12.8 1.2 8.8 12.8 6.4 7.6 1.2 5.2Z M12.8 1.2 6.4 7.6"}
        fill="none"
        stroke="currentColor"
        strokeWidth={apple ? 1.9 : 1.2}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
