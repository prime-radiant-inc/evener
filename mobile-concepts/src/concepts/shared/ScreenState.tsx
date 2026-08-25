import { useId } from "react";

export type ScreenStateValue =
  | { kind: "loading"; title: string; detail?: string }
  | { kind: "empty"; title: string; detail: string }
  | { kind: "offline"; title: string; detail: string }
  | { kind: "error"; title: string; detail: string };

export interface ScreenStateProps {
  state: ScreenStateValue;
  retryAction?: () => void;
}

export function ScreenState({ state, retryAction }: ScreenStateProps) {
  const headingId = `${useId()}-screen-state-title`;
  const semantics =
    state.kind === "loading"
      ? { role: "status" as const, "aria-live": "polite" as const }
      : state.kind === "error"
        ? { role: "alert" as const, "aria-live": "assertive" as const }
        : {
            role: "region" as const,
            "aria-live": undefined,
            "aria-labelledby": headingId,
          };

  return (
    <section {...semantics} data-screen-state={state.kind}>
      <h2 id={headingId}>{state.title}</h2>
      {state.detail ? <p>{state.detail}</p> : null}
      {retryAction ? (
        <button type="button" onClick={retryAction}>
          Retry
        </button>
      ) : null}
    </section>
  );
}
