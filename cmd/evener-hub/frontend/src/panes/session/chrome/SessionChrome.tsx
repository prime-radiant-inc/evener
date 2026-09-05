// SessionChrome: the session pane's chrome surface. Its default footer
// presentation is ONE quiet status-bar row (cadence where needed, model ·
// effort, context, live work, queue depth, and the goal chip when a goal is
// set) with the session "⋯" menu pinned to the trailing edge. Its composer
// presentation keeps the same StatusRow and menu owner but omits the
// footer-only cadence and goal controls. Every real value (the ThreadModel,
// capabilities, ...) is read from the threads store internally via
// useThreadsStore, same as every other pane-level component in this app
// (mirrors Session.tsx's own model lookup).
//
// The menu is the shared SessionMenu (2026-08-05-unified-session-context-
// menu-design): Details/Tasks/Activity and pane-only Verbosity lead it at every
// width (there are no inline triggers and no narrow-collapse - the status
// row's container-query variants own compression inside .body instead),
// followed by Rename, the tree-gated Pin/Archive/Delete organization group,
// and Shut down. The three
// panels stay mounted triggerless so their imperative handles still open the
// mobile Sheets; ActivityPanel's refreshWhenHidden is unconditional because
// the menu's "Activity · N" label reads the summary that refresh maintains.
// Slash-command actions (goal/aside/compact/clear) are deliberately NOT in
// the menu - the session's own composer owns those now (2026-08-14, "the
// composer is where you act on this session"; the command palette only
// hands off to it - design-system.md §9) - so GoalControl below is the
// goal chip + clear popover only.
import { useRef, useState } from "react";
import { sessionActionError } from "../../../protocol/errors";
import type { NavigationSessionLocation } from "../../../protocol/types.gen";
import { useClient } from "../../../shell/clientContext";
import { closePanesForDeletedSessions } from "../../../shell/deletedSessionPanes";
import { assignSessionPin, deleteSession, setArchived, unpinSession } from "../../../shell/rail/actions";
import { navigate, paneToURL } from "../../../shell/routing";
import { SessionMenu } from "../../../shell/sessionMenu/SessionMenu";
import { useIsMobile } from "../../../shell/useIsMobile";
import { isPaneOpen, useWorkspaceStore, workspaceStore } from "../../../shell/workspace";
import { useActivitySummaryStore } from "../../../stores/activitySummary";
import { selectLocation } from "../../../stores/navigation/selectors";
import { navigationStore, useNavigationStore } from "../../../stores/navigation/store";
import { isNavigationUnavailable } from "../../../stores/navigation/types";
import { threadsStore, useThreadsStore } from "../../../stores/threads";
import { Cadence, useToasts } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { cadenceStateForStatus, NOW_TICK_MS, useNowTick } from "../liveness";
import { navigationSummaryFor } from "../threadTitle";
import { TranscriptDetailControl } from "../transcript/TranscriptDetailControl";
import { ActivityPanel, type ActivityPanelHandle } from "./ActivityPanel";
import { DetailsPanel, type DetailsPanelHandle } from "./DetailsPanel";
import { GoalControl } from "./GoalControl";
import { StatusRow } from "./StatusRow";
import styles from "./sessionchrome.module.css";
import { TasksPanel, type TasksPanelHandle, taskAggregateLabel } from "./TasksPanel";
import "../../sessionPanels";

export type SessionChromePlacement = "footer" | "composer";

