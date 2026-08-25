import type { ReactNode } from "react";
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

export interface StillwaterShellProps {
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

export function StillwaterShell({
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
}: StillwaterShellProps) {
  const activeTab = state.route.kind === "root" ? state.route.tab : null;
  return (
    <div
      className="concept-stillwater"
      data-concept-root
      data-platform={primitives.platform}
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
      data-reduced-motion={state.reducedMotion}
    >
      <main className="sw-scroll" data-route={routeName}>
        <header className="sw-topbar">
          <div className="sw-topbar__leading">
            {pushed ? (
              <button
                className="sw-icon-action"
                type="button"
                aria-label={pushed === "close" ? "Close" : "Back"}
                onClick={() => dispatch({ type: "goBack" })}
              >
                <Icon name={pushed === "close" ? "close" : "back"} decorative />
              </button>
            ) : null}
            <div className="sw-title-block">
              <p className="sw-title-block__kicker">Quiet Instrument</p>
              <h1>{title}</h1>
              {subtitle ? <p>{subtitle}</p> : null}
            </div>
          </div>
          <div className="sw-topbar__actions">
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
        <div className="sw-route-content">{children}</div>
      </main>

      {activeTab ? (
        <nav className="sw-root-nav" aria-label="Primary">
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
