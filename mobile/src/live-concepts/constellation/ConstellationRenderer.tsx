import type { ReactNode } from "react";
import type { LiveConceptRendererProps, LiveConceptState } from "../contract";
import { ConstellationShell } from "./ConstellationShell";
import { SessionsView } from "./SessionsView";
import { WorkView } from "./WorkView";

function assertNever(value: never): never {
  throw new Error(`Unhandled surface: ${JSON.stringify(value)}`);
}

export function ConstellationRenderer({
  state,
  dispatch,
}: LiveConceptRendererProps) {
  if (state.surface === "conversation") return null;
  let content: ReactNode;
  switch (state.surface) {
    case "sessions":
      content = <SessionsView state={state} dispatch={dispatch} />;
      break;
    case "work":
      content = <WorkView state={state} dispatch={dispatch} />;
      break;
    default:
      return assertNever(state.surface);
  }
  return (
    <ConstellationShell state={state} dispatch={dispatch}>
      {content}
    </ConstellationShell>
  );
}

export type ConstellationRendererState = LiveConceptState;
