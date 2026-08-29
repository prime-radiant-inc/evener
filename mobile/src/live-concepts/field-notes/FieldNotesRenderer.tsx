import type { ReactNode } from "react";
import type { LiveConceptRendererProps, LiveConceptState } from "../contract";
import { FieldNotesShell } from "./FieldNotesShell";
import { SessionsView } from "./SessionsView";
import { WorkView } from "./WorkView";

function assertNever(value: never): never {
  throw new Error(`Unhandled Field Notes surface: ${JSON.stringify(value)}`);
}

export function FieldNotesRenderer({
  state,
  dispatch,
}: LiveConceptRendererProps) {
  if (state.surface === "conversation") return null;

  const surface = state.surface;
  let content: ReactNode;
  let title: string;
  let subtitle: string | undefined;
  let pushed: "close" | undefined;

  switch (surface) {
    case "sessions":
      title = "Workbooks";
      subtitle = "Live annotated records.";
      content = <SessionsView state={state} dispatch={dispatch} />;
      break;
    case "work":
      title = "Work ledger";
      subtitle = state.conversation?.title.text;
      pushed = "close";
      content = <WorkView state={state} dispatch={dispatch} />;
      break;
    default:
      return assertNever(surface);
  }

  return (
    <FieldNotesShell
      state={state}
      dispatch={dispatch}
      surface={surface}
      title={title}
      subtitle={subtitle}
      pushed={pushed}
    >
      {content}
    </FieldNotesShell>
  );
}

export type { LiveConceptState };
