import type { CSSProperties, ReactNode } from "react";
import type { PlatformPrimitives } from "../../core/platform";
import type { PrototypeAction, PrototypeState } from "../../core/state";
import { Icon, type IconName } from "../shared/Icon";

const rootNavigation = [
  { tab: "sessions", label: "Sessions", icon: "sessions" },
  { tab: "search", label: "Search", icon: "search" },
  { tab: "new", label: "New Session", icon: "new" },
  { tab: "settings", label: "Settings", icon: "settings" },
] as const satisfies readonly {
  tab: "sessions" | "search" | "new" | "settings";
  label: string;
  icon: IconName;
}[];

export interface FieldNotesShellProps {
  state: PrototypeState;
  primitives: PlatformPrimitives;
  dispatch(action: PrototypeAction): void;
  onOpenConceptSwitcher(): void;
  onOpenLabControls(): void;
  routeName: string;
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

function edgeStyles(primitives: PlatformPrimitives): FieldNotesEdgeStyles {
  const source = (edge: "top" | "right" | "bottom" | "left") =>
    primitives.safeArea === "ios-environment"
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
  primitives,
  dispatch,
  onOpenConceptSwitcher,
  onOpenLabControls,
  routeName,
  title,
  subtitle,
  pushed,
  children,
}: FieldNotesShellProps) {
  const activeTab = state.route.kind === "root" ? state.route.tab : null;
  const iconTarget = `${primitives.minimumTarget}px`;
  const topPadding =
    primitives.title === "large-or-inline"
      ? "max(0.85rem, var(--fn-edge-top))"
      : "max(0.75rem, var(--fn-edge-top))";
  return (
    <div
      className="concept-field-notes"
      data-concept-root
      data-platform={primitives.platform}
      data-reading-role={
        primitives.platform === "android" ? "editorial-sans" : "editorial-serif"
      }
      data-navigation={primitives.navigation}
      data-title={primitives.title}
      data-sheet={primitives.sheet}
      data-dialog={primitives.dialog}
      data-elevation={primitives.elevation}
      data-feedback={primitives.feedback}
      data-back={primitives.back}
      data-safe-area={primitives.safeArea}
      data-minimum-target={primitives.minimumTarget}
      data-appearance={state.appearance}
      data-text-scale={state.textScale}
      data-reduced-motion={state.reducedMotion}
      data-motion={
        state.reducedMotion ||
        document.documentElement.dataset.reducedMotion === "true"
          ? "reduced"
          : "full"
      }
      style={edgeStyles(primitives)}
    >
      <main className="fn-scroll-owner" data-route={routeName}>
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
                onClick={() => dispatch({ type: "goBack" })}
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
            <button type="button" onClick={onOpenConceptSwitcher}>
              <Icon name="switch" decorative />
              <span>Switch concept</span>
            </button>
            <button type="button" onClick={onOpenLabControls}>
              <Icon name="lab" decorative />
              <span>Lab Controls</span>
            </button>
          </div>
        </header>
        <div
          className="fn-route-content"
          style={{
            paddingRight: "max(1rem, var(--fn-edge-right))",
            paddingLeft: "max(1rem, var(--fn-edge-left))",
          }}
        >
          {children}
        </div>
      </main>

      {activeTab ? (
        <nav
          className="fn-root-nav"
          aria-label="Primary"
          style={{
            paddingRight: "max(0.55rem, var(--fn-edge-right))",
            paddingBottom: "max(0.35rem, var(--fn-edge-bottom))",
            paddingLeft: "max(0.55rem, var(--fn-edge-left))",
          }}
        >
          {rootNavigation.map((item) => (
            <button
              type="button"
              aria-current={activeTab === item.tab ? "page" : undefined}
              className={activeTab === item.tab ? "is-active" : undefined}
              key={item.tab}
              onClick={() => dispatch({ type: "navigateRoot", tab: item.tab })}
            >
              <Icon name={item.icon} decorative />
              <span>{item.label}</span>
            </button>
          ))}
        </nav>
      ) : null}
    </div>
  );
}
