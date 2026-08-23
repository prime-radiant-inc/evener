import type { JSX } from "react";
import type { MobileTimelineItem } from "../../conversation/model";
import { ActivityRow } from "./ActivityRow";
import { AssistantMessage } from "./AssistantMessage";
import { AttachmentsRow } from "./AttachmentsRow";
import { FailureRow } from "./FailureRow";
import { NoticeRow } from "./NoticeRow";
import { QuestionRow } from "./QuestionRow";
import { UserMessage } from "./UserMessage";

export interface TimelineItemProps {
  readonly item: MobileTimelineItem;
  /** Invoked when the user taps an external link in an assistant message. */
  readonly onExternalLink?: (url: string) => void;
}

/**
 * Dispatches a single MobileTimelineItem to the appropriate family component
 * by its `kind` discriminant. Every family renders escaped plain text except
 * AssistantMessage, which sanitizes Markdown to HTML via renderSafeMarkdown —
 * the sole sanctioned `dangerouslySetInnerHTML` injection point. Unknown or
 * forward-compatible items never reach here with a raw protocol shape; they
 * are projected to a neutral activity row by projectThread.
 */
export function TimelineItem({
  item,
  onExternalLink,
}: TimelineItemProps): JSX.Element {
  switch (item.kind) {
    case "user":
      return <UserMessage text={item.text} />;
    case "assistant":
      return (
        <AssistantMessage
          source={item.markdown}
          streaming={item.streaming}
          onExternalLink={onExternalLink}
        />
      );
    case "activity":
      return (
        <ActivityRow
          id={item.id}
          label={item.label}
          state={item.state}
          detail={item.detail}
        />
      );
    case "notice":
      return <NoticeRow tone={item.tone} text={item.text} />;
    case "question":
      return <QuestionRow batch={item.batch} />;
    case "failure":
      return <FailureRow title={item.title} detail={item.detail} />;
    case "attachments":
      return <AttachmentsRow items={item.items} />;
  }
}
