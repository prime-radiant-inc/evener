import type { ReactNode } from "react";
import type { ConceptRendererProps } from "../contract";
import { ScreenState } from "../shared/ScreenState";
import { ConversationView } from "./ConversationView";
import { NewSessionView } from "./NewSessionView";
import { SearchView } from "./SearchView";
import { SessionsView } from "./SessionsView";
import { SettingsView } from "./SettingsView";
import { StillwaterShell } from "./StillwaterShell";
import { VoiceView } from "./VoiceView";
import { WorkView } from "./WorkView";

function assertNever(value: never): never {
  throw new Error(`Unhandled Stillwater route: ${JSON.stringify(value)}`);
}

export function StillwaterRenderer(props: ConceptRendererProps) {
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
      title = "Stillwater";
      subtitle = "Choose this concept from the gallery to begin.";
      content = (
        <ScreenState
          state={{
            kind: "empty",
            title: "No concept selected",
            detail: "Return to the concept gallery and select Stillwater.",
          }}
        />
      );
      break;
    case "root": {
      const tab = route.tab;
      routeName = tab;
      switch (tab) {
        case "sessions":
          title = "Sessions";
          subtitle = "Needs You first. Everything else stays quiet.";
          content = <SessionsView state={state} dispatch={dispatch} />;
          break;
        case "search":
          title = "Search";
          subtitle = "Find local context across the fixture.";
          content = <SearchView state={state} dispatch={dispatch} />;
          break;
        case "new":
          title = "New Session";
          subtitle = "A clear start with explicit outcomes.";
          content = <NewSessionView state={state} dispatch={dispatch} />;
          break;
        case "settings":
          title = "Settings";
          subtitle = "Display, voice, concepts, and fictional Hubs.";
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
      title = "Work";
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
    <StillwaterShell
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
    </StillwaterShell>
  );
}
