import type { ReactElement } from "react";
import type {
  ActivityMarkerRenderProps,
  ChromeRenderProps,
  ConversationSkin,
  NarrativeItemRenderProps,
} from "../conversation/contract";

function FieldNotesChronologySegment({
  sequence,
}: {
  readonly sequence: string;
}): ReactElement {
  return (
    <span
      aria-hidden="true"
      className="fn-conversation-chronology"
      data-chronology-segment="row-local"
      data-field-notes-decoration="chronology"
    >
      <span className="fn-conversation-chronology__rule" />
      <span className="fn-conversation-chronology__label">Record</span>
      <span className="fn-conversation-chronology__sequence">{sequence}</span>
    </span>
  );
}

function FieldNotesConversationChrome({
  title,
  project,
  status,
  updatedLabel,
}: ChromeRenderProps): ReactElement {
  return (
    <div className="fn-conversation-chrome">
      <div className="fn-conversation-chrome__title">
        <p className="fn-conversation-chrome__project">{project.text}</p>
        <h1 className="fn-editorial">{title.text}</h1>
      </div>
      <div className="fn-conversation-chrome__meta">
        <p className="fn-conversation-chrome__status">{status.text}</p>
        {updatedLabel !== null ? <p>{updatedLabel.text}</p> : null}
      </div>
    </div>
  );
}

function FieldNotesNarrativeItem({
  item,
  body,
  focused,
}: NarrativeItemRenderProps): ReactElement {
  const marginLabel =
    item.sourceKind === "user"
      ? { kind: "user", text: "Your note" }
      : item.sourceKind === "assistant"
        ? { kind: "assistant", text: "Assistant · response" }
        : null;
  return (
    <div
      className={`fn-conversation-entry fn-conversation-entry--${item.sourceKind}`}
      data-focused={focused ? "true" : "false"}
      data-tone={item.tone}
    >
      <FieldNotesChronologySegment sequence={item.sequence} />
      <span
        aria-hidden="true"
        className="fn-conversation-row-rule"
        data-field-notes-decoration="ruled-motif"
      />
      <div
        className={`fn-conversation-entry__body${
          item.sourceKind === "assistant" ? " fn-editorial" : ""
        }`}
        data-editorial-reading={
          item.sourceKind === "assistant" ? "true" : undefined
        }
      >
        {marginLabel !== null ? (
          <p
            aria-hidden="true"
            className="fn-conversation-margin-label"
            data-margin-label={marginLabel.kind}
          >
            {marginLabel.text}
          </p>
        ) : null}
        {body}
      </div>
    </div>
  );
}

function FieldNotesActivityMarker({
  item,
  focused,
}: ActivityMarkerRenderProps): ReactElement {
  return (
    <div
      className="fn-conversation-marker"
      data-focused={focused ? "true" : "false"}
      data-state={item.state}
      data-tone={item.tone}
    >
      <FieldNotesChronologySegment sequence={item.sequence} />
      <span
        aria-hidden="true"
        className="fn-conversation-row-rule"
        data-field-notes-decoration="ruled-motif"
      />
      <div className="fn-conversation-marker__copy">
        <small>{item.semanticKind}</small>
        <strong>{item.label.text}</strong>
        {item.preview !== null ? <p>{item.preview.text}</p> : null}
      </div>
      {item.duration !== null ? (
        <small className="fn-conversation-marker__duration">
          {item.duration.text}
        </small>
      ) : null}
    </div>
  );
}

export const fieldNotesConversationSkin: ConversationSkin = {
  id: "field-notes",
  className: "concept-field-notes fn-conversation-skin",
  composerAppearance: { density: "comfortable", accent: "rust" },
  renderConversationChrome: (props) => (
    <FieldNotesConversationChrome {...props} />
  ),
  renderNarrativeItem: (props) => <FieldNotesNarrativeItem {...props} />,
  renderActivityMarker: (props) => <FieldNotesActivityMarker {...props} />,
};
