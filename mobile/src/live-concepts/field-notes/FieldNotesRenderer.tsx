import type { ReactNode } from "react";
import type { LiveConceptRendererProps, LiveConceptState } from "../contract";
import { ConversationView } from "./ConversationView";
import { FieldNotesShell } from "./FieldNotesShell";
import { SessionsView } from "./SessionsView";
import { WorkView } from "./WorkView";

// Live Field Notes renderer. Switches on the live surface (sessions,
// conversation, work) and renders the matching milestone view inside the
// shell. No prototype route/fixture/synthetic infrastructure.

function assertNever(value: never): never {
  throw new Error(`Unhandled Field Notes surface: ${JSON.stringify(value)}`);
}

export function FieldNotesRenderer(props: LiveConceptRendererProps) {
  const { state, dispatch } = props;
  const surface = state.surface;
  let content: ReactNode;
  let title: string;
  let subtitle: string | undefined;
  let pushed: "back" | "close" | undefined;

  switch (surface) {
    case "sessions":
      title = "Workbooks";
      subtitle = "Live annotated records.";
      content = <SessionsView state={state} dispatch={dispatch} />;
      break;
    case "conversation": {
      const conversation = state.conversation;
      title = conversation?.title.text ?? "Conversation";
      subtitle = conversation?.project.text;
      pushed = "back";
      content = <ConversationView state={state} dispatch={dispatch} />;
      break;
    }
    case "work": {
      const conversation = state.conversation;
      title = "Work ledger";
      subtitle = conversation?.title.text;
      pushed = "close";
      content = <WorkView state={state} dispatch={dispatch} />;
      break;
    }
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

// Convenience export so tests can build surface-aware expectations without
// reaching into the shell internals.
export type { LiveConceptState };
