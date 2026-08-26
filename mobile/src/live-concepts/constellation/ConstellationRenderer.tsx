import type { LiveConceptRendererProps, LiveConceptState } from "../contract";
import { ConstellationShell } from "./ConstellationShell";
import { ConversationView } from "./ConversationView";
import { SessionsView } from "./SessionsView";
import { WorkView } from "./WorkView";

export function ConstellationRenderer({
  state,
  dispatch,
}: LiveConceptRendererProps) {
  const surface = state.surface;
  return (
    <ConstellationShell state={state} dispatch={dispatch}>
      {surface === "sessions" ? (
        <SessionsView state={state} dispatch={dispatch} />
      ) : surface === "conversation" ? (
        <ConversationView state={state} dispatch={dispatch} />
      ) : surface === "work" ? (
        <WorkView state={state} dispatch={dispatch} />
      ) : (
        <SessionsView state={state} dispatch={dispatch} />
      )}
    </ConstellationShell>
  );
}

export type ConstellationRendererState = LiveConceptState;
