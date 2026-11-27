import styles from "../../styles/actionSymbol.module.css";

// Send and Start share one upright submit arrow.
export function SendIcon() {
  return (
    <svg viewBox="0 0 14 14" className={styles.symbol} aria-hidden="true">
      <path
        d="M7 12.2V1.8 M2.7 6.1 7 1.8l4.3 4.3"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.9"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
