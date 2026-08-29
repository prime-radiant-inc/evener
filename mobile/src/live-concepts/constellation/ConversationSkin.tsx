import type { ReactElement } from "react";
import type {
  ActivityMarkerRenderProps,
  ChromeRenderProps,
  ConversationSkin,
  NarrativeItemRenderProps,
} from "../conversation/contract";

function ConstellationStarField(): ReactElement {
  return (
    <span
      aria-hidden="true"
      className="co-conversation-star-field"
      data-constellation-decoration="star-field"
    >
      <span />
      <span />
      <span />
    </span>
  );
}

function ConstellationConnection(): ReactElement {
  return (
    <span
      aria-hidden="true"
      className="co-conversation-connection"
      data-constellation-decoration="connection"
    />
  );
}

function ConstellationConversationChrome({
  title,
  project,
  status,
  updatedLabel,
}: ChromeRenderProps): ReactElement {
  return (
    <div className="co-conversation-chrome">
      <ConstellationStarField />
      <div className="co-conversation-chrome__title">
        <p className="co-conversation-chrome__project">{project.text}</p>
        <h1>{title.text}</h1>
      </div>
      <div className="co-conversation-telemetry">
        <p className="co-conversation-telemetry__status">{status.text}</p>
        {updatedLabel !== null ? <p>{updatedLabel.text}</p> : null}
      </div>
    </div>
  );
}

function ConstellationNarrativeItem({
  item,
  body,
  focused,
}: NarrativeItemRenderProps): ReactElement {
  const className =
    item.sourceKind === "assistant"
      ? "co-conversation-assistant"
      : item.sourceKind === "user"
        ? "co-conversation-user"
        : `co-conversation-surface co-conversation-${item.sourceKind}`;
  return (
    <div
      className={className}
      data-focused={focused ? "true" : "false"}
      data-tone={item.tone}
    >
      <ConstellationStarField />
      <ConstellationConnection />
      <div className="co-conversation-narrative__body">{body}</div>
    </div>
  );
}

function ConstellationActivityMarker({
  item,
  focused,
}: ActivityMarkerRenderProps): ReactElement {
  return (
    <div
      className="co-conversation-marker"
      data-focused={focused ? "true" : "false"}
      data-state={item.state}
      data-tone={item.tone}
    >
      <ConstellationStarField />
      <ConstellationConnection />
      <div className="co-conversation-marker__copy">
        <small>{item.semanticKind}</small>
        <strong>{item.label.text}</strong>
        {item.preview !== null ? <p>{item.preview.text}</p> : null}
      </div>
      {item.duration !== null ? (
        <small className="co-conversation-marker__duration">
          {item.duration.text}
        </small>
      ) : null}
    </div>
  );
}

export const constellationConversationSkin: ConversationSkin = {
  id: "constellation",
  className: "concept-constellation co-conversation-skin",
  composerAppearance: { density: "compact", accent: "luminous" },
  renderConversationChrome: (props) => (
    <ConstellationConversationChrome {...props} />
  ),
  renderNarrativeItem: (props) => <ConstellationNarrativeItem {...props} />,
  renderActivityMarker: (props) => <ConstellationActivityMarker {...props} />,
};
