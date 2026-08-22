import type { JSX, ReactNode } from "react";

export interface EmptyProps {
  readonly title?: ReactNode;
  readonly hint?: ReactNode;
}

/** Honest empty state — never fake functionality. */
export function Empty({
  title = "Nothing here yet",
  hint,
}: EmptyProps): JSX.Element {
  return (
    <div className="evener-empty" role="status">
      <span>{title}</span>
      {hint ? <span>{hint}</span> : null}
    </div>
  );
}

export interface ErrorStateProps {
  readonly message: ReactNode;
  readonly retry?: () => void;
}

/** Honest error state with optional retry. Never echoes secrets. */
export function ErrorState({ message, retry }: ErrorStateProps): JSX.Element {
  return (
    <div className="evener-error" role="alert">
      <span>{message}</span>
      {retry ? (
        <button type="button" className="evener-button primary" onClick={retry}>
          Retry
        </button>
      ) : null}
    </div>
  );
}

export interface LoadingProps {
  readonly label?: ReactNode;
}

/** Honest loading state. */
export function Loading({ label = "Loading…" }: LoadingProps): JSX.Element {
  return (
    <div className="evener-loading" role="status" aria-live="polite">
      <span>{label}</span>
    </div>
  );
}
