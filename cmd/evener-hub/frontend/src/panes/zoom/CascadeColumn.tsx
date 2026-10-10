import type { ReactNode } from "react";
import type { ActivityScope } from "../../shell/statusbar/statusScope";
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
  return (
    <>
      <header className={CLASS.header}>
        <div className={CLASS.heading}>
          <h3 className={CLASS.title}>{scope.leaf.title}</h3>
          <ScopeRuntimeState scope={scope} />
        </div>
        <Button variant="quiet" size="xs" onClick={() => openCascadeConversation(paneId, scope.leaf.ref)}>
          Open conversation
        </Button>
      </header>
      <ScopeActivityControls paneId={paneId} scope={scope} />
      <div className={CLASS.content}>{children}</div>
    </>
  );
}
