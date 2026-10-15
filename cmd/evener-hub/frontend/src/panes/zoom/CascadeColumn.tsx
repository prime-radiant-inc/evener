import type { ReactNode } from "react";
import type { ActivityScope } from "../../shell/statusbar/statusScope";
import { useIsMobile } from "../../shell/useIsMobile";
import { requireClass } from "../../widgets/internal/requireClass";
import { ScopeActivityControls, ScopeRuntimeState } from "./ActivityPeek";
import styles from "./zoom.module.css";

const CLASS = {
  header: requireClass(styles.header, "zoom.module.css", "header"),
  heading: requireClass(styles.heading, "zoom.module.css", "heading"),
  title: requireClass(styles.title, "zoom.module.css", "title"),
  content: requireClass(styles.content, "zoom.module.css", "content"),
  columnFooterBar: requireClass(styles.columnFooterBar, "zoom.module.css", "columnFooterBar"),
  columnComposerBar: requireClass(styles.columnComposerBar, "zoom.module.css", "columnComposerBar"),
};

// The column frame around an embedded conversation: the cascade's own chrome
// (title, runtime state, activity controls) plus the conversation's footer
// in the column's bottom bar. The conversation itself renders into `content`
// through the shared ConversationBody; the old Open-conversation link is gone
// because there is no other view to open.
export function CascadeColumn({
  paneId,
  scope,
  content,
  conversationFooter,
}: {
  paneId: string;
  scope: ActivityScope;
  content: ReactNode;
  conversationFooter: ReactNode;
}) {
  const mobile = useIsMobile();
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
        </header>
      )}
      <div className={CLASS.content} data-testid="cascade-column-content">
        {content}
      </div>
      {mobile && (
        <div className={CLASS.columnFooterBar} data-testid="cascade-column-footer">
          <ScopeRuntimeState scope={scope} />
          {conversationFooter}
        </div>
      )}
      {!mobile && conversationFooter && <div className={CLASS.columnComposerBar}>{conversationFooter}</div>}
      <ScopeActivityControls paneId={paneId} scope={scope} />
    </>
  );
}