export interface SessionChromeProps {
  ref: string;
  placement?: SessionChromePlacement;
  onOpenTasks?: () => void;
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

export function SessionChrome({ ref: sessionRef, placement = "footer", onOpenTasks }: SessionChromeProps) {
  const client = useClient();
  const model = useThreadsStore((s) => s.threads.get(sessionRef));
  const isMobile = useIsMobile();
  const [verbosityOpen, setVerbosityOpen] = useState(false);
  const toasts = useToasts();
  const detailsOpen = useWorkspaceStore((s) => isPaneOpen(s, "sessionDetails", { ref: sessionRef }));
  const tasksOpen = useWorkspaceStore((s) => isPaneOpen(s, "sessionTasks", { ref: sessionRef }));
  const activityOpen = useWorkspaceStore((s) => isPaneOpen(s, "sessionActivity", { ref: sessionRef }));
  const activitySummary = useActivitySummaryStore((s) => s.entries.get(sessionRef));
  // Route-demanded locations carry the authoritative owner/tier/pin metadata;
  // no project is expanded merely to decide menu eligibility.
  const navigation = useNavigationStore();
  const locationResource = selectLocation(sessionRef)(navigation);
  const location = isNavigationUnavailable(locationResource?.error)
    ? undefined
    : (locationResource?.data as NavigationSessionLocation | undefined);
  const navigationSession = location?.session;
  const fallbackSession = navigationSession ?? navigationSummaryFor(sessionRef, navigation);
  const eligibleFallback = fallbackSession && !["subagent", "fork", "cluster"].includes(fallbackSession.kind);
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
  const detailsRef = useRef<DetailsPanelHandle>(null);
  const tasksRef = useRef<TasksPanelHandle>(null);
  const activityRef = useRef<ActivityPanelHandle>(null);
  if (!model) return null;

  const openDetails = () => {
    if (isMobile) detailsRef.current?.open();
    else workspaceStore.getState().togglePane("sessionDetails", { ref: sessionRef });
  };
  const openTasks = () => {
    if (onOpenTasks) onOpenTasks();
    else if (isMobile) tasksRef.current?.open();
    else workspaceStore.getState().togglePane("sessionTasks", { ref: sessionRef });
  };
  const openActivity = () => {
    if (isMobile) activityRef.current?.open();
    else workspaceStore.getState().togglePane("sessionActivity", { ref: sessionRef });
  };
  const activityLabel = activitySummary?.counts?.complete ? `Activity · ${activitySummary.counts.active}` : "Activity";

  return (
    <>
      <div
        className={placement === "composer" ? CLASS.inline : CLASS.chrome}
        data-testid={placement === "composer" ? "session-chrome-inline" : "session-chrome"}
      >
        {placement === "composer" ? (
          <div className={CLASS.body} data-testid="session-chrome-inline-status">
            <StatusRow sessionRef={sessionRef} model={model} now={now} />
            {/* Production mounts ONLY this placement (Composer.tsx), so the
              goal chip must ride here too — footer-only left it unreachable
              in the real app. */}
            <GoalControl sessionRef={sessionRef} model={model} />
          </div>
        ) : (
          /* .body owns compression (sessionchrome.module.css says why): its
           inline-size container progressively simplifies status content, so
           .right - and with it the "..." menu - always shares this one line. */
          <div className={CLASS.body} data-testid="session-chrome-body">
            <span className={CLASS.cadenceSlot} data-testid="session-chrome-cadence">
              <Cadence state={cadenceStateForStatus(model.status.type)} frameTimes={frameTimes} now={now} />
            </span>
            <StatusRow sessionRef={sessionRef} model={model} now={now} />
            <GoalControl sessionRef={sessionRef} model={model} />
          </div>
        )}
        <div className={CLASS.right}>
          <DetailsPanel ref={detailsRef} model={model} now={now} hideTrigger />
          {!onOpenTasks && <TasksPanel ref={tasksRef} sessionRef={sessionRef} model={model} hideTrigger />}
          <ActivityPanel
            ref={activityRef}
            sessionRef={sessionRef}
            model={model}
            now={now}
            hideTrigger
            refreshWhenHidden
          />
          <SessionMenu
            sessionRef={sessionRef}
            title={model.name}
            triggerLabel="Session actions"
            canRename={model.capabilities.rename}
            canShutdown={model.capabilities.shutdown}
            session={menuSession}
            panesOpen={{ details: detailsOpen, tasks: tasksOpen, activity: activityOpen }}
            taskLabel={model.tasks ? `Tasks ${taskAggregateLabel(model.tasks)}` : undefined}
            activityLabel={activityLabel}
            onOpenVerbosity={() => setVerbosityOpen(true)}
            actions={{
              onOpenPane: (pane) => {
                if (pane === "details") openDetails();
                else if (pane === "tasks") openTasks();
                else openActivity();
              },
              // Failure convention (SessionMenu.tsx's header comment): the
              // ADAPTER toasts with sessionActionError and rethrows, so a
              // rejected action leaves SessionMenu's dialog open with its
              // confirm button re-enabled; only success closes it.
              onRename: async (name) => {
                try {
                  await threadsStore.getState().rename(sessionRef, name);
                } catch (err) {
                  toasts.push("error", sessionActionError("Couldn't rename session", err));
                  throw err;
                }
              },
              onShutdown: async () => {
                const invalidation = navigationStore
                  .getState()
                  .awaitNavigationInvalidation((payload) =>
                    payload.targets.some(
                      (target) =>
                        target.kind === "all_loaded_projects" ||
                        (target.kind === "section" && (target.section === "live" || target.section === "needs_you")) ||
                        (target.kind === "pin_section" && target.sectionId === menuSession?.pin_section_id) ||
                        (target.kind === "project" && target.projectKey === location?.project_key),
                    ),
                  );
                void invalidation.promise.catch(() => undefined);
                try {
                  await threadsStore.getState().shutdown(sessionRef);
                  const payload = await invalidation.promise;
                  await navigationStore.getState().awaitNavigationTargets(payload.targets, payload.generationId);
                } catch (err) {
                  invalidation.cancel();
                  toasts.push("error", sessionActionError("Couldn't shut down session", err));
                  throw err;
                }
              },
              onPin: async (target) => {
                try {
                  const result = await assignSessionPin(client, sessionRef, target);
                  navigationStore.getState().trackPinSection(result.assignment.section.id);
                  if (result.navigation) await navigationStore.getState().applyNavigationMutation(result.navigation);
                } catch (err) {
                  toasts.push("error", sessionActionError("Couldn't assign pinned session", err));
                  throw err;
                }
              },
              onUnpin: async () => {
                try {
                  const result = await unpinSession(client, sessionRef);
                  if (result.navigation) await navigationStore.getState().applyNavigationMutation(result.navigation);
                } catch (err) {
                  toasts.push("error", sessionActionError("Couldn't unpin session", err));
                  throw err;
                }
              },
              onToggleArchive: async () => {
                if (!menuSession) return;
                try {
                  const result = await setArchived("session", menuSession.session_id, menuSession.tier !== "archived");
                  if (result.navigation) await navigationStore.getState().applyNavigationMutation(result.navigation);
                } catch (err) {
                  toasts.push("error", sessionActionError("Couldn't update archive state", err));
                  throw err;
                }
              },
              onDelete: async () => {
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
              },
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
