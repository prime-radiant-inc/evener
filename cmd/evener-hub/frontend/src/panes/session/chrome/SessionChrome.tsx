// SessionChrome: the session pane's chrome surface. Its default footer
// presentation is ONE quiet status-bar row (cadence where needed, model ·
// effort, context, live work, queue depth) with the session "⋯" menu pinned
// to the trailing edge. Its composer presentation keeps the same StatusRow
// and menu owner but omits the footer-only cadence. Every real value (the
// ThreadModel, capabilities, ...) is read from the threads store internally via
// useThreadsStore, same as every other pane-level component in this app
// (mirrors Session.tsx's own model lookup).
//
// The menu is the shared SessionMenu (2026-08-05-unified-session-context-
// menu-design): Overview and pane-only Verbosity lead it at every
// width (there are no inline triggers and no narrow-collapse - the status
// row's container-query variants own compression inside .body instead),
// followed by Rename, the tree-gated Pin/Archive/Delete organization group,
// and Shut down. The composer placement alone can also lead with the
// narrow-layout turn verbs (Stop/Steer - SessionMenuProps.turnVerbs). The
// hidden ActivityPanel stays mounted for summary discovery. Overview opens the
// shared sidebar at every viewport, and ActivityPanel's refreshWhenHidden is
// unconditional because the menu's "Overview · N" label reads the summary that
// refresh maintains.
// Slash-command actions (goal/aside/compact/clear) are deliberately NOT in
// the menu - the session's own composer owns those now (2026-08-14, "the
// composer is where you act on this session"; the command palette only
// hands off to it - design-system.md §9). The former goal chip + clear
// popover (GoalControl) is deleted entirely: Jesse's 2026-09-28 ruling on
// the #1339 composer row dropped it from the composer placement, and the
// footer mount that remained was production-dead (only the composer and
// menu placements ever mount), so the component went rather than staying
// dead code - the goal objective stays editable, and it and its live
// status stay visible, through the composer's own CurrentWork goal row
// and its inline /goal built-in; the iteration count the chip's popover
// carried has no surface now.

