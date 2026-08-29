import type { ReactNode } from "react";
import { conceptRootAxLabel } from "../accessibility-semantics";
import type { LiveConceptRendererProps } from "../contract";
import type { LiveConceptSurface } from "../model";
import { Icon } from "../shared/Icon";
import { ConversationView } from "./ConversationView";
import { SessionsView } from "./SessionsView";
import { WorkView } from "./WorkView";

function assertNever(value: never): never {
  throw new Error(`Unhandled surface: ${JSON.stringify(value)}`);
}

interface SurfaceMeta {
  route: string;
  title: string;
  subtitle?: string;
  pushed: boolean;
}

const surfaceMeta: Readonly<Record<LiveConceptSurface, SurfaceMeta>> = {
  sessions: {
    route: "sessions",
    title: "Sessions",
    subtitle: "Needs You first. Everything else stays quiet.",
    pushed: false,
  },
  conversation: {
    route: "conversation",
    title: "Conversation",
    pushed: true,
  },
  work: {
    route: "work",
    title: "Work",
    pushed: true,
  },
};

export function StillwaterRenderer({
  state,
  dispatch,
}: LiveConceptRendererProps) {
  const { surface, conversation } = state;
  const meta = surfaceMeta[surface];
  let content: ReactNode;
  let title = meta.title;
  let subtitle: string | undefined = meta.subtitle;

  switch (surface) {
    case "sessions":
      content = <SessionsView state={state} dispatch={dispatch} />;
      break;
    case "conversation": {
      if (conversation) {
        title = conversation.title.text;
        subtitle = conversation.project.text;
      }
      content = <ConversationView state={state} dispatch={dispatch} />;
      break;
    }
    case "work": {
      if (conversation) {
        subtitle = conversation.title.text;
      }
      content = <WorkView state={state} dispatch={dispatch} />;
      break;
    }
    default:
      return assertNever(surface);
  }

  return (
    <section
      className="concept-stillwater"
      data-concept-root
      data-platform={state.platform}
      data-appearance={state.appearance}
      data-reduced-motion={state.reducedMotion}
      data-surface={surface}
      data-text-scale={state.textScale}
      data-thread-key={state.conversation?.threadKey}
      aria-label={conceptRootAxLabel(state)}
    >
      <main
        className="sw-scroll"
        data-route={meta.route}
        data-live-concept-scroller="true"
      >
        <header className="sw-topbar">
          <div className="sw-topbar__leading">
            {meta.pushed ? (
              <button
                className="sw-icon-action"
                type="button"
                aria-label="Back"
                onClick={() => dispatch({ type: "goBack" })}
              >
                <Icon name="back" decorative />
              </button>
            ) : null}
            <div className="sw-title-block">
              <p className="sw-title-block__kicker">Quiet Instrument</p>
              <h1>{title}</h1>
              {subtitle ? <p>{subtitle}</p> : null}
            </div>
          </div>
          <div className="sw-topbar__actions">
            <button
              type="button"
              data-concept-switch-trigger="true"
              onClick={() => dispatch({ type: "openConceptSwitcher" })}
            >
              <Icon name="switch" decorative />
              <span>Switch concept</span>
            </button>
          </div>
        </header>
        <div className="sw-route-content">{content}</div>
      </main>
    </section>
  );
}
