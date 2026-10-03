import { useLayoutEffect, useRef } from "react";
import { useStore } from "zustand";
import { useShallow } from "zustand/react/shallow";
import { m, spatialTransition, useReducedMotion } from "../../motion";
import { conversationPaneLifetime, type PaneLifetime } from "../../shell/paneLifetime";
import type { PaneProps } from "../../shell/paneRegistry";
import { ScopeCrumbs } from "../../shell/statusbar/ScopeCrumbs";
import { StatusBar } from "../../shell/statusbar/StatusBar";
import { deriveScope } from "../../shell/statusbar/statusScope";
import { useIsMobile } from "../../shell/useIsMobile";
import { type OpenPaneRecord, workspaceStore } from "../../shell/workspace";
import { navigationStore, useNavigationStore } from "../../stores/navigation/store";
import { useSessionActivity } from "../../stores/sessionActivity";
import { useThreadsStore } from "../../stores/threads";
import { Button, EmptyState, PaneScaffold } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import { ReadOnlyThreadContent } from "../transcript/ReadOnlyThreadContent";
import {
  clearCascadeUserTransition,
  hasCascadeUserTransition,
  popAgentCascade,
  reconcileCascadeBinding,
  returnFromAgentCascade,
} from "./actions";
import { CascadeColumn } from "./CascadeColumn";
import { CascadeSpine } from "./CascadeSpine";
import { type CascadeScope, deriveCascadePath, type SessionZoomParams } from "./intent";
import styles from "./zoom.module.css";

const CLASS = {
  body: requireClass(styles.body, "zoom.module.css", "body"),
  path: requireClass(styles.path, "zoom.module.css", "path"),
  hint: requireClass(styles.hint, "zoom.module.css", "hint"),
  track: requireClass(styles.track, "zoom.module.css", "track"),
  column: requireClass(styles.column, "zoom.module.css", "column"),
  spine: requireClass(styles.spine, "zoom.module.css", "spine"),
};

function ScopeConversation({
  pane,
  lifetime,
  scope,
  readable,
  leaf,
  mobile,
  userTransition,
}: {
  pane: OpenPaneRecord;
  lifetime: PaneLifetime;
  scope: CascadeScope;
  readable: boolean;
  leaf: boolean;
  mobile: boolean;
  userTransition: boolean;
}) {
  // Spines retain summary demand, not transcript or collection demand.
  const { snapshot } = useSessionActivity(scope.requestedRef);
  useNavigationStore((state) => state.resources);
  const model = useThreadsStore(
    useShallow((state) => {
      const thread = state.threads.get(scope.requestedRef);
      return thread === undefined ? undefined : { name: thread.name, imageSessionId: thread.imageSessionId };
    }),
  );
  const sessionId = snapshot?.context?.sessionId ?? scope.sessionId;
  const previous = lifetime.resolvedSessions.get(scope.requestedRef);
  const rebinding = sessionId !== undefined && previous !== undefined && previous !== sessionId;
  // A new thread image can arrive before its activity context. Keep its bytes
  // behind the identity fence until that context has retired the old suffix.
  const imageMatches = sessionId === undefined || model === undefined || model.imageSessionId === sessionId;
  const admitted = !rebinding && imageMatches;
  const view = retainedTranscriptReadView(
    lifetime,
    scope.requestedRef,
    scope.requestedRef === lifetime.sourceRef ? lifetime.sourceType : "cascade",
  );
  useLayoutEffect(() => {
    if (sessionId !== undefined) reconcileCascadeBinding(pane, scope.requestedRef, sessionId);
  }, [pane, scope.requestedRef, sessionId]);
  useLayoutEffect(() => {
    view.setReadable(readable && admitted);
    return () => view.setReadable(false);
  }, [view, readable, admitted]);

  const activity = deriveScope(navigationStore.getState(), scope.requestedRef, snapshot);
  activity.leaf.title = (imageMatches && model?.name) || scope.title;
  const readableWidth = leaf ? 440 : 400;
  const desktopWidth = readable ? readableWidth : 52;
  const width = mobile ? "100%" : desktopWidth;
  return (
    <m.section
      className={readable ? CLASS.column : CLASS.spine}
      data-testid={readable ? "cascade-column" : "cascade-spine"}
      data-scope-ref={scope.requestedRef}
      data-leaf={leaf || undefined}
      aria-label={activity.leaf.title}
      initial={userTransition ? { x: 48 } : false}
      animate={{ width, x: 0 }}
      transition={userTransition ? spatialTransition() : { duration: 0 }}
    >
      {readable ? (
        <CascadeColumn paneId={pane.id} scope={activity}>
          {admitted ? (
            <ReadOnlyThreadContent ref={scope.requestedRef} view={view} availability={snapshot?.summaryState} />
          ) : (
            <EmptyState title="Loading transcript…" />
          )}
        </CascadeColumn>
      ) : (
        <CascadeSpine paneId={pane.id} scope={activity} />
      )}
    </m.section>
  );
}