import type { NavigationSessionLocation } from "@evener/appwire-client";
import { SHUT_DOWN_STATUSES, sessionActionError } from "@evener/appwire-client";
import { isNavigationUnavailable } from "@evener/appwire-client/state/navigation";
import { useState } from "react";
import {
  activitySidebarOpenFor,
  activitySidebarStore,
  useActivitySidebarOpenFor,
} from "../../../shell/activitybar/activitySidebarStore";
import { useClient } from "../../../shell/clientContext";
import { closePanesForDeletedSessions } from "../../../shell/deletedSessionPanes";
import { assignSessionPin, deleteSession, setArchived, unpinSession } from "../../../shell/rail/actions";
import { isConfirmedCrashedSession } from "../../../shell/rail/sessionKind";
import { navigate, paneToURL, refParam } from "../../../shell/routing";
import { SessionMenu, type SessionMenuProps, type SessionMenuTurnVerbs } from "../../../shell/sessionMenu/SessionMenu";
import { useIsMobile } from "../../../shell/useIsMobile";
import { useWorkspaceStore } from "../../../shell/workspace";
import { selectLocation } from "../../../stores/navigation/selectors";
import { buildShutdownConvergence } from "../../../stores/navigation/shutdownConvergence";
import { navigationStore, useNavigationStore } from "../../../stores/navigation/store";
import { useSessionActivity } from "../../../stores/sessionActivity";
import { threadsStore, useThreadsStore } from "../../../stores/threads";
import { Cadence, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { cadenceStateForStatus, NOW_TICK_MS, useNowTick } from "../liveness";
import { navigationSummaryFor } from "../threadTitle";
import { TranscriptDetailControl } from "../transcript/TranscriptDetailControl";
import { ActivityPanel } from "./ActivityPanel";
import { activityActionLabel } from "./activityFormat";
import { StatusRow } from "./StatusRow";
import styles from "./sessionchrome.module.css";
import "../../sessionPanels";

export type SessionChromePlacement = "footer" | "composer" | "menu";

export interface SessionChromeProps {
  ref: string;
  placement?: SessionChromePlacement;
  /** Live session mounts opt into the hidden panel's initial activity discovery. */
  discoverActivity?: boolean;
  /**
   * Mount ONLY the hidden discovery owner, with no chrome of its own. The
   * composer uses this while its follow-up card rests, when the card's own
   * control row - the mount that otherwise carries `discoverActivity` - is
   * absent, so initial activity discovery still follows the pane on screen
   * (issue #1335).
   */
  discoveryOnly?: boolean;
  /**
   * Turn verbs for the session menu, offered by the composer's narrow
   * layout (Jesse's 2026-09-28 ruling on the #1339 phone-width wrap). Only
   * the composer placement forwards them: the footer and menu-only mounts
   * share SessionMenu with the rail, where no draft exists for Steer to
   * send. Composer.tsx owns presence, disablement, and the press handlers.
   */
  turnVerbs?: SessionMenuTurnVerbs;
}

const CLASS = {
  chrome: requireClass(styles.chrome, "sessionchrome.module.css", "chrome"),
  inline: requireClass(styles.inline, "sessionchrome.module.css", "inline"),
  body: requireClass(styles.body, "sessionchrome.module.css", "body"),
  cadenceSlot: requireClass(styles.cadenceSlot, "sessionchrome.module.css", "cadenceSlot"),
  right: requireClass(styles.right, "sessionchrome.module.css", "right"),
};

// Module-level empty array so a ref with no tracked frames yet doesn't get a
// fresh [] identity every render (Session.tsx's own frameTimes lookup does
// the same for the header cadence).
const EMPTY_FRAME_TIMES: number[] = [];

export function SessionChrome({
  ref: sessionRef,
  placement = "footer",
  discoverActivity = false,
  discoveryOnly = false,
  turnVerbs,
}: SessionChromeProps) {
  const client = useClient();
  const model = useThreadsStore((s) => s.threads.get(sessionRef));
  const paneId = useWorkspaceStore(
    (state) => state.panes.find((pane) => pane.type === "session" && refParam(pane.params) === sessionRef)?.id,
  );
  const isMobile = useIsMobile();
  const [verbosityOpen, setVerbosityOpen] = useState(false);
  const toasts = useToasts();
  // The Overview menu item's checked state is the sidebar open ON THIS
  // SESSION (the shared predicate hook). The mobile overlay uses the same
  // scope, so rail and session menus agree at every viewport.
  const sidebarOpenHere = useActivitySidebarOpenFor(sessionRef);
  const { snapshot: activitySnapshot } = useSessionActivity(sessionRef);
  const activitySummary = activitySnapshot?.summary;
  const mutationStateAuthoritative = useThreadsStore((s) => s.mutationAuthorityRefs.has(sessionRef));
  // Route-demanded locations carry the authoritative owner/tier/pin metadata;
  // no project is expanded merely to decide menu eligibility.
  const navigation = useNavigationStore();
  const locationResource = selectLocation(sessionRef)(navigation);
  const location = isNavigationUnavailable(locationResource?.error)
    ? undefined
    : (locationResource?.data as NavigationSessionLocation | undefined);
  const navigationSession = location?.session;
  const fallbackSession = navigationSession ?? navigationSummaryFor(sessionRef, navigation);
  const eligibleFallback = fallbackSession && !["subagent", "fork"].includes(fallbackSession.kind);
  const validIdentity =
    typeof fallbackSession?.host_id === "string" &&
    fallbackSession.host_id.trim() !== "" &&
    typeof fallbackSession.session_id === "string" &&
    fallbackSession.session_id.trim() !== "" &&
    typeof fallbackSession.kind === "string" &&
    fallbackSession.kind.trim() !== "";
  const menuSession =
    fallbackSession && validIdentity
      ? {
          ref: sessionRef,
          title: fallbackSession.title || model?.name || sessionRef,
          host_id: fallbackSession.host_id,
          session_id: fallbackSession.session_id,
          kind: fallbackSession.kind,
          failure: fallbackSession.failure,
          top_level: location?.top_level ?? eligibleFallback,
          tier: location?.tier,
          pin_section_id: location?.pin_section_id,
        }
      : undefined;
  // The cadence that used to live in the pane header's cadence slot: the
  // header is hidden on mobile (2026-07-30-mobile-session-layout-design.md,
  // decision 3), so the liveness marker relocates here. Rendered always,
  // revealed only below the breakpoint by .cadenceSlot's own CSS - the same
  // "panes never ask am I mobile?" rule the header channel follows. Read
  // from the same store fields Session.tsx reads for the header copy.
  const frameTimes = useThreadsStore((s) => s.frameTimes.get(sessionRef) ?? EMPTY_FRAME_TIMES);
  // Session.tsx already runs one useNowTick(NOW_TICK_MS) for the header's
  // own Cadence/LivenessLine; this is a second, independent instance for
  // the footer's work-time clock (widgets/cadence's own doc comment: "no
  // timers, no Date.now()" is a rule for the pure prop-driven widgets
  // downstream, not a ban on more than one clock owner upstream - see
  // liveness.ts's own useNowTick doc comment: "transient by design").
  const now = useNowTick(NOW_TICK_MS);
  if (!model) return null;

  // The ONE hidden ActivityPanel every shape below shares. `discoverWhenHidden`
  // is the opt-in wiring that must not drift, so it is spelled exactly once
  // here; `discoveryOnly` is itself an opt-in (it exists for nothing else).
  const hiddenActivityPanel = (
    <ActivityPanel
      sessionRef={sessionRef}
      model={model}
      hideTrigger
      refreshWhenHidden
      discoverWhenHidden={discoverActivity || discoveryOnly}
    />
  );

  // A chrome-less mount returns the hidden discovery owner and nothing else:
  // no status row, no menu, no second "⋯". This early return sits after every
  // hook call above (everything below is plain values and handlers).
  if (discoveryOnly) return hiddenActivityPanel;

  // Force-stop eligibility mirrors the reach of the retired inline footer
  // button (Session.tsx): any local session that isn't closed, including
  // saved notLoaded panes (a pending or failed resume can still own a daemon)
  // and panes with no navigation identity. The one exclusion is a session
  // retained by its owning session - its notice directs the user to the owner
  // instead (same predicate as Session.tsx's recoveryOwnerRef).
  const recoveryOwnerRef =
    !mutationStateAuthoritative &&
    model.status.type !== "notLoaded" &&
    model.status.type !== "restartRequired" &&
    model.parentRef?.startsWith("local:")
      ? model.parentRef
      : undefined;

  const openOverview = () => {
    // Mobile and desktop share the activity sidebar. Desktop toggles only when
    // the sidebar already shows THIS session; opening on another session
    // re-scopes it here instead of closing it under the user. Opening it also
    // retires a leftover sessionActivity pane for this session.
    if (!isMobile && activitySidebarOpenFor(sessionRef)) activitySidebarStore.getState().close();
    else {
      activitySidebarStore.getState().openFor(sessionRef);
    }
  };
  const overviewLabel = activityActionLabel(activitySummary, "Overview");

  // The menu's action adapters, shared by the composer and menu-only
  // placements so the failure convention (SessionMenu.tsx's header comment:
  // the adapter toasts with sessionActionError and rethrows) cannot drift
  // between them.
  const forceStopAction =
    sessionRef.startsWith("local:") && model.status.type !== "closed" && !recoveryOwnerRef
      ? async () => {
          try {
            await threadsStore.getState().forceStop(sessionRef);
          } catch (err) {
            toasts.push("error", sessionActionError("Couldn't force shutdown session", err));
            throw err;
          }
          try {
            await threadsStore.getState().refreshThread(sessionRef);
          } catch (err) {
            toasts.push("error", sessionActionError("Session stopped; couldn't refresh its view", err));
          }
        }
      : undefined;
  const shutdownAction = async () => {
    const convergence = buildShutdownConvergence(sessionRef, {
      pinSectionId: menuSession?.pin_section_id,
      projectKey: location?.project_key,
    });
    const invalidation = convergence.arm();
    try {
      await threadsStore.getState().shutdown(sessionRef);
      await convergence.converge(invalidation);
    } catch (err) {
      invalidation.cancel();
      toasts.push("error", sessionActionError("Couldn't shut down session", err));
      throw err;
    }
  };
  const pinAction = async (target: Parameters<SessionMenuProps["actions"]["onPin"]>[0]) => {
    try {
      const result = await assignSessionPin(client, sessionRef, target);
      navigationStore.getState().trackPinSection(result.assignment.section.id);
      if (result.navigation) await navigationStore.getState().applyNavigationMutation(result.navigation);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't assign pinned session", err));
      throw err;
    }
  };
  const unpinAction = async () => {
    try {
      const result = await unpinSession(client, sessionRef);
      if (result.navigation) await navigationStore.getState().applyNavigationMutation(result.navigation);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't unpin session", err));
      throw err;
    }
  };
  const archiveAction = async () => {
    if (!menuSession) return;
    try {
      // The canonical ref, not the bare session ID: a remote row's ref is
      // host-qualified and is the identity its archive decision is read back
      // under, so the bare ID would store a local decision the row never
      // consults. "local:<id>" refs normalize server-side to the bare ID.
      const result = await setArchived("session", menuSession.ref, menuSession.tier !== "archived");
      if (result.navigation) await navigationStore.getState().applyNavigationMutation(result.navigation);
    } catch (err) {
      toasts.push("error", sessionActionError("Couldn't update archive state", err));
      throw err;
    }
  };
  const deleteAction = async () => {
    try {
      const result = await deleteSession(client, sessionRef);
      if (result.navigation) await navigationStore.getState().applyNavigationMutation(result.navigation);
      closePanesForDeletedSessions(result.deleted);
      if (result.skipped.length > 0) {
        const reason = result.skipped[0]?.reason ?? "still in use";
        toasts.push("warning", `Couldn't delete "${model.name}": ${reason}`);
      }
    } catch (err) {
      toasts.push("error", sessionActionError(`Couldn't delete "${model.name}"`, err));
      throw err;
    }
  };

  return (
    <>
      {/* The "menu" placement (Session.tsx's notLoaded footer mount) renders
          ONLY the actions group below: the pane footer already carries the
          liveness line and any recovery notice, and the composer hides its
          own chrome for exactly this state, so there is no status body to
          compress and no second menu to dedupe against. */}
      <div
        className={placement === "composer" ? CLASS.inline : CLASS.chrome}
        data-testid={
          placement === "composer"
            ? "session-chrome-inline"
            : placement === "menu"
              ? "session-chrome-menu"
              : "session-chrome"
        }
      >
        {placement === "composer" ? (
          <div className={CLASS.body} data-testid="session-chrome-inline-status">
            <StatusRow sessionRef={sessionRef} model={model} now={now} />
            {/* The goal chip is gone entirely (Jesse's 2026-09-28 ruling,
                "drop goal inline in the composer"): the composer row is
                status and menu alone, and the goal objective stays visible
                and editable through the composer's own CurrentWork goal row
                and its inline /goal built-in. */}
          </div>
        ) : placement === "menu" ? null : (
          /* .body owns compression (sessionchrome.module.css says why): its
           inline-size container progressively simplifies status content, so
           .right - and with it the "..." menu - always shares this one line. */
          <div className={CLASS.body} data-testid="session-chrome-body">
            <span className={CLASS.cadenceSlot} data-testid="session-chrome-cadence">
              <Cadence state={cadenceStateForStatus(model.status.type)} frameTimes={frameTimes} now={now} />
            </span>
            <StatusRow sessionRef={sessionRef} model={model} now={now} />
          </div>
        )}
        <div className={CLASS.right}>
          {hiddenActivityPanel}
          <SessionMenu
            sessionRef={sessionRef}
            paneId={paneId}
            title={model.name}
            triggerLabel="Session actions"
            canRename={model.capabilities.rename}
            canShutdown={model.capabilities.shutdown}
            stopped={SHUT_DOWN_STATUSES.has(model.status.type) || isConfirmedCrashedSession(fallbackSession)}
            session={menuSession}
            overviewOpen={sidebarOpenHere}
            overviewLabel={overviewLabel}
            onOpenVerbosity={() => setVerbosityOpen(true)}
            // Composer placement only: the header comment on the prop says
            // why the other placements must never carry turn verbs.
            turnVerbs={placement === "composer" ? turnVerbs : undefined}
            actions={{
              onOpenOverview: openOverview,
              onRename: async (name) => {
                try {
                  await threadsStore.getState().rename(sessionRef, name);
                } catch (err) {
                  toasts.push("error", sessionActionError("Couldn't rename session", err));
                  throw err;
                }
              },
              onForceStop: forceStopAction,
              onShutdown: shutdownAction,
              onPin: pinAction,
              onUnpin: unpinAction,
              onToggleArchive: archiveAction,
              onDelete: deleteAction,
            }}
          />
        </div>
      </div>
      <TranscriptDetailControl
        open={verbosityOpen}
        onClose={() => setVerbosityOpen(false)}
        layout={isMobile ? "mobile" : "desktop"}
        onEditHubDefaults={() => {
          const url = paneToURL("settings", { section: "transcript" });
          if (url !== null) navigate(url);
        }}
      />
    </>
  );
}
