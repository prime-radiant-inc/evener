import type { ReactElement } from "react";
import { AssistantMessage } from "../../components/timeline/AssistantMessage";
import type {
  ActivityMarkerRenderProps,
  ChromeRenderProps,
  ConversationSkin,
  NarrativeItemRenderProps,
} from "../conversation/contract";

function StillwaterConversationChrome({
  title,
  project,
  status,
  updatedLabel,
}: ChromeRenderProps): ReactElement {
  return (
    <div className="sw-conversation-chrome">
      <div className="sw-conversation-chrome__title">
        <p className="sw-conversation-chrome__project">{project.text}</p>
        <h1>{title.text}</h1>
      </div>
      <div className="sw-conversation-chrome__meta">
        <p className="sw-conversation-chrome__status">{status.text}</p>
        {updatedLabel !== null ? (
          <p className="sw-conversation-chrome__updated">{updatedLabel.text}</p>
        ) : null}
      </div>
    </div>
  );
}

function StillwaterNarrativeItem({
  item,
  body,
  focused,
}: NarrativeItemRenderProps): ReactElement {
  const className =
    item.sourceKind === "assistant"
      ? "sw-conversation-assistant"
      : item.sourceKind === "user"
        ? "sw-conversation-user"
        : `sw-conversation-surface sw-conversation-${item.sourceKind}`;
  return (
    <div className={className} data-focused={focused ? "true" : "false"}>
      <span
        aria-hidden="true"
        className="sw-conversation-row-rule"
        data-stillwater-decoration="row-rule"
      />
      {item.sourceKind === "assistant" ? (
        <AssistantMessage source={item.body.text} streaming={item.streaming} />
      ) : (
        body
      )}
    </div>
  );
}

function StillwaterActivityMarker({
  item,
  focused,
}: ActivityMarkerRenderProps): ReactElement {
  return (
    <div
      className="sw-conversation-marker"
      data-focused={focused ? "true" : "false"}
      data-tone={item.tone}
    >
      <span
        aria-hidden="true"
        className="sw-conversation-marker__rule"
        data-stillwater-decoration="marker-rule"
      />
      <div className="sw-conversation-marker__copy">
        <strong>{item.label.text}</strong>
        {item.preview !== null ? <p>{item.preview.text}</p> : null}
      </div>
      {item.duration !== null ? (
        <small className="sw-conversation-marker__duration">
          {item.duration.text}
        </small>
      ) : null}
    </div>
  );
}

export const stillwaterConversationSkin: ConversationSkin = {
  id: "stillwater",
  className: "concept-stillwater sw-conversation-skin",
  composerAppearance: { density: "comfortable", accent: "forest" },
  renderConversationChrome: (props) => (
    <StillwaterConversationChrome {...props} />
  ),
  renderNarrativeItem: (props) => <StillwaterNarrativeItem {...props} />,
  renderActivityMarker: (props) => <StillwaterActivityMarker {...props} />,
};
