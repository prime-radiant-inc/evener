import { useLayoutEffect } from "react";
import { useStore } from "zustand";
import { conversationPaneLifetime, type PaneLifetime } from "../../shell/paneLifetime";
import type { PaneProps } from "../../shell/paneRegistry";
import { type OpenPaneRecord, workspaceStore } from "../../shell/workspace";
import { useSessionActivity } from "../../stores/sessionActivity";
import { useThreadsStore } from "../../stores/threads";
import { Button, EmptyState, PaneScaffold } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { retainedTranscriptReadView } from "../session/transcript/transcriptReadView";
import { ReadOnlyThreadContent } from "../transcript/ReadOnlyThreadContent";
import { openCascadeConversation, popAgentCascade, reconcileCascadeBinding, returnFromAgentCascade } from "./actions";
import { type CascadeScope, deriveCascadePath, type SessionZoomParams } from "./intent";
import styles from "./zoom.module.css";

const CLASS = {
  body: requireClass(styles.body, "zoom.module.css", "body"),
  path: requireClass(styles.path, "zoom.module.css", "path"),
  hint: requireClass(styles.hint, "zoom.module.css", "hint"),
  track: requireClass(styles.track, "zoom.module.css", "track"),
  column: requireClass(styles.column, "zoom.module.css", "column"),
  header: requireClass(styles.header, "zoom.module.css", "header"),
  title: requireClass(styles.title, "zoom.module.css", "title"),
  content: requireClass(styles.content, "zoom.module.css", "content"),
  spine: requireClass(styles.spine, "zoom.module.css", "spine"),
};

function ScopeConversation({
  pane,
  lifetime,
  scope,
  readable,
  leaf,
}: {
  pane: OpenPaneRecord;
  lifetime: PaneLifetime;
  scope: CascadeScope;
  readable: boolean;
  leaf: boolean;
}) {
  // Spines retain summary demand, not transcript or collection demand.
  const { snapshot } = useSessionActivity(scope.requestedRef);
  const model = useThreadsStore((state) => state.threads.get(scope.requestedRef));
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

  const title = model?.name || scope.title;
  if (!readable) {
    return (
      <button
        type="button"
        className={CLASS.spine}
        data-testid="cascade-spine"
        data-scope-ref={scope.requestedRef}
        aria-label={`Show ${title}`}
        onClick={() => popAgentCascade(pane.id, scope.requestedRef)}
      >
        {title}
      </button>
    );
  }
  return (
    <section
      className={CLASS.column}
      data-testid="cascade-column"
      data-scope-ref={scope.requestedRef}
      data-leaf={leaf || undefined}
      aria-label={title}
    >
      <header className={CLASS.header}>
        <h3 className={CLASS.title}>{title}</h3>
        <Button variant="quiet" size="xs" onClick={() => openCascadeConversation(pane.id, scope.requestedRef)}>
          Open conversation
        </Button>
      </header>
      <div className={CLASS.content}>
        {admitted ? (
          <ReadOnlyThreadContent ref={scope.requestedRef} view={view} />
        ) : (
          <EmptyState title="Loading transcript…" />
        )}
      </div>
    </section>
  );
}

export default function Zoom({ paneId, focused }: PaneProps<SessionZoomParams>) {
  const pane = useStore(workspaceStore, (state) =>
    state.panes.find((record) => record.id === paneId && record.type === "sessionZoom"),
  );
  const params = pane?.params as SessionZoomParams | undefined;
  const { snapshot } = useSessionActivity(params?.ref ?? null);
  if (!pane || !params) return null;
  const lifetime = conversationPaneLifetime(pane);
  const path = deriveCascadePath(params, snapshot?.context ?? null);
  const firstReadable = Math.max(0, path.scopes.length - 2);
  return (
    <PaneScaffold
      paneId={paneId}
      focused={focused}
      title="Agent cascade"
      scaffoldMarker="cascade"
      actions={
        <Button variant="quiet" size="sm" onClick={() => returnFromAgentCascade(paneId)}>
          Return to previous view
        </Button>
      }
    >
      <div className={CLASS.body}>
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
        <div className={CLASS.track}>
          {path.scopes.map((scope, index) => (
            <ScopeConversation
              key={scope.requestedRef}
              pane={pane}
              lifetime={lifetime}
              scope={scope}
              readable={index >= firstReadable}
              leaf={index === path.scopes.length - 1}
            />
          ))}
        </div>
      </div>
    </PaneScaffold>
  );
}
