import type { ReactNode } from "react";
import type { ActivityScope } from "../../shell/statusbar/statusScope";
import { useIsMobile } from "../../shell/useIsMobile";
import { Button } from "../../widgets";
import { requireClass } from "../../widgets/internal/requireClass";
import { ScopeActivityControls, ScopeRuntimeState } from "./ActivityPeek";
import { openCascadeConversation } from "./actions";
import styles from "./zoom.module.css";

const CLASS = {
  header: requireClass(styles.header, "zoom.module.css", "header"),
  heading: requireClass(styles.heading, "zoom.module.css", "heading"),
  title: requireClass(styles.title, "zoom.module.css", "title"),
  content: requireClass(styles.content, "zoom.module.css", "content"),
  columnFooterBar: requireClass(styles.columnFooterBar, "zoom.module.css", "columnFooterBar"),
};

export function CascadeColumn({
  paneId,
  scope,
  children,
}: {
  paneId: string;
  scope: ActivityScope;
  children: ReactNode;
}) {
  const mobile = useIsMobile();
  const openAction = (
    <Button variant="quiet" size="xs" onClick={() => openCascadeConversation(paneId, scope.leaf.ref)}>
      Open conversation
    </Button>
  );
  return (
    <>
      {/* The phone's top bar already publishes the selected conversation's
       * name, so the column's own title stays desktop-only. */}
      {!mobile && (
        <header className={CLASS.header}>
          <div className={CLASS.heading}>
            <h3 className={CLASS.title}>{scope.leaf.title}</h3>
            <ScopeRuntimeState scope={scope} />
          </div>
          {openAction}
        </header>
      )}
      <div className={CLASS.content} data-testid="cascade-column-content">
        {children}
      </div>
      {mobile && (
        <div className={CLASS.columnFooterBar} data-testid="cascade-column-footer">
          <ScopeRuntimeState scope={scope} />
          {openAction}
        </div>
      )}
      <ScopeActivityControls paneId={paneId} scope={scope} />
    </>
  );
}