export default function Zoom({ paneId, focused }: PaneProps<SessionZoomParams>) {
  const pane = useStore(workspaceStore, (state) =>
    state.panes.find((record) => record.id === paneId && record.type === "sessionZoom"),
  );
  const params = pane?.params as SessionZoomParams | undefined;
  const { snapshot } = useSessionActivity(params?.ref ?? null);
  const mobile = useIsMobile();
  const reducedMotion = useReducedMotion();
  const track = useRef<HTMLDivElement>(null);
  const userTransition = params !== undefined && hasCascadeUserTransition(params);
  useLayoutEffect(() => {
    if (!params || !userTransition) return;
    clearCascadeUserTransition(params);
    if (track.current) track.current.scrollLeft = track.current.scrollWidth - track.current.clientWidth;
  }, [params, userTransition]);
  if (!pane || !params) return null;
  const lifetime = conversationPaneLifetime(pane);
  const path = deriveCascadePath(params, snapshot?.context ?? null);
  const scopes = mobile ? path.scopes.slice(-1) : path.scopes;
  const firstReadable = Math.max(0, scopes.length - 2);
  const returnAction = (
    <Button variant="quiet" size="sm" onClick={() => returnFromAgentCascade(paneId)}>
      Return to previous view
    </Button>
  );
  return (
    <PaneScaffold
      paneId={paneId}
      focused={focused}
      title="Agent cascade"
      scaffoldMarker="cascade"
      actions={mobile ? undefined : returnAction}
      edgeFooter={
        <StatusBar
          sessionRef={params.ref}
          paneId={paneId}
          leading={
            <ScopeCrumbs
              path={path.scopes.map((scope) => ({ ref: scope.requestedRef, title: scope.title }))}
              onNavigate={(ref) => popAgentCascade(paneId, ref)}
            />
          }
        />
      }
    >
      <div className={CLASS.body}>
        {mobile && returnAction}
        <nav className={CLASS.path} aria-label="Agent path">
          {path.scopes.map((scope) => (
            <Button
              key={scope.requestedRef}
              variant="quiet"
              size="xs"
              onClick={() => popAgentCascade(paneId, scope.requestedRef)}
            >
              {scope.title}
            </Button>
          ))}
        </nav>
        {!path.ancestryKnown && <p className={CLASS.hint}>Earlier ancestry is incomplete</p>}
        <div ref={track} className={CLASS.track}>
          {scopes.map((scope, index) => (
            <ScopeConversation
              key={scope.requestedRef}
              pane={pane}
              lifetime={lifetime}
              scope={scope}
              readable={index >= firstReadable}
              leaf={index === scopes.length - 1}
              mobile={mobile}
              userTransition={userTransition && reducedMotion !== true}
            />
          ))}
        </div>
      </div>
    </PaneScaffold>
  );
}
