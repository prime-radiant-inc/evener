import { useState } from "react";
import { View } from "react-native";
import type { MobileTimelineItem } from "../../mobile/src/conversation/model";
import { Action, Copy, styles, useColors } from "./ui";

export function TimelineItem({ item }: { item: MobileTimelineItem }) {
  const [expanded, setExpanded] = useState(false);
  const colors = useColors();
  let content;
  switch (item.kind) {
    case "user":
      content = (
        <>
          <Copy muted>You</Copy>
          <Copy>{item.text}</Copy>
        </>
      );
      break;
    case "assistant":
      content = (
        <>
          <Copy muted>
            {item.streaming ? "Assistant · writing" : "Assistant"}
          </Copy>
          <Copy>{item.markdown || "…"}</Copy>
        </>
      );
      break;
    case "notice":
      content = (
        <>
          <Copy muted>{item.tone === "warning" ? "Warning" : "Notice"}</Copy>
          <Copy>{item.text}</Copy>
        </>
      );
      break;
    case "failure":
      content = (
        <>
          <Copy>{item.title}</Copy>
          <Copy>{item.detail}</Copy>
        </>
      );
      break;
    case "attachments":
      content = (
        <>
          <Copy muted>Attachments</Copy>
          {item.items.map((attachment) => (
            <Copy key={attachment.id}>
              {attachment.name ?? attachment.mediaType ?? "Attachment"}
            </Copy>
          ))}
        </>
      );
      break;
    case "question":
      content = (
        <>
          {item.batch.questions.map((question) => (
            <View key={question.key} style={{ gap: 8 }}>
              <Copy>{question.header}</Copy>
              <Copy>{question.question}</Copy>
              {question.options.map((option, index) => (
                <Copy key={`${question.key}:${index}`}>
                  {option.label}
                  {option.recommended ? " (recommended)" : ""}
                  {option.detail ? ` — ${option.detail}` : ""}
                </Copy>
              ))}
              {question.why ? <Copy muted>{question.why}</Copy> : null}
            </View>
          ))}
          <Copy muted>Reply in the message field below.</Copy>
        </>
      );
      break;
    case "activity":
      content = (
        <>
          <Action
            label={`${expanded ? "Collapse" : "Expand"} ${item.label}`}
            onPress={() => setExpanded(!expanded)}
          >{`${expanded ? "▾" : "▸"} ${item.label} · ${item.state}`}</Action>
          {expanded ? (
            <>
              {item.detail.arguments ? (
                <>
                  <Copy muted>Input</Copy>
                  <Copy>{item.detail.arguments}</Copy>
                </>
              ) : null}
              {item.detail.output ? (
                <>
                  <Copy muted>Output</Copy>
                  <Copy>{item.detail.output}</Copy>
                </>
              ) : null}
              {item.detail.error ? (
                <>
                  <Copy muted>Error</Copy>
                  <Copy>{item.detail.error}</Copy>
                </>
              ) : null}
              {item.detail.exitCode !== undefined ? (
                <Copy muted>Exit code: {item.detail.exitCode}</Copy>
              ) : null}
              {item.detail.durationMs !== undefined ? (
                <Copy muted>Duration: {item.detail.durationMs} ms</Copy>
              ) : null}
            </>
          ) : null}
        </>
      );
      break;
  }
  return (
    <View
      style={[
        styles.card,
        { backgroundColor: colors.surface, borderColor: colors.border },
      ]}
    >
      {content}
    </View>
  );
}
