import type { ReactNode } from "react";
import type {
  LiveConceptIntent,
  LiveConceptRendererProps,
  LiveConceptState,
} from "../contract";
import { Icon } from "../shared/Icon";
import { conceptRootAxLabel } from "../smoke-semantics";

export interface ConstellationShellProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
  children: ReactNode;
}

const TITLES: Readonly<Record<LiveConceptState["surface"], string>> = {
  sessions: "Orbit",
  conversation: "Conversation",
  work: "Work constellation",
};

const SUBTITLES: Readonly<Record<LiveConceptState["surface"], string>> = {
  sessions: "Attention and active work, connected without clutter.",
  conversation: "Live thread evidence and steering.",
  work: "Tasks, subagents, and jobs in their canonical hierarchy.",
};

export function ConstellationShell({
  state,
  dispatch,
  children,
}: ConstellationShellProps) {
  const surface = state.surface;
  const title = TITLES[surface];
  const subtitle = SUBTITLES[surface];
  const pushed = surface === "conversation" || surface === "work";
  const reducedMotion =
    state.reducedMotion ||
    document.documentElement.dataset.reducedMotion === "true";

  return (
    <section
      className="concept-constellation"
      data-concept-root
      data-testid="concept-root"
      data-platform={state.platform}
      data-appearance={state.appearance}
      data-text-scale={state.textScale}
      data-reduced-motion={String(state.reducedMotion)}
      data-motion={reducedMotion ? "reduced" : "full"}
      data-surface={surface}
      data-thread-key={state.conversation?.threadKey}
      aria-label={conceptRootAxLabel(state)}
    >
      <main className="co-scroll" data-route={surface}>
        <header className="co-topbar">
          <div className="co-topbar__leading">
            {pushed ? (
              <button
                className="co-icon-action"
                type="button"
                aria-label="Back"
                onClick={() =>
                  dispatch({
                    type: surface === "work" ? "closeWork" : "goBack",
                  })
                }
              >
                <Icon name="back" decorative />
              </button>
            ) : null}
            <div className="co-title-block">
              <p className="co-title-block__kicker">Living System</p>
              <h1>{title}</h1>
              <p>{subtitle}</p>
            </div>
          </div>
          <div className="co-topbar__actions">
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
        <div className="co-route-content">{children}</div>
      </main>
    </section>
  );
}

export type { LiveConceptRendererProps };
