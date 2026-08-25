import type { PrototypeAction, PrototypeState } from "../../core/state";
import { ScreenState } from "../shared/ScreenState";

export interface RouteStateProps {
  state: PrototypeState;
  routeLabel: string;
  dispatch(action: PrototypeAction): void;
}

export function isBlockingRouteState(state: PrototypeState): boolean {
  return (
    state.projection.screenState === "loading" ||
    state.projection.screenState === "empty" ||
    state.projection.screenState === "error"
  );
}

export function BlockingRouteState({
  state,
  routeLabel,
  dispatch,
}: RouteStateProps) {
  const { screenState } = state.projection;
  if (screenState === "loading") {
    return (
      <section
        className="sw-route-state sw-route-state--loading"
        data-screen-state="loading"
        role="status"
        aria-live="polite"
      >
        <div>
          <h2>Loading {routeLabel}</h2>
          <p>Preparing the stable local layout.</p>
        </div>
        <div className="sw-state-skeleton" aria-hidden="true">
          <span />
          <span />
          <span />
        </div>
      </section>
    );
  }
  if (screenState === "empty") {
    return (
      <ScreenState
        state={{
          kind: "empty",
          title: `No ${routeLabel.toLowerCase()} available`,
          detail: "Choose another scenario or start from an available fixture.",
        }}
      />
    );
  }
  if (screenState === "error") {
    return (
      <ScreenState
        state={{
          kind: "error",
          title: `${routeLabel} could not be shown`,
          detail:
            "The local prototype state is recoverable with an explicit retry.",
        }}
        retryAction={() => dispatch({ type: "refreshSessions" })}
      />
    );
  }
  return null;
}

export interface OfflineNoticeProps extends RouteStateProps {
  mutationDetail: string;
}

export function OfflineNotice({
  state,
  mutationDetail,
  dispatch,
}: OfflineNoticeProps) {
  if (state.projection.screenState !== "offline") return null;
  const syncAge =
    state.projection.fixture.sessions[0]?.updatedLabel ?? "age unavailable";
  return (
    <section
      className="sw-banner sw-offline-policy"
      data-offline-policy="read-only"
      role="status"
    >
      <div>
        <strong>Offline · Last synced {syncAge}</strong>
        <p>
          Read-only scope: saved fixture evidence remains available.{" "}
          {mutationDetail}
        </p>
      </div>
      <button
        type="button"
        disabled={state.refreshState === "refreshing"}
        onClick={() => dispatch({ type: "refreshSessions" })}
      >
        Retry
      </button>
    </section>
  );
}
