import type { CSSProperties, ReactNode } from "react";
import type { LiveConceptIntent, LiveConceptState } from "../contract";
import type { LiveConceptSurface } from "../model";
import { Icon } from "../shared/Icon";
import { conceptRootAxLabel } from "../smoke-semantics";

// Live adaptation of the Field Notes shell. Carries the live platform,
// appearance, text-scale, and reduced-motion state; renders the topbar with
// back/close actions (driven by live intents); and scopes content via the
// single scroll owner. No prototype root navigation, no Lab controls.

export interface FieldNotesShellProps {
  state: LiveConceptState;
  dispatch(intent: LiveConceptIntent): void;
  surface: LiveConceptSurface;
  title: string;
  subtitle?: string;
  pushed?: "back" | "close";
  children: ReactNode;
}

type FieldNotesEdgeStyles = CSSProperties & {
  "--fn-edge-top": string;
  "--fn-edge-right": string;
  "--fn-edge-bottom": string;
  "--fn-edge-left": string;
};

function edgeStyles(platform: "ios" | "android"): FieldNotesEdgeStyles {
  const source = (edge: "top" | "right" | "bottom" | "left") =>
    platform === "ios"
      ? `env(safe-area-inset-${edge}, 0px)`
      : `var(--safe-area-${edge}, 0px)`;
  return {
    "--fn-edge-top": source("top"),
    "--fn-edge-right": source("right"),
    "--fn-edge-bottom": source("bottom"),
    "--fn-edge-left": source("left"),
  };
}

export function FieldNotesShell({
  state,
  dispatch,
  surface,
  title,
  subtitle,
  pushed,
  children,
}: FieldNotesShellProps) {
  const { platform, appearance, textScale, reducedMotion } = state;
  const iconTarget = "44px";
  const topPadding =
    platform === "ios"
      ? "max(0.85rem, var(--fn-edge-top))"
      : "max(0.75rem, var(--fn-edge-top))";

  return (
    <section
      className="concept-field-notes"
      data-concept-root
      data-platform={platform}
      data-reading-role={
        platform === "android" ? "editorial-sans" : "editorial-serif"
      }
      data-appearance={appearance}
      data-text-scale={textScale}
      data-reduced-motion={reducedMotion}
      data-thread-key={state.conversation?.threadKey}
      data-motion={
        reducedMotion ||
        document.documentElement.dataset.reducedMotion === "true"
          ? "reduced"
          : "full"
      }
      aria-label={conceptRootAxLabel(state)}
      style={edgeStyles(platform)}
    >
      <main
        className="fn-scroll-owner"
        data-surface={surface}
        data-pushed-route={pushed ? "true" : undefined}
      >
        <header
          className="fn-topbar"
          style={{
            paddingTop: topPadding,
            paddingRight: "max(1rem, var(--fn-edge-right))",
            paddingLeft: "max(1rem, var(--fn-edge-left))",
          }}
        >
          <div className="fn-topbar__leading">
            {pushed ? (
              <button
                className="fn-icon-action"
                type="button"
                aria-label={pushed === "close" ? "Close" : "Back"}
                data-icon-target={iconTarget}
                style={{
                  width: iconTarget,
                  minWidth: iconTarget,
                  height: iconTarget,
                  minHeight: iconTarget,
                }}
                onClick={() =>
                  dispatch(
                    pushed === "close"
                      ? { type: "closeWork" }
                      : { type: "goBack" },
                  )
                }
              >
                <Icon name={pushed === "close" ? "close" : "back"} decorative />
              </button>
            ) : null}
            <div className="fn-title-block">
              <p className="fn-title-block__kicker">
                Field Notes · Live record
              </p>
              <h1>{title}</h1>
              {subtitle ? <p>{subtitle}</p> : null}
            </div>
          </div>
          <div className="fn-topbar__actions">
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
        <div
          className="fn-route-content"
          style={{
            paddingRight: "max(1rem, var(--fn-edge-right))",
            paddingBottom: pushed
              ? "calc(1.5rem + var(--fn-edge-bottom) + var(--keyboard-inset-height, var(--keyboard-inset, 0px)))"
              : undefined,
            paddingLeft: "max(1rem, var(--fn-edge-left))",
          }}
        >
          {children}
        </div>
      </main>
    </section>
  );
}
