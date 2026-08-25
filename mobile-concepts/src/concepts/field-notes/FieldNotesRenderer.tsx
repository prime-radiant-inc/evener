import type { ReactNode } from "react";
import type { ConceptRendererProps } from "../contract";
import { ScreenState } from "../shared/ScreenState";
import { ConversationView } from "./ConversationView";
import { FieldNotesShell } from "./FieldNotesShell";
import { NewSessionView } from "./NewSessionView";
import { SearchView } from "./SearchView";
import { SessionsView } from "./SessionsView";
import { SettingsView } from "./SettingsView";
import { VoiceView } from "./VoiceView";
import { WorkView } from "./WorkView";

function assertNever(value: never): never {
  throw new Error(`Unhandled Field Notes route: ${JSON.stringify(value)}`);
}

export function FieldNotesRenderer(props: ConceptRendererProps) {
  const { state, dispatch, primitives } = props;
  const route = state.route;
  let content: ReactNode;
  let routeName: string;
  let title: string;
  let subtitle: string | undefined;
  let pushed: "back" | "close" | undefined;

  switch (route.kind) {
    case "gallery":
      routeName = "gallery";
      title = "Field Notes";
      subtitle = "Choose this concept from the gallery to begin.";
      content = (
        <ScreenState
          state={{
            kind: "empty",
            title: "No concept selected",
            detail: "Return to the concept gallery and select Field Notes.",
          }}
        />
      );
      break;
    case "root": {
      const tab = route.tab;
      routeName = tab;
      switch (tab) {
        case "sessions":
          title = "Workbooks";
          subtitle = "Monday, 24 August · Live annotated records.";
          content = <SessionsView state={state} dispatch={dispatch} />;
          break;
        case "search":
          title = "Find passages";
          subtitle = "Search sessions, notes, tools, and projects.";
          content = <SearchView state={state} dispatch={dispatch} />;
          break;
        case "new":
          title = "New workbook";
          subtitle = "Begin with a project and an opening instruction.";
          content = <NewSessionView state={state} dispatch={dispatch} />;
          break;
        case "settings":
          title = "Desk settings";
          subtitle = "Desks, reading, voice, and fictional Hubs.";
          content = <SettingsView state={state} dispatch={dispatch} />;
          break;
        default:
          return assertNever(tab);
      }
      break;
    }
    case "conversation": {
      routeName = "conversation";
      const session = state.projection.fixture.sessions.find(
        ({ id }) => id === route.sessionId,
      );
      title = session?.title ?? "Conversation";
      subtitle = session?.project;
      pushed = "back";
      content = (
        <ConversationView
          state={state}
          sessionId={route.sessionId}
          dispatch={dispatch}
        />
      );
      break;
    }
    case "work": {
      routeName = "work";
      const session = state.projection.fixture.sessions.find(
        ({ id }) => id === route.sessionId,
      );
      title = "Work ledger";
      subtitle = session?.title;
      pushed = "back";
      content = (
        <WorkView
          state={state}
          sessionId={route.sessionId}
          primitives={primitives}
          dispatch={dispatch}
        />
      );
      break;
    }
    case "voice": {
      routeName = "voice";
      const session = state.projection.fixture.sessions.find(
        ({ id }) => id === route.sessionId,
      );
      title = "Voice";
      subtitle = session?.title;
      pushed = "close";
      content = (
        <VoiceView
          state={state}
          sessionId={route.sessionId}
          dispatch={dispatch}
        />
      );
      break;
    }
    default:
      return assertNever(route);
  }

  return (
    <FieldNotesShell
      state={state}
      primitives={primitives}
      dispatch={dispatch}
      onOpenConceptSwitcher={props.onOpenConceptSwitcher}
      onOpenLabControls={props.onOpenLabControls}
      routeName={routeName}
      title={title}
      subtitle={subtitle}
      pushed={pushed}
    >
      {content}
    </FieldNotesShell>
  );
}
