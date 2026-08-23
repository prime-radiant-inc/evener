import type { JSX, ReactNode } from "react";
import type { ActivityView } from "../../services/activity";
import type { ConversationService } from "../../services/conversation";
import { Sheet } from "../../ui/Sheet";
import { ControlsSection } from "./ControlsSection";
import { TaskSection } from "./TaskSection";
import { UsageSection } from "./UsageSection";
import { WorkSection } from "./WorkSection";

export type ActivityDetent = "medium" | "large";

export interface ActivitySheetProps {
  readonly open: boolean;
  readonly onClose: () => void;
  readonly view: ActivityView | null;
  readonly conversationService: ConversationService;
  readonly detent?: ActivityDetent;
}

/**
 * Medium/large detent bottom sheet with four sections: Tasks, Work, Usage,
 * Controls. Rows summarize first and disclose detail on tap. Raw identifiers
 * and payloads live behind a diagnostics disclosure. Destructive shutdown
 * requires confirmation. A false capability removes the control and leaves
 * an "Unavailable for this source" explanation.
 */
export function ActivitySheet({
  open,
  onClose,
  view,
  conversationService,
  detent = "large",
}: ActivitySheetProps): JSX.Element | null {
  return (
    <Sheet
      open={open}
      onClose={onClose}
      title="Activity"
      dialogDataAttrs={{ "data-activity-detent": detent }}
    >
      <div className="evener-activity-sheet">
        {view === null ? (
          <div className="evener-activity-sheet__empty">No activity</div>
        ) : (
          <>
            <TaskSection tasks={view.tasks} />
            <WorkSection work={view.work} />
            <UsageSection usage={view.usage} />
            <ControlsSection
              capabilities={view.capabilities}
              reasoningEffort={view.reasoningEffort}
              conversationService={conversationService}
            />
          </>
        )}
      </div>
    </Sheet>
  );
}

/** Internal: a collapsible section header used by all four sections. */
export function SectionHeader({
  label,
  expanded,
  onToggle,
  children,
}: {
  label: string;
  expanded: boolean;
  onToggle: () => void;
  children?: ReactNode;
}): JSX.Element {
  return (
    <div className="evener-activity-section__header">
      <button
        type="button"
        className="evener-activity-section__toggle"
        aria-expanded={expanded}
        aria-label={label}
        onClick={onToggle}
      >
        <span className="evener-activity-section__label">{label}</span>
        <span className="evener-activity-section__chevron" aria-hidden="true">
          {expanded ? "▴" : "▾"}
        </span>
      </button>
      {children !== undefined && !expanded ? (
        <div className="evener-activity-section__summary">{children}</div>
      ) : null}
    </div>
  );
}
