import type { JSX } from "react";
import type { AttachmentRef } from "../../conversation/model";

export interface AttachmentsRowProps {
  readonly items: AttachmentRef[];
}

/**
 * Renders an attachment thumbnail strip. Each attachment shows its name as
 * plain text (escaped by React). Images are referenced by `src` but not
 * rendered as `<img>` in the mobile timeline (the Hub handles image display);
 * the strip is a labeled list so captions are always accessible as text.
 */
export function AttachmentsRow({ items }: AttachmentsRowProps): JSX.Element {
  return (
    <div className="evener-attachments-row">
      {items.map((att) => (
        <div key={att.id} className="evener-attachments-row__item">
          {att.name ? (
            <span className="evener-attachments-row__name">{att.name}</span>
          ) : null}
        </div>
      ))}
    </div>
  );
}
